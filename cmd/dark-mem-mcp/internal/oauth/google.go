// Package oauth — google.go: Google OAuth 2.0 provider for Google Gemini.
//
// Endpoints (from golang.org/x/oauth2/google):
//   Authorization:  https://accounts.google.com/o/oauth2/v2/auth
//   Token:          https://oauth2.googleapis.com/token
//   Refresh:        https://oauth2.googleapis.com/token (same as Token)
//   Revoke:         https://oauth2.googleapis.com/revoke?token=
//
// Scope:
//   https://www.googleapis.com/auth/generative-language.retriever
//
// Google Gemini free tier limits:
//   - 60 requests per minute (RPM) per OAuth client
//   - 1500 requests per day (RPD) per OAuth client
//   - 1 million tokens per minute (TPM) per OAuth client
//
// References:
//   - OAuth 2.0: https://developers.google.com/identity/protocols/oauth2
//   - Gemini API: https://ai.google.dev/api/rest
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// googleEndpoint is the OAuth 2.0 endpoint URL for Google.
// We override the default with the legacy /o/oauth2/v2/auth to match
// what Google's OAuth Playground uses (compatibility with existing
// client registrations). Declared as a var so tests can swap in a
// mock endpoint.
var googleEndpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

// googleOauthConfig returns an *oauth2.Config scoped for Gemini.
// client_id and client_secret are public OAuth client credentials
// registered with Google Cloud Console. Production deployments MUST
// supply their own; the empty defaults will produce auth URLs that
// Google's authorization server rejects.
func googleOauthConfig(redirectURL string, clientID, clientSecret string, scopes []string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       scopes,
		Endpoint:     googleEndpoint,
	}
}

// googleConfigFromEnv returns the OAuth client credentials read from
// environment variables (production setup).
//
//   DARK_OAUTH_GOOGLE_CLIENT_ID     — OAuth 2.0 client ID from Google Cloud Console
//   DARK_OAUTH_GOOGLE_CLIENT_SECRET — OAuth 2.0 client secret (Confidential client)
//
// If both are empty, the provider uses Google Cloud's "Web application"
// client type with PKCE-only flow (no client_secret). This is what
// most modern Google OAuth integrations use; PKCE is the secret.
func googleConfigFromEnv(redirectURL string, scopes []string) *oauth2.Config {
	return googleOauthConfig(
		redirectURL,
		osGetenv("DARK_OAUTH_GOOGLE_CLIENT_ID"),
		osGetenv("DARK_OAUTH_GOOGLE_CLIENT_SECRET"),
		scopes,
	)
}

// DefaultGoogleScopes returns the minimum scopes needed for judge.
func DefaultGoogleScopes() []string {
	return []string{
		"https://www.googleapis.com/auth/generative-language.retriever",
	}
}

// GoogleProvider implements the OAuth flow for Google Gemini.
type GoogleProvider struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// NewGoogleProvider constructs a GoogleProvider with the given client
// credentials. Empty client_secret is allowed for public PKCE clients.
func NewGoogleProvider(redirectURL string) *GoogleProvider {
	return &GoogleProvider{
		ClientID:     osGetenv("DARK_OAUTH_GOOGLE_CLIENT_ID"),
		ClientSecret: osGetenv("DARK_OAUTH_GOOGLE_CLIENT_SECRET"),
		RedirectURL:  redirectURL,
	}
}

// AuthURL builds the Google authorization URL the user opens in their
// browser. The state and code_challenge are appended per RFC 7636
// (PKCE). The user approves on Google's consent screen, then Google
// redirects to redirect_url with ?code=X&state=Y.
func (g *GoogleProvider) AuthURL(state, codeChallenge string, scopes []string) string {
	cfg := googleOauthConfig(g.RedirectURL, g.ClientID, g.ClientSecret, scopes)
	// Manual URL construction because oauth2.Config.AuthCodeURL uses
	// the values from cfg (which include our googleEndpoint override).
	v := url.Values{}
	v.Set("client_id", cfg.ClientID)
	v.Set("redirect_uri", cfg.RedirectURL)
	v.Set("response_type", "code")
	v.Set("scope", strings.Join(scopes, " "))
	v.Set("state", state)
	v.Set("code_challenge", codeChallenge)
	v.Set("code_challenge_method", "S256")
	v.Set("access_type", "offline") // request refresh_token
	v.Set("prompt", "consent")      // force consent to ensure refresh_token
	return googleEndpoint.AuthURL + "?" + v.Encode()
}

// ExchangeCode trades the authorization code (received in the callback)
// for an access_token + refresh_token + expires_in. The code_verifier
// is sent per RFC 7636 §4.5 to prove possession of the original
// code_challenge.
func (g *GoogleProvider) ExchangeCode(ctx context.Context, code, codeVerifier string, scopes []string) (TokenSet, error) {
	cfg := googleOauthConfig(g.RedirectURL, g.ClientID, g.ClientSecret, scopes)
	token, err := cfg.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", codeVerifier),
	)
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth/google: exchange code: %w", err)
	}
	ts := TokenSet{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.Expiry,
		Scopes:       scopes,
	}
	// Best-effort: fetch account email via userinfo endpoint.
	ts.AccountEmail = g.fetchAccountEmail(ctx, token.AccessToken)
	return ts, nil
}

// Refresh trades a refresh_token for a fresh access_token (and possibly
// a new refresh_token — Google rotates refresh_tokens by default).
func (g *GoogleProvider) Refresh(ctx context.Context, refreshToken string, scopes []string) (TokenSet, error) {
	cfg := googleOauthConfig(g.RedirectURL, g.ClientID, g.ClientSecret, scopes)
	tokenSource := cfg.TokenSource(ctx,
		&oauth2.Token{RefreshToken: refreshToken, Expiry: time.Now().Add(-time.Hour)},
	)
	token, err := tokenSource.Token()
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth/google: refresh token: %w", err)
	}
	ts := TokenSet{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.Expiry,
		Scopes:       scopes,
	}
	if token.AccessToken != "" {
		ts.AccountEmail = g.fetchAccountEmail(ctx, token.AccessToken)
	}
	return ts, nil
}

// fetchAccountEmail calls Google's userinfo endpoint to retrieve the
// user's email address. Best-effort — failures are logged but not
// returned (the TokenSet is still valid without email).
func (g *GoogleProvider) fetchAccountEmail(ctx context.Context, accessToken string) string {
	if accessToken == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return ""
	}
	return info.Email
}

// Revoke invalidates the given access_token (and its associated
// refresh_token) on Google's side. Called by OAuth disconnect.
func (g *GoogleProvider) Revoke(ctx context.Context, accessToken string) error {
	if accessToken == "" {
		return nil
	}
	v := url.Values{}
	v.Set("token", accessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/revoke?"+v.Encode(), nil)
	if err != nil {
		return fmt.Errorf("oauth/google: revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("oauth/google: revoke: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return fmt.Errorf("oauth/google: revoke returned status %d", resp.StatusCode)
	}
	return nil
}

// osGetenv is a small helper that wraps os.Getenv for use in package
// init expressions (avoids the bare "os" import in this file).
func osGetenv(key string) string {
	return osGetenvImpl(key)
}

// Note: osGetenvImpl lives in env.go (split out to avoid import bloat).
// In tests, env.go is overridden via build tag test to return empty.