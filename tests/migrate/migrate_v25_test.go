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

// TestMigrate_V25ErrorEvents exercises v25 (Error Observatory,
// spec 757, Wave 5D). The migration adds the error_events table
// that durably captures classified, deduplicated error clusters.
func TestMigrate_V25ErrorEvents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v25.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := migrate.Migrate(ctx, db, migratesqlite.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := migrate.SchemaVersion(ctx, db)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if got < 25 {
		t.Errorf("schema version: got %d, want >= 25", got)
	}

	// error_events table exists.
	var hasTable int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='error_events'`,
	).Scan(&hasTable); err != nil {
		t.Fatalf("pragma table: %v", err)
	}
	if hasTable != 1 {
		t.Errorf("error_events table: got count %d, want 1", hasTable)
	}

	// Column presence check.
	colNames := map[string]bool{}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(error_events)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		colNames[name] = true
	}
	rows.Close()
	for _, want := range []string{
		"id", "project_id", "session_id", "tool_name", "domain", "code",
		"message", "message_hash", "context_json", "severity", "count",
		"first_seen_at", "last_seen_at", "resolved", "resolved_at",
		"resolution_note", "created_at",
	} {
		if !colNames[want] {
			t.Errorf("error_events missing column %q", want)
		}
	}

	// Idempotency: re-running Migrate is a no-op.
	if err := migrate.Migrate(ctx, db, migratesqlite.Migrations); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	// INSERT works end-to-end.
	_, err = db.ExecContext(ctx, `
		INSERT INTO error_events (
			project_id, domain, code, message, message_hash, severity,
			count, first_seen_at, last_seen_at, resolved, created_at
		) VALUES ('default', 'store', 'ErrNotFound', 'row not found', 'abc123',
		          'error', 1, '2026-08-04T00:00:00Z', '2026-08-04T00:00:00Z', 0, '2026-08-04T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// The 5 indexes exist.
	for _, idx := range []string{
		"idx_error_events_last_seen", "idx_error_events_domain",
		"idx_error_events_severity", "idx_error_events_resolved",
		"idx_error_events_dedup",
	} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&n); err != nil {
			t.Fatalf("index check %s: %v", idx, err)
		}
		if n != 1 {
			t.Errorf("index %s: got %d, want 1", idx, n)
		}
	}
}
