// Package dual_driver — error_events_test.go: integration tests for
// the Error Observatory storage plane (spec 757, Wave 5D, migration
// v25). Validates the Store interface methods: SaveErrorEvent /
// ListErrorEvents / GetErrorEvent / ResolveErrorEvent / ErrorSummary.
//
// The suite exercises the real SQLite driver (the Postgres driver
// returns ErrNotConfigured for this namespace until the backplane
// is built out — see internal/store/postgres/store.go).
package dual_driver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	sqlitestore "github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// openErrorDB creates a fresh dark.db at a temp path, applies
// migrations, returns a ready Store + cleanup func.
func openErrorDB(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "error_events.db")
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

// sampleEvent builds a valid ErrorEvent for a store sentinel.
// Uses the REAL store.ErrNotFound sentinel (wrapped, production shape)
// so the classifier matches the sentinel table, not the message
// heuristic.
func sampleEvent(sessionID, toolName string) *errorobs.ErrorEvent {
	return errorobs.New("default", sessionID, toolName, fmt.Errorf("GetSpec: %w", store.ErrNotFound))
}

func TestErrorEvents_Save_Get(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	e := sampleEvent("sess-1", "memory_state")
	if err := st.SaveErrorEvent(ctx, e); err != nil {
		t.Fatalf("SaveErrorEvent: %v", err)
	}
	if e.ID <= 0 {
		t.Fatal("SaveErrorEvent did not populate ID")
	}

	got, err := st.GetErrorEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetErrorEvent: %v", err)
	}
	if got == nil {
		t.Fatal("GetErrorEvent returned nil for saved row")
	}
	if got.Domain != errorobs.DomainStore {
		t.Errorf("domain = %s, want store", got.Domain)
	}
	if got.Code != "ErrNotFound" {
		t.Errorf("code = %s, want ErrNotFound", got.Code)
	}
	if got.ProjectID != "default" {
		t.Errorf("project_id = %s, want default", got.ProjectID)
	}
	if got.Resolved {
		t.Error("resolved = true, want false")
	}
	if got.Count != 1 {
		t.Errorf("count = %d, want 1", got.Count)
	}
}

func TestErrorEvents_Save_Dedup(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	// Same fingerprint (session, tool, code, message) → 3 saves = 1
	// cluster with count=3.
	var ids []int64
	for i := 0; i < 3; i++ {
		e := sampleEvent("sess-dedup", "session_status")
		if err := st.SaveErrorEvent(ctx, e); err != nil {
			t.Fatalf("SaveErrorEvent iter %d: %v", i, err)
		}
		ids = append(ids, e.ID)
	}
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Fatalf("dedup failed: ids = %v, want all equal", ids)
	}

	got, err := st.GetErrorEvent(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetErrorEvent: %v", err)
	}
	if got.Count != 3 {
		t.Errorf("count = %d, want 3 (dedup increments)", got.Count)
	}

	// Different message → different cluster.
	other := errorobs.New("default", "sess-dedup", "session_status", errors.New("store: something else"))
	if err := st.SaveErrorEvent(ctx, other); err != nil {
		t.Fatalf("SaveErrorEvent other: %v", err)
	}
	if other.ID == ids[0] {
		t.Error("different message deduped into same cluster — message_hash not respected")
	}
}

func TestErrorEvents_Resolve_And_Dedup_StaysResolved(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	e := sampleEvent("sess-r", "health_ping")
	if err := st.SaveErrorEvent(ctx, e); err != nil {
		t.Fatalf("SaveErrorEvent: %v", err)
	}

	if err := st.ResolveErrorEvent(ctx, store.WriteContext{Actor: "tester", WritePath: "error_resolve"}, e.ID, "fixed in v2.11.0"); err != nil {
		t.Fatalf("ResolveErrorEvent: %v", err)
	}

	got, err := st.GetErrorEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetErrorEvent: %v", err)
	}
	if !got.Resolved {
		t.Error("resolved = false after ResolveErrorEvent")
	}
	if got.ResolutionNote != "fixed in v2.11.0" {
		t.Errorf("resolution_note = %q", got.ResolutionNote)
	}

	// A NEW identical occurrence after resolution creates a FRESH
	// cluster (resolved clusters stay resolved).
	fresh := sampleEvent("sess-r", "health_ping")
	if err := st.SaveErrorEvent(ctx, fresh); err != nil {
		t.Fatalf("SaveErrorEvent fresh: %v", err)
	}
	if fresh.ID == e.ID {
		t.Error("new occurrence after resolution deduped into resolved cluster — contract violated")
	}

	// Idempotent resolve: second resolve is nil.
	if err := st.ResolveErrorEvent(ctx, store.WriteContext{Actor: "tester", WritePath: "error_resolve"}, e.ID, "still fixed"); err != nil {
		t.Errorf("second resolve: %v, want nil (idempotent)", err)
	}
}

func TestErrorEvents_Resolve_NotFound(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	err := st.ResolveErrorEvent(ctx, store.WriteContext{Actor: "tester", WritePath: "error_resolve"}, 999999, "nope")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("resolve missing: %v, want ErrNotFound", err)
	}
}

func TestErrorEvents_List_Filters(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	// Two clusters in different domains.
	storeEv := sampleEvent("sess-list", "memory_state")
	if err := st.SaveErrorEvent(ctx, storeEv); err != nil {
		t.Fatalf("Save store event: %v", err)
	}
	llmEv := errorobs.New("default", "sess-list", "mindset_apply", errors.New("no llm available: rate limit"))
	if err := st.SaveErrorEvent(ctx, llmEv); err != nil {
		t.Fatalf("Save llm event: %v", err)
	}

	// Filter by domain.
	onlyLLM, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{Domain: errorobs.DomainLLM})
	if err != nil {
		t.Fatalf("ListErrorEvents: %v", err)
	}
	if len(onlyLLM) != 1 || onlyLLM[0].Code != "ErrNoLLMAvailable" {
		t.Errorf("domain filter: got %+v, want 1 llm row", onlyLLM)
	}

	// Filter by tool.
	byTool, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{ToolName: "memory_state"})
	if err != nil {
		t.Fatalf("ListErrorEvents tool: %v", err)
	}
	if len(byTool) != 1 || byTool[0].ToolName != "memory_state" {
		t.Errorf("tool filter: got %+v, want 1 memory_state row", byTool)
	}

	// Filter by session.
	bySess, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{SessionID: "sess-list"})
	if err != nil {
		t.Fatalf("ListErrorEvents session: %v", err)
	}
	if len(bySess) != 2 {
		t.Errorf("session filter: got %d rows, want 2", len(bySess))
	}

	// Unresolved default view: both are unresolved.
	unresolved, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{Resolved: boolP(false)})
	if err != nil {
		t.Fatalf("ListErrorEvents unresolved: %v", err)
	}
	if len(unresolved) != 2 {
		t.Errorf("unresolved filter: got %d rows, want 2", len(unresolved))
	}

	// Limit.
	limited, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{Limit: 1})
	if err != nil {
		t.Fatalf("ListErrorEvents limit: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limit filter: got %d rows, want 1", len(limited))
	}
}

func TestErrorEvents_Summary(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	// 2 store + 1 llm, one resolved.
	e1 := sampleEvent("sess-sum", "memory_state")
	e2 := sampleEvent("sess-sum", "session_close")
	_ = e2
	if err := st.SaveErrorEvent(ctx, e1); err != nil {
		t.Fatalf("Save e1: %v", err)
	}
	// Different message → separate cluster.
	e1b := errorobs.New("default", "sess-sum", "session_close", errors.New("store: cross project access denied"))
	if err := st.SaveErrorEvent(ctx, e1b); err != nil {
		t.Fatalf("Save e1b: %v", err)
	}
	llmEv := errorobs.New("default", "sess-sum", "mindset_apply", errors.New("no llm available: rate limit"))
	if err := st.SaveErrorEvent(ctx, llmEv); err != nil {
		t.Fatalf("Save llm: %v", err)
	}
	if err := st.ResolveErrorEvent(ctx, store.WriteContext{Actor: "tester", WritePath: "error_resolve"}, e1.ID, "done"); err != nil {
		t.Fatalf("Resolve e1: %v", err)
	}

	sum, err := st.ErrorSummary(ctx, 1)
	if err != nil {
		t.Fatalf("ErrorSummary: %v", err)
	}
	if sum.TotalErrors != 3 {
		t.Errorf("TotalErrors = %d, want 3", sum.TotalErrors)
	}
	if sum.Unresolved != 2 {
		t.Errorf("Unresolved = %d, want 2", sum.Unresolved)
	}
	if sum.ErrorsLastHour != 3 {
		t.Errorf("ErrorsLastHour = %d, want 3 (all just saved)", sum.ErrorsLastHour)
	}
	if sum.ByDomain[errorobs.DomainStore] != 2 {
		t.Errorf("ByDomain[store] = %d, want 2", sum.ByDomain[errorobs.DomainStore])
	}
	if sum.ByDomain[errorobs.DomainLLM] != 1 {
		t.Errorf("ByDomain[llm] = %d, want 1", sum.ByDomain[errorobs.DomainLLM])
	}
	if len(sum.TopRecurring) != 2 {
		t.Errorf("TopRecurring = %d rows, want 2 (unresolved)", len(sum.TopRecurring))
	}
}

func TestErrorEvents_Save_EmitsWriteAudit(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	// Save a fresh cluster → write_audit row must exist (INV-1,
	// drift 777 hardening: the INSERT emits an audit row atomically).
	e := sampleEvent("sess-audit", "session_status")
	if err := st.SaveErrorEvent(ctx, e); err != nil {
		t.Fatalf("SaveErrorEvent: %v", err)
	}
	if e.ID <= 0 {
		t.Fatal("SaveErrorEvent did not populate ID")
	}

	auditRows, err := st.ListWrites(ctx, audit.ListFilters{
		TableName: "error_events",
		RowID:     e.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	if len(auditRows) != 1 {
		t.Fatalf("write_audit rows for error_events cluster: got %d, want 1 (INV-1)", len(auditRows))
	}
	if auditRows[0].TableName != "error_events" {
		t.Errorf("audit table_name = %q, want error_events", auditRows[0].TableName)
	}
	if auditRows[0].RowID != e.ID {
		t.Errorf("audit row_id = %d, want %d", auditRows[0].RowID, e.ID)
	}
	if auditRows[0].WritePath != "SaveErrorEvent" {
		t.Errorf("audit write_path = %q, want SaveErrorEvent", auditRows[0].WritePath)
	}

	// Dedup hit (same fingerprint) → count++ but NO second audit row.
	e2 := sampleEvent("sess-audit", "session_status")
	if err := st.SaveErrorEvent(ctx, e2); err != nil {
		t.Fatalf("SaveErrorEvent dedup: %v", err)
	}
	if e2.ID != e.ID {
		t.Fatalf("dedup violated: e2.ID=%d want %d", e2.ID, e.ID)
	}
	auditRows2, err := st.ListWrites(ctx, audit.ListFilters{
		TableName: "error_events",
		RowID:     e.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListWrites after dedup: %v", err)
	}
	if len(auditRows2) != 1 {
		t.Errorf("write_audit rows after dedup increment: got %d, want 1 (dedup UPDATE emits no audit row)", len(auditRows2))
	}

	// A DIFFERENT cluster → its own audit row.
	other := errorobs.New("default", "sess-audit", "session_status", fmt.Errorf("GetSpec: %w", store.ErrInvalidState))
	if err := st.SaveErrorEvent(ctx, other); err != nil {
		t.Fatalf("SaveErrorEvent other: %v", err)
	}
	if other.ID == e.ID {
		t.Fatal("different error deduped into same cluster — message_hash not respected")
	}
	auditRows3, err := st.ListWrites(ctx, audit.ListFilters{
		TableName: "error_events",
		RowID:     other.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListWrites other: %v", err)
	}
	if len(auditRows3) != 1 {
		t.Errorf("write_audit rows for second cluster: got %d, want 1", len(auditRows3))
	}
}

func TestErrorEvents_CrossProject_Invisible(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	// Project isolation: event in project A, then switch to B.
	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-a", DisplayName: "A"})); err != nil {
		t.Fatalf("CreateProject A: %v", err)
	}
	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-b", DisplayName: "B"})); err != nil {
		t.Fatalf("CreateProject B: %v", err)
	}

	if err := st.SetActiveProject(ctx, "proj-a"); err != nil {
		t.Fatalf("SetActiveProject A: %v", err)
	}
	e := sampleEvent("sess-x", "tool")
	if err := st.SaveErrorEvent(ctx, e); err != nil {
		t.Fatalf("SaveErrorEvent: %v", err)
	}

	if err := st.SetActiveProject(ctx, "proj-b"); err != nil {
		t.Fatalf("SetActiveProject B: %v", err)
	}
	got, err := st.GetErrorEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetErrorEvent cross: %v", err)
	}
	if got != nil {
		t.Errorf("cross-project GetErrorEvent returned row %+v, want nil (INV-7)", got)
	}
	list, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{})
	if err != nil {
		t.Fatalf("ListErrorEvents cross: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("cross-project ListErrorEvents returned %d rows, want 0 (INV-7)", len(list))
	}
}

func boolP(b bool) *bool { return &b }

// --- Cross-project opt-in (admin elevation, 2026-08-18) ---
//
// The 4 tests below pin the contract for the error_events
// cross-project escape hatch. INV-7's default is preserved
// (TestErrorEvents_CrossProject_Invisible above); these tests
// verify the explicit opt-in path (filters.CrossProject +
// GetErrorEventCrossProject + ResolveErrorEventCrossProject)
// returns rows from any project. The tools-layer policy gate
// (DARK_ERROR_OBS_OPERATOR_OVERRIDE / DARK_ERROR_OBS_ADMIN_OPERATORS)
// is exercised at the integration layer (T10); the Store layer
// just trusts the caller.
//
// NOTE on SaveErrorEvent semantics: projectID is taken from
// e.ProjectID when non-empty (the caller's explicit value),
// else from s.activeProject. sampleEvent() returns an event
// with ProjectID="default" — that's the project's effective
// origin, but for these tests we want explicit project_id so
// we build the events directly with errorobs.New(projectID, ...).

func TestErrorEvents_List_CrossProject_ReturnsAllProjects(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	// Two projects, one cluster each.
	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-a", DisplayName: "A"})); err != nil {
		t.Fatalf("CreateProject A: %v", err)
	}
	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-b", DisplayName: "B"})); err != nil {
		t.Fatalf("CreateProject B: %v", err)
	}

	// Save one cluster per project, with project_id explicit on
	// the event (sampleEvent's ProjectID="default" would not pin
	// to the active project — SaveErrorEvent honors e.ProjectID).
	eA := errorobs.New("proj-a", "sess-xp-a", "tool_a", fmt.Errorf("GetSpec: %w", store.ErrNotFound))
	if err := st.SaveErrorEvent(ctx, eA); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if eA.ProjectID != "proj-a" {
		t.Fatalf("eA.ProjectID = %q, want proj-a (SaveErrorEvent honors e.ProjectID)", eA.ProjectID)
	}

	eB := errorobs.New("proj-b", "sess-xp-b", "tool_b", fmt.Errorf("GetSpec: %w", store.ErrNotFound))
	if err := st.SaveErrorEvent(ctx, eB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	if eB.ProjectID != "proj-b" {
		t.Fatalf("eB.ProjectID = %q, want proj-b", eB.ProjectID)
	}

	// INV-7 default (CrossProject=false): only B's row visible
	// (active project is "default", which has no rows).
	scoped, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{})
	if err != nil {
		t.Fatalf("ListErrorEvents scoped: %v", err)
	}
	if len(scoped) != 0 {
		t.Fatalf("scoped list: got %d rows, want 0 (default project has no clusters)", len(scoped))
	}

	// Switch active to proj-b and verify the INV-7 default shows
	// only proj-b's cluster.
	if err := st.SetActiveProject(ctx, "proj-b"); err != nil {
		t.Fatalf("SetActiveProject proj-b: %v", err)
	}
	scopedB, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{})
	if err != nil {
		t.Fatalf("ListErrorEvents scoped (proj-b active): %v", err)
	}
	if len(scopedB) != 1 || scopedB[0].ProjectID != "proj-b" {
		t.Fatalf("scoped list (proj-b active): got %d rows, want 1 from proj-b (INV-7)", len(scopedB))
	}

	// CrossProject=true: both rows visible (regardless of active project).
	cross, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{CrossProject: true})
	if err != nil {
		t.Fatalf("ListErrorEvents cross: %v", err)
	}
	if len(cross) != 2 {
		t.Fatalf("cross list: got %d rows, want 2", len(cross))
	}
	gotProjects := map[string]bool{}
	for _, r := range cross {
		gotProjects[r.ProjectID] = true
	}
	if !gotProjects["proj-a"] || !gotProjects["proj-b"] {
		t.Errorf("cross list missing projects: got %v", gotProjects)
	}
}

func TestErrorEvents_Get_CrossProject_IgnoresActiveProject(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-a", DisplayName: "A"})); err != nil {
		t.Fatalf("CreateProject A: %v", err)
	}
	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-b", DisplayName: "B"})); err != nil {
		t.Fatalf("CreateProject B: %v", err)
	}

	eA := errorobs.New("proj-a", "sess-get-xp", "tool_a", fmt.Errorf("GetSpec: %w", store.ErrNotFound))
	if err := st.SaveErrorEvent(ctx, eA); err != nil {
		t.Fatalf("Save A: %v", err)
	}

	// Switch to project B; INV-7 default hides A's row.
	if err := st.SetActiveProject(ctx, "proj-b"); err != nil {
		t.Fatalf("SetActiveProject B: %v", err)
	}
	hidden, err := st.GetErrorEvent(ctx, eA.ID)
	if err != nil {
		t.Fatalf("GetErrorEvent scoped: %v", err)
	}
	if hidden != nil {
		t.Errorf("scoped GetErrorEvent returned row from proj-a while active=proj-b (INV-7)")
	}

	// Cross-project variant returns it.
	cross, err := st.GetErrorEventCrossProject(ctx, eA.ID)
	if err != nil {
		t.Fatalf("GetErrorEventCrossProject: %v", err)
	}
	if cross == nil {
		t.Fatal("cross-project GetErrorEvent returned nil for existing row")
	}
	if cross.ProjectID != "proj-a" {
		t.Errorf("cross-project row project_id = %q, want proj-a", cross.ProjectID)
	}

	// Non-existent id still returns (nil, nil) — existence-leak parity.
	missing, err := st.GetErrorEventCrossProject(ctx, 999999)
	if err != nil {
		t.Fatalf("GetErrorEventCrossProject missing: %v", err)
	}
	if missing != nil {
		t.Errorf("cross-project GetErrorEvent for missing id returned %+v, want nil", missing)
	}
}

func TestErrorEvents_Resolve_CrossProject_MarksResolved(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-a", DisplayName: "A"})); err != nil {
		t.Fatalf("CreateProject A: %v", err)
	}
	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-b", DisplayName: "B"})); err != nil {
		t.Fatalf("CreateProject B: %v", err)
	}

	eA := errorobs.New("proj-a", "sess-resolve-xp", "tool_a", fmt.Errorf("GetSpec: %w", store.ErrNotFound))
	if err := st.SaveErrorEvent(ctx, eA); err != nil {
		t.Fatalf("Save A: %v", err)
	}

	// Switch to B; cross-project resolve A's row from B's session.
	if err := st.SetActiveProject(ctx, "proj-b"); err != nil {
		t.Fatalf("SetActiveProject B: %v", err)
	}

	wc := store.WriteContext{
		Actor:     "error_resolve_cross_project_admin",
		WritePath: "ResolveErrorEventCrossProject",
	}
	if err := st.ResolveErrorEventCrossProject(ctx, wc, eA.ID, "triage from proj-b session"); err != nil {
		t.Fatalf("ResolveErrorEventCrossProject: %v", err)
	}

	// Confirm the row is resolved, regardless of which project is active.
	got, err := st.GetErrorEventCrossProject(ctx, eA.ID)
	if err != nil {
		t.Fatalf("GetErrorEventCrossProject: %v", err)
	}
	if got == nil {
		t.Fatal("row vanished after cross-project resolve")
	}
	if !got.Resolved {
		t.Errorf("resolved = false after cross-project resolve")
	}
	if got.ResolutionNote != "triage from proj-b session" {
		t.Errorf("resolution_note = %q", got.ResolutionNote)
	}
	if got.ProjectID != "proj-a" {
		t.Errorf("row project_id = %q, want proj-a", got.ProjectID)
	}

	// Idempotency: second resolve is nil.
	if err := st.ResolveErrorEventCrossProject(ctx, wc, eA.ID, "redundant"); err != nil {
		t.Errorf("second cross-project resolve: %v, want nil (idempotent)", err)
	}

	// Non-existent id returns ErrNotFound.
	err = st.ResolveErrorEventCrossProject(ctx, wc, 999999, "nope")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-project resolve missing: %v, want ErrNotFound", err)
	}
}

func TestErrorEvents_Resolve_CrossProject_EmitsAuditRow(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-a", DisplayName: "A"})); err != nil {
		t.Fatalf("CreateProject A: %v", err)
	}
	if err := st.CreateProject(ctx, projectForCreate(projectAlias{ProjectID: "proj-b", DisplayName: "B"})); err != nil {
		t.Fatalf("CreateProject B: %v", err)
	}

	eA := errorobs.New("proj-a", "sess-audit-xp", "tool_a", fmt.Errorf("GetSpec: %w", store.ErrNotFound))
	if err := st.SaveErrorEvent(ctx, eA); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	// Audit row from the SaveErrorEvent (1 row).
	saveAudits, err := st.ListWrites(ctx, audit.ListFilters{TableName: "error_events", RowID: eA.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListWrites after save: %v", err)
	}
	if len(saveAudits) != 1 {
		t.Fatalf("audit rows after save: got %d, want 1", len(saveAudits))
	}

	if err := st.SetActiveProject(ctx, "proj-b"); err != nil {
		t.Fatalf("SetActiveProject B: %v", err)
	}
	wc := store.WriteContext{
		Actor:     "error_resolve_cross_project_admin",
		WritePath: "ResolveErrorEventCrossProject",
		SessionID: "sess-b-for-audit",
	}
	if err := st.ResolveErrorEventCrossProject(ctx, wc, eA.ID, "audit-trail test"); err != nil {
		t.Fatalf("ResolveErrorEventCrossProject: %v", err)
	}

	// 2 audit rows now: the save + the cross-project resolve.
	allAudits, err := st.ListWrites(ctx, audit.ListFilters{TableName: "error_events", RowID: eA.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListWrites after cross resolve: %v", err)
	}
	if len(allAudits) != 2 {
		t.Fatalf("audit rows after cross resolve: got %d, want 2 (save + resolve)", len(allAudits))
	}

	// The cross-project audit row has Actor=error_resolve_cross_project_admin
	// and WritePath=ResolveErrorEventCrossProject (distinguishable from the save).
	var resolveAudit *audit.WriteEvent
	for i := range allAudits {
		if allAudits[i].WritePath == "ResolveErrorEventCrossProject" {
			resolveAudit = &allAudits[i]
		}
	}
	if resolveAudit == nil {
		t.Fatal("no audit row with WritePath=ResolveErrorEventCrossProject")
	}
	if resolveAudit.Actor != "error_resolve_cross_project_admin" {
		t.Errorf("audit Actor = %q, want error_resolve_cross_project_admin", resolveAudit.Actor)
	}
	if resolveAudit.ProjectID != "proj-a" {
		t.Errorf("audit ProjectID = %q, want proj-a (the affected project, not active)", resolveAudit.ProjectID)
	}
	if resolveAudit.Notes != "audit-trail test" {
		t.Errorf("audit Notes = %q, want the resolution note", resolveAudit.Notes)
	}
}

// TestErrorEvents_NullSession_NullTool pins the NULL-column tolerance:
// a gate-refusal event saved with NO session and NO tool (exactly what
// RecordRefusal produces when the gate refuses before a session is
// bound — the error_events rows 1-13 in prod were all
// session_id=NULL) must round-trip through ListErrorEvents and
// GetErrorEvent without the Scan failing on NULL → ErrInternal.
func TestErrorEvents_NullSession_NullTool(t *testing.T) {
	ctx := context.Background()
	st, cleanup := openErrorDB(t)
	defer cleanup()

	// No session, no tool — the RecordRefusal shape.
	e := errorobs.New("default", "", "", errors.New("gate refusal ErrFrameStaleTooFar: session or project not bound"))
	if err := st.SaveErrorEvent(ctx, e); err != nil {
		t.Fatalf("SaveErrorEvent: %v", err)
	}
	if e.ID <= 0 {
		t.Fatal("SaveErrorEvent did not populate ID")
	}

	// List must not error (regression: Scan on NULL session_id failed).
	list, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{})
	if err != nil {
		t.Fatalf("ListErrorEvents with NULL session/tool: %v", err)
	}
	found := false
	for _, row := range list {
		if row.ID == e.ID {
			found = true
			if row.SessionID != "" {
				t.Errorf("SessionID = %q, want empty (COALESCE)", row.SessionID)
			}
			if row.ToolName != "" {
				t.Errorf("ToolName = %q, want empty (COALESCE)", row.ToolName)
			}
		}
	}
	if !found {
		t.Errorf("ListErrorEvents did not return the saved NULL-session row")
	}

	// Get must not error either.
	got, err := st.GetErrorEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetErrorEvent with NULL session/tool: %v", err)
	}
	if got == nil {
		t.Fatal("GetErrorEvent returned nil for saved row")
	}
}
