// Package e2e — oauth_stub.go: a test-mode OAuthRegistrar that satisfies
// the canonical-order completeness check (tools/register.go:319) by
// registering the 4 OAUTH-namespace tools with stub handlers, without
// requiring the real OAuth router (which lives in cmd/dark-mem-mcp and
// cannot be imported here due to Go's parent-child layering).
//
// The 4 tools' wire handlers are no-ops in tests: any direct invocation
// returns a structured error ("oauth not configured in test environment")
// rather than performing real OAuth work. None of the e2e tests call the
// OAuth tools directly — they exercise the gate, daemon stream,
// constitution fallback, and identity-frame paths. The stub exists ONLY
// to satisfy the canonical-order check that RegisterAllWithDeps runs at
// the end of RegisterAll (line 321) — without it, every test that calls
// newTestServer fails with "missing tool 'judge_options'".
//
// This is the e2e half of the B1 fix that fast-forwarded main to v4
// (2026-10-15, archive tags: archive/main-pre-ff-20261015). Without the
// half, the 5 tests (TestDaemon_ServesNativeMCPWire + 4 Gate tests) that
// drive CI turned red because alpha.32 was the first push to bring
// RegisterAllWithDeps's OAuth-Registrar-required invariant into main.
// The other half (registering OAuth for real in production boot) lives
// in cmd/dark-mem-mcp/legacy_main.go:217.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// oauthStubNames are the bare tool names (without dark_memory_ prefix)
// that the canonical-order check expects to find in the registry. Order
// here is irrelevant — what matters is presence (RegisterAll's loop
// checks each canonical name against the registry, not the register
// order). When a real OAuth Registrar is wired (legacy_main.go:217), it
// registers these same 4 names with full handlers backed by the OAuth
// router; here we register stubs with identical names + no-op handlers.
var oauthStubNames = []string{
	"judge_options",
	"oauth_connect",
	"oauth_status",
	"oauth_disconnect",
}

// stubOAuthHandler returns a stable, descriptive error so any test that
// accidentally invokes one of the stub tools gets a clear message that
// explains why it can't actually do anything. Tests that exercise the
// OAuth tools proper live in cmd/dark-mem-mcp/internal/oauth (where the
// real router + keystore are addressable).
func stubOAuthHandler(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
	return nil, fmt.Errorf("oauth stub: tool not configured in test environment (use cmd/dark-mem-mcp/internal/oauth tests for real OAuth flow)")
}

// registerOAuthStubs adds the 4 OAuth-stub tools to the registry. It is
// the closure value assigned to tools.OAuthRegistrar by installOAuthStub.
// Idempotent: re-calling on the same registry is a no-op for already-
// registered names (Registry.Add panics on duplicates; the guard below
// preempts the panic for tests that somehow register twice, e.g. when
// newTestServer is called by both withGate and withGateAutoConstitution
// within one test process).
func registerOAuthStubs(reg *tools.Registry) {
	if reg == nil {
		return
	}
	for _, name := range oauthStubNames {
		if reg.Get(name) != nil {
			// already registered (parent test setup or re-entry) — skip
			continue
		}
		reg.Add(&tools.Tool{
			Name:        name,
			Description: "OAuth stub (e2e test mode; real handler at cmd/dark-mem-mcp)",
			Handler:     stubOAuthHandler,
		})
	}
}

// installOAuthStub assigns the stub registrar to the package-level
// tools.OAuthRegistrar (the closure slot Read tools' canonical-order
// check expects to be non-nil in v4alpha+). Cleanup restores nil so
// tests in OTHER packages can't be poisoned by a leaked closure from
// an e2e test that set it and didn't reset it. Returns the registrar
// so callers can also wire it directly without touching the package
// var (preferred when test scope is single-call).
func installOAuthStub(t *testing.T) {
	t.Helper()
	if tools.OAuthRegistrar == nil {
		tools.OAuthRegistrar = registerOAuthStubs
		t.Cleanup(func() {
			tools.OAuthRegistrar = nil
		})
	}
}
