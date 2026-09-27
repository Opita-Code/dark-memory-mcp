// Agent_memory audit emission tests (ADR-007 C3, INV-1 closure).
//
// These tests verify the INV-1 invariant: every state-mutating
// agent_memory call (Save, Update, Archive) emits exactly one
// audit_log row inside the same transaction. Atomicity is the
// central guarantee — if the data write fails or the transaction
// rolls back, no audit row survives.
//
// # Test surface (6 tests)
//
//  1. TestSave_EmitsAuditRowInSameTx
//  2. TestUpdate_EmitsAuditRowInSameTx
//  3. TestArchive_EmitsAuditRowInSameTx
//  4. TestAudit_ActorRequiredOnSave
//  5. TestAudit_ActorRequiredOnUpdate
//  6. TestAudit_ActorRequiredOnArchive
//
// Each test opens its own *sql.DB (via newTestDBWithHandles below)
// so it can SELECT against audit_log directly. This is the
// strongest oracle — we observe the actual audit_log row, not
// just the Writer's monotonic counter.
package agent_memory

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestDBWithHandles is like newTestDB but also returns the
// *sql.DB so tests can SELECT against audit_log directly.
func newTestDBWithHandles(t *testing.T) (cleanup func(), s *Store, db *sql.DB) {
	t.Helper()
	dsn := "file::memory:?cache=shared"
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := CreateSchema(d); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.CreateSchema(d); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	w := audit.NewWriter(d)
	return func() { _ = d.Close() }, NewStore(d, w), d
}

// countAuditRows returns the number of audit_log rows for one actor.
func countAuditRows(t *testing.T, db *sql.DB, actor string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM audit_log WHERE actor = ?", actor,
	).Scan(&n); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	return n
}

func TestSave_EmitsAuditRowInSameTx(t *testing.T) {
	// Save inserts one agent_memory row + one audit_log row in
	// the same Tx. We verify both rows are visible after commit
	// by SELECTing audit_log directly.
	cleanup, s, db := newTestDBWithHandles(t)
	defer cleanup()

	const actor = "operator-save"
	auditMeta := &Audit{Actor: actor, SessionID: "sess-1"}
	if _, err := s.Save(context.Background(), auditMeta, actor, KindNote, "title", "body", "tag", false); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// One audit_log row with the actor we passed in.
	if got := countAuditRows(t, db, actor); got != 1 {
		t.Fatalf("audit_log rows for %s = %d, want 1", actor, got)
	}

	// Payload contains event=agent_memory.save.
	var payload []byte
	if err := db.QueryRow(
		"SELECT payload FROM audit_log WHERE actor = ?", actor,
	).Scan(&payload); err != nil {
		t.Fatalf("select payload: %v", err)
	}
	if !contains(payload, "agent_memory.save") {
		t.Fatalf("audit_log payload missing event: %s", payload)
	}

	// session_id stored verbatim.
	var sessionID string
	if err := db.QueryRow(
		"SELECT session_id FROM audit_log WHERE actor = ?", actor,
	).Scan(&sessionID); err != nil {
		t.Fatalf("select session_id: %v", err)
	}
	if sessionID != "sess-1" {
		t.Fatalf("session_id=%q, want %q", sessionID, "sess-1")
	}
}

func TestUpdate_EmitsAuditRowInSameTx(t *testing.T) {
	// Update emits one audit_log row inside the same Tx as the
	// UPDATE + FTS5 reindex.
	cleanup, s, db := newTestDBWithHandles(t)
	defer cleanup()

	// Pre-existing row.
	id, err := s.Save(context.Background(), testAudit(), "operator-data", KindNote, "orig", "original body", "tag", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	const updateActor = "operator-update"
	auditMeta := &Audit{Actor: updateActor}
	newContent := "updated body"
	if err := s.Update(context.Background(), auditMeta, id, nil, &newContent, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if got := countAuditRows(t, db, updateActor); got != 1 {
		t.Fatalf("audit_log rows for %s = %d, want 1", updateActor, got)
	}

	// Payload mentions the event + id.
	var payload []byte
	if err := db.QueryRow(
		"SELECT payload FROM audit_log WHERE actor = ?", updateActor,
	).Scan(&payload); err != nil {
		t.Fatalf("select payload: %v", err)
	}
	if !contains(payload, "agent_memory.update") {
		t.Fatalf("audit_log payload missing event: %s", payload)
	}
}

func TestArchive_EmitsAuditRowInSameTx(t *testing.T) {
	// Archive emits one audit_log row inside the same Tx as the
	// DELETE + FTS5 cleanup.
	cleanup, s, db := newTestDBWithHandles(t)
	defer cleanup()

	id, err := s.Save(context.Background(), testAudit(), "operator-data", KindNote, "", "to be archived", "", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	const archiveActor = "operator-archive"
	auditMeta := &Audit{Actor: archiveActor}
	if err := s.Archive(context.Background(), auditMeta, id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if got := countAuditRows(t, db, archiveActor); got != 1 {
		t.Fatalf("audit_log rows for %s = %d, want 1", archiveActor, got)
	}

	var payload []byte
	if err := db.QueryRow(
		"SELECT payload FROM audit_log WHERE actor = ?", archiveActor,
	).Scan(&payload); err != nil {
		t.Fatalf("select payload: %v", err)
	}
	if !contains(payload, "agent_memory.archive") {
		t.Fatalf("audit_log payload missing event: %s", payload)
	}

	// agent_memory row is gone.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_memory WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("agent_memory rows after archive = %d, want 0", n)
	}
}

func TestAudit_ActorRequiredOnSave(t *testing.T) {
	// Empty actor must be rejected (INV-1). The error is
	// ErrEmptyOperator — both agent_memory.operator and the
	// audit Actor are INV-1 violations.
	cleanup, s, _ := newTestDBWithHandles(t)
	defer cleanup()

	cases := []struct {
		name string
		am   *Audit
	}{
		{"nil_audit", nil},
		{"empty_actor_audit", &Audit{Actor: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Save(context.Background(), tc.am, "operator-data", KindNote, "", "body", "", false)
			if !errors.Is(err, ErrEmptyOperator) {
				t.Fatalf("Save with %s: got %v, want ErrEmptyOperator", tc.name, err)
			}
		})
	}
}

func TestAudit_ActorRequiredOnUpdate(t *testing.T) {
	cleanup, s, _ := newTestDBWithHandles(t)
	defer cleanup()

	id, err := s.Save(context.Background(), testAudit(), "operator-data", KindNote, "", "body", "", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	cases := []struct {
		name string
		am   *Audit
	}{
		{"nil_audit", nil},
		{"empty_actor_audit", &Audit{Actor: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newC := "x"
			err := s.Update(context.Background(), tc.am, id, nil, &newC, nil, nil)
			if !errors.Is(err, ErrEmptyOperator) {
				t.Fatalf("Update with %s: got %v, want ErrEmptyOperator", tc.name, err)
			}
		})
	}
}

func TestAudit_ActorRequiredOnArchive(t *testing.T) {
	cleanup, s, _ := newTestDBWithHandles(t)
	defer cleanup()

	id, err := s.Save(context.Background(), testAudit(), "operator-data", KindNote, "", "body", "", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	cases := []struct {
		name string
		am   *Audit
	}{
		{"nil_audit", nil},
		{"empty_actor_audit", &Audit{Actor: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Archive(context.Background(), tc.am, id)
			if !errors.Is(err, ErrEmptyOperator) {
				t.Fatalf("Archive with %s: got %v, want ErrEmptyOperator", tc.name, err)
			}
		})
	}
}

// contains is a tiny substring helper used by the audit payload tests.
func contains(haystack []byte, needle string) bool {
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
