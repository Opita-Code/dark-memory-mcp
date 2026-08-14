// Package orchestration — judge_evidence_validator.go
//
// v2.16.0: T2 — Strict Validator.
//
// Validates JudgeVerdict against an ArtifactReader (loaded by
// Transport). Rejects malformed JSON, empty evidence, and quotes
// that don't match the artifact at the cited file:line.
//
// Failure modes (all escalate to needs_human per the anti-hallucination
// anchor):
//
//   - Malformed JSON → needs_human
//   - Empty evidence → needs_human (the judge must cite at least
//     one file:line+quote; needs_human with empty evidence is allowed)
//   - Quote mismatch at file:line → needs_human
//   - Verdict not in {aligned, drift_detected, needs_human} → needs_human
//   - Confidence out of range [0,1] → needs_human
//
// Backward compatibility: ValidateOrNeedsHuman accepts ANY raw LLM
// output and returns a valid JudgeVerdict. Existing code that
// passes the VerdictJSON string through other paths (legacy drift
// judge) is unaffected.
package orchestration

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidVerdict is the sentinel error returned by Validate when
// a parsed JudgeVerdict has invalid evidence (e.g., quote doesn't
// match artifact). The ValidateOrNeedsHuman wrapper converts this
// into a needs_human verdict.
var ErrInvalidVerdict = errors.New("judge_evidence_validator: invalid verdict")

// Validate parses raw LLM output and verifies its evidence against
// the artifact at the cited file:line. Returns the parsed
// JudgeVerdict or an error.
//
// Errors:
//   - json parse failure
//   - missing required fields (verdict, confidence, reasoning)
//   - quote at file:line doesn't match the artifact
//   - evidence empty when verdict is aligned or drift_detected
func Validate(raw string, reader ArtifactReader) (*JudgeVerdict, error) {
	parsed, err := VerdictFromJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidVerdict, err)
	}

	// needs_human is allowed to have empty evidence (the judge
	// couldn't ground a quote, so it escalated).
	if parsed.Verdict != VerdictNeedsHuman {
		if len(parsed.Evidence) == 0 {
			return nil, fmt.Errorf("%w: evidence[] empty for verdict=%s (judge must cite at least one file:line+quote)",
				ErrInvalidVerdict, parsed.Verdict)
		}
		for i, e := range parsed.Evidence {
			if e.File == "" {
				return nil, fmt.Errorf("%w: evidence[%d] file is empty", ErrInvalidVerdict, i)
			}
			if e.Line < 1 {
				return nil, fmt.Errorf("%w: evidence[%d] line %d < 1", ErrInvalidVerdict, i, e.Line)
			}
			if strings.TrimSpace(e.Quote) == "" {
				return nil, fmt.Errorf("%w: evidence[%d] quote is empty", ErrInvalidVerdict, i)
			}
			actual, err := reader.ReadLine(e.Line)
			if err != nil {
				return nil, fmt.Errorf("%w: evidence[%d] %s:%d unreadable: %v",
					ErrInvalidVerdict, i, e.File, e.Line, err)
			}
			// Allow exact match OR containment (LLM may quote a substring
			// of the line). The Validator rejects only when the cited
			// quote has zero overlap with the artifact at file:line.
			if !strings.Contains(actual, e.Quote) && !strings.Contains(e.Quote, actual) {
				return nil, fmt.Errorf("%w: evidence[%d] at %s:%d — cited quote %q not found in artifact line (have %q)",
					ErrInvalidVerdict, i, e.File, e.Line, e.Quote, actual)
			}
		}
	}

	return parsed, nil
}

// ValidateOrNeedsHuman is the anti-hallucination escape hatch.
// Any failure in Validate is converted into a needs_human verdict
// with the failure reason as reasoning. This guarantees that callers
// always receive a valid JudgeVerdict and the LLM cannot "leak"
// invalid output through the pipeline.
//
// The returned JudgeVerdict ALWAYS has Verdict=needs_human on error;
// the caller can check this via `parsed.Verdict == VerdictNeedsHuman`
// and surface the underlying error via parsed.Reasoning.
func ValidateOrNeedsHuman(raw, personaID, evalType, harnessID, modelUsed string, reader ArtifactReader) *JudgeVerdict {
	parsed, err := Validate(raw, reader)
	if err == nil {
		return parsed
	}

	// Quote-validation failure: the judge invented or hallucinated a
	// quote. This is the canonical "needs_human" case.
	if errors.Is(err, ErrInvalidVerdict) {
		// Try to parse the verdict string anyway so we know if the
		// LLM at least got the verdict enum right.
		parsedRaw, parseErr := VerdictFromJSON(raw)
		verdict := VerdictNeedsHuman
		if parseErr == nil {
			// Preserve the LLM's verdict enum but force needs_human
			// anyway (quote mismatch is unrecoverable).
			_ = parsedRaw
			verdict = VerdictNeedsHuman
		}
		return &JudgeVerdict{
			Verdict:    verdict,
			Confidence: 0,
			Evidence:   []EvidenceItem{},
			Reasoning:  fmt.Sprintf("insufficient information to ground verdict: %v", err),
			PersonaID:  personaID,
			EvalType:   evalType,
			HarnessID:  harnessID,
			ModelUsed:  modelUsed,
			Timestamp:  time.Now().UTC(),
		}
	}

	// JSON parse failure: the LLM output was completely malformed.
	return &JudgeVerdict{
		Verdict:    VerdictNeedsHuman,
		Confidence: 0,
		Evidence:   []EvidenceItem{},
		Reasoning:  fmt.Sprintf("insufficient information to ground verdict: %v", err),
		PersonaID:  personaID,
		EvalType:   evalType,
		HarnessID:  harnessID,
		ModelUsed:  modelUsed,
		Timestamp:  time.Now().UTC(),
	}
}

// parseLineField is unused but kept for potential future direct LLM
// output pre-parsing. Remove if not needed by v2.16.x.
var _ = json.Marshal
