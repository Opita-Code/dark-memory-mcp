// delegate_intent tool — DELEGATION namespace (Phase 7 alpha.19, LLM-routed).
//
// Per SPEC-alpha-11-phase7.md §3.1, dark_memory_delegate_intent runs the
// DelegationRouter pipeline (DECIDE→EXTRACT→MIND→CURATE) to decide whether
// an intent is handled inline, delegated to sub-agents, or refused.
//
// # Phase 7 alpha.19 architecture (vs Phase 6 alpha.18.1)
//
// Phase 6 alpha.18.1 chunk 6.3: DECIDE was a literal-pattern router
// (deterministic; "first ... then" only matched the exact substring).
//
// Phase 7 alpha.19 (this file):
//   1. DECIDE: same deterministic priority chain (refusal > delegation
//      markers > C7 > length>200 > inline).
//   2. EXTRACT: NEW. When DECIDE=delegate AND (length>200 OR vibe=C7),
//      invoke the LLM via the judge-delegator persona to extract atomic
//      non-overlapping subtasks. Validated by drift_judge (eval_type=
//      subtask_extraction). Failures surface as needs_human with
//      alternatives[].
//   3. MIND: same as alpha.18.1 (composeSystemPrompt per subtask).
//   4. CURATE: stub in Chunk 7.1 (subagent_register + delegation_context
//      land in Chunk 7.2).
//
// # Wire shape v2 (alpha.19, additive over v1)
//
//	{
//	  "decision":   "inline" | "delegate" | "refused",
//	  "reasoning":  "DECIDE: ... | EXTRACT: ... | JUDGE: ...",
//	  "subtasks":   [{ id, description, system_prompt, model,
//	                   tools, subagent_id, delegation_context }],
//	  "cache_hit":  bool,
//	  "verdict":    "aligned" | "drift_detected" | "needs_human" |
//	                 "cached" | "errored",
//	  "alternatives": []Alternative  // populated only when verdict=needs_human
//	}
//
// Backward compat: alpha.18.1 callers reading `decision`, `reasoning`,
// `subtasks[]` continue to work — the new fields are additive.
package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
)

const delegateIntentToolName = "dark_memory_delegate_intent"

// delegateIntentInput is the wire shape for dark_memory_delegate_intent.
// Same as alpha.18.1 — operator-facing input is unchanged.
type delegateIntentInput struct {
	TaskDescription string `json:"task_description" jsonschema:"required" jsonschema_description:"≥10 char description of the work to be routed"`
	VibeCase        string `json:"vibe_case" jsonschema:"required" jsonschema_description:"One of C1..C7 (canonical vibe_case taxonomy per spec.go:14-16)"`
	Operator        string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; defaults to active session operator when empty)"`
}

// delegateIntentSubtask is one planned subtask. v2 (alpha.19): adds
// subagent_id + delegation_context fields (populated by CURATE in
// Chunk 7.2 — empty in Chunk 7.1).
type delegateIntentSubtask struct {
	ID                string   `json:"id"`
	Description       string   `json:"description"`
	SystemPrompt      string   `json:"system_prompt"`
	Tools             []string `json:"tools"`
	Model             string   `json:"model"`
	SubagentID        string   `json:"subagent_id,omitempty"`
	DelegationContext string   `json:"delegation_context,omitempty"`
}

// delegateIntentAlternative mirrors delegation.Alternative for the wire shape.
type delegateIntentAlternative struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Outcome struct {
		Kind      string `json:"kind"`
		Reasoning string `json:"reasoning,omitempty"`
	} `json:"outcome"`
}

// delegateIntentOutput is the wire shape returned by
// dark_memory_delegate_intent. Phase 7 alpha.19 v2 adds cache_hit,
// verdict, and alternatives[] on top of alpha.18.1 v1.
type delegateIntentOutput struct {
	Decision     string                       `json:"decision"`     // "inline"|"delegate"|"refused"
	Reasoning    string                       `json:"reasoning"`   // multi-phase trace
	Subtasks     []delegateIntentSubtask      `json:"subtasks"`    // 0..N subtasks
	CacheHit     bool                         `json:"cache_hit"`   // NEW alpha.19
	Verdict      string                       `json:"verdict"`     // NEW alpha.19
	Alternatives []delegateIntentAlternative  `json:"alternatives,omitempty"` // NEW alpha.19 (needs_human only)
}

func registerDelegationTools(s *Server) {
	s.mcpSrv.AddTool(buildDelegateIntentTool(), s.handleDelegateIntent)
}

// buildDelegateIntentTool constructs the mcp.Tool descriptor.
func buildDelegateIntentTool() mcp.Tool {
	return mcp.NewTool(delegateIntentToolName,
		mcp.WithDescription("Decide whether an intent is handled inline, delegated to sub-agents, or refused. "+
			"Phase 7 alpha.19 LLM-router upgrade: DECIDE→EXTRACT→MIND→CURATE pipeline. "+
			"DECIDE uses deterministic rules (short task=inline, C7 multi or coordination keywords=delegate, "+
			"explicit refusal=refused). EXTRACT (NEW) invokes the LLM via the judge-delegator persona when "+
			"DECIDE=delegate AND length>200 OR vibe_case=C7; validated by drift_judge (eval_type=subtask_extraction). "+
			"MIND calls mindset_apply per subtask. CURATE prepares delegation context (subagent_register live in Chunk 7.2)."),
		mcp.WithInputSchema[delegateIntentInput](),
	)
}

// handleDelegateIntent is the JSON-RPC handler (Phase 7 alpha.19).
//
// Flow:
//  1. Validate input.
//  2. DECIDE via delegation package (deterministic).
//  3. EXTRACT via delegation.Extractor when ShouldExtract returns true.
//  4. PLAN (deterministic) when EXTRACT is skipped.
//  5. MIND: composeSystemPrompt per subtask.
//  6. CURATE stub (Chunk 7.2 wires subagent_register).
//  7. Return wire shape v2.
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

	// 1+2+3+4. DECIDE → optional EXTRACT → PLAN.
	decision, reason := delegation.DecideDelegation(in.VibeCase, in.TaskDescription)

	var subtasks []delegation.Subtask
	var extractVerdict string
	var alternatives []delegation.Alternative
	cacheHit := false

	if delegation.ShouldExtract(decision, in.VibeCase, in.TaskDescription) {
		// EXTRACT via the LLM (per SPEC P1=C + P4=drift_judge).
		ex := s.buildDelegationExtractor(ctx, in.Operator)
		result, _ := ex.Extract(ctx, in.VibeCase, in.TaskDescription)
		subtasks = result.Subtasks
		extractVerdict = result.Verdict
		alternatives = result.Alternatives
		cacheHit = result.CacheHit
		// Reasoning combines DECIDE + EXTRACT (when EXTRACT ran).
		reason = reason + " | " + result.Reasoning
	} else {
		// PLAN (deterministic) for short delegate + inline + refused.
		subtasks = delegation.PlanSubtasks(in.VibeCase, in.TaskDescription, decision)
	}

	// 5. MIND — invoke composeSystemPrompt per subtask.
	out := make([]delegateIntentSubtask, 0, len(subtasks))
	for i, st := range subtasks {
		personaID := defaultPersonaForVibeCase[st.VibeCase]
		sp := composeSystemPrompt(personaID, st.VibeCase, st.Description, in.Operator)
		out = append(out, delegateIntentSubtask{
			ID:                st.ID,
			Description:       st.Description,
			SystemPrompt:      sp,
			Tools:             st.Tools,
			Model:             st.Model,
			SubagentID:        st.SubagentID,        // empty in Chunk 7.1
			DelegationContext: st.DelegationContext, // empty in Chunk 7.1
		})
		_ = i
	}

	// 6. CURATE stub — Chunk 7.2 wires subagent_register per subtask.

	// 7. Return wire shape v2.
	verdict := extractVerdict
	if verdict == "" {
		verdict = "aligned" // deterministic PLAN path = aligned by default
	}
	altOut := make([]delegateIntentAlternative, 0, len(alternatives))
	for _, a := range alternatives {
		var alt delegateIntentAlternative
		alt.ID = a.ID
		alt.Label = a.Label
		alt.Outcome.Kind = a.Outcome.Kind
		alt.Outcome.Reasoning = a.Outcome.Reasoning
		altOut = append(altOut, alt)
	}

	return resultJSON(delegateIntentOutput{
		Decision:     decision,
		Reasoning:    reason,
		Subtasks:     out,
		CacheHit:     cacheHit,
		Verdict:      verdict,
		Alternatives: altOut,
	})
}

// buildDelegationExtractor constructs a delegation.Extractor wired to
// the Server's LLMClient + cache + agent_memory store. Pure construction
// (no LLM call yet) — caller invokes Extract afterwards.
//
// The extract cache is hoisted to the Server (per buildDelegationExtractor's
// field s.extractCache) so cache hits work across calls.
func (s *Server) buildDelegationExtractor(ctx context.Context, operator string) *delegation.Extractor {
	var memStore delegation.AgentMemoryStore
	if s.memories != nil {
		memStore = &agentMemoryStoreAdapter{s.memories}
	}
	return &delegation.Extractor{
		LLM:       s.llmClient,
		Cache:     s.extractCache,
		MemStore:  memStore,
		Operator:  operator,
		ProjectID: "default", // Phase 4 isolation is at session level; delegate is per-call
	}
}

// agentMemoryStoreAdapter wraps *agent_memory.Store to satisfy the
// minimal delegation.AgentMemoryStore interface. Avoids forcing the
// delegation package to depend on the full agent_memory.Store (95+
// methods).
type agentMemoryStoreAdapter struct {
	store *agent_memory.Store
}

// RecallFiltered adapts agent_memory.Store.RecallFiltered to the
// delegation interface. Returns empty slice on error (best-effort;
// caller treats as cache miss).
func (a *agentMemoryStoreAdapter) RecallFiltered(ctx context.Context, operator, query string, filter delegation.RecallFilter, limit int) ([]delegation.AgentMemoryRow, error) {
	if a.store == nil {
		return nil, nil
	}
	rows, err := a.store.RecallFiltered(ctx, operator, query, agent_memory.RecallFilter{
		TagPrefix: filter.TagPrefix,
	}, limit)
	if err != nil {
		return nil, err
	}
	out := make([]delegation.AgentMemoryRow, len(rows))
	for i, r := range rows {
		out[i] = delegation.AgentMemoryRow{
			ID:      r.ID,
			Title:   r.Title,
			Content: r.Content,
			Tags:    r.Tags,
		}
	}
	return out, nil
}

// Save adapts agent_memory.Store.Save to the delegation interface.
func (a *agentMemoryStoreAdapter) Save(ctx context.Context, audit any, operator, kind, title, content string, tags string, pinned bool) (int64, error) {
	if a.store == nil {
		return 0, nil
	}
	// The audit parameter is `any` because we don't want delegation to
	// depend on audit.Audit. The agent_memory.Save signature takes a
	// concrete audit.Audit; pass nil (audit emission is handled by the
	// caller via audit.Writer when needed).
	return a.store.Save(ctx, nil, operator, kind, title, content, tags, pinned)
}