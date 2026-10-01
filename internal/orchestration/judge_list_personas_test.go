// judge_list_personas_test.go — Phase 9 alpha.20 Chunk 8.2.
//
// E2E tests for the v3 binary's dark_memory_judge_list_personas after
// Chunk 8.2. Validates that the v4alpha 6-person PersonaContent registry
// is exposed through the v3 MCP surface (closes Phase 8 e2e critical
// finding #2, row 2323 — T3).
//
// # Test layout
//
// Per SPEC-alpha-11-phase8.md §3.2, the 5 tests cover:
//  1. TestJudgeListPersonas_DefaultV2Only: WITHOUT WithV4AlphaPersonas,
//     registry returns exactly 8 compiled personas (legacy behavior
//     preserved — backward compat for harnesses that expect 8).
//  2. TestJudgeListPersonas_V4AlphaMerge: WITH WithV4AlphaPersonas(true),
//     registry returns 14 personas (8 v2 + 6 v4-new).
//  3. TestJudgeListPersonas_V4AlphaSourceDiscriminator: the 6 v4-new
//     entries carry Source="v4alpha" (not "compiled"); v2 entries
//     keep Source="compiled".
//  4. TestJudgeListPersonas_V4AlphaIDs: the 6 v4-new IDs are exactly
//     {judge-cross-modal, judge-pipeline, judge-opinion, judge-decision,
//     judge-research, judge-delegator} (the compile-time invariant
//     from v4alpha/judge/personas_v4.go:337).
//  5. TestJudgeListPersonas_V4AlphaFieldMapping: each v4-new persona
//     has non-empty Role (first sentence of PromptTemplate), Lens
//     (= EvaluationLens verbatim), Constraints (BiasControls),
//     Rubric (RequiredEvidence), Source="v4alpha", Default=false.
package orchestration

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// --- Test 1: legacy default (no v4alpha) → 8 personas ---

func TestJudgeListPersonas_DefaultV2Only(t *testing.T) {
	r, err := NewPersonaRegistry(RegistryOptions{
		IncludeMarkdownOverrides: false,
		IncludeV4Alpha:          false, // explicit
	})
	if err != nil {
		t.Fatalf("NewPersonaRegistry: %v", err)
	}
	personas := r.List()
	if len(personas) != 8 {
		t.Errorf("List returned %d personas; want 8 (legacy default with IncludeV4Alpha=false)", len(personas))
	}
	// All 8 must have Source="compiled".
	for _, p := range personas {
		if p.Source != PersonaSourceCompiled {
			t.Errorf("persona %s has Source=%q; want %q (legacy default excludes v4alpha)",
				p.ID, p.Source, PersonaSourceCompiled)
		}
	}
}

// --- Test 2: with v4alpha → 14 personas ---

func TestJudgeListPersonas_V4AlphaMerge(t *testing.T) {
	r, err := NewPersonaRegistry(RegistryOptions{
		IncludeMarkdownOverrides: false,
		IncludeV4Alpha:          true,
	})
	if err != nil {
		t.Fatalf("NewPersonaRegistry: %v", err)
	}
	personas := r.List()
	if len(personas) != 14 {
		t.Errorf("List returned %d personas; want 14 (8 v2 + 6 v4alpha)", len(personas))
	}
	// Spot check: 8 v2 IDs still present.
	wantV2 := []string{
		"judge-logical", "judge-visual", "judge-security", "judge-compositional",
		"judge-mutation", "judge-resilience", "judge-evidential", "judge-coverage",
	}
	for _, id := range wantV2 {
		if _, ok := r.Get(id); !ok {
			t.Errorf("v2 persona %q missing after v4alpha merge", id)
		}
	}
	// Spot check: 6 v4alpha IDs present.
	wantV4 := []string{
		"judge-cross-modal", "judge-pipeline", "judge-opinion",
		"judge-decision", "judge-research", "judge-delegator",
	}
	for _, id := range wantV4 {
		if _, ok := r.Get(id); !ok {
			t.Errorf("v4alpha persona %q missing after merge", id)
		}
	}
}

// --- Test 3: Source discriminator ---

func TestJudgeListPersonas_V4AlphaSourceDiscriminator(t *testing.T) {
	r, err := NewPersonaRegistry(RegistryOptions{
		IncludeMarkdownOverrides: false,
		IncludeV4Alpha:          true,
	})
	if err != nil {
		t.Fatalf("NewPersonaRegistry: %v", err)
	}
	personas := r.List()
	v2Count, v4Count := 0, 0
	for _, p := range personas {
		switch p.Source {
		case PersonaSourceCompiled:
			v2Count++
		case PersonaSourceV4Alpha:
			v4Count++
		default:
			t.Errorf("persona %s has unexpected Source=%q (expected %q or %q)",
				p.ID, p.Source, PersonaSourceCompiled, PersonaSourceV4Alpha)
		}
	}
	if v2Count != 8 {
		t.Errorf("v2 count = %d; want 8", v2Count)
	}
	if v4Count != 6 {
		t.Errorf("v4alpha count = %d; want 6", v4Count)
	}
}

// --- Test 4: v4alpha IDs match v4alpha registry ---

func TestJudgeListPersonas_V4AlphaIDs(t *testing.T) {
	// Cross-check: every v4alpha PersonaContent ID must produce an
	// orchestration Persona. The compile-time invariant at
	// v4alpha/judge/personas_v4.go:337 enforces this — but only for
	// the 6 IDs in that slice. Our registry must surface exactly
	// those 6 IDs (no more, no less) when IncludeV4Alpha=true.
	wantIDs := map[string]bool{
		"judge-cross-modal": false,
		"judge-pipeline":    false,
		"judge-opinion":     false,
		"judge-decision":    false,
		"judge-research":    false,
		"judge-delegator":   false,
	}
	r, err := NewPersonaRegistry(RegistryOptions{
		IncludeMarkdownOverrides: false,
		IncludeV4Alpha:          true,
	})
	if err != nil {
		t.Fatalf("NewPersonaRegistry: %v", err)
	}
	for _, p := range r.List() {
		if _, expected := wantIDs[p.ID]; expected {
			wantIDs[p.ID] = true
		}
	}
	for id, present := range wantIDs {
		if !present {
			t.Errorf("v4alpha persona %q missing from registry", id)
		}
	}
	// Also verify: each v4alpha ID has a PersonaContent in v4alpha
	// (round-trip check — our adapter would silently skip if the
	// v4alpha registry is missing the entry).
	for _, id := range v4alphaPersonaIDs {
		if c := judge.LookupPersonaContent(id); c == nil {
			t.Errorf("v4alpha/judge.LookupPersonaContent(%q) returned nil — compile-time invariant should prevent this", id)
		}
	}
}

// --- Test 5: field mapping correctness ---

func TestJudgeListPersonas_V4AlphaFieldMapping(t *testing.T) {
	r, err := NewPersonaRegistry(RegistryOptions{
		IncludeMarkdownOverrides: false,
		IncludeV4Alpha:          true,
	})
	if err != nil {
		t.Fatalf("NewPersonaRegistry: %v", err)
	}
	for _, id := range v4alphaPersonaIDs {
		p, ok := r.Get(id)
		if !ok {
			t.Errorf("Get(%q) returned ok=false", id)
			continue
		}
		// Source discriminator.
		if p.Source != PersonaSourceV4Alpha {
			t.Errorf("persona %s: Source=%q; want %q", id, p.Source, PersonaSourceV4Alpha)
		}
		// Default must be false (v4alpha personas are explicit-only).
		if p.Default {
			t.Errorf("persona %s: Default=true; want false (v4alpha personas are explicit-only)", id)
		}
		// EvalTypes is nil — v4alpha lenses are persona-level.
		if len(p.EvalTypes) != 0 {
			t.Errorf("persona %s: EvalTypes=%v; want empty (v4alpha lenses are persona-level)", id, p.EvalTypes)
		}
		// Lens = EvaluationLens verbatim (cross-check against v4alpha).
		c := judge.LookupPersonaContent(id)
		if c == nil {
			t.Errorf("v4alpha/judge.LookupPersonaContent(%q) returned nil", id)
			continue
		}
		if p.Lens != c.EvaluationLens {
			t.Errorf("persona %s: Lens != EvaluationLens", id)
		}
		// Voice = full PromptTemplate.
		if p.Voice != c.PromptTemplate {
			t.Errorf("persona %s: Voice != PromptTemplate", id)
		}
		// Constraints = BiasControls verbatim (length match).
		if len(p.Constraints) != len(c.BiasControls) {
			t.Errorf("persona %s: Constraints length=%d, want %d (BiasControls)",
				id, len(p.Constraints), len(c.BiasControls))
		}
		// Rubric = RequiredEvidence verbatim (length match).
		if len(p.Rubric) != len(c.RequiredEvidence) {
			t.Errorf("persona %s: Rubric length=%d, want %d (RequiredEvidence)",
				id, len(p.Rubric), len(c.RequiredEvidence))
		}
		// Role = first sentence of PromptTemplate (or full if no terminator).
		wantRole := firstSentence(c.PromptTemplate)
		if p.Role != wantRole {
			t.Errorf("persona %s: Role=%q, want %q (first sentence)",
				id, p.Role, wantRole)
		}
		// Role must be non-empty (asserted via firstSentence invariant).
		if p.Role == "" {
			t.Errorf("persona %s: Role is empty", id)
		}
		// Name must be non-empty AND contain "Judge" (derived from ID).
		if p.Name == "" {
			t.Errorf("persona %s: Name is empty", id)
		}
		if !strings.Contains(p.Name, "Judge") {
			t.Errorf("persona %s: Name=%q does not contain 'Judge'", id, p.Name)
		}
	}
}

// --- Test 6 (extra): Orchestrator.WithV4AlphaPersonas wiring ---

func TestOrchestrator_WithV4AlphaPersonas_Default(t *testing.T) {
	// By default, V4AlphaPersonasEnabled() returns false → registry
	// contains 8 personas. Operator opts in via WithV4AlphaPersonas(true).
	o := &Orchestrator{}
	if o.V4AlphaPersonasEnabled() {
		t.Error("V4AlphaPersonasEnabled default = true; want false")
	}
	o.WithV4AlphaPersonas(true)
	if !o.V4AlphaPersonasEnabled() {
		t.Error("V4AlphaPersonasEnabled after WithV4AlphaPersonas(true) = false; want true")
	}
	o.WithV4AlphaPersonas(false)
	if o.V4AlphaPersonasEnabled() {
		t.Error("V4AlphaPersonasEnabled after WithV4AlphaPersonas(false) = true; want false")
	}
	// Idempotent: calling WithV4AlphaPersonas(true) twice stays true.
	o.WithV4AlphaPersonas(true)
	o.WithV4AlphaPersonas(true)
	if !o.V4AlphaPersonasEnabled() {
		t.Error("V4AlphaPersonasEnabled idempotent=true; got false after 2× WithV4AlphaPersonas(true)")
	}
}