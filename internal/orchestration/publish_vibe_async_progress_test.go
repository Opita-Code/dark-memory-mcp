// Package orchestration — publish_vibe_async_progress_test.go
//
// Phase 12 T-103b: e2e regression test for the auto-emit progress
// events from the async drift_judge background path.
//
// OD7 invariant (o): async drift_judge background goroutine emits
// progress events through the lifecycle (started → in_progress →
// completed/failed) so operators polling pipeline_status have
// visibility into the background work.
//
// This test wires a real *event.DriftJudgeProgressEmitter into the
// orchestrator (via WithEventEmitter), runs publish_vibe with
// AsyncDriftCheck=true, and asserts that the events table contains
// at least 2 progress events for the artifact (started + completed;
// in_progress is opportunistic — EmitInProgress isn't wired yet at
// every judge step, just at the bookkeeping boundaries; final test
// is for the 2 guaranteed events).
package orchestration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
)

// TestPublishVibe_Async_EmitsProgressEvents is the Phase 12 T-103b
// e2e regression: with an EventEmitter wired, async drift_judge must
// emit at least 2 progress events (started + completed) into the
// events table. The events carry process_id="drift-<artifactID>" so
// operators can correlate the 2 emits with the background goroutine.
func TestPublishVibe_Async_EmitsProgressEvents(t *testing.T) {
	clearJudgeEnv(t)
	ctx := context.Background()
	orch, st := newAsyncTestOrchestrator(t, ctx)

	// Wire the EventEmitter (Phase 12 T-103b). projectID="default"
	// matches the test orchestrator's active project. The Writer
	// uses st.ActiveProject() for the project_id column. event.New
	// requires the concrete *sqlite.Store (because InsertEvent +
	// ActiveProject are not on the store.Store interface), so we
	// type-assert here.
	concreteStore, ok := st.(*sqlite.Store)
	if !ok {
		t.Fatalf("expected *sqlite.Store from sqlite.Open, got %T", st)
	}
	w, werr := event.New(concreteStore, nil) // no audit writer (test env)
	if werr != nil {
		t.Fatalf("event.New: %v", werr)
	}
	emitter := event.NewDriftJudgeProgressEmitter(w, "default", "orchestrator_publish_vibe_async")
	orch.WithEventEmitter(emitter)

	out, err := orch.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{
			VibeCase: "C2",
			Spec:     `{"intent":"write a landing page"}`,
		},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "http://example.test/async-progress.md",
			Text:         "# Landing\n\nAsync progress events test.",
		},
		AsyncDriftCheck: true,
		SessionID:       "sess-async-progress-1",
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if !out.Async {
		t.Fatal("async flag = false, want true")
	}

	// Wait for the background goroutine to finish (so "completed" is
	// definitely in the table by the time we assert). 10s is generous.
	deadline := time.Now().Add(10 * time.Second)
	for {
		drift, err := st.LatestDriftForArtifact(ctx, out.ArtifactID)
		if err != nil {
			t.Fatalf("LatestDriftForArtifact: %v", err)
		}
		if drift != nil && drift.Verdict != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background judge never completed before deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Give EmitAsync a brief window to land (it's fire-and-forget
	// through a goroutine; even on in-memory SQLite the channel+log
	// handoff can race the polling deadline above).
	time.Sleep(100 * time.Millisecond)

	// List all events and filter to our process_id.
	allEvents, err := concreteStore.ListEvents(ctx, sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	processID := "drift-" + intToString(out.ArtifactID)
	var started, completed *sqlite.Event
	for i := range allEvents {
		ev := allEvents[i]
		if ev.ProcessID.String != processID {
			continue
		}
		switch ev.Phase.String {
		case event.PhaseStarted:
			started = ev
		case event.PhaseCompleted:
			completed = ev
		}
	}
	if started == nil {
		t.Fatalf("no 'started' progress event with process_id=%s", processID)
	}
	if completed == nil {
		t.Fatalf("no 'completed' progress event with process_id=%s", processID)

	}
	// Spot-check the "started" event.
	if started.Kind != event.KindProgress {
		t.Errorf("started.Kind = %q; want %q", started.Kind, event.KindProgress)
	}
	if started.ProgressPct.Float64 != 0 {
		t.Errorf("started.ProgressPct = %f; want 0", started.ProgressPct.Float64)
	}
	if started.TargetTable.String != "drift_report" {
		t.Errorf("started.TargetTable = %q; want drift_report", started.TargetTable.String)
	}
	// Spot-check the "completed" event.
	if completed.Kind != event.KindProgress {
		t.Errorf("completed.Kind = %q; want %q", completed.Kind, event.KindProgress)
	}
	if completed.ProgressPct.Float64 != 100 {
		t.Errorf("completed.ProgressPct = %f; want 100", completed.ProgressPct.Float64)
	}
	if completed.DurationMs.Int64 <= 0 {
		t.Errorf("completed.DurationMs = %d; want > 0", completed.DurationMs.Int64)
	}
	if completed.JudgeVerdict.String != "needs_human" {
		t.Errorf("completed.JudgeVerdict = %q; want needs_human (no LLM configured)", completed.JudgeVerdict.String)
	}
	if !strings.Contains(completed.Message.String, "needs_human") {
		t.Errorf("completed.Message = %q; want contains 'needs_human'", completed.Message.String)
	}
	t.Logf("progress events emitted: started@%s, completed@%s (process_id=%s, duration_ms=%d)",
		started.TS, completed.TS, processID, completed.DurationMs.Int64)
}

// intToString is a tiny helper to avoid fmt.Sprintf import noise.
func intToString(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}