// Phase 4 Chunk 4.3 — audit.Writer.WriteExecWithProject tests.
//
// Three tests cover the new method that threads project_id into the
// audit_log row inside a transaction:
//
//  1. TestWriteExecWithProject_StampsColumn  — happy path: project_id
//     is persisted; SELECT against audit_log returns the value.
//  2. TestWriteExecWithProject_EmptyActorFailsFast — INV-1: empty
//     actor rejected (same contract as WriteExec).
//  3. TestWriteExecWithProject_EmptyProjectIDFailsFast — empty
//     project_id rejected (Phase 4 Chunk 4.3 invariant).
//
// The test DB has the project_id column added by
// project.ApplyProjectIDColumns (matches production boot).
package audit_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestWriterWithProjectID opens an in-memory SQLite, applies
// audit_log + project_id column migration, returns the writer +
// db handle.
func newTestWriterWithProjectID(t *testing.T) (*audit.Writer, *sql.DB) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project.ApplyProjectIDColumns: %v", err)
	}
	return audit.NewWriter(db), db
}

func TestWriteExecWithProject_StampsColumn(t *testing.T) {
	w, db := newTestWriterWithProjectID(t)
	ctx := context.Background()

	// Use a *sql.Tx to exercise the executor path (production
	// callers always pass *sql.Tx, never *sql.DB).
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()

	id, err := w.WriteExecWithProject(ctx, tx, "operator-nico", "sess-test", "proj-huila",
		[]byte(`{"event":"isolation.test"}`))
	if err != nil {
		t.Fatalf("WriteExecWithProject: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive audit_id, got %d", id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Read back the audit_log row; expect project_id = 'proj-huila'.
	var projectID string
	if err := db.QueryRowContext(ctx,
		"SELECT project_id FROM audit_log WHERE audit_id = ?", id,
	).Scan(&projectID); err != nil {
		t.Fatalf("SELECT project_id: %v", err)
	}
	if projectID != "proj-huila" {
		t.Errorf("project_id = %q; want proj-huila", projectID)
	}
}

func TestWriteExecWithProject_EmptyActorFailsFast(t *testing.T) {
	w, db := newTestWriterWithProjectID(t)
	ctx := context.Background()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()

	_, err = w.WriteExecWithProject(ctx, tx, "", "sess-test", "proj-huila",
		[]byte(`{"event":"x"}`))
	if err == nil {
		t.Fatal("WriteExecWithProject accepted empty actor; want INV-1 rejection")
	}
	if !strings.Contains(err.Error(), "INV-1") {
		t.Errorf("error = %v; want substring %q", err, "INV-1")
	}
}

func TestWriteExecWithProject_EmptyProjectIDFailsFast(t *testing.T) {
	w, db := newTestWriterWithProjectID(t)
	ctx := context.Background()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()

	_, err = w.WriteExecWithProject(ctx, tx, "operator-nico", "sess-test", "",
		[]byte(`{"event":"x"}`))
	if err == nil {
		t.Fatal("WriteExecWithProject accepted empty project_id; want INV-1 rejection")
	}
	if !strings.Contains(err.Error(), "projectID must be non-empty") {
		t.Errorf("error = %v; want substring %q", err, "projectID must be non-empty")
	}
}
