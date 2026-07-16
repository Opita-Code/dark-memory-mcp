// Package main is the dark-mem-cli binary — the operator-side admin
// tool for dark-memory-mcp. Subcommands:
//
//	migrate        Run pending schema migrations on the active Store.
//	vacuum         Run a vacuum (SQLite: reclaim space; Postgres: no-op).
//	schema-status  Print the current schema version + applied migrations.
//	set-driver     Write the driver + DSN to $DARK_HOME/config.toml.
//	version        Print the dark-memory-mcp version.
//	help           Print this message.
//
// All subcommands accept a common flag set:
//
//	--driver  sqlite|postgres  (default: sqlite; also reads $DARK_DB_DRIVER
//	                           or $DARK_HOME/config.toml)
//	--dsn     <path|url>       (default: $DARK_DB, then $DARK_HOME/config.toml,
//	                           then ./dark.db for sqlite)
//	--json                    Emit JSON instead of human-readable tables.
//
// Exit codes:
//
//	0  success
//	1  runtime error (DB unavailable, migration failed, etc.)
//	2  usage error (unknown subcommand, invalid flag, bad driver)
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// Version is set at build time via -ldflags. Default in dev builds.
var Version = "1.1.0-dev"

// Exit codes (matching RFC D-1 §6 conventions).
const (
	exitOK         = 0
	exitRuntimeErr = 1
	exitUsageErr   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. Returns the process exit code.
func run(args []string, stdout, stderr *os.File) int {
	// Trap panics so a bug in one subcommand doesn't crash the
	// operator's terminal session with a stack trace.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "dark-mem-cli: panic: %v\n%s\n", r, debug.Stack())
			os.Exit(exitRuntimeErr)
		}
	}()

	// First arg is the subcommand.
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(stdout)
		return exitOK
	}

	// Handle SIGINT/SIGTERM gracefully (used by migrate / vacuum
	// which can take time on large DBs).
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch args[0] {
	case "migrate":
		return runMigrate(ctx, args[1:], stdout, stderr)
	case "vacuum":
		return runVacuum(ctx, args[1:], stdout, stderr)
	case "schema-status":
		return runSchemaStatus(ctx, args[1:], stdout, stderr)
	case "set-driver":
		return runSetDriver(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "dark-mem-cli %s\n", Version)
		return exitOK
	default:
		fmt.Fprintf(stderr, "dark-mem-cli: unknown subcommand %q (try 'dark-mem-cli help')\n", args[0])
		return exitUsageErr
	}
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `dark-mem-cli %s — operator-side admin for dark-memory-mcp

Usage:
  dark-mem-cli <subcommand> [flags]

Subcommands:
  migrate         Run pending schema migrations.
  vacuum          Reclaim space (SQLite VACUUM; GC old rows).
  schema-status   Show schema version + applied migrations + table list.
  set-driver      Write driver+DSN to $DARK_HOME/config.toml.
  version         Print version.
  help            Print this message.

Common flags (all subcommands):
  --driver=sqlite|postgres   Default: $DARK_DB_DRIVER or sqlite.
  --dsn=<path|url>            Default: $DARK_DB or ./dark.db.
  --json                     Emit JSON instead of human-readable tables.

Environment:
  DARK_DB             path/URL for the database (overrides config file).
  DARK_DB_DRIVER      sqlite | postgres.
  DARK_HOME           base dir for config.toml (default ~/.config/dark-memory-mcp).

Exit codes:
  0  success
  1  runtime error
  2  usage error
`, Version)
}

// loadConfig resolves the driver + DSN from CLI flags, env, and
// $DARK_HOME/config.toml (in that priority order). Used by every
// subcommand that opens a Store.
func loadConfig(flags *commonFlags) (*store.Config, error) {
	driver := flags.Driver
	dsn := flags.DSN

	// 1. CLI flags win (already set by caller).
	if driver == "" {
		driver = os.Getenv("DARK_DB_DRIVER")
	}
	if driver == "" {
		driver = "sqlite"
	}
	if dsn == "" {
		dsn = os.Getenv("DARK_DB")
	}
	// 2. Fall back to config file (only for dsn; driver is rarely in
	// the config file because we usually override at the cmd line).
	if dsn == "" {
		if cfg, err := readDriverConfig(); err == nil {
			dsn = cfg.DSN
		}
	}
	if dsn == "" {
		dsn = "dark.db"
	}

	switch driver {
	case "sqlite", "postgres":
		// ok
	default:
		return nil, fmt.Errorf("invalid driver %q (must be 'sqlite' or 'postgres')", driver)
	}

	return &store.Config{
		Driver: store.Driver(driver),
		DSN:    dsn,
	}, nil
}

// openStore is a small wrapper that runs migrations + constitution
// watchdog. The watchdog refuses to start under drift (INV-4) —
// callers can choose to bypass via openStoreNoCheck if they need
// read-only access during diagnosis.
func openStore(ctx context.Context, cfg *store.Config) (store.Store, error) {
	st, err := runtime.Open(ctx, *cfg)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return st, nil
}

// closeStore is a best-effort close that logs but never propagates.
func closeStore(st store.Store, stderr *os.File) {
	if st == nil {
		return
	}
	if err := st.Close(); err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli: close store: %v\n", err)
	}
}

// configDir returns $DARK_HOME (or the default ~/.config/dark-memory-mcp).
func configDir() (string, error) {
	if v := os.Getenv("DARK_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "dark-memory-mcp"), nil
}

// isUsageError returns true if err is a usage / invalid-argument error.
// We treat known store sentinel ErrInvalidArgument as a usage error so
// the operator gets exit code 2 (not 1).
func isUsageError(err error) bool {
	return errors.Is(err, store.ErrInvalidArgument)
}

// errSilent is returned by handlers that have already printed to stderr
// and just need to bubble up an exit code.
var errSilent = errors.New("silent")

// commonFlags is the parsed flag set shared by every subcommand.
type commonFlags struct {
	Driver string
	DSN    string
	JSON   bool
}

// parseCommonFlags extracts --driver, --dsn, --json from args.
// Returns the remaining args (subcommand-specific) and any parse error.
func parseCommonFlags(args []string) (*commonFlags, []string, error) {
	f := &commonFlags{}
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--driver" && i+1 < len(args):
			f.Driver = args[i+1]
			i++
		case len(a) > 9 && a[:9] == "--driver=":
			f.Driver = a[9:]
		case a == "--dsn" && i+1 < len(args):
			f.DSN = args[i+1]
			i++
		case len(a) > 6 && a[:6] == "--dsn=":
			f.DSN = a[6:]
		case a == "--json":
			f.JSON = true
		default:
			remaining = append(remaining, a)
		}
	}
	return f, remaining, nil
}