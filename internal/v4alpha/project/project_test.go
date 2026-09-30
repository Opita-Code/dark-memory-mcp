package project_test

// L1 + L2 tests for the project package.
//
// Each test names a specific scenario that must hold:
//
//   - CreateSchema is idempotent (safe to call twice).
//   - The 'default' project is auto-seeded on first boot.
//   - Store.Create rejects reserved ids ('default', 'dark').
//   - Store.Create rejects invalid project_ids (uppercase, special chars).
//   - Store.Create is idempotent on project_id (replay returns
//     same row, mutable fields NOT overwritten).
//   - Store.Archive soft-deletes (ArchivedAt != nil).
//   - ApplyProjectIDColumns is idempotent (pragma_table_info
//     detects pre-existing columns, no error on re-run).

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// TestCreateSchema_Idempotent — call CreateSchema twice; second
// call is no-op (no error, no double-seed).
func TestCreateSchema_Idempotent(t *testing.T) {
	db := newRawDB(t)

	if err := project.CreateSchema(db); err != nil {
		t.Fatalf("first CreateSchema: %v", err)
	}
	if err := project.CreateSchema(db); err != nil {
		t.Fatalf("second CreateSchema (should be no-op): %v", err)
	}

	// Verify the 'default' row exists exactly ONCE (not twice).
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM projects WHERE project_id = 'default'",
	).Scan(&n); err != nil {
		t.Fatalf("count default: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 'default' row, got %d", n)
	}
}

// TestStore_Create_DefaultSeed — Lookup("default") succeeds WITHOUT
// a prior Store.Create call (the seed runs at CreateSchema).
func TestStore_Create_DefaultSeed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.Lookup(ctx, "default")
	if err != nil {
		t.Fatalf("Lookup default: %v", err)
	}
	if !got.IsDefault() {
		t.Errorf("expected IsDefault=true, got false (ProjectID=%q)", got.ProjectID)
	}
	if got.DisplayName == "" {
		t.Error("default project display_name is empty (seed bug)")
	}
	if got.ArchivedAt != nil {
		t.Errorf("default project archived_at should be nil, got %v", got.ArchivedAt)
	}
}

// TestStore_Create_ReservedID_Rejected — Create with project_id
// "default" or "dark" returns ErrReservedProjectID.
//
// Critical: 'default' MUST be rejected so an operator typo cannot
// override the seeded workstream.
func TestStore_Create_ReservedID_Rejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, reserved := range []string{"default", "dark"} {
		t.Run("reserved="+reserved, func(t *testing.T) {
			err := s.Create(ctx, &project.Project{
				ProjectID:   reserved,
				DisplayName: "Should not be allowed",
			})
			if err == nil {
				t.Fatalf("expected ErrReservedProjectID for %q, got nil", reserved)
			}
			if !errors.Is(err, project.ErrReservedProjectID) {
				t.Errorf("expected errors.Is(err, ErrReservedProjectID)=true for %q, got %v",
					reserved, err)
			}
		})
	}
}

// TestStore_Create_Idempotent — replay Create with same project_id;
// no error, mutable fields NOT overwritten (first-writer wins).
func TestStore_Create_Idempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	original := &project.Project{
		ProjectID:      "opita-market",
		DisplayName:    "Opita Market",
		Description:    "Pilot workstream for opita-market",
		DefaultAgentID: "agent-opita-1",
	}
	if err := s.Create(ctx, original); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	originalCreatedAt := original.CreatedAt

	// Replay with MUTATED display_name + description.
	replay := &project.Project{
		ProjectID:      "opita-market",
		DisplayName:    "MUTATED Opita Market",
		Description:    "MUTATED description",
		DefaultAgentID: "MUTATED agent",
	}
	if err := s.Create(ctx, replay); err != nil {
		t.Fatalf("replay Create (should be no-op on existing): %v", err)
	}

	// The Store should have OVERWRITTEN the caller's struct with
	// the canonical (original) row. Verify by Lookup.
	got, err := s.Lookup(ctx, "opita-market")
	if err != nil {
		t.Fatalf("Lookup after replay: %v", err)
	}

	if got.DisplayName != "Opita Market" {
		t.Errorf("DisplayName mutated on replay: expected %q, got %q",
			"Opita Market", got.DisplayName)
	}
	if got.Description != "Pilot workstream for opita-market" {
		t.Errorf("Description mutated on replay: expected original, got %q", got.Description)
	}
	if got.DefaultAgentID != "agent-opita-1" {
		t.Errorf("DefaultAgentID mutated on replay: expected original, got %q", got.DefaultAgentID)
	}
	if !got.CreatedAt.Equal(originalCreatedAt) {
		t.Errorf("CreatedAt mutated on replay: expected %v, got %v",
			originalCreatedAt, got.CreatedAt)
	}
}

// TestStore_Create_InvalidProjectID — Create with malformed
// project_id returns ErrInvalidProjectID.
//
// Tests 5 invalid forms: uppercase, too short, too long, starts
// with hyphen, contains underscore.
func TestStore_Create_InvalidProjectID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	invalids := []string{
		"OPITA-MARKET",         // uppercase
		"ab",                   // too short (< 3 chars)
		"a-very-long-project-id-that-exceeds-the-sixty-four-character-limit-12345678", // too long (> 64)
		"-leading-hyphen",      // starts with hyphen
		"contains_underscore",  // contains underscore
		"",                     // empty
		"  spaces  ",           // spaces
	}

	for _, id := range invalids {
		t.Run("invalid="+id, func(t *testing.T) {
			err := s.Create(ctx, &project.Project{
				ProjectID:   id,
				DisplayName: "Test",
			})
			if err == nil {
				t.Fatalf("expected error for invalid project_id %q, got nil", id)
			}
			if id == "" {
				// Empty is a generic ErrInvalidProject, not ErrInvalidProjectID
				// (the regex requires ≥1 char; Validate catches the empty
				// case before the regex).
				if !errors.Is(err, project.ErrInvalidProject) {
					t.Errorf("expected ErrInvalidProject for empty, got %v", err)
				}
				return
			}
			if !errors.Is(err, project.ErrInvalidProjectID) {
				t.Errorf("expected ErrInvalidProjectID for %q, got %v", id, err)
			}
		})
	}
}

// TestStore_Archive_SetsArchivedAt — Archive soft-deletes; Lookup
// returns ArchivedAt != nil. Re-archive returns ErrProjectAlreadyGone.
func TestStore_Archive_SetsArchivedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Create(ctx, &project.Project{
		ProjectID:   "pasiones",
		DisplayName: "Pasiones",
	}); err != nil {
		t.Fatalf("Create pasiones: %v", err)
	}

	if err := s.Archive(ctx, "pasiones"); err != nil {
		t.Fatalf("Archive pasiones: %v", err)
	}

	got, err := s.Lookup(ctx, "pasiones")
	if err != nil {
		t.Fatalf("Lookup after archive: %v", err)
	}
	if !got.IsArchived() {
		t.Errorf("expected IsArchived=true, got false (ArchivedAt=%v)", got.ArchivedAt)
	}
	if got.ArchivedAt == nil {
		t.Error("ArchivedAt should be set, got nil")
	}

	// Re-archive: must return ErrProjectAlreadyGone.
	err = s.Archive(ctx, "pasiones")
	if err == nil {
		t.Fatal("expected ErrProjectAlreadyGone on re-archive, got nil")
	}
	if !errors.Is(err, project.ErrProjectAlreadyGone) {
		t.Errorf("expected ErrProjectAlreadyGone, got %v", err)
	}

	// Archive non-existent: must return ErrProjectNotFound.
	err = s.Archive(ctx, "does-not-exist")
	if err == nil {
		t.Fatal("expected ErrProjectNotFound, got nil")
	}
	if !errors.Is(err, project.ErrProjectNotFound) {
		t.Errorf("expected ErrProjectNotFound, got %v", err)
	}
}

// TestApplyProjectIDColumns_Idempotent — call twice; second call
// no-op (pragma_table_info detects pre-existing column).
//
// Critical: production DBs may already be Phase-4-initialized.
// Re-running ApplyProjectIDColumns must not error or duplicate.
func TestApplyProjectIDColumns_Idempotent(t *testing.T) {
	s := newTestStoreFull(t)
	ctx := context.Background()

	// First call (already ran in newTestStoreFull, but let's be
	// explicit). newTestStoreFull already called it once.
	// Re-run to verify idempotency.
	if err := project.ApplyProjectIDColumns(ctx, rawDB(s)); err != nil {
		t.Fatalf("re-run ApplyProjectIDColumns: %v", err)
	}

	// Verify the 5 tables each have project_id.
	tables := []string{
		"agent_memory", "audit_log", "sdd_evaluations",
		"vibe_specs", "vibe_artifacts",
	}
	for _, table := range tables {
		var n int
		if err := rawDB(s).QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'project_id'",
			table,
		).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s: expected project_id column, got count=%d", table, n)
		}
	}
}

// TestStore_List_FiltersArchived — List with includeArchived=false
// returns only active projects.
func TestStore_List_FiltersArchived(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Create(ctx, &project.Project{ProjectID: "active-1", DisplayName: "Active 1"}); err != nil {
		t.Fatalf("Create active-1: %v", err)
	}
	if err := s.Create(ctx, &project.Project{ProjectID: "active-2", DisplayName: "Active 2"}); err != nil {
		t.Fatalf("Create active-2: %v", err)
	}
	if err := s.Create(ctx, &project.Project{ProjectID: "to-archive", DisplayName: "To archive"}); err != nil {
		t.Fatalf("Create to-archive: %v", err)
	}
	if err := s.Archive(ctx, "to-archive"); err != nil {
		t.Fatalf("Archive to-archive: %v", err)
	}

	// includeArchived=false: should see default + active-1 + active-2.
	active, err := s.List(ctx, false)
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(active) != 3 {
		names := make([]string, 0, len(active))
		for _, p := range active {
			names = append(names, p.ProjectID)
		}
		t.Errorf("expected 3 active projects, got %d (%v)", len(active), names)
	}

	// includeArchived=true: should see default + active-1 + active-2 + to-archive.
	all, err := s.List(ctx, true)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 4 {
		names := make([]string, 0, len(all))
		for _, p := range all {
			names = append(names, p.ProjectID)
		}
		t.Errorf("expected 4 total projects, got %d (%v)", len(all), names)
	}
}

// newRawDB opens an in-memory SQLite + creates audit + project
// schemas (no Store, no audit.Writer). Used by tests that need
// raw DB access (e.g., TestCreateSchema_Idempotent verifies the
// 'default' row count via raw SQL).
func newRawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := project.CreateSchema(db); err != nil {
		t.Fatalf("project.CreateSchema: %v", err)
	}
	return db
}

// rawDB extracts the underlying *sql.DB from a *project.Store.
// Tests that need raw DB access (e.g., pragma_table_info checks)
// use this. Production code should NEVER call this — use the
// Store methods instead.
func rawDB(s *project.Store) *sql.DB {
	return s.DB()
}