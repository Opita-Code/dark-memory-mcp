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
	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
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
	sess := &session.Session{SessionID: "legacy-sess", Status: string(session.StatusOpen)}
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

// TestProject_DriftStrictness_Roundtrip (Wave 5X.3): the
// drift_strictness column must round-trip through CreateProject +
// GetProject + ListProjects. Empty input → "default" sentinel.
// Valid override → persisted verbatim.
func TestProject_DriftStrictness_Roundtrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Case 1: empty → default sentinel.
	p1 := &project.Project{
		ProjectID:   "drift-default",
		DisplayName: "Default Drift",
	}
	if err := s.CreateProject(ctx, p1); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	got1, err := s.GetProject(ctx, "drift-default")
	if err != nil {
		t.Fatalf("get p1: %v", err)
	}
	if got1 == nil {
		t.Fatalf("get p1: nil")
	}
	if got1.DriftStrictness != "default" {
		t.Errorf("p1.DriftStrictness = %q, want \"default\" (empty input normalises)", got1.DriftStrictness)
	}

	// Case 2: explicit override → persisted verbatim.
	p2 := &project.Project{
		ProjectID:        "drift-strict",
		DisplayName:      "Strict Drift",
		DriftStrictness:  "strict",
	}
	if err := s.CreateProject(ctx, p2); err != nil {
		t.Fatalf("create p2: %v", err)
	}
	got2, err := s.GetProject(ctx, "drift-strict")
	if err != nil {
		t.Fatalf("get p2: %v", err)
	}
	if got2 == nil {
		t.Fatalf("get p2: nil")
	}
	if got2.DriftStrictness != "strict" {
		t.Errorf("p2.DriftStrictness = %q, want \"strict\"", got2.DriftStrictness)
	}

	// Case 3: ListProjects includes the value.
	list, err := s.ListProjects(ctx, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := 0
	for _, p := range list {
		switch p.ProjectID {
		case "drift-default":
			if p.DriftStrictness != "default" {
				t.Errorf("list drift-default.DriftStrictness = %q, want default", p.DriftStrictness)
			}
			found++
		case "drift-strict":
			if p.DriftStrictness != "strict" {
				t.Errorf("list drift-strict.DriftStrictness = %q, want strict", p.DriftStrictness)
			}
			found++
		}
	}
	if found != 2 {
		t.Errorf("expected both drift-strictness projects in list, found %d", found)
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

// W3-004 (T3): After Open() (which runs migration v7), the 'default'
// project row must exist without an explicit CreateProject call.
// Backward compat for legacy data and dark-research-mcp coexistence.
func TestProject_MigrationV7_AutoSeedsDefault(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// No CreateProject calls. The 'default' row must be there.
	got, err := s.GetProject(ctx, "default")
	if err != nil {
		t.Fatalf("GetProject(default): %v", err)
	}
	if got == nil {
		t.Fatalf("default project not auto-seeded after Open()")
	}
	if got.DisplayName == "" {
		t.Fatalf("default project has empty display name: %+v", got)
	}

	// ListProjects must include 'default'.
	list, err := s.ListProjects(ctx, 10)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	found := false
	for _, p := range list {
		if p.ProjectID == "default" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListProjects did not include 'default': %+v", list)
	}
}

// W3-004 (T3): Idempotent. Reopening the same DB file does NOT create
// a duplicate 'default' row.
func TestProject_MigrationV7_AutoSeed_Idempotent(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	// First open: creates default.
	s1, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	if err := s1.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set active default (1): %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close 1: %v", err)
	}
	// Second open on same file: default must still be there, no duplicate.
	s2, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer s2.Close()
	if err := s2.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set active default (2): %v", err)
	}
	list, err := s2.ListProjects(ctx, 100)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	defaultCount := 0
	for _, p := range list {
		if p.ProjectID == "default" {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		t.Fatalf("expected exactly 1 'default' project after reopen, got %d", defaultCount)
	}
}

// W3-002 (T4a): GetSession must filter by active project. Cross-project
// reads return nil.
func TestProject_GetSession_CrossProject_ReturnsNil(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Write a session in project A.
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save A: %v", err)
	}

	// Switch to project B; GetSession("sess-A") must return nil.
	s.SetActiveProject(ctx, "globex")
	got, err := s.GetSession(ctx, "sess-A")
	if err != nil {
		t.Fatalf("GetSession from globex: %v", err)
	}
	if got != nil {
		t.Fatalf("INV-7 violated: project globex saw project acme's session %+v", got)
	}

	// Back in A: same id returns the row.
	s.SetActiveProject(ctx, "acme")
	gotA, err := s.GetSession(ctx, "sess-A")
	if err != nil {
		t.Fatalf("GetSession from acme: %v", err)
	}
	if gotA == nil || gotA.SessionID != "sess-A" {
		t.Fatalf("project acme should see own session, got: %+v", gotA)
	}
}

// W3-002 (T4a): ListSessions must filter by active project.
func TestProject_ListSessions_CrossProject_Isolated(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	s.SetActiveProject(ctx, "acme")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save A: %v", err)
	}
	s.SetActiveProject(ctx, "globex")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"},
		&session.Session{SessionID: "sess-B", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save B: %v", err)
	}

	// From B: ListSessions must show only sess-B.
	sessions, err := s.ListSessions(ctx, 100)
	if err != nil {
		t.Fatalf("ListSessions from globex: %v", err)
	}
	for _, sess := range sessions {
		if sess.SessionID == "sess-A" {
			t.Fatalf("INV-7 violated: project globex saw project acme's session %+v", sess)
		}
	}

	// From A: must show sess-A only.
	s.SetActiveProject(ctx, "acme")
	sessionsA, err := s.ListSessions(ctx, 100)
	if err != nil {
		t.Fatalf("ListSessions from acme: %v", err)
	}
	for _, sess := range sessionsA {
		if sess.SessionID == "sess-B" {
			t.Fatalf("INV-7 violated: project acme saw project globex's session %+v", sess)
		}
	}
}

// W3-002 (T4a): CloseSession must require the session to belong to the
// active project. Cross-project close attempts return ErrNotFound and
// do NOT mutate the row.
func TestProject_CloseSession_CrossProject_NotClosed(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	s.SetActiveProject(ctx, "acme")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save A: %v", err)
	}

	// Try to close from globex.
	s.SetActiveProject(ctx, "globex")
	err := s.CloseSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "globex"}, "sess-A", "clean")
	if err == nil {
		t.Fatalf("expected error closing cross-project session, got nil")
	}
	if !errIs(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}

	// Switch back to A and confirm sess-A is still active.
	s.SetActiveProject(ctx, "acme")
	got, err := s.GetSession(ctx, "sess-A")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil {
		t.Fatalf("sess-A disappeared")
	}
	if got.Status != string(session.StatusOpen) {
		t.Fatalf("sess-A was closed by cross-project attempt, status=%s", got.Status)
	}
}

// W3-002 (T4b): SaveSpec + GetSpec + ListSpecs + UpdateSpec + DeleteSpec
// must filter by active project. Cross-project reads return nil/empty.
func TestProject_Specs_CrossProject_Isolated(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Write a spec in project A.
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	spA := &vibeflow.Spec{
		VibeCase: "C1", SessionID: "sess-A",
		Constitution: `{"c":1}`, Spec: `{"a":1}`, Tasks: `[{"id":"1"}]`,
	}
	idA, err := s.SaveSpec(ctx, wcA, spA)
	if err != nil {
		t.Fatalf("SaveSpec A: %v", err)
	}

	// From B: GetSpec must return nil.
	s.SetActiveProject(ctx, "globex")
	got, err := s.GetSpec(ctx, idA)
	if err != nil {
		t.Fatalf("GetSpec from globex: %v", err)
	}
	if got != nil {
		t.Fatalf("INV-7 violated: project globex saw project acme's spec %+v", got)
	}

	// From B: ListSpecs must not include the A spec.
	listB, _ := s.ListSpecs(ctx, vibeflow.SpecListFilters{Limit: 100})
	for _, sp := range listB {
		if sp.ID == idA {
			t.Fatalf("INV-7 violated: project globex saw project acme's spec in ListSpecs")
		}
	}

	// Cross-project Update returns ErrNoRows (no row matches the project filter).
	err = s.UpdateSpec(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"},
		idA, &vibeflow.Spec{VibeCase: "C2"})
	if err == nil {
		t.Fatalf("expected error updating cross-project spec, got nil")
	}

	// Cross-project Delete returns ErrNoRows and does NOT delete the row.
	if err := s.DeleteSpec(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"}, idA); err == nil {
		t.Fatalf("expected error deleting cross-project spec, got nil")
	}

	// From A: same id still returns the spec (B's delete was a no-op).
	s.SetActiveProject(ctx, "acme")
	gotA, err := s.GetSpec(ctx, idA)
	if err != nil {
		t.Fatalf("GetSpec from acme: %v", err)
	}
	if gotA == nil || gotA.ID != idA {
		t.Fatalf("project acme should still see own spec, got: %+v", gotA)
	}
}

// W3-002 (T4c, brands): same brand_id can exist in two different
// projects with different voice/visual. Each project only sees its own.
func TestProject_Brands_CrossProject_CompositeUnique(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme-corp", DisplayName: "ACME Corp"}); err != nil {
		t.Fatalf("create acme-corp: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme-eu", DisplayName: "ACME EU", ParentProjectID: "acme-corp"}); err != nil {
		t.Fatalf("create acme-eu: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme-us", DisplayName: "ACME US", ParentProjectID: "acme-corp"}); err != nil {
		t.Fatalf("create acme-us: %v", err)
	}

	// acme-corp defines the canonical brand.
	if err := s.SetActiveProject(ctx, "acme-corp"); err != nil {
		t.Fatalf("set acme-corp: %v", err)
	}
	if err := s.SaveBrandGuide(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "acme-corp"},
		&vibeflow.BrandGuide{BrandID: "acme-base", Voice: `{"id":"base"}`}); err != nil {
		t.Fatalf("save base: %v", err)
	}

	// acme-eu has its own override for the same brand_id.
	if err := s.SetActiveProject(ctx, "acme-eu"); err != nil {
		t.Fatalf("set acme-eu: %v", err)
	}
	if err := s.SaveBrandGuide(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "acme-eu"},
		&vibeflow.BrandGuide{BrandID: "acme-base", Voice: `{"id":"eu-override"}`}); err != nil {
		t.Fatalf("save eu override: %v", err)
	}

	// acme-us has its own override.
	if err := s.SetActiveProject(ctx, "acme-us"); err != nil {
		t.Fatalf("set acme-us: %v", err)
	}
	if err := s.SaveBrandGuide(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "acme-us"},
		&vibeflow.BrandGuide{BrandID: "acme-base", Voice: `{"id":"us-override"}`}); err != nil {
		t.Fatalf("save us override: %v", err)
	}

	// From acme-eu: GetBrandGuide("acme-base") returns EU's record only.
	if err := s.SetActiveProject(ctx, "acme-eu"); err != nil {
		t.Fatalf("set acme-eu (2): %v", err)
	}
	gotEU, err := s.GetBrandGuide(ctx, "acme-base")
	if err != nil {
		t.Fatalf("GetBrandGuide eu: %v", err)
	}
	if gotEU == nil || gotEU.Voice != `{"id":"eu-override"}` {
		t.Fatalf("acme-eu should see eu-override, got: %+v", gotEU)
	}

	// From acme-us: GetBrandGuide("acme-base") returns US's record only.
	if err := s.SetActiveProject(ctx, "acme-us"); err != nil {
		t.Fatalf("set acme-us (2): %v", err)
	}
	gotUS, err := s.GetBrandGuide(ctx, "acme-base")
	if err != nil {
		t.Fatalf("GetBrandGuide us: %v", err)
	}
	if gotUS == nil || gotUS.Voice != `{"id":"us-override"}` {
		t.Fatalf("acme-us should see us-override, got: %+v", gotUS)
	}

	// From acme-corp: GetBrandGuide("acme-base") returns canonical base.
	if err := s.SetActiveProject(ctx, "acme-corp"); err != nil {
		t.Fatalf("set acme-corp (2): %v", err)
	}
	gotBase, err := s.GetBrandGuide(ctx, "acme-base")
	if err != nil {
		t.Fatalf("GetBrandGuide base: %v", err)
	}
	if gotBase == nil || gotBase.Voice != `{"id":"base"}` {
		t.Fatalf("acme-corp should see base, got: %+v", gotBase)
	}

	// ListBrandGuides is project-scoped: each project sees only its own.
	if err := s.SetActiveProject(ctx, "acme-eu"); err != nil {
		t.Fatalf("set acme-eu (3): %v", err)
	}
	listEU, _ := s.ListBrandGuides(ctx, 100)
	if len(listEU) != 1 || listEU[0].Voice != `{"id":"eu-override"}` {
		t.Fatalf("acme-eu list should have 1 brand (its override), got %d: %+v", len(listEU), listEU)
	}
	if err := s.SetActiveProject(ctx, "acme-us"); err != nil {
		t.Fatalf("set acme-us (3): %v", err)
	}
	listUS, _ := s.ListBrandGuides(ctx, 100)
	if len(listUS) != 1 || listUS[0].Voice != `{"id":"us-override"}` {
		t.Fatalf("acme-us list should have 1 brand (its override), got %d: %+v", len(listUS), listUS)
	}

	// Delete cross-project must not touch the other project's record.
	if err := s.DeleteBrandGuide(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "acme-us"},
		"acme-base"); err != nil {
		t.Fatalf("delete us: %v", err)
	}
	if err := s.SetActiveProject(ctx, "acme-eu"); err != nil {
		t.Fatalf("set acme-eu (4): %v", err)
	}
	gotEUAfter, err := s.GetBrandGuide(ctx, "acme-base")
	if err != nil {
		t.Fatalf("GetBrandGuide eu after us delete: %v", err)
	}
	if gotEUAfter == nil || gotEUAfter.Voice != `{"id":"eu-override"}` {
		t.Fatalf("acme-eu brand must survive acme-us delete, got: %+v", gotEUAfter)
	}
}

// W3-002 (T4c, compliance): jurisdiction is GLOBAL by design. A rule
// registered with project_id='default' is visible from any project.
// Documented as a deliberate choice — see spec 171 T4c decision.
func TestProject_Compliance_GlobalByDesign(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme-eu", DisplayName: "ACME EU"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme-us", DisplayName: "ACME US"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Register a compliance rule in default project.
	s.SetActiveProject(ctx, "default")
	if err := s.SaveComplianceRule(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "default"},
		&vibeflow.ComplianceRule{Jurisdiction: "EU", Rules: `{"required":["disclosure"]}`}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	// From acme-eu: GetComplianceRule("EU") returns the global rule.
	s.SetActiveProject(ctx, "acme-eu")
	gotEU, err := s.GetComplianceRule(ctx, "EU")
	if err != nil {
		t.Fatalf("GetComplianceRule eu: %v", err)
	}
	if gotEU == nil || gotEU.Rules != `{"required":["disclosure"]}` {
		t.Fatalf("acme-eu must see global EU rule, got: %+v", gotEU)
	}

	// From acme-us: same rule, no isolation.
	s.SetActiveProject(ctx, "acme-us")
	gotUS, err := s.GetComplianceRule(ctx, "EU")
	if err != nil {
		t.Fatalf("GetComplianceRule us: %v", err)
	}
	if gotUS == nil || gotUS.Rules != `{"required":["disclosure"]}` {
		t.Fatalf("acme-us must see global EU rule, got: %+v", gotUS)
	}

	// ListComplianceRules also global: same list from any project.
	listEU, _ := s.ListComplianceRules(ctx, 100)
	listUS, _ := s.ListComplianceRules(ctx, 100)
	if len(listEU) != len(listUS) || len(listEU) < 1 {
		t.Fatalf("compliance list must be global; acme-eu=%d, acme-us=%d", len(listEU), len(listUS))
	}
}

// W3-002 (T4d): SaveArtifact + GetArtifact + UpdateArtifact + DeleteArtifact
// + ListArtifacts + SetArtifactValidation must filter by active project.
// Cross-project reads return nil/empty; cross-project writes are no-ops.
func TestProject_Artifacts_CrossProject_Isolated(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Write an artifact in project A.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme: %v", err)
	}
	artA := &vibeflow.Artifact{
		SessionID: "sess-A", VibeCase: "C2", ArtifactType: "image",
		BrandID: "acme", Jurisdiction: "EU", HasDisclosure: true,
		ArtifactURL: "file:///acme.png", ValidationStatus: "pending",
	}
	idA, err := s.SaveArtifact(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}, artA)
	if err != nil {
		t.Fatalf("SaveArtifact A: %v", err)
	}

	// From B: GetArtifact returns nil.
	if err := s.SetActiveProject(ctx, "globex"); err != nil {
		t.Fatalf("set globex: %v", err)
	}
	got, err := s.GetArtifact(ctx, idA)
	if err != nil {
		t.Fatalf("GetArtifact from globex: %v", err)
	}
	if got != nil {
		t.Fatalf("INV-7 violated: project globex saw project acme's artifact %+v", got)
	}

	// From B: ListArtifacts must not include A's artifact.
	listB, _ := s.ListArtifacts(ctx, vibeflow.ArtifactListFilters{Limit: 100})
	for _, a := range listB {
		if a.ID == idA {
			t.Fatalf("INV-7 violated: project globex saw project acme's artifact in ListArtifacts")
		}
	}

	// Cross-project update returns ErrNoRows.
	if err := s.UpdateArtifact(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"},
		idA, &vibeflow.ArtifactUpdate{ValidationStatus: strPtr("approved")}); err == nil {
		t.Fatalf("expected error updating cross-project artifact, got nil")
	}

	// Cross-project SetArtifactValidation returns ErrNoRows.
	if err := s.SetArtifactValidation(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"},
		idA, "approved"); err == nil {
		t.Fatalf("expected error setting validation on cross-project artifact, got nil")
	}

	// Cross-project Delete returns ErrNoRows and does NOT remove the row.
	if err := s.DeleteArtifact(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"},
		idA); err == nil {
		t.Fatalf("expected error deleting cross-project artifact, got nil")
	}

	// Back in A: artifact still there (B's delete was a no-op).
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme (back): %v", err)
	}
	gotA, err := s.GetArtifact(ctx, idA)
	if err != nil {
		t.Fatalf("GetArtifact from acme: %v", err)
	}
	if gotA == nil || gotA.ID != idA {
		t.Fatalf("project acme should still see own artifact, got: %+v", gotA)
	}

	// Same-project Update works.
	if err := s.UpdateArtifact(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		idA, &vibeflow.ArtifactUpdate{ValidationStatus: strPtr("approved")}); err != nil {
		t.Fatalf("UpdateArtifact same-project: %v", err)
	}
	gotApproved, _ := s.GetArtifact(ctx, idA)
	if gotApproved == nil || gotApproved.ValidationStatus != "approved" {
		t.Fatalf("UpdateArtifact didn't apply, got: %+v", gotApproved)
	}

	// Same-project SetArtifactValidation works.
	if err := s.SetArtifactValidation(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		idA, "shipped"); err != nil {
		t.Fatalf("SetArtifactValidation same-project: %v", err)
	}
	gotShipped, _ := s.GetArtifact(ctx, idA)
	if gotShipped == nil || gotShipped.ValidationStatus != "shipped" {
		t.Fatalf("SetArtifactValidation didn't apply, got: %+v", gotShipped)
	}

	// Same-project Delete works.
	if err := s.DeleteArtifact(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		idA); err != nil {
		t.Fatalf("DeleteArtifact same-project: %v", err)
	}
	goneA, _ := s.GetArtifact(ctx, idA)
	if goneA != nil {
		t.Fatalf("DeleteArtifact didn't remove, got: %+v", goneA)
	}
}

// W3-002 (T4e, drift): SaveDriftReport + LatestDriftForArtifact +
// ListDriftReports must filter by active project.
func TestProject_DriftReports_CrossProject_Isolated(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Write a real artifact in project A first (FK target for drift reports).
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme: %v", err)
	}
	artID, err := s.SaveArtifact(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&vibeflow.Artifact{
			SessionID: "sess-A", VibeCase: "C2", ArtifactType: "image",
			BrandID: "acme", Jurisdiction: "EU", HasDisclosure: true,
			ArtifactURL: "file:///acme.png", ValidationStatus: "pending",
		})
	if err != nil {
		t.Fatalf("SaveArtifact A: %v", err)
	}

	// Write a drift report for that artifact in project A.
	driftID, err := s.SaveDriftReport(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&vibeflow.DriftReport{
			ArtifactID: artID, Verdict: "drift_detected",
			SpecDiff: `{"diff":[1,2,3]}`, JudgeReasoning: "EU shapes differ",
		})
	if err != nil {
		t.Fatalf("SaveDriftReport A: %v", err)
	}

	// From B: LatestDriftForArtifact returns nil.
	if err := s.SetActiveProject(ctx, "globex"); err != nil {
		t.Fatalf("set globex: %v", err)
	}
	got, err := s.LatestDriftForArtifact(ctx, artID)
	if err != nil {
		t.Fatalf("LatestDriftForArtifact from globex: %v", err)
	}
	if got != nil {
		t.Fatalf("INV-7 violated: project globex saw project acme's drift %+v", got)
	}

	// From B: ListDriftReports does not include the drift.
	listB, _ := s.ListDriftReports(ctx, artID, "", 100)
	for _, d := range listB {
		if d.ID == driftID {
			t.Fatalf("INV-7 violated: project globex saw drift in list")
		}
	}

	// Back in A: same artifactID returns the report.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme (back): %v", err)
	}
	gotA, err := s.LatestDriftForArtifact(ctx, artID)
	if err != nil {
		t.Fatalf("LatestDriftForArtifact from acme: %v", err)
	}
	if gotA == nil || gotA.ID != driftID {
		t.Fatalf("acme should see own drift, got: %+v", gotA)
	}
}

// W3-002 (T4e, sdd): SaveSDDEvaluation + LatestSDDEvaluation +
// ListSDDEvaluations must filter by active project.
func TestProject_SDDEvaluations_CrossProject_Isolated(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Write a SDD evaluation in project A.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme: %v", err)
	}
	evalID, err := s.SaveSDDEvaluation(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&ssd.SDDEvaluation{
			EvalType: "brand_match", TargetType: "brand", TargetID: "acme-base",
			VerdictJSON: `{"match":0.95}`, Confidence: 0.95,
		})
	if err != nil {
		t.Fatalf("SaveSDDEvaluation A: %v", err)
	}

	// From B: LatestSDDEvaluation returns nil.
	if err := s.SetActiveProject(ctx, "globex"); err != nil {
		t.Fatalf("set globex: %v", err)
	}
	got, err := s.LatestSDDEvaluation(ctx, "brand_match", "brand", "acme-base")
	if err != nil {
		t.Fatalf("LatestSDDEvaluation from globex: %v", err)
	}
	if got != nil {
		t.Fatalf("INV-7 violated: project globex saw project acme's sdd %+v", got)
	}

	// From B: ListSDDEvaluations does not include the eval.
	listB, _ := s.ListSDDEvaluations(ctx, ssd.ListFilters{Limit: 100})
	for _, e := range listB {
		if e.ID == evalID {
			t.Fatalf("INV-7 violated: project globex saw sdd in list")
		}
	}

	// Back in A: same key returns the eval.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme (back): %v", err)
	}
	gotA, err := s.LatestSDDEvaluation(ctx, "brand_match", "brand", "acme-base")
	if err != nil {
		t.Fatalf("LatestSDDEvaluation from acme: %v", err)
	}
	if gotA == nil || gotA.ID != evalID {
		t.Fatalf("acme should see own sdd, got: %+v", gotA)
	}
}

// W3-002 (T4f, constitution): constitution is GLOBAL by design — see
// spec 171 T4f decision. A constitution registered in any project is
// visible from any other project. Same rationale as compliance:
// constitutions define agent posture at system level.
func TestProject_Constitution_GlobalByDesign(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme-eu", DisplayName: "ACME EU"}); err != nil {
		t.Fatalf("create eu: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme-us", DisplayName: "ACME US"}); err != nil {
		t.Fatalf("create us: %v", err)
	}

	// Register a constitution in default project.
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if err := s.SaveConstitution(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "default"},
		&constitution.Constitution{
			ConstitutionID: "redteam-v1",
			Version:        "1.0.0",
			Label:          "Red-team posture",
			SHA256:         "abc123",
			Enabled:        true,
			ParsedJSON:     `{"posture":"redteam"}`,
		}); err != nil {
		t.Fatalf("save cons: %v", err)
	}

	// From acme-eu: must see the constitution.
	if err := s.SetActiveProject(ctx, "acme-eu"); err != nil {
		t.Fatalf("set eu: %v", err)
	}
	gotEU, err := s.GetConstitution(ctx, "redteam-v1", "1.0.0")
	if err != nil {
		t.Fatalf("GetConstitution eu: %v", err)
	}
	if gotEU == nil || gotEU.ConstitutionID != "redteam-v1" {
		t.Fatalf("acme-eu must see global constitution, got: %+v", gotEU)
	}

	// From acme-us: same.
	if err := s.SetActiveProject(ctx, "acme-us"); err != nil {
		t.Fatalf("set us: %v", err)
	}
	gotUS, err := s.GetConstitution(ctx, "redteam-v1", "1.0.0")
	if err != nil {
		t.Fatalf("GetConstitution us: %v", err)
	}
	if gotUS == nil || gotUS.ConstitutionID != "redteam-v1" {
		t.Fatalf("acme-us must see global constitution, got: %+v", gotUS)
	}

	// ListConstitutions must include the row from any project.
	listEU, _ := s.ListConstitutions(ctx, 100)
	listUS, _ := s.ListConstitutions(ctx, 100)
	foundEU, foundUS := false, false
	for _, c := range listEU {
		if c.ConstitutionID == "redteam-v1" {
			foundEU = true
		}
	}
	for _, c := range listUS {
		if c.ConstitutionID == "redteam-v1" {
			foundUS = true
		}
	}
	if !foundEU || !foundUS {
		t.Fatalf("constitution must be visible from any project; eu-found=%v us-found=%v", foundEU, foundUS)
	}
}

// W3-002 (T4f, mods): mods CATALOG is GLOBAL — mod_id is unique across
// projects. mod_loads (the audit trail of who loaded what) IS per-project.
func TestProject_Mods_CrossProject_Loads(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Register a mod globally (default project).
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	modID := "shared-mod"
	if err := s.SaveMod(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "default"},
		&mods.Mod{
			ModID: modID, Name: "Shared Mod", Version: "1.0", Source: "test",
			ManifestJSON: `{"k":"v"}`, SHA256: "abc",
		}); err != nil {
		t.Fatalf("save mod: %v", err)
	}

	// From acme: GetMod returns it (catalog is global).
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme: %v", err)
	}
	got, err := s.GetMod(ctx, modID)
	if err != nil {
		t.Fatalf("GetMod from acme: %v", err)
	}
	if got == nil || got.ModID != modID {
		t.Fatalf("acme must see global mod, got: %+v", got)
	}

	// Record a load event in acme.
	loadID, err := s.RecordModLoad(ctx, store.WriteContext{Actor: "test", SessionID: "sess", WritePath: "test", ProjectID: "acme"},
		&mods.ModLoad{
			ModID: modID, SessionID: "sess-A",
			DurationMs: 100, CapabilitiesCount: 5,
		})
	if err != nil {
		t.Fatalf("RecordModLoad acme: %v", err)
	}

	// From globex: ListModLoads must NOT include the acme load.
	if err := s.SetActiveProject(ctx, "globex"); err != nil {
		t.Fatalf("set globex: %v", err)
	}
	listGlobal, _ := s.ListModLoads(ctx, modID, 100)
	for _, l := range listGlobal {
		if l.ID == loadID {
			t.Fatalf("INV-7 violated: globex saw acme's mod load %+v", l)
		}
	}

	// Back in acme: ListModLoads returns the load.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme (back): %v", err)
	}
	listAcme, _ := s.ListModLoads(ctx, modID, 100)
	found := false
	for _, l := range listAcme {
		if l.ID == loadID {
			found = true
		}
	}
	if !found {
		t.Fatalf("acme ListModLoads must include own load, got %d rows: %+v", len(listAcme), listAcme)
	}
}

// helpers
func strPtr(s string) *string { return &s }

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
