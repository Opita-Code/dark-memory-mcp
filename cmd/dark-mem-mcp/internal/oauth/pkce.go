// Package oauth — pkce.go: PKCE (Proof Key for Code Exchange) per
// RFC 7636. Used by both Google (Authorization Code + PKCE) and
// HuggingFace (Authorization Code + PKCE + Device Code fallback).
//
// The flow:
//
//   1. Server generates code_verifier (random URL-safe string 43-128 chars)
//   2. Server computes code_challenge = BASE64URL(SHA256(code_verifier))
//   3. Client includes code_challenge + code_challenge_method=S256 in
//      the authorization request
//   4. Provider stores the challenge, redirects with code
//   5. Server exchanges code + code_verifier for tokens
//   6. Provider validates SHA256(code_verifier) == stored challenge
//
// PKCE protects against authorization code interception attacks (e.g.,
// malicious browser extension or proxy reading the redirect URL).
// Without PKCE, an attacker who intercepts the code could exchange it
// for tokens themselves.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

const (
	// minVerifierLen is the minimum length of code_verifier per RFC 7636 §4.1.
	minVerifierLen = 43
	// maxVerifierLen is the maximum length of code_verifier per RFC 7636 §4.1.
	maxVerifierLen = 128
	// verifierBytes is the entropy source: 32 random bytes → 43-char
	// base64url-encoded string (the minimum). Higher values produce
	// longer verifiers (still within 43-128 range).
	verifierBytes = 32
)

// GeneratePKCE returns (code_verifier, code_challenge) for OAuth 2.0
// PKCE per RFC 7636 §4. The verifier is generated from 32 cryptographically
// random bytes, base64url-encoded without padding (43 chars). The
// challenge is the SHA-256 hash of the verifier, also base64url-encoded
// without padding (43 chars).
//
// Example output:
//
//   verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
//   challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
func GeneratePKCE() (verifier, challenge string, err error) {
	raw := make([]byte, verifierBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	if len(verifier) < minVerifierLen || len(verifier) > maxVerifierLen {
		return "", "", errors.New("oauth: PKCE verifier length out of RFC 7636 bounds")
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// GenerateState returns a 32-character base64url-encoded random nonce
// for CSRF protection in OAuth flows. The state is sent in the
// authorization URL and echoed back by the provider in the callback;
// the server validates that the two match before exchanging the code.
func GenerateState() (string, error) {
	raw := make([]byte, 18) // 18 bytes → 24-char base64url (well over 16-char minimum)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// VerifyPKCEChallenge verifies that a code_challenge matches a given
// code_verifier per RFC 7636 §4.6. Used by providers to validate the
// PKCE pair after the authorization code is received (the verifier is
// included in the token exchange request, the challenge was stored
// at authorization time).
func VerifyPKCEChallenge(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	return expected == challenge
}