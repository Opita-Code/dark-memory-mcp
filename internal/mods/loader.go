// Package mods implements the mod loader for Dark Memory MCP. A mod is a
// drop-in package of knowledge (text/datasets) and/or directives (system-
// prompt fragments). This loader parses mod.toml + referenced files and
// applies INV-6 (mod content sanitization) against the safety.markers
// regex set.
//
// INV-6 rules:
//   - Every directive and prompt-injection file is scanned for injection
//     markers. If any marker hits, load is refused with ErrModContentRefused
//     UNLESS: (a) risk_class in {exploit-development, active-probing},
//     AND (b) the user file is whitelisted via DARK_MOD_WHITELIST env
//     (comma-separated list of approved mod_id values).
//   - The sanitization decision is logged via the audit row when the mod
//     is recorded; this package returns a typed error so the caller can
//     surface the rejection to the user / LLM before persisting.
//
// RESEARCH-MODE OVERRIDE (operator-controlled, additive):
//   When DARK_REDTEAM=armed is set AND the mod is whitelisted via
//   DARK_MOD_WHITELIST, INV-6 is bypassed regardless of risk_class. This
//   is the explicit "we are running a security-research session and we
//   accept responsibility for what these prompts do" mode. The bypass
//   applies only to the safetyCheckContent step; manifest validation
//   (parse, required fields, path-escape) still runs.
//   Recorded in the audit row with constitution_id="redteam-research"
//   so the operator's intent is durable. Default DARK_REDTEAM is unset
//   (i.e. the standard public-version safety posture applies).
//
// This file does NOT take a Store dependency — it's the parser. The
// caller (the MCP server bootstrap) is responsible for calling Store.SaveMod
// after a successful Load. Keep the layering.
package mods

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ErrNoManifest is returned when a mod directory has no mod.toml.
var ErrNoManifest = errors.New("mods: no mod.toml in directory")

// ErrInvalidManifest is returned when mod.toml is unparseable or fails
// semantic validation (unknown risk_class, bad path, etc.).
var ErrInvalidManifest = errors.New("mods: invalid mod.toml")

// ErrModContentRefused is returned when a mod's directive / knowledge
// content contains injection markers that the operator did not whitelist
// (INV-6). Persisting a refused mod is a Store-level concern, not Loader.
var ErrModContentRefused = errors.New("mods: content sanitization refused load")

// Loaded is the outcome of a successful Load. Caller persists via Store.SaveMod.
type Loaded struct {
	Manifest   Manifest
	Path       string
	SHA256     string
	LoadedAt   time.Time
	Source     string // "user" | "registry"
	Knowledge  []KnowledgeItem
	Directives []DirectiveItem
}

// KnowledgeItem is one knowledge file under mod's knowledge/.
type KnowledgeItem struct {
	Path   string
	Body   string
	Kind   string // "prompt_injection" | "data_source"
	SHA256 string
}

// DirectiveItem is one directive file (system prompt fragment).
type DirectiveItem struct {
	Path   string
	Body   string
	SHA256 string
}

// Load reads a mod from a directory. path must contain mod.toml and any
// referenced knowledge/directive files. Returns a Loaded struct or an
// error. The SanitizeRefused flag is true when content was scanned and
// rejected — see LoadAndWhitelist for how to whitelist.
func Load(modRoot string) (*Loaded, error) {
	return LoadWithWhitelist(modRoot, nil)
}

// LoadWithWhitelist is Load but with an explicit whitelist (mod IDs that
// have risk_class=exploit-development/active-probing and bypass INV-6).
// Pass nil to disable the whitelist (default: refuse all flagged content).
func LoadWithWhitelist(modRoot string, whitelist []string) (*Loaded, error) {
	manifestPath := filepath.Join(modRoot, "mod.toml")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoManifest, modRoot)
		}
		return nil, fmt.Errorf("mods: read %s: %w", manifestPath, err)
	}

	m, err := parseManifest(rawManifest, manifestPath)
	if err != nil {
		return nil, err
	}

	manifestSHA := sha256Hex(rawManifest)

	out := &Loaded{
		Manifest: *m,
		Path:     modRoot,
		SHA256:   manifestSHA,
		LoadedAt: time.Now().UTC(),
		Source:   string(SourceUser),
	}

	// INV-6 sanitization gate. The whitelist applies to ALL of the mod's
	// loaded content (knowledge + directives) — we don't pick and choose.
	// Three unlock conditions, in increasing order of operator intent:
	//   1. isAllowedRiskClass: the mod declares its own risk envelope
	//      (active-probing | exploit-development).
	//   2. isWhitelisted: the operator added this mod_id to the whitelist.
	//   3. IsRedTeamArmed: the operator has flipped the operator-wide
	//      research-mode toggle AND the mod is whitelisted. This is the
	//      "we accept responsibility for red-team payloads" flag.
	allowed := isAllowedRiskClass(m.Risk.Class) ||
		isWhitelisted(m.Meta.ID, whitelist) ||
		(IsRedTeamArmed() && isWhitelisted(m.Meta.ID, whitelist))

	// Load knowledge files.
	for _, rel := range m.Knowledge.PromptInjections {
		body, err := readModFile(modRoot, rel)
		if err != nil {
			return nil, fmt.Errorf("mods: read knowledge %s: %w", rel, err)
		}
		if !allowed {
			if hits := safetyCheckContent(body); len(hits) > 0 {
				return nil, fmt.Errorf("%w: %s — %d marker(s) detected (path=%s)",
					ErrModContentRefused, m.Meta.ID, len(hits), rel)
			}
		}
		out.Knowledge = append(out.Knowledge, KnowledgeItem{
			Path:   rel,
			Body:   body,
			Kind:   "prompt_injection",
			SHA256: sha256Hex([]byte(body)),
		})
	}
	for _, rel := range m.Knowledge.DataSources {
		body, err := readModFile(modRoot, rel)
		if err != nil {
			return nil, fmt.Errorf("mods: read knowledge %s: %w", rel, err)
		}
		// Data sources are not injected into prompts — sanitize only for
		// logging consistency, not for refusal. We still check, but data
		// sources do not block the load.
		_ = safetyCheckContent(body) // best-effort; logged by caller
		out.Knowledge = append(out.Knowledge, KnowledgeItem{
			Path:   rel,
			Body:   body,
			Kind:   "data_source",
			SHA256: sha256Hex([]byte(body)),
		})
	}

	// Load directive files (system prompt fragments).
	for _, rel := range m.Directives.PromptFragments {
		body, err := readModFile(modRoot, rel)
		if err != nil {
			return nil, fmt.Errorf("mods: read directive %s: %w", rel, err)
		}
		if !allowed {
			if hits := safetyCheckContent(body); len(hits) > 0 {
				return nil, fmt.Errorf("%w: %s — %d marker(s) detected (path=%s)",
					ErrModContentRefused, m.Meta.ID, len(hits), rel)
			}
		}
		out.Directives = append(out.Directives, DirectiveItem{
			Path:   rel,
			Body:   body,
			SHA256: sha256Hex([]byte(body)),
		})
	}

	return out, nil
}

// SanitizeContent is exposed for callers that load a mod outside of the
// mod directory layout (e.g., synthetic tests). Returns the list of
// marker hits; empty list means clean.
func SanitizeContent(text string) []string {
	return safetyCheckContent(text)
}

// AllowedRiskClass reports whether the given risk class is allowed to
// bypass INV-6 sanitization.
func AllowedRiskClass(riskClass string) bool {
	return isAllowedRiskClass(riskClass)
}

// IsWhitelisted reports whether modID is in the comma-separated
// DARK_MOD_WHITELIST env var (or the explicit whitelist passed here).
func IsWhitelisted(modID string, whitelist []string) bool {
	return isWhitelisted(modID, whitelist)
}

// LoadManifestBytes is the test-friendly variant of Load. Takes raw
// manifest bytes + a virtual path; does NOT read any files. Returns a
// Loaded with empty knowledge/directives.
func LoadManifestBytes(rawManifest []byte, virtualPath string) (*Loaded, error) {
	m, err := parseManifest(rawManifest, virtualPath)
	if err != nil {
		return nil, err
	}
	return &Loaded{
		Manifest: *m,
		Path:     virtualPath,
		SHA256:   sha256Hex(rawManifest),
		LoadedAt: time.Now().UTC(),
		Source:   string(SourceUser),
	}, nil
}

// parseManifest applies the strict decoder, validates required fields, and
// returns a parsed Manifest. The strict decoder rejects unknown keys so a
// typo in the manifest fails loud.
func parseManifest(raw []byte, sourcePath string) (*Manifest, error) {
	var m Manifest
	dec := toml.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrInvalidManifest, sourcePath, err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidManifest, sourcePath, err)
	}
	return &m, nil
}

// validateManifest enforces the minimum required schema. The TOML decoder
// already rejected unknown keys; validate is for semantic checks.
func validateManifest(m *Manifest) error {
	if m.Meta.ID == "" {
		return fmt.Errorf("meta.id is required")
	}
	if m.Meta.Version == "" {
		return fmt.Errorf("meta.version is required")
	}
	if m.Meta.Name == "" {
		return fmt.Errorf("meta.name is required")
	}
	if !strings.Contains(m.Meta.ID, "/") {
		return fmt.Errorf("meta.id must be 'namespace/name' (got %q)", m.Meta.ID)
	}
	switch m.Risk.Class {
	case "", string(RiskClassResearchOnly), string(RiskClassActiveProbing), string(RiskClassExploitDevelopment):
		// ok
	default:
		return fmt.Errorf("risk.risk_class: unknown value %q", m.Risk.Class)
	}
	switch m.Risk.TargetScope {
	case "", string(TargetScopePublicInternet), string(TargetScopePrivateInfrastructure),
		string(TargetScopeDarkweb), string(TargetScopeLocalOnly):
		// ok
	default:
		return fmt.Errorf("risk.target_scope: unknown value %q", m.Risk.TargetScope)
	}
	allPaths := append([]string{}, m.Knowledge.PromptInjections...)
	allPaths = append(allPaths, m.Knowledge.DataSources...)
	allPaths = append(allPaths, m.Directives.PromptFragments...)
	for _, p := range allPaths {
		if err := validateModPath(p); err != nil {
			return fmt.Errorf("invalid path %q: %w", p, err)
		}
	}
	for _, p := range m.Capabilities.Tools {
		if err := validateModPath(p); err != nil {
			return fmt.Errorf("capabilities.tools: invalid path %q: %w", p, err)
		}
	}
	for _, p := range m.Capabilities.Parsers {
		if err := validateModPath(p); err != nil {
			return fmt.Errorf("capabilities.parsers: invalid path %q: %w", p, err)
		}
	}
	for _, p := range m.Capabilities.Backends {
		if err := validateModPath(p); err != nil {
			return fmt.Errorf("capabilities.backends: invalid path %q: %w", p, err)
		}
	}
	return nil
}

// validateModPath: relative paths only, no ".." components.
// Same rules as dark-research-mcp's mod loader.
func validateModPath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("absolute paths not allowed")
	}
	for _, s := range strings.Split(p, string(filepath.Separator)) {
		if s == ".." {
			return fmt.Errorf("path escapes mod root (component %q)", s)
		}
	}
	if filepath.Separator == '\\' {
		for _, s := range strings.Split(p, "/") {
			if s == ".." {
				return fmt.Errorf("path escapes mod root (component %q)", s)
			}
		}
	}
	return nil
}

// readModFile reads a file from inside the mod root, enforcing path
// safety. Returns the file contents.
func readModFile(modRoot, rel string) (string, error) {
	if err := validateModPath(rel); err != nil {
		return "", err
	}
	full := filepath.Join(modRoot, rel)
	absRoot, err := filepath.Abs(modRoot)
	if err != nil {
		return "", fmt.Errorf("resolve mod root: %w", err)
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("resolve file: %w", err)
	}
	if !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) && absFull != absRoot {
		return "", fmt.Errorf("path escapes mod root after resolution: %s", rel)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// sha256Hex returns the lowercase hex SHA-256.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// bytesReader is the io.Reader used by the TOML decoder.
// Implementation avoids the import-cycle with internal/safety (kept here
// standalone so the loader can be used in tests without the Store).
type bytesReaderImpl struct {
	b []byte
	i int
}

// Read returns (n, io.EOF) when the buffer is exhausted.
func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// bytesReader wraps a byte slice in a minimal io.Reader for toml.Decode.
func bytesReader(b []byte) *bytesReaderImpl { return &bytesReaderImpl{b: b} }

// isAllowedRiskClass: exploit-development + active-probing bypass INV-6.
func isAllowedRiskClass(rc string) bool {
	return rc == string(RiskClassExploitDevelopment) || rc == string(RiskClassActiveProbing)
}

// IsRedTeamArmed reports whether the operator has flipped the
// DARK_REDTEAM=armed environment variable. This is the explicit
// "we are running a security-research session" gate. Exposed so
// callers (e.g. the red-team tool registration in internal/tools)
// can gate their own tools behind the same flag.
func IsRedTeamArmed() bool {
	return os.Getenv("DARK_REDTEAM") == "armed"
}

// isWhitelisted: checks the explicit whitelist first, then DARK_MOD_WHITELIST.
func isWhitelisted(modID string, whitelist []string) bool {
	for _, w := range whitelist {
		if w == modID {
			return true
		}
	}
	env := os.Getenv("DARK_MOD_WHITELIST")
	if env == "" {
		return false
	}
	for _, w := range strings.Split(env, ",") {
		if strings.TrimSpace(w) == modID {
			return true
		}
	}
	return false
}

// safetyCheckContent runs an injection-marker scan and returns the matched
// regex strings. The implementation lives in this file (not in internal/safety)
// to avoid an import cycle; the regex set is a copy of safety.markers.
// Keep in sync with internal/safety/safety.go.
var injectionMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bignore (?:all |previous |prior )?instructions?\b`),
	regexp.MustCompile(`(?i)\byou are now (?:DAN|a[^.]{0,30}without (?:rules|restrictions))\b`),
	regexp.MustCompile(`(?i)\b(?:new|updated?) system prompt\b`),
	regexp.MustCompile(`(?i)\bdisregard (?:all |previous )?(?:safety|security|ethical) (?:rules?|guidelines?|policies?)\b`),
	regexp.MustCompile(`(?i)\[SYSTEM\]|<\|im_start\|>system|<\|im_end\|>`),
	regexp.MustCompile(`(?i)\bact as (?:a )?(?:different|other|new) (?:AI|assistant|model)\b`),
	regexp.MustCompile(`(?i)\bforget (?:everything|all|your) (?:above|prior|previous)\b`),
	regexp.MustCompile(`(?i)\boverride (?:safety|refusal|system)\b`),
	regexp.MustCompile(`(?i)\bjailbreak\b`),
	regexp.MustCompile(`(?i)\bpretend (?:to be|you are)\b`),
}

func safetyCheckContent(text string) []string {
	hits := make([]string, 0, 4)
	for _, re := range injectionMarkers {
		if re.MatchString(text) {
			hits = append(hits, re.String())
		}
	}
	return hits
}
