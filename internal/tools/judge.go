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
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/judgeparse"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterJudge wires the JUDGE namespace tools into the registry.
// v2.17.0 (spec 1155): adds judge_list_personas alongside the
// existing judge + consensus + judgment_history.
func RegisterJudge(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// judge — wraps O5 Judge orchestrator (single-sample).
	//
	// v2.20.0 T12 (spec 1276): artifact_ref is the artifact-anchored
	// path for drift_judge. When EvalType=drift_judge AND artifact_ref
	// is set, the judge resolves the artifact (file/git_sha/url/
	// spec_id/artifact_id) and NLI-anchors the verdict to the
	// resolved SHA-256. The Content field is ignored on this branch.
	// For other eval_types, Content remains the canonical input and
	// artifact_ref is ignored. The Content field is required for non-
	// drift_judge eval_types; for drift_judge, supply artifact_ref
	// instead (Content is deprecated on this branch and emits a
	// deprecation warn row in the Error Observatory).
	reg.Add(BindOrchestrator("judge",
		"Run a single LLM-as-judge verdict on content. Eval types: drift_judge, brand_match, compliance_check, pii_detect, prompt_injection_scan, grounding_check, mindset_compose, mindset_quality, spec_test_alignment, mutation_score_check, test_quality_review, security_coverage, resilience_check, oracle_quality. v2.20.0 T12 (spec 1276): for drift_judge, supply artifact_ref instead of content — the judge resolves the artifact (file/git_sha/url/spec_id/artifact_id) and NLI-anchors the verdict. The Content field is ignored on this branch. For other eval_types, Content remains the canonical input. v2.4.2: brand_match + compliance_check consult prior agent_memory decisions/findings (filtered by resolved agent_id); pass no_enrich=true to opt out. v2.17.0: persona_id selects the evaluation lens; spec_intent is included in the user prompt.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"eval_type"},
			"properties": map[string]any{
				"eval_type":   map[string]any{"type": "string", "enum": []string{"drift_judge", "brand_match", "compliance_check", "pii_detect", "prompt_injection_scan", "grounding_check", "mindset_compose", "mindset_quality", "spec_test_alignment", "mutation_score_check", "test_quality_review", "security_coverage", "resilience_check", "oracle_quality"}},
				"target_type": map[string]any{"type": "string"},
				"target_id":   map[string]any{"type": "string"},
				// content is required for non-drift_judge eval_types;
				// for drift_judge, supply artifact_ref instead.
				"content": map[string]any{"type": "string", "description": "Text to evaluate. Required for non-drift_judge eval_types. For drift_judge, supply artifact_ref instead — the Content field is ignored when artifact_ref is set (v2.20.0 T12, spec 1276)."},
				"model":       map[string]any{"type": "string"},
				"agent_id":    map[string]any{"type": "string", "description": "v2.4.2: optional Mem0 agent_id (LLM identity). Resolved priority: caller input > projects.default_agent_id > empty string."},
				"no_enrich":   map[string]any{"type": "boolean", "description": "v2.4.2: opt-out escape hatch. Default false (enrichment runs for brand_match + compliance_check). When true, Judge passes raw content to the LLM."},
				"vibe_case":   map[string]any{"type": "string", "enum": []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}, "description": "v2.12.0: vibe-flow case of the artifact (C1=code, C2=text, ...). When set, the judge uses a G-Eval rubric for that case (technical criteria for code). Empty = legacy generic prompt."},
				"persona_id":  map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): explicit persona id. If empty, the registry resolves by eval_type default (lex-smallest ID wins). Use judge_list_personas to enumerate available personas."},
				"spec_intent": map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): one-paragraph description of what the artifact should be. Included in the user prompt as the 'Spec intent' section. Required for drift_judge with artifact_ref (the hypothesis)."},
				// v2.20.0 T12 (spec 1276): artifact_ref anchors the
				// drift_judge path to a resolved artifact. The
				// single-shot Judge resolves the artifact via
				// artifact.Resolver (T01), NLI-scores the resolved
				// bytes against the spec_intent hypothesis, and
				// returns the verdict bound to the artifact's SHA-256
				// (v29 audit columns). The caller cannot influence
				// the verdict by submitting arbitrary text.
				//
				// Supply exactly one of: file.path, git_sha.path +
				// git_sha, url, spec_id, artifact_id. The resolved
				// SHA-256 is bound to the verdict for audit. Ignored
				// for non-drift_judge eval_types.
				"artifact_ref": map[string]any{
					"type":        "object",
					"description": "v2.20.0 T12 (spec 1276): artifact reference for drift_judge. Anchors the judge to a resolved artifact (file/git_sha/url/spec_id/artifact_id). Required for drift_judge (the Content field is ignored when artifact_ref is set). Ignored for other eval_types.",
					"properties": map[string]any{
						"kind": map[string]any{
							"type": "string",
							"enum": []string{"file", "git_sha", "url", "spec_id", "artifact_id"},
						},
						"path":        map[string]any{"type": "string", "description": "Kind=file or git_sha: filesystem path."},
						"git_repo":    map[string]any{"type": "string", "description": "Kind=git_sha: working dir for git cat-file."},
						"git_sha":     map[string]any{"type": "string", "description": "Kind=git_sha: pinned commit hash."},
						"url":         map[string]any{"type": "string", "description": "Kind=url: URL (SSRF-guarded at resolve time)."},
						"spec_id":     map[string]any{"type": "integer", "description": "Kind=spec_id: vibe_specs.id."},
						"artifact_id": map[string]any{"type": "integer", "description": "Kind=artifact_id: vibe_artifacts.id."},
						"range": map[string]any{
							"type":        "object",
							"description": "Optional byte window [start, end].",
							"properties": map[string]any{
								"start": map[string]any{"type": "integer"},
								"end":   map[string]any{"type": "integer"},
							},
						},
						"max_bytes": map[string]any{"type": "integer", "description": "Cap on resolved bytes; default 256 KiB, hard cap 4 MiB."},
					},
				},
			},
		}),
		func(ctx context.Context, in orchestration.JudgeInput) (*orchestration.JudgeOutput, error) {
			return orch.Judge(ctx, in)
		}))

	// consensus — wraps O8 JudgeConsensus orchestrator (N-shot).
	//
	// v2.20.0 T09 (spec 1276): artifact_ref is the artifact-anchored
	// path for drift_judge. When EvalType=drift_judge AND
	// artifact_ref is set, the consensus path runs against the
	// resolved artifact (file/git_sha/url/spec_id/artifact_id) and
	// the result is bound to the artifact's SHA-256. The Content
	// field is ignored on this branch. For other eval_types, Content
	// remains the canonical input.
	reg.Add(BindOrchestrator("consensus",
		"Run N-shot LLM-as-judge and return modal verdict + confidence interval. N clamped to [1, 7]. v2.4.2: forwards agent_id + no_enrich to all N samples so each gets the same agent-scoped enrichment. v2.17.0: forwards persona_id and spec_intent to all N samples so each uses the same persona. v2.20.0 T09 (spec 1276): for drift_judge, supply artifact_ref to anchor consensus to a resolved artifact (recommended). The Content field is then ignored.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"eval_type"},
			"properties": map[string]any{
				"eval_type":   map[string]any{"type": "string"},
				"target_type": map[string]any{"type": "string"},
				"target_id":   map[string]any{"type": "string"},
				// content is required for non-drift_judge eval_types;
				// for drift_judge, supply artifact_ref instead.
				"content": map[string]any{"type": "string", "description": "Text to evaluate. Required for non-drift_judge eval_types. For drift_judge, supply artifact_ref instead — the Content field is ignored when artifact_ref is set."},
				"n":       map[string]any{"type": "integer", "description": "Sample count. Default 3, clamped to [1, 7]."},
				"model":   map[string]any{"type": "string"},
				"agent_id":    map[string]any{"type": "string", "description": "v2.4.2: forwarded to all N Judge samples for consistent agent-scoped enrichment."},
				"no_enrich":   map[string]any{"type": "boolean", "description": "v2.4.2: forwarded to all N Judge samples for consistent opt-out semantics. Default false."},
				"vibe_case":   map[string]any{"type": "string", "enum": []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}, "description": "v2.12.0: vibe-flow case (C1=code, C2=text, ...). Forwarded to all N samples so each uses the same G-Eval rubric. Empty = legacy."},
				"persona_id":  map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): explicit persona id; forwarded to all N samples."},
				"spec_intent": map[string]any{"type": "string", "description": "v2.17.0 (spec 1155): spec intent; forwarded to all N samples. Required for drift_judge with artifact_ref."},
				// v2.20.0 T09 (spec 1276): artifact_ref anchors the
				// drift_judge consensus path to a resolved artifact.
				// The judge resolves the artifact via
				// artifact.Resolver (T01), chunks the resolved bytes
				// into N pieces, scores each chunk independently
				// via nli.Provider (T05), and returns the modal
				// verdict across chunks. The caller cannot influence
				// the verdict by submitting arbitrary text.
				//
				// Supply exactly one of: file.path, git_sha.path +
				// git_sha, url, spec_id, artifact_id. The resolved
				// SHA-256 is bound to the verdict for audit.
				"artifact_ref": map[string]any{
					"type":        "object",
					"description": "v2.20.0 T09 (spec 1276): artifact reference for drift_judge consensus. Required for the artifact-anchored path (recommended for drift_judge). Ignored for other eval_types.",
					"properties": map[string]any{
						"kind": map[string]any{
							"type": "string",
							"enum": []string{"file", "git_sha", "url", "spec_id", "artifact_id"},
						},
						"path":        map[string]any{"type": "string", "description": "Kind=file or git_sha: filesystem path."},
						"git_repo":    map[string]any{"type": "string", "description": "Kind=git_sha: working dir for git cat-file."},
						"git_sha":     map[string]any{"type": "string", "description": "Kind=git_sha: pinned commit hash."},
						"url":         map[string]any{"type": "string", "description": "Kind=url: URL (SSRF-guarded at resolve time)."},
						"spec_id":     map[string]any{"type": "integer", "description": "Kind=spec_id: vibe_specs.id."},
						"artifact_id": map[string]any{"type": "integer", "description": "Kind=artifact_id: vibe_artifacts.id."},
						"range": map[string]any{
							"type":        "object",
							"description": "Optional byte window [start, end].",
							"properties": map[string]any{
								"start": map[string]any{"type": "integer"},
								"end":   map[string]any{"type": "integer"},
							},
						},
						"max_bytes": map[string]any{"type": "integer", "description": "Cap on resolved bytes; default 256 KiB, hard cap 4 MiB."},
					},
				},
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
				ID:            e.ID,
				EvalType:      e.EvalType,
				TargetType:    e.TargetType,
				TargetID:      e.TargetID,
				Confidence:    e.Confidence,
				Verdict:       parseVerdictJSON(e.VerdictJSON),
				Model:         e.Model,
				CreatedAt:     e.CreatedAt,
				MerkleRoot:    e.MerkleRoot,
				ArtifactSource: e.ArtifactSource,
				ArtifactSHA256: e.ArtifactSHA256,
				ArtifactPath:   e.ArtifactPath,
				ArtifactSize:   e.ArtifactSize,
				ChunkIndex:     e.ChunkIndex,
				ChunkTotal:     e.ChunkTotal,
				NLIProviderID:  e.NLIProviderID,
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
//
// v29 (spec 1276, T10) added the v29 audit + anchor columns to the
// entry so the operator can audit "which bytes were evaluated" and
// "which chunk of which consensus run" without parsing the
// VerdictJSON blob. The fields are populated only by drift_judge +
// drift_judge_consensus; non-drift evaluations leave them empty.
type JudgmentHistoryEntry struct {
	ID         int64   `json:"id"`
	EvalType   string  `json:"eval_type"`
	TargetType string  `json:"target_type"`
	TargetID   string  `json:"target_id"`
	Confidence float32 `json:"confidence"`
	Verdict    string  `json:"verdict"`
	Model      string  `json:"model,omitempty"`
	CreatedAt  string  `json:"created_at"`
	// v29 anchor + audit columns (spec 1276 T10). Empty for
	// non-drift evaluations and pre-v29 rows.
	MerkleRoot     string `json:"merkle_root,omitempty"`
	ArtifactSource string `json:"artifact_source,omitempty"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	ArtifactPath   string `json:"artifact_path,omitempty"`
	ArtifactSize   int64  `json:"artifact_size,omitempty"`
	// ChunkIndex is 0 for non-consensus rows and for the consensus
	// row itself; N (>= 1) for the N-th chunk of a consensus run.
	ChunkIndex int `json:"chunk_index,omitempty"`
	// ChunkTotal is 0 for non-consensus; N (>= 1) for any row in a
	// consensus run (including the consensus row itself).
	ChunkTotal int `json:"chunk_total,omitempty"`
	// NLIProviderID is the nli.Provider.ID() that scored the verdict.
	NLIProviderID string `json:"nli_provider_id,omitempty"`
}

// parseVerdictJSON returns the canonical verdict (aligned |
// drift_detected | needs_human | unknown) from an SDDEvaluation
// verdict JSON blob. t3 (spec 1242): delegates to the shared
// judgeparse package — the single canonical verdict parser used by the
// vibe pipeline, the M6 drift gate, and this read-only history view.
// Contract preserved: empty → unknown; verbatim pass-through for
// non-canonical verdict values; YAML/markdown fallback; unknown
// default. The TD-J4 last-occurrence scan also fixes the old
// Contains-ordered fallback, which could record a verdict token QUOTED
// in the judge's reasoning as the actual verdict.
func parseVerdictJSON(blob string) string {
	return judgeparse.ParseHistoryVerdict(blob)
}
