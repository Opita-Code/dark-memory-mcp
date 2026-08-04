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
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// RegisterDelegation wires the 1 DELEGATION tool into the registry.
func RegisterDelegation(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
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
