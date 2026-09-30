// mindset_apply tool — MINDSET namespace (Phase 4 Chunk 4.2 STUB).
//
// Per SPEC-alpha-11-phase4.md §4.2, dark_memory_mindset_apply
// procedurally composes a subagent system_prompt for the given
// (vibe_case, task_description) and validates it via LLM-as-judge
// before returning. The full implementation is DEFERRED to alpha.18
// because v4's LLMClient is not yet wired into any v4 MCP tool.
//
// What this STUB returns:
//   - A canned system_prompt that names the vibe_case and task.
//   - An empty tools_recommended slice (no tools wired in MVP).
//   - A static model_recommended of "inherit" (no model override).
//   - cache_hit: false (no caching yet).
//   - iterations: 0 (no judge-validate loop yet).
//
// Why stub vs omit:
//   - v3.0.0-docfix has a real mindset_apply (deferred + cached +
//     judge-validated). v4 needs the API surface exposed NOW so
//     operator workflow can integrate with it; the LLM-backed
//     implementation lands when v4 LLMClient is wired (alpha.18).
//   - The alternative (omit until alpha.18) delays operator
//     integration by 2+ weeks.
//
// Operator decision (per spec §8 Q3): stubbed (this design).
// See rows 2218 (§E open questions) for the operator ack queue.
//
// Cache: agent_memory (kind=link, pinned, TTL via DARK_MINDSET_CACHE_TTL
// default 1h). NOT IMPLEMENTED in v4 MVP — alpha.18.
//
// Composition: per task_description + vibe_case, build the system
// prompt. Iteration: up to DARK_MINDSET_MAX_ITERATIONS (default 3)
// if the judge rejects. NOT IMPLEMENTED in v4 MVP — alpha.18.
package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const mindsetApplyToolName = "dark_memory_mindset_apply"

// validVibeCases is the closed allow-list for the vibe_case field.
// Mirrors the dark-memory-mcp vibe spec enumeration (C1..C7 per
// ADR-007 §3). Any other value is rejected at the tool surface.
var validVibeCases = map[string]bool{
	"C1": true, // code
	"C2": true, // text
	"C3": true, // image
	"C4": true, // video
	"C5": true, // audio
	"C6": true, // multi
	"C7": true, // (future, reserved)
}

// mindsetApplyInput is the wire-shape for dark_memory_mindset_apply.
type mindsetApplyInput struct {
	VibeCase       string `json:"vibe_case" jsonschema:"required" jsonschema_description:"One of C1..C7 (canonical vibe_case taxonomy)"`
	TaskDescription string `json:"task_description" jsonschema:"required" jsonschema_description:"≥10 char description of what the sub-agent should do (becomes the seed for procedural composition)"`
	Operator       string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; default 'orchestrator_mindset' if empty)"`
	ModelFloor     string `json:"model_floor,omitempty" jsonschema_description:"Minimum model capability (sonnet|opus|haiku|inherit). Empty = no override."`
}

// mindsetApplyOutput is the wire-shape returned by dark_memory_mindset_apply.
// In v4 MVP (alpha.17 STUB), the system_prompt is canned and there
// is no cache_hit / iterations loop.
type mindsetApplyOutput struct {
	SystemPrompt      string   `json:"system_prompt"`
	ToolsRecommended  []string `json:"tools_recommended"`
	ModelRecommended  string   `json:"model_recommended"`
	CacheHit          bool     `json:"cache_hit"`
	Iterations        int      `json:"iterations"`
	VibeCase          string   `json:"vibe_case"`
	TaskDescription   string   `json:"task_description"`
	StubNotice        string   `json:"stub_notice"`
}

func registerMindsetTools(s *Server) {
	s.mcpSrv.AddTool(buildMindsetApplyTool(), s.handleMindsetApply)
}

// buildMindsetApplyTool constructs the mcp.Tool descriptor for
// dark_memory_mindset_apply. Extracted so tests can introspect
// the tool definition without invoking the handler.
func buildMindsetApplyTool() mcp.Tool {
	return mcp.NewTool(mindsetApplyToolName,
		mcp.WithDescription("Compose a sub-agent system_prompt for the given vibe_case + task_description. "+
			"v4 MVP STUB: full LLM-as-judge iteration loop lands in alpha.18 when v4 LLMClient is wired. "+
			"Returns a canned system_prompt that names the vibe_case and task; no judge-validate; no cache. "+
			"Returns `stub_notice` to make the MVP nature explicit."),
		mcp.WithInputSchema[mindsetApplyInput](),
	)
}

// handleMindsetApply is the JSON-RPC handler. Extracted so tests
// can invoke it directly without going through the full MCP
// server lifecycle.
func (s *Server) handleMindsetApply(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in mindsetApplyInput
	if err := bindArgs(req, &in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !validVibeCases[in.VibeCase] {
		return mcp.NewToolResultError(fmt.Sprintf(
			"mindset_apply: vibe_case %q not in canonical allow-list (C1..C7)", in.VibeCase)), nil
	}
	if len(strings.TrimSpace(in.TaskDescription)) < 10 {
		return mcp.NewToolResultError("mindset_apply: task_description must be ≥10 chars"), nil
	}
	if in.Operator == "" {
		// INV-1 still applies (audit owner). Default to the
		// canonical orchestrator operator when caller doesn't
		// supply one (matches v3.0.0-docfix behavior).
		in.Operator = "orchestrator_mindset"
	}
	if in.ModelFloor != "" {
		switch in.ModelFloor {
		case "sonnet", "opus", "haiku", "inherit":
			// valid
		default:
			return mcp.NewToolResultError(fmt.Sprintf(
				"mindset_apply: model_floor %q not in canonical allow-list (sonnet|opus|haiku|inherit)",
				in.ModelFloor)), nil
		}
	}

	// MVP STUB: canned system_prompt. The full composition lands
	// in alpha.18 when v4 LLMClient is wired.
	systemPrompt := fmt.Sprintf(
		"You are a sub-agent for vibe_case %s. Task: %s. "+
			"Operator: %s. "+
			"This is a v4 MVP STUB system prompt generated by dark_memory_mindset_apply. "+
			"The full procedural composition (with judge-validate iteration loop and "+
			"agent_memory cache) lands in alpha.18 when the v4 LLMClient is wired into "+
			"a v4 MCP tool. For now, treat this prompt as a placeholder that names the "+
			"role; do not rely on its content for production behavior.",
		in.VibeCase, in.TaskDescription, in.Operator,
	)

	return resultJSON(mindsetApplyOutput{
		SystemPrompt:     systemPrompt,
		ToolsRecommended: []string{},     // no tools wired in MVP
		ModelRecommended: "inherit",      // defer to caller
		CacheHit:         false,          // no caching in MVP
		Iterations:       0,              // no judge loop in MVP
		VibeCase:         in.VibeCase,
		TaskDescription:  in.TaskDescription,
		StubNotice: "v4 MVP STUB (alpha.17); full composition + judge-validate iteration lands in alpha.18. " +
			"System prompt is a named-role placeholder; not production-grade.",
	})
}