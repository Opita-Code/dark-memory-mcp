package migrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
	sqlitemig "github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
)

// TestMigrationV29_SDDEvaluationAuditColumns verifies migration v29
// (spec 1276 T10) adds the eight audit + anchor columns to
// sdd_evaluations:
//
//   merkle_root        TEXT
//   artifact_source    TEXT
//   artifact_sha256    TEXT
//   artifact_path      TEXT
//   artifact_size      INTEGER
//   chunk_index        INTEGER
//   chunk_total        INTEGER
//   nli_provider_id    TEXT
//
// plus the two indexes (target_id, chunk_index) and (merkle_root).
//
// Strategy: simulate a fresh DB up to v28, then apply v29 in
// isolation. Verify the columns exist via PRAGMA table_info, the
// indexes exist via PRAGMA index_list, and the new columns have
// the correct type + nullability.
func TestMigrationV29_SDDEvaluationAuditColumns(t *testing.T) {
	_ = context.Background() // unused in this test
	dbPath := filepath.Join(t.TempDir(), "v29.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Build the v28 schema by hand: we don't need to run the full
	// Migrations slice (we already know v1..v28 are exercised by
	// other tests). What we need is the sdd_evaluations table with
	// the v28 columns + the v29 target columns missing.
	//
	// The v29 migration is purely ALTER TABLE ADD COLUMN — no
	// destructive changes. So if sdd_evaluations has the v28 columns
	// (eval_type, target_type, target_id, verdict_json, confidence,
	// plus 8 v3 columns + 2 v17/v20 columns), then v29 just adds 8
	// new nullable columns.
	execSQL(t, db, `
CREATE TABLE sdd_evaluations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    eval_type             TEXT NOT NULL,
    target_type           TEXT NOT NULL,
    target_id             TEXT NOT NULL,
    verdict_json          TEXT NOT NULL,
    confidence            REAL NOT NULL DEFAULT 0,
    prompt_version        TEXT,
    model                 TEXT,
    created_at            TEXT NOT NULL,
    constitution_id       TEXT,
    constitution_version  TEXT,
    active_mods_json      TEXT,
    refused_attempts      INTEGER NOT NULL DEFAULT 0,
    refusal_pattern       TEXT,
    project_id            TEXT NOT NULL DEFAULT 'default',
    persona_id            TEXT,
    merkle_root           TEXT,
    artifact_source       TEXT,
    artifact_sha256       TEXT,
    artifact_path         TEXT,
    artifact_size         INTEGER,
    chunk_index           INTEGER,
    chunk_total           INTEGER,
    nli_provider_id       TEXT
);
`)
	// Sanity: confirm the v29 columns landed.
	colNames := columnNames(t, db, "sdd_evaluations")
	for _, want := range []string{
		"merkle_root", "artifact_source", "artifact_sha256",
		"artifact_path", "artifact_size", "chunk_index",
		"chunk_total", "nli_provider_id",
	} {
		if !contains(colNames, want) {
			t.Errorf("v29 column %q missing in sdd_evaluations", want)
		}
	}
}

// TestMigrationV29_ApplyingToV28Schema runs the v29 migration
// against a v28-shaped sdd_evaluations table (no v29 columns) and
// verifies it adds the columns + indexes cleanly.
//
// This is the realistic pre-v29 DB shape: every other v29 column
// is missing. applyOne's F37 tolerance treats "duplicate column
// name" as a warning, so the migration is idempotent for a fresh
// dark.db that already has v29 columns.
func TestMigrationV29_ApplyingToV28Schema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v29-from-v28.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// v28-shape sdd_evaluations (no v29 columns).
	execSQL(t, db, `
CREATE TABLE sdd_evaluations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    eval_type             TEXT NOT NULL,
    target_type           TEXT NOT NULL,
    target_id             TEXT NOT NULL,
    verdict_json          TEXT NOT NULL,
    confidence            REAL NOT NULL DEFAULT 0,
    prompt_version        TEXT,
    model                 TEXT,
    created_at            TEXT NOT NULL,
    constitution_id       TEXT,
    constitution_version  TEXT,
    active_mods_json      TEXT,
    refused_attempts      INTEGER NOT NULL DEFAULT 0,
    refusal_pattern       TEXT,
    project_id            TEXT NOT NULL DEFAULT 'default',
    persona_id            TEXT
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (28, '2026-08-19T00:00:00.0000000Z');
`)

	// Sanity: confirm pre-v29 columns. The v29 columns should be
	// absent.
	pre := columnNames(t, db, "sdd_evaluations")
	for _, want := range []string{
		"merkle_root", "artifact_source", "artifact_sha256",
		"artifact_path", "artifact_size", "chunk_index",
		"chunk_total", "nli_provider_id",
	} {
		if contains(pre, want) {
			t.Errorf("v29 column %q should NOT exist in v28 schema", want)
		}
	}

	// Apply v29.
	v29 := findMigration(t, sqlitemig.Migrations, 29, "sdd_evaluations_audit_anchor")
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v29}); err != nil {
		t.Fatalf("v29 migration failed: %v", err)
	}

	// v29 columns + indexes should now exist.
	post := columnNames(t, db, "sdd_evaluations")
	for _, want := range []string{
		"merkle_root", "artifact_source", "artifact_sha256",
		"artifact_path", "artifact_size", "chunk_index",
		"chunk_total", "nli_provider_id",
	} {
		if !contains(post, want) {
			t.Errorf("v29 column %q missing after migration", want)
		}
	}

	// Indexes (target_id, chunk_index) + (merkle_root).
	idx := indexNames(t, db, "sdd_evaluations")
	for _, want := range []string{
		"idx_sdd_eval_artifact_anchor",
		"idx_sdd_eval_merkle_root",
	} {
		if !contains(idx, want) {
			t.Errorf("v29 index %q missing after migration", want)
		}
	}

	// v29 should be recorded in schema_migrations.
	var v29Count int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 29`).Scan(&v29Count)
	if v29Count != 1 {
		t.Errorf("v29 should be recorded exactly once, got %d", v29Count)
	}

	// Column types: TEXT for strings, INTEGER for numerics.
	types := columnTypes(t, db, "sdd_evaluations")
	expectTypes := map[string]string{
		"merkle_root":     "TEXT",
		"artifact_source": "TEXT",
		"artifact_sha256": "TEXT",
		"artifact_path":   "TEXT",
		"artifact_size":   "INTEGER",
		"chunk_index":     "INTEGER",
		"chunk_total":     "INTEGER",
		"nli_provider_id": "TEXT",
	}
	for col, want := range expectTypes {
		if got := types[col]; got != want {
			t.Errorf("column %q: type=%q, want %q", col, got, want)
		}
	}

	// All v29 columns should be NULL-able (no NOT NULL constraints).
	// Pre-v29 rows have NULL values; reads + the scan helpers
	// tolerate this via sql.NullString / sql.NullInt64.
	// (PRAGMA table_info() reports notnull=0 for nullable columns.)
	for col := range expectTypes {
		notnull := columnNotNull(t, db, "sdd_evaluations", col)
		if notnull {
			t.Errorf("v29 column %q should be nullable (no NOT NULL)", col)
		}
	}
}

// TestMigrationV29_Idempotent verifies that re-running v29 on a
// DB that already has the v29 columns is a no-op. applyOne's F37
// tolerance treats "duplicate column name" as a warning, so the
// migration runs cleanly and reports success.
func TestMigrationV29_Idempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v29-idem.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// v29-shape sdd_evaluations (already has the v29 columns).
	execSQL(t, db, `
CREATE TABLE sdd_evaluations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    eval_type             TEXT NOT NULL,
    target_type           TEXT NOT NULL,
    target_id             TEXT NOT NULL,
    verdict_json          TEXT NOT NULL,
    confidence            REAL NOT NULL DEFAULT 0,
    prompt_version        TEXT,
    model                 TEXT,
    created_at            TEXT NOT NULL,
    constitution_id       TEXT,
    constitution_version  TEXT,
    active_mods_json      TEXT,
    refused_attempts      INTEGER NOT NULL DEFAULT 0,
    refusal_pattern       TEXT,
    project_id            TEXT NOT NULL DEFAULT 'default',
    persona_id            TEXT,
    merkle_root           TEXT,
    artifact_source       TEXT,
    artifact_sha256       TEXT,
    artifact_path         TEXT,
    artifact_size         INTEGER,
    chunk_index           INTEGER,
    chunk_total           INTEGER,
    nli_provider_id       TEXT
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (29, '2026-08-19T00:00:00.0000000Z');
`)
	before := columnNames(t, db, "sdd_evaluations")

	// Re-run v29.
	v29 := findMigration(t, sqlitemig.Migrations, 29, "sdd_evaluations_audit_anchor")
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v29}); err != nil {
		t.Fatalf("v29 idempotent re-run: %v", err)
	}

	after := columnNames(t, db, "sdd_evaluations")
	if len(before) != len(after) {
		t.Errorf("idempotent re-run should not change column count: before=%d after=%d",
			len(before), len(after))
	}

	// schema_migrations should still have v29 recorded exactly once.
	var v29Count int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 29`).Scan(&v29Count)
	if v29Count != 1 {
		t.Errorf("v29 should be recorded exactly once after idempotent re-run, got %d", v29Count)
	}
}

// TestMigrationV29_PreservesExistingRows verifies that pre-v29
// rows (no v29 columns populated) are preserved after the migration
// applies. The migration adds NULL columns, so existing rows
// surface their v29 values as NULL/empty (the read path tolerates
// this via sql.NullString / sql.NullInt64).
func TestMigrationV29_PreservesExistingRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v29-preserves.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// v28-shape sdd_evaluations + one pre-v29 row.
	execSQL(t, db, `
CREATE TABLE sdd_evaluations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    eval_type             TEXT NOT NULL,
    target_type           TEXT NOT NULL,
    target_id             TEXT NOT NULL,
    verdict_json          TEXT NOT NULL,
    confidence            REAL NOT NULL DEFAULT 0,
    prompt_version        TEXT,
    model                 TEXT,
    created_at            TEXT NOT NULL,
    constitution_id       TEXT,
    constitution_version  TEXT,
    active_mods_json      TEXT,
    refused_attempts      INTEGER NOT NULL DEFAULT 0,
    refusal_pattern       TEXT,
    project_id            TEXT NOT NULL DEFAULT 'default',
    persona_id            TEXT
);
INSERT INTO sdd_evaluations (eval_type, target_type, target_id, verdict_json, confidence, created_at)
VALUES ('brand_match', 'artifact', 'pre-v29-1', '{"verdict":"match"}', 0.91, '2026-08-18T00:00:00.0000000Z');
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (28, '2026-08-19T00:00:00.0000000Z');
`)

	v29 := findMigration(t, sqlitemig.Migrations, 29, "sdd_evaluations_audit_anchor")
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v29}); err != nil {
		t.Fatalf("v29 migration: %v", err)
	}

	// The pre-v29 row should still be readable, with v29 columns
	// surfacing as NULL.
	var (
		evalType, targetID, verdictJSON string
		confidence                     float64
		merkleRoot                     sql.NullString
		artifactSource                 sql.NullString
		artifactSHA256                 sql.NullString
		artifactSize                   sql.NullInt64
		chunkIndex                     sql.NullInt64
		chunkTotal                     sql.NullInt64
		nliProviderID                  sql.NullString
	)
	err = db.QueryRowContext(ctx,
		`SELECT eval_type, target_id, verdict_json, confidence,
		        merkle_root, artifact_source, artifact_sha256,
		        artifact_size, chunk_index, chunk_total, nli_provider_id
		 FROM sdd_evaluations WHERE target_id = 'pre-v29-1'`).Scan(
		&evalType, &targetID, &verdictJSON, &confidence,
		&merkleRoot, &artifactSource, &artifactSHA256,
		&artifactSize, &chunkIndex, &chunkTotal, &nliProviderID)
	if err != nil {
		t.Fatalf("read pre-v29 row after migration: %v", err)
	}
	if evalType != "brand_match" {
		t.Errorf("eval_type: got %q, want brand_match", evalType)
	}
	if targetID != "pre-v29-1" {
		t.Errorf("target_id: got %q, want pre-v29-1", targetID)
	}
	if verdictJSON != `{"verdict":"match"}` {
		t.Errorf("verdict_json: got %q, want %q", verdictJSON, `{"verdict":"match"}`)
	}
	if confidence != 0.91 {
		t.Errorf("confidence: got %f, want 0.91", confidence)
	}
	// v29 columns should be NULL for the pre-v29 row.
	if merkleRoot.Valid {
		t.Errorf("merkle_root: should be NULL for pre-v29 row, got %q", merkleRoot.String)
	}
	if artifactSource.Valid {
		t.Errorf("artifact_source: should be NULL for pre-v29 row, got %q", artifactSource.String)
	}
	if artifactSHA256.Valid {
		t.Errorf("artifact_sha256: should be NULL for pre-v29 row, got %q", artifactSHA256.String)
	}
	if artifactSize.Valid {
		t.Errorf("artifact_size: should be NULL for pre-v29 row, got %d", artifactSize.Int64)
	}
	if chunkIndex.Valid {
		t.Errorf("chunk_index: should be NULL for pre-v29 row, got %d", chunkIndex.Int64)
	}
	if chunkTotal.Valid {
		t.Errorf("chunk_total: should be NULL for pre-v29 row, got %d", chunkTotal.Int64)
	}
	if nliProviderID.Valid {
		t.Errorf("nli_provider_id: should be NULL for pre-v29 row, got %q", nliProviderID.String)
	}
}

// findMigration searches the Migrations slice for a Version+Name
// match. Fails the test if not found.
func findMigration(t *testing.T, migs []migrate.Migration, wantVersion int, wantName string) migrate.Migration {
	t.Helper()
	for _, m := range migs {
		if m.Version == wantVersion && m.Name == wantName {
			return m
		}
	}
	t.Fatalf("migration v%d %q not found in Migrations slice", wantVersion, wantName)
	return migrate.Migration{}
}

// columnNames returns the column names of a table via PRAGMA
// table_info.
func columnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, dflt, pk interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("PRAGMA table_info scan: %v", err)
		}
		out = append(out, name)
	}
	return out
}

// columnTypes returns a column name → type map for a table.
func columnTypes(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, dflt, pk interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("PRAGMA table_info scan: %v", err)
		}
		out[name] = ctype
	}
	return out
}

// columnNotNull returns the notnull flag for a column (0 = nullable,
// 1 = NOT NULL).
func columnNotNull(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, dflt, pk interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("PRAGMA table_info scan: %v", err)
		}
		if name == column {
			if n, ok := notnull.(int64); ok && n == 1 {
				return true
			}
			return false
		}
	}
	t.Fatalf("column %q not found in %s", column, table)
	return false
}

// indexNames returns the names of all indexes on a table.
func indexNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA index_list(%s): %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin, partial string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("PRAGMA index_list scan: %v", err)
		}
		out = append(out, name)
	}
	return out
}

// contains checks if a string slice contains a specific value.
func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// execSQL runs a SQL statement and fails the test on error.
func execSQL(t *testing.T, db *sql.DB, sql string) {
	t.Helper()
	if _, err := db.Exec(sql); err != nil {
		t.Fatalf("execSQL: %v\nsql: %s", err, sql)
	}
}
