// Package recall_test — assemble_final2_test.go: Phase 9 Chunk 8.6
// (last-mile coverage). Closes the last reachable branches in
// assemble.go:161 IdentityFrame (90% → ≥95%) and assemble.go:199
// ScopeFrame (90.9% → ≥95%).
//
// # Branches covered
//
//   - IdentityFrame (assemble.go:181-188): NewIdentityFrame returns
//     ErrEmptyOperator when sess.Operator == "". Reachable by
//     inserting a session with an empty Operator field.
//   - ScopeFrame (assemble.go:225-232): NewScopeFrame returns
//     ErrScopeVerdictUnknown when last_drift_verdict is non-empty and
//     not in {aligned, drift_detected, needs_human}. Reachable by
//     inserting a vlp_state row with an out-of-vocabulary verdict.

package recall_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newFinal2TestStore mirrors the SQLite-in-tempdir pattern from the
// other Chunk 6 test files.
func newFinal2TestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "final2-test.db"),
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

// saveSessionWithOperator inserts a session with explicit operator +
// constitution fields (used to drive empty-operator tests).
func saveSessionWithOperator(t *testing.T, st store.Store, operator, sessionID, cID, cVer string) {
	t.Helper()
	_, err := st.SaveSession(context.Background(),
		store.WriteContext{ProjectID: "default", Actor: "test"},
		&session.Session{
			SessionID:       sessionID,
			Operator:        operator,
			Status:          "open",
			StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
			ConstitutionID:  cID,
			ConstitutionVer: cVer,
		})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
}

// === Test 1: IdentityFrame empty Operator → NewIdentityFrame error =========
//
// Branch: assemble.go:181-188 NewIdentityFrame → ErrEmptyOperator.
// Reachable via session row with operator="".

func TestIdentityFrame_EmptyOperator_ReturnsError(t *testing.T) {
	st, cleanup := newFinal2TestStore(t)
	defer cleanup()

	id := "sess-identity-empty-op-" + time.Now().Format("150405.000000000")
	saveSessionWithOperator(t, st, "", id, "dark-agents/dark-mem", "1.0.0")

	src := recall.NewStoreSource(st, nil)
	frame, err := src.IdentityFrame(context.Background(), id)
	if err == nil {
		t.Fatal("IdentityFrame with empty Operator: expected error, got nil")
	}
	if frame != nil {
		t.Errorf("IdentityFrame returned non-nil on error; got %v", frame)
	}
}

// === Test 2: ScopeFrame out-of-vocabulary verdict → NewScopeFrame error ====
//
// Branch: assemble.go:225-232 NewScopeFrame → ErrScopeVerdictUnknown.
// Reachable via vlp_state row with LastVerdict="weird-string" (not in
// the canonical 3-verdict vocabulary).

func TestScopeFrame_UnknownVerdict_ReturnsError(t *testing.T) {
	st, cleanup := newFinal2TestStore(t)
	defer cleanup()

	id := "sess-scope-unknown-verdict-" + time.Now().Format("150405.000000000")
	saveSessionWithOperator(t, st, "nico", id, "dark-agents/dark-mem", "1.0.0")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.SaveVLPState(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&store.VLPStateRow{
			SessionID:   id,
			State:       3, // active
			LastVerdict: "frobnicated", // out-of-vocabulary
			UpdatedAt:   now,
			ProjectID:   "default",
		}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	src := recall.NewStoreSource(st, nil)
	frame, err := src.ScopeFrame(context.Background(), id)
	if err == nil {
		t.Fatal("ScopeFrame with unknown verdict: expected error, got nil")
	}
	if frame != nil {
		t.Errorf("ScopeFrame returned non-nil on error; got %v", frame)
	}
}

// === Test 3 (NOT REACHABLE): CapabilitiesFrame empty projectID ============
//
// Originally drafted to cover assemble.go:250's projectID = ActiveProject()
// branch when no project is set. NOT REACHABLE because SaveSession
// itself requires an active project ("store: workflow tool requires an
// active session: no active project — call SetActiveProject first"),
// so any reachable session legitimately has projectID non-empty.
//
// The CapabilitiesFrame 86.7% ceiling reflects this structural
// invariant: the empty-tool-name skip path (assemble.go:255-256) is
// dead code (DefaultToolGrants ends with a non-comma tool name, no
// trailing empty entry); and the projectID-empty branch is unreachable
// from above via the public API.
//
// Documented here so future reviewers don't re-attempt the same
// failed approach. A possible Chunk 8.7+ work item: a mock Store
// interface that lets tests drive CapabilitiesFrame without going
// through SaveSession's active-project precondition.