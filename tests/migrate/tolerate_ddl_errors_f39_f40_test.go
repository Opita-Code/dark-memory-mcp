package migrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
)

// F39 (v1.2.2): orphan sqlite-vec triggers (vec_delete on a vec0
// virtual table) cause "no such module: vec0" errors during DDL
// operations like ALTER TABLE RENAME. The migration runner must
// tolerate these and continue. We simulate the failure mode by
// crafting a multi-statement migration where the FIRST statement
// raises "no such module" (e.g. a CREATE VIRTUAL TABLE using a
// fictional module that the runner's tolerant list covers) and the
// SECOND statement is a normal ADD COLUMN that must be applied.
func TestMigrate_ToleratesNoSuchModule_F39(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f39-vec.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mustExec(t, db, `
CREATE TABLE x (id INTEGER PRIMARY KEY AUTOINCREMENT);
`)

	// Migration whose first stmt fails with "no such module" (tolerated
	// by isToleratedDDLError) and second stmt succeeds.
	migs := []migrate.Migration{
		{Version: 1, Name: "vec-then-col",
			Up: `CREATE VIRTUAL TABLE vt_does_not_exist USING fts5(content, not_a_real_col);
ALTER TABLE x ADD COLUMN c TEXT NOT NULL DEFAULT 'y';
`},
	}
	if err := migrate.Migrate(ctx, db, migs); err != nil {
		t.Fatalf("Migrate should pass with tolerated 'no such module', got: %v", err)
	}
	// Verify the second statement DID run.
	var col int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('x') WHERE name = 'c'`).Scan(&col)
	if col != 1 {
		t.Fatalf("ALTER TABLE after tolerated failure should still apply, got col count=%d", col)
	}
}

// F40 (v1.2.2): CREATE TABLE on an already-existing table must be
// tolerated (the rare case where EnsureCoreTables and Migrate both
// try to create the same table in the same boot).
func TestMigrate_ToleratesTableAlreadyExists_F40(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "f40-existing.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mustExec(t, db, `CREATE TABLE foo (id INTEGER PRIMARY KEY AUTOINCREMENT, q TEXT);`)

	migs := []migrate.Migration{
		{Version: 1, Name: "recreate-foo",
			Up: `CREATE TABLE foo (id INTEGER PRIMARY KEY AUTOINCREMENT, q TEXT, extra TEXT NOT NULL DEFAULT 'y');`},
	}
	if err := migrate.Migrate(ctx, db, migs); err != nil {
		t.Fatalf("Migrate should tolerate table-already-exists: %v", err)
	}
	// After the migration, foo still has the original columns (the
	// CREATE TABLE statement was treated as already-satisfied).
	var extraCol int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('foo') WHERE name = 'extra'`).Scan(&extraCol)
	if extraCol != 0 {
		t.Fatalf("CREATE TABLE 'foo' should have been skipped (existing table preserved), got extra col count=%d", extraCol)
	}
}
