// judge_* tools — the v4-alpha LLM-backed judge surface (ADR-007 C2).
//
//   - dark_memory_judge              → judge.Pipeline.Evaluate (one-shot)
//   - dark_memory_consensus          → judge.ConsensusWithSemaphore (N-shot)
//   - dark_memory_judgment_history   → read-only history of past verdicts
//   - dark_memory_judge_list_personas → list the 11 registered personas
//
// All 4 tools share the v4alpha/judge.Pipeline wired at server boot
// (see server.go). The Pipeline owns the LLMClient + PersonaRegistry
// + RubricRegistry — the tools are thin closures over them.
//
// judgment_history is a stub in commit 2: verdict persistence lands
// in commit 3 (audit emission + ssd_evaluations row). Until then the
// history is empty; the tool is registered so the wire contract is
// stable across commits (callers don't need to switch tool names
// when persistence lands).
package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

const (
	judgeToolName                  = "dark_memory_judge"
	judgeConsensusToolName         = "dark_memory_consensus"
	judgeJudgmentHistoryToolName   = "dark_memory_judgment_history"
	judgeListPersonasToolName      = "dark_memory_judge_list_personas"
)

func registerJudgeTools(s *Server) {
	registerJudge(s)
	registerJudgeConsensus(s)
	registerJudgeJudgmentHistory(s)
	registerJudgeListPersonas(s)
}

// ---------- judge (one-shot) ----------

type judgeInput struct {
	EvalType        string  `json:"eval_type" jsonschema:"required" jsonschema_description:"drift_judge | brand_match | compliance_check | grounding_check"`
	SpecIntent      string  `json:"spec_intent" jsonschema:"required" jsonschema_description:"One-paragraph 'what should this artifact be' hypothesis"`
	VibeCase        string  `json:"vibe_case" jsonschema:"required" jsonschema_description:"C1..C7 (code, text, decision, research, video, audio, multi)"`
	ArtifactType    string  `json:"artifact_type,omitempty" jsonschema_description:"text | file | (others reserved for commit 3)"`
	ArtifactText    string  `json:"artifact_text,omitempty" jsonschema_description:"Inline text when artifact_type=text"`
	ArtifactPath    string  `json:"artifact_path,omitempty" jsonschema_description:"File path when artifact_type=file"`
	PersonaID       string  `json:"persona_id,omitempty" jsonschema_description:"Optional explicit persona id"`
	SchemaVersion   string  `json:"schema_version,omitempty" jsonschema_description:"Defaults to project schema version"`
	MaxArtifactBytes int     `json:"max_artifact_bytes,omitempty" jsonschema_description:"Cap for EC-004 (default 256 KiB)"`
}

type judgeOutput struct {
	Verdict         string  `json:"verdict"`
	Confidence      float64 `json:"confidence"`
	Reasoning       string  `json:"reasoning,omitempty"`
	PersonaID       string  `json:"persona_id,omitempty"`
	RubricVersion   string  `json:"rubric_version,omitempty"`
	Criteria        []judgeCriterionOutput `json:"criteria,omitempty"`
	EvidenceCount   int     `json:"evidence_count"`
	EdgeCaseHitIDs  []string `json:"edge_case_hit_ids,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	Model           string  `json:"model,omitempty"`
}

type judgeCriterionOutput struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Score  float64 `json:"score"`
	Note   string  `json:"note,omitempty"`
}

func registerJudge(s *Server) {
	tool := mcp.NewTool(judgeToolName,
		mcp.WithDescription("LLM-backed one-shot judge evaluation. Runs the 6-step pipeline (ADR-007 §2) and returns the Verdict."),
		mcp.WithInputSchema[judgeInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in judgeInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := validateJudgeInput(&in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		pReq := buildEvaluateRequest(&in)
		v, err := s.judgePipeline.Evaluate(ctx, pReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("judge: %v", err)), nil
		}
		out := verdictToOutput(v)
		return resultJSON(out)
	})
}

// validateJudgeInput checks the input shape. Mirrors the Pipeline's
// step [1] invariants but surfaces errors BEFORE the LLM call so
// the operator gets a fast-fail.
func validateJudgeInput(in *judgeInput) error {
	if in.EvalType == "" {
		return fmt.Errorf("judge: eval_type required")
	}
	if in.VibeCase == "" {
		return fmt.Errorf("judge: vibe_case required (C1..C7)")
	}
	if len(in.SpecIntent) < 10 {
		return fmt.Errorf("judge: spec_intent too short (< 10 chars, EC-006)")
	}
	switch in.ArtifactType {
	case "", "text":
		if in.ArtifactText == "" {
			return fmt.Errorf("judge: artifact_text required when artifact_type=text")
		}
	case "file":
		if in.ArtifactPath == "" {
			return fmt.Errorf("judge: artifact_path required when artifact_type=file")
		}
	default:
		return fmt.Errorf("judge: artifact_type %q not supported (commit 2: text|file)", in.ArtifactType)
	}
	return nil
}

// buildEvaluateRequest converts the tool input into a v4alpha
// judge.EvaluateRequest. Centralised so the wire ↔ pipeline
// conversion is testable + one place to evolve.
func buildEvaluateRequest(in *judgeInput) judge.EvaluateRequest {
	pReq := judge.EvaluateRequest{
		EvalType:      in.EvalType,
		SpecIntent:    in.SpecIntent,
		VibeCase:      in.VibeCase,
		PersonaID:     in.PersonaID,
		SchemaVersion: in.SchemaVersion,
	}
	if in.SchemaVersion == "" {
		pReq.SchemaVersion = "v4alpha/2026-09-27/001"
	}
	switch in.ArtifactType {
	case "", "text":
		pReq.ArtifactContent = []byte(in.ArtifactText)
	case "file":
		pReq.Ref = &judge.Ref{
			Kind: "file",
			Path: in.ArtifactPath,
			MaxBytes: in.MaxArtifactBytes,
		}
	}
	return pReq
}

// verdictToOutput projects a *judge.Verdict into the wire output
// shape. Excludes Criteria + Evidence by default (those are
// diagnostic fields; callers who need them can read the audit
// row once commit 3 lands).
func verdictToOutput(v *judge.Verdict) judgeOutput {
	out := judgeOutput{
		Verdict:       v.Verdict,
		Confidence:    v.Confidence,
		Reasoning:     v.Reasoning,
		PersonaID:     v.PersonaID,
		RubricVersion: v.RubricVersion,
		EvidenceCount: len(v.Evidence),
		Provider:      v.TemperatureNote.Provider,
		Model:         v.TemperatureNote.Model,
	}
	for _, c := range v.Criteria {
		out.Criteria = append(out.Criteria, judgeCriterionOutput{
			Name: c.Name, Weight: c.Weight, Score: c.Score, Note: c.Note,
		})
	}
	for _, h := range v.EdgeCaseHits {
		out.EdgeCaseHitIDs = append(out.EdgeCaseHitIDs, h.ID)
	}
	return out
}

// ---------- judge_consensus (N-shot modal) ----------

type judgeConsensusInput struct {
	EvalType        string `json:"eval_type" jsonschema:"required"`
	SpecIntent      string `json:"spec_intent" jsonschema:"required"`
	VibeCase        string `json:"vibe_case" jsonschema:"required"`
	ArtifactType    string `json:"artifact_type,omitempty"`
	ArtifactText    string `json:"artifact_text,omitempty"`
	ArtifactPath    string `json:"artifact_path,omitempty"`
	PersonaID       string `json:"persona_id,omitempty"`
	SchemaVersion   string `json:"schema_version,omitempty"`
	N               int    `json:"n,omitempty" jsonschema_description:"Sample count (1..7, default 3)"`
	Concurrency     int    `json:"concurrency,omitempty" jsonschema_description:"Max in-flight samples (0=unbounded, default=N)"`
	MaxArtifactBytes int   `json:"max_artifact_bytes,omitempty"`
}

type judgeConsensusOutput struct {
	Verdict           string  `json:"verdict"`
	ModalVerdict      string  `json:"modal_verdict"`
	ModalCount        int     `json:"modal_count"`
	ModalFraction     float64 `json:"modal_fraction"`
	AvgConfidence     float64 `json:"avg_confidence"`
	StdDevConfidence  float64 `json:"stddev_confidence"`
	ConfidenceLow     float64 `json:"confidence_low"`
	ConfidenceHigh    float64 `json:"confidence_high"`
	RequestedN        int     `json:"requested_n"`
	NextAction        string  `json:"next_action"`
	Degraded          bool    `json:"degraded"`
	FailedSampleCount int     `json:"failed_sample_count"`
	Reasoning         string  `json:"reasoning,omitempty"`
	VerdictDistribution []struct {
		Verdict string `json:"verdict"`
		Count   int    `json:"count"`
	} `json:"verdict_distribution,omitempty"`
}

func registerJudgeConsensus(s *Server) {
	tool := mcp.NewTool(judgeConsensusToolName,
		mcp.WithDescription("N-shot consensus judge (ADR-007 §9 commit 2). Runs Pipeline.Evaluate N times in parallel and returns the modal verdict + confidence interval."),
		mcp.WithInputSchema[judgeConsensusInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in judgeConsensusInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// Reuse judgeInput validation for the common fields.
		synthetic := judgeInput{
			EvalType: in.EvalType, SpecIntent: in.SpecIntent, VibeCase: in.VibeCase,
			ArtifactType: in.ArtifactType, ArtifactText: in.ArtifactText,
			ArtifactPath: in.ArtifactPath, PersonaID: in.PersonaID,
			SchemaVersion: in.SchemaVersion, MaxArtifactBytes: in.MaxArtifactBytes,
		}
		if err := validateJudgeInput(&synthetic); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		consensusReq := judge.ConsensusRequest{
			EvaluateRequest: buildEvaluateRequest(&synthetic),
			N:               in.N,
		}
		res, err := judge.ConsensusWithSemaphore(ctx, s.judgePipeline, consensusReq, in.Concurrency)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("judge_consensus: %v", err)), nil
		}
		out := judgeConsensusOutput{
			Verdict:           res.Verdict,
			ModalVerdict:      res.ModalVerdict,
			ModalCount:        res.ModalCount,
			ModalFraction:     res.ModalFraction,
			AvgConfidence:     res.AvgConfidence,
			StdDevConfidence:  res.StdDevConfidence,
			ConfidenceLow:     res.ConfidenceLow,
			ConfidenceHigh:    res.ConfidenceHigh,
			RequestedN:        res.RequestedN,
			NextAction:        res.NextAction,
			Degraded:          res.Degraded,
			FailedSampleCount: len(res.FailedSampleIndices),
			Reasoning:         res.Reasoning,
		}
		// Project the verdict distribution.
		dist := res.VerdictDistribution()
		for _, d := range dist {
			out.VerdictDistribution = append(out.VerdictDistribution, struct {
				Verdict string `json:"verdict"`
				Count   int    `json:"count"`
			}{Verdict: d.Verdict, Count: d.Count})
		}
		return resultJSON(out)
	})
}

// ---------- judgment_history (stub for commit 3) ----------

type judgmentHistoryInput struct {
	EvalType string `json:"eval_type,omitempty" jsonschema_description:"Filter by eval_type (empty = all)"`
	Limit    int    `json:"limit,omitempty" jsonschema_description:"Max rows (default 50)"`
}

type judgmentHistoryOutput struct {
	Note  string `json:"note"`
	Count int    `json:"count"`
	Rows  []any  `json:"rows"`
}

// in-memory judgment history (commit 2 stub).
// Commit 3 will replace this with a SQLite-backed ssd_evaluations
// read; the wire shape is stable so callers don't have to change.
type judgmentHistoryStore struct {
	maxRows int
}

func (j *judgmentHistoryStore) record(v *judge.Verdict) {
	// Reserved for commit 3.
	_ = v
}

func registerJudgeJudgmentHistory(s *Server) {
	tool := mcp.NewTool(judgeJudgmentHistoryToolName,
		mcp.WithDescription("List recent judge verdicts. Read-only. Commit 3 will wire this to ssd_evaluations; commit 2 returns an empty list (in-memory history is not persisted)."),
		mcp.WithInputSchema[judgmentHistoryInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in judgmentHistoryInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Limit <= 0 {
			in.Limit = 50
		}
		// Commit 2: empty list. Commit 3 will query ssd_evaluations.
		out := judgmentHistoryOutput{
			Note:  "judgment_history lands in commit 3 (audit emission + ssd_evaluations persistence); commit 2 returns an empty list. Use dark_memory_judge + dark_memory_consensus for in-flight verdicts.",
			Count: 0,
			Rows:  []any{},
		}
		return resultJSON(out)
	})
}

// ---------- judge_list_personas ----------

type judgeListPersonasInput struct {
	IncludeContent bool `json:"include_content,omitempty" jsonschema_description:"When true, returns the rich PersonaContent for v4-new personas (PromptTemplate + EvaluationLens + BiasControls + RequiredEvidence). Default false."`
}

type judgePersonaOutput struct {
	ID              string   `json:"id"`
	DisplayName     string   `json:"display_name"`
	ProviderHint    string   `json:"provider_hint,omitempty"`
	Description     string   `json:"description,omitempty"`
	HasRichContent  bool     `json:"has_rich_content"`
	BiasControls    []string `json:"bias_controls,omitempty"`
	EvaluationLens  string   `json:"evaluation_lens,omitempty"`
	RequiredEvidence []string `json:"required_evidence,omitempty"`
}

type judgeListPersonasOutput struct {
	Count   int                   `json:"count"`
	Personas []judgePersonaOutput `json:"personas"`
}

func registerJudgeListPersonas(s *Server) {
	tool := mcp.NewTool(judgeListPersonasToolName,
		mcp.WithDescription("List registered judge personas (8 legacy + 3 v4-new). Optionally include rich content (PromptTemplate + EvaluationLens + BiasControls)."),
		mcp.WithInputSchema[judgeListPersonasInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in judgeListPersonasInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ids := s.judgePersonas.List()
		out := judgeListPersonasOutput{Count: len(ids)}
		for _, id := range ids {
			p, err := s.judgePersonas.Get(id)
			if err != nil {
				continue // defensive: registry mid-mutation
			}
			po := judgePersonaOutput{
				ID:             p.ID,
				DisplayName:    p.DisplayName,
				ProviderHint:   p.ProviderHint,
				Description:    p.Description,
				HasRichContent: judge.LookupPersonaContent(p.ID) != nil,
			}
			if in.IncludeContent {
				if c := judge.LookupPersonaContent(p.ID); c != nil {
					po.BiasControls = c.BiasControls
					po.EvaluationLens = c.EvaluationLens
					po.RequiredEvidence = c.RequiredEvidence
				}
			}
			out.Personas = append(out.Personas, po)
		}
		return resultJSON(out)
	})
}
