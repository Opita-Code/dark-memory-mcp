// Tests for personas_v4.go (rich content for the 3 v4-new personas).
package judge

import (
	"strings"
	"testing"
)

func TestPersonaContent_Validate(t *testing.T) {
	cases := []struct {
		name    string
		content PersonaContent
		wantErr bool
	}{
		{
			name: "valid",
			content: PersonaContent{
				PromptTemplate: "I am a judge",
				BiasControls:   []string{"don't bias"},
			},
			wantErr: false,
		},
		{
			name: "empty prompt template",
			content: PersonaContent{
				PromptTemplate: "  ",
				BiasControls:   []string{"don't bias"},
			},
			wantErr: true,
		},
		{
			name: "empty bias controls",
			content: PersonaContent{
				PromptTemplate: "I am a judge",
				BiasControls:   []string{},
			},
			wantErr: true,
		},
		{
			name: "nil bias controls",
			content: PersonaContent{
				PromptTemplate: "I am a judge",
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.content.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLookupPersonaContent_LegacyReturnsNil(t *testing.T) {
	// The 8 legacy personas have no rich content (they use the
	// generic system prompt template). Verify LookupPersonaContent
	// returns nil for each.
	for _, id := range []string{
		"judge-logical", "judge-visual", "judge-security",
		"judge-compositional", "judge-mutation", "judge-resilience",
		"judge-evidential", "judge-coverage",
		"", "unknown-id",
	} {
		if c := LookupPersonaContent(id); c != nil {
			t.Errorf("LookupPersonaContent(%q) returned non-nil; legacy personas should have no content", id)
		}
	}
}

func TestLookupPersonaContent_V4Personas(t *testing.T) {
	cases := []struct {
		id       string
		mustHave string // substring expected in PromptTemplate
	}{
		{"judge-cross-modal", "cross-modal"},
		{"judge-pipeline", "pipeline"},
		{"judge-opinion", "opinion"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			c := LookupPersonaContent(tc.id)
			if c == nil {
				t.Fatalf("LookupPersonaContent(%q) returned nil; expected rich content", tc.id)
			}
			if !strings.Contains(strings.ToLower(c.PromptTemplate), tc.mustHave) {
				t.Errorf("PromptTemplate missing %q: %q", tc.mustHave, c.PromptTemplate)
			}
			if len(c.BiasControls) == 0 {
				t.Error("BiasControls empty; expected at least 1")
			}
			if c.EvaluationLens == "" {
				t.Error("EvaluationLens empty; expected persona-specific scoring focus")
			}
		})
	}
}

func TestRegisterPersonaContent_Replaces(t *testing.T) {
	// Save + restore (don't pollute other tests).
	original := LookupPersonaContent("test-register-1")
	defer func() {
		if original != nil {
			_ = RegisterPersonaContent("test-register-1", original)
		} else {
			UnregisterPersonaContent("test-register-1")
		}
	}()

	// Empty content → rejected.
	if err := RegisterPersonaContent("test-register-1", &PersonaContent{
		PromptTemplate: "",
		BiasControls:   []string{"x"},
	}); err == nil {
		t.Error("expected error for empty PromptTemplate")
	}

	// Valid content → accepted.
	custom := &PersonaContent{
		PromptTemplate: "custom prompt",
		BiasControls:   []string{"custom control"},
	}
	if err := RegisterPersonaContent("test-register-1", custom); err != nil {
		t.Fatalf("register: %v", err)
	}
	got := LookupPersonaContent("test-register-1")
	if got == nil || got.PromptTemplate != "custom prompt" {
		t.Errorf("Lookup returned %+v; want custom prompt", got)
	}

	// Re-register with empty personaID → rejected.
	if err := RegisterPersonaContent("", custom); err == nil {
		t.Error("expected error for empty personaID")
	}
}

func TestPersonaContent_Roundtrip(t *testing.T) {
	// Sanity: every v4-new persona's content passes Validate. Catches
	// "I added a 4th v4 persona to defaultPersonas but forgot
	// defaultPersonaContents" regressions.
	for _, id := range []string{"judge-cross-modal", "judge-pipeline", "judge-opinion", "judge-decision", "judge-research", "judge-delegator"} {
		c := LookupPersonaContent(id)
		if c == nil {
			t.Errorf("missing content for %s", id)
			continue
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s.Validate() = %v", id, err)
		}
	}
}

// TestPersonaContent_Phase12_AuditQuality (Phase 12 T-104): the 2 new
// v4-new personas judge-modifications + judge-progress MUST have valid
// rich content with non-empty PromptTemplate, non-empty BiasControls,
// and non-empty RequiredEvidence. RequiredEvidence is critical for
// audit-quality: it tells the judge WHAT evidence to cite (so the
// verdict isn't just a thumbs-up).
func TestPersonaContent_Phase12_AuditQuality(t *testing.T) {
	for _, id := range []string{"judge-modifications", "judge-progress"} {
		t.Run(id, func(t *testing.T) {
			c := LookupPersonaContent(id)
			if c == nil {
				t.Fatalf("LookupPersonaContent(%q) returned nil; expected rich content for audit-quality persona", id)
			}
			if err := c.Validate(); err != nil {
				t.Fatalf("%s.Validate() = %v; expected valid content", id, err)
			}
			if c.PromptTemplate == "" {
				t.Errorf("%s.PromptTemplate empty; persona must declare its identity", id)
			}
			if c.EvaluationLens == "" {
				t.Errorf("%s.EvaluationLens empty; persona must declare its scoring focus", id)
			}
			if len(c.BiasControls) == 0 {
				t.Errorf("%s.BiasControls empty; persona must declare anti-bias instructions", id)
			}
			if len(c.RequiredEvidence) == 0 {
				t.Errorf("%s.RequiredEvidence empty; audit-quality persona MUST list what evidence to cite", id)
			}
		})
	}
}

// TestPersonaContent_Phase12_RegistrySize: registry has exactly 8
// v4-new personas (6 + 2 added in Phase 12 T-104). Catches both
// under-count (forgot to add to defaultPersonaContents) and
// over-count (added to defaultPersonaContents but forgot to add to
// the compile-time invariant slice).
func TestPersonaContent_Phase12_RegistrySize(t *testing.T) {
	// Every persona registered must pass Validate (catches the
	// over-count case — registry has the entry but the invariant
	// doesn't expect it).
	wantV4 := []string{
		"judge-cross-modal", "judge-pipeline", "judge-opinion",
		"judge-decision", "judge-research", "judge-delegator",
		"judge-modifications", "judge-progress",
	}
	for _, id := range wantV4 {
		c := LookupPersonaContent(id)
		if c == nil {
			t.Errorf("v4 persona %q missing from registry (under-count)", id)
			continue
		}
		if err := c.Validate(); err != nil {
			t.Errorf("v4 persona %q failed Validate: %v", id, err)
		}
	}
}
