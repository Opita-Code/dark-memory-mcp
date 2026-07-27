// Package server — middleware_test.go: covers GateMiddleware.Wrap.
//
// Three categories:
//  1. PreCheck refusal propagation (gate refuses → ToolError w/ Reason code).
//  2. PostCheck drift propagation (drift detected → ErrDriftAtWrite ToolError).
//  3. Pass-through (allowed + read-only tool → inner result verbatim).
//
// All tests use stubFrameSource + mockJudge (no real Store), keeping
// the suite hermetic and fast.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/drift"
	"github.com/dark-agents/dark-memory-mcp/internal/policy"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// stubFrameSource returns nil for every frame unless overridden.
type stubFrameSource struct {
	identity *atomic.IdentityFrame
	scope    *atomic.ScopeFrame
	caps     *atomic.CapabilitiesFrame
	drift    *atomic.DriftFrame
	persona  *atomic.PersonaFrame

	identityErr error
	capsErr     error
	personaErr  error
}

func (s *stubFrameSource) IdentityFrame(context.Context, string) (*atomic.IdentityFrame, error) {
	return s.identity, s.identityErr
}
func (s *stubFrameSource) ScopeFrame(context.Context, string) (*atomic.ScopeFrame, error) {
	return s.scope, nil
}
func (s *stubFrameSource) CapabilitiesFrame(context.Context, string) (*atomic.CapabilitiesFrame, error) {
	return s.caps, s.capsErr
}
func (s *stubFrameSource) DriftFrame(context.Context, string) (*atomic.DriftFrame, error) {
	return s.drift, nil
}
func (s *stubFrameSource) PersonaFrame(context.Context, string) (*atomic.PersonaFrame, error) {
	return s.persona, s.personaErr
}

// staticConstitution returns a fixed (id, ver) pair matching the
// happyFrames constitution. Used by every test that wants PreCheck
// to pass the constitution-match check (priority #1.5: identity
// constitution must equal the active constitution).
func staticConstitution() func() (string, string) {
	return func() (string, string) { return "cerebro", "1.1.0" }
}

// mockJudge is a drift.JudgeCaller that returns a fixed verdict.
// Drift.Checker.CheckArtifact calls judge.Judge(ctx, in) → *JudgeOutput;
// we satisfy that interface here without the real orchestrator.
type mockJudge struct {
	decision  string
	reasoning string
	err       error
}

func (m *mockJudge) Judge(ctx context.Context, in drift.JudgeInput) (*drift.JudgeOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	verdictJSON := fmt.Sprintf(`{"verdict":%q,"confidence":0.95,"reasoning":%q}`, m.decision, m.reasoning)
	return &drift.JudgeOutput{
		EvaluationID: 1,
		VerdictJSON:  verdictJSON,
		Confidence:   0.95,
		Model:        "mock-judge-test",
		Provider:     "mock",
	}, nil
}

// happyFrames returns the minimum frame set that lets PreCheck pass:
// Identity (with valid constitution binding), Capabilities (grants
// the requested tool), Persona (matches Identity's constitution).
func happyFrames(toolName string) *stubFrameSource {
	now := time.Now().UTC()
	id, _ := atomic.NewIdentityFrame("op-test", "op-test", "sess-test", "cerebro", "1.1.0", false)
	// Override composed_at to "now" so freshness checks pass; NewIdentityFrame
	// sets ComposedAtValue to time.Now() too, but be explicit.
	id.ComposedAtValue = now
	persona, _ := atomic.NewPersonaFrame("cerebro", "1.1.0", "default",
		"test voice", "test claims policy", "test refusal pattern", "test tone")
	return &stubFrameSource{
		identity: id,
		caps: &atomic.CapabilitiesFrame{
			ComposedAtValue:  now,
			ProjectID:        "default",
			SessionID:        "sess-test",
			GrantedTools:     []atomic.ToolGrant{{ToolName: toolName, Scope: "*", GrantedAt: now}},
			GrantedScopes:    []atomic.ScopeGrant{{ProjectID: "default", GrantedAt: now}},
			GrantedExpiresAt: now.Add(1 * time.Hour),
			Source:           "test",
		},
		persona: persona,
	}
}

// noIdentity returns frames without an IdentityFrame — gate refuses
// with ReasonFrameStale at priority #1.
func noIdentity() *stubFrameSource {
	now := time.Now().UTC()
	persona, _ := atomic.NewPersonaFrame("cerebro", "1.1.0", "default",
		"test voice", "test claims policy", "test refusal pattern", "test tone")
	return &stubFrameSource{
		caps: &atomic.CapabilitiesFrame{
			ComposedAtValue: now,
			ProjectID:       "default",
			SessionID:       "sess-test",
			GrantedTools:    []atomic.ToolGrant{{ToolName: "health_ping", Scope: "*", GrantedAt: now}},
			GrantedScopes:   []atomic.ScopeGrant{{ProjectID: "default", GrantedAt: now}},
			Source:          "test",
		},
		persona: persona,
	}
}

// noCaps returns frames where Capabilities does NOT grant the tool.
// PreCheck refuses with ReasonCapabilityDenied at priority #2.
func noCaps(toolName string) *stubFrameSource {
	now := time.Now().UTC()
	id, _ := atomic.NewIdentityFrame("op-test", "op-test", "sess-test", "cerebro", "1.1.0", false)
	id.ComposedAtValue = now
	persona, _ := atomic.NewPersonaFrame("cerebro", "1.1.0", "default",
		"test voice", "test claims policy", "test refusal pattern", "test tone")
	return &stubFrameSource{
		identity: id,
		caps: &atomic.CapabilitiesFrame{
			ComposedAtValue: now,
			ProjectID:       "default",
			SessionID:       "sess-test",
			GrantedTools:    []atomic.ToolGrant{{ToolName: "some_other_tool", Scope: "*", GrantedAt: now}},
			GrantedScopes:   []atomic.ScopeGrant{{ProjectID: "default", GrantedAt: now}},
			Source:          "test",
		},
		persona: persona,
	}
}

// --- PreCheck tests ---

func TestGateMiddleware_PreCheck_RefusesMissingIdentity(t *testing.T) {
	m := &GateMiddleware{
		FrameSource:        noIdentity(),
		ActiveSession:      StaticSessionResolver{SessionID: "sess-test"},
		ActiveConstitution: staticConstitution(),
	}
	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "should not happen"}, nil
	}
	// v2.0.2 update: previously this test used "health_ping", but
	// health_ping is now in the session-free allowlist — PreCheck
	// returns Allowed=true without consulting identity. We use
	// "vibe_publish" (a session-required artifact-creating tool)
	// to exercise the missing-identity refusal path.
	resp, err := m.Wrap(context.Background(), "vibe_publish", json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if innerCalled {
		t.Fatalf("PreCheck should have refused before inner ran")
	}
	if resp.Error == nil {
		t.Fatalf("expected refusal ToolError; got %+v", resp)
	}
	if resp.Error.Code != "ErrFrameStaleTooFar" {
		t.Errorf("Error.Code = %q, want ErrFrameStaleTooFar", resp.Error.Code)
	}
}

func TestGateMiddleware_PreCheck_RefusesMissingCapability(t *testing.T) {
	m := &GateMiddleware{
		FrameSource:        noCaps("vibe_publish"),
		ActiveSession:      StaticSessionResolver{SessionID: "sess-test"},
		ActiveConstitution: staticConstitution(),
	}
	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "should not happen"}, nil
	}
	resp, err := m.Wrap(context.Background(), "vibe_publish", json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if innerCalled {
		t.Fatalf("PreCheck should have refused before inner ran")
	}
	if resp.Error == nil || resp.Error.Code != "ErrCapabilityNotGranted" {
		t.Errorf("expected ErrCapabilityNotGranted; got %+v", resp.Error)
	}
}

func TestGateMiddleware_PreCheck_AllowsReadOnlyTool(t *testing.T) {
	m := &GateMiddleware{
		FrameSource:        happyFrames("health_ping"),
		ActiveSession:      StaticSessionResolver{SessionID: "sess-test"},
		ActiveConstitution: staticConstitution(),
	}
	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: map[string]any{"ok": true}}, nil
	}
	resp, err := m.Wrap(context.Background(), "health_ping", json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !innerCalled {
		t.Fatalf("inner should have been called for read-only tool")
	}
	if resp.Error != nil {
		t.Errorf("expected success; got refusal %+v", resp.Error)
	}
}

// --- PostCheck tests ---

func TestGateMiddleware_PostCheck_DriftDetected_Strict_Refuses(t *testing.T) {
	src := happyFrames("vibe_publish")
	// NewChecker requires (store, judge, strictness). The middleware
	// never actually reads the store from the checker — CheckArtifact
	// reads from the artifact input we provide via buildArtifactInput.
	// (See drift_test.go for the store-backed end-to-end suite.)
	checker := drift.NewChecker(nil, &mockJudge{decision: "drift_detected", reasoning: "stub"}, drift.StrictnessStrict)
	m := &GateMiddleware{
		FrameSource:        src,
		DriftChecker:       checker,
		ActiveSession:      StaticSessionResolver{SessionID: "sess-test"},
		ActiveConstitution: staticConstitution(),
	}
	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{
			Data: map[string]any{"spec_id": int64(42), "url": "https://example.com/art"},
		}, nil
	}
	resp, err := m.Wrap(context.Background(), "vibe_publish",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default","spec_id":42}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !innerCalled {
		t.Fatalf("inner should have been called for artifact-creating tool with allowed PreCheck")
	}
	if resp.Error == nil {
		t.Fatalf("expected drift refusal; got %+v", resp)
	}
	if resp.Error.Code != "ErrDriftAtWrite" {
		t.Errorf("Error.Code = %q, want ErrDriftAtWrite", resp.Error.Code)
	}
}

func TestGateMiddleware_PostCheck_DriftAligned_AllowsSave(t *testing.T) {
	src := happyFrames("vibe_publish")
	checker := drift.NewChecker(nil, &mockJudge{decision: "aligned", reasoning: "stub"}, drift.StrictnessStrict)
	m := &GateMiddleware{
		FrameSource:        src,
		DriftChecker:       checker,
		ActiveSession:      StaticSessionResolver{SessionID: "sess-test"},
		ActiveConstitution: staticConstitution(),
	}
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		return &tools.ToolResponse{Data: map[string]any{"spec_id": int64(42)}}, nil
	}
	resp, err := m.Wrap(context.Background(), "vibe_publish",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default","spec_id":42}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("expected success on aligned drift; got refusal %+v", resp.Error)
	}
}

func TestGateMiddleware_PostCheck_ReadOnlyTool_SkipsDrift(t *testing.T) {
	// health_ping is NOT in IsArtifactCreating — even with a DriftChecker
	// set, the middleware should NOT call it.
	src := happyFrames("health_ping")
	checker := drift.NewChecker(nil, &mockJudge{decision: "drift_detected", reasoning: "would refuse if called"}, drift.StrictnessStrict)
	m := &GateMiddleware{
		FrameSource:        src,
		DriftChecker:       checker,
		ActiveSession:      StaticSessionResolver{SessionID: "sess-test"},
		ActiveConstitution: staticConstitution(),
	}
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		return &tools.ToolResponse{Data: map[string]any{"ok": true}}, nil
	}
	resp, err := m.Wrap(context.Background(), "health_ping",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("read-only tool should skip drift; got refusal %+v", resp.Error)
	}
}

func TestGateMiddleware_NilMiddleware_CallsInnerDirectly(t *testing.T) {
	// Defensive: a nil GateMiddleware must still work (legacy tests
	// can build a Server without wiring the gate).
	var m *GateMiddleware
	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "legacy"}, nil
	}
	resp, err := m.Wrap(context.Background(), "any_tool", json.RawMessage(`{}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !innerCalled {
		t.Fatalf("nil middleware should call inner directly")
	}
	if resp.Error != nil {
		t.Errorf("expected success; got %+v", resp.Error)
	}
}

// Sanity check: Reason error_kind mapping (used by refusalResponse).
func TestReasonErrorKind(t *testing.T) {
	cases := map[policy.Reason]string{
		policy.ReasonScopeRequired:    "ErrScopeRequired",
		policy.ReasonCapabilityDenied: "ErrCapabilityNotGranted",
		policy.ReasonPersonaMissing:   "ErrPersonaNotResolvable",
		policy.ReasonFrameStale:       "ErrFrameStaleTooFar",
		policy.ReasonDriftAtWrite:     "ErrDriftAtWrite",
		policy.ReasonOK:               "ErrGateOK",
	}
	for r, want := range cases {
		if got := r.ErrorKind(); got != want {
			t.Errorf("Reason(%q).ErrorKind() = %q, want %q", r, got, want)
		}
	}
}

// IsArtifactCreating is exported; verify the public set the middleware
// uses matches the documented wire contract.
func TestIsArtifactCreating_Contract(t *testing.T) {
	expected := map[string]bool{
		"vibe_publish":      true,
		"vibe_spec":         true,
		"resolve_drift":     true,
		"judge":             true,
		"consensus":         true,
		"load_constitution": true,
		"admin_migrate":     true,
		"vlp_handle_event":  true,
		// Read-only / introspection: NOT artifact-creating.
		"health_ping":      false,
		"memory_state":     false,
		"session_status":   false,
		"pipeline_status":  false,
		"artifact_context": false,
		"spec_context":     false,
		"recall":           false,
		"research_topic":   false,
		"active_policy":    false,
	}
	for tool, want := range expected {
		if got := IsArtifactCreating(tool); got != want {
			t.Errorf("IsArtifactCreating(%q) = %v, want %v", tool, got, want)
		}
	}
}

// Compile-time guard: GateMiddleware implements the patterns wrapHandler uses.
var _ = func() bool {
	var _ = func() *tools.ToolResponse {
		var m *GateMiddleware
		resp, _ := m.Wrap(context.Background(), "x", json.RawMessage(`{}`),
			func(context.Context, json.RawMessage) (*tools.ToolResponse, error) { return nil, nil })
		return resp
	}
	return true
}()