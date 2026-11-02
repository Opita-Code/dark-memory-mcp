// Package orchestration — nli_wiring.go
//
// v2.20.0 T08 (spec 1276): wire nli.Provider from Project.NLIConfig
// (added in T07). The drift_judge pipeline reads the project's NLIConfig,
// constructs a Provider chain (Router → CachedProvider → DeBERTaProvider/
// MiniCheckProvider/ChatProvider), and routes the drift score through it.
//
// Phase 13 T-201 (alpha.24-pre-1, 2026-10-06): added the `chat-*`
// dispatch case to close the Phase H5 / row 1370 gap. Operators with
// OpenAI-compatible chat completions configs (e.g. "chat-minimax-cn"
// against https://api.minimaxi.com/v1/chat/completions,
// "chat-deepseek" against https://api.deepseek.com/v1/chat/completions)
// previously hit `ErrInvalidConfig: unknown provider_id` and drift_judge
// returned needs_human@0 even though the DB config was valid. The fix
// dispatches `chat-*` to internal/nli/ChatProvider which uses the
// OpenAI chat-completions API with a standard RAG-eval NLI prompt
// (model replies with one canonical word: entailment / contradiction /
// neutral; we map that to the 3-label space).
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
//   - The AuthToken NEVER comes from the project config alone: the
//     config stores the PROVIDER ID and the credential is resolved
//     from the OS keyring at call time (see buildNLIPrimary). A
//     legacy config token is still honoured verbatim. The token is
//     NEVER logged, NEVER echoed in errors (T05 invariant).
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

	primary, err := buildNLIPrimary(ctx, nliCfg.Primary, hc, nliCfg.MaxPremiseBytes, nliCfg.MaxHypothesisBytes)
	if err != nil {
		return nil, fmt.Errorf("nli primary: %w", err)
	}

	var fallback nli.Provider
	if nliCfg.FallbackEnabled {
		fallback, err = buildNLIPrimary(ctx, nliCfg.Fallback, hc, nliCfg.MaxPremiseBytes, nliCfg.MaxHypothesisBytes)
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
//	"chat-*"          → ChatProvider (OpenAI-compatible chat completions)
//
// Phase 13 T-201 (alpha.24-pre-1, 2026-10-06): added the "chat-*"
// dispatch case for the operator's chat-provider configs (closes
// row 1370 / Phase H5 partial).
//
// The context is used by the chat providers to resolve their endpoint
// against the credential in the config: the endpoint is a property of
// the CREDENTIAL, not of the provider (see internal/nli/chat.go).
//
// CREDENTIAL RESOLUTION (the secret never lives in the config)
//
// The project config stores the PROVIDER ID; the API key lives in the
// OS keyring and is resolved HERE, at call time:
//
//   - a legacy row that still carries a token in the config is honoured
//     verbatim (compatibility);
//   - a config without a token is resolved from the keyring through
//     llm.CredentialForProvider, which maps the dispatch-prefixed id
//     ("chat-minimax") to the catalog id ("minimax") that owns the key;
//   - when the id maps to a catalog provider but NO credential can be
//     resolved the call FAILS with an explicit error instead of
//     building a provider that would 401 on every call.
//
// Anything else → ErrInvalidConfig. The operator can extend the
// dispatch via a project.NLIConfig fixture when new providers land
// (registry grow path is the same as the nli package's labels).
func buildNLIPrimary(ctx context.Context, p project.NLIPrimary, hc nli.HFInferenceClient, maxPBytes, maxHBytes int) (nli.Provider, error) {
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
	case strings.HasPrefix(p.ProviderID, "chat-"):
		// The secret does NOT live in the project config: resolve it
		// from the keyring when the config carries none. A legacy
		// config token wins (it is what the operator explicitly
		// stored).
		if strings.TrimSpace(pc.AuthToken) == "" {
			token, _, cerr := resolveBindingCredential(p.ProviderID)
			if cerr != nil {
				return nil, cerr
			}
			pc.AuthToken = token
		}
		return nli.NewChatProviderWithContext(ctx, pc, hc, maxPBytes, maxHBytes)
	case strings.HasPrefix(p.ProviderID, "judge-"):
		// A "judge-*" binding is the SAME OpenAI-compatible chat
		// endpoint, consumed by the LLM-judge client. The NLI chain
		// is the judge's fallback, so it must be able to use it: a
		// project configured only with a "judge-*" binding had
		// nowhere to fall back to, and the verdict degraded to
		// needs_human with "unknown provider_id".
		if strings.TrimSpace(pc.AuthToken) == "" {
			token, _, cerr := resolveBindingCredential(p.ProviderID)
			if cerr != nil {
				return nil, cerr
			}
			pc.AuthToken = token
		}
		return nli.NewChatProviderWithContext(ctx, pc, hc, maxPBytes, maxHBytes)
	default:
		return nil, fmt.Errorf("%w: unknown provider_id %q (supported: deberta*, minicheck*, chat-*)",
			nli.ErrInvalidConfig, p.ProviderID)
	}
}

// nliPrimaryToProviderConfig converts project.NLIPrimary to nli.
// ProviderConfig. The fields map 1:1; differences are documented at
// the type defs. Defined as a free function (not a method) because
// project.NLIPrimary is a non-local type and Go forbids methods on
// non-local types defined in other packages.
//
// T-407-c (v4.0.0-alpha.28): passes MaxTokensOverride so operators
// can escape the per-model defaults table for chat providers whose
// model_rev is unknown to resolveMaxTokens or whose workload needs
// a different budget than the table default.
func nliPrimaryToProviderConfig(p project.NLIPrimary) nli.ProviderConfig {
	return nli.ProviderConfig{
		ProviderID:        p.ProviderID,
		Endpoint:          p.Endpoint,
		AuthToken:         p.AuthToken,
		TimeoutMS:         p.TimeoutMS,
		ModelRev:          p.ModelRev,
		MaxTokensOverride: p.MaxTokensOverride,
	}
}
