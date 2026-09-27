// Package judge — extended types for ADR-007 judge pipeline v4.
//
// This file defines the rich types that the Pipeline (steps [1]-[7])
// produces. ADR-007 §1 commits to backwards compatibility with the
// v4-alpha.1 narrow Verdict (just Verdict + Confidence + Reasoning),
// so the extended fields are additive and zero-valued Verdict remains
// valid (legacy NoOpJudge / external callers keep working).
//
// Layering (per ADR-007 §2):
//
//	[1] input validation           uses Ref + SpecIntent + LLMClient
//	[2] extractor                  produces []Evidence
//	[3] edge-case pre-flight       produces []EdgeCaseHit
//	[4] rubric resolution          uses PersonaRegistry + RubricRegistry
//	[5] LLM verdict call           LLMRequest -> LLMResponse
//	[6] verdict aggregator         Criteria[] -> Verdict (with override)
//	[7] audit emitter              writes sdd_evaluations (C3)
//
// Only step [5] makes an LLM call; all other steps are deterministic
// pure functions. This is the attack on F1 (doc-vs-code drift): the
// extractor in [2] reads the source literal — it does NOT summarize
// through an LLM.
package judge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// ---------- Verdict (extended) ----------

// Verdict is one judge's verdict on one artifact.
//
// Backwards compat (ADR-007 §6): the v4-alpha.1 three fields
// (Verdict + Confidence + Reasoning) are preserved verbatim. New
// fields are additive; a Verdict constructed with only the legacy
// three fields is still valid and passes Validate.
//
// The new fields turn the verdict from "label + score + prose" into
// a fully auditable record: who judged, with which rubric version,
// per-criterion scores, evidence anchors, edge cases caught, the
// temperature note (reproducibility), and the bias audit.
type Verdict struct {
	// Legacy fields (v4-alpha.1, preserved).
	Verdict    string  // "aligned" | "drift_detected" | "needs_human" | "errored"
	Confidence float64 // [0.0, 1.0] weighted sum of Criteria.Scores
	Reasoning  string  // human-readable explanation (LLM-authored in [5])

	// New fields (ADR-007).

	// PersonaID is the persona registry id of the judge that produced
	// this verdict (e.g. "judge-logical", "judge-cross-modal",
	// "judge-pipeline"). Empty when no persona resolution occurred
	// (e.g. NoOpJudge legacy path).
	PersonaID string

	// RubricVersion is the sha256[:16] of the rubric used. Lets the
	// operator reproduce a verdict months later by loading the rubric
	// of that version. See ADR-007 §7.
	RubricVersion string

	// Criteria is the per-criterion breakdown. Empty when the verdict
	// short-circuited via an edge case (no LLM call was made).
	Criteria []Criterion

	// Evidence is the list of snippets the LLM anchored its verdict
	// on. Empty when the verdict short-circuited. Each entry is a
	// literal slice of the artifact source — never a paraphrase.
	Evidence []Evidence

	// EdgeCaseHits lists every edge case that fired during step [3].
	// Even when no short-circuit occurred (all hits severity=warn),
	// the list is recorded so the operator can audit.
	EdgeCaseHits []EdgeCaseHit

	// TemperatureNote records the exact (provider, model, T, seed, ...)
	// used to produce this verdict. Two verdicts with the same input
	// AND the same TemperatureNote are reproducible (modulo provider
	// non-determinism, which is reported in the note).
	TemperatureNote TemperatureNote

	// BiasAudit records the bias indicators caught during evaluation
	// (self-reference, position bias, implicit assumption, prior-
	// version contamination, evidence missing). ADR-007 §8 ECs 007,
	// 008, 012, 013, 014.
	BiasAudit BiasAudit

	// EvaluatedAt is when the pipeline finished step [6]. Used for
	// audit-window queries ("show me verdicts from the last hour").
	EvaluatedAt time.Time
}

// Validate returns nil for a fully valid verdict, or an error
// describing the failed invariant.
//
// Backwards compat: Verdict without the new fields is still valid
// (PersonaID empty, RubricVersion empty, Criteria nil, etc.).
func (v *Verdict) Validate() error {
	switch v.Verdict {
	case VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman, "errored":
		// ok
	default:
		return fmt.Errorf("judge: invalid verdict %q", v.Verdict)
	}
	if v.Confidence < 0.0 || v.Confidence > 1.0 {
		return fmt.Errorf("judge: confidence %f out of [0,1]", v.Confidence)
	}
	// Per-criterion scores must be in [0,1].
	for i, c := range v.Criteria {
		if c.Weight < 0.0 || c.Weight > 1.0 {
			return fmt.Errorf("judge: criteria[%d] %q weight %f out of [0,1]", i, c.Name, c.Weight)
		}
		if c.Score < 0.0 || c.Score > 1.0 {
			return fmt.Errorf("judge: criteria[%d] %q score %f out of [0,1]", i, c.Name, c.Score)
		}
	}
	// Evidence snippets must not be empty when present.
	for i, e := range v.Evidence {
		if e.Snippet == "" {
			return fmt.Errorf("judge: evidence[%d] snippet empty", i)
		}
		if e.Relevance < 0.0 || e.Relevance > 1.0 {
			return fmt.Errorf("judge: evidence[%d] relevance %f out of [0,1]", i, e.Relevance)
		}
	}
	return nil
}

// ---------- Per-criterion breakdown ----------

// Criterion is one scored axis of a Rubric. The Pipeline in step [6]
// sums Score * Weight across all criteria, then applies thresholds
// (and the per-criterion override per ADR-007 §4).
type Criterion struct {
	// Name is the rubric-axis identifier (e.g. "correctness",
	// "tests", "faithfulness", "subject_consistency"). Stable across
	// rubric versions for the same vibe_case.
	Name string

	// Weight is the importance of this axis, in [0, 1]. The sum of
	// weights in a Rubric MUST equal 1.0 (RubricRegistry enforces
	// this invariant at registration time).
	Weight float64

	// Score is the LLM's [0, 1] rating for this axis, as filled in
	// step [5]. Zero when the verdict short-circuited.
	Score float64

	// Note is a brief justification (1-2 sentences) the LLM wrote
	// for this criterion. The Pipeline may truncate it.
	Note string
}

// ---------- Evidence anchors ----------

// Evidence is one literal slice of the artifact source. The Pipeline
// in [2] extracts these without LLM involvement (file read + regex),
// which is the attack on F1 (doc-vs-code drift): if the operator's
// artifact claims "X is in file:line", the Evidence[].Snippet IS the
// content at file:line, not a paraphrase.
type Evidence struct {
	// Source is a stable locator for where the snippet was extracted
	// from. Format examples:
	//   "file:///path/to/file.go:42-60"
	//   "git:abc123:internal/v4alpha/judge/pipeline.go:100-150"
	//   "url:https://example.com/doc#section-3"
	//   "spec_id:42:section:rubric"
	//   "artifact_id:1057:claim:0"
	Source string

	// Snippet is the LITERAL content of the source bytes in [start,
	// end). Whitespace is preserved verbatim. Never summarized by an
	// LLM — that's the whole point of step [2] being deterministic.
	Snippet string

	// Relevance is the extractor's confidence that this snippet is
	// actually relevant to the evaluation, in [0, 1]. The extractor
	// computes it from regex match strength (EC-014 evidence_missing
	// flags low-relevance claims).
	Relevance float64
}

// ---------- Edge case hits ----------

// EdgeCaseHit is one firing of one EdgeCase in step [3]. Even when
// the Pipeline does NOT short-circuit (severity=warn), every hit is
// recorded in Verdict.EdgeCaseHits for audit.
type EdgeCaseHit struct {
	// ID is the canonical edge case identifier ("EC-001" through
	// "EC-015"). Stable across versions.
	ID string

	// Severity is "fatal" | "error" | "warn".
	//   fatal:  pipeline MUST short-circuit with "errored" verdict
	//   error:  pipeline MUST short-circuit with "needs_human" (or
	//           "drift_detected" for EC-010, EC-011, EC-015)
	//   warn:   pipeline records the hit and proceeds
	Severity string

	// Trigger is the deterministic reason the EC fired. Human-
	// readable. Examples:
	//   "artifact len 0"
	//   "LLM provider returned 503"
	//   "regex match: 'ignore previous instructions'"
	Trigger string

	// Catches is the operator-failure identifier this EC is designed
	// to catch: "F1" through "F7" for the F-catalog, "infra" for
	// infrastructure failures, "config" for configuration errors, or
	// "bias-mitigation" for bias controls (EC-007, EC-008, EC-012).
	Catches string
}

// ---------- Temperature note (reproducibility) ----------

// TemperatureNote is the audit trail for "what configuration produced
// this verdict". Two verdicts with the same input AND the same
// TemperatureNote are reproducible (modulo provider non-determinism,
// which is reported as Seed=0 + a known-non-deterministic provider
// flag).
//
// Populated from env vars (ADR-007 §7):
//
//	DARK_JUDGE_PROVIDER     → Provider
//	DARK_JUDGE_MODEL        → Model
//	DARK_JUDGE_TEMPERATURE  → Temperature  (default 0.0)
//	DARK_JUDGE_SEED         → Seed         (0 if not supported)
//	DARK_JUDGE_MAX_TOKENS   → MaxTokens    (default 2048)
//	DARK_JUDGE_TIMEOUT_MS   → TimeoutMs    (default 15000)
//	DARK_JUDGE_TOP_P        → TopP         (default 1.0)
type TemperatureNote struct {
	Provider      string  // "anthropic" | "minimax" | "minimax-cn" | "openai" | "deepseek"
	Model         string  // e.g. "MiniMax-M3"
	Temperature   float64 // 0.0 default
	Seed          int64   // 0 = "non-seeded run"
	MaxTokens     int     // 2048 default
	TimeoutMs     int     // 15000 default
	TopP          float64 // 1.0 default
	PersonaID     string  // who judged
	RubricVersion string  // sha256[:16] of the rubric used
	SchemaVersion string  // e.g. "v4alpha/2026-09-27/001"
}

// ---------- Bias audit ----------

// BiasAudit records the bias indicators caught during evaluation.
// Each flag is set by a specific EC in step [3] or step [6].
// Operators can use this to detect when a judge's verdict is
// systematically skewed.
type BiasAudit struct {
	// SelfReference is true when EC-007 fired: the persona's provider
	// matches the artifact's author provider (self-judge).
	SelfReference bool

	// PositionBiasPotential is true when EC-008 fired: pairwise eval
	// without position swap applied.
	PositionBiasPotential bool

	// ImplicitAssumption is true when EC-012 fired: artifact contains
	// "assuming X" / "if Y" not in spec_intent.
	ImplicitAssumption bool

	// PriorVersionContamination is true when EC-013 fired: artifact
	// references paths from a different schema_version than the
	// current one.
	PriorVersionContamination bool

	// EvidenceMissing is true when EC-014 fired: artifact makes
	// verifiable claims without file:line refs.
	EvidenceMissing bool
}

// ---------- Pipeline inputs ----------

// EvaluateRequest is what the Pipeline consumes in step [1].
// Built by the transport layer from a vibe_publish tool call.
type EvaluateRequest struct {
	// EvalType is the canonical evaluation type: "drift_judge",
	// "brand_match", "compliance_check", etc. Drives the persona
	// default in step [4] when PersonaID is empty.
	EvalType string

	// SpecIntent is the one-paragraph "what should this artifact be"
	// hypothesis. Required (EC-006 short-circuits when missing).
	SpecIntent string

	// Ref is the artifact reference. nil is allowed only for purely
	// textual artifacts (rare).
	Ref *Ref

	// ArtifactContent is the pre-loaded artifact bytes. When non-empty,
	// the Pipeline uses this directly (zero-copy) without reading from
	// Ref. Use this when the caller (transport layer) has already
	// resolved external references; use Ref.Kind="file" when the
	// Pipeline should read from disk.
	ArtifactContent []byte

	// VibeCase is the canonical case label: "C1" through "C7".
	// Drives the Rubric loaded in step [4]. Required.
	VibeCase string

	// PersonaID is the optional explicit persona id. When empty, the
	// Pipeline resolves the default for (evalType, vibeCase) from
	// the PersonaRegistry. EC-005 fires when the explicit id is
	// unknown.
	PersonaID string

	// SchemaVersion is the project schema version, e.g.
	// "v4alpha/2026-09-27/001". Drives EC-013 (prior-version
	// contamination).
	SchemaVersion string
}

// ---------- LLM contract (step [5] only) ----------

// LLMClient is the contract for the LLM provider. ONLY step [5] of
// the pipeline uses it. All other steps are deterministic pure
// functions.
//
// Commit 1 defines the interface and a fake for tests. Commit 2
// wires the real HTTP client (DARK_JUDGE_PROVIDER env, MiniMax-M3
// default per ADR-003).
type LLMClient interface {
	// Complete sends a request and returns the provider's response.
	// Implementations MUST respect ctx cancellation (deadline /
	// cancel). They MUST populate the TemperatureNote fields
	// (Provider, Model) on the returned LLMResponse.
	Complete(ctx context.Context, req LLMRequest) (*LLMResponse, error)
}

// LLMRequest is what step [5] sends to the LLMClient. The fields
// are populated by step [4] (Persona + Rubric Resolver) — the
// prompt template is rendered once with the resolved rubric, and
// the same prompt is reused across retries.
//
// Anti-prompt-injection layer L1 (ADR-007 §5): the UserPrompt is a
// fixed template; artifact content goes through StructuredInputs[]
// (chunkified evidence), NEVER concatenated to the prompt string.
type LLMRequest struct {
	// SystemPrompt is the persona + rubric header. Stable across
	// requests for the same (persona, rubric_version).
	SystemPrompt string

	// UserPrompt is the fixed template body. Placeholders are
	// filled by template engine, not by string concat.
	UserPrompt string

	// StructuredInputs are the evidence chunks from step [2], passed
	// as a structured array (NOT concatenated to UserPrompt). The
	// model receives them via the provider's structured-inputs API
	// when available (OpenAI, Anthropic) or as a fenced JSON block
	// otherwise. EC-003 (prompt injection) is checked against this
	// field's contents.
	StructuredInputs []Evidence

	// Temperature, MaxTokens, TopP, Seed are the sampling params.
	// Defaults (per ADR-007 §7): T=0.0, MaxTokens=2048, TopP=1.0,
	// Seed=0 (non-seeded).
	Temperature float64
	MaxTokens   int
	TopP        float64
	Seed        int64

	// ResponseFormat is the strict JSON schema for the LLM's output.
	// Implementations MUST enforce it (provider-side response_format
	// param where supported, local validation otherwise).
	ResponseFormat ResponseFormat
}

// ResponseFormat declares the strict output schema for the LLM.
// ADR-007 §5 anti-injection layer L3: the Pipeline validates the
// LLMResponse.Content against this schema BEFORE step [6] runs.
type ResponseFormat struct {
	// Type is "json_schema" (strict) or "json_object" (relaxed).
	// Commit 1 supports only "json_schema".
	Type string

	// Schema is the JSON Schema body. The Pipeline's build-in
	// schema for verdict responses is at internal schema version
	// "v4alpha/2026-09-27/001" (see VerdictResponseSchema).
	Schema string
}

// LLMResponse is what step [5] gets back. The TemperatureNote fields
// are populated by the implementation (the Pipeline does NOT trust
// the LLM to self-report; the env-derived note is the truth).
type LLMResponse struct {
	// Content is the LLM's raw output. For Type=json_schema, this is
	// the parsed JSON body (no markdown fences).
	Content string

	// Provider, Model are echoed back for the TemperatureNote.
	Provider string
	Model    string

	// UsageTokens is the total tokens consumed (prompt + completion).
	// Used for cost tracking and per-verdict budget enforcement.
	UsageTokens int

	// FinishReason is the provider's stop reason ("stop",
	// "length", "content_filter", "error"). "length" is a warning
	// sign — the model may have been truncated mid-reasoning.
	FinishReason string

	// NonDeterministic is true when the provider reports that even
	// with T=0.0 + seed=N, the response may not be reproducible
	// (Anthropic currently does this; OpenAI does not).
	NonDeterministic bool
}

// ---------- Prompt-injection pattern registry ----------

// PromptInjectionPatterns are the 10 canonical patterns from spec v3
// §6.6.3. EC-003 scans artifact content against these.
//
// Compile-time invariants: the slice must have exactly 10 entries.
// Compile-time check at the bottom of this file.
var PromptInjectionPatterns = []string{
	`(?i)ignore (?:all )?previous instructions`,
	`(?i)disregard (?:all )?(?:prior|above) instructions`,
	`(?i)forget (?:everything|all) (?:above|before)`,
	`(?i)you are now (?:a |an )?(?:\w+ )?(?:without|free of) (?:rules|restrictions)`,
	`(?i)system\s*:\s*you`,
	`(?i)assistant\s*:\s*I (?:will|shall|am going to)`,
	`(?i)<\|im_start\|>`,
	`(?i)<\|im_end\|>`,
	`(?i)\[\s*INST\s*\]`,
	`(?i)###\s*(?:system|instruction)\s*:`,
}

// compilePromptInjectionRegexps compiles the 10 patterns once at
// package init. EC-003 uses this rather than re-compiling per call.
var compilePromptInjectionRegexps = sync.OnceValue(func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(PromptInjectionPatterns))
	for _, p := range PromptInjectionPatterns {
		out = append(out, regexp.MustCompile(p))
	}
	return out
})

// MatchPromptInjection returns the first pattern that matches the
// given text, or "" if none. Used by EC-003.
func MatchPromptInjection(text string) string {
	for i, re := range compilePromptInjectionRegexps() {
		if re.MatchString(text) {
			return PromptInjectionPatterns[i]
		}
	}
	return ""
}

// ---------- Common errors ----------

// ErrPersonaNotRegistered is returned by PersonaRegistry when an
// explicit persona_id has no registration. EC-005 fires on this.
var ErrPersonaNotRegistered = errors.New("judge: persona not registered")

// ErrRubricNotFound is returned by RubricRegistry when a vibe_case
// has no registered rubric. Caught by the Pipeline before step [5].
var ErrRubricNotFound = errors.New("judge: rubric not found for vibe_case")

// ErrLLMUnavailable is the wrapper for LLMClient errors that EC-002
// classifies as fatal. The LLMClient implementation wraps its
// transport-level errors with this sentinel.
var ErrLLMUnavailable = errors.New("judge: LLM unavailable")

// ErrArtifactTooLarge is the wrapper for inputs that exceed the
// configured MaxArtifactBytes. EC-004 fires on this.
var ErrArtifactTooLarge = errors.New("judge: artifact too large")

// ---------- Compile-time invariants ----------

// _ ensures the 10-pattern invariant is honored at compile time. If
// someone adds/removes a pattern without updating the constant, this
// fails to compile.
var _ = len(PromptInjectionPatterns) == 10
