package migrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
	migratesqlite "github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
)

// TestMigrate_V24EntitiesTable exercises v24 (PR-3 of v2.9.0
// plan, agent_memory row 160). The migration adds the
// agent_memory_entities side table that holds extracted noun
// phrases per agent_memory row.
//
// The fixture mirrors the PR-2 v23 test pattern: open a fresh
// dark.db, apply all migrations, verify the schema is present
// + accepts INSERTs + ON CONFLICT DO NOTHING is idempotent.
func TestMigrate_V24EntitiesTable(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v24.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := migrate.Migrate(ctx, db, migratesqlite.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Highest applied version must be >= 24.
	got, err := migrate.SchemaVersion(ctx, db)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if got < 24 {
		t.Errorf("schema version: got %d, want >= 24", got)
	}

	// agent_memory_entities table exists.
	var hasTable int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_memory_entities'`,
	).Scan(&hasTable); err != nil {
		t.Fatalf("pragma table: %v", err)
	}
	if hasTable != 1 {
		t.Errorf("agent_memory_entities table: got count %d, want 1", hasTable)
	}

	// Schema check: PK on (mem_id, entity). PRAGMA table_info
	// returns columns; the PK is a separate check.
	colNames := map[string]bool{}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(agent_memory_entities)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, dfltValue, pk interface{}
		_ = cid
		_ = notnull
		_ = dfltValue
		_ = pk
		colNames[ctype+"."+name] = false
		_ = colNames
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		colNames[name] = true
	}
	rows.Close()
	for _, want := range []string{"mem_id", "entity", "source", "confidence", "model", "created_at"} {
		if !colNames[want] {
			t.Errorf("column missing: %q", want)
		}
	}

	// End-to-end: insert a parent agent_memory row + a child
	// entity row. Round-trip the entity via SELECT. ON CONFLICT
	// DO NOTHING is exercised by re-inserting.
	now := "2026-08-02T00:00:00Z"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_memory (
			project_id, operator, kind, content, created_at, updated_at
		) VALUES ('test', 'alice', 'note', 'memory row for entities', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	var memID int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM agent_memory WHERE operator = 'alice' AND content = 'memory row for entities'`,
	).Scan(&memID); err != nil {
		t.Fatalf("select parent id: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_memory_entities (mem_id, entity, source, confidence, model, created_at)
		VALUES (?, 'dark', 'deterministic', 1.0, NULL, ?)`,
		memID, now); err != nil {
		t.Fatalf("insert entity: %v", err)
	}

	// Round-trip.
	var ent string
	var src string
	var conf float64
	var model *string
	if err := db.QueryRowContext(ctx,
		`SELECT entity, source, confidence, model FROM agent_memory_entities WHERE mem_id = ? ORDER BY entity`,
		memID,
	).Scan(&ent, &src, &conf, &model); err != nil {
		t.Fatalf("select entity: %v", err)
	}
	if ent != "dark" {
		t.Errorf("entity value: got %q, want dark", ent)
	}
	if src != "deterministic" {
		t.Errorf("source: got %q, want deterministic", src)
	}
	if conf != 1.0 {
		t.Errorf("confidence: got %f, want 1.0", conf)
	}
	if model != nil {
		t.Errorf("model: got %v, want nil (PR-3 stub)", *model)
	}

	// ON CONFLICT (mem_id, entity) DO NOTHING: re-inserting the
	// same entity must NOT error and must NOT create a duplicate.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_memory_entities (mem_id, entity, source, confidence, model, created_at)
		VALUES (?, 'dark', 'deterministic', 1.0, NULL, ?)
		ON CONFLICT (mem_id, entity) DO NOTHING`,
		memID, now); err != nil {
		t.Errorf("on-conflict re-insert: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_memory_entities WHERE mem_id = ? AND entity = 'dark'`,
		memID,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("on-conflict: count got %d, want 1", count)
	}

	// Idempotency: re-running migrate on a v24+ DB is a no-op.
	if err := migrate.Migrate(ctx, db, migratesqlite.Migrations); err != nil {
		t.Errorf("migrate (re-run): %v", err)
	}
	got2, _ := migrate.SchemaVersion(ctx, db)
	if got2 != got {
		t.Errorf("schema version drift: got %d after re-run, want %d", got2, got)
	}
}
