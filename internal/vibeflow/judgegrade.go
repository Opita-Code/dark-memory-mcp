// Package vibeflow — Loop 11 L11.2 (Phase 21 wiring): grade the
// confidence number the PRODUCTION drift_judge path already emits.
//
// # WHY THIS FILE EXISTS
//
// Phase 20 closed with the finding that all nine of its primitives had
// ZERO production callers (core/phase-20-closeout.md §1). Phase 21
// starts by fixing that, for exactly ONE primitive, at the one place
// where the operator already receives a bare number.
//
// `PublishResult.Confidence float32` (internal/orchestration/
// publish_vibe.go:137) has shipped to every harness that calls
// vibe_publish, as:
//
//	"verdict": "needs_human", "confidence": 0.9
//
// with no indication of what that 0.9 means. That is the precise
// failure Loop 11 L11.1 was written to fix — a bare number whose
// epistemic status does not travel with it — sitting in the one
// output the operator reads on every single publish.
//
// # WHY THIS AND NOT ComputeVerdict
//
// ComputeVerdict was the originally proposed wiring and it was the
// wrong one. It requires a CostSummary, and CostSummary has zero
// producers outside this package (verified 2026-10-08: no reference
// to CostSession/CostSummary exists outside internal/vibeflow/). To
// wire it we would first have to invent a cost-recording subsystem.
//
// Inventing the input to justify shipping the output is the exact
// failure mode this whole phase exists to prevent. So we did not.
//
// What is available is better: `confidence` is a REAL recorded
// observation — the NLI provider's own confidence on its own verdict.
// It needs no invented data source. It only needs a label.
//
// # THE FOUR RULES
//
//	skipped  -> unverified. Confidence 0 here means NO LLM RAN.
//	            It is not a measured zero, and reading it as one
//	            would report "the judge measured 0% confidence".
//	pending  -> unverified. The check is still running; nothing has
//	            been observed yet.
//	measured -> the NLI confidence really is a recorded observation,
//	            so it is honestly `measured`. But it is measured
//	            AS A MEASUREMENT. One judge is correlated authority,
//	            not independent review (L12.1).
//	needs_human -> confidence describes the ESCALATION, not evidence
//	            against the artifact. This project's own record
//	            (rows 482/1985/2013/2016/2017) shows this judge
//	            produces false positives that operators had to
//	            overrule twice (evals 2016/2017, row 2533).
//
// # REUSE, NOT A NEW MECHANISM
//
// This reuses the L11.1 types (Claim, EvidenceGrade, GradeCost's
// ladder). It adds no new epistemic vocabulary, no new grade, no new
// struct family. The ladder is the same four rungs L11.1 defined.
//
// Purity: pure functions, deterministic, no clock, no I/O.
package vibeflow

import (
	"fmt"
	"strings"
)

// JudgeOutcome mirrors exactly what the production drift_judge path
// observed. Every field is already available inside publish_vibe at the
// moment the verdict is assigned; nothing here is fetched or inferred.
type JudgeOutcome struct {
	// Verdict is the canonical drift verdict as published:
	// aligned | drift_detected | needs_human | skipped | pending.
	// Empty means the pessimistic pre-judge default was returned.
	Verdict string

	// Confidence is the NLI provider's confidence, 0..1. Zero when
	// the judge was skipped or no LLM was configured.
	Confidence float32

	// ProviderID / ModelRev identify the authority that produced the
	// number. Empty means no judge actually ran.
	ProviderID string
	ModelRev   string
}

// Canonical verdict strings, mirrored from publish_vibe so the
// comparison cannot drift into a typo that silently ungrades.
const (
	VerdictAligned      = "aligned"
	VerdictDriftDetect  = "drift_detected"
	VerdictNeedsHuman   = "needs_human"
	VerdictSkipped      = "skipped"
	VerdictPendingState = "pending"
)

// judgeGradeCaveats are the honesty notes. Kept as named constants so
// a test can pin each string and a grep can find every one.
const (
	caveatSkipped = "no judge ran (auto_drift_check=false) — confidence 0 means no LLM was invoked, not a measured zero; this verdict certifies nothing"

	caveatPending = "drift check still running in the background (async_drift_check=true) — no verdict has been observed yet; this confidence is not a result"

	caveatNoJudge = "no judge provider recorded — the number was not produced by an NLI provider; treat as no observation at all"

	caveatNeedsHuman = "confidence describes the ESCALATION, not evidence against the artifact — a needs_human verdict means the judge declined to certify, which is not the same as finding drift; this project's judge has a documented false-positive pattern on good artifacts (rows 482/1985/2013/2016/2017) that operators overrode twice (evals 2016/2017, row 2533)"
)

// judgeAuthorityCaveat is appended to every grade that claims a real
// measurement. It is the L12.1 rule applied to the production path:
// one judge agreeing with itself is correlated authority, and the
// phase's own closeout could not claim the verdict was independently
// reviewed.
func judgeAuthorityCaveat(o JudgeOutcome) string {
	who := strings.TrimSpace(o.ProviderID)
	if who == "" {
		// Honest wording matters here. The judge DID run — we only know
		// that because Confidence > 0 — but runJudgePipeline does not
		// plumb the provider id, so we cannot name it. Saying "unknown"
		// would read as "no provider", contradicting the rest of the
		// sentence. Say what is actually true.
		who = "an unidentified provider (the call path does not return the provider id)"
	} else if rev := strings.TrimSpace(o.ModelRev); rev != "" {
		who += "/" + rev
	}
	return fmt.Sprintf(
		"single-judge measurement from %s — the number is a real recorded observation, but one authority judging is correlated, not independent review; it has not been confirmed by a second source (Loop 12 L12.1)", who)
}

// GradeJudgeConfidence returns the evidence grade OF THE CONFIDENCE
// NUMBER ITSELF, as the operator receives it.
//
// The ladder is strictly downward-only, matching L11.1:
//
//	no judge ran                        -> Unverified
//	no provider recorded                -> Unverified
//	check still pending                 -> Unverified
//	a real NLI confidence was observed  -> Measured
//
// It never returns Estimated or Modeled: this number is either an
// observation or it is nothing. Returning Modeled here would be a
// lie — nothing is projected.
func GradeJudgeConfidence(o JudgeOutcome) EvidenceGrade {
	switch strings.TrimSpace(o.Verdict) {
	case VerdictSkipped:
		return GradeUnverified
	case VerdictPendingState, "":
		return GradeUnverified
	}

	// A verdict was assigned. But did a judge actually produce it?
	if strings.TrimSpace(o.ProviderID) == "" && o.Confidence == 0 {
		return GradeUnverified
	}
	return GradeMeasured
}

// JudgeConfidenceCaveat returns the honesty note for the confidence
// number. Always non-empty for anything below Measured, and non-empty
// even at Measured because the authority caveat always applies.
func JudgeConfidenceCaveat(o JudgeOutcome) string {
	switch strings.TrimSpace(o.Verdict) {
	case VerdictSkipped:
		return caveatSkipped
	case VerdictPendingState, "":
		return caveatPending
	}

	if strings.TrimSpace(o.ProviderID) == "" && o.Confidence == 0 {
		return caveatNoJudge
	}

	if strings.TrimSpace(o.Verdict) == VerdictNeedsHuman {
		// The escalation caveat leads, because it is the one that
		// changes what the operator should DO.
		return caveatNeedsHuman + "; " + judgeAuthorityCaveat(o)
	}
	return judgeAuthorityCaveat(o)
}

// JudgeConfidenceClaim renders the confidence number as a Claim, using
// the L11.1 type so the operator-facing vocabulary stays identical
// between the library readout and the production MCP response.
//
// The caller can read the grade and the caveat straight off the Claim,
// or call GradeJudgeConfidence / JudgeConfidenceCaveat directly if it
// only needs one of the two. Both paths are the same ladder — there is
// no second derivation that could disagree.
func JudgeConfidenceClaim(o JudgeOutcome) Claim {
	g := GradeJudgeConfidence(o)
	return Claim{
		Label:  "drift judge confidence",
		Value:  fmt.Sprintf("%.4f (%s)", o.Confidence, o.Verdict),
		Grade:  g,
		Source: "drift_judge(production)",
		Caveat: JudgeConfidenceCaveat(o),
	}
}
