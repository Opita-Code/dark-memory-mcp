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

// TestMigrationV30_Phase5Port verifies migration v30 (Phase 5 schema
// port, alpha.20 Chunk 8.7 operator decision B) adds:
//
//   - 19 new columns on agent_memory (5 embeddings + 5 decay + 3 code refs
//     + 1 graph residual + 5 decision subsystem).
//   - 2 new tables: agent_memory_links (CABLE), decision_transitions
//     (TokenMizer-style supersession history).
//   - 7 new indexes (idx_embedding_model, idx_vibe_case, idx_adr_refs,
//     idx_inv_refs, idx_decision_state, idx_valid_from + 4 link/tx
//     indexes — actually 9 total, listed below).
//
// Strategy: simulate a fresh DB up to v29 (with v29-shape agent_memory,
// no Phase 5 columns), then apply v30 in isolation. Verify columns +
// tables + indexes via PRAGMA table_info / index_list.
func TestMigrationV30_Phase5Port(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v30.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Build v29-shape schema: core tables needed for v30 FK references
	// (agent_memory_links, decision_transitions point at agent_memory.id).
	execSQL(t, db, `
CREATE TABLE agent_memory (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL,
    session_id  TEXT,
    operator    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    title       TEXT,
    content     TEXT NOT NULL,
    tags        TEXT,
    pinned      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    archived_at TEXT,
    expires_at  TEXT,
    agent_id    TEXT,
    memory_type TEXT,
    embedding   BLOB
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (29, '2026-08-19T00:00:00.0000000Z');
`)

	// Apply v30.
	v30 := findMigration(t, sqlitemig.Migrations, 30, "phase5_port_to_production")
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v30}); err != nil {
		t.Fatalf("v30 migration failed: %v", err)
	}

	// Verify 19 new columns landed on agent_memory.
	cols := columnNames(t, db, "agent_memory")
	expectedNew := []string{
		// R-B cross-modal embeddings (5).
		"embedding_model", "embedding_dim", "embedding_created_at", "vibe_case", "voice_embed_kind",
		// R-C temporal decay (5).
		"decay_class", "decay_tau_days", "last_refreshed_at", "access_count", "refresh_on_access",
		// R-E code refs (3).
		"adr_refs", "inv_refs", "commit_hashes",
		// R-D graph residual (1).
		"extracted_residuals",
		// R-F decision subsystem (5).
		"rationale", "supersedes_id", "decision_state", "valid_from", "valid_to",
	}
	for _, want := range expectedNew {
		if !contains(cols, want) {
			t.Errorf("v30 column %q missing in agent_memory", want)
		}
	}

	// Verify 2 new tables exist.
	for _, table := range []string{"agent_memory_links", "decision_transitions"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&n); err != nil {
			t.Fatalf("check %s exists: %v", table, err)
		}
		if n != 1 {
			t.Errorf("v30 table %q missing", table)
		}
	}

	// Verify indexes.
	for _, table := range []string{"agent_memory", "agent_memory_links", "decision_transitions"} {
		idx := indexNames(t, db, table)
		// Spot-check at least one index per table.
		switch table {
		case "agent_memory":
			for _, want := range []string{
				"idx_embedding_model", "idx_vibe_case", "idx_adr_refs",
				"idx_inv_refs", "idx_decision_state", "idx_valid_from",
			} {
				if !contains(idx, want) {
					t.Errorf("v30 index %q missing on %s", want, table)
				}
			}
		case "agent_memory_links":
			for _, want := range []string{"idx_link_source", "idx_link_target"} {
				if !contains(idx, want) {
					t.Errorf("v30 index %q missing on %s", want, table)
				}
			}
		case "decision_transitions":
			for _, want := range []string{"idx_dt_decision", "idx_dt_superseded"} {
				if !contains(idx, want) {
					t.Errorf("v30 index %q missing on %s", want, table)
				}
			}
		}
	}

	// decision_state NOT NULL DEFAULT 'active' should backfill to 'active'
	// for newly inserted rows.
	execSQL(t, db, `INSERT INTO agent_memory
		(project_id, operator, kind, content, created_at, updated_at)
		VALUES ('default', 'tester', 'decision', 'phase5-port row',
		        '2026-10-01T00:00:00.0000000Z', '2026-10-01T00:00:00.0000000Z')`)
	var decisionState string
	if err := db.QueryRowContext(ctx,
		`SELECT decision_state FROM agent_memory ORDER BY id DESC LIMIT 1`,
	).Scan(&decisionState); err != nil {
		t.Fatalf("read decision_state: %v", err)
	}
	if decisionState != "active" {
		t.Errorf("decision_state DEFAULT 'active' not applied: got %q", decisionState)
	}

	// v30 should be recorded in schema_migrations.
	var v30Count int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 30`).Scan(&v30Count)
	if v30Count != 1 {
		t.Errorf("v30 should be recorded exactly once, got %d", v30Count)
	}
}

// TestMigrationV30_Idempotent verifies re-running v30 on a DB that
// already has the v30 columns is a no-op. applyOne's F37 tolerance
// treats "duplicate column name" as a warning.
func TestMigrationV30_Idempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v30-idem.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// v30-shape agent_memory (already has the 19 new columns).
	execSQL(t, db, `
CREATE TABLE agent_memory (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL,
    session_id  TEXT,
    operator    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    title       TEXT,
    content     TEXT NOT NULL,
    tags        TEXT,
    pinned      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    archived_at TEXT,
    expires_at  TEXT,
    agent_id    TEXT,
    memory_type TEXT,
    embedding   BLOB,
    embedding_model      TEXT,
    embedding_dim        INTEGER,
    embedding_created_at TEXT,
    vibe_case            TEXT,
    voice_embed_kind     TEXT,
    decay_class         TEXT,
    decay_tau_days      INTEGER,
    last_refreshed_at   TEXT,
    access_count         INTEGER NOT NULL DEFAULT 0,
    refresh_on_access   INTEGER NOT NULL DEFAULT 1,
    adr_refs      TEXT,
    inv_refs      TEXT,
    commit_hashes TEXT,
    extracted_residuals TEXT,
    rationale       TEXT,
    supersedes_id   INTEGER,
    decision_state  TEXT NOT NULL DEFAULT 'active',
    valid_from      TEXT,
    valid_to        TEXT
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (30, '2026-10-01T00:00:00.0000000Z');
`)

	before := columnNames(t, db, "agent_memory")

	v30 := findMigration(t, sqlitemig.Migrations, 30, "phase5_port_to_production")
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v30}); err != nil {
		t.Fatalf("v30 idempotent re-run: %v", err)
	}

	after := columnNames(t, db, "agent_memory")
	if len(before) != len(after) {
		t.Errorf("idempotent re-run should not change column count: before=%d after=%d",
			len(before), len(after))
	}

	// schema_migrations should still have v30 recorded exactly once.
	var v30Count int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 30`).Scan(&v30Count)
	if v30Count != 1 {
		t.Errorf("v30 should be recorded exactly once after idempotent re-run, got %d", v30Count)
	}
}

// TestMigrationV31_BitemporalLite verifies migration v31 adds
// transaction_time + valid_time columns + 2 indexes, AND backfills
// existing rows from created_at.
func TestMigrationV31_BitemporalLite(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v31.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// v30-shape agent_memory + one pre-v31 row.
	execSQL(t, db, `
CREATE TABLE agent_memory (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL,
    session_id  TEXT,
    operator    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    title       TEXT,
    content     TEXT NOT NULL,
    tags        TEXT,
    pinned      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    archived_at TEXT,
    expires_at  TEXT,
    decision_state TEXT NOT NULL DEFAULT 'active',
    valid_from TEXT,
    valid_to   TEXT
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (30, '2026-10-01T00:00:00.0000000Z');
INSERT INTO agent_memory
    (project_id, operator, kind, content, created_at, updated_at)
VALUES
    ('default', 'tester', 'note', 'pre-v31 content',
     '2025-01-15T00:00:00.0000000Z', '2025-01-15T00:00:00.0000000Z');
`)

	// Apply v31.
	v31 := findMigration(t, sqlitemig.Migrations, 31, "bitemporal_lite")
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v31}); err != nil {
		t.Fatalf("v31 migration failed: %v", err)
	}

	// Verify the two new columns + backfill.
	cols := columnNames(t, db, "agent_memory")
	for _, want := range []string{"transaction_time", "valid_time"} {
		if !contains(cols, want) {
			t.Errorf("v31 column %q missing", want)
		}
	}

	// Pre-v31 row should be backfilled from created_at.
	var txTime, validTime string
	if err := db.QueryRowContext(ctx,
		`SELECT transaction_time, valid_time FROM agent_memory ORDER BY id ASC LIMIT 1`,
	).Scan(&txTime, &validTime); err != nil {
		t.Fatalf("read backfilled row: %v", err)
	}
	if txTime != "2025-01-15T00:00:00.0000000Z" {
		t.Errorf("transaction_time backfill: got %q, want 2025-01-15T00:00:00.0000000Z", txTime)
	}
	if validTime != "2025-01-15T00:00:00.0000000Z" {
		t.Errorf("valid_time backfill: got %q, want 2025-01-15T00:00:00.0000000Z", validTime)
	}

	// Verify indexes.
	idx := indexNames(t, db, "agent_memory")
	for _, want := range []string{"idx_transaction_time", "idx_valid_time"} {
		if !contains(idx, want) {
			t.Errorf("v31 index %q missing", want)
		}
	}
}

// TestMigrationV31_Idempotent verifies v31 re-run is a no-op (F37).
func TestMigrationV31_Idempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "v31-idem.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	execSQL(t, db, `
CREATE TABLE agent_memory (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL,
    session_id  TEXT,
    operator    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    title       TEXT,
    content     TEXT NOT NULL,
    tags        TEXT,
    pinned      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    archived_at TEXT,
    expires_at  TEXT,
    decision_state TEXT NOT NULL DEFAULT 'active',
    valid_from TEXT,
    valid_to   TEXT,
    transaction_time TEXT,
    valid_time       TEXT
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (31, '2026-10-01T00:00:00.0000000Z');
`)
	before := columnNames(t, db, "agent_memory")

	v31 := findMigration(t, sqlitemig.Migrations, 31, "bitemporal_lite")
	if err := migrate.Migrate(ctx, db, []migrate.Migration{v31}); err != nil {
		t.Fatalf("v31 idempotent re-run: %v", err)
	}

	after := columnNames(t, db, "agent_memory")
	if len(before) != len(after) {
		t.Errorf("v31 idempotent re-run should not change column count: before=%d after=%d",
			len(before), len(after))
	}
}