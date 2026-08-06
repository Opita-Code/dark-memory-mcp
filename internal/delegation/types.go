// Package delegation implements the DelegationRouter — the Wave 5C
// component that decides whether an intent is handled by the LLM
// itself, delegated to sub-agents, or refused (A1: Memory decides).
//
// The architectural thesis (vibe-flow/main/DELEGATION_ARCHITECTURE.md):
// delegation is NOT "passing context in the prompt". It is:
//
//  1. CURATE — which subset of agent_memory the subagent inherits
//     (via agent_memory_delegate).
//  2. ARM — the correct mindset for the task (via mindset_apply).
//  3. ISOLATE — the subagent with a C2 binding (subagent_register)
//     so its writes never pollute the principal's ContextRecap.
//  4. RECALL — the subagent's findings via agent_memory recall,
//     not via a return message (the handoff survives session close).
//  5. SYNTHESIZE — from persistent memory, not from chat history.
//
// The handoff substrate is agent_memory (the DB), not the chat
// history. This package defines the wire types shared by the router,
// the orchestrator, and the audit layer.
package delegation

import (
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// errInvalidPlan wraps a plan-validation failure with a formatted
// message. Errors from Plan.Validate() wrap this sentinel so callers
// can branch with errors.Is.
var errInvalidPlan = errors.New("delegation: invalid plan")

// invalidPlanErrorf formats a validation failure and wraps the
// errInvalidPlan sentinel.
func invalidPlanErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidPlan, fmt.Sprintf(format, args...))
}

// Handler is the A1 outcome for an intent: does the LLM handle it,
// delegate it to sub-agents, or refuse it?
type Handler string

const (
	// HandlerHandle means the orchestrator executes the task itself,
	// without spawning sub-agents. The router returns this for
	// non-delegable cases (short tasks, no capability gaps).
	HandlerHandle Handler = "HANDLE"

	// HandlerDelegate means the orchestrator spawns sub-agents per
	// the returned Plan. The sub-agents receive a curated
	// agent_memory view (not the parent's chat history).
	HandlerDelegate Handler = "DELEGATE"

	// HandlerRefuse means the intent cannot be satisfied with the
	// current capability set. The orchestrator refuses with
	// ErrCapabilityNotGranted semantics (A1: "se rehúsa").
	HandlerRefuse Handler = "REFUSE"
)

// SubTask is one unit of delegated work in a Plan. Each subtask
// carries its own vibe_case (which may differ from the parent's —
// a C7 mixed parent can decompose into C1/C2/C3 subtasks), the
// task description that seeds mindset composition, and the
// dependency edges for topological batching.
type SubTask struct {
	ID           string   `json:"id"`
	VibeCase     string   `json:"vibe_case"`
	Task         string   `json:"task"`
	DependsOn    []string `json:"depends_on,omitempty"`
	ModelFloor   string   `json:"model_floor,omitempty"`
	SubagentID   string   `json:"subagent_id,omitempty"`
	MindsetID    int64    `json:"mindset_id,omitempty"` // 0 = procedural composition
	Tools        []string `json:"tools_recommended,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"` // set by MIND stage
}

// Plan is the ordered subtask graph produced by the PLAN stage.
// Batches are computed from DependsOn via topological ordering;
// tasks in the same batch can run in parallel (supervisor pattern).
type Plan struct {
	Subtasks []SubTask `json:"subtasks"`
}

// Batch groups subtasks that can run concurrently (no dependency
// edges between them). Batch index i may depend on batches < i.
func (p *Plan) Batch(dep map[string][]string) [][]SubTask {
	// Build a resolved-set topological batching. Tasks whose
	// DependsOn is empty (or all deps resolved) go in the current
	// batch; the rest wait for later batches.
	resolved := map[string]bool{}
	var batches [][]SubTask
	remaining := make([]SubTask, len(p.Subtasks))
	copy(remaining, p.Subtasks)

	for len(remaining) > 0 {
		var batch []SubTask
		var next []SubTask
		for _, st := range remaining {
			ready := true
			for _, d := range st.DependsOn {
				if !resolved[d] {
					ready = false
					break
				}
			}
			if ready {
				batch = append(batch, st)
			} else {
				next = append(next, st)
			}
		}
		// Mark the batch's ids resolved ONLY after the pass
		// completes — a task in the same batch must not satisfy
		// another task's dependency within the same batch (that
		// would collapse sequential pipelines into one batch).
		for _, st := range batch {
			resolved[st.ID] = true
		}
		if len(batch) == 0 {
			// Circular dependency — treat remaining as one batch
			// (defensive; the spec validator rejects cycles, but a
			// caller could construct a Plan by hand).
			return append(batches, remaining)
		}
		batches = append(batches, batch)
		remaining = next
	}
	return batches
}

// DelegationDecision is the router's output (DECIDE stage). The
// handler is the A1 outcome; the plan is non-nil only when
// handler == HandlerDelegate. Reasoning carries the decision trail
// for audit.
type DelegationDecision struct {
	Handler   Handler `json:"handler"`
	Reasoning string  `json:"reasoning"`
	Plan      *Plan   `json:"plan,omitempty"`
	Case      string  `json:"case"`
}

// DelegationContext is the CURATE stage output: the curated
// markdown block the parent embeds in the subagent's task prompt
// plus the counters that verify what was included. Mirrors
// AgentMemoryDelegateOutput's meaningful fields so the router can
// pass it through without depending on the orchestration package.
type DelegationContext struct {
	SubagentID        string `json:"subagent_id"`
	DelegationContext string `json:"delegation_context"`
	PinnedCount       int    `json:"pinned_count"`
	TodoCount         int    `json:"todo_count"`
	Truncated         bool   `json:"truncated"`
}

// Validate checks a Plan for structural integrity: unique subtask
// ids, valid vibe_cases, and no self-references in DependsOn. The
// vibe_spec validator performs the same checks on spec tasks; this
// guard keeps hand-constructed plans honest.
func (p *Plan) Validate() error {
	seen := map[string]bool{}
	for _, st := range p.Subtasks {
		if st.ID == "" {
			return invalidPlanErrorf("subtask id must not be empty")
		}
		if seen[st.ID] {
			return invalidPlanErrorf("duplicate subtask id %q", st.ID)
		}
		seen[st.ID] = true
		if st.VibeCase != "" {
			if _, err := vibecase.Parse(st.VibeCase); err != nil {
				return invalidPlanErrorf("subtask %q: %v", st.ID, err)
			}
		}
		for _, d := range st.DependsOn {
			if d == st.ID {
				return invalidPlanErrorf("subtask %q depends on itself", st.ID)
			}
		}
	}
	// Every depends_on target must exist.
	for _, st := range p.Subtasks {
		for _, d := range st.DependsOn {
			if !seen[d] {
				return invalidPlanErrorf("subtask %q depends on unknown %q", st.ID, d)
			}
		}
	}
	return nil
}
