// Package orchestration — oauth_resolver.go: the boundary interface
// that lets the orchestrator reach OAuth-backed NLI providers
// without importing the cmd sub-module.
//
// # Architecture (Phase 23 Tier 1, Sesión 7, 2026-10-14)
//
// The orchestrator (parent module) cannot import
// cmd/dark-mem-mcp/internal/oauth (cmd sub-module) — Go's import
// direction is parent → child only, never the reverse. To give
// defaultDriftJudgeProvider a way to load tokens without breaking
// the layering rule, this file declares an interface (the
// "what") and the cmd sub-module provides a concrete
// implementation (the "how") that satisfies it via Go's
// structural typing (a.k.a. duck typing).
//
// The orchestrator calls WithOAuthResolver at boot with the
// RouterAdapter constructed in legacy_main.go. nil is allowed
// (legacy path returns errMissingField); non-nil is the Tier 1
// self-auth path.
//
// # Concurrency
//
// The interface methods MUST be safe for concurrent invocation.
// The orchestrator's DriftJudge pipeline can issue multiple
// parallel Score calls across providers; LoadActiveToken must
// tolerate this without racing on the keystore underneath.
package orchestration

import (
	"context"
	"errors"
)

// OAuthTokenResolver is the boundary contract the orchestrator
// uses to reach OAuth-backed NLI providers.
//
// Implementations live in the cmd sub-module and MUST satisfy
// this interface via Go's structural typing (no `implements`
// keyword). The cmd sub-module's RouterAdapter is the production
// implementation; tests may inject a stub.
//
// Hard invariants:
//
//   - LoadActiveToken transparently refreshes expired tokens
//     when a refresh_token is available. The orchestrator calls
//     this once per drift_judge pipeline step, so a refresh that
//     happens mid-flight MUST be visible to the next call.
//   - LoadActiveToken returns a NON-EXPIRED access_token on
//     success. The orchestrator uses this token as a Bearer
//     header on the next NLI HTTP call; an expired token would
//     surface as a 401 from the upstream provider.
//   - Errors are sealed: ErrNoConnection (no token persisted)
//     and ErrConnectionExpired (token persisted, refresh_token
//     missing). Other errors are wrapped with context.
//
// See cmd/dark-mem-mcp/internal/oauth/resolver_adapter.go for
// the production implementation.
type OAuthTokenResolver interface {
	// LoadActiveToken returns a fresh, non-expired access_token
	// for the named provider, plus the endpoint base URL and the
	// recommended model id. The endpoint + model are returned
	// alongside the token so callers can build their NLI
	// provider config in one round-trip (avoiding two
	// keystore-level operations).
	//
	// Errors:
	//   - ErrNoConnection: no token persisted for this provider
	//     (the user has not connected yet). Callers should treat
	//     this as "skip this provider, try the next one".
	//   - ErrConnectionExpired: token persisted but past its
	//     expiry, and no refresh_token is available. Callers
	//     should surface this as an actionable error (the user
	//     must reconnect).
	//   - Other: transport or store failure; wrapped with context.
	LoadActiveToken(ctx context.Context, providerName string) (accessToken string, endpoint string, model string, err error)

	// ProviderDisplayName returns a human-friendly name for the
	// given provider key (e.g. "google" -> "Google Gemini",
	// "huggingface" -> "HuggingFace Hub"). Used in actionable
	// error messages surfaced to the operator in needs_human
	// verdicts.
	ProviderDisplayName(providerName string) string
}

// OAuth provider names. Defined here (in the parent module) as
// constants because defaultDriftJudgeProvider iterates them in
// priority order, and the orchestrator owns that iteration
// policy. The values MUST match the canonical names returned by
// the cmd sub-module's oauth package (oauth.Provider).
const (
	OAuthProviderGoogle      = "google"
	OAuthProviderHuggingFace = "huggingface"
)

// orderedOAuthProviders is the iteration order used by
// defaultDriftJudgeProvider. Google first (chat-completion API,
// lower latency for short premises typical of drift_judge
// scores); HuggingFace second (DeBERTa-v3-large-mnli on the
// free Inference API, slower but higher-precision model).
//
// Adjusting the order here is an operator-controlled knob. To
// prefer HuggingFace, swap the two entries. To add a new
// provider, append the constant + new entry; ensure the cmd
// sub-module's providerInfo knows the new key.
var orderedOAuthProviders = []string{
	OAuthProviderGoogle,
	OAuthProviderHuggingFace,
}

// Sealed error set. Callers MUST use errors.Is to classify; no
// string matching. The orchestrator wraps these in errMissingField
// for the tool layer to surface as verdict=needs_human in the
// drift_judge pipeline.
var (
	// ErrNoConnection signals the user has not connected the
	// requested provider via dark_memory_oauth_connect yet.
	// defaultDriftJudgeProvider skips this provider and tries the
	// next one (silent fall-through).
	ErrNoConnection = errors.New("oauth: no connection for provider")

	// ErrConnectionExpired signals the persisted token is past
	// its expiry and no refresh_token is available. The user
	// MUST reconnect (dark_memory_oauth_connect) to obtain a
	// fresh token. This is a sticky error — surfaced to the
	// operator immediately, no fall-through.
	ErrConnectionExpired = errors.New("oauth: connection expired and refresh unavailable")
)
