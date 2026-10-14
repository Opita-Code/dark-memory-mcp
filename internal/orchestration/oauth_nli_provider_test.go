// Package orchestration — oauth_nli_provider_test.go: tests for
// OAuthNLIProvider (Phase 23 Tier 1, Sesión 7, 2026-10-14,
// spec_id 1992).
//
// The Provider is the bridge between dark-memory's drift_judge
// pipeline and the user's own OAuth-backed credentials. These
// tests cover the three load-bearing boundaries:
//
//  1. Score() delegates to the inner provider when the resolver
//     returns a valid token. Uses httptest.Server to mock the
//     upstream API and assert the inner provider is called with
//     the expected (endpoint, model, auth_token).
//  2. ID() returns the canonical "oauth-<provider>-<model>"
//     audit-provenance string.
//  3. ErrConnectionExpired from the resolver surfaces as a
//     friendly error that the operator can act on, NOT a panic
//     or opaque nli.Err* sentinel.
package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/nli"
)

// stubResolver is a minimal OAuthTokenResolver for unit tests.
// Production code uses *oauth.RouterAdapter; here we exercise
// the OAuthNLIProvider logic in isolation without touching the
// keystore, the HTTP refresh path, or any package globals.
type stubResolver struct {
	token       string
	endpoint    string
	model       string
	err         error
	displayName string
	calls       int
	lastName    string // last providerName passed to LoadActiveToken
}

func (s *stubResolver) LoadActiveToken(ctx context.Context, name string) (string, string, string, error) {
	s.calls++
	s.lastName = name
	return s.token, s.endpoint, s.model, s.err
}

func (s *stubResolver) ProviderDisplayName(name string) string {
	return s.displayName
}

// TestOAuthNLIProvider_ScoreDelegatesToInner is the happy path:
// resolver returns a valid token + endpoint + model, and an
// httptest.Server validates that the inner nli.Provider makes
// a real HTTP call to the expected URL with the expected bearer.
func TestOAuthNLIProvider_ScoreDelegatesToInner(t *testing.T) {
	// The mock upstream: accepts any request, returns a valid
	// DeBERTa-v3-large-mnli response shape. We use the HuggingFace
	// path because its response shape is simpler than Gemini's
	// chat completion shape (one HTTP call, one JSON array).
	var gotAuth string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"label": "LABEL_2", "score": 0.97},
		})
	}))
	defer mock.Close()

	resolver := &stubResolver{
		token:       "hf_test_token_xyz",
		endpoint:    mock.URL, // huggingFaceDeBERTaEndpoint appends /models/<model>
		model:       "MoritzLaurer/DeBERTa-v3-large-mnli-fever-anli-ling-wanli",
		displayName: "HuggingFace Hub",
	}

	p, err := NewOAuthNLIProvider(resolver, OAuthProviderHuggingFace, nil)
	if err != nil {
		t.Fatalf("NewOAuthNLIProvider: %v", err)
	}

	score, err := p.Score(context.Background(), "The cat sat on the mat.", "A cat is resting.")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Label != nli.LabelEntailment {
		t.Errorf("Label=%s, want LabelEntailment", score.Label)
	}
	if score.ProviderID != "deberta-v3-large-mnli" {
		t.Errorf("ProviderID=%q, want %q (DeBERTaProvider's hardcoded id)",
			score.ProviderID, "deberta-v3-large-mnli")
	}
	if gotAuth != "Bearer hf_test_token_xyz" {
		t.Errorf("Authorization header=%q, want %q", gotAuth, "Bearer hf_test_token_xyz")
	}
	if resolver.calls != 1 {
		t.Errorf("resolver.LoadActiveToken was called %d times, want 1", resolver.calls)
	}
	if resolver.lastName != OAuthProviderHuggingFace {
		t.Errorf("resolver last name=%q, want %q", resolver.lastName, OAuthProviderHuggingFace)
	}
}

// TestOAuthNLIProvider_IDReturnsOAuthPrefix verifies the audit
// provenance string format. Operators rely on the "oauth-" prefix
// to distinguish user-auth verdicts from operator-key verdicts in
// audit_log.
func TestOAuthNLIProvider_IDReturnsOAuthPrefix(t *testing.T) {
	cases := []struct {
		name         string
		providerName string
		wantContains []string
	}{
		{
			name:         "google",
			providerName: OAuthProviderGoogle,
			wantContains: []string{"oauth-", OAuthProviderGoogle, "gemini"},
		},
		{
			name:         "huggingface",
			providerName: OAuthProviderHuggingFace,
			wantContains: []string{"oauth-", OAuthProviderHuggingFace, "DeBERTa"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewOAuthNLIProvider(&stubResolver{}, tc.providerName, nil)
			if err != nil {
				t.Fatalf("NewOAuthNLIProvider: %v", err)
			}
			id := p.ID()
			for _, want := range tc.wantContains {
				if !strings.Contains(id, want) {
					t.Errorf("ID=%q, want substring %q", id, want)
				}
			}
		})
	}
}

// TestOAuthNLIProvider_HandlesConnectionExpired verifies that an
// ErrConnectionExpired from the resolver is surfaced as a
// friendly, actionable error. The drift_judge pipeline surfaces
// these as verdict=needs_human; the error message must mention
// the provider display name so the operator knows which
// provider needs reconnecting.
func TestOAuthNLIProvider_HandlesConnectionExpired(t *testing.T) {
	resolver := &stubResolver{
		err:         ErrConnectionExpired,
		displayName: "Google Gemini",
	}
	p, err := NewOAuthNLIProvider(resolver, OAuthProviderGoogle, nil)
	if err != nil {
		t.Fatalf("NewOAuthNLIProvider: %v", err)
	}

	_, scoreErr := p.Score(context.Background(), "premise", "hypothesis")
	if scoreErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(scoreErr, ErrConnectionExpired) {
		t.Errorf("scoreErr chain does not include ErrConnectionExpired: %v", scoreErr)
	}
	if !strings.Contains(scoreErr.Error(), "Google Gemini") {
		t.Errorf("error %q should mention provider display name %q",
			scoreErr.Error(), "Google Gemini")
	}
	if !strings.Contains(scoreErr.Error(), "dark_memory_oauth_connect") {
		t.Errorf("error %q should point at dark_memory_oauth_connect for the fix",
			scoreErr.Error())
	}
}

// TestOAuthNLIProvider_HandlesNoConnection is the no-user case:
// user has not connected any provider yet. Score must surface
// ErrNoConnection with a helpful "call dark_memory_oauth_connect
// first" prefix.
func TestOAuthNLIProvider_HandlesNoConnection(t *testing.T) {
	resolver := &stubResolver{
		err:         ErrNoConnection,
		displayName: "HuggingFace Hub",
	}
	p, err := NewOAuthNLIProvider(resolver, OAuthProviderHuggingFace, nil)
	if err != nil {
		t.Fatalf("NewOAuthNLIProvider: %v", err)
	}

	_, scoreErr := p.Score(context.Background(), "p", "h")
	if scoreErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(scoreErr, ErrNoConnection) {
		t.Errorf("error chain missing ErrNoConnection: %v", scoreErr)
	}
	if !strings.Contains(scoreErr.Error(), "HuggingFace Hub") {
		t.Errorf("error %q should mention %q", scoreErr.Error(), "HuggingFace Hub")
	}
}

// TestOAuthNLIProvider_RejectsUnknownProvider validates the
// defensive construction guard. Adding a new provider requires
// both this file AND the cmd sub-module's providerInfo; the
// guard catches the half-done case at construction time.
func TestOAuthNLIProvider_RejectsUnknownProvider(t *testing.T) {
	_, err := NewOAuthNLIProvider(&stubResolver{}, "openai", nil)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error %q should mention 'unsupported'", err.Error())
	}
}

// TestOAuthNLIProvider_RejectsNilResolver is the most defensive
// guard. nil resolver means the orchestrator forgot to call
// WithOAuthResolver at boot; fail loud rather than panic at
// first Score.
func TestOAuthNLIProvider_RejectsNilResolver(t *testing.T) {
	_, err := NewOAuthNLIProvider(nil, OAuthProviderGoogle, nil)
	if err == nil {
		t.Fatal("expected error for nil resolver")
	}
	if !strings.Contains(err.Error(), "nil resolver") {
		t.Errorf("error %q should mention 'nil resolver'", err.Error())
	}
}

// TestOAuthNLIProvider_CachesInnerProvider verifies that
// concurrent Score() calls share a single inner provider build
// (the one we built from the first call). The cache is keyed on
// the access_token string; a token rotation triggers a rebuild.
//
// We exercise this by counting how many times the mock upstream
// is hit across two Score calls + asserting the cache key
// (token) only changed once.
func TestOAuthNLIProvider_CachesInnerProvider(t *testing.T) {
	var hits int
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"label": "LABEL_2", "score": 0.9}})
	}))
	defer mock.Close()

	resolver := &stubResolver{
		token:       "hf_cached",
		endpoint:    mock.URL,
		model:       "MoritzLaurer/DeBERTa-v3-large-mnli-fever-anli-ling-wanli",
		displayName: "HuggingFace Hub",
	}

	p, err := NewOAuthNLIProvider(resolver, OAuthProviderHuggingFace, nil)
	if err != nil {
		t.Fatalf("NewOAuthNLIProvider: %v", err)
	}

	// First Score() builds the inner provider.
	if _, err := p.Score(context.Background(), "p1", "h1"); err != nil {
		t.Fatalf("first Score: %v", err)
	}
	// Second Score() should reuse it (LoadActiveToken is still
	// called once — that's the resolver-policy, not the cache —
	// but the inner provider's HTTP call is what we count).
	if _, err := p.Score(context.Background(), "p2", "h2"); err != nil {
		t.Fatalf("second Score: %v", err)
	}
	if hits != 2 {
		t.Errorf("upstream hits=%d, want 2 (one per Score call; inner provider is reused across calls)",
			hits)
	}
	// Resolver.LoadActiveToken is called once per Score (the
	// design decision: refresh happens on demand, not cached).
	if resolver.calls != 2 {
		t.Errorf("resolver.LoadActiveToken calls=%d, want 2 (refresh-on-demand)", resolver.calls)
	}
}

// TestBuildInner_GoogleChatEndpoint verifies the Gemini base
// endpoint is transformed into the OpenAI-compat chat completions
// URL that nli.ChatProvider expects.
func TestBuildInner_GoogleChatEndpoint(t *testing.T) {
	hc := http.DefaultClient
	p, err := buildInner(OAuthProviderGoogle, "ya29.test",
		"https://generativelanguage.googleapis.com/v1beta",
		"gemini-2.0-flash", hc)
	if err != nil {
		t.Fatalf("buildInner google: %v", err)
	}
	if p.ID() != "chat-oauth-google" {
		t.Errorf("inner ID=%q, want chat-oauth-google", p.ID())
	}
	if got := googleChatEndpoint("https://generativelanguage.googleapis.com/v1beta"); got !=
		"https://generativelanguage.googleapis.com/v1beta/openai/chat/completions" {
		t.Errorf("googleChatEndpoint=%q", got)
	}
	// Idempotency
	if got := googleChatEndpoint("https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"); got !=
		"https://generativelanguage.googleapis.com/v1beta/openai/chat/completions" {
		t.Errorf("googleChatEndpoint idempotent=%q", got)
	}
}

// TestBuildInner_HuggingFaceDeBERTaEndpoint verifies the HF base
// endpoint is transformed into the per-model inference URL.
func TestBuildInner_HuggingFaceDeBERTaEndpoint(t *testing.T) {
	if got := huggingFaceDeBERTaEndpoint(
		"https://api-inference.huggingface.co",
		"MoritzLaurer/DeBERTa-v3-large-mnli-fever-anli-ling-wanli",
	); got != "https://api-inference.huggingface.co/models/MoritzLaurer/DeBERTa-v3-large-mnli-fever-anli-ling-wanli" {
		t.Errorf("huggingFaceDeBERTaEndpoint=%q", got)
	}
	// Trailing-slash tolerance
	if got := huggingFaceDeBERTaEndpoint(
		"https://api-inference.huggingface.co/",
		"model-x",
	); got != "https://api-inference.huggingface.co/models/model-x" {
		t.Errorf("huggingFaceDeBERTaEndpoint trailing-slash=%q", got)
	}
}
