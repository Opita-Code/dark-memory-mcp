// Package orchestration — nli_wiring.go
//
// v2.20.0 T08 (spec 1276): wire nli.Provider from Project.NLIConfig
// (added in T07). The drift_judge pipeline reads the project's NLIConfig,
// constructs a Provider chain (Router → CachedProvider → DeBERTaProvider/
// MiniCheckProvider), and routes the drift score through it.
//
// Hard invariants (sealed):
//
//   - nliProviderForConfig returns (nil, nil) when nliCfg is nil OR
//     nliCfg.Enabled is false. The caller falls through to default
//     provider construction (no project override).
//   - When nliCfg.Enabled is true, the Provider chain is built
//     EXACTLY once per orchestrator (lazy via EnsureNLIRouter).
//   - The Provider's ID is the inner provider's ID (Router /
//     CachedProvider both pass through ID()). Cache key uses this
//     id (invariant from T06).
//   - The AuthToken is read from nliCfg.Primary.AuthToken (and
//     nliCfg.Fallback.AuthToken), NEVER logged, NEVER echoed in
//     errors (T05 invariant).
//   - Project.NLIConfig.Validate() is the entry gate — invalid
//     configs are rejected at the project_create surface (T07), not
//     here. We trust nliCfg here.
package orchestration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
)

// nliProviderForConfig builds an nli.Provider from the project's NLIConfig.
//
// Returns (nil, nil) when nliCfg is nil OR nliCfg.Enabled is false.
// The caller (orchestrator) treats (nil, nil) as "no override; use defaults".
//
// When nliCfg.Enabled is true, the composition is:
//
//	[Router] (if FallbackEnabled)
//	  primary: [CachedProvider] (if MaxCacheEntries > 0)
//	    inner: [DeBERTaProvider] (ProviderID matches "deberta*")
//	            OR [MiniCheckProvider] (ProviderID matches "minicheck*")
//	  fallback: [Provider] (no cache — fallback is only hit on transient errors)
//
// hc is the HTTP client used by DeBERTaProvider / MiniCheckProvider.
// Tests pass a stub; production uses http.DefaultClient.
func nliProviderForConfig(ctx context.Context, nliCfg *project.NLIConfig, hc nli.HFInferenceClient) (nli.Provider, error) {
	if nliCfg == nil || !nliCfg.Enabled {
		return nil, nil
	}
	if hc == nil {
		hc = http.DefaultClient
	}

	primary, err := buildNLIPrimary(nliCfg.Primary, hc, nliCfg.MaxPremiseBytes, nliCfg.MaxHypothesisBytes)
	if err != nil {
		return nil, fmt.Errorf("nli primary: %w", err)
	}

	var fallback nli.Provider
	if nliCfg.FallbackEnabled {
		fallback, err = buildNLIPrimary(nliCfg.Fallback, hc, nliCfg.MaxPremiseBytes, nliCfg.MaxHypothesisBytes)
		if err != nil {
			return nil, fmt.Errorf("nli fallback: %w", err)
		}
	}

	// Wrap primary with cache if MaxCacheEntries > 0. The cache key
	// is keyed by the inner provider's ID (T06 invariant), so wrapping
	// does not silently change cross-provider isolation.
	scored := nli.Provider(primary)
	if nliCfg.MaxCacheEntries > 0 {
		ttl := time.Duration(nliCfg.CacheTTLSeconds) * time.Second
		cache, err := nli.NewInMemoryLRU(nliCfg.MaxCacheEntries)
		if err != nil {
			return nil, fmt.Errorf("nli cache: %w", err)
		}
		cached, err := nli.NewCachedProvider(primary, cache, ttl)
		if err != nil {
			return nil, fmt.Errorf("nli cached provider: %w", err)
		}
		scored = cached
	}

	// Wrap with Router if fallback is enabled.
	if nliCfg.FallbackEnabled {
		cfg := nli.Config{
			Primary:            nliPrimaryToProviderConfig(nliCfg.Primary),
			Fallback:           nliPrimaryToProviderConfig(nliCfg.Fallback),
			FallbackEnabled:    true,
			LatencyBudgetMS:    nliCfg.LatencyBudgetMS,
			MaxPremiseBytes:    nliCfg.MaxPremiseBytes,
			MaxHypothesisBytes: nliCfg.MaxHypothesisBytes,
		}
		router, err := nli.NewRouter(scored, fallback, cfg)
		if err != nil {
			return nil, fmt.Errorf("nli router: %w", err)
		}
		return router, nil
	}

	return scored, nil
}

// buildNLIPrimary dispatches to the right Provider implementation
// based on the ProviderID prefix. Convention:
//
//	"deberta*"        → DeBERTaProvider (HuggingFace Inference)
//	"minicheck*"      → MiniCheckProvider (self-hosted HTTP)
//
// Anything else → ErrInvalidConfig. The operator can extend the
// dispatch via a project.NLIConfig fixture when new providers land
// (registry grow path is the same as the nli package's labels).
func buildNLIPrimary(p project.NLIPrimary, hc nli.HFInferenceClient, maxPBytes, maxHBytes int) (nli.Provider, error) {
	if p.ProviderID == "" {
		return nil, fmt.Errorf("%w: provider_id empty", nli.ErrInvalidConfig)
	}
	if p.Endpoint == "" {
		return nil, fmt.Errorf("%w: endpoint empty", nli.ErrInvalidConfig)
	}
	pc := nliPrimaryToProviderConfig(p)
	switch {
	case strings.HasPrefix(p.ProviderID, "deberta"):
		return nli.NewDeBERTaProvider(pc, hc, maxPBytes, maxHBytes)
	case strings.HasPrefix(p.ProviderID, "minicheck"):
		return nli.NewMiniCheckProvider(pc, hc, maxPBytes, maxHBytes)
	default:
		return nil, fmt.Errorf("%w: unknown provider_id %q (only deberta* and minicheck* supported)",
			nli.ErrInvalidConfig, p.ProviderID)
	}
}

// nliPrimaryToProviderConfig converts project.NLIPrimary to nli.
// ProviderConfig. The fields map 1:1; differences are documented at
// the type defs. Defined as a free function (not a method) because
// project.NLIPrimary is a non-local type and Go forbids methods on
// non-local types defined in other packages.
func nliPrimaryToProviderConfig(p project.NLIPrimary) nli.ProviderConfig {
	return nli.ProviderConfig{
		ProviderID: p.ProviderID,
		Endpoint:   p.Endpoint,
		AuthToken:  p.AuthToken,
		TimeoutMS:  p.TimeoutMS,
		ModelRev:   p.ModelRev,
	}
}
