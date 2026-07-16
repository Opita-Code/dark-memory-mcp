// Package opencode — adapter_test.go: tests for the OpenCode adapter
// (atomic spec 6.1).
//
// Verifies the end-to-end wire-format integration: drive the VLP loop
// through the adapter's DriveSession entry point (same JSON envelope
// an MCP harness would send) and assert the state-machine response.
package opencode

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// newTestAdapter builds a sqlite-backed Store + UseCase + Adapter for
// testing. Returns the adapter and a cleanup func.
func newTestAdapter(t *testing.T) (*Adapter, store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "adapter-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("runtime.Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	p, err := vlp.NewPersistence(st)
	if err != nil {
		t.Fatalf("NewPersistence: %v", err)
	}
	a, err := vlp.NewAuditor(st)
	if err != nil {
		t.Fatalf("NewAuditor: %v", err)
	}
	uc, err := vlp.NewUseCase(p, a)
	if err != nil {
		t.Fatalf("NewUseCase: %v", err)
	}
	adapter, err := NewAdapter(uc)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	cleanup := func() { _ = st.Close() }
	return adapter, st, cleanup
}

// callDrive marshals a DriveRequest and calls DriveSession with the
// raw JSON envelope. Returns the response or *DriveError.
func callDrive(t *testing.T, a *Adapter, req DriveRequest) (*DriveResponse, *DriveError) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := a.DriveSession(context.Background(), raw)
	if err != nil {
		if de, ok := err.(*DriveError); ok {
			return nil, de
		}
		t.Fatalf("DriveSession: %v", err)
	}
	return resp, nil
}

// TestAdapter_EndToEnd is the ACCEPTANCE test for atomic spec 6.1:
// drive the VLP loop from session_start to complete through the
// OpenCode adapter, verifying the wire-format envelope works the
// same as the dark_memory_vlp_handle_event MCP tool would.
func TestAdapter_EndToEnd(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()

	steps := []struct {
		name      string
		req       DriveRequest
		wantState string
		wantNext  string
		wantTerm  bool
	}{
		{
			name:      "session_start",
			req:       DriveRequest{SessionID: "adapter-1", Event: "session_start"},
			wantState: "drafting_spec",
			wantNext:  "vibe_publish",
		},
		{
			name:      "vibe_publish",
			req:       DriveRequest{SessionID: "adapter-1", Event: "vibe_publish"},
			wantState: "spec_active",
			wantNext:  "artifact_log",
		},
		{
			name:      "artifact_log",
			req:       DriveRequest{SessionID: "adapter-1", Event: "artifact_log"},
			wantState: "drift_judging",
			wantNext:  "drift_log",
		},
		{
			name:      "drift_log_aligned",
			req:       DriveRequest{SessionID: "adapter-1", Event: "drift_log", Verdict: "aligned"},
			wantState: "complete",
			wantTerm:  true,
		},
	}

	for i, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			resp, derr := callDrive(t, a, step.req)
			if derr != nil {
				t.Fatalf("step %d (%s): drive error: %s: %s", i, step.name, derr.Code, derr.Message)
			}
			if resp.NewState != step.wantState {
				t.Errorf("step %d (%s): new_state want %q got %q", i, step.name, step.wantState, resp.NewState)
			}
			if resp.TurnCount != i+1 {
				t.Errorf("step %d (%s): turn_count want %d got %d", i, step.name, i+1, resp.TurnCount)
			}
			if resp.NextAction != step.wantNext {
				t.Errorf("step %d (%s): next_action want %q got %q", i, step.name, step.wantNext, resp.NextAction)
			}
			if resp.IsTerminal != step.wantTerm {
				t.Errorf("step %d (%s): is_terminal want %v got %v", i, step.name, step.wantTerm, resp.IsTerminal)
			}
		})
	}
}

// TestAdapter_RejectsInvalidTransition verifies the adapter surfaces
// vlp.ErrInvalidTransition as DriveError{Code: "ErrInvalidTransition"}.
func TestAdapter_RejectsInvalidTransition(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()

	// Get to spec_active, then fire drift_log (invalid: must be in
	// drift_judging first).
	for _, req := range []DriveRequest{
		{SessionID: "invalid-trans", Event: "session_start"},
		{SessionID: "invalid-trans", Event: "vibe_publish"},
	} {
		if _, derr := callDrive(t, a, req); derr != nil {
			t.Fatalf("setup: %v", derr)
		}
	}
	_, derr := callDrive(t, a, DriveRequest{
		SessionID: "invalid-trans", Event: "drift_log", Verdict: "aligned",
	})
	if derr == nil {
		t.Fatalf("expected error for out-of-order drift_log")
	}
	if derr.Code != "ErrInvalidTransition" {
		t.Errorf("code: want ErrInvalidTransition, got %q", derr.Code)
	}
}

// TestAdapter_RejectsMalformedJSON verifies the adapter rejects
// non-JSON input with a clear ErrInvalidArgument.
func TestAdapter_RejectsMalformedJSON(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()

	_, err := a.DriveSession(context.Background(), json.RawMessage(`{not valid`))
	if err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
	derr, ok := err.(*DriveError)
	if !ok {
		t.Fatalf("expected *DriveError, got %T", err)
	}
	if derr.Code != "ErrInvalidArgument" {
		t.Errorf("code: want ErrInvalidArgument, got %q", derr.Code)
	}
}

// TestAdapter_RejectsEmptySessionID verifies input validation at the
// adapter layer.
func TestAdapter_RejectsEmptySessionID(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()

	_, derr := callDrive(t, a, DriveRequest{SessionID: "", Event: "session_start"})
	if derr == nil {
		t.Fatalf("expected error for empty session_id")
	}
	if derr.Code != "ErrInvalidArgument" {
		t.Errorf("code: want ErrInvalidArgument, got %q", derr.Code)
	}
}

// TestAdapter_RejectsUnknownEvent verifies the wire-format translator
// catches unknown events before reaching UseCase.
func TestAdapter_RejectsUnknownEvent(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()

	_, derr := callDrive(t, a, DriveRequest{SessionID: "bad-event", Event: "not_an_event"})
	if derr == nil {
		t.Fatalf("expected error for unknown event")
	}
	if derr.Code != "ErrInvalidArgument" {
		t.Errorf("code: want ErrInvalidArgument, got %q", derr.Code)
	}
}

// TestNewAdapter_NilCheck verifies the constructor's defensive guard.
func TestNewAdapter_NilCheck(t *testing.T) {
	_, err := NewAdapter(nil)
	if err == nil {
		t.Errorf("NewAdapter(nil): expected error, got nil")
	}
}

// TestAdapter_DriftLoop verifies the drift_detected → spec_active loop
// path through the adapter (regeneration pattern).
func TestAdapter_DriftLoop(t *testing.T) {
	a, _, cleanup := newTestAdapter(t)
	defer cleanup()

	sid := "drift-loop"
	steps := []struct {
		req       DriveRequest
		wantState string
	}{
		{DriveRequest{SessionID: sid, Event: "session_start"}, "drafting_spec"},
		{DriveRequest{SessionID: sid, Event: "vibe_publish"}, "spec_active"},
		{DriveRequest{SessionID: sid, Event: "artifact_log"}, "drift_judging"},
		{DriveRequest{SessionID: sid, Event: "drift_log", Verdict: "drift_detected"}, "spec_active"},
		{DriveRequest{SessionID: sid, Event: "artifact_log"}, "drift_judging"},
		{DriveRequest{SessionID: sid, Event: "drift_log", Verdict: "aligned"}, "complete"},
	}
	for i, step := range steps {
		resp, derr := callDrive(t, a, step.req)
		if derr != nil {
			t.Fatalf("step %d: %v", i, derr)
		}
		if resp.NewState != step.wantState {
			t.Errorf("step %d: want %q, got %q", i, step.wantState, resp.NewState)
		}
	}
}
