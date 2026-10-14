// Package oauth — pkce_test.go: tests for PKCE generation per RFC 7636
// §4. Includes the canonical RFC vector (Appendix B) and round-trip
// verification.
package oauth

import (
	"strings"
	"testing"
)

// TestPKCE_RFC7636Vector uses the example from RFC 7636 Appendix B:
//
//   code_verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
//   code_challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
//
// We don't seed rand — the test verifies the SHAPE (length 43, valid
// base64url, correct SHA-256 derivation) rather than a fixed value.
func TestPKCE_RFC7636Shape(t *testing.T) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE: %v", err)
	}
	if len(verifier) < minVerifierLen || len(verifier) > maxVerifierLen {
		t.Errorf("verifier length %d out of RFC bounds [%d, %d]", len(verifier), minVerifierLen, maxVerifierLen)
	}
	if !isValidBase64URL(verifier) {
		t.Errorf("verifier %q contains non-base64url characters", verifier)
	}
	if !isValidBase64URL(challenge) {
		t.Errorf("challenge %q contains non-base64url characters", challenge)
	}
	// SHA-256 should produce 43-char base64url (32 bytes raw → 43 chars
	// without padding).
	if len(challenge) != 43 {
		t.Errorf("challenge length %d, want 43", len(challenge))
	}
	// Verify challenge derives correctly from verifier.
	if !VerifyPKCEChallenge(verifier, challenge) {
		t.Errorf("VerifyPKCEChallenge failed for verifier/challenge pair")
	}
	// Cross-check: tampered verifier should NOT match.
	if VerifyPKCEChallenge(verifier+"X", challenge) {
		t.Errorf("VerifyPKCEChallenge accepted tampered verifier")
	}
}

// TestPKCE_RFC7636AppendixB manually exercises the RFC 7636 §B vector
// (the canonical test vector for SHA-256 PKCE). We compute the expected
// challenge from the canonical verifier and assert GeneratePKCE's
// verification logic accepts it.
func TestPKCE_RFC7636AppendixB(t *testing.T) {
	const (
		canonicalVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		canonicalChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	if !VerifyPKCEChallenge(canonicalVerifier, canonicalChallenge) {
		t.Errorf("RFC 7636 Appendix B vector failed: verifier=%s, challenge=%s",
			canonicalVerifier, canonicalChallenge)
	}
}

// TestGenerateState verifies the state nonce is well-formed.
func TestGenerateState(t *testing.T) {
	state, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	if len(state) < 16 {
		t.Errorf("state length %d too short (CSRF protection requires >=16 chars)", len(state))
	}
	if !isValidBase64URL(state) {
		t.Errorf("state %q contains non-base64url characters", state)
	}
	// Two consecutive generations should produce different values
	// (cryptographic randomness check).
	state2, _ := GenerateState()
	if state == state2 {
		t.Errorf("GenerateState returned duplicate value (rand failure)")
	}
}

// isValidBase64URL returns true if s is composed entirely of base64url
// characters (RFC 4648 §5): A-Z, a-z, 0-9, '-', '_'. Padding ('=')
// is excluded because RawURLEncoding omits it.
func isValidBase64URL(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", c) {
			return false
		}
	}
	return true
}