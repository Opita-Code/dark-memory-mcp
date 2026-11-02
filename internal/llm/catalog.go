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

import (
	"strings"
)

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

// EndpointSpec is one concrete deployment (region) of a provider.
//
// A provider may be reachable at more than one endpoint, and which
// one accepts a given API key is a property of the CREDENTIAL, not of
// the provider: MiniMax, for instance, mints region-scoped keys
// (api.minimax.io for the global region, api.minimaxi.com for China)
// and each key authenticates ONLY against its own endpoint.
//
// The catalog therefore declares CANDIDATES and the effective endpoint
// is resolved against the key at runtime — see ResolveEndpoint in
// internal/llm/endpoint.go.
type EndpointSpec struct {
	// ID is a stable identifier for the deployment ("global", "cn",
	// "env" for an operator override). It is persisted alongside the
	// key in the credential record.
	ID string
	// BaseURL is the base URL for the provider's primary dialect.
	BaseURL string
	// AnthropicBaseURL is the Anthropic-compatible base, when the
	// deployment offers one.
	AnthropicBaseURL string
	// ProbePath is the GET path appended to BaseURL to validate a key
	// without spending tokens.
	ProbePath string
	// ProbeAuthMode is how the probe authenticates.
	ProbeAuthMode ProbeAuthMode
}

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
	//
	// It is the DEFAULT endpoint (the first candidate) and is kept
	// for the legacy compat wrappers that read it directly. Code that
	// has a credential in hand must use ResolveEndpoint instead.
	BaseURL string
	// AnthropicBaseURL is optional; set for providers that ALSO speak
	// Anthropic Messages (deepseek, minimax, qwen). When the operator
	// pins DARK_JUDGE_DIALECT=anthropic for such a provider, this URL
	// is used instead of BaseURL.
	AnthropicBaseURL string
	// Endpoints are the deployment candidates for this provider, in
	// preference order. Empty means "one implicit endpoint built from
	// BaseURL/AnthropicBaseURL" (see EndpointCandidates).
	Endpoints []EndpointSpec
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

// EndpointCandidates returns the deployment candidates for this
// provider, in preference order. Never empty: a provider that declares
// no Endpoints yields one implicit candidate built from its own
// BaseURL/AnthropicBaseURL fields.
func (p *ProviderSpec) EndpointCandidates() []EndpointSpec {
	if p == nil {
		return nil
	}
	if len(p.Endpoints) > 0 {
		out := make([]EndpointSpec, len(p.Endpoints))
		copy(out, p.Endpoints)
		return out
	}
	return []EndpointSpec{{
		ID:               "default",
		BaseURL:          p.BaseURL,
		AnthropicBaseURL: p.AnthropicBaseURL,
		ProbePath:        p.ProbePath,
		ProbeAuthMode:    p.ProbeAuthMode,
	}}
}

// DefaultEndpoint returns the first candidate — the endpoint used when
// no credential-specific resolution has happened.
func (p *ProviderSpec) DefaultEndpoint() EndpointSpec {
	cands := p.EndpointCandidates()
	if len(cands) == 0 {
		return EndpointSpec{}
	}
	return cands[0]
}

// EndpointByID returns the candidate with the given id.
func (p *ProviderSpec) EndpointByID(id string) (EndpointSpec, bool) {
	for _, e := range p.EndpointCandidates() {
		if e.ID == id {
			return e, true
		}
	}
	return EndpointSpec{}, false
}

// Aliases maps legacy provider IDs to canonical ones. Accepted by
// ResolveID and surfaced as a deprecation warning on first use.
var Aliases = map[string]string{
	"z-ai":      "zhipu",
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
// minimax endpoint note (2026-08-13, operator directive; revised
// 2026-11-02): the catalog originally pointed at api.minimaxi.com (CN
// regional) because the key injected by the harness was scoped to it.
//
// The truth is that the endpoint is a property of the CREDENTIAL, not
// of the provider: MiniMax mints DIFFERENT keys per region and each
// one authenticates ONLY against its own endpoint (a CN key against
// api.minimax.io returns HTTP 401 "invalid api key"). A single
// hardcoded BaseURL therefore breaks the moment the operator rotates
// keys or uses a key from another region.
//
// So the catalog declares CANDIDATES (Endpoints) and the effective
// endpoint is resolved against the credential at runtime by
// ResolveEndpoint (internal/llm/endpoint.go): env override -> cached
// credential endpoint -> probe discovery.
//
//	minimax    -> global (api.minimax.io), cn (api.minimaxi.com)
//	minimax-cn -> cn (api.minimaxi.com), key from MINIMAX_API_KEY_CN
//
// Both speak the OpenAI dialect and expose an Anthropic-compatible
// /anthropic path. An operator can still force one endpoint without a
// recompile: DARK_LLM_BASE_URL_minimax / DARK_LLM_ANTHROPIC_BASE_URL_minimax.
var Catalog = []ProviderSpec{
	{
		ID:            "anthropic",
		Region:        RegionUS,
		Dialect:       DialectAnthropic,
		BaseURL:       "https://api.anthropic.com",
		EnvKey:        "ANTHROPIC_API_KEY",
		DefaultModel:  "claude-sonnet-4-5",
		Family:        "anthropic",
		Models:        []string{"claude-sonnet-4.5", "claude-opus-4.5", "claude-haiku-4.5"},
		DefaultRung:   "medium",
		ProbePath:     "/v1/models",
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
		Region:           RegionUS,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://api.minimax.io/v1",
		AnthropicBaseURL: "https://api.minimax.io/anthropic",
		Endpoints: []EndpointSpec{
			{
				ID:               "global",
				BaseURL:          "https://api.minimax.io/v1",
				AnthropicBaseURL: "https://api.minimax.io/anthropic",
				ProbePath:        "/models",
				ProbeAuthMode:    ProbeAuthBearer,
			},
			{
				ID:               "cn",
				BaseURL:          "https://api.minimaxi.com/v1",
				AnthropicBaseURL: "https://api.minimaxi.com/anthropic",
				ProbePath:        "/models",
				ProbeAuthMode:    ProbeAuthBearer,
			},
		},
		EnvKey:        "MINIMAX_API_KEY",
		DefaultModel:  "MiniMax-M3",
		Family:        "minimax",
		Models:        []string{"MiniMax-M3", "minimax/M3"},
		DefaultRung:   "heavy",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
	},
	{
		ID:               "minimax-cn",
		Region:           RegionChina,
		Dialect:          DialectOpenAI,
		BaseURL:          "https://api.minimaxi.com/v1",
		AnthropicBaseURL: "https://api.minimaxi.com/anthropic",
		Endpoints: []EndpointSpec{
			{
				ID:               "cn",
				BaseURL:          "https://api.minimaxi.com/v1",
				AnthropicBaseURL: "https://api.minimaxi.com/anthropic",
				ProbePath:        "/models",
				ProbeAuthMode:    ProbeAuthBearer,
			},
		},
		EnvKey:        "MINIMAX_API_KEY_CN",
		DefaultModel:  "MiniMax-M3",
		Family:        "minimax-cn",
		Models:        []string{"MiniMax-M3"},
		DefaultRung:   "heavy",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
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
//
// The returned spec is a POINTER INTO the package-level Catalog, so
// callers must treat it as read-only. Endpoint resolution (which
// endpoint accepts a given key) lives in internal/llm/endpoint.go, not
// here: the catalog is data, the resolution is policy.
func SpecByID(id string) *ProviderSpec {
	canonical, _ := ResolveID(id)
	for i := range Catalog {
		if Catalog[i].ID == canonical {
			return &Catalog[i]
		}
	}
	return nil
}

// DefaultModelForURL returns the catalog DefaultModel of the provider
// whose endpoint (any candidate) prefixes the given endpoint URL.
// Empty string when no catalog entry matches.
//
// Used by the connectivity probe: providers reject a request whose
// "model" field names a model they do not know, so the probe cannot
// send a placeholder model name.
func DefaultModelForURL(endpoint string) string {
	ep := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if ep == "" {
		return ""
	}
	for i := range Catalog {
		s := &Catalog[i]
		for _, e := range s.EndpointCandidates() {
			for _, base := range []string{e.BaseURL, e.AnthropicBaseURL} {
				base = strings.TrimRight(strings.TrimSpace(base), "/")
				if base == "" {
					continue
				}
				if ep == base || strings.HasPrefix(ep, base+"/") {
					return s.DefaultModel
				}
			}
		}
	}
	return ""
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
