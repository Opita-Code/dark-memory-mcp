// dark-memory-v4 schema-status subcommand — prints the schema
// summary + active pragmas. Used as the operator's eyeball check
// that INV-16 is in force on the active dark-db.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// runSchemaStatus opens the dark-db (INV-16), then reports:
//
//   - the version row stamped by `dark-memory-v4 migrate`
//   - the active pragmas (must include busy_timeout=5000,
//     journal_mode=wal, synchronous=normal)
//   - the table list (must include every v4alpha table)
//
// In JSON mode the same data is emitted as a single object so the
// operator can pipe it into jq.
func runSchemaStatus(ctx context.Context, args []string, stdout, stderr *os.File) int {
	flags, err := parseCommonFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "dark-memory-v4: schema-status:", err)
		return exitUsageErr
	}
	dsn, err := resolveDSN(flags, stderr)
	if err != nil {
		return exitUsageErr
	}

	db, err := store.OpenSQLite(ctx, dsn)
	if err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: schema-status: open %s: %v\n", dsn, err)
		return exitRuntimeErr
	}
	defer db.Close()

	status := schemaStatus{DSN: dsn}

	// Read the version row stamped by migrate (if any).
	row := db.QueryRowContext(ctx,
		`SELECT version, applied_at FROM schema_migrations ORDER BY applied_at DESC LIMIT 1`)
	if err := row.Scan(&status.SchemaVersion, &status.AppliedAt); err != nil {
		status.SchemaVersion = "(not migrated)"
		status.AppliedAt = "-"
	}

	// Confirm INV-16 pragmas are actually in force on this *sql.DB.
	status.Pragmas = readPragmas(ctx, db)

	// List every user table — the operator can eyeball that all
	// v4alpha tables are present.
	tables, err := listTables(ctx, db)
	if err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: schema-status: list tables: %v\n", err)
		return exitRuntimeErr
	}
	status.Tables = tables

	if flags.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(status); err != nil {
			fmt.Fprintf(stderr, "dark-memory-v4: schema-status: json encode: %v\n", err)
			return exitRuntimeErr
		}
		return exitOK
	}

	fmt.Fprintf(stdout, "dark-memory-v4 schema status\n")
	fmt.Fprintf(stdout, "  dsn             %s\n", status.DSN)
	fmt.Fprintf(stdout, "  schema_version  %s\n", status.SchemaVersion)
	fmt.Fprintf(stdout, "  applied_at      %s\n", status.AppliedAt)
	fmt.Fprintf(stdout, "\n  pragmas (INV-16):\n")
	for k, v := range status.Pragmas {
		fmt.Fprintf(stdout, "    %-16s %s\n", k, v)
	}
	fmt.Fprintf(stdout, "\n  tables (%d):\n", len(status.Tables))
	for _, t := range status.Tables {
		fmt.Fprintf(stdout, "    %s\n", t)
	}
	return exitOK
}

type schemaStatus struct {
	DSN           string            `json:"dsn"`
	SchemaVersion string            `json:"schema_version"`
	AppliedAt     string            `json:"applied_at"`
	Pragmas       map[string]string `json:"pragmas"`
	Tables        []string          `json:"tables"`
}

// readPragmas reads the INV-16 contract pragmas. If the active
// dark-db is missing any of them, the operator sees a clear
// mismatch on the report (e.g. busy_timeout=0, journal_mode=delete).
//
// The pragma values come from PRAGMA statements directly — the
// virtual-table form (pragma_busy_timeout etc.) is unreliable
// across drivers because the column names differ. The pragma
// statement form is portable.
//
// Returns:
//   - busy_timeout: int milliseconds (5000 per INV-16)
//   - journal_mode: string ("wal" per INV-16)
//   - synchronous: int (1=NORMAL per INV-16; FULL=2; OFF=0)
//   - foreign_keys: int (1 per INV-16)
func readPragmas(ctx context.Context, db *sql.DB) map[string]string {
	out := map[string]string{}

	// busy_timeout is an INTEGER (milliseconds).
	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		out["busy_timeout_query_error"] = err.Error()
	} else {
		out["busy_timeout"] = fmt.Sprintf("%d", busyTimeout)
	}

	// journal_mode is a TEXT.
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		out["journal_mode_query_error"] = err.Error()
	} else {
		out["journal_mode"] = journalMode
	}

	// synchronous is an INTEGER (0=OFF, 1=NORMAL, 2=FULL, 3=EXTRA).
	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		out["synchronous_query_error"] = err.Error()
	} else {
		out["synchronous"] = fmt.Sprintf("%d", synchronous)
	}

	// foreign_keys is an INTEGER (0/1).
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		out["foreign_keys_query_error"] = err.Error()
	} else {
		out["foreign_keys"] = fmt.Sprintf("%d", foreignKeys)
	}

	return out
}

// listTables returns every user table (sqlite_schema.kind='table')
// excluding sqlite's own bookkeeping.
func listTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
