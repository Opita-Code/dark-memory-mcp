package orchestration

import (
	"strings"
	"testing"
)

// TestProviderCatalog_Completeness guards the provider catalog against
// accidental truncation: all 8 providers from the 2026-08-10 primary-
// source verification must be present with non-empty endpoints.
func TestProviderCatalog_Completeness(t *testing.T) {
	expected := []string{
		"anthropic", "openai", "google",
		"deepseek", "minimax", "zhipu", "moonshot", "qwen",
	}
	got := catalogProviderIDs()
	if len(got) != len(expected) {
		t.Fatalf("catalog has %d providers, want %d (%s)", len(got), len(expected), strings.Join(got, ", "))
	}
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, id := range expected {
		if !seen[id] {
			t.Errorf("catalog missing provider %q", id)
		}
	}
}

// TestProviderCatalog_FieldsNoEmpty verifies every catalog entry has a
// non-empty EnvKey, BaseURL, and DefaultModel (the client depends on
// these — an empty value would silently misroute).
func TestProviderCatalog_FieldsNoEmpty(t *testing.T) {
	for i := range providerCatalog {
		p := &providerCatalog[i]
		if p.ID == "" {
			t.Fatalf("entry %d has empty ID", i)
		}
		if p.EnvKey == "" {
			t.Errorf("provider %s: EnvKey empty", p.ID)
		}
		if p.BaseURL == "" {
			t.Errorf("provider %s: BaseURL empty", p.ID)
		}
		if p.DefaultModel == "" {
			t.Errorf("provider %s: DefaultModel empty", p.ID)
		}
		if p.Dialect != DialectAnthropic && p.Dialect != DialectOpenAI {
			t.Errorf("provider %s: dialect %q invalid", p.ID, p.Dialect)
		}
	}
}

// TestProviderCatalog_EndpointsVerified pins the exact endpoints from
// the 2026-08-10 primary-source research (row 587). If a vendor
// changes a base URL, this test forces a conscious decision.
func TestProviderCatalog_EndpointsVerified(t *testing.T) {
	cases := []struct {
		id, baseURL, anthropicURL, envKey, defaultModel string
		dialect                                          ProviderDialect
		region                                           ProviderRegion
	}{
		{"anthropic", "https://api.anthropic.com", "", "ANTHROPIC_API_KEY", "claude-sonnet-4-5", DialectAnthropic, RegionUS},
		{"openai", "https://api.openai.com/v1", "", "OPENAI_API_KEY", "gpt-5", DialectOpenAI, RegionUS},
		{"google", "https://generativelanguage.googleapis.com/v1beta/openai/", "", "GEMINI_API_KEY", "gemini-3.6-flash", DialectOpenAI, RegionUS},
		{"deepseek", "https://api.deepseek.com", "https://api.deepseek.com/anthropic", "DEEPSEEK_API_KEY", "deepseek-v4-flash", DialectOpenAI, RegionChina},
		{"minimax", "https://api.minimaxi.com/v1", "https://api.minimaxi.com/anthropic", "MINIMAX_API_KEY", "MiniMax-M3", DialectOpenAI, RegionChina},
		{"zhipu", "https://open.bigmodel.cn/api/paas/v4/", "", "ZAI_API_KEY", "glm-5.2", DialectOpenAI, RegionChina},
		{"moonshot", "https://api.moonshot.cn/v1", "", "MOONSHOT_API_KEY", "kimi-k3", DialectOpenAI, RegionChina},
		{"qwen", "https://dashscope.aliyuncs.com/compatible-mode/v1", "https://dashscope.aliyuncs.com/apps/anthropic", "DASHSCOPE_API_KEY", "qwen3.8-max", DialectOpenAI, RegionChina},
	}
	for _, c := range cases {
		p := providerSpecByID(c.id)
		if p == nil {
			t.Errorf("provider %q missing from catalog", c.id)
			continue
		}
		if p.BaseURL != c.baseURL {
			t.Errorf("provider %s: BaseURL = %q, want %q", c.id, p.BaseURL, c.baseURL)
		}
		if p.AnthropicBaseURL != c.anthropicURL {
			t.Errorf("provider %s: AnthropicBaseURL = %q, want %q", c.id, p.AnthropicBaseURL, c.anthropicURL)
		}
		if p.EnvKey != c.envKey {
			t.Errorf("provider %s: EnvKey = %q, want %q", c.id, p.EnvKey, c.envKey)
		}
		if p.DefaultModel != c.defaultModel {
			t.Errorf("provider %s: DefaultModel = %q, want %q", c.id, p.DefaultModel, c.defaultModel)
		}
		if p.Dialect != c.dialect {
			t.Errorf("provider %s: Dialect = %q, want %q", c.id, p.Dialect, c.dialect)
		}
		if p.Region != c.region {
			t.Errorf("provider %s: Region = %q, want %q", c.id, p.Region, c.region)
		}
	}
}

// TestProviderDialect_AnthropicOverride verifies DARK_JUDGE_DIALECT
// switches dual-dialect providers to Anthropic Messages.
func TestProviderDialect_AnthropicOverride(t *testing.T) {
	deepseek := providerSpecByID("deepseek")
	if deepseek == nil {
		t.Fatal("deepseek missing")
	}
	if got := providerDialect(deepseek); got != DialectOpenAI {
		t.Errorf("deepseek default dialect = %q, want openai", got)
	}
	t.Setenv("DARK_JUDGE_DIALECT", "anthropic")
	if got := providerDialect(deepseek); got != DialectAnthropic {
		t.Errorf("deepseek with DARK_JUDGE_DIALECT=anthropic = %q, want anthropic", got)
	}
	// zhipu has no AnthropicBaseURL — the override must NOT apply.
	zhipu := providerSpecByID("zhipu")
	if zhipu == nil {
		t.Fatal("zhipu missing")
	}
	if got := providerDialect(zhipu); got != DialectOpenAI {
		t.Errorf("zhipu with override (no anthropic URL) = %q, want openai", got)
	}
}
