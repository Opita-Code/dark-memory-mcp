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

// TestMigrate_V23EmbeddingColumn exists to verify v23 (PR-2 of v2.9.0
// plan, agent_memory row 160) actually applies the embedding BLOB
// column to the agent_memory table. The PR-2 + drift_judge flagged
// that the migration was code-present but not end-to-end exercised;
// this test closes that gap with the smallest possible fixture.
//
// The test:
//  1. Opens a fresh dark.db at a temp path.
//  2. Applies all migrations via migrate.Migrate.
//  3. Verifies SchemaVersion == 23.
//  4. Verifies the agent_memory.embedding column is queryable +
//     accepts a BLOB (not just NULL).
//  5. Verifies the migration is idempotent (F37 duplicate-column
//     tolerance): running it twice does not break the schema.
func TestMigrate_V23EmbeddingColumn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v23.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := migrate.Migrate(ctx, db, migratesqlite.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Highest applied version must be 23 (porter + porter_stemming +
	// agent_memory_embedding are the last 3 entries).
	got, err := migrate.SchemaVersion(ctx, db)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if got < 23 {
		t.Errorf("schema version: got %d, want >= 23 (v22 lands first; v23 requires the embedding column)", got)
	}

	// Verify the embedding column accepts a BLOB (real byte data, not
	// just NULL). PRAGMA table_info() confirms the column exists; an
	// INSERT+SELECT round-trips confirms the BLOB is storable.
	var hasCol int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('agent_memory') WHERE name = 'embedding'`,
	).Scan(&hasCol); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if hasCol != 1 {
		t.Errorf("agent_memory.embedding column: got count %d, want 1", hasCol)
	}

	// End-to-end: insert a row with a non-NULL embedding blob, read it
	// back. This exercises the column's actual storage path (not just
	// its DDL).
	var blob = []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_memory (
			project_id, operator, kind, content, created_at, updated_at, embedding
		) VALUES ('test', 'alice', 'note', 'round-trip-canary', '2026-08-02T00:00:00Z', '2026-08-02T00:00:00Z', ?)`, blob); err != nil {
		t.Fatalf("insert with blob: %v", err)
	}
	var got2 []byte
	if err := db.QueryRowContext(ctx,
		`SELECT embedding FROM agent_memory WHERE operator = 'alice' AND content = 'round-trip-canary'`,
	).Scan(&got2); err != nil {
		t.Fatalf("select blob: %v", err)
	}
	if len(got2) != len(blob) {
		t.Errorf("blob round-trip: got %d bytes, want %d", len(got2), len(blob))
	}
	for i := range blob {
		if got2[i] != blob[i] {
			t.Errorf("blob[%d]: got %02x, want %02x", i, got2[i], blob[i])
			break
		}
	}

	// Idempotency (F37 tolerance): re-running migrate on a v23 DB
	// should be a no-op (the runner checks schema_migrations before
	// applying each entry). Schema version must stay at 23.
	if err := migrate.Migrate(ctx, db, migratesqlite.Migrations); err != nil {
		t.Errorf("migrate (re-run): %v", err)
	}
	got3, err := migrate.SchemaVersion(ctx, db)
	if err != nil {
		t.Fatalf("schema version (re-run): %v", err)
	}
	if got3 != got {
		t.Errorf("schema version drift: got %d after re-run, want %d", got3, got)
	}
}
