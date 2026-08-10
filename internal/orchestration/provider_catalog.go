// ProviderCatalog is the data-driven registry of LLM providers that
// dark-memory-mcp can use as judge. Every endpoint in this table was
// verified against the provider's PRIMARY documentation on 2026-08-10
// (see agent_memory row 587 for the source list). Nothing here is
// invented — if a provider is not in this table, it is not supported.
//
// The catalog is the SINGLE source of truth for:
//   - which env var carries the API key (EnvKey)
//   - which HTTP dialect to use (Dialect: anthropic | openai)
//   - which base URLs to hit (BaseURL for the primary dialect,
//     AnthropicBaseURL for providers that also speak Anthropic
//     Messages)
//   - the default model to request when the operator does not pin
//     one via DARK_JUDGE_MODEL_<PROVIDER>
//
// Dialect "anthropic" = Anthropic Messages API (POST /v1/messages,
// x-api-key + anthropic-version headers).
// Dialect "openai" = OpenAI Chat Completions (POST /chat/completions,
// Authorization: Bearer, choices[0].message.content).
package orchestration

import "os"

// ProviderDialect is the wire dialect a provider speaks.
type ProviderDialect string

const (
	// DialectAnthropic = Anthropic Messages API (v1/messages).
	DialectAnthropic ProviderDialect = "anthropic"
	// DialectOpenAI = OpenAI Chat Completions (chat/completions).
	DialectOpenAI ProviderDialect = "openai"
)

// ProviderRegion is the geo-classification of a provider.
type ProviderRegion string

const (
	// RegionUS = US/EU primary.
	RegionUS ProviderRegion = "us"
	// RegionChina = PRC primary.
	RegionChina ProviderRegion = "china"
)

// ProviderSpec describes one supported LLM provider.
type ProviderSpec struct {
	// ID is the canonical provider id used in ProviderFor(), audit
	// rows, and DARK_JUDGE_PROVIDER.
	ID string
	// Region classifies the provider as us | china.
	Region ProviderRegion
	// Dialect is the wire dialect for BaseURL.
	Dialect ProviderDialect
	// BaseURL is the base URL for the primary dialect. For dialect
	// openai it is the full OpenAI-compatible base (the client appends
	// /chat/completions); for dialect anthropic it is the base the
	// client appends /v1/messages to.
	BaseURL string
	// AnthropicBaseURL is optional; set for providers that ALSO speak
	// Anthropic Messages (deepseek, minimax, qwen). When the operator
	// pins DARK_JUDGE_DIALECT=anthropic for such a provider, this URL
	// is used instead of BaseURL.
	AnthropicBaseURL string
	// EnvKey is the environment variable that carries the API key.
	EnvKey string
	// DefaultModel is the model requested when the operator does not
	// set DARK_JUDGE_MODEL_<PROVIDER> and RecommendedModel() has no
	// eval_type-specific pick.
	DefaultModel string
}

// providerCatalog is the authoritative list. Order matters for
// DARK_JUDGE_PROVIDER resolution: the first provider whose EnvKey is
// set wins (unless the operator pins DARK_JUDGE_PROVIDER explicitly).
//
// Sources (fetched 2026-08-10, primary docs):
//
//	anthropic  -> https://docs.anthropic.com/en/api/errors  (Messages API)
//	openai     -> https://developers.openai.com/api/docs/guides/error-codes
//	google     -> https://ai.google.dev/gemini-api/docs/openai  (OpenAI-compat)
//	deepseek   -> https://api-docs.deepseek.com/  (OpenAI + /anthropic)
//	minimax    -> https://platform.minimax.io/docs/api-reference/text-openai-api
//	zhipu      -> https://docs.bigmodel.cn/cn/guide/develop/openai/introduction
//	moonshot   -> https://platform.moonshot.cn/docs/guide/start-using-kimi-api
//	qwen       -> https://help.aliyun.com/zh/model-studio/getting-started/models
var providerCatalog = []ProviderSpec{
	{
		ID:          "anthropic",
		Region:      RegionUS,
		Dialect:     DialectAnthropic,
		BaseURL:     "https://api.anthropic.com",
		EnvKey:      "ANTHROPIC_API_KEY",
		DefaultModel: "claude-sonnet-4-5",
	},
	{
		ID:          "openai",
		Region:      RegionUS,
		Dialect:     DialectOpenAI,
		BaseURL:     "https://api.openai.com/v1",
		EnvKey:      "OPENAI_API_KEY",
		DefaultModel: "gpt-5",
	},
	{
		ID:          "google",
		Region:      RegionUS,
		Dialect:     DialectOpenAI,
		BaseURL:     "https://generativelanguage.googleapis.com/v1beta/openai/",
		EnvKey:      "GEMINI_API_KEY",
		DefaultModel: "gemini-3.6-flash",
	},
	{
		ID:               "deepseek",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://api.deepseek.com",
		AnthropicBaseURL: "https://api.deepseek.com/anthropic",
		EnvKey:           "DEEPSEEK_API_KEY",
		DefaultModel:     "deepseek-v4-flash",
	},
	{
		ID:               "minimax",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://api.minimax.io/v1",
		AnthropicBaseURL: "https://api.minimax.io/anthropic",
		EnvKey:           "MINIMAX_API_KEY",
		DefaultModel:     "MiniMax-M3",
	},
	{
		ID:           "zhipu",
		Region:       RegionChina,
		Dialect:      DialectOpenAI,
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4/",
		EnvKey:       "ZAI_API_KEY",
		DefaultModel: "glm-5.2",
	},
	{
		ID:           "moonshot",
		Region:       RegionChina,
		Dialect:      DialectOpenAI,
		BaseURL:      "https://api.moonshot.cn/v1",
		EnvKey:       "MOONSHOT_API_KEY",
		DefaultModel: "kimi-k3",
	},
	{
		ID:               "qwen",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://dashscope.aliyuncs.com/compatible-mode/v1",
		AnthropicBaseURL: "https://dashscope.aliyuncs.com/apps/anthropic",
		EnvKey:           "DASHSCOPE_API_KEY",
		DefaultModel:     "qwen3.8-max",
	},
}

// providerSpecByID returns the ProviderSpec for a canonical id, or nil.
func providerSpecByID(id string) *ProviderSpec {
	for i := range providerCatalog {
		if providerCatalog[i].ID == id {
			return &providerCatalog[i]
		}
	}
	return nil
}

// providerSpecByEnvKey returns the ProviderSpec whose EnvKey matches,
// or nil.
func providerSpecByEnvKey(envKey string) *ProviderSpec {
	for i := range providerCatalog {
		if providerCatalog[i].EnvKey == envKey {
			return &providerCatalog[i]
		}
	}
	return nil
}

// catalogProviderIDs returns the sorted-by-catalog-order list of
// supported provider ids.
func catalogProviderIDs() []string {
	out := make([]string, 0, len(providerCatalog))
	for _, p := range providerCatalog {
		out = append(out, p.ID)
	}
	return out
}

// ListSupportedProviders returns the human-readable list of providers
// in the catalog (used in error hints so operators know what to set).
func ListSupportedProviders() []string {
	return catalogProviderIDs()
}

// providerDialect returns the effective dialect for a provider after
// the optional DARK_JUDGE_DIALECT override (anthropic providers that
// also speak Anthropic Messages honor it; others ignore it).
func providerDialect(p *ProviderSpec) ProviderDialect {
	if p == nil {
		return DialectAnthropic
	}
	if os.Getenv("DARK_JUDGE_DIALECT") == "anthropic" && p.AnthropicBaseURL != "" {
		return DialectAnthropic
	}
	return p.Dialect
}
