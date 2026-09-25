package session_test

// L2 example tests for session.Store.
//
// Each test names a specific scenario that must hold:
//   - Start returns a session with the operator/project_id set
//   - Heartbeat refreshes last_heartbeat_at
//   - Close returns Summary and is terminal
//   - INV-7: cross-project reads fail

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
)

// newTestStore wires up a Store backed by in-memory SQLite with both
// audit and session schemas applied. Cleanup is registered.
func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	return newTestStoreAny(t)
}

// TestExample_Start_ReturnsSessionWithOperatorAndProject — Start
// returns a *Session whose Operator and ProjectID match the inputs.
func TestExample_Start_ReturnsSessionWithOperatorAndProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess, err := s.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.Operator != "operator-nico" {
		t.Errorf("Operator: expected operator-nico, got %q", sess.Operator)
	}
	if sess.ProjectID != "proj-huila" {
		t.Errorf("ProjectID: expected proj-huila, got %q", sess.ProjectID)
	}
	if sess.Status != session.StatusOpen {
		t.Errorf("Status: expected %q, got %q", session.StatusOpen, sess.Status)
	}
	if sess.ID == "" {
		t.Error("ID must be non-empty (A14 defense)")
	}
}

// TestExample_Heartbeat_RefreshesLastHeartbeatAt — Heartbeat updates
// the timestamp strictly after the original started_at.
func TestExample_Heartbeat_RefreshesLastHeartbeatAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess, err := s.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Force a measurable gap (SQLite CURRENT_TIMESTAMP is second-granularity;
	// we sleep just enough to ensure ordering).
	timeSleepMS(t, 1100)

	if err := s.Heartbeat(ctx, sess.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	got, err := s.Read(ctx, sess.ID, "proj-huila")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.LastHeartbeatAt.After(sess.StartedAt) {
		t.Fatalf("LastHeartbeatAt must advance after Heartbeat: started=%v, hb=%v",
			sess.StartedAt, got.LastHeartbeatAt)
	}
}

// TestExample_Close_ReturnsSummaryAndIsTerminal — Close returns a
// Summary and the session becomes closed (cannot be heartbeated).
func TestExample_Close_ReturnsSummaryAndIsTerminal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess, err := s.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Heartbeat(ctx, sess.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	summary, err := s.Close(ctx, sess.ID, true)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if summary == nil {
		t.Fatal("Close returned nil summary")
	}

	// Heartbeat on a closed session must fail.
	if err := s.Heartbeat(ctx, sess.ID); err == nil {
		t.Fatal("Heartbeat on closed session: expected error, got nil")
	}
}

// TestExample_Read_CrossProjectRejected_INV7 — INV-7: a session
// started in project P1 cannot be read by an operator in project P2.
// Read takes (sessionID, projectID); mismatch yields ErrProjectMismatch.
func TestExample_Read_CrossProjectRejected_INV7(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess, err := s.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = s.Read(ctx, sess.ID, "proj-other")
	if err == nil {
		t.Fatal("cross-project Read: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "project") {
		t.Fatalf("error must mention 'project', got: %v", err)
	}

	// Sanity: read in correct project still works.
	if _, err := s.Read(ctx, sess.ID, "proj-huila"); err != nil {
		t.Fatalf("same-project Read: unexpected error %v", err)
	}
}

// TestExample_Start_EmptyOperatorRejected — defensive: empty
// operator must fail at Start, not silently create an orphan session.
func TestExample_Start_EmptyOperatorRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Start(ctx, "", "proj-huila")
	if err == nil {
		t.Fatal("Start with empty operator: expected error, got nil")
	}
}

// timeSleepMS sleeps for n milliseconds (test-only; not exported).
func timeSleepMS(t *testing.T, ms int) {
	t.Helper()
	timeSleep(ms)
}
