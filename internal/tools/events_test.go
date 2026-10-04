package tools

// Phase 12 T-105: e2e regression tests for dark_memory_event_log +
// dark_memory_event_replay (the 2 new EVENTS namespace tools).
//
// OD7 invariant (n): events table observability — operators can
// query the polymorphic events table (schema v32) via MCP.
// This test suite asserts:
//   - event_log with no filter returns all events for the active project
//   - event_log with kind/target_table/process_id filters works
//   - event_replay returns the root event + direct children
//   - event_replay with include_children=false returns only the root
//
// All tests are hermetic: in-memory SQLite via the production Open()
// path (no mocks — per dark-testing A3 no mystery guests + A5 mock
// only at boundaries).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newEventsTestEnv returns a Registry + *sqlite.Store with the events
// schema applied, the default project active, and 6 sample events
// inserted (3 modifications + 3 progress across 2 process_ids).
func newEventsTestEnv(t *testing.T) (*Registry, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()

	// 1. In-memory SQLite with v32 events schema.
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "events_tools_test.db"),
		WALMode:     true,
		ForeignKeys: true,
	}
	iface, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = iface.Close() })
	st := iface.(*sqlite.Store)
	if err := st.CreateProject(ctx, &project.Project{
		ProjectID: "default", DisplayName: "Default",
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	// 2. Insert 6 events: 3 modifications + 3 progress.
	// mod1: target_table=agent_memory, target_row_id=100
	// mod2: target_table=drift_report, target_row_id=200
	// mod3: target_table=delegation_trees, target_row_id=300
	// prog1: process_id=drift-1, phase=started
	// prog2: process_id=drift-1, phase=completed, parent=prog1
	// prog3: process_id=delegate-1, phase=started
	wc := store.WriteContext{Actor: "test-actor", WritePath: "events_tools_test"}
	_ = wc // not used by InsertEvent signature; kept for documentation
	mod1, err := st.InsertEvent(ctx, &store.Event{
		Kind: store.EventKindModification, Actor: "system",
		TargetTable:    toNullString("agent_memory"),
		TargetRowID:    toNullInt64(100),
		Operation:      toNullString("update"),
		Classification: toNullString("decay"),
		Source:         toNullString("recall/decay.go"),
		Rationale:      toNullString("RefreshOnAccess: access_count -> 5"),
		RationaleKind:  toNullString("decay_function"),
	})
	if err != nil {
		t.Fatalf("InsertEvent mod1: %v", err)
	}
	_, err = st.InsertEvent(ctx, &store.Event{
		Kind: store.EventKindModification, Actor: "system",
		TargetTable:    toNullString("drift_report"),
		TargetRowID:    toNullInt64(200),
		Operation:      toNullString("update"),
		Classification: toNullString("judge"),
		Source:         toNullString("judge/pipeline.go"),
		Rationale:      toNullString("drift_judge verdict=aligned"),
		RationaleKind:  toNullString("judge_verdict"),
	})
	if err != nil {
		t.Fatalf("InsertEvent mod2: %v", err)
	}
	_, err = st.InsertEvent(ctx, &store.Event{
		Kind: store.EventKindModification, Actor: "system",
		TargetTable:    toNullString("delegation_trees"),
		TargetRowID:    toNullInt64(300),
		Operation:      toNullString("insert"),
		Classification: toNullString("calibration"),
		Source:         toNullString("judge/personas_v4.go"),
		Rationale:      toNullString("persona content registered/replaced"),
		RationaleKind:  toNullString("cache_ttl_expired"),
	})
	if err != nil {
		t.Fatalf("InsertEvent mod3: %v", err)
	}
	prog1, err := st.InsertEvent(ctx, &store.Event{
		Kind: store.EventKindProgress, Actor: "orchestrator",
		ProcessID:   toNullString("drift-1"),
		TargetTable: toNullString("drift_report"),
		Phase:       toNullString("started"),
		ProgressPct: toNullFloat64(0),
		Message:     toNullString("drift_judge background goroutine started"),
	})
	if err != nil {
		t.Fatalf("InsertEvent prog1: %v", err)
	}
	_, err = st.InsertEvent(ctx, &store.Event{
		Kind: store.EventKindProgress, Actor: "orchestrator",
		ProcessID:     toNullString("drift-1"),
		ParentEventID: toNullInt64(prog1),
		RootEventID:   toNullInt64(prog1),
		TargetTable:   toNullString("drift_report"),
		Phase:         toNullString("completed"),
		ProgressPct:   toNullFloat64(100),
		DurationMs:    toNullInt64(1234),
		Message:       toNullString("drift_judge completed: verdict=aligned"),
		JudgeVerdict:  toNullString("aligned"),
		Confidence:    toNullFloat64(0.92),
	})
	if err != nil {
		t.Fatalf("InsertEvent prog2: %v", err)
	}
	_, err = st.InsertEvent(ctx, &store.Event{
		Kind: store.EventKindProgress, Actor: "orchestrator",
		ProcessID:   toNullString("delegate-1"),
		TargetTable: toNullString("delegation_trees"),
		Phase:       toNullString("started"),
		ProgressPct: toNullFloat64(0),
		Message:     toNullString("DECIDE: delegate"),
	})
	if err != nil {
		t.Fatalf("InsertEvent prog3: %v", err)
	}
	_ = mod1 // mark used (the id is the root_event_id for the drift-1 tree)

	// 3. Build a minimal Registry with the events tools registered.
	reg := NewRegistry()
	RegisterEventsTools(reg, st)
	_ = safety.Holder{} // safety not needed for event tools; silence unused

	return reg, st
}

// toNullInt64 / toNullString / toNullFloat64: small helpers to
// construct sql.Null* from a non-null value.
func toNullInt64(v int64) sql.NullInt64   { return sql.NullInt64{Int64: v, Valid: true} }
func toNullString(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
func toNullFloat64(v float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: v, Valid: true}
}

// callTool invokes a registered tool by canonical name.
func callTool(t *testing.T, reg *Registry, name string, in map[string]any) *ToolResponse {
	t.Helper()
	tool := reg.Get(name)
	if tool == nil {
		t.Fatalf("tool %q not registered", name)
	}
	raw, _ := jsonMarshal(in)
	resp, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s handler: %v", name, err)
	}
	return resp
}

// jsonMarshal is shared with health_test.go and is intentionally a
// thin wrapper to keep the test file's imports tidy.

// --- Test 1: event_log no filter ---

func TestEventLog_NoFilter(t *testing.T) {
	reg, _ := newEventsTestEnv(t)
	resp := callTool(t, reg, "event_log", map[string]any{})
	if resp == nil || resp.Data == nil {
		t.Fatal("event_log returned no data")
	}
	res, ok := resp.Data.(*EventLogResult)
	if !ok {
		t.Fatalf("event_log data = %T; want *EventLogResult", resp.Data)
	}
	if res.Count != 6 {
		t.Errorf("Count = %d; want 6 (3 mods + 3 progs)", res.Count)
	}
	if res.ProjectID != "default" {
		t.Errorf("ProjectID = %q; want default", res.ProjectID)
	}
}

// --- Test 2: event_log kind=modification ---

func TestEventLog_FilterByKind(t *testing.T) {
	reg, _ := newEventsTestEnv(t)
	resp := callTool(t, reg, "event_log", map[string]any{
		"kind": "modification",
	})
	res, ok := resp.Data.(*EventLogResult)
	if !ok {
		t.Fatalf("event_log data = %T", resp.Data)
	}
	if res.Count != 3 {
		t.Errorf("Count = %d; want 3 (modifications only)", res.Count)
	}
	for _, ev := range res.Events {
		if ev.Kind != "modification" {
			t.Errorf("event %d: Kind = %q; want modification", ev.ID, ev.Kind)
		}
	}
}

// --- Test 3: event_log kind=progress + process_id filter ---

func TestEventLog_FilterByProcessID(t *testing.T) {
	reg, _ := newEventsTestEnv(t)
	resp := callTool(t, reg, "event_log", map[string]any{
		"kind":       "progress",
		"process_id": "drift-1",
	})
	res, ok := resp.Data.(*EventLogResult)
	if !ok {
		t.Fatalf("event_log data = %T", resp.Data)
	}
	if res.Count != 2 {
		t.Errorf("Count = %d; want 2 (drift-1 has 2 events: started + completed)", res.Count)
	}
	for _, ev := range res.Events {
		if ev.ProcessID.String != "drift-1" {
			t.Errorf("event %d: ProcessID = %q; want drift-1", ev.ID, ev.ProcessID.String)
		}
	}
}

// --- Test 4: event_log target_table filter ---

func TestEventLog_FilterByTargetTable(t *testing.T) {
	reg, _ := newEventsTestEnv(t)
	resp := callTool(t, reg, "event_log", map[string]any{
		"target_table": "drift_report",
	})
	res := resp.Data.(*EventLogResult)
	// 3 events target drift_report: mod2 (modification) + prog1
	// (progress started) + prog2 (progress completed).
	if res.Count != 3 {
		t.Errorf("Count = %d; want 3 (mod2 + prog1 + prog2 all target drift_report)", res.Count)
	}
	// At least one is the modification event.
	hasMod := false
	for _, ev := range res.Events {
		if ev.Kind == "modification" {
			hasMod = true
			if ev.Classification.String != "judge" {
				t.Errorf("Classification = %q; want judge", ev.Classification.String)
			}
		}
	}
	if !hasMod {
		t.Errorf("expected at least one modification event in the drift_report target")
	}
}

// --- Test 5: event_replay happy path (root + children) ---

func TestEventReplay_RootAndChildren(t *testing.T) {
	reg, st := newEventsTestEnv(t)
	// Find the drift-1 root event (prog1, phase=started).
	ctx := context.Background()
	rows, err := st.ListEventsByProcessID(ctx, "drift-1")
	if err != nil {
		t.Fatalf("ListEventsByProcessID: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("drift-1 has %d events; want 2", len(rows))
	}
	var rootID int64
	for _, ev := range rows {
		if ev.Phase.String == "started" {
			rootID = ev.ID
		}
	}
	if rootID == 0 {
		t.Fatal("no root event with phase=started for drift-1")
	}
	resp := callTool(t, reg, "event_replay", map[string]any{
		"event_id": rootID,
	})
	res, ok := resp.Data.(*EventReplayResult)
	if !ok {
		t.Fatalf("event_replay data = %T", resp.Data)
	}
	if res.Root == nil {
		t.Fatal("Root = nil")
	}
	if res.Root.ID != rootID {
		t.Errorf("Root.ID = %d; want %d", res.Root.ID, rootID)
	}
	// 2 events total: the root + 1 child (drift-1 progress tree).
	// ListEventsByRootEventID includes the root itself (per the
	// data-layer contract documented in the wire.go).
	if res.ChildCount < 1 {
		t.Errorf("ChildCount = %d; want >= 1 (root + at least 1 child)", res.ChildCount)
	}
	// All children share root_event_id = rootID.
	for i, child := range res.Children {
		if child.RootEventID.Int64 != rootID {
			t.Errorf("child[%d] RootEventID = %d; want %d", i, child.RootEventID.Int64, rootID)
		}
	}
}

// --- Test 6: event_replay include_children=false ---

func TestEventReplay_OnlyRoot(t *testing.T) {
	reg, st := newEventsTestEnv(t)
	ctx := context.Background()
	rows, _ := st.ListEventsByProcessID(ctx, "drift-1")
	var rootID int64
	for _, ev := range rows {
		if ev.Phase.String == "started" {
			rootID = ev.ID
		}
	}
	resp := callTool(t, reg, "event_replay", map[string]any{
		"event_id":         rootID,
		"include_children": false,
	})
	res := resp.Data.(*EventReplayResult)
	if res.Root == nil {
		t.Fatal("Root = nil")
	}
	if res.ChildCount != 0 {
		t.Errorf("ChildCount = %d; want 0 (include_children=false)", res.ChildCount)
	}
}

// --- Test 7: event_replay not found ---

func TestEventReplay_NotFound(t *testing.T) {
	reg, _ := newEventsTestEnv(t)
	resp := callTool(t, reg, "event_replay", map[string]any{
		"event_id": 99999,
	})
	if resp.Error != nil {
		t.Fatalf("event_replay(event_id=99999) returned Error: %+v", resp.Error)
	}
	res, ok := resp.Data.(*EventReplayResult)
	if !ok {
		t.Fatalf("event_replay data = %T", resp.Data)
	}
	if res.Root != nil {
		t.Errorf("Root = %v; want nil for missing event_id", res.Root)
	}
	if res.ChildCount != 0 {
		t.Errorf("ChildCount = %d; want 0", res.ChildCount)
	}
}

// --- Test 8: event_replay invalid event_id ---

func TestEventReplay_InvalidEventID(t *testing.T) {
	reg, _ := newEventsTestEnv(t)
	resp := callTool(t, reg, "event_replay", map[string]any{
		"event_id": 0,
	})
	if resp == nil || resp.Error == nil {
		t.Fatal("event_replay(event_id=0) should return an error, got nil error")
	}
}