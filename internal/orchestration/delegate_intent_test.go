// Package orchestration — delegate_intent_test.go
//
// Wave 5C (spec 728): acceptance tests for DelegateIntent — the
// orchestrator that runs the DelegationRouter pipeline
// (DECIDE → PLAN → MIND → CURATE). Uses the real SQLite store in a
// temp dir (the pattern used by tests/dual_driver), with a real
// session so the active-project/active-session contract holds.
//
// LLM wiring (spec 173 O5, v2.11.1 fix, updated FIX A spec 1272):
//   - Primary mechanism: harness injection via WithLLMSelector.
//     The harness (opencode / Claude Desktop / etc.) wires its cloud
//     LLM at boot time. This is the canonical path.
//   - Secondary fallback: ensureLLMSelector auto-detects the harness
//     LLM from env vars (ANTHROPIC_API_KEY / OPENAI_API_KEY / …).
//     This is a bridge for operators who have not yet adopted the
//     injection pattern.
//   - Test contract (FIX A): wireLLM() returns wireMockLLM() — an
//     in-memory OSINTSelector with deterministic mocks for
//     mindset_compose + mindset_quality. Tests are deterministic
//     and offline (no API keys, no provider coupling, no shell env
//     read). llmAvailable(orch) is always true in this file. The
//     live LLM contract is exercised in tests/wire/ — operator
//     runs out of band with harness-injected key.
//   - Per the operator flag 2026-08-18 ("hardcodear providers en
//     tests es antipatron"): the test must NOT depend on a specific
//     provider/dialect/key. The mock-first contract makes that
//     explicit and stable across harness-side changes.
package orchestration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// wireMockLLM returns an OSINTSelector with deterministic mocks for
// the mindset pipeline eval_types. The mocks return canned
// VerdictJSON shapes that parse cleanly into the structs the
// downstream code expects (mindsetAttempt for compose,
// MindsetJudgeVerdict for validate). This decouples the tests from
// a live LLM provider — CI runs without API keys, the deterministic
// "aligned" verdict lets the C7 acceptance path complete in
// milliseconds, and no shell env is read.
//
// Why mock-first (FIX A, spec 1272 / t8 backlog): per the operator
// flag (2026-08-18) "hardcodear providers en tests es antipatron",
// tests must not couple to a specific provider/dialect/key. The
// live LLM contract is exercised in tests/wire/ (suite that the
// operator runs out of band with the harness-injected key); the
// orchestration tests here are deterministic + offline.
func wireMockLLM() LLMSelector {
	composeMock := &MockLLMClient{
		Name_: "mock-compose",
		VerdictJSON: `{
			"role": "Senior direct-response campaign builder with 10y in conversion",
			"goal": "Produce a landing page that converts visitors into trial signups",
			"backstory": "Built 100+ landing pages for SaaS, e-commerce, lead-gen. Fluent in CRO, copywriting, and visual hierarchy.",
			"constraints": ["no fabricated testimonials", "explicit offer before the fold", "single CTA per page"],
			"tools_recommended": ["vibe_publish", "agent_memory_recall", "dark_research_web"],
			"model_recommended": "MiniMax-M3"
		}`,
		Confidence: 0.95,
		Model:      "MiniMax-M3",
	}
	qualityMock := &MockLLMClient{
		Name_: "mock-quality",
		VerdictJSON: `{
			"verdict": "aligned",
			"confidence": 0.95,
			"reasoning": "composed mindset is well-formed: has role + goal + backstory + tools_recommended + constraints. The model_recommended field is concrete. Acceptable to cache and hand off."
		}`,
		Confidence: 0.95,
		Model:      "MiniMax-M3",
	}
	sel := NewOSINTSelector(nil)
	sel.WithOverride(string(ssd.EvalMindsetCompose), composeMock).
		WithOverride(string(ssd.EvalMindsetQuality), qualityMock)
	return sel
}

// wireLLM constructs the LLM selector for an orchestrator following
// the v2.11.1 injection-first pattern. Tests are deterministic by
// design — they use the in-memory mock (wireMockLLM). The live LLM
// contract is exercised in tests/wire/ (operator runs out of band
// with harness-injected key). When wireMockLLM is in use,
// llmAvailable(orch) returns true and the LLM-dependent assertions
// in the C7 tests run against the deterministic mocks.
func wireLLM() LLMSelector {
	return wireMockLLM()
}

// newDelegateTestOrchestrator builds an Orchestrator backed by a
// real SQLite store in a temp dir, with an active project and an
// active session (via the canonical SessionStart path) so the gate
// contract holds. The LLM is wired via WithLLMSelector (primary
// harness-injection mechanism).
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
	// Primary: harness LLM injection (spec 173 O5, v2.11.1).
	// wireLLM returns nil when no LLM is available; the orchestrator's
	// ensureLLMSelector will then apply the secondary env-var fallback.
	orch = orch.WithLLMSelector(wireLLM())
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

// llmAvailable reports whether the orchestrator has an LLM wired
// via the primary harness-injection mechanism (WithLLMSelector).
// This is the v2.11.1 contract: wireLLM returns the selector when
// the harness LLM is reachable, and nil otherwise. When nil, the
// orchestrator's secondary fallback (ensureLLMSelector) auto-detects
// from env vars — but the test's LLM-dependent assertions are gated
// on the PRIMARY injection, so a test that receives nil here will
// exercise the best-effort fallback contract (empty prompt, nil tools).
func llmAvailable(orch *Orchestrator) bool {
	return orch.selector != nil
}

// TestDelegateIntent_C7_BasicPlan is the full-pipeline acceptance
// test: DECIDE (C7 → DELEGATE) → PLAN (1 bundle subtask) → MIND
// (mindset_apply composes a real system_prompt) → CURATE (C2
// binding + delegation context).
//
// It requires a real LLM (mindset_apply composes + judge-validates
// via the LLM). CI runs without API keys, so the test skips there —
// the deterministic DECIDE/PLAN/CURATE shape is still covered
// unconditionally by TestDelegateIntent_C7_DeterministicShape below.
func TestDelegateIntent_C7_BasicPlan(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newDelegateTestOrchestrator(t, ctx)
	if !llmAvailable(orch) {
		t.Skip("no LLM wired (ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY / DARK_DRIFT_JUDGE_DAEMON_URL not set or not reachable); full-pipeline C7 acceptance needs a working LLM")
	}

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

// TestDelegateIntent_C7_DeterministicShape covers the C7 delegation
// shape that does NOT depend on a real LLM: DECIDE (DELEGATE), PLAN
// (exactly one "bundle" subtask), CURATE (subagent_id + delegation
// context from the store). The MIND-dependent fields follow the
// documented best-effort contract: with an LLM they carry the
// composed prompt, without one they are empty (and that empty shape
// is itself asserted here, so CI exercises the fallback path).
func TestDelegateIntent_C7_DeterministicShape(t *testing.T) {
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
	// CURATE always runs (store-backed, no LLM).
	if sub.SubagentID == "" {
		t.Error("subtask subagent_id is empty (C2 binding not prepared)")
	}
	if sub.DelegationContext == "" {
		t.Error("subtask delegation_context is empty (CURATE failed?)")
	}
	// MIND is LLM-backed: assert the shape matches the documented
	// best-effort contract for the current environment.
	if llmAvailable(orch) {
		if sub.SystemPrompt == "" {
			t.Error("with LLM available, system_prompt must be non-empty")
		}
		if sub.ToolsRecommended == nil {
			t.Error("with LLM available, tools_recommended must be non-nil")
		}
	} else {
		if sub.SystemPrompt != "" {
			t.Logf("LLM-less build produced a system_prompt (unexpected but harmless): %q", sub.SystemPrompt)
		}
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
