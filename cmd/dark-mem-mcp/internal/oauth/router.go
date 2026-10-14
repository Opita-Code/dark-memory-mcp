// Package oauth — router.go: top-level flow state machine for OAuth
// self-auth. Coordinates PKCE generation, callback server lifecycle,
// provider-specific token exchange, and keystore persistence.
//
// One Router per process. Created at server boot; shared across all
// OAuth tool calls and the judge orchestrator.
package oauth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
)

// orchestrationErr is a short alias to keep the imports section
// readable. We import internal/orchestration for the sealed error
// sentinels (ErrNoConnection, ErrConnectionExpired) that the
// parent module's OAuthTokenResolver interface contract returns
// from LoadActiveToken. See internal/orchestration/oauth_resolver.go.

// Router is the central OAuth coordinator.
type Router struct {
	mu        sync.Mutex
	pending   map[string]*PendingFlow // keyed by state
	keystore  *Keystore
	server    *Server
	port      int
}

// NewRouter constructs a Router with the given keystore and callback port.
func NewRouter(ks *Keystore, port int) *Router {
	if port <= 0 {
		port = DefaultCallbackPort
	}
	return &Router{
		pending:  make(map[string]*PendingFlow),
		keystore: ks,
		server:   NewServer(port),
		port:     port,
	}
}

// Server returns the underlying callback server (for diagnostics).
func (r *Router) Server() *Server {
	return r.server
}

// Connect initiates an OAuth flow for the given provider. Generates
// PKCE pair, state nonce, and returns the auth_url the user opens in
// any browser. The callback server is started on the configured port
// and will receive the redirect when the user approves.
func (r *Router) Connect(ctx context.Context, req ConnectRequest) (ConnectResponse, error) {
	if req.Provider == ProviderNone {
		return ConnectResponse{}, fmt.Errorf("oauth: provider required")
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return ConnectResponse{}, fmt.Errorf("oauth: generate PKCE: %w", err)
	}
	state, err := GenerateState()
	if err != nil {
		return ConnectResponse{}, fmt.Errorf("oauth: generate state: %w", err)
	}

	scopes := req.Scopes
	if len(scopes) == 0 {
		switch req.Provider {
		case ProviderGoogle:
			scopes = DefaultGoogleScopes()
		case ProviderHuggingFace:
			scopes = DefaultHuggingFaceScopes()
		}
	}

	redirectURL := req.RedirectURI
	if redirectURL == "" {
		redirectURL = r.server.CallbackURL()
	}

	// Build the auth_url based on provider.
	var authURL string
	switch req.Provider {
	case ProviderGoogle:
		g := NewGoogleProvider(redirectURL)
		authURL = g.AuthURL(state, challenge, scopes)
	case ProviderHuggingFace:
		hf := NewHuggingFaceProvider(redirectURL)
		authURL = hf.AuthURL(state, challenge, scopes)
	default:
		return ConnectResponse{}, ErrInvalidProvider(string(req.Provider))
	}

	// Register the pending flow so the callback handler can match state.
	flow := &PendingFlow{
		Provider:      req.Provider,
		State:         state,
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
		RedirectURI:   redirectURL,
		CreatedAt:     time.Now().UTC(),
		ExpiresAt:     time.Now().UTC().Add(DefaultFlowTimeout),
		Callback:      make(chan callbackResult, 1),
	}
	r.mu.Lock()
	r.pending[state] = flow
	r.mu.Unlock()

	// Start the callback server (idempotent — if already running for
	// a different flow, returns an error).
	if err := r.server.Start(flow); err != nil {
		r.mu.Lock()
		delete(r.pending, state)
		r.mu.Unlock()
		return ConnectResponse{}, fmt.Errorf("oauth: start callback server: %w", err)
	}

	return ConnectResponse{
		AuthURL:       authURL,
		State:         state,
		ExpiresAt:     flow.ExpiresAt,
		Provider:      req.Provider,
		CallbackPort:  r.server.Port(),
		Instructions:  fmt.Sprintf("Open this URL in any browser, approve, and return to your harness: %s", authURL),
	}, nil
}

// WaitForCallback blocks until the user approves (or the flow expires).
// Returns the authorization code + state from the provider's redirect.
// Caller should then call ExchangeCode with the verifier.
func (r *Router) WaitForCallback(ctx context.Context, state string) (string, error) {
	r.mu.Lock()
	flow, ok := r.pending[state]
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("oauth: no pending flow for state=%s", state)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(time.Until(flow.ExpiresAt)):
		return "", fmt.Errorf("oauth: flow expired")
	case res := <-flow.Callback:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Code, nil
	}
}

// ExchangeAndStore takes the authorization code from WaitForCallback
// and exchanges it for tokens, then persists via the keystore.
func (r *Router) ExchangeAndStore(ctx context.Context, state string) (TokenSet, error) {
	r.mu.Lock()
	flow, ok := r.pending[state]
	r.mu.Unlock()
	if !ok {
		return TokenSet{}, fmt.Errorf("oauth: no pending flow for state=%s", state)
	}
	code, err := r.WaitForCallback(ctx, state)
	if err != nil {
		return TokenSet{}, err
	}
	ts, err := r.exchange(ctx, flow, code)
	if err != nil {
		return TokenSet{}, err
	}
	if err := r.keystore.SaveToken(flow.Provider, ts); err != nil {
		return TokenSet{}, fmt.Errorf("oauth: persist token: %w", err)
	}
	// Cleanup
	r.mu.Lock()
	delete(r.pending, state)
	r.mu.Unlock()
	return ts, nil
}

// exchange dispatches to the per-provider token exchange.
func (r *Router) exchange(ctx context.Context, flow *PendingFlow, code string) (TokenSet, error) {
	scopes := flowScopes(flow)
	switch flow.Provider {
	case ProviderGoogle:
		g := NewGoogleProvider(flow.RedirectURI)
		return g.ExchangeCode(ctx, code, flow.CodeVerifier, scopes)
	case ProviderHuggingFace:
		hf := NewHuggingFaceProvider(flow.RedirectURI)
		return hf.ExchangeCode(ctx, code, flow.CodeVerifier, scopes)
	default:
		return TokenSet{}, ErrInvalidProvider(string(flow.Provider))
	}
}

// flowScopes returns the scopes the flow was started with. We don't
// store this in PendingFlow (saves memory); callers compute it on
// demand. The set is provider-specific.
func flowScopes(flow *PendingFlow) []string {
	switch flow.Provider {
	case ProviderGoogle:
		return DefaultGoogleScopes()
	case ProviderHuggingFace:
		return DefaultHuggingFaceScopes()
	}
	return nil
}

// Status returns the current connection status for the given provider.
func (r *Router) Status(provider Provider) (ConnectionStatus, TokenSet, error) {
	ts, found, err := r.keystore.LoadToken(provider)
	if err != nil {
		return StatusError, TokenSet{}, err
	}
	if !found {
		return StatusDisconnected, TokenSet{}, nil
	}
	if ts.IsExpired() {
		if ts.CanRefresh() {
			return StatusExpired, ts, nil
		}
		return StatusError, ts, fmt.Errorf("oauth: token expired and no refresh token")
	}
	return StatusConnected, ts, nil
}

// Disconnect removes the stored tokens for the given provider and
// (best-effort) revokes them on the provider's side.
func (r *Router) Disconnect(ctx context.Context, provider Provider) error {
	ts, found, err := r.keystore.LoadToken(provider)
	if err != nil {
		return err
	}
	if found {
		switch provider {
		case ProviderGoogle:
			g := NewGoogleProvider("")
			_ = g.Revoke(ctx, ts.AccessToken)
		case ProviderHuggingFace:
			// HF doesn't expose a public revoke endpoint in the free
			// tier; tokens simply expire on their own.
		}
	}
	return r.keystore.DeleteToken(provider)
}

// Providers returns metadata for all supported providers with their
// current connection status.
func (r *Router) Providers() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(AllProviders()))
	for _, p := range AllProviders() {
		status, _, _ := r.Status(p)
		out = append(out, providerInfo(p, status))
	}
	return out
}

// providerInfo returns metadata for a single provider.
func providerInfo(p Provider, status ConnectionStatus) ProviderInfo {
	switch p {
	case ProviderGoogle:
		return ProviderInfo{
			Provider:    p,
			DisplayName: "Google Gemini",
			Flows:       []Flow{FlowAuthorizationCodePKCE},
			Scopes:      DefaultGoogleScopes(),
			Endpoint:    "https://generativelanguage.googleapis.com/v1beta",
			Model:       "gemini-2.0-flash",
			FreeTier:    "60 RPM, 1500 RPD (Google free tier)",
			Status:      status,
		}
	case ProviderHuggingFace:
		return ProviderInfo{
			Provider:    p,
			DisplayName: "HuggingFace Hub",
			Flows:       []Flow{FlowAuthorizationCodePKCE, FlowDeviceCode},
			Scopes:      DefaultHuggingFaceScopes(),
			Endpoint:    "https://api-inference.huggingface.co",
			Model:       "MoritzLaurer/DeBERTa-v3-large-mnli-fever-anli-ling-wanli",
			FreeTier:    "1000 RPD (HuggingFace Inference API)",
			Status:      status,
		}
	}
	return ProviderInfo{Provider: p, Status: status}
}

// displayNameFor returns the human-friendly provider name from
// providerInfo. Used by RouterAdapter.ProviderDisplayName to
// satisfy the parent module's OAuthTokenResolver interface.
func displayNameFor(p Provider) string {
	return providerInfo(p, StatusDisconnected).DisplayName
}

// endpointForModel returns the base endpoint + canonical model id
// for the provider, looked up from providerInfo. Used by
// RouterAdapter.LoadActiveToken so the caller (Judge pipeline)
// knows where to direct HTTP calls and which model to populate
// in the request payload.
func endpointForModel(p Provider) (endpoint, model string) {
	info := providerInfo(p, StatusConnected)
	return info.Endpoint, info.Model
}

// LoadActiveToken returns a fresh, non-expired access_token for
// the given provider along with the endpoint + model the caller
// should target. The accessor transparently refreshes expired
// tokens when a refresh_token is available, persisting the new
// TokenSet via the keystore before returning.
//
// This is the Tier 1 self-auth load-bearing primitive: every
// drift_judge call into the OAuth path goes through here. It is
// invoked by RouterAdapter.LoadActiveToken (the implementation
// of the parent module's OAuthTokenResolver interface) which
// sits in front of OAuthNLIProvider.Score.
//
// Errors (sealed):
//   - ErrNoConnection: no token persisted. The user has not
//     connected yet (or disconnected recently). Callers
//     typically skip the provider.
//   - ErrConnectionExpired: token persisted but past its
//     expiry and no refresh_token is available. The user MUST
//     reconnect. Surfaced to the operator as an actionable
//     error — never silently retried.
//   - Other: transport, keystore, or refresh-endpoint failure;
//     wrapped with context.
//
// Endpoint semantics:
//   - google       -> https://generativelanguage.googleapis.com/v1beta
//   - huggingface  -> https://api-inference.huggingface.co
//
// These are the BASE endpoints. Callers (OAuthNLIProvider) append
// the per-protocol path (/openai/chat/completions for Gemini,
// /models/<model> for HF DeBERTa).
func (r *Router) LoadActiveToken(ctx context.Context, p Provider) (accessToken, endpoint, model string, err error) {
	ts, found, err := r.keystore.LoadToken(p)
	if err != nil {
		return "", "", "", fmt.Errorf("oauth: load token from keystore: %w", err)
	}
	if !found {
		return "", "", "", orchestration.ErrNoConnection
	}
	if ts.IsExpired() {
		// Try a refresh before giving up. This is the only path
		// that keeps long-lived deployments working without user
		// intervention: Google rotates access_tokens every ~1h,
		// and HF rotates them similarly. Without auto-refresh,
		// every judgment would need a manual reconnect every hour.
		if !ts.CanRefresh() {
			return "", "", "", orchestration.ErrConnectionExpired
		}
		refreshed, refreshErr := r.refresh(ctx, p, ts)
		if refreshErr != nil {
			// Refresh failed (revoked refresh_token, network blip,
			// provider outage). Surface the failure with context
			// so the operator knows the user must reconnect.
			return "", "", "", fmt.Errorf("oauth: refresh token for %s: %w", p, refreshErr)
		}
		ts = refreshed
		// Persist the refreshed token so subsequent calls don't
		// hit the provider again. Best-effort; if the keystore
		// write fails we still return the new token (caller can
		// use it once; next call will refresh again).
		if saveErr := r.keystore.SaveToken(p, ts); saveErr != nil {
			return ts.AccessToken, "", "", fmt.Errorf("oauth: persist refreshed token: %w", saveErr)
		}
	}
	endpoint, model = endpointForModel(p)
	return ts.AccessToken, endpoint, model, nil
}

// refresh dispatches the refresh call to the per-provider
// implementation. Returns the new TokenSet (with refreshed
// access_token + possibly rotated refresh_token). On error the
// TokenSet is zero-valued and the err explains why.
//
// We pass the original scopes (preserved from the keystore) so
// the refreshed token requests exactly what the user originally
// approved. Adding scopes would silently require re-consent on
// the provider's side.
func (r *Router) refresh(ctx context.Context, p Provider, ts TokenSet) (TokenSet, error) {
	scopes := ts.Scopes
	if len(scopes) == 0 {
		// Fallback to defaults if scopes were lost (older
		// keystore entries without the field).
		switch p {
		case ProviderGoogle:
			scopes = DefaultGoogleScopes()
		case ProviderHuggingFace:
			scopes = DefaultHuggingFaceScopes()
		}
	}
	switch p {
	case ProviderGoogle:
		g := NewGoogleProvider("")
		return g.Refresh(ctx, ts.RefreshToken, scopes)
	case ProviderHuggingFace:
		hf := NewHuggingFaceProvider("")
		return hf.Refresh(ctx, ts.RefreshToken, scopes)
	default:
		return TokenSet{}, fmt.Errorf("oauth: refresh unsupported for provider %q", p)
	}
}