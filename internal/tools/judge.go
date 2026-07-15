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
			for _, e := range evals {
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
			return &JudgmentHistoryResult{Evaluations: out, Count: len(out)}, nil
		}))
}

// JudgmentHistoryInput is the input for judgment_history.
type JudgmentHistoryInput struct {
	EvalType string `json:"eval_type,omitempty"`
	TargetID string `json:"target_id,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// JudgmentHistoryResult is the output for judgment_history.
type JudgmentHistoryResult struct {
	Evaluations []JudgmentHistoryEntry `json:"evaluations"`
	Count       int                    `json:"count"`
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
// drift_detected | needs_human) from an SDDEvaluation verdict JSON
// blob. Tolerant parser: substring match for the {"aligned":...}
// marker; falls back to "unknown" if not parseable.
func parseVerdictJSON(blob string) string {
	// Cheap substring check. We don't need full JSON parsing here —
	// the verdict JSON shape is small and stable.
	if blob == "" {
		return "unknown"
	}
	// Look for "aligned":true → aligned
	idx := indexOfAligned(blob)
	if idx < 0 {
		return "drift_detected"
	}
	// Check if "aligned":true comes before "aligned":false (rare).
	if indexOfFalse(blob, idx) > 0 {
		return "drift_detected"
	}
	return "aligned"
}

func indexOfAligned(s string) int {
	const tag = `"aligned":`
	for i := 0; i+len(tag) <= len(s); i++ {
		if s[i:i+len(tag)] == tag {
			return i + len(tag)
		}
	}
	return -1
}

func indexOfFalse(s string, from int) int {
	if from >= len(s) {
		return -1
	}
	// Look for "false" within ~32 chars after the aligned marker.
	end := from + 32
	if end > len(s) {
		end = len(s)
	}
	for i := from; i+5 <= end; i++ {
		if s[i:i+5] == "false" {
			return i
		}
	}
	return -1
}