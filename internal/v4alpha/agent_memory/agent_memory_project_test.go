// Phase 4 Chunk 4.3 — agent_memory.Store project_id threading tests.
//
// Two tests cover the audit emission path that threads project_id
// via audit.Writer.WriteExecWithProject:
//
//  1. TestSaveAuditProjectIDSurfaced — Save with auditMeta.ProjectID
//     stamps the audit_log row's project_id column.
//  2. TestSaveAuditEmptyProjectIDFallsBack — Save with empty
//     auditMeta.ProjectID still works (legacy contract preserved).
package agent_memory_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newStoreWithProject opens the DB with all required schemas,
// including the project_id column migration (Phase 4 Chunk 4.1).
func newStoreWithProject(t *testing.T) (*agent_memory.Store, *sql.DB) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := agent_memory.CreateSchema(db); err != nil {
		t.Fatalf("agent_memory.CreateSchema: %v", err)
	}
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project.ApplyProjectIDColumns: %v", err)
	}
	w := audit.NewWriter(db)
	return agent_memory.NewStore(db, w), db
}

func TestSaveAuditProjectIDSurfaced(t *testing.T) {
	s, db := newStoreWithProject(t)
	ctx := context.Background()

	id, err := s.Save(ctx, &agent_memory.Audit{
		Actor:     "operator-nico",
		ProjectID: "proj-huila",
	}, "operator-nico", agent_memory.KindNote, "test", "body", "tag1,tag2", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id <= 0 {
		t.Fatalf("Save returned id=%d; want >0", id)
	}

	// Audit_log row's project_id must match.
	var projectID string
	if err := db.QueryRowContext(ctx,
		"SELECT project_id FROM audit_log ORDER BY audit_id DESC LIMIT 1",
	).Scan(&projectID); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if projectID != "proj-huila" {
		t.Errorf("audit_log.project_id = %q; want proj-huila", projectID)
	}
}

func TestSaveAuditEmptyProjectIDFallsBack(t *testing.T) {
	// Pre-Phase-4 callers (or callers that don't carry a
	// project_id) keep working — the audit row gets project_id=
	// 'default' via the column DEFAULT clause.
	s, db := newStoreWithProject(t)
	ctx := context.Background()

	_, err := s.Save(ctx, &agent_memory.Audit{
		Actor: "operator-nico",
		// ProjectID intentionally empty
	}, "operator-nico", agent_memory.KindNote, "test", "body", "", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	var projectID string
	if err := db.QueryRowContext(ctx,
		"SELECT project_id FROM audit_log ORDER BY audit_id DESC LIMIT 1",
	).Scan(&projectID); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if projectID != "default" {
		t.Errorf("audit_log.project_id = %q; want default (column DEFAULT)", projectID)
	}
}
