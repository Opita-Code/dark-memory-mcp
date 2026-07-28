// Package agentbootstrap - fs.go: embedded bootstrap content with optional
// filesystem override.
//
// Self-bootstrap content (SYSTEM_PROMPT.md, COMPATIBILITY_MATRIX.md, 6
// install guides, 2 companion docs) lives in ./data and is embedded into
// the Go binary at build time via //go:embed. At runtime, LoadFS
// returns either the embedded fs (default) or an os.DirFS rooted at
// the directory pointed to by the DARK_AGENT_BOOTSTRAP_DIR env var
// (operator override).
//
// Why an override?
//
//   - Operators who want to customize the dark-agent operating manual
//     without rebuilding the MCP can drop their own .md files in a
//     directory and point DARK_AGENT_BOOTSTRAP_DIR at it.
//   - Tests that want to exercise the read path with synthetic content
//     don't need to rebuild.
//   - Drift tests can verify the override path doesn't bypass the
//     embedded-content contract (no missing files, no extra files).
//
// The override is read-only from the MCP's perspective: we never write
// back. The non-invasive contract applies here too.
package agentbootstrap

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EnvOverride is the env var operators set to override the embedded
// bootstrap content. When set to a directory that contains the expected
// files (SYSTEM_PROMPT.md + COMPATIBILITY_MATRIX.md + install/* +
// companions/*), LoadFS uses that directory as the source of truth.
// Otherwise it falls back to the embedded content and the warning
// returned via LoadFSWithWarn tells the operator why.
const EnvOverride = "DARK_AGENT_BOOTSTRAP_DIR"

//go:embed all:data
var embeddedRaw embed.FS

// embedded is the canonical embedded fs used by LoadFS / EmbeddedFS.
// We wrap embeddedRaw in fs.Sub(_, "data") so the public surface is
// rooted at the data/ directory — operators see paths like
// "SYSTEM_PROMPT.md", "install/claude-desktop.md", etc., NOT
// "data/SYSTEM_PROMPT.md". This matches the override contract
// (validateOverrideDir expects files at the root, not under data/).
//
// The wrap happens at package init so the cost is paid once.
var embedded fs.FS

func init() {
	sub, err := fs.Sub(embeddedRaw, "data")
	if err != nil {
		// fs.Sub on a valid subtree cannot fail at runtime; if it
		// does, the embedded content is structurally broken. Panic
		// rather than silently ship a half-broken fs.
		panic(fmt.Sprintf("agentbootstrap: fs.Sub(embeddedRaw, data): %v", err))
	}
	embedded = sub
}

// LoadFS returns the bootstrap filesystem to use at runtime.
//
// Resolution order:
//  1. If DARK_AGENT_BOOTSTRAP_DIR is unset OR empty OR points to a
//     non-existent directory, return the embedded fs as-is.
//  2. Otherwise, stat the directory and verify the expected files
//     are present. If any are missing, fall back to the embedded fs
//     and return a non-nil warning via LoadFSWithWarn.
//  3. If the override is valid, return os.DirFS(overrideDir) so all
//     reads go to disk instead of the embedded copy.
//
// The returned fs is always non-nil. Callers do not need to nil-check.
func LoadFS() fs.FS {
	fsys, _ := LoadFSWithWarn()
	return fsys
}

// LoadFSWithWarn is like LoadFS but also returns a non-nil warning
// when the operator override was requested but rejected (and the
// embedded fallback was used instead). Callers that want to surface
// the warning to the operator can do so; silent callers can ignore it.
//
// The warning is informational; the embedded fallback is always safe
// to use regardless.
func LoadFSWithWarn() (fs.FS, error) {
	overrideDir := strings.TrimSpace(os.Getenv(EnvOverride))
	if overrideDir == "" {
		return embedded, nil
	}

	if err := validateOverrideDir(overrideDir); err != nil {
		return embedded, fmt.Errorf("agentbootstrap: DARK_AGENT_BOOTSTRAP_DIR=%q invalid (%v); using embedded fallback", overrideDir, err)
	}

	return os.DirFS(overrideDir), nil
}

// validateOverrideDir checks that the directory exists, is a directory,
// and contains the expected bootstrap files (SYSTEM_PROMPT.md +
// COMPATIBILITY_MATRIX.md + install/{6 clients}.md + companions/{
// dark-research, dark-copilot }.md).
//
// This is the same set of files that the embedded fs provides; the
// check is intentional. An operator override that lacks any of these
// is rejected as misconfigured: shipping a half-overridden bootstrap
// would silently omit content the harness expects.
func validateOverrideDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}

	required := []string{
		"SYSTEM_PROMPT.md",
		"COMPATIBILITY_MATRIX.md",
		"install/claude-desktop.md",
		"install/claude-code.md",
		"install/opencode.md",
		"install/cline.md",
		"install/cursor.md",
		"install/continue.md",
		"companions/dark-research.md",
		"companions/dark-copilot.md",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			return fmt.Errorf("missing required file %q: %w", rel, err)
		}
	}
	return nil
}

// EmbeddedFS returns the embedded fs directly, bypassing the override
// logic. Useful for tests that want to assert the embedded content is
// intact regardless of the runtime env. Also used by the
// `dark_memory_agent_bootstrap` tool when the operator passes
// `force_embedded=true` for diagnostics.
func EmbeddedFS() fs.FS {
	return embedded
}
