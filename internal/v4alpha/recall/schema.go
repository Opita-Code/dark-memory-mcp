package recall

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CreateSchema applies the Phase 5 memory subsystem schema. Idempotent.
//
// Three steps:
//
//  1. Additive ALTERs on agent_memory: 20 new columns for embeddings,
//     decay, code refs, graph residuals, decision subsystem. Each
//     ALTER is guarded by pragma_table_info so re-runs are no-ops.
//
//  2. New tables: agent_memory_entities (ProGraph 2-layer entities),
//     agent_memory_links (CABLE sparse directed links),
//     decision_transitions (TokenMizer-style bitemporal transitions).
//     All three carry project_id (INV-19, alpha.17 hard isolation).
//
//  3. Indexes: 9 indexes covering embedding_model, vibe_case, ADR/INV
//     refs, entity lookups, link traversal, decision transition
//     queries. CREATE INDEX IF NOT EXISTS makes re-runs no-ops.
//
// Safe to call on:
//   - Fresh DBs (all ADD COLUMN adds + creates tables).
//   - alpha.17 DBs (pragma detects missing columns + adds them;
//     new tables absent → CREATE IF NOT EXISTS creates them).
//   - Phase-5-shipped DBs (every pragma returns n>0 → no-op).
//   - pre-alpha.17 DBs with rows (ADD COLUMN backfills via DEFAULT).
//
// The migration is split across 3 steps so a partial application is
// recoverable: if step 1 succeeds and step 2 fails, the next call
// sees the columns in place and only retries step 2.
func CreateSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("recall CreateSchema: db is nil")
	}

	// Step 1: additive columns on agent_memory.
	for _, col := range agentMemoryColumns {
		if err := ensureColumn(ctx, db, "agent_memory", col.name, col.typ); err != nil {
			return fmt.Errorf("recall CreateSchema (agent_memory.%s): %w", col.name, err)
		}
	}

	// Step 2: new tables.
	for _, stmt := range newTables {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("recall CreateSchema (%s): %w", firstLine(stmt), err)
		}
	}

	// Step 3: indexes.
	for _, stmt := range newIndexes {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("recall CreateSchema (%s): %w", firstLine(stmt), err)
		}
	}

	return nil
}

// columnDef is one additive column. typ is the full DDL fragment
// after the column name (e.g. "TEXT", "INT DEFAULT 0", "BOOLEAN DEFAULT 1").
type columnDef struct {
	name string
	typ  string
}

// agentMemoryColumns is the canonical Phase 5 extension to agent_memory.
// Every column is sourced from a primary-source research artifact:
//
//   R-B §4 I-2 (cross-modal embeddings):
//     embedding, embedding_model, embedding_dim, embedding_created_at,
//     vibe_case, voice_embed_kind.
//   R-C §4 I-1 (temporal decay):
//     decay_class, decay_tau_days, last_refreshed_at, access_count,
//     refresh_on_access.
//   R-E §4 I-3 (code-specific retrieval):
//     adr_refs, inv_refs, commit_hashes.
//   R-D §4 I-2 (multi-hop graph residuals):
//     extracted_residuals.
//   R-F §4 I-2 (decision subsystem):
//     rationale, supersedes_id, decision_state, valid_from, valid_to.
//
// Count: 20 columns. See SPEC-alpha-11-phase5.md §4.1.
var agentMemoryColumns = []columnDef{
	// R-B §4 I-2: cross-modal embeddings (6 columns).
	{"embedding", "BLOB"},
	{"embedding_model", "TEXT"},
	{"embedding_dim", "INT"},
	{"embedding_created_at", "TEXT"},
	{"vibe_case", "TEXT"},
	{"voice_embed_kind", "TEXT"},
	// R-C §4 I-1: temporal decay (5 columns).
	{"decay_class", "TEXT"},
	{"decay_tau_days", "INT"},
	{"last_refreshed_at", "TEXT"},
	{"access_count", "INT DEFAULT 0"},
	{"refresh_on_access", "BOOLEAN DEFAULT 1"},
	// R-E §4 I-3: code-specific refs (3 columns).
	{"adr_refs", "TEXT"},
	{"inv_refs", "TEXT"},
	{"commit_hashes", "TEXT"},
	// R-D §4 I-2: graph residuals (1 column).
	{"extracted_residuals", "TEXT"},
	// R-F §4 I-2: decision subsystem (5 columns).
	{"rationale", "TEXT"},
	{"supersedes_id", "INTEGER"},
	{"decision_state", "TEXT DEFAULT 'active'"},
	{"valid_from", "TEXT"},
	{"valid_to", "TEXT"},
}

// newTables is the canonical Phase 5 table list. Each table carries
// project_id (INV-19, alpha.17) for hard isolation across coexistence
// groups.
var newTables = []string{
	// R-D §4 I-2: ProGraph 2-layer entities (memory row → named entities).
	// Entity extraction runs on Save (alpha.18 stubs it; alpha.19 LLM-extracts).
	//
	// NOTE: FOREIGN KEY references "agent_memory(id)" — NOT
	// "agent_memory(row_id)". The agent_memory primary key column is
	// `id`; there is no `row_id` column. SQLite enforces the
	// referenced column list; using `row_id` here caused "foreign
	// key mismatch" errors on DELETE FROM agent_memory (which broke
	// agent_memory.Store.Archive / FTS5 sync in v0).
	`CREATE TABLE IF NOT EXISTS agent_memory_entities (
		entity_id INTEGER PRIMARY KEY AUTOINCREMENT,
		row_id INTEGER NOT NULL,
		entity TEXT NOT NULL,
		entity_kind TEXT NOT NULL,
		project_id TEXT NOT NULL DEFAULT 'default',
		FOREIGN KEY (row_id) REFERENCES agent_memory(id) ON DELETE CASCADE
	)`,
	// R-D §4 I-3: CABLE sparse directed links (source → target with kind + weight).
	// Alpha.18: writes are operator-flag (default OFF). Alpha.19: auto on Save.
	`CREATE TABLE IF NOT EXISTS agent_memory_links (
		source_id INTEGER NOT NULL,
		target_id INTEGER NOT NULL,
		kind TEXT NOT NULL,
		weight REAL DEFAULT 1.0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		project_id TEXT NOT NULL DEFAULT 'default',
		PRIMARY KEY (source_id, target_id, kind),
		FOREIGN KEY (source_id) REFERENCES agent_memory(id) ON DELETE CASCADE,
		FOREIGN KEY (target_id) REFERENCES agent_memory(id) ON DELETE CASCADE
	)`,
	// R-F §4 I-2: TokenMizer-style decision transitions.
	// Records each supersession event (trigger, reason, evidence).
	// alpha.18: writes are operator-flag. alpha.19: auto on decision_state change.
	`CREATE TABLE IF NOT EXISTS decision_transitions (
		transition_id INTEGER PRIMARY KEY AUTOINCREMENT,
		decision_id INTEGER NOT NULL,
		superseded_id INTEGER,
		trigger TEXT NOT NULL,
		reason TEXT NOT NULL,
		evidence TEXT,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		project_id TEXT NOT NULL DEFAULT 'default',
		FOREIGN KEY (decision_id) REFERENCES agent_memory(id) ON DELETE CASCADE,
		FOREIGN KEY (superseded_id) REFERENCES agent_memory(id) ON DELETE SET NULL
	)`,
}

// newIndexes covers the 5 hot query paths.
//
// The 9 indexes are not arbitrary; each one matches a documented
// Phase 5 retrieval path (see SPEC §12 acceptance criteria).
var newIndexes = []string{
	// Embedder lookups (C2/C4 vector, C5/C6 cross-modal).
	`CREATE INDEX IF NOT EXISTS idx_embedding_model ON agent_memory(embedding_model)`,
	// Vibe-case dispatch (RecallFor routes by vibe_case).
	`CREATE INDEX IF NOT EXISTS idx_vibe_case ON agent_memory(vibe_case)`,
	// C1 code retrieval: ADR/INV refs indexed for graph expansion.
	`CREATE INDEX IF NOT EXISTS idx_adr_refs ON agent_memory(adr_refs)`,
	`CREATE INDEX IF NOT EXISTS idx_inv_refs ON agent_memory(inv_refs)`,
	// R-D §4 I-2: entity lookup (PPR seed nodes).
	`CREATE INDEX IF NOT EXISTS idx_entity ON agent_memory_entities(entity)`,
	// R-D §4 I-3: link traversal (source-side, target-side).
	`CREATE INDEX IF NOT EXISTS idx_link_source ON agent_memory_links(source_id)`,
	`CREATE INDEX IF NOT EXISTS idx_link_target ON agent_memory_links(target_id)`,
	// R-F §4 I-2: decision-transition history per row.
	`CREATE INDEX IF NOT EXISTS idx_dt_decision ON decision_transitions(decision_id)`,
	`CREATE INDEX IF NOT EXISTS idx_dt_superseded ON decision_transitions(superseded_id)`,
}

// ensureColumn adds one column to one table iff missing. Uses
// pragma_table_info to detect pre-existing columns (idempotent).
//
// The typ argument is the full DDL fragment after the column name,
// e.g. "TEXT", "INT DEFAULT 0", "BOOLEAN DEFAULT 1". SQLite accepts
// these forms directly after ADD COLUMN.
func ensureColumn(ctx context.Context, db *sql.DB, table, name, typ string) error {
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		table, name,
	).Scan(&n); err != nil {
		return fmt.Errorf("pragma_table_info %s.%s: %w", table, name, err)
	}
	if n > 0 {
		return nil
	}
	// Quote the column name defensively. All Phase 5 column names
	// are valid SQL identifiers (lowercase + underscores) so the
	// extra quotes are belt-and-suspenders, not a load-bearing
	// requirement.
	if _, err := db.ExecContext(ctx,
		"ALTER TABLE "+table+" ADD COLUMN "+name+" "+typ,
	); err != nil {
		return fmt.Errorf("ALTER TABLE %s ADD COLUMN %s %s: %w", table, name, typ, err)
	}
	return nil
}

// firstLine returns the first line of s (up to the first newline or
// the whole string). Used to keep error messages readable when a
// multi-line CREATE TABLE statement fails.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}
