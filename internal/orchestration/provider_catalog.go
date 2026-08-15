// ProviderCatalog is the data-driven registry of LLM providers that
// dark-memory-mcp can use as judge.
//
// Spec 1188 (v2.20.0): the catalog moved to internal/llm/catalog.go —
// the SINGLE canonical source (endpoints + dialect + models + probe
// config). This file keeps the orchestration-facing surface as type
// aliases + derived helpers so existing call sites keep compiling for
// one release cycle (backwards-compat decision D9). New code should
// import internal/llm directly.
//
// Every endpoint in the canonical table was verified against the
// provider's PRIMARY documentation on 2026-08-10 (agent_memory row
// 587). Nothing here is invented.
//
// Dialect "anthropic" = Anthropic Messages API (POST /v1/messages,
// x-api-key + anthropic-version headers).
// Dialect "openai" = OpenAI Chat Completions (POST /chat/completions,
// Authorization: Bearer, choices[0].message.content).
package orchestration

import (
	"os"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
)

// ProviderDialect is the wire dialect a provider speaks (alias of
// llm.ProviderDialect).
type ProviderDialect = llm.ProviderDialect

const (
	// DialectAnthropic = Anthropic Messages API (v1/messages).
	DialectAnthropic = llm.DialectAnthropic
	// DialectOpenAI = OpenAI Chat Completions (chat/completions).
	DialectOpenAI = llm.DialectOpenAI
)

// ProviderRegion is the geo-classification of a provider (alias).
type ProviderRegion = llm.ProviderRegion

const (
	// RegionUS = US/EU primary.
	RegionUS = llm.RegionUS
	// RegionChina = PRC primary.
	RegionChina = llm.RegionChina
)

// ProviderSpec describes one supported LLM provider (alias of the
// canonical llm.ProviderSpec).
type ProviderSpec = llm.ProviderSpec

// providerCatalog is the authoritative list, in failover priority
// order. It IS the canonical llm.Catalog (single source of truth).
var providerCatalog = llm.Catalog

// providerSpecByID returns the ProviderSpec for a canonical or legacy
// id (aliases z-ai→zhipu, dashscope→qwen resolved), or nil.
func providerSpecByID(id string) *ProviderSpec {
	return llm.SpecByID(id)
}

// providerSpecByEnvKey returns the ProviderSpec whose EnvKey matches,
// or nil.
func providerSpecByEnvKey(envKey string) *ProviderSpec {
	return llm.SpecByEnvKey(envKey)
}

// catalogProviderIDs returns the sorted-by-catalog-order list of
// canonical provider ids.
func catalogProviderIDs() []string {
	return llm.CanonicalIDs()
}

// ListSupportedProviders returns the human-readable list of providers
// in the catalog (used in error hints so operators know what to set).
func ListSupportedProviders() []string {
	return llm.CanonicalIDs()
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
