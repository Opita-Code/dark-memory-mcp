package migrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
)

// F38 (v1.2.2): EnsureCoreTables must create core tables on a fresh
// DB where they don't exist yet, without colliding with later
// migrate.Migrate (the migrations themselves also CREATE TABLE IF
// NOT EXISTS — same shape, same result).
func TestEnsureCoreTables_FreshDB_F38(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f38-fresh.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := migrate.EnsureCoreTables(ctx, db); err != nil {
		t.Fatalf("EnsureCoreTables: %v", err)
	}
	for _, table := range []string{"sessions", "projects"} {
		var n int
		row := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`,
			table)
		row.Scan(&n)
		if n != 1 {
			t.Fatalf("expected table %s to exist after EnsureCoreTables, got n=%d", table, n)
		}
	}
}

// F38 (v1.2.2): EnsureCoreTables must be a no-op on a DB that
// already has the tables (idempotent re-run, common at boot when
// the boot loop re-tries after the dark-mem-mcp process restart).
func TestEnsureCoreTables_Idempotent_F38(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f38-idem.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := migrate.EnsureCoreTables(ctx, db); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := migrate.EnsureCoreTables(ctx, db); err != nil {
		t.Fatalf("second (should be no-op): %v", err)
	}
	var n int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sessions'`).Scan(&n)
	if n != 1 {
		t.Fatalf("sessions should still exist exactly once, got %d", n)
	}
}

// F38 (v1.2.2): simulates the real boot path that triggered the
// dark.db boot crash (table missing + corresponding migration
// recorded as applied in shared schema_migrations ledger):
//
//  1. Start with a dark.db where schema_migrations has v1..v6
//     recorded but the `sessions` table is missing.
//  2. Run EnsureCoreTables — it materialises sessions + projects.
//  3. Run migrate.Migrate on the full sqlite.Migrations list —
//     it should now pass through v7 (project_id ADD COLUMN works
//     because the source tables exist) without column-drift errors.
//
// This is the exact recovery flow the operator needs.
func TestEnsureCoreTables_RecoveryFromHalfMigratedDarkDB_F38(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f38-recovery.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Bootstrap: write_audit exists with project_id (the partial
	// state dark.db was in), but sessions doesn't. Build the minimum
	// schema that allows the rest of v7 to run after EnsureCoreTables.
	mustExec(t, db, `
CREATE TABLE write_audit (id INTEGER PRIMARY KEY AUTOINCREMENT, action TEXT, project_id TEXT NOT NULL DEFAULT 'default');
CREATE TABLE research_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, query TEXT);
CREATE TABLE research_items (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER);
CREATE TABLE research_links (id INTEGER PRIMARY KEY AUTOINCREMENT, target_type TEXT);
CREATE TABLE vibe_specs (id INTEGER PRIMARY KEY AUTOINCREMENT, vibe_case TEXT);
CREATE TABLE vibe_artifacts (id INTEGER PRIMARY KEY AUTOINCREMENT, vibe_case TEXT);
CREATE TABLE vibe_drift_reports (id INTEGER PRIMARY KEY AUTOINCREMENT, verdict TEXT);
CREATE TABLE sdd_evaluations (id INTEGER PRIMARY KEY AUTOINCREMENT, eval_type TEXT);
CREATE TABLE mod_loads (id INTEGER PRIMARY KEY AUTOINCREMENT, mod_id TEXT);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (6, '2026-07-13T22:27:08.8600736Z');
`)

	// Recovery step.
	if err := migrate.EnsureCoreTables(ctx, db); err != nil {
		t.Fatalf("EnsureCoreTables: %v", err)
	}

	// Verify sessions + projects are now created.
	for _, t1 := range []string{"sessions", "projects"} {
		var n int
		db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`,
			t1).Scan(&n)
		if n != 1 {
			t.Fatalf("expected %s after EnsureCoreTables, got n=%d", t1, n)
		}
	}

	// Apply a representative v7-like migration. With F37 (no error
	// on duplicate column for write_audit) + F38 (tables exist),
	// this should fully apply cleanly.
	v7like := migrate.Migration{
		Version: 7,
		Name:    "project_namespace_recovery",
		Up: `
ALTER TABLE research_runs     ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE research_items    ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE research_links    ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_specs        ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_artifacts    ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_drift_reports ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sdd_evaluations   ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE write_audit       ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE mod_loads         ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sessions          ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sessions          ADD COLUMN active_project_id TEXT;
`,
	}
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v7like}); err != nil {
		t.Fatalf("Migrate v7-like after EnsureCoreTables: %v", err)
	}

	// v7 should now be recorded in schema_migrations.
	var v7 int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 7`).Scan(&v7)
	if v7 != 1 {
		t.Fatalf("v7 should be recorded exactly once, got %d", v7)
	}
}
