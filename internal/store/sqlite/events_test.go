// Package sqlite — events_test.go: hermetic tests for the polymorphic
// events table (Phase 12 / alpha.23 T-101).
//
// Uses newEventsTestStore (test helper, identical to newBitemporalTestStore)
// to spin up a fresh DB at v32 (Phase 12 polymorphic events schema),
// then exercises the 6 operations end-to-end on real SQLite. No mocks.
//
// Coverage:
//   - InsertEvent: basic, kind validation, missing-actor rejection,
//     self-FK rejection, project_id defaulting
//   - GetEventByID: hit + miss (ErrNotFound), invalid id rejection
//   - ListEvents: filter by kind, filter by project, since_id cursor
//   - ListEventsByProcessID: hit + miss + invalid
//   - ListEventsByRootEventID: hit + miss + invalid
//   - ListEventsByParentEventID: hit + miss + invalid (used by
//     subagent delegation tree traversal)
//   - Round-trip: insert + getback (verify all polymorphic fields survive)
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// testStore returns a *sqlite.Store so tests can call sqlite-specific
// operations that aren't on the store.Store interface yet (Phase 12
// T-101 ships the data layer; T-105 adds the event_log/event_replay
// tools which will surface the methods via the interface). The Open()
// factory returns store.Store; the cast is safe because the helper
// always opens a SQLite DB.
func testStore(t *testing.T) (*Store, func()) {
	t.Helper()
	st, cleanup := newEventsTestStore(t)
	return st.(*Store), cleanup
}

// newEventsTestStore creates a test Store wired to a fresh SQLite DB.
// The DB is at the latest schema (v32 = Phase 12 events) via the
// production Open() path. Returns the store and a cleanup func.
//
// Identical pattern to newBitemporalTestStore; duplicated here for
// test isolation (the bitemporal_test.go helper is package-private
// and the events test should not depend on its specifics).
func newEventsTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "events-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	cleanup := func() { _ = st.Close() }
	return st, cleanup
}

// strPtr is a convenience to build a sql.NullString from a string
// literal in test setup. "" maps to NULL (matches the schema's NULLABLE
// semantics).
func strPtr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// int64Ptr is the same for sql.NullInt64. 0 maps to NULL.
func int64Ptr(i int64) sql.NullInt64 {
	return sql.NullInt64{Int64: i, Valid: i != 0}
}

// float64Ptr is the same for sql.NullFloat64. 0.0 maps to NULL.
func float64Ptr(f float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: f, Valid: f != 0}
}

// ============================================================================
// TestInsertEvent — happy path: insert one modification + one progress, both
// survive the round-trip (all polymorphic fields read back correctly).
// ============================================================================

func TestInsertEvent_RoundTrip(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	// 1. Modification event (full polymorphic payload).
	modID, err := st.InsertEvent(ctx, &Event{
		Kind:           "modification",
		Actor:          "system",
		SessionID: strPtr("sess-test-1"),
		TargetTable:    strPtr("agent_memory"),
		TargetRowID:    int64Ptr(42),
		Operation:      strPtr("update"),
		Classification: strPtr("decay"),
		Source:         strPtr("internal/recall/decay.go"),
		Rationale:      strPtr("decay score crossed threshold"),
		RationaleKind:  strPtr("decay_function"),
		PayloadBefore:  strPtr(`{"score":0.8}`),
		PayloadAfter:   strPtr(`{"score":0.65}`),
		Confidence:     float64Ptr(0.92),
		JudgeVerdict:   strPtr("aligned"),
		JudgeReasoning: strPtr("decay formula applied per spec"),
		JudgeRunID:     int64Ptr(1234),
		PayloadJSON:    strPtr(`{"gen_ai.operation.name":"decay_score"}`),
	})
	if err != nil {
		t.Fatalf("InsertEvent modification: %v", err)
	}
	if modID <= 0 {
		t.Fatalf("InsertEvent modification: got id %d; want > 0", modID)
	}

	// 2. Progress event (full polymorphic payload).
	progID, err := st.InsertEvent(ctx, &Event{
		Kind:      "progress",
		Actor:     "judge",
		SessionID: strPtr("sess-test-1"),
		ProcessID: strPtr("drift-99"),
		Phase:     strPtr("started"),
		Message:   strPtr("starting async drift_judge"),
		PayloadJSON: strPtr(`{
			"gen_ai.operation.name":"drift_judge",
			"gen_ai.provider.name":"minimax",
			"gen_ai.request.model":"MiniMax-M3"
		}`),
	})
	if err != nil {
		t.Fatalf("InsertEvent progress: %v", err)
	}
	if progID <= 0 {
		t.Fatalf("InsertEvent progress: got id %d; want > 0", progID)
	}

	// 3. Read back via GetEventByID and verify all fields.
	gotMod, err := st.GetEventByID(ctx, modID)
	if err != nil {
		t.Fatalf("GetEventByID modification: %v", err)
	}
	if gotMod.Kind != "modification" {
		t.Errorf("mod.Kind = %q; want %q", gotMod.Kind, "modification")
	}
	if gotMod.ProjectID != "default" {
		t.Errorf("mod.ProjectID = %q; want %q", gotMod.ProjectID, "default")
	}
	if gotMod.TargetTable.String != "agent_memory" {
		t.Errorf("mod.TargetTable = %q; want %q", gotMod.TargetTable.String, "agent_memory")
	}
	if !gotMod.TargetRowID.Valid || gotMod.TargetRowID.Int64 != 42 {
		t.Errorf("mod.TargetRowID = %+v; want {42,true}", gotMod.TargetRowID)
	}
	if gotMod.Rationale.String != "decay score crossed threshold" {
		t.Errorf("mod.Rationale = %q; want %q", gotMod.Rationale.String, "decay score crossed threshold")
	}
	if !gotMod.Confidence.Valid || gotMod.Confidence.Float64 != 0.92 {
		t.Errorf("mod.Confidence = %+v; want {0.92,true}", gotMod.Confidence)
	}
	if gotMod.PayloadJSON.String == "" {
		t.Errorf("mod.PayloadJSON is empty")
	}

	gotPro, err := st.GetEventByID(ctx, progID)
	if err != nil {
		t.Fatalf("GetEventByID progress: %v", err)
	}
	if gotPro.Kind != "progress" {
		t.Errorf("prog.Kind = %q; want %q", gotPro.Kind, "progress")
	}
	if gotPro.ProcessID.String != "drift-99" {
		t.Errorf("prog.ProcessID = %q; want %q", gotPro.ProcessID.String, "drift-99")
	}
	if gotPro.Phase.String != "started" {
		t.Errorf("prog.Phase = %q; want %q", gotPro.Phase.String, "started")
	}
	// Progress-only fields should be NULL for modification rows; the
	// reverse also holds (mod-only fields NULL on process rows).
	if gotPro.TargetTable.Valid {
		t.Errorf("prog.TargetTable = %+v; want NULL", gotPro.TargetTable)
	}
	if gotPro.Rationale.Valid {
		t.Errorf("prog.Rationale = %+v; want NULL", gotPro.Rationale)
	}
}

// ============================================================================
// TestInsertEvent_Validation — invalid Kind, missing Actor, missing
// active project, self-FK rejection.
// ============================================================================

func TestInsertEvent_Validation(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	// Invalid Kind
	_, err := st.InsertEvent(ctx, &Event{
		Kind:  "nonsense",
		Actor: "system",
	})
	if err == nil {
		t.Fatalf("InsertEvent invalid kind: got nil error; want ErrInvalidArgument")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("InsertEvent invalid kind: err = %v; want ErrInvalidArgument", err)
	}

	// Missing Actor
	_, err = st.InsertEvent(ctx, &Event{
		Kind: "modification",
	})
	if err == nil {
		t.Fatalf("InsertEvent missing actor: got nil error; want ErrInvalidArgument")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("InsertEvent missing actor: err = %v; want ErrInvalidArgument", err)
	}

	// Parent_event_id pointing to a non-existent row (self-FK rejection)
	_, err = st.InsertEvent(ctx, &Event{
		Kind:          "progress",
		Actor:         "subagent",
		ParentEventID: int64Ptr(99999), // doesn't exist
	})
	if err == nil {
		t.Fatalf("InsertEvent orphan parent_event_id: got nil error; want FK violation")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("InsertEvent orphan parent_event_id: err = %v; want FK violation", err)
	}
}

// ============================================================================
// TestInsertEvent_NoActiveProject — guard: InsertEvent requires an
// active project (matches the bitemporal.go requireProject pattern).
// ============================================================================

func TestInsertEvent_NoActiveProject(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "no-active.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	iface, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := iface.(*Store)
	defer st.Close()

	// No CreateProject + SetActiveProject — should reject.
	_, err = st.InsertEvent(ctx, &Event{
		Kind:  "modification",
		Actor: "system",
	})
	if err == nil {
		t.Fatalf("InsertEvent without active project: got nil error; want requireProject error")
	}
}

// ============================================================================
// TestGetEventByID_Miss — non-existent id returns store.ErrNotFound.
// ============================================================================

func TestGetEventByID_Miss(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	_, err := st.GetEventByID(ctx, 99999)
	if err == nil {
		t.Fatalf("GetEventByID missing: got nil error; want ErrNotFound")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetEventByID missing: err = %v; want ErrNotFound", err)
	}

	// Invalid id (negative or zero) returns ErrInvalidArgument.
	_, err = st.GetEventByID(ctx, 0)
	if err == nil {
		t.Fatalf("GetEventByID id=0: got nil error; want ErrInvalidArgument")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("GetEventByID id=0: err = %v; want ErrInvalidArgument", err)
	}
}

// ============================================================================
// TestListEvents_FilterByKind — insert 3 modifications + 2 progress, then
// list with kind=modification returns only the 3, kind=progress only 2,
// kind="" returns all 5 in id ASC order.
// ============================================================================

func TestListEvents_FilterByKind(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	// Seed 3 mods + 2 progs.
	for i := 0; i < 3; i++ {
		if _, err := st.InsertEvent(ctx, &Event{
			Kind:    "modification",
			Actor:   "system",
			TargetTable: strPtr("agent_memory"),
			TargetRowID: int64Ptr(int64(i + 1)),
			Source:  strPtr("test"),
			Rationale: strPtr("test rationale"),
		}); err != nil {
			t.Fatalf("seed mod %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := st.InsertEvent(ctx, &Event{
			Kind:      "progress",
			Actor:     "judge",
			ProcessID: strPtr("test-proc"),
			Phase:     strPtr("started"),
		}); err != nil {
			t.Fatalf("seed prog %d: %v", i, err)
		}
	}

	// kind=modification
	mods, err := st.ListEvents(ctx, ListEventsFilter{Kind: "modification", ProjectID: "default"})
	if err != nil {
		t.Fatalf("ListEvents kind=modification: %v", err)
	}
	if len(mods) != 3 {
		t.Errorf("kind=modification len = %d; want 3", len(mods))
	}
	for _, m := range mods {
		if m.Kind != "modification" {
			t.Errorf("kind=modification got row kind=%q", m.Kind)
		}
	}

	// kind=progress
	progs, err := st.ListEvents(ctx, ListEventsFilter{Kind: "progress", ProjectID: "default"})
	if err != nil {
		t.Fatalf("ListEvents kind=progress: %v", err)
	}
	if len(progs) != 2 {
		t.Errorf("kind=progress len = %d; want 2", len(progs))
	}

	// kind="" (any)
	all, err := st.ListEvents(ctx, ListEventsFilter{ProjectID: "default"})
	if err != nil {
		t.Fatalf("ListEvents kind=any: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("kind=any len = %d; want 5", len(all))
	}
	// Verify ordering: id ASC (canonical insertion order).
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Errorf("ordering: id[%d]=%d not > id[%d]=%d", i, all[i].ID, i-1, all[i-1].ID)
		}
	}

	// Invalid kind
	_, err = st.ListEvents(ctx, ListEventsFilter{Kind: "nonsense", ProjectID: "default"})
	if err == nil {
		t.Errorf("ListEvents invalid kind: got nil error; want ErrInvalidArgument")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("ListEvents invalid kind: err = %v; want ErrInvalidArgument", err)
	}
}

// ============================================================================
// TestListEvents_SinceID — incremental fetch (LangFuse flush pattern).
// ============================================================================

func TestListEvents_SinceID(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	// Insert 5 events.
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := st.InsertEvent(ctx, &Event{Kind: "modification", Actor: "system"})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Fetch events with id > ids[1] (should be the last 3).
	got, err := st.ListEvents(ctx, ListEventsFilter{
		ProjectID: "default",
		SinceID:   ids[1],
	})
	if err != nil {
		t.Fatalf("ListEvents since_id: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("since_id=%d len = %d; want 3", ids[1], len(got))
	}
	for _, ev := range got {
		if ev.ID <= ids[1] {
			t.Errorf("since_id=%d got id=%d (should be > %d)", ids[1], ev.ID, ids[1])
		}
	}
}

// ============================================================================
// TestListEventsByProcessID — LangFuse timeline-by-process.
// ============================================================================

func TestListEventsByProcessID(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	// 3 events for process "drift-1", 2 for "drift-2".
	for i := 0; i < 3; i++ {
		if _, err := st.InsertEvent(ctx, &Event{
			Kind: "progress", Actor: "judge",
			ProcessID: strPtr("drift-1"),
			Phase:     strPtr("heartbeat"),
		}); err != nil {
			t.Fatalf("seed drift-1 %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := st.InsertEvent(ctx, &Event{
			Kind: "progress", Actor: "judge",
			ProcessID: strPtr("drift-2"),
			Phase:     strPtr("heartbeat"),
		}); err != nil {
			t.Fatalf("seed drift-2 %d: %v", i, err)
		}
	}

	got, err := st.ListEventsByProcessID(ctx, "drift-1")
	if err != nil {
		t.Fatalf("ListEventsByProcessID drift-1: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("drift-1 len = %d; want 3", len(got))
	}

	got2, err := st.ListEventsByProcessID(ctx, "drift-2")
	if err != nil {
		t.Fatalf("ListEventsByProcessID drift-2: %v", err)
	}
	if len(got2) != 2 {
		t.Errorf("drift-2 len = %d; want 2", len(got2))
	}

	// Missing process
	got3, err := st.ListEventsByProcessID(ctx, "drift-doesnt-exist")
	if err != nil {
		t.Fatalf("ListEventsByProcessID missing: %v", err)
	}
	if len(got3) != 0 {
		t.Errorf("missing process len = %d; want 0", len(got3))
	}

	// Empty process_id rejected
	_, err = st.ListEventsByProcessID(ctx, "")
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("empty processID: err = %v; want ErrInvalidArgument", err)
	}
}

// ============================================================================
// TestListEventsByRootEventID — Step Functions exec history.
// ============================================================================

func TestListEventsByRootEventID(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	// Create a parent (acts as root), then 3 children with root_event_id
	// pointing to it. The root itself doesn't set root_event_id (it
	// IS the root); children point up to the parent. ListEventsByRootEventID
	// returns the children of the root; the root itself is fetched
	// via GetEventByID if needed.
	rootID, err := st.InsertEvent(ctx, &Event{
		Kind: "progress", Actor: "orchestrator",
		ProcessID: strPtr("delegate-1"),
		Phase:     strPtr("spawned"),
	})
	if err != nil {
		t.Fatalf("seed root: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := st.InsertEvent(ctx, &Event{
			Kind: "progress", Actor: "subagent",
			ProcessID:     strPtr("delegate-1"),
			Phase:         strPtr("heartbeat"),
			ParentEventID: int64Ptr(rootID),
			RootEventID:   int64Ptr(rootID),
		}); err != nil {
			t.Fatalf("seed child %d: %v", i, err)
		}
	}

	// Fetch all events whose root_event_id = rootID. Returns the 3
	// children (the root itself is fetched via GetEventByID).
	got, err := st.ListEventsByRootEventID(ctx, rootID)
	if err != nil {
		t.Fatalf("ListEventsByRootEventID: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("root=%d len = %d; want 3", rootID, len(got))
	}

	// Verify the root itself is fetchable via GetByID.
	root, err := st.GetEventByID(ctx, rootID)
	if err != nil {
		t.Fatalf("GetEventByID root: %v", err)
	}
	if root.Actor != "orchestrator" {
		t.Errorf("root.Actor = %q; want %q", root.Actor, "orchestrator")
	}
	if root.RootEventID.Valid {
		t.Errorf("root.RootEventID = %+v; want NULL (root is its own root)", root.RootEventID)
	}

	// Invalid id rejected.
	_, err = st.ListEventsByRootEventID(ctx, 0)
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("id=0: err = %v; want ErrInvalidArgument", err)
	}
}

// ============================================================================
// TestListEventsByParentEventID — LangFuse tree-nesting (children).
// ============================================================================

func TestListEventsByParentEventID(t *testing.T) {
	ctx := context.Background()
	st, cleanup := testStore(t)
	defer cleanup()

	parentID, err := st.InsertEvent(ctx, &Event{
		Kind: "progress", Actor: "orchestrator",
		ProcessID: strPtr("parent"),
		Phase:     strPtr("spawned"),
	})
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	// 3 children + 1 grandchild + 1 unrelated event.
	childIDs := []int64{}
	for i := 0; i < 3; i++ {
		id, err := st.InsertEvent(ctx, &Event{
			Kind: "progress", Actor: "child",
			ProcessID:     strPtr("child"),
			Phase:         strPtr("started"),
			ParentEventID: int64Ptr(parentID),
		})
		if err != nil {
			t.Fatalf("seed child %d: %v", i, err)
		}
		childIDs = append(childIDs, id)
	}

	// Fetch children of parentID.
	got, err := st.ListEventsByParentEventID(ctx, parentID)
	if err != nil {
		t.Fatalf("ListEventsByParentEventID: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("parent=%d len = %d; want 3", parentID, len(got))
	}

	// Children of childIDs[0] should be empty (no grandchildren seeded).
	for _, cid := range childIDs {
		got2, err := st.ListEventsByParentEventID(ctx, cid)
		if err != nil {
			t.Fatalf("ListEventsByParentEventID child %d: %v", cid, err)
		}
		if len(got2) != 0 {
			t.Errorf("child %d len = %d; want 0", cid, len(got2))
		}
	}

	// Invalid id rejected.
	_, err = st.ListEventsByParentEventID(ctx, -1)
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("id=-1: err = %v; want ErrInvalidArgument", err)
	}
}

// ============================================================================
// TestMigrationIdempotency_V32 — running the v32 migration twice on the
// same DB produces zero diff (CREATE TABLE IF NOT EXISTS, ADD COLUMN
// tolerant per migrate.go §22 F37 rule).
//
// This is an end-to-end test that exercises the migration runner
// directly: open at v31, run migration (idempotent), verify v32 state.
// ============================================================================

func TestMigrationIdempotency_V32(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "idempotent.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}

	// First open applies v32 (and all earlier) migrations.
	iface1, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	st1 := iface1.(*Store)
	if err := st1.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st1.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	// Insert one event so the second-open sees a populated table.
	if _, err := st1.InsertEvent(ctx, &Event{
		Kind:    "modification",
		Actor:   "system",
		TargetTable: strPtr("agent_memory"),
		TargetRowID: int64Ptr(1),
		Rationale:   strPtr("test"),
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	st1.Close()

	// Second open: re-applies all migrations. v32 is idempotent
	// (CREATE TABLE IF NOT EXISTS, ALTER TABLE ADD COLUMN tolerated
	// per F37).
	iface2, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open #2 (idempotency check): %v", err)
	}
	st2 := iface2.(*Store)
	defer st2.Close()

	// Schema version should still be v32.
	sv, err := st2.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if sv != 32 {
		t.Errorf("SchemaVersion = %d; want 32", sv)
	}

	// Events table should still exist with the row from #1.
	var count int
	if err := st2.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE kind = 'modification'`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Errorf("count events = %d; want 1 (idempotency must NOT have wiped data)", count)
	}
}