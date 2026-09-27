// Package store provides the dark-db SQLite access layer.
//
// It centralises the connection setup and transaction wrapper used by
// every v4alpha package that touches a *sql.DB. The design is informed
// by the tier-1 research recorded in dark-memory agent_memory row for
// the BUG-5 SQLite-concurrency design (2026-09-27).
//
// The two helpers exported here, OpenSQLite and WithTx, embody the
// following non-negotiable invariants for the dark-db:
//
//   - journal_mode=WAL: persistent, multi-reader + single-writer. SQLite
//     serialises writers via the WAL file (sqlite.org/wal.html §2.2).
//   - busy_timeout=5000: connection waits 5 s on a lock before returning
//     SQLITE_BUSY. Per-connection; modernc.org/sqlite applies this pragma
//     FIRST in its DSN ordering (sqlite.go:217-230 in v1.53.0).
//   - synchronous=NORMAL: fsync only on checkpoint, never on writer
//     commit. Eliminates the "writable commits block on disk I/O"
//     failure class under load (sqlite.org/wal.html §2.3).
//   - foreign_keys=ON: schema integrity, already present before the fix.
//   - SetMaxOpenConns(N): bound the pool — verbatim modernc.org/sqlite
//     maintainer advice ("do not issue a periodic query before the
//     previous one has returned", pkg.go.dev/modernc.org/sqlite,
//     Performance §).
//
// The schema-create helpers in other packages (CreateCapSchema, etc.)
// remain idempotent and are safe to call once per *sql.DB lifetime; they
// are NOT exported here — callers keep their own DDL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

// Default pool tuning. These are starting points, not gospel — tune with
// db.Stats() once the production workload is observable.
//
// Rationale (recorded 2026-09-27):
//
//   MaxOpenConns = 8: dark-db is single-host local. SQLite WAL allows
//     many readers + 1 writer; 8 connections covers ~6 reader-heavy
//     queries concurrent with 1 writer plus a headroom connection.
//
//   MaxIdleConns = 4: keeps the pool warm for steady-state read traffic.
//     Going higher than half of MaxOpenConns wastes file descriptors.
//
//   ConnMaxIdleTime = 5m: recycles connections across restarts of the
//     operator's CLI sessions without flapping on short pauses.
const (
	DefaultMaxOpenConns    = 8
	DefaultMaxIdleConns    = 4
	DefaultConnMaxIdleTime = 5 * time.Minute
)

// busyOpenRetries: when multiple goroutines open the same file
// concurrently, the journal_mode=WAL conversion briefly takes an
// EXCLUSIVE lock. busy_timeout covers most races but the very first
// open (before pragmas are applied) can still observe SQLITE_BUSY. We
// retry up to 3 times with a small linear backoff. Beyond that the
// caller should investigate.
const busyOpenRetries = 3

// OpenSQLite opens a SQLite database tuned for multi-agent concurrent
// access and returns a *sql.DB whose pool is bounded per the constants
// above. The DSN form is the modernc.org/sqlite native _pragma list —
// it is parsed in applyQueryParams (sqlite.go:207-237 in v1.53.0).
//
// Path may be:
//
//   - "file::memory:?cache=shared" for in-memory tests that need a
//     single shared DB across multiple *sql.DB handles.
//   - An absolute or relative filesystem path for persistent storage.
//     The WAL file and -shm shared-memory file will live next to it.
//
// The caller MUST close the returned *sql.DB when done. The returned
// handle is safe for concurrent use by multiple goroutines — that is
// the whole point of this package.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	dsn, err := buildDSN(path)
	if err != nil {
		return nil, fmt.Errorf("store: build dsn: %w", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(DefaultMaxOpenConns)
	db.SetMaxIdleConns(DefaultMaxIdleConns)
	db.SetConnMaxIdleTime(DefaultConnMaxIdleTime)
	// PingContext forces the connection to actually open, which in
	// turn forces modernc to apply the DSN pragmas. If journal_mode
	// conversion fails (e.g. read-only filesystem), we surface the
	// error here rather than at the first Exec. Retry briefly on
	// SQLITE_BUSY since the first writer of a fresh file needs the
	// EXCLUSIVE lock to convert to WAL.
	if err := pingWithRetry(ctx, db, path, busyOpenRetries); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %q: %w", path, err)
	}
	return db, nil
}

// pingWithRetry calls db.PingContext up to attempts times, retrying on
// SQLITE_BUSY with a 50ms linear backoff. The retry budget is bounded
// (default 3) because persistent BUSY usually signals a real problem
// (locked file from a crashed process, foreign_keys deadlock, etc.)
// that the caller should surface.
func pingWithRetry(ctx context.Context, db *sql.DB, path string, attempts int) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		err := db.PingContext(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isBusyError(err) {
			return err
		}
		// Linear backoff: 50ms, 100ms, 150ms.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 50 * time.Millisecond):
		}
	}
	return fmt.Errorf("after %d retries: %w", attempts, lastErr)
}

// isBusyError returns true when err carries the modernc.org/sqlite
// typed SQLITE_BUSY code. Uses errors.As against *sqlite.Error (the
// exported typed error — pkg.go.dev/modernc.org/sqlite, var
// ErrorCodeString).
func isBusyError(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == 5 // SQLITE_BUSY
}

// buildDSN composes the modernc.org/sqlite DSN with the BUG-5 pragma
// set. Extracted for testability — the order and contents are part of
// the public contract and have a deliberate-break test in store_test.go.
//
// We DO NOT use url.Values.Encode here because it URL-encodes the
// pragma parentheses (e.g. "busy_timeout(5000)" becomes
// "busy_timeout%285000%29"), which modernc.org/sqlite's parser does
// not undo. The pragma syntax uses only characters safe in a query
// string — letters, digits, parentheses, commas, underscores.
func buildDSN(path string) (string, error) {
	pragmas := []string{
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"wal_autocheckpoint(1000)",
		"cache_size(-2000)",
		"temp_store(MEMORY)",
	}
	var sb strings.Builder
	for i, p := range pragmas {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString("_pragma=")
		sb.WriteString(p)
	}
	pragmasStr := sb.String()

	// Path may already carry its own query (e.g. ":memory:" mode
	// tests pass "file::memory:?cache=shared"). Detect and merge.
	if base, existing, ok := splitQuery(path); ok {
		return base + "?" + existing + "&" + pragmasStr, nil
	}
	return path + "?" + pragmasStr, nil
}

// splitQuery returns (base, query-without-leading-?, present). Recognises
// "?" as the query separator. Path is expected to be of the form
// "file:foo?k=v" or "foo?k=v" (no scheme). Empty query is treated as
// absent.
func splitQuery(dsn string) (base, query string, ok bool) {
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == '?' {
			base = dsn[:i]
			q := dsn[i+1:]
			if q == "" {
				return base, "", false
			}
			return base, q, true
		}
	}
	return dsn, "", false
}

// stripQuery returns dsn with everything from the first "?" removed.
func stripQuery(dsn string) string {
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == '?' {
			return dsn[:i]
		}
	}
	return dsn
}

