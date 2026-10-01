// Package tools — delegation.go: the DELEGATION namespace (1 tool).
//
// Wave 5C. Per the DelegationRouter architecture
// (vibe-flow/main/DELEGATION_ARCHITECTURE.md):
//
//	dark_memory_delegate_intent
//
// Decides whether the orchestrator handles an intent inline,
// delegates it to sub-agents, or refuses (A1: Memory decides). Runs
// the DECIDE→PLAN→MIND→CURATE pipeline and returns ready-to-spawn
// material: for each subtask, the composed system_prompt (via
// mindset_apply), the curated delegation context (via
// agent_memory_delegate, which also registers the C2 binding), and
// the model/tools recommendation. The harness performs the actual
// spawn with its Task() tool.
//
// # Phase 9 alpha.20 Chunk 8.1
//
// The default backend switched from v2 (alpha.18.1 deterministic
// router) to v4alpha (alpha.19 Chunk 7.1 EXTRACT pipeline +
// drift_judge validation + LLM-extracted atomic subtasks).
// Operators without an LLM key can roll back via:
//
//	DARK_DELEGATION_BACKEND=v2
//
// Feature flag defaults to "v4alpha" (per Phase 8 e2e finding
// row 2327 — the v4alpha DECIDE→EXTRACT→MIND→CURATE pipeline is
// the canonical Phase 7 alpha.19 shipping; v2 is the alpha.18.1
// legacy).
//
// Wire contract (v3 + v4alpha extended — backward compat):
//
//	{
//	  "handler":       "DELEGATE"|"HANDLE"|"REFUSE",
//	  "reasoning":     "DECIDE: ... | EXTRACT: ... | JUDGE: ...",
//	  "case":          "C7",
//	  "plan":          [{id, vibe_case, task, system_prompt,
//	                     delegation_context, subagent_id,
//	                     tools_recommended, model_recommended,
//	                     depends_on}],
//	  "operator":      "...",
//	  "decision":      "delegate"|"inline"|"refused",   // NEW alpha.20
//	  "cache_hit":     bool,                            // NEW alpha.20
//	  "verdict":       "aligned"|"drift_detected"|      // NEW alpha.20
//	                   "needs_human"|"cached"|"errored",
//	  "alternatives":  [{id, label, kind, reasoning}]   // NEW alpha.20
//	                   // populated only when verdict=needs_human
//	}
//
// Existing alpha.18.1 callers ignore cache_hit/verdict/alternatives.
// New alpha.20 fields are populated only by the v4alpha backend —
// v2 backend leaves them empty (omitempty).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
	v4mcpt "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/transport/mcp"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// delegationBackend enumerates the two delegate_intent implementations
// selectable via the DARK_DELEGATION_BACKEND env var. Defaults to
// BackendV4Alpha (Phase 7 alpha.19 shipping).
type delegationBackend int

const (
	// BackendV2 is the alpha.18.1 deterministic router (Wave 5C).
	// No LLM call, no EXTRACT. Single-bundle C7, HANDLE everywhere else.
	BackendV2 delegationBackend = iota

	// BackendV4Alpha is the alpha.19 Chunk 7.1 pipeline:
	// DECIDE→EXTRACT→MIND→CURATE with drift_judge validation.
	// LLM-backed when an LLM key is configured; falls back to
	// needs_human with alternatives[] otherwise.
	BackendV4Alpha
)

// delegationBackendFromEnv reads DARK_DELEGATION_BACKEND and returns
// the selected backend. Unknown values fall back to BackendV4Alpha
// (Phase 9 default). Empty value also returns BackendV4Alpha.
func delegationBackendFromEnv() delegationBackend {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DARK_DELEGATION_BACKEND")))
	switch v {
	case "", "v4alpha", "alpha":
		return BackendV4Alpha
	case "v2", "alpha18", "alpha18.1":
		return BackendV2
	default:
		return BackendV4Alpha
	}
}

// DelegateIntentBackend carries the optional dependencies the v4alpha
// backend requires. When BackendV4Alpha is selected AND any dependency
// is nil, the adapter falls back to needs_human with alternatives[].
// This is the ONLY safe behavior: silently downgrading to v2 would
// hide a Phase 8 e2e critical finding from the operator.
type DelegateIntentBackend struct {
	// LLMClient is the judge.LLMClient used by EXTRACT. nil →
	// needs_human with llm_network_error alternative.
	LLMClient judge.LLMClient

	// ExtractCache is the cross-call LLM result cache. nil →
	// fresh in-memory cache constructed per call.
	ExtractCache *delegation.ExtractCache

	// Memories is the agent_memory.Store used by CURATE (Chunk 7.2).
	// nil → CURATE no-ops, subagent_id remains empty in subtasks.
	Memories *agent_memory.Store
}

// RegisterDelegation wires the 1 DELEGATION tool into the registry.
//
// The implementation switches between v2 and v4alpha based on
// DARK_DELEGATION_BACKEND (default v4alpha). When v4alpha is selected
// but backend is nil, the adapter falls back to needs_human with
// alternatives[] (does NOT silently downgrade — the harness sees
// the alternatives and can pick fallback-plan).
//
// DEPRECATED signature: RegisterDelegation(reg, orch, st). Still
// works (calls RegisterDelegationWithBackend with backend=nil).
func RegisterDelegation(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	RegisterDelegationWithBackend(reg, orch, st, nil)
}

// RegisterDelegationWithBackend is the full-signature registration.
// Backend is required for the v4alpha backend; the v2 backend
// ignores it.
func RegisterDelegationWithBackend(reg *Registry, orch *orchestration.Orchestrator, st store.Store, backend *DelegateIntentBackend) {
	selected := delegationBackendFromEnv()

	switch selected {
	case BackendV4Alpha:
		registerV4Alpha(reg, backend)
	case BackendV2:
		registerV2(reg, orch, st)
	default:
		registerV2(reg, orch, st)
	}
}

// registerV2 wires the alpha.18.1 deterministic delegate_intent
// (BindOrchestrator → orch.DelegateIntent). Kept verbatim from the
// pre-Chunk-8.1 implementation.
func registerV2(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	reg.Add(BindOrchestrator("delegate_intent",
		"Wave 5C: Decide whether an intent is handled inline, delegated to sub-agents, or refused (A1: Memory decides). Runs the DelegationRouter pipeline: DECIDE (deterministic rules per vibe_case + bounded LLM choice) → PLAN (subtask graph with dependency batches) → MIND (mindset_apply per subtask: system_prompt + tools + model) → CURATE (agent_memory_delegate per subtask: curated agent_memory context + C2 subagent binding). Returns ready-to-spawn material: the harness injects each subtask's system_prompt + delegation_context into its own Task() spawn call. Gated by DARK_MEMORY_V280=1.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"vibe_case", "task_description"},
			"properties": map[string]any{
				"vibe_case": map[string]any{
					"type": "string",
					"enum": vibecase.JSONSchemaEnum(),
				},
				"task_description": map[string]any{
					"type":        "string",
					"minLength":   10,
					"description": "What the work is. Becomes the seed for mindset composition and the delegation context selection.",
				},
				"operator": map[string]any{
					"type":        "string",
					"description": "Operator id for audit trail (INV-1). Defaults to the active session's operator.",
				},
			},
		}),
		func(ctx context.Context, in orchestration.DelegateIntentInput) (*orchestration.DelegateIntentOutput, error) {
			return orch.DelegateIntent(ctx, in)
		}))
}

// registerV4Alpha wires the alpha.19+ delegate_intent (RunDelegateIntentCore
// in v4alpha/transport/mcp/wire.go). The input is the v3 wire shape
// (DelegateIntentInput); the handler translates to v4alpha's
// DelegateIntentInput, invokes the pipeline, and converts the v4alpha
// output back to the v3 DelegateIntentOutput with 3 additive fields
// (decision / cache_hit / verdict / alternatives[]) for v4alpha-aware
// harnesses.
func registerV4Alpha(reg *Registry, backend *DelegateIntentBackend) {
	reg.Add(&Tool{
		Name: "delegate_intent",
		Description: "Phase 9 alpha.20 (Chunk 8.1): delegate_intent runs the v4alpha DECIDE→EXTRACT→MIND→CURATE pipeline (alpha.19 Chunk 7.1). DECIDE is deterministic (refusal markers / coordination markers / C7 / length>200). EXTRACT (NEW over alpha.18.1) invokes the LLM via the judge-delegator persona when DECIDE=delegate AND length>200 OR vibe_case=C7; validated by drift_judge (eval_type=subtask_extraction). Failures surface as needs_human with alternatives[] (the harness picks one). MIND composes the system_prompt per subtask via composeSystemPrompt. CURATE registers subagent bindings via agent_memory.Save (kind=link, tag=subagent:v1). Wire shape is v3-compatible (handler/case/plan/operator) with three additive v4alpha fields (decision/cache_hit/verdict/alternatives) — see SPEC-alpha-11-phase8.md §3.1. Gated by DARK_DELEGATION_BACKEND (default v4alpha; set DARK_DELEGATION_BACKEND=v2 to roll back to the alpha.18.1 deterministic router).",
		InputSchema: MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"vibe_case", "task_description"},
			"properties": map[string]any{
				"vibe_case": map[string]any{
					"type":        "string",
					"enum":        vibecase.JSONSchemaEnum(),
					"description": "Canonical vibe_case (C1..C7 per spec.go:14-16).",
				},
				"task_description": map[string]any{
					"type":        "string",
					"minLength":   10,
					"description": "≥10 char description of the work to be routed.",
				},
				"operator": map[string]any{
					"type":        "string",
					"description": "Operator id (INV-1 audit owner). Empty defaults to orchestrator_delegate.",
				},
				"parent_session_id": map[string]any{
					"type":        "string",
					"description": "Optional parent session id (CURATE subagent binding). Empty when outside a session context.",
				},
				"parent_agent_id": map[string]any{
					"type":        "string",
					"description": "Optional parent agent id (CURATE subagent binding). Empty when not chained from a vibe_publish.",
				},
			},
		}),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			return handleV4AlphaDelegateIntent(ctx, raw, backend)
		},
	})
}

// handleV4AlphaDelegateIntent is the handler that translates v3 input
// → v4alpha input → invokes RunDelegateIntentCore → translates v4alpha
// output → v3 DelegateIntentOutput.
//
// Error contract:
//   - Input validation errors (vibe_case invalid, task too short)
//     return ToolError{Code: ErrInvalidArgument, Field: ...} per F35.
//   - Pipeline errors surface as ToolResponse{Data: <v3 output with
//     verdict=errored>}. The harness sees the errored verdict and can
//     decide.
func handleV4AlphaDelegateIntent(ctx context.Context, raw json.RawMessage, backend *DelegateIntentBackend) (*ToolResponse, error) {
	var in struct {
		VibeCase        string `json:"vibe_case"`
		TaskDescription string `json:"task_description"`
		Operator        string `json:"operator,omitempty"`
		ParentSessionID string `json:"parent_session_id,omitempty"`
		ParentAgentID   string `json:"parent_agent_id,omitempty"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &ToolResponse{
				Error: typeMismatchToolError("delegate_intent", err),
			}, nil
		}
	}

	// Resolve nil backend → empty deps (pipeline handles nil-safe).
	var llmClient judge.LLMClient
	var extractCache *delegation.ExtractCache
	var memories *agent_memory.Store
	if backend != nil {
		llmClient = backend.LLMClient
		extractCache = backend.ExtractCache
		memories = backend.Memories
	}

	// Invoke the v4alpha pipeline.
	v4in := v4mcpt.DelegateIntentInput{
		VibeCase:        in.VibeCase,
		TaskDescription: in.TaskDescription,
		Operator:        in.Operator,
		ParentSessionID: in.ParentSessionID,
		ParentAgentID:   in.ParentAgentID,
	}
	v4res, err := v4mcpt.RunDelegateIntentCore(ctx, v4in, llmClient, extractCache, memories)
	if err != nil {
		// Map *WireError → ToolError (F35 propagation).
		we, ok := v4mcpt.AsWireError(err)
		if ok {
			return &ToolResponse{
				Error: &ToolError{
					Code:    we.Code,
					Message: we.Message,
					Field:   we.Field,
					Hint:    fmt.Sprintf("Field %q failed validation per delegate_intent input schema.", we.Field),
				},
			}, nil
		}
		return &ToolResponse{Error: ToToolError(err)}, nil
	}

	// Convert v4alpha output → v3 DelegateIntentOutput.
	out := convertV4AlphaToV3Output(v4res, in.Operator)
	return &ToolResponse{Data: out}, nil
}

// convertV4AlphaToV3Output maps the v4alpha wire shape back to the
// v3 DelegateIntentOutput. The mapping:
//
//	handler  = v4alpha decision ("inline"|"delegate"|"refused") →
//	           v3 constructor signature ("HANDLE"|"DELEGATE"|"REFUSE")
//	case     = "" (v4alpha doesn't echo case; caller can use input vibe_case)
//	plan     = subtasks[] converted to DelegateSubTaskOutput
//	reasoning = reasoning (verbatim, includes DECIDE+EXTRACT+JUDGE trace)
//	operator = operator (verbatim)
//
// Plus the three additive v4alpha fields: decision (lowercase),
// cache_hit, verdict, alternatives[].
//
// The v3 wire uses HandlerHandle="HANDLE" / HandlerDelegate="DELEGATE"
// / HandlerRefuse="REFUSE" (NOT "REFUSED" — match the v2 router
// signatures in internal/delegation/router.go). The mapping
// handles the v4alpha's "refused" past-participle → v3's "REFUSE".
func convertV4AlphaToV3Output(v4out *v4mcpt.DelegateIntentOutput, operator string) *orchestration.DelegateIntentOutput {
	out := &orchestration.DelegateIntentOutput{
		Handler:   v4DecisionToV3Handler(v4out.Decision),
		Reasoning: v4out.Reasoning,
		Case:      "", // v4alpha doesn't echo case in output; caller can use input vibe_case
		Operator:  operator,
		// Additive v4alpha fields — populated only here, empty under v2 backend.
		Decision:     v4out.Decision,
		CacheHit:     v4out.CacheHit,
		Verdict:      v4out.Verdict,
		Alternatives: convertV4AlphaAlternatives(v4out.Alternatives),
	}
	if v4out.Subtasks != nil {
		out.Plan = make([]orchestration.DelegateSubTaskOutput, 0, len(v4out.Subtasks))
		for _, st := range v4out.Subtasks {
			out.Plan = append(out.Plan, orchestration.DelegateSubTaskOutput{
				ID:                st.ID,
				VibeCase:          "", // v4alpha doesn't echo vibe_case per subtask; caller knows the parent
				Task:              st.Description,
				SystemPrompt:      st.SystemPrompt,
				DelegationContext: st.DelegationContext,
				SubagentID:        st.SubagentID,
				ToolsRecommended:  st.Tools,
				ModelRecommended:  st.Model,
			})
		}
	}
	return out
}

// v4DecisionToV3Handler maps the v4alpha lowercase decision to the v3
// uppercase Handler constant. The mapping:
//
//	v4 "inline"   → v3 "HANDLE"     (orchestration.HandlerHandle)
//	v4 "delegate" → v3 "DELEGATE"   (orchestration.HandlerDelegate)
//	v4 "refused"  → v3 "REFUSE"     (orchestration.HandlerRefuse, NOT "REFUSED")
//
// Unknown values fall through as-is (uppercased) so a future v4alpha
// decision type does not silently disappear.
func v4DecisionToV3Handler(v4decision string) string {
	switch v4decision {
	case "inline":
		return "HANDLE"
	case "delegate":
		return "DELEGATE"
	case "refused":
		return "REFUSE"
	default:
		return strings.ToUpper(v4decision)
	}
}

// convertV4AlphaAlternatives copies the alternatives slice from
// v4alpha wire shape to v3 wire shape.
func convertV4AlphaAlternatives(in []v4mcpt.DelegateIntentAlternative) []orchestration.DelegateIntentAlternative {
	if in == nil {
		return nil
	}
	out := make([]orchestration.DelegateIntentAlternative, 0, len(in))
	for _, a := range in {
		var outAlt orchestration.DelegateIntentAlternative
		outAlt.ID = a.ID
		outAlt.Label = a.Label
		// AlternativeOutcome uses Kind + Reasoning in both shapes.
		outAlt.Kind = a.Outcome.Kind
		outAlt.Reasoning = a.Outcome.Reasoning
		out = append(out, outAlt)
	}
	return out
}