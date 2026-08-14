package lifecycle

import "os"

// ProviderInfo describes one LLM provider and the env var that
// carries its API key.
type ProviderInfo struct {
	// ID is the provider's canonical identifier. Examples: "anthropic",
	// "openai", "google", "deepseek", "minimax", "minimax-cn",
	// "moonshot", "z-ai", "dashscope".
	ID string
	// EnvKey is the env var that carries the API key. The key is
	// considered "available" when os.Getenv(EnvKey) returns a non-empty
	// string.
	EnvKey string
	// Family is the model family. Matches HarnessNative.Family for
	// native providers. "multi" is reserved for the harness catalog
	// (Continue.dev, Cline) and is not a provider family.
	Family string
	// Models is the provider's model lineup, ordered from highest to
	// lowest rung. The first model is the recommended default.
	Models []string
	// DefaultRung is the rung of the first model in Models.
	DefaultRung HarnessRung
}

// providerCatalog is the curated list of providers. The 9 entries
// cover the 8 keys from v2.13.0 (ANTHROPIC / OPENAI / GEMINI / DEEPSEEK
// / MINIMAX / MOONSHOT / ZAI / DASHSCOPE) plus the China variant
// MINIMAX_API_KEY_CN (separate env var, separate endpoint, separate
// model namespace).
//
// Adding a new provider means adding one entry here. The catalog is
// iterated in order by DetectAvailableProviders so the order matters
// for the recommendation algorithm (first match wins).
var providerCatalog = []ProviderInfo{
	{
		ID:          "anthropic",
		EnvKey:      "ANTHROPIC_API_KEY",
		Family:      "anthropic",
		Models:      []string{"claude-sonnet-4.5", "claude-opus-4.5", "claude-haiku-4.5"},
		DefaultRung: RungMedium,
	},
	{
		ID:          "openai",
		EnvKey:      "OPENAI_API_KEY",
		Family:      "openai",
		Models:      []string{"gpt-5", "gpt-5.5", "gpt-5-mini"},
		DefaultRung: RungMedium,
	},
	{
		ID:          "google",
		EnvKey:      "GEMINI_API_KEY",
		Family:      "google",
		Models:      []string{"gemini-3.0-pro", "gemini-2.5-flash"},
		DefaultRung: RungMedium,
	},
	{
		ID:          "deepseek",
		EnvKey:      "DEEPSEEK_API_KEY",
		Family:      "deepseek",
		Models:      []string{"deepseek-v4", "deepseek-v3"},
		DefaultRung: RungMedium,
	},
	{
		ID:          "minimax",
		EnvKey:      "MINIMAX_API_KEY",
		Family:      "minimax",
		Models:      []string{"MiniMax-M3", "minimax/M3"},
		DefaultRung: RungHeavy,
	},
	{
		ID:          "minimax-cn",
		EnvKey:      "MINIMAX_API_KEY_CN",
		Family:      "minimax-cn",
		Models:      []string{"MiniMax-M3"},
		DefaultRung: RungHeavy,
	},
	{
		ID:          "moonshot",
		EnvKey:      "MOONSHOT_API_KEY",
		Family:      "moonshot",
		Models:      []string{"moonshot-v1-128k", "moonshot-v1-32k"},
		DefaultRung: RungMedium,
	},
	{
		ID:          "z-ai",
		EnvKey:      "ZAI_API_KEY",
		Family:      "z-ai",
		Models:      []string{"glm-4.6", "glm-4.5"},
		DefaultRung: RungMedium,
	},
	{
		ID:          "dashscope",
		EnvKey:      "DASHSCOPE_API_KEY",
		Family:      "dashscope",
		Models:      []string{"qwen3-max", "qwen3-coder-plus"},
		DefaultRung: RungMedium,
	},
}

// DetectAvailableProviders scans os.Getenv(EnvKey) for each provider
// and returns the subset with non-empty keys. Order is preserved
// (catalog order), which is deterministic for tests and for the
// recommendation algorithm.
//
// Returns an empty slice when no keys are configured. The orchestrator
// uses this to surface "no LLM configured" to the operator.
func DetectAvailableProviders() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(providerCatalog))
	for _, p := range providerCatalog {
		if os.Getenv(p.EnvKey) != "" {
			out = append(out, p)
		}
	}
	return out
}

// MatchNativeProvider returns the first provider whose Family matches
// the harness's native family. Returns zero-value ProviderInfo + false
// when no provider matches.
//
// "unknown" family is never matched; the recommendation falls back
// to the first available provider in that case.
func MatchNativeProvider(hn HarnessNative, available []ProviderInfo) (ProviderInfo, bool) {
	if hn.Family == "unknown" || hn.Family == "multi" {
		return ProviderInfo{}, false
	}
	for _, p := range available {
		if p.Family == hn.Family {
			return p, true
		}
	}
	return ProviderInfo{}, false
}
