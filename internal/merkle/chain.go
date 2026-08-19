// Package merkle implements a tamper-evident hash chain for the
// vibe_drift_reports table.
//
// Each drift report row carries a merkle_root computed as:
//
//	root = SHA-256( prev_root || canonical_json(row) )
//
// where prev_root is the previous row's merkle_root (ordered by id
// ascending) and canonical_json is a deterministic projection of the
// audit-relevant columns (see CanonicalInput). The first row in the
// chain uses the sentinel GenesisRoot (64 zero hex chars) as prev_root.
//
// Tamper detection: an attacker who mutates any audit-relevant column
// of any row — or inserts/deletes a row in the middle of the chain —
// invalidates the merkle_root of that row and every subsequent row.
// VerifyChain walks the rows in id order, recomputes each merkle_root
// from the previous row's RECOMPUTED root (not stored), and reports
// the first mismatch.
//
// Design notes:
//   - The chain is append-only at the primitive level. T11+
//     deprecates the in-place UpdateDriftReportVerdict path so the
//     chain stays consistent in normal operation. Until then, any
//     UPDATE on audit-relevant columns will be detected by the
//     verifier (intended signal: "this row was modified after write").
//   - Rows written before T04 land have NULL merkle_root. The verifier
//     skips leading NULL rows (treated as a legacy boundary) and stops
//     if a gap appears in the middle of a chain (NULL after non-NULL).
//   - The chain does NOT provide external anchoring (a signed manifest,
//     WORM log, etc.). T12+ adds that. For now the chain detects
//     modifications within the DB but does not prove anything about
//     the DB itself to an outside observer.
//   - The chain uses crypto/sha256 (FIPS-approved, fast, ubiquitous).
//     SHA-256 collision resistance is more than sufficient for the
//     non-adversarial input space of "drift report verdict rows"
//     (one new row every few seconds at peak). For higher-stakes
//     chains, switch to SHA-3 or BLAKE3.
//
// Concurrency: ComputeRoot is pure-functional and safe for concurrent
// callers. The store layer serializes appends via the existing
// transaction + s.mu.Lock so prev_root lookups are race-free within
// a single process.
package merkle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// HashLen is the hex-encoded length of a SHA-256 digest.
const HashLen = 64

// GenesisRoot is the sentinel prev_root for the first row in a chain.
// It is 64 ASCII '0' characters, distinct from any real chain root
// (real roots have ~uniform hex distribution; the chance of a real
// root being all zeros is 1/2^256).
const GenesisRoot = "0000000000000000000000000000000000000000000000000000000000000000"

// CanonicalInput is the projection of a drift report row that
// participates in the merkle chain. Only audit-relevant fields are
// included; mutable or already-redundant fields are excluded:
//
//   - ID: the autoincrement id, used for ORDER BY in the verifier.
//     Not hashed because two distinct ids have different rows by
//     construction; including it would also force recomputation after
//     id renumbering (a hypothetical future op).
//   - ProjectID: tenant namespace (INV-7). Excluded because the
//     same row's merkle_root must be valid regardless of which
//     project it's filtered into.
//   - ReconciledAt: set asynchronously by reconciliation, mutable.
//   - MerkleRoot: the chain value itself, not part of the input.
//
// Field names are 1-character to keep the JSON projection small in
// the common case. Determinism comes from encoding/json's struct
// field ordering (Go json always emits fields in declaration order).
type CanonicalInput struct {
	ArtifactID     int64  `json:"a"`
	SpecID         int64  `json:"s,omitempty"` // omitempty: 0 → omitted (= NULL spec_id)
	Verdict        string `json:"v"`
	SpecDiff       string `json:"d,omitempty"` // omitempty: "" → omitted
	JudgeReasoning string `json:"j,omitempty"` // omitempty: "" → omitted
	CreatedAt      string `json:"t"`
}

// ComputeRoot returns the hex-encoded SHA-256 digest of
// (prev || canonical_json(in)).
//
// prev must be exactly HashLen ASCII hex characters (lowercase or
// uppercase, no whitespace, no prefix). GenesisRoot is the canonical
// empty-chain prev.
//
// Returns an error if prev has the wrong length, contains non-hex
// characters, or if json.Marshal fails (which only happens for invalid
// values in in, which is a programmer error).
func ComputeRoot(prev string, in CanonicalInput) (string, error) {
	if len(prev) != HashLen {
		return "", fmt.Errorf("merkle: prev must be %d hex chars, got %d", HashLen, len(prev))
	}
	if _, err := hex.DecodeString(prev); err != nil {
		return "", fmt.Errorf("merkle: prev not valid hex: %w", err)
	}
	canon, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("merkle: marshal canonical: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write(canon)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Report is the chain-side projection of a drift_reports row,
// suitable for VerifyChain. The store layer constructs Reports from
// rows by reading the same audit-relevant columns that go into
// CanonicalInput.
type Report struct {
	ID         int64
	Canonical  CanonicalInput
	MerkleRoot string
}

// VerifyResult describes the outcome of a chain verification.
type VerifyResult struct {
	OK          bool   `json:"ok"`
	FirstBadID  int64  `json:"first_bad_id,omitempty"`
	FirstBadWhy string `json:"first_bad_why,omitempty"`
	RowsChecked int    `json:"rows_checked"`
	RowsSkipped int    `json:"rows_skipped"` // pre-T04 legacy rows with NULL merkle_root
	ChainHead   string `json:"chain_head,omitempty"`
}

// VerifyChain walks rows in id ASC order, recomputing each row's
// merkle_root from the previously-recomputed root, and compares to
// the stored merkle_root. Returns OK=true iff every non-empty row's
// stored root matches the recomputed value.
//
// Rules:
//   - Rows with empty MerkleRoot (legacy pre-T04) are skipped at the
//     start of the chain (treated as a boundary). Once a non-empty
//     row is seen, a subsequent empty MerkleRoot is a chain GAP and
//     fails verification (RowsSkipped counts only leading empties).
//   - The first non-empty row's prev is GenesisRoot. The verifier
//     does NOT trust the row's stored prev_root (we don't store
//     prev_root as a column; that's intentional — recomputing from
//     the previous row is the tamper-evident form).
//   - On mismatch, FirstBadID is the offending row's id and
//     FirstBadWhy explains ("expected X, got Y" or "chain gap: ..."
//     or "compute: ...").
//
// Complexity: O(n) time, O(1) extra space.
func VerifyChain(rows []Report) VerifyResult {
	prev := GenesisRoot
	res := VerifyResult{}
	var seenNonEmpty bool
	for _, r := range rows {
		if r.MerkleRoot == "" {
			res.RowsSkipped++
			if seenNonEmpty {
				res.OK = false
				res.FirstBadID = r.ID
				res.FirstBadWhy = "chain gap: empty merkle_root after non-empty chain"
				return res
			}
			continue
		}
		seenNonEmpty = true
		expected, err := ComputeRoot(prev, r.Canonical)
		if err != nil {
			res.OK = false
			res.FirstBadID = r.ID
			res.FirstBadWhy = fmt.Sprintf("compute: %v", err)
			return res
		}
		if expected != r.MerkleRoot {
			res.OK = false
			res.FirstBadID = r.ID
			res.FirstBadWhy = fmt.Sprintf("expected %s, got %s", expected, r.MerkleRoot)
			return res
		}
		prev = r.MerkleRoot
		res.RowsChecked++
	}
	res.OK = true
	res.ChainHead = prev
	return res
}