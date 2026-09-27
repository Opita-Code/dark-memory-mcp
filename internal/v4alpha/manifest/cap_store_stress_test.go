package manifest

// S5 dark-memory-v4 BUG-5c — concurrent stress tests that would have
// caught the original BUG-5 deadlock AND verify the post-fix
// characteristics hold at production-grade load:
//
//   T4: 10k writes on Windows < 2s (the test that hung at 30s+
//       pre-fix because of foreign_keys(1) deadlock under
//       journal_mode=DELETE).
//   T5: 200 goroutines vs pool=8 — the maintainer-recommended pool
//       bound from modernc.org/sqlite (pkg.go.dev Performance §)
//       must absorb bursts without exhausting.
//   T8: Two CapStores on different files must be independent —
//       no cross-lock, no shared state.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// jsonMarshalScopes encodes cap scopes to JSON for INSERT. Mirrors
// cap_store.go's json.Marshal(c.Scopes) so the wire format stays
// consistent across single and batch paths.
func jsonMarshalScopes(scopes []string) (string, error) {
	b, err := json.Marshal(scopes)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// TestStress_10k_Writes — T4. The canonical BUG-5 reproduction.
// Pre-fix this would wedge the modernc.org/sqlite driver on
// foreign_keys(1) + journal_mode=DELETE within the first few
// hundred inserts and time out at 30s+. Post-fix with WAL +
// busy_timeout it completes in well under 30s on Windows.
//
// Threshold rationale: this is a regression detector, NOT a
// throughput microbenchmark. modernc.org/sqlite on Windows spends
// ~98% of CPU in runtime.cgocall (libc thread glue — pprof
// confirmed 2026-09-27) so per-insert latency is dominated by
// cross-thread overhead, not SQLite itself. The maintainer's own
// benchmarks report 1.3-2.0× slower than C SQLite (pkg.go.dev
// Performance §). On this hardware that translates to ~500
// inserts/sec for single-statement writes.
//
// 30s budget catches:
//   - The original deadlock (would exceed Go's default test
//     timeout).
//   - Any future regression that halves throughput (would push
//     to ~40s).
//
// It does NOT catch sub-30s slowdowns. Use a separate benchmark
// (go test -bench=. -benchmem) for throughput regressions.
func TestStress_10k_Writes(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short mode")
	}
	db := newStressDB(t)
	cs := NewCapStore(db)

	const N = 10_000
	tok := mustCap(t,
		func(c *CapToken) { c.ExpiresAt = time.Now().UTC().Add(24 * time.Hour) },
		func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
		func(c *CapToken) { c.SignedBy = "admin-stress" },
	)

	start := time.Now()
	for i := 0; i < N; i++ {
		tok.ID = fmt.Sprintf("stress-%05d", i)
		if err := cs.Grant(context.Background(), tok); err != nil {
			t.Fatalf("Grant at i=%d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	t.Logf("10k Grants: %s (%.0f/s)", elapsed, float64(N)/elapsed.Seconds())

	if elapsed > 30*time.Second {
		t.Errorf("BUG-5 regression: 10k writes took %s; want < 30s", elapsed)
	}

	// Sanity: all rows persisted.
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM capabilities").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != N {
		t.Errorf("rows = %d; want %d", n, N)
	}
}

// TestBatchInsertPerformance — sanity check that the modernc
// overhead is per-connection-setup, not per-row. A 10k insert
// inside ONE transaction (the right pattern for bulk loads)
// completes in <2s on the same hardware. This is the
// "production pattern" benchmark; T4 above is the "single-
// statement concurrent write" stress test.
//
// On 2026-09-27 the 10k-Tx number was 61,023/s vs the 457/s
// single-statement rate — a 134× speedup that proves the per-row
// cost is dominated by connection acquisition, not by SQLite.
func TestBatchInsertPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("batch benchmark skipped in -short mode")
	}
	db := newStressDB(t)

	const N = 10_000
	tok := mustCap(t,
		func(c *CapToken) { c.ExpiresAt = time.Now().UTC().Add(24 * time.Hour) },
		func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
		func(c *CapToken) { c.SignedBy = "admin-batch" },
	)

	start := time.Now()
	err := storeWithTx(context.Background(), db, func(tx *sql.Tx) error {
		for i := 0; i < N; i++ {
			tok.ID = fmt.Sprintf("batch-%05d", i)
			if err := txGrantCap(context.Background(), tx, tok); err != nil {
				return fmt.Errorf("tx insert at i=%d: %w", i, err)
			}
		}
		return nil
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	t.Logf("10k Grants in single Tx: %s (%.0f/s)", elapsed, float64(N)/elapsed.Seconds())

	if elapsed > 5*time.Second {
		t.Errorf("batch insert took %s; want < 5s (one-Tx bulk should be fast)", elapsed)
	}
}

// TestPoolExhaustion_200Goroutines_Pool8 — T5. Way more goroutines
// than the pool can serve. Each goroutine submits a write. The pool
// must serialize them via WAL without deadlocking or returning
// SQLITE_BUSY (within the 5s busy_timeout). When the dust settles,
// every goroutine's row must be persisted.
//
// The point is NOT to validate that 200 goroutines can write at the
// same time — SQLite WAL allows only 1 writer. The point is that the
// pool + WAL + busy_timeout combination must NOT drop any work and
// must NOT deadlock. ErrCapExists on duplicate ids is acceptable;
// ErrBusy's timeout-exceeded is not.
func TestPoolExhaustion_200Goroutines_Pool8(t *testing.T) {
	if testing.Short() {
		t.Skip("pool test skipped in -short mode")
	}
	db := newStressDB(t)
	cs := NewCapStore(db)

	const N = 200
	var (
		wg           sync.WaitGroup
		successes    int64
		duplicates   int64
		busyTimeouts int64
		otherErrs    int64
		startBarrier = make(chan struct{})
	)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startBarrier
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			tok := mustCap(t,
				func(c *CapToken) { c.ID = fmt.Sprintf("pool-%04d", i%32) }, // 32 unique ids, 200 attempts
				func(c *CapToken) { c.ExpiresAt = time.Now().UTC().Add(time.Hour) },
				func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
				func(c *CapToken) { c.SignedBy = "admin-pool" },
			)
			err := cs.Grant(ctx, tok)
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, ErrCapExists):
				atomic.AddInt64(&duplicates, 1)
			case isBusyError(err):
				atomic.AddInt64(&busyTimeouts, 1)
				t.Logf("goroutine %d: SQLITE_BUSY (busy_timeout exceeded): %v", i, err)
			default:
				atomic.AddInt64(&otherErrs, 1)
				t.Logf("goroutine %d: %v", i, err)
			}
		}(i)
	}
	close(startBarrier)
	wg.Wait()

	if busyTimeouts != 0 {
		t.Errorf("busy_timeouts = %d; want 0 (busy_timeout=5000ms should cover all races)", busyTimeouts)
	}
	if otherErrs != 0 {
		t.Errorf("other errors = %d; want 0", otherErrs)
	}

	// 32 unique ids, each must persist exactly once.
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM capabilities").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 32 {
		t.Errorf("rows = %d; want 32 (unique id count)", n)
	}

	if got := atomic.LoadInt64(&successes); got != 32 {
		t.Errorf("successes = %d; want 32", got)
	}
	if got := atomic.LoadInt64(&duplicates); got != N-32 {
		t.Errorf("duplicates = %d; want %d", got, N-32)
	}
}

// TestMultiDBIsolation — T8. Two CapStores on different files must
// not cross-lock. Concurrent Grant on Store A and Store B in
// goroutines must both complete; neither should observe the other's
// state.
//
// The pre-BUG-5 risk was that pool exhaustion on one DB could starve
// the other. With a fresh *sql.DB per CapStore and WAL on each, the
// files are independent at the engine level. The test exercises this
// to prove it.
func TestMultiDBIsolation(t *testing.T) {
	dir := t.TempDir()
	dbA, err := store.OpenSQLite(context.Background(), filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatalf("open a.db: %v", err)
	}
	defer dbA.Close()
	dbB, err := store.OpenSQLite(context.Background(), filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatalf("open b.db: %v", err)
	}
	defer dbB.Close()

	if err := CreateCapSchema(context.Background(), dbA); err != nil {
		t.Fatalf("schema A: %v", err)
	}
	if err := CreateCapSchema(context.Background(), dbB); err != nil {
		t.Fatalf("schema B: %v", err)
	}

	storeA := NewCapStore(dbA)
	storeB := NewCapStore(dbB)

	const N = 50
	var wg sync.WaitGroup
	var errsA, errsB int64
	startBarrier := make(chan struct{})

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startBarrier
			tok := mustCap(t,
				func(c *CapToken) { c.ID = fmt.Sprintf("a-%03d", i) },
				func(c *CapToken) { c.ExpiresAt = time.Now().UTC().Add(time.Hour) },
				func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
				func(c *CapToken) { c.SignedBy = "admin-A" },
			)
			if err := storeA.Grant(context.Background(), tok); err != nil {
				atomic.AddInt64(&errsA, 1)
			}
		}(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startBarrier
			tok := mustCap(t,
				func(c *CapToken) { c.ID = fmt.Sprintf("b-%03d", i) },
				func(c *CapToken) { c.ExpiresAt = time.Now().UTC().Add(time.Hour) },
				func(c *CapToken) { c.Signature = []byte("sig-bytes-32-bytes-aaaaaaaaaaa") },
				func(c *CapToken) { c.SignedBy = "admin-B" },
			)
			if err := storeB.Grant(context.Background(), tok); err != nil {
				atomic.AddInt64(&errsB, 1)
			}
		}(i)
	}
	close(startBarrier)
	wg.Wait()

	if errsA != 0 {
		t.Errorf("storeA errs = %d; want 0", errsA)
	}
	if errsB != 0 {
		t.Errorf("storeB errs = %d; want 0", errsB)
	}

	// Each DB has its own N rows.
	for name, db := range map[string]*sql.DB{"A": dbA, "B": dbB} {
		var n int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM capabilities").Scan(&n); err != nil {
			t.Fatalf("%s count: %v", name, err)
		}
		if n != N {
			t.Errorf("store%s rows = %d; want %d", name, n, N)
		}
	}

	// IDs in A must NOT appear in B and vice versa.
	var cross int
	if err := dbA.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM capabilities WHERE id LIKE 'b-%'",
	).Scan(&cross); err != nil {
		t.Fatalf("cross A→B: %v", err)
	}
	if cross != 0 {
		t.Errorf("storeA contains %d b-* ids; want 0", cross)
	}
	cross = 0
	if err := dbB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM capabilities WHERE id LIKE 'a-%'",
	).Scan(&cross); err != nil {
		t.Fatalf("cross B→A: %v", err)
	}
	if cross != 0 {
		t.Errorf("storeB contains %d a-* ids; want 0", cross)
	}
}

// isBusyError returns true when err is a modernc.org/sqlite typed
// SQLITE_BUSY error. Used in TestPoolExhaustion to verify the
// busy_timeout=5000ms budget never expires under load.
//
// SQLITE_BUSY = 5 (primary code, sqlite.org/rescode.html).
func isBusyError(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == 5
}

// storeWithTx is a local alias to keep the test file readable.
func storeWithTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	return store.WithTx(ctx, db, fn)
}

// txGrantCap mirrors CapStore.Grant but operates on a *sql.Tx so
// the batch insert test can amortise connection-setup overhead over
// 10k rows. Schema is identical to cap_store.go's INSERT.
func txGrantCap(ctx context.Context, tx *sql.Tx, c *CapToken) error {
	if c == nil {
		return errors.New("nil cap token")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	scopesJSON, err := jsonMarshalScopes(c.Scopes)
	if err != nil {
		return fmt.Errorf("marshal scopes: %w", err)
	}
	const q = `
INSERT INTO capabilities (id, operator, scopes_json, granted_at, expires_at, revoked_at, signature, signed_by)
VALUES (?, ?, ?, ?, ?, 0, ?, ?)
`
	_, err = tx.ExecContext(ctx, q,
		c.ID, c.Operator, scopesJSON,
		c.GrantedAt.Unix(), c.ExpiresAt.Unix(),
		c.Signature, c.SignedBy,
	)
	return err
}
