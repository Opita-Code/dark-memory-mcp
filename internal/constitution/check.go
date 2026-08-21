// Package constitution — check.go
//
// v2.20.0 T07 (spec 1276): SelfCritique performs post-NLI constitutional
// validation on a drift verdict. Implements the 5 constitutional
// principles from spec 1276 H6 invariant ("constitution_after_nli"):
//
//	(1) verdict-grounded-in-evidence   — verdict must reference the artifact
//	(2) claims-cite-locations          — artifact body must be processed
//	(3) NLI-contradiction → evidence wrong, not artifact
//	(4) drift/needs_human are findings not failures (informational)
//	(5) spec-ambiguity → refuse verdict — short/empty SpecIntent refuses
//
// The orchestrator invokes SelfCritique AFTER the NLI Score step
// (H6 hard invariant). On Passed=false, the verdict is overridden
// to "needs_human" so the operator reviews the constitutional concern.
package constitution

import (
	"strings"
)

// SelfCritiqueInput is the canonical input to SelfCritique. The
// orchestrator populates this from DriftJudgeOutput + NLI Score +
// resolved artifact metadata.
type SelfCritiqueInput struct {
	// Verdict is the NLI-derived verdict BEFORE self-critique.
	// "aligned" | "drift_detected" | "needs_human"
	Verdict string

	// NLILabel is the raw NLI label. "entailment" | "contradiction" | "neutral"
	NLILabel string

	// NLIConfidence is the raw NLI confidence (0..1).
	NLIConfidence float64

	// SpecIntent is the hypothesis (one-paragraph spec intent).
	// Required, but may be ambiguous (principle 5).
	SpecIntent string

	// ArtifactBody is the resolved artifact body (premise).
	// May be empty if the artifact is a sentinel (principle 2).
	ArtifactBody string

	// ArtifactSHA is the resolved artifact's SHA-256 (hex string).
	// Required by principle 1 (verdict grounded in evidence).
	ArtifactSHA string

	// ArtifactSource identifies how the artifact was resolved.
	// "url" | "file" | "git_sha" | "spec_id" | "artifact_id" |
	// "materialized_inline" (legacy Content route)
	ArtifactSource string

	// ArtifactPath is the canonical identifier for the artifact
	// (URL string, file path, git sha, spec id, artifact id).
	ArtifactPath string

	// ArtifactSize is the resolved body size in bytes.
	ArtifactSize int64
}

// SelfCritiqueOutput is the result of SelfCritique.
type SelfCritiqueOutput struct {
	// Passed is true if all 5 principles are satisfied.
	// false → orchestrator should override verdict to "needs_human".
	Passed bool

	// Reason is the explanation when Passed=false. Concatenates
	// the violated principle(s) with their descriptions.
	Reason string

	// Violated lists the principle numbers that failed (1..5).
	// Empty when Passed=true.
	Violated []int
}

// minSpecIntentLen is the minimum length for a non-ambiguous
// SpecIntent (principle 5). Below this, the spec is too short
// for the LLM to ground a verdict; SelfCritique refuses.
const minSpecIntentLen = 10

// SelfCritique performs post-NLI constitutional validation on a drift
// verdict, per spec 1276 T07 / H6 invariant.
//
// Invariants (sealed):
//   - Pure function. No I/O. Safe to call in tight loops.
//   - Does NOT log artifact body or AuthToken (H6 + sealed invariant #6).
//   - Returns Passed=true if and only if all 5 principles are satisfied.
//   - On any violation, adds the principle number to Violated and
//     appends to Reason (operator audit trail).
//
// 5 principles (spec 1276 T07 verbatim):
//   (1) verdict grounded in evidence
//   (2) claims cite locations
//   (3) NLI contradiction → evidence wrong, not artifact
//   (4) drift/needs_human are findings not failures
//   (5) spec ambiguity → refuse verdict
//
// Principle 4 is informational — it does not produce a violation
// (drift_detected and needs_human are valid findings, not failures).
func SelfCritique(in SelfCritiqueInput) SelfCritiqueOutput {
	out := SelfCritiqueOutput{Passed: true}

	// Principle 1: verdict grounded in evidence.
	// The verdict must reference the resolved artifact body. The
	// artifact SHA-256 is the canonical binding; if empty, the
	// verdict is not grounded in any artifact.
	if strings.TrimSpace(in.ArtifactSHA) == "" {
		out.Passed = false
		out.Violated = append(out.Violated, 1)
		out.Reason += "[principle 1] verdict not grounded in evidence: artifact SHA-256 missing; "
	}

	// Principle 2: claims cite locations.
	// The artifact body must have been processed (non-empty).
	// A zero-size artifact has no locations to cite.
	if strings.TrimSpace(in.ArtifactBody) == "" && in.ArtifactSize == 0 {
		out.Passed = false
		out.Violated = append(out.Violated, 2)
		out.Reason += "[principle 2] claims cannot cite locations: artifact body is empty (size=0); "
	}

	// Principle 3: NLI contradiction → evidence wrong, not artifact.
	// If the NLI model says the artifact contradicts the spec, the
	// EVIDENCE (spec/hypothesis, premise extraction, or model) is
	// suspect — NOT the artifact itself. A verdict=drift_detected
	// derived from contradiction violates this principle. The
	// orchestrator should mark needs_human instead.
	if in.NLILabel == "contradiction" && in.Verdict == "drift_detected" {
		out.Passed = false
		out.Violated = append(out.Violated, 3)
		out.Reason += "[principle 3] NLI contradiction implies evidence wrong, not artifact; verdict should be needs_human; "
	}

	// Principle 4: drift/needs_human are findings not failures.
	// Informational — verdict=drift_detected or needs_human is OK
	// and not a SelfCritique violation. This principle is documented
	// for operator reference but does not affect Passed.

	// Principle 5: spec ambiguity → refuse verdict.
	// A SpecIntent shorter than minSpecIntentLen is too ambiguous
	// to ground any verdict. SelfCritique refuses (Passed=false).
	trimmed := strings.TrimSpace(in.SpecIntent)
	if len(trimmed) < minSpecIntentLen {
		out.Passed = false
		out.Violated = append(out.Violated, 5)
		out.Reason += "[principle 5] spec intent is ambiguous (length < 10 chars after trim); refuse verdict; "
	}

	// Trim trailing separator for cleanliness.
	out.Reason = strings.TrimSpace(out.Reason)
	return out
}