// Package constitution — check_test.go
//
// v2.20.0 T07 (spec 1276): SelfCritique unit tests. One test per
// principle + boundary tests + integration tests with DriftJudge.
package constitution

import (
	"strings"
	"testing"
)

// validInput returns a SelfCritiqueInput that satisfies all 5
// principles. Tests mutate one field at a time to verify each
// principle's effect.
func validInput() SelfCritiqueInput {
	return SelfCritiqueInput{
		Verdict:        "aligned",
		NLILabel:       "entailment",
		NLIConfidence:  0.85,
		SpecIntent:     "The artifact should describe a Go function that returns the SHA-256 hash of its input.",
		ArtifactBody:   "func Hash(s string) string { ... returns 64-char hex ... }",
		ArtifactSHA:    "a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd",
		ArtifactSource: "file",
		ArtifactPath:   "/tmp/example.go",
		ArtifactSize:   256,
	}
}

// TestSelfCritique_AllPrinciplesSatisfied — happy path.
func TestSelfCritique_AllPrinciplesSatisfied(t *testing.T) {
	out := SelfCritique(validInput())
	if !out.Passed {
		t.Fatalf("expected Passed=true; got false. Reason=%q Violated=%v", out.Reason, out.Violated)
	}
	if out.Reason != "" {
		t.Errorf("expected empty Reason on Pass; got %q", out.Reason)
	}
	if len(out.Violated) != 0 {
		t.Errorf("expected empty Violated on Pass; got %v", out.Violated)
	}
}

// TestSelfCritique_Principle1_NoArtifactSHA — verdict not grounded in evidence.
func TestSelfCritique_Principle1_NoArtifactSHA(t *testing.T) {
	in := validInput()
	in.ArtifactSHA = ""
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when artifact SHA is missing")
	}
	if !containsInt(out.Violated, 1) {
		t.Errorf("expected principle 1 in Violated; got %v", out.Violated)
	}
	if !strings.Contains(out.Reason, "principle 1") {
		t.Errorf("expected 'principle 1' in Reason; got %q", out.Reason)
	}
}

// TestSelfCritique_Principle1_WhitespaceSHA — SHA whitespace-only is treated as empty.
func TestSelfCritique_Principle1_WhitespaceSHA(t *testing.T) {
	in := validInput()
	in.ArtifactSHA = "   \t\n"
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when SHA is whitespace-only")
	}
	if !containsInt(out.Violated, 1) {
		t.Errorf("expected principle 1 in Violated; got %v", out.Violated)
	}
}

// TestSelfCritique_Principle2_EmptyArtifactBody — claims cannot cite locations.
func TestSelfCritique_Principle2_EmptyArtifactBody(t *testing.T) {
	in := validInput()
	in.ArtifactBody = ""
	in.ArtifactSize = 0
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when artifact body is empty")
	}
	if !containsInt(out.Violated, 2) {
		t.Errorf("expected principle 2 in Violated; got %v", out.Violated)
	}
}

// TestSelfCritique_Principle2_WhitespaceBody — whitespace-only body still violates principle 2.
func TestSelfCritique_Principle2_WhitespaceBody(t *testing.T) {
	in := validInput()
	in.ArtifactBody = "   \n\t"
	in.ArtifactSize = 0
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when artifact body is whitespace and size=0")
	}
	if !containsInt(out.Violated, 2) {
		t.Errorf("expected principle 2 in Violated; got %v", out.Violated)
	}
}

// TestSelfCritique_Principle2_NonZeroSizePasses — non-zero size is enough even if body is empty.
func TestSelfCritique_Principle2_NonZeroSizePasses(t *testing.T) {
	in := validInput()
	in.ArtifactBody = "" // empty body BUT size > 0 (binary artifact, etc.)
	in.ArtifactSize = 128
	out := SelfCritique(in)
	// Size > 0 means the artifact was processed (even if body is binary).
	// Principle 2 checks BOTH "body empty AND size=0"; non-zero size passes.
	if !out.Passed {
		t.Errorf("expected Passed=true with non-zero size even if body is empty; got false. Reason=%q", out.Reason)
	}
}

// TestSelfCritique_Principle3_NLIContradiction_DriftDetected — violation.
func TestSelfCritique_Principle3_NLIContradiction_DriftDetected(t *testing.T) {
	in := validInput()
	in.NLILabel = "contradiction"
	in.Verdict = "drift_detected"
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when NLI=contradiction AND verdict=drift_detected")
	}
	if !containsInt(out.Violated, 3) {
		t.Errorf("expected principle 3 in Violated; got %v", out.Violated)
	}
	if !strings.Contains(out.Reason, "evidence wrong") {
		t.Errorf("expected 'evidence wrong' in Reason; got %q", out.Reason)
	}
}

// TestSelfCritique_Principle3_Contradiction_NeedsHuman_OK — needs_human is the correct response.
func TestSelfCritique_Principle3_Contradiction_NeedsHuman_OK(t *testing.T) {
	in := validInput()
	in.NLILabel = "contradiction"
	in.Verdict = "needs_human"
	out := SelfCritique(in)
	if !out.Passed {
		t.Errorf("expected Passed=true when contradiction → needs_human; got false. Reason=%q", out.Reason)
	}
}

// TestSelfCritique_Principle3_Entailment_NoViolation — happy NLI path.
func TestSelfCritique_Principle3_Entailment_NoViolation(t *testing.T) {
	in := validInput()
	in.NLILabel = "entailment"
	in.Verdict = "aligned"
	out := SelfCritique(in)
	if !out.Passed {
		t.Errorf("expected Passed=true on entailment; got false. Reason=%q", out.Reason)
	}
}

// TestSelfCritique_Principle3_Neutral_NoViolation — neutral verdict doesn't violate principle 3.
func TestSelfCritique_Principle3_Neutral_NoViolation(t *testing.T) {
	in := validInput()
	in.NLILabel = "neutral"
	in.Verdict = "needs_human"
	out := SelfCritique(in)
	if !out.Passed {
		t.Errorf("expected Passed=true on neutral; got false. Reason=%q", out.Reason)
	}
}

// TestSelfCritique_Principle4_Informational — drift_detected and needs_human don't violate principle 4.
func TestSelfCritique_Principle4_Informational_DriftDetected(t *testing.T) {
	in := validInput()
	in.NLILabel = "entailment" // use entailment to avoid principle 3 violation
	in.Verdict = "drift_detected" // but mark drift_detected anyway
	out := SelfCritique(in)
	// Principle 4 is informational: drift_detected is a valid finding.
	// No violation.
	if !out.Passed {
		t.Errorf("expected Passed=true (principle 4 informational); got false. Reason=%q", out.Reason)
	}
}

func TestSelfCritique_Principle4_Informational_NeedsHuman(t *testing.T) {
	in := validInput()
	in.NLILabel = "entailment"
	in.Verdict = "needs_human"
	out := SelfCritique(in)
	if !out.Passed {
		t.Errorf("expected Passed=true (principle 4 informational); got false. Reason=%q", out.Reason)
	}
}

// TestSelfCritique_Principle5_EmptySpec — refuse ambiguous spec.
func TestSelfCritique_Principle5_EmptySpec(t *testing.T) {
	in := validInput()
	in.SpecIntent = ""
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when SpecIntent is empty")
	}
	if !containsInt(out.Violated, 5) {
		t.Errorf("expected principle 5 in Violated; got %v", out.Violated)
	}
}

// TestSelfCritique_Principle5_TooShortSpec — refuse short spec.
func TestSelfCritique_Principle5_TooShortSpec(t *testing.T) {
	in := validInput()
	in.SpecIntent = "go hash" // 7 chars, below minSpecIntentLen=10
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when SpecIntent is too short")
	}
	if !containsInt(out.Violated, 5) {
		t.Errorf("expected principle 5 in Violated; got %v", out.Violated)
	}
}

// TestSelfCritique_Principle5_WhitespaceOnlySpec — trimmed whitespace counts as short.
func TestSelfCritique_Principle5_WhitespaceOnlySpec(t *testing.T) {
	in := validInput()
	in.SpecIntent = "   \n\t   "
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false when SpecIntent is whitespace-only")
	}
	if !containsInt(out.Violated, 5) {
		t.Errorf("expected principle 5 in Violated; got %v", out.Violated)
	}
}

// TestSelfCritique_Principle5_BoundaryCaseSpec — exactly minSpecIntentLen passes.
func TestSelfCritique_Principle5_BoundaryCaseSpec(t *testing.T) {
	in := validInput()
	in.SpecIntent = "1234567890" // exactly 10 chars
	out := SelfCritique(in)
	if !out.Passed {
		t.Errorf("expected Passed=true when SpecIntent is exactly 10 chars; got false. Reason=%q", out.Reason)
	}
}

// TestSelfCritique_MultipleViolations — list all violated principles.
func TestSelfCritique_MultipleViolations(t *testing.T) {
	in := validInput()
	in.ArtifactSHA = "" // principle 1
	in.ArtifactBody = "" // principle 2
	in.ArtifactSize = 0 // principle 2
	in.SpecIntent = "" // principle 5
	out := SelfCritique(in)
	if out.Passed {
		t.Fatal("expected Passed=false with multiple violations")
	}
	// Expect 1, 2, 5 (NOT 3 or 4).
	if !containsInt(out.Violated, 1) || !containsInt(out.Violated, 2) || !containsInt(out.Violated, 5) {
		t.Errorf("expected Violated=[1,2,5]; got %v", out.Violated)
	}
	if containsInt(out.Violated, 3) {
		t.Errorf("did not expect principle 3 violation; got %v", out.Violated)
	}
	if containsInt(out.Violated, 4) {
		t.Errorf("principle 4 is informational; should never appear in Violated; got %v", out.Violated)
	}
	if !strings.Contains(out.Reason, "principle 1") || !strings.Contains(out.Reason, "principle 2") || !strings.Contains(out.Reason, "principle 5") {
		t.Errorf("expected all 3 violated principles in Reason; got %q", out.Reason)
	}
}

// TestSelfCritique_PrincipleOrdering — Violated should be sorted ascending.
func TestSelfCritique_PrincipleOrdering(t *testing.T) {
	in := validInput()
	in.SpecIntent = ""
	in.ArtifactSHA = ""
	in.ArtifactBody = ""
	in.ArtifactSize = 0
	out := SelfCritique(in)
	if !isSortedAscending(out.Violated) {
		t.Errorf("expected Violated sorted ascending; got %v", out.Violated)
	}
}

// TestSelfCritique_InvariantPureFunction — same input → same output.
func TestSelfCritique_InvariantPureFunction(t *testing.T) {
	in := validInput()
	out1 := SelfCritique(in)
	out2 := SelfCritique(in)
	if out1.Passed != out2.Passed {
		t.Errorf("SelfCritique is not pure: out1.Passed=%v out2.Passed=%v", out1.Passed, out2.Passed)
	}
	if out1.Reason != out2.Reason {
		t.Errorf("SelfCritique is not pure: out1.Reason=%q out2.Reason=%q", out1.Reason, out2.Reason)
	}
	if len(out1.Violated) != len(out2.Violated) {
		t.Errorf("SelfCritique is not pure: out1.Violated=%v out2.Violated=%v", out1.Violated, out2.Violated)
	}
	for i := range out1.Violated {
		if out1.Violated[i] != out2.Violated[i] {
			t.Errorf("SelfCritique is not pure: out1.Violated[%d]=%d out2.Violated[%d]=%d",
				i, out1.Violated[i], i, out2.Violated[i])
		}
	}
}

// TestSelfCritique_InvariantNoLogging — pure function, no observable side effects.
// (No global state mutation; tested implicitly by TestSelfCritique_InvariantPureFunction.)

// helpers

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func isSortedAscending(s []int) bool {
	for i := 1; i < len(s); i++ {
		if s[i] < s[i-1] {
			return false
		}
	}
	return true
}