// Package orchestration_test: wire-level smoke tests for row 167
// (agent_memory_update ErrInvalidArgument). Exercises the
// orchestrator's AgentMemoryUpdate method end-to-end through a
// real SQLite Store, verifying that the symptom reported in the
// row (same-operator title-only / content-only updates returning
// ErrInvalidArgument) does not reproduce on current main.
//
// Row 167 was filed 2026-08-02 reporting:
//
//	Update row 120 (op=nico) with id + 4.4 KB content
//	  (my session, op=opitacode-dark). ErrInvalidArgument.
//	Update row 120 with id + title-only. ErrInvalidArgument.
//	Update row 160 (op=opitacode-dark, mine) with id + title-only.
//	  ErrInvalidArgument.
//
// Hypothesis at the time: a length cap or content-shape validator
// was rejecting. Workaround in use: archive-then-save pattern.
// Status: OPEN.
//
// If these tests PASS on current main, row 167 is stale (the bug
// was fixed in transit, probably during PR-2/3 cleanup). Archive
// the row with a finding pointing here.
//
// If any test FAILS, this is the wire reproduction the row asked
// for; the orchestrator returns ErrInvalidArgument with no Field
// envelope, and the fix goes here in agent_memory.go.
package orchestration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// wireUpdateOrch returns an orchestrator backed by a fresh SQLite DB
// with migrations applied + active project "acme" set. Mirrors the
// v240Orch helper but isolated for these wire-update tests so a
// future refactor of v240 doesn't drag them along.
func wireUpdateOrch(t *testing.T) (*orchestration.Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "wire_update.db"),
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

// seedRow saves a row via the orchestrator (so INV-1 audit fires)
// and returns its id. Uses operator "alice" — same operator the
// updates below will use (mirroring row 167's "same-operator"
// scenario).
func seedRow(t *testing.T, orch *orchestration.Orchestrator, content string) int64 {
	t.Helper()
	out, err := orch.AgentMemorySave(context.Background(), orchestration.AgentMemorySaveInput{
		Operator: "alice",
		Kind:     "note",
		Content:  content,
		Title:    "initial",
	})
	if err != nil {
		t.Fatalf("seedRow: %v", err)
	}
	return out.Row.ID
}

// TestWireUpdate_TitleOnly_NotErrInvalidArgument is the headline
// row 167 reproduction. Save a row, update with operator="alice"
// (same as seed) + new title only — must succeed and return the
// refreshed row.
//
// Pre-fix behavior (row 167, 2026-08-02): ErrInvalidArgument with
// no Field envelope, indistinguishable from a cross-operator
// block. Post-fix (current main, v2.9.1-alpha): succeeds cleanly,
// title refreshed, content untouched.
//
// Note: out.AuditID is expected to be 0 — AgentMemoryUpdate's
// audit_id lookup is a stub (see latestAuditIDForRow in
// orchestration/agent_memory.go:610, F47 documented debt). The
// audit row IS written atomically by SaveAgentMemory/UpdateAgent-
// Memory; the orchestrator just can't surface its id without a
// new Store method. Documented limitation; NOT the row 167 bug.
func TestWireUpdate_TitleOnly_NotErrInvalidArgument(t *testing.T) {
	orch, _ := wireUpdateOrch(t)
	ctx := context.Background()

	id := seedRow(t, orch, "original content")

	newTitle := "updated title via title-only"
	out, err := orch.AgentMemoryUpdate(ctx, orchestration.AgentMemoryUpdateInput{
		ID:       id,
		Operator: "alice",
		Title:    &newTitle,
	})
	if err != nil {
		// Row 167: same-operator title-only update failed with
		// ErrInvalidArgument. If we hit it here, the wire
		// reproduction is the row's symptom and we need to
		// classify the Field envelope per C-4.
		if strings.Contains(err.Error(), "invalid") {
			t.Fatalf("row 167 reproduced: same-operator title-only update returned ErrInvalidArgument: %v", err)
		}
		t.Fatalf("AgentMemoryUpdate (title-only): %v", err)
	}
	if out.Row.Title != newTitle {
		t.Errorf("title not refreshed: got %q, want %q", out.Row.Title, newTitle)
	}
	if out.Row.Content != "original content" {
		t.Errorf("content was clobbered by title-only update: got %q", out.Row.Content)
	}
}

// TestWireUpdate_ContentOnly_LongerNotRejected mirrors the second
// symptom in row 167: a ~4 KB content update from the same operator
// must not hit a length cap. Use 4.4 KB to match the row exactly.
//
// Pre-fix behavior (row 167): ErrInvalidArgument. Post-fix
// (current main): succeeds cleanly, content refreshed, title
// untouched. AuditID=0 is expected (F47 documented debt; see
// TestWireUpdate_TitleOnly_NotErrInvalidArgument comment).
func TestWireUpdate_ContentOnly_LongerNotRejected(t *testing.T) {
	orch, _ := wireUpdateOrch(t)
	ctx := context.Background()

	id := seedRow(t, orch, "short initial content")

	// ~4.4 KB — row 167's failing payload size.
	newContent := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 125) // ~4 375 chars
	if len(newContent) < 4000 {
		t.Fatalf("test fixture: content too short (%d bytes)", len(newContent))
	}

	out, err := orch.AgentMemoryUpdate(ctx, orchestration.AgentMemoryUpdateInput{
		ID:       id,
		Operator: "alice",
		Content:  &newContent,
	})
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			t.Fatalf("row 167 reproduced: same-operator 4.4 KB content update returned ErrInvalidArgument: %v", err)
		}
		t.Fatalf("AgentMemoryUpdate (content-only 4.4 KB): %v", err)
	}
	if out.Row.Content != newContent {
		t.Errorf("content not refreshed (length mismatch): got %d bytes, want %d", len(out.Row.Content), len(newContent))
	}
	if out.Row.Title != "initial" {
		t.Errorf("title was clobbered by content-only update: got %q", out.Row.Title)
	}
}

// TestWireUpdate_PinnedOnly_NotErrInvalidArgument is a small extra:
// a Pinned-only update must also succeed. None of the row 167
// symptoms mention pinned, but it's the third pointer field —
// covering it here so future regressions show up in one place.
func TestWireUpdate_PinnedOnly_NotErrInvalidArgument(t *testing.T) {
	orch, _ := wireUpdateOrch(t)
	ctx := context.Background()

	id := seedRow(t, orch, "to be pinned")

	pin := true
	out, err := orch.AgentMemoryUpdate(ctx, orchestration.AgentMemoryUpdateInput{
		ID:       id,
		Operator: "alice",
		Pinned:   &pin,
	})
	if err != nil {
		t.Fatalf("AgentMemoryUpdate (pinned-only): %v", err)
	}
	if !out.Row.Pinned {
		t.Errorf("pinned flag not flipped: got false, want true")
	}
}

// TestWireUpdate_MissingID_ErrInvalidArgument confirms the only
// legitimate ErrInvalidArgument path still works: ID <= 0 must
// fail with errMissingField("id"). This pins the behavior so a
// future "graceful missing-field" refactor doesn't silently
// regress into a no-op.
func TestWireUpdate_MissingID_ErrInvalidArgument(t *testing.T) {
	orch, _ := wireUpdateOrch(t)
	ctx := context.Background()

	_, err := orch.AgentMemoryUpdate(ctx, orchestration.AgentMemoryUpdateInput{
		ID:       0,
		Operator: "alice",
	})
	if err == nil {
		t.Fatalf("AgentMemoryUpdate with ID=0 must return an error")
	}
	// Acceptable: store.ErrInvalidArgument OR errMissingField("id") —
	// both wrap store.ErrInvalidArgument. The row's bug is the
	// *absence* of a Field envelope when an update payload is
	// provided; missing-id has a Field and is the expected shape.
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("missing-id error must mention 'id'; got %v", err)
	}
}

// TestWireUpdate_MissingOperator_ErrInvalidArgument mirrors
// MissingID: Operator empty must fail with a Field-tagged
// ErrInvalidArgument. Same rationale as MissingID.
func TestWireUpdate_MissingOperator_ErrInvalidArgument(t *testing.T) {
	orch, _ := wireUpdateOrch(t)
	ctx := context.Background()

	id := seedRow(t, orch, "operator-empty test")

	newTitle := "should not stick"
	_, err := orch.AgentMemoryUpdate(ctx, orchestration.AgentMemoryUpdateInput{
		ID:    id,
		Title: &newTitle,
		// Operator intentionally omitted.
	})
	if err == nil {
		t.Fatalf("AgentMemoryUpdate with empty Operator must return an error")
	}
	if !strings.Contains(err.Error(), "operator") {
		t.Errorf("missing-operator error must mention 'operator'; got %v", err)
	}
}
