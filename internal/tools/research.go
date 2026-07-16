// Package tools — research.go: the RESEARCH namespace (3 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_research_topic
//	dark_memory_research_recall
//	dark_memory_research_resume_thread
//
// Maps to orchestrator O3 (ResearchTopic), O4 (RecallContext) + 1
// new wrapper (resume_thread re-calls ResearchTopic with a thread
// hint for continuation).
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterResearch wires the 3 RESEARCH tools into the registry.
func RegisterResearch(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// research_topic — wraps O3 ResearchTopic orchestrator.
	reg.Add(BindOrchestrator("research_topic",
		"Run a research query against the registered ResearchBackend set. Returns deduplicated, confidence-ranked items.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query":     map[string]any{"type": "string", "description": "The natural-language research query."},
				"intent":    map[string]any{"type": "string", "description": "Optional intent tag (e.g. 'cve', 'academic'). Default 'general'."},
				"max_items": map[string]any{"type": "integer", "description": "Cap on returned items. Default 20."},
				"thread_id": map[string]any{"type": "string", "description": "Optional thread id for multi-turn research continuation."},
			},
		}),
		func(ctx context.Context, in orchestration.ResearchTopicInput) (*orchestration.ResearchTopicOutput, error) {
			return orch.ResearchTopic(ctx, in)
		}))

	// research_recall — wraps O4 RecallContext orchestrator.
	reg.Add(BindOrchestrator("research_recall",
		"Recall previously stored research items by query, with a token budget. Runs the Atlan 5-bucket economy pipeline.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query":      map[string]any{"type": "string"},
				"max_tokens": map[string]any{"type": "integer", "description": "Token budget for the response. Default 4000."},
				"min_confidence": map[string]any{"type": "number", "description": "Drop items below this confidence (0..1). Default 0.3."},
			},
		}),
		func(ctx context.Context, in orchestration.RecallContextInput) (*orchestration.RecallContextOutput, error) {
			return orch.RecallContext(ctx, in)
		}))

	// research_resume_thread — re-calls ResearchTopic with a thread_id
	// hint so the new results append to the existing thread rather
	// than starting a new one. Thin wrapper around ResearchTopic.
	reg.Add(BindOrchestrator("research_resume_thread",
		"Continue a multi-turn research thread by re-running ResearchTopic with the same thread_id.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"thread_id", "query"},
			"properties": map[string]any{
				"thread_id": map[string]any{"type": "string", "description": "Existing thread id to resume."},
				"query":     map[string]any{"type": "string", "description": "The follow-up query."},
				"max_items": map[string]any{"type": "integer"},
			},
		}),
		func(ctx context.Context, in ResearchResumeThreadInput) (*orchestration.ResearchTopicOutput, error) {
			// ThreadID is mapped to SessionID today (see note on
			// ResearchResumeThreadInput).
			return orch.ResearchTopic(ctx, orchestration.ResearchTopicInput{
				Query:     in.Query,
				MaxItems:  in.MaxItems,
				SessionID: in.ThreadID,
			})
		}))
}

// ResearchResumeThreadInput is the input for research_resume_thread.
type ResearchResumeThreadInput struct {
	ThreadID string `json:"thread_id"`
	Query    string `json:"query"`
	MaxItems int    `json:"max_items,omitempty"`
}

// Note: ResearchTopicInput does not currently carry ThreadID; the
// orchestrator records the run_id and session_id but a "thread" is
// really session-scoped. The MCP wrapper accepts a thread_id for
// future compatibility (the orchestrator may add thread support
// later) and threads it through SessionID today.