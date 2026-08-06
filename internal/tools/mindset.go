// Package tools — mindset.go: the MINDSET namespace (1 tool).
//
// v2.7.0-alpha. Per spec D-12 / BRIDGE_AND_COEXISTENCE.md §3:
//
//	dark_memory_mindset_apply
//
// Procedurally composes a subagent system_prompt given (vibe_case,
// task_description) and validates it via LLM-as-judge (eval_type=
// mindset_quality) before returning. Cached in agent_memory for
// sub-second repeat hits.
//
// Positioned between AGENT_MEMORY (the data plane it caches against)
// and JUDGE (the validator). See docs/mindsets.md for operator-facing
// usage.
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// RegisterMindset wires the 1 MINDSET tool into the registry.
func RegisterMindset(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// mindset_apply — wraps O13 MindsetApply orchestrator (procedural +
	// judge-validated). Cache hit returns in <50ms with 0 LLM calls;
	// cache miss loops composition + validation up to
	// DARK_MINDSET_MAX_ITERATIONS times. See docs/mindsets.md.
	reg.Add(BindOrchestrator("mindset_apply",
		"v2.7.0-alpha: Procedurally compose a subagent system_prompt for the given (vibe_case, task_description) and validate it via LLM-as-judge before returning. Cached in agent_memory (TTL via DARK_MINDSET_CACHE_TTL, default 1h). Composition iterates up to DARK_MINDSET_MAX_ITERATIONS (default 3) if the judge rejects the first attempt. Returns a ready-to-inject system_prompt plus tools_recommended and model_recommended for the harness to spawn the subagent. Use case: the harness spawns the subagent via its Task/Agent tool; dark-memory provides the mindset (role + goal + backstory + constraints).",
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
					"description": "What the subagent should do. Becomes the seed for procedural composition.",
				},
				"model_floor": map[string]any{
					"type":        "string",
					"description": "Minimum capability for the subagent (sonnet|opus|haiku|inherit). Optional.",
				},
				"operator": map[string]any{
					"type":        "string",
					"description": "Operator id for audit trail (INV-1). Defaults to 'orchestrator_mindset'.",
				},
			},
		}),
		func(ctx context.Context, in orchestration.MindsetApplyInput) (*orchestration.MindsetApplyOutput, error) {
			return orch.MindsetApply(ctx, in)
		}))
}
