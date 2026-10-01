// Package audit — Ed25519 payload signature (ADR-017, Phase 6 alpha.18.1).
//
// ADR-017 was deferred from Phase 2 (alpha.15) because no external
// verifier use case existed at the time. Phase 6 closes ADR-017:
// every Write optionally signs its row_hash with an Ed25519 private
// key. The signature lands in a new `signature BLOB` column on
// audit_log (nullable for legacy rows).
//
// # Threat model
//
// The hash chain alone (Phase 2, alpha.15) detects:
//
//   - Modification (a row's content changed → row_hash mismatch)
//   - Deletion (a missing row → next row's prev_hash dangling)
//   - Forgery (a fake row → AUTOINCREMENT breaks prev_hash linkage)
//
// What the chain does NOT detect:
//
//   - "Rewrite the whole file from scratch" with consistent fake rows
//     (would require external trust anchor — Rekor deferred alpha.3).
//   - An attacker who holds the canonical encoding + has write access
//     to the audit_log table (no actor identity, just bytes).
//
// Ed25519 closes the second gap: every row_hash is signed by a
// private key held outside the DB. An attacker with write access but
// without the private key can produce a row with valid row_hash +
// valid chain linkage, but the signature won't verify. The chain
// detects tampering by row content; the signature detects tampering
// by actor.
//
// # Wire contract
//
// Backward-compatible. The signature column is nullable; legacy
// rows (Phase 2 alpha.15 and earlier, before this commit) have
// NULL signature. VerifySignature treats NULL as "no signature to
// check" (warns but does not fail). Only rows with non-NULL
// signature are verified.
//
// # Configuration
//
// Signing is opt-in. The Writer reads the Ed25519 private key from
// env var DARK_AUDIT_SIGNING_KEY (base64-encoded 64-byte key). When
// unset, the Writer skips signing (signature column stays NULL).
// VerifyWithSignature reads the corresponding public key from
// DARK_AUDIT_VERIFY_KEY (base64-encoded 32-byte key).
//
// Production operators MUST set both env vars. In dev/test, signing
// is typically disabled.
package audit

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// ErrSigKeyMalformed is returned when DARK_AUDIT_SIGNING_KEY or
// DARK_AUDIT_VERIFY_KEY is not valid base64 or the wrong length.
var ErrSigKeyMalformed = errors.New("audit signature: key must be valid base64 with correct Ed25519 length")

// ErrSigInvalid is returned when a signature does not verify against
// the row_hash + public key.
var ErrSigInvalid = errors.New("audit signature: signature does not verify")

// ParsePrivateKey decodes a base64-encoded Ed25519 private key.
// Returns ErrSigKeyMalformed on invalid base64 or wrong length.
func ParsePrivateKey(b64 string) (ed25519.PrivateKey, error) {
	if b64 == "" {
		return nil, ErrSigKeyMalformed
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSigKeyMalformed, err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: got %d bytes, want %d",
			ErrSigKeyMalformed, len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// ParsePublicKey decodes a base64-encoded Ed25519 public key.
// Returns ErrSigKeyMalformed on invalid base64 or wrong length.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	if b64 == "" {
		return nil, ErrSigKeyMalformed
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSigKeyMalformed, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: got %d bytes, want %d",
			ErrSigKeyMalformed, len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// SignRowHash signs a row_hash (32 bytes) with the given private key.
// Returns the 64-byte Ed25519 signature. Pure function — no DB access.
func SignRowHash(rowHash []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, rowHash)
}

// VerifyRowHashSignature verifies an Ed25519 signature over a row_hash.
// Returns nil on success, ErrSigInvalid on mismatch.
func VerifyRowHashSignature(rowHash, signature []byte, pub ed25519.PublicKey) error {
	if !ed25519.Verify(pub, rowHash, signature) {
		return ErrSigInvalid
	}
	return nil
}

// RowHashPublicKey derives a deterministic 32-byte identifier from a
// public key. SHA-256 over the key bytes; truncated to 32. Used as
// a stable key identifier in audit meta (so an operator can confirm
// which key signed a row without revealing the key itself).
func RowHashPublicKey(pub ed25519.PublicKey) []byte {
	h := sha256.Sum256(pub)
	return h[:]
}
