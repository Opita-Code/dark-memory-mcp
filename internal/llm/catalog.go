// catalog.go — the SINGLE canonical LLM provider catalog (spec 1188 T1).
//
// Until v2.20.0 dark-memory kept TWO hand-rolled provider catalogs:
//
//   - internal/orchestration/provider_catalog.go (8 entries, IDs
//     anthropic/openai/google/deepseek/minimax/zhipu/moonshot/qwen) —
//     rich fields: BaseURL, Dialect, AnthropicBaseURL, DefaultModel.
//     Verified against provider PRIMARY documentation on 2026-08-10
//     (agent_memory row 587).
//   - internal/lifecycle/provider_catalog.go (9 entries, IDs with
//     z-ai/dashscope/minimax-cn) — harness recommendation fields:
//     Family, Models, DefaultRung.
//
// That duplication was a drift hazard: the same provider had different
// IDs in each catalog and resolution order was "first non-empty env var
// in whichever catalog the caller happened to iterate".
//
// This package is now the single source of truth. The two legacy
// packages derive their view types from it (compat wrappers kept for
// one release cycle — see the D9 backwards-compat decision).
//
// Canonical IDs: anthropic, openai, google, deepseek, minimax,
// minimax-cn, zhipu, moonshot, qwen.
// Aliases (legacy IDs still accepted): z-ai → zhipu, dashscope → qwen.
//
// Catalog order IS the failover priority order (first healthy provider
// with a key wins).
package llm

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

// ProbeAuthMode says how the hot-probe authenticates (T3).
type ProbeAuthMode string

const (
	// ProbeAuthBearer sends "Authorization: Bearer <key>".
	ProbeAuthBearer ProbeAuthMode = "bearer"
	// ProbeAuthXAPIKey sends "x-api-key: <key>" (+ anthropic-version
	// header for Anthropic-dialect providers).
	ProbeAuthXAPIKey ProbeAuthMode = "x-api-key"
)

// ProviderSpec describes one supported LLM provider. It is the union of
// the old orchestration.ProviderSpec (endpoint/dialect/model) and the
// old lifecycle.ProviderInfo (family/models/rung), plus the T3 probe
// configuration.
type ProviderSpec struct {
	// ID is the canonical provider id (DARK_JUDGE_PROVIDER, audit rows).
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

	// --- lifecycle (harness recommendation) fields ---
	// Family matches HarnessNative.Family for native providers.
	Family string
	// Models is the provider's model lineup, ordered from highest to
	// lowest rung. The first model is the recommended default.
	Models []string
	// DefaultRung is the rung of the first model in Models
	// (heavy | medium | light | unknown — string to avoid an import
	// cycle; lifecycle maps it to HarnessRung).
	DefaultRung string

	// --- T3 hot-probe fields ---
	// ProbePath is the GET path appended to BaseURL to validate the key
	// without spending tokens: OpenAI-compat providers use "/models";
	// Anthropic uses "/v1/models".
	ProbePath string
	// ProbeAuthMode is how the probe authenticates.
	ProbeAuthMode ProbeAuthMode
	// BalancePath (optional) is an extra probe path that also reports
	// account balance (DeepSeek: /user/balance). Empty = none.
	BalancePath string
}

// Aliases maps legacy provider IDs to canonical ones. Accepted by
// ResolveID and surfaced as a deprecation warning on first use.
var Aliases = map[string]string{
	"z-ai":     "zhipu",
	"dashscope": "qwen",
}

// Catalog is the canonical provider list. Order = failover priority.
//
// Endpoint sources (fetched 2026-08-10, primary docs — row 587):
//
//	anthropic  -> https://docs.anthropic.com/en/api/errors  (Messages API)
//	openai     -> https://developers.openai.com/api/docs/guides/error-codes
//	google     -> https://ai.google.dev/gemini-api/docs/openai  (OpenAI-compat)
//	deepseek   -> https://api-docs.deepseek.com/  (OpenAI + /anthropic)
//	minimax    -> https://platform.minimax.io/docs/api-reference/text-openai-api
//	zhipu      -> https://docs.bigmodel.cn/cn/guide/develop/openai/introduction
//	moonshot   -> https://platform.moonshot.cn/docs/guide/start-using-kimi-api
//	qwen       -> https://help.aliyun.com/zh/model-studio/getting-started/models
//
// minimax endpoint note (2026-08-13, operator directive): the catalog
// points at api.minimaxi.com (CN regional) — the key currently injected
// by the harness is scoped to it only. minimax-cn shares the endpoint
// but uses the MINIMAX_API_KEY_CN env var.
var Catalog = []ProviderSpec{
	{
		ID:           "anthropic",
		Region:       RegionUS,
		Dialect:      DialectAnthropic,
		BaseURL:      "https://api.anthropic.com",
		EnvKey:       "ANTHROPIC_API_KEY",
		DefaultModel: "claude-sonnet-4-5",
		Family:       "anthropic",
		Models:       []string{"claude-sonnet-4.5", "claude-opus-4.5", "claude-haiku-4.5"},
		DefaultRung:  "medium",
		ProbePath:    "/v1/models",
		ProbeAuthMode: ProbeAuthXAPIKey,
	},
	{
		ID:            "openai",
		Region:        RegionUS,
		Dialect:       DialectOpenAI,
		BaseURL:       "https://api.openai.com/v1",
		EnvKey:        "OPENAI_API_KEY",
		DefaultModel:  "gpt-5",
		Family:        "openai",
		Models:        []string{"gpt-5", "gpt-5.5", "gpt-5-mini"},
		DefaultRung:   "medium",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
	},
	{
		ID:            "google",
		Region:        RegionUS,
		Dialect:       DialectOpenAI,
		BaseURL:       "https://generativelanguage.googleapis.com/v1beta/openai/",
		EnvKey:        "GEMINI_API_KEY",
		DefaultModel:  "gemini-3.6-flash",
		Family:        "google",
		Models:        []string{"gemini-3.0-pro", "gemini-2.5-flash"},
		DefaultRung:   "medium",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
	},
	{
		ID:               "deepseek",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://api.deepseek.com",
		AnthropicBaseURL: "https://api.deepseek.com/anthropic",
		EnvKey:           "DEEPSEEK_API_KEY",
		DefaultModel:     "deepseek-v4-flash",
		Family:           "deepseek",
		Models:           []string{"deepseek-v4", "deepseek-v3"},
		DefaultRung:      "medium",
		ProbePath:        "/models",
		ProbeAuthMode:    ProbeAuthBearer,
		BalancePath:      "/user/balance",
	},
	{
		ID:               "minimax",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://api.minimaxi.com/v1",
		AnthropicBaseURL: "https://api.minimaxi.com/anthropic",
		EnvKey:           "MINIMAX_API_KEY",
		DefaultModel:     "MiniMax-M3",
		Family:           "minimax",
		Models:           []string{"MiniMax-M3", "minimax/M3"},
		DefaultRung:      "heavy",
		ProbePath:        "/models",
		ProbeAuthMode:    ProbeAuthBearer,
	},
	{
		ID:               "minimax-cn",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://api.minimaxi.com/v1",
		AnthropicBaseURL: "https://api.minimaxi.com/anthropic",
		EnvKey:           "MINIMAX_API_KEY_CN",
		DefaultModel:     "MiniMax-M3",
		Family:           "minimax-cn",
		Models:           []string{"MiniMax-M3"},
		DefaultRung:      "heavy",
		ProbePath:        "/models",
		ProbeAuthMode:    ProbeAuthBearer,
	},
	{
		ID:            "zhipu",
		Region:        RegionChina,
		Dialect:       DialectOpenAI,
		BaseURL:       "https://open.bigmodel.cn/api/paas/v4/",
		EnvKey:        "ZAI_API_KEY",
		DefaultModel:  "glm-5.2",
		Family:        "zhipu",
		Models:        []string{"glm-4.6", "glm-4.5"},
		DefaultRung:   "medium",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
	},
	{
		ID:            "moonshot",
		Region:        RegionChina,
		Dialect:       DialectOpenAI,
		BaseURL:       "https://api.moonshot.cn/v1",
		EnvKey:        "MOONSHOT_API_KEY",
		DefaultModel:  "kimi-k3",
		Family:        "moonshot",
		Models:        []string{"moonshot-v1-128k", "moonshot-v1-32k"},
		DefaultRung:   "medium",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
	},
	{
		ID:               "qwen",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://dashscope.aliyuncs.com/compatible-mode/v1",
		AnthropicBaseURL: "https://dashscope.aliyuncs.com/apps/anthropic",
		EnvKey:           "DASHSCOPE_API_KEY",
		DefaultModel:     "qwen3.8-max",
		Family:           "qwen",
		Models:           []string{"qwen3-max", "qwen3-coder-plus"},
		DefaultRung:      "medium",
		ProbePath:        "/models",
		ProbeAuthMode:    ProbeAuthBearer,
	},
}

// ResolveID maps a (possibly legacy) provider id to its canonical id.
// Returns the canonical id and whether an alias was applied.
func ResolveID(id string) (canonical string, wasAlias bool) {
	if c, ok := Aliases[id]; ok {
		return c, true
	}
	return id, false
}

// SpecByID returns the catalog spec for a canonical or legacy id, or
// nil when unknown.
func SpecByID(id string) *ProviderSpec {
	canonical, _ := ResolveID(id)
	for i := range Catalog {
		if Catalog[i].ID == canonical {
			return &Catalog[i]
		}
	}
	return nil
}

// SpecByEnvKey returns the catalog spec whose EnvKey matches, or nil.
func SpecByEnvKey(envKey string) *ProviderSpec {
	for i := range Catalog {
		if Catalog[i].EnvKey == envKey {
			return &Catalog[i]
		}
	}
	return nil
}

// CanonicalIDs returns the sorted-by-catalog-order list of canonical
// provider ids.
func CanonicalIDs() []string {
	out := make([]string, 0, len(Catalog))
	for _, p := range Catalog {
		out = append(out, p.ID)
	}
	return out
}

// EnvKeyForProvider resolves the EnvKey for a (canonical or legacy)
// provider id. Empty string when unknown. Used by the env-var
// keystore backend.
func EnvKeyForProvider(providerID string) string {
	if s := SpecByID(providerID); s != nil {
		return s.EnvKey
	}
	return ""
}
