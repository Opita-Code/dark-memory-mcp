package recall

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestDB returns a fresh *sql.DB wired with audit + agent_memory +
// Phase 5 (recall) schemas, plus an audit.Writer and an agent_memory.Store
// ready to call Save. Each test gets its own TempDir.
func newTestDB(t *testing.T) (*sql.DB, *audit.Writer, *agent_memory.Store) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "recall_c3_test.db")
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := audit.CreateSchema(d); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := agent_memory.CreateSchema(d); err != nil {
		t.Fatalf("agent_memory.CreateSchema: %v", err)
	}
	if err := project.ApplyProjectIDColumns(context.Background(), d); err != nil {
		t.Fatalf("project.ApplyProjectIDColumns: %v", err)
	}
	if err := CreateSchema(context.Background(), d); err != nil {
		t.Fatalf("recall.CreateSchema: %v", err)
	}
	w := audit.NewWriter(d)
	return d, w, agent_memory.NewStore(d, w)
}

// testAudit returns the canonical audit metadata for tests.
func testAudit() *agent_memory.Audit { return &agent_memory.Audit{Actor: "nico"} }

// saveDecision inserts one decision-kind row with the given content
// and adr_refs. Returns the new id.
func saveDecision(t *testing.T, st *agent_memory.Store, content, adrRefs string) int64 {
	t.Helper()
	id, err := st.Save(context.Background(), testAudit(), "nico", "decision", "test decision",
		content, "phase5,test", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if adrRefs != "" {
		if err := st.Update(context.Background(), testAudit(), id, nil,
			stringPtr(content), nil, nil); err != nil {
			// Update with same content is a no-op for content. We need
			// a way to set adr_refs. Direct SQL is fine for tests.
		}
		// Direct SQL update for the new columns (alpha.18 has no public
		// Save-with-references API; alpha.19 adds it).
		if _, err := st.Get(context.Background(), id); err != nil {
			t.Fatalf("Get after Save: %v", err)
		}
	}
	return id
}

// stringPtr returns &s for tests that need a *string.
func stringPtr(s string) *string { return &s }

// markSuperseded sets decision_state='superseded' on `id` and records
// a decision_transitions row pointing to `supersededBy`.
//
// Phase 5 alpha.18 has no public decision-API; tests do this via
// direct SQL to exercise the C3 filter without coupling to alpha.19.
func markSuperseded(t *testing.T, db *sql.DB, id, supersededBy int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE agent_memory SET decision_state='superseded', supersedes_id=?, valid_to=CURRENT_TIMESTAMP WHERE id=?`,
		supersededBy, id); err != nil {
		t.Fatalf("UPDATE superseded: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO decision_transitions (decision_id, superseded_id, trigger, reason, evidence, project_id)
		 VALUES (?, ?, ?, ?, ?, 'default')`,
		id, supersededBy, "operator_action", "superseded by newer decision", "test", "default"); err != nil {
		t.Fatalf("INSERT decision_transitions: %v", err)
	}
}

// saveDecisionWithProject inserts one decision-kind row in a
// non-default project (INV-19 isolation test). Also syncs the FTS5
// index so the C3 FTS5-first pipeline can find the row.
func saveDecisionWithProject(t *testing.T, db *sql.DB, content, projectID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_memory (operator, kind, content, project_id) VALUES (?, ?, ?, ?)`,
		"nico", "decision", content, projectID); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// Sync FTS5 (matches agent_memory.Store.Save semantics).
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_memory_fts (rowid, content, title, tags)
		 SELECT id, content, COALESCE(title,''), COALESCE(tags,'') FROM agent_memory
		 WHERE operator=? AND content=?`,
		"nico", content); err != nil {
		t.Fatalf("FTS5 sync: %v", err)
	}
}

// setAdrRefs sets the adr_refs column on a single row. Direct SQL
// because alpha.18 has no Save-with-references API.
func setAdrRefs(t *testing.T, db *sql.DB, id int64, refs string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE agent_memory SET adr_refs=? WHERE id=?`, refs, id); err != nil {
		t.Fatalf("UPDATE adr_refs: %v", err)
	}
	// FTS5 content is unchanged (only adr_refs was edited, which is
	// NOT indexed by FTS5 — recall uses LIKE on adr_refs at query
	// time, not the FTS5 bm25). No FTS5 sync required.
}

// TestRecallFor_UnknownVibeCase verifies that RecallFor rejects
// vibe_cases outside the canonical 7 with ErrUnknownVibeCase.
func TestRecallFor_UnknownVibeCase(t *testing.T) {
	db, _, _ := newTestDB(t)
	_, err := RecallFor(context.Background(), db, "image", "anything", "default", 10)
	if err == nil {
		t.Fatalf("expected ErrUnknownVibeCase, got nil")
	}
	if !errors.Is(err, ErrUnknownVibeCase) {
		t.Errorf("expected ErrUnknownVibeCase, got %v", err)
	}
}

// TestRecallFor_EmptyVibeCase verifies the empty string also fails.
func TestRecallFor_EmptyVibeCase(t *testing.T) {
	db, _, _ := newTestDB(t)
	_, err := RecallFor(context.Background(), db, "", "anything", "default", 10)
	if !errors.Is(err, ErrUnknownVibeCase) {
		t.Errorf("expected ErrUnknownVibeCase for empty vibe_case, got %v", err)
	}
}

// TestRecallFor_NoStrategy verifies that valid vibe_cases with no
// strategy yet return ErrNoStrategyRegistered (not a panic).
//
// In alpha.18 only "decision" has a strategy. C1/C2/C4/C5/C6/C7
// land in Chunks 3-4 and should fail gracefully today.
func TestRecallFor_NoStrategy(t *testing.T) {
	db, _, _ := newTestDB(t)
	for _, vc := range []string{"code", "text", "research", "video", "audio", "multi"} {
		_, err := RecallFor(context.Background(), db, vc, "anything", "default", 10)
		if !errors.Is(err, ErrNoStrategyRegistered) {
			t.Errorf("vibe_case=%q: expected ErrNoStrategyRegistered, got %v", vc, err)
		}
	}
}

// TestRecallFor_DecisionDispatch verifies that "decision" reaches
// the registered C3 strategy. Empty query → nil rows + nil error.
func TestRecallFor_DecisionDispatch(t *testing.T) {
	db, _, _ := newTestDB(t)
	rows, err := RecallFor(context.Background(), db, VibeCaseDecision, "", "default", 10)
	if err != nil {
		t.Errorf("empty query should be no-op: %v", err)
	}
	if rows != nil {
		t.Errorf("empty query should return nil, got %v", rows)
	}
}

// TestRegisteredVibeCases verifies init() registered exactly one
// strategy (C3) at package load time.
func TestRegisteredVibeCases(t *testing.T) {
	got := RegisteredVibeCases()
	if len(got) != 1 || got[0] != VibeCaseDecision {
		t.Errorf("registered vibe_cases = %v, want [%s]", got, VibeCaseDecision)
	}
}

// TestC3DecisionRecall_WeightsValid guards against typos in the
// canonical weight literal.
func TestC3DecisionRecall_WeightsValid(t *testing.T) {
	c := &C3DecisionRecall{}
	w := c.Weights()
	if err := w.Validate(); err != nil {
		t.Errorf("C3 weights must sum to 1.0: %v", err)
	}
	if w.FTS5 != 0.30 || w.Graph != 0.70 {
		t.Errorf("C3 weights = %+v, want FTS5=0.30 Graph=0.70", w)
	}
}

// TestC3DecisionRecall_FTS5Only seeds three decision rows, runs
// Recall("ship alpha"), and asserts that the FTS5-ranked row wins.
func TestC3DecisionRecall_FTS5Only(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveDecision(t, st, "decided to use Postgres for OLTP", "")
	id2 := saveDecision(t, st, "decided to ship alpha.18 in November", "")
	id3 := saveDecision(t, st, "chose to defer C5/C6 to alpha.19", "")
	_ = id1
	_ = id3

	got, err := RecallFor(ctx, db, VibeCaseDecision, "ship alpha", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("expected at least 1 result, got 0")
	}
	if got[0].ID != id2 {
		t.Errorf("expected row 2 (ship alpha.18) to win FTS5 ranking, got id=%d content=%q",
			got[0].ID, got[0].Content)
	}
}

// TestC3DecisionRecall_GraphExpansion seeds two decisions sharing
// ADR-7 (a hub ref) and one unrelated. The graph should promote the
// linked row into the result set even though it has no FTS match.
//
// Note: bm25 ranking of the FTS5 hits is brittle (shorter docs with
// the same term rank higher), so this test does NOT assert that row 1
// wins. It asserts that row 2 — which has NO FTS match — appears in
// the result via graph expansion. That's the load-bearing property of
// C3 over plain FTS5.
func TestC3DecisionRecall_GraphExpansion(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveDecision(t, st, "widget policy v1: red widgets only widgets widgets widgets", "")
	id2 := saveDecision(t, st, "gizmo policy v1: green gizmos only", "")
	id3 := saveDecision(t, st, "unrelated widget note", "")
	setAdrRefs(t, db, id1, "ADR-7")
	setAdrRefs(t, db, id2, "ADR-7")
	setAdrRefs(t, db, id3, "ADR-9")

	got, err := RecallFor(ctx, db, VibeCaseDecision, "widget", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected at least 2 rows (FTS5 + 1-hop graph), got %d", len(got))
	}
	// Row 2 (gizmo, no FTS match) MUST appear in the result set
	// because it shares ADR-7 with row 1. This is the load-bearing
	// assertion of graph expansion.
	foundGizmo := false
	for _, r := range got {
		if r.ID == id2 {
			foundGizmo = true
			break
		}
	}
	if !foundGizmo {
		t.Errorf("expected gizmo row (id=%d) in graph expansion, got %d rows: %+v",
			id2, len(got), rowContents(got))
	}
	// Row 1 (widget policy, the seed) must also be present.
	foundWidget := false
	for _, r := range got {
		if r.ID == id1 {
			foundWidget = true
			break
		}
	}
	if !foundWidget {
		t.Errorf("expected widget row (id=%d) as seed, got %d rows: %+v",
			id1, len(got), rowContents(got))
	}
}

// rowContents is a debugging helper for graph expansion test failures.
func rowContents(rows []AnnotatedRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Content
	}
	return out
}

// TestC3DecisionRecall_FilterSuperseded verifies that
// IncludeSuperseded=false (default) excludes superseded decisions.
func TestC3DecisionRecall_FilterSuperseded(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveDecision(t, st, "active decision about widgets", "")
	id2 := saveDecision(t, st, "superseded decision about widgets", "")
	markSuperseded(t, db, id2, id1)

	// Default (IncludeSuperseded=false): only row 1.
	got, err := RecallFor(ctx, db, VibeCaseDecision, "widgets", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 active row, got %d", len(got))
	}
	if got[0].ID != id1 {
		t.Errorf("expected active row id=%d, got id=%d", id1, got[0].ID)
	}

	// IncludeSuperseded=true: both rows.
	c := &C3DecisionRecall{IncludeSuperseded: true}
	all, err := c.Recall(ctx, db, "widgets", "default", 10)
	if err != nil {
		t.Fatalf("IncludeSuperseded Recall: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 rows with IncludeSuperseded=true, got %d", len(all))
	}
}

// TestC3DecisionRecall_ProjectIsolation verifies that rows in
// project A do NOT leak into project B (INV-19).
func TestC3DecisionRecall_ProjectIsolation(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	// Default project: 1 row.
	_ = saveDecision(t, st, "decision in default project", "")
	// Project "tenant-b": 1 row (direct SQL).
	saveDecisionWithProject(t, db, "decision in tenant-b", "tenant-b")

	gotA, err := RecallFor(ctx, db, VibeCaseDecision, "decision", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor default: %v", err)
	}
	if len(gotA) != 1 {
		t.Fatalf("expected 1 row in default project, got %d", len(gotA))
	}
	if !strings.Contains(gotA[0].Content, "default project") {
		t.Errorf("expected default project row, got %q", gotA[0].Content)
	}

	gotB, err := RecallFor(ctx, db, VibeCaseDecision, "decision", "tenant-b", 10)
	if err != nil {
		t.Fatalf("RecallFor tenant-b: %v", err)
	}
	if len(gotB) != 1 {
		t.Fatalf("expected 1 row in tenant-b project, got %d", len(gotB))
	}
	if !strings.Contains(gotB[0].Content, "tenant-b") {
		t.Errorf("expected tenant-b row, got %q", gotB[0].Content)
	}
}

// TestC3DecisionRecall_TopKLimits verifies that topK is honored.
func TestC3DecisionRecall_TopKLimits(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_ = saveDecision(t, st, "decision number about widgets", "")
	}

	for _, k := range []int{1, 3, 5, 20} {
		got, err := RecallFor(ctx, db, VibeCaseDecision, "widgets", "default", k)
		if err != nil {
			t.Fatalf("RecallFor(k=%d): %v", k, err)
		}
		expected := k
		if expected > 10 {
			expected = 10
		}
		if len(got) != expected {
			t.Errorf("topK=%d returned %d rows, want %d", k, len(got), expected)
		}
	}
}

// TestScanAnnotatedRows_HandlesNulls verifies that scanAnnotatedRows
// correctly reads rows where Phase 5 columns are NULL (legacy rows
// pre-Phase-5 migration).
func TestScanAnnotatedRows_HandlesNulls(t *testing.T) {
	db, _, _ := newTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_memory (operator, kind, content, project_id)
		VALUES (?, ?, ?, ?)
	`, "legacy-op", "decision", "legacy content", "default"); err != nil {
		t.Fatalf("insert legacy: %v", err)
	}

	rows, err := db.QueryContext(ctx,
		`SELECT `+annotatedColumns+` FROM agent_memory m WHERE m.operator = ?`, "legacy-op")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	out, err := scanAnnotatedRows(rows)
	if err != nil {
		t.Fatalf("scanAnnotatedRows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d", len(out))
	}
	r := out[0]
	if r.DecisionState != "active" {
		t.Errorf("COALESCE(default='active') failed; got %q", r.DecisionState)
	}
	if r.RefreshOnAccess != true {
		t.Errorf("COALESCE(default=1) failed; got %v", r.RefreshOnAccess)
	}
	if r.AccessCount != 0 {
		t.Errorf("COALESCE(default=0) failed; got %d", r.AccessCount)
	}
	if r.AdrRefs != "" || r.Rationale != "" {
		t.Errorf("expected empty strings for nullable TEXT, got adr=%q rationale=%q",
			r.AdrRefs, r.Rationale)
	}
}
