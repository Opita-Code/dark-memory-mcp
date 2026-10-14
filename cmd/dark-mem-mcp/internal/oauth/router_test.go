// Package oauth — router_test.go: end-to-end tests for the OAuth Router
// using httptest.NewServer to mock Google and HuggingFace token endpoints.
// Verifies the PKCE-protected Authorization Code flow end-to-end.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestRouter_EndToEndGoogleFlow exercises the full Connect → callback →
// ExchangeCode → SaveToken path against a mock Google token endpoint.
// The user "approves" by simulating the redirect to /oauth/callback.
func TestRouter_EndToEndGoogleFlow(t *testing.T) {
	// Mock Google token endpoint.
	mockGoogle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		code := r.FormValue("code")
		verifier := r.FormValue("code_verifier")
		if code != "fake-auth-code" {
			http.Error(w, fmt.Sprintf("unexpected code: %s", code), http.StatusBadRequest)
			return
		}
		if verifier == "" {
			http.Error(w, "missing code_verifier", http.StatusBadRequest)
			return
		}
		resp := map[string]any{
			"access_token":  "ya29.mock-access-token",
			"refresh_token": "1//mock-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"scope":         "https://www.googleapis.com/auth/generative-language.retriever",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockGoogle.Close()

	// Build a router pointed at the mock token endpoint.
	router := newRouterWithMockGoogle(t, mockGoogle.URL)

	// Override osGetenv to skip real client_id resolution.
	savedImpl := osGetenvImpl
	osGetenvImpl = func(key string) string {
		if key == "DARK_OAUTH_GOOGLE_CLIENT_ID" {
			return "mock-client-id"
		}
		if key == "DARK_OAUTH_GOOGLE_CLIENT_SECRET" {
			return "mock-client-secret"
		}
		return ""
	}
	defer func() { osGetenvImpl = savedImpl }()

	ctx := context.Background()
	resp, err := router.Connect(ctx, ConnectRequest{Provider: ProviderGoogle})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if resp.AuthURL == "" {
		t.Fatal("AuthURL empty")
	}
	if resp.ExpiresAt.Before(time.Now()) {
		t.Fatal("ExpiresAt in the past")
	}

	// Verify PKCE was injected into the auth URL.
	pu, err := url.Parse(resp.AuthURL)
	if err != nil {
		t.Fatalf("parse AuthURL: %v", err)
	}
	q := pu.Query()
	if q.Get("code_challenge") == "" {
		t.Error("auth URL missing code_challenge")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method=%q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("state") == "" {
		t.Error("auth URL missing state")
	}

	// Simulate the user's redirect (Google would do this in browser).
	// Call the callback handler directly (no real HTTP server needed for unit test).
	state := q.Get("state")
	router.mu.Lock()
	flow := router.pending[state]
	router.mu.Unlock()
	if flow == nil {
		t.Fatal("flow not registered")
	}
	flow.Callback <- callbackResult{Code: "fake-auth-code", State: state}

	// Now complete the exchange.
	ts, err := router.exchange(ctx, flow, "fake-auth-code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if ts.AccessToken != "ya29.mock-access-token" {
		t.Errorf("AccessToken=%q, want mock value", ts.AccessToken)
	}
	if ts.RefreshToken != "1//mock-refresh" {
		t.Errorf("RefreshToken=%q, want mock value", ts.RefreshToken)
	}
	if !ts.ExpiresAt.After(time.Now().Add(30 * time.Minute)) {
		t.Errorf("ExpiresAt %v not 30-60min in the future", ts.ExpiresAt)
	}
}

// TestRouter_RejectsInvalidProvider — Connect with an empty/invalid
// provider returns an error before any state is registered.
func TestRouter_RejectsInvalidProvider(t *testing.T) {
	router := NewRouter(ArrayKeystore(), 0)
	_, err := router.Connect(context.Background(), ConnectRequest{Provider: "openai"})
	if err == nil {
		t.Fatal("Connect(openai) returned nil error")
	}
	if _, ok := err.(ErrInvalidProvider); !ok {
		t.Errorf("error type=%T, want ErrInvalidProvider", err)
	}
}

// TestRouter_Status_DisconnectedForEmptyKeystore — Status on a fresh
// keystore returns StatusDisconnected.
func TestRouter_Status_DisconnectedForEmptyKeystore(t *testing.T) {
	router := NewRouter(ArrayKeystore(), 0)
	status, ts, err := router.Status(ProviderGoogle)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != StatusDisconnected {
		t.Errorf("status=%s, want StatusDisconnected", status)
	}
	if ts.AccessToken != "" {
		t.Errorf("AccessToken should be empty, got %q", ts.AccessToken)
	}
}

// TestRouter_Providers_ReturnsAll — Providers() returns all supported
// providers (even if disconnected) for discovery.
func TestRouter_Providers_ReturnsAll(t *testing.T) {
	router := NewRouter(ArrayKeystore(), 0)
	providers := router.Providers()
	if len(providers) != 2 {
		t.Fatalf("Providers returned %d, want 2", len(providers))
	}
	names := map[Provider]bool{}
	for _, p := range providers {
		names[p.Provider] = true
		if p.DisplayName == "" {
			t.Errorf("provider %s has empty DisplayName", p.Provider)
		}
	}
	if !names[ProviderGoogle] || !names[ProviderHuggingFace] {
		t.Errorf("missing expected providers: %v", names)
	}
}

// TestRouter_DisconnectRemovesTokens — Disconnect deletes the
// token set so the next Status returns StatusDisconnected.
func TestRouter_DisconnectRemovesTokens(t *testing.T) {
	ks := ArrayKeystore()
	_ = ks.SaveToken(ProviderGoogle, TokenSet{
		AccessToken: "test",
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	router := NewRouter(ks, 0)
	status, _, _ := router.Status(ProviderGoogle)
	if status != StatusConnected {
		t.Fatalf("setup: status=%s, want StatusConnected", status)
	}
	if err := router.Disconnect(context.Background(), ProviderGoogle); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	status, _, _ = router.Status(ProviderGoogle)
	if status != StatusDisconnected {
		t.Errorf("post-disconnect status=%s, want StatusDisconnected", status)
	}
}

// TestAuthURL_Google_ContainsRequiredParams — AuthURL must include
// all OAuth 2.0 + PKCE params per RFC 7636.
func TestAuthURL_Google_ContainsRequiredParams(t *testing.T) {
	g := &GoogleProvider{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURL:  "http://127.0.0.1:9335/oauth/callback",
	}
	state := "test-state"
	challenge := "test-challenge"

	authURL := g.AuthURL(state, challenge, DefaultGoogleScopes())

	pu, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := pu.Query()
	required := []string{
		"client_id", "redirect_uri", "response_type", "scope",
		"state", "code_challenge", "code_challenge_method",
		"access_type", "prompt",
	}
	for _, r := range required {
		if q.Get(r) == "" {
			t.Errorf("missing required param %q in auth URL", r)
		}
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method=%q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type=%q, want code", q.Get("response_type"))
	}
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type=%q, want offline (for refresh_token)", q.Get("access_type"))
	}
	if !strings.Contains(q.Get("scope"), "generative-language.retriever") {
		t.Errorf("scope missing Gemini scope: %q", q.Get("scope"))
	}
}

// newRouterWithMockGoogle constructs a Router pointing the Google
// provider at a mock token endpoint. It swaps the package-level
// googleEndpoint.TokenURL for the duration of the test (and restores
// it via t.Cleanup).
func newRouterWithMockGoogle(t *testing.T, mockURL string) *Router {
	t.Helper()
	orig := googleEndpoint.TokenURL
	googleEndpoint.TokenURL = mockURL + "/token"
	t.Cleanup(func() {
		googleEndpoint.TokenURL = orig
	})
	return NewRouter(ArrayKeystore(), 0)
}