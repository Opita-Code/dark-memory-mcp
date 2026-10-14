// Package orchestration — oauth_nli_provider.go: the nli.Provider
// implementation that delegates to OAuth-backed backends (Google
// Gemini via ChatProvider; HuggingFace via DeBERTaProvider).
//
// This is the Tier 1 self-auth bridge: a user authenticates with
// their own Google or HuggingFace account (via
// dark_memory_oauth_connect), tokens live in the OS keyring, and
// this provider translates them into NLI HTTP calls. The operator
// no longer has to provision shared API keys for end users.
//
// Phase 23 Tier 1 (Sesión 7, 2026-10-14, spec_id 1992): this
// file closes the deferred gap from Sesión 6 (commit 8ed967a)
// by wiring defaultDriftJudgeProvider (orchestrator.go:480-488)
// to OAuth-backed providers.
//
// # Design notes
//
//   - The provider is thread-safe. The inner provider is rebuilt
//     only when the access_token changes (token rotation, refresh,
//     manual disconnect+reconnect). Concurrent Score() calls share
//     a single inner provider via sync.RWMutex.
//   - ID() returns "oauth-<provider>-<model>" so audit logs can
//     distinguish OAuth-backed verdicts from operator-key-backed
//     ones (the latter keep the bare provider id).
//   - Resolver errors are surfaced verbatim with a human-friendly
//     prefix. The drift_judge pipeline can classify ErrNoConnection
//     and ErrConnectionExpired via errors.Is when deciding whether
//     to fall through to a different provider or surface
//     verdict=needs_human.
//   - HTTP client is injectable for tests; nil → http.DefaultClient
//     (the same pattern as nli.ChatProvider / DeBERTaProvider).
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/dark-agents/dark-memory-mcp/internal/nli"
)

// OAuthNLIProvider adapts an OAuth-backed token into dark-memory's
// nli.Provider contract.
//
// Construction:
//
//	resolver := // cmd sub-module's RouterAdapter, wired via WithOAuthResolver
//	p, err := NewOAuthNLIProvider(resolver, OAuthProviderGoogle, nil)
//	if err != nil { ... }
//	// p is nli.Provider — pass it to EnsureNLIRouter or to
//	// defaultDriftJudgeProvider directly.
//
// Behavior:
//
//   - ID() returns "oauth-<provider>-<model>" so audit + logging
//     distinguish OAuth-backed verdicts from operator-key-backed
//     ones.
//   - Score(ctx, premise, hypothesis):
//     1. resolver.LoadActiveToken(ctx, name) returns (token,
//        endpoint, model, nil) on success or ErrNoConnection /
//        ErrConnectionExpired.
//     2. If the token differs from the cached one, build the
//        inner provider (ChatProvider for Google, DeBERTaProvider
//        for HuggingFace) using the fresh credentials.
//     3. Delegate Score to the inner provider; surface its
//        errors verbatim so the standard nli error
//        classification in DriftJudge still works.
type OAuthNLIProvider struct {
	resolver     OAuthTokenResolver
	providerName string
	httpClient   nli.HFInferenceClient // injectable for tests; nil -> http.DefaultClient

	mu    sync.RWMutex   // protects the inner-provider cache
	token string          // last access_token the inner was built with
	model string          // last model the inner was built with
	inner nli.Provider    // nil until first Score() call
}

// NewOAuthNLIProvider validates inputs and returns a thread-safe
// adapter.
//
//   - resolver MUST be non-nil (the parent module's
//     defaultDriftJudgeProvider guarantees this; tests may pass a
//     stub that panics-on-call to verify negative paths).
//   - providerName MUST be OAuthProviderGoogle or
//     OAuthProviderHuggingFace. Any other value returns an error
//     (defensive — adding a new provider requires both this file
//     AND the cmd sub-module's router knowing how to issue tokens).
//   - httpClient may be nil; production uses http.DefaultClient and
//     tests inject httptest.Server-bound clients.
func NewOAuthNLIProvider(resolver OAuthTokenResolver, providerName string, httpClient nli.HFInferenceClient) (*OAuthNLIProvider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("oauth_nli_provider: nil resolver")
	}
	switch providerName {
	case OAuthProviderGoogle, OAuthProviderHuggingFace:
		// ok — supported
	default:
		return nil, fmt.Errorf("oauth_nli_provider: unsupported provider %q (want %q or %q)",
			providerName, OAuthProviderGoogle, OAuthProviderHuggingFace)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OAuthNLIProvider{
		resolver:     resolver,
		providerName: providerName,
		httpClient:   httpClient,
	}, nil
}

// ID returns the provenance string for audit + logging.
//
// Format: "oauth-<provider>-<model>" where <model> is the
// canonical model id for the provider. Today:
//
//   - google      -> gemini-2.0-flash
//   - huggingface -> MoritzLaurer/DeBERTa-v3-large-mnli-fever-anli-ling-wanli
//
// The model portion is hardcoded because every Score() call goes
// through the same provider-defined model id (the resolver
// returns the canonical model name from providerInfo, never an
// override). Future work: allow per-project model override via
// Project.NLIConfig.Primary.ModelRev.
func (p *OAuthNLIProvider) ID() string {
	return "oauth-" + p.providerName + "-" + p.canonicalModel()
}

// canonicalModel is the model id used in ID() — must match what
// the resolver returns for providerInfo(p, StatusConnected). Kept
// here (rather than fetched on every call) because ID() is hot
// path: orchestrator logging fires on every Score.
func (p *OAuthNLIProvider) canonicalModel() string {
	switch p.providerName {
	case OAuthProviderGoogle:
		return "gemini-2.0-flash"
	case OAuthProviderHuggingFace:
		return "MoritzLaurer/DeBERTa-v3-large-mnli-fever-anli-ling-wanli"
	}
	return "unknown"
}

// Score is the nli.Provider bridge. See type doc for behavior.
//
// On ErrNoConnection or ErrConnectionExpired from the resolver,
// returns a wrapped error containing the human-friendly provider
// name (so drift_judge's verdict=needs_human path surfaces an
// actionable hint such as "OAuth connection for Google Gemini has
// expired; reconnect via dark_memory_oauth_connect").
//
// Other resolver errors (keystore I/O, network blip on
// refresh) are wrapped with "oauth_nli_provider: load token:"
// prefix. The drift_judge pipeline classifies these via
// errors.Is on the inner sealed errors (ErrProviderUnavailable,
// ErrProviderTimeout, etc.) — LoadActiveToken's underlying
// failure modes bubble up through the chain.
func (p *OAuthNLIProvider) Score(ctx context.Context, premise, hypothesis string) (nli.Score, error) {
	accessToken, endpoint, model, err := p.resolver.LoadActiveToken(ctx, p.providerName)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoConnection):
			return nli.Score{}, fmt.Errorf("oauth_nli_provider: %s has no connection — call dark_memory_oauth_connect to authenticate: %w",
				p.resolver.ProviderDisplayName(p.providerName), err)
		case errors.Is(err, ErrConnectionExpired):
			return nli.Score{}, fmt.Errorf("oauth_nli_provider: %s connection expired and cannot refresh — reconnect via dark_memory_oauth_connect: %w",
				p.resolver.ProviderDisplayName(p.providerName), err)
		}
		return nli.Score{}, fmt.Errorf("oauth_nli_provider: load token: %w", err)
	}

	inner, err := p.getOrBuildInner(accessToken, endpoint, model)
	if err != nil {
		return nli.Score{}, fmt.Errorf("oauth_nli_provider: build inner provider: %w", err)
	}
	return inner.Score(ctx, premise, hypothesis)
}

// getOrBuildInner returns the cached inner provider if the
// (token, endpoint, model) tuple matches the cache key;
// otherwise rebuilds. The lock pattern keeps concurrent Score
// calls in flight on the same inner provider (RLock fast path),
// while a token refresh in another call rebuilds without
// dropping in-flight requests (Lock blocks the next reader
// until the rebuild completes).
func (p *OAuthNLIProvider) getOrBuildInner(accessToken, endpoint, model string) (nli.Provider, error) {
	p.mu.RLock()
	if p.inner != nil && p.token == accessToken && p.model == model {
		inner := p.inner
		p.mu.RUnlock()
		return inner, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	// Double-check under the write lock: another goroutine may
	// have built it while we were waiting.
	if p.inner != nil && p.token == accessToken && p.model == model {
		return p.inner, nil
	}

	inner, err := buildInner(p.providerName, accessToken, endpoint, model, p.httpClient)
	if err != nil {
		return nil, err
	}
	p.inner = inner
	p.token = accessToken
	p.model = model
	return inner, nil
}

// buildInner constructs the appropriate nli.Provider for the
// given OAuth provider.
//
//   - google      -> nli.ChatProvider against
//                    <endpoint>/openai/chat/completions
//                    (Gemini OpenAI-compat mode)
//   - huggingface -> nli.DeBERTaProvider against
//                    <endpoint>/models/<model>
//                    (HF Inference API)
//
// ProviderID is operator-readable provenance (e.g.
// "chat-oauth-google"); ID() of the inner provider is what
// Score fields in the verdict's audit row.
//
// Endpoint resolution note: the resolver returns the BASE
// endpoint (e.g. https://generativelanguage.googleapis.com/
// v1beta); this function appends the path the underlying
// nli.Provider expects. Keeping the suffix logic in this
// file means each provider family owns its own URL shape and
// the resolver stays provider-agnostic.
func buildInner(providerName, accessToken, endpoint, model string, hc nli.HFInferenceClient) (nli.Provider, error) {
	switch providerName {
	case OAuthProviderGoogle:
		cfg := nli.ProviderConfig{
			ProviderID: "chat-oauth-google",
			Endpoint:   googleChatEndpoint(endpoint),
			AuthToken:  accessToken,
			ModelRev:   model,
			TimeoutMS:  nli.DefaultTimeoutMS,
		}
		return nli.NewChatProvider(cfg, hc, nli.DefaultMaxPremiseBytes, nli.DefaultMaxHypothesisBytes)
	case OAuthProviderHuggingFace:
		cfg := nli.ProviderConfig{
			ProviderID: "hf-oauth-deberta-v3-large-mnli",
			Endpoint:   huggingFaceDeBERTaEndpoint(endpoint, model),
			AuthToken:  accessToken,
			ModelRev:   model,
			TimeoutMS:  nli.DefaultTimeoutMS,
		}
		return nli.NewDeBERTaProvider(cfg, hc, nli.DefaultMaxPremiseBytes, nli.DefaultMaxHypothesisBytes)
	}
	return nil, fmt.Errorf("oauth_nli_provider: unknown provider %q", providerName)
}

// googleChatEndpoint converts the Gemini base endpoint into the
// OpenAI-compatible chat completions URL (the only path
// nli.ChatProvider understands).
//
//	https://generativelanguage.googleapis.com/v1beta
//	  -> https://generativelanguage.googleapis.com/v1beta/openai/chat/completions
//
// Idempotent: if the path is already present, returns it
// unchanged.
func googleChatEndpoint(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.Contains(base, "/openai/chat/completions") {
		return base
	}
	return base + "/openai/chat/completions"
}

// huggingFaceDeBERTaEndpoint builds the HF Inference URL for a
// model. The HF Inference API takes POST /models/{repo_id}; the
// resolver returns the API base (no trailing slash):
//
//	https://api-inference.huggingface.co
//	  -> https://api-inference.huggingface.co/models/<model>
//
// Idempotent and defensive against trailing slashes.
func huggingFaceDeBERTaEndpoint(base, model string) string {
	base = strings.TrimRight(base, "/")
	return base + "/models/" + model
}
