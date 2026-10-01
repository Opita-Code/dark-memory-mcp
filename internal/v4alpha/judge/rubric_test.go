// Tests for rubric.go (Rubric + Persona registries + scoring).
package judge

import (
	"strings"
	"testing"
)

func TestNewRubricRegistry_HasAllSeven(t *testing.T) {
	r := NewRubricRegistry()
	expected := []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}
	got := r.List()
	if !stringSliceEq(got, expected) {
		t.Errorf("List() = %v; want %v", got, expected)
	}
}

func TestRubricRegistry_Get(t *testing.T) {
	r := NewRubricRegistry()

	t.Run("known vibe case", func(t *testing.T) {
		rub, err := r.Get("C1")
		if err != nil {
			t.Fatalf("Get(C1) returned error: %v", err)
		}
		if rub.VibeCase != "C1" {
			t.Errorf("VibeCase = %q; want C1", rub.VibeCase)
		}
		if rub.PersonaID != "judge-logical" {
			t.Errorf("PersonaID = %q; want judge-logical", rub.PersonaID)
		}
		if len(rub.Criteria) != 5 {
			t.Errorf("Criteria len = %d; want 5", len(rub.Criteria))
		}
		if rub.Version == "" {
			t.Error("Version empty; expected sha256[:16]")
		}
		if len(rub.Version) != 16 {
			t.Errorf("Version len = %d; want 16 (sha256[:16])", len(rub.Version))
		}
	})

	t.Run("unknown vibe case", func(t *testing.T) {
		_, err := r.Get("C99")
		if err == nil {
			t.Fatal("expected error for unknown vibe case")
		}
		if !strings.Contains(err.Error(), "C99") {
			t.Errorf("error message should contain 'C99': %v", err)
		}
		if !strings.Contains(err.Error(), ErrRubricNotFound.Error()) {
			t.Errorf("error should wrap ErrRubricNotFound: %v", err)
		}
	})

	t.Run("returned rubric is a copy", func(t *testing.T) {
		rub1, _ := r.Get("C1")
		rub1.Criteria[0].Weight = 999.0
		rub2, _ := r.Get("C1")
		if rub2.Criteria[0].Weight == 999.0 {
			t.Error("Registry returned a shared pointer; mutations leaked")
		}
	})
}

func TestRubricRegistry_Register_Validation(t *testing.T) {
	r := &defaultRubricRegistry{byCase: make(map[string]*Rubric)}

	t.Run("happy path", func(t *testing.T) {
		err := r.Register(&Rubric{
			VibeCase: "C8", PersonaID: "judge-logical",
			AlignedThreshold: 0.85, DriftThreshold: 0.50,
			Criteria: []CriterionDef{
				{Name: "a", Weight: 0.4},
				{Name: "b", Weight: 0.6},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("sum != 1.0", func(t *testing.T) {
		err := r.Register(&Rubric{
			VibeCase: "C9", PersonaID: "judge-logical",
			AlignedThreshold: 0.85, DriftThreshold: 0.50,
			Criteria: []CriterionDef{
				{Name: "a", Weight: 0.5},
				{Name: "b", Weight: 0.6}, // sum=1.1
			},
		})
		if err == nil {
			t.Fatal("expected error for sum != 1.0")
		}
	})

	t.Run("duplicate criterion name", func(t *testing.T) {
		err := r.Register(&Rubric{
			VibeCase: "C10", PersonaID: "judge-logical",
			AlignedThreshold: 0.85, DriftThreshold: 0.50,
			Criteria: []CriterionDef{
				{Name: "x", Weight: 0.5},
				{Name: "x", Weight: 0.5}, // duplicate
			},
		})
		if err == nil {
			t.Fatal("expected error for duplicate name")
		}
	})

	t.Run("duplicate vibe case", func(t *testing.T) {
		err := r.Register(&Rubric{
			VibeCase: "C8", PersonaID: "judge-logical",
			AlignedThreshold: 0.85, DriftThreshold: 0.50,
			Criteria: []CriterionDef{
				{Name: "a", Weight: 0.5},
				{Name: "b", Weight: 0.5},
			},
		})
		if err == nil {
			t.Fatal("expected error for duplicate case")
		}
	})

	t.Run("aligned <= drift threshold", func(t *testing.T) {
		err := r.Register(&Rubric{
			VibeCase: "C11", PersonaID: "judge-logical",
			AlignedThreshold: 0.50, DriftThreshold: 0.85, // inverted
			Criteria: []CriterionDef{
				{Name: "a", Weight: 1.0},
			},
		})
		if err == nil {
			t.Fatal("expected error for inverted thresholds")
		}
	})

	t.Run("nil rubric", func(t *testing.T) {
		err := r.Register(nil)
		if err == nil {
			t.Fatal("expected error for nil rubric")
		}
	})

	t.Run("empty VibeCase", func(t *testing.T) {
		err := r.Register(&Rubric{PersonaID: "x"})
		if err == nil {
			t.Fatal("expected error for missing VibeCase")
		}
	})
}

func TestNewPersonaRegistry_HasAllFourteen(t *testing.T) {
	r := NewPersonaRegistry()
	// Phase 6 alpha.18.1: added judge-decision + judge-research
	// (canonical C3 + C4 mapping). 13 total.
	// Phase 7 alpha.19: added judge-delegator (EXTRACT step in
	// delegate_intent). 14 total. Breaks Phase 6 §6.1 canon.
	want := []string{
		"judge-compositional", "judge-coverage", "judge-cross-modal",
		"judge-decision", "judge-delegator", "judge-evidential",
		"judge-logical", "judge-mutation", "judge-opinion",
		"judge-pipeline", "judge-research", "judge-resilience",
		"judge-security", "judge-visual",
	}
	if !stringSliceEq(r.List(), want) {
		t.Errorf("List() = %v; want %v", r.List(), want)
	}
}

func TestPersonaRegistry_Get(t *testing.T) {
	r := NewPersonaRegistry()
	t.Run("known id", func(t *testing.T) {
		p, err := r.Get("judge-cross-modal")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ProviderHint != "minimax" {
			t.Errorf("ProviderHint = %q; want minimax", p.ProviderHint)
		}
	})
	t.Run("unknown id", func(t *testing.T) {
		_, err := r.Get("judge-unknown")
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), ErrPersonaNotRegistered.Error()) {
			t.Errorf("should wrap ErrPersonaNotRegistered: %v", err)
		}
	})
}

func TestComputeWeightedScore(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		score := ComputeWeightedScore([]Criterion{
			{Name: "a", Weight: 0.5, Score: 1.0},
			{Name: "b", Weight: 0.5, Score: 0.0},
		})
		if score != 0.5 {
			t.Errorf("score = %f; want 0.5", score)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if score := ComputeWeightedScore(nil); score != 0.0 {
			t.Errorf("score = %f; want 0.0", score)
		}
	})
	t.Run("all perfect", func(t *testing.T) {
		score := ComputeWeightedScore([]Criterion{
			{Name: "a", Weight: 0.35, Score: 1.0},
			{Name: "b", Weight: 0.25, Score: 1.0},
			{Name: "c", Weight: 0.20, Score: 1.0},
			{Name: "d", Weight: 0.10, Score: 1.0},
			{Name: "e", Weight: 0.10, Score: 1.0},
		})
		if score != 1.0 {
			t.Errorf("score = %f; want 1.0", score)
		}
	})
}

func TestThresholdToVerdict(t *testing.T) {
	rub := &Rubric{
		VibeCase:         "test",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
	}
	cases := []struct {
		score    float64
		expected string
	}{
		{1.0, VerdictAligned},
		{0.85, VerdictAligned}, // inclusive lower bound
		{0.84, VerdictNeedsHuman},
		{0.50, VerdictNeedsHuman}, // inclusive lower bound
		{0.49, VerdictDriftDetected},
		{0.0, VerdictDriftDetected},
	}
	for _, tc := range cases {
		got := ThresholdToVerdict(tc.score, rub)
		if got != tc.expected {
			t.Errorf("ThresholdToVerdict(%f) = %s; want %s", tc.score, got, tc.expected)
		}
	}
}

func TestApplyPerCriterionOverride(t *testing.T) {
	t.Run("aligned override to needs_human", func(t *testing.T) {
		// C1 example: correctness=0.0 (weight 0.35 >= 0.20) but
		// tests=1.0, security=1.0, idiomatic=1.0, docs=1.0.
		// Sum=0.65 -> baseVerdict=needs_human. Override fires
		// because correctness (weight 0.35 >= 0.20, score 0.0 < 0.30).
		criteria := []Criterion{
			{Name: "correctness", Weight: 0.35, Score: 0.0},
			{Name: "tests", Weight: 0.25, Score: 1.0},
			{Name: "security", Weight: 0.20, Score: 1.0},
			{Name: "idiomatic", Weight: 0.10, Score: 1.0},
			{Name: "docs", Weight: 0.10, Score: 1.0},
		}
		v, overrideBy := ApplyPerCriterionOverride(criteria, VerdictAligned)
		if v != VerdictNeedsHuman {
			t.Errorf("verdict = %s; want needs_human", v)
		}
		if overrideBy == "" {
			t.Error("overrideBy empty; expected correctness (weight=0.35, score=0.00)")
		}
	})

	t.Run("low-weight criterion does NOT trigger override", func(t *testing.T) {
		// idiomatic has weight 0.10 < 0.20, so even with score 0.0,
		// no override.
		criteria := []Criterion{
			{Name: "idiomatic", Weight: 0.10, Score: 0.0},
			{Name: "a", Weight: 0.45, Score: 1.0},
			{Name: "b", Weight: 0.45, Score: 1.0},
		}
		v, overrideBy := ApplyPerCriterionOverride(criteria, VerdictAligned)
		if v != VerdictAligned {
			t.Errorf("verdict = %s; want aligned", v)
		}
		if overrideBy != "" {
			t.Errorf("overrideBy = %q; want empty", overrideBy)
		}
	})

	t.Run("score 0.30 is NOT low enough to trigger", func(t *testing.T) {
		// score < 0.30 is required. 0.30 doesn't trigger.
		criteria := []Criterion{
			{Name: "x", Weight: 0.50, Score: 0.30},
			{Name: "y", Weight: 0.50, Score: 1.0},
		}
		_, overrideBy := ApplyPerCriterionOverride(criteria, VerdictAligned)
		if overrideBy != "" {
			t.Errorf("overrideBy = %q; want empty (score 0.30 is NOT < 0.30)", overrideBy)
		}
	})
}

func TestDefaultRubrics_WeightSums(t *testing.T) {
	// INV-7-like invariant: every default rubric's criteria weights
	// must sum to exactly 1.0 (within float tolerance).
	r := NewRubricRegistry()
	for _, vc := range r.List() {
		rub, err := r.Get(vc)
		if err != nil {
			t.Fatalf("%s: %v", vc, err)
		}
		var sum float64
		for _, c := range rub.Criteria {
			sum += c.Weight
		}
		if diff := sum - 1.0; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("rubric %s weights sum to %f, want 1.0", vc, sum)
		}
	}
}

func TestDefaultRubrics_PersonaResolvable(t *testing.T) {
	// Every rubric's default PersonaID must resolve to a registered
	// persona. Caught at init by the package init check, but tested
	// explicitly here for documentation.
	rr := NewRubricRegistry()
	pr := NewPersonaRegistry()
	for _, vc := range rr.List() {
		rub, _ := rr.Get(vc)
		if _, err := pr.Get(rub.PersonaID); err != nil {
			t.Errorf("rubric %s -> persona %q not registered: %v", vc, rub.PersonaID, err)
		}
	}
}

func TestRubricVersionHash_DeterministicAndStable(t *testing.T) {
	rub := &Rubric{
		VibeCase:         "C1",
		PersonaID:        "judge-logical",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "a", Weight: 0.5},
			{Name: "b", Weight: 0.5},
		},
	}
	h1 := rubricVersionHash(rub)
	h2 := rubricVersionHash(rub)
	if h1 != h2 {
		t.Errorf("hash not deterministic: %s vs %s", h1, h2)
	}
	if len(h1) != 16 {
		t.Errorf("hash len = %d; want 16 (sha256[:16])", len(h1))
	}

	// Different criteria order produces same hash (sort-stable).
	rub2 := &Rubric{
		VibeCase:         "C1",
		PersonaID:        "judge-logical",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "b", Weight: 0.5},
			{Name: "a", Weight: 0.5},
		},
	}
	if rubricVersionHash(rub2) != h1 {
		t.Error("hash not stable under criteria reordering")
	}

	// Different weight produces different hash.
	rub3 := &Rubric{
		VibeCase:         "C1",
		PersonaID:        "judge-logical",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "a", Weight: 0.4},
			{Name: "b", Weight: 0.6},
		},
	}
	if rubricVersionHash(rub3) == h1 {
		t.Error("hash not sensitive to weight changes")
	}
}

func TestNewPersonaRegistry_RegisterValidation(t *testing.T) {
	r := &defaultPersonaRegistry{byID: make(map[string]*Persona)}
	t.Run("happy path", func(t *testing.T) {
		err := r.Register(&Persona{ID: "judge-test", DisplayName: "Test Judge"})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	})
	t.Run("nil persona", func(t *testing.T) {
		if err := r.Register(nil); err == nil {
			t.Fatal("expected error for nil")
		}
	})
	t.Run("empty ID", func(t *testing.T) {
		if err := r.Register(&Persona{DisplayName: "x"}); err == nil {
			t.Fatal("expected error for missing ID")
		}
	})
	t.Run("missing display name", func(t *testing.T) {
		if err := r.Register(&Persona{ID: "x"}); err == nil {
			t.Fatal("expected error for missing DisplayName")
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		err := r.Register(&Persona{ID: "judge-test", DisplayName: "Test"})
		if err == nil {
			t.Fatal("expected error for duplicate")
		}
	})
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
