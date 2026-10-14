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
)

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