package mcp

// Phase 12 T-103c: e2e regression test for the auto-emit progress
// events from the DECIDE→EXTRACT→MIND→CURATE pipeline (delegate_intent).
//
// OD7 invariant (p): subagent delegation tree — when an EventEmitter
// is wired into RunDelegateIntentCore, the events table contains a
// tree of progress events:
//
//	1. DECIDE → root (phase=started, progress_pct=0)
//	2. EXTRACT → child of root (phase=running, progress_pct=25)
//	3. MIND    → child of root (phase=running, progress_pct=50)
//	4. CURATE  → child of root (phase=running, progress_pct=75)
//	5. COMPLETED → child of root (phase=completed, progress_pct=100)
//
// All 5 share process_id="delegate-<taskID>". The DECIDE event id
// becomes parent_event_id + root_event_id for the 4 children.
//
// Note: This test uses PLAN (not EXTRACT) because we don't wire an
// LLM client — short delegation paths route through the deterministic
// PlanSubtasks step. The progress events still fire for PLAN.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	eventpkg "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
)

// TestRunDelegateIntentCore_EmitsProgressTree is the Phase 12 T-103c
// e2e regression: with an EventEmitter wired, delegate_intent emits
// a tree of 5 progress events into the events table.
//
// Test approach:
//  1. Set up an in-memory sqlite with v32 events_polymorphic schema.
//  2. Construct a *event.DelegationProgressEmitter pointed at it.
//  3. Call RunDelegateIntentCore with the emitter wired.
//  4. Assert the events table contains:
//     - 1 event with phase=started, progress_pct=0 (the DECIDE root)
//     - 1 event with phase=running, progress_pct=25 (EXTRACT child)
//     - 1 event with phase=running, progress_pct=50 (MIND child)
//     - 1 event with phase=running, progress_pct=75 (CURATE child)
//     - 1 event with phase=completed, progress_pct=100 (COMPLETED)
//     All 5 share process_id="delegate-<taskID>"; the 4 children
//     have parent_event_id == root_event_id == DECIDE event id.
func TestRunDelegateIntentCore_EmitsProgressTree(t *testing.T) {
	ctx := context.Background()

	// 1. In-memory SQLite with the events table (v32 schema).
	eventsCfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "delegation_events.db"),
		WALMode:     true,
		ForeignKeys: true,
	}
	iface, err := sqlite.Open(ctx, eventsCfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = iface.Close() })
	eventsStore := iface.(*sqlite.Store)
	if err := eventsStore.CreateProject(ctx,
		&project.Project{ProjectID: "default", DisplayName: "Default"},
	); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := eventsStore.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	// 2. Construct the emitter pointed at the events table.
	w, werr := eventpkg.New(eventsStore, nil) // nil audit writer (test env)
	if werr != nil {
		t.Fatalf("event.New: %v", werr)
	}
	emitter := eventpkg.NewDelegationProgressEmitter(w, "default", "orchestrator_delegate")

	// 3. Call RunDelegateIntentCore. We use a SHORT inline-style task
	//    that doesn't trigger EXTRACT — PLAN handles it. No LLM client
	//    is passed (nil-safe — the pipeline still runs DECIDE→PLAN
	//    →MIND→COMPLETED).
	taskDesc := "build a small landing page for the campaign"  // < 200 chars → no LLM call
	in := DelegateIntentInput{
		VibeCase:        "C1",
		TaskDescription: taskDesc,
		Operator:        "test-operator",
	}
	out, err := RunDelegateIntentCore(ctx, in, nil, nil, nil, emitter)
	if err != nil {
		t.Fatalf("RunDelegateIntentCore: %v", err)
	}
	if out == nil {
		t.Fatal("RunDelegateIntentCore returned nil output")
	}

	// 4. Assert the events table contains the 5 expected progress events.
	// MIND + CURATE are EmitAsync (Q2 decision: cosmetic steps async),
	// so wait briefly for them to land.
	all, err := waitForEventCount(t, eventsStore, 5, 2000000000) // 2s
	if err != nil {
		t.Fatalf("waitForEventCount: %v", err)
	}

	// Bucket the events by progress_pct (each phase has a unique pct).
	bucket := map[float64]*sqlite.Event{}
	for i := range all {
		ev := all[i]
		bucket[ev.ProgressPct.Float64] = ev
	}
	wantPcts := []float64{0, 25, 50, 75, 100}
	for _, pct := range wantPcts {
		if bucket[pct] == nil {
			t.Errorf("no event with progress_pct=%.0f; got bucket=%+v", pct, bucket)
		}
	}

	// 5. Spot-check the tree relationships.
	root := bucket[0]
	if root == nil {
		t.Fatal("DECIDE event missing")
	}
	if root.Phase.String != eventpkg.PhaseStarted {
		t.Errorf("root.Phase = %q; want started", root.Phase.String)
	}
	if !strings.HasPrefix(root.ProcessID.String, "delegate-") {
		t.Errorf("root.ProcessID = %q; want prefix 'delegate-'", root.ProcessID.String)
	}

	// All 4 children must reference the root's id as parent_event_id
	// AND root_event_id.
	for _, pct := range []float64{25, 50, 75, 100} {
		child := bucket[pct]
		if child == nil {
			continue
		}
		if child.ParentEventID.Int64 != root.ID {
			t.Errorf("child@%.0f%% ParentEventID = %d; want %d (root id)", pct, child.ParentEventID.Int64, root.ID)
		}
		if child.RootEventID.Int64 != root.ID {
			t.Errorf("child@%.0f%% RootEventID = %d; want %d (root id)", pct, child.RootEventID.Int64, root.ID)
		}
		if child.ProcessID.String != root.ProcessID.String {
			t.Errorf("child@%.0f%% ProcessID = %q; want %q (same as root)", pct, child.ProcessID.String, root.ProcessID.String)
		}
		if child.TargetTable.String != "delegation_trees" {
			t.Errorf("child@%.0f%% TargetTable = %q; want delegation_trees", pct, child.TargetTable.String)
		}
	}

	// 6. Spot-check the COMPLETED event.
	completed := bucket[100]
	if completed != nil {
		if completed.Phase.String != eventpkg.PhaseCompleted {
			t.Errorf("completed.Phase = %q; want completed", completed.Phase.String)
		}
		if completed.DurationMs.Int64 < 0 {
			t.Errorf("completed.DurationMs = %d; want >= 0", completed.DurationMs.Int64)
		}
	}

	t.Logf("delegate_intent emitted %d progress events (root=%d, process_id=%s)",
		len(all), root.ID, root.ProcessID.String)
}

// waitForEventCount polls the events table until at least n rows
// appear, or timeout (nanoseconds) elapses. Returns the final list.
func waitForEventCount(t *testing.T, st *sqlite.Store, n int, timeoutNs int64) ([]*sqlite.Event, error) {
	t.Helper()
	deadline := time.Now().Add(time.Duration(timeoutNs))
	for {
		all, err := st.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		if err != nil {
			return nil, err
		}
		if len(all) >= n {
			return all, nil
		}
		if time.Now().After(deadline) {
			return all, nil // return what we have; caller checks
		}
		time.Sleep(10 * time.Millisecond)
	}
}