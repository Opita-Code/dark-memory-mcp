// Package audit (v4alpha) — INV-1 enforcement primitive.
//
// INV-1 (operator id audit): every Save operation emits exactly one
// audit row tagged with the operator id, written via the audit.Writer.
// The audit_id is strictly monotonic — both per-process AND cross-
// process — backed by SQLite's AUTOINCREMENT (sqlite_sequence table,
// persistent across processes and crashes).
//
// # Atomicity contract (per internal/atomic convention)
//   - ONE constructor: NewWriter
//   - ONE write method: Write
//   - ONE schema function: CreateSchema
//   - THREE invariants enforced in Write:
//       1. audit_id > previous audit_id (monotonic, cross-process)
//       2. exactly one row inserted per call (atomicity)
//       3. operator (actor) is non-empty (INV-1 audit row is identifiable)
//
// # BUG-12 fix (2026-09-29, alpha.14)
//
// Pre-BUG-12, the Writer held an in-memory counter (w.seq) that it
// passed explicitly as audit_id in the INSERT. This was process-
// local monotonic but cross-process UNSAFE: two Writers writing to
// the same DB file could both compute seq=5 and the second INSERT
// would fail with PRIMARY KEY conflict.
//
// Post-BUG-12, the in-memory counter is gone. The INSERT omits
// audit_id and SQLite's AUTOINCREMENT assigns it (via the
// sqlite_sequence table). Read back via Result.LastInsertId().
// Cross-process monotonicity is guaranteed by SQLite itself.
//
// The mutex is kept for lastID mirror updates (race-free reads of
// the in-memory mirror for fast LastID() reads).
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Writer emits INV-1 audit rows with a strictly monotonic audit_id
// (per-process AND cross-process, backed by SQLite AUTOINCREMENT).
//
// lastID is an in-memory mirror of the most recent id returned by
// LastInsertId. It is a strict under-approximation of MAX(audit_id)
// in the DB (other processes may have written more). It exists for
// fast LastID() reads (no DB roundtrip).
type Writer struct {
	db     *sql.DB
	mu     sync.Mutex
	lastID int64
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
// non-session writes). AUTOINCREMENT (not just INTEGER PRIMARY KEY)
// is critical for cross-process monotonicity — without AUTOINCREMENT,
// SQLite may recycle ids after the max value, breaking audit log
// ordering. See https://www.sqlite.org/autoinc.html for the precise
// semantics.
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
// greater than any prior audit_id in the audit_log table — both
// per-process and cross-process, backed by SQLite AUTOINCREMENT).
//
// INV-1 enforcement: every Write emits exactly one row tagged with
// the actor (operator id).
//
// sessionID is optional. Empty string means "not associated with a
// session" (e.g., standalone writes). For session-bound writes
// (Start/Heartbeat/Close), pass the session.ID so downstream Summary
// queries can JOIN cleanly without LIKE substring matching.
//
// The audit_id is assigned by SQLite AUTOINCREMENT. This Writer does
// NOT drive the id sequence — see BUG-12 in the package doc.
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
	res, err := w.db.ExecContext(ctx,
		"INSERT INTO audit_log (actor, session_id, payload) VALUES (?, ?, ?)",
		actor, sessionIDArg, payload,
	)
	if err != nil {
		return 0, fmt.Errorf("audit Write insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		// Rare: INSERT succeeded but LastInsertId is not supported
		// by the driver (shouldn't happen with modernc.org/sqlite).
		// The row IS in the DB; we just can't tell the caller what
		// id it got. Surface the error so the caller knows.
		return 0, fmt.Errorf("audit Write LastInsertId: %w", err)
	}
	w.mu.Lock()
	w.lastID = id
	w.mu.Unlock()
	return id, nil
}

// LastID returns the most recent audit_id emitted by THIS Writer
// process, or 0 if no Write has been performed yet. Diagnostic
// helper used by tests and operators to inspect this process's
// audit position.
//
// IMPORTANT: this is a per-process mirror. If other processes have
// written to the same audit_log DB, their ids are NOT reflected
// here. For the global truth, query MAX(audit_id) directly.
//
// session.Summary.AuditIDAtClose uses this value as a diagnostic
// snapshot; it is not the canonical cross-process truth.
func (w *Writer) LastID() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastID
}
