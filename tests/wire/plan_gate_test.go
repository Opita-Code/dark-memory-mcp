package wire

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// This is the END-TO-END proof that the gate of the plan works on the
// real wire: the real binary, the real middleware, the real store.
//
// MEDIDO el 2026-11-18 (PR B): the gate reads the plan of the session
// through vlp_state.open_spec_id, and NOTHING in production wrote that
// column — so the gate would have found no plan and would have refused
// every mutating call. The Go-level tests prove the pieces; this one
// proves the assembled thing.
//
// The three modes, measured on the wire:
//
//   - off:    the mutating tool is NOT refused.
//   - warn:   the mutating tool is NOT refused, but the refusal is
//             RECORDED.
//   - strict: the mutating tool IS refused with ErrPlanRequired.

// requireFreshWireBin skips the test unless DARK_MEM_MCP_BIN points at
// a binary built from THIS tree.
//
// The reason is not fussiness: these tests assert the behaviour of the
// GATE, so they need a binary that HAS the gate. resolveWireBin falls
// back to ../cmd/dark-mem-mcp/dark-mem-mcp when the variable is unset,
// and that file can be a STALE build from another commit (it is
// untracked, so it survives across checkouts). A test that silently
// measures the wrong binary is worse than a test that says so.
//
// The CI wire job builds the binary from the checkout and sets the
// variable, so there the tests run; in the unit job they skip, exactly
// like the rest of the wire suite.
func requireFreshWireBin(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("DARK_MEM_MCP_BIN")) == "" {
		t.Skip("wire: DARK_MEM_MCP_BIN is not set: these tests assert the behaviour of the gate, and they need the binary built from this tree (the CI wire job sets it)")
	}
}

// TestPlanGate_StrictRefusesAMutatingToolOnTheWire is the proof that
// the gate refuses over the real wire in strict mode, with the
// canonical error_kind.
func TestPlanGate_StrictRefusesAMutatingToolOnTheWire(t *testing.T) {
	requireFreshWireBin(t)
	s := startWireSessionWithEnv(t, []string{"DARK_PLAN_STRICTNESS=strict"})

	// A session, so that PreCheck lets the call through to the gate
	// of the plan (which is AFTER the identity check).
	if out, err := callToolWire(s, "session_start", map[string]any{
		"operator":   "plan-gate-wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v (body=%s)", err, out)
	}

	// A tool that MUTATES and is not exempt from the gate.
	out, err := callToolWire(s, "agent_memory_save", map[string]any{
		"kind":    "observation",
		"title":   "wire test of the plan gate",
		"content": "this call should be refused: there is no plan",
	})
	if err != nil {
		t.Fatalf("tools/call agent_memory_save: wire error: %v (body=%s)", err, out)
	}

	if !strings.Contains(out, "ErrPlanRequired") {
		t.Errorf("the mutating tool was NOT refused with ErrPlanRequired: %s", summarizeErr(out))
	}
}

// TestPlanGate_WarnLetsTheCallThrough is the proof that the default
// mode measures instead of blocking: the tool runs.
func TestPlanGate_WarnLetsTheCallThrough(t *testing.T) {
	requireFreshWireBin(t)
	s := startWireSessionWithEnv(t, []string{"DARK_PLAN_STRICTNESS=warn"})

	if out, err := callToolWire(s, "session_start", map[string]any{
		"operator":   "plan-gate-wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v (body=%s)", err, out)
	}

	out, err := callToolWire(s, "agent_memory_save", map[string]any{
		"kind":    "observation",
		"title":   "wire test of the plan gate in warn mode",
		"content": "this call goes through: warn measures, it does not block",
	})
	if err != nil {
		t.Fatalf("tools/call agent_memory_save: wire error: %v (body=%s)", err, out)
	}
	if strings.Contains(out, "ErrPlanRequired") {
		t.Errorf("the mutating tool was refused in warn mode: %s", summarizeErr(out))
	}
}

// TestPlanGate_OffDoesNotCheck is the proof that `off` leaves the
// behaviour of today.
func TestPlanGate_OffDoesNotCheck(t *testing.T) {
	requireFreshWireBin(t)
	s := startWireSessionWithEnv(t, []string{"DARK_PLAN_STRICTNESS=off"})

	if out, err := callToolWire(s, "session_start", map[string]any{
		"operator":   "plan-gate-wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v (body=%s)", err, out)
	}

	out, err := callToolWire(s, "agent_memory_save", map[string]any{
		"kind":    "observation",
		"title":   "wire test of the plan gate in off mode",
		"content": "this call goes through: off does not check",
	})
	if err != nil {
		t.Fatalf("tools/call agent_memory_save: wire error: %v (body=%s)", err, out)
	}
	if strings.Contains(out, "ErrPlanRequired") {
		t.Errorf("the mutating tool was refused in off mode: %s", summarizeErr(out))
	}
}

// TestPlanGate_TheLifecycleNeverDeadlocks is the wire proof of T12-bis:
// the tools of the lifecycle of the plan are exempt, so the FIRST plan
// can always be created, even in strict mode.
func TestPlanGate_TheLifecycleNeverDeadlocks(t *testing.T) {
	requireFreshWireBin(t)
	s := startWireSessionWithEnv(t, []string{"DARK_PLAN_STRICTNESS=strict"})

	if out, err := callToolWire(s, "session_start", map[string]any{
		"operator":   "plan-gate-wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v (body=%s)", err, out)
	}

	// vibe_spec creates the spec the plan hangs from. It is exempt, so
	// it cannot be refused for want of a plan (that would be the
	// deadlock: to make a plan you need a spec, and to make a spec
	// you need a plan).
	out, err := callToolWire(s, "vibe_spec", map[string]any{
		"vibe_case": "C2",
		"spec":      `{"intent":"write the wire test of the plan gate"}`,
		"tasks":     []any{map[string]any{"id": "t1", "description": "prove the gate"}},
	})
	if err != nil {
		t.Fatalf("tools/call vibe_spec: wire error: %v (body=%s)", err, out)
	}
	if strings.Contains(out, "ErrPlanRequired") {
		t.Errorf("vibe_spec was refused for want of a plan: the gate deadlocks: %s", summarizeErr(out))
	}
}

// The refusal carries the reason in the error_kind of the envelope,
// and NOT a generic one: a harness has to be able to tell the three
// reasons apart. And it carries a HINT, because the fix is different
// in each case: create a plan, approve it, or raise its budget.
func TestPlanGate_TheRefusalCarriesTheCanonicalErrorKind(t *testing.T) {
	requireFreshWireBin(t)
	s := startWireSessionWithEnv(t, []string{"DARK_PLAN_STRICTNESS=strict"})

	if out, err := callToolWire(s, "session_start", map[string]any{
		"operator":   "plan-gate-wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v (body=%s)", err, out)
	}

	out, err := callToolWire(s, "agent_memory_save", map[string]any{
		"kind":    "observation",
		"title":   "wire test of the error kind",
		"content": "the refusal must name its reason",
	})
	if err != nil {
		t.Fatalf("tools/call agent_memory_save: wire error: %v (body=%s)", err, out)
	}

	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("the response is not JSON: %v (body=%s)", err, out)
	}
	text := ""
	for _, c := range env.Result.Content {
		text += c.Text
	}
	if !strings.Contains(text, "ErrPlanRequired") {
		t.Errorf("the refusal does not carry ErrPlanRequired: %s", text)
	}
	if !strings.Contains(text, "plan_create") && !strings.Contains(text, "plan_decide") {
		t.Errorf("the refusal does not say what to do: %s", text)
	}
}
