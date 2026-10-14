// Package oauth implements OAuth 2.0 self-authentication for dark-memory
// judge providers. Users authenticate with their own provider account
// (Google, HuggingFace) so the operator never has to provision shared
// keys. Per RFC 6749 + RFC 7636 (PKCE).
//
// Phase 23 Tier 0 (Sesión 6, 2026-10-14): Google Gemini + HuggingFace Hub.
// Tier 1 (BYOK API key) and Tier 2 (self-hosted endpoint) live in other
// packages; this package is the Tier 0 surface only.
//
// Architecture:
//
//   user runs `dark_memory_oauth_connect({provider: "google"})`
//      ↓
//   oauth.Router generates code_verifier + code_challenge + state
//      ↓
//   returns auth_url + state to caller
//      ↓
//   user opens auth_url in any browser
//      ↓
//   user approves on Google/HuggingFace consent screen
//      ↓
//   Google/HuggingFace redirects to http://localhost:9335/oauth/callback?code=X&state=Y
//      ↓
//   oauth.Server (running in background) captures code + state
//      ↓
//   oauth.Router exchanges code → access_token + refresh_token
//      ↓
//   oauth.Keystore persists tokens to OS keyring (encrypted at rest)
//      ↓
//   oauth.Server shuts down (single-shot)
//      ↓
//   user can now use judge with their provider's free tier
package oauth

import (
	"time"
)

// Provider is the canonical identifier for an OAuth provider. New
// providers are added by appending here and wiring a corresponding
// file in this package (e.g., google.go, huggingface.go).
type Provider string

const (
	ProviderNone        Provider = ""
	ProviderGoogle      Provider = "google"
	ProviderHuggingFace Provider = "huggingface"
)

// AllProviders returns every supported Provider (used for discovery).
func AllProviders() []Provider {
	return []Provider{ProviderGoogle, ProviderHuggingFace}
}

// Flow describes the OAuth flow type a provider supports.
type Flow string

const (
	FlowAuthorizationCodePKCE Flow = "authorization_code_pkce"
	FlowDeviceCode            Flow = "device_code"
)

// ConnectionStatus describes the current state of a provider connection.
type ConnectionStatus string

const (
	StatusConnected    ConnectionStatus = "connected"
	StatusPending      ConnectionStatus = "pending"
	StatusExpired      ConnectionStatus = "expired"
	StatusError        ConnectionStatus = "error"
	StatusDisconnected ConnectionStatus = "disconnected"
)

// TokenSet holds the credentials obtained after a successful OAuth
// exchange. AccessToken is used for API calls; RefreshToken is used to
// obtain new AccessTokens when the current one expires (or is revoked).
// ExpiresAt is an absolute timestamp (UTC).
type TokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes,omitempty"`
	// AccountEmail is the user's email on the provider, if the
	// provider exposes it during token exchange (Google does via
	// the id_token claim; HuggingFace does not).
	AccountEmail string `json:"account_email,omitempty"`
}

// IsExpired returns true if the access_token's expiry has passed (with
// a small safety margin to avoid edge-of-expiry race conditions).
func (t TokenSet) IsExpired() bool {
	return time.Now().UTC().Add(30 * time.Second).After(t.ExpiresAt)
}

// CanRefresh returns true if the token set has a refresh token, which
// is needed to obtain a new access_token without user re-auth.
func (t TokenSet) CanRefresh() bool {
	return t.RefreshToken != ""
}

// ConnectRequest is the input for Router.Connect().
type ConnectRequest struct {
	// Provider to authenticate with (required).
	Provider Provider `json:"provider"`
	// Scopes to request (optional; provider default if empty).
	Scopes []string `json:"scopes,omitempty"`
	// RedirectURI overrides the default callback URL (optional;
	// only used for tests with a mock callback server).
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// ConnectResponse is returned by Router.Connect(). The caller opens
// auth_url in any browser. When the user approves, the provider
// redirects to the callback URL, where the oauth.Server captures the
// code and finalizes the flow.
type ConnectResponse struct {
	// AuthURL is the URL the user opens in their browser to approve.
	AuthURL string `json:"auth_url"`
	// State is the CSRF nonce; included in the callback URL. The
	// server validates state == pending.state before exchanging.
	State string `json:"state"`
	// ExpiresAt is when the auth_url becomes invalid (typically 5
	// minutes after Connect was called). After this time, callers
	// should call Connect again with a fresh state.
	ExpiresAt time.Time `json:"expires_at"`
	// Provider is echoed back so the caller knows what they initiated.
	Provider Provider `json:"provider"`
	// CallbackPort is the localhost port the oauth.Server is listening
	// on (default 9335). Surfaced for debugging only.
	CallbackPort int `json:"callback_port"`
	// Instructions is a human-readable summary of what the user
	// should do next (open auth_url, approve, wait for callback).
	Instructions string `json:"instructions"`
}

// PendingFlow tracks an in-flight OAuth flow. Created by Router.Connect,
// consumed by Router.handleCallback once the callback server receives
// the redirect.
type PendingFlow struct {
	Provider      Provider
	State         string
	CodeVerifier  string
	CodeChallenge string
	RedirectURI   string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	Callback      chan callbackResult // signals completion
}

// callbackResult is what the callback server writes to PendingFlow.Callback.
type callbackResult struct {
	Code  string
	State string
	Err   error
}

// ProviderInfo describes a supported OAuth provider for discovery
// (returned by JudgeOptions and OAuthStatus).
type ProviderInfo struct {
	Provider    Provider         `json:"provider"`
	DisplayName string           `json:"display_name"`
	Flows       []Flow           `json:"flows"`
	Scopes      []string         `json:"scopes,omitempty"`
	Endpoint    string           `json:"endpoint,omitempty"`
	Model       string           `json:"model,omitempty"`
	FreeTier    string           `json:"free_tier,omitempty"`
	Status      ConnectionStatus `json:"status"`
}

// ErrInvalidProvider is returned when a Provider value is not recognized.
type ErrInvalidProvider string

func (e ErrInvalidProvider) Error() string {
	return "oauth: invalid provider: " + string(e)
}

// DefaultCallbackPort is the localhost port the OAuth callback server
// listens on. Configurable via DARK_OAUTH_CALLBACK_PORT env var.
const DefaultCallbackPort = 9335

// DefaultFlowTimeout is how long an in-flight flow waits for the
// callback before being cancelled.
const DefaultFlowTimeout = 5 * time.Minute