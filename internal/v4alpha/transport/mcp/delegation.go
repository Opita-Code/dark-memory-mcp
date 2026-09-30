// delegate_intent tool — DELEGATION namespace (Phase 4 Chunk 4.2 STUB).
//
// Per SPEC-alpha-11-phase4.md §4.3, dark_memory_delegate_intent
// runs the DelegationRouter pipeline (DECIDE→PLAN→MIND→CURATE)
// to decide whether an intent is handled inline, delegated to
// sub-agents, or refused. The full implementation is DEFERRED to
// alpha.18 because the v4 LLMClient + agent_memory_delegate
// infrastructure is not yet wired into v4 MCP tools.
//
// What this STUB returns:
//   - Always {"decision": "inline", "subtasks": []}.
//   - No DECIDE phase (no deterministic rule dispatch).
//   - No PLAN phase (no subtask graph construction).
//   - No MIND phase (no mindset_apply per subtask).
//   - No CURATE phase (no agent_memory_delegate binding).
//
// Why stub vs omit:
//   - Same reasoning as mindset_apply (this file's sibling):
//     expose the API surface NOW for workflow integration;
//     full implementation lands when v4 LLMClient + subagent
//     binding surface are wired (alpha.18).
//   - Operator decision (per spec §8 Q3): stubbed.
//
// Future (alpha.18):
//   - DECIDE: bounded-LLM choice over (vibe_case, task_description)
//     with deterministic rule fallback per ADR-007 §3.
//   - PLAN: build subtask graph (dependency batches).
//   - MIND: invoke mindset_apply per subtask (system_prompt +
//     tools + model).
//   - CURATE: invoke agent_memory_delegate per subtask (curated
//     agent_memory context + C2 subagent binding).
package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const delegateIntentToolName = "dark_memory_delegate_intent"

// delegateIntentInput is the wire-shape for dark_memory_delegate_intent.
type delegateIntentInput struct {
	TaskDescription string `json:"task_description" jsonschema:"required" jsonschema_description:"≥10 char description of the work to be routed"`
	VibeCase        string `json:"vibe_case" jsonschema:"required" jsonschema_description:"One of C1..C7"`
	Operator        string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; defaults to active session operator when empty)"`
}

// delegateIntentSubtask is one planned subtask in the returned
// decision. v4 MVP STUB always returns subtasks=[] (no PLAN phase),
// but the wire shape is defined so callers can integrate.
type delegateIntentSubtask struct {
	ID                string   `json:"id"`
	SystemPrompt      string   `json:"system_prompt"`
	Tools             []string `json:"tools"`
	Model             string   `json:"model"`
	DelegationContext string   `json:"delegation_context,omitempty"`
}

// delegateIntentOutput is the wire-shape returned by dark_memory_delegate_intent.
// In v4 MVP (alpha.17 STUB), always decision="inline" + subtasks=[].
type delegateIntentOutput struct {
	Decision  string                 `json:"decision"` // "inline" | "delegate" | "refused"
	Subtasks  []delegateIntentSubtask `json:"subtasks"`
	Reasoning string                 `json:"reasoning"`
	StubNotice string                `json:"stub_notice"`
}

func registerDelegationTools(s *Server) {
	s.mcpSrv.AddTool(buildDelegateIntentTool(), s.handleDelegateIntent)
}

// buildDelegateIntentTool constructs the mcp.Tool descriptor for
// dark_memory_delegate_intent. Extracted so tests can introspect
// the tool definition without invoking the handler.
func buildDelegateIntentTool() mcp.Tool {
	return mcp.NewTool(delegateIntentToolName,
		mcp.WithDescription("Decide whether an intent is handled inline, delegated to sub-agents, or refused. "+
			"v4 MVP STUB: always returns decision='inline' with empty subtasks. "+
			"Full DECIDE→PLAN→MIND→CURATE pipeline lands in alpha.18 when v4 LLMClient + "+
			"agent_memory_delegate are wired. "+
			"Returns `stub_notice` to make the MVP nature explicit."),
		mcp.WithInputSchema[delegateIntentInput](),
	)
}

// handleDelegateIntent is the JSON-RPC handler. Extracted so tests
// can invoke it directly without going through the full MCP
// server lifecycle.
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
		// INV-1 still applies. Default to the canonical
		// orchestrator operator (matches v3.0.0-docfix default).
		in.Operator = "orchestrator_delegate"
	}

	// MVP STUB: always inline. The full pipeline lands in alpha.18.
	return resultJSON(delegateIntentOutput{
		Decision:  "inline",
		Subtasks:  []delegateIntentSubtask{}, // empty — no PLAN phase
		Reasoning: fmt.Sprintf(
			"v4 MVP STUB: decided to handle inline (no DECIDE/PLAN/MIND/CURATE pipeline yet). "+
				"Task: %q (vibe_case=%s, operator=%s). "+
				"Full pipeline lands in alpha.18.",
			in.TaskDescription, in.VibeCase, in.Operator,
		),
		StubNotice: "v4 MVP STUB (alpha.17); full DECIDE→PLAN→MIND→CURATE pipeline lands in alpha.18. " +
			"This tool always returns decision='inline' with empty subtasks until then.",
	})
}