// Package recall_test — assemble_drift_test.go: Phase 9 Chunk 8.6.
//
// # Goal
//
// Bring assemble.go DriftFrame (line 288) from 38.1% to ≥95% by
// covering the 4 remaining branches that no existing test exercises:
//
//   1. ListSDDEvaluations returns error → DriftFrame wraps + returns
//      (assemble.go line 299-306).
//   2. parseSDDVerdict fails on malformed verdict_json → DriftFrame
//      returns error (assemble.go line 311-315).
//   3. parseSDDVerdict succeeds but verdict is empty → zero-value
//      DriftFrame (assemble.go line 316-318).
//   4. verdict is "aligned" → full DriftFrame composed (assemble.go
//      line 324).
//   5. verdict is "drift_detected" with reasoning → pendingItems
//      populated (line 321-323).
//
// # Pre-existing coverage (do NOT regress)
//
//   - branch A: state == nil → zero-value (TestDriftFrame_NoState in
//     assemble_store_test.go:258).
//   - branch B: state.State == 0 → zero-value (TestDriftFrame_StateZero
//     in assemble_store_test.go:271).
//   - branch C: state non-zero but ListSDDEvaluations returns 0 rows
//     → zero-value (TestDriftFrame_NoEvaluations in
//     assemble_store_test.go:296).
//
// # Why this lives in a new file
//
// Chunk 8.6's contract: no production code changes (test-only). The
// new file groups all Chunk 8.6 DriftFrame coverage so a future
// reviewer can see the delta in one place. Existing
// assemble_store_test.go keeps its baseline role (Chunk 6.4 origin).
package recall_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newDriftTestStore opens a fresh SQLite store + creates the default
// project. Mirrors the newTestStore pattern from
// assemble_store_test.go.
func newDriftTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "drift-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	return st, func() { _ = st.Close() }
}

// newDriftSession inserts a minimal session row.
func newDriftSession(t *testing.T, st store.Store, operator string) string {
	t.Helper()
	id := "sess-drift-" + operator + "-" + time.Now().Format("150405.000000000")
	_, err := st.SaveSession(context.Background(), store.WriteContext{
		Actor:     operator,
		ProjectID: "default",
	}, &session.Session{
		SessionID:       id,
		Operator:        operator,
		Status:          "open",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ConstitutionID:  "dark-agents/dark-mem",
		ConstitutionVer: "1.0.0",
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	return id
}

// saveVLPState is a thin wrapper around SaveVLPState for the test
// setup. State=3 is the canonical "drafting_spec" VLP state
// (drafting → evaluations drift → cooldown_v1); ActiveDraft state
// means DriftFrame should consult sdd_evaluations.
func saveVLPState(t *testing.T, st store.Store, sessionID string, state int) {
	t.Helper()
	_, err := st.SaveVLPState(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&store.VLPStateRow{
			SessionID: sessionID,
			State:     state,
			ProjectID: "default",
		})
	if err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}
}

// saveSDDEval inserts one sdd_evaluations row. TargetID format must
// match DriftFrame's ListSDDEvaluations filter: "fmt.Sprintf("%d",
// vlpStateRow.ID)" — i.e. the VLP state row's numeric id. The test
// looks up the state id via GetVLPState after saveVLPState to wire
// this correctly.
func saveSDDEval(t *testing.T, st store.Store, vlpStateID int64, targetType, verdictJSON string) {
	t.Helper()
	_, err := st.SaveSDDEvaluation(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&ssd.SDDEvaluation{
			EvalType:    "drift_judge",
			TargetType:  targetType,
			TargetID:    formatTargetID(vlpStateID, targetType),
			VerdictJSON: verdictJSON,
			Confidence:  0.9,
			Model:       "test-model",
			CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		})
	if err != nil {
		t.Fatalf("SaveSDDEvaluation: %v", err)
	}
}

// formatTargetID mirrors DriftFrame's ListSDDEvaluations filter
// (line 301: TargetID: fmt.Sprintf("%d", state.ID), TargetType: "spec").
// DriftFrame's TargetType filter is hardcoded to "spec"; the TargetID
// must be the VLP state row's integer id formatted as a decimal string.
func formatTargetID(vlpStateID int64, targetType string) string {
	if targetType == "spec" {
		return fmt.Sprintf("%d", vlpStateID)
	}
	return ""
}

// getVLPStateID returns the row id of the VLP state for a session.
// Used to construct the target_id for sdd_evaluations rows.
func getVLPStateID(t *testing.T, st store.Store, sessionID string) int64 {
	t.Helper()
	state, err := st.GetVLPState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetVLPState: %v", err)
	}
	if state == nil {
		t.Fatalf("GetVLPState returned nil for session %q (SaveVLPState should have created it)", sessionID)
	}
	return state.ID
}

// === Test 1: ListSDDEvaluations returns error ==============================
//
// Branch: line 299-306. Triggered when the underlying Store cannot
// execute the SELECT. Easiest reliable trigger: close the Store
// before calling DriftFrame. ListSDDEvaluations errors with
// "sql: database is closed"; DriftFrame wraps it via
// fmt.Errorf("recall: DriftFrame ListSDDEvaluations: %w", err).

func TestDriftFrame_ListSDDEvaluationsError(t *testing.T) {
	st, cleanup := newDriftTestStore(t)
	defer cleanup()
	id := newDriftSession(t, st, "nico")
	saveVLPState(t, st, id, 3) // state != 0 → triggers the ListSDDEvaluations branch

	src := recall.NewStoreSource(st, nil)

	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := src.DriftFrame(context.Background(), id)
	if err == nil {
		t.Fatal("DriftFrame on closed store: expected error, got nil")
	}
}

// === Test 2: parseSDDVerdict returns error on malformed JSON ==============
//
// Branch: line 311-315. Writes an sdd_evaluations row with
// verdict_json = "{not json" (invalid syntax). parseSDDVerdict
// (assemble.go:408) returns error; DriftFrame wraps + returns.

func TestDriftFrame_ParseVerdictError(t *testing.T) {
	st, cleanup := newDriftTestStore(t)
	defer cleanup()
	id := newDriftSession(t, st, "nico")
	saveVLPState(t, st, id, 3)
	stateID := getVLPStateID(t, st, id)
	saveSDDEval(t, st, stateID, "spec", `{not valid json`)

	src := recall.NewStoreSource(st, nil)
	_, err := src.DriftFrame(context.Background(), id)
	if err == nil {
		t.Fatal("DriftFrame with malformed verdict_json: expected error, got nil")
	}
}

// === Test 3: parseSDDVerdict succeeds but verdict is empty =================
//
// Branch: line 316-318. verdict_json is valid JSON but the "verdict"
// field is empty. parseSDDVerdict returns ("", "", nil). DriftFrame
// returns a zero-value frame (state_id=0, last_verdict="",
// last_reconciled_at=zero).

func TestDriftFrame_EmptyVerdict(t *testing.T) {
	st, cleanup := newDriftTestStore(t)
	defer cleanup()
	id := newDriftSession(t, st, "nico")
	saveVLPState(t, st, id, 3)
	stateID := getVLPStateID(t, st, id)
	saveSDDEval(t, st, stateID, "spec", `{"verdict":"","reasoning":"no decision"}`)

	src := recall.NewStoreSource(st, nil)
	frame, err := src.DriftFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("DriftFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("DriftFrame returned nil for empty-verdict path")
	}
	if frame.LastVerdict != "" {
		t.Errorf("LastVerdict = %q; want empty (empty-verdict branch)", frame.LastVerdict)
	}
	if frame.SpecID != 0 {
		t.Errorf("SpecID = %d; want 0 (empty-verdict branch)", frame.SpecID)
	}
}

// === Test 4: aligned verdict → OpenSpecID=state.ID, LastVerdict="aligned" ==
//
// Branch: line 324 (full DriftFrame composition). verdict_json is
// `{"verdict":"aligned","reasoning":"ok"}`. DriftFrame returns a
// fully-populated frame.

func TestDriftFrame_AlignedVerdict(t *testing.T) {
	st, cleanup := newDriftTestStore(t)
	defer cleanup()
	id := newDriftSession(t, st, "nico")
	saveVLPState(t, st, id, 3)
	stateID := getVLPStateID(t, st, id)
	saveSDDEval(t, st, stateID, "spec",
		`{"verdict":"aligned","reasoning":"all checks passed"}`)

	src := recall.NewStoreSource(st, nil)
	frame, err := src.DriftFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("DriftFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("DriftFrame returned nil for aligned verdict")
	}
	if frame.LastVerdict != "aligned" {
		t.Errorf("LastVerdict = %q; want aligned", frame.LastVerdict)
	}
	if frame.SpecID != stateID {
		t.Errorf("SpecID = %d; want %d", frame.SpecID, stateID)
	}
	if frame.LastReconciledAt.IsZero() {
		t.Error("LastReconciledAt is zero; want non-zero (parsed from sdd_evaluations row)")
	}
}

// === Test 5: drift_detected + reasoning → pendingItems populated ==========
//
// Branch: line 321-323 (pendingItems construction). verdict_json has
// verdict="drift_detected" with a reasoning field. DriftFrame sets
// PendingItems to [truncate(reasoning, 200)] (line 322).

func TestDriftFrame_DriftDetected_WithPendingItems(t *testing.T) {
	st, cleanup := newDriftTestStore(t)
	defer cleanup()
	id := newDriftSession(t, st, "nico")
	saveVLPState(t, st, id, 3)
	stateID := getVLPStateID(t, st, id)
	reasoning := "spec drifted from dark-agents/dark-mem v1.0.0; Chunk 8.6 wording mismatch on line 12"
	saveSDDEval(t, st, stateID, "spec",
		`{"verdict":"drift_detected","reasoning":"`+reasoning+`"}`)

	src := recall.NewStoreSource(st, nil)
	frame, err := src.DriftFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("DriftFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("DriftFrame returned nil for drift_detected")
	}
	if frame.LastVerdict != "drift_detected" {
		t.Errorf("LastVerdict = %q; want drift_detected", frame.LastVerdict)
	}
	if len(frame.PendingItems) != 1 {
		t.Fatalf("PendingItems len = %d; want 1 (drift_detected + reasoning → 1 entry)", len(frame.PendingItems))
	}
	if frame.PendingItems[0] != reasoning {
		t.Errorf("PendingItems[0] = %q; want %q (verbatim reasoning)", frame.PendingItems[0], reasoning)
	}
}