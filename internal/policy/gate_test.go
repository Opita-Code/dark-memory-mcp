// Package policy - gate_test.go: covers PreCheck + PostCheck
// including the new Wave 5A.vi (M6) drift-at-write behavior.
package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/drift"
)

// stubFrameSource is an in-memory FrameSource for testing the gate.
// All frames must be pre-populated via the *Frame helpers; missing
// frames surface as (nil, nil) per the gate contract.
type stubFrameSource struct {
	identity     *atomic.IdentityFrame
	capabilities *atomic.CapabilitiesFrame
	persona      *atomic.PersonaFrame
	scope        *atomic.ScopeFrame
	drift        *atomic.DriftFrame
}

func (s *stubFrameSource) IdentityFrame(ctx context.Context, sid string) (*atomic.IdentityFrame, error) {
	if s.identity == nil || s.identity.SessionID != sid {
		return nil, nil
	}
	return s.identity, nil
}
func (s *stubFrameSource) CapabilitiesFrame(ctx context.Context, sid string) (*atomic.CapabilitiesFrame, error) {
	if s.capabilities == nil {
		return nil, nil
	}
	return s.capabilities, nil
}
func (s *stubFrameSource) PersonaFrame(ctx context.Context, sid string) (*atomic.PersonaFrame, error) {
	if s.persona == nil {
		return nil, nil
	}
	return s.persona, nil
}
func (s *stubFrameSource) ScopeFrame(ctx context.Context, sid string) (*atomic.ScopeFrame, error) {
	if s.scope == nil || s.scope.SessionID != sid {
		return nil, nil
	}
	return s.scope, nil
}
func (s *stubFrameSource) DriftFrame(ctx context.Context, sid string) (*atomic.DriftFrame, error) {
	if s.drift == nil || s.drift.SessionID != sid {
		return nil, nil
	}
	return s.drift, nil
}

// minimalFrames returns the minimum frame set that lets PreCheck pass:
// valid identity, valid capabilities granting "dark_memory_vibe_publish",
// valid persona, optional scope.
func minimalFrames(t *testing.T, sessionID, projectID, constitutionID, constitutionVer string) *stubFrameSource {
	t.Helper()
	now := time.Now().UTC()
	identity, err := atomic.NewIdentityFrame(
		"actor", "operator", sessionID, constitutionID, constitutionVer, false,
	)
	if err != nil {
		t.Fatalf("NewIdentityFrame: %v", err)
	}
	caps, err := atomic.NewCapabilitiesFrame(
		projectID, sessionID,
		[]atomic.ToolGrant{
			{ToolName: "dark_memory_vibe_publish", Scope: projectID, GrantedAt: now},
			{ToolName: "dark_memory_artifact_log", Scope: projectID, GrantedAt: now},
		},
		[]atomic.ScopeGrant{{ProjectID: projectID, ReadOnly: false, GrantedAt: now}},
		now.Add(1*time.Hour),
		"default:test",
	)
	if err != nil {
		t.Fatalf("NewCapabilitiesFrame: %v", err)
	}
	persona, err := atomic.NewPersonaFrame(
		constitutionID, constitutionVer, "brand-1",
		"voice", "claims", "refusal-pattern", "tone",
	)
	if err != nil {
		t.Fatalf("NewPersonaFrame: %v", err)
	}
	return &stubFrameSource{
		identity:     identity,
		capabilities: caps,
		persona:      persona,
	}
}

func preCheckFor(t *testing.T, src *stubFrameSource, toolName, projectID string) *PreCheckResult {
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
	if !out.Allowed {
		t.Fatalf("PreCheck should have allowed; reason=%s message=%s", out.Reason, out.Message)
	}
	return out
}

// --- PostCheck tests (Wave 5A.vi, M6 drift-at-write) ---

// TestPostCheck_NoDriftChecker_LegacyStub: the pre-5A.vi behavior
// (no DriftChecker wired) must still work — DriftVerdict="skipped",
// Allowed=true, no ErrDriftAtWrite.
func TestPostCheck_NoDriftChecker_LegacyStub(t *testing.T) {
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:      pre,
		Response: "ok",
	})
	if !out.Allowed {
		t.Errorf("Allowed = false, want true (no drift checker → legacy stub)")
	}
	if out.DriftVerdict != "skipped" {
		t.Errorf("DriftVerdict = %q, want skipped", out.DriftVerdict)
	}
	if !strings.Contains(out.Message, "Wave 5A.vi") {
		t.Errorf("Message should mention Wave 5A.vi; got %q", out.Message)
	}
}

// TestPostCheck_PreRefused_Echoes: when PreCheck already refused,
// PostCheck echoes the refusal regardless of drift wiring.
func TestPostCheck_PreRefused_Echoes(t *testing.T) {
	pre := &PreCheckResult{Allowed: false, Reason: ReasonCapabilityDenied, Message: "no caps"}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  drift.NewChecker(nil, nil, drift.StrictnessStrict),
		DriftArtifact: &drift.ArtifactInput{SpecID: 1},
	})
	if out.Allowed {
		t.Errorf("Allowed = true, want false (pre-check refused)")
	}
	if out.Reason != ReasonFrameStale {
		t.Errorf("Reason = %q, want ReasonFrameStale (pre-check failed → echoes)", out.Reason)
	}
}

// mockJudge is a JudgeCaller for the PostCheck integration tests.
type mockJudge struct {
	resp *drift.JudgeOutput
	err  error
}

func (m *mockJudge) Judge(ctx context.Context, in drift.JudgeInput) (*drift.JudgeOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func TestPostCheck_DriftWired_Aligned_AllowsSave(t *testing.T) {
	mock := &mockJudge{
		resp: &drift.JudgeOutput{
			VerdictJSON:  `{"verdict":"aligned","confidence":0.92}`,
			Confidence:   0.92,
			EvaluationID: 11,
		},
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessStrict)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: &drift.ArtifactInput{SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x"},
	})
	if !out.Allowed {
		t.Errorf("Allowed = false on aligned verdict; reason=%s message=%s", out.Reason, out.Message)
	}
	if out.DriftVerdict != "aligned" {
		t.Errorf("DriftVerdict = %q, want aligned", out.DriftVerdict)
	}
}

func TestPostCheck_DriftWired_DriftDetected_Strict_Refuses(t *testing.T) {
	mock := &mockJudge{
		resp: &drift.JudgeOutput{
			VerdictJSON: `{"verdict":"drift_detected","confidence":0.85}`,
			Confidence:  0.85,
		},
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessStrict)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: &drift.ArtifactInput{SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x"},
	})
	if out.Allowed {
		t.Errorf("Allowed = true on drift_detected + strict; should refuse")
	}
	if out.Reason != ReasonDriftAtWrite {
		t.Errorf("Reason = %q, want ReasonDriftAtWrite", out.Reason)
	}
	if out.DriftVerdict != "drift_detected" {
		t.Errorf("DriftVerdict = %q, want drift_detected", out.DriftVerdict)
	}
	if !strings.Contains(out.Hint, "dark_memory_resolve_drift") {
		t.Errorf("Hint should mention dark_memory_resolve_drift; got %q", out.Hint)
	}
}

func TestPostCheck_DriftWired_DriftDetected_Warn_AllowsSave(t *testing.T) {
	mock := &mockJudge{
		resp: &drift.JudgeOutput{
			VerdictJSON: `{"verdict":"drift_detected","confidence":0.85}`,
			Confidence:  0.85,
		},
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessWarn)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: &drift.ArtifactInput{SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x"},
	})
	if !out.Allowed {
		t.Errorf("Allowed = false on drift_detected + warn; should allow with drift_pending")
	}
	if out.DriftVerdict != "drift_detected" {
		t.Errorf("DriftVerdict = %q, want drift_detected (surfaced for caller tagging)", out.DriftVerdict)
	}
	if !strings.Contains(out.Message, "warn mode") {
		t.Errorf("Message should mention warn mode; got %q", out.Message)
	}
}

func TestPostCheck_DriftWired_NeedsHuman_Strict_Refuses(t *testing.T) {
	mock := &mockJudge{
		resp: &drift.JudgeOutput{
			VerdictJSON: `{"verdict":"needs_human","confidence":0.7}`,
			Confidence:  0.7,
		},
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessStrict)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: &drift.ArtifactInput{SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x"},
	})
	// Conservative: needs_human under strict → refuse (same as drift_detected).
	if out.Allowed {
		t.Errorf("Allowed = true on needs_human + strict; should refuse")
	}
	if out.Reason != ReasonDriftAtWrite {
		t.Errorf("Reason = %q, want ReasonDriftAtWrite", out.Reason)
	}
}

func TestPostCheck_DriftWired_StrictnessOff_AllowsSave(t *testing.T) {
	mock := &mockJudge{
		resp: &drift.JudgeOutput{
			VerdictJSON: `{"verdict":"drift_detected","confidence":0.95}`,
			Confidence:  0.95,
		},
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessOff)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: &drift.ArtifactInput{SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x"},
	})
	if !out.Allowed {
		t.Errorf("Allowed = false on strictness=off; should allow (skip drift)")
	}
	if out.DriftVerdict != "skipped" {
		t.Errorf("DriftVerdict = %q, want skipped (off mode)", out.DriftVerdict)
	}
}

func TestPostCheck_DriftWired_JudgeError_Strict_Refuses(t *testing.T) {
	mock := &mockJudge{
		err: errors.New("network unreachable"),
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessStrict)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: &drift.ArtifactInput{SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x"},
	})
	// Under strict, a judge error MUST refuse (operator must not be
	// able to bypass drift by disabling the LLM).
	if out.Allowed {
		t.Errorf("Allowed = true on judge error + strict; should refuse")
	}
	if out.Reason != ReasonDriftAtWrite {
		t.Errorf("Reason = %q, want ReasonDriftAtWrite", out.Reason)
	}
}

func TestPostCheck_DriftWired_JudgeError_Warn_AllowsSave(t *testing.T) {
	mock := &mockJudge{
		err: errors.New("network unreachable"),
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessWarn)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: &drift.ArtifactInput{SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x"},
	})
	if !out.Allowed {
		t.Errorf("Allowed = false on judge error + warn; should allow with skipped verdict")
	}
	if out.DriftVerdict != "skipped" {
		t.Errorf("DriftVerdict = %q, want skipped", out.DriftVerdict)
	}
}

// TestPostCheck_DriftArtifactNil_NoDriftCheck: when DriftChecker is
// provided but DriftArtifact is nil, drift check is skipped (caller
// signal: "I'm not creating an artifact in this call").
func TestPostCheck_DriftArtifactNil_NoDriftCheck(t *testing.T) {
	mock := &mockJudge{
		resp: &drift.JudgeOutput{VerdictJSON: `{"verdict":"drift_detected"}`, Confidence: 0.9},
	}
	checker := drift.NewChecker(nil, mock, drift.StrictnessStrict)
	pre := &PreCheckResult{Allowed: true}
	out := PostCheck(context.Background(), PostCheckInput{
		Pre:           pre,
		DriftChecker:  checker,
		DriftArtifact: nil, // explicit no-artifact
	})
	if !out.Allowed {
		t.Errorf("Allowed = false on no-artifact; should allow (skip drift)")
	}
	if out.DriftVerdict != "skipped" {
		t.Errorf("DriftVerdict = %q, want skipped", out.DriftVerdict)
	}
}

// TestReason_ErrorKind_IncludesDriftAtWrite: the Reason→error_kind
// mapping must include the ReasonDriftAtWrite case (gate-level
// refusal maps to ErrDriftAtWrite via the MCP error envelope).
func TestReason_ErrorKind_IncludesDriftAtWrite(t *testing.T) {
	if got := ReasonDriftAtWrite.ErrorKind(); got != "ErrDriftAtWrite" {
		t.Errorf("ReasonDriftAtWrite.ErrorKind() = %q, want ErrDriftAtWrite", got)
	}
}

// TestIsToolArtifactCreating: verifies the gate knows which tools
// trigger drift checks. Currently vibe_publish + artifact_log; the
// gate won't drift-check a session_start (no artifact produced).
func TestIsToolArtifactCreating(t *testing.T) {
	cases := []struct {
		tool string
		want bool
	}{
		{"dark_memory_vibe_publish", true},
		{"dark_memory_artifact_log", true},
		{"dark_memory_session_start", false},
		{"dark_memory_recall", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			got := isToolArtifactCreating(c.tool)
			if got != c.want {
				t.Errorf("isToolArtifactCreating(%q) = %v, want %v", c.tool, got, c.want)
			}
		})
	}
}

// _ ensures the test file's variable refs compile against minimalFrames.
var _ = preCheckFor