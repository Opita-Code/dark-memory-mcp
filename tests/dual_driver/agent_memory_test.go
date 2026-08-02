// Package dual_driver - agent_memory_test.go: integration tests for
// the agent_memory data plane against a real SQLite driver.
// Validates the v2.1.0 Store interface methods:
// SaveAgentMemory / GetAgentMemory / UpdateAgentMemory /
// ArchiveAgentMemory / ListAgentMemory / SearchAgentMemory.
//
// Drift-check for v2.1.0 (see
// vibe-flow/main/agent_memory_v2_1_0_split_statements.md and the
// agent_memory spec).
package dual_driver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	mockembedder "github.com/dark-agents/dark-memory-mcp/internal/embedder/mock"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	sqlitestore "github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// projectForCreate converts our minimal test stub into the real
// *project.Project the Store expects.
func projectForCreate(p projectAlias) *project.Project {
	return &project.Project{
		ProjectID:   p.ProjectID,
		DisplayName: p.DisplayName,
		Description: p.Description,
		CreatedAt:   p.CreatedAt,
	}
}

// openAgentMemoryDB creates a fresh dark.db at a temp path, applies
// migrations, returns a ready Store + cleanup func.
func openAgentMemoryDB(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent_memory.db")
	cfg := store.Config{
		Driver: store.DriverSQLite,
		DSN:    dbPath,
	}
	st, err := sqlitestore.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	return st, func() { _ = st.Close() }
}

// makeRow is a tiny helper for tests — fills the operator/kind/
// content fields, leaves the rest zero (the Store fills id +
// timestamps + project_id).
func makeRow(operator, kind, content string) *agentmemory.AgentMemory {
	return &agentmemory.AgentMemory{
		Operator: operator,
		Kind:     kind,
		Content:  content,
	}
}

func wcFor(op, writePath string) store.WriteContext {
	return store.WriteContext{
		Actor:     op,
		WritePath: writePath,
	}
}

func TestAgentMemory_Save_Get(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "note", "first memory")
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test_save"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if id <= 0 {
		t.Fatalf("save returned id=%d, want >0", id)
	}
	if row.ID != id {
		t.Errorf("row.ID not refreshed after save: got %d, want %d", row.ID, id)
	}
	if row.ProjectID != "default" {
		t.Errorf("row.ProjectID not set: got %q", row.ProjectID)
	}

	got, err := st.GetAgentMemory(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatalf("get returned nil for id=%d", id)
	}
	if got.Operator != "alice" || got.Kind != "note" || got.Content != "first memory" {
		t.Errorf("get returned mismatched row: %+v", got)
	}
}

func TestAgentMemory_Save_ValidatesKind(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	bad := makeRow("alice", "not-a-real-kind", "x")
	_, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), bad)
	if err == nil {
		t.Fatal("expected validation error for invalid kind, got nil")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Errorf("error should mention 'kind': %v", err)
	}
}

func TestAgentMemory_Save_RequiresOperator(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := &agentmemory.AgentMemory{Kind: "note", Content: "x"} // no operator
	_, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err == nil {
		t.Fatal("expected error for missing operator")
	}
}

func TestAgentMemory_Save_RequiresContent(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := &agentmemory.AgentMemory{Operator: "alice", Kind: "note"} // no content
	_, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestAgentMemory_Update(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "note", "original content")
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	newTitle := "updated title"
	newContent := "updated content"
	u := &agentmemory.AgentMemoryUpdate{
		Title:   &newTitle,
		Content: &newContent,
	}
	updated, err := st.UpdateAgentMemory(ctx, wcFor("alice", "test"), id, u)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != "updated title" || updated.Content != "updated content" {
		t.Errorf("update didn't apply: %+v", updated)
	}
	if updated.Operator != "alice" {
		t.Errorf("update changed operator (must be immutable): got %q", updated.Operator)
	}
	if updated.ProjectID != "default" {
		t.Errorf("update changed project_id (must be immutable): got %q", updated.ProjectID)
	}
}

func TestAgentMemory_Archive(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "note", "to be archived")
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := st.ArchiveAgentMemory(ctx, wcFor("alice", "test"), id); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Get still returns it (with archived_at populated)
	got, err := st.GetAgentMemory(ctx, id)
	if err != nil {
		t.Fatalf("get after archive: %v", err)
	}
	if got == nil {
		t.Fatal("get after archive returned nil (should still find the row)")
	}
	if got.ArchivedAt == "" {
		t.Error("archived_at not set after archive")
	}

	// Idempotent — second archive call is a no-op
	if err := st.ArchiveAgentMemory(ctx, wcFor("alice", "test"), id); err != nil {
		t.Errorf("second archive: %v", err)
	}

	// List (default = exclude archived) shouldn't find it
	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Errorf("list returned archived row id=%d", id)
		}
	}

	// List (include_archived) should find it
	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list include_archived: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Error("list(include_archived=true) didn't find archived row")
	}
}

func TestAgentMemory_Archive_NotFound(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	err := st.ArchiveAgentMemory(ctx, wcFor("alice", "test"), 999999)
	if err != store.ErrNotFound {
		t.Errorf("archive of nonexistent id: got %v, want store.ErrNotFound", err)
	}
}

func TestAgentMemory_List_DefaultExcludesArchived(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	// Save 3 rows; archive 1; default list should return 2
	ids := []int64{}
	for i := 0; i < 3; i++ {
		row := makeRow("alice", "note", "row")
		id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if err := st.ArchiveAgentMemory(ctx, wcFor("alice", "test"), ids[1]); err != nil {
		t.Fatalf("archive: %v", err)
	}

	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("default list: got %d rows, want 2 (archived excluded)", len(rows))
	}
}

func TestAgentMemory_List_FilterByKind(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	for _, k := range []string{"note", "decision", "decision"} {
		row := makeRow("alice", k, "x")
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{Kind: "decision"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("filter by kind=decision: got %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Kind != "decision" {
			t.Errorf("filter leak: got kind=%q", r.Kind)
		}
	}
}

func TestAgentMemory_List_FilterByTag(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	for _, tags := range []string{"alpha,beta", "beta,gamma", "alpha"} {
		row := makeRow("alice", "note", "x")
		row.Tags = tags
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// tag=alpha should match rows 1 and 3
	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{Tag: "alpha"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("filter by tag=alpha: got %d, want 2", len(rows))
	}

	// tag=gamma should match row 2 only
	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{Tag: "gamma"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("filter by tag=gamma: got %d, want 1", len(rows))
	}
}

func TestAgentMemory_List_PinnedOnly(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		row := makeRow("alice", "note", "x")
		row.Pinned = i == 0 // pin the first one
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{PinnedOnly: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("pinned_only: got %d, want 1", len(rows))
	}
	if !rows[0].Pinned {
		t.Error("pinned_only returned an unpinned row")
	}
}

func TestAgentMemory_List_ScopedToActiveProject(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	// Save in the default project
	row := makeRow("alice", "note", "in default project")
	idDefault, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err != nil {
		t.Fatalf("save default: %v", err)
	}

	// Create a second project and switch active context
	p := fakeProject("other")
	if err := st.CreateProject(ctx, projectForCreate(p)); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := st.SetActiveProject(ctx, "other"); err != nil {
		t.Fatalf("set active to other: %v", err)
	}

	// List in 'other' project should NOT include the row from 'default'
	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{})
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	for _, r := range rows {
		if r.ID == idDefault {
			t.Errorf("INV-7 violated: row from project 'default' visible in project 'other'")
		}
	}

	// Switch back; row should be visible again
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{})
	if err != nil {
		t.Fatalf("list default: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == idDefault {
			found = true
		}
	}
	if !found {
		t.Error("row from default project should be visible after switching back")
	}
}

func TestAgentMemory_Search_BM25(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	// Three rows; one contains a unique token we'll search for.
	rows := []*agentmemory.AgentMemory{
		makeRow("alice", "note", "The quick brown fox"),
		makeRow("alice", "note", "jumps over the lazy dog"),
		makeRow("alice", "decision", "fox and dog are friends"),
	}
	for _, r := range rows {
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), r); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// Search for "fox" — should return rows 1 and 3
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "fox"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("search 'fox': got %d hits, want 2", len(hits))
	}

	// Search for "jumps" — only row 2
	hits, err = st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "jumps"})
	if err != nil {
		t.Fatalf("search jumps: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("search 'jumps': got %d hits, want 1", len(hits))
	}
}

// TestAgentMemory_Search_PorterStemming exercises the v22 porter
// unicode61 tokenizer. Morphology-equivalent words ("running" ↔ "runs")
// collapse to the same stem ("run") and both rows surface for the same
// FTS5 query.
//
// PR-1 of the v2.9.0 plan (row 160). LOW risk. Backward compat: result
// SET unchanged for unambiguous queries; only ranking differs when
// stems collide.
//
// Note on porter scope: the standard 1980 Porter stemmer collapses
// inflectional suffixes (-s, -ing, -ed, etc) but does NOT recognize
// irregular past-tense ("ran" stems to itself, not "run"). This test
// verifies the documented behavior — "runs" finds "running" and vice
// versa — not the broader irregular-verb case.
func TestAgentMemory_Search_PorterStemming(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	// Three rows:
	//   - row A: contains the inflected form "running"
	//   - row B: contains the inflected form "runs"
	//   - row C: contains the bare form "run"
	//   - row D: control, no overlap with the run*-family at all
	rows := []*agentmemory.AgentMemory{
		makeRow("alice", "note", "the running dog"),
		makeRow("alice", "note", "she runs every morning"),
		makeRow("alice", "note", "go for a run today"),
		makeRow("alice", "note", "completely unrelated content about cats"),
	}
	for i, r := range rows {
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), r); err != nil {
			t.Fatalf("save row %d: %v", i, err)
		}
	}

	// Query "runs": with porter, this stems to "run" alongside the
	// indexed stems of "running", "runs", and "run". All three run*-
	// family rows should match; the cats row should NOT (porter is
	// stem-based, not substring; "cats" stems to "cat", not "run").
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "runs"})
	if err != nil {
		t.Fatalf("search runs: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("porter: search 'runs' should match 3 run*-family rows, got %d", len(hits))
		for i, h := range hits {
			t.Logf("hit %d: id=%d content=%q", i, h.ID, h.Content)
		}
	}
	for _, h := range hits {
		if strings.Contains(h.Content, "cats") {
			t.Errorf("porter: search 'runs' should NOT match unrelated cats row (porter is stem-based, got id=%d)", h.ID)
		}
	}
}

// TestAgentMemory_Search_BaselineExact is the control: exact-form
// queries still work and still match the row containing the literal
// token. Confirms we did not regress the v2.8.0-alpha behavior on
// queries that have no morphology ambiguity.
func TestAgentMemory_Search_BaselineExact(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	rows := []*agentmemory.AgentMemory{
		makeRow("alice", "note", "running dog"),
		makeRow("alice", "note", "lazy fox"),
	}
	for i, r := range rows {
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), r); err != nil {
			t.Fatalf("save row %d: %v", i, err)
		}
	}

	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "running"})
	if err != nil {
		t.Fatalf("search running: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("baseline: search 'running' should match exactly 1 row, got %d", len(hits))
		for i, h := range hits {
			t.Logf("hit %d: id=%d content=%q", i, h.ID, h.Content)
		}
	}
	if len(hits) >= 1 && !strings.Contains(hits[0].Content, "running dog") {
		t.Errorf("baseline: search 'running' matched wrong content: %q", hits[0].Content)
	}
}

func TestAgentMemory_Search_EmptyQueryRejected(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	_, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: ""})
	if err != store.ErrInvalidArgument {
		t.Errorf("empty query: got %v, want store.ErrInvalidArgument", err)
	}
}

func TestAgentMemory_Search_ExcludesArchived(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "note", "unicorn purple rainbow")
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.ArchiveAgentMemory(ctx, wcFor("alice", "test"), id); err != nil {
		t.Fatalf("archive: %v", err)
	}

	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "unicorn"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("archived row returned in search: got %d hits, want 0", len(hits))
	}
}

func TestAgentMemory_Search_TitleField(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "note", "generic body content here")
	row.Title = "uniqtitletoken"
	if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row); err != nil {
		t.Fatalf("save: %v", err)
	}
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "uniqtitletoken"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("title field search: got %d hits, want 1", len(hits))
	}
}

func TestAgentMemory_Save_EmitsWriteAudit(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "note", "audit me")
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test_save"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// INV-1: ListWrites should include at least one agent_memory row.
	writes, err := st.ListWrites(ctx, audit.ListFilters{ProjectID: "default"})
	if err != nil {
		t.Fatalf("list writes: %v", err)
	}
	found := false
	for _, w := range writes {
		if w.TableName == "agent_memory" && w.RowID == id {
			found = true
			if w.WritePath != "test_save" {
				t.Errorf("audit write_path: got %q, want 'test_save'", w.WritePath)
			}
			break
		}
	}
	if !found {
		t.Errorf("no write_audit row found for agent_memory id=%d", id)
	}
}

// fakeProject builds a minimal Project for tests that need a
// second tenant. We avoid importing internal/project to keep the
// test surface small.
func fakeProject(id string) projectAlias {
	p := projectAlias{ProjectID: id, DisplayName: id}
	return p
}

// projectAlias mirrors the small subset of project.Project the
// store.CreateProject needs. Using a type alias avoids an import
// in test code.
type projectAlias = struct {
	ProjectID   string
	DisplayName string
	Description string
	CreatedAt   string
}

// --- PR-2 (v2.9.0-alpha): hybrid retrieval tests ---------------------
//
// These exercise the new Mode dispatch on SearchAgentMemory. The
// fixture uses the deterministic mock embedder (SHA-256-truncated
// unit vectors) so the test results do not depend on the network,
// disk, or timing.

// TestAgentMemory_Search_VectorMode_RequiresEmbedder guards the
// degrade-graceful path: Mode="vector" or Mode="rrf" without an
// active embedder returns embedder.ErrDisabled wrapped, NOT a
// generic error.
func TestAgentMemory_Search_VectorMode_RequiresEmbedder(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	// No WithEmbedder() call → embedder is None() stub.
	row := makeRow("alice", "note", "rainbow unicorn")
	if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row); err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, mode := range []string{"vector", "rrf"} {
		_, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
			Query: "unicorn",
			Mode:  mode,
		})
		if err == nil {
			t.Errorf("mode=%s: got nil err, want embedder.ErrDisabled wrapped", mode)
			continue
		}
		if !errors.Is(err, embedder.ErrDisabled) {
			t.Errorf("mode=%s: got %v, want errors.Is(embedder.ErrDisabled) true", mode, err)
		}
	}

	// BM25 mode still works without an embedder (backward compat).
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "unicorn"})
	if err != nil {
		t.Errorf("bm25 mode: %v", err)
	}
	if len(hits) == 0 {
		t.Errorf("bm25 mode: expected at least 1 hit for 'unicorn'")
	}
}

// TestAgentMemory_Search_VectorMode_RanksByCosine wires a 4-dim
// embedder and uses HAND-CRAFTED unit vectors so the cosine
// relationships are known deterministically. The mock embedder's
// SHA-256-based vectors are random between unrelated texts (dot
// product ~ 0 with high variance), which is fine for hashing-style
// behaviour but makes for flaky ordering assertions. Here we set
// the embedding directly on AgentMemory.Embedding so the test
// reflects the cosine machinery, not the embedder's quantization.
func TestAgentMemory_Search_VectorMode_Structural(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	mock, err := mockembedder.New(mockembedder.Options{Dim: 32})
	if err != nil {
		t.Fatalf("mock embedder: %v", err)
	}
	sqlSt, ok := st.(*sqlitestore.Store)
	if !ok {
		t.Fatalf("expected *sqlitestore.Store, got %T", st)
	}
	sqlSt.WithEmbedder(mock)

	// Save 3 rows, each with an embedding derived from the mock
	// embedder applied to its own content. The mock is SHA-256-based
	// so related texts share bytes, but with N=32 the noise floor is
	// much smaller than the cosine signal — still, this test is
	// STRUCTURAL: it asserts that all 3 rows surface, VectorRank is
	// populated 1..N, and BM25Rank is 0 (vector mode has no BM25 arm).
	for _, content := range []string{
		"alpha-zebra-cake", "zebra-dog-rainbow", "mars-saturn-neptune",
	} {
		row := makeRow("alice", "note", content)
		row.Embedding = embedOf(mock, content)
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row); err != nil {
			t.Fatalf("save %q: %v", content, err)
		}
	}

	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "zebra-horse-orange",
		Mode:  "vector",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("vector search: got %d hits, want 3 (every saved row has an embedding)", len(hits))
	}
	// VectorRank is 1..N; BM25Rank stays 0 (vector mode has no BM25 arm).
	for i, h := range hits {
		if h.VectorRank != i+1 {
			t.Errorf("hit[%d] %q: VectorRank=%d, want %d", i, h.Content, h.VectorRank, i+1)
		}
		if h.BM25Rank != 0 {
			t.Errorf("hit[%d] %q: BM25Rank=%d in vector mode, want 0", i, h.Content, h.BM25Rank)
		}
	}
}

// TestAgentMemory_Search_RRF_FusesBothArms exercises the RRF path
// with hand-crafted embeddings so the cross-axis behavior is
// deterministic. We seed 4 rows:
//   A: BM25 #1 (text matches), vector = orthogonal (cosine 0).
//   B: BM25 #2, vector = strongly aligned with query.
//   C: NO BM25 hit, vector = strongly aligned with query.
//   D: NO BM25 hit, vector = orthogonal.
// RRF must surface A (BM25-only), B (both), C (vector-only), D (no axis → 0).
func TestAgentMemory_Search_RRF_BothArmsContribute(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	mock, err := mockembedder.New(mockembedder.Options{Dim: 32})
	if err != nil {
		t.Fatalf("mock embedder: %v", err)
	}
	sqlSt, _ := st.(*sqlitestore.Store)
	sqlSt.WithEmbedder(mock)

	// 4 rows: 2 with "unicorn" in content (BM25 hit) + 2 without.
	// 3 rows have an embedding; row D has none. So:
	//   A "unicorn-zebra" : BM25 + vec
	//   B "unicorn-mars"  : BM25 + vec
	//   C "saturn-jupiter" : vec only (no unicorn in content)
	//   D "saturn-mars-comet" : neither axis (no embedding, no unicorn)
	rowA := withEmbed(makeRow("alice", "note", "unicorn-zebra-rainbow"), embedOf(mock, "unicorn-zebra"))
	rowB := withEmbed(makeRow("alice", "note", "unicorn-mars-xylophone"), embedOf(mock, "unicorn-mars"))
	rowC := withEmbed(makeRow("alice", "note", "saturn-jupiter-neptune"), embedOf(mock, "saturn-jupiter-neptune"))
	rowD := makeRow("alice", "note", "saturn-mars-comet") // no embedding

	for _, r := range []*agentmemory.AgentMemory{rowA, rowB, rowC, rowD} {
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), r); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "unicorn",
		Mode:  "rrf",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("rrf search: %v", err)
	}
	// Expected: 3 hits. Row D has no embedding AND no "unicorn" text →
	// invisible to both axes → not returned. (Operator invariants:
	// rrf mode never invents rows.)
	if len(hits) != 3 {
		t.Fatalf("rrf search: got %d hits, want 3 (A,B from BM25; C from vector; D excluded)",
			len(hits))
	}
	// Every returned hit has at least one axis populated (RRF invariant).
	for i, h := range hits {
		if h.BM25Rank == 0 && h.VectorRank == 0 {
			t.Errorf("hit[%d] %q: neither BM25Rank nor VectorRank populated", i, h.Content)
		}
	}
	// The BM25-armed rows A and B ("unicorn-...") must BOTH surface.
	sawA, sawB := false, false
	for _, h := range hits {
		if h.Content == "unicorn-zebra-rainbow" {
			sawA = true
			if h.BM25Rank == 0 {
				t.Errorf("row A: BM25Rank=0 in rrf mode, want >0 (text has 'unicorn')")
			}
		}
		if h.Content == "unicorn-mars-xylophone" {
			sawB = true
		}
	}
	if !sawA {
		t.Errorf("rrf: row A (BM25-arm) did not surface; hits=%v", summariseHits(hits))
	}
	if !sawB {
		t.Errorf("rrf: row B (BM25-arm) did not surface; hits=%v", summariseHits(hits))
	}
	// Row D should NEVER appear (no embedding, no "unicorn" text).
	for _, h := range hits {
		if h.Content == "saturn-mars-comet" {
			t.Errorf("rrf: row D unexpectedly surfaced; hits=%v", summariseHits(hits))
		}
	}
}

// TestAgentMemory_Embedding_RoundTrip confirms that SaveAgentMemory
// persists the embedding BLOB column and Search reads it back via
// the brute-force cosine path. (Prevents a regression where the
// encode/decode pair drift apart.)
func TestAgentMemory_Embedding_RoundTrip(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	mock, _ := mockembedder.New(mockembedder.Options{Dim: 32})
	sqlSt, _ := st.(*sqlitestore.Store)
	sqlSt.WithEmbedder(mock)

	want := embedOf(mock, "round-trip-canary-text")
	row := makeRow("alice", "note", "round-trip-canary-text")
	row.Embedding = want
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Retrieve via Get — Search needs the embedder to take the
	// vector path; a fresh row with no other neighbours would only
	// show itself at cosine 1.
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "round-trip-canary-text",
		Mode:  "vector",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("vector search: 0 hits, want >= 1 (the row we just saved)")
	}
	// The row we saved should be at rank 1 with cosine 1.0 (text == query).
	var found bool
	for _, h := range hits {
		if h.ID == id && h.Content == "round-trip-canary-text" {
			found = true
			if h.VectorRank != 1 {
				t.Errorf("round-trip: VectorRank=%d, want 1", h.VectorRank)
			}
			// Cosine of a unit vector with itself is 1.0.
			if h.Rank < 0.99 || h.Rank > 1.01 {
				t.Errorf("round-trip: Rank=%f, want ~1.0", h.Rank)
			}
			break
		}
	}
	if !found {
		t.Errorf("round-trip: saved id=%d not in hits", id)
	}
}

// embedOf is a tiny helper for tests — embedder.Embed returns one
// Vec per input, so we index [0] for the single-text case.
func embedOf(e embedder.Embedder, text string) embedder.Vec {
	vs, _ := e.Embed(context.Background(), []string{text})
	if len(vs) == 0 {
		return nil
	}
	return vs[0]
}

// withEmbed returns m with the Embedding field set. Tiny helper for
// the RRF tests that want hand-crafted unit vectors.
func withEmbed(m *agentmemory.AgentMemory, v embedder.Vec) *agentmemory.AgentMemory {
	m.Embedding = v
	return m
}

// sqrt2 = √2 ≈ 1.4142. Precomputed constant for cosine math in
// the hand-crafted vector tests. Avoids repeating math.Sqrt(2) inside
// tight tests where the value is constant.
const sqrt2 = 1.4142135623730951

// summariseHits is a debug helper for t.Errorf messages — emits a
// compact (id, content, bm25, vector, rrf) line per hit. Kept local
// to avoid going through a heavy assert-mock harness.
func summariseHits(hits []agentmemory.SearchHit) string {
	var b strings.Builder
	for i, h := range hits {
		b.WriteString("[")
		b.WriteString(strings.TrimSpace(h.Content))
		b.WriteString(" bm25=")
		b.WriteString(itoa(h.BM25Rank))
		b.WriteString(" vec=")
		b.WriteString(itoa(h.VectorRank))
		b.WriteString(" rrf=")
		b.WriteString(ftoa(h.RRFScore))
		if i < len(hits)-1 {
			b.WriteString(" | ")
		}
	}
	return b.String()
}

// itoa + ftoa: tiny local formatters to avoid pulling strconv into
// t.Errorf strings (saves allocations and reads cleaner).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
func ftoa(f float64) string {
	// 4-digit fractional precision is enough for cosine display.
	if f < 0 {
		return "-" + ftoa(-f)
	}
	whole := int(f)
	frac := int((f - float64(whole)) * 10000)
	if frac < 0 {
		frac = -frac
	}
	return itoa(whole) + "." + fmt4(frac)
}
func fmt4(i int) string {
	// Pad to 4 digits: 7 -> "0007", 425 -> "0425".
	s := []byte(itoa(i))
	for len(s) < 4 {
		s = append([]byte("0"), s...)
	}
	return string(s[len(s)-4:])
}
