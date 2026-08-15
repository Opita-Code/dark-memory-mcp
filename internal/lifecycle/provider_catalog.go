package lifecycle

import (
	"os"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
)

// ProviderInfo describes one LLM provider and the env var that
// carries its API key. It is the harness-recommendation view of a
// provider; the canonical source of provider data now lives in
// internal/llm/catalog.go (spec 1188 — single source of truth).
type ProviderInfo struct {
	// ID is the provider's canonical identifier. Examples: "anthropic",
	// "openai", "google", "deepseek", "minimax", "minimax-cn",
	// "zhipu", "moonshot", "qwen".
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

// providerCatalog is the harness-recommendation view of the canonical
// llm.Catalog (same order = failover priority). Derived once at init.
var providerCatalog = deriveProviderCatalog()

// deriveProviderCatalog maps the canonical llm.Catalog onto the
// harness-recommendation ProviderInfo view.
func deriveProviderCatalog() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(llm.Catalog))
	for _, spec := range llm.Catalog {
		out = append(out, ProviderInfo{
			ID:          spec.ID,
			EnvKey:      spec.EnvKey,
			Family:      spec.Family,
			Models:      spec.Models,
			DefaultRung: rungFromString(spec.DefaultRung),
		})
	}
	return out
}

// rungFromString maps the canonical llm.DefaultRung string to the
// lifecycle HarnessRung enum.
func rungFromString(s string) HarnessRung {
	switch s {
	case string(RungHeavy):
		return RungHeavy
	case string(RungMedium):
		return RungMedium
	case string(RungLight):
		return RungLight
	default:
		return RungUnknown
	}
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
