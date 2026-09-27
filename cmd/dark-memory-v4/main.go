// Package main is the dark-memory-v4 binary — the v4-alpha.1 entry
// point for the redesigned dark-memory-mcp. As of BUG-6 (2026-09-27)
// this skeleton has 5 subcommands:
//
//	migrate         Run CreateSchema for every v4alpha package.
//	schema-status   Print the schema summary + active pragmas.
//	serve           Boot the v4 MCP server (skeleton — full JSON-RPC
//	                wiring is BUG-7).
//	version         Print version.
//	help            Print this message.
//
// All subcommands accept a common flag set:
//
//	--dsn   <path>   SQLite path (default: $DARK_DB or ./dark.db).
//	--json           Emit JSON instead of human-readable tables.
//
// Exit codes (RFC D-1 §6 conventions):
//
//	0  success
//	1  runtime error (DB unavailable, migration failed, ...)
//	2  usage error (unknown subcommand, invalid flag)
//
// # Boot sequence (serve)
//
// The serve subcommand opens the dark-db through
// internal/v4alpha/store.OpenSQLite — INV-16 enforces the BUG-5
// pragma set + bounded pool. It then applies CreateSchema for
// every v4alpha package (idempotent), installs a SIGINT/SIGTERM
// handler, and blocks until shutdown. Full JSON-RPC transport + 75
// tool registration lands in BUG-7.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

// Version is set at build time via -ldflags in version.go. The
// resolver pattern there breaks the init cycle by reading the
// private ldflags target (`versionLD`) instead of the public
// Version var.
var Version = ResolveVersion().String()

// Exit codes (RFC D-1 §6 conventions).
const (
	exitOK         = 0
	exitRuntimeErr = 1
	exitUsageErr   = 2
)

// errSilent is returned by handlers that have already printed to
// stderr and just need to bubble up an exit code.
var errSilent = errors.New("silent")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. Returns the process exit code.
//
// The shape mirrors cmd/dark-mem-cli/run (v3.0) so that the
// operator's muscle memory transfers: same panic-recovery
// pattern, same SIGINT/SIGTERM NotifyContext, same exit codes.
func run(args []string, stdout, stderr *os.File) int {
	// Trap panics so a bug in one subcommand doesn't crash the
	// operator's terminal session with a stack trace.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "dark-memory-v4: panic: %v\n%s\n", r, debug.Stack())
			os.Exit(exitRuntimeErr)
		}
	}()

	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(stdout)
		return exitOK
	}

	// Handle SIGINT/SIGTERM gracefully. Used by serve (blocks until
	// shutdown) and migrate (which can take time on a fresh DB).
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch args[0] {
	case "migrate":
		return runMigrate(ctx, args[1:], stdout, stderr)
	case "schema-status":
		return runSchemaStatus(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "dark-memory-v4 %s\n", Version)
		return exitOK
	default:
		fmt.Fprintf(stderr, "dark-memory-v4: unknown subcommand %q (try 'dark-memory-v4 help')\n", args[0])
		return exitUsageErr
	}
}
