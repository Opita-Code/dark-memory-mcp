// Package e2e — gate_test.go: end-to-end test that exercises the
// GateMiddleware (NOT just tool.Handler). The other e2e tests call
// tools via callTool → t.Handler directly, which BYPASSES the gate
// entirely. That's why the v2.1.0 DefaultToolGrants bug (prefixed
// names that never matched HasGrant's exact-match) went undetected
// for months: every test that landed in CI went through Handler.
//
// This test wires a real GateMiddleware onto the test server and
// invokes tools through Gate.Wrap. If the gate refuses a call that
// the handler would otherwise accept, the test catches it. The
// regression that motivated this file (v2.1.2): DefaultToolGrants
// had the wire-format prefix on every entry, so HasGrant returned
// false for every tool name — meaning the gate refused every
// session-required call in production.
//
// Per RFC §6: every session-required tool (anything outside the
// RequiresActiveSession allowlist) must pass the full gate flow.
// agent_memory_save is the v2.1.0-added exemplar.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// withGate wraps newTestServer to also install a GateMiddleware on
// the BootState. After this returns, every tool invocation through
// the registry goes through PreCheck → handler → PostCheck, the
// same path the live MCP server uses.
//
// The gate is wired with the same FrameSource + ActiveSession +
// ActiveProject + ActiveConstitution pattern that cmd/dark-mem-mcp
// uses (see cmd/dark-mem-mcp/main.go:100-112). Tests that want to
// exercise the gate should use this constructor instead of
// newTestServer.
func withGate(t *testing.T) *testServer {
	t.Helper()
	ts := newTestServer(t)

	// Convert *safety.Holder to *store.SafetyHolder — same pattern
	// cmd/dark-mem-mcp/main.go uses (the function-field SafetyHolder
	// in internal/store is a layer over the concrete safety.Holder
	// to avoid an import cycle with internal/safety).
	safetyFP := &store.SafetyHolder{
		SetCanary: func(token string) { ts.boot.Safety.Set(safety.CanaryToken(token)) },
		Active:    func() string { return string(ts.boot.Safety.Active()) },
		ValidatePayload: func(payload string) error {
			return ts.boot.Safety.ValidatePayload(payload)
		},
	}

	// Frame source — recall.NewSingleton opens the TTL-cached
	// CachedSource that composes IdentityFrame + CapabilitiesFrame
	// from the Store on every call. Production wires this at boot;
	// tests need it too to exercise the gate.
	frameSrc, err := recall.NewSingleton(ts.boot.Store, safetyFP, nil)
	if err != nil {
		ts.close()
		t.Fatalf("recall.NewSingleton: %v", err)
	}

	// v2.0.2 gate fix: real ActiveSessionResolver backed by the
	// store's projects.active_session_id column.
	resolver := server.NewStoreBackedActiveSessionResolver(
		server.StoreBackedLookup(ts.boot.Store),
	)

	// v2.1.1: ActiveProject fallback so session-required tools
	// without project_id in args (agent_memory_*, session_status,
	// session_close) can resolve the active project's session_id.
	// Same wiring pattern as cmd/dark-mem-mcp/main.go:110.
	ts.boot.Gate = &server.GateMiddleware{
		FrameSource:   frameSrc,
		ActiveSession: resolver,
		ActiveProject: ts.boot.Store.ActiveProject,
		// Match the test middleware's staticConstitution helper.
		// Without this, the gate's constitution cross-check refuses
		// every session-required call. cerebro/1.1.0 is the
		// canonical constitution used by internal/server/
		// middleware_test.go's happyFrames.
		ActiveConstitution: func() (string, string) { return "cerebro", "1.1.0" },
	}
	return ts
}

// TestE2E_Gate_BareNameGrant is the v2.1.2 regression test: a
// session-required tool (agent_memory_save) must reach the inner
// handler when invoked through GateMiddleware.Wrap. Pre-fix,
// DefaultToolGrants had wire-prefixed entries that never matched
// HasGrant's exact-match, so the gate refused with
// ErrCapabilityNotGranted before the handler ran.
//
// This test would also have caught the v2.1.1 GateMiddleware
// empty-project_id regression (ErrFrameStaleTooFar on the same
// path) — the gate would refuse before reaching the handler.
func TestE2E_Gate_BareNameGrant(t *testing.T) {
	ts := withGate(t)
	defer ts.close()
	_ = context.Background

	// Step 1: session_start bypasses the gate (per RequiresActiveSession)
	// but is still routed through the registry → handler.
	startOut, err := callTool(ts, "session_start", map[string]any{
		"operator":         "op-test",
		"project_id":       "default",
		"constitution_id":  "cerebro",
		"constitution_ver": "1.1.0",
	})
	if err != nil {
		t.Fatalf("session_start: %v", err)
	}
	sid := asString(startOut, "session_id")
	if sid == "" {
		t.Fatalf("session_start returned no session_id: %+v", startOut)
	}

	// Step 2: agent_memory_save goes through the gate. Pre-v2.1.2,
	// this refused with ErrCapabilityNotGranted because the default
	// grants list had wire-prefixed entries. Post-v2.1.2, it
	// reaches the handler and writes a row.
	saveOut, err := callTool(ts, "agent_memory_save", map[string]any{
		"operator": "op-test",
		"kind":     "note",
		"content":  "v2.1.2 regression test: gate must allow agent_memory_save",
		"title":    "gate-bare-name-grant",
		"tags":     "v2.1.2,regression,gate",
	})
	if err != nil {
		t.Fatalf("agent_memory_save: %v", err)
	}
	idFloat, ok := saveOut["id"].(float64)
	if !ok || idFloat == 0 {
		// The handler returns {"audit_id":N,"row":{...,"id":N}}. Newer
		// versions may flatten this — accept either shape.
		row, rowOK := saveOut["row"].(map[string]any)
		if rowOK {
			idFloat, ok = row["id"].(float64)
		}
		if !ok || idFloat == 0 {
			t.Fatalf("agent_memory_save returned no id: %+v", saveOut)
		}
	}

	// Step 3: agent_memory_list must also pass the gate.
	listOut, err := callTool(ts, "agent_memory_list", map[string]any{
		"scope": "session",
		"limit": 10,
	})
	if err != nil {
		t.Fatalf("agent_memory_list: %v", err)
	}
	rows, ok := listOut["rows"].([]any)
	if !ok {
		t.Fatalf("agent_memory_list missing rows array: %+v", listOut)
	}
	if len(rows) == 0 {
		t.Fatalf("agent_memory_list returned 0 rows; expected at least the row we just saved")
	}
}

// TestE2E_Gate_SessionStatus exercises session_status through the
// gate. Like agent_memory_save, this is session-required and was
// broken by the v2.1.0 DefaultToolGrants bug. Added as a separate
// test so the failure points at the affected tool clearly.
func TestE2E_Gate_SessionStatus(t *testing.T) {
	ts := withGate(t)
	defer ts.close()
	_ = context.Background

	// session_start (gate bypass — bootstrap tool)
	startOut, err := callTool(ts, "session_start", map[string]any{
		"operator":         "op-test",
		"project_id":       "default",
		"constitution_id":  "cerebro",
		"constitution_ver": "1.1.0",
	})
	if err != nil {
		t.Fatalf("session_start: %v", err)
	}
	sid := asString(startOut, "session_id")
	if sid == "" {
		t.Fatalf("session_start returned no session_id: %+v", startOut)
	}

	// session_status through the gate. Requires session_id arg
	// (its JSON schema marks it required). The gate still validates
	// project/session scopes — this exercises the full path.
	statusOut, err := callTool(ts, "session_status", map[string]any{
		"session_id": sid,
	})
	if err != nil {
		t.Fatalf("session_status: %v", err)
	}
	gotSid := asString(statusOut, "session_id")
	if gotSid != sid {
		t.Errorf("session_status returned session_id=%q, want %q", gotSid, sid)
	}
}

// silence unused-import warnings if a future refactor drops a
// reference (e.g. ctx, json, fmt, time). Kept for clarity — these
// imports are used by the helpers and the test bodies above.
var (
	_ = context.Background
	_ = json.Marshal
	_ = fmt.Sprintf
	_ = time.Now
	_ = tools.WirePrefix
)
