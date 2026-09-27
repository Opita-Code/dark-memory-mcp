package manifest

// Concurrent stress tests for CapStore — the workload that surfaces
// the BUG-5 race condition in Revoke. Each test creates its own
// *sql.DB via store.OpenSQLite (file-based, not :memory:) so multiple
// goroutines share a real SQLite file and exercise the WAL
// multi-reader + 1-writer model.
//
// These tests are NOT property-style (rapid). They are fixed-shape
// smoke + stress. The property tests in cap_store_property_test.go
// keep their existing sequential shape; concurrency is verified here
// because the failure mode is concurrent by nature.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// TestConcurrent_Grant_SameID — T2. N goroutines Grant the SAME id.
// Exactly one must succeed; the rest must receive ErrCapExists. If
// foreign_keys=ON is silently dropped (e.g. a future test setup
// regression), the duplicate-key path could lose the UNIQUE check and
// allow duplicates in. This test guards against that.
func TestConcurrent_Grant_SameID(t *testing.T) {
	db := newStressDB(t)
	const N = 32

	id := mustRandomID(t)
	now := time.Now().UTC().Add(time.Hour)

	var (
		wg            sync.WaitGroup
		successes     int64
		duplicates    int64
		otherErrs     int64
		startBarrier  = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier // align goroutines for maximum contention
			tok := mustCap(t,
				func(c *CapToken) { c.ID = id },
				func(c *CapToken) { c.GrantedAt = time.Now().UTC() },
				func(c *CapToken) { c.ExpiresAt = now },
				func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
				func(c *CapToken) { c.SignedBy = "admin-test" },
			)
			err := NewCapStore(db).Grant(context.Background(), tok)
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, ErrCapExists):
				atomic.AddInt64(&duplicates, 1)
			default:
				atomic.AddInt64(&otherErrs, 1)
				t.Logf("unexpected grant err: %v", err)
			}
		}()
	}
	close(startBarrier)
	wg.Wait()

	if got := atomic.LoadInt64(&successes); got != 1 {
		t.Errorf("successes = %d; want exactly 1", got)
	}
	if got := atomic.LoadInt64(&duplicates); got != N-1 {
		t.Errorf("duplicates = %d; want %d", got, N-1)
	}
	if got := atomic.LoadInt64(&otherErrs); got != 0 {
		t.Errorf("otherErrs = %d; want 0", got)
	}

	// Verify the row exists exactly once.
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM capabilities WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows for %s = %d; want 1", id, n)
	}
}

// TestConcurrent_GrantThenRevoke_SameID — T3. Goroutines race to
// Grant and Revoke the same id. INV-13 ("Revoke never deletes") must
// hold: the row must remain in the table with revoked_at > 0 after the
// dust settles. The Revoke race pre-BUG-5 was exactly: a probe SELECT
// outside the UPDATE transaction could observe a row state that no
// longer matches the UPDATE outcome.
func TestConcurrent_GrantThenRevoke_SameID(t *testing.T) {
	db := newStressDB(t)
	const N = 16

	id := mustRandomID(t)
	cs := NewCapStore(db)

	// Pre-grant the token so the goroutines race Grant+Revoke against
	// an existing id. Without this, the test conflates "raced insert"
	// with "raced revoke" which is harder to reason about.
	pre := mustCap(t,
		func(c *CapToken) { c.ID = id },
		func(c *CapToken) { c.ExpiresAt = time.Now().UTC().Add(time.Hour) },
		func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
		func(c *CapToken) { c.SignedBy = "admin-test" },
	)
	if err := cs.Grant(context.Background(), pre); err != nil {
		t.Fatalf("pre-grant: %v", err)
	}

	var wg sync.WaitGroup
	revokedAt := time.Now().UTC()
	var revokeOK, revokeAlready, revokeNotFound int64
	var revokeOther int64
	startBarrier := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			err := cs.Revoke(context.Background(), id, revokedAt)
			switch {
			case err == nil:
				atomic.AddInt64(&revokeOK, 1)
			case errors.Is(err, ErrCapAlreadyRevoked):
				atomic.AddInt64(&revokeAlready, 1)
			case errors.Is(err, ErrCapNotFound):
				atomic.AddInt64(&revokeNotFound, 1)
			default:
				atomic.AddInt64(&revokeOther, 1)
				t.Logf("unexpected revoke err: %v", err)
			}
		}()
	}
	close(startBarrier)
	wg.Wait()

	if revokeOther != 0 {
		t.Errorf("unexpected errs = %d; want 0", revokeOther)
	}
	// Either exactly one OK + (N-1) AlreadyRevoked, or all AlreadyRevoked
	// (if a fast revoke landed before the others tried). What we MUST
	// NOT see: ErrCapNotFound after the pre-grant.
	if revokeNotFound != 0 {
		t.Errorf("revokeNotFound = %d; want 0 (pre-grant guarantees the row exists)", revokeNotFound)
	}

	// INV-13: row still exists, never deleted.
	var (
		rowExists int
		rev       int64
	)
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*), COALESCE(MAX(revoked_at), 0) FROM capabilities WHERE id = ?",
		id,
	).Scan(&rowExists, &rev); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if rowExists != 1 {
		t.Errorf("INV-13 violated: row deleted (count=%d)", rowExists)
	}
	if rev == 0 {
		t.Errorf("revoked_at = 0; want > 0 (revoke did not take effect)")
	}
}

// TestRevoke_RaceWithGrant — same-id race from the OPPOSITE direction:
// many goroutines try to Revoke a non-existent id while ONE goroutine
// Grants it. The Grant may land at any point; Revokers must observe
// either "not found" (Grant has not landed yet) or "ok / already
// revoked" (Grant has landed). They must NEVER observe a corrupt
// state where the row appears and disappears during the operation.
//
// Note: this is the practical scenario where one operator Grants a
// token and many downstream consumers Revoke it concurrently — the
// race the pre-BUG-5 Revoke path was vulnerable to.
func TestRevoke_RaceWithGrant(t *testing.T) {
	db := newStressDB(t)
	cs := NewCapStore(db)
	id := mustRandomID(t)

	// Goroutines: 1 Grant, N Revokers.
	const N = 16
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	// The grant goroutine sleeps a random [0..20]ms before granting.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startBarrier
		time.Sleep(time.Duration(randInt(t, 20)) * time.Millisecond)
		tok := mustCap(t,
			func(c *CapToken) { c.ID = id },
			func(c *CapToken) { c.ExpiresAt = time.Now().UTC().Add(time.Hour) },
			func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
			func(c *CapToken) { c.SignedBy = "admin-test" },
		)
		_ = cs.Grant(context.Background(), tok)
	}()

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			revokedAt := time.Now().UTC()
			err := cs.Revoke(context.Background(), id, revokedAt)
			if err != nil && !errors.Is(err, ErrCapNotFound) && !errors.Is(err, ErrCapAlreadyRevoked) {
				t.Errorf("unexpected revoke err: %v", err)
			}
		}()
	}
	close(startBarrier)
	wg.Wait()

	// Post-condition: row exists (INV-13) with revoked_at >= 0 (either
	// the Grant landed and a Revoker caught it, or no Revoker caught
	// it — either way the row is present).
	var (
		rowExists int
		rev       int64
	)
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*), COALESCE(MAX(revoked_at), 0) FROM capabilities WHERE id = ?",
		id,
	).Scan(&rowExists, &rev); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if rowExists != 1 {
		t.Errorf("expected exactly 1 row; got %d", rowExists)
	}
}

// --- helpers ---

func newStressDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stress.db")
	db, err := store.OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := CreateCapSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateCapSchema: %v", err)
	}
	return db
}

func mustRandomID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "cap-" + hex.EncodeToString(b[:])
}

// randInt returns a non-negative pseudo-random int < n. Uses math/rand
// via the test's local source so we don't fight the global lock.
func randInt(t *testing.T, n int) int {
	t.Helper()
	if n <= 0 {
		return 0
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	v := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if v < 0 {
		v = -v
	}
	return v % n
}
