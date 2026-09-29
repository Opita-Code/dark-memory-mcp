// Package audit (v4alpha) — INV-12 (audit chain) canonical encoding.
//
// Phase 2 (alpha.15, 2026-09-29) introduces a SHA-256 hash chain
// over audit_log rows so tampering (modification, deletion, forgery)
// is detectable by the dark_memory_audit_verify tool.
//
// The chain is purely structural (no key, no signature). It detects
// everything except "rewrite the whole file from scratch". Ed25519
// signatures (ADR-017) are forward-compatible: when the audit chain
// needs external non-repudiation, the signature column lands and
// signs row_hash. The canonical encoding in this file is the
// single source of truth — Writer.Write, WriteExec, and Verify all
// use ComputeRowHash so any drift breaks the chain (which is the
// point).
//
// # Canonical encoding (§3.2 of SPEC-alpha-11-phase2)
//
//   row_hash = SHA256(
//       prev_hash          // 32 bytes (zero for row 1)
//       || audit_id        // int64 big-endian (8 bytes)
//       || actor           // UTF-8 bytes (non-empty per INV-1)
//       || 0x00            // separator
//       || session_id      // UTF-8 bytes (empty string if NULL)
//       || 0x00            // separator
//       || payload         // raw bytes (may be empty)
//       || 0x00            // separator
//       || created_at      // RFC3339 string from SQLite CURRENT_TIMESTAMP
//       || 0x00            // trailing separator
//   )
//
// Why this encoding (per §3.2 of spec):
//   - Fixed-width where possible (audit_id 8B BE): cheap, no length prefix.
//   - Implicit length prefix via 0x00 separators: avoids
//     "fo" + "o" vs "foo" + "" ambiguity (test: TestSeparatorDisambiguation).
//   - Trailing 0x00: prevents payload ending in ASCII that looks
//     like RFC3339 from fusing with created_at (test: TestTrailingSeparatorFuseAttack).
//   - created_at is the SQLite CURRENT_TIMESTAMP — UTC, RFC3339, identical
//     across writers. No time.Now() in the hash input.
package audit

import (
	"crypto/sha256"
	"encoding/binary"
)

// zeroHash is the canonical prev_hash for row 1 (and for the first
// post-migration Write when no prior row has a non-NULL row_hash).
// 32 zero bytes. Package-level (reused on hot path; no allocation).
//
// The slice is intentionally NOT const — Go's `const` for slices
// isn't supported, and the var survives across function calls in the
// same package. Writers and Verify both reference this; it must
// always equal [32]byte{} (test: TestZeroHash_Stable in canonical_test.go).
var zeroHash = make([]byte, 32)

// sep is the canonical field separator (0x00). Package-level so
// the writer and the verify walker always agree on the same byte.
var sep = []byte{0x00}

// HashLen is the byte length of a SHA-256 output (and therefore the
// size of row_hash and prev_hash columns). Constant for readability.
const HashLen = 32

// ComputeRowHash computes the SHA-256 row_hash for a single audit_log
// row. Pure function — no DB, no time.Now, no process-local state.
// Same inputs always produce the same [32]byte output (test:
// TestComputeRowHash_Deterministic).
//
// Arguments:
//   - prevHash: 32 bytes; use zeroHash for row 1
//   - auditID: assigned by SQLite AUTOINCREMENT (BUG-12)
//   - actor: non-empty per INV-1; UTF-8 bytes (writer validates non-empty)
//   - sessionID: empty string if not bound to a session
//   - payload: raw bytes; may be empty
//   - createdAt: the SQLite CURRENT_TIMESTAMP value (RFC3339, UTC)
//
// The returned [32]byte is intended to be stored as BLOB(32) — the
// writer converts via result[:] for the SQL bind.
func ComputeRowHash(prevHash []byte, auditID int64, actor, sessionID string, payload []byte, createdAt string) [HashLen]byte {
	// Avoid the dynamic-allocation chain by reusing a stack buffer.
	// In Go, a single append chain is fine for our payload sizes (a
	// typical payload is <1 KiB; we pre-size to that budget).
	var stack [1024]byte
	buf := stack[:0]

	buf = append(buf, prevHash...)

	var idBytes [8]byte
	binary.BigEndian.PutUint64(idBytes[:], uint64(auditID))
	buf = append(buf, idBytes[:]...)

	buf = append(buf, actor...)
	buf = append(buf, sep...)

	buf = append(buf, sessionID...)
	buf = append(buf, sep...)

	buf = append(buf, payload...)
	buf = append(buf, sep...)

	buf = append(buf, createdAt...)
	buf = append(buf, sep...)

	// sha256.Sum256 allocates the output array. The alloc is tiny
	// (32 bytes on the stack) and happens once per Write.
	return sha256.Sum256(buf)
}

// ZeroHash returns the canonical prev_hash for row 1 (32 zero bytes).
// Exported so verify.go and any future code that needs the canonical
// zero value can refer to it without re-defining it. Returns a fresh
// slice (32 zero bytes from the Go zero value) — callers can mutate
// it without affecting any canonical state. The package-level
// zeroHash is the hot-path reference; this accessor is for callers
// that need an isolated slice (e.g., Verify uses it as the initial
// prevRowHash for the chain start).
func ZeroHash() []byte {
	out := make([]byte, HashLen)
	return out
}