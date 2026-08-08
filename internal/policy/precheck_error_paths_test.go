// Package policy - precheck_error_paths_test.go: covers the
// PreCheck error branches that the original suite missed. The
// baseline mutation run (gremlins v0.6.0) reported 19 NOT COVERED
// mutants at gate.go:239-363 — every one of them a "frame lookup or
// validation failed" branch that no test exercised. These tests
// close the gap by driving each failure mode through a real
// PreCheck call and asserting the exact Reason + Allowed=false.
package policy

import (
	"context"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
)

// preCheckDenied runs PreCheck and asserts it was refused with the
// expected Reason. Returns the result for extra field asserts.
func preCheckDenied(t *testing.T, src *stubFrameSource, toolName, projectID string, wantReason Reason) *PreCheckResult {
	t.Helper()
	in := GateInput{
		SessionID:       "sess-test",
		ProjectID:       projectID,
		ConstitutionID:  "constitution-1",
		ConstitutionVer: "1.0.0",
		ToolName:        toolName,
		Now:             time.Now().UTC(),
	}
	out, err := PreCheck(context.Background(), src, in)
	if err != nil {
		t.Fatalf("PreCheck: %v", err)
	}
	if out.Allowed {
		t.Fatalf("PreCheck(%s) should have refused; got Allowed=true", toolName)
	}
	if out.Reason != wantReason {
		t.Errorf("PreCheck(%s).Reason = %q, want %q (message=%s)", toolName, out.Reason, wantReason, out.Message)
	}
	return out
}

// TestPreCheck_IdentityUnavailable: IdentityFrame returns (nil, nil)
// (or error) → refused with ReasonFrameStale.
func TestPreCheck_IdentityUnavailable(t *testing.T) {
	src := &stubFrameSource{} // identity == nil
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonFrameStale)
}

// TestPreCheck_IdentityInvalid: identity fails Validate (e.g.
// emptied ConstitutionID after construction).
func TestPreCheck_IdentityInvalid(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.identity.ConstitutionID = "" // breaks Validate
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonFrameStale)
}

// TestPreCheck_ConstitutionMismatch: identity bound to a different
// constitution than the input.
func TestPreCheck_ConstitutionMismatch(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.identity.ConstitutionID = "constitution-other"
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonFrameStale)
}

// TestPreCheck_IdentityStale: ComposedAt older than
// MaxIdentityFrameAge → refused.
func TestPreCheck_IdentityStale(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.identity.ComposedAtValue = time.Now().Add(-2 * atomic.MaxIdentityFrameAge)
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonFrameStale)
}

// TestPreCheck_CapabilitiesUnavailable: CapabilitiesFrame returns
// (nil, nil) → refused with ReasonCapabilityDenied.
func TestPreCheck_CapabilitiesUnavailable(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.capabilities = nil
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonCapabilityDenied)
}

// TestPreCheck_CapabilitiesInvalid: caps fails Validate (emptied
// ProjectID).
func TestPreCheck_CapabilitiesInvalid(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.capabilities.ProjectID = "" // breaks Validate
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonCapabilityDenied)
}

// TestPreCheck_ToolNotGranted: caps valid but the tool is not in
// GrantedTools.
func TestPreCheck_ToolNotGranted(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	preCheckDenied(t, src, "dark_memory_session_close", "p1", ReasonCapabilityDenied)
}

// TestPreCheck_PersonaUnavailable: PersonaFrame returns (nil, nil)
// → refused with ReasonPersonaMissing.
func TestPreCheck_PersonaUnavailable(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.persona = nil
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonPersonaMissing)
}

// TestPreCheck_PersonaInvalid: persona fails Validate (emptied
// ConstitutionID).
func TestPreCheck_PersonaInvalid(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.persona.ConstitutionID = "" // breaks Validate
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonPersonaMissing)
}

// TestPreCheck_PersonaIdentityMismatch: persona bound to a
// different constitution than the identity.
func TestPreCheck_PersonaIdentityMismatch(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	src.persona.ConstitutionID = "constitution-other"
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonPersonaMissing)
}

// TestPreCheck_ScopeRequiredButUnavailable: tool requires a scope
// (vibe_publish) but no ScopeFrame exists → refused with
// ReasonScopeRequired.
func TestPreCheck_ScopeRequiredButUnavailable(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonScopeRequired)
}

// TestPreCheck_ScopeIdentityMismatch: scope present but bound to a
// different session → refused with ReasonScopeRequired.
func TestPreCheck_ScopeIdentityMismatch(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	scope, err := atomic.NewScopeFrame(
		"different-session", 1, nil, nil, "aligned", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewScopeFrame: %v", err)
	}
	src.scope = scope
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonScopeRequired)
}

// TestPreCheck_ScopeProjectNotGranted: scope has an open spec but
// the project is not in the caps scope grants.
func TestPreCheck_ScopeProjectNotGranted(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	scope, err := atomic.NewScopeFrame(
		"sess-test", 1, nil, nil, "aligned", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewScopeFrame: %v", err)
	}
	src.scope = scope
	// Grant caps only for a different project so HasProjectAccess
	// fails while the identity is still valid for sess-test.
	src.capabilities.GrantedScopes = []atomic.ScopeGrant{
		{ProjectID: "other-project", ReadOnly: false, GrantedAt: time.Now().UTC()},
	}
	preCheckDenied(t, src, "dark_memory_vibe_publish", "p1", ReasonScopeRequired)
}

// TestPreCheck_ScopeSatisfied_PassesThrough: the happy path with a
// matching scope must NOT be refused — guards the switch-case at
// gate.go:330 from over-refusing when a scope IS available.
func TestPreCheck_ScopeSatisfied_PassesThrough(t *testing.T) {
	src := minimalFrames(t, "sess-test", "p1", "constitution-1", "1.0.0")
	scope, err := atomic.NewScopeFrame(
		"sess-test", 1, nil, nil, "aligned", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewScopeFrame: %v", err)
	}
	src.scope = scope
	out := preCheckFor(t, src, "dark_memory_vibe_publish", "p1")
	if out.Frames == nil || out.Frames.Scope != scope {
		t.Errorf("PreCheck should have attached the scope to Frames")
	}
}
