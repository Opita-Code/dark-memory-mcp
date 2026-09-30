package project_test

// Shared helpers for project tests (L1 + L2 + property).
//
// Wires up in-memory SQLite with audit + project schemas applied,
// then returns a *project.Store. Cleanup is auto-registered.
//
// Same pattern as internal/v4alpha/session/helpers_test.go:
// newTestStoreAny takes a storeCleanupper so both *testing.T and
// *rapid.T can use it.

import (
	"context"

	"pgregory.net/rapid"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/vibe"
)

// storeCleanupper is the interface satisfied by both *testing.T and
// *rapid.T — both have Cleanup(func()) and Fatalf(format, ...).
type storeCleanupper interface {
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// newTestStore opens in-memory SQLite, creates audit + project
// schemas, wires Store + Writer, and registers cleanup. Uses
// store.OpenSQLite so the connection setup matches production.
//
// Does NOT call ApplyProjectIDColumns by default (some tests
// verify the migration independently). Callers can run the
// migration manually if needed.
func newTestStore(t storeCleanupper) *project.Store {
	return newTestStoreAny(t)
}

// newTestStoreFull is like newTestStore but ALSO runs
// ApplyProjectIDColumns. Use when the test needs the 5-table
// project_id migration applied.
func newTestStoreFull(t storeCleanupper) *project.Store {
	return newTestStoreAnyFull(t)
}

// newTestStoreAny opens in-memory SQLite + audit + project schema
// + the 4 OTHER tables that get project_id (sdd_evaluations,
// vibe_specs, vibe_artifacts, agent_memory). Without these tables
// existing, ApplyProjectIDColumns fails because it tries to ADD
// COLUMN to non-existent tables.
//
// Mirrors the production applyAllSchemas order:
//   audit → session → agent_memory → manifest → vibe → judge → project.
func newTestStoreAny(t storeCleanupper) *project.Store {
	db, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := session.CreateSchema(db); err != nil {
		t.Fatalf("session.CreateSchema: %v", err)
	}
	if err := agent_memory.CreateSchema(db); err != nil {
		t.Fatalf("agent_memory.CreateSchema: %v", err)
	}
	if err := vibe.CreateSpecSchema(db); err != nil {
		t.Fatalf("vibe.CreateSpecSchema: %v", err)
	}
	if err := vibe.CreateArtifactSchema(db); err != nil {
		t.Fatalf("vibe.CreateArtifactSchema: %v", err)
	}
	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge.CreateSchema: %v", err)
	}
	if err := project.CreateSchema(db); err != nil {
		t.Fatalf("project.CreateSchema: %v", err)
	}
	// ApplyProjectIDColumns adds project_id to audit_log so the
	// audit.Writer.WriteWithProject can INSERT the column. Must
	// run AFTER audit_log + the other 4 tables exist.
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project.ApplyProjectIDColumns: %v", err)
	}

	w := audit.NewWriter(db)
	ps, err := project.NewStore(db, w)
	if err != nil {
		t.Fatalf("project.NewStore: %v", err)
	}
	return ps
}

// newTestStoreAnyFull is like newTestStoreAny but ALSO creates the
// 4 OTHER tables that get project_id (agent_memory, sdd_evaluations,
// vibe_specs, vibe_artifacts). This lets TestApplyProjectIDColumns
// verify the migration across all 5 targets.
//
// Note: agent_memory, vibe, judge packages each have their own
// CreateSchema. We call them to make the 5 tables exist so the
// ApplyProjectIDColumns migration has targets to add columns to.
func newTestStoreAnyFull(t storeCleanupper) *project.Store {
	db, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Order matters: all other schemas first (so ApplyProjectIDColumns
	// has tables to operate on), then project schema + migration.
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := session.CreateSchema(db); err != nil {
		t.Fatalf("session.CreateSchema: %v", err)
	}
	if err := agent_memory.CreateSchema(db); err != nil {
		t.Fatalf("agent_memory.CreateSchema: %v", err)
	}
	if err := vibe.CreateSpecSchema(db); err != nil {
		t.Fatalf("vibe.CreateSpecSchema: %v", err)
	}
	if err := vibe.CreateArtifactSchema(db); err != nil {
		t.Fatalf("vibe.CreateArtifactSchema: %v", err)
	}
	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge.CreateSchema: %v", err)
	}
	if err := project.CreateSchema(db); err != nil {
		t.Fatalf("project.CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)

	// Apply the 5-table migration AFTER all schemas exist.
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project.ApplyProjectIDColumns: %v", err)
	}

	ps, err := project.NewStore(db, w)
	if err != nil {
		t.Fatalf("project.NewStore: %v", err)
	}
	return ps
}

// newTestStoreRapid is the *rapid.T facade for newTestStoreAny.
func newTestStoreRapid(t *rapid.T) *project.Store {
	return newTestStoreAny(t)
}

// newTestStoreRapidFull is the *rapid.T facade for newTestStoreAnyFull.
func newTestStoreRapidFull(t *rapid.T) *project.Store {
	return newTestStoreAnyFull(t)
}