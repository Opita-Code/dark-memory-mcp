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
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
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
func LoadConfig() (*Config, error) {
	cfg := &Config{
		DBDriver:         strings.TrimSpace(strings.ToLower(envOr("DARK_DB_DRIVER", "sqlite"))),
		DBDSN:            strings.TrimSpace(envOr("DARK_DB", defaultDSN())),
		CacheDir:         strings.TrimSpace(envOr("DARK_CACHE_DIR", "")),
		ServerName:       strings.TrimSpace(envOr("DARK_SERVER_NAME", "dark-memory-mcp")),
		ServerVersion:    strings.TrimSpace(envOr("DARK_SERVER_VERSION", "0.1.0")),
		CoexistenceGroup: strings.TrimSpace(envOr("DARK_COEXISTENCE_GROUP", "dark-agents/memory")),
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

	return cfg, nil
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func defaultDSN() string {
	// Default to ./dark.db in the current working directory. Safe for
	// the e2e test and for development; production deployments set
	// DARK_DB explicitly.
	return "dark.db"
}