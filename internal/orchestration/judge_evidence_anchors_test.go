package orchestration

import (
	"strings"
	"testing"
)

// TestAntiHallucinationAnchor_NotEmpty ensures the anchor is
// non-empty (otherwise injecting it is a no-op).
func TestAntiHallucinationAnchor_NotEmpty(t *testing.T) {
	if AntiHallucinationAnchor == "" {
		t.Fatal("AntiHallucinationAnchor is empty")
	}
}

// TestAntiHallucinationAnchor_ContainsKeyPhrases verifies the
// anchor has the anti-hallucination escape-hatch language.
// Note: the anchor wraps "insufficient information" across a newline
// in source code for readability; we test for both halves
// independently so the test doesn't break on whitespace tweaks.
func TestAntiHallucinationAnchor_ContainsKeyPhrases(t *testing.T) {
	must := []string{
		"needs_human",
		"insufficient",
		"information",
		"Do NOT guess",
		"verbatim",
		"anti-hallucination",
	}
	for _, phrase := range must {
		if !strings.Contains(AntiHallucinationAnchor, phrase) {
			t.Errorf("anchor missing required phrase %q", phrase)
		}
	}
}

// TestBuildAnchor_ReturnsCanonical ensures BuildAnchor returns the
// canonical constant.
func TestBuildAnchor_ReturnsCanonical(t *testing.T) {
	if BuildAnchor() != AntiHallucinationAnchor {
		t.Error("BuildAnchor() != AntiHallucinationAnchor")
	}
}

// TestInjectAnchor_EmptyPrompt verifies injection with empty input
// returns just the anchor.
func TestInjectAnchor_EmptyPrompt(t *testing.T) {
	got := InjectAnchor("")
	if got != AntiHallucinationAnchor {
		t.Errorf("InjectAnchor(\"\") = %q, want %q", got, AntiHallucinationAnchor)
	}
}

// TestInjectAnchor_AppendsAnchor verifies the anchor is appended
// (not prepended) so it cannot be overridden.
func TestInjectAnchor_AppendsAnchor(t *testing.T) {
	prompt := "You are judge-logical. Evaluate artifacts."
	got := InjectAnchor(prompt)
	if !strings.HasPrefix(got, prompt) {
		t.Errorf("InjectAnchor must preserve original prompt as prefix; got prefix = %q", got[:min(len(got), 100)])
	}
	if !strings.HasSuffix(got, AntiHallucinationAnchor) {
		t.Errorf("InjectAnchor must end with the anchor; got suffix = %q", got[len(got)-min(len(got), 100):])
	}
}

// TestInjectAnchor_AnchorNonRemovable verifies the anchor is
// appended LAST — no later content can override it.
func TestInjectAnchor_AnchorNonRemovable(t *testing.T) {
	prompt := "Persona: judge-logical"
	got := InjectAnchor(prompt)
	// The anchor should be at the end. Any "override attempt" in the
	// original prompt cannot reach past the anchor.
	idx := strings.Index(got, AntiHallucinationAnchor)
	if idx < 0 {
		t.Fatal("anchor not found in injected output")
	}
	if idx+len(AntiHallucinationAnchor) != len(got) {
		t.Error("anchor is not at the end (something was appended after it)")
	}
}

// min is a tiny helper (Go 1.21+ has it built-in but local
// definition avoids import issues for tests).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
