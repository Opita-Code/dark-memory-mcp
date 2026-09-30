package audit_test

// L2 chain tests for audit.Writer + audit.Verify (Phase 2, INV-12).
//
// These tests cover specific cases per dark-testing skill §3.4.1
// (execution-based verification — the strongest oracle). They run
// alongside the L1 unit tests in canonical_test.go and the L1
// property tests in writer_property_test.go.
//
// A14 defense: every assertion uses NON-default values —
// specifically, a deliberately INTRODUCED break is asserted to be
// detected. The chain's invariant is "if you change one byte, the
// verify breaks" — and these tests prove that for the realistic
// failure modes (modification, deletion, rewrite).

import (
	"bytes"
	"context"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// TestExample_HashChain_LinearSequence — 100 Writes, Verify
// returns verified=true with broken_at=0. Baseline happy-path.
func TestExample_HashChain_LinearSequence(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	const N = 100
	for i := 0; i < N; i++ {
		if _, err := w.Write(ctx, "operator-nico", "sess-chain", []byte{byte(i)}); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}

	res, err := audit.Verify(ctx, w.DBG(), 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Verified {
		t.Fatalf("Verify not verified: broken_at=%d, count=%d", res.BrokenAt, res.Count)
	}
	if res.Count != N {
		t.Fatalf("count = %d; want %d", res.Count, N)
	}
	if res.BrokenAt != 0 {
		t.Fatalf("broken_at = %d; want 0", res.BrokenAt)
	}
}

// TestExample_HashChain_PostMigration — simulate a DB with 50
// legacy rows (NULL hashes, from before Phase 2) then 50 chained
// rows. Verify must skip the legacy rows and validate the chained
// portion. The first non-legacy row's prev_hash MUST be zeroHash.
func TestExample_HashChain_PostMigration(t *testing.T) {
	db, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Pre-Phase-2 schema: only 5 columns (no prev_hash, no row_hash).
	if _, err := db.Exec(`
		CREATE TABLE audit_log (
			audit_id   INTEGER PRIMARY KEY AUTOINCREMENT,
			actor      TEXT    NOT NULL CHECK (actor <> ''),
			session_id TEXT,
			payload    BLOB,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatalf("legacy create: %v", err)
	}

	ctx := context.Background()

	// 50 legacy rows. NULL prev_hash and NULL row_hash.
	for i := 0; i < 50; i++ {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO audit_log (actor, session_id, payload) VALUES (?, ?, ?)",
			"operator-legacy", "", []byte{byte(i)},
		); err != nil {
			t.Fatalf("legacy insert[%d]: %v", i, err)
		}
	}

	// Apply the Phase 2 migration.
	if err := audit.ApplyChainColumns(ctx, db); err != nil {
		t.Fatalf("ApplyChainColumns: %v", err)
	}

	// Verify the columns exist now.
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info('audit_log') WHERE name IN ('prev_hash', 'row_hash')",
	).Scan(&n); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if n != 2 {
		t.Fatalf("Phase 2 columns after migration: %d; want 2", n)
	}

	// Now write 50 chained rows.
	w := audit.NewWriter(db)
	for i := 0; i < 50; i++ {
		if _, err := w.Write(ctx, "operator-nico", "sess-post", []byte{byte(i + 100)}); err != nil {
			t.Fatalf("chained Write[%d]: %v", i, err)
		}
	}

	// Verify default range: startID defaults to MIN(audit_id)
	// WHERE row_hash IS NOT NULL — so the 50 legacy rows are NOT
	// walked (they're treated as "before the chain started").
	// The walker only counts and validates the 50 chained rows.
	res, err := audit.Verify(ctx, db, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Verified {
		t.Fatalf("Verify not verified: broken_at=%d, count=%d", res.BrokenAt, res.Count)
	}
	if res.Count != 50 {
		t.Fatalf("count = %d; want 50 (chained only — legacy skipped via default startID)", res.Count)
	}

	// Verify should also pass when explicitly bounded to the
	// chained rows.
	res2, err := audit.Verify(ctx, db, 51, 100)
	if err != nil {
		t.Fatalf("Verify(51,100): %v", err)
	}
	if !res2.Verified {
		t.Fatalf("Verify(51,100) not verified: broken_at=%d", res2.BrokenAt)
	}
	if res2.Count != 50 {
		t.Fatalf("Verify(51,100) count = %d; want 50", res2.Count)
	}
}

// TestExample_HashChain_RestartMidSequence — Writer 1 writes 10
// rows; Writer 2 (new instance, same DB) bootstraps prev_hash from
// the DB and writes 10 more. Verify confirms both segments chain.
func TestExample_HashChain_RestartMidSequence(t *testing.T) {
	db, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	ctx := context.Background()

	// Writer 1: 10 rows.
	w1 := audit.NewWriter(db)
	for i := 0; i < 10; i++ {
		if _, err := w1.Write(ctx, "operator-w1", "sess-w1", []byte{byte(i)}); err != nil {
			t.Fatalf("w1.Write[%d]: %v", i, err)
		}
	}

	// Writer 2: fresh instance, bootstraps from DB.
	w2 := audit.NewWriter(db)
	for i := 0; i < 10; i++ {
		if _, err := w2.Write(ctx, "operator-w2", "sess-w2", []byte{byte(i + 100)}); err != nil {
			t.Fatalf("w2.Write[%d]: %v", i, err)
		}
	}

	// Verify across the full range.
	res, err := audit.Verify(ctx, db, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Verified {
		t.Fatalf("Verify not verified after restart: broken_at=%d, count=%d", res.BrokenAt, res.Count)
	}
	if res.Count != 20 {
		t.Fatalf("count = %d; want 20", res.Count)
	}

	// And: w2.LastHash should equal w1.LastHash chain-extended —
	// they are different processes but w2 bootstrapped from w1's
	// last row, so the chain is continuous. The 20th row's prev_hash
	// should equal the 19th row's row_hash.
	var lastRowHash []byte
	if err := db.QueryRowContext(ctx,
		"SELECT row_hash FROM audit_log WHERE audit_id = 20",
	).Scan(&lastRowHash); err != nil {
		t.Fatalf("select row 20 row_hash: %v", err)
	}
	if !bytes.Equal(lastRowHash, w2.LastHash()) {
		t.Fatalf("w2.LastHash (%x) != row 20 row_hash (%x)", w2.LastHash(), lastRowHash)
	}
}

// TestExample_HashChain_DetectsModification — DELIBERATE BREAK.
// Write 10 rows. Manually UPDATE row 5's row_hash to a corrupted
// value. Verify must detect the modification at audit_id=5.
//
// Per dark-testing §6 Q7: this is the deliberate-break calibration
// — without it, the "verified" verdict is fake.
func TestExample_HashChain_DetectsModification(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := w.Write(ctx, "operator-nico", "sess-detect", []byte{byte(i)}); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}

	// DELIBERATE BREAK: corrupt row 5's row_hash to a fake value.
	// This simulates an attacker (or buggy process) who writes a
	// bad hash into the column.
	fake := bytes.Repeat([]byte{0xAB}, audit.HashLen)
	db := w.DBG()
	if _, err := db.ExecContext(ctx,
		"UPDATE audit_log SET row_hash = ? WHERE audit_id = 5",
		fake,
	); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	res, err := audit.Verify(ctx, db, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Verified {
		t.Fatalf("Verify verified=true after deliberate corruption at id=5; expected detected; res=%+v", res)
	}
	if res.BrokenAt != 5 {
		t.Fatalf("broken_at = %d; want 5", res.BrokenAt)
	}
}

// TestExample_HashChain_DetectsDeletion — DELIBERATE BREAK.
// Write 10 rows. DELETE row 5. Verify must detect the deletion at
// audit_id=6 (because row 6's prev_hash no longer matches the
// previous non-deleted row's row_hash, which is row 4's).
func TestExample_HashChain_DetectsDeletion(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := w.Write(ctx, "operator-nico", "sess-del", []byte{byte(i)}); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}

	db := w.DBG()
	if _, err := db.ExecContext(ctx, "DELETE FROM audit_log WHERE audit_id = 5"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := audit.Verify(ctx, db, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Verified {
		t.Fatalf("Verify verified=true after deletion at id=5; expected detected")
	}
	// Row 6's prev_hash was set to row 5's row_hash. After deletion,
	// row 5 is gone. Row 6's prev_hash still says "row 5's hash",
	// but the previous row in the chain is now row 4. The verify
	// walker computes prevHashExpected = prev non-deleted row's
	// row_hash = row 4's row_hash. Row 6's stored prev_hash ≠ row
	// 4's row_hash → broken_at = 6.
	if res.BrokenAt != 6 {
		t.Fatalf("broken_at = %d; want 6", res.BrokenAt)
	}
}

// TestExample_HashChain_DetectsRewrite — DELIBERATE BREAK.
// Write 10 rows. UPDATE row 5's PAYLOAD. Verify must detect the
// rewrite at audit_id=5 because the stored row_hash no longer
// matches the recomputed hash for the new payload.
//
// This is the "tampering" case — the payload is changed but the
// row_hash is left intact. The chain catches it because row_hash
// is a function of payload.
func TestExample_HashChain_DetectsRewrite(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := w.Write(ctx, "operator-nico", "sess-rw", []byte{byte(i)}); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}

	db := w.DBG()
	// DELIBERATE BREAK: change row 5's payload. The row_hash stays
	// the same (we only change payload). The verify walker will
	// recompute row_hash from the NEW payload and find a mismatch.
	if _, err := db.ExecContext(ctx,
		"UPDATE audit_log SET payload = ? WHERE audit_id = 5",
		[]byte{0xFF, 0xFF, 0xFF},
	); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	res, err := audit.Verify(ctx, db, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Verified {
		t.Fatalf("Verify verified=true after payload rewrite at id=5; expected detected")
	}
	if res.BrokenAt != 5 {
		t.Fatalf("broken_at = %d; want 5", res.BrokenAt)
	}
}

// TestExample_HashChain_DetectsPrevHashModification — DELIBERATE
// BREAK. Modify row 5's prev_hash without changing anything else.
// Verify must detect at row 6 (its prev_hash chain link is wrong).
func TestExample_HashChain_DetectsPrevHashModification(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := w.Write(ctx, "operator-nico", "sess-ph", []byte{byte(i)}); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}

	db := w.DBG()
	fake := bytes.Repeat([]byte{0xCD}, audit.HashLen)
	if _, err := db.ExecContext(ctx,
		"UPDATE audit_log SET prev_hash = ? WHERE audit_id = 5",
		fake,
	); err != nil {
		t.Fatalf("corrupt prev_hash: %v", err)
	}

	res, err := audit.Verify(ctx, db, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Verified {
		t.Fatalf("Verify verified=true after prev_hash corruption at id=5; expected detected at id=6")
	}
	// Row 6's prev_hash was set to row 5's CORRECT row_hash at write
	// time. After the corruption, row 6's prev_hash still says
	// row 5's correct row_hash, but row 5's stored prev_hash is now
	// wrong (it was set to row 4's correct row_hash at write time
	// too — but verify checks row 5 first and detects the prev_hash
	// linkage break... wait. Let me reason:
	//
	// Walk:
	//   row 4: prev_hash = row 3's hash. row_hash = row 4's hash. OK.
	//   row 5: prev_hash = fake. Expected = row 4's row_hash. Mismatch.
	//     → broken_at = 5.
	//
	// Actually the walker checks BOTH prev_hash linkage AND row_hash
	// match. When the walker is at row 5, it sees prev_hash=fake,
	// expected = row 4's row_hash → mismatch → broken_at = 5.
	if res.BrokenAt != 5 {
		t.Fatalf("broken_at = %d; want 5", res.BrokenAt)
	}
}

// TestExample_HashChain_DetectsFirstRowNonZeroPrevHash — DELIBERATE
// BREAK. The first row's prev_hash MUST be zeroHash. Change it to
// a fake. Verify must detect at row 1 (the very first check).
func TestExample_HashChain_DetectsFirstRowNonZeroPrevHash(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	if _, err := w.Write(ctx, "operator-nico", "sess-first", []byte("a")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}

	db := w.DBG()
	fake := bytes.Repeat([]byte{0xEE}, audit.HashLen)
	if _, err := db.ExecContext(ctx,
		"UPDATE audit_log SET prev_hash = ? WHERE audit_id = 1",
		fake,
	); err != nil {
		t.Fatalf("corrupt row 1 prev_hash: %v", err)
	}

	res, err := audit.Verify(ctx, db, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Verified {
		t.Fatalf("Verify verified=true after row 1 prev_hash corruption; expected detected")
	}
	if res.BrokenAt != 1 {
		t.Fatalf("broken_at = %d; want 1", res.BrokenAt)
	}
}

// TestExample_HashChain_ApplyChainColumns_Idempotent — call
// ApplyChainColumns twice; must not error (idempotency is the
// migration contract for existing DBs).
func TestExample_HashChain_ApplyChainColumns_Idempotent(t *testing.T) {
	db, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Pre-Phase-2 schema.
	if _, err := db.Exec(`
		CREATE TABLE audit_log (
			audit_id   INTEGER PRIMARY KEY AUTOINCREMENT,
			actor      TEXT    NOT NULL CHECK (actor <> ''),
			session_id TEXT,
			payload    BLOB,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatalf("legacy create: %v", err)
	}

	ctx := context.Background()

	// First call: adds the columns.
	if err := audit.ApplyChainColumns(ctx, db); err != nil {
		t.Fatalf("ApplyChainColumns (1st): %v", err)
	}

	// Second call: must not error (idempotent).
	if err := audit.ApplyChainColumns(ctx, db); err != nil {
		t.Fatalf("ApplyChainColumns (2nd, idempotent): %v", err)
	}

	// Third call on a DB created with the new schema: also idempotent.
	db2, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	if err := audit.CreateSchema(db2); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.ApplyChainColumns(ctx, db2); err != nil {
		t.Fatalf("ApplyChainColumns on new schema: %v", err)
	}
}

// TestExample_HashChain_VerifyInvertedRange — startID > endID
// returns an error (caller bug). Empty ranges (startID==endID with
// 0 rows in between) are valid and verified=true.
func TestExample_HashChain_VerifyInvertedRange(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()
	if _, err := w.Write(ctx, "operator-nico", "sess-inv", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	db := w.DBG()
	_, err := audit.Verify(ctx, db, 5, 1)
	if err == nil {
		t.Fatalf("Verify(5, 1) expected error; got nil")
	}
	if !contains(err.Error(), "startID=5 > endID=1") {
		t.Fatalf("error should mention the inverted range; got: %v", err)
	}
}

// TestExample_HashChain_VerifyEmptyTable — judge fix F5 (2026-09-30).
// Verify on an empty audit_log returns verified=true with count=0
// (vacuous truth), NOT the inverted-range error. Fresh DBs must pass
// dark_memory_audit_verify out of the box.
func TestExample_HashChain_VerifyEmptyTable(t *testing.T) {
	db, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	res, err := audit.Verify(context.Background(), db, 0, 0)
	if err != nil {
		t.Fatalf("Verify on empty table returned error: %v", err)
	}
	if !res.Verified {
		t.Fatalf("Verify on empty table: verified=false, want true (vacuous truth)")
	}
	if res.Count != 0 {
		t.Fatalf("count = %d; want 0", res.Count)
	}
}

// contains is a tiny helper (substrings.Contains style) to avoid
// importing strings here just for one assertion.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}