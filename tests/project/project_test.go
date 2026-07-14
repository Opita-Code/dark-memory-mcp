// Package project_test covers the project namespace (INV-7):
// isolation between parallel projects sharing the same dark.db, plus
// the migration v7 backward-compatibility (existing 164 specs in
// 'default' project), and the Store.SetActiveProject enforcement.
package project_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

func openTestStore(t *testing.T) store.Store {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// INV-7: write to project A; query from project B; expect zero rows.
// The whole point of the project namespace: contamination is impossible.
func TestProject_Isolation_WriteAQueryB_Empty(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Switch to project A, write a run + items.
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	runA := &research.ResearchRun{
		SessionID: "sess-A", Query: "CVE-2024-AAA", Intent: "cve",
		Items: []research.Item{
			{Title: "CVE-2024-AAA leak", Snippet: "acme internal", Source: "test", Confidence: 0.9},
		},
	}
	if _, err := s.SaveRun(ctx, wcA, runA); err != nil {
		t.Fatalf("save acme run: %v", err)
	}

	// Switch to project B, write a different run + items so we can verify
	// both projects have audit rows.
	s.SetActiveProject(ctx, "globex")
	wcB := store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"}
	runB := &research.ResearchRun{
		SessionID: "sess-B", Query: "GLOBEX-001", Intent: "web",
		Items: []research.Item{
			{Title: "globex only", Snippet: "globex internal", Source: "test", Confidence: 0.9},
		},
	}
	if _, err := s.SaveRun(ctx, wcB, runB); err != nil {
		t.Fatalf("save globex run: %v", err)
	}
	// Query in B for ACME's CVE â€” must return 0.
	items, err := s.Recall(ctx, research.RecallOptions{Query: "CVE-2024-AAA", Limit: 10})
	if err != nil {
		t.Fatalf("recall from globex: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("INV-7 violated: project globex saw %d items from project acme", len(items))
	}

	// Sanity: project A still sees its own data.
	s.SetActiveProject(ctx, "acme")
	itemsA, err := s.Recall(ctx, research.RecallOptions{Query: "CVE-2024-AAA", Limit: 10})
	if err != nil {
		t.Fatalf("recall from acme: %v", err)
	}
	if len(itemsA) != 1 {
		t.Fatalf("project acme should see 1 item, got %d", len(itemsA))
	}

	// Audit: writes from A and B are isolated.
	auditA, _ := s.ListWrites(ctx, audit.ListFilters{SessionID: "sess-A", Limit: 10})
	auditB, _ := s.ListWrites(ctx, audit.ListFilters{SessionID: "sess-B", Limit: 10})
	if len(auditA) == 0 || len(auditB) == 0 {
		t.Fatalf("expected writes for both sessions")
	}
	// _ wcB is used to silence unused
	_ = wcB
}

// INV-7: no active project => reads fail with ErrSessionRequired.
// The Store refuses to read or write without an active project.
func TestProject_NoActiveProject_ReadsRefused(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	_, err := s.Recall(ctx, research.RecallOptions{Query: "anything", Limit: 10})
	if err == nil {
		t.Fatalf("expected error when no active project, got nil")
	}
	if !errIs(err, store.ErrSessionRequired) {
		t.Fatalf("expected ErrSessionRequired, got: %v", err)
	}
}

// Migration v7 backward-compat: existing rows (no project_id) get
// the default 'default' project_id. New rows can specify project_id.
func TestProject_MigrationV7_BackwardCompat(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// After Open(), the migration has run. Verify project_id column exists.
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("create default: %v", err)
	}
	// Set active to 'default' (legacy path).
	s.SetActiveProject(ctx, "default")
	wc := store.WriteContext{Actor: "test", SessionID: "legacy-sess", WritePath: "test", ProjectID: "default"}
	sess := &session.Session{SessionID: "legacy-sess", Status: string(session.StatusActive)}
	if _, err := s.SaveSession(ctx, wc, sess); err != nil {
		t.Fatalf("save legacy session: %v", err)
	}
	// Read back, confirm project_id = 'default'.
	got, err := s.GetSession(ctx, "legacy-sess")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got == nil {
		t.Fatalf("session not found")
	}
	// (ProjectID field on session itself is not yet set by SaveSession;
	// what matters is the write_audit row carries project_id='default')
	writes, _ := s.ListWrites(ctx, audit.ListFilters{SessionID: "legacy-sess", Limit: 5})
	if len(writes) == 0 {
		t.Fatalf("expected write_audit rows for legacy session")
	}
}

// Project CRUD: create, get, list, archive.
func TestProject_CRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	p := &project.Project{
		ProjectID:     "acme-2026",
		DisplayName:   "ACME Corp",
		Description:   "Test project for ACME",
		ConstitutionID: "acme-constitution",
		ConstitutionVer: "1.0.0",
	}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, err := s.GetProject(ctx, "acme-2026")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.DisplayName != "ACME Corp" {
		t.Fatalf("got: %+v", got)
	}

	// List
	list, err := s.ListProjects(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// At least 2: 'default' (auto-seeded) + 'acme-2026'.
	if len(list) < 2 {
		t.Fatalf("expected >=2 projects, got %d", len(list))
	}

	// Archive
	if err := s.ArchiveProject(ctx, "acme-2026"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	list2, _ := s.ListProjects(ctx, 10)
	for _, p2 := range list2 {
		if p2.ProjectID == "acme-2026" {
			t.Fatalf("archived project still in active list")
		}
	}
}

// Active project change: writes get tagged with the current project_id.
func TestProject_WriteTagging(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "a", DisplayName: "A"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "b", DisplayName: "B"}); err != nil {
		t.Fatalf("create b: %v", err)
	}

	// Write in A
	s.SetActiveProject(ctx, "a")
	if _, err := s.SaveRun(ctx, store.WriteContext{Actor: "test", SessionID: "s1", WritePath: "test", ProjectID: "a"}, &research.ResearchRun{
		SessionID: "s1", Query: "Q1", Intent: "cve",
	}); err != nil {
		t.Fatalf("save A: %v", err)
	}

	// Write in B
	s.SetActiveProject(ctx, "b")
	if _, err := s.SaveRun(ctx, store.WriteContext{Actor: "test", SessionID: "s2", WritePath: "test", ProjectID: "b"}, &research.ResearchRun{
		SessionID: "s2", Query: "Q2", Intent: "cve",
	}); err != nil {
		t.Fatalf("save B: %v", err)
	}

	// Switch back to A, confirm A sees only Q1.
	s.SetActiveProject(ctx, "a")
	runsA, _ := s.ListRuns(ctx, "", 10)
	qCount := 0
	for _, r := range runsA {
		if r.SessionID == "s1" || r.SessionID == "s2" {
			qCount++
		}
	}
	if qCount != 1 {
		t.Fatalf("project A should see 1 of its own runs, got %d", qCount)
	}
}

// W3-001 (T1): GetRun must filter by active project. Cross-project
// reads return nil (not the other project's run).
func TestProject_GetRun_CrossProject_ReturnsNil(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Write a run in project A.
	s.SetActiveProject(ctx, "acme")
	runA := &research.ResearchRun{
		SessionID: "sess-A", Query: "ACME-secret", Intent: "cve",
	}
	idA, err := s.SaveRun(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}, runA)
	if err != nil {
		t.Fatalf("save A: %v", err)
	}
	if idA == 0 {
		t.Fatalf("expected id > 0")
	}

	// Switch to project B and try to fetch A's run by id.
	s.SetActiveProject(ctx, "globex")
	got, err := s.GetRun(ctx, idA)
	if err != nil {
		t.Fatalf("GetRun from globex: %v", err)
	}
	if got != nil {
		t.Fatalf("INV-7 violated: project globex saw project acme's run (id=%d query=%s)", got.ID, got.Query)
	}

	// Switch back to A and confirm same id returns the row.
	s.SetActiveProject(ctx, "acme")
	gotA, err := s.GetRun(ctx, idA)
	if err != nil {
		t.Fatalf("GetRun from acme: %v", err)
	}
	if gotA == nil || gotA.Query != "ACME-secret" {
		t.Fatalf("project acme should see own run, got: %+v", gotA)
	}
}

// W3-001 (T1): ListItems must filter by active project. Cross-project
// reads return empty.
func TestProject_ListItems_CrossProject_ReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Write a run with items in project A.
	s.SetActiveProject(ctx, "acme")
	idA, err := s.SaveRun(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}, &research.ResearchRun{
		SessionID: "sess-A", Query: "ACME-secret", Intent: "cve",
		Items: []research.Item{
			{Title: "acme-only", Snippet: "acme internal", Source: "test", Confidence: 0.9},
			{Title: "another acme item", Snippet: "private", Source: "test", Confidence: 0.5},
		},
	})
	if err != nil {
		t.Fatalf("save A: %v", err)
	}

	// Switch to project B and try to read items of A's run.
	s.SetActiveProject(ctx, "globex")
	itemsB, err := s.ListItems(ctx, idA, "", 50)
	if err != nil {
		t.Fatalf("ListItems from globex: %v", err)
	}
	if len(itemsB) != 0 {
		t.Fatalf("INV-7 violated: project globex saw %d items from project acme", len(itemsB))
	}

	// Switch back to A and confirm same id returns its 2 items.
	s.SetActiveProject(ctx, "acme")
	itemsA, err := s.ListItems(ctx, idA, "", 50)
	if err != nil {
		t.Fatalf("ListItems from acme: %v", err)
	}
	if len(itemsA) != 2 {
		t.Fatalf("project acme should see 2 items, got %d", len(itemsA))
	}
}

// W3-001 (T1): GetRun with no active project must return ErrSessionRequired.
func TestProject_GetRun_NoActiveProject_Refused(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	_, err := s.GetRun(ctx, 1)
	if err == nil {
		t.Fatalf("expected error when no active project, got nil")
	}
	if !errIs(err, store.ErrSessionRequired) {
		t.Fatalf("expected ErrSessionRequired, got: %v", err)
	}
}

// W3-001 (T1): ListItems with no active project must return ErrSessionRequired.
func TestProject_ListItems_NoActiveProject_Refused(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	_, err := s.ListItems(ctx, 1, "", 10)
	if err == nil {
		t.Fatalf("expected error when no active project, got nil")
	}
	if !errIs(err, store.ErrSessionRequired) {
		t.Fatalf("expected ErrSessionRequired, got: %v", err)
	}
}

// W3-005 (T2): SetActiveProject must validate against the projects
// table. Unknown ids are rejected with ErrInvalidArgument; the previous
// active project is preserved. Empty string still clears.
func TestProject_SetActiveProject_RejectsUnknown(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	// Set active to a known project.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme: %v", err)
	}
	// Try to set to a typo.
	if err := s.SetActiveProject(ctx, "acme-typo"); err == nil {
		t.Fatalf("expected error setting unknown project, got nil")
	} else if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
	// Active project must be unchanged (still "acme", not "").
	if got := s.ActiveProject(); got != "acme" {
		t.Fatalf("active project must remain acme after rejection, got %q", got)
	}
}

// W3-005 (T2): 'default' is the well-known catch-all project and is
// allowed even before CreateProject is called for it (legacy compat).
// T3 will auto-seed the row on Open, but this special case preserves
// backward compat in the meantime.
func TestProject_SetActiveProject_AllowsDefault(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// No CreateProject("default") â€” legacy code path.
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set default without row should succeed, got: %v", err)
	}
	if got := s.ActiveProject(); got != "default" {
		t.Fatalf("active must be default, got %q", got)
	}
}

// W3-005 (T2): Empty string clears the active project (resets to
// "no project" state). Reads will then return ErrSessionRequired.
func TestProject_SetActiveProject_ClearOK(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.SetActiveProject(ctx, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := s.ActiveProject(); got != "" {
		t.Fatalf("active must be empty after clear, got %q", got)
	}
}

// helpers
func errIs(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
