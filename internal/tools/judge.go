// Package tools — judge.go: the JUDGE namespace (3 tools).
//
// Per RFC §5 / D-9:
//
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
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterJudge wires the JUDGE namespace tools into the registry.
// v2.17.0 (spec 1155): adds judge_list_personas alongside the
// existing judge + consensus + judgment_history.
func RegisterJudge(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// judge — wraps O5 Judge orchestrator (single-sample).
	reg.Add(BindOrchestrator("judge",
		"Run a single LLM-as-judge verdict on content. Eval types: drift_judge, brand_match, compliance_check, pii_detect, prompt_injection_scan, grounding_check, mindset_compose, mindset_quality, spec_test_alignment, mutation_score_check, test_quality_review, security_coverage, resilience_check, oracle_quality. v2.4.2: brand_match + compliance_check consult prior agent_memory decisions/findings (filtered by resolved agent_id); pass no_enrich=true to opt out. v2.17.0: persona_id selects the evaluation lens; spec_intent is included in the user prompt.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"eval_type", "content"},
			"properties": map[string]any{
				"eval_type":   map[string]any{"type": "string", "enum": []string{"drift_judge", "brand_match", "compliance_check", "pii_detect", "prompt_injection_scan", "grounding_check", "mindset_compose", "mindset_quality", "spec_test_alignment", "mutation_score_check", "test_quality_review", "security_coverage", "resilience_check", "oracle_quality"}},
				"target_type": map[string]any{"type": "string"},
				"target_id":   map[string]any{"type": "string"},
				"content":     map[string]any{"type": "string"},
				"model":       map[string]any{"type": "string"},
				"agent_id":    map[string]any{"type": "string", "description": "v2.4.2: optional Mem0 agent_id (LLM identity). Resolved priority: caller input > projects.default_agent_id > empty string."},
				"no_enrich":   map[string]any{"type": "boolean", "description": "v2.4.2: opt-out escape hatch. Default false (enrichment runs for brand_match + compliance_check). When true, Judge passes raw content to the LLM."},
				"vibe_case":   map[string]any{"type": "string", "enum": []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}, "description": "v2.12.0: vibe-flow case of the artifact (C1=code, C2=text, ...). When set, the judge uses a G-Eval rubric for that case (technical criteria for code). Empty = legacy generic prompt."},
				"persona_id":  map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): explicit persona id. If empty, the registry resolves by eval_type default (lex-smallest ID wins). Use judge_list_personas to enumerate available personas."},
				"spec_intent": map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): one-paragraph description of what the artifact should be. Included in the user prompt as the 'Spec intent' section."},
			},
		}),
		func(ctx context.Context, in orchestration.JudgeInput) (*orchestration.JudgeOutput, error) {
			return orch.Judge(ctx, in)
		}))

	// consensus — wraps O8 JudgeConsensus orchestrator (N-shot).
	reg.Add(BindOrchestrator("consensus",
		"Run N-shot LLM-as-judge and return modal verdict + confidence interval. N clamped to [1, 7]. v2.4.2: forwards agent_id + no_enrich to all N samples so each gets the same agent-scoped enrichment. v2.17.0: forwards persona_id and spec_intent to all N samples so each uses the same persona.",
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
				"agent_id":    map[string]any{"type": "string", "description": "v2.4.2: forwarded to all N Judge samples for consistent agent-scoped enrichment."},
				"no_enrich":   map[string]any{"type": "boolean", "description": "v2.4.2: forwarded to all N Judge samples for consistent opt-out semantics. Default false."},
				"vibe_case":   map[string]any{"type": "string", "enum": []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}, "description": "v2.12.0: vibe-flow case (C1=code, C2=text, ...). Forwarded to all N samples so each uses the same G-Eval rubric. Empty = legacy."},
				"persona_id":  map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): explicit persona id; forwarded to all N samples."},
				"spec_intent": map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): spec intent; forwarded to all N samples."},
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

	// judge_list_personas (v2.17.0, spec 1155) — enumerate registered
	// personas. No-op when the registry has not been initialized
	// (returns empty list with a synthetic note).
	reg.Add(BindOrchestrator("judge_list_personas",
		"List the registered persona registries (compiled defaults + Markdown overrides). Read-only. v2.17.0 (spec 1155).",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, in struct{}) (*JudgeListPersonasResult, error) {
			reg, err := orch.PersonaRegistry()
			if err != nil {
				return nil, fmt.Errorf("judge_list_personas: %w", err)
			}
			personas := reg.List()
			out := make([]JudgePersonaSummary, 0, len(personas))
			for _, p := range personas {
				out = append(out, JudgePersonaSummary{
					ID:        p.ID,
					Name:      p.Name,
					EvalTypes: p.EvalTypes,
					Default:   p.Default,
					Source:    p.Source,
				})
			}
			return &JudgeListPersonasResult{Personas: out, Count: len(out)}, nil
		}))
}

// JudgeListPersonasResult is the result of judge_list_personas.
type JudgeListPersonasResult struct {
	Personas []JudgePersonaSummary `json:"personas"`
	Count    int                   `json:"count"`
}

// JudgePersonaSummary is one persona in the judge_list_personas output.
type JudgePersonaSummary struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	EvalTypes []string `json:"eval_types"`
	Default   bool     `json:"default"`
	Source    string   `json:"source"`
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
//
// v2.21.1 (spec 1205 P0): MiniMax-M3 with thinking adaptive answers
// in YAML or markdown (```yaml\nverdict: needs_human\n...```), NOT
// JSON. The old parser returned "unknown" for any non-JSON blob,
// which made the judgment_history lie (a rich needs_human reasoning
// was recorded as "unknown") and hid real drift decisions. This
// parser now shares the same whitespace-normalised YAML/markdown
// fallback as publish_vibe.parseVerdict, so the recorded verdict
// matches the pipeline's drift decision.
func parseVerdictJSON(blob string) string {
	if blob == "" {
		return "unknown"
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(blob), &v); err == nil {
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
	}
	// YAML / markdown fallback (MiniMax-M3 with thinking adaptive).
	// Collapse whitespace, strip backticks/bold, compact colons, then
	// match the canonical verdict values. Mirrors
	// orchestration.parseVerdict's lenient path (v2.21.0, spec 1200).
	normalized := strings.ToLower(blob)
	var b strings.Builder
	b.Grow(len(normalized))
	prevSpace := false
	for _, r := range normalized {
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if isSpace {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	b2 := strings.ReplaceAll(b.String(), "`", "")
	b2 = strings.ReplaceAll(b2, "**", "")
	compact := strings.ReplaceAll(b2, ": ", ":")
	compact = strings.ReplaceAll(compact, " :", ":")
	switch {
	case strings.Contains(compact, "verdict:aligned"),
		strings.Contains(compact, `verdict:"aligned"`):
		return "aligned"
	case strings.Contains(compact, "verdict:needs_human"),
		strings.Contains(compact, `verdict:"needs_human"`):
		return "needs_human"
	case strings.Contains(compact, "verdict:drift_detected"),
		strings.Contains(compact, `verdict:"drift_detected"`):
		return "drift_detected"
	}
	return "unknown"
}
