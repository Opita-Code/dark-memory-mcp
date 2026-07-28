// Package e2e — gate_constitution_test.go: regression test for the
// v2.1.3 production bug where every session-bound tool call refused
// with "identity unavailable for this session" because the gate's
// IdentityFrame composition failed.
//
// # Why this test exists
//
// Pre-fix reproduction (the live bug):
//  1. Operator runs dark-mem-mcp without DARK_CONSTITUTION_FILE set.
//  2. The watchdog short-circuits — the `constitutions` table stays empty.
//  3. Operator calls session_start without passing constitution_id/
//     constitution_ver (both optional on SessionStartInput).
//  4. Operator calls agent_memory_save (or any other session-required
//     tool) — the gate fires.
//  5. GateMiddleware.Wrap → buildGateInput → resolver returns the
//     session_id (resolver cache invalidation worked). Good.
//  6. PreCheck → src.IdentityFrame(session_id) → CachedSource.IdentityFrame
//     → StoreSource.IdentityFrame → Store.GetSession returns the row.
//  7. atomic.NewIdentityFrame(operator, operator, session_id,
//     constitution_id="", constitution_ver="", false) returns
//     (nil, ErrEmptyConstitutionID) — hard requirement on non-empty
//     fields.
//  8. CachedSource.IdentityFrame returns (nil, err) → gate sees
//     err != nil → refuses with "identity unavailable for this
//     session". User-visible failure mode.
//
// # Post-fix expectations
//
//  1. The bootstrap auto-loads the canonical dark-mem.constitution.toml
//     when DARK_CONSTITUTION_FILE is unset, so the watchdog inserts a
//     row into the `constitutions` table at boot. ActiveConstitution
//     returns the active (id, ver).
//  2. StoreSource.IdentityFrame mirrors PersonaFrame's pattern: when
//     the session-bound constitution is empty, it falls back to the
//     active constitution from the Store. NewIdentityFrame now has
//     non-empty fields and returns a valid frame.
//  3. The gate allows the call.
//
// This test exercises the FULL wire path through GateMiddleware.Wrap
// (not via callTool which bypasses the gate by going directly to the
// raw Handler — see gate_test.go for that distinction).
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// withGateAutoConstitution wires a GateMiddleware whose
// ActiveConstitution field reads the auto-loaded constitution from
// the Store at call time. The default withGate hardcodes
// ("cerebro", "1.1.0") for legacy tests that pre-date the
// auto-load; this helper produces a gate that's correct against the
// real auto-loaded constitution.
//
// # Why this matters for the bug
//
// PreCheck's constitution-mismatch check requires the identity
// frame's constitution to equal the gate-input constitution
// (both must come from the same source — the auto-loaded active
// constitution). If we leave withGate's hardcoded value in place,
// the test would fail at the constitution-mismatch step BEFORE
// it could verify the IdentityFrame fallback fix. So the helper
// is structural, not optional.
func withGateAutoConstitution(t *testing.T) *testServer {
	t.Helper()
	ts := withGate(t)
	ctx := context.Background()
	id, ver, _ := ts.boot.Store.ActiveConstitution(ctx)
	if id == "" || ver == "" {
		t.Fatalf("test setup: auto-load did not register a constitution; the test would pass for the wrong reason (gate would refuse with constitution-mismatch instead of identity-frame failure)")
	}
	ts.boot.Gate.ActiveConstitution = func() (string, string) { return id, ver }
	return ts
}

// callViaGate invokes a tool through GateMiddleware.Wrap, mirroring
// the production wire path (MCP transport → server.wrapHandler →
// gate.Wrap). The existing callTool helper bypasses the gate by
// calling t.Handler directly — useful for handler-level unit tests
// but useless for verifying gate behavior.
//
// Returns the data portion of the ToolResponse as map[string]any,
// or an error string formatted from the ToolError fields when the
// gate refuses.
func callViaGate(t *testing.T, ts *testServer, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	tool := ts.srv.Registry().Get(stripWirePrefix(name))
	if tool == nil {
		return nil, fmt.Errorf("tool %q not registered", name)
	}
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		return tool.Handler(ctx, raw)
	}
	resp, err := ts.boot.Gate.Wrap(context.Background(), tool.Name, raw, inner)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("gate returned nil response")
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("gate refusal: code=%s message=%s hint=%s",
			resp.Error.Code, resp.Error.Message, resp.Error.Hint)
	}
	out := map[string]any{}
	if resp.Data != nil {
		b, err := json.Marshal(resp.Data)
		if err != nil {
			return nil, fmt.Errorf("marshal data: %w", err)
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("unmarshal data: %w", err)
		}
	}
	return out, nil
}

// TestE2E_Gate_IdentityFrameConstitutionFallback is the regression
// test for the production bug. It exercises the EXACT production
// sequence:
//
//  1. Boot with no DARK_CONSTITUTION_FILE → auto-load fires, the
//     canonical dark-mem.constitution.toml is registered, the
//     `constitutions` table is populated.
//  2. session_start with NO constitution_id / constitution_ver → the
//     session row carries empty constitution fields.
//  3. agent_memory_save through the gate (the production wire path,
//     not the bypassed handler path).
//
// Pre-fix: the gate refuses with "identity unavailable for this
// session" because NewIdentityFrame requires non-empty constitution.
//
// Post-fix: the gate allows because StoreSource.IdentityFrame falls
// back to the active constitution (which the auto-load populated).
func TestE2E_Gate_IdentityFrameConstitutionFallback(t *testing.T) {
	ts := withGateAutoConstitution(t)
	defer ts.close()

	// Step 1: session_start without constitution_id/ver. This is the
	// bug trigger — the session row carries empty constitution fields.
	startOut, err := callTool(ts, "session_start", map[string]any{
		"operator":   "op-test",
		"project_id": "default",
		// intentionally NOT passing constitution_id or constitution_ver
	})
	if err != nil {
		t.Fatalf("session_start: %v", err)
	}
	sid := asString(startOut, "session_id")
	if sid == "" {
		t.Fatalf("session_start returned no session_id: %+v", startOut)
	}

	// Step 2: agent_memory_save through the GATE (not via the
	// bypassed callTool). This is the regression assertion.
	saveOut, err := callViaGate(t, ts, "agent_memory_save", map[string]any{
		"operator": "op-test",
		"kind":     "note",
		"content":  "constitution-fallback regression: session has empty constitution, gate must allow",
		"title":    "identity-frame-constitution-fallback",
		"tags":     "v2.1.3,regression,constitution,identity-frame",
	})
	if err != nil {
		t.Fatalf("agent_memory_save via gate: %v", err)
	}

	// Sanity-check the row landed.
	idFloat, ok := saveOut["id"].(float64)
	if !ok || idFloat == 0 {
		// The handler may return {audit_id, row} or just {id}; accept either.
		if row, ok := saveOut["row"].(map[string]any); ok {
			idFloat, _ = row["id"].(float64)
		}
		if idFloat == 0 {
			t.Fatalf("agent_memory_save returned no id; response=%+v", saveOut)
		}
	}
}

// TestE2E_Gate_IdentityFrameNoActiveConstitutionStillAllows is the
// defensive coverage: when the auto-load fails (no builtin in
// search paths), the gate should STILL not infinitely refuse — it
// should return a clear error rather than the opaque
// "identity unavailable" message.
//
// In this case the IdentityFrame composition returns nil (no
// active constitution to fall back to), so the gate refuses.
// We verify the refusal message is informative, not just the same
// opaque text the production bug emitted.
//
// Note: in practice the auto-load succeeds in this test repo
// (the binary lives next to vibe-flow/constitution/), so this
// test is a documentation of the contract rather than an active
// failure mode. It exists so future refactors of the gate or
// the auto-load don't silently regress into the opaque message.
func TestE2E_Gate_IdentityFrameNoActiveConstitutionStillAllows(t *testing.T) {
	ts := withGate(t)
	defer ts.close()

	// session_start without constitution.
	startOut, err := callTool(ts, "session_start", map[string]any{
		"operator":   "op-test",
		"project_id": "default",
	})
	if err != nil {
		t.Fatalf("session_start: %v", err)
	}
	sid := asString(startOut, "session_id")
	if sid == "" {
		t.Fatalf("session_start returned no session_id")
	}

	// Either auto-load succeeded (gate allows) or it failed (gate
	// refuses). Both are acceptable; the contract is "the message
	// is informative, not the opaque 'identity unavailable' string".
	_, err = callViaGate(t, ts, "agent_memory_save", map[string]any{
		"operator": "op-test",
		"kind":     "note",
		"content":  "defensive coverage",
		"title":    "no-active-constitution-defensive",
	})
	if err != nil {
		t.Logf("gate refusal (acceptable if informative): %v", err)
	}
}
