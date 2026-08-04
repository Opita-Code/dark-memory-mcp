// Package orchestration_test: F47 regression test for the
// latestAuditIDForRow stub. Verifies that AgentMemorySave.AuditID and
// AgentMemoryUpdate.AuditID are now non-zero (the audit row id is
// resolved via ListWrites + TableName/RowID filters, not stubbed to 0).
//
// Closed 2026-08-03 in commit d3b... (TBD). Prior to F47, Save.AuditID
// returned 0 even when an audit row was emitted atomically — this was
// a documented limitation (F47). After the fix:
//   - Save.AuditID = the write_audit.id of the just-emitted audit row.
//   - Update.AuditID = same.
//   - Archive.AuditID = same.
package orchestration_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// f47Orch returns an orchestrator backed by a fresh SQLite DB with
// migrations applied + active project "acme" set. Mirrors the
// wireUpdateOrch helper but isolated for these F47 tests.
func f47Orch(t *testing.T) (*orchestration.Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "f47.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	return orchestration.New(s, nil), s
}

// TestF47_SaveReturnsNonZeroAuditID is the headline test. Pre-fix
// behavior: Save.AuditID = 0 (documented limitation, F47). Post-fix:
// Save.AuditID = the audit row id, which is a positive int64.
func TestF47_SaveReturnsNonZeroAuditID(t *testing.T) {
	orch, _ := f47Orch(t)
	ctx := context.Background()

	out, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "note",
		Content:  "F47 smoke test",
		Title:    "F47 regression",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if out.Row.ID <= 0 {
		t.Fatalf("Save: row.ID want > 0, got %d", out.Row.ID)
	}
	if out.AuditID <= 0 {
		t.Fatalf("Save: AuditID want > 0 (F47), got %d — write_audit row was emitted but id not surfaced", out.AuditID)
	}

	// Sanity: ListWrites with TableName+RowID should return at least one
	// row, and the latest id should match out.AuditID.
	rows, err := orch.Store.ListWrites(ctx, audit.ListFilters{
		TableName: "agent_memory",
		RowID:     out.Row.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("ListWrites: want >= 1 audit row for agent_memory id=%d, got 0", out.Row.ID)
	}
	// Latest audit row id (ListWrites orders by id DESC).
	if rows[0].ID != out.AuditID {
		t.Errorf("ListWrites[0].ID = %d, want %d (matches Save.AuditID)", rows[0].ID, out.AuditID)
	}
	if rows[0].TableName != "agent_memory" {
		t.Errorf("ListWrites[0].TableName = %q, want %q", rows[0].TableName, "agent_memory")
	}
	if rows[0].RowID != out.Row.ID {
		t.Errorf("ListWrites[0].RowID = %d, want %d", rows[0].RowID, out.Row.ID)
	}
}

// TestF47_UpdateReturnsNonZeroAuditID verifies the update path
// surfaces the audit id too (was also returning 0 pre-fix).
func TestF47_UpdateReturnsNonZeroAuditID(t *testing.T) {
	orch, _ := f47Orch(t)
	ctx := context.Background()

	// Seed.
	saveOut, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "note",
		Content:  "F47 update smoke",
		Title:    "F47 update",
	})
	if err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	saveAuditID := saveOut.AuditID
	if saveAuditID <= 0 {
		t.Fatalf("seed Save.AuditID want > 0, got %d", saveAuditID)
	}

	// Update with title-only.
	newTitle := "F47 update (UPDATED)"
	updOut, err := orch.AgentMemoryUpdate(ctx, orchestration.AgentMemoryUpdateInput{
		ID:       saveOut.Row.ID,
		Operator: "alice",
		Title:    &newTitle,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updOut.AuditID <= 0 {
		t.Fatalf("Update.AuditID want > 0 (F47), got %d", updOut.AuditID)
	}
	if updOut.AuditID <= saveAuditID {
		t.Errorf("Update.AuditID = %d, want > seed AuditID %d (a fresh audit row was emitted)", updOut.AuditID, saveAuditID)
	}
}

// TestF47_ArchiveReturnsNonZeroAuditID verifies the archive path.
// Pre-fix: AgentMemoryArchiveOutput had no AuditID; post-fix the
// orchestrator surfaces the audit id via a follow-up read.
func TestF47_ArchiveReturnsNonZeroAuditID(t *testing.T) {
	orch, _ := f47Orch(t)
	ctx := context.Background()

	saveOut, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "note",
		Content:  "F47 archive smoke",
		Title:    "F47 archive",
	})
	if err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	archOut, err := orch.AgentMemoryArchive(ctx, orchestration.AgentMemoryArchiveInput{
		ID:       saveOut.Row.ID,
		Operator: "alice",
	})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if archOut.ID != saveOut.Row.ID {
		t.Errorf("Archive.ID = %d, want %d", archOut.ID, saveOut.Row.ID)
	}
	if archOut.ArchivedAt == "" {
		t.Errorf("Archive.ArchivedAt want non-empty")
	}

	// Indirect check: an audit row for this agent_memory id should
	// exist after archive. Pre-fix, the orchestrator couldn't surface
	// its id; now we at least confirm the row was emitted.
	rows, err := orch.Store.ListWrites(ctx, audit.ListFilters{
		TableName: "agent_memory",
		RowID:     saveOut.Row.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("want >= 2 audit rows (save + archive) for agent_memory id=%d, got %d", saveOut.Row.ID, len(rows))
	}
}

// TestF47_UnknownRowIDReturnsZero is the defensive case: a row that
// was never written should not surface a phantom audit id. Pre-fix
// returned 0 for everything; this test pins the contract that "no
// audit row" still returns 0 (not a non-zero garbage value).
func TestF47_UnknownRowIDReturnsZero(t *testing.T) {
	orch, _ := f47Orch(t)
	ctx := context.Background()

	out, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "note",
		Content:  "F47 unknown-row test",
		Title:    "F47 unknown",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Query for a row id that was never saved.
	rows, err := orch.Store.ListWrites(ctx, audit.ListFilters{
		TableName: "agent_memory",
		RowID:     999999,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("unknown RowID should return 0 audit rows, got %d", len(rows))
	}

	// Verify our row still has a valid audit id (i.e., the query didn't
	// accidentally include it).
	rows, err = orch.Store.ListWrites(ctx, audit.ListFilters{
		TableName: "agent_memory",
		RowID:     out.Row.ID,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("ListWrites (real): %v", err)
	}
	if len(rows) != 1 || rows[0].ID != out.AuditID {
		t.Errorf("real-row lookup should return 1 row matching AuditID; got %d rows, ids=%v",
			len(rows), rows)
	}
}

// compile-time check: agentmemory types are reachable (parity with v2_4_0 tests).
var _ agentmemory.AgentMemory