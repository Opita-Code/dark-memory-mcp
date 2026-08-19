package nli

import (
	"errors"
	"testing"
)

func TestLabel_Valid(t *testing.T) {
	t.Parallel()
	for _, l := range []Label{LabelEntailment, LabelContradiction, LabelNeutral} {
		if !l.Valid() {
			t.Errorf("expected %q to be valid", l)
		}
	}
	for _, l := range []Label{"", "ENTAILMENT", "supported", "EN", "x"} {
		if l.Valid() {
			t.Errorf("expected %q to be invalid", l)
		}
	}
}

func TestScore_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    Score
		want bool
	}{
		{
			name: "all valid",
			s:    Score{Label: LabelEntailment, Confidence: 0.5, ProviderID: "x", LatencyMS: 1},
			want: true,
		},
		{
			name: "boundary confidence 0",
			s:    Score{Label: LabelEntailment, Confidence: 0, ProviderID: "x", LatencyMS: 0},
			want: true,
		},
		{
			name: "boundary confidence 1",
			s:    Score{Label: LabelEntailment, Confidence: 1, ProviderID: "x", LatencyMS: 0},
			want: true,
		},
		{
			name: "invalid label",
			s:    Score{Label: "x", Confidence: 0.5, ProviderID: "x"},
			want: false,
		},
		{
			name: "confidence out of range high",
			s:    Score{Label: LabelEntailment, Confidence: 1.0001, ProviderID: "x"},
			want: false,
		},
		{
			name: "confidence out of range low",
			s:    Score{Label: LabelEntailment, Confidence: -0.001, ProviderID: "x"},
			want: false,
		},
		{
			name: "empty ProviderID",
			s:    Score{Label: LabelEntailment, Confidence: 0.5, ProviderID: ""},
			want: false,
		},
		{
			name: "negative LatencyMS",
			s:    Score{Label: LabelEntailment, Confidence: 0.5, ProviderID: "x", LatencyMS: -1},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.s.Valid(); got != tt.want {
				t.Errorf("Valid()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_DefaultsFor(t *testing.T) {
	t.Parallel()
	cfg := Config{} // zero
	got := cfg.DefaultsFor()
	if got.LatencyBudgetMS != DefaultLatencyBudgetMS {
		t.Errorf("LatencyBudgetMS=%d, want %d", got.LatencyBudgetMS, DefaultLatencyBudgetMS)
	}
	if got.MaxPremiseBytes != DefaultMaxPremiseBytes {
		t.Errorf("MaxPremiseBytes=%d, want %d", got.MaxPremiseBytes, DefaultMaxPremiseBytes)
	}
	if got.MaxHypothesisBytes != DefaultMaxHypothesisBytes {
		t.Errorf("MaxHypothesisBytes=%d, want %d", got.MaxHypothesisBytes, DefaultMaxHypothesisBytes)
	}
	if got.Primary.TimeoutMS != DefaultTimeoutMS {
		t.Errorf("Primary.TimeoutMS=%d, want %d", got.Primary.TimeoutMS, DefaultTimeoutMS)
	}
	if got.Fallback.TimeoutMS != DefaultTimeoutMS {
		t.Errorf("Fallback.TimeoutMS=%d, want %d", got.Fallback.TimeoutMS, DefaultTimeoutMS)
	}
}

func TestConfig_DefaultsFor_PreservesExplicit(t *testing.T) {
	t.Parallel()
	cfg := Config{
		LatencyBudgetMS:     5000,
		MaxPremiseBytes:     1024,
		MaxHypothesisBytes:  512,
	}
	cfg.Primary.TimeoutMS = 2000
	got := cfg.DefaultsFor()
	if got.LatencyBudgetMS != 5000 || got.MaxPremiseBytes != 1024 || got.MaxHypothesisBytes != 512 {
		t.Errorf("explicit tunables were overwritten: %+v", got)
	}
	if got.Primary.TimeoutMS != 2000 {
		t.Errorf("Primary.TimeoutMS overwritten: %d", got.Primary.TimeoutMS)
	}
}

func TestErrors_Seal(t *testing.T) {
	t.Parallel()
	// Sealed: errors must be distinct.
	all := []error{
		ErrProviderUnavailable,
		ErrProviderTimeout,
		ErrProviderRateLimited,
		ErrProviderBadResponse,
		ErrInputTooLarge,
		ErrInputEmpty,
		ErrInvalidConfig,
		ErrNoProvider,
	}
	for i, a := range all {
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("errors overlap: %v is %v", a, b)
			}
		}
	}
}