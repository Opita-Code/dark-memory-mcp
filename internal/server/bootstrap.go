// Package server — bootstrap.go: load configuration from env + flags.
//
// Per RFC §6 boot sequence step 1: load config from env (DARK_DB_DRIVER,
// DARK_DB_DSN, DARK_CACHE_DIR, DARK_MOD_WHITELIST). All four are
// optional with sensible defaults; DARK_DB_DRIVER is the only one the
// operator must set explicitly to choose between sqlite and postgres.
package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/version"
)

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
		Driver: store.Driver(c.DBDriver),
		DSN:    c.DBDSN,
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
const DefaultServerVersion = "1.4.0-dev"
