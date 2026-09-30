// judge_* tools — the v4-alpha LLM-backed judge surface (ADR-007 C2 + C3).
//
//   - dark_memory_judge              → judge.Pipeline.Evaluate + judge.Store.SaveEvaluation
//   - dark_memory_consensus          → judge.ConsensusWithSemaphore + judge.Store.SaveConsensusSamples
//   - dark_memory_judgment_history   → judge.Store.ListEvaluations (real persistence, no longer stub)
//   - dark_memory_judge_list_personas → list the 11 registered personas
//
// All 4 tools share the v4alpha/judge.Pipeline + judge.Store wired
// at server boot (see server.go). The Pipeline owns the LLMClient
// + PersonaRegistry + RubricRegistry; the Store owns the
// sdd_evaluations table. The tools are thin closures over both.
//
// ADR-007 C3 audit emission: every dark_memory_judge and
// dark_memory_consensus call persists the verdict(s) to
// sdd_evaluations + emits one audit_log row per evaluation, all
// inside a single transaction (atomic). Operator id comes from
// the tool input (the same field that drives audit_log.actor).
package mcp

import (
	"context"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

const (
	judgeToolName                = "dark_memory_judge"
	judgeConsensusToolName       = "dark_memory_consensus"
	judgeJudgmentHistoryToolName = "dark_memory_judgment_history"
	judgeListPersonasToolName    = "dark_memory_judge_list_personas"
)

func registerJudgeTools(s *Server) {
	registerJudge(s)
	registerJudgeConsensus(s)
	registerJudgeJudgmentHistory(s)
	registerJudgeListPersonas(s)
}

// ---------- judge (one-shot) ----------

type judgeInput struct {
	EvalType         string `json:"eval_type" jsonschema:"required" jsonschema_description:"drift_judge | brand_match | compliance_check | grounding_check"`
	SpecIntent       string `json:"spec_intent" jsonschema:"required" jsonschema_description:"One-paragraph 'what should this artifact be' hypothesis"`
	VibeCase         string `json:"vibe_case" jsonschema:"required" jsonschema_description:"C1..C7 (code, text, decision, research, video, audio, multi)"`
	ArtifactType     string `json:"artifact_type,omitempty" jsonschema_description:"text | file"`
	ArtifactText     string `json:"artifact_text,omitempty" jsonschema_description:"Inline text when artifact_type=text"`
	ArtifactPath     string `json:"artifact_path,omitempty" jsonschema_description:"File path when artifact_type=file"`
	PersonaID        string `json:"persona_id,omitempty" jsonschema_description:"Optional explicit persona id"`
	SchemaVersion    string `json:"schema_version,omitempty" jsonschema_description:"Defaults to project schema version"`
	MaxArtifactBytes int    `json:"max_artifact_bytes,omitempty" jsonschema_description:"Cap for EC-004 (default 256 KiB)"`

	// ADR-007 C3 audit metadata. Operator is required (INV-1);
	// SessionID + ProjectID are optional.
	Operator  string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; reused as audit_log.actor)"`
	SessionID string `json:"session_id,omitempty" jsonschema_description:"Optional active session id for audit_log.session_id + sdd_evaluations.session_id"`
	ProjectID string `json:"project_id,omitempty" jsonschema_description:"Optional project id (INV-7 reserved)"`
}

type judgeOutput struct {
	Verdict        string                 `json:"verdict"`
	Confidence     float64                `json:"confidence"`
	Reasoning      string                 `json:"reasoning,omitempty"`
	PersonaID      string                 `json:"persona_id,omitempty"`
	RubricVersion  string                 `json:"rubric_version,omitempty"`
	Criteria       []judgeCriterionOutput `json:"criteria,omitempty"`
	EvidenceCount  int                    `json:"evidence_count"`
	EdgeCaseHitIDs []string               `json:"edge_case_hit_ids,omitempty"`
	Provider       string                 `json:"provider,omitempty"`
	Model          string                 `json:"model,omitempty"`
	EvaluationID   int64                  `json:"evaluation_id,omitempty"`
}

type judgeCriterionOutput struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Score  float64 `json:"score"`
	Note   string  `json:"note,omitempty"`
}

func registerJudge(s *Server) {
	tool := mcp.NewTool(judgeToolName,
		mcp.WithDescription("LLM-backed one-shot judge evaluation. Runs the 6-step pipeline (ADR-007 §2), persists the verdict to sdd_evaluations + audit_log (INV-1), and returns the verdict."),
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

		// C3: persist the verdict to sdd_evaluations + audit_log.
		// Failure to persist is logged into the result's evaluation_id
		// field (id=0 means not persisted) but does NOT fail the
		// verdict — the operator already has the verdict in hand;
		// the persistence is a best-effort audit trail.
		var evalID int64
		if e, err := judge.EvaluationFromVerdict(in.EvalType, in.ArtifactType, targetID(&in), v); err == nil {
			e.SessionID = in.SessionID
			auditMeta := &judge.Audit{
				Actor:     in.Operator,
				SessionID: in.SessionID,
				ProjectID: in.ProjectID,
			}
			if id, err := s.judgeStore.SaveEvaluation(ctx, auditMeta, e); err == nil {
				evalID = id
				// ADR-011: best-effort bootstrap-CI calibration.
				// Pulls historical confidences for this
				// (provider, target_type, eval_type) tuple and,
				// when ShouldRecalibrate(N) holds, computes the CI
				// and stamps the row. Failure is non-fatal.
				s.populateCalibration(ctx, id, e, v)
			}
		}

		out := verdictToOutput(v)
		out.EvaluationID = evalID
		return resultJSON(out)
	})
}

// populateCalibration (ADR-011) computes the bootstrap-CI for the
// just-saved evaluation and stamps the row. Best-effort — failure
// is logged at debug level (no audit row; calibration is metadata,
// not state).
//
// Triggers only when ShouldRecalibrate(N) holds for the
// (provider, target_type, eval_type) tuple — i.e., at least 50
// historical confidences. Below that threshold we leave the
// calibration columns NULL (EC-007a handles the cold-start case).
func (s *Server) populateCalibration(ctx context.Context, evalID int64, e *judge.Evaluation, v *judge.Verdict) {
	provider := ""
	if v != nil {
		provider = v.TemperatureNote.Provider
	}
	confidences, err := s.judgeStore.ConfidencesByProviderTarget(
		ctx, provider, e.TargetType, e.EvalType, 1000,
	)
	if err != nil || !judge.ShouldRecalibrate(len(confidences)) {
		return
	}
	ci := judge.BootstrapCI(confidences, 0.95, 1000)
	if err := s.judgeStore.SetCalibration(ctx, evalID, ci, "play_favorites_v1"); err != nil {
		// Best-effort: log to stderr, do not fail the verdict.
		fmt.Fprintf(os.Stderr, "dark-memory-v4: populateCalibration id=%d: %v\n", evalID, err)
	}
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
	if in.Operator == "" {
		return fmt.Errorf("judge: operator required (INV-1)")
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
			Kind:     "file",
			Path:     in.ArtifactPath,
			MaxBytes: in.MaxArtifactBytes,
		}
	}
	return pReq
}

// targetID derives a stable identifier for the artifact so the
// sdd_evaluations row can be queried later. For text artifacts we
// use a content-hash prefix; for file artifacts we use the path.
func targetID(in *judgeInput) string {
	if in.ArtifactType == "file" {
		return in.ArtifactPath
	}
	// For text: first 32 chars of the content + length. Stable
	// across calls, short enough for a unique-ish key.
	body := in.ArtifactText
	if len(body) > 32 {
		body = body[:32]
	}
	return fmt.Sprintf("text:%s:%d", body, len(in.ArtifactText))
}

// verdictToOutput projects a *judge.Verdict into the wire output
// shape. Excludes Criteria + Evidence by default (those are
// diagnostic fields; callers who need them can read the audit
// row).
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
	EvalType         string `json:"eval_type" jsonschema:"required"`
	SpecIntent       string `json:"spec_intent" jsonschema:"required"`
	VibeCase         string `json:"vibe_case" jsonschema:"required"`
	ArtifactType     string `json:"artifact_type,omitempty"`
	ArtifactText     string `json:"artifact_text,omitempty"`
	ArtifactPath     string `json:"artifact_path,omitempty"`
	PersonaID        string `json:"persona_id,omitempty"`
	SchemaVersion    string `json:"schema_version,omitempty"`
	N                int    `json:"n,omitempty" jsonschema_description:"Sample count (1..7, default 3)"`
	Concurrency      int    `json:"concurrency,omitempty" jsonschema_description:"Max in-flight samples (0=unbounded, default=N)"`
	MaxArtifactBytes int    `json:"max_artifact_bytes,omitempty"`

	// C3 audit metadata.
	Operator  string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
	SessionID string `json:"session_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

type judgeConsensusOutput struct {
	Verdict             string  `json:"verdict"`
	ModalVerdict        string  `json:"modal_verdict"`
	ModalCount          int     `json:"modal_count"`
	ModalFraction       float64 `json:"modal_fraction"`
	AvgConfidence       float64 `json:"avg_confidence"`
	StdDevConfidence    float64 `json:"stddev_confidence"`
	ConfidenceLow       float64 `json:"confidence_low"`
	ConfidenceHigh      float64 `json:"confidence_high"`
	RequestedN          int     `json:"requested_n"`
	NextAction          string  `json:"next_action"`
	Degraded            bool    `json:"degraded"`
	FailedSampleCount   int     `json:"failed_sample_count"`
	Reasoning           string  `json:"reasoning,omitempty"`
	ModalEvaluationID   int64   `json:"modal_evaluation_id,omitempty"`
	VerdictDistribution []struct {
		Verdict string `json:"verdict"`
		Count   int    `json:"count"`
	} `json:"verdict_distribution,omitempty"`
}

func registerJudgeConsensus(s *Server) {
	tool := mcp.NewTool(judgeConsensusToolName,
		mcp.WithDescription("N-shot consensus judge (ADR-007 §9 commit 2 + C3 persistence). Runs Pipeline.Evaluate N times in parallel, persists each sample + the modal result to sdd_evaluations, returns the modal verdict + confidence interval."),
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
			Operator: in.Operator,
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

		// C3: persist N samples + 1 modal row. We build Evaluation
		// structs from each surviving sample. The modal Evaluation
		// is built from a synthetic Verdict that captures the
		// aggregated confidence + reasoning.
		var modalID int64
		if len(res.Samples) > 0 {
			auditMeta := &judge.Audit{
				Actor:     in.Operator,
				SessionID: in.SessionID,
				ProjectID: in.ProjectID,
			}
			samples := make([]*judge.Evaluation, 0, len(res.Samples))
			for i := range res.Samples {
				if res.Samples[i].Error != nil || res.Samples[i].Verdict == nil {
					continue
				}
				v := res.Samples[i].Verdict
				e, err := judge.EvaluationFromVerdict(in.EvalType, in.ArtifactType, targetID(&synthetic), v)
				if err != nil {
					continue
				}
				e.SessionID = in.SessionID
				samples = append(samples, e)
			}

			// Modal Evaluation: synthesised from res.Verdict +
			// res.Reasoning + res.AvgConfidence + the persona/rubric
			// from the first surviving sample.
			modal := buildModalEvaluation(in, res, targetID(&synthetic))

			if id, err := s.judgeStore.SaveConsensusSamples(ctx, auditMeta, samples, modal); err == nil {
				modalID = id
			}
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
			ModalEvaluationID: modalID,
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

// buildModalEvaluation constructs the modal-row Evaluation. The
// persona/rubric/schema_version are inherited from the first
// surviving sample; the temperature note uses average values.
func buildModalEvaluation(in judgeConsensusInput, res *judge.ConsensusResult, targetID string) *judge.Evaluation {
	var (
		personaID, rubricVer, schemaVer, provider, model string
	)
	for i := range res.Samples {
		v := res.Samples[i].Verdict
		if v == nil || res.Samples[i].Error != nil {
			continue
		}
		personaID = v.PersonaID
		rubricVer = v.RubricVersion
		schemaVer = v.TemperatureNote.SchemaVersion
		provider = v.TemperatureNote.Provider
		model = v.TemperatureNote.Model
		break
	}
	modalVerdict := &judge.Verdict{
		Verdict:       res.ModalVerdict,
		Confidence:    res.AvgConfidence,
		Reasoning:     res.Reasoning,
		PersonaID:     personaID,
		RubricVersion: rubricVer,
		TemperatureNote: judge.TemperatureNote{
			Provider:      provider,
			Model:         model,
			PersonaID:     personaID,
			RubricVersion: rubricVer,
			SchemaVersion: schemaVer,
		},
	}
	e, _ := judge.EvaluationFromVerdict(in.EvalType, in.ArtifactType, targetID, modalVerdict)
	if e != nil {
		e.SessionID = in.SessionID
		// Modal rows carry the consensus eval_type; SaveConsensusSamples
		// will overwrite it with judge.EvalConsensus to keep the
		// sentinel contract.
		e.EvalType = judge.EvalConsensus
	}
	return e
}

// ---------- judgment_history (real, replaces C2 stub) ----------

type judgmentHistoryInput struct {
	EvalType   string `json:"eval_type,omitempty" jsonschema_description:"Filter by eval_type (empty = all)"`
	TargetType string `json:"target_type,omitempty" jsonschema_description:"Filter by target_type (empty = all)"`
	TargetID   string `json:"target_id,omitempty" jsonschema_description:"Filter by target_id (empty = all)"`
	Limit      int    `json:"limit,omitempty" jsonschema_description:"Max rows (default 50)"`
}

type judgmentHistoryOutput struct {
	EvalTypeFilter string                  `json:"eval_type_filter,omitempty"`
	Count          int                     `json:"count"`
	Rows           []judgmentHistoryRowOut `json:"rows"`
}

type judgmentHistoryRowOut struct {
	ID         int64   `json:"id"`
	EvalType   string  `json:"eval_type"`
	TargetType string  `json:"target_type"`
	TargetID   string  `json:"target_id"`
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Provider   string  `json:"provider,omitempty"`
	Model      string  `json:"model,omitempty"`
	PersonaID  string  `json:"persona_id,omitempty"`
	RubricVer  string  `json:"rubric_version,omitempty"`
	SessionID  string  `json:"session_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

func registerJudgeJudgmentHistory(s *Server) {
	tool := mcp.NewTool(judgeJudgmentHistoryToolName,
		mcp.WithDescription("List recent judge verdicts persisted to sdd_evaluations. Read-only. Supports optional filters by eval_type, target_type, target_id."),
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
		filter := judge.ListFilter{
			EvalType:   in.EvalType,
			TargetType: in.TargetType,
			TargetID:   in.TargetID,
			Limit:      in.Limit,
		}
		rows, err := s.judgeStore.ListEvaluations(ctx, filter)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("judgment_history: %v", err)), nil
		}
		out := judgmentHistoryOutput{
			EvalTypeFilter: in.EvalType,
			Count:          len(rows),
		}
		for _, e := range rows {
			// Reconstruct the inner verdict (VerdictJSON already
			// contains the full v4 Verdict). We only surface the
			// top-level Verdict + Confidence fields.
			verdict := ""
			if v, err := judge.VerdictFromEvaluation(&e); err == nil && v != nil {
				verdict = v.Verdict
			}
			out.Rows = append(out.Rows, judgmentHistoryRowOut{
				ID:         e.ID,
				EvalType:   e.EvalType,
				TargetType: e.TargetType,
				TargetID:   e.TargetID,
				Verdict:    verdict,
				Confidence: e.Confidence,
				Provider:   e.Provider,
				Model:      e.Model,
				PersonaID:  e.PersonaID,
				RubricVer:  e.RubricVer,
				SessionID:  e.SessionID,
				CreatedAt:  e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			})
		}
		if out.Rows == nil {
			out.Rows = []judgmentHistoryRowOut{} // never null in JSON
		}
		return resultJSON(out)
	})
}

// ---------- judge_list_personas ----------

type judgeListPersonasInput struct {
	IncludeContent bool `json:"include_content,omitempty" jsonschema_description:"When true, returns the rich PersonaContent for v4-new personas (PromptTemplate + EvaluationLens + BiasControls + RequiredEvidence). Default false."`
}

type judgePersonaOutput struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name"`
	ProviderHint     string   `json:"provider_hint,omitempty"`
	Description      string   `json:"description,omitempty"`
	HasRichContent   bool     `json:"has_rich_content"`
	BiasControls     []string `json:"bias_controls,omitempty"`
	EvaluationLens   string   `json:"evaluation_lens,omitempty"`
	RequiredEvidence []string `json:"required_evidence,omitempty"`
}

type judgeListPersonasOutput struct {
	Count    int                  `json:"count"`
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
