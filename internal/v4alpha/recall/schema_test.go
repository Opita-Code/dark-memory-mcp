package recall

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// openTestDB returns a freshly-opened *sql.DB wired for tests.
// Each test gets its own file in t.TempDir() so parallel runs
// don't collide on the WAL.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "recall_test.db")
	db, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(); _ = os.RemoveAll(dir) })
	return db
}

// bootMinimalSchema creates the audit_log + agent_memory schemas
// that the recall package expects to extend. It does NOT create
// the projects / sessions / vibe / judge schemas — those are not
// needed for recall-specific tests.
func bootMinimalSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := agent_memory.CreateSchema(db); err != nil {
		t.Fatalf("agent_memory.CreateSchema: %v", err)
	}
}

// listColumns returns every column name on `table` (sorted).
// Used to verify additive ALTERs actually landed.
func listColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("pragma_table_info %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma_table_info rows: %v", err)
	}
	sort.Strings(out)
	return out
}

// listTables returns every table in the schema (sorted).
func listTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatalf("sqlite_master list: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master rows: %v", err)
	}
	return out
}

// listIndexes returns every index on `table` (sorted by name).
func listIndexes(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='index' AND tbl_name = ? ORDER BY name",
		table)
	if err != nil {
		t.Fatalf("sqlite_master index list: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master rows: %v", err)
	}
	return out
}

// containsAll reports whether `got` contains every name in `want`.
// Used to verify a subset of columns/tables/indexes is present
// without caring about extra ones (Phase 4 added project_id and
// we don't want tests to break if a future phase adds more).
func containsAll(got, want []string) bool {
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}

// TestCreateSchema_AddsColumnsToAgentMemory verifies that all 20
// Phase 5 columns land on agent_memory after CreateSchema.
//
// Critical: this is the "what does Phase 5 ship?" contract. If a
// new column is added in schema.go but not in this test, the
// drift is silent — operator may not notice until a recall query
// fails at runtime.
func TestCreateSchema_AddsColumnsToAgentMemory(t *testing.T) {
	db := openTestDB(t)
	bootMinimalSchema(t, db)

	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	want := []string{
		// R-B §4 I-2: cross-modal embeddings.
		"embedding", "embedding_model", "embedding_dim", "embedding_created_at",
		"vibe_case", "voice_embed_kind",
		// R-C §4 I-1: temporal decay.
		"decay_class", "decay_tau_days", "last_refreshed_at",
		"access_count", "refresh_on_access",
		// R-E §4 I-3: code-specific refs.
		"adr_refs", "inv_refs", "commit_hashes",
		// R-D §4 I-2: graph residuals.
		"extracted_residuals",
		// R-F §4 I-2: decision subsystem.
		"rationale", "supersedes_id", "decision_state", "valid_from", "valid_to",
	}
	got := listColumns(t, db, "agent_memory")
	if !containsAll(got, want) {
		t.Errorf("agent_memory missing Phase 5 columns.\nwant any-of: %v\ngot: %v\nmissing: %v",
			want, got, missingFrom(got, want))
	}
}

// TestCreateSchema_AddsNewTables verifies the 3 Phase 5 tables land.
func TestCreateSchema_AddsNewTables(t *testing.T) {
	db := openTestDB(t)
	bootMinimalSchema(t, db)

	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	want := []string{
		"agent_memory_entities",
		"agent_memory_links",
		"decision_transitions",
	}
	got := listTables(t, db)
	if !containsAll(got, want) {
		t.Errorf("missing Phase 5 tables.\nwant any-of: %v\ngot: %v\nmissing: %v",
			want, got, missingFrom(got, want))
	}
}

// TestCreateSchema_AddsIndexes verifies all 9 Phase 5 indexes land.
//
// Indexes are split across agent_memory (4), agent_memory_entities (1),
// agent_memory_links (2), and decision_transitions (2). Each listIndexes
// call scopes to the right table.
func TestCreateSchema_AddsIndexes(t *testing.T) {
	db := openTestDB(t)
	bootMinimalSchema(t, db)

	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	wantByTable := map[string][]string{
		"agent_memory": {
			"idx_embedding_model", "idx_vibe_case",
			"idx_adr_refs", "idx_inv_refs",
		},
		"agent_memory_entities": {"idx_entity"},
		"agent_memory_links":    {"idx_link_source", "idx_link_target"},
		"decision_transitions":  {"idx_dt_decision", "idx_dt_superseded"},
	}
	for table, want := range wantByTable {
		got := listIndexes(t, db, table)
		if !containsAll(got, want) {
			t.Errorf("%s missing Phase 5 indexes.\nwant any-of: %v\ngot: %v\nmissing: %v",
				table, want, got, missingFrom(got, want))
		}
	}
}

// TestCreateSchema_Idempotent verifies that calling CreateSchema
// twice (or on a pre-Phase-5 DB) doesn't error or duplicate state.
func TestCreateSchema_Idempotent(t *testing.T) {
	db := openTestDB(t)
	bootMinimalSchema(t, db)

	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("first CreateSchema: %v", err)
	}
	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("second CreateSchema (idempotency check): %v", err)
	}

	// And once more — third call must also succeed.
	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("third CreateSchema (idempotency check): %v", err)
	}

	// Verify column count is still 20 (not doubled).
	// Compute the expected set directly from agentMemoryColumns so the
	// test cannot drift from schema.go (avoid the "forgot to add a name
	// to the counter" failure class — exact mistake we hit once).
	cols := listColumns(t, db, "agent_memory")
	want := make(map[string]struct{}, len(agentMemoryColumns))
	for _, c := range agentMemoryColumns {
		want[c.name] = struct{}{}
	}
	count := 0
	for _, c := range cols {
		if _, ok := want[c]; ok {
			count++
		}
	}
	if count != len(agentMemoryColumns) {
		t.Errorf("expected %d Phase 5 columns, found %d (possible duplicate-add)",
			len(agentMemoryColumns), count)
	}
}

// TestCreateSchema_PreservesExistingRows verifies that running
// CreateSchema on a DB with pre-existing agent_memory rows does
// NOT lose those rows. The Phase 4 ApplyProjectIDColumns precedent
// established this contract; Phase 5 must honour it.
func TestCreateSchema_PreservesExistingRows(t *testing.T) {
	db := openTestDB(t)
	bootMinimalSchema(t, db)

	// Insert one row before applying Phase 5 schema.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO agent_memory (operator, kind, content)
		VALUES (?, ?, ?)
	`, "test-op", "note", "pre-existing content"); err != nil {
		t.Fatalf("insert pre-existing row: %v", err)
	}

	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	// Row must still exist with the original content.
	var content string
	if err := db.QueryRowContext(context.Background(),
		"SELECT content FROM agent_memory WHERE operator = ?", "test-op",
	).Scan(&content); err != nil {
		t.Fatalf("read pre-existing row after migration: %v", err)
	}
	if content != "pre-existing content" {
		t.Errorf("pre-existing row content corrupted.\nwant: %q\ngot:  %q",
			"pre-existing content", content)
	}

	// New columns must be NULL (not back-filled with garbage).
	var decayClass sql.NullString
	if err := db.QueryRowContext(context.Background(),
		"SELECT decay_class FROM agent_memory WHERE operator = ?", "test-op",
	).Scan(&decayClass); err != nil {
		t.Fatalf("read decay_class: %v", err)
	}
	if decayClass.Valid {
		t.Errorf("pre-existing row decay_class should be NULL, got %q", decayClass.String)
	}
}

// TestCreateSchema_NewTablesHaveProjectID verifies that all 3
// new tables carry project_id (INV-19, alpha.17 hard isolation).
// Each new coexistence instance must be able to scope queries
// by project_id without leaking across groups.
func TestCreateSchema_NewTablesHaveProjectID(t *testing.T) {
	db := openTestDB(t)
	bootMinimalSchema(t, db)

	if err := CreateSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	for _, table := range []string{
		"agent_memory_entities",
		"agent_memory_links",
		"decision_transitions",
	} {
		cols := listColumns(t, db, table)
		if !containsAll(cols, []string{"project_id"}) {
			t.Errorf("%s missing project_id column (INV-19).\ncols: %v", table, cols)
		}
	}
}

// TestDefaultDecayForKind verifies the canonical kind → decay mapping
// per R-C §4 I-1.
//
// The mapping is a public contract: changing it changes the meaning
// of "decay" across the whole subsystem. Tests are exhaustive.
func TestDefaultDecayForKind(t *testing.T) {
	cases := []struct {
		kind        string
		wantClass   string
		wantTauDays int
	}{
		{"decision", "forever", 0},
		{"finding", "persistent", 1095},
		{"observation", "stable", 365},
		{"context", "stable", 365},
		{"note", "perishable", 90},
		{"todo", "perishable", 60},
		{"link", "instant", 30},
	}
	for _, c := range cases {
		gotClass, gotTau := DefaultDecayForKind(c.kind)
		if gotClass != c.wantClass || gotTau != c.wantTauDays {
			t.Errorf("DefaultDecayForKind(%q) = (%q, %d), want (%q, %d)",
				c.kind, gotClass, gotTau, c.wantClass, c.wantTauDays)
		}
	}
	// Unknown kind returns empty/zero — caller decides.
	if gotClass, gotTau := DefaultDecayForKind("unknown"); gotClass != "" || gotTau != 0 {
		t.Errorf("DefaultDecayForKind(unknown) = (%q, %d), want (\"\", 0)",
			gotClass, gotTau)
	}
}

// TestIsValidVibeCase verifies the canonical vibe_case allow-list.
func TestIsValidVibeCase(t *testing.T) {
	for _, v := range []string{
		"code", "text", "decision", "research",
		"video", "audio", "multi",
	} {
		if !IsValidVibeCase(v) {
			t.Errorf("IsValidVibeCase(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "image", "infra", "governance", "unknown"} {
		if IsValidVibeCase(v) {
			t.Errorf("IsValidVibeCase(%q) = true, want false", v)
		}
	}
}

// missingFrom returns elements of want that are NOT in got.
// Used to build actionable error messages.
func missingFrom(got, want []string) []string {
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	var out []string
	for _, w := range want {
		if _, ok := set[w]; !ok {
			out = append(out, w)
		}
	}
	return out
}
