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
//  1. DECIDE: same deterministic priority chain (refusal > delegation
//     markers > C7 > length>200 > inline).
//  2. EXTRACT: NEW. When DECIDE=delegate AND (length>200 OR vibe=C7),
//     invoke the LLM via the judge-delegator persona to extract atomic
//     non-overlapping subtasks. Validated by drift_judge (eval_type=
//     subtask_extraction). Failures surface as needs_human with
//     alternatives[].
//  3. MIND: same as alpha.18.1 (composeSystemPrompt per subtask).
//  4. CURATE: stub in Chunk 7.1 (subagent_register + delegation_context
//     land in Chunk 7.2).
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
	"crypto/sha256"
	"encoding/hex"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
)

const delegateIntentToolName = "dark_memory_delegate_intent"

// DelegateIntentInput is the wire shape for dark_memory_delegate_intent.
//
// Phase 7 alpha.19 v2 adds two optional fields (parent_session_id,
// parent_agent_id) for CURATE subagent binding (Chunk 7.2). They are
// additive — alpha.18.1 callers omitting them continue to work; the
// binding rows are still persisted but with empty parent fields.
//
// Phase 9 alpha.20 Chunk 8.1: exported (capitalized) so v3 binary
// can construct the input without re-declaring the schema.
type DelegateIntentInput struct {
	TaskDescription string `json:"task_description" jsonschema:"required" jsonschema_description:"≥10 char description of the work to be routed"`
	VibeCase        string `json:"vibe_case" jsonschema:"required" jsonschema_description:"One of C1..C7 (canonical vibe_case taxonomy per spec.go:14-16)"`
	Operator        string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; defaults to active session operator when empty)"`
	ParentSessionID string `json:"parent_session_id,omitempty" jsonschema_description:"Optional parent session id (CURATE subagent binding — empty when delegate_intent runs outside a session context)"`
	ParentAgentID   string `json:"parent_agent_id,omitempty" jsonschema_description:"Optional parent agent id (CURATE subagent binding — empty when delegate isn't chained from a vibe_publish)"`
}

// DelegateIntentSubtask is one planned subtask. v2 (alpha.19): adds
// subagent_id + delegation_context fields (populated by CURATE in
// Chunk 7.2 — empty in Chunk 7.1).
//
// Phase 9 alpha.20 Chunk 8.1: exported.
type DelegateIntentSubtask struct {
	ID                string   `json:"id"`
	Description       string   `json:"description"`
	SystemPrompt      string   `json:"system_prompt"`
	Tools             []string `json:"tools"`
	Model             string   `json:"model"`
	SubagentID        string   `json:"subagent_id,omitempty"`
	DelegationContext string   `json:"delegation_context,omitempty"`
}

// DelegateIntentAlternative mirrors delegation.Alternative for the wire shape.
//
// Phase 9 alpha.20 Chunk 8.1: exported.
type DelegateIntentAlternative struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Outcome struct {
		Kind      string `json:"kind"`
		Reasoning string `json:"reasoning,omitempty"`
	} `json:"outcome"`
}

// DelegateIntentOutput is the wire shape returned by
// dark_memory_delegate_intent. Phase 7 alpha.19 v2 adds cache_hit,
// verdict, and alternatives[] on top of alpha.18.1 v1.
//
// Phase 9 alpha.20 Chunk 8.1: exported (capitalized) so v3 binary
// can decode the output without re-declaring the schema.
type DelegateIntentOutput struct {
	Decision     string                      `json:"decision"`               // "inline"|"delegate"|"refused"
	Reasoning    string                      `json:"reasoning"`              // multi-phase trace
	Subtasks     []DelegateIntentSubtask     `json:"subtasks"`               // 0..N subtasks
	CacheHit     bool                        `json:"cache_hit"`              // NEW alpha.19
	Verdict      string                      `json:"verdict"`                // NEW alpha.19
	Alternatives []DelegateIntentAlternative `json:"alternatives,omitempty"` // NEW alpha.19 (needs_human only)

	// TaskClass (Phase 21 L8.1a instrumentation) reports what the
	// canonical vibeflow classifier sees in the task, ALONGSIDE the
	// router's own decision. It does not influence Decision.
	//
	// WHY IT IS ADVISORY AND NOT LOAD-BEARING
	// --------------------------------------
	// DECIDE already routes on `len(task) > 200` (delegation/router.go:73).
	// Classify is richer — keywords, language, code signals, multi-artifact
	// — so adopting it as the router would CHANGE WHO GETS DELEGATED, and
	// therefore change spend. That is a design decision with a cost, and
	// Phase 20's own closeout says the optimizations are not yet switched
	// on because nobody has measured them against a real baseline.
	//
	// So this runs the classifier and publishes its verdict WITHOUT
	// acting on it. After a measurement window the operator can compare
	// `router_threshold` against `task_class.class` on real traffic and
	// decide with data instead of by argument. Until then, the cost of a
	// second opinion is one deterministic function call and the benefit
	// is that the decision to adopt it becomes evidence-based.
	//
	// Adding it as a third opinion alongside the router would be the
	// failure mode this whole phase documents; this is explicitly NOT
	// that. The router remains the single decision-maker.
	TaskClass *TaskClassAdvisory `json:"task_class,omitempty"`
}

// TaskClassAdvisory is the published output of vibeflow.Classify for
// one delegate_intent call. Carries its own epistemic status: the
// classifier is a deterministic heuristic, so its output is ModeLED,
// never MEASURED — the same downward-only rule L11.2 applies to the
// drift judge's confidence.
type TaskClassAdvisory struct {
	// Class is the canonical task class from vibeflow.Classify
	// (short | medium | long | …).
	Class string `json:"class"`

	// Confidence is the classifier's own score in 0..1.
	Confidence float64 `json:"confidence"`

	// Grade is the evidence status of this whole block. Always
	// "modeled" — the classifier is a heuristic over the task text,
	// not an observation of how the task actually behaved.
	Grade string `json:"grade"`

	// TokenCount / Features is what the classifier actually saw, so an
	// operator can judge the input rather than trust the label.
	TokenCount int  `json:"token_count"`
	MultiStep  bool `json:"multi_step"`
	HasCode    bool `json:"has_code_signals"`

	// RouterThreshold is the router's own length cutoff, published here
	// so the two opinions are comparable in one place.
	RouterThreshold int `json:"router_threshold"`

	// AgreesWithRouter is nil when the two cannot be compared (the
	// router decided on a coordination marker or vibe_case, not on
	// length). When non-nil, it says whether a length-only router
	// would have reached the same decision.
	AgreesWithRouter *bool `json:"agrees_with_router,omitempty"`

	// Caveat states what this block is not. Never empty.
	Caveat string `json:"caveat"`
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
			"MIND calls mindset_apply per subtask. CURATE prepares delegation context (subagent_register live in Chunk 7.2). "+
			"Phase 9 alpha.20 Chunk 8.1: pipeline also exposed to v3 binary via DARK_DELEGATION_BACKEND=v4alpha."),
		mcp.WithInputSchema[DelegateIntentInput](),
	)
}

// handleDelegateIntent is the JSON-RPC handler (Phase 7 alpha.19,
// refactored Phase 9 alpha.20 Chunk 8.1).
//
// As of Chunk 8.1 this is a thin adapter over RunDelegateIntentCore:
// the mcp-go transport (bindArgs + resultJSON) wraps the pure
// pipeline function. Code that lived in handleDelegateIntent pre-Chunk
// 8.1 was moved verbatim to wire.go:RunDelegateIntentCore; the
// Server handleDelegateIntent is kept (NOT removed) because:
//   - Existing v4alpha tests (project_test.go) invoke it via
//     HandleDelegateIntentForTest. Removing it would break those tests.
//   - v4alpha's mcp-go MCPServer registration still calls it via
//     AddTool at server.go:225.
//
// Flow (unchanged from previous):
//  1. bindArgs unmarshal.
//  2. Delegate to RunDelegateIntentCore (DECIDE→EXTRACT→MIND→CURATE).
//  3. Map wire shape to mcp.CallToolResult via resultJSON.
func (s *Server) handleDelegateIntent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in DelegateIntentInput
	if err := bindArgs(req, &in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// Delegate to the shared pipeline. Server fields are the v4alpha
	// dependency graph (llmClient, extractCache, memories). nil-safe:
	// the pipeline falls back to needs_human when llmClient is nil and
	// no-ops the CURATE step when memories is nil.
	out, err := RunDelegateIntentCore(ctx, in, s.llmClient, s.extractCache, s.memories, s.eventEmitter)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return resultJSON(out)
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

// newTaskID builds the task_id used in subagent binding rows so the
// orchestrator can group bindings originating from the same delegate
// call. Stable across repeated calls (operator+task -> same id).
//
// Format: sha256 hex of operator + \x00 + task_description. Truncated
// to 16 chars for log readability. This is NOT a cryptographic
// guarantee — it's a correlation key for agent_memory.Recall queries.
func newTaskID(operator, taskDescription string) string {
	h := sha256.Sum256([]byte(operator + "\x00" + taskDescription))
	return hex.EncodeToString(h[:8])
}
