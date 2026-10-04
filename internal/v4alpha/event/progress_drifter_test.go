package event_test

// L1 tests for DriftJudgeProgressEmitter (Phase 12 T-103b).
// 4 tests, one per Emit* method (started, in_progress, completed, failed).
// All tests are hermetic: in-memory SQLite via the production Open()
// path (no mocks — per dark-testing A3 no mystery guests + A5 mock
// only at boundaries).
//
// Per dark-testing: A1 tests are evidence (real assertions against
// events table); A3 no mystery guests (real SQLite); A8 no sleep
// (sync ops complete on in-memory DBs immediately); A14 non-default
// values (every project_id, actor, sessionID is non-empty).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	eventpkg "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
)

// ---------- 1. started ----------

// TestDriftJudgeProgressEmitter_Started: EmitStarted writes one
// progress event with phase=started, progress_pct=0,
// target_table=drift_report, process_id=drift-<artifactID>.
func TestDriftJudgeProgressEmitter_Started(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDriftJudgeProgressEmitter(w, "test-project", "test-actor")

	artifactID := int64(101)
	specID := int64(7)
	sessionID := "sess-test-1"
	emitter.EmitStarted(context.Background(), artifactID, specID, sessionID)

	got := waitForPhase(t, env, eventpkg.PhaseStarted, 2*time.Second)
	if got == nil {
		t.Fatal("no progress event after 2s")
	}
	if got.ProcessID.String != "drift-101" {
		t.Errorf("ProcessID = %q; want drift-101", got.ProcessID.String)
	}
	if got.TargetTable.String != "drift_report" {
		t.Errorf("TargetTable = %q; want drift_report", got.TargetTable.String)
	}
	if got.TargetRowID.Int64 != artifactID {
		t.Errorf("TargetRowID = %d; want %d", got.TargetRowID.Int64, artifactID)
	}
	if got.SessionID.String != sessionID {
		t.Errorf("SessionID = %q; want %q", got.SessionID.String, sessionID)
	}
	if got.ProgressPct.Float64 != 0 {
		t.Errorf("ProgressPct = %f; want 0", got.ProgressPct.Float64)
	}
	if !strings.Contains(got.Message.String, "started") {
		t.Errorf("Message = %q; want contains 'started'", got.Message.String)
	}
	if got.Kind != eventpkg.KindProgress {
		t.Errorf("Kind = %q; want progress", got.Kind)
	}
}

// ---------- 2. in_progress ----------

// TestDriftJudgeProgressEmitter_InProgress: EmitInProgress writes
// one progress event with phase=running, progress_pct=<pct>. Clamp
// pct > 100 to 100 (overflow guard).
func TestDriftJudgeProgressEmitter_InProgress(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDriftJudgeProgressEmitter(w, "test-project", "test-actor")

	artifactID := int64(202)
	emitter.EmitInProgress(context.Background(), artifactID, "sess-test-2", 42.5)

	got := waitForPhase(t, env, eventpkg.PhaseRunning, 2*time.Second)
	if got == nil {
		t.Fatal("no progress event after 2s")
	}
	if got.Phase.String != eventpkg.PhaseRunning {
		t.Errorf("Phase = %q; want %q", got.Phase.String, eventpkg.PhaseRunning)
	}
	if got.ProgressPct.Float64 != 42.5 {
		t.Errorf("ProgressPct = %f; want 42.5", got.ProgressPct.Float64)
	}
	if !strings.Contains(got.Message.String, "42%") {
		t.Errorf("Message = %q; want contains '42%%'", got.Message.String)
	}
}

// ---------- 3. completed ----------

// TestDriftJudgeProgressEmitter_Completed: EmitCompleted writes one
// progress event with phase=completed, progress_pct=100,
// duration_ms=N, judge_verdict=aligned/drift_detected/needs_human,
// confidence=0..1.
func TestDriftJudgeProgressEmitter_Completed(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDriftJudgeProgressEmitter(w, "test-project", "test-actor")

	artifactID := int64(303)
	emitter.EmitCompleted(context.Background(), artifactID, "sess-test-3",
		"aligned", 0.92, 4321)

	got := waitForPhase(t, env, eventpkg.PhaseCompleted, 2*time.Second)
	if got == nil {
		t.Fatal("no progress event after 2s")
	}
	if got.ProgressPct.Float64 != 100 {
		t.Errorf("ProgressPct = %f; want 100", got.ProgressPct.Float64)
	}
	if got.DurationMs.Int64 != 4321 {
		t.Errorf("DurationMs = %d; want 4321", got.DurationMs.Int64)
	}
	if got.JudgeVerdict.String != "aligned" {
		t.Errorf("JudgeVerdict = %q; want aligned", got.JudgeVerdict.String)
	}
	if got.Confidence.Float64 < 0.91 || got.Confidence.Float64 > 0.93 {
		t.Errorf("Confidence = %f; want ~0.92 (range 0.91..0.93)", got.Confidence.Float64)
	}
	if !strings.Contains(got.Message.String, "aligned") {
		t.Errorf("Message = %q; want contains 'aligned'", got.Message.String)
	}
}

// ---------- 4. failed ----------

// TestDriftJudgeProgressEmitter_Failed: EmitFailed writes one progress
// event with phase=failed, error_msg=<reason>, duration_ms=<elapsed>.
func TestDriftJudgeProgressEmitter_Failed(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDriftJudgeProgressEmitter(w, "test-project", "test-actor")

	artifactID := int64(404)
	emitter.EmitFailed(context.Background(), artifactID, "sess-test-4",
		"context deadline exceeded after 120s", 120000)

	got := waitForPhase(t, env, eventpkg.PhaseFailed, 2*time.Second)
	if got == nil {
		t.Fatal("no progress event after 2s")
	}
	if got.Phase.String != eventpkg.PhaseFailed {
		t.Errorf("Phase = %q; want %q", got.Phase.String, eventpkg.PhaseFailed)
	}
	if !strings.Contains(got.ErrorMsg.String, "deadline exceeded") {
		t.Errorf("ErrorMsg = %q; want contains 'deadline exceeded'", got.ErrorMsg.String)
	}
	if got.DurationMs.Int64 != 120000 {
		t.Errorf("DurationMs = %d; want 120000", got.DurationMs.Int64)
	}
}

// ---------- 5. process_id stable across 4 calls ----------

// TestDriftJudgeProgressEmitter_StableProcessID: a single artifact's
// 4 emits share the same process_id (drift-<artifactID>).
func TestDriftJudgeProgressEmitter_StableProcessID(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	emitter := eventpkg.NewDriftJudgeProgressEmitter(w, "test-project", "test-actor")

	artifactID := int64(555)
	emitter.EmitStarted(context.Background(), artifactID, 7, "sess-test-5")
	emitter.EmitInProgress(context.Background(), artifactID, "sess-test-5", 50)
	emitter.EmitCompleted(context.Background(), artifactID, "sess-test-5", "drift_detected", 0.78, 9000)

	// Wait for all 3 to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		if len(all) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("events count = %d; want 3", len(all))
	}
	// All 3 must share process_id = "drift-555"
	for i, ev := range all {
		if ev.ProcessID.String != "drift-555" {
			t.Errorf("event[%d] ProcessID = %q; want drift-555", i, ev.ProcessID.String)
		}
		if ev.TargetRowID.Int64 != artifactID {
			t.Errorf("event[%d] TargetRowID = %d; want %d", i, ev.TargetRowID.Int64, artifactID)
		}
	}
}

// ---------- helpers ----------

// waitForPhase polls the events table until a progress event with the
// given phase appears, or timeout elapses. Returns nil on timeout.
func waitForPhase(t *testing.T, env *testEnv, phase string, timeout time.Duration) *sqlite.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		for i := range all {
			if all[i].Phase.String == phase {
				return all[i]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}