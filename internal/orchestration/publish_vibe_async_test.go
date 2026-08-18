// Package orchestration — publish_vibe_async_test.go
//
// v2.14.0 (spec 998 p1): regression tests for the async drift check.
// The sync path is covered by error_observatory_test.go +
// publish_vibe_test.go; these tests pin the NEW async behavior:
//
//   - TestPublishVibe_Async_ReturnsPendingImmediately: the MCP call
//     must NOT block on the LLM judge. With AsyncDriftCheck=true and
//     NO LLM configured, PublishVibe returns verdict="pending" +
//     next_action="poll" + async=true synchronously, and a pending
//     drift report row exists for pipeline_status to poll.
//   - TestPublishVibe_Async_BackgroundUpdatesDriftReport: the
//     background goroutine updates the pending row in place when the
//     judge completes (needs_human here because no LLM is configured).
//
// The async goroutine runs detached from the request ctx, so the
// tests must WAIT (poll with deadline) for the background update —
// they do not assume a particular goroutine scheduling.
package orchestration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newAsyncTestOrchestrator is the same fixture as
// newErrorObsTestOrchestrator (real SQLite in temp dir + active
// project + session). Duplicated here to avoid cross-file coupling.
func newAsyncTestOrchestrator(t *testing.T, ctx context.Context) (*Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "async.db"),
		WALMode:     true,
		ForeignKeys: true,
	}
	st, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	orch := New(st, &safety.Holder{})
	if _, err := orch.SessionStart(ctx, SessionStartInput{
		Operator:  "tester",
		ProjectID: "default",
		Notes:     "async test",
	}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	return orch, st
}

// clearJudgeEnv is the no-LLM guard used across orchestration tests:
// without any DARK_*_API_KEY the Judge deterministically returns
// ErrNoLLMAvailable, which is what we want here (fast background
// needs_human verdict, no network).
func clearJudgeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"DARK_DRIFT_JUDGE_DAEMON_URL", "DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON",
		"DARK_JUDGE_MODEL_ANTHROPIC", "DARK_JUDGE_MODEL_OPENAI", "DARK_JUDGE_MODEL_GEMINI",
		"DEEPSEEK_API_KEY", "MINIMAX_API_KEY", "MINIMAX_API_KEY_CN", "MOONSHOT_API_KEY",
		"ZAI_API_KEY", "DASHSCOPE_API_KEY", "DARK_JUDGE_PROVIDER",
	} {
		t.Setenv(k, "")
	}
	// v2.20.0 (spec 1188): force env-var-only keys — the OS keyring may
	// hold a real migrated key, and this guard asserts the NO-LLM path.
	t.Setenv("DARK_LLM_KEYRING", "0")
}

// TestPublishVibe_Async_ReturnsPendingImmediately verifies the core
// UX fix: async publish must return WITHOUT touching the LLM judge.
// The artifact + spec are persisted, and a pending drift report row
// exists so pipeline_status has something to poll.
func TestPublishVibe_Async_ReturnsPendingImmediately(t *testing.T) {
	clearJudgeEnv(t)
	ctx := context.Background()
	orch, st := newAsyncTestOrchestrator(t, ctx)

	start := time.Now()
	out, err := orch.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{
			VibeCase: "C2",
			Spec:     `{"intent":"write a landing page"}`,
		},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "http://example.test/async-landing.md",
			Text:         "# Landing\n\nAsync publish.",
		},
		AsyncDriftCheck: true,
		SessionID:       "sess-async-1",
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}

	// The synchronous return must be pending + poll, and FAST.
	if !out.Async {
		t.Error("async flag = false, want true")
	}
	if out.Verdict != "pending" {
		t.Errorf("verdict = %s, want pending", out.Verdict)
	}
	if out.NextAction != "poll" {
		t.Errorf("next_action = %s, want poll", out.NextAction)
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("async publish blocked %s — the sync judge ran (UX bug)", elapsed)
	}
	t.Logf("async publish returned in %s (verdict=%s)", elapsed, out.Verdict)

	// pipeline_status must see the pending row immediately.
	drift, err := st.LatestDriftForArtifact(ctx, out.ArtifactID)
	if err != nil {
		t.Fatalf("LatestDriftForArtifact: %v", err)
	}
	if drift == nil {
		t.Fatal("no drift row for pipeline_status to poll")
	}
	// drift.Verdict at this exact moment is racy: the no-LLM judge
	// returns ErrNoLLMAvailable in <1ms, so by the time we read the
	// drift row here the background goroutine may have already settled
	// it to needs_human. The polling loop below (L146-159) verifies
	// the pending → final transition; here we only confirm the row
	// exists. We LOG the verdict state for diagnostic visibility but
	// do NOT fail on race-acceptable outcomes.
	if drift.Verdict == "pending" {
		t.Logf("verdict still pending at first read (ideal)")
	} else {
		t.Logf("verdict already settled at first read: %s (race-acceptable, polling loop verifies transition)", drift.Verdict)
	}
	if out.DriftID == 0 {
		t.Error("DriftID = 0, want the pending drift row id")
	}

	// Sync with the background goroutine: it MUST finish before the
	// test returns, otherwise t.Cleanup closes the store while the
	// detached goroutine still writes (database is closed + TempDir
	// cleanup fails). The no-LLM judge returns in ms; 10s is a
	// generous scheduling bound.
	deadline := time.Now().Add(10 * time.Second)
	for {
		drift, err := st.LatestDriftForArtifact(ctx, out.ArtifactID)
		if err != nil {
			t.Fatalf("LatestDriftForArtifact: %v", err)
		}
		if drift != nil && drift.Verdict != "pending" {
			t.Logf("background verdict landed: %s", drift.Verdict)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background judge never completed before test cleanup")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPublishVibe_Async_BackgroundUpdatesDriftReport verifies the
// background goroutine fills in the pending row in place. With no LLM
// the judge returns needs_human (infra, not drift — T6 semantics),
// and the drift row must transition pending → needs_human within the
// poll deadline.
func TestPublishVibe_Async_BackgroundUpdatesDriftReport(t *testing.T) {
	clearJudgeEnv(t)
	ctx := context.Background()
	orch, st := newAsyncTestOrchestrator(t, ctx)

	out, err := orch.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{
			VibeCase: "C2",
			Spec:     `{"intent":"write a landing page"}`,
		},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "http://example.test/async-update.md",
			Text:         "# Landing\n\nAsync update test.",
		},
		AsyncDriftCheck: true,
		SessionID:       "sess-async-2",
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}

	// Poll for the background transition (deadline 10s; no-LLM judge
	// returns in ms, but we don't assume goroutine scheduling).
	deadline := time.Now().Add(10 * time.Second)
	for {
		drift, err := st.LatestDriftForArtifact(ctx, out.ArtifactID)
		if err != nil {
			t.Fatalf("LatestDriftForArtifact: %v", err)
		}
		if drift != nil && drift.Verdict != "pending" {
			if drift.Verdict != "needs_human" {
				t.Errorf("background verdict = %s, want needs_human (no LLM)", drift.Verdict)
			}
			t.Logf("background verdict landed: %s (reasoning: %.120s)", drift.Verdict, drift.JudgeReasoning)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background judge never updated the pending drift row (deadline exceeded)")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPublishVibe_Async_SkipAutoCheck verifies async + auto_drift_check
// false: no LLM call, verdict="skipped" lands in the background row
// (the operator reviews manually).
func TestPublishVibe_Async_SkipAutoCheck(t *testing.T) {
	clearJudgeEnv(t)
	ctx := context.Background()
	orch, st := newAsyncTestOrchestrator(t, ctx)

	autoFalse := false
	out, err := orch.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{
			VibeCase: "C2",
			Spec:     `{"intent":"write a landing page"}`,
		},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "http://example.test/async-skip.md",
			Text:         "# Landing\n\nAsync skip test.",
		},
		AsyncDriftCheck: true,
		AutoDriftCheck:  &autoFalse,
		SessionID:       "sess-async-3",
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if out.Verdict != "pending" {
		t.Fatalf("immediate verdict = %s, want pending", out.Verdict)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		drift, err := st.LatestDriftForArtifact(ctx, out.ArtifactID)
		if err != nil {
			t.Fatalf("LatestDriftForArtifact: %v", err)
		}
		if drift != nil && drift.Verdict != "pending" {
			if drift.Verdict != "skipped" {
				t.Errorf("background verdict = %s, want skipped", drift.Verdict)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background judge never updated (deadline exceeded)")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
