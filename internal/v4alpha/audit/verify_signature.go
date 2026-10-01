// Package audit — Verify with Ed25519 signature check (ADR-017, Phase 6).
//
// VerifyWithSignature walks the same range as Verify (chain integrity)
// AND verifies the Ed25519 signature on every row that has one.
// Rows without a signature are tolerated (legacy pre-Phase-6 rows)
// but reported as warnings in the result.
//
// # Signature vs chain
//
// The hash chain (Phase 2, alpha.15) detects structural tampering
// (modification, deletion, forgery). The Ed25519 signature
// (Phase 6, alpha.18.1) detects actor-identity tampering: a row
// whose row_hash matches the chain AND the chain is intact, but
// whose signature was produced by a different key than the
// configured verification key, fails VerifyWithSignature.
//
// # Public key matching
//
// Each row stores a sig_pubkey column containing SHA-256(public_key)
// (a deterministic identifier, NOT the key itself). VerifyWithSignature
// derives the same hash from the configured public key and verifies
// only rows whose sig_pubkey matches. Rows with a non-matching
// sig_pubkey (signed by a different key) fail verification.
//
// Rows with NULL sig_pubkey AND NULL signature: legacy, ignored.
// Rows with NULL sig_pubkey AND non-NULL signature: malformed, fail.
// Rows with non-NULL sig_pubkey AND NULL signature: malformed, fail.
package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"time"
)

// SignatureVerifyResult is the outcome of VerifyWithSignature.
// Embeds VerifyResult (chain integrity) and adds signature-specific
// fields.
type SignatureVerifyResult struct {
	VerifyResult
	// SignaturesChecked is the count of rows with non-NULL signature
	// that were verified.
	SignaturesChecked int
	// SignaturesMissing is the count of rows with NULL signature
	// (legacy or pre-Phase-6). Not failing — informational.
	SignaturesMissing int
	// SignatureFailures is the count of rows whose signature did
	// NOT verify. Non-zero → Verified=false.
	SignatureFailures int
	// FirstSignatureFailure is the audit_id of the first failing
	// row (the smallest id with a bad signature).
	FirstSignatureFailure int64
}

// VerifyWithSignature walks audit_log from startID to endID,
// validates each row's hash chain link (per Verify), AND verifies
// the Ed25519 signature on every row that has one. The public key
// is required (no public key = no verification possible).
//
// Returns Verified=true iff (a) the chain is intact AND (b) every
// non-NULL signature verifies. Legacy rows (NULL signature) are
// tolerated but counted in SignaturesMissing.
//
// Empty range / no rows → Verified=true vacuously.
func VerifyWithSignature(ctx context.Context, db *sql.DB, startID, endID int64, pub ed25519.PublicKey) (SignatureVerifyResult, error) {
	if db == nil {
		return SignatureVerifyResult{}, fmt.Errorf("audit VerifyWithSignature: db is nil")
	}
	if len(pub) != ed25519.PublicKeySize {
		return SignatureVerifyResult{}, fmt.Errorf("audit VerifyWithSignature: pub must be %d bytes, got %d",
			ed25519.PublicKeySize, len(pub))
	}

	// Default-resolution. Mirrors Verify.
	if startID == 0 {
		err := db.QueryRowContext(ctx,
			"SELECT COALESCE(MIN(audit_id), 1) FROM audit_log WHERE row_hash IS NOT NULL",
		).Scan(&startID)
		if err != nil {
			return SignatureVerifyResult{}, fmt.Errorf("audit VerifyWithSignature (default start): %w", err)
		}
	}
	if endID == 0 {
		err := db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(audit_id), 0) FROM audit_log",
		).Scan(&endID)
		if err != nil {
			return SignatureVerifyResult{}, fmt.Errorf("audit VerifyWithSignature (default end): %w", err)
		}
	}

	res := SignatureVerifyResult{
		VerifyResult: VerifyResult{StartID: startID, EndID: endID},
	}
	if endID == 0 {
		res.Verified = true
		return res, nil
	}
	if startID > endID {
		return res, fmt.Errorf("audit VerifyWithSignature: startID=%d > endID=%d", startID, endID)
	}

	start := time.Now()
	defer func() {
		res.ElapsedMS = time.Since(start).Milliseconds()
	}()

	// Derive the expected sig_pubkey identifier for the configured
	// public key. Rows signed by a different key fail verification.
	expectedPubKeyHash := RowHashPublicKey(pub)

	rows, err := db.QueryContext(ctx, `
		SELECT audit_id,
		       actor,
		       COALESCE(session_id, '') AS session_id_text,
		       payload,
		       created_at,
		       prev_hash,
		       row_hash,
		       signature,
		       sig_pubkey
		FROM audit_log
		WHERE audit_id >= ? AND audit_id <= ?
		ORDER BY audit_id ASC`,
		startID, endID,
	)
	if err != nil {
		return res, fmt.Errorf("audit VerifyWithSignature query [%d, %d]: %w", startID, endID, err)
	}
	defer rows.Close()

	var prevRowHash []byte
	sawFirstNonNull := false

	for rows.Next() {
		var (
			id                int64
			actor             string
			sessionID         string
			payload           []byte
			createdAt         string
			prevHash, rowHash []byte
			signature         []byte
			sigPubkey         []byte
		)
		if err := rows.Scan(&id, &actor, &sessionID, &payload, &createdAt, &prevHash, &rowHash, &signature, &sigPubkey); err != nil {
			return res, fmt.Errorf("audit VerifyWithSignature scan at id=%d: %w", id, err)
		}
		res.Count++

		// Legacy row: skip chain check; reset prevRowHash.
		if rowHash == nil {
			prevRowHash = nil
			continue
		}

		// Chain check (same as Verify).
		if !sawFirstNonNull {
			if prevHash == nil || !bytes.Equal(prevHash, ZeroHash()) {
				res.BrokenAt = id
				return res, nil
			}
			sawFirstNonNull = true
		} else {
			if !bytes.Equal(prevHash, prevRowHash) {
				res.BrokenAt = id
				return res, nil
			}
		}
		expected := ComputeRowHash(prevHash, id, actor, sessionID, payload, createdAt)
		if !bytes.Equal(rowHash, expected[:]) {
			res.BrokenAt = id
			return res, nil
		}
		prevRowHash = rowHash

		// Signature check.
		if signature == nil {
			// Legacy or pre-Phase-6 row — no signature to verify.
			res.SignaturesMissing++
			continue
		}
		res.SignaturesChecked++

		// Verify the signature matches the configured public key.
		// First check the sig_pubkey identifier (fast, no signature
		// math). If the identifier doesn't match, the row was signed
		// by a different key → fail.
		if sigPubkey == nil {
			res.SignatureFailures++
			if res.FirstSignatureFailure == 0 {
				res.FirstSignatureFailure = id
			}
			continue
		}
		if !bytes.Equal(sigPubkey, expectedPubKeyHash) {
			res.SignatureFailures++
			if res.FirstSignatureFailure == 0 {
				res.FirstSignatureFailure = id
			}
			continue
		}

		// Identifier matches — verify the actual signature.
		if err := VerifyRowHashSignature(rowHash, signature, pub); err != nil {
			res.SignatureFailures++
			if res.FirstSignatureFailure == 0 {
				res.FirstSignatureFailure = id
			}
			continue
		}
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("audit VerifyWithSignature rows: %w", err)
	}

	// Verified iff chain intact AND no signature failures.
	res.Verified = res.SignatureFailures == 0 && res.BrokenAt == 0
	return res, nil
}

// signatureMigrationApplied is a helper that checks whether the
// signature columns already exist on audit_log (idempotent migration
// detection). Used by tests.
func signatureMigrationApplied(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info('audit_log') WHERE name = 'signature'",
	).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
