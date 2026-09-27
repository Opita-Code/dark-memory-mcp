// Tx-aware audit emission tests (ADR-007 C3).
//
// The five tests below cover the five combinations the operator-
// facing code paths depend on:
//
//  1. WriteExecOnDB           — equivalent to Write; standalone audit
//  2. WriteExecOnTx           — atomic with another INSERT in same tx
//  3. WriteExecRollback       — rolled-back tx leaves audit_log empty
//  4. WriteExecEmptyActor     — INV-1 enforcement rejects empty actor
//  5. WriteExecNilExecutor    — defensive: nil executor rejected
//  6. WriteExecEmptySessionID — empty sessionID is stored as NULL
//
// We open our own DB (via the shared sqlOpenMemory helper in
// helpers_test.go) so each test can SELECT against the same *sql.DB
// the writer writes through — atomicity assertions require access
// to the writer's DB handle.
package audit_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestWriterWithDB opens an in-memory SQLite, calls CreateSchema,
// returns the writer + db handle. The db is closed via t.Cleanup.
func newTestWriterWithDB(t *testing.T) (*audit.Writer, *sql.DB) {
	t.Helper()
	db, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	return audit.NewWriter(db), db
}

func TestWriteExecOnDB(t *testing.T) {
	// Same contract as Write: standalone audit_log insert.
	// Equivalent to writer_test.go's TestWrite but exercises the
	// sqlExec interface path rather than the *sql.DB path.
	w, db := newTestWriterWithDB(t)

	id, err := w.WriteExec(context.Background(), db, "operator-a", "sess-1", []byte(`{"event":"x"}`))
	if err != nil {
		t.Fatalf("WriteExec: %v", err)
	}
	if id <= 0 {
		t.Fatalf("WriteExec returned id=%d, want > 0", id)
	}

	// Verify the row landed in audit_log.
	var actor, sessionID string
	var payload []byte
	if err := db.QueryRow(
		"SELECT actor, COALESCE(session_id,''), payload FROM audit_log WHERE audit_id = ?",
		id,
	).Scan(&actor, &sessionID, &payload); err != nil {
		t.Fatalf("audit_log select: %v", err)
	}
	if actor != "operator-a" {
		t.Fatalf("actor=%q, want %q", actor, "operator-a")
	}
	if sessionID != "sess-1" {
		t.Fatalf("session_id=%q, want %q", sessionID, "sess-1")
	}
}

func TestWriteExecOnTx(t *testing.T) {
	// Atomic emission: sidecar INSERT + audit_log row inside the
	// SAME transaction. Both rows must be visible after commit.
	w, db := newTestWriterWithDB(t)

	// Schema for a sidecar table to simulate the agent_memory
	// pattern. We don't import agent_memory here (avoid coupling
	// audit tests to the agent_memory package) — a bare sidecar
	// table is sufficient for the atomicity test.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sidecar (
			id    INTEGER PRIMARY KEY AUTOINCREMENT,
			actor TEXT NOT NULL,
			body  TEXT NOT NULL
		)
	`); err != nil {
		t.Fatalf("sidecar schema: %v", err)
	}

	ctx := context.Background()
	var agentID, auditID int64

	err := store.WithTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO sidecar (actor, body) VALUES (?, ?)`, "operator-a", "hello")
		if err != nil {
			return err
		}
		agentID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		auditID, err = w.WriteExec(ctx, tx, "operator-a", "sess-2",
			[]byte(`{"event":"sidecar.insert","id":`+
				itoaInt64(agentID)+`}`))
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	// Both rows visible.
	var agentRow int
	if err := db.QueryRow("SELECT COUNT(*) FROM sidecar WHERE id = ?", agentID).Scan(&agentRow); err != nil {
		t.Fatalf("sidecar count: %v", err)
	}
	if agentRow != 1 {
		t.Fatalf("sidecar rows=%d, want 1", agentRow)
	}
	var auditRow int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_log WHERE audit_id = ?", auditID).Scan(&auditRow); err != nil {
		t.Fatalf("audit_log count: %v", err)
	}
	if auditRow != 1 {
		t.Fatalf("audit_log rows=%d, want 1", auditRow)
	}
}

func TestWriteExecRollback(t *testing.T) {
	// If the transaction is rolled back, the audit_log row written
	// via WriteExec MUST NOT survive. This is the central invariant
	// of C3: agent_memory.Save + audit_log are atomic; if the
	// enclosing agent_memory insert fails, audit_log doesn't get a
	// phantom row.
	w, db := newTestWriterWithDB(t)

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sidecar (
			id    INTEGER PRIMARY KEY AUTOINCREMENT,
			actor TEXT NOT NULL
		)
	`); err != nil {
		t.Fatalf("sidecar schema: %v", err)
	}

	ctx := context.Background()
	// Use WithTx and return a non-nil error to trigger rollback.
	err := store.WithTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sidecar (actor) VALUES (?)`, "operator-a"); err != nil {
			return err
		}
		if _, err := w.WriteExec(ctx, tx, "operator-a", "", []byte(`{"event":"will.rollback"}`)); err != nil {
			return err
		}
		return errors.New("intentional rollback")
	})
	if err == nil {
		t.Fatalf("WithTx returned nil; want error from rollback")
	}

	// Both sidecar AND audit_log should be empty (the tx rolled back).
	var sidecarCount, auditCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM sidecar").Scan(&sidecarCount); err != nil {
		t.Fatalf("sidecar count: %v", err)
	}
	if sidecarCount != 0 {
		t.Fatalf("sidecar rows after rollback=%d, want 0", sidecarCount)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&auditCount); err != nil {
		t.Fatalf("audit_log count: %v", err)
	}
	if auditCount != 0 {
		t.Fatalf("audit_log rows after rollback=%d, want 0", auditCount)
	}

	// Counter behaviour after rollback: WriteExec succeeded at the
	// ExecContext level (the INSERT was queued in the tx), so the
	// counter advanced from 0 to 1 even though the row was rolled
	// back. The next Write returns id=2 (monotonic, with a gap at 1).
	// Gaps are acceptable — the audit_id column is writer-driven,
	// not DB AUTOINCREMENT-driven.
	id, err := w.Write(ctx, "operator-a", "", []byte(`{"event":"after.rollback"}`))
	if err != nil {
		t.Fatalf("Write after rollback: %v", err)
	}
	if id != 2 {
		t.Fatalf("Write returned id=%d after rollback, want 2 (counter advances optimistically; tx rollback creates a gap)", id)
	}
	// And the gap at id=1 is visible (audit_id=1 was rolled back).
	var visibleCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&visibleCount); err != nil {
		t.Fatalf("audit_log count: %v", err)
	}
	if visibleCount != 1 {
		t.Fatalf("audit_log rows=%d, want 1 (id=2 only; id=1 was rolled back)", visibleCount)
	}
}

func TestWriteExecInsertFailureCounterRollsBack(t *testing.T) {
	// Counter rollback contract: when the ExecContext itself
	// returns an error (e.g. UNIQUE violation, NOT NULL violation,
	// closed tx), the counter MUST roll back so the next successful
	// Write returns a contiguous id.
	//
	// We trigger the failure by calling ExecContext on a tx that
	// has already been committed (ExecContext on a closed tx
	// returns sql.ErrTxDone).
	w, db := newTestWriterWithDB(t)
	ctx := context.Background()

	var tx *sql.Tx
	err := store.WithTx(ctx, db, func(t *sql.Tx) error {
		tx = t
		return nil // commit empty tx so the tx is closed
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	// tx is now committed (and closed). WriteExec on the closed
	// tx returns an error.
	_, err = w.WriteExec(ctx, tx, "operator-a", "", []byte(`{"event":"will.fail"}`))
	if err == nil {
		t.Fatalf("WriteExec on closed tx returned nil error")
	}

	// Counter did NOT advance (rejection happens before seq++).
	if got := w.LastID(); got != 0 {
		t.Fatalf("LastID=%d after failed WriteExec, want 0 (counter must roll back)", got)
	}

	// Next Write returns id=1 (contiguous, no gap).
	id, err := w.Write(ctx, "operator-a", "", []byte(`{"event":"after.fail"}`))
	if err != nil {
		t.Fatalf("Write after failed WriteExec: %v", err)
	}
	if id != 1 {
		t.Fatalf("Write returned id=%d after failed WriteExec, want 1", id)
	}
}

func TestWriteExecEmptyActor(t *testing.T) {
	// INV-1 enforcement: empty actor is rejected before any DB I/O.
	w, db := newTestWriterWithDB(t)

	_, err := w.WriteExec(context.Background(), db, "", "sess-3", []byte(`{}`))
	if err == nil {
		t.Fatalf("WriteExec with empty actor returned nil error")
	}
	// The error message should mention INV-1 (operator convention).
	if !containsStr(err.Error(), "INV-1") {
		t.Fatalf("WriteExec error %q does not mention INV-1", err.Error())
	}

	// No row was inserted.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("audit_log rows=%d after rejected WriteExec, want 0", count)
	}

	// Counter was NOT advanced (rejection happens before seq++).
	if got := w.LastID(); got != 0 {
		t.Fatalf("LastID=%d after rejected WriteExec, want 0", got)
	}
}

func TestWriteExecNilExecutor(t *testing.T) {
	// Defensive: nil executor must be rejected before any DB I/O.
	w, _ := newTestWriterWithDB(t)

	_, err := w.WriteExec(context.Background(), nil, "operator-a", "", []byte(`{}`))
	if err == nil {
		t.Fatalf("WriteExec with nil executor returned nil error")
	}
}

func TestWriteExecEmptySessionIDStoredAsNULL(t *testing.T) {
	// Empty sessionID must be stored as SQL NULL (matches the Write
	// contract; documented in writer.go:82-87).
	w, db := newTestWriterWithDB(t)

	id, err := w.WriteExec(context.Background(), db, "operator-a", "", []byte(`{}`))
	if err != nil {
		t.Fatalf("WriteExec: %v", err)
	}
	var sessionID sql.NullString
	if err := db.QueryRow("SELECT session_id FROM audit_log WHERE audit_id = ?", id).Scan(&sessionID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if sessionID.Valid {
		t.Fatalf("session_id=%q, want NULL", sessionID.String)
	}
}

// --- helpers ---

// itoaInt64 converts an int64 to its base-10 string representation.
// Avoids pulling in strconv just for the few test cases that need it.
func itoaInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// containsStr is a small substring check used by the negative tests.
func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
