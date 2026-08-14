// Package orchestration — judge_evidence_types.go
//
// v2.16.0: Evidence Contract types for the LLM-as-judge pipeline.
//
// Background: prior versions of dark-memory accepted the LLM's verdict
// JSON as an opaque string (SSD.SDDEvaluation.VerdictJSON). This
// meant the judge could invent quotes without grounding them in the
// artifact, and the operator had no way to verify that the judge's
// evidence was real (vs hallucinated). The "recap problem" that
// surfaced in dark-testing skill v4 evaluations (eval 919) was
// caused by exactly this gap: the judge returned needs_human because
// it had no way to cite file:line+quote evidence.
//
// Spec 1150 v2 closes this gap by adding a STRICT output schema:
//
//   {verdict, confidence, evidence[{file,line,quote,concern}],
//    reasoning, calibration_metrics?, persona_id, eval_type,
//    harness_id, model_used, timestamp}
//
// EvidenceItem.Quote MUST be verbatim — the Transport layer
// (judge_evidence_transport.go) computes sha256 at load time and
// the Validator (judge_evidence_validator.go) verifies each cited
// quote matches the artifact at file:line. Quotes that don't match
// are rejected → verdict=needs_human (anti-hallucination escape hatch).
//
// Backward compatibility: existing SSD rows with VerdictJSON as a
// string continue to parse via the legacy drift_judge path. New rows
// can additionally carry the structured fields. Old callers that
// don't read the new fields see no change.
package orchestration

import (
	"encoding/json"
	"time"
)

// VerdictValue is the discriminator for a judge verdict.
type VerdictValue string

const (
	VerdictAligned      VerdictValue = "aligned"
	VerdictDriftDetected VerdictValue = "drift_detected"
	VerdictNeedsHuman   VerdictValue = "needs_human"
)

// IsValid reports whether v is one of the three verdict values.
func (v VerdictValue) IsValid() bool {
	switch v {
	case VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman:
		return true
	default:
		return false
	}
}

// EvidenceItem is one piece of grounded evidence the judge cites.
// Quote MUST be verbatim — the Validator verifies against the
// Transport-loaded artifact at file:line.
type EvidenceItem struct {
	File    string `json:"file"`    // relative path (e.g. "src/auth.ts")
	Line    int    `json:"line"`    // 1-indexed
	Quote   string `json:"quote"`   // VERBATIM, must match file:line in the artifact
	Concern string `json:"concern"` // what the judge sees here (one short sentence)
}

// CalibrationMetrics captures judge calibration signals for a single
// verdict. Computed by the Validator from N-shot samples (N=1 for
// single-shot, N=3 or N=7 for consensus). CalibrationMetrics is
// OPTIONAL — single-shot verdicts may omit it.
type CalibrationMetrics struct {
	// Brier is the quadratic loss L = (1/N) * sum((y_hat - y)^2).
	// Per FAIR/Meta arxiv 2512.22245: lower is better, 0 is perfect.
	Brier *float64 `json:"brier,omitempty"`
	// ECE is the Expected Calibration Error, bin=0.1.
	// NOTE: known to be sensitive to bin size (per the same paper).
	ECE *float64 `json:"ece,omitempty"`
	// Kuiper is the Kuiper statistic: max C_k - min C_k weighted by S_j.
	// Per FAIR/Meta: most stable calibration metric.
	Kuiper *float64 `json:"kuiper,omitempty"`
	// NShot is the sample count used to compute this verdict.
	// 1 = single-shot, 3 or 7 = consensus (per Spec 1150 v2 T7).
	NShot int `json:"n_shot"`
	// SampleSize is the number of consensus samples that survived
	// (≤ NShot). 0 means single-shot (SampleSize = 1 = the verdict itself).
	SampleSize int `json:"sample_size,omitempty"`
}

// JudgeVerdict is the strict output schema that all judge LLMs MUST
// emit when the evidence contract is enabled (see
// PublishVibe.EvidenceContract). The Validator (T2) parses raw LLM
// output through this schema and rejects malformed responses.
type JudgeVerdict struct {
	Verdict             VerdictValue        `json:"verdict"`
	Confidence          float64             `json:"confidence"` // 0..1
	Evidence            []EvidenceItem      `json:"evidence"`    // ≥1 item; empty if needs_human
	Reasoning           string              `json:"reasoning"`
	CalibrationMetrics  *CalibrationMetrics `json:"calibration_metrics,omitempty"`
	PersonaID           string              `json:"persona_id"` // which persona was used
	EvalType            string              `json:"eval_type"`   // drift_judge | brand_match | ...
	HarnessID           string              `json:"harness_id"`  // opencode | claude_code | ...
	ModelUsed           string              `json:"model_used"`  // anthropic/claude-sonnet-4.5 | ...
	Timestamp           time.Time           `json:"timestamp"`
}

// MarshalJSON re-emits the schema in canonical form so callers that
// expect RFC3339 timestamps get them (time.Time's default JSON form
// is also RFC3339Nano but we make it explicit).
func (v JudgeVerdict) MarshalJSON() ([]byte, error) {
	type alias JudgeVerdict // avoid infinite recursion
	return json.Marshal(alias(v))
}

// VerdictFromJSON parses raw LLM output into a JudgeVerdict. Used by
// the Validator (T2). Returns ErrInvalidVerdictJSON on parse failure —
// caller should escalate to needs_human (anti-hallucination anchor).
//
// This function does NOT validate evidence (that's the Validator's
// job, which requires the Transport layer). It only parses the JSON
// shape.
func VerdictFromJSON(raw string) (*JudgeVerdict, error) {
	var v JudgeVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, errInvalidVerdictJSON{Reason: err.Error()}
	}
	if !v.Verdict.IsValid() {
		return nil, errInvalidVerdictJSON{Reason: "verdict not in {aligned, drift_detected, needs_human}"}
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return nil, errInvalidVerdictJSON{Reason: "confidence out of range [0,1]"}
	}
	if v.Reasoning == "" {
		return nil, errInvalidVerdictJSON{Reason: "reasoning is empty"}
	}
	// Note: we DON'T reject empty Evidence here. A needs_human
	// verdict with empty evidence is valid (the judge couldn't
	// ground a quote, so it returned needs_human). The Validator
	// (T2) handles the quote-validation logic separately.
	return &v, nil
}

// errInvalidVerdictJSON signals that raw LLM output did not parse
// into a valid JudgeVerdict. The caller should escalate to
// needs_human per the anti-hallucination anchor (T3).
type errInvalidVerdictJSON struct {
	Reason string
}

func (e errInvalidVerdictJSON) Error() string {
	return "judge_evidence_types: invalid verdict JSON: " + e.Reason
}
