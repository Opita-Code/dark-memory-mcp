// Package tools — vlp_test.go: tests for atomic spec 6.1 (L6 VLP wire tool).
//
// Tests the dark_memory_vlp_handle_event MCP tool directly through the
// Registry (no stdio transport). Sets up a fresh sqlite-backed Store in
// a temp dir, constructs Persistence + Auditor + UseCase, registers the
// tool, and exercises the handler with raw JSON envelopes matching what
// an MCP harness would send on the wire.
package tools

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
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// newVLPTestSetup builds a sqlite-backed Store + Persistence + Auditor +
// UseCase + Registry with the L6 VLP wire tool registered. Returns the
// registry, store (for cross-checks), and a cleanup func.
//
// Each test gets a fresh temp DB so they're independent.
func newVLPTestSetup(t *testing.T) (*Registry, store.Store, *vlp.UseCase, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "vlp-test.db"),
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

	reg := NewRegistry()
	RegisterVLP(reg, uc)

	cleanup := func() { _ = st.Close() }
	return reg, st, uc, cleanup
}

// callVLP invokes the dark_memory_vlp_handle_event tool with raw JSON
// matching what an MCP harness would send. Returns the typed result or
// error envelope.
func callVLP(t *testing.T, reg *Registry, args map[string]any) (*VLPHandleEventResult, *ToolError) {
	t.Helper()
	tool := reg.Get("vlp_handle_event")
	if tool == nil {
		t.Fatalf("vlp_handle_event not registered")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	resp, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	out, ok := resp.Data.(*VLPHandleEventResult)
	if !ok {
		t.Fatalf("response.Data not *VLPHandleEventResult: %T", resp.Data)
	}
	return out, nil
}

// TestVLPHandleEventTool_BootToComplete is the ACCEPTANCE test for
// atomic spec 6.1: drive the VLP loop from session_start through
// drift_log+aligned and assert the state-machine progression + audit.
func TestVLPHandleEventTool_BootToComplete(t *testing.T) {
	reg, st, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	// Sequence: idle → drafting_spec → spec_active → drift_judging → complete
	steps := []struct {
		name      string
		args      map[string]any
		wantState string
		wantNext  string
		wantTerm  bool
	}{
		{
			name:      "session_start",
			args:      map[string]any{"session_id": "boot-1", "event": "session_start"},
			wantState: "drafting_spec",
			wantNext:  "vibe_publish",
		},
		{
			name:      "vibe_publish",
			args:      map[string]any{"session_id": "boot-1", "event": "vibe_publish"},
			wantState: "spec_active",
			wantNext:  "artifact_log",
		},
		{
			name:      "artifact_log",
			args:      map[string]any{"session_id": "boot-1", "event": "artifact_log"},
			wantState: "drift_judging",
			wantNext:  "drift_log",
		},
		{
			name:      "drift_log_aligned",
			args:      map[string]any{"session_id": "boot-1", "event": "drift_log", "verdict": "aligned"},
			wantState: "complete",
			wantTerm:  true,
		},
	}

	for i, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			res, terr := callVLP(t, reg, step.args)
			if terr != nil {
				t.Fatalf("step %d (%s): tool error: %s: %s", i, step.name, terr.Code, terr.Message)
			}
			if res.NewState != step.wantState {
				t.Errorf("step %d (%s): new_state want %q got %q", i, step.name, step.wantState, res.NewState)
			}
			if res.TurnCount != i+1 {
				t.Errorf("step %d (%s): turn_count want %d got %d", i, step.name, i+1, res.TurnCount)
			}
			if res.NextAction != step.wantNext {
				t.Errorf("step %d (%s): next_action want %q got %q", i, step.name, step.wantNext, res.NextAction)
			}
			if res.IsTerminal != step.wantTerm {
				t.Errorf("step %d (%s): is_terminal want %v got %v", i, step.name, step.wantTerm, res.IsTerminal)
			}
		})
	}

	// Verify the audit trail: 4 HandleEvents should produce 8 audit rows
	// (4 row-level + 4 transition-level) per F32+F33+bonus hardening.
	ctx := context.Background()
	writes, err := st.ListWrites(ctx, audit.ListFilters{
		SessionID: "boot-1",
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	if len(writes) != 8 {
		t.Errorf("audit rows for boot-1: want 8 (4 row-level + 4 transition-level), got %d", len(writes))
	}
	transitionRows := 0
	for _, w := range writes {
		if w.WritePath == "vlp.transition" {
			transitionRows++
		}
	}
	if transitionRows != 4 {
		t.Errorf("transition-level audit rows: want 4, got %d", transitionRows)
	}
}

// TestVLPHandleEventTool_RejectsInvalidTransition verifies that firing
// an event out of order returns ErrInvalidTransition (NOT ErrInternal —
// it's an expected runtime condition).
func TestVLPHandleEventTool_RejectsInvalidTransition(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	// Drive to spec_active (no drift_judging yet).
	sid := "out-of-order"
	for _, args := range []map[string]any{
		{"session_id": sid, "event": "session_start"},
		{"session_id": sid, "event": "vibe_publish"},
	} {
		if _, terr := callVLP(t, reg, args); terr != nil {
			t.Fatalf("setup: %v", terr)
		}
	}
	// Now fire drift_log while in spec_active — NOT a valid transition
	// (must be in drift_judging for drift_log). UseCase returns
	// vlp.ErrInvalidTransition which the wire tool surfaces as
	// ErrInvalidTransition (not ErrInternal).
	_, terr := callVLP(t, reg, map[string]any{
		"session_id": sid,
		"event":      "drift_log",
		"verdict":    "aligned",
	})
	if terr == nil {
		t.Fatalf("expected error for out-of-order event, got nil")
	}
	if terr.Code != "ErrInvalidTransition" {
		t.Errorf("error code: want ErrInvalidTransition, got %q", terr.Code)
	}
	if terr.Hint == "" {
		t.Errorf("expected non-empty Hint for ErrInvalidTransition")
	}
}

// TestVLPHandleEventTool_RejectsUnknownEvent verifies that an unknown
// event string returns ErrInvalidArgument with a helpful Hint.
func TestVLPHandleEventTool_RejectsUnknownEvent(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	_, terr := callVLP(t, reg, map[string]any{
		"session_id": "bad-event",
		"event":      "not_a_real_event",
	})
	if terr == nil {
		t.Fatalf("expected error for unknown event, got nil")
	}
	if terr.Code != "ErrInvalidArgument" {
		t.Errorf("error code: want ErrInvalidArgument, got %q", terr.Code)
	}
	if terr.Hint == "" {
		t.Errorf("expected non-empty Hint for invalid argument")
	}
}

// TestVLPHandleEventTool_RejectsUnknownVerdict verifies that an unknown
// verdict returns ErrInvalidArgument.
func TestVLPHandleEventTool_RejectsUnknownVerdict(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	// Need to drive to drift_judging first so the verdict is consumed.
	steps := []map[string]any{
		{"session_id": "bad-verdict", "event": "session_start"},
		{"session_id": "bad-verdict", "event": "vibe_publish"},
		{"session_id": "bad-verdict", "event": "artifact_log"},
	}
	for _, s := range steps {
		if _, terr := callVLP(t, reg, s); terr != nil {
			t.Fatalf("setup step: %v", terr)
		}
	}
	// Now drift_log with a bad verdict.
	_, terr := callVLP(t, reg, map[string]any{
		"session_id": "bad-verdict",
		"event":      "drift_log",
		"verdict":    "not_a_verdict",
	})
	if terr == nil {
		t.Fatalf("expected error for unknown verdict, got nil")
	}
	if terr.Code != "ErrInvalidArgument" {
		t.Errorf("error code: want ErrInvalidArgument, got %q", terr.Code)
	}
}

// TestVLPHandleEventTool_RejectsVerdictOnNonDriftLog verifies that
// passing verdict with a non-drift_log event surfaces as ErrInvalidArgument
// (HIGH #1 bug-hunt fix — vlp.Transition now wraps store.ErrInvalidArgument).
func TestVLPHandleEventTool_RejectsVerdictOnNonDriftLog(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	// Drift verdict on session_start (non-drift_log event) — must reject.
	_, terr := callVLP(t, reg, map[string]any{
		"session_id": "verdict-on-non-drift",
		"event":      "session_start",
		"verdict":    "aligned",
	})
	if terr == nil {
		t.Fatalf("expected error for verdict on non-drift_log event, got nil")
	}
	if terr.Code != "ErrInvalidArgument" {
		t.Errorf("code: want ErrInvalidArgument, got %q", terr.Code)
	}
}

// TestVLPHandleEventTool_NextActionIncludesSessionID verifies HIGH #2 fix:
// Next.Args must include session_id so the harness can use the hint literally.
func TestVLPHandleEventTool_NextActionIncludesSessionID(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	sid := "next-action-test"
	tool := reg.Get("vlp_handle_event")
	raw, _ := json.Marshal(map[string]any{"session_id": sid, "event": "session_start"})
	resp, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if resp.Next == nil {
		t.Fatalf("expected Next action hint after session_start")
	}
	if got := resp.Next.Args["session_id"]; got != sid {
		t.Errorf("Next.Args.session_id: want %q, got %v", sid, got)
	}
	if got := resp.Next.Args["event"]; got != "vibe_publish" {
		t.Errorf("Next.Args.event: want vibe_publish, got %v", got)
	}
	if resp.Next.Tool != "vlp_handle_event" {
		t.Errorf("Next.Tool: want vlp_handle_event, got %q", resp.Next.Tool)
	}
}

// TestVLPHandleEventTool_NeedsHumanTerminal verifies the needs_human
// terminal path (regression for MEDIUM #2a — only complete was tested).
func TestVLPHandleEventTool_NeedsHumanTerminal(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	sid := "needs-human-terminal"
	steps := []map[string]any{
		{"session_id": sid, "event": "session_start"},
		{"session_id": sid, "event": "vibe_publish"},
		{"session_id": sid, "event": "artifact_log"},
		{"session_id": sid, "event": "drift_log", "verdict": "needs_human"},
	}
	expectedStates := []string{"drafting_spec", "spec_active", "drift_judging", "needs_human"}
	for i, s := range steps {
		res, terr := callVLP(t, reg, s)
		if terr != nil {
			t.Fatalf("step %d: %v", i, terr)
		}
		if res.NewState != expectedStates[i] {
			t.Errorf("step %d: want %q, got %q", i, expectedStates[i], res.NewState)
		}
		if i == 3 {
			if !res.IsTerminal {
				t.Errorf("needs_human: want IsTerminal=true, got false")
			}
			if res.NextAction != "" {
				t.Errorf("needs_human: want NextAction=\"\", got %q", res.NextAction)
			}
		}
	}
}

// TestVLPHandleEventTool_AbortedTerminal verifies the aborted terminal path.
func TestVLPHandleEventTool_AbortedTerminal(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	sid := "aborted-terminal"
	// abort from drafting_spec (mid-loop abort path)
	steps := []map[string]any{
		{"session_id": sid, "event": "session_start"},
		{"session_id": sid, "event": "abort"},
	}
	expectedStates := []string{"drafting_spec", "aborted"}
	for i, s := range steps {
		res, terr := callVLP(t, reg, s)
		if terr != nil {
			t.Fatalf("step %d: %v", i, terr)
		}
		if res.NewState != expectedStates[i] {
			t.Errorf("step %d: want %q, got %q", i, expectedStates[i], res.NewState)
		}
		if i == 1 {
			if !res.IsTerminal {
				t.Errorf("aborted: want IsTerminal=true, got false")
			}
			if res.NextAction != "" {
				t.Errorf("aborted: want NextAction=\"\", got %q", res.NextAction)
			}
		}
	}
}

// TestVLPHandleEventTool_RejectsEmptySessionID verifies input validation.
func TestVLPHandleEventTool_RejectsEmptySessionID(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	_, terr := callVLP(t, reg, map[string]any{
		"session_id": "",
		"event":      "session_start",
	})
	if terr == nil {
		t.Fatalf("expected error for empty session_id, got nil")
	}
	if terr.Code != "ErrInvalidArgument" {
		t.Errorf("error code: want ErrInvalidArgument, got %q", terr.Code)
	}
}

// TestVLPHandleEventTool_RejectsMissingEvent verifies JSON schema
// enforcement via the BindSimple adapter's Unmarshal step.
func TestVLPHandleEventTool_RejectsMissingEvent(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	// Omit the required "event" field.
	_, terr := callVLP(t, reg, map[string]any{
		"session_id": "missing-event",
	})
	if terr == nil {
		t.Fatalf("expected error for missing event, got nil")
	}
	if terr.Code != "ErrInvalidArgument" {
		t.Errorf("error code: want ErrInvalidArgument, got %q", terr.Code)
	}
}

// TestVLPHandleEventTool_RejectsInvalidJSON verifies that malformed
// input returns a clear ErrInvalidArgument, not a Go error.
func TestVLPHandleEventTool_RejectsInvalidJSON(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	tool := reg.Get("vlp_handle_event")
	resp, err := tool.Handler(context.Background(), json.RawMessage(`{not valid json`))
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error envelope for invalid JSON")
	}
	if resp.Error.Code != "ErrInvalidArgument" {
		t.Errorf("error code: want ErrInvalidArgument, got %q", resp.Error.Code)
	}
}

// TestVLPHandleEventTool_TerminalReturnsEmptyNextAction verifies that
// reaching a terminal state (Complete) yields NextAction="" and
// IsTerminal=true.
func TestVLPHandleEventTool_TerminalReturnsEmptyNextAction(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	sid := "terminal-test"
	for _, args := range []map[string]any{
		{"session_id": sid, "event": "session_start"},
		{"session_id": sid, "event": "vibe_publish"},
		{"session_id": sid, "event": "artifact_log"},
		{"session_id": sid, "event": "drift_log", "verdict": "aligned"},
	} {
		res, terr := callVLP(t, reg, args)
		if terr != nil {
			t.Fatalf("setup: %v", terr)
		}
		if args["event"] == "drift_log" {
			if !res.IsTerminal {
				t.Errorf("after drift_log+aligned: want IsTerminal=true, got false")
			}
			if res.NextAction != "" {
				t.Errorf("after drift_log+aligned: want NextAction=\"\", got %q", res.NextAction)
			}
			if res.NewState != "complete" {
				t.Errorf("after drift_log+aligned: want NewState=complete, got %q", res.NewState)
			}
		}
	}
}

// TestVLPHandleEventTool_DriftLoop verifies the canonical drift loop:
// drift_detected loops back to spec_active for regeneration.
func TestVLPHandleEventTool_DriftLoop(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	sid := "drift-loop"
	steps := []map[string]any{
		{"session_id": sid, "event": "session_start"},
		{"session_id": sid, "event": "vibe_publish"},
		{"session_id": sid, "event": "artifact_log"},
		{"session_id": sid, "event": "drift_log", "verdict": "drift_detected"},
	}
	expectedStates := []string{"drafting_spec", "spec_active", "drift_judging", "spec_active"}
	for i, args := range steps {
		res, terr := callVLP(t, reg, args)
		if terr != nil {
			t.Fatalf("step %d: %v", i, terr)
		}
		if res.NewState != expectedStates[i] {
			t.Errorf("step %d: want state %q, got %q", i, expectedStates[i], res.NewState)
		}
	}
}

// TestVLPHandleEventTool_NeedsHuman verifies the verdict=needs_human
// path lands in StateNeedsHuman (terminal in-place).
func TestVLPHandleEventTool_NeedsHuman(t *testing.T) {
	reg, _, _, cleanup := newVLPTestSetup(t)
	defer cleanup()

	sid := "needs-human"
	for _, args := range []map[string]any{
		{"session_id": sid, "event": "session_start"},
		{"session_id": sid, "event": "vibe_publish"},
		{"session_id": sid, "event": "artifact_log"},
		{"session_id": sid, "event": "drift_log", "verdict": "needs_human"},
	} {
		if _, terr := callVLP(t, reg, args); terr != nil {
			t.Fatalf("setup: %v", terr)
		}
	}
	// Final state should be needs_human, terminal.
	res, terr := callVLP(t, reg, map[string]any{
		"session_id": sid,
		"event":      "vibe_publish",
	})
	if terr == nil {
		t.Fatalf("expected ErrInvalidTransition when firing event on terminal state")
	}
	if terr.Code != "ErrInvalidTransition" {
		t.Errorf("want ErrInvalidTransition, got %q", terr.Code)
	}
	_ = res
}

// TestRegisterVLP_PanicsOnNilArgs verifies the constructor's
// fail-fast behavior on bad inputs.
func TestRegisterVLP_PanicsOnNilArgs(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("RegisterVLP(nil, nil): expected panic, got none")
		}
	}()
	RegisterVLP(nil, nil)
}
