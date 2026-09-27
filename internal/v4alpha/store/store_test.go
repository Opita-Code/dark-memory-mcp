package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestOpenSQLite_DSNContainsAllPragmas — deliberate-break guard.
// The pragma set is the BUG-5 design contract; if a future change drops
// any of these, multi-agent concurrent writes will deadlock. This test
// pins the DSN form so the regression cannot pass CI silently.
func TestOpenSQLite_DSNContainsAllPragmas(t *testing.T) {
	cases := []struct {
		name   string
		substr string
	}{
		{"busy_timeout", "busy_timeout(5000)"},
		{"foreign_keys", "foreign_keys(1)"},
		{"journal_mode_WAL", "journal_mode(WAL)"},
		{"synchronous_NORMAL", "synchronous(NORMAL)"},
		{"wal_autocheckpoint", "wal_autocheckpoint(1000)"},
		{"cache_size", "cache_size(-2000)"},
		{"temp_store", "temp_store(MEMORY)"},
	}
	dsn := buildDSNForTest("/tmp/test.db")
	for _, tc := range cases {
		if !strings.Contains(dsn, tc.substr) {
			t.Errorf("DSN missing %s (%q)\n--- DSN ---\n%s", tc.name, tc.substr, dsn)
		}
	}
}

// TestOpenSQLite_JournalModeIsWAL — confirms the pragma actually took
// effect at the engine level (not just in the DSN string). If WAL mode
// were rejected at open time, OpenSQLite would have returned an error
// — but a regression where the pragma is silently ignored would slip
// past that. This test queries pragma journal_mode directly.
func TestOpenSQLite_JournalModeIsWAL(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	var mode string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("journal_mode query: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q; want wal", mode)
	}
}

// TestOpenSQLite_BusyTimeoutApplied — busy_timeout is per-connection.
// This test asserts the value lands at 5000ms on a fresh connection
// from the pool, not just in the DSN string.
func TestOpenSQLite_BusyTimeoutApplied(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	var got int
	if err := db.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&got); err != nil {
		t.Fatalf("busy_timeout query: %v", err)
	}
	if got != 5000 {
		t.Errorf("busy_timeout = %d; want 5000", got)
	}
}

// TestOpenSQLite_PoolBounds — SetMaxOpenConns(N) caps in-flight queries.
// This is the maintainer-recommended invariant from modernc.org/sqlite
// (pkg.go.dev Performance §). If a future change drops the bound, the
// "unbounded connections" anti-pattern returns and per-connection state
// (page cache, libc thread) piles up.
func TestOpenSQLite_PoolBounds(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := db.Stats()
	if s.MaxOpenConnections != DefaultMaxOpenConns {
		t.Errorf("MaxOpenConnections = %d; want %d", s.MaxOpenConnections, DefaultMaxOpenConns)
	}
}

// TestOpenSQLite_PreservesExistingQuery — callers may pass a DSN that
// already carries query parameters (e.g. ":memory:" + cache=shared).
// The pragma list must be ADDED, not REPLACE.
func TestOpenSQLite_PreservesExistingQuery(t *testing.T) {
	dsn := buildDSNForTest("file::memory:?cache=shared")
	for _, want := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "cache=shared"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN missing %q\n--- DSN ---\n%s", want, dsn)
		}
	}
}

// TestWithTx_CommitsOnSuccess — happy path: fn returns nil → commit.
func TestWithTx_CommitsOnSuccess(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	setupTable(t, db)

	err := WithTx(context.Background(), db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			"INSERT INTO kv (k, v) VALUES (?, ?)", "alpha", 1)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kv").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows after commit = %d; want 1", n)
	}
}

// TestWithTx_RollsBackOnError — fn returns non-nil → rollback; no rows
// visible afterwards. T6 in the BUG-5 plan.
func TestWithTx_RollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	setupTable(t, db)

	sentinel := errors.New("caller aborted")
	err := WithTx(context.Background(), db, func(tx *sql.Tx) error {
		_, _ = tx.ExecContext(context.Background(),
			"INSERT INTO kv (k, v) VALUES (?, ?)", "beta", 2)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx err = %v; want sentinel wrapped", err)
	}
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kv").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rows after rollback = %d; want 0", n)
	}
}

// TestWithTx_RollsBackOnPanic — caller panics → rollback → panic
// re-raised. Critical for not leaving half-committed audit state when
// an operator crashes mid-transaction.
func TestWithTx_RollsBackOnPanic(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	setupTable(t, db)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic, got nil")
		}
		var n int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kv").Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Errorf("rows after panic-rollback = %d; want 0", n)
		}
	}()

	_ = WithTx(context.Background(), db, func(tx *sql.Tx) error {
		_, _ = tx.ExecContext(context.Background(),
			"INSERT INTO kv (k, v) VALUES (?, ?)", "gamma", 3)
		panic("caller panic")
	})
}

// TestOpenSQLite_ConcurrentOpens — T7 (schema migration race). N
// goroutines open the SAME path simultaneously and each runs the
// idempotent schema. With WAL + busy_timeout, only ONE DDL effectively
// runs and all callers see a clean schema; without WAL the second
// concurrent PRAGMA journal_mode=WAL can wedge.
func TestOpenSQLite_ConcurrentOpens(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows file-locking semantics around concurrent journal_mode
		// transitions are exactly what we're testing — do not skip.
	}
	dir := t.TempDir()
	dsnPath := filepath.Join(dir, "race.db")
	const n = 16

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db, err := OpenSQLite(ctx, dsnPath)
			if err != nil {
				errs[i] = err
				return
			}
			defer db.Close()
			// Idempotent schema: every caller runs it.
			if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS probe (id INTEGER PRIMARY KEY)`); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: %v", i, e)
		}
	}
	// Final opener must see the table from any of the racers.
	db, err := OpenSQLite(context.Background(), dsnPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	var nRows int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM probe").Scan(&nRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	// Just confirming the table exists; rows == 0 is fine.
	_ = nRows
}

// TestConcurrent_Grant_DistinctIDs_Smoke — T1 (concurrent grants
// against distinct ids). Smoke version kept inside store_test.go to
// prove the package-level infrastructure survives the workload that
// caused the original BUG-5 deadlock. The full property-style test
// lives in cap_store_concurrent_test.go (commit 5c).
func TestConcurrent_Grant_DistinctIDs_Smoke(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	setupTable(t, db)

	const N = 64
	var wg sync.WaitGroup
	var failed int64
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := WithTx(ctx, db, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx,
					"INSERT INTO kv (k, v) VALUES (?, ?)",
					keyName(i), i)
				return err
			})
			if err != nil {
				atomic.AddInt64(&failed, 1)
				t.Logf("goroutine %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := atomic.LoadInt64(&failed); got != 0 {
		t.Errorf("%d/%d goroutines failed", got, N)
	}
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kv").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != N {
		t.Errorf("rows = %d; want %d", n, N)
	}
}

// --- helpers ---

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func setupTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v INTEGER NOT NULL)`); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func keyName(i int) string {
	return "key-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	buf := make([]byte, 0, 8)
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// buildDSNForTest exposes the package-private buildDSN for assertion
// in the DSN-shape tests.
func buildDSNForTest(path string) string {
	dsn, err := buildDSN(path)
	if err != nil {
		panic(err)
	}
	return dsn
}
