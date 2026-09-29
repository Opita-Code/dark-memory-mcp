package audit_test

// BUG-12 cross-process monotonicity tests.
//
// These tests verify that audit.Writer produces strictly increasing
// audit_ids across multiple Writer instances sharing the SAME
// database file (simulating multi-process access).
//
// Pre-BUG-12, each Writer had its own in-memory counter (w.seq).
// Two Writers writing to the same audit_log file would both compute
// the same seq (e.g., seq=5) and the second INSERT would fail with
// PRIMARY KEY conflict — losing the audit row.
//
// Post-BUG-12, each Writer omits audit_id from the INSERT and lets
// SQLite AUTOINCREMENT (backed by the persistent sqlite_sequence
// table) assign it. Two Writers writing to the same DB file get
// strictly increasing ids assigned by SQLite itself.
//
// We use a file-backed DB (not :memory:) so the sqlite_sequence
// table persists across Writer instances (each gets its own *sql.DB
// handle pointing at the same file).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newFileDB opens a file-backed SQLite DB in a temp dir, applies
// the audit schema, and returns the *sql.DB + cleanup. We use the
// store package's OpenSQLite to get the BUG-5 pragma set (WAL +
// busy_timeout + MaxOpenConns) so the cross-process test exercises
// the same configuration as production.
func newFileDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "audit-bug12-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "audit.db")
	db, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	return db, path
}

// newWriterForPath opens a SECOND *sql.DB handle pointing at the
// same file. Each Writer has its own handle (different in-process
// state), simulating a different process owning the DB. The
// handle is closed via t.Cleanup.
func newWriterForPath(t *testing.T, path string) *audit.Writer {
	t.Helper()
	ctx := context.Background()

	db, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("store.OpenSQLite (second handle): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return audit.NewWriter(db)
}

// TestCrossProcess_Monotonic — 2 Writers (2 *sql.DB instances) on
// the same file-backed DB. Each Writer inserts 5 audit rows in
// sequence. Assert:
//   1. All 10 ids are present in audit_log
//   2. ids are strictly increasing (1, 2, 3, ..., 10)
//   3. No PRIMARY KEY conflict (no INSERT failure)
//
// Pre-BUG-12: second Writer's first INSERT would fail with
// "UNIQUE constraint failed: audit_log.audit_id".
//
// Post-BUG-12: both Writers see ids 1..5 (first) then 6..10
// (second), assigned by SQLite AUTOINCREMENT.
func TestCrossProcess_Monotonic(t *testing.T) {
	db1, path := newFileDB(t)
	w1 := audit.NewWriter(db1)

	w2 := newWriterForPath(t, path)

	ctx := context.Background()

	// Writer 1 inserts 5 rows.
	for i := 0; i < 5; i++ {
		id, err := w1.Write(ctx, "operator-a", "sess-a", []byte(`{"from":"w1"}`))
		if err != nil {
			t.Fatalf("w1.Write[%d]: %v", i, err)
		}
		_ = id
	}

	// Writer 2 inserts 5 rows.
	for i := 0; i < 5; i++ {
		id, err := w2.Write(ctx, "operator-b", "sess-b", []byte(`{"from":"w2"}`))
		if err != nil {
			t.Fatalf("w2.Write[%d]: %v (this would be the BUG-12 failure)", i, err)
		}
		_ = id
	}

	// Assert: all 10 ids present, strictly increasing.
	rows, err := db1.QueryContext(ctx, "SELECT audit_id FROM audit_log ORDER BY audit_id ASC")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(ids) != 10 {
		t.Fatalf("audit_log rows: expected 10, got %d", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("monotonicity violated: ids[%d]=%d <= ids[%d]=%d",
				i, ids[i], i-1, ids[i-1])
		}
	}

	// First id should be 1 (SQLite AUTOINCREMENT starts at 1).
	if ids[0] != 1 {
		t.Fatalf("first id: expected 1, got %d", ids[0])
	}
	// Last id should be 10.
	if ids[len(ids)-1] != 10 {
		t.Fatalf("last id: expected 10, got %d", ids[len(ids)-1])
	}
}

// TestCrossProcess_Interleaved — Writers interleave Writes.
// Assert: each Write gets a unique, strictly-increasing id.
// Also: each Writer's lastID mirror matches the most recent id
// that THIS Writer inserted.
func TestCrossProcess_Interleaved(t *testing.T) {
	db1, path := newFileDB(t)
	w1 := audit.NewWriter(db1)

	w2 := newWriterForPath(t, path)

	ctx := context.Background()

	type write struct {
		writer  *audit.Writer
		actor   string
		session string
		wantID  int64 // expected id after this write
	}

	// Interleave: A, B, A, B, A, B (6 writes total).
	plan := []write{
		{w1, "operator-a", "sess-a", 1},
		{w2, "operator-b", "sess-b", 2},
		{w1, "operator-a", "sess-a", 3},
		{w2, "operator-b", "sess-b", 4},
		{w1, "operator-a", "sess-a", 5},
		{w2, "operator-b", "sess-b", 6},
	}

	for i, p := range plan {
		id, err := p.writer.Write(ctx, p.actor, p.session, []byte(`{}`))
		if err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
		if id != p.wantID {
			t.Fatalf("Write[%d]: id=%d, want %d", i, id, p.wantID)
		}
	}

	// Verify each Writer's lastID mirror matches its last insertion.
	if got := w1.LastID(); got != 5 {
		t.Errorf("w1.LastID: expected 5, got %d", got)
	}
	if got := w2.LastID(); got != 6 {
		t.Errorf("w2.LastID: expected 6, got %d", got)
	}

	// Verify all 6 rows present, strictly increasing.
	rows, err := db1.QueryContext(ctx, "SELECT audit_id, actor FROM audit_log ORDER BY audit_id ASC")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()

	var count int
	var prevID int64
	for rows.Next() {
		var id int64
		var actor string
		if err := rows.Scan(&id, &actor); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if count > 0 && id <= prevID {
			t.Fatalf("monotonicity violated at row %d: prev=%d, curr=%d", count, prevID, id)
		}
		prevID = id
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 6 {
		t.Fatalf("audit_log rows: expected 6, got %d", count)
	}
}

// TestCrossProcess_Concurrent — multiple goroutines on multiple
// Writers writing concurrently. Each Writer has its own *sql.DB
// handle. Asserts all writes succeed (no PK conflicts) and ids
// are unique + strictly increasing.
//
// Pre-BUG-12: this test would have intermittent PRIMARY KEY
// failures (both goroutines incrementing their own w.seq to the
// same value). Post-BUG-12: SQLite AUTOINCREMENT coordinates
// across the two Writers.
func TestCrossProcess_Concurrent(t *testing.T) {
	db1, path := newFileDB(t)
	w1 := audit.NewWriter(db1)

	w2 := newWriterForPath(t, path)

	ctx := context.Background()
	const writesPerWriter = 25

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer 1 goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < writesPerWriter; i++ {
			if _, err := w1.Write(ctx, "operator-a", "sess-a", []byte(`{"from":"w1"}`)); err != nil {
				t.Errorf("w1.Write[%d]: %v", i, err)
				return
			}
		}
	}()

	// Writer 2 goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < writesPerWriter; i++ {
			if _, err := w2.Write(ctx, "operator-b", "sess-b", []byte(`{"from":"w2"}`)); err != nil {
				t.Errorf("w2.Write[%d]: %v", i, err)
				return
			}
		}
	}()

	wg.Wait()

	// Assert: all 50 rows present, no duplicates.
	var count int
	rows, err := db1.QueryContext(ctx, "SELECT audit_id FROM audit_log ORDER BY audit_id ASC")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()

	var prevID int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if count > 0 && id <= prevID {
			t.Fatalf("monotonicity violated: prev=%d, curr=%d", prevID, id)
		}
		prevID = id
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if count != writesPerWriter*2 {
		t.Fatalf("audit_log rows: expected %d, got %d", writesPerWriter*2, count)
	}
}