// Package orchestration — error_observatory_test.go
//
// v2.11.0 (spec 757, Wave 5D): regression tests for the Error
// Observatory integration:
//
//   - TestPublishVibe_NoLLM_NeedsHumanNotDrift (T6 conflation fix):
//     when Judge fails (no LLM configured), PublishVibe must return
//     verdict="needs_human" (NOT "drift_detected") AND record an
//     llm-domain error_event durably.
//   - TestSessionClose_ReportsErrorsTotal: session_close surfaces
//     the Error Observatory counts for the session.
//   - TestRecordError_PersistsDeduped: RecordError writes a
//     deduplicated cluster into the store.
package orchestration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newErrorObsTestOrchestrator builds an Orchestrator backed by a
// real SQLite store in a temp dir, with an active project + session.
func newErrorObsTestOrchestrator(t *testing.T, ctx context.Context) (*Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "errorobs.db"),
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
		Notes:     "errorobs test",
	}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	return orch, st
}

// TestPublishVibe_NoLLM_NeedsHumanNotDrift is the T6 conflation-fix
// regression. Pre-fix: a Judge failure (no LLM configured) produced
// verdict="drift_detected" — the operator believed the artifact
// drifted when the judge simply never ran. Post-fix: verdict must be
// "needs_human" and an llm-domain error_event must be persisted.
func TestPublishVibe_NoLLM_NeedsHumanNotDrift(t *testing.T) {
	// Isolate: this test asserts the NO-LLM path. The dev machine
	// may have LLM keys in env; clear them so Judge deterministically
	// returns ErrNoLLMAvailable.
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"DARK_DRIFT_JUDGE_DAEMON_URL", "DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON",
		"DARK_JUDGE_MODEL_ANTHROPIC", "DARK_JUDGE_MODEL_OPENAI", "DARK_JUDGE_MODEL_GEMINI",
		// v2.13.0: catalog keys must be cleared too — otherwise a real
		// key (e.g. DEEPSEEK_API_KEY) makes Judge succeed and this
		// no-LLM regression fails.
		"DEEPSEEK_API_KEY", "MINIMAX_API_KEY", "MOONSHOT_API_KEY",
		"ZAI_API_KEY", "DASHSCOPE_API_KEY", "DARK_JUDGE_PROVIDER",
	} {
		t.Setenv(k, "")
	}
	ctx := context.Background()
	orch, st := newErrorObsTestOrchestrator(t, ctx)

	// No LLM configured (fresh orchestrator, no DARK_*_API_KEY in
	// this test process → Judge returns ErrNoLLMAvailable).
	out, err := orch.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{
			VibeCase: "C2",
			Spec:     `{"intent":"write a landing page"}`,
		},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "http://example.test/landing.md",
			Text:         "# Landing\n\nBienvenido.",
		},
		SessionID: "sess-t6",
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}

	// THE CONFLATION ASSERTION: infra failure ≠ semantic drift.
	if out.Verdict == "drift_detected" {
		t.Fatal("VERDICT BUG: LLM failure was conflated with drift_detected (T6 fix violated)")
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict = %s, want needs_human (no LLM → no verdict, not drift)", out.Verdict)
	}
	if out.NextAction != "human_gate" {
		t.Errorf("next_action = %s, want human_gate", out.NextAction)
	}
	t.Logf("reasoning: %s", out.Reasoning)

	// The llm-domain error_event must have been persisted.
	llm, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{
		Domain:   errorobs.DomainLLM,
		ToolName: "publish_vibe",
	})
	if err != nil {
		t.Fatalf("ListErrorEvents: %v", err)
	}
	if len(llm) == 0 {
		// Debug: what IS in the table?
		all, _ := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{})
		t.Fatalf("no llm-domain error_event recorded after Judge failure (T6 telemetry missing); table has %d rows: %+v", len(all), all)
	}
}

// TestSessionClose_ReportsErrorsTotal verifies session_close surfaces
// the Error Observatory counts captured during the session.
func TestSessionClose_ReportsErrorsTotal(t *testing.T) {
	ctx := context.Background()
	orch, st := newErrorObsTestOrchestrator(t, ctx)

	// SessionStart created a session; get its id via the store.
	sessID, err := st.GetActiveSession(ctx, "default")
	if err != nil || sessID == "" {
		t.Fatalf("GetActiveSession: %v (id=%q)", err, sessID)
	}

	// Record 2 distinct clusters for this session (dedup keeps them
	// separate because the messages differ).
	orch.RecordError(ctx, "session_status", sessID, store.ErrNotFound, "")
	orch.RecordError(ctx, "session_status", sessID, store.ErrInvalidState, "")

	// Verify the store has them.
	rows, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{
		SessionID: sessID,
	})
	if err != nil {
		t.Fatalf("ListErrorEvents: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 clusters for session, got %d", len(rows))
	}

	// Close and check the summary surfaces ErrorsTotal.
	out, err := orch.SessionClose(ctx, SessionCloseInput{SessionID: sessID})
	if err != nil {
		t.Fatalf("SessionClose: %v", err)
	}
	if out.ErrorsTotal != 2 {
		t.Errorf("errors_total = %d, want 2", out.ErrorsTotal)
	}
	if out.ErrorOccurrences != 2 {
		t.Errorf("error_occurrences = %d, want 2", out.ErrorOccurrences)
	}
}

// TestRecordError_PersistsDeduped verifies RecordError writes a
// DEDUPLICATED cluster: 3 identical errors → 1 row with count=3.
func TestRecordError_PersistsDeduped(t *testing.T) {
	ctx := context.Background()
	orch, st := newErrorObsTestOrchestrator(t, ctx)

	sessID, err := st.GetActiveSession(ctx, "default")
	if err != nil || sessID == "" {
		t.Fatalf("GetActiveSession: %v", err)
	}

	for i := 0; i < 3; i++ {
		orch.RecordError(ctx, "memory_state", sessID, store.ErrNotFound, "")
	}

	rows, err := st.ListErrorEvents(ctx, errorobs.ErrorListFilters{
		ToolName:  "memory_state",
		SessionID: sessID,
	})
	if err != nil {
		t.Fatalf("ListErrorEvents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("dedup violated: got %d rows, want 1", len(rows))
	}
	if rows[0].Count != 3 {
		t.Errorf("count = %d, want 3", rows[0].Count)
	}
	if rows[0].Domain != errorobs.DomainStore {
		t.Errorf("domain = %s, want store", rows[0].Domain)
	}
	if rows[0].Code != "ErrNotFound" {
		t.Errorf("code = %s, want ErrNotFound", rows[0].Code)
	}
}

// TestRecordError_NilSafe ensures RecordError(nil error) is a no-op
// and never panics.
func TestRecordError_NilSafe(t *testing.T) {
	ctx := context.Background()
	orch, _ := newErrorObsTestOrchestrator(t, ctx)
	orch.RecordError(ctx, "memory_state", "", nil, "") // must not panic
}
