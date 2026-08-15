package lifecycle

import (
	"os"
	"testing"
)

// clearAllKnownEnvKeys unsets every env key from the provider catalog
// for the duration of the test. Tests that need a clean slate should
// call this. The previous values are restored on cleanup.
func clearAllKnownEnvKeys(t *testing.T) {
	t.Helper()
	keys := make([]string, 0, len(providerCatalog))
	for _, p := range providerCatalog {
		keys = append(keys, p.EnvKey)
	}
	clearEnv(t, keys...)
}

// clearEnv unsets the given env keys for the duration of the test,
// restoring the previous values on cleanup.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		old, wasSet := os.LookupEnv(k)
		t.Cleanup(func() {
			if wasSet {
				_ = os.Setenv(k, old)
			} else {
				_ = os.Unsetenv(k)
			}
		})
		_ = os.Unsetenv(k)
	}
}

// setEnv is a tiny wrapper around os.Setenv that ignores the error
// (cannot fail in normal use).
func setEnv(k, v string) {
	_ = os.Setenv(k, v)
}

// unsetEnv is a tiny wrapper around os.Unsetenv that ignores the
// error.
func unsetEnv(k string) {
	_ = os.Unsetenv(k)
}

func TestDetectAvailableProviders_NoKeys(t *testing.T) {
	clearAllKnownEnvKeys(t)

	got := DetectAvailableProviders()
	if len(got) != 0 {
		t.Errorf("DetectAvailableProviders() with no keys = %d entries, want 0", len(got))
	}
}

func TestDetectAvailableProviders_OneKey(t *testing.T) {
	clearAllKnownEnvKeys(t)
	setEnv("DEEPSEEK_API_KEY", "sk-test-deepseek")

	got := DetectAvailableProviders()
	if len(got) != 1 {
		t.Fatalf("DetectAvailableProviders() = %d entries, want 1", len(got))
	}
	if got[0].ID != "deepseek" {
		t.Errorf("got[0].ID = %q, want %q", got[0].ID, "deepseek")
	}
	if got[0].Family != "deepseek" {
		t.Errorf("got[0].Family = %q, want %q", got[0].Family, "deepseek")
	}
}

func TestDetectAvailableProviders_EmptyValueIsNotAvailable(t *testing.T) {
	clearAllKnownEnvKeys(t)
	setEnv("DEEPSEEK_API_KEY", "")

	got := DetectAvailableProviders()
	for _, p := range got {
		if p.EnvKey == "DEEPSEEK_API_KEY" {
			t.Errorf("DEEPSEEK_API_KEY with empty value should NOT be detected as available")
		}
	}
}

func TestDetectAvailableProviders_PreservesCatalogOrder(t *testing.T) {
	clearAllKnownEnvKeys(t)
	// Set keys in reverse-catalog order to detect that the result
	// follows the catalog order, not the env-var-set order.
	setEnv("DASHSCOPE_API_KEY", "k")
	setEnv("ZAI_API_KEY", "k")
	setEnv("MOONSHOT_API_KEY", "k")
	setEnv("DEEPSEEK_API_KEY", "k")
	setEnv("OPENAI_API_KEY", "k")
	setEnv("ANTHROPIC_API_KEY", "k")

	got := DetectAvailableProviders()
	wantOrder := []string{"anthropic", "openai", "deepseek", "zhipu", "moonshot", "qwen"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d entries, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestMatchNativeProvider_Hit(t *testing.T) {
	hn := LookupHarnessNative("opencode") // minimax
	available := []ProviderInfo{
		{ID: "anthropic", Family: "anthropic", Models: []string{"claude-sonnet-4.5"}},
		{ID: "minimax", Family: "minimax", Models: []string{"MiniMax-M3"}},
		{ID: "deepseek", Family: "deepseek", Models: []string{"deepseek-v4"}},
	}
	got, ok := MatchNativeProvider(hn, available)
	if !ok {
		t.Fatalf("MatchNativeProvider returned ok=false, want true")
	}
	if got.ID != "minimax" {
		t.Errorf("got.ID = %q, want %q", got.ID, "minimax")
	}
}

func TestMatchNativeProvider_Miss(t *testing.T) {
	hn := LookupHarnessNative("opencode") // minimax
	available := []ProviderInfo{
		{ID: "anthropic", Family: "anthropic"},
		{ID: "deepseek", Family: "deepseek"},
	}
	got, ok := MatchNativeProvider(hn, available)
	if ok {
		t.Errorf("MatchNativeProvider returned ok=true with mismatched providers")
	}
	if got.ID != "" {
		t.Errorf("got.ID = %q, want empty", got.ID)
	}
}

func TestMatchNativeProvider_UnknownFamily(t *testing.T) {
	// "unknown" family should never match a provider.
	hn := LookupHarnessNative("unknown")
	available := []ProviderInfo{
		{ID: "minimax", Family: "minimax"},
		{ID: "deepseek", Family: "deepseek"},
	}
	_, ok := MatchNativeProvider(hn, available)
	if ok {
		t.Errorf("MatchNativeProvider should return false for unknown family")
	}
}

func TestMatchNativeProvider_MultiFamilyNeverMatches(t *testing.T) {
	// "multi" family (continue, cline) should never match a provider
	// because the operator configures models per harness.
	hn := LookupHarnessNative("continue")
	available := []ProviderInfo{
		{ID: "anthropic", Family: "anthropic"},
		{ID: "minimax", Family: "minimax"},
	}
	_, ok := MatchNativeProvider(hn, available)
	if ok {
		t.Errorf("MatchNativeProvider should return false for multi family")
	}
}
