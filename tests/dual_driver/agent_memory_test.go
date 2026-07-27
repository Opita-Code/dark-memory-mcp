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
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
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