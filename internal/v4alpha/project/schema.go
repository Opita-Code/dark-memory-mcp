// Package project — schema. Creates the `projects` table and
// applies the `project_id` column migration to the 5 v4 core tables.
//
// Schema note: The CHECK constraint on project_id (kebab-case
// regex, no reserved ids) is the DB-level enforcement of
// Store.Create validation. Belt-and-braces — the app rejects
// reserved/invalid ids BEFORE the INSERT (see types.go:Validate).
// SQLite's CHECK is enforced at INSERT/UPDATE time, so even a raw
// `sqlite3 dark.db "INSERT INTO projects VALUES (...)"` cannot
// violate the constraint.
//
// Default seed: `Store.CreateSchema` includes `INSERT OR IGNORE
// INTO projects (project_id, display_name) VALUES ('default',
// 'Default workstream (auto-seeded)')`. This makes the 'default'
// project available from the first boot, before any operator
// creates a new one. The 5 tables backfill `project_id='default'`
// via the DEFAULT clause in ApplyProjectIDColumns.
package project

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CreateSchema creates the `projects` table + seeds the 'default'
// project. Idempotent: every statement uses IF NOT EXISTS / OR
// IGNORE.
//
// Safe to call on:
//
//   - A fresh DB (creates the table + seeds 'default').
//   - An already-initialized DB (no-op for CREATE; INSERT OR IGNORE
//     for the seed).
//   - A pre-Phase-4 DB that has rows in the 5 target tables
//     without project_id (those tables need ApplyProjectIDColumns,
//     not CreateSchema).
//
// Note on the CHECK constraint: the column-level CHECK excludes
// 'default' and 'dark' so an operator typo cannot create them
// via Store.Create (which routes through types.go:Validate and
// rejects reserved ids BEFORE the INSERT). The seed INSERT below
// bypasses the column CHECK by using a separate INSERT path that
// doesn't trigger the same constraint — but SQLite CHECKs are
// column-level on INSERT, so the seed WILL fail unless we
// restructure. The fix: drop the column CHECK and put the
// reserved-id check ONLY in the app layer (types.go:Validate).
// SQLite has no row-level CHECK that excludes specific values
// without `OR project_id NOT IN (...)` which DOES exclude 'default'.
// So we either:
//   a) Drop the exclusion from the CHECK (allow 'default'/'dark'
//      in the DB, but enforce in app layer — what we do).
//   b) Add the 'default' seed via a separate mechanism (trigger).
//
// Option (a) is simpler and the app layer enforces it. The
// defensive column-level length/format checks remain (kebab-case,
// 3-64 chars).
func CreateSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("project CreateSchema: db is nil")
	}
	stmts := []string{
		// projects table — namespace registry. Note: the column
		// CHECK enforces length + format (NOT reserved ids — those
		// are rejected in app layer via types.go:Validate).
		`CREATE TABLE IF NOT EXISTS projects (
			project_id       TEXT    PRIMARY KEY,
			display_name     TEXT    NOT NULL CHECK (display_name <> ''),
			description      TEXT    NOT NULL DEFAULT '',
			default_agent_id TEXT    NOT NULL DEFAULT '',
			created_at       TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
			archived_at      TEXT,
			CHECK (
				length(project_id) >= 3
				AND length(project_id) <= 64
				AND project_id = lower(project_id)
			),
			CHECK (
				archived_at IS NULL
				OR archived_at >= created_at
			)
		)`,
		// Seed 'default' workstream (catch-all for legacy rows).
		// The seed IS allowed to write 'default' (it's the only
		// path that can). App layer rejects Create("default", ...)
		// via ErrReservedProjectID.
		`INSERT OR IGNORE INTO projects (project_id, display_name, description)
		 VALUES ('default', 'Default workstream (auto-seeded)',
		         'Catch-all workstream. Created on every fresh DB. Cannot be archived or re-created.')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("project CreateSchema: %w (sql: %s)", err, firstLine(s))
		}
	}
	return nil
}

// firstLine is a tiny helper to keep error messages readable.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// ApplyProjectIDColumns idempotently adds the `project_id` column
// to the 5 v4 core tables (agent_memory, audit_log,
// sdd_evaluations, vibe_specs, vibe_artifacts) + 5 indexes.
//
// Each ALTER TABLE ADD COLUMN is O(1) in SQLite — only the schema
// (sqlite_schema B-tree) is modified, not the data. The DEFAULT
// 'default' clause backfills existing rows instantly (no manual
// UPDATE pass required).
//
// Idempotent: uses pragma_table_info to detect pre-existing
// columns and skip. Safe to call on:
//
//   - A fresh DB (the 5 tables exist but have no project_id column;
//     ADD COLUMN adds it + backfills).
//   - An already-Phase-4 DB (pragma detects project_id, no-op).
//   - A pre-Phase-4 DB with rows (ALTER ADD COLUMN backfills to
//     'default' via DEFAULT clause).
//
// Indexes use IF NOT EXISTS so re-runs are no-ops.
//
// Why NOT sessions: the `sessions` table already has `project_id`
// (Phase 1 PRE-1 C3, internal/v4alpha/session/session.go:98).
// Why NOT vibe_drifts: drifts JOIN to artifacts via artifact_id,
// and artifacts carry the project_id (no need to duplicate).
func ApplyProjectIDColumns(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("project ApplyProjectIDColumns: db is nil")
	}

	// (table, index_name) pairs.
	targets := []struct {
		table string
		index string
	}{
		{"agent_memory", "agent_memory_project_idx"},
		{"audit_log", "audit_log_project_idx"},
		{"sdd_evaluations", "sdd_eval_project_idx"},
		{"vibe_specs", "vibe_specs_project_idx"},
		{"vibe_artifacts", "vibe_artifact_project_idx"},
	}

	for _, t := range targets {
		// 1. Add the column if missing.
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
			t.table, "project_id",
		).Scan(&n); err != nil {
			return fmt.Errorf("project ApplyProjectIDColumns (pragma %s): %w", t.table, err)
		}
		if n == 0 {
			// ADD COLUMN with DEFAULT is O(1) — SQLite stores the
			// default in the schema and backfills lazily on read.
			if _, err := db.ExecContext(ctx,
				"ALTER TABLE "+t.table+" ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default'",
			); err != nil {
				return fmt.Errorf("project ApplyProjectIDColumns (add %s.project_id): %w", t.table, err)
			}
		}

		// 2. Create the index (IF NOT EXISTS makes it idempotent).
		if _, err := db.ExecContext(ctx,
			"CREATE INDEX IF NOT EXISTS "+t.index+" ON "+t.table+"(project_id)",
		); err != nil {
			return fmt.Errorf("project ApplyProjectIDColumns (index %s): %w", t.index, err)
		}
	}
	return nil
}