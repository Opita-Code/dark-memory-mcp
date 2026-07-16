// Tests for atomic spec 2.3 (VLPPersistence) — see docs/DMAP_V1_1.md §4 Layer 2.
//
// The acceptance test (TestVLP_PersistenceRoundTrip) is the ONE test that
// defines "done" for this spec: save a state, load it back, verify the
// typed State round-trips through the int column. Other tests cover edge
// cases (empty session_id, unknown state rejection, listing) and the
// post-bug-hunt additions (write_audit verification, cross-project
// isolation, name→int conversion in ListByState, input validation).
package vlp

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// newTestPersistence opens an in-memory SQLite store (via tempfile), seeds
// the default project, and returns a Persistence + cleanup func.
func newTestPersistence(t *testing.T) (*Persistence, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("create default project: %v", err)
	}
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	p, err := NewPersistence(s)
	if err != nil {
		_ = s.Close()
		t.Fatalf("new persistence: %v", err)
	}
	return p, func() { _ = s.Close() }
}

// TestVLP_PersistenceRoundTrip is the ACCEPTANCE TEST for spec 2.3.
// Save a state, load it back, verify the typed State round-trips through
// the int column. Also tests upsert (save twice, same row count).
func TestVLP_PersistenceRoundTrip(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{
		Actor:     "test-actor",
		SessionID: "session-rt",
		WritePath: "TestVLP_PersistenceRoundTrip",
	}

	// Initial save: drafting_spec, turn 0
	if err := p.Save(ctx, wc, "session-rt", StateDraftingSpec, EventSessionStart, VerdictUnknown, 0, ""); err != nil {
		t.Fatalf("Save 1: %v", err)
	}

	// Load → should be drafting_spec
	snap, err := p.Load(ctx, "session-rt")
	if err != nil {
		t.Fatalf("Load 1: %v", err)
	}
	if !snap.Exists {
		t.Fatal("Load 1: snapshot should exist")
	}
	if snap.State != StateDraftingSpec {
		t.Errorf("Load 1.State = %s, want drafting_spec", snap.State)
	}
	if snap.TurnCount != 0 {
		t.Errorf("Load 1.TurnCount = %d, want 0", snap.TurnCount)
	}

	// Upsert: same session, advance state, increment turn
	if err := p.Save(ctx, wc, "session-rt", StateSpecActive, EventVibePublish, VerdictUnknown, 1, ""); err != nil {
		t.Fatalf("Save 2 (upsert): %v", err)
	}

	// Load again → should be spec_active, turn 1
	snap, err = p.Load(ctx, "session-rt")
	if err != nil {
		t.Fatalf("Load 2: %v", err)
	}
	if snap.State != StateSpecActive {
		t.Errorf("Load 2.State = %s, want spec_active", snap.State)
	}
	if snap.TurnCount != 1 {
		t.Errorf("Load 2.TurnCount = %d, want 1", snap.TurnCount)
	}

	// List should still find exactly one row (upsert, not insert)
	rows, err := p.ListByState(ctx, StateUnknown, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, r := range rows {
		if r.SessionID == "session-rt" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("List count for session-rt = %d, want 1 (upsert should not duplicate)", count)
	}
}

// TestPersistence_Save_RejectsInvalidInputs covers input validation
// (empty session_id, StateUnknown, negative turn).
func TestPersistence_Save_RejectsInvalidInputs(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"empty session_id", func() error {
			return p.Save(ctx, wc, "", StateIdle, EventSessionStart, VerdictUnknown, 0, "")
		}},
		{"StateUnknown rejected", func() error {
			return p.Save(ctx, wc, "s-unknown", StateUnknown, EventSessionStart, VerdictUnknown, 0, "")
		}},
		{"negative turn rejected", func() error {
			return p.Save(ctx, wc, "s-neg-turn", StateIdle, EventSessionStart, VerdictUnknown, -1, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestPersistence_Load_NonExistent returns Snapshot{Exists: false}, no error.
func TestPersistence_Load_NonExistent(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()

	snap, err := p.Load(ctx, "session-that-does-not-exist")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Exists {
		t.Error("Snapshot.Exists should be false for non-existent session")
	}
	if snap.State != StateUnknown {
		t.Errorf("Snapshot.State = %s, want StateUnknown (zero value)", snap.State)
	}
	if snap.Row != nil {
		t.Error("Snapshot.Row should be nil for non-existent session")
	}
}

// TestPersistence_Load_EmptySessionID is rejected.
func TestPersistence_Load_EmptySessionID(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	_, err := p.Load(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty session_id")
	}
}

// TestPersistence_AllStatesRoundTrip verifies every State enum value
// survives the int ↔ string ↔ int round-trip through the database.
func TestPersistence_AllStatesRoundTrip(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "all-states", WritePath: "TestAllStates"}

	// Skip StateUnknown (the zero-value sentinel; not persistable).
	states := []State{
		StateIdle, StateDraftingSpec, StateSpecActive, StateDriftJudging,
		StateComplete, StateNeedsHuman, StateAborted,
	}
	for i, st := range states {
		sessionID := "all-states-" + st.String()
		if err := p.Save(ctx, wc, sessionID, st, EventSessionStart, VerdictUnknown, i, ""); err != nil {
			t.Fatalf("Save %s: %v", st, err)
		}
		snap, err := p.Load(ctx, sessionID)
		if err != nil {
			t.Fatalf("Load %s: %v", st, err)
		}
		if !snap.Exists {
			t.Fatalf("Load %s: Exists should be true", st)
		}
		if snap.State != st {
			t.Errorf("State round-trip: saved %s, loaded %s", st, snap.State)
		}
	}
}

// TestPersistence_UnknownEventStoredAsEmpty verifies the bug-hunt fix:
// passing EventUnknown or VerdictUnknown should NOT store the literal
// string "unknown(0)" — it should store an empty string.
func TestPersistence_UnknownEventStoredAsEmpty(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "u", WritePath: "TestUnknownEvent"}

	// First save with EventUnknown + VerdictUnknown (typical "starting" state).
	if err := p.Save(ctx, wc, "session-x", StateIdle, EventUnknown, VerdictUnknown, 0, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	snap, err := p.Load(ctx, "session-x")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Row == nil {
		t.Fatal("Row should be non-nil")
	}
	if snap.Row.LastEvent != "" {
		t.Errorf("LastEvent = %q, want \"\" (EventUnknown should NOT be stored as 'unknown(0)')", snap.Row.LastEvent)
	}
	if snap.Row.LastVerdict != "" {
		t.Errorf("LastVerdict = %q, want \"\" (VerdictUnknown should NOT be stored as 'unknown(0)')", snap.Row.LastVerdict)
	}

	// Save again with EventSessionStart + VerdictUnknown — DB should now have "session_start".
	if err := p.Save(ctx, wc, "session-x", StateDraftingSpec, EventSessionStart, VerdictUnknown, 1, ""); err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	snap, _ = p.Load(ctx, "session-x")
	if snap.Row.LastEvent != "session_start" {
		t.Errorf("LastEvent after Save 2 = %q, want session_start", snap.Row.LastEvent)
	}
}

// TestPersistence_ListByStateByEnum verifies the bug-hunt fix:
// ListByState should accept the typed State enum (StateDraftingSpec)
// and resolve it to the numeric form internally. Previously the
// Store's stateFilter was passed through as-is, and the test had to
// filter client-side.
func TestPersistence_ListByStateByEnum(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "list-test", WritePath: "TestList"}

	// Seed: 2 in drafting_spec, 1 in spec_active, 1 in complete
	seeds := []struct {
		sessionID string
		state     State
	}{
		{"s1", StateDraftingSpec},
		{"s2", StateDraftingSpec},
		{"s3", StateSpecActive},
		{"s4", StateComplete},
	}
	for _, seed := range seeds {
		if err := p.Save(ctx, wc, seed.sessionID, seed.state, EventSessionStart, VerdictUnknown, 0, ""); err != nil {
			t.Fatalf("Save %s: %v", seed.sessionID, err)
		}
	}

	// Filter by typed enum — this is the new API.
	rows, err := p.ListByState(ctx, StateDraftingSpec, 0)
	if err != nil {
		t.Fatalf("ListByState drafting_spec: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("ListByState(StateDraftingSpec) = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.State != int(StateDraftingSpec) {
			t.Errorf("row %s has state=%d, want %d (%s)", r.SessionID, r.State, int(StateDraftingSpec), StateDraftingSpec)
		}
	}

	rows, err = p.ListByState(ctx, StateSpecActive, 0)
	if err != nil {
		t.Fatalf("ListByState spec_active: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("ListByState(StateSpecActive) = %d, want 1", len(rows))
	}
}

// TestPersistence_LimitRespected verifies the limit parameter on List.
func TestPersistence_LimitRespected(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "limit-test", WritePath: "TestLimit"}

	// Seed 5 sessions in drafting_spec
	for i := 0; i < 5; i++ {
		sessionID := "limit-" + StateDraftingSpec.String() + "-" + strconv.Itoa(i)
		if err := p.Save(ctx, wc, sessionID, StateDraftingSpec, EventSessionStart, VerdictUnknown, 0, ""); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	rows, err := p.ListByState(ctx, StateDraftingSpec, 2)
	if err != nil {
		t.Fatalf("List with limit: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("ListByState(limit=2) = %d, want 2", len(rows))
	}
}

// TestPersistence_WritesAuditRow verifies the bug-hunt fix (C4):
// every Save emits exactly one write_audit row in the same transaction.
// Requires direct access to the Store; uses runtime.Open like other
// tests so it shares the same SQLite file.
func TestPersistence_WritesAuditRow(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{
		Actor:     "test-audit",
		SessionID: "session-audit",
		WritePath: "TestPersistence_WritesAuditRow",
	}

	// Snapshot audit row count before.
	writesBefore, err := p.store.ListWrites(ctx, audit.ListFilters{SessionID: "session-audit", Limit: 100})
	if err != nil {
		t.Fatalf("ListWrites before: %v", err)
	}

	// Save → should emit exactly one write_audit row.
	if err := p.Save(ctx, wc, "session-audit", StateDraftingSpec, EventSessionStart, VerdictUnknown, 0, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}

	writesAfter, err := p.store.ListWrites(ctx, audit.ListFilters{SessionID: "session-audit", Limit: 100})
	if err != nil {
		t.Fatalf("ListWrites after: %v", err)
	}
	if len(writesAfter)-len(writesBefore) != 1 {
		t.Errorf("expected 1 new audit row, got %d", len(writesAfter)-len(writesBefore))
	}

	// Upsert → another audit row.
	if err := p.Save(ctx, wc, "session-audit", StateSpecActive, EventVibePublish, VerdictUnknown, 1, ""); err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	writesAfter2, err := p.store.ListWrites(ctx, audit.ListFilters{SessionID: "session-audit", Limit: 100})
	if err != nil {
		t.Fatalf("ListWrites after2: %v", err)
	}
	if len(writesAfter2)-len(writesBefore) != 2 {
		t.Errorf("expected 2 audit rows total, got %d", len(writesAfter2)-len(writesBefore))
	}
}

// TestPersistence_CrossProjectIsolation verifies the bug-hunt fix (C1):
// vlp_state rows must be isolated per project (INV-7). Two projects can
// have rows with the same session_id without colliding on UNIQUE.
func TestPersistence_CrossProjectIsolation(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "test-cross.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Seed two projects.
	for _, pid := range []string{"default", "tenant-b"} {
		if err := s.CreateProject(ctx, &project.Project{ProjectID: pid, DisplayName: pid}); err != nil {
			t.Fatalf("create %s: %v", pid, err)
		}
	}

	wcA := store.WriteContext{ProjectID: "default", Actor: "a", WritePath: "T"}
	wcB := store.WriteContext{ProjectID: "tenant-b", Actor: "b", WritePath: "T"}

	p, err := NewPersistence(s)
	if err != nil {
		t.Fatalf("new persistence: %v", err)
	}

	// Save under project "default" with session_id "shared".
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set active default: %v", err)
	}
	if err := p.Save(ctx, wcA, "shared", StateIdle, EventSessionStart, VerdictUnknown, 0, ""); err != nil {
		t.Fatalf("Save A: %v", err)
	}

	// Switch to project "tenant-b" and save with the SAME session_id.
	if err := s.SetActiveProject(ctx, "tenant-b"); err != nil {
		t.Fatalf("set active tenant-b: %v", err)
	}
	if err := p.Save(ctx, wcB, "shared", StateDraftingSpec, EventVibePublish, VerdictUnknown, 0, ""); err != nil {
		t.Fatalf("Save B (same session_id, different project): %v", err)
	}

	// Load from tenant-b → should see drafting_spec (state B), not idle (state A).
	snapB, err := p.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}
	if !snapB.Exists {
		t.Fatal("B snapshot should exist")
	}
	if snapB.State != StateDraftingSpec {
		t.Errorf("B.State = %s, want drafting_spec (project B's value)", snapB.State)
	}

	// Switch back to default → should see idle (state A), not drafting_spec.
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set active default 2: %v", err)
	}
	snapA, err := p.Load(ctx, "shared")
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	if snapA.State != StateIdle {
		t.Errorf("A.State = %s, want idle (project A's value)", snapA.State)
	}

	// Sanity: ListByState from default returns A's row only.
	rows, err := p.ListByState(ctx, StateIdle, 0)
	if err != nil {
		t.Fatalf("ListByState idle: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("ListByState(Idle) from default = %d, want 1", len(rows))
	}
}

// TestNewPersistence_NilCheck verifies the constructor rejects nil store.
func TestNewPersistence_NilCheck(t *testing.T) {
	_, err := NewPersistence(nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}