// Package delegation — audit.go: the AUDIT stage of the
// DelegationRouter (Wave 5C).
//
// After the sub-agents finish, the orchestrator must NOT rely on
// chat-history return messages: the handoff substrate is
// agent_memory (the DB). This file provides the post-delegation
// audit that RECALLS what each sub-agent persisted (findings,
// decisions, observations) via the store, tagged by subagent_id.
//
// Per vibe-flow/main/DELEGATION_ARCHITECTURE.md §2.5:
//
//	1. per batch (topological order): spawn, wait, AUDIT
//	2. SYNTHESIZE: compose the final output from persisted findings
//	   (the orchestrator may compact, close, or crash — the trail
//	   survives in agent_memory)
package delegation

import (
	"context"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// SubagentFindings is the consolidated audit result for one
// sub-agent: everything it persisted under tag "subagent-{id}".
type SubagentFindings struct {
	SubagentID  string                      `json:"subagent_id"`
	Findings    []agentmemory.AgentMemory   `json:"findings"`
	Decisions   []agentmemory.AgentMemory   `json:"decisions"`
	Observations []agentmemory.AgentMemory  `json:"observations"`
	Total       int                         `json:"total"`
}

// TagForSubagent returns the canonical agent_memory tag used by a
// sub-agent's writes: "subagent-{id}". The orchestrator (or the
// sub-agent harness) must include this tag in every
// agent_memory_save so the AUDIT stage can recall it.
func TagForSubagent(subagentID string) string {
	return "subagent-" + subagentID
}

// AuditRecaller is the read-side contract the AUDIT stage needs.
// Implemented by store.Store; abstracted here so tests can use a
// lightweight fake.
type AuditRecaller interface {
	// ListAgentMemory returns rows matching the filters in the
	// active project (INV-7 enforced by the store).
	ListAgentMemory(ctx context.Context, f agentmemory.AgentMemoryListFilters) ([]agentmemory.AgentMemory, error)
}

// RecallSubagentFindings audits ONE sub-agent's persisted output.
// It queries agent_memory for kind=finding, kind=decision, and
// kind=observation rows tagged with "subagent-{id}".
//
// The store enforces INV-7 (active project), so a sub-agent whose
// writes landed in a different project is invisible here — correct
// isolation semantics.
//
// Missing rows are not an error: a sub-agent that persisted nothing
// returns an empty SubagentFindings with Total=0.
func RecallSubagentFindings(ctx context.Context, r AuditRecaller, subagentID string) (*SubagentFindings, error) {
	if subagentID == "" {
		return nil, fmt.Errorf("delegation: RecallSubagentFindings: empty subagent_id")
	}
	tag := TagForSubagent(subagentID)

	out := &SubagentFindings{SubagentID: subagentID}

	findings, err := r.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Kind: "finding",
		Tag:  tag,
	})
	if err != nil {
		return nil, fmt.Errorf("delegation: recall findings for %s: %w", subagentID, err)
	}
	out.Findings = findings

	decisions, err := r.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Kind: "decision",
		Tag:  tag,
	})
	if err != nil {
		return nil, fmt.Errorf("delegation: recall decisions for %s: %w", subagentID, err)
	}
	out.Decisions = decisions

	observations, err := r.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Kind: "observation",
		Tag:  tag,
	})
	if err != nil {
		return nil, fmt.Errorf("delegation: recall observations for %s: %w", subagentID, err)
	}
	out.Observations = observations

	out.Total = len(findings) + len(decisions) + len(observations)
	return out, nil
}

// RecallerFromStore adapts store.Store to AuditRecaller. The
// orchestrator constructs this once and passes it to
// RecallSubagentFindings; tests can pass a fake.
type RecallerFromStore struct {
	Store store.Store
}

// ListAgentMemory delegates to the underlying store.
func (r RecallerFromStore) ListAgentMemory(ctx context.Context, f agentmemory.AgentMemoryListFilters) ([]agentmemory.AgentMemory, error) {
	return r.Store.ListAgentMemory(ctx, f)
}
