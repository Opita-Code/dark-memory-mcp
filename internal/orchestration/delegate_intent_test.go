// Package orchestration — delegate_intent_test.go
//
// Wave 5C (spec 728): acceptance tests for DelegateIntent — the
// orchestrator that runs the DelegationRouter pipeline
// (DECIDE → PLAN → MIND → CURATE). Uses the real SQLite store in a
// temp dir (the pattern used by tests/dual_driver), with a real
// session so the active-project/active-session contract holds.
package orchestration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newDelegateTestOrchestrator builds an Orchestrator backed by a
// real SQLite store in a temp dir, with an active project and an
// active session (via the canonical SessionStart path) so the gate
// contract holds.
func newDelegateTestOrchestrator(t *testing.T, ctx context.Context) *Orchestrator {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "delegate.db"),
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
		Notes:     "delegate_intent test",
	}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	return orch
}

func TestDelegateIntent_V280Disabled(t *testing.T) {
	// DARK_MEMORY_V280 must be off for this test (the gate flag is
	// read from env at call time). Ensure it's unset.
	t.Setenv("DARK_MEMORY_V280", "0")
	ctx := context.Background()
	orch := newDelegateTestOrchestrator(t, ctx)
	_, err := orch.DelegateIntent(ctx, DelegateIntentInput{
		VibeCase:        "C7",
		TaskDescription: "campaña con imagen + copy",
	})
	if err == nil {
		t.Fatal("expected ErrInvalidState when DARK_MEMORY_V280=0")
	}
}

func TestDelegateIntent_MissingFields(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newDelegateTestOrchestrator(t, ctx)

	cases := []struct {
		name string
		in   DelegateIntentInput
	}{
		{"empty case", DelegateIntentInput{TaskDescription: "task"}},
		{"empty task", DelegateIntentInput{VibeCase: "C7"}},
		{"invalid case", DelegateIntentInput{VibeCase: "C9", TaskDescription: "task"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := orch.DelegateIntent(ctx, tc.in)
			if err == nil {
				t.Fatal("expected error for missing/invalid fields")
			}
		})
	}
}

func TestDelegateIntent_C7_BasicPlan(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newDelegateTestOrchestrator(t, ctx)

	out, err := orch.DelegateIntent(ctx, DelegateIntentInput{
		VibeCase:        "C7",
		TaskDescription: "campaña: hero image + copy de venta + código de landing",
	})
	if err != nil {
		t.Fatalf("DelegateIntent: %v", err)
	}
	if out.Handler != "DELEGATE" {
		t.Fatalf("handler = %s, want DELEGATE", out.Handler)
	}
	if len(out.Plan) != 1 {
		t.Fatalf("plan has %d subtasks, want 1 (bundle)", len(out.Plan))
	}
	sub := out.Plan[0]
	if sub.ID != "bundle" {
		t.Errorf("subtask id = %q, want bundle", sub.ID)
	}
	if sub.VibeCase != "C7" {
		t.Errorf("subtask vibe_case = %q, want C7", sub.VibeCase)
	}
	if sub.SystemPrompt == "" {
		t.Errorf("subtask system_prompt is empty (mindset_apply failed silently?)")
	}
	if sub.SubagentID == "" {
		t.Error("subtask subagent_id is empty (C2 binding not prepared)")
	}
	if sub.ToolsRecommended == nil {
		t.Error("subtask tools_recommended is nil")
	}
}

func TestDelegateIntent_C1_Handles(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newDelegateTestOrchestrator(t, ctx)

	out, err := orch.DelegateIntent(ctx, DelegateIntentInput{
		VibeCase:        "C1",
		TaskDescription: "refactor del módulo auth",
	})
	if err != nil {
		t.Fatalf("DelegateIntent: %v", err)
	}
	if out.Handler != "HANDLE" {
		t.Fatalf("handler = %s, want HANDLE (MVP fallback for C1)", out.Handler)
	}
	if out.Plan != nil {
		t.Fatalf("plan = %+v, want nil for HANDLE", out.Plan)
	}
}
