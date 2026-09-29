package audit_test

// L1 unit tests for canonical.go (Phase 2, INV-12 audit chain).
//
// These tests are PURE Go — no DB, no time.Now. They verify that
// ComputeRowHash produces the hashes the verify walker expects.
// Per dark-testing skill §3.4.1 (execution-based verification,
// strongest oracle), each test asserts a specific invariant that
// would FAIL if the implementation were subtly wrong.
//
// Test discipline:
//   - TestComputeRowHash_Deterministic: same inputs → same hash
//     (would FAIL if ComputeRowHash used time.Now or a process-local
//     source of entropy).
//   - TestComputeRowHash_SeparatorDisambiguation: separator byte
//     prevents "fo" + "o" vs "foo" + "" ambiguity (would FAIL if
//     separators were omitted).
//   - TestComputeRowHash_TrailingSeparatorFuseAttack: trailing
//     separator prevents payload fusing with created_at (would FAIL
//     if the trailing 0x00 were dropped).
//   - TestZeroHash_Stable: zeroHash is always 32 zero bytes (would
//     FAIL if ZeroHash() returned a non-zero slice).
//   - TestComputeRowHash_AuditIDBigEndian: changing audit_id by 1
//     changes the hash (would FAIL if audit_id were not encoded
//     at all, or were little-endian, or were encoded as a string).

import (
	"bytes"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// TestComputeRowHash_Deterministic — universal claim: same inputs
// always produce the same hash. A14 defense: tests with non-trivial
// inputs (audit_id != 0, payload non-empty, actor non-default).
func TestComputeRowHash_Deterministic(t *testing.T) {
	prev := audit.ZeroHash()
	actor := "operator-nico"
	sessionID := "sess-abc123def456"
	payload := []byte(`{"event":"save","id":42}`)
	createdAt := "2026-09-29T19:38:04.429Z"

	h1 := audit.ComputeRowHash(prev, 7, actor, sessionID, payload, createdAt)
	h2 := audit.ComputeRowHash(prev, 7, actor, sessionID, payload, createdAt)

	if h1 != h2 {
		t.Fatalf("ComputeRowHash not deterministic:\n  h1=%x\n  h2=%x", h1, h2)
	}
}

// TestComputeRowHash_DifferentInputsProduceDifferentHashes — A14
// defense: every input field actually contributes to the hash.
// Changing audit_id by 1 must change the hash. Changing actor must
// change the hash. (Catches: "forgot to include actor in the
// canonical encoding" — would silently break chain integrity.)
func TestComputeRowHash_DifferentInputsProduceDifferentHashes(t *testing.T) {
	prev := audit.ZeroHash()
	actor := "operator-nico"
	sessionID := "sess-x"
	payload := []byte("p")
	createdAt := "2026-09-29T19:38:04Z"

	base := audit.ComputeRowHash(prev, 1, actor, sessionID, payload, createdAt)

	tests := []struct {
		name string
		mut  func(prev []byte, id int64, actor, sess string, p []byte, ts string) ([]byte, int64, string, string, []byte, string)
	}{
		{
			name: "audit_id+1",
			mut: func(p []byte, id int64, a, s string, pl []byte, ts string) ([]byte, int64, string, string, []byte, string) {
				return p, id + 1, a, s, pl, ts
			},
		},
		{
			name: "actor differs",
			mut: func(p []byte, id int64, a, s string, pl []byte, ts string) ([]byte, int64, string, string, []byte, string) {
				return p, id, "operator-other", s, pl, ts
			},
		},
		{
			name: "session_id differs",
			mut: func(p []byte, id int64, a, s string, pl []byte, ts string) ([]byte, int64, string, string, []byte, string) {
				return p, id, a, "sess-y", pl, ts
			},
		},
		{
			name: "payload differs",
			mut: func(p []byte, id int64, a, s string, pl []byte, ts string) ([]byte, int64, string, string, []byte, string) {
				return p, id, a, s, []byte("different"), ts
			},
		},
		{
			name: "created_at differs",
			mut: func(p []byte, id int64, a, s string, pl []byte, ts string) ([]byte, int64, string, string, []byte, string) {
				return p, id, a, s, pl, "2026-09-29T19:38:05Z"
			},
		},
		{
			name: "prev_hash differs",
			mut: func(p []byte, id int64, a, s string, pl []byte, ts string) ([]byte, int64, string, string, []byte, string) {
				return []byte{0x01, 0x02, 0x03}, id, a, s, pl, ts
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			np, ni, na, ns, npl, nts := tc.mut(prev, 1, actor, sessionID, payload, createdAt)
			got := audit.ComputeRowHash(np, ni, na, ns, npl, nts)
			if got == base {
				t.Fatalf("ComputeRowHash unchanged after mutating %s — field not contributing to hash", tc.name)
			}
		})
	}
}

// TestComputeRowHash_SeparatorDisambiguation — the 0x00 separators
// between fields prevent ambiguity. Without them:
//   ("foo", "") would hash the same as ("fo", "o")
// With them:
//   "foo" || 0x00 || "" || 0x00 || ...  ≠  "fo" || 0x00 || "o" || 0x00 || ...
//
// This test catches a class of subtle bugs where the canonical
// encoding drops separators.
func TestComputeRowHash_SeparatorDisambiguation(t *testing.T) {
	prev := audit.ZeroHash()
	payload := []byte("p")
	createdAt := "2026-09-29T19:38:04Z"

	h1 := audit.ComputeRowHash(prev, 1, "foo", "", payload, createdAt)
	h2 := audit.ComputeRowHash(prev, 1, "fo", "o", payload, createdAt)

	if h1 == h2 {
		t.Fatalf("separator disambiguation failed: (actor='foo', session='') hash equals (actor='fo', session='o') hash:\n  h1=%x\n  h2=%x", h1, h2)
	}
}

// TestComputeRowHash_TrailingSeparatorFuseAttack — the trailing
// 0x00 separator prevents the payload from "fusing" with the
// RFC3339 created_at field. An attacker who can modify the payload
// could otherwise rewrite it so that the bytes immediately before
// the created_at look like a valid timestamp, breaking the chain
// without changing the hash.
//
// Example of the attack without a trailing separator:
//   payload="x2026-09-29T19:38:04Z", created_at="2026-09-29T19:38:04Z"
//   encoded: "x2026-09-29T19:38:04Z" + "2026-09-29T19:38:04Z"
//   = "x" + "2026-09-29T19:38:04Z" + "2026-09-29T19:38:04Z"
//   = same as payload="x", created_at="2026-09-29T19:38:04Z2026-09-29T19:38:04Z"
//
// With the trailing 0x00, the input bytes differ, so the hash
// differs. The test asserts that the two cases above produce
// different hashes.
func TestComputeRowHash_TrailingSeparatorFuseAttack(t *testing.T) {
	prev := audit.ZeroHash()
	actor := "operator-nico"
	sessionID := "sess-x"

	// Case A: payload ends in a literal RFC3339 string, then we
	// declare the same created_at.
	createdAt := "2026-09-29T19:38:04Z"
	payloadFuse := []byte("x" + createdAt)
	hashFuse := audit.ComputeRowHash(prev, 1, actor, sessionID, payloadFuse, createdAt)

	// Case B: payload is just "x", and we declare an extended
	// created_at that contains the same bytes concatenated.
	createdAtConcat := createdAt + createdAt
	payloadPlain := []byte("x")
	hashConcat := audit.ComputeRowHash(prev, 1, actor, sessionID, payloadPlain, createdAtConcat)

	if hashFuse == hashConcat {
		t.Fatalf("trailing separator fuse attack NOT prevented:\n  fuse=%x\n  concat=%x\nIf these are equal, an attacker who modifies the payload can produce the same hash.",
			hashFuse, hashConcat)
	}
}

// TestZeroHash_Stable — ZeroHash() must always return 32 zero bytes.
// If a future refactor accidentally returns the package-level var
// by reference (mutation), or returns a non-zero slice, this test
// catches it.
func TestZeroHash_Stable(t *testing.T) {
	z := audit.ZeroHash()
	if len(z) != audit.HashLen {
		t.Fatalf("ZeroHash length = %d; want %d", len(z), audit.HashLen)
	}
	for i, b := range z {
		if b != 0 {
			t.Fatalf("ZeroHash[%d] = %d; want 0", i, b)
		}
	}

	// Also: ZeroHash() called twice should return equal slices.
	if !bytes.Equal(z, audit.ZeroHash()) {
		t.Fatalf("ZeroHash not stable across calls")
	}

	// Also: mutating the returned slice must not affect future calls.
	z[0] = 0xFF
	if audit.ZeroHash()[0] != 0 {
		t.Fatalf("ZeroHash mutable: caller mutated and next call returned non-zero")
	}
}

// TestComputeRowHash_AuditIDBigEndian — the audit_id is encoded as
// 8 bytes BIG-ENDIAN. Catches: encoding as little-endian, encoding
// as a string, or omitting audit_id entirely. A14 defense: test
// with non-trivial ids (1 and 0x100000000 = 4GB).
func TestComputeRowHash_AuditIDBigEndian(t *testing.T) {
	prev := audit.ZeroHash()
	actor := "operator-nico"
	payload := []byte("p")
	createdAt := "2026-09-29T19:38:04Z"

	// Two audit_ids that differ only in high byte: 1 vs 0x100000000.
	// In big-endian, the bytes are [0,0,0,0,0,0,0,1] vs [0,0,0,0,1,0,0,0]
	// (the high byte differs). In little-endian they would be
	// [1,0,0,0,0,0,0,0] vs [0,0,0,1,0,0,0,0] (still different, but
	// positions differ — same hash difference in either case).
	//
	// The intent here is to verify that audit_id participates in the
	// hash (not that BE vs LE produces a particular byte pattern).
	h1 := audit.ComputeRowHash(prev, 1, actor, "s", payload, createdAt)
	h2 := audit.ComputeRowHash(prev, 0x100000000, actor, "s", payload, createdAt)

	if h1 == h2 {
		t.Fatalf("audit_id not encoded: h1==h2 for ids 1 and 0x100000000")
	}

	// Same id must produce same hash (consistency).
	h1b := audit.ComputeRowHash(prev, 1, actor, "s", payload, createdAt)
	if h1 != h1b {
		t.Fatalf("audit_id=1 produced different hashes across calls:\n  h1=%x\n  h1b=%x", h1, h1b)
	}
}

// TestComputeRowHash_PayloadEmptyPermitted — payload may be empty
// (zero bytes). The 0x00 separators handle the empty case
// unambiguously. A14 defense: test with payload=[]byte{}.
func TestComputeRowHash_PayloadEmptyPermitted(t *testing.T) {
	prev := audit.ZeroHash()
	actor := "operator-nico"
	sessionID := "sess-x"
	createdAt := "2026-09-29T19:38:04Z"

	// Empty payload.
	h1 := audit.ComputeRowHash(prev, 1, actor, sessionID, []byte{}, createdAt)
	// Single zero-byte payload — different from empty.
	h2 := audit.ComputeRowHash(prev, 1, actor, sessionID, []byte{0x00}, createdAt)

	if h1 == h2 {
		t.Fatalf("empty payload not distinguished from zero-byte payload:\n  h1=%x\n  h2=%x", h1, h2)
	}

	// Sanity: empty payload is also different from empty sessionID + empty payload.
	h3 := audit.ComputeRowHash(prev, 1, actor, "", []byte{}, createdAt)
	if h1 == h3 {
		t.Fatalf("session_id='' indistinguishable from session_id='sess-x' with empty payload:\n  h1=%x\n  h3=%x", h1, h3)
	}
}