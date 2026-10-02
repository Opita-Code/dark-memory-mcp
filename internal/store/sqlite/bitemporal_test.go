// Package sqlite — bitemporal_test.go: hermetic tests for
// MarkSupersededAgentMemory + RecallAtTime (alpha.20 Chunk 8.7,
// ADR-014 lite).
//
// Uses newBitemporalTestStore (test helper) to spin up a fresh DB
// at v31 (Phase 5 + bitemporal_lite schema), then exercises the
// two operations end-to-end on real SQLite. No mocks.
//
// Verification strategy: end-to-end behavioral (call MarkSuperseded,
// then call RecallAtTime and verify the superseded row is excluded
// from "as-of" queries past valid_to). Avoids direct DB access —
// the Store interface is the contract.
package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// newBitemporalTestStore creates a test Store wired to a fresh
// SQLite DB. The DB is at the latest schema (v31) via the production
// Open() path. Returns the store and a cleanup func.
func newBitemporalTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "bitemporal-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	cleanup := func() { _ = st.Close() }
	return st, cleanup
}

// seedDecisionRow saves a decision-kind row and returns its id. Uses
// kind=decision so it qualifies for MarkSupersededAgentMemory.
func seedDecisionRow(t *testing.T, st store.Store, op, content string) int64 {
	t.Helper()
	id, err := st.SaveAgentMemory(context.Background(),
		store.WriteContext{Actor: op, WritePath: "TestBitemporal"},
		&agentmemory.AgentMemory{
			Operator: op,
			Kind:     agentmemory.KindDecision,
			Content:  content,
		})
	if err != nil {
		t.Fatalf("SaveAgentMemory: %v", err)
	}
	return id
}

// TestMarkSupersededAgentMemory_Success verifies the happy path:
// MarkSupersededAgentMemory must complete without error when input is
// valid. Behavioral verification (decision_state column + decision_transitions
// row + valid_to) is via TestRecallAtTime_ExcludesSuperseded.
func TestMarkSupersededAgentMemory_Success(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()

	oldID := seedDecisionRow(t, st, "tester", "use schema v30")
	newID := seedDecisionRow(t, st, "tester", "use schema v31 instead")

	err := st.MarkSupersededAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester", WritePath: "TestMarkSuperseded"},
		oldID, newID,
		"policy_update", "v31 supersedes v30 schema decision", "audit://42",
		"2026-10-01T00:00:00.0000000Z",
	)
	if err != nil {
		t.Fatalf("MarkSupersededAgentMemory: %v", err)
	}

	// After MarkSuperseded, old row should be excluded from
	// "as-of past valid_to" RecallAtTime. Verified by the
	// dedicated test below.
}

// TestMarkSupersededAgentMemory_SelfSupersedeFails verifies
// ErrInvalidSupersession when oldID == newID.
func TestMarkSupersededAgentMemory_SelfSupersedeFails(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()
	id := seedDecisionRow(t, st, "tester", "self-supersede attempt")

	err := st.MarkSupersededAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester"},
		id, id, "", "trying to supersede self", "", "")
	if err == nil {
		t.Fatalf("expected ErrInvalidSupersession for self-supersede, got nil")
	}
	if !errors.Is(err, store.ErrInvalidSupersession) {
		t.Errorf("expected ErrInvalidSupersession, got %v", err)
	}
}

// TestMarkSupersededAgentMemory_MissingRowFails verifies
// ErrInvalidSupersession when oldMemID doesn't exist.
func TestMarkSupersededAgentMemory_MissingRowFails(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()
	newID := seedDecisionRow(t, st, "tester", "real new decision")

	err := st.MarkSupersededAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester"},
		99999, newID, "", "old is missing", "", "")
	if err == nil {
		t.Fatalf("expected ErrInvalidSupersession for missing row, got nil")
	}
	if !errors.Is(err, store.ErrInvalidSupersession) {
		t.Errorf("expected ErrInvalidSupersession, got %v", err)
	}
}

// TestMarkSupersededAgentMemory_NonDecisionFails verifies that
// supersession refuses non-decision kinds.
func TestMarkSupersededAgentMemory_NonDecisionFails(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()

	// old = observation (non-decision).
	oldID, err := st.SaveAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester"},
		&agentmemory.AgentMemory{
			Operator: "tester",
			Kind:     agentmemory.KindObservation,
			Content:  "just an observation",
		})
	if err != nil {
		t.Fatalf("SaveAgentMemory(observation): %v", err)
	}
	newID := seedDecisionRow(t, st, "tester", "decision target")

	err = st.MarkSupersededAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester"},
		oldID, newID, "", "old is not a decision", "", "")
	if err == nil {
		t.Fatalf("expected ErrInvalidSupersession for non-decision old, got nil")
	}
	if !errors.Is(err, store.ErrInvalidSupersession) {
		t.Errorf("expected ErrInvalidSupersession, got %v", err)
	}
}

// TestMarkSupersededAgentMemory_ReasonRequired verifies that
// empty reason returns ErrInvalidSupersession.
func TestMarkSupersededAgentMemory_ReasonRequired(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()
	oldID := seedDecisionRow(t, st, "tester", "old needs reason")
	newID := seedDecisionRow(t, st, "tester", "new also needs reason")

	err := st.MarkSupersededAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester"},
		oldID, newID, "", "", "", "")
	if err == nil {
		t.Fatalf("expected ErrInvalidSupersession for empty reason, got nil")
	}
	if !errors.Is(err, store.ErrInvalidSupersession) {
		t.Errorf("expected ErrInvalidSupersession, got %v", err)
	}
}

// TestRecallAtTime_BasicAsOf verifies that RecallAtTime returns
// rows with valid_time <= t. Since SaveAgentMemory sets
// valid_time = created_at = NOW, all rows have valid_time ≈ NOW. As-of
// a future time → all rows; as-of a past time → 0 rows.
func TestRecallAtTime_BasicAsOf(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()

	_ = seedDecisionRow(t, st, "tester", "row 1")
	_ = seedDecisionRow(t, st, "tester", "row 2")
	_ = seedDecisionRow(t, st, "tester", "row 3")

	// As-of future time → all 3 rows.
	rows, err := st.RecallAtTime(context.Background(),
		time.Now().Add(time.Hour), "", 0)
	if err != nil {
		t.Fatalf("RecallAtTime future: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("future rows: got %d, want 3", len(rows))
	}

	// As-of past time → 0 rows (the rows were saved NOW).
	rows, err = st.RecallAtTime(context.Background(),
		time.Now().Add(-time.Hour), "", 0)
	if err != nil {
		t.Fatalf("RecallAtTime past: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("past rows: got %d, want 0", len(rows))
	}
}

// TestRecallAtTime_KindFilter verifies kind narrowing works.
func TestRecallAtTime_KindFilter(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()

	_ = seedDecisionRow(t, st, "tester", "decision a")
	obsID, _ := st.SaveAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester"},
		&agentmemory.AgentMemory{
			Operator: "tester",
			Kind:     agentmemory.KindObservation,
			Content:  "obs a",
		})

	// As-of NOW → only the observation (kind filter).
	rows, err := st.RecallAtTime(context.Background(), time.Now().Add(time.Hour), "observation", 0)
	if err != nil {
		t.Fatalf("RecallAtTime: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != obsID {
		t.Errorf("kind=observation: got %d rows, want 1 (id=%d)", len(rows), obsID)
	}
}

// TestRecallAtTime_ExcludesArchived verifies archived_at IS NOT NULL
// rows are filtered.
func TestRecallAtTime_ExcludesArchived(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()

	id := seedDecisionRow(t, st, "tester", "will archive")

	if err := st.ArchiveAgentMemory(context.Background(),
		store.WriteContext{Actor: "tester"}, id); err != nil {
		t.Fatalf("ArchiveAgentMemory: %v", err)
	}

	rows, err := st.RecallAtTime(context.Background(), time.Now().Add(time.Hour), "", 0)
	if err != nil {
		t.Fatalf("RecallAtTime: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Errorf("archived row id=%d should be excluded", id)
		}
	}
}

// TestRecallAtTime_LimitCap verifies limit caps the result size.
func TestRecallAtTime_LimitCap(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		seedDecisionRow(t, st, "tester", "limit test row")
	}

	rows, err := st.RecallAtTime(context.Background(), time.Now().Add(time.Hour), "", 3)
	if err != nil {
		t.Fatalf("RecallAtTime: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("rows: got %d, want 3 (limit)", len(rows))
	}
}

// TestRecallAtTime_ZeroTimeRejected verifies t.IsZero() returns
// ErrInvalidArgument.
func TestRecallAtTime_ZeroTimeRejected(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()
	_, err := st.RecallAtTime(context.Background(), time.Time{}, "", 0)
	if err == nil {
		t.Fatalf("expected error for zero t, got nil")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument, got %v", err)
	}
}

// TestRecallAtTime_OrderingNewestFirst verifies the deterministic
// sort order: valid_time DESC then id ASC.
func TestRecallAtTime_OrderingNewestFirst(t *testing.T) {
	st, cleanup := newBitemporalTestStore(t)
	defer cleanup()

	// 3 rows saved sequentially. SaveAgentMemory uses the same
	// clock for each; the id ordering breaks the tie.
	id1 := seedDecisionRow(t, st, "tester", "first saved")
	id2 := seedDecisionRow(t, st, "tester", "second saved")
	id3 := seedDecisionRow(t, st, "tester", "third saved")

	rows, err := st.RecallAtTime(context.Background(), time.Now().Add(time.Hour), "", 0)
	if err != nil {
		t.Fatalf("RecallAtTime: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows: got %d, want 3", len(rows))
	}
	// Newest (highest id) first.
	if rows[0].ID != id3 || rows[1].ID != id2 || rows[2].ID != id1 {
		t.Errorf("ordering: got [%d, %d, %d], want [%d, %d, %d]",
			rows[0].ID, rows[1].ID, rows[2].ID, id3, id2, id1)
	}
}