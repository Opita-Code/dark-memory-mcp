// wire.go (v4alpha) — Phase 9 alpha.20 Chunk 8.1.
//
// Closes the Phase 8 e2e critical finding #1 (row 2327):
// the v4alpha DECIDE→EXTRACT→MIND→CURATE pipeline was implemented
// in internal/v4alpha/transport/mcp/delegation.go (Phase 7 alpha.19
// Chunk 7.1) but wired ONLY into v4alpha's mcp-go MCPServer. The v3
// binary (dark-mem-mcp.exe) registered `dark_memory_delegate_intent`
// at internal/tools/delegation.go:28 via BindOrchestrator, which
// called orch.DelegateIntent (the alpha.18.1 v2 router in
// internal/orchestration/delegate_intent.go:86). The v2 router is
// deterministic; it does NOT run EXTRACT. Operators consuming the v3
// MCP surface saw only the v2 behavior.
//
// This file exports the EXTRACT pipeline as a pure function — no
// mcp.CallToolRequest, no *mcp.CallToolResult, no mcp-go types —
// so the v3 binary can call it without importing mcp-go and without
// instantiating a v4alpha Server at boot.
//
// # Architecture (Chunk 8.1)
//
//	v3 binary (dark-mem-mcp.exe)
//	  ├── boot wires: llmClient, extractCache, memories
//	  │   (via cmd/dark-mem-mcp/legacy_main.go + v4alpha constructors)
//	  └── tools/delegation.go:RegisterDelegation
//	       ├── DARK_DELEGATION_BACKEND=v4alpha (default) →
//	       │   mcp.RegisterDelegateIntentTool(reg, llmClient, extractCache, memories)
//	       │     └── RunDelegateIntentCore → v4alpha wire shape (DelegateIntentOutput)
//	       │     └── convert to v3 DelegateIntentOutput (additive fields)
//	       └── DARK_DELEGATION_BACKEND=v2 (rollback escape hatch) →
//	           BindOrchestrator → orch.DelegateIntent (existing v2 path)
//
// # Wire-shape contract
//
// RunDelegateIntentCore returns the v4alpha wire shape (decision +
// reasoning + subtasks + cache_hit + verdict + alternatives). The
// v3 caller in internal/tools/delegation.go converts that to v3's
// DelegateIntentOutput (handler + reasoning + case + plan + operator)
// with three ADDITIVE fields: CacheHit bool, Verdict string,
// Alternatives []DelegateIntentAlternative. Existing v3 consumers
// ignore the additive fields; v4alpha-aware harnesses can read them.
//
// # Feature flag semantics
//
//	DARK_DELEGATION_BACKEND=v4alpha (default)
//	  → RunDelegateIntentCore. EXTRACT runs when conditions met.
//	  → If no LLM key is configured, EXTRACT returns needs_human
//	    with alternatives[] (incl. "fallback-plan"). The v3 caller
//	    surfaces this verbatim — operators without an LLM key see
//	    the alternatives instead of an automatic fallback.
//
//	DARK_DELEGATION_BACKEND=v2 (rollback escape hatch)
//	  → BindOrchestrator → orch.DelegateIntent. Deterministic only.
//	  → No EXTRACT, no LLM call. Operators without an LLM key use
//	    this path.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// RunDelegateIntentCore executes the v4alpha DECIDE→EXTRACT→MIND→CURATE
// pipeline for one (vibeCase, taskDescription) and returns the v4alpha
// wire shape (DelegateIntentOutput). Pure function — no mcp-go types.
//
// Wire contract:
//
//	{
//	  "decision":   "inline" | "delegate" | "refused",
//	  "reasoning":  "DECIDE: ... | EXTRACT: ... | JUDGE: ...",
//	  "subtasks":   [{id, description, system_prompt, model, tools,
//	                  subagent_id, delegation_context}],
//	  "cache_hit":  bool,
//	  "verdict":    "aligned" | "drift_detected" | "needs_human" |
//	                 "cached" | "errored",
//	  "alternatives": []Alternative  // populated only when verdict=needs_human
//	}
//
// Error contract:
//   - *DelegateIntentInput field validation errors return
//     (nil, *WireError). Callers extract via errors.As.
//   - Pipeline-level failures (LLM unavailable, cache full, etc.)
//     surface as verdict=needs_human with alternatives[] — NEVER as
//     a Go error. The harness picks the alternative.
//
// Parameters:
//   - llmClient: judge.LLMClient (can be nil — extract returns
//     needs_human with fallback-plan alternative).
//   - extractCache: *delegation.ExtractCache (can be nil — fresh
//     cache constructed for the call).
//   - memories: *agent_memory.Store (can be nil — CURATE step is
//     a no-op, subtasks returned with empty subagent_id).
func RunDelegateIntentCore(
	ctx context.Context,
	in DelegateIntentInput,
	llmClient judge.LLMClient,
	extractCache *delegation.ExtractCache,
	memories *agent_memory.Store,
	emitter *event.DelegationProgressEmitter, // Phase 12 T-103c, nil-safe
) (*DelegateIntentOutput, error) {
	if !validVibeCases[in.VibeCase] {
		return nil, errInvalidVibeCase(in.VibeCase)
	}
	if len(strings.TrimSpace(in.TaskDescription)) < 10 {
		return nil, errTaskDescriptionTooShort()
	}
	if in.Operator == "" {
		in.Operator = "orchestrator_delegate"
	}

	// Phase 12 T-103c: capture start time for duration_ms forensic.
	// Used by EmitCompleted / EmitFailed at the end of the pipeline.
	startTime := time.Now()

	// taskID is stable across DECIDE→EXTRACT→MIND→CURATE→COMPLETED so
	// observers can correlate the 5+ progress events via process_id.
	taskID := newTaskID(in.Operator, in.TaskDescription)

	// 1+2. DECIDE.
	decision, reason := delegation.DecideDelegation(in.VibeCase, in.TaskDescription)

	// Phase 12 T-103c: emit "started" (root) progress event. SYNC so we
	// capture the event id; downstream emits reference it as
	// parent_event_id. Failures are logged but don't fail the pipeline.
	if emitter != nil {
		_, _ = emitter.EmitDecide(ctx, taskID, decision, reason)
	}

	// 3+4. EXTRACT (when ShouldExtract fires) OR PLAN (otherwise).
	var (
		subtasks     []delegation.Subtask
		extractVerd  string
		alternatives []delegation.Alternative
		cacheHit     bool
	)

	if delegation.ShouldExtract(decision, in.VibeCase, in.TaskDescription) {
		// EXTRACT via LLM (per SPEC P1=C + P4=drift_judge). When the
		// LLM client is nil (no provider key at boot) the Extractor
		// returns needs_human with alternatives[] — caller's choice.
		ex := buildExtractorForWire(ctx, llmClient, extractCache, memories, in.Operator)
		result, _ := ex.Extract(ctx, in.VibeCase, in.TaskDescription)
		subtasks = result.Subtasks
		extractVerd = result.Verdict
		alternatives = result.Alternatives
		cacheHit = result.CacheHit
		if result.Reasoning != "" {
			reason = reason + " | " + result.Reasoning
		}
		// Phase 12 T-103c: emit "running" for EXTRACT (pct=25).
		if emitter != nil {
			emitter.EmitExtract(ctx, taskID, extractVerd, len(subtasks))
		}
	} else {
		// PLAN for short delegate + inline + refused.
		subtasks = delegation.PlanSubtasks(in.VibeCase, in.TaskDescription, decision)
		// Phase 12 T-103c: when EXTRACT is bypassed, emit a synthetic
		// EXTRACT event so observers see the full timeline.
		if emitter != nil {
			emitter.EmitExtract(ctx, taskID, "aligned", len(subtasks))
		}
	}

	// Initial verdict: aligned for PLAN/INLINE/REFUSED, or whatever
	// the EXTRACT step returned. CURATE may downgrade to errored.
	verdict := extractVerd
	if verdict == "" {
		verdict = "aligned"
	}

	// 5. MIND — invoke composeSystemPrompt per subtask.
	out := make([]DelegateIntentSubtask, 0, len(subtasks))
	for _, st := range subtasks {
		personaID := defaultPersonaForVibeCase[st.VibeCase]
		sp := composeSystemPrompt(personaID, st.VibeCase, st.Description, in.Operator)
		out = append(out, DelegateIntentSubtask{
			ID:                st.ID,
			Description:       st.Description,
			SystemPrompt:      sp,
			Tools:             st.Tools,
			Model:             st.Model,
			SubagentID:        st.SubagentID,
			DelegationContext: st.DelegationContext,
		})
	}
	// Phase 12 T-103c: emit "running" for MIND (pct=50).
	if emitter != nil {
		emitter.EmitMind(ctx, taskID, len(out))
	}

	// 6. CURATE — register a subagent binding per subtask (Chunk 7.2
	// path). Skipped when verdict != aligned (a needs_human / cached
	// / errored response has no stable subtasks to bind).
	if verdict == "aligned" && len(out) > 0 {
		stSlice := make([]delegation.Subtask, len(out))
		for i, st := range out {
			stSlice[i] = delegation.Subtask{
				ID:                st.ID,
				Description:       st.Description,
				VibeCase:          in.VibeCase,
				SubagentID:        st.SubagentID,
				DelegationContext: st.DelegationContext,
			}
		}
		bound, err := BindSubtasksToSubagents(ctx, memories, in.Operator,
			in.ParentSessionID, in.ParentAgentID, taskID, stSlice, DefaultSubagentTTLSeconds)
		if err != nil {
			// CURATE failure is non-fatal — surface verdict=errored.
			verdict = "errored"
		} else {
			for i, b := range bound {
				out[i].SubagentID = b.SubagentID
				out[i].DelegationContext = b.DelegationContext
			}
		}
	}
	// Phase 12 T-103c: emit "running" for CURATE (pct=75). Always emit
	// (even when verdict != aligned) so the timeline shows CURATE was
	// either skipped (verdict != aligned) or ran (verdict == aligned).
	if emitter != nil {
		boundCount := 0
		for _, st := range out {
			if st.SubagentID != "" {
				boundCount++
			}
		}
		emitter.EmitCurate(ctx, taskID, boundCount)
	}

	// Phase 12 T-103c: emit "completed" (pct=100) with final verdict
	// + subtask count + duration. SYNC so observers polling the
	// process_id see the final state before the MCP call returns.
	if emitter != nil {
		elapsedMs := time.Since(startTime).Milliseconds()
		emitter.EmitCompleted(ctx, taskID, verdict, len(out), elapsedMs)
	}

	// 7. Build the wire shape.
	altOut := make([]DelegateIntentAlternative, 0, len(alternatives))
	for _, a := range alternatives {
		var alt DelegateIntentAlternative
		alt.ID = a.ID
		alt.Label = a.Label
		alt.Outcome.Kind = a.Outcome.Kind
		alt.Outcome.Reasoning = a.Outcome.Reasoning
		altOut = append(altOut, alt)
	}

	return &DelegateIntentOutput{
		Decision:     decision,
		Reasoning:    reason,
		Subtasks:     out,
		CacheHit:     cacheHit,
		Verdict:      verdict,
		Alternatives: altOut,
	}, nil
}

// buildExtractorForWire constructs a delegation.Extractor for use by
// RunDelegateIntentCore. Mirrors Server.buildDelegationExtractor but
// does NOT require the full Server struct — wires deps from explicit
// parameters so the function is testable in isolation.
//
// When llmClient is nil (no provider key at boot), the Extractor
// short-circuits to needs_human with llm_network_error alternatives.
// When extractCache is nil, a fresh in-memory cache with default TTL
// is constructed (one-shot — no cross-call hits).
//
// When memories is nil, CURATE in RunDelegateIntentCore is a no-op
// (subagent_id remains empty per BindSubtasksToSubagents contract).
func buildExtractorForWire(
	ctx context.Context,
	llmClient judge.LLMClient,
	extractCache *delegation.ExtractCache,
	memories *agent_memory.Store,
	operator string,
) *delegation.Extractor {
	var memStore delegation.AgentMemoryStore
	if memories != nil {
		memStore = &agentMemoryStoreAdapter{memories}
	}
	if extractCache == nil {
		extractCache = delegation.NewExtractCache(delegation.DefaultCacheTTL)
	}
	return &delegation.Extractor{
		LLM:       llmClient,
		Cache:     extractCache,
		MemStore:  memStore,
		Operator:  operator,
		ProjectID: "default", // Chunk 8.1: project isolation at session level only
	}
}

// --- error helpers (returned as Go errors; v3 caller maps to ToolError) ---

// WireError is the error type returned by RunDelegateIntentCore for
// input-validation failures. Exported (capitalized) so v3 callers
// can extract Field via errors.As and populate ToolError.Field
// (per F35 wire-propagation contract).
type WireError struct {
	Code    string
	Field   string
	Message string
}

// Error implements the error interface.
func (e *WireError) Error() string { return e.Message }

// AsWireError extracts a *WireError from err if present. Convenience
// for v3 callers using errors.As without re-importing the type.
func AsWireError(err error) (*WireError, bool) {
	var we *WireError
	if errors.As(err, &we) {
		return we, true
	}
	return nil, false
}

// errInvalidVibeCase returns a structured error for non-canonical
// vibe_case input. The v3 caller maps to ToolError{Code: ErrInvalidArgument}.
func errInvalidVibeCase(c string) error {
	return &WireError{Code: "ErrInvalidArgument", Field: "vibe_case",
		Message: fmt.Sprintf("delegate_intent: vibe_case %q not in canonical allow-list (valid: %v)",
			c, vibecase.JSONSchemaEnum())}
}

// errTaskDescriptionTooShort returns a structured error when the
// task_description is <10 chars. Mirrors the v4alpha handler's check.
func errTaskDescriptionTooShort() error {
	return &WireError{Code: "ErrInvalidArgument", Field: "task_description",
		Message: "delegate_intent: task_description must be ≥10 chars"}
}