// Package policy - gate_v2_0_2_test.go: v2.0.2 per-tool session
// requirement regression tests.
//
// The bug we are guarding against (DARK-MEM-v2.0.1-REGRESSION-1):
// PreCheck unconditionally refused `SessionID == "" || ProjectID ==
// ""`, which broke session_start, health_ping, memory_state and
// every read-only introspection tool (they're supposed to work
// without a session). The v2.0.2 fix is RequiresActiveSession() —
// a per-tool allow list that lets bootstrap and introspection
// tools through.
package policy

import (
	"context"
	"testing"
	"time"
)

// TestRequiresActiveSession pins the v2.0.2 contract: tools NOT
// on the list require a session (default). Operators adding a
// tool here is a wire-contract change.
func TestRequiresActiveSession(t *testing.T) {
	// Tools that MUST be callable without an active session.
	// Same list lives at the gate definition site; double-checked
	// here so a drift in either place surfaces as a test failure.
	sessionFree := []string{
		"session_start",
		"session_resume",
		"health_ping",
		"memory_state",
		"project_create",
		"active_policy",
		"load_constitution",
		"admin_schema_status",
		"admin_vacuum",
		"admin_migrate",
	}
	for _, name := range sessionFree {
		if RequiresActiveSession(name) {
			t.Errorf("%q should NOT require an active session (v2.0.2 contract); if you intentionally added it to the required list, update this test in lockstep", name)
		}
	}

	// Tools that DO require an active session (representative
	// sample — the list above is the gate-side authoritative
	// list, not these test rows). Each entry is a tool that
	// reads or writes session-scoped data.
	sessionRequired := []string{
		"vibe_publish",
		"vibe_spec",
		"research_topic",
		"research_recall",
		"session_close",
		"session_status",
		"consensus",
		"judge",
	}
	for _, name := range sessionRequired {
		if !RequiresActiveSession(name) {
			t.Errorf("%q SHOULD require an active session; if you intentionally exempted it, update this test in lockstep", name)
		}
	}
}

// TestPreCheck_SessionFreeToolAllowsWithoutSession is the direct
// regression guard for DARK-MEM-v2.0.1-REGRESSION-1. Before the
// fix, calling PreCheck with an empty SessionID/ProjectID on a
// session-free tool returned Allowed=false with ReasonFrameStale.
// After the fix, the same call returns Allowed=true.
//
// Note: we pass a session-free ToolName (health_ping). The
// FrameSource is nil — it must NOT be consulted for session-free
// tools, which is the whole point. If a future change starts
// dereferencing src for session-free tools, that would be the
// regression this test catches.
func TestPreCheck_SessionFreeToolAllowsWithoutSession(t *testing.T) {
	in := GateInput{
		ToolName:  "health_ping",
		ProjectID: "",  // explicitly empty
		SessionID: "",  // explicitly empty
		Now:       timeNow(),
	}
	res, err := PreCheck(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("PreCheck returned error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("health_ping: want Allowed=true (session-free tool), got %+v", res)
	}
	if res.Reason != ReasonOK {
		t.Errorf("health_ping: want ReasonOK, got %q", res.Reason)
	}
}

// TestPreCheck_SessionRequiredToolRefusesWithoutSession
// confirms that session-required tools still refuse (the v2.0.2
// split didn't widen the gate to non-free tools).
func TestPreCheck_SessionRequiredToolRefusesWithoutSession(t *testing.T) {
	in := GateInput{
		ToolName:  "vibe_publish",
		ProjectID: "",
		SessionID: "",
		Now:       timeNow(),
	}
	res, err := PreCheck(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("PreCheck returned error: %v", err)
	}
	if res.Allowed {
		t.Fatalf("vibe_publish: want Allowed=false when session is unbound, got %+v", res)
	}
	if res.Reason != ReasonFrameStale {
		t.Errorf("vibe_publish: want ReasonFrameStale, got %q", res.Reason)
	}
	if res.Hint == "" {
		t.Errorf("vibe_publish: Hint empty (the message should nudge the operator toward session_start)")
	}
}

// timeNow is a tiny helper so the tests don't import "time"
// directly (already done in gate_test.go above but a local copy
// keeps the regression file self-contained).
func timeNow() time.Time {
	return time.Now().UTC()
}
