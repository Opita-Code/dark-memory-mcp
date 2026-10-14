// Package oauth — router_active_token_test.go: tests for Router.LoadActiveToken
// (Phase 23 Tier 1, Sesión 7, 2026-10-14, spec_id 1992).
//
// LoadActiveToken is the load-bearing primitive of the OAuth-backed
// judge path. These tests cover the four boundary cases:
//
//  1. Non-expired token: returned as-is, no refresh attempted.
//  2. Expired token + refresh_token present: refreshes via the
//     provider's token endpoint, persists the new TokenSet,
//     returns the new access_token.
//  3. Expired token + no refresh_token: ErrConnectionExpired
//     (the user MUST reconnect via dark_memory_oauth_connect).
//  4. No token in keystore: ErrNoConnection (the user has not
//     connected yet).
//
// The tests use ArrayKeystore for in-memory isolation and the
// existing pattern (osGetenvImpl override + googleEndpoint.TokenURL
// swap) to mock the Google token endpoint. Hermetic — no network,
// no real keyring.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
)

// TestRouter_LoadActiveToken_ReturnsValidToken verifies the happy
// path: a non-expired TokenSet is returned as-is with no refresh
// attempt. The endpoint + model come from providerInfo.
func TestRouter_LoadActiveToken_ReturnsValidToken(t *testing.T) {
	ks := ArrayKeystore()
	future := time.Now().Add(2 * time.Hour)
	if err := ks.SaveToken(ProviderGoogle, TokenSet{
		AccessToken:  "ya29.valid-token",
		RefreshToken: "1//refresh-kept-but-not-used",
		ExpiresAt:    future,
		Scopes:       DefaultGoogleScopes(),
	}); err != nil {
		t.Fatalf("setup SaveToken: %v", err)
	}

	router := NewRouter(ks, 0)

	accessToken, endpoint, model, err := router.LoadActiveToken(context.Background(), ProviderGoogle)
	if err != nil {
		t.Fatalf("LoadActiveToken: %v", err)
	}
	if accessToken != "ya29.valid-token" {
		t.Errorf("access_token=%q, want %q (must NOT refresh a non-expired token)",
			accessToken, "ya29.valid-token")
	}
	wantEndpoint, wantModel := endpointForModel(ProviderGoogle)
	if endpoint != wantEndpoint {
		t.Errorf("endpoint=%q, want %q", endpoint, wantEndpoint)
	}
	if model != wantModel {
		t.Errorf("model=%q, want %q", model, wantModel)
	}
}

// TestRouter_LoadActiveToken_AutoRefreshesWhenExpired verifies the
// transparent auto-refresh path. The pre-populated TokenSet is
// already expired; the test mock returns a fresh TokenSet from
// the Google token endpoint; we assert the new access_token is
// returned and persisted.
func TestRouter_LoadActiveToken_AutoRefreshesWhenExpired(t *testing.T) {
	// Mock Google's refresh endpoint. We accept BOTH the refresh_token
	// grant_type (refresh) and authorization_code (ExchangeCode used by
	// other tests) so a single mock can serve multiple scenarios.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Echo the grant_type so the test can verify the request was
		// actually a refresh (not a fresh exchange).
		grantType := r.FormValue("grant_type")
		if grantType != "refresh_token" {
			http.Error(w, fmt.Sprintf("expected grant_type=refresh_token, got %q", grantType),
				http.StatusBadRequest)
			return
		}
		if r.FormValue("refresh_token") == "" {
			http.Error(w, "missing refresh_token", http.StatusBadRequest)
			return
		}
		resp := map[string]any{
			"access_token":  "ya29.refreshed-token",
			"refresh_token": "1//rotated-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"scope":         "https://www.googleapis.com/auth/generative-language.retriever",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	// Swap the global token URL for the duration of the test.
	savedTokenURL := googleEndpoint.TokenURL
	googleEndpoint.TokenURL = mock.URL + "/token"
	t.Cleanup(func() {
		googleEndpoint.TokenURL = savedTokenURL
	})

	// Save and restore the env-var indirection. NewGoogleProvider
	// reads DARK_OAUTH_GOOGLE_CLIENT_ID / SECRET when called from
	// Router.refresh.
	savedEnv := osGetenvImpl
	osGetenvImpl = func(key string) string {
		switch key {
		case "DARK_OAUTH_GOOGLE_CLIENT_ID":
			return "mock-client-id"
		case "DARK_OAUTH_GOOGLE_CLIENT_SECRET":
			return "mock-client-secret"
		}
		return ""
	}
	t.Cleanup(func() {
		osGetenvImpl = savedEnv
	})

	// Pre-populate the keystore with an EXPIRED token + a
	// refresh_token. IsExpired() uses a 30s safety margin so we set
	// the expiry 1 hour in the past to be safe.
	ks := ArrayKeystore()
	if err := ks.SaveToken(ProviderGoogle, TokenSet{
		AccessToken:  "ya29.expired-token",
		RefreshToken: "1//refresh-this",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		Scopes:       DefaultGoogleScopes(),
	}); err != nil {
		t.Fatalf("setup SaveToken: %v", err)
	}

	router := NewRouter(ks, 0)

	accessToken, endpoint, model, err := router.LoadActiveToken(context.Background(), ProviderGoogle)
	if err != nil {
		t.Fatalf("LoadActiveToken (expired w/ refresh): %v", err)
	}
	if accessToken != "ya29.refreshed-token" {
		t.Errorf("access_token=%q, want %q (refresh should have produced the new value)",
			accessToken, "ya29.refreshed-token")
	}
	if endpoint == "" || model == "" {
		t.Errorf("endpoint/model should be populated: endpoint=%q model=%q", endpoint, model)
	}

	// Confirm the refreshed token was persisted: a second LoadActiveToken
	// should return the new value WITHOUT going to the network.
	// We detect "no network" by swapping the mock's URL to a dead one
	// — if LoadActiveToken re-attempted a refresh it would fail.
	googleEndpoint.TokenURL = "http://127.0.0.1:1/dead"
	accessToken2, _, _, err := router.LoadActiveToken(context.Background(), ProviderGoogle)
	if err != nil {
		t.Fatalf("second LoadActiveToken (after refresh, dead URL): %v", err)
	}
	if accessToken2 != "ya29.refreshed-token" {
		t.Errorf("second access_token=%q, want %q (the refresh should have been persisted)",
			accessToken2, "ya29.refreshed-token")
	}
}

// TestRouter_LoadActiveToken_ErrConnectionExpiredWhenNoRefresh
// verifies the sticky expired-no-refresh path. The token is past
// its expiry and there is no refresh_token; the method returns
// ErrConnectionExpired so the caller can surface an actionable
// hint to the operator.
func TestRouter_LoadActiveToken_ErrConnectionExpiredWhenNoRefresh(t *testing.T) {
	ks := ArrayKeystore()
	if err := ks.SaveToken(ProviderGoogle, TokenSet{
		AccessToken: "ya29.expired-no-refresh",
		// No RefreshToken — the user revoked the refresh grant, or
		// the provider never issued one. Either way, only a manual
		// reconnect can recover.
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		Scopes:    DefaultGoogleScopes(),
	}); err != nil {
		t.Fatalf("setup SaveToken: %v", err)
	}

	router := NewRouter(ks, 0)

	_, _, _, err := router.LoadActiveToken(context.Background(), ProviderGoogle)
	if err == nil {
		t.Fatal("expected ErrConnectionExpired; got nil")
	}
	if !errors.Is(err, orchestration.ErrConnectionExpired) {
		t.Fatalf("got err=%v, want errors.Is(err, orchestration.ErrConnectionExpired)", err)
	}
	// The router emits the bare sealed sentinel. The orchestrator
	// wraps it with the provider name via errMissingField before
	// it reaches the operator; tested separately in
	// internal/orchestration/oauth_nli_provider_test.go (the
	// HandlesConnectionExpired case).
}

// TestRouter_LoadActiveToken_ErrNoConnectionWhenNoToken verifies the
// boundary case: a fresh keystore has no entry for the provider.
// LoadActiveToken returns ErrNoConnection so the orchestrator can
// fall through to the next provider in orderedOAuthProviders.
func TestRouter_LoadActiveToken_ErrNoConnectionWhenNoToken(t *testing.T) {
	router := NewRouter(ArrayKeystore(), 0)

	_, _, _, err := router.LoadActiveToken(context.Background(), ProviderGoogle)
	if err == nil {
		t.Fatal("expected ErrNoConnection; got nil")
	}
	if !errors.Is(err, orchestration.ErrNoConnection) {
		t.Fatalf("got err=%v, want errors.Is(err, orchestration.ErrNoConnection)", err)
	}
}

// TestRouter_LoadActiveToken_HuggingFaceProvider verifies the
// router dispatches to the right per-provider Refresh when the
// caller asks for HuggingFace (catches a class of bugs where the
// Google path is reused for HF, which would break token rotation).
func TestRouter_LoadActiveToken_HuggingFaceProvider(t *testing.T) {
	// Mock HuggingFace's refresh endpoint. HF accepts client_id +
	// grant_type=refresh_token + refresh_token (and optional
	// client_secret for confidential clients).
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" {
			http.Error(w, "expected grant_type=refresh_token", http.StatusBadRequest)
			return
		}
		resp := map[string]any{
			"access_token":  "hf_refreshed",
			"refresh_token": "hf_rotated",
			"expires_in":    3600,
			"scope":         "openid profile inference-api",
			"token_type":    "bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	savedHF := hfTokenURL
	hfTokenURL = mock.URL + "/oauth/token"
	t.Cleanup(func() { hfTokenURL = savedHF })

	savedEnv := osGetenvImpl
	osGetenvImpl = func(key string) string {
		if key == "DARK_OAUTH_HUGGINGFACE_CLIENT_ID" {
			return "mock-hf-client-id"
		}
		return ""
	}
	t.Cleanup(func() { osGetenvImpl = savedEnv })

	ks := ArrayKeystore()
	if err := ks.SaveToken(ProviderHuggingFace, TokenSet{
		AccessToken:  "hf_old",
		RefreshToken: "hf_refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		Scopes:       DefaultHuggingFaceScopes(),
	}); err != nil {
		t.Fatalf("setup SaveToken: %v", err)
	}

	router := NewRouter(ks, 0)
	accessToken, endpoint, model, err := router.LoadActiveToken(context.Background(), ProviderHuggingFace)
	if err != nil {
		t.Fatalf("LoadActiveToken (HF expired): %v", err)
	}
	if accessToken != "hf_refreshed" {
		t.Errorf("access_token=%q, want %q", accessToken, "hf_refreshed")
	}
	if endpoint == "" || !strings.Contains(endpoint, "api-inference.huggingface.co") {
		t.Errorf("endpoint=%q, want HF base URL", endpoint)
	}
	if !strings.Contains(model, "DeBERTa") {
		t.Errorf("model=%q, want a DeBERTa model name", model)
	}
}
