package session_test

// Shared helpers for session tests (L1 + L2).

import (
	"database/sql"
	"time"

	"pgregory.net/rapid"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
)

// storeCleanupper is the interface satisfied by both *testing.T and
// *rapid.T — both have Cleanup(func()) and Fatalf(format, ...).
type storeCleanupper interface {
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// newTestStoreAny opens in-memory SQLite, creates audit + session
// schemas, wires Store + Writer, and registers cleanup.
func newTestStoreAny(t storeCleanupper) *session.Store {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := session.CreateSchema(db); err != nil {
		t.Fatalf("session.CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)
	return session.NewStore(db, w)
}

// newTestStoreAudit returns (Store, *auditWriterExposed) so property
// tests can assert on audit_id directly.
func newTestStoreAudit(t *rapid.T) (*session.Store, *auditWriterExposed) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := session.CreateSchema(db); err != nil {
		t.Fatalf("session.CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)
	return session.NewStore(db, w), &auditWriterExposed{w: w}
}

// newTestStoreT is the *rapid.T facade for newTestStoreAny.
func newTestStoreT(t *rapid.T) *session.Store {
	return newTestStoreAny(t)
}

// auditWriterExposed wraps audit.Writer to expose LastID. The Writer
// itself exposes LastID as a diagnostic method; this type exists for
// readability in property test signatures.
type auditWriterExposed struct {
	w *audit.Writer
}

func (a *auditWriterExposed) LastID() int64 {
	return a.w.LastID()
}

// timeSleep is the actual sleep helper used by L2 tests that need
// measurable timestamp advance. Centralized so we can change the
// mechanism (e.g., to virtual time) without touching call sites.
func timeSleep(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
