// Tests for atomic spec 2.4 (VLPAuditor) — see docs/DMAP_V1_1.md §4 Layer 2.
//
// The acceptance test (TestVLPAuditor_AuditOnEachTransition) is the ONE
// test that defines "done" for this spec: every state transition emits
// exactly one transition-level audit row, with the correct from→to
// tuple, event, verdict, and turn embedded in the JSON notes payload.
//
// Other tests cover:
//   - input validation (empty session_id)
//   - nil constructor hygiene
//   - full happy path (7-state loop)
//   - ListTransitionsForSession with limit
//   - ListTransitionsForSession filter isolation (doesn't see other sessions' rows)
//   - verdict captured correctly for drift_log transitions
//   - row-level vs transition-level audit coexist (no cross-contamination)
package vlp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// newTestAuditor opens an in-memory SQLite store, seeds the default
// project, and returns an Auditor + cleanup. Distinct from
// newTestPersistence because Auditor tests need direct access to the
// underlying Store for ListWrites verification.
func newTestAuditor(t *testing.T) (*Auditor, store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "auditor-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		_ = s.Close()
		t.Fatalf("create default project: %v", err)
	}
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		_ = s.Close()
		t.Fatalf("set active project: %v", err)
	}
	a, err := NewAuditor(s)
	if err != nil {
		_ = s.Close()
		t.Fatalf("new auditor: %v", err)
	}
	return a, s, func() { _ = s.Close() }
}

// TestVLPAuditor_AuditOnEachTransition is the ACCEPTANCE TEST for spec 2.4.
// Records 4 transitions for a session, then lists them and verifies
// that every transition appears in the audit log with correct metadata.
func TestVLPAuditor_AuditOnEachTransition(t *testing.T) {
	a, _, cleanup := newTestAuditor(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{
		Actor:     "test-actor",
		SessionID: "session-audit",
		WritePath: "TestVLPAuditor_AuditOnEachTransition",
	}

	transitions := []TransitionRecord{
		{From: StateIdle, Event: EventSessionStart, Verdict: VerdictUnknown, To: StateDraftingSpec, Turn: 1},
		{From: StateDraftingSpec, Event: EventVibePublish, Verdict: VerdictUnknown, To: StateSpecActive, Turn: 2},
		{From: StateSpecActive, Event: EventArtifactLog, Verdict: VerdictUnknown, To: StateDriftJudging, Turn: 3},
		{From: StateDriftJudging, Event: EventDriftLog, Verdict: VerdictAligned, To: StateComplete, Turn: 4},
	}
	for _, tr := range transitions {
		if err := a.RecordTransition(ctx, wc, "session-audit", tr); err != nil {
			t.Fatalf("RecordTransition %+v: %v", tr, err)
		}
	}

	// List returns newest-first (write_audit ORDER BY id DESC).
	rows, err := a.ListTransitionsForSession(ctx, "session-audit", 0)
	if err != nil {
		t.Fatalf("ListTransitionsForSession: %v", err)
	}
	if len(rows) != len(transitions) {
		t.Fatalf("expected %d audit rows, got %d", len(transitions), len(rows))
	}

	// Verify each transition is recorded correctly (newest first).
	for i := range transitions {
		// rows[0] is the newest = last transition recorded.
		want := transitions[len(transitions)-1-i]
		got := rows[i]

		if got.SessionID != "session-audit" {
			t.Errorf("rows[%d].SessionID = %q, want session-audit", i, got.SessionID)
		}
		if got.TableName != "vlp_state" {
			t.Errorf("rows[%d].TableName = %q, want vlp_state", i, got.TableName)
		}
		if got.WritePath != "vlp.transition" {
			t.Errorf("rows[%d].WritePath = %q, want vlp.transition", i, got.WritePath)
		}
		if got.RowID != 0 {
			t.Errorf("rows[%d].RowID = %d, want 0 (transition-level has no specific row)", i, got.RowID)
		}

		// Parse the JSON notes to verify the transition record.
		var gotRec TransitionRecord
		if err := json.Unmarshal([]byte(got.Notes), &gotRec); err != nil {
			t.Errorf("rows[%d].Notes JSON parse: %v\n  notes=%s", i, err, got.Notes)
			continue
		}
		if gotRec.From != want.From {
			t.Errorf("rows[%d].From = %s, want %s", i, gotRec.From, want.From)
		}
		if gotRec.Event != want.Event {
			t.Errorf("rows[%d].Event = %s, want %s", i, gotRec.Event, want.Event)
		}
		if gotRec.Verdict != want.Verdict {
			t.Errorf("rows[%d].Verdict = %s, want %s", i, gotRec.Verdict, want.Verdict)
		}
		if gotRec.To != want.To {
			t.Errorf("rows[%d].To = %s, want %s", i, gotRec.To, want.To)
		}
		if gotRec.Turn != want.Turn {
			t.Errorf("rows[%d].Turn = %d, want %d", i, gotRec.Turn, want.Turn)
		}
	}
}

// TestVLPAuditor_RejectsInvalidInputs covers empty session_id.
func TestVLPAuditor_RejectsInvalidInputs(t *testing.T) {
	a, _, cleanup := newTestAuditor(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"RecordTransition empty session_id", func() error {
			return a.RecordTransition(ctx, wc, "", TransitionRecord{})
		}},
		{"ListTransitionsForSession empty session_id", func() error {
			_, err := a.ListTransitionsForSession(ctx, "", 10)
			return err
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

// TestVLPAuditor_FullHappyPath exercises every event in the state machine
// (10 transitions) and verifies all appear in the audit log. Mirrors
// TestTransition_AllValidPaths from spec 2.1 to ensure the audit layer
// captures the full transition space.
func TestVLPAuditor_FullHappyPath(t *testing.T) {
	a, _, cleanup := newTestAuditor(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "happy", SessionID: "happy-path", WritePath: "TestHappyPath"}

	// Walk the entire transition table (10 rows).
	allTransitions := []TransitionRecord{
		{From: StateIdle, Event: EventSessionStart, To: StateDraftingSpec, Turn: 1},
		{From: StateIdle, Event: EventAbort, To: StateAborted, Turn: 1},
		{From: StateDraftingSpec, Event: EventVibePublish, To: StateSpecActive, Turn: 2},
		{From: StateDraftingSpec, Event: EventAbort, To: StateAborted, Turn: 2},
		{From: StateSpecActive, Event: EventArtifactLog, To: StateDriftJudging, Turn: 3},
		{From: StateSpecActive, Event: EventAbort, To: StateAborted, Turn: 3},
		{From: StateDriftJudging, Event: EventDriftLog, Verdict: VerdictAligned, To: StateComplete, Turn: 4},
		{From: StateDriftJudging, Event: EventDriftLog, Verdict: VerdictDriftDetected, To: StateSpecActive, Turn: 4},
		{From: StateDriftJudging, Event: EventDriftLog, Verdict: VerdictNeedsHuman, To: StateNeedsHuman, Turn: 4},
		{From: StateDriftJudging, Event: EventAbort, To: StateAborted, Turn: 4},
	}
	for _, tr := range allTransitions {
		if err := a.RecordTransition(ctx, wc, "happy-path", tr); err != nil {
			t.Fatalf("RecordTransition %+v: %v", tr, err)
		}
	}

	rows, err := a.ListTransitionsForSession(ctx, "happy-path", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != len(allTransitions) {
		t.Errorf("expected %d audit rows, got %d", len(allTransitions), len(rows))
	}
}

// TestVLPAuditor_ListWithLimit verifies the limit parameter.
func TestVLPAuditor_ListWithLimit(t *testing.T) {
	a, _, cleanup := newTestAuditor(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "limit", SessionID: "limit-test", WritePath: "TestLimit"}

	for i := 1; i <= 5; i++ {
		if err := a.RecordTransition(ctx, wc, "limit-test", TransitionRecord{
			From: StateIdle, Event: EventSessionStart, To: StateDraftingSpec, Turn: i,
		}); err != nil {
			t.Fatalf("RecordTransition %d: %v", i, err)
		}
	}

	rows, err := a.ListTransitionsForSession(ctx, "limit-test", 2)
	if err != nil {
		t.Fatalf("List limited: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("ListTransitionsForSession(limit=2) = %d, want 2", len(rows))
	}
}

// TestVLPAuditor_ListFiltersBySession verifies isolation: rows from one
// session are not returned when listing another session's transitions.
func TestVLPAuditor_ListFiltersBySession(t *testing.T) {
	a, _, cleanup := newTestAuditor(t)
	defer cleanup()
	ctx := context.Background()
	wcA := store.WriteContext{Actor: "a", SessionID: "session-A", WritePath: "T"}
	wcB := store.WriteContext{Actor: "b", SessionID: "session-B", WritePath: "T"}

	// 3 transitions for session A
	for i := 1; i <= 3; i++ {
		if err := a.RecordTransition(ctx, wcA, "session-A", TransitionRecord{
			From: StateIdle, Event: EventSessionStart, To: StateDraftingSpec, Turn: i,
		}); err != nil {
			t.Fatalf("RecordTransition A %d: %v", i, err)
		}
	}
	// 2 transitions for session B
	for i := 1; i <= 2; i++ {
		if err := a.RecordTransition(ctx, wcB, "session-B", TransitionRecord{
			From: StateIdle, Event: EventSessionStart, To: StateDraftingSpec, Turn: i,
		}); err != nil {
			t.Fatalf("RecordTransition B %d: %v", i, err)
		}
	}

	rowsA, err := a.ListTransitionsForSession(ctx, "session-A", 0)
	if err != nil {
		t.Fatalf("List A: %v", err)
	}
	if len(rowsA) != 3 {
		t.Errorf("ListTransitionsForSession(A) = %d, want 3", len(rowsA))
	}
	for _, r := range rowsA {
		if r.SessionID != "session-A" {
			t.Errorf("rows leak: got SessionID=%q in A's list", r.SessionID)
		}
	}

	rowsB, err := a.ListTransitionsForSession(ctx, "session-B", 0)
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(rowsB) != 2 {
		t.Errorf("ListTransitionsForSession(B) = %d, want 2", len(rowsB))
	}
}

// TestVLPAuditor_VerdictCapturedInJSON verifies that Verdict is embedded
// in the notes JSON for drift_log transitions and OMITTED for non-drift
// events (saves space, matches the omitempty tag).
func TestVLPAuditor_VerdictCapturedInJSON(t *testing.T) {
	a, _, cleanup := newTestAuditor(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "v", SessionID: "v-test", WritePath: "T"}

	// Non-drift event: VerdictUnknown → JSON should NOT include "verdict" key.
	if err := a.RecordTransition(ctx, wc, "v-test", TransitionRecord{
		From: StateIdle, Event: EventSessionStart, Verdict: VerdictUnknown, To: StateDraftingSpec, Turn: 1,
	}); err != nil {
		t.Fatalf("RecordTransition non-drift: %v", err)
	}

	// Drift event with verdict: JSON SHOULD include "verdict" key.
	if err := a.RecordTransition(ctx, wc, "v-test", TransitionRecord{
		From: StateDriftJudging, Event: EventDriftLog, Verdict: VerdictDriftDetected, To: StateSpecActive, Turn: 2,
	}); err != nil {
		t.Fatalf("RecordTransition drift: %v", err)
	}

	rows, err := a.ListTransitionsForSession(ctx, "v-test", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	// rows[0] is newest (drift_log), rows[1] is non-drift.
	// Note: Verdict is encoded as int (since type Verdict int).
	var driftRec struct {
		Verdict Verdict `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(rows[0].Notes), &driftRec); err != nil {
		t.Fatalf("drift JSON parse: %v\n  notes=%s", err, rows[0].Notes)
	}
	if driftRec.Verdict != VerdictDriftDetected {
		t.Errorf("drift verdict = %d, want %d (drift_detected)", driftRec.Verdict, VerdictDriftDetected)
	}

	var nonDriftRec struct {
		Verdict Verdict `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(rows[1].Notes), &nonDriftRec); err != nil {
		t.Fatalf("non-drift JSON parse: %v\n  notes=%s", err, rows[1].Notes)
	}
	if nonDriftRec.Verdict != VerdictUnknown {
		t.Errorf("non-drift verdict = %d, want %d (VerdictUnknown)", nonDriftRec.Verdict, VerdictUnknown)
	}

	// Confirm the raw JSON actually omits the verdict key for non-drift
	// (omitempty on the JSON tag).
	var rawMap map[string]any
	if err := json.Unmarshal([]byte(rows[1].Notes), &rawMap); err != nil {
		t.Fatalf("non-drift rawMap parse: %v", err)
	}
	if _, hasVerdict := rawMap["verdict"]; hasVerdict {
		t.Errorf("non-drift JSON has 'verdict' key; omitempty failed (notes=%s)", rows[1].Notes)
	}
}

// TestVLPAuditor_NoCrossContaminationWithRowLevel verifies that the
// transition-level audit (write_path="vlp.transition") is distinct from
// the row-level audit emitted by SaveVLPState (write_path="SaveVLPState").
// Recording a transition should NOT produce a row with write_path=
// "SaveVLPState" and vice versa.
func TestVLPAuditor_NoCrossContaminationWithRowLevel(t *testing.T) {
	a, s, cleanup := newTestAuditor(t)
	defer cleanup()
	ctx := context.Background()

	wc := store.WriteContext{
		Actor:     "audit-test",
		SessionID: "shared",
		WritePath: "TestNoCrossContam",
	}

	// 1. Record a transition
	if err := a.RecordTransition(ctx, wc, "shared", TransitionRecord{
		From: StateIdle, Event: EventSessionStart, To: StateDraftingSpec, Turn: 1,
	}); err != nil {
		t.Fatalf("RecordTransition: %v", err)
	}

	// 2. Save a vlp_state row (emits row-level audit). The row-level audit
//    inherits the user's wc.WritePath verbatim (we tested that in 2.3).
//    Here we leave wc.WritePath empty so SaveVLPState's defensive fallback
//    kicks in ("SaveVLPState") — that makes the row-level audit
//    distinguishable from the transition-level "vlp.transition".
	wcRow := store.WriteContext{
		Actor:     "audit-test",
		SessionID: "shared",
		WritePath: "", // empty → SaveVLPState defaults to "SaveVLPState"
	}
	if _, err := s.SaveVLPState(ctx, wcRow, &store.VLPStateRow{
		SessionID: "shared",
		State:     int(StateDraftingSpec),
		LastEvent: "session_start",
		TurnCount: 1,
	}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	// 3. Verify the two audit layers coexist (transition-level + row-level).

	transitions, err := a.ListTransitionsForSession(ctx, "shared", 0)
	if err != nil {
		t.Fatalf("ListTransitionsForSession: %v", err)
	}
	if len(transitions) != 1 {
		t.Errorf("transition-level rows = %d, want 1", len(transitions))
	}
	if len(transitions) > 0 && transitions[0].WritePath != "vlp.transition" {
		t.Errorf("transition WritePath = %q, want vlp.transition", transitions[0].WritePath)
	}

	// Total writes for the session should be 2: 1 transition + 1 row-level.
	rowLevel, err := s.ListWrites(ctx, writeFiltersFor(wcRow))
	if err != nil {
		t.Fatalf("ListWrites all: %v", err)
	}
	counts := map[string]int{}
	for _, r := range rowLevel {
		if r.SessionID == "shared" {
			counts[r.WritePath]++
		}
	}
	if counts["vlp.transition"] != 1 {
		t.Errorf("transition-level audit rows = %d, want 1", counts["vlp.transition"])
	}
	if counts["SaveVLPState"] != 1 {
		t.Errorf("row-level audit rows = %d, want 1 (got %v)", counts["SaveVLPState"], counts)
	}
}

// writeFiltersFor returns audit.ListFilters that scope to a session + actor
// for cross-contamination tests.
func writeFiltersFor(wc store.WriteContext) audit.ListFilters {
	return audit.ListFilters{
		SessionID: wc.SessionID,
		Limit:     100,
	}
}

// TestNewAuditor_NilCheck verifies the constructor rejects nil store.
func TestNewAuditor_NilCheck(t *testing.T) {
	_, err := NewAuditor(nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}