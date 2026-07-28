// Package dual_driver_test: v2.4.1 Store-level additive agent_id
// filter test.
//
// The drift_judge evaluation 418 flagged that the orchestrator-
// level tests in tests/orchestration/agent_memory_v2_4_1_test.go
// only verify the resolveActiveAgentID helper, NOT the additive
// agent_id filter at the Store layer (Store.ListAgentMemory). The
// original cross-agent leakage failure mode was at the Store layer:
// v2.3.0 only applied agent_id when scope=agent, so v2.4.0's
// project-wide scope queries leaked. v2.4.1 makes agent_id an
// ADDITIVE filter that applies regardless of scope.
//
// This test directly exercises Store.ListAgentMemory to verify:
//   - When Scope=ScopeProject and AgentID="X", only X's rows return.
//   - When Scope=ScopeProject and AgentID="", all rows return
//     (v2.4.0 backward compat).
//   - When Scope=ScopeAgent and AgentID="X", only X's rows return
//     (existing v2.3.0 behavior preserved).
//
// This is the end-to-end Store-level verification of the additive
// filter — the structural fix that closes the cross-agent leakage
// debt at the data plane.
package dual_driver_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// v241StoreLevelOrch returns an orch + store backed by a fresh SQLite
// DB with migrations applied + the 'acme' project as active. Same
// pattern as the orchestration-level v2.4.1 tests but exercised at
// the Store.ListAgentMemory layer.
func v241StoreLevelOrch(t *testing.T) (*orchestration.Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "v241-store.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateProject(context.Background(), &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.SetActiveProject(context.Background(), "acme"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	return orchestration.New(s, nil), s
}

// v241StoreSave seeds an agent_memory row directly via the
// orchestrator (so INV-1 audit row is emitted). Returns the row id.
func v241StoreSave(t *testing.T, orch *orchestration.Orchestrator, operator, kind, content, agentID string) int64 {
	t.Helper()
	out, err := orch.AgentMemorySave(context.Background(), orchestration.AgentMemorySaveInput{
		Operator: operator,
		Kind:     kind,
		Content:  content,
		AgentID:  agentID,
	})
	if err != nil {
		t.Fatalf("save %s: %v", kind, err)
	}
	return out.Row.ID
}

// TestV241_Store_ListAgentMemory_AdditiveAgentIDFilter is the
// Store-level end-to-end verification of the v2.4.1 additive
// agent_id filter. Three assertions:
//
//  1. Scope=Project + AgentID="gpt-4o" returns ONLY gpt-4o's rows
//     (the v2.4.1 fix — v2.3.0 would have returned all rows).
//  2. Scope=Project + AgentID="" returns all rows (v2.4.0 backward
//     compat preserved).
//  3. Scope=Agent + AgentID="gpt-4o" returns ONLY gpt-4o's rows
//     (v2.3.0 behavior preserved).
//
// This is the structural invariant the drift_judge evaluation 418
// asked for: the additive filter at the Store layer prevents the
// cross-agent leakage that v2.4.0 introduced.
func TestV241_Store_ListAgentMemory_AdditiveAgentIDFilter(t *testing.T) {
	orch, st := v241StoreLevelOrch(t)
	ctx := context.Background()

	// Seed: claude-sonnet-4.5's pinned + gpt-4o's pinned + claude's
	// unpinned note + gpt's unpinned note. Two agents, mixed
	// pinned/non-pinned — the additive filter should apply on
	// ALL rows, not just pinned.
	idClaudePinned := v241StoreSave(t, orch, "alice", "decision",
		"claude's pinned decision in the additive-filter test", "claude-sonnet-4.5")
	idGPTPinned := v241StoreSave(t, orch, "alice", "decision",
		"gpt's pinned decision in the additive-filter test", "gpt-4o")
	idClaudeNote := v241StoreSave(t, orch, "alice", "note",
		"claude's unpinned note in the additive-filter test", "claude-sonnet-4.5")
	idGPTNote := v241StoreSave(t, orch, "alice", "note",
		"gpt's unpinned note in the additive-filter test", "gpt-4o")
	_ = idClaudePinned
	_ = idClaudeNote

	// Pin the pinned rows so the filter test exercises both pinned
	// and unpinned.
	pin := true
	for _, id := range []int64{idClaudePinned, idGPTPinned} {
		if _, err := st.UpdateAgentMemory(ctx, store.WriteContext{Actor: "test", WritePath: "v241_store_test"}, id, &agentmemory.AgentMemoryUpdate{Pinned: &pin}); err != nil {
			t.Fatalf("pin %d: %v", id, err)
		}
	}

	// Sanity: at least 4 rows exist in the project.
	all, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope: agentmemory.ScopeProject,
	})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) < 4 {
		t.Fatalf("expected >= 4 seeded rows; got %d", len(all))
	}

	// Assertion 1: Scope=Project + AgentID="gpt-4o" returns ONLY gpt-4o's rows.
	gptOnly, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:   agentmemory.ScopeProject,
		AgentID: "gpt-4o",
	})
	if err != nil {
		t.Fatalf("list gpt-only (project scope): %v", err)
	}
	if len(gptOnly) != 2 {
		t.Errorf("Scope=Project AgentID=gpt-4o: got %d rows, want 2 (the two gpt-4o rows). "+
			"Cross-agent leakage not prevented at the Store layer.",
			len(gptOnly))
	}
	for _, r := range gptOnly {
		if r.AgentID != "gpt-4o" {
			t.Errorf("Scope=Project AgentID=gpt-4o leaked row with AgentID=%q", r.AgentID)
		}
	}

	// Assertion 2: Scope=Project + AgentID="" returns ALL rows
	// (v2.4.0 backward compat preserved).
	allAgain, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope: agentmemory.ScopeProject,
		// AgentID intentionally empty — v2.4.0 project-wide.
	})
	if err != nil {
		t.Fatalf("list all (empty agent_id): %v", err)
	}
	if len(allAgain) != len(all) {
		t.Errorf("Scope=Project AgentID=\"\": got %d rows, want %d (project-wide backward compat)",
			len(allAgain), len(all))
	}

	// Assertion 3: Scope=Agent + AgentID="gpt-4o" returns ONLY
	// gpt-4o's rows (v2.3.0 behavior preserved).
	gptOnlyAgent, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:   agentmemory.ScopeAgent,
		AgentID: "gpt-4o",
	})
	if err != nil {
		t.Fatalf("list gpt-only (agent scope): %v", err)
	}
	if len(gptOnlyAgent) != 2 {
		t.Errorf("Scope=Agent AgentID=gpt-4o: got %d rows, want 2 (v2.3.0 behavior)",
			len(gptOnlyAgent))
	}
	for _, r := range gptOnlyAgent {
		if r.AgentID != "gpt-4o" {
			t.Errorf("Scope=Agent AgentID=gpt-4o leaked row with AgentID=%q", r.AgentID)
		}
	}

	// Reference unused locals to silence linter.
	_ = idGPTNote
}
