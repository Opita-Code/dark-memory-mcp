// Package orchestration — c2_binding_test.go
//
// The C2 binding trap.
//
// WHAT THE TRAP IS
//
// agent_memory_delegate registers a "subagent binding" (active_subagents)
// so that the sub-agent's agent_memory writes are tagged with its
// subagent_id instead of the principal's. The binding is resolved by
// OPERATOR (GetActiveSubagent → WHERE project_id = ? AND operator = ?),
// NOT by caller — so while it is alive it captures the PRINCIPAL's own
// writes too, not just the sub-agent's.
//
// The damage is invisible: the row just carries a different agent_id,
// and the ContextRecap only narrows by agent_id when one is configured
// (store.go: `if f.AgentID != ""`). So the mis-attribution either does
// nothing today or silently hides memories the moment a project sets
// default_agent_id.
//
// WHAT THIS FILE PROVES
//
//  1. The trap is real and reproducible (write without agent_id while a
//     binding is alive → the row carries the subagent_id).
//  2. The caller-provided agent_id is the escape hatch (priority 1 in
//     the resolution chain: caller > binding > project default > "").
//  3. register_subagent=false gives a context-only handoff: the context
//     comes back, no binding is created.
//  4. Live bindings are now VISIBLE: session_start and session_close
//     report them, so an orphan binding is no longer invisible.
//
// Deterministic and offline: real SQLite store in a temp dir, no LLM.
package orchestration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newC2TestOrchestrator builds an Orchestrator over a temp SQLite store
// with an active project AND an active session, because both the
// delegation path and the agent_id resolution chain need them.
func newC2TestOrchestrator(t *testing.T, ctx context.Context, operator string) *Orchestrator {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "c2.db"),
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
		Operator:  operator,
		ProjectID: "default",
		Notes:     "c2 binding test",
	}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	return orch
}

// TestC2Binding_TrapIsReal pins the behaviour that motivated the fix:
// with a binding alive, a write that does NOT carry agent_id is tagged
// with the SUBAGENT's id. If this test ever starts failing because the
// trap was fixed at the resolution-chain level, the escape hatch below
// becomes redundant — but it must not silently change semantics.
func TestC2Binding_TrapIsReal(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newC2TestOrchestrator(t, ctx, "tester")

	out, err := orch.AgentMemoryDelegate(ctx, AgentMemoryDelegateInput{
		Operator:        "tester",
		SubagentID:      "sub-trap",
		TaskDescription: "inspect the auth module",
	})
	if err != nil {
		t.Fatalf("AgentMemoryDelegate: %v", err)
	}
	if !out.BindingRegistered {
		t.Fatalf("default delegation must register the binding")
	}

	// A write WITHOUT agent_id → captured by the binding.
	trapped, err := orch.AgentMemorySave(ctx, AgentMemorySaveInput{
		Operator: "tester",
		Kind:     "note",
		Content:  "write without agent_id",
	})
	if err != nil {
		t.Fatalf("AgentMemorySave: %v", err)
	}
	if trapped.Row.AgentID != "sub-trap" {
		t.Fatalf("trap changed shape: got agent_id=%q, want %q", trapped.Row.AgentID, "sub-trap")
	}
}

// TestC2Binding_CallerAgentIDWins is the escape hatch the principal
// uses: an explicit agent_id is priority 1 in the resolution chain, so
// no binding can contaminate the principal's writes.
func TestC2Binding_CallerAgentIDWins(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newC2TestOrchestrator(t, ctx, "tester")

	if _, err := orch.AgentMemoryDelegate(ctx, AgentMemoryDelegateInput{
		Operator:        "tester",
		SubagentID:      "sub-escape",
		TaskDescription: "inspect the auth module",
	}); err != nil {
		t.Fatalf("AgentMemoryDelegate: %v", err)
	}

	clean, err := orch.AgentMemorySave(ctx, AgentMemorySaveInput{
		Operator: "tester",
		AgentID:  "tester",
		Kind:     "decision",
		Content:  "write with an explicit agent_id",
	})
	if err != nil {
		t.Fatalf("AgentMemorySave: %v", err)
	}
	if clean.Row.AgentID != "tester" {
		t.Fatalf("binding contaminated the principal's write: agent_id=%q", clean.Row.AgentID)
	}
}

// TestC2Binding_ContextOnlyHandoff proves the new escape hatch on the
// delegation tool itself: register_subagent=false returns the curated
// context but creates NO binding, so the principal is never captured.
func TestC2Binding_ContextOnlyHandoff(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newC2TestOrchestrator(t, ctx, "tester")

	out, err := orch.AgentMemoryDelegate(ctx, AgentMemoryDelegateInput{
		Operator:         "tester",
		SubagentID:       "sub-ctx-only",
		TaskDescription:  "inspect the auth module",
		RegisterSubagent: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("AgentMemoryDelegate: %v", err)
	}
	if out.BindingRegistered {
		t.Fatalf("register_subagent=false must not register a binding")
	}
	if out.DelegationContext == "" {
		t.Fatalf("context-only handoff must still return the context")
	}

	active, err := orch.Store.GetActiveSubagent(ctx, "tester")
	if err != nil {
		t.Fatalf("GetActiveSubagent: %v", err)
	}
	if active != nil {
		t.Fatalf("expected no binding, got subagent_id=%q", active.SubagentID)
	}

	// And the principal's write is clean even without an explicit
	// agent_id, because there is no binding to capture it.
	w, err := orch.AgentMemorySave(ctx, AgentMemorySaveInput{
		Operator: "tester",
		Kind:     "note",
		Content:  "write with no binding around",
	})
	if err != nil {
		t.Fatalf("AgentMemorySave: %v", err)
	}
	if w.Row.AgentID != "" {
		t.Fatalf("expected no agent_id, got %q", w.Row.AgentID)
	}
}

// TestC2Binding_LiveBindingsAreVisible proves the visibility fix: the
// orphan binding that causes the trap is now reported at session start
// and at session close, instead of being invisible.
func TestC2Binding_LiveBindingsAreVisible(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newC2TestOrchestrator(t, ctx, "tester")

	if _, err := orch.AgentMemoryDelegate(ctx, AgentMemoryDelegateInput{
		Operator:        "tester",
		SubagentID:      "sub-orphan",
		TaskDescription: "left behind on purpose",
	}); err != nil {
		t.Fatalf("AgentMemoryDelegate: %v", err)
	}

	// session_start reports the live binding.
	started, err := orch.SessionStart(ctx, SessionStartInput{
		Operator:  "tester",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if len(started.ActiveSubagentIDs) != 1 || started.ActiveSubagentIDs[0] != "sub-orphan" {
		t.Fatalf("session_start must report the live binding, got %v", started.ActiveSubagentIDs)
	}

	// session_close reports it too (without closing it — a background
	// subagent may still be writing).
	closed, err := orch.SessionClose(ctx, SessionCloseInput{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("SessionClose: %v", err)
	}
	if len(closed.ActiveSubagentIDs) != 1 || closed.ActiveSubagentIDs[0] != "sub-orphan" {
		t.Fatalf("session_close must report the live binding, got %v", closed.ActiveSubagentIDs)
	}

	// It is still alive (we did not silently break isolation).
	active, err := orch.Store.GetActiveSubagent(ctx, "tester")
	if err != nil {
		t.Fatalf("GetActiveSubagent: %v", err)
	}
	if active == nil {
		t.Fatalf("session_close must NOT close the binding")
	}
}

// TestC2Binding_ListActiveSubagentsEnumerate checks the store method
// that backs the visibility: it enumerates ALL live bindings, not just
// the most recent one (GetActiveSubagent is LIMIT 1).
//
// The expired-row filter is defense-in-depth (the sweeper reaps those
// at session_start); it is not exercised here because the store
// clamps ttl_seconds to a 3600s minimum, so an already-expired row
// cannot be created through the public API.
func TestC2Binding_ListActiveSubagentsEnumerate(t *testing.T) {
	t.Setenv("DARK_MEMORY_V280", "1")
	ctx := context.Background()
	orch := newC2TestOrchestrator(t, ctx, "tester")

	for _, id := range []string{"sub-a", "sub-b"} {
		if _, err := orch.AgentMemoryDelegate(ctx, AgentMemoryDelegateInput{
			Operator:        "tester",
			SubagentID:      id,
			TaskDescription: "two bindings at once",
		}); err != nil {
			t.Fatalf("AgentMemoryDelegate(%s): %v", id, err)
		}
	}

	all, err := orch.Store.ListActiveSubagents(ctx, "tester")
	if err != nil {
		t.Fatalf("ListActiveSubagents: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 live bindings, got %d", len(all))
	}

	// Newest first: the last delegation registered is the first row.
	if all[0].SubagentID != "sub-b" {
		t.Fatalf("expected newest binding first, got %q", all[0].SubagentID)
	}

	// A different operator sees none of them (bindings are per-operator).
	none, err := orch.Store.ListActiveSubagents(ctx, "other-operator")
	if err != nil {
		t.Fatalf("ListActiveSubagents: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no bindings for another operator, got %d", len(none))
	}
}
