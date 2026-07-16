// set_driver.go — dark-mem-cli set-driver subcommand. Writes the
// driver + DSN to $DARK_HOME/config.toml so subsequent invocations
// (including the MCP server) can pick them up without env vars.
//
// Usage:
//
//	dark-mem-cli set-driver --driver=sqlite|postgres --dsn=PATH|URL [--config=PATH]
//
// The config file is TOML. Example contents:
//
//	driver = "sqlite"
//	dsn    = "/var/lib/dark-memory-mcp/dark.db"
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

func runSetDriver(args []string, stdout, stderr *os.File) int {
	driver := ""
	dsn := ""
	configPath := ""

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--driver" && i+1 < len(args):
			i++
			driver = args[i]
		case len(a) > 9 && a[:9] == "--driver=":
			driver = a[9:]
		case a == "--dsn" && i+1 < len(args):
			i++
			dsn = args[i]
		case len(a) > 6 && a[:6] == "--dsn=":
			dsn = a[6:]
		case a == "--config" && i+1 < len(args):
			i++
			configPath = args[i]
		case len(a) > 9 && a[:9] == "--config=":
			configPath = a[9:]
		default:
			fmt.Fprintf(stderr, "dark-mem-cli set-driver: unknown flag %q\n", a)
			return exitUsageErr
		}
		i++
	}

	if driver == "" {
		fmt.Fprintf(stderr, "dark-mem-cli set-driver: --driver is required\n")
		return exitUsageErr
	}
	if dsn == "" {
		fmt.Fprintf(stderr, "dark-mem-cli set-driver: --dsn is required\n")
		return exitUsageErr
	}
	switch driver {
	case "sqlite", "postgres":
		// ok
	default:
		fmt.Fprintf(stderr, "dark-mem-cli set-driver: invalid driver %q (must be 'sqlite' or 'postgres')\n", driver)
		return exitUsageErr
	}

	if configPath == "" {
		dir, err := configDir()
		if err != nil {
			fmt.Fprintf(stderr, "dark-mem-cli set-driver: config dir: %v\n", err)
			return exitRuntimeErr
		}
		configPath = filepath.Join(dir, "config.toml")
	}

	// Build the in-memory config struct and serialize to TOML. Using
	// a typed struct (vs. string concat) keeps the file format
	// explicit and lets us validate fields.
	cfg := driverConfig{Driver: driver, DSN: dsn}
	body, err := toml.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli set-driver: marshal toml: %v\n", err)
		return exitRuntimeErr
	}

	// Ensure the parent directory exists (mkdir -p semantics).
	if dir := filepath.Dir(configPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(stderr, "dark-mem-cli set-driver: mkdir %s: %v\n", dir, err)
			return exitRuntimeErr
		}
	}

	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli set-driver: write %s: %v\n", configPath, err)
		return exitRuntimeErr
	}
	fmt.Fprintf(stdout, "dark-mem-cli set-driver: wrote %s\n%s", configPath, string(body))
	return exitOK
}

// driverConfig is the on-disk schema for $DARK_HOME/config.toml.
// Field names map to TOML keys (via the toml tag).
type driverConfig struct {
	Driver string `toml:"driver"`
	DSN    string `toml:"dsn"`
}

// driverConfigFromFile is the read-side mirror of driverConfig.
// Loaded by loadConfig (in main.go) when --dsn / $DARK_DB are empty.
type driverConfigFromFile struct {
	Driver string `toml:"driver"`
	DSN    string `toml:"dsn"`
}

// readDriverConfig attempts to load $DARK_HOME/config.toml. Returns
// nil with no error if the file does not exist (a fresh install has
// no config yet); returns an error if the file exists but is
// malformed.
func readDriverConfig() (*driverConfigFromFile, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.toml")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg driverConfigFromFile
	if err := toml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}