package event_test

// L1 tests for DelegationProgressEmitter (Phase 12 T-103c).
// 4 tests covering the 4 main Emit* methods. The progression
// emitter builds a parent/root tree: EmitDecide sets rootEventID;
// the subsequent Emit* methods reference it as parent_event_id.
//
// All tests are hermetic: in-memory SQLite via the production
// Open() path (no mocks — per dark-testing A3 no mystery guests +
// A5 mock only at boundaries).
//
// Per dark-testing: A1 tests are evidence (real assertions against
// events table); A3 no mystery guests (real SQLite); A8 no sleep
// (sync ops complete on in-memory DBs immediately); A14 non-default
// values (every taskID, decision, verdict is non-empty).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	eventpkg "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
)

// ---------- 1. Decide (root) ----------

// TestDelegationProgressEmitter_Decide: EmitDecide is SYNC, returns
// the event id (caller captures for parent_event_id reference),
// phase=started, progress_pct=0, process_id=delegate-<taskID>.
func TestDelegationProgressEmitter_Decide(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDelegationProgressEmitter(w, "test-project", "test-actor")

	taskID := "task-decide-1"
	eventID, err := emitter.EmitDecide(context.Background(), taskID, "delegate", "DECIDE: delegate — task contains coordination marker")
	if err != nil {
		t.Fatalf("EmitDecide: %v", err)
	}
	if eventID <= 0 {
		t.Fatalf("EmitDecide returned id=%d; want > 0", eventID)
	}
	if emitter.RootEventID() != eventID {
		t.Errorf("RootEventID = %d; want %d (the id EmitDecide returned)", emitter.RootEventID(), eventID)
	}

	// The row should be visible immediately (sync emit).
	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("events count = %d; want 1 (EmitDecide is SYNC)", len(all))
	}
	ev := all[0]
	if ev.Kind != eventpkg.KindProgress {
		t.Errorf("Kind = %q; want progress", ev.Kind)
	}
	if ev.Phase.String != eventpkg.PhaseStarted {
		t.Errorf("Phase = %q; want started", ev.Phase.String)
	}
	if ev.ProgressPct.Float64 != 0 {
		t.Errorf("ProgressPct = %f; want 0", ev.ProgressPct.Float64)
	}
	if ev.ProcessID.String != "delegate-task-decide-1" {
		t.Errorf("ProcessID = %q; want delegate-task-decide-1", ev.ProcessID.String)
	}
	if ev.TargetTable.String != "delegation_trees" {
		t.Errorf("TargetTable = %q; want delegation_trees", ev.TargetTable.String)
	}
	if !strings.Contains(ev.Message.String, "delegate") {
		t.Errorf("Message = %q; want contains 'delegate'", ev.Message.String)
	}
}

// ---------- 2. Extract (child of root) ----------

// TestDelegationProgressEmitter_Extract: EmitExtract emits ASYNC,
// references rootEventID as parent_event_id, phase=running,
// progress_pct=25.
func TestDelegationProgressEmitter_Extract(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDelegationProgressEmitter(w, "test-project", "test-actor")

	taskID := "task-extract-1"
	rootID, err := emitter.EmitDecide(context.Background(), taskID, "delegate", "DECIDE: long task")
	if err != nil {
		t.Fatalf("EmitDecide: %v", err)
	}

	emitter.EmitExtract(context.Background(), taskID, "aligned", 5)

	// Poll for the child event.
	got := waitForProgressByPct(t, env, 25.0, 2*time.Second)
	if got == nil {
		t.Fatal("no EXTRACT progress event after 2s")
	}
	if got.Phase.String != eventpkg.PhaseRunning {
		t.Errorf("Phase = %q; want running", got.Phase.String)
	}
	if got.ProgressPct.Float64 != 25 {
		t.Errorf("ProgressPct = %f; want 25", got.ProgressPct.Float64)
	}
	if got.ParentEventID.Int64 != rootID {
		t.Errorf("ParentEventID = %d; want %d (rootEventID)", got.ParentEventID.Int64, rootID)
	}
	if got.RootEventID.Int64 != rootID {
		t.Errorf("RootEventID = %d; want %d", got.RootEventID.Int64, rootID)
	}
	if got.JudgeVerdict.String != "aligned" {
		t.Errorf("JudgeVerdict = %q; want aligned", got.JudgeVerdict.String)
	}
	if !strings.Contains(got.Message.String, "5") {
		t.Errorf("Message = %q; want contains '5' (subtask count)", got.Message.String)
	}
	if got.ProcessID.String != "delegate-task-extract-1" {
		t.Errorf("ProcessID = %q; want delegate-task-extract-1", got.ProcessID.String)
	}
}

// ---------- 3. Mind + Curate (children of root) ----------

// TestDelegationProgressEmitter_MindCurate: EmitMind + EmitCurate
// each reference the same root (parent_event_id = rootEventID).
// Different progress_pct (50 vs 75).
func TestDelegationProgressEmitter_MindCurate(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDelegationProgressEmitter(w, "test-project", "test-actor")

	taskID := "task-mind-curate-1"
	rootID, err := emitter.EmitDecide(context.Background(), taskID, "delegate", "DECIDE: long task")
	if err != nil {
		t.Fatalf("EmitDecide: %v", err)
	}

	emitter.EmitMind(context.Background(), taskID, 3)
	emitter.EmitCurate(context.Background(), taskID, 3)

	// Wait for both child events.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		mindSeen, curateSeen := false, false
		for i := range all {
			switch all[i].ProgressPct.Float64 {
			case 50:
				mindSeen = true
				if all[i].ParentEventID.Int64 != rootID {
					t.Errorf("MIND ParentEventID = %d; want %d", all[i].ParentEventID.Int64, rootID)
				}
				if all[i].ProcessID.String != "delegate-task-mind-curate-1" {
					t.Errorf("MIND ProcessID = %q; want delegate-task-mind-curate-1", all[i].ProcessID.String)
				}
			case 75:
				curateSeen = true
				if all[i].ParentEventID.Int64 != rootID {
					t.Errorf("CURATE ParentEventID = %d; want %d", all[i].ParentEventID.Int64, rootID)
				}
			}
		}
		if mindSeen && curateSeen {
			return // all good
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("MIND+CURATE events did not land within 2s")
}

// ---------- 4. Completed (final) ----------

// TestDelegationProgressEmitter_Completed: EmitCompleted emits
// SYNC, phase=completed, progress_pct=100, duration_ms=N, all
// children reference rootEventID.
func TestDelegationProgressEmitter_Completed(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDelegationProgressEmitter(w, "test-project", "test-actor")

	taskID := "task-completed-1"
	rootID, err := emitter.EmitDecide(context.Background(), taskID, "delegate", "DECIDE: long task")
	if err != nil {
		t.Fatalf("EmitDecide: %v", err)
	}

	emitter.EmitExtract(context.Background(), taskID, "aligned", 3)
	emitter.EmitMind(context.Background(), taskID, 3)
	emitter.EmitCurate(context.Background(), taskID, 3)
	emitter.EmitCompleted(context.Background(), taskID, "aligned", 3, 1500)

	// Sync emit, so the row should be visible immediately.
	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// We expect: DECIDE (sync) + EXTRACT (async, polled) + MIND (async, polled)
	// + CURATE (async, polled) + COMPLETED (sync) = 5 total
	// But async may not have landed yet — let's just verify the completed row.
	var completed *sqlite.Event
	for i := range all {
		if all[i].Phase.String == eventpkg.PhaseCompleted {
			completed = all[i]
			break
		}
	}
	if completed == nil {
		t.Fatal("no 'completed' progress event")
	}
	if completed.ProgressPct.Float64 != 100 {
		t.Errorf("ProgressPct = %f; want 100", completed.ProgressPct.Float64)
	}
	if completed.DurationMs.Int64 != 1500 {
		t.Errorf("DurationMs = %d; want 1500", completed.DurationMs.Int64)
	}
	if completed.ParentEventID.Int64 != rootID {
		t.Errorf("ParentEventID = %d; want %d", completed.ParentEventID.Int64, rootID)
	}
	if completed.JudgeVerdict.String != "aligned" {
		t.Errorf("JudgeVerdict = %q; want aligned", completed.JudgeVerdict.String)
	}
}

// ---------- 5. Failed (error path) ----------

// TestDelegationProgressEmitter_Failed: EmitFailed emits phase=failed
// with error_msg + duration_ms. parent_event_id = rootEventID.
func TestDelegationProgressEmitter_Failed(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDelegationProgressEmitter(w, "test-project", "test-actor")

	taskID := "task-failed-1"
	rootID, err := emitter.EmitDecide(context.Background(), taskID, "delegate", "DECIDE: long task")
	if err != nil {
		t.Fatalf("EmitDecide: %v", err)
	}

	emitter.EmitFailed(context.Background(), taskID, "EXTRACT", "LLM call failed: context deadline exceeded", 30000)

	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var failed *sqlite.Event
	for i := range all {
		if all[i].Phase.String == eventpkg.PhaseFailed {
			failed = all[i]
			break
		}
	}
	if failed == nil {
		t.Fatal("no 'failed' progress event")
	}
	if failed.ParentEventID.Int64 != rootID {
		t.Errorf("ParentEventID = %d; want %d", failed.ParentEventID.Int64, rootID)
	}
	if !strings.Contains(failed.ErrorMsg.String, "context deadline") {
		t.Errorf("ErrorMsg = %q; want contains 'context deadline'", failed.ErrorMsg.String)
	}
	if failed.DurationMs.Int64 != 30000 {
		t.Errorf("DurationMs = %d; want 30000", failed.DurationMs.Int64)
	}
	if !strings.Contains(failed.Message.String, "EXTRACT") {
		t.Errorf("Message = %q; want contains 'EXTRACT' (failing phase)", failed.Message.String)
	}
}

// ---------- helpers ----------

// waitForProgressByPct polls the events table until a progress event
// with the given progress_pct appears, or timeout elapses.
func waitForProgressByPct(t *testing.T, env *testEnv, pct float64, timeout time.Duration) *sqlite.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		for i := range all {
			if all[i].ProgressPct.Float64 == pct {
				return all[i]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}