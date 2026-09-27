// Package judge — Pipeline orchestrator (ADR-007 §2).
//
// Pipeline.Evaluate runs the 6-step pipeline that turns an artifact
// reference + spec_intent into a Verdict. Step [7] (audit emission)
// is commit 3's job; Pipeline returns the Verdict and the caller
// (transport layer) is responsible for persisting it.
//
// 7 steps (commit 1 implements [1]-[6]):
//
//	[1] input validation    — req shape + artifact load
//	[2] extractor           — bytes -> []Evidence (deterministic)
//	[3] pre-flight ECs      — 15 ECs, short-circuit on error/fatal
//	[4] persona + rubric    — resolve default or explicit
//	[5] LLM verdict call    — ONLY LLM call in the pipeline
//	[6] parse + verify      — score aggregation + verifier override
//	[7] audit emitter       — commit 3 (write_audit + ssd_evaluations)
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// PipelineConfig is what New consumes. All fields except LLMClient
// have sensible defaults; New fills them in if zero-valued.
type PipelineConfig struct {
	// LLMClient is the only field that has no default. When nil,
	// step [5] short-circuits via EC-002 with verdict=errored.
	// Commit 2 wires the real HTTP client; tests use a Fake.
	LLMClient LLMClient

	// Rubrics is the vibe_case -> Rubric registry. Defaults to the
	// 7 canonical C1-C7 rubrics (NewRubricRegistry()).
	Rubrics RubricRegistry

	// Personas is the persona_id -> Persona registry. Defaults to
	// the 11 canonical personas (NewPersonaRegistry()).
	Personas PersonaRegistry

	// SchemaVersion is the project's schema version, e.g.
	// "v4alpha/2026-09-27/001". Drives EC-013 (prior-version
	// contamination). Default: "v4alpha/2026-09-27/001".
	SchemaVersion string

	// MaxArtifactBytes is the cap for step [1] input validation
	// (EC-004 fires when exceeded). Default 256 KiB.
	MaxArtifactBytes int

	// Extractor is the step [2] implementation. Defaults to
	// NewDefaultExtractor() (chunked paragraph splitter).
	Extractor *Extractor

	// EdgeCaseRunner is the step [3]/[6] implementation. Defaults to
	// NewDefaultEdgeCaseRunner() (15 canonical ECs).
	EdgeCaseRunner *EdgeCaseRunner

	// Verifier is the step [6] independent post-check. Defaults to
	// NewVerifier().
	Verifier *Verifier
}

// Pipeline is the orchestrator. Built by New; thread-safe.
type Pipeline struct {
	cfg      PipelineConfig
	extractor *Extractor
	ecRunner *EdgeCaseRunner
	verifier *Verifier
	rubrics  RubricRegistry
	personas PersonaRegistry
}

// New constructs a Pipeline with defaults filled in. Returns an
// error when the registries can't be initialized (none in commit 1).
func New(cfg PipelineConfig) (*Pipeline, error) {
	if cfg.Rubrics == nil {
		cfg.Rubrics = NewRubricRegistry()
	}
	if cfg.Personas == nil {
		cfg.Personas = NewPersonaRegistry()
	}
	if cfg.SchemaVersion == "" {
		cfg.SchemaVersion = "v4alpha/2026-09-27/001"
	}
	if cfg.MaxArtifactBytes == 0 {
		cfg.MaxArtifactBytes = 256 * 1024
	}
	if cfg.Extractor == nil {
		cfg.Extractor = NewDefaultExtractor()
	}
	if cfg.EdgeCaseRunner == nil {
		cfg.EdgeCaseRunner = NewDefaultEdgeCaseRunner()
	}
	if cfg.Verifier == nil {
		cfg.Verifier = NewVerifier()
	}
	return &Pipeline{
		cfg:      cfg,
		extractor: cfg.Extractor,
		ecRunner:  cfg.EdgeCaseRunner,
		verifier:  cfg.Verifier,
		rubrics:   cfg.Rubrics,
		personas:  cfg.Personas,
	}, nil
}

// Evaluate runs the 6-step pipeline. Returns a *Verdict that
// always passes Validate() (or an infra error from ctx cancellation
// or artifact loading).
//
// Backwards compat: when LLMClient is nil OR the LLM call fails,
// the verdict is VerdictErrored with EdgeCaseHits including EC-002.
// When the artifact is empty, EC-001 short-circuits with
// VerdictNeedsHuman. The Pipeline never returns nil Verdict (it
// returns VerdictErrored for any infra failure so the caller can
// distinguish "the judge didn't run" from "the judge ran and said
// aligned").
func (p *Pipeline) Evaluate(ctx context.Context, req EvaluateRequest) (*Verdict, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// ---------- Step [1]: input validation ----------

	if req.VibeCase == "" {
		return erroredVerdict("vibe_case required"), nil
	}
	if req.EvalType == "" {
		return erroredVerdict("eval_type required"), nil
	}
	if req.SchemaVersion == "" {
		req.SchemaVersion = p.cfg.SchemaVersion
	}

	content, err := p.resolveArtifact(ctx, req)
	if err != nil {
		return erroredVerdict(fmt.Sprintf("artifact resolve failed: %v", err)), nil
	}

	pc := &PipelineContext{
		ArtifactContent: content,
		ArtifactRef:     req.Ref,
		EvalType:        req.EvalType,
		SpecIntent:      req.SpecIntent,
		VibeCase:        req.VibeCase,
		PersonaID:       req.PersonaID,
		SchemaVersion:   req.SchemaVersion,
		LLMClient:       p.cfg.LLMClient,
		Personas:        p.personas,
		Rubrics:         p.rubrics,
	}

	// ---------- Step [2]: extract evidence ----------

	evidence, err := p.extractor.Extract(ctx, pc)
	if err != nil {
		return erroredVerdict(fmt.Sprintf("extractor failed: %v", err)), nil
	}
	pc.Evidence = evidence

	// ---------- Step [3]: pre-flight ECs ----------

	preHits := p.ecRunner.RunPreFlight(ctx, pc)
	if v, ok := ShortCircuitVerdict(preHits); ok {
		return p.buildShortCircuitVerdict(v, preHits, req), nil
	}

	// ---------- Step [4]: resolve persona + rubric ----------

	rub, err := p.rubrics.Get(req.VibeCase)
	if err != nil {
		return erroredVerdict(fmt.Sprintf("rubric resolve failed: %v", err)), nil
	}
	personaID := req.PersonaID
	if personaID == "" {
		personaID = rub.PersonaID
	}
	pc.PersonaID = personaID

	// ---------- Step [5]: LLM verdict call ----------

	if p.cfg.LLMClient == nil {
		// EC-002 fires (fatal: errored).
		hits := append(preHits, EdgeCaseHit{
			ID: "EC-002", Severity: "fatal",
			Trigger: "no LLMClient configured",
			Catches: "infra",
		})
		return p.buildShortCircuitVerdict(VerdictErrored, hits, req), nil
	}

	llmReq := p.buildLLMRequest(pc, rub)
	llmResp, err := p.cfg.LLMClient.Complete(ctx, llmReq)
	if err != nil {
		hits := append(preHits, EdgeCaseHit{
			ID: "EC-002", Severity: "fatal",
			Trigger: fmt.Sprintf("LLM error: %v", err),
			Catches: "infra",
		})
		return p.buildShortCircuitVerdict(VerdictErrored, hits, req), nil
	}

	// ---------- Step [6]: parse + verifier ----------

	parsed, parseErr := p.parseVerdictResponse(llmResp)
	if parseErr != nil {
		return erroredVerdict(fmt.Sprintf("verdict response parse failed: %v", parseErr)), nil
	}

	// Attach Weight to each parsed Criterion by looking up the
	// rubric's criteria (the LLM doesn't know weights).
	rubDefs := make(map[string]float64)
	for _, c := range rub.Criteria {
		rubDefs[c.Name] = c.Weight
	}
	for i := range parsed.criteria {
		if w, ok := rubDefs[parsed.criteria[i].Name]; ok {
			parsed.criteria[i].Weight = w
		}
	}

	// Attach criteria + Evidence before verifier runs (verifier
	// inspects both).
	proposed := &Verdict{
		Verdict:    parsed.verdictLabel,
		Confidence: 0.0,
		Reasoning:  parsed.reasoning,
		PersonaID:  personaID,
		RubricVersion: rub.Version,
		Criteria:   parsed.criteria,
		Evidence:   pc.Evidence,
		EdgeCaseHits: preHits,
	}

	// Compute weighted score + base verdict FIRST so EC-015 sees
	// the proposed verdict label (it checks pc.Verdict.Verdict ==
	// VerdictAligned).
	score := ComputeWeightedScore(parsed.criteria)
	baseVerdict := ThresholdToVerdict(score, rub)
	if proposed.Verdict == "" {
		proposed.Verdict = baseVerdict
	}
	proposed.Confidence = score
	pc.Verdict = proposed

	// EC-015 (post-LLM). Runs NOW that pc.Verdict has a label.
	postHits := p.ecRunner.RunPostLLM(ctx, pc)
	proposed.EdgeCaseHits = append(append([]EdgeCaseHit{}, preHits...), postHits...)

	// If EC-015 short-circuited, override before verifier (the
	// verifier's F7 logic is subsumed by EC-015's override).
	if v, ok := ShortCircuitVerdict(postHits); ok {
		return p.buildShortCircuitVerdict(v, proposed.EdgeCaseHits, req), nil
	}

	// Per-criterion override (ADR-007 §4).
	overrideVerdict, overrideBy := ApplyPerCriterionOverride(parsed.criteria, proposed.Verdict)
	if overrideBy != "" {
		proposed.Verdict = overrideVerdict
		proposed.Reasoning = fmt.Sprintf("%s; per-criterion override (%s)", proposed.Reasoning, overrideBy)
		proposed.EdgeCaseHits = append(proposed.EdgeCaseHits, EdgeCaseHit{
			ID: "PER-CRITERION-OVERRIDE", Severity: "error",
			Trigger: overrideBy, Catches: "F7",
		})
	}

	// Build bias audit from EC hits (EC-007/008/012/013/014 set flags).
	proposed.BiasAudit = p.buildBiasAudit(postHits)

	// Apply verifier (independent post-check). May override Verdict.
	final := p.verifier.Verify(ctx, pc, proposed)
	final.TemperatureNote = p.buildTemperatureNote(llmResp, personaID, rub, req)
	final.EvaluatedAt = time.Now()

	if err := final.Validate(); err != nil {
		return erroredVerdict(fmt.Sprintf("validate failed: %v", err)), nil
	}
	return final, nil
}

// ---------- Step [1] helper: artifact resolution ----------

// resolveArtifact loads artifact content from the request. Three cases:
//
//   1. req.ArtifactContent non-empty: use it directly (zero-copy).
//   2. req.Ref.Kind == "file": read from req.Ref.Path (with
//      MaxArtifactBytes cap).
//   3. req.Ref.Kind == "" or req.Ref == nil: return empty content.
//   4. Other kinds (url, git_sha, spec_id, artifact_id): not supported
//      in commit 1; return error so the operator knows to upgrade.
func (p *Pipeline) resolveArtifact(ctx context.Context, req EvaluateRequest) ([]byte, error) {
	if len(req.ArtifactContent) > 0 {
		return req.ArtifactContent, nil
	}
	if req.Ref == nil || req.Ref.Kind == "" {
		return nil, nil
	}
	switch req.Ref.Kind {
	case "file":
		data, err := os.ReadFile(req.Ref.Path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", req.Ref.Path, err)
		}
		if len(data) > p.cfg.MaxArtifactBytes {
			return nil, fmt.Errorf("%w: %d > %d", ErrArtifactTooLarge, len(data), p.cfg.MaxArtifactBytes)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("ref.Kind=%q not supported in commit 1 (caller must pre-load ArtifactContent)", req.Ref.Kind)
	}
}

// ---------- Step [5] helper: LLM request builder ----------

// buildLLMRequest renders the prompt template (commit 1: literal
// string, no template engine) with the persona + rubric + evidence
// attached as structured inputs. Anti-prompt-injection layer L1:
// artifact bytes are chunkified into evidence[] and passed as a
// structured array, NEVER concatenated to the prompt string.
func (p *Pipeline) buildLLMRequest(pc *PipelineContext, rub *Rubric) LLMRequest {
	systemPrompt := fmt.Sprintf(
		"You are a %s persona. Rubric version: %s. Schema version: %s. "+
			"Respond ONLY with valid JSON matching the response_format schema. "+
			"For each criterion, output name+score+note.",
		pc.PersonaID, rub.Version, pc.SchemaVersion,
	)
	userPrompt := fmt.Sprintf(
		"Vibe case: %s. Spec intent: %s. "+
			"Evaluate the artifact whose evidence is provided in structured_inputs. "+
			"Per criterion, score [0,1] and write a 1-2 sentence note.",
		pc.VibeCase, pc.SpecIntent,
	)
	return LLMRequest{
		SystemPrompt:     systemPrompt,
		UserPrompt:       userPrompt,
		StructuredInputs: pc.Evidence,
		Temperature:      0.0,
		MaxTokens:        2048,
		TopP:             1.0,
		Seed:             0,
		ResponseFormat: ResponseFormat{
			Type:   "json_schema",
			Schema: `{"type":"object","required":["reasoning","criteria"]}`,
		},
	}
}

// ---------- Step [6] helper: parse LLM response ----------

// parsedVerdict is the decoded LLM response.
type parsedVerdict struct {
	verdictLabel string
	reasoning    string
	criteria     []Criterion
}

// parseVerdictResponse decodes the LLM's JSON output. The schema
// (commit 1: minimal) requires { reasoning: string, criteria:
// [{name, score, note}] } and accepts an optional verdict_label.
func (p *Pipeline) parseVerdictResponse(resp *LLMResponse) (*parsedVerdict, error) {
	if resp == nil {
		return nil, fmt.Errorf("nil LLM response")
	}
	if strings.TrimSpace(resp.Content) == "" {
		return nil, fmt.Errorf("empty LLM response content")
	}
	var raw struct {
		VerdictLabel string `json:"verdict_label"`
		Reasoning    string `json:"reasoning"`
		Criteria     []struct {
			Name  string  `json:"name"`
			Score float64 `json:"score"`
			Note  string  `json:"note"`
		} `json:"criteria"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &raw); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}
	out := &parsedVerdict{
		verdictLabel: raw.VerdictLabel,
		reasoning:    raw.Reasoning,
	}
	for _, c := range raw.Criteria {
		out.criteria = append(out.criteria, Criterion{
			Name:   c.Name,
			Score:  c.Score,
			Note:   c.Note,
			Weight: 0.0, // set by Pipeline from rubric after parsing
		})
	}
	return out, nil
}

// ---------- Step [6] helper: bias audit ----------

// buildBiasAudit converts post-LLM EC hits into BiasAudit flags.
// EC-007 -> SelfReference, EC-008 -> PositionBiasPotential,
// EC-012 -> ImplicitAssumption, EC-013 -> PriorVersionContamination,
// EC-014 -> EvidenceMissing.
func (p *Pipeline) buildBiasAudit(postHits []EdgeCaseHit) BiasAudit {
	var ba BiasAudit
	for _, h := range postHits {
		switch h.ID {
		case "EC-007":
			ba.SelfReference = true
		case "EC-008":
			ba.PositionBiasPotential = true
		case "EC-012":
			ba.ImplicitAssumption = true
		case "EC-013":
			ba.PriorVersionContamination = true
		case "EC-014":
			ba.EvidenceMissing = true
		}
	}
	return ba
}

// ---------- Step [6] helper: temperature note ----------

// buildTemperatureNote composes the auditable (provider, model, T,
// ...) record. Commit 1: provider/model from LLMResponse; the rest
// from the request defaults. Commit 2 reads from env vars.
func (p *Pipeline) buildTemperatureNote(resp *LLMResponse, personaID string, rub *Rubric, req EvaluateRequest) TemperatureNote {
	provider, model := "", ""
	if resp != nil {
		provider = resp.Provider
		model = resp.Model
	}
	return TemperatureNote{
		Provider:      provider,
		Model:         model,
		Temperature:   0.0,
		Seed:          0,
		MaxTokens:     2048,
		TimeoutMs:     15000,
		TopP:          1.0,
		PersonaID:     personaID,
		RubricVersion: rub.Version,
		SchemaVersion: req.SchemaVersion,
	}
}

// ---------- Helpers: short-circuit + errored verdicts ----------

// buildShortCircuitVerdict produces a verdict when an EC short-
// circuit fired. Includes the EdgeCaseHits, an explanation, and
// empty Criteria (no LLM call was made).
func (p *Pipeline) buildShortCircuitVerdict(label string, hits []EdgeCaseHit, req EvaluateRequest) *Verdict {
	v := &Verdict{
		Verdict:       label,
		Confidence:    0.0,
		Reasoning:     fmt.Sprintf("pipeline short-circuit: %s", joinHitIDs(hits)),
		PersonaID:     req.PersonaID,
		EdgeCaseHits:  hits,
		EvaluatedAt:   time.Now(),
		TemperatureNote: TemperatureNote{
			SchemaVersion: req.SchemaVersion,
		},
	}
	if v.PersonaID == "" {
		v.PersonaID = "EC-SHORT-CIRCUIT"
	}
	// Validate before returning so callers can rely on invariants.
	if err := v.Validate(); err != nil {
		return erroredVerdict(fmt.Sprintf("short-circuit validate failed: %v", err))
	}
	return v
}

// erroredVerdict returns the canonical "the pipeline failed to run"
// verdict. Always VerdictErrored + Reasoning describing the cause.
func erroredVerdict(reason string) *Verdict {
	v := &Verdict{
		Verdict:     VerdictErrored,
		Confidence:  0.0,
		Reasoning:   "pipeline infra error: " + reason,
		PersonaID:   "PIPELINE-INFRA",
		EvaluatedAt: time.Now(),
	}
	_ = v.Validate()
	return v
}

// joinHitIDs joins hit IDs for short-circuit reasoning. Deterministic
// order (input order is already sorted by EdgeCaseRunner).
func joinHitIDs(hits []EdgeCaseHit) string {
	if len(hits) == 0 {
		return "no hits"
	}
	parts := make([]string, len(hits))
	for i, h := range hits {
		parts[i] = h.ID
	}
	return strings.Join(parts, ",")
}
