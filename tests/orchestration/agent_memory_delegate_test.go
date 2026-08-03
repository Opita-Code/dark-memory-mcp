// Package orchestration_test: agent_memory_delegate (v2.9.3).
//
// The delegate tool prepares a delegation context for a sub-agent
// spawn: it registers the C2 subagent binding AND returns a
// ready-to-inject markdown block (session metadata + curated pinned
// memories + open todos). These tests cover the orchestrator contract:
//
//   - Happy path: binding registered + context block built with pinned
//     memories + open todos.
//   - Validation: missing operator / subagent_id / task_description.
//   - Session requirement: delegate fails without an active session.
//   - Truncation: token budget forces the B1 truncation path.
//   - IncludePinned/IncludeTodos: exclusion toggles.
//   - Zero token budget: context sections omitted, metadata only.
//   - Registration persisted: GetActiveSubagent returns the binding.
//   - Subagent writes tagged: a save from the subagent uses
//     subagent_id, not the parent's agent_id.
package orchestration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// delegateOrch returns an orchestrator backed by a fresh SQLite DB
// with migrations applied + active project "acme" + DARK_MEMORY_V280
// enabled (so the delegate + C2 paths are live). Callers that need a
// session must start one explicitly via orch.SessionStart.
func delegateOrch(t *testing.T) (*orchestration.Orchestrator, store.Store) {
	t.Helper()
	t.Setenv("DARK_MEMORY_V280", "1")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "delegate.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	return orchestration.New(s, nil), s
}

// delegateStartSession opens a session in project "acme" and returns
// its session id.
func delegateStartSession(t *testing.T, orch *orchestration.Orchestrator) string {
	t.Helper()
	out, err := orch.SessionStart(context.Background(), orchestration.SessionStartInput{
		Operator:  "alice",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	return out.SessionID
}

// boolPtr returns a pointer to b (for *bool input fields).
func boolPtr(b bool) *bool { return &b }

// intPtr returns a pointer to i (for *int input fields).
func intPtr(i int) *int { return &i }

// TestDelegate_HappyPath is the headline test: with a session + one
// pinned memory + one open todo, the delegate call registers the
// binding AND returns a context block containing both.
func TestDelegate_HappyPath(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	// Seed a pinned decision + an open todo (operator "alice").
	if _, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "decision",
		Title:    "Pinned decision",
		Content:  "The canonical approach is hybrid retrieval.",
		Pinned:   true,
	}); err != nil {
		t.Fatalf("seed pinned: %v", err)
	}
	if _, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "todo",
		Title:    "Open todo",
		Content:  "Deploy the delegate tool.",
	}); err != nil {
		t.Fatalf("seed todo: %v", err)
	}

	out, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-abc123",
		TaskDescription: "Research the state of the art.",
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if out.SubagentID != "sub-abc123" {
		t.Errorf("SubagentID = %q, want %q", out.SubagentID, "sub-abc123")
	}
	if out.SessionID == "" {
		t.Errorf("SessionID want non-empty")
	}
	if out.ProjectID != "acme" {
		t.Errorf("ProjectID = %q, want %q", out.ProjectID, "acme")
	}
	if out.PinnedCount != 1 {
		t.Errorf("PinnedCount = %d, want 1", out.PinnedCount)
	}
	if out.TodoCount != 1 {
		t.Errorf("TodoCount = %d, want 1", out.TodoCount)
	}
	if out.Truncated {
		t.Errorf("Truncated = true, want false (small context fits budget)")
	}
	if out.DelegationContext == "" {
		t.Fatalf("DelegationContext want non-empty")
	}
	for _, want := range []string{
		"## dark-memory context",
		"| Session |", "| Project | acme |",
		"sub-abc123", "Pinned decision", "Open todo",
	} {
		if !strings.Contains(out.DelegationContext, want) {
			t.Errorf("DelegationContext missing %q", want)
		}
	}
}

// TestDelegate_NoActiveSession: without a session, delegate must fail
// with ErrInvalidState (wrapped). The gate would block this on the
// wire; the orchestrator check keeps direct calls honest.
func TestDelegate_NoActiveSession(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()

	_, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-nosess",
		TaskDescription: "This should fail.",
	})
	if err == nil {
		t.Fatalf("Delegate want error (no active session), got nil")
	}
	if !strings.Contains(err.Error(), "no active session") {
		t.Errorf("error = %q, want 'no active session'", err.Error())
	}
}

// TestDelegate_MissingOperator: operator is required (INV-1 audit).
func TestDelegate_MissingOperator(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	_, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		SubagentID:      "sub-1",
		TaskDescription: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("want errMissingField(operator), got %v", err)
	}
}

// TestDelegate_MissingSubagentID: subagent_id is required.
func TestDelegate_MissingSubagentID(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	_, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		TaskDescription: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "subagent_id") {
		t.Fatalf("want errMissingField(subagent_id), got %v", err)
	}
}

// TestDelegate_MissingTaskDescription: task_description is required.
func TestDelegate_MissingTaskDescription(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	_, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:   "alice",
		SubagentID: "sub-1",
	})
	if err == nil || !strings.Contains(err.Error(), "task_description") {
		t.Fatalf("want errMissingField(task_description), got %v", err)
	}
}

// TestDelegate_TokenBudgetTruncation: a large pinned set + small
// budget must force truncation and surface Truncated=true.
func TestDelegate_TokenBudgetTruncation(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	for i := 0; i < 5; i++ {
		if _, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
			Operator: "alice",
			Kind:     "decision",
			Title:    "Pinned decision " + string(rune('A'+i)),
			Content:  "This is a long canonical decision body that should eat tokens quickly. " + strings.Repeat("padding ", 60),
			Pinned:   true,
		}); err != nil {
			t.Fatalf("seed pinned %d: %v", i, err)
		}
	}

	out, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-trunc",
		TaskDescription: "x",
		MaxTokens:       intPtr(120), // tiny budget
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if !out.Truncated {
		t.Errorf("Truncated = false, want true (budget tiny)")
	}
	if out.PinnedCount >= 5 {
		t.Errorf("PinnedCount = %d, want < 5 (truncated)", out.PinnedCount)
	}
}

// TestDelegate_ExcludePinned: include_pinned=false drops the pinned
// section but keeps todos.
func TestDelegate_ExcludePinned(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	if _, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice", Kind: "decision", Title: "Pin me", Content: "x", Pinned: true,
	}); err != nil {
		t.Fatalf("seed pinned: %v", err)
	}
	if _, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice", Kind: "todo", Title: "Todo me", Content: "y",
	}); err != nil {
		t.Fatalf("seed todo: %v", err)
	}

	out, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-nopin",
		TaskDescription: "x",
		IncludePinned:   boolPtr(false),
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if out.PinnedCount != 0 {
		t.Errorf("PinnedCount = %d, want 0 (include_pinned=false)", out.PinnedCount)
	}
	if out.TodoCount != 1 {
		t.Errorf("TodoCount = %d, want 1", out.TodoCount)
	}
	if strings.Contains(out.DelegationContext, "Pin me") {
		t.Errorf("DelegationContext should not contain pinned memory")
	}
	if !strings.Contains(out.DelegationContext, "Todo me") {
		t.Errorf("DelegationContext should contain the todo")
	}
}

// TestDelegate_ZeroTokenBudget: max_tokens=0 omits context sections
// entirely; only session metadata + usage instructions remain.
func TestDelegate_ZeroTokenBudget(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	if _, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice", Kind: "decision", Title: "Pin me", Content: "x", Pinned: true,
	}); err != nil {
		t.Fatalf("seed pinned: %v", err)
	}

	out, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-zero",
		TaskDescription: "x",
		MaxTokens:       intPtr(0),
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if out.PinnedCount != 0 {
		t.Errorf("PinnedCount = %d, want 0 (max_tokens=0)", out.PinnedCount)
	}
	if strings.Contains(out.DelegationContext, "Pin me") {
		t.Errorf("DelegationContext should omit context at max_tokens=0")
	}
	if !strings.Contains(out.DelegationContext, "## dark-memory context") {
		t.Errorf("DelegationContext should still have the metadata header")
	}
}

// TestDelegate_RegistrationPersists: after delegate, GetActiveSubagent
// returns the binding with the correct parent agent id.
func TestDelegate_RegistrationPersists(t *testing.T) {
	orch, st := delegateOrch(t)
	ctx := context.Background()

	// Re-create the project with default_agent_id set (CreateProject
	// is idempotent) so resolveActiveAgentID has something to
	// resolve (otherwise ParentAgentID = "").
	if err := st.CreateProject(ctx, &project.Project{
		ProjectID:      "acme",
		DisplayName:    "ACME",
		DefaultAgentID: "agent-acme",
	}); err != nil {
		t.Fatalf("re-create project with default_agent_id: %v", err)
	}
	delegateStartSession(t, orch)

	if _, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-persist",
		TaskDescription: "x",
	}); err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	sub, err := st.GetActiveSubagent(ctx, "alice")
	if err != nil {
		t.Fatalf("GetActiveSubagent: %v", err)
	}
	if sub == nil {
		t.Fatalf("GetActiveSubagent want binding, got nil")
	}
	if sub.SubagentID != "sub-persist" {
		t.Errorf("SubagentID = %q, want %q", sub.SubagentID, "sub-persist")
	}
	if sub.ParentAgentID != "agent-acme" {
		t.Errorf("ParentAgentID = %q, want %q", sub.ParentAgentID, "agent-acme")
	}
}

// TestDelegate_SubagentWritesTagged: after delegate, a subagent save
// (no explicit agent_id) is tagged with the subagent_id, NOT the
// parent's agent_id. This is the C2 isolation invariant.
func TestDelegate_SubagentWritesTagged(t *testing.T) {
	orch, _ := delegateOrch(t)
	ctx := context.Background()
	delegateStartSession(t, orch)

	if _, err := orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-tagged",
		TaskDescription: "x",
	}); err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	// Subagent saves WITHOUT agent_id — the C2 resolution should pick
	// up sub-tagged from the active_subagents table.
	out, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "finding",
		Title:    "Subagent finding",
		Content:  "Discovered during delegation.",
	})
	if err != nil {
		t.Fatalf("Save (subagent): %v", err)
	}
	if out.Row.AgentID != "sub-tagged" {
		t.Errorf("AgentID = %q, want %q (C2 subagent tagging)", out.Row.AgentID, "sub-tagged")
	}
}

// TestDelegate_V280Disabled: when DARK_MEMORY_V280=0, delegate
// returns ErrInvalidState (feature disabled).
func TestDelegate_V280Disabled(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "0")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "delegate-off.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	orch := orchestration.New(s, nil)
	if _, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator: "alice", ProjectID: "acme",
	}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	_, err = orch.AgentMemoryDelegate(ctx, orchestration.AgentMemoryDelegateInput{
		Operator:        "alice",
		SubagentID:      "sub-off",
		TaskDescription: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "DARK_MEMORY_V280") {
		t.Fatalf("want ErrInvalidState (V280 off), got %v", err)
	}
}

// compile-time check: agentmemory types are reachable (parity with
// other orchestration_test files).
var _ agentmemory.AgentMemory
