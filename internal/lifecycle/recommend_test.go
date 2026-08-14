package lifecycle

import "testing"

func TestRecommend_NativeMatch(t *testing.T) {
	hn := LookupHarnessNative("opencode") // minimax
	available := []ProviderInfo{
		{ID: "anthropic", Family: "anthropic", Models: []string{"claude-sonnet-4.5"}, DefaultRung: RungMedium},
		{ID: "minimax", Family: "minimax", Models: []string{"MiniMax-M3"}, DefaultRung: RungHeavy},
		{ID: "deepseek", Family: "deepseek", Models: []string{"deepseek-v4"}, DefaultRung: RungMedium},
	}
	got := Recommend(hn, available)
	if got.ProviderID != "minimax" {
		t.Errorf("ProviderID = %q, want %q", got.ProviderID, "minimax")
	}
	if got.Model != "MiniMax-M3" {
		t.Errorf("Model = %q, want %q", got.Model, "MiniMax-M3")
	}
	if got.Rung != RungHeavy {
		t.Errorf("Rung = %q, want %q", got.Rung, RungHeavy)
	}
	if !got.MatchedNative {
		t.Errorf("MatchedNative = false, want true")
	}
	if len(got.AvailableProviders) != 3 {
		t.Errorf("AvailableProviders = %v, want 3 entries", got.AvailableProviders)
	}
}

func TestRecommend_FallbackToFirstAvailable(t *testing.T) {
	hn := LookupHarnessNative("opencode") // minimax
	available := []ProviderInfo{
		{ID: "anthropic", Family: "anthropic", Models: []string{"claude-sonnet-4.5"}, DefaultRung: RungMedium},
		{ID: "deepseek", Family: "deepseek", Models: []string{"deepseek-v4"}, DefaultRung: RungMedium},
	}
	got := Recommend(hn, available)
	if got.ProviderID != "anthropic" {
		t.Errorf("ProviderID = %q, want %q (first available)", got.ProviderID, "anthropic")
	}
	if got.MatchedNative {
		t.Errorf("MatchedNative = true, want false (no minimax provider)")
	}
}

func TestRecommend_NoProviderAvailable(t *testing.T) {
	hn := LookupHarnessNative("opencode")
	available := []ProviderInfo{}
	got := Recommend(hn, available)
	if got.ProviderID != "" {
		t.Errorf("ProviderID = %q, want empty", got.ProviderID)
	}
	if got.Model != "" {
		t.Errorf("Model = %q, want empty", got.Model)
	}
	if got.MatchedNative {
		t.Errorf("MatchedNative = true, want false")
	}
	if got.AvailableProviders == nil {
		t.Errorf("AvailableProviders = nil, want empty slice")
	}
	if len(got.AvailableProviders) != 0 {
		t.Errorf("AvailableProviders len = %d, want 0", len(got.AvailableProviders))
	}
}

func TestRecommend_MultiProvider_PrefersNative(t *testing.T) {
	// Multiprovider: harness is opencode (minimax), and
	// anthropic + minimax + deepseek are all available. The
	// recommendation should pick minimax (native).
	hn := LookupHarnessNative("opencode")
	available := []ProviderInfo{
		{ID: "anthropic", Family: "anthropic", Models: []string{"claude-sonnet-4.5"}, DefaultRung: RungMedium},
		{ID: "minimax", Family: "minimax", Models: []string{"MiniMax-M3"}, DefaultRung: RungHeavy},
		{ID: "deepseek", Family: "deepseek", Models: []string{"deepseek-v4"}, DefaultRung: RungMedium},
		{ID: "google", Family: "google", Models: []string{"gemini-3.0-pro"}, DefaultRung: RungMedium},
	}
	got := Recommend(hn, available)
	if got.ProviderID != "minimax" {
		t.Errorf("ProviderID = %q, want %q", got.ProviderID, "minimax")
	}
	if !got.MatchedNative {
		t.Errorf("MatchedNative = false, want true")
	}
}

func TestRecommend_UnknownHarness_DefaultsToFirst(t *testing.T) {
	hn := LookupHarnessNative("unknown")
	available := []ProviderInfo{
		{ID: "deepseek", Family: "deepseek", Models: []string{"deepseek-v4"}, DefaultRung: RungMedium},
		{ID: "anthropic", Family: "anthropic", Models: []string{"claude-sonnet-4.5"}, DefaultRung: RungMedium},
	}
	got := Recommend(hn, available)
	if got.ProviderID != "deepseek" {
		t.Errorf("ProviderID = %q, want %q (first available)", got.ProviderID, "deepseek")
	}
	if got.MatchedNative {
		t.Errorf("MatchedNative = true, want false (unknown harness has no native family)")
	}
}

func TestRecommend_MultiHarness_FallsToFirst(t *testing.T) {
	// continue and cline are multi-family. The recommendation should
	// fall through to the first available provider.
	hn := LookupHarnessNative("continue")
	available := []ProviderInfo{
		{ID: "anthropic", Family: "anthropic", Models: []string{"claude-sonnet-4.5"}, DefaultRung: RungMedium},
		{ID: "deepseek", Family: "deepseek", Models: []string{"deepseek-v4"}, DefaultRung: RungMedium},
	}
	got := Recommend(hn, available)
	if got.ProviderID != "anthropic" {
		t.Errorf("ProviderID = %q, want %q", got.ProviderID, "anthropic")
	}
	if got.MatchedNative {
		t.Errorf("MatchedNative = true, want false (multi-family harness has no native match)")
	}
}
