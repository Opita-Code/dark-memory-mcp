// Package oauth — huggingface.go: HuggingFace OAuth 2.0 provider.
//
// Endpoints (from https://huggingface.co/docs/hub/oauth):
//   Authorization:  https://huggingface.co/oauth/authorize
//   Token:          https://huggingface.co/oauth/token
//   Device Code:    https://huggingface.co/oauth/device/code
//   Device Token:   https://huggingface.co/oauth/device/token
//
// Scopes:
//   "openid profile"  — minimum identity scope
//   "email"           — optional, for account email
//   "inference-api"   — required for free Inference API access
//
// HuggingFace free tier:
//   - 1000 requests per day on Inference API (DeBERTa-v3-large-mnli)
//   - Rate limits reset at midnight UTC
//
// HuggingFace also supports Device Code Flow (RFC 8628) for users
// without a local browser (SSH sessions, headless containers). The
// device_code flow returns a user_code + verification_url; the user
// visits the URL on any device (e.g., phone), enters the code, and
// the polling endpoint returns the access_token once approved.
//
// References:
//   - https://huggingface.co/docs/hub/oauth
//   - https://datatracker.ietf.org/doc/html/rfc8628
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
)

const (
	hfAuthURL    = "https://huggingface.co/oauth/authorize"
	hfTokenURL   = "https://huggingface.co/oauth/token"
	hfDeviceURL  = "https://huggingface.co/oauth/device/code"
	hfDeviceTokURL = "https://huggingface.co/oauth/device/token"
)

// HuggingFaceProvider implements OAuth + Device Code Flow for HF Hub.
type HuggingFaceProvider struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// NewHuggingFaceProvider constructs a HuggingFaceProvider with the
// given client credentials. HF supports both confidential (with
// client_secret) and public (PKCE-only, no secret) clients.
func NewHuggingFaceProvider(redirectURL string) *HuggingFaceProvider {
	return &HuggingFaceProvider{
		ClientID:     osGetenv("DARK_OAUTH_HUGGINGFACE_CLIENT_ID"),
		ClientSecret: osGetenv("DARK_OAUTH_HUGGINGFACE_CLIENT_SECRET"),
		RedirectURL:  redirectURL,
	}
}

// AuthURL builds the HuggingFace authorization URL the user opens in
// their browser. Standard OAuth 2.0 Authorization Code + PKCE.
func (h *HuggingFaceProvider) AuthURL(state, codeChallenge string, scopes []string) string {
	v := url.Values{}
	v.Set("client_id", h.ClientID)
	v.Set("redirect_uri", h.RedirectURL)
	v.Set("response_type", "code")
	v.Set("scope", strings.Join(scopes, " "))
	v.Set("state", state)
	v.Set("code_challenge", codeChallenge)
	v.Set("code_challenge_method", "S256")
	return hfAuthURL + "?" + v.Encode()
}

// ExchangeCode trades the authorization code for an access_token +
// refresh_token + expires_in.
func (h *HuggingFaceProvider) ExchangeCode(ctx context.Context, code, codeVerifier string, scopes []string) (TokenSet, error) {
	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", h.RedirectURL)
	v.Set("client_id", h.ClientID)
	if h.ClientSecret != "" {
		v.Set("client_secret", h.ClientSecret)
	}
	v.Set("code_verifier", codeVerifier)
	return h.tokenRequest(ctx, v, scopes)
}

// Refresh trades a refresh_token for a fresh access_token.
func (h *HuggingFaceProvider) Refresh(ctx context.Context, refreshToken string, scopes []string) (TokenSet, error) {
	v := url.Values{}
	v.Set("grant_type", "refresh_token")
	v.Set("refresh_token", refreshToken)
	v.Set("client_id", h.ClientID)
	if h.ClientSecret != "" {
		v.Set("client_secret", h.ClientSecret)
	}
	return h.tokenRequest(ctx, v, scopes)
}

// tokenRequest is the common implementation for Exchange and Refresh.
func (h *HuggingFaceProvider) tokenRequest(ctx context.Context, v url.Values, scopes []string) (TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hfTokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth/huggingface: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if h.ClientSecret != "" {
		// Confidential clients may also use Basic Auth.
		req.SetBasicAuth(h.ClientID, h.ClientSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth/huggingface: token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth/huggingface: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return TokenSet{}, fmt.Errorf("oauth/huggingface: token endpoint returned %d: %s", resp.StatusCode, string(body))
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return TokenSet{}, fmt.Errorf("oauth/huggingface: parse token response: %w", err)
	}
	ts := TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC(),
		Scopes:       scopes,
	}
	if tr.Scope != "" {
		ts.Scopes = strings.Fields(tr.Scope)
	}
	return ts, nil
}

// DeviceCodeResponse holds the result of starting a Device Code Flow.
type DeviceCodeResponse struct {
	// DeviceCode is the opaque token used to poll the token endpoint.
	DeviceCode string `json:"device_code"`
	// UserCode is the short, human-readable code the user enters at
	// the verification URL. Examples: "ABCD-1234", "WXYZ-9876".
	UserCode string `json:"user_code"`
	// VerificationURL is where the user goes to enter UserCode.
	// Typically https://huggingface.co/oauth/device.
	VerificationURL string `json:"verification_url"`
	// ExpiresIn is how long until the device_code itself expires
	// (typically 600 seconds = 10 minutes).
	ExpiresIn int `json:"expires_in"`
	// Interval is how often the client should poll (typically 5s).
	Interval int `json:"interval"`
}

// StartDeviceFlow begins a Device Code Flow per RFC 8628. Used for
// headless users (SSH, containers) who can't open a browser on the
// same machine as dark-memory.
func (h *HuggingFaceProvider) StartDeviceFlow(ctx context.Context, scopes []string) (DeviceCodeResponse, error) {
	v := url.Values{}
	v.Set("client_id", h.ClientID)
	v.Set("scope", strings.Join(scopes, " "))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hfDeviceURL, strings.NewReader(v.Encode()))
	if err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("oauth/huggingface: build device request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if h.ClientSecret != "" {
		req.SetBasicAuth(h.ClientID, h.ClientSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("oauth/huggingface: device request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("oauth/huggingface: read device response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return DeviceCodeResponse{}, fmt.Errorf("oauth/huggingface: device endpoint returned %d: %s", resp.StatusCode, string(body))
	}
	var dcr DeviceCodeResponse
	if err := json.Unmarshal(body, &dcr); err != nil {
		return DeviceCodeResponse{}, fmt.Errorf("oauth/huggingface: parse device response: %w", err)
	}
	return dcr, nil
}

// PollDeviceToken polls the token endpoint with the device_code until
// the user approves (returns access_token) or the code expires.
// Caller polls at the rate specified by DeviceCodeResponse.Interval.
func (h *HuggingFaceProvider) PollDeviceToken(ctx context.Context, deviceCode string, scopes []string) (TokenSet, error) {
	v := url.Values{}
	v.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	v.Set("device_code", deviceCode)
	v.Set("client_id", h.ClientID)
	if h.ClientSecret != "" {
		v.Set("client_secret", h.ClientSecret)
	}
	return h.tokenRequest(ctx, v, scopes)
}

// DefaultHuggingFaceScopes returns the minimum scopes needed for
// judge (inference API access + identity claims).
func DefaultHuggingFaceScopes() []string {
	return []string{"openid", "profile", "inference-api"}
}