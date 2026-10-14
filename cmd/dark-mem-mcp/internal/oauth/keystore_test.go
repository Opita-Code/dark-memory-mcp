// Package oauth — keystore_test.go: round-trip tests for the in-memory
// (ArrayKeyring) Keystore backend. Exercises SaveToken → LoadToken →
// DeleteToken → ListProviders in sequence.
package oauth

import (
	"errors"
	"testing"
	"time"
)

// TestKeystore_SaveLoadDeleteRoundTrip is the happy path: save a token
// set, load it back, verify all fields match, delete it, verify it's
// gone.
func TestKeystore_SaveLoadDeleteRoundTrip(t *testing.T) {
	ks := ArrayKeystore()
	original := TokenSet{
		AccessToken:  "ya29.a0AfH6SMBxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		RefreshToken: "1//0eXyZxxxxxxxxxxxxxxxxxxxxxxxxxx",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC().Truncate(time.Second),
		Scopes:       []string{"https://www.googleapis.com/auth/generative-language.retriever"},
		AccountEmail: "test-user@gmail.com",
	}

	if err := ks.SaveToken(ProviderGoogle, original); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	loaded, found, err := ks.LoadToken(ProviderGoogle)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if !found {
		t.Fatalf("LoadToken: not found after save")
	}
	if loaded.AccessToken != original.AccessToken {
		t.Errorf("AccessToken mismatch: got %q, want %q", loaded.AccessToken, original.AccessToken)
	}
	if loaded.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken mismatch: got %q, want %q", loaded.RefreshToken, original.RefreshToken)
	}
	if !loaded.ExpiresAt.Equal(original.ExpiresAt) {
		t.Errorf("ExpiresAt mismatch: got %v, want %v", loaded.ExpiresAt, original.ExpiresAt)
	}
	if loaded.AccountEmail != original.AccountEmail {
		t.Errorf("AccountEmail mismatch: got %q, want %q", loaded.AccountEmail, original.AccountEmail)
	}
	if len(loaded.Scopes) != 1 || loaded.Scopes[0] != original.Scopes[0] {
		t.Errorf("Scopes mismatch: got %v, want %v", loaded.Scopes, original.Scopes)
	}

	if err := ks.DeleteToken(ProviderGoogle); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	_, found, err = ks.LoadToken(ProviderGoogle)
	if err != nil {
		t.Fatalf("LoadToken after delete: %v", err)
	}
	if found {
		t.Errorf("LoadToken still found token after delete")
	}
}

// TestKeystore_MultipleProviders verifies that tokens for different
// providers coexist without interfering.
func TestKeystore_MultipleProviders(t *testing.T) {
	ks := ArrayKeystore()

	googleTok := TokenSet{
		AccessToken: "google-access",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	huggingfaceTok := TokenSet{
		AccessToken: "hf-access",
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	if err := ks.SaveToken(ProviderGoogle, googleTok); err != nil {
		t.Fatalf("SaveToken google: %v", err)
	}
	if err := ks.SaveToken(ProviderHuggingFace, huggingfaceTok); err != nil {
		t.Fatalf("SaveToken hf: %v", err)
	}

	g, _, _ := ks.LoadToken(ProviderGoogle)
	hf, _, _ := ks.LoadToken(ProviderHuggingFace)
	if g.AccessToken != "google-access" {
		t.Errorf("google token corrupted: %q", g.AccessToken)
	}
	if hf.AccessToken != "hf-access" {
		t.Errorf("hf token corrupted: %q", hf.AccessToken)
	}

	providers, err := ks.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 2 {
		t.Errorf("ListProviders returned %d, want 2", len(providers))
	}

	// Delete only google; hf should remain.
	if err := ks.DeleteToken(ProviderGoogle); err != nil {
		t.Fatalf("DeleteToken google: %v", err)
	}
	_, found, _ := ks.LoadToken(ProviderGoogle)
	if found {
		t.Errorf("google token still present after delete")
	}
	_, found, _ = ks.LoadToken(ProviderHuggingFace)
	if !found {
		t.Errorf("hf token disappeared after google delete")
	}
}

// TestKeystore_LoadTokenNotFound verifies LoadToken returns (empty, false, nil)
// when no token is stored.
func TestKeystore_LoadTokenNotFound(t *testing.T) {
	ks := ArrayKeystore()
	_, found, err := ks.LoadToken(ProviderGoogle)
	if err != nil {
		t.Fatalf("LoadToken on empty keystore: %v", err)
	}
	if found {
		t.Errorf("LoadToken found token in empty keystore")
	}
}

// TestKeystore_RejectsEmptyProvider verifies the API guard against
// ProviderNone (which would be a programmer error in callers).
func TestKeystore_RejectsEmptyProvider(t *testing.T) {
	ks := ArrayKeystore()
	if err := ks.SaveToken(ProviderNone, TokenSet{AccessToken: "x"}); err == nil {
		t.Error("SaveToken(ProviderNone) returned nil error")
	}
	if _, _, err := ks.LoadToken(ProviderNone); err == nil {
		t.Error("LoadToken(ProviderNone) returned nil error")
	}
	if err := ks.DeleteToken(ProviderNone); err == nil {
		t.Error("DeleteToken(ProviderNone) returned nil error")
	}
}

// TestTokenSet_IsExpired verifies the 30-second safety margin.
// The contract: a token is considered expired when fewer than 30s
// remain before its ExpiresAt (to avoid edge-of-expiry race conditions
// when refreshing). So:
//
//   - 1h ahead:  NOT expired
//   - 15s ahead: IS expired (within the 30s safety margin)
//   - 1m past:   IS expired
func TestTokenSet_IsExpired(t *testing.T) {
	fresh := TokenSet{ExpiresAt: time.Now().Add(1 * time.Hour)}
	if fresh.IsExpired() {
		t.Error("1h-fresh token reported as expired")
	}
	marginal := TokenSet{ExpiresAt: time.Now().Add(15 * time.Second)}
	if !marginal.IsExpired() {
		t.Error("15s-ahead token reported as not expired (within 30s safety margin)")
	}
	past := TokenSet{ExpiresAt: time.Now().Add(-1 * time.Minute)}
	if !past.IsExpired() {
		t.Error("1m-past token reported as not expired")
	}
}

// TestTokenSet_CanRefresh verifies the refresh-token presence check.
func TestTokenSet_CanRefresh(t *testing.T) {
	if (&TokenSet{AccessToken: "x"}).CanRefresh() {
		t.Error("CanRefresh returned true with no refresh_token")
	}
	if !(&TokenSet{AccessToken: "x", RefreshToken: "y"}).CanRefresh() {
		t.Error("CanRefresh returned false with refresh_token set")
	}
	// Sanity: errors.Is on the sentinel — not testable since the
	// sentinel is opaque. Just confirm SaveToken returns nil for
	// real input.
	ks := ArrayKeystore()
	if err := ks.SaveToken(ProviderGoogle, TokenSet{AccessToken: "x", RefreshToken: "y"}); err != nil {
		t.Errorf("SaveToken with refresh_token failed: %v", err)
	}
}

// TestKeystore_KeyNotFoundOnDelete verifies DeleteToken on a missing
// key returns nil (idempotent), not an error.
func TestKeystore_KeyNotFoundOnDelete(t *testing.T) {
	ks := ArrayKeystore()
	if err := ks.DeleteToken(ProviderGoogle); err != nil {
		t.Errorf("DeleteToken on missing key returned error: %v (want nil per idempotency)", err)
	}
}

// errors dummy import avoidance — used by IsExpired/CanRefresh checks.
var _ = errors.New