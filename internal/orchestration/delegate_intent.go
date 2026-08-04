// Package orchestration — delegate_intent.go
//
// Wave 5C: dark_memory_delegate_intent orchestrator. This is the
// entry point that closes the delegation gap: the harness asks
// "should I delegate this, and if so, how?" and the orchestrator
// runs the DelegationRouter's 5-stage pipeline:
//
//	DECIDE  → DelegationRouter.Decide (A1: HANDLE | DELEGATE | REFUSE)
//	PLAN    → topological batching of subtasks (MVP: single bundle)
//	MIND    → mindset_apply per subtask (system_prompt + tools + model)
//	CURATE  → agent_memory_delegate per subtask (C2 binding + curated context)
//	AUDIT   → (post-execution, not in this call) delegation.RecallSubagentFindings
//
// The orchestrator does NOT spawn sub-agents (the harness owns the
// spawn tool). It returns everything the harness needs to spawn them:
// the mindset (system_prompt), the curated delegation context, and
// the model/tools recommendation — plus the subagent C2 binding id
// so the harness can inject it into its Task() call.
//
// Wire contract:
//   - input  : { vibe_case, task_description, operator? }
//   - output : { handler, reasoning, plan: [{id, vibe_case, task,
//                system_prompt, delegation_context, subagent_id,
//                tools_recommended, model_recommended, depends_on}] }
//
// Gated by DARK_MEMORY_V280=1 (same gate as mindset_apply's
// spawn_subagent path and agent_memory_delegate). When the flag is
// off, returns ErrInvalidState with a clear message.

package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/delegation"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// DelegateIntentInput is the request to decide whether an intent
// should be handled, delegated, or refused.
type DelegateIntentInput struct {
	VibeCase        string `json:"vibe_case"`
	TaskDescription string `json:"task_description"`
	Operator        string `json:"operator,omitempty"`
}

// DelegateSubTaskOutput is one ready-to-spawn unit of the plan:
// the mindset + curated context + C2 binding for ONE sub-agent.
type DelegateSubTaskOutput struct {
	ID                 string   `json:"id"`
	VibeCase           string   `json:"vibe_case"`
	Task               string   `json:"task"`
	SystemPrompt       string   `json:"system_prompt,omitempty"`
	DelegationContext  string   `json:"delegation_context,omitempty"`
	SubagentID         string   `json:"subagent_id,omitempty"`
	ToolsRecommended   []string `json:"tools_recommended,omitempty"`
	ModelRecommended   string   `json:"model_recommended,omitempty"`
	DependsOn          []string `json:"depends_on,omitempty"`
	PinnedCount        int      `json:"pinned_count,omitempty"`
	TodoCount          int      `json:"todo_count,omitempty"`
}

// DelegateIntentOutput is the router's decision + the ready-to-spawn
// plan (nil plan when handler != DELEGATE).
type DelegateIntentOutput struct {
	Handler   string                 `json:"handler"`
	Reasoning string                 `json:"reasoning"`
	Case      string                 `json:"case"`
	Plan      []DelegateSubTaskOutput `json:"plan,omitempty"`
	Operator  string                 `json:"operator"`
}

// DelegateIntent runs the DECIDE → PLAN → MIND → CURATE pipeline for
// one intent. It does NOT spawn sub-agents; it returns the
// ready-to-inject material the harness needs for its own spawn.
//
// Error contract:
//   - ErrInvalidState when DARK_MEMORY_V280 is off.
//   - errMissingField("vibe_case"|"task_description") on missing input.
//   - ErrInvalidArgument when vibe_case fails vibecase.Parse.
//   - ErrInvalidState when no active project or session.
func (o *Orchestrator) DelegateIntent(ctx context.Context, in DelegateIntentInput) (*DelegateIntentOutput, error) {
	if !v280Enabled() {
		return nil, fmt.Errorf("delegate_intent: %w (DARK_MEMORY_V280 not enabled)", store.ErrInvalidState)
	}
	if strings.TrimSpace(in.VibeCase) == "" {
		return nil, errMissingField("vibe_case")
	}
	if strings.TrimSpace(in.TaskDescription) == "" {
		return nil, errMissingField("task_description")
	}
	c, err := vibecase.Parse(in.VibeCase)
	if err != nil {
		return nil, err
	}

	// Active project + session required (same contract as
	// agent_memory_delegate; the gate enforces it on the wire, this
	// check keeps direct-orchestrator test paths honest).
	projID := o.Store.ActiveProject()
	if projID == "" {
		return nil, fmt.Errorf("delegate_intent: %w: no active project", store.ErrInvalidState)
	}
	sessID, err := o.Store.GetActiveSession(ctx, projID)
	if err != nil || sessID == "" {
		return nil, fmt.Errorf("delegate_intent: %w: no active session", store.ErrInvalidState)
	}

	operator := in.Operator
	if strings.TrimSpace(operator) == "" {
		operator = o.activeOperator(ctx)
	}
	if strings.TrimSpace(operator) == "" {
		operator = "orchestrator_delegate"
	}

	// DECIDE — the DelegationRouter (stateless, pure).
	router := delegation.NewRouter()
	decision := router.Decide(delegation.RouterInput{
		Case: c,
		Task: in.TaskDescription,
	})

	out := &DelegateIntentOutput{
		Handler:   string(decision.Handler),
		Reasoning: decision.Reasoning,
		Case:      decision.Case,
		Operator:  operator,
	}

	// PLAN + MIND + CURATE — only when the router decided DELEGATE.
	if decision.Plan != nil {
		if err := decision.Plan.Validate(); err != nil {
			return nil, fmt.Errorf("delegate_intent: router returned invalid plan: %w", err)
		}
		for _, st := range decision.Plan.Subtasks {
			sub, err := o.prepareSubTask(ctx, st, operator)
			if err != nil {
				return nil, fmt.Errorf("delegate_intent: prepare subtask %q: %w", st.ID, err)
			}
			out.Plan = append(out.Plan, *sub)
		}
	}

	return out, nil
}

// prepareSubTask runs MIND (mindset_apply) + CURATE
// (agent_memory_delegate) for one subtask and returns the
// ready-to-spawn output.
//
// Both calls are best-effort-tolerant in a specific sense: a
// mindset_apply failure (e.g. no LLM key) returns the subtask with
// an empty system_prompt rather than failing the whole delegation —
// the harness can still spawn with its default prompt. An
// agent_memory_delegate failure is likewise surfaced as an empty
// delegation_context (no C2 binding) instead of a hard error, so
// the harness's delegation loop never breaks (mirrors
// mindset_apply's failure-mode contract).
func (o *Orchestrator) prepareSubTask(ctx context.Context, st delegation.SubTask, operator string) (*DelegateSubTaskOutput, error) {
	sub := &DelegateSubTaskOutput{
		ID:        st.ID,
		VibeCase:  st.VibeCase,
		Task:      st.Task,
		DependsOn: st.DependsOn,
	}

	// MIND — compose the mindset for this subtask's vibe_case + task.
	mindset, err := o.MindsetApply(ctx, MindsetApplyInput{
		VibeCase:        st.VibeCase,
		TaskDescription: st.Task,
		Operator:        operator,
	})
	if err != nil {
		// Best-effort: no system_prompt, but don't fail the plan.
		sub.SystemPrompt = ""
	} else {
		sub.SystemPrompt = mindset.SystemPrompt
		sub.ToolsRecommended = mindset.ToolsRecommended
		sub.ModelRecommended = mindset.ModelRecommended
	}

	// CURATE — prepare the delegation context + register the C2
	// binding. The subagent_id is generated here (opaque uuid),
	// stable for this subtask id.
	subagentID := st.SubagentID
	if subagentID == "" {
		subagentID = "sub-" + st.ID
	}
	sub.SubagentID = subagentID

	delegateOut, err := o.AgentMemoryDelegate(ctx, AgentMemoryDelegateInput{
		Operator:        operator,
		SubagentID:      subagentID,
		TaskDescription: st.Task,
	})
	if err != nil {
		// Best-effort: no delegation context, no C2 binding. The
		// harness still gets the mindset + subagent_id.
		sub.DelegationContext = ""
	} else {
		sub.DelegationContext = delegateOut.DelegationContext
		sub.PinnedCount = delegateOut.PinnedCount
		sub.TodoCount = delegateOut.TodoCount
	}

	return sub, nil
}
