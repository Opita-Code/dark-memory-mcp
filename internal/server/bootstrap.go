// Package server — bootstrap.go: load configuration from env + flags.
//
// Per RFC §6 boot sequence step 1: load config from env (DARK_DB_DRIVER,
// DARK_DB_DSN, DARK_CACHE_DIR, DARK_MOD_WHITELIST). All four are
// optional with sensible defaults; DARK_DB_DRIVER is the only one the
// operator must set explicitly to choose between sqlite and postgres.
package server

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/version"
	"github.com/pelletier/go-toml/v2"
)

// builtinConstitutionEmbedded is the canonical dark-mem constitution
// TOML embedded at build time. Used by resolveBuiltinConstitution
// when no filesystem path is found in the search paths. Embedding
// at build time makes the auto-load resilient to layout drift
// (binary copied to /usr/local/bin without the constitution file),
// to test environments (binary built in a temp dir), and to
// operators who haven't set DARK_CONSTITUTION_FILE explicitly.
//
// The embedded copy and the on-disk copy should have the SAME
// content; if they diverge (operator edits the on-disk copy but
// doesn't rebuild), the watchdog will refuse to start because
// the stored SHA from auto-load won't match the embedded TOML's
// SHA. That's a feature, not a bug — operators who want to
// override must rebuild (or set DARK_CONSTITUTION_FILE explicitly).
//
//go:embed constitution/dark-mem.constitution.toml
var builtinConstitutionEmbedded string

// builtinConstitutionName is the canonical filename the auto-loader
// looks for inside each vibe-flow/constitution/ search directory.
// Operators can override the auto-load entirely by setting
// DARK_CONSTITUTION_FILE (the explicit env var wins).
const builtinConstitutionName = "dark-mem.constitution.toml"

// builtinConstitutionSubdir is the path fragment (relative to a
// search root) where the constitution catalog lives in a typical
// repo checkout. Used by builtinConstitutionSearchPaths to build the
// candidate list at boot.
const builtinConstitutionSubdir = "vibe-flow" + string(filepath.Separator) + "constitution"

// builtinConstitution captures the minimal projection of a constitution
// TOML needed to wire the watchdog. Only [meta] is parsed — the gate's
// PersonaFrame re-parses the full TOML when it needs the [persona]
// section (assemble.go:parsePersonaFromConstitution).
type builtinConstitution struct {
	FilePath string
	ID       string
	Version  string
	Label    string
}

// Config is the resolved boot configuration. Constructed by
// LoadConfig from env + flags (flags take precedence; we don't add
// a flag parser in v1 — env-only). Stored as a *Config; converted
// to store.Config when calling runtime.Open.
type Config struct {
	// DBDriver is "sqlite" or "postgres". Defaults to "sqlite".
	DBDriver string

	// DBDSN is the driver-specific connection string.
	//   sqlite:   path to the dark.db file (default: ./dark.db)
	//   postgres: libpq connection string
	DBDSN string

	// CacheDir is where the LLM cache (INV-5) persists. Default: empty
	// (co-located with DB).
	CacheDir string

	// ModWhitelist is a comma-separated list of mod IDs allowed to
	// load (INV-6 sanitization). Empty = no whitelist.
	ModWhitelist []string

	// ServerName is the MCP server name (serverInfo.name). Default:
	// "dark-memory-mcp".
	ServerName string

	// ServerVersion is the MCP server version (serverInfo.version).
	// Default: "0.1.0".
	ServerVersion string

	// CoexistenceGroup is declared in serverInfo (BRIDGE_AND_COEXISTENCE.md
	// §3 / spec 164 bridge.2). Default: "dark-agents/memory".
	CoexistenceGroup string

	// ConstitutionFile is the path to the constitution TOML. When set,
	// the Store watchdog (INV-4) hashes the file and verifies against
	// the stored SHA in the constitutions table. Wave INFRA-003 v2:
	// combined with ConstitutionID + ConstitutionVer, enables the
	// operator-facing migration step (DARK_CONSTITUTION_ACCEPT_BUMPS=1
	// for version-bump opt-in). Default: empty (watchdog dormant).
	ConstitutionFile string

	// ConstitutionID identifies which constitution family the watchdog
	// tracks. Default: empty. See ConstitutionFile.
	ConstitutionID string

	// ConstitutionVer is the active version the operator wants to run.
	// The watchdog compares against this on Open(). Default: empty.
	ConstitutionVer string

	// BootedAt is the wall-clock time the server began its boot
	// sequence (set by LoadConfig from time.Now()). dark_memory_health_ping
	// uses it to compute uptime_seconds. v1.3.0.
	BootedAt time.Time
}

// StoreConfig converts Config to store.Config (the shape
// runtime.Open expects). Kept separate so we can pass the *Config
// around the server without re-wrapping.
func (c *Config) StoreConfig() store.Config {
	return store.Config{
		Driver:          store.Driver(c.DBDriver),
		DSN:             c.DBDSN,
		ConstitutionFile: c.ConstitutionFile,
		ConstitutionID:  c.ConstitutionID,
		ConstitutionVer: c.ConstitutionVer,
	}
}

// LoadConfig reads the env and returns a resolved Config. Never
// returns an error for missing env vars — defaults are applied
// silently. Returns an error only for malformed values (e.g. a
// DBDriver that isn't sqlite or postgres).
//
// Per CONSTITUTION.md Rule 1, the canonical ServerVersion default is
// the resolver's value (`version.Resolve().Version`). Operators may
// still override via DARK_SERVER_VERSION (e.g. for canary / blue-green
// deploys), but the typical path is to build the binary with
// `make release`, which injects the git tag into the resolver.
func LoadConfig() (*Config, error) {
	resolvedVersion := version.Resolve().Version
	cfg := &Config{
		DBDriver:         strings.TrimSpace(strings.ToLower(envOr("DARK_DB_DRIVER", "sqlite"))),
		DBDSN:            strings.TrimSpace(envOr("DARK_DB", defaultDSN())),
		CacheDir:         strings.TrimSpace(envOr("DARK_CACHE_DIR", "")),
		ServerName:       strings.TrimSpace(envOr("DARK_SERVER_NAME", "dark-memory-mcp")),
		ServerVersion:    strings.TrimSpace(envOr("DARK_SERVER_VERSION", resolvedVersion)),
		CoexistenceGroup: strings.TrimSpace(envOr("DARK_COEXISTENCE_GROUP", "dark-agents/memory")),
		// Constitution watchdog (INV-4 + Wave INFRA-003 v2). All three
		// must be set for the watchdog to be active. If ConstitutionFile
		// is empty the watchdog short-circuits (existing behavior).
		ConstitutionFile: strings.TrimSpace(envOr("DARK_CONSTITUTION_FILE", "")),
		ConstitutionID:   strings.TrimSpace(envOr("DARK_CONSTITUTION_ID", "")),
		ConstitutionVer:  strings.TrimSpace(envOr("DARK_CONSTITUTION_VER", "")),
		BootedAt:         time.Now().UTC(),
	}

	whitelist := strings.TrimSpace(os.Getenv("DARK_MOD_WHITELIST"))
	if whitelist != "" {
		for _, p := range strings.Split(whitelist, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.ModWhitelist = append(cfg.ModWhitelist, p)
			}
		}
	}

	// Constitution auto-load (gate identity-frame fix, 2026-07-28):
	// if the operator didn't set DARK_CONSTITUTION_FILE explicitly,
	// search for the canonical dark-mem.constitution.toml in standard
	// repo locations. Without this, the watchdog short-circuits at
	// store.Open() and the `constitutions` table stays empty, which
	// in turn makes StoreSource.IdentityFrame fail to compose (it
	// requires non-empty ConstitutionID) and the gate refuses every
	// session-bound tool call with "identity unavailable for this
	// session".
	//
	// Precedence: explicit env var wins; auto-load is the fallback.
	// No env var change required to keep manual config working.
	if applied := resolveBuiltinConstitution(cfg); applied {
		_ = applied // discovery is logged by resolveBuiltinConstitution
	}

switch cfg.DBDriver {
	case "sqlite", "postgres":
		// ok
	default:
		return nil, fmt.Errorf("server: invalid DARK_DB_DRIVER=%q (must be 'sqlite' or 'postgres')", cfg.DBDriver)
	}

	// v1.3.0 (bug-hunt polish): pre-flight check for sqlite driver.
	// If the operator's DARK_DB path points to a directory that
	// does not exist or is not writable, fail fast with a clear
	// message at step1 rather than crashing deep inside the
	// modernc/sqlite driver at step2 with a stack trace operators
	// can't decode. Postgres is skipped (the driver fails later
	// with a connection-level error that's already clear).
	if cfg.DBDriver == "sqlite" && cfg.DBDSN != ":memory:" {
		if err := preflightSQLiteDSN(cfg.DBDSN); err != nil {
			return nil, fmt.Errorf("server: preflight DARK_DB=%q: %w", cfg.DBDSN, err)
		}
	}

	return cfg, nil
}

// preflightSQLiteDSN verifies that the directory holding the sqlite
// DSN exists and is writable. The file itself does not need to
// exist (sqlite creates it on first open); the operator may pass
// DARK_DB=./new/path/dark.db and expect the daemon to create the
// file. We only check the directory.
func preflightSQLiteDSN(dsn string) error {
	dir := filepath.Dir(dsn)
	// filepath.Dir on a bare filename returns "." which always
	// exists; skip the check in that case.
	if dir == "" || dir == "." {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("directory %q does not exist; create it or set DARK_DB to an existing path", dir)
		}
		return fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q exists but is not a directory", dir)
	}
	// Probe writability with a temp file create+remove. Avoids
	// the false negative of os.Access on Windows where ACLs are
	// not always respected by access(2).
	probe := filepath.Join(dir, ".dark-mem-mcp-preflight")
	if f, err := os.Create(probe); err != nil {
		return fmt.Errorf("directory not writable: %w", err)
	} else {
		_ = f.Close()
		_ = os.Remove(probe)
	}
	return nil
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// resolveBuiltinConstitution is the auto-load glue. Returns true if it
// populated cfg.ConstitutionFile/ID/Ver from a discovered builtin TOML;
// false when:
//   - the operator set DARK_CONSTITUTION_FILE explicitly (precedence: manual wins)
//   - no builtin constitution TOML was found (filesystem + embedded fallback)
//
// Discovery order:
//
//  1. Search the filesystem paths returned by
//     builtinConstitutionSearchPaths() for
//     <search-root>/<builtinConstitutionSubdir>/<builtinConstitutionName>.
//  2. If none found, fall back to the embedded TOML
//     (builtinConstitutionEmbedded). This covers test environments
//     where the binary is built in a temp dir, and operators who
//     copied the binary without the constitution file.
//
// On success, logs the discovery to stderr so operators can see in the
// boot log that the auto-loader fired and from where.
func resolveBuiltinConstitution(cfg *Config) bool {
	if cfg.ConstitutionFile != "" {
		return false
	}
	// (1) filesystem search.
	if candidate := findBuiltinConstitutionFile(builtinConstitutionSearchPaths()); candidate != "" {
		if c, err := parseBuiltinConstitutionMeta(candidate); err == nil {
			cfg.ConstitutionFile = c.FilePath
			cfg.ConstitutionID = c.ID
			cfg.ConstitutionVer = c.Version
			fmt.Fprintf(os.Stderr, "dark-mem-mcp: auto-loaded builtin constitution id=%s version=%s from filesystem %s\n",
				c.ID, c.Version, candidate)
			return true
		} else {
			fmt.Fprintf(os.Stderr, "dark-mem-mcp: builtin constitution %s: %v (skipping auto-load)\n", candidate, err)
			return false
		}
	}
	// (2) embedded fallback.
	if c, err := parseBuiltinConstitutionMetaBytes("embedded://"+builtinConstitutionName, []byte(builtinConstitutionEmbedded)); err == nil {
		// The watchdog (runWatchdog) does os.ReadFile on
		// cfg.ConstitutionFile. The embedded:// marker isn't a real
		// path, so we materialise the embedded content to a temp
		// file before populating the config. The temp file is
		// overwritten on subsequent boots with the same content
		// (deterministic, hash-stable), so the watchdog's stored
		// SHA check stays consistent across restarts.
		tmpPath, tmpErr := materialiseEmbeddedConstitution()
		if tmpErr != nil {
			fmt.Fprintf(os.Stderr, "dark-mem-mcp: embedded builtin constitution materialise error: %v (skipping auto-load)\n", tmpErr)
			return false
		}
		cfg.ConstitutionFile = tmpPath
		cfg.ConstitutionID = c.ID
		cfg.ConstitutionVer = c.Version
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: auto-loaded embedded builtin constitution id=%s version=%s (filesystem search yielded no match; materialised to %s)\n",
			c.ID, c.Version, tmpPath)
		return true
	} else {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: embedded builtin constitution parse error: %v (skipping auto-load)\n", err)
		return false
	}
}

// materialiseEmbeddedConstitution writes the build-time-embedded
// constitution TOML to a deterministic temp file path so the
// watchdog can read it via os.ReadFile. The path is namespaced by
// the content's SHA so:
//   - the same content maps to the same file across boots (the
//     watchdog's stored SHA check stays consistent);
//   - different embedded content (across builds) gets distinct paths;
//   - cleanup is operator-driven (the temp file lives until the
//     OS reboots or the operator rm's it).
//
// Filename pattern: <os.TempDir>/.dark-mem-builtin-<sha12>.toml.
// The leading dot makes it a hidden file; ls doesn't show it by
// default. sha12 is the first 12 hex chars of the SHA256 of the
// embedded content — short enough to be readable in a process
// listing, long enough to be collision-resistant for our purposes.
func materialiseEmbeddedConstitution() (string, error) {
	sum := safety.HashBytes([]byte(builtinConstitutionEmbedded))
	filename := fmt.Sprintf(".dark-mem-builtin-%s.toml", sum[:12])
	fullPath := filepath.Join(os.TempDir(), filename)
	if err := os.WriteFile(fullPath, []byte(builtinConstitutionEmbedded), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", fullPath, err)
	}
	return fullPath, nil
}

// builtinConstitutionSearchPaths builds the ordered list of directories
// the auto-loader searches for dark-mem.constitution.toml. First match
// wins. Order:
//
//  1. <exe-dir>/../vibe-flow/constitution/ — typical repo layout when the
//     binary lives at bin/dark-mem-mcp.exe (Windows) or
//     cmd/dark-mem-mcp/dark-mem-mcp (POSIX). The ../ walks up out of
//     bin/.
//  2. <exe-dir>/vibe-flow/constitution/ — for layouts where the binary
//     lives at the repo root (e.g. `go build` from cmd/dark-mem-mcp).
//  3. <cwd>/vibe-flow/constitution/ — last-resort for callers that chdir
//     into the repo before exec'ing.
//
// Search paths are deduplicated by filepath.Clean so the same canonical
// path isn't returned twice (e.g. when exe-dir == cwd).
func builtinConstitutionSearchPaths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		clean := filepath.Clean(p)
		if seen[clean] {
			return
		}
		seen[clean] = true
		out = append(out, clean)
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		add(filepath.Join(exeDir, "..", builtinConstitutionSubdir))
		add(filepath.Join(exeDir, builtinConstitutionSubdir))
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, builtinConstitutionSubdir))
	}
	return out
}

// findBuiltinConstitutionFile returns the absolute path of the first
// existing canonical constitution file across the given search paths,
// or "" if none of them contain dark-mem.constitution.toml. Exported
// for tests so the search-path resolution can be exercised without
// needing the auto-load to actually mutate cfg.
func findBuiltinConstitutionFile(searchPaths []string) string {
	for _, dir := range searchPaths {
		candidate := filepath.Join(dir, builtinConstitutionName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// parseBuiltinConstitutionMeta reads the [meta] section of a constitution
// TOML from a filesystem path. Only the three fields the watchdog
// needs (id, version, label) are extracted; the rest of the file is
// parsed by the gate's PersonaFrame at request time, not at boot.
// Returns a non-nil error when the file is unreadable, the TOML is
// malformed, or the required [meta] fields are empty.
func parseBuiltinConstitutionMeta(path string) (*builtinConstitution, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return parseBuiltinConstitutionMetaBytes(path, data)
}

// parseBuiltinConstitutionMetaBytes is the in-memory variant of
// parseBuiltinConstitutionMeta. Same contract; takes the raw TOML
// bytes instead of a filesystem path. path is used as the FilePath
// label on success; pass "embedded://<name>" for the embedded path
// so operators reading the boot log see the source clearly.
//
// Strips UTF-8 BOM if present — Windows editors save TOML files
// with a leading BOM which the pelletier parser rejects with
// "invalid character at start of key". This is a no-op on BOM-less
// inputs.
func parseBuiltinConstitutionMetaBytes(path string, data []byte) (*builtinConstitution, error) {
	data = stripUTF8BOM(data)
	var meta struct {
		Meta struct {
			ID      string `toml:"id"`
			Version string `toml:"version"`
			Label   string `toml:"label"`
		} `toml:"meta"`
	}
	if err := toml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if meta.Meta.ID == "" {
		return nil, fmt.Errorf("[meta].id is empty")
	}
	if meta.Meta.Version == "" {
		return nil, fmt.Errorf("[meta].version is empty")
	}
	return &builtinConstitution{
		FilePath: path,
		ID:       meta.Meta.ID,
		Version:  meta.Meta.Version,
		Label:    meta.Meta.Label,
	}, nil
}

// stripUTF8BOM removes the 3-byte UTF-8 BOM (EF BB BF) prefix if
// present. Returns the input unchanged otherwise.
func stripUTF8BOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

func defaultDSN() string {
	// F38+ (v1.2.2): Default to ./dark-memory.db in the current
	// working directory — NOT dark.db. dark-memory-mcp and
	// dark-research-mcp historically shared the same dark.db file
	// (sharing `schema_migrations` book-keeping by version NAME,
	// e.g. v1=initial_schema in both) which produced v7/v8 boot
	// crashes against partial-state dbs shared with dark-research's
	// vec0 triggers + v1-v3 book-keeping rows. The v1.2.2 release
	// split dark-memory-mcp out of that shared namespace by giving it
	// its own default filename; operators can still force the legacy
	// shared mode via DARK_DB=dark.db in the env. Production
	// deployments continue to set DARK_DB explicitly.
	return "dark-memory.db"
}

// DefaultDSN exposes defaultDSN for tests/invariants. See docs/INVARIANTS.md INV-8.
func DefaultDSN() string { return defaultDSN() }

// DefaultServerVersion is the canonical server version. Deprecated as
// a hardcoded constant since the v1.4.0 release; the canonical source
// is now `version.Resolve().Version` (set by `make release` via
// `-ldflags`). Retained as a string for any external call sites that
// still reference it. See CONSTITUTION.md Rule 1.
const DefaultServerVersion = "2.1.3-dev"
