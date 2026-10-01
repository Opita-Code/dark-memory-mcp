// Package judge — Phase 6 alpha.18.1 regression tests (Chunk 6.1).
//
// Verifies the canonical C3-C7 persona mapping per
// internal/v4alpha/vibe/spec.go:14-16:
//
//   C3 = decision (judge-decision)
//   C4 = research (judge-research)
//   C5 = video (judge-cross-modal)
//   C6 = audio (judge-pipeline)
//   C7 = multi (judge-evidential)
//
// Before this fix, the rubric.go default definitions encoded v3-legacy
// mappings (C3=image, C4=video, C5=bundle, C6=infra, C7=governance).
// The populateCalibration hook would have applied the wrong persona
// defaults — a LIVE BUG closed by this commit.
package judge

import (
	"testing"
)

// TestRubric_AllCanonicalC3C7PersonaIDs verifies the C3-C7 rubric
// defaults point at the canonical persona IDs per spec.go:14-16.
func TestRubric_AllCanonicalC3C7PersonaIDs(t *testing.T) {
	rr := NewRubricRegistry()

	tests := []struct {
		vibeCase  string
		personaID string
	}{
		{"C3", "judge-decision"},
		{"C4", "judge-research"},
		{"C5", "judge-cross-modal"},
		{"C6", "judge-pipeline"},
		{"C7", "judge-evidential"},
	}

	for _, tc := range tests {
		t.Run(tc.vibeCase, func(t *testing.T) {
			rub, err := rr.Get(tc.vibeCase)
			if err != nil {
				t.Fatalf("Get(%s): %v", tc.vibeCase, err)
			}
			if rub.PersonaID != tc.personaID {
				t.Errorf("rubric %s default persona: got %q, want %q (canonical mapping per spec.go:14-16)",
					tc.vibeCase, rub.PersonaID, tc.personaID)
			}
		})
	}
}

// TestPersonaRegistry_DefaultsContainDecisionAndResearch verifies that
// the v4-new personas judge-decision + judge-research (Phase 6 alpha.18.1)
// are registered in the default PersonaRegistry.
func TestPersonaRegistry_DefaultsContainDecisionAndResearch(t *testing.T) {
	pr := NewPersonaRegistry()

	for _, id := range []string{"judge-decision", "judge-research"} {
		t.Run(id, func(t *testing.T) {
			p, err := pr.Get(id)
			if err != nil {
				t.Fatalf("Get(%s): %v", id, err)
			}
			if p.DisplayName == "" {
				t.Errorf("persona %s missing DisplayName", id)
			}
		})
	}
}

// TestPersonaContent_DefaultsValidateForDecisionAndResearch verifies
// that the new persona contents pass Validate() (catches empty
// prompt templates or bias controls).
func TestPersonaContent_DefaultsValidateForDecisionAndResearch(t *testing.T) {
	for _, id := range []string{"judge-decision", "judge-research"} {
		t.Run(id, func(t *testing.T) {
			c := LookupPersonaContent(id)
			if c == nil {
				t.Fatalf("LookupPersonaContent(%s): nil", id)
			}
			if err := c.Validate(); err != nil {
				t.Errorf("persona %s content Validate: %v", id, err)
			}
			if c.PromptTemplate == "" {
				t.Errorf("persona %s has empty PromptTemplate", id)
			}
			if len(c.BiasControls) == 0 {
				t.Errorf("persona %s has no BiasControls", id)
			}
		})
	}
}

// TestRubric_CanonicalCriteriaPerVibeCase verifies the criteria names
// match the canonical per-vibe_case set. Catches any future regression
// where the wrong criteria are re-introduced for a vibe_case.
func TestRubric_CanonicalCriteriaPerVibeCase(t *testing.T) {
	rr := NewRubricRegistry()

	tests := []struct {
		vibeCase      string
		wantCriterion string // sample criterion name unique to canonical
	}{
		{"C3", "rationale_clarity"},   // decision (not composition)
		{"C4", "source_diversity"},    // research (not subject_consistency)
		{"C5", "subject_consistency"}, // video (kept from old C4)
		{"C6", "speech_clarity"},      // audio (not idempotence)
		{"C7", "invariant_compliance"}, // multi (kept)
	}

	for _, tc := range tests {
		t.Run(tc.vibeCase, func(t *testing.T) {
			rub, err := rr.Get(tc.vibeCase)
			if err != nil {
				t.Fatalf("Get(%s): %v", tc.vibeCase, err)
			}
			found := false
			for _, c := range rub.Criteria {
				if c.Name == tc.wantCriterion {
					found = true
					break
				}
			}
			if !found {
				names := make([]string, 0, len(rub.Criteria))
				for _, c := range rub.Criteria {
					names = append(names, c.Name)
				}
				t.Errorf("rubric %s missing canonical criterion %q. Have: %v",
					tc.vibeCase, tc.wantCriterion, names)
			}
		})
	}
}

// TestRubric_AllWeightsSumToOne verifies the invariant holds for
// every canonical C1-C7 rubric. Catches "typo'd a weight" regressions
// (e.g., 0.30 → 0.03).
func TestRubric_AllWeightsSumToOne(t *testing.T) {
	rr := NewRubricRegistry()
	for _, vc := range rr.List() {
		t.Run(vc, func(t *testing.T) {
			rub, err := rr.Get(vc)
			if err != nil {
				t.Fatalf("Get(%s): %v", vc, err)
			}
			var sum float64
			for _, c := range rub.Criteria {
				sum += c.Weight
			}
			if diff := sum - 1.0; diff > 1e-6 || diff < -1e-6 {
				t.Errorf("rubric %s weights sum to %f, must sum to 1.0", vc, sum)
			}
		})
	}
}
