package migrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
)

// F37 (v1.2.2): a migration that adds a column should be tolerated
// when the column already exists on the table (partial-state dark.db
// recovered after a crash mid-DDL). The migration runner must still
// record v7 as applied and continue to v8+.
func TestMigrate_TolerantOfDuplicateColumn_F37(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f37.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Pre-create the target table + simulate that a half-applied v7
	// already added the project_id column to ONE table but not the
	// others. This is the post-crash state we want to recover from.
	mustExec(t, db, `
CREATE TABLE research_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    query TEXT NOT NULL,
    project_id TEXT NOT NULL DEFAULT 'default'
);
CREATE TABLE research_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL
);
CREATE TABLE vibe_specs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    vibe_case TEXT NOT NULL,
    tasks_json TEXT NOT NULL
);
`)

	migs := []migrate.Migration{
		{
			Version: 7,
			Name:    "project_namespace_recovery",
			Up: `
ALTER TABLE research_runs ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE research_items ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_specs ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
`,
		},
	}

	if err := migrate.Migrate(ctx, db, migs); err != nil {
		t.Fatalf("Migrate should tolerate pre-existing column: %v", err)
	}

	// schema_migrations should now have v7 recorded.
	var applied int
	row := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 7`)
	if err := row.Scan(&applied); err != nil {
		t.Fatalf("count v7: %v", err)
	}
	if applied != 1 {
		t.Fatalf("v7 should be recorded in schema_migrations, got count=%d", applied)
	}

	// All three tables must now have project_id (the other two were
	// added during F37-tolerant run; the first was already present).
	for _, table := range []string{"research_runs", "research_items", "vibe_specs"} {
		var n int
		row := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'project_id'`,
			table)
		if err := row.Scan(&n); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if n != 1 {
			t.Fatalf("table %s should have project_id column, got n=%d", table, n)
		}
	}
}

// F37 (v1.2.2): applyOne must propagate non-idempotent errors
// (anything that ISN'T a "duplicate column" error class). Regression
// guard against an over-broad catch swallowing real DDL bugs.
func TestMigrate_StillFailsOnNonDuplicateErrors_F37(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f37-neg.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mustExec(t, db, `CREATE TABLE x (id INTEGER PRIMARY KEY AUTOINCREMENT);`)

	migs := []migrate.Migration{
		{Version: 1, Name: "bad-syntax", Up: `NOT VALID SQL AT ALL`},
	}
	if err := migrate.Migrate(ctx, db, migs); err == nil {
		t.Fatalf("expected error for invalid SQL, got nil")
	}
}

// F37 (v1.2.2): a successfully applied migration returns success and
// is recorded; a subsequent migrate.Migrate call is a no-op (idempotent
// re-run on fresh DB).
func TestMigrate_PartialMigration_RecordsAndContinues_F37(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f37-idem.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mustExec(t, db, `CREATE TABLE x (id INTEGER PRIMARY KEY AUTOINCREMENT, q TEXT);`)

	migs := []migrate.Migration{
		{
			Version: 1,
			Name:    "add-c",
			Up:      `ALTER TABLE x ADD COLUMN c TEXT NOT NULL DEFAULT 'x';`,
		},
		{
			Version: 2,
			Name:    "add-d",
			Up:      `ALTER TABLE x ADD COLUMN d TEXT NOT NULL DEFAULT 'y';`,
		},
	}
	if err := migrate.Migrate(ctx, db, migs); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := migrate.Migrate(ctx, db, migs); err != nil {
		t.Fatalf("second Migrate should be a no-op: %v", err)
	}

	// Both versions recorded exactly once.
	for _, v := range []int{1, 2} {
		var n int
		row := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, v)
		row.Scan(&n)
		if n != 1 {
			t.Fatalf("version %d should be recorded exactly once, got %d", v, n)
		}
	}
}

// Sanity check: the production migration list (real SQLite driver)
// applies cleanly on a brand-new dark.db. This guards against any
// breakage introduced by the splitStatements / isDuplicateColumnError
// refactor — the production migrations use multi-statement bodies with
// comments and CREATE INDEX IF NOT EXISTS, all of which splitStatements
// must handle.
func TestMigrate_RealDriverSQLite_BrandNewDB_F37(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f37-real.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := migrate.Migrate(ctx, db, sqlite.Migrations); err != nil {
		t.Fatalf("full SQLite migrations on fresh DB: %v", err)
	}
	v, err := migrate.SchemaVersion(ctx, db)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 10 {
		t.Fatalf("expected schema_version=10 after all migrations applied, got %d", v)
	}
}

func mustExec(t *testing.T, db *sql.DB, sqlText string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), sqlText); err != nil {
		t.Fatalf("exec: %v", err)
	}
}
