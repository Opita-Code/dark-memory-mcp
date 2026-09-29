// Audit chain verification (Phase 2, INV-12, alpha.15).
//
// Verify walks audit_log from startID to endID and recomputes each
// row's SHA-256 row_hash (per canonical.go). It detects:
//   - Modification (a row's content changed → row_hash mismatch).
//   - Deletion (a missing row causes the next row's prev_hash to
//     refer to a row that doesn't exist → broken_at = next audit_id).
//   - Forgery (a fake row inserted between K and K+1 — but
//     AUTOINCREMENT monotonicity makes this impossible, so any
//     "fake" row in the middle would shift the sequence and break
//     prev_hash linkage at K+1).
//
// # What Verify does NOT detect
//
//   - "Rewrite the whole file from scratch" (needs external trust
//     anchor — Rekor-style transparency log; alpha.3 deferred).
//   - Modifications to the schema that change the canonical encoding
//     (would break the chain, but Verify's signature is fixed:
//     one canonical.go, one Verify; any drift = test failure).
//
// # Legacy rows
//
// Rows emitted before Phase 2 deployment have prev_hash=NULL and
// row_hash=NULL. Verify treats them as "trust anchors": the chain
// picks up at the first non-NULL row. That row's prev_hash MUST
// equal zeroHash.
//
// # Concurrency
//
// Verify is read-only. No locks taken. The snapshot is point-in-
// time; a row committed DURING verify may or may not be seen.
// Acceptable: the goal is "did anyone tamper with the past", not
// "is the live DB consistent right now".
package audit

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"time"
)

// VerifyResult is the outcome of Verify. Verified=true means every
// row in the range passed the chain check; BrokenAt is 0. Verified=
// false means the chain is broken; BrokenAt is the audit_id of the
// first failing row (the smallest audit_id with a broken link).
//
// Count is the number of rows walked (including legacy NULL rows,
// which are skipped — not failing).
type VerifyResult struct {
	Verified  bool
	BrokenAt  int64
	Count     int
	StartID   int64
	EndID     int64
	ElapsedMS int64
}

// Verify walks audit_log in [startID, endID] (inclusive) and
// validates each row's hash chain link.
//
// Sentinel: startID==0 → defaults to MIN(audit_id) WHERE row_hash
// IS NOT NULL (or 1 if no chained rows). endID==0 → defaults to
// MAX(audit_id).
//
// Errors:
//   - startID > endID after default-resolution: returns error
//     (caller should fix the input — empty range is not an error,
//     but inverted range is).
//   - DB errors during query/scan: returned as error.
func Verify(ctx context.Context, db *sql.DB, startID, endID int64) (VerifyResult, error) {
	if db == nil {
		return VerifyResult{}, fmt.Errorf("audit Verify: db is nil")
	}

	// Default-resolution. Sentinel is 0 → "use default".
	if startID == 0 {
		// Smallest audit_id with a non-NULL row_hash.
		// COALESCE to 1 when no rows have row_hash set (the table
		// is all-legacy). After defaulting, the range will produce
		// 0 rows (since endID==MAX >= startID, but all rows are
		// legacy, which Verify skips — Count>0 but Verified=true).
		err := db.QueryRowContext(ctx,
			"SELECT COALESCE(MIN(audit_id), 1) FROM audit_log WHERE row_hash IS NOT NULL",
		).Scan(&startID)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("audit Verify (default start): %w", err)
		}
	}
	if endID == 0 {
		err := db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(audit_id), 0) FROM audit_log",
		).Scan(&endID)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("audit Verify (default end): %w", err)
		}
	}

	res := VerifyResult{StartID: startID, EndID: endID}

	if startID > endID {
		// Inverted range is a caller bug. Empty range (endID < startID
		// before resolution was wrong; we resolved them so this is
		// an explicit error). Return error with the count=0 result.
		return res, fmt.Errorf("audit Verify: startID=%d > endID=%d", startID, endID)
	}

	start := time.Now()
	defer func() {
		res.ElapsedMS = time.Since(start).Milliseconds()
	}()

	rows, err := db.QueryContext(ctx, `
		SELECT audit_id,
		       actor,
		       COALESCE(session_id, '') AS session_id_text,
		       payload,
		       created_at,
		       prev_hash,
		       row_hash
		FROM audit_log
		WHERE audit_id >= ? AND audit_id <= ?
		ORDER BY audit_id ASC`,
		startID, endID,
	)
	if err != nil {
		return res, fmt.Errorf("audit Verify query [%d, %d]: %w", startID, endID, err)
	}
	defer rows.Close()

	// prevRowHash is the row_hash of the previous NON-LEGACY row.
	// Initialized to nil; reset to nil whenever a legacy row is
	// encountered (the next non-legacy row becomes the "new row 1").
	var prevRowHash []byte
	// sawFirstNonNull tracks whether we've seen a non-legacy row
	// yet. The first such row's prev_hash must be zeroHash.
	sawFirstNonNull := false

	for rows.Next() {
		var (
			id                 int64
			actor              string
			sessionID          string
			payload            []byte
			createdAt          string
			prevHash, rowHash  []byte
		)
		if err := rows.Scan(&id, &actor, &sessionID, &payload, &createdAt, &prevHash, &rowHash); err != nil {
			return res, fmt.Errorf("audit Verify scan at id=%d: %w", id, err)
		}
		res.Count++

		// Legacy row: skip; reset prevRowHash so the next non-legacy
		// row is treated as "row 1" with prev_hash = zeroHash.
		if rowHash == nil {
			prevRowHash = nil
			continue
		}

		// First non-legacy row: prev_hash must equal zeroHash.
		if !sawFirstNonNull {
			if prevHash == nil || !bytes.Equal(prevHash, ZeroHash()) {
				res.BrokenAt = id
				return res, nil // Verified=false (zero value)
			}
			sawFirstNonNull = true
		} else {
			// Subsequent non-legacy row: prev_hash must equal
			// previous non-legacy row's row_hash.
			if !bytes.Equal(prevHash, prevRowHash) {
				res.BrokenAt = id
				return res, nil
			}
		}

		// Recompute row_hash from the canonical encoding.
		expected := ComputeRowHash(prevHash, id, actor, sessionID, payload, createdAt)
		if !bytes.Equal(rowHash, expected[:]) {
			res.BrokenAt = id
			return res, nil
		}

		// Advance.
		prevRowHash = rowHash
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("audit Verify rows: %w", err)
	}

	res.Verified = true
	return res, nil
}