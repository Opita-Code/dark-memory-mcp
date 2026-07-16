// Tests for atomic spec 2.5 (VLPLoopUseCase) — see docs/DMAP_V1_1.md §4 Layer 2.
//
// The acceptance test (TestVLP_E2E_DraftToComplete) is the ONE test that
// defines "done" for this spec: it drives the full happy path
// (session_start → vibe_publish → artifact_log → drift_log(aligned))
// end-to-end through the UseCase, verifying state persistence, audit
// chain, and terminal semantics.
//
// Other tests cover:
//   - bootstrap enforcement (first event MUST be session_start)
//   - invalid transitions rejected
//   - drift loop (drift_detected → spec_active back)
//   - abort from non-terminal
//   - next-action hint mapping
//   - audit chain reconstruction (4 from→to tuples)
//   - constructor hygiene (nil checks)
//   - turn count monotonicity
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

// newTestUseCase opens an in-memory SQLite store, seeds the default
// project, and returns a UseCase + cleanup. Distinct from
// newTestPersistence / newTestAuditor because UseCase tests need the
// full driver wiring (Persistence + Auditor + UseCase).
func newTestUseCase(t *testing.T) (*UseCase, *Persistence, *Auditor, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "usecase-test.db"),
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
	p, err := NewPersistence(s)
	if err != nil {
		_ = s.Close()
		t.Fatalf("new persistence: %v", err)
	}
	a, err := NewAuditor(s)
	if err != nil {
		_ = s.Close()
		t.Fatalf("new auditor: %v", err)
	}
	uc, err := NewUseCase(p, a)
	if err != nil {
		_ = s.Close()
		t.Fatalf("new usecase: %v", err)
	}
	return uc, p, a, func() { _ = s.Close() }
}

// TestVLP_E2E_DraftToComplete is the ACCEPTANCE TEST for spec 2.5.
// Drives the full happy path: session_start → vibe_publish →
// artifact_log → drift_log(aligned). Verifies:
//   - every HandleEvent returns expected NewState
//   - final state is terminal (complete)
//   - persisted state matches the last NewState
//   - exactly 4 transitions were audited with correct from→to tuples
//   - turn count increments 1, 2, 3, 4
//   - next-action hint at each step matches the canonical mapping
func TestVLP_E2E_DraftToComplete(t *testing.T) {
	uc, p, a, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "e2e", SessionID: "e2e-test", WritePath: "TestVLP_E2E"}

	type step struct {
		event        Event
		verdict      Verdict
		wantState    State
		wantTurn     int
		wantNext     string
		wantTerminal bool
	}
	steps := []step{
		{EventSessionStart, VerdictUnknown, StateDraftingSpec, 1, "vibe_publish", false},
		{EventVibePublish, VerdictUnknown, StateSpecActive, 2, "artifact_log", false},
		{EventArtifactLog, VerdictUnknown, StateDriftJudging, 3, "drift_log", false},
		{EventDriftLog, VerdictAligned, StateComplete, 4, "", true},
	}
	for i, s := range steps {
		result, err := uc.HandleEvent(ctx, wc, "e2e-test", s.event, s.verdict, "")
		if err != nil {
			t.Fatalf("step %d (%s): %v", i, s.event, err)
		}
		if result.NewState != s.wantState {
			t.Errorf("step %d: NewState = %s, want %s", i, result.NewState, s.wantState)
		}
		if result.TurnCount != s.wantTurn {
			t.Errorf("step %d: TurnCount = %d, want %d", i, result.TurnCount, s.wantTurn)
		}
		if result.NextAction != s.wantNext {
			t.Errorf("step %d: NextAction = %q, want %q", i, result.NextAction, s.wantNext)
		}
		if result.IsTerminal != s.wantTerminal {
			t.Errorf("step %d: IsTerminal = %v, want %v", i, result.IsTerminal, s.wantTerminal)
		}
	}

	// Verify persisted state matches
	snap, err := p.Load(ctx, "e2e-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.State != StateComplete {
		t.Errorf("persisted state = %s, want complete", snap.State)
	}

	// Verify exactly 4 transition-level audit rows
	transitions, err := a.ListTransitionsForSession(ctx, "e2e-test", 0)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 4 {
		t.Fatalf("expected 4 transition audit rows, got %d", len(transitions))
	}

	// Verify from→to tuples (newest first → reverse order).
	// Note: State is encoded as int (matches type State int).
	wantFromTo := []struct{ from, to State }{
		{StateDriftJudging, StateComplete}, // newest
		{StateSpecActive, StateDriftJudging},
		{StateDraftingSpec, StateSpecActive},
		{StateIdle, StateDraftingSpec}, // oldest (bootstrap)
	}
	for i, want := range wantFromTo {
		var rec struct {
			From State `json:"from"`
			To   State `json:"to"`
		}
		if err := json.Unmarshal([]byte(transitions[i].Notes), &rec); err != nil {
			t.Errorf("transitions[%d] JSON parse: %v", i, err)
			continue
		}
		if rec.From != want.from || rec.To != want.to {
			t.Errorf("transitions[%d]: got %s→%s, want %s→%s", i, rec.From, rec.To, want.from, want.to)
		}
	}
}

// TestVLP_E2E_BootstrapRequiresSessionStart verifies that the first
// event for a new session MUST be EventSessionStart. Any other first
// event returns an error and does not persist any state.
func TestVLP_E2E_BootstrapRequiresSessionStart(t *testing.T) {
	uc, p, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "bootstrap", SessionID: "wrong-start", WritePath: "T"}

	bad := []Event{EventVibePublish, EventArtifactLog, EventDriftLog, EventAbort, EventUnknown}
	for _, e := range bad {
		_, err := uc.HandleEvent(ctx, wc, "wrong-start", e, VerdictUnknown, "")
		if err == nil {
			t.Errorf("first event = %s: expected error, got nil", e)
		}
	}

	// State should not have been persisted
	snap, err := p.Load(ctx, "wrong-start")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Exists {
		t.Error("state should NOT have been persisted after failed bootstrap")
	}
}

// TestVLP_E2E_RejectsInvalidTransitionAfterBootstrap verifies that
// after a session is initialized, calling with an invalid event for the
// current state returns ErrInvalidTransition from vlp.Transition.
func TestVLP_E2E_RejectsInvalidTransitionAfterBootstrap(t *testing.T) {
	uc, _, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "invalid", SessionID: "invalid-test", WritePath: "T"}

	// Bootstrap
	if _, err := uc.HandleEvent(ctx, wc, "invalid-test", EventSessionStart, VerdictUnknown, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Now in StateDraftingSpec. artifact_log is invalid (must be vibe_publish first).
	_, err := uc.HandleEvent(ctx, wc, "invalid-test", EventArtifactLog, VerdictUnknown, "")
	if err == nil {
		t.Fatal("expected error for invalid transition")
	}
}

// TestVLP_E2E_HandlesDriftLoop verifies the drift_detected → spec_active
// loop back: after a drift is detected, the spec_active state allows
// the loop to retry artifact_log → drift_log.
func TestVLP_E2E_HandlesDriftLoop(t *testing.T) {
	uc, p, a, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "drift-loop", SessionID: "drift-loop", WritePath: "T"}

	// session_start → vibe_publish → artifact_log → drift_log(drift_detected) → spec_active → artifact_log → drift_log(aligned) → complete
	events := []struct {
		event   Event
		verdict Verdict
	}{
		{EventSessionStart, VerdictUnknown},
		{EventVibePublish, VerdictUnknown},
		{EventArtifactLog, VerdictUnknown},
		{EventDriftLog, VerdictDriftDetected}, // loop back
		{EventArtifactLog, VerdictUnknown},
		{EventDriftLog, VerdictAligned}, // aligned → complete
	}
	for i, e := range events {
		if _, err := uc.HandleEvent(ctx, wc, "drift-loop", e.event, e.verdict, ""); err != nil {
			t.Fatalf("step %d (%s): %v", i, e.event, err)
		}
	}

	// Final state should be complete
	snap, err := p.Load(ctx, "drift-loop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.State != StateComplete {
		t.Errorf("persisted state = %s, want complete", snap.State)
	}
	if snap.TurnCount != 6 {
		t.Errorf("TurnCount = %d, want 6", snap.TurnCount)
	}

	// 6 transition audit rows (drift loop creates 2 spec_active→drift_judging transitions)
	transitions, err := a.ListTransitionsForSession(ctx, "drift-loop", 0)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 6 {
		t.Errorf("expected 6 audit rows, got %d", len(transitions))
	}
}

// TestVLP_E2E_HandlesAborted verifies EventAbort works from any
// non-terminal state and lands the session in StateAborted.
func TestVLP_E2E_HandlesAborted(t *testing.T) {
	uc, p, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "abort", SessionID: "abort-test", WritePath: "T"}

	// Bootstrap then abort
	if _, err := uc.HandleEvent(ctx, wc, "abort-test", EventSessionStart, VerdictUnknown, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	result, err := uc.HandleAbort(ctx, wc, "abort-test", "")
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if result.NewState != StateAborted {
		t.Errorf("NewState = %s, want aborted", result.NewState)
	}
	if !result.IsTerminal {
		t.Error("IsTerminal should be true for StateAborted")
	}
	if result.NextAction != "" {
		t.Errorf("NextAction = %q, want \"\" (terminal)", result.NextAction)
	}

	snap, err := p.Load(ctx, "abort-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.State != StateAborted {
		t.Errorf("persisted state = %s, want aborted", snap.State)
	}
}

// TestVLP_E2E_NextActionHints exhaustively verifies the state→next-event
// mapping for every state in the enum.
func TestVLP_E2E_NextActionHints(t *testing.T) {
	cases := []struct {
		state    State
		expected string
	}{
		{StateIdle, "session_start"},
		{StateDraftingSpec, "vibe_publish"},
		{StateSpecActive, "artifact_log"},
		{StateDriftJudging, "drift_log"},
		{StateComplete, ""},
		{StateNeedsHuman, ""},
		{StateAborted, ""},
	}
	for _, tc := range cases {
		got := nextEventNameFor(tc.state)
		if got != tc.expected {
			t.Errorf("nextEventNameFor(%s) = %q, want %q", tc.state, got, tc.expected)
		}
	}
}

// TestVLP_E2E_TurnCountMonotonic verifies turn count increments by 1 on
// every HandleEvent call, starting from 0 (virtual) for the first event.
func TestVLP_E2E_TurnCountMonotonic(t *testing.T) {
	uc, _, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "turns", SessionID: "turn-test", WritePath: "T"}

	for want := 1; want <= 4; want++ {
		var event Event
		var verdict Verdict
		switch want {
		case 1:
			event, verdict = EventSessionStart, VerdictUnknown
		case 2:
			event, verdict = EventVibePublish, VerdictUnknown
		case 3:
			event, verdict = EventArtifactLog, VerdictUnknown
		case 4:
			event, verdict = EventDriftLog, VerdictAligned
		}
		result, err := uc.HandleEvent(ctx, wc, "turn-test", event, verdict, "")
		if err != nil {
			t.Fatalf("turn %d: %v", want, err)
		}
		if result.TurnCount != want {
			t.Errorf("turn %d: TurnCount = %d, want %d", want, result.TurnCount, want)
		}
	}
}

// TestVLP_E2E_NeedsHuman verifies that VerdictNeedsHuman takes the loop
// to StateNeedsHuman, which is terminal. Operator intervention required.
func TestVLP_E2E_NeedsHuman(t *testing.T) {
	uc, p, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "needs_human", SessionID: "nh-test", WritePath: "T"}

	events := []struct {
		event   Event
		verdict Verdict
	}{
		{EventSessionStart, VerdictUnknown},
		{EventVibePublish, VerdictUnknown},
		{EventArtifactLog, VerdictUnknown},
		{EventDriftLog, VerdictNeedsHuman},
	}
	for _, e := range events {
		if _, err := uc.HandleEvent(ctx, wc, "nh-test", e.event, e.verdict, ""); err != nil {
			t.Fatalf("step (%s): %v", e.event, err)
		}
	}

	snap, err := p.Load(ctx, "nh-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.State != StateNeedsHuman {
		t.Errorf("persisted state = %s, want needs_human", snap.State)
	}
	if !snap.State.Terminal() {
		t.Error("StateNeedsHuman.Terminal() should be true")
	}

	// Calling HandleEvent again with any event from a terminal state
	// must return an error.
	_, err = uc.HandleEvent(ctx, wc, "nh-test", EventSessionStart, VerdictUnknown, "")
	if err == nil {
		t.Error("expected error when calling HandleEvent from terminal state")
	}
}

// TestVLP_E2E_AtomicSaveEmitsTwoAuditRows verifies the bonus atomic
// refactor: every HandleEvent now writes BOTH a row-level audit row
// (SaveVLPState) AND a transition-level audit row (vlp.transition) in
// the SAME DB transaction. After the acceptance test runs 4 events,
// we expect 4 row-level + 4 transition-level = 8 audit rows total,
// all with the same row_id (the upserted vlp_state row).
func TestVLP_E2E_AtomicSaveEmitsTwoAuditRows(t *testing.T) {
	uc, _, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "atomic", SessionID: "atomic-test", WritePath: "T"}

	seq := []struct {
		event   Event
		verdict Verdict
	}{
		{EventSessionStart, VerdictUnknown},
		{EventVibePublish, VerdictUnknown},
		{EventArtifactLog, VerdictUnknown},
		{EventDriftLog, VerdictAligned},
	}
	for _, s := range seq {
		if _, err := uc.HandleEvent(ctx, wc, "atomic-test", s.event, s.verdict, ""); err != nil {
			t.Fatalf("HandleEvent %s: %v", s.event, err)
		}
	}

	// Use the Auditor's ListTransitionsForSession to verify transition-level rows.
	transitions, err := uc.auditor.ListTransitionsForSession(ctx, "atomic-test", 0)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 4 {
		t.Errorf("transition-level audit rows = %d, want 4 (one per HandleEvent)", len(transitions))
	}
	for _, r := range transitions {
		if r.WritePath != "vlp.transition" {
			t.Errorf("unexpected WritePath %q in transitions list", r.WritePath)
		}
		if r.SessionID != "atomic-test" {
			t.Errorf("unexpected SessionID %q", r.SessionID)
		}
		if r.ProjectID == "" {
			t.Errorf("F33 fix: ProjectID should be populated, got empty")
		}
	}

	// Also verify row-level audit rows exist (one per HandleEvent via SaveVLPState).
	allWrites, err := uc.store.ListWrites(ctx, audit.ListFilters{SessionID: "atomic-test", Limit: 100})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	rowLevel := 0
	for _, r := range allWrites {
		if r.WritePath != "vlp.transition" {
			rowLevel++
		}
	}
	if rowLevel != 4 {
		t.Errorf("row-level audit rows = %d, want 4 (one per HandleEvent)", rowLevel)
	}
	if len(allWrites) != 8 {
		t.Errorf("total audit rows = %d, want 8 (4 row-level + 4 transition-level)", len(allWrites))
	}
}

// TestNewUseCase_NilChecks verifies the constructor rejects nil inputs.
func TestNewUseCase_NilChecks(t *testing.T) {
	_, err := NewUseCase(nil, nil)
	if err == nil {
		t.Error("nil persistence: expected error, got nil")
	}

	a, s, cleanup := newTestAuditor(t)
	defer cleanup()
	_ = a
	p, _ := NewPersistence(s)
	if p == nil {
		t.Fatal("NewPersistence returned nil without error")
	}

	_, err = NewUseCase(p, nil)
	if err == nil {
		t.Error("nil auditor: expected error, got nil")
	}
}

// TestVLP_E2E_RejectsEmptySessionID verifies input validation.
func TestVLP_E2E_RejectsEmptySessionID(t *testing.T) {
	uc, _, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{}

	_, err := uc.HandleEvent(ctx, wc, "", EventSessionStart, VerdictUnknown, "")
	if err == nil {
		t.Error("empty session_id: expected error, got nil")
	}
}

// TestVLP_E2E_RejectsEventUnknown verifies input validation.
func TestVLP_E2E_RejectsEventUnknown(t *testing.T) {
	uc, _, _, cleanup := newTestUseCase(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{SessionID: "u"}

	_, err := uc.HandleEvent(ctx, wc, "u", EventUnknown, VerdictUnknown, "")
	if err == nil {
		t.Error("EventUnknown: expected error, got nil")
	}
}