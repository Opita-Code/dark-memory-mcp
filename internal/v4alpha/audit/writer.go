// Package audit (v4alpha) — INV-1 enforcement primitive.
//
// INV-1 (operator id audit): every Save operation emits exactly one
// audit row tagged with the operator id, written via the audit.Writer.
// The audit_id is strictly monotonic per Writer instance and is the
// canonical ordering axis for write_audit (per project INV-1 contract).
//
// # Atomicity contract (per internal/atomic convention)
//   - ONE constructor: NewWriter
//   - ONE write method: Write
//   - ONE schema function: CreateSchema
//   - THREE invariants enforced in Write:
//       1. audit_id > previous audit_id (monotonic)
//       2. exactly one row inserted per call (atomicity)
//       3. operator (actor) is non-empty (INV-1 audit row is identifiable)
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Writer emits INV-1 audit rows with a strictly monotonic audit_id
// per Writer instance. The audit_id is process-local (single Writer);
// cross-process monotonicity is the caller's responsibility (DB-level
// sequence or external coordinator).
//
// The Writer holds a mutex around the counter + insert to make the
// (seq++, INSERT) pair atomic from the test's perspective. SQLite's
// AUTOINCREMENT is used at the DB level for crash-safety, but the
// Writer's monotonicity guarantee is per-instance.
type Writer struct {
	db  *sql.DB
	mu  sync.Mutex
	seq int64
}

// NewWriter returns a Writer bound to the given DB. The DB must have
// had CreateSchema called on it before any Write calls.
func NewWriter(db *sql.DB) *Writer {
	return &Writer{db: db}
}

// CreateSchema creates the audit_log table. Idempotent (IF NOT EXISTS).
// Called once at session-store initialization.
//
// Schema note: session_id is a nullable TEXT column. Empty string is
// stored as NULL to keep the COUNT/WHERE queries clean (sessions vs.
// non-session writes).
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_log (
			audit_id   INTEGER PRIMARY KEY AUTOINCREMENT,
			actor      TEXT    NOT NULL CHECK (actor <> ''),
			session_id TEXT,
			payload    BLOB,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("audit CreateSchema: %w", err)
	}
	return nil
}

// Write inserts one audit row. Returns the new audit_id (strictly
// greater than any prior audit_id returned by this Writer).
//
// INV-1 enforcement: every Write emits exactly one row tagged with
// the actor (operator id).
//
// sessionID is optional. Empty string means "not associated with a
// session" (e.g., standalone writes). For session-bound writes
// (Start/Heartbeat/Close), pass the session.ID so downstream Summary
// queries can JOIN cleanly without LIKE substring matching.
func (w *Writer) Write(ctx context.Context, actor, sessionID string, payload []byte) (int64, error) {
	if actor == "" {
		return 0, fmt.Errorf("audit Write: actor must be non-empty (INV-1)")
	}
	var sessionIDArg interface{}
	if sessionID == "" {
		sessionIDArg = nil
	} else {
		sessionIDArg = sessionID
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	_, err := w.db.ExecContext(ctx,
		"INSERT INTO audit_log (audit_id, actor, session_id, payload) VALUES (?, ?, ?, ?)",
		w.seq, actor, sessionIDArg, payload,
	)
	if err != nil {
		w.seq-- // rollback the seq claim on insert failure
		return 0, fmt.Errorf("audit Write insert: %w", err)
	}
	return w.seq, nil
}

// LastID returns the most recent audit_id emitted by this Writer,
// or 0 if no Write has been performed yet. Diagnostic helper used
// by tests and operators to inspect the current audit position.
func (w *Writer) LastID() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}
