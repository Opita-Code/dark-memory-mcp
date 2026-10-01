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
// # Phase 2 chain (2026-09-29, alpha.15)
//
// WriteExec follows the same hash-chain flow as Write. Because the
// executor may be a *sql.Tx, prev_hash is resolved INSIDE the
// transaction (the row we read is the consistent snapshot at tx
// BEGIN). The hash covers (prev_hash, audit_id, actor, session_id,
// payload, created_at) per canonical.go — the same canonical
// encoding Write uses, so any drift breaks the chain (which is the
// point).
//
// # Phase 4 Chunk 4.3 — WriteExecWithProject
//
// WriteExecWithProject is the project-tagged sibling of WriteExec.
// Same canonical hash (project_id is metadata, NOT part of the
// chain). Used by agent_memory.Save/Update/Archive and
// judge.Store.SaveEvaluation when their Audit metadata carries a
// project_id. The un-tagged WriteExec remains the default — its
// audit rows get project_id='default' via the column DEFAULT.
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
	"time"
)

// sqlExec is the minimum surface WriteExec needs from an executor.
// Both *sql.DB and *sql.Tx satisfy it. Callers can pass either one
// directly — they don't need to declare this interface locally.
//
// # Phase 2 chain queries
//
// The chain flow needs 2 queries per WriteExec on the hot path:
//   1. INSERT INTO audit_log (actor, session_id, payload, prev_hash,
//      created_at) — created_at generated in Go (RFC3339Nano), no SELECT.
//   2. UPDATE audit_log SET row_hash = ? WHERE audit_id = ?
//
// Cold start only (lastHash mirror empty): one extra SELECT for the
// bootstrap — SELECT row_hash FROM audit_log ORDER BY audit_id DESC
// LIMIT 1 via the executor (consistent snapshot at tx BEGIN when the
// executor is a *sql.Tx).
//
// sqlExec exposes ExecContext + QueryRowContext: ExecContext for the
// INSERT/UPDATE, QueryRowContext for the cold-start bootstrap SELECT.
// Both *sql.DB and *sql.Tx satisfy it.
type sqlExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// WriteExec inserts one audit_log row via the given executor. The
// executor may be a *sql.DB (standalone audit, equivalent to Write)
// or a *sql.Tx (audit emission inside an existing transaction).
//
// INV-1 enforcement: actor must be non-empty; empty sessionID is
// stored as NULL. The audit_id monotonicity guarantee is identical
// to Write — backed by SQLite AUTOINCREMENT (BUG-12).
//
// Phase 2 chain flow (mirror of Write, optimized):
//  1. Generate created_at in Go (UTC RFC3339Nano).
//  2. Resolve prev_hash (mirror or SELECT from executor).
//  3. INSERT with prev_hash + created_at (row_hash populated step 5).
//  4. Compute row_hash (pure function — canonical.go).
//  5. UPDATE row_hash.
//  6. Update mirrors (lastID, lastHash).
//
// Performance: 2 queries per WriteExec (INSERT + UPDATE), same as
// Write. No per-Write SELECT for created_at — Go generates it.
//
// On INSERT failure, no state change (sqlite_sequence is not
// advanced; the next Write gets a fresh id from the DB). The
// caller sees the error. The mutex protects lastID + lastHash
// mirrors; the executor itself is not locked (the caller owns
// the tx).
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

	// 1. Generate created_at in Go. Same value goes into both the
	// INSERT and the row_hash input.
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	w.mu.Lock()
	defer w.mu.Unlock()

	// 2. Resolve prev_hash. When executor is a *sql.Tx, this reads
	// the consistent snapshot at tx BEGIN (not the live DB).
	prevHash, err := w.resolvePrevHashExecLocked(ctx, ex)
	if err != nil {
		return 0, fmt.Errorf("audit WriteExec resolvePrevHash: %w", err)
	}

	// 3. INSERT with prev_hash + created_at (row_hash populated
	// in step 5) + structured payload columns (Phase 6 ADR-019).
	pf := ExtractPayloadFields(payload)
	res, err := ex.ExecContext(ctx,
		"INSERT INTO audit_log (actor, session_id, payload, prev_hash, created_at, payload_event, payload_id, payload_kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		actor, sessionIDArg, payload, prevHash, createdAt, nullableString(pf.Event), nullableInt64(pf.ID), nullableString(pf.Kind),
	)
	if err != nil {
		return 0, fmt.Errorf("audit WriteExec insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("audit WriteExec LastInsertId: %w", err)
	}

	// 4. Compute row_hash (pure function — canonical.go). Uses the
	// SAME createdAt we just stored.
	rowHash := ComputeRowHash(prevHash, id, actor, sessionID, payload, createdAt)

	// 5. UPDATE row_hash.
	if _, err := ex.ExecContext(ctx,
		"UPDATE audit_log SET row_hash = ? WHERE audit_id = ?",
		rowHash[:], id,
	); err != nil {
		return id, fmt.Errorf("audit WriteExec update row_hash: %w", err)
	}

	// 6. Update mirrors.
	w.lastID = id
	w.lastHash = rowHash[:]
	return id, nil
}

// WriteExecWithProject is the project-tagged sibling of WriteExec
// (Phase 4 Chunk 4.3 — hard isolation enforcement). Same canonical
// hash as WriteExec (project_id is metadata, NOT part of the chain
// — Phase 2 §3.2 invariant preserved); same prev_hash resolution
// via the executor; same row_hash computation. The ONLY difference
// is the INSERT includes the project_id column.
//
// Why a sibling method (not a WriteExec 6th arg):
//   - 4 existing callers (agent_memory.Save/Update/Archive,
//     judge.Store.SaveEvaluation) would have to thread projectID
//     through their public APIs (a cascade of signature changes).
//   - Old callers' audit rows get project_id='default' via the
//     column DEFAULT clause. New callers use WriteExecWithProject
//     when they have a project_id in their Audit metadata.
//   - The hash chain is backward-compatible: any pre-Phase-4 audit
//     row continues to verify (project_id was never part of the
//     hash).
//
// projectID is required (non-empty) — INV-1 requires every audit
// row to identify its scope. Use "default" for system writes.
func (w *Writer) WriteExecWithProject(ctx context.Context, ex sqlExec, actor, sessionID, projectID string, payload []byte) (int64, error) {
	if ex == nil {
		return 0, fmt.Errorf("audit WriteExecWithProject: executor is nil")
	}
	if actor == "" {
		return 0, fmt.Errorf("audit WriteExecWithProject: actor must be non-empty (INV-1)")
	}
	if projectID == "" {
		return 0, fmt.Errorf("audit WriteExecWithProject: projectID must be non-empty (INV-1)")
	}
	var sessionIDArg interface{}
	if sessionID == "" {
		sessionIDArg = nil
	} else {
		sessionIDArg = sessionID
	}

	// 1. Generate created_at in Go. Same value goes into both the
	// INSERT and the row_hash input.
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	w.mu.Lock()
	defer w.mu.Unlock()

	// 2. Resolve prev_hash via the executor (consistent snapshot
	// at tx BEGIN when ex is *sql.Tx).
	prevHash, err := w.resolvePrevHashExecLocked(ctx, ex)
	if err != nil {
		return 0, fmt.Errorf("audit WriteExecWithProject resolvePrevHash: %w", err)
	}

	// 3. INSERT with prev_hash, project_id, created_at (row_hash
	// populated in step 5) + structured payload columns (Phase 6
	// ADR-019). project_id is the namespace primitive (Phase 4);
	// queryable metadata but NOT part of the canonical hash
	// (Phase 2 §3.2 invariant).
	pf := ExtractPayloadFields(payload)
	res, err := ex.ExecContext(ctx,
		"INSERT INTO audit_log (actor, session_id, project_id, payload, prev_hash, created_at, payload_event, payload_id, payload_kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		actor, sessionIDArg, projectID, payload, prevHash, createdAt, nullableString(pf.Event), nullableInt64(pf.ID), nullableString(pf.Kind),
	)
	if err != nil {
		return 0, fmt.Errorf("audit WriteExecWithProject insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("audit WriteExecWithProject LastInsertId: %w", err)
	}

	// 4. Compute row_hash (pure function — canonical.go). Same
	// canonical hash as WriteExec (project_id is metadata).
	rowHash := ComputeRowHash(prevHash, id, actor, sessionID, payload, createdAt)

	// 5. UPDATE row_hash on the new row.
	if _, err := ex.ExecContext(ctx,
		"UPDATE audit_log SET row_hash = ? WHERE audit_id = ?",
		rowHash[:], id,
	); err != nil {
		return id, fmt.Errorf("audit WriteExecWithProject update row_hash: %w", err)
	}

	// 6. Update mirrors.
	w.lastID = id
	w.lastHash = rowHash[:]
	return id, nil
}

// resolvePrevHashExecLocked is the executor-aware variant of
// resolvePrevHashLocked. Reads the most recent row_hash via the
// given executor (which may be a *sql.Tx). MUST be called with
// w.mu held.
func (w *Writer) resolvePrevHashExecLocked(ctx context.Context, ex sqlExec) ([]byte, error) {
	if w.lastHash != nil {
		return w.lastHash, nil
	}
	var prev []byte
	err := ex.QueryRowContext(ctx,
		"SELECT row_hash FROM audit_log ORDER BY audit_id DESC LIMIT 1",
	).Scan(&prev)
	if err == sql.ErrNoRows {
		return ZeroHash(), nil
	}
	if err != nil {
		return nil, err
	}
	if prev == nil {
		return ZeroHash(), nil
	}
	return prev, nil
}

// NOTE (judge fix F3, 2026-09-30): no fetchCreatedAt helper exists —
// created_at is generated in Go (WriteExec step 1), so no DB read is
// ever needed. resolvePrevHashExecLocked above is the only helper.
