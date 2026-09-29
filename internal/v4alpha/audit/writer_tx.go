// Tx-aware audit emission (ADR-007 C3).
//
// WriteExec is the same as Write but accepts any executor that
// implements sqlExec (interface satisfied by both *sql.DB and *sql.Tx).
// It lets the caller emit an audit_log row INSIDE an existing
// transaction so the audit row and the underlying data row are
// atomic — both succeed or both rollback.
//
// # Why a separate method
//
// The std lib gives *sql.Tx.ExecContext a different method set from
// *sql.DB.ExecContext. A small interface that exposes only what the
// audit insert needs (ExecContext) is the minimum surface that lets
// callers pass either one interchangeably:
//
//	var ex audit.sqlExec = db  // standalone write (audit_log only)
//	var ex audit.sqlExec = tx  // inside an agent_memory.WithTx
//	w.WriteExec(ctx, ex, actor, sessionID, payload)
//
// # BUG-12 alignment (2026-09-29)
//
// The audit_id is assigned by SQLite AUTOINCREMENT, not driven by an
// in-memory counter. WriteExec follows the same pattern as Write:
// omit audit_id from the INSERT, read it back via Result.LastInsertId(),
// update the lastID mirror under the mutex.
//
// # Why not change Write
//
// Write is the public API every existing call site uses. Changing
// its signature would force every caller (session.Start/Heartbeat/
// Close, error_resolve) to thread the executor through. Adding a
// sibling method keeps backwards compat while unlocking tx-aware
// emission for new call sites (agent_memory.Save/Update/Archive,
// judge.Store.SaveEvaluation).
package audit

import (
	"context"
	"database/sql"
	"fmt"
)

// sqlExec is the minimum surface WriteExec needs from an executor.
// Both *sql.DB and *sql.Tx satisfy it. Callers can pass either one
// directly — they don't need to declare this interface locally.
type sqlExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// WriteExec inserts one audit_log row via the given executor. The
// executor may be a *sql.DB (standalone audit, equivalent to Write)
// or a *sql.Tx (audit emission inside an existing transaction).
//
// INV-1 enforcement: actor must be non-empty; empty sessionID is
// stored as NULL. The audit_id monotonicity guarantee is identical
// to Write — backed by SQLite AUTOINCREMENT (BUG-12).
//
// On INSERT failure, no state change (sqlite_sequence is not
// advanced; the next Write gets a fresh id from the DB). The
// caller sees the error.
//
// Returns the new audit_id (strictly greater than any prior
// audit_id in the audit_log table — per-process AND cross-process).
func (w *Writer) WriteExec(ctx context.Context, ex sqlExec, actor, sessionID string, payload []byte) (int64, error) {
	if ex == nil {
		return 0, fmt.Errorf("audit WriteExec: executor is nil")
	}
	if actor == "" {
		return 0, fmt.Errorf("audit WriteExec: actor must be non-empty (INV-1)")
	}
	var sessionIDArg interface{}
	if sessionID == "" {
		sessionIDArg = nil
	} else {
		sessionIDArg = sessionID
	}
	res, err := ex.ExecContext(ctx,
		"INSERT INTO audit_log (actor, session_id, payload) VALUES (?, ?, ?)",
		actor, sessionIDArg, payload,
	)
	if err != nil {
		return 0, fmt.Errorf("audit WriteExec insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("audit WriteExec LastInsertId: %w", err)
	}
	w.mu.Lock()
	w.lastID = id
	w.mu.Unlock()
	return id, nil
}
