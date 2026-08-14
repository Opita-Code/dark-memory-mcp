// Package orchestration — judge_personas_test.go
//
// v2.17.0 (spec 1155): unit tests for the Persona Registry + Markdown
// loader + Anchor composer + Prompt builder.
//
// These tests cover the core composition + resolution logic without
// requiring an LLM. Integration tests (smoke) that exercise the full
// Pipeline with the live LLM live in judge_personas_smoke_test.go.
package orchestration

import (
	"strings"
	"testing"
)

// --- Persona struct basics ---

func TestPersona_HasEvalType(t *testing.T) {
	p := &Persona{EvalTypes: []string{"drift_judge", "spec_test_alignment"}}
	if !p.HasEvalType("drift_judge") {
		t.Error("HasEvalType should return true for listed eval_type")
	}
	if !p.HasEvalType("spec_test_alignment") {
		t.Error("HasEvalType should return true for second listed eval_type")
	}
	if p.HasEvalType("brand_match") {
		t.Error("HasEvalType should return false for unlisted eval_type")
	}
	if p.HasEvalType("") {
		t.Error("HasEvalType should return false for empty eval_type")
	}
}

func TestPersona_HasEvalType_EmptyList(t *testing.T) {
	p := &Persona{EvalTypes: nil}
	if p.HasEvalType("drift_judge") {
		t.Error("HasEvalType should return false when EvalTypes is nil")
	}
}

func TestPersona_EffectiveConstraints(t *testing.T) {
	p := &Persona{
		Constraints: []string{"persona-specific-1", "persona-specific-2"},
	}
	got := p.EffectiveConstraints()

	// Must contain all 4 SharedConstraints.
	for _, sc := range SharedConstraints {
		found := false
		for _, c := range got {
			if c == sc {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("EffectiveConstraints missing shared constraint: %q", sc)
		}
	}

	// Must contain persona-specific constraints, appended AFTER shared.
	if len(got) != len(SharedConstraints)+2 {
		t.Errorf("EffectiveConstraints returned %d items, expected %d", len(got), len(SharedConstraints)+2)
	}
	if got[len(got)-2] != "persona-specific-1" {
		t.Error("persona-specific-1 should come before persona-specific-2")
	}
	if got[len(got)-1] != "persona-specific-2" {
		t.Error("persona-specific-2 should be last")
	}
}

// --- Default personas ---

func TestCompiledDefaultPersonas_LoadsAllEight(t *testing.T) {
	personas := CompiledDefaultPersonas()
	if len(personas) != 8 {
		t.Fatalf("CompiledDefaultPersonas returned %d personas, expected 8", len(personas))
	}
}

func TestCompiledDefaultPersonas_AllHaveRequiredFields(t *testing.T) {
	personas := CompiledDefaultPersonas()
	for _, p := range personas {
		if p.ID == "" {
			t.Errorf("persona has empty ID")
		}
		if p.Name == "" {
			t.Errorf("persona %s has empty Name", p.ID)
		}
		if p.Lens == "" {
			t.Errorf("persona %s has empty Lens", p.ID)
		}
		if len(p.Rubric) < 3 {
			t.Errorf("persona %s has %d rubric items, expected at least 3 (DAGMetric-style atomic checks)", p.ID, len(p.Rubric))
		}
		if p.Voice == "" {
			t.Errorf("persona %s has empty Voice", p.ID)
		}
		if p.Source != PersonaSourceCompiled {
			t.Errorf("persona %s has Source %q, expected %q", p.ID, p.Source, PersonaSourceCompiled)
		}
	}
}

func TestCompiledDefaultPersonas_SevenDefaultsOneExplicitOnly(t *testing.T) {
	personas := CompiledDefaultPersonas()
	defaultCount := 0
	explicitOnlyCount := 0
	for _, p := range personas {
		if p.Default {
			defaultCount++
		}
		// judge-coverage is registered but EvalTypes is nil (explicit-only).
		if len(p.EvalTypes) == 0 {
			explicitOnlyCount++
		}
	}
	if defaultCount != 7 {
		t.Errorf("expected 7 default personas, got %d", defaultCount)
	}
	if explicitOnlyCount != 1 {
		t.Errorf("expected 1 explicit-only persona (judge-coverage), got %d", explicitOnlyCount)
	}
}

func TestCompiledDefaultPersonas_JudgeCoverageIsExplicitOnly(t *testing.T) {
	personas := CompiledDefaultPersonas()
	var coverage *Persona
	for _, p := range personas {
		if p.ID == "judge-coverage" {
			coverage = p
			break
		}
	}
	if coverage == nil {
		t.Fatal("judge-coverage not found in compiled defaults")
	}
	if coverage.Default {
		t.Error("judge-coverage should NOT be a default for any eval_type")
	}
	if len(coverage.EvalTypes) != 0 {
		t.Errorf("judge-coverage should have no eval_types (explicit-only), got %v", coverage.EvalTypes)
	}
}

func TestCompiledDefaultPersonas_CoverAllEvalTypes(t *testing.T) {
	personas := CompiledDefaultPersonas()
	expectedEvalTypes := []string{
		"drift_judge", "spec_test_alignment", "brand_match",
		"pii_detect", "prompt_injection_scan", "security_coverage",
		"mindset_compose", "mindset_quality",
		"mutation_score_check", "resilience_check", "grounding_check",
	}
	covered := map[string]bool{}
	for _, p := range personas {
		for _, et := range p.EvalTypes {
			covered[et] = true
		}
	}
	for _, expected := range expectedEvalTypes {
		if !covered[expected] {
			t.Errorf("eval_type %q is not covered by any default persona", expected)
		}
	}
}

func TestCompiledDefaultPersonas_JudgeSecurityHasCanonicalText(t *testing.T) {
	// spec 1155 v14 §4.2 mandates the canonical full text for the
	// judge-security persona. Verify it.
	personas := CompiledDefaultPersonas()
	var security *Persona
	for _, p := range personas {
		if p.ID == "judge-security" {
			security = p
			break
		}
	}
	if security == nil {
		t.Fatal("judge-security not found")
	}

	// Must mention OWASP LLM Top 10.
	if !strings.Contains(security.Lens, "OWASP LLM Top 10") {
		t.Error("judge-security lens must mention OWASP LLM Top 10")
	}
	// Must have a fail-closed constraint.
	found := false
	for _, c := range security.Constraints {
		if strings.Contains(c, "needs_human") {
			found = true
			break
		}
	}
	if !found {
		t.Error("judge-security must have a fail-closed constraint that escalates to needs_human")
	}
	// Must have at least 5 rubric items (we shipped 6).
	if len(security.Rubric) < 5 {
		t.Errorf("judge-security has %d rubric items, expected at least 5", len(security.Rubric))
	}
}

// --- Anchor composer ---

func TestComposeAnchorText_NoPersona(t *testing.T) {
	got := composeAnchorText(nil)
	if !strings.Contains(got, "insufficient") || !strings.Contains(got, "information") {
		t.Error("composeAnchorText(nil) must include the anti-hallucination anchor text (with 'insufficient' + 'information' as substrings)")
	}
	// No persona-specific section when persona is nil.
	if strings.Contains(got, "PERSONA-SPECIFIC CONSTRAINTS") {
		t.Error("composeAnchorText(nil) should not include PERSONA-SPECIFIC CONSTRAINTS section")
	}
}

func TestComposeAnchorText_WithPersona(t *testing.T) {
	p := &Persona{
		Constraints: []string{
			"custom-constraint-1",
			"custom-constraint-2",
		},
	}
	got := composeAnchorText(p)
	if !strings.Contains(got, "PERSONA-SPECIFIC CONSTRAINTS") {
		t.Error("composeAnchorText with persona must include PERSONA-SPECIFIC CONSTRAINTS section")
	}
	if !strings.Contains(got, "custom-constraint-1") {
		t.Error("composeAnchorText must include persona constraint 1")
	}
	if !strings.Contains(got, "custom-constraint-2") {
		t.Error("composeAnchorText must include persona constraint 2")
	}
	// Must include the canonical anchor.
	if !strings.Contains(got, "insufficient") || !strings.Contains(got, "information") {
		t.Error("composeAnchorText must include the canonical anti-hallucination anchor (with 'insufficient' + 'information' as substrings)")
	}
	// Persona-specific constraints must come BEFORE the canonical anchor.
	idxCustom := strings.Index(got, "custom-constraint-1")
	idxAnchor := strings.Index(got, "anti-hallucination")
	if idxCustom >= idxAnchor {
		t.Error("persona-specific constraints must come BEFORE the canonical anchor")
	}
}

// --- Markdown loader ---

func TestParseMarkdownPersona_FullFile(t *testing.T) {
	content := `---
id: judge-security
eval_types: [security_coverage]
default: false
---

# Role

Custom security reviewer role.

## Lens

Custom lens with OWASP LLM Top 10.

## Rubric

- First rubric item
- Second rubric item
- Third rubric item

## Constraints

- Custom constraint 1
- Custom constraint 2

## Voice

Custom voice.
`
	got, err := parseMarkdownPersona(content, "test.md")
	if err != nil {
		t.Fatalf("parseMarkdownPersona error: %v", err)
	}
	if got.ID != "judge-security" {
		t.Errorf("ID = %q, want judge-security", got.ID)
	}
	if len(got.EvalTypes) != 1 || got.EvalTypes[0] != "security_coverage" {
		t.Errorf("EvalTypes = %v, want [security_coverage]", got.EvalTypes)
	}
	if !strings.Contains(got.Role, "Custom security reviewer") {
		t.Errorf("Role = %q, expected to contain 'Custom security reviewer'", got.Role)
	}
	if len(got.Rubric) != 3 {
		t.Errorf("Rubric = %v, expected 3 items", got.Rubric)
	}
	if len(got.Constraints) != 2 {
		t.Errorf("Constraints = %v, expected 2 items", got.Constraints)
	}
	if !strings.Contains(got.Source, "test.md") {
		t.Errorf("Source = %q, expected to contain 'test.md'", got.Source)
	}
}

func TestParseMarkdownPersona_MissingID(t *testing.T) {
	content := `---
eval_types: [security_coverage]
---

# Role

Test role.
`
	_, err := parseMarkdownPersona(content, "test.md")
	if err == nil {
		t.Error("parseMarkdownPersona should error when id is missing")
	}
	if !strings.Contains(err.Error(), "id is required") {
		t.Errorf("error should mention 'id is required', got: %v", err)
	}
}

func TestParseMarkdownPersona_MissingFrontmatter(t *testing.T) {
	content := `# Role

No frontmatter here.
`
	_, err := parseMarkdownPersona(content, "test.md")
	if err == nil {
		t.Error("parseMarkdownPersona should error when frontmatter is missing")
	}
}

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantFM      string
		wantBody    string
		expectError bool
	}{
		{
			name:     "valid",
			input:    "---\nid: foo\n---\n# Body\nContent",
			wantFM:   "id: foo",
			wantBody: "# Body\nContent",
		},
		{
			name:        "missing leading",
			input:       "id: foo\n---\n# Body",
			expectError: true,
		},
		{
			name:        "missing trailing",
			input:       "---\nid: foo\n# Body",
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body, err := splitFrontmatter(tt.input)
			if tt.expectError {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fm != tt.wantFM {
				t.Errorf("frontmatter = %q, want %q", fm, tt.wantFM)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestParseBulletList(t *testing.T) {
	got := parseBulletList("- one\n- two\n* three\n")
	if len(got) != 3 {
		t.Fatalf("got %d items, expected 3", len(got))
	}
	if got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Errorf("got %v, expected [one two three]", got)
	}
}

func TestParseBodySections(t *testing.T) {
	body := `# Role

Role content.

## Lens

Lens content.

## Rubric

- one
- two
`
	sections := parseBodySections(body)
	if !strings.Contains(sections["Role"], "Role content") {
		t.Errorf("Role section = %q, expected 'Role content'", sections["Role"])
	}
	if !strings.Contains(sections["Lens"], "Lens content") {
		t.Errorf("Lens section = %q, expected 'Lens content'", sections["Lens"])
	}
	if !strings.Contains(sections["Rubric"], "one") {
		t.Errorf("Rubric section = %q, expected to contain 'one'", sections["Rubric"])
	}
}

func TestMergePersonaOverride_FieldLevelMerge(t *testing.T) {
	base := &Persona{
		ID:        "judge-security",
		Name:      "Security Reviewer (default)",
		Role:      "Original role",
		Lens:      "Original lens",
		Rubric:    []string{"r1", "r2"},
		Voice:     "Original voice",
		Source:    PersonaSourceCompiled,
		Default:   true,
		EvalTypes: []string{"security_coverage"},
	}
	override := &Persona{
		ID:        "judge-security",
		Role:      "OVERRIDDEN role",
		Rubric:    []string{"o1", "o2", "o3"},
		Source:    "file:test.md",
		Default:   false,
		EvalTypes: []string{"security_coverage", "pii_detect"},
	}

	merged, err := MergePersonaOverride(base, override)
	if err != nil {
		t.Fatalf("MergePersonaOverride error: %v", err)
	}
	// Merged fields from override.
	if merged.Role != "OVERRIDDEN role" {
		t.Errorf("Role = %q, expected 'OVERRIDDEN role'", merged.Role)
	}
	if len(merged.Rubric) != 3 {
		t.Errorf("Rubric length = %d, expected 3", len(merged.Rubric))
	}
	if len(merged.EvalTypes) != 2 {
		t.Errorf("EvalTypes = %v, expected 2 items", merged.EvalTypes)
	}
	// Preserved fields from base.
	if merged.Name != "Security Reviewer (default)" {
		t.Errorf("Name = %q, expected 'Security Reviewer (default)' (preserved)", merged.Name)
	}
	if merged.Lens != "Original lens" {
		t.Errorf("Lens = %q, expected 'Original lens' (preserved)", merged.Lens)
	}
	// Source updated.
	if merged.Source != "file:test.md" {
		t.Errorf("Source = %q, expected 'file:test.md'", merged.Source)
	}
}

func TestMergePersonaOverride_IDMismatchErrors(t *testing.T) {
	base := &Persona{ID: "judge-security"}
	override := &Persona{ID: "judge-visual", Source: "file:test.md"}
	_, err := MergePersonaOverride(base, override)
	if err == nil {
		t.Error("MergePersonaOverride should error when id mismatches")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should mention 'does not match', got: %v", err)
	}
}

// --- Registry ---

func TestNewPersonaRegistry_Defaults(t *testing.T) {
	r, err := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	if err != nil {
		t.Fatalf("NewPersonaRegistry error: %v", err)
	}
	if r == nil {
		t.Fatal("registry is nil")
	}
	// 8 personas registered.
	if got := len(r.List()); got != 8 {
		t.Errorf("List returned %d, expected 8", got)
	}
}

func TestPersonaRegistry_ResolveByID(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	p, err := r.Resolve("security_coverage", "judge-security")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if p.ID != "judge-security" {
		t.Errorf("Resolve returned id %q, expected judge-security", p.ID)
	}
}

func TestPersonaRegistry_ResolveByID_NotFound(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	_, err := r.Resolve("security_coverage", "nonexistent-persona")
	if err == nil {
		t.Error("Resolve should error for unknown persona_id")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestPersonaRegistry_ResolveByEvalType_Default(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	// drift_judge → judge-logical (default).
	p, err := r.Resolve("drift_judge", "")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if p.ID != "judge-logical" {
		t.Errorf("drift_judge should resolve to judge-logical, got %q", p.ID)
	}
	// brand_match → judge-visual.
	p, err = r.Resolve("brand_match", "")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if p.ID != "judge-visual" {
		t.Errorf("brand_match should resolve to judge-visual, got %q", p.ID)
	}
}

func TestPersonaRegistry_ResolveFallbackToJudgeLogical(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	// Some eval_type with no default persona.
	p, err := r.Resolve("nonexistent_eval_type", "")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if p.ID != "judge-logical" {
		t.Errorf("nonexistent eval_type should fallback to judge-logical, got %q", p.ID)
	}
}

func TestPersonaRegistry_ListSorted(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	personas := r.List()
	if len(personas) < 2 {
		t.Fatalf("List returned %d, expected at least 2", len(personas))
	}
	for i := 1; i < len(personas); i++ {
		if personas[i-1].ID > personas[i].ID {
			t.Errorf("List not sorted: %q > %q", personas[i-1].ID, personas[i].ID)
		}
	}
}

func TestPersonaRegistry_GetByID(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	p, ok := r.Get("judge-security")
	if !ok {
		t.Error("Get(judge-security) should return ok=true")
	}
	if p.ID != "judge-security" {
		t.Errorf("Get returned id %q, expected judge-security", p.ID)
	}
	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) should return ok=false")
	}
}

func TestPersonaRegistry_DeterministicTieBreak(t *testing.T) {
	// Two defaults for the same eval_type → lex-smallest ID wins.
	_, _ = NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	// Simulate by overriding a persona's eval_type to claim drift_judge
	// (conflicting with judge-logical). Use the registry's internal
	// access via a custom registry construction.
	custom := CompiledDefaultPersonas()
	// Add a second claimant for drift_judge by copying judge-evidential
	// and changing its eval_types.
	for _, p := range custom {
		if p.ID == "judge-evidential" {
			p.EvalTypes = []string{"drift_judge"}
			p.Default = true // both claim default
			break
		}
	}
	// To test tie-break, we'd need to inject this into the registry.
	// For now, verify the tie-break method directly via reindex.
	registry := &PersonaRegistry{
		personas: map[string]*Persona{},
		byEval:   map[string][]*Persona{},
	}
	for _, p := range custom {
		registry.personas[p.ID] = p
	}
	registry.reindex()
	// drift_judge should have 2 personas: judge-evidential (id
	// "judge-evidential") and judge-logical (id "judge-logical").
	// Both claim default=true. Lex-smallest ID wins.
	list := registry.byEval["drift_judge"]
	if len(list) != 2 {
		t.Fatalf("drift_judge should have 2 personas, got %d", len(list))
	}
	if list[0].ID != "judge-evidential" {
		t.Errorf("default should be judge-evidential (lex-smaller), got %q", list[0].ID)
	}
	if list[1].ID != "judge-logical" {
		t.Errorf("second should be judge-logical, got %q", list[1].ID)
	}
}

// --- Prompt builder ---

func TestNewJudgePromptBuilder_NilRegistry(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewJudgePromptBuilder(nil) should panic")
		}
	}()
	_ = NewJudgePromptBuilder(nil)
}

func TestJudgePromptBuilder_Build_SystemPrompt(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	b := NewJudgePromptBuilder(r)

	jp, err := b.Build("security_coverage", "", nil, "Spec intent: test")
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if jp.PersonaID != "judge-security" {
		t.Errorf("PersonaID = %q, expected judge-security", jp.PersonaID)
	}
	if jp.EvalType != "security_coverage" {
		t.Errorf("EvalType = %q, expected security_coverage", jp.EvalType)
	}
	if !strings.Contains(jp.SystemPrompt, "judge-security") && !strings.Contains(jp.SystemPrompt, "Security Reviewer") {
		t.Errorf("SystemPrompt should reference the persona; got prefix: %q", jp.SystemPrompt[:min(200, len(jp.SystemPrompt))])
	}
	if !strings.Contains(jp.SystemPrompt, "insufficient") || !strings.Contains(jp.SystemPrompt, "information") {
		t.Error("SystemPrompt must include the anti-hallucination anchor (with 'insufficient' + 'information' as substrings)")
	}
	if !strings.Contains(jp.UserPrompt, "Spec intent") {
		t.Error("UserPrompt should contain 'Spec intent' section")
	}
}

func TestJudgePromptBuilder_Build_ExplicitPersonaID(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	b := NewJudgePromptBuilder(r)

	// Explicit persona_id resolves to that persona, regardless of eval_type default.
	jp, err := b.Build("drift_judge", "judge-security", nil, "")
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if jp.PersonaID != "judge-security" {
		t.Errorf("explicit persona_id should win, got %q", jp.PersonaID)
	}
}

func TestJudgePromptBuilder_Build_ExplicitOnlyPersona(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	b := NewJudgePromptBuilder(r)

	// judge-coverage is explicit-only; reachable only via persona_id.
	jp, err := b.Build("spec_test_alignment", "judge-coverage", nil, "")
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if jp.PersonaID != "judge-coverage" {
		t.Errorf("explicit persona_id should resolve to judge-coverage, got %q", jp.PersonaID)
	}
}

func TestJudgePromptBuilder_Build_UnknownPersonaErrors(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	b := NewJudgePromptBuilder(r)

	_, err := b.Build("drift_judge", "nonexistent-persona", nil, "")
	if err == nil {
		t.Error("Build should error for unknown persona_id")
	}
}

// --- Orchestrator integration ---

func TestOrchestrator_EnsurePersonaRegistry(t *testing.T) {
	// Build a minimal orchestrator with just a Store (no LLM needed).
	o := &Orchestrator{}
	r, err := o.ensurePersonaRegistry()
	if err != nil {
		t.Fatalf("ensurePersonaRegistry error: %v", err)
	}
	if r == nil {
		t.Fatal("registry is nil")
	}
	if got := len(r.List()); got != 8 {
		t.Errorf("registry has %d personas, expected 8", got)
	}
}

func TestOrchestrator_EnsurePersonaBuilder(t *testing.T) {
	o := &Orchestrator{}
	b, err := o.ensurePersonaBuilder()
	if err != nil {
		t.Fatalf("ensurePersonaBuilder error: %v", err)
	}
	if b == nil {
		t.Fatal("builder is nil")
	}
	// Should be the same instance on second call.
	b2, _ := o.ensurePersonaBuilder()
	if b != b2 {
		t.Error("ensurePersonaBuilder should return the same instance on second call")
	}
}
