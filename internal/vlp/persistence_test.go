// Tests for atomic spec 2.3 (VLPPersistence) — see docs/DMAP_V1_1.md §4 Layer 2.
//
// The acceptance test (TestVLP_PersistenceRoundTrip) is the ONE test that
// defines "done" for this spec: save a state, load it back, verify the
// typed State round-trips through the int column. Other tests cover edge
// cases (empty session_id, unknown state rejection, listing).
package vlp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
	return NewPersistence(s), func() { _ = s.Close() }
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
	rows, err := p.ListByState(ctx, "", 0)
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

// TestPersistence_Save_RejectsInvalidInputs covers input validation.
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

// TestPersistence_ListByState verifies filtering.
func TestPersistence_ListByState(t *testing.T) {
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

	// List drafting_spec — empty filter passes through, so we filter client-side.
	// In v2, ListByState will accept State enum directly. For now, filter by string.
	rows, err := p.ListByState(ctx, "", 0)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(rows) < 4 {
		t.Errorf("List all = %d rows, want >= 4", len(rows))
	}

	// Filter client-side to count by state
	countByState := map[string]int{}
	for _, r := range rows {
		countByState[State(r.State).String()]++
	}
	if countByState["drafting_spec"] != 2 {
		t.Errorf("drafting_spec count = %d, want 2", countByState["drafting_spec"])
	}
	if countByState["spec_active"] != 1 {
		t.Errorf("spec_active count = %d, want 1", countByState["spec_active"])
	}
	if countByState["complete"] != 1 {
		t.Errorf("complete count = %d, want 1", countByState["complete"])
	}
}

// TestPersistence_LimitRespected verifies the limit parameter on List.
func TestPersistence_LimitRespected(t *testing.T) {
	p, cleanup := newTestPersistence(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "limit-test", WritePath: "TestLimit"}

	// Seed 5 sessions
	for i := 0; i < 5; i++ {
		sessionID := "limit-" + StateDraftingSpec.String() + "-" + intToStr(i)
		if err := p.Save(ctx, wc, sessionID, StateDraftingSpec, EventSessionStart, VerdictUnknown, 0, ""); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	rows, err := p.ListByState(ctx, "", 2)
	if err != nil {
		t.Fatalf("List with limit: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("List(limit=2) = %d, want 2", len(rows))
	}
}

// intToStr is a small helper to avoid importing strconv just for tests.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}