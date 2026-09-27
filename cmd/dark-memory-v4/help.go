// Package main — dark-memory-v4 help/usage text.
package main

import (
	"fmt"
	"os"
)

// printUsage writes the operator-facing help to w. The shape is
// deliberately close to cmd/dark-mem-cli so muscle memory transfers
// (exit codes 0/1/2, subcommand list, common flags block, env block).
func printUsage(w *os.File) {
	fmt.Fprintf(w, `dark-memory-v4 %s — v4-alpha.1 entry point for dark-memory-mcp

Usage:
  dark-memory-v4 <subcommand> [flags]

Subcommands:
  migrate         Run CreateSchema for every v4alpha package (idempotent).
  schema-status   Print schema summary + active pragmas (INV-16 verification).
  serve           Boot the v4 MCP server (BUG-6 skeleton; JSON-RPC in BUG-7).
  version         Print version.
  help            Print this message.

Common flags (migrate, schema-status, serve):
  --dsn=<path>       SQLite path (default: $DARK_DB or ./dark.db).
  --json             Emit JSON instead of human-readable tables.

Environment:
  DARK_DB          path for the SQLite database.
  DARK_OPERATOR    operator id surfaced by serve (default: "dark-agent").

Exit codes:
  0  success
  1  runtime error
  2  usage error

Conventions:
  - All subcommands open the dark-db through
    internal/v4alpha/store.OpenSQLite (INV-16: WAL +
    busy_timeout=5000 + bounded pool). Direct sql.Open against a
    dark-db path is an invariant violation.
  - Schema creation is idempotent (CREATE TABLE IF NOT EXISTS);
    running migrate on an already-initialised DB is safe and a
    no-op for rows that already exist.
`, Version)
}
