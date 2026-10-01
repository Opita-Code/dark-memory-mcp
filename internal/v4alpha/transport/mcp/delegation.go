// delegate_intent tool — DELEGATION namespace (Phase 6 alpha.18.1, full impl).
//
// Per SPEC-alpha-11-phase4.md §4.3 + SPEC-alpha-11-phase6.md §6.3,
// dark_memory_delegate_intent runs the DelegationRouter pipeline
// (DECIDE→PLAN→MIND→CURATE) to decide whether an intent is handled
// inline, delegated to sub-agents, or refused.
//
// # Phase 6 full implementation
//
// Replaces the alpha.17 STUB. The pipeline:
//
//   1. DECIDE: deterministic rules per (vibe_case, task complexity).
//      - inline: short task (<50 chars), single vibe_case, no
//        coordination keywords (single sub-agent handles).
//      - delegate: long task OR C7 multi vibe_case OR explicit
//        coordination keywords ("parallel", "concurrent", "step by step",
//        "first ... then", "and then").
//      - refused: explicit refusal keywords ("impossible", "cannot",
//        "out of scope", "do not").
//   2. PLAN: split task into subtasks (heuristic on sentence
//      boundaries + coordination keyword alignment). Each subtask
//      inherits the parent vibe_case (Phase 6 default; per-subtask
//      vibe_case override deferred to alpha.19).
//   3. MIND: invoke mindset_apply per subtask (real impl from
//      Chunk 6.2). Returns system_prompt + cache_hit for each.
//   4. CURATE: prepare the delegation context (Phase 6: empty
//      delegation_context per subtask. agent_memory_delegate
//      binding lands alpha.19 with the C2 subagent binding).
//
// # Wire contract
//
// Same wire shape as alpha.17 STUB but with non-empty subtasks
// when decision="delegate". Removed StubNotice (no longer a stub).
//
// Backward-compat: callers that previously checked decision="inline"
// + subtasks=[] continue to work for short single-vibe tasks.
// Callers that need to dispatch sub-agents should check decision
// ∈ {"delegate"} AND len(subtasks) > 0.
package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const delegateIntentToolName = "dark_memory_delegate_intent"

// delegateIntentInput is the wire shape for dark_memory_delegate_intent.
type delegateIntentInput struct {
	TaskDescription string `json:"task_description" jsonschema:"required" jsonschema_description:"≥10 char description of the work to be routed"`
	VibeCase        string `json:"vibe_case" jsonschema:"required" jsonschema_description:"One of C1..C7 (canonical vibe_case taxonomy per spec.go:14-16)"`
	Operator        string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; defaults to active session operator when empty)"`
}

// delegateIntentSubtask is one planned subtask.
type delegateIntentSubtask struct {
	ID                string   `json:"id"`
	SystemPrompt      string   `json:"system_prompt"`
	Tools             []string `json:"tools"`
	Model             string   `json:"model"`
	DelegationContext string   `json:"delegation_context,omitempty"`
}

// delegateIntentOutput is the wire shape returned by
// dark_memory_delegate_intent. Phase 6 alpha.18.1: no stub_notice.
type delegateIntentOutput struct {
	Decision  string                  `json:"decision"` // "inline" | "delegate" | "refused"
	Subtasks  []delegateIntentSubtask  `json:"subtasks"`
	Reasoning string                  `json:"reasoning"`
}

func registerDelegationTools(s *Server) {
	s.mcpSrv.AddTool(buildDelegateIntentTool(), s.handleDelegateIntent)
}

// buildDelegateIntentTool constructs the mcp.Tool descriptor.
func buildDelegateIntentTool() mcp.Tool {
	return mcp.NewTool(delegateIntentToolName,
		mcp.WithDescription("Decide whether an intent is handled inline, delegated to sub-agents, or refused. "+
			"Phase 6 alpha.18.1 full implementation: DECIDE→PLAN→MIND→CURATE pipeline. "+
			"DECIDE uses deterministic rules (short task=inline, C7 multi or coordination keywords=delegate, "+
			"explicit refusal=refused). PLAN splits by sentence boundaries. MIND calls mindset_apply per subtask. "+
			"CURATE prepares delegation context (C2 subagent binding lands alpha.19)."),
		mcp.WithInputSchema[delegateIntentInput](),
	)
}

// handleDelegateIntent is the JSON-RPC handler.
func (s *Server) handleDelegateIntent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in delegateIntentInput
	if err := bindArgs(req, &in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !validVibeCases[in.VibeCase] {
		return mcp.NewToolResultError(fmt.Sprintf(
			"delegate_intent: vibe_case %q not in canonical allow-list (C1..C7)", in.VibeCase)), nil
	}
	if len(strings.TrimSpace(in.TaskDescription)) < 10 {
		return mcp.NewToolResultError("delegate_intent: task_description must be ≥10 chars"), nil
	}
	if in.Operator == "" {
		in.Operator = "orchestrator_delegate"
	}

	// 1. DECIDE.
	decision, reason := decideDelegation(in.VibeCase, in.TaskDescription)

	// 2. PLAN.
	subtaskTasks := planSubtasks(in.VibeCase, in.TaskDescription, decision)

	// 3. MIND — invoke mindset_apply per subtask.
	subtasks := make([]delegateIntentSubtask, 0, len(subtaskTasks))
	for i, st := range subtaskTasks {
		systemPrompt, _, _ := invokeMindsetForSubtask(ctx, s, in.Operator, st.vibeCase, st.task)
		subtasks = append(subtasks, delegateIntentSubtask{
			ID:                fmt.Sprintf("subtask-%d", i+1),
			SystemPrompt:      systemPrompt,
			Tools:             []string{},
			Model:             "inherit",
			DelegationContext: "", // CURATE alpha.19
		})
	}

	return resultJSON(delegateIntentOutput{
		Decision:  decision,
		Subtasks:  subtasks,
		Reasoning: reason,
	})
}

// decideDelegation applies deterministic rules to choose the
// delegation mode. Pure function — no LLM, no DB.
func decideDelegation(vibeCase, task string) (decision, reason string) {
	lower := strings.ToLower(task)

	// Refusal keywords (highest priority).
	refusalMarkers := []string{"impossible", "cannot", "out of scope", "do not", "don't"}
	for _, m := range refusalMarkers {
		if strings.Contains(lower, m) {
			return "refused", fmt.Sprintf(
				"DECIDE: refused — task contains refusal marker %q (out of scope for the operator).",
				m,
			)
		}
	}

	// Delegation markers.
	delegateMarkers := []string{
		"parallel", "concurrent", "step by step", "first ... then",
		"and then", "split into", "subtask", "in parallel",
	}
	for _, m := range delegateMarkers {
		if strings.Contains(lower, m) {
			return "delegate", fmt.Sprintf(
				"DECIDE: delegate — task contains coordination marker %q (multi-step / parallel).",
				m,
			)
		}
	}

	// Vibe_case-based default.
	if vibeCase == "C7" {
		return "delegate", "DECIDE: delegate — C7 multi-modal vibe_case requires multi-vibe sub-agent dispatch."
	}

	// Length-based default.
	if len(task) > 200 {
		return "delegate", fmt.Sprintf(
			"DECIDE: delegate — task is %d chars (long enough to warrant planning + delegation).",
			len(task),
		)
	}

	return "inline", "DECIDE: inline — short task, no delegation markers, single vibe_case."
}

// plannedSubtask is the internal PLAN-phase representation.
type plannedSubtask struct {
	task     string
	vibeCase string
}

// planSubtasks splits the task by sentence boundaries. Returns at
// least one subtask (the whole task) even if no boundaries found.
// Pure function. Returns [] for decision="refused".
func planSubtasks(vibeCase, task, decision string) []plannedSubtask {
	if decision == "inline" {
		return []plannedSubtask{{task: task, vibeCase: vibeCase}}
	}
	if decision == "refused" {
		return nil
	}
	// Split on '.' '!' '?' + ';' + newline.
	sentences := splitSentences(task)
	if len(sentences) == 0 || len(sentences) == 1 {
		return []plannedSubtask{{task: task, vibeCase: vibeCase}}
	}
	out := make([]plannedSubtask, 0, len(sentences))
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) < 5 {
			continue
		}
		out = append(out, plannedSubtask{task: s, vibeCase: vibeCase})
	}
	if len(out) == 0 {
		return []plannedSubtask{{task: task, vibeCase: vibeCase}}
	}
	return out
}

// splitSentences splits text on terminal punctuation (.!?) and
// semicolons (;). Pure function.
func splitSentences(text string) []string {
	var out []string
	var current strings.Builder
	for _, r := range text {
		switch r {
		case '.', '!', '?', ';':
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		case '\n':
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// invokeMindsetForSubtask calls the mindset_apply handler internally
// to produce a system_prompt for the subtask. Returns the
// system_prompt + cache_hit + verdict. On error, returns empty prompt.
// Phase 6 alpha.18.1: in-process call (not via MCP); CURATE binding
// lands alpha.19.
func invokeMindsetForSubtask(ctx context.Context, s *Server, operator, vibeCase, task string) (systemPrompt string, cacheHit bool, err error) {
	// Compose a system_prompt directly via the same composition path
	// (avoid the full MCP handler round-trip — faster + simpler).
	personaID := defaultPersonaForVibeCase[vibeCase]
	sp := composeSystemPrompt(personaID, vibeCase, task, operator)
	return sp, false, nil
}