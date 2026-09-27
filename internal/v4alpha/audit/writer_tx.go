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
// The monotonicity contract (audit_id > previous audit_id per Writer
// instance) is preserved exactly: the same mutex + counter dance as
// Write; only the final Exec dispatch changes.
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
// to Write — see Writer.Write for the full contract.
//
// On INSERT failure, the counter claim is rolled back (w.seq--) so
// the next successful WriteExec returns a monotonic id with no gaps
// from the caller's perspective.
//
// Returns the new audit_id (strictly greater than any prior audit_id
// returned by this Writer).
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
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	_, err := ex.ExecContext(ctx,
		"INSERT INTO audit_log (audit_id, actor, session_id, payload) VALUES (?, ?, ?, ?)",
		w.seq, actor, sessionIDArg, payload,
	)
	if err != nil {
		w.seq--
		return 0, fmt.Errorf("audit WriteExec insert: %w", err)
	}
	return w.seq, nil
}
