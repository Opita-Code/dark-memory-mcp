// Package tools — judge.go: the JUDGE namespace (3 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_judge
//	dark_memory_consensus
//	dark_memory_judgment_history
//
// Maps to orchestrator O5 (Judge), O8 (JudgeConsensus) + 1 new
// read-only tool (judgment_history lists past SDDEvaluations via
// Store.ListSDDEvaluations).
package tools

import (
	"context"
	"encoding/json"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterJudge wires the 3 JUDGE tools into the registry.
func RegisterJudge(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// judge — wraps O5 Judge orchestrator (single-sample).
	reg.Add(BindOrchestrator("judge",
		"Run a single LLM-as-judge verdict on content. Eval types: drift_judge, brand_match, compliance_check, pii_detect, prompt_injection_scan, grounding_check.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"eval_type", "content"},
			"properties": map[string]any{
				"eval_type":   map[string]any{"type": "string", "enum": []string{"drift_judge", "brand_match", "compliance_check", "pii_detect", "prompt_injection_scan", "grounding_check"}},
				"target_type": map[string]any{"type": "string"},
				"target_id":   map[string]any{"type": "string"},
				"content":     map[string]any{"type": "string"},
				"model":       map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.JudgeInput) (*orchestration.JudgeOutput, error) {
			return orch.Judge(ctx, in)
		}))

	// consensus — wraps O8 JudgeConsensus orchestrator (N-shot).
	reg.Add(BindOrchestrator("consensus",
		"Run N-shot LLM-as-judge and return modal verdict + confidence interval. N clamped to [1, 7].",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"eval_type", "content"},
			"properties": map[string]any{
				"eval_type":   map[string]any{"type": "string"},
				"target_type": map[string]any{"type": "string"},
				"target_id":   map[string]any{"type": "string"},
				"content":     map[string]any{"type": "string"},
				"n":           map[string]any{"type": "integer", "description": "Sample count. Default 3, clamped to [1, 7]."},
				"model":       map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.JudgeConsensusInput) (*orchestration.JudgeConsensusResult, error) {
			return orch.JudgeConsensus(ctx, in)
		}))

	// judgment_history — read-only list of past SDDEvaluations.
	reg.Add(BindStore("judgment_history",
		"List recent SSD evaluations (judge verdicts) for an eval_type + target. Read-only.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"eval_type": map[string]any{"type": "string", "description": "Filter by eval type. Empty = all."},
				"target_id": map[string]any{"type": "string", "description": "Filter by target id. Empty = all."},
				"limit":     map[string]any{"type": "integer", "description": "Max rows. Default 50."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in JudgmentHistoryInput) (*JudgmentHistoryResult, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 50
			}
			evals, err := s.ListSDDEvaluations(ctx, ssd.ListFilters{
				EvalType: in.EvalType,
				Limit:    limit,
			})
			if err != nil {
				return nil, err
			}
			out := make([]JudgmentHistoryEntry, 0, len(evals))
			filteredOut := 0
			for _, e := range evals {
				// Client-side target_id filter (see JudgmentHistoryInput
				// doc for why).
				if in.TargetID != "" && e.TargetID != in.TargetID {
					filteredOut++
					continue
				}
				out = append(out, JudgmentHistoryEntry{
					ID:         e.ID,
					EvalType:   e.EvalType,
					TargetType: e.TargetType,
					TargetID:   e.TargetID,
					Confidence: e.Confidence,
					Verdict:    parseVerdictJSON(e.VerdictJSON),
					Model:      e.Model,
					CreatedAt:  e.CreatedAt,
				})
			}
			return &JudgmentHistoryResult{Evaluations: out, Count: len(out), FilteredOut: filteredOut}, nil
		}))
}

// JudgmentHistoryInput is the input for judgment_history.
//
// target_id is filtered CLIENT-SIDE after Store.ListSDDEvaluations
// returns the rows (the Store's ListFilters doesn't support
// target_id today). This is fine for typical use (history of one
// artifact, usually <100 rows). For large-scale queries we'd add a
// target_id filter to ssd.ListFilters + the Store layer.
type JudgmentHistoryInput struct {
	EvalType string `json:"eval_type,omitempty"`
	TargetID string `json:"target_id,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// JudgmentHistoryResult is the output for judgment_history.
type JudgmentHistoryResult struct {
	Evaluations []JudgmentHistoryEntry `json:"evaluations"`
	Count       int                    `json:"count"`
	FilteredOut int                    `json:"filtered_out,omitempty"`
}

// JudgmentHistoryEntry is one row in the judgment history.
type JudgmentHistoryEntry struct {
	ID         int64   `json:"id"`
	EvalType   string  `json:"eval_type"`
	TargetType string  `json:"target_type"`
	TargetID   string  `json:"target_id"`
	Confidence float32 `json:"confidence"`
	Verdict    string  `json:"verdict"`
	Model      string  `json:"model,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// parseVerdictJSON returns the canonical verdict (aligned |
// drift_detected | needs_human | unknown) from an SDDEvaluation
// verdict JSON blob. Uses encoding/json so we get accurate parsing
// even for nested structures.
func parseVerdictJSON(blob string) string {
	if blob == "" {
		return "unknown"
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(blob), &v); err != nil {
		return "unknown"
	}
	if aligned, ok := v["aligned"].(bool); ok {
		if aligned {
			return "aligned"
		}
		return "drift_detected"
	}
	// Some judge verdicts use "verdict": "needs_human" directly.
	if verdict, ok := v["verdict"].(string); ok && verdict != "" {
		return verdict
	}
	return "unknown"
}