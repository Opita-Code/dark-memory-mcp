// subagent_binding.go — Phase 7 alpha.19 Chunk 7.2: CURATE step for
// delegate_intent. Per SPEC-alpha-11-phase7.md §3.2, after MIND we
// register a subagent binding per subtask via agent_memory.Save
// (kind=link, tag=subagent:v1). Each row is the durable record of
// "this subagent was delegated from this parent session".
//
// Defense-in-depth vs arxiv:2605.08460 (subagent inheritance attack):
// the row stores (operator, parent_session_id, parent_agent_id,
// subtask_index, subagent_id). A compromised subagent cannot forge
// another subagent's binding because agent_memory.Save enforces
// INV-1 (operator non-empty) AND audit (Actor non-empty) atomically
// inside one transaction — see agent_memory.go:177-181. A forged
// write would fail at the audit.Meta validation step before the
// binding row is persisted.
//
// Note on v2 vs v4alpha: the v2 surface (internal/orchestration/
// subagent.go:64) maintains a dedicated active_subagents table with
// TTL semantics. v4alpha piggybacks on agent_memory (kind=link,
// tag=subagent:v1, expires_at in content). The two implementations
// will converge in alpha.20+ when we port the v2 orchestration
// layer into v4alpha (per SPEC-alpha-11-phase7.md §4).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
)

// SubagentBinding is the durable state for one delegated subagent.
// Persisted as an agent_memory row (kind=link, tag=subagent:v1).
// The JSON marshalling is stable for v4alpha so callers can inspect
// content with json.Unmarshal.
type SubagentBinding struct {
	SubagentID      string `json:"subagent_id"`
	ParentSessionID string `json:"parent_session_id"`
	ParentAgentID   string `json:"parent_agent_id"`
	ProjectID       string `json:"project_id"`
	SubtaskIndex    int    `json:"subtask_index"`
	SubtaskID       string `json:"subtask_id"`
	SubtaskDesc     string `json:"subtask_desc"`
	TaskID          string `json:"task_id"`
	Operator        string `json:"operator"`
	DelegatedAt     string `json:"delegated_at"`
	ExpiresAt       string `json:"expires_at"`
}

// CURATE constants — the canonical tag prefix + ttl bounds.
const (
	// SubagentBindingKind is the agent_memory.kind value used for
	// binding rows. "link" is canonical for cross-reference rows
	// per spec/agent_memory.md §3.2.
	SubagentBindingKind = "link"

	// SubagentBindingTagPrefix is the tag prefix that scopes FTS5
	// RecallFiltered queries to binding rows only. All binding rows
	// carry this tag (along with parent_session_id, subtask_id,
	// operator scoping tags for defense-in-depth).
	SubagentBindingTagPrefix = "subagent:v1"

	// DefaultSubagentTTLSeconds is the default time-to-live for a
	// binding row (1 hour). Matches v2's SubagentRegister default.
	DefaultSubagentTTLSeconds = 3600

	// MinSubagentTTLSeconds / MaxSubagentTTLSeconds — TTL clamp
	// at bind time. Matches v2's clamp [60, 86400].
	MinSubagentTTLSeconds = 60
	MaxSubagentTTLSeconds = 86400
)

// BindSubtasksToSubagents is the CURATE pipeline step. For each
// subtask, generate a uuid subagent_id and persist a binding row.
// Returns the input subtasks with SubagentID + DelegationContext
// filled. The wire output then surfaces these to the caller (the
// subagent harness that spawns the actual subagent).
//
// Best-effort: if any individual binding fails, we continue and
// leave that subtask's SubagentID="" in the output (the caller
// can still proceed with the plan-defined IDs as fallback). The
// function returns an error ONLY for programmer errors (e.g.
// empty operator + memStore != nil).
//
// TTL clamp matches v2's clamp [60, 86400]. Default 3600 when
// ttlSeconds <= 0.
//
// Wire contract (per SPEC-alpha-11-phase7.md §3.2):
//
//	subtasks[].subagent_id        — opaque uuid from this function
//	subtasks[].delegation_context — JSON blob:
//	  {
//	    "parent_session_id": "sess-XXX" | "",
//	    "parent_agent_id":   "alpha-11-phase7" | "",
//	    "project_id":        "default",
//	    "subtask_index":     0..N,
//	    "subtask_id":        "subtask-1",
//	    "task_id":           "task-N" | "",
//	    "subagent_id":       "uuid",
//	    "delegated_at":      "2026-10-02T...",
//	    "expires_at":        "2026-10-02T..."
//	  }
func BindSubtasksToSubagents(
	ctx context.Context,
	memStore *agent_memory.Store,
	operator string,
	parentSessionID string,
	parentAgentID string,
	taskID string,
	subtasks []delegation.Subtask,
	ttlSeconds int,
) ([]delegation.Subtask, error) {
	// No-op when memories are not wired (test seam). Return
	// input unchanged.
	if memStore == nil {
		return subtasks, nil
	}
	if strings.TrimSpace(operator) == "" {
		return subtasks, fmt.Errorf("bindSubtasksToSubagents: operator is empty (INV-1)")
	}

	// TTL clamp [60, 86400]. Default 3600 when <= 0.
	if ttlSeconds <= 0 {
		ttlSeconds = DefaultSubagentTTLSeconds
	}
	if ttlSeconds < MinSubagentTTLSeconds {
		ttlSeconds = MinSubagentTTLSeconds
	}
	if ttlSeconds > MaxSubagentTTLSeconds {
		ttlSeconds = MaxSubagentTTLSeconds
	}

	now := time.Now().UTC()
	delegatedAt := now.Format(time.RFC3339Nano)
	expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second).Format(time.RFC3339Nano)

	out := make([]delegation.Subtask, len(subtasks))
	copy(out, subtasks)

	// auditMeta is INV-1 enforced by agent_memory.Save (rejects
	// empty Actor). SessionID/ProjectID are optional; we set
	// SessionID = parent_session_id so the audit_log row carries
	// the provenance for forensic queries.
	auditMeta := &agent_memory.Audit{
		Actor:     operator,
		SessionID: parentSessionID,
	}

	for i := range subtasks {
		subagentID := uuid.New().String()
		binding := SubagentBinding{
			SubagentID:      subagentID,
			ParentSessionID: parentSessionID,
			ParentAgentID:   parentAgentID,
			ProjectID:       "default",
			SubtaskIndex:    i,
			SubtaskID:       subtasks[i].ID,
			SubtaskDesc:     subtasks[i].Description,
			TaskID:          taskID,
			Operator:        operator,
			DelegatedAt:     delegatedAt,
			ExpiresAt:       expiresAt,
		}
		bindingJSON, err := json.Marshal(binding)
		if err != nil {
			// Marshal of a fixed struct is deterministic; an
			// error here indicates a programmer mistake.
			return nil, fmt.Errorf("bindSubtasksToSubagents: marshal: %w", err)
		}
		title := fmt.Sprintf("subagent-binding:%s:%s", subtasks[i].ID, subagentID)
		// Tags: prefix + delegation namespace + 3 cross-reference
		// tags (parent session, subtask, operator). The cross-
		// reference tags make RecallFiltered queries scoped by any
		// of these dimensions possible.
		tagList := []string{
			SubagentBindingTagPrefix,
			"delegation",
			"phase7-chunk7.2",
		}
		if parentSessionID != "" {
			tagList = append(tagList, "parent:"+parentSessionID)
		}
		if subtasks[i].ID != "" {
			tagList = append(tagList, "subtask:"+subtasks[i].ID)
		}
		tagList = append(tagList, "operator:"+operator)
		tags := strings.Join(tagList, ",")

		_, err = memStore.Save(ctx, auditMeta, operator, SubagentBindingKind,
			title, string(bindingJSON), tags, false)
		if err != nil {
			// Best-effort: leave SubagentID empty so the
			// caller surfaces the partial result with
			// verdict=errored. Continue with the next
			// subtask — one bad row shouldn't poison the
			// batch.
			continue
		}

		// Build delegation_context JSON blob. This is what
		// the spawned subagent reads on its first call so it
		// knows its provenance + ttl + parent.
		delegationCtx, err := json.Marshal(map[string]any{
			"parent_session_id": parentSessionID,
			"parent_agent_id":   parentAgentID,
			"project_id":        "default",
			"subtask_index":     i,
			"subtask_id":        subtasks[i].ID,
			"task_id":           taskID,
			"subagent_id":       subagentID,
			"delegated_at":      delegatedAt,
			"expires_at":        expiresAt,
		})
		if err != nil {
			continue
		}
		out[i].SubagentID = subagentID
		out[i].DelegationContext = string(delegationCtx)
	}

	return out, nil
}

// UnregisterSubagent archives the binding row(s) for a subagent_id
// (soft-delete via agent_memory.Archive, which emits a DELETE audit
// row in the same transaction). Returns (true, nil) if at least
// one row was archived, (false, nil) if no matching binding.
//
// Lookup strategy: query by subagent:v1 tag prefix, then filter
// results client-side by subagent_id match in the JSON content.
// The FTS5 index on content makes the content filter efficient
// even at scale.
//
// Idempotent: archiving a missing subagent_id is not an error
// (returns false, nil). The caller can use this to clean up
// stale bindings on session close.
func UnregisterSubagent(
	ctx context.Context,
	memStore *agent_memory.Store,
	operator string,
	subagentID string,
) (bool, error) {
	if memStore == nil {
		return false, nil
	}
	if strings.TrimSpace(operator) == "" {
		return false, fmt.Errorf("unregisterSubagent: operator is empty (INV-1)")
	}
	if strings.TrimSpace(subagentID) == "" {
		return false, fmt.Errorf("unregisterSubagent: subagent_id is empty")
	}

	// Query: tag prefix scopes to binding rows; query term
	// "subagent" hits the FTS5 index which tokenizes the
	// subagent_id into its dash-separated parts. Then we
	// filter client-side for exact subagent_id match in
	// content JSON (defense against partial FTS5 matches).
	rows, err := memStore.RecallFiltered(ctx, operator,
		"subagent",
		agent_memory.RecallFilter{TagPrefix: SubagentBindingTagPrefix},
		100)
	if err != nil {
		return false, fmt.Errorf("unregisterSubagent: recall: %w", err)
	}

	auditMeta := &agent_memory.Audit{
		Actor:     operator,
	}

	archived := 0
	for _, r := range rows {
		var b SubagentBinding
		if err := json.Unmarshal([]byte(r.Content), &b); err != nil {
			continue
		}
		if b.SubagentID != subagentID {
			continue
		}
		if err := memStore.Archive(ctx, auditMeta, r.ID); err != nil {
			return archived > 0, fmt.Errorf("unregisterSubagent: archive %d: %w", r.ID, err)
		}
		archived++
	}
	return archived > 0, nil
}

// SubagentBindingFilter is a thin convenience wrapper that returns
// all live binding rows for (operator, parent_session_id). Used by
// session.Store.Close cleanup paths (alpha.20 will wire this).
func SubagentBindingFilter(parentSessionID string) agent_memory.RecallFilter {
	return agent_memory.RecallFilter{TagPrefix: SubagentBindingTagPrefix}
}

// UnregisterSubagentForTest is a thin test seam that re-exports
// UnregisterSubagent for tests in package mcp_test. Returns the
// (archived, nil) tuple directly (the unexported version also
// returns an error which tests can ignore when not relevant).
//
// Mirrors mcp_unregister_subagent semantics for v4alpha.
func UnregisterSubagentForTest(ctx context.Context, memStore *agent_memory.Store, operator, subagentID string) bool {
	ok, _ := UnregisterSubagent(ctx, memStore, operator, subagentID)
	return ok
}