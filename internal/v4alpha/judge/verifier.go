// Package judge — Verifier (ADR-007 §6 step [6] post-check).
//
// Independent cheap post-check that runs AFTER step [5] (LLM call)
// and AFTER the EC-015 post-LLM pass. Regex-based. 0 LLM cost. 100%
// reproducible.
//
// The verifier is the canonical F7 catcher (verdict=aligned when
// reasoning lists problems). EC-015 is the EC-catalog entry that
// also catches F7; the verifier is the authoritative override.
//
// Override ladder (highest first):
//
//   evidence[i].Snippet contains negative keyword -> drift_detected
//     (most severe: the LLM anchored its verdict on contradictory
//      evidence; the verdict itself is wrong, not just suspicious)
//   reasoning contains negative keyword             -> needs_human
//     (less severe: the LLM may have meant something different
//      by "fail", but the operator should review)
package judge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Verifier is the cheap post-check. Stateless.
type Verifier struct{}

// NewVerifier returns a Verifier.
func NewVerifier() *Verifier { return &Verifier{} }

// Verify returns the final Verdict after independent verification.
// The original Verdict is returned unchanged when no override is
// warranted (verdict != aligned, or no contradictions found).
//
// Backwards compat: this is a pure function — Verify(proposed)
// returns either proposed (same pointer, no copy) or a new *Verdict
// with Verdict+EdgeCaseHits+Reasoning modified.
func (v *Verifier) Verify(ctx context.Context, pc *PipelineContext, proposed *Verdict) *Verdict {
	if err := ctx.Err(); err != nil {
		return proposed
	}
	if proposed == nil {
		return nil
	}
	// No override for non-aligned verdicts — the LLM already
	// reported drift/needs_human/errored.
	if proposed.Verdict != VerdictAligned {
		return proposed
	}

	// Override 1: evidence snippets contain negative keywords.
	// Most severe: drift_detected.
	for _, e := range pc.Evidence {
		for _, kw := range inconsistencyKeywords {
			if strings.Contains(strings.ToLower(e.Snippet), kw) {
				return overrideVerdict(proposed, VerdictDriftDetected,
					fmt.Sprintf("evidence %q contains %q", e.Source, kw))
			}
		}
	}

	// Override 2: reasoning contains negative keywords.
	// Less severe: needs_human (operator reviews).
	for _, kw := range inconsistencyKeywords {
		if strings.Contains(strings.ToLower(proposed.Reasoning), kw) {
			return overrideVerdict(proposed, VerdictNeedsHuman,
				fmt.Sprintf("reasoning contains %q", kw))
		}
	}

	return proposed
}

// overrideVerdict produces a new Verdict with the override applied.
// All non-verdict fields (PersonaID, RubricVersion, Criteria,
// Evidence, TemperatureNote, BiasAudit) are preserved.
func overrideVerdict(orig *Verdict, label, trigger string) *Verdict {
	out := *orig
	out.Verdict = label
	out.EdgeCaseHits = append(append([]EdgeCaseHit{}, orig.EdgeCaseHits...), EdgeCaseHit{
		ID:       "VERIFIER-OVERRIDE",
		Severity: "error",
		Trigger:  trigger,
		Catches:  "F7",
	})
	out.Reasoning = fmt.Sprintf("verifier override (%s): %s (original verdict=aligned)", label, trigger)
	out.EvaluatedAt = time.Now()
	return &out
}
