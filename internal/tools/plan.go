package tools

// SPEC-194 (T7): the PLAN tools.
//
// Three tools in a new PLAN namespace:
//
//	plan_create  — proposes a plan (candidates, tier, budget)
//	plan_get     — reads the plan of a spec
//	plan_decide  — the human decides: execute | edit | cancel
//
// They are the first half of SPEC-194: the plan itself. The second
// half — the GATE that makes the plan obligatory — is PR B.
//
// # The lifecycle exemption
//
// These three tools are EXEMPT from the plan gate (SPEC-194 §3.4-bis,
// AC13). Without that exemption there is a DEADLOCK: plan_decide is
// the tool that RECORDS the approval, so requiring an approved plan
// in order to approve the first plan is impossible.
//
// The exemption lives in the MIDDLEWARE, in a closed list
// (PlanLifecycleExempt), and NOT in the arguments: a flag could be
// passed by any caller, and the one caller that must not use it is
// exactly the one that would.
//
// # The ranker
//
// The candidates come from the tool registry itself: the shortlist is
// ranked over the FULL doc of every tool (name + description +
// schema), because that is the configuration the literature reports as
// the best one (ToolRet: BM25 over the registry, ~40x faster than
// BGE-large on CPU, and the full doc wins).
//
// The registry is the corpus, so the ranker is built once per boot and
// reused: the corpus does not change while the process lives.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/plan"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// RegisterPlan wires the PLAN namespace (3 tools).
//
// The ranker is built from the registry AFTER the other namespaces are
// registered, so the corpus is the WHOLE canonical surface: a plan
// that intends to write memory must be able to see agent_memory_save
// among the candidates.
//
// The judge is optional: without it, the shortlist is proposed but not
// validated, and the result says so (Judged=false). That is honest
// rather than silent.
func RegisterPlan(reg *Registry, orch *orchestration.Orchestrator) error {
	if reg == nil {
		return errors.New("tools: RegisterPlan: nil registry")
	}
	if orch == nil {
		return errors.New("tools: RegisterPlan: nil orchestrator")
	}

	// The corpus: the whole canonical surface, with the full doc.
	docs := make([]plan.Document, 0, len(reg.ListCanonical()))
	for _, t := range reg.ListCanonical() {
		docs = append(docs, plan.Document{
			Tool: t.Name,
			Doc:  toolFullDoc(t),
		})
	}
	ranker := plan.NewRanker(docs)
	orch.SetPlanRanker(ranker)

	reg.Add(BindOrchestrator("plan_create",
		"Propose a plan for a spec: the tool candidates with their weights, the depth tier and the consumable budget. The plan starts in draft; a human approves it with plan_decide.",
		MustJSONSchema(map[string]any{
			"type":                 "object",
			"required":             []string{"spec_id", "intent"},
			"additionalProperties": false,
			"properties": map[string]any{
				"spec_id": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "The spec this plan describes. The plan is a spec with a plan on it.",
				},
				"intent": map[string]any{
					"type":        "string",
					"minLength":   3,
					"description": "What the work is. The candidates are ranked against it, and the judge validates it.",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "The session that owns the call. It decides which project the plan lands in.",
				},
				"max_candidates": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Cap on the shortlist. Default 8. The shortlist is a proposal, not a catalogue.",
				},
				"max_tool_calls": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Override the tier's proposed tool-call budget.",
				},
				"max_judge_calls": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Override the tier's proposed judge-call budget.",
				},
			},
		}),
		func(ctx context.Context, in orchestration.PlanCreateInput) (*orchestration.PlanCreateResult, error) {
			return orch.PlanCreate(ctx, in)
		}))

	reg.Add(BindOrchestrator("plan_get",
		"Read the plan of a spec: its status, its candidates, its tier and its effective budget. Without spec_id, it reads the open plan of the session.",
		MustJSONSchema(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"spec_id": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "The spec whose plan to read. Omit to read the open plan of the session.",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "The session that owns the call.",
				},
			},
		}),
		func(ctx context.Context, in orchestration.PlanGetInput) (*orchestration.PlanGetResult, error) {
			return orch.PlanGet(ctx, in)
		}))

	reg.Add(BindOrchestrator("plan_decide",
		"Record the human decision on a plan: execute (approve it), edit (reopen it with a prompt) or cancel (stop it). The decision is recorded in write_audit.",
		MustJSONSchema(map[string]any{
			"type":                 "object",
			"required":             []string{"decision"},
			"additionalProperties": false,
			"properties": map[string]any{
				"spec_id": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "The spec whose plan is being decided. Omit to decide on the open plan of the session.",
				},
				"decision": map[string]any{
					"type":        "string",
					"enum":        []string{"execute", "edit", "cancel"},
					"description": "execute approves the plan; edit reopens it with a prompt; cancel stops it.",
				},
				"edit_prompt": map[string]any{
					"type":        "string",
					"description": "The reviewer's prompt. Required when decision is edit.",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "The session that owns the call.",
				},
				"decided_by": map[string]any{
					"type":        "string",
					"description": "Who decided. Defaults to the human.",
				},
			},
		}),
		func(ctx context.Context, in orchestration.PlanDecideInput) (*orchestration.PlanDecideResult, error) {
			return orch.PlanDecide(ctx, in)
		}))

	return nil
}

// toolFullDoc returns the FULL documentation of a tool, flattened into
// one text: the name, the description and the schema.
//
// The full doc is the corpus on purpose: measured, BM25 over the
// registry with the full doc of each tool was the best configuration
// (the tool's own description alone loses the terms that live in its
// parameters, and those are often the terms the intent uses).
func toolFullDoc(t *Tool) string {
	var b strings.Builder
	b.WriteString(t.Name)
	b.WriteString("\n")
	if t.Description != "" {
		b.WriteString(t.Description)
		b.WriteString("\n")
	}
	if len(t.InputSchema) > 0 {
		// The schema is JSON, not prose: flatten it into a token soup
		// by writing its keys and string values. A JSON blob would
		// tokenize into punctuation-heavy fragments and lose the
		// parameter NAMES, which are the useful part.
		var raw map[string]any
		if err := json.Unmarshal(t.InputSchema, &raw); err == nil {
			writeSchemaTokens(&b, raw)
		}
	}
	return b.String()
}

// writeSchemaTokens walks a decoded schema and writes its keys and
// string values, recursively.
func writeSchemaTokens(b *strings.Builder, v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			b.WriteString(k)
			b.WriteString(" ")
			writeSchemaTokens(b, val)
		}
	case []any:
		for _, val := range x {
			writeSchemaTokens(b, val)
		}
	case string:
		b.WriteString(x)
		b.WriteString(" ")
	}
}

// planCandidatesFromDocs is a helper for tests and for the judge
// adapter: it builds the candidate list from a corpus without going
// through the orchestrator.
func planCandidatesFromDocs(docs []plan.Document, intent string, k int) []vibeflow.Candidate {
	return plan.NewRanker(docs).Rank(intent, k)
}
