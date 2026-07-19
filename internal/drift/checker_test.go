// Package drift — checker_test.go: covers Checker.CheckArtifact
// across strictness modes + judge outcomes. Uses a mock JudgeCaller
// (no LLM dependency).
package drift

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// mockJudge is a JudgeCaller fixture. It returns canned responses
// (or errors) per the configured scenario. Thread-safe via t.Helper
// + the fact that CheckArtifact is called sequentially in tests.
type mockJudge struct {
	resp   *JudgeOutput
	err    error
	calls  int
	lastIn JudgeInput
}

func (m *mockJudge) Judge(ctx context.Context, in JudgeInput) (*JudgeOutput, error) {
	m.calls++
	m.lastIn = in
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

// newTestChecker builds a Checker with a mockJudge + nil Store
// (the Store is only consulted when SpecID > 0 AND Strictness != off;
// most tests don't exercise that path).
func newTestChecker(jc JudgeCaller, s Strictness) *Checker {
	return &Checker{
		Store:      nil, // not used by tests that don't hit GetSpec
		Judge:      jc,
		Strictness: s,
		Logger:     nil, // no log noise during tests
	}
}

// TestCheckArtifact_StrictnessOff_AlwaysSkips: when strictness=off,
// CheckArtifact must NOT call the judge — it returns Decision="skipped"
// with the canonical "drift strictness off" reasoning.
func TestCheckArtifact_StrictnessOff_AlwaysSkips(t *testing.T) {
	mock := &mockJudge{
		resp: &JudgeOutput{VerdictJSON: `{"verdict":"aligned"}`, Confidence: 0.95},
	}
	c := newTestChecker(mock, StrictnessOff)
	v, err := c.CheckArtifact(context.Background(), ArtifactInput{
		SpecID:       42,
		ArtifactType: "code",
		ArtifactURL:  "file://x.go",
		Text:         "package x",
	})
	if err != nil {
		t.Fatalf("CheckArtifact: %v", err)
	}
	if v.Decision != "skipped" {
		t.Errorf("Decision = %q, want skipped", v.Decision)
	}
	if !strings.Contains(v.Reasoning, "off") {
		t.Errorf("Reasoning = %q, want contains 'off'", v.Reasoning)
	}
	if mock.calls != 0 {
		t.Errorf("judge called %d times, want 0 (strictness=off must short-circuit)", mock.calls)
	}
}

// TestCheckArtifact_StrictnessWarn_Aligned_AllowsSave: aligned verdict
// under warn mode → Allowed path (DriftVerdict="aligned", no ErrDriftAtWrite).
func TestCheckArtifact_StrictnessWarn_Aligned_AllowsSave(t *testing.T) {
	mock := &mockJudge{
		resp: &JudgeOutput{
			VerdictJSON:  `{"verdict":"aligned","confidence":0.92,"reasoning":"spec and artifact match"}`,
			Confidence:   0.92,
			EvaluationID: 7,
		},
	}
	c := newTestChecker(mock, StrictnessWarn)
	v, err := c.CheckArtifact(context.Background(), ArtifactInput{
		SpecID: 42, ArtifactType: "code", ArtifactURL: "file://x.go", Text: "package x",
	})
	if err != nil {
		t.Fatalf("CheckArtifact: %v", err)
	}
	if v.Decision != "aligned" {
		t.Errorf("Decision = %q, want aligned", v.Decision)
	}
	if v.Confidence != 0.92 {
		t.Errorf("Confidence = %v, want 0.92", v.Confidence)
	}
	if v.EvaluationID != 7 {
		t.Errorf("EvaluationID = %d, want 7", v.EvaluationID)
	}
	if v.StrictnessApplied != StrictnessWarn {
		t.Errorf("StrictnessApplied = %v, want warn", v.StrictnessApplied)
	}
}

// TestCheckArtifact_StrictnessStrict_DriftDetected_ReturnsVerdict:
// strict mode + drift_detected → Verdict carries the drift signal
// (NOT ErrDriftAtWrite — that's the gate's job to emit based on
// StrictnessApplied + Decision).
func TestCheckArtifact_StrictnessStrict_DriftDetected_ReturnsVerdict(t *testing.T) {
	mock := &mockJudge{
		resp: &JudgeOutput{
			VerdictJSON:  `{"verdict":"drift_detected","confidence":0.85,"reasoning":"spec says add audit, artifact does not"}`,
			Confidence:   0.85,
			EvaluationID: 9,
		},
	}
	c := newTestChecker(mock, StrictnessStrict)
	v, err := c.CheckArtifact(context.Background(), ArtifactInput{
		SpecID: 42, ArtifactType: "code", ArtifactURL: "file://x.go", Text: "package x",
	})
	if err != nil {
		t.Fatalf("CheckArtifact: %v", err)
	}
	if v.Decision != "drift_detected" {
		t.Errorf("Decision = %q, want drift_detected", v.Decision)
	}
	if v.StrictnessApplied != StrictnessStrict {
		t.Errorf("StrictnessApplied = %v, want strict", v.StrictnessApplied)
	}
}

// TestCheckArtifact_NoSpecID_SkipsWithoutJudge: SpecID=0 → skip even
// under strict mode (drift is meaningless without a spec).
func TestCheckArtifact_NoSpecID_SkipsWithoutJudge(t *testing.T) {
	mock := &mockJudge{}
	c := newTestChecker(mock, StrictnessStrict)
	v, err := c.CheckArtifact(context.Background(), ArtifactInput{
		SpecID: 0, ArtifactType: "code", ArtifactURL: "file://x.go", Text: "x",
	})
	if err != nil {
		t.Fatalf("CheckArtifact: %v", err)
	}
	if v.Decision != "skipped" {
		t.Errorf("Decision = %q, want skipped", v.Decision)
	}
	if mock.calls != 0 {
		t.Errorf("judge called %d times, want 0 (no spec → skip)", mock.calls)
	}
}

// TestCheckArtifact_JudgeUnavailable_SkipsUnderAllModes: when the
// judge returns an error (no LLM, canary rejection, network), the
// checker reports Decision="skipped" — never refuses for a missing
// judge (that would make strict mode a footgun).
func TestCheckArtifact_JudgeUnavailable_SkipsUnderAllModes(t *testing.T) {
	for _, s := range []Strictness{StrictnessWarn, StrictnessStrict} {
		t.Run(s.String(), func(t *testing.T) {
			mock := &mockJudge{
				err: errors.New("no LLM available (ANTHROPIC_API_KEY unset)"),
			}
			c := newTestChecker(mock, s)
			v, err := c.CheckArtifact(context.Background(), ArtifactInput{
				SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x",
			})
			if err != nil {
				t.Fatalf("CheckArtifact: %v", err)
			}
			if v.Decision != "skipped" {
				t.Errorf("Decision = %q, want skipped (LLM unavailable)", v.Decision)
			}
			if !strings.Contains(v.Reasoning, "no LLM") {
				t.Errorf("Reasoning should mention LLM unavailability; got %q", v.Reasoning)
			}
		})
	}
}

// TestCheckArtifact_CanaryRejection_SkipsSpecifically: when the
// judge refuses because of canary in payload (INV-3), the checker
// reports "skipped" with the canary-specific reasoning (NOT
// "judge unavailable" — those are different operational signals).
func TestCheckArtifact_CanaryRejection_SkipsSpecifically(t *testing.T) {
	mock := &mockJudge{
		err: errors.New("judge content contains canary token (ErrCanaryInPayload)"),
	}
	c := newTestChecker(mock, StrictnessStrict)
	v, err := c.CheckArtifact(context.Background(), ArtifactInput{
		SpecID: 42, ArtifactType: "code", ArtifactURL: "f", Text: "x",
	})
	if err != nil {
		t.Fatalf("CheckArtifact: %v", err)
	}
	if v.Decision != "skipped" {
		t.Errorf("Decision = %q, want skipped (canary rejection)", v.Decision)
	}
	if !strings.Contains(v.Reasoning, "canary") {
		t.Errorf("Reasoning should mention canary; got %q", v.Reasoning)
	}
}

// TestCheckArtifact_PassesContentToJudge: the content string passed
// to Judge must include artifact type, URL, spec_id, and body — so
// the LLM has full context for the drift verdict.
func TestCheckArtifact_PassesContentToJudge(t *testing.T) {
	mock := &mockJudge{
		resp: &JudgeOutput{VerdictJSON: `{"verdict":"aligned"}`, Confidence: 0.9},
	}
	c := newTestChecker(mock, StrictnessWarn)
	_, err := c.CheckArtifact(context.Background(), ArtifactInput{
		SpecID:       42,
		ArtifactType: "code",
		ArtifactURL:  "file://internal/foo.go",
		Text:         "package foo\nfunc Bar() {}",
	})
	if err != nil {
		t.Fatalf("CheckArtifact: %v", err)
	}
	if mock.calls != 1 {
		t.Fatalf("expected 1 judge call, got %d", mock.calls)
	}
	if mock.lastIn.EvalType != "drift_judge" {
		t.Errorf("EvalType = %q, want drift_judge", mock.lastIn.EvalType)
	}
	if mock.lastIn.TargetType != "artifact" {
		t.Errorf("TargetType = %q, want artifact", mock.lastIn.TargetType)
	}
	if !strings.Contains(mock.lastIn.Content, "file://internal/foo.go") {
		t.Errorf("Content missing URL; got: %s", mock.lastIn.Content)
	}
	if !strings.Contains(mock.lastIn.Content, "package foo") {
		t.Errorf("Content missing body; got: %s", mock.lastIn.Content)
	}
	if !strings.Contains(mock.lastIn.Content, "code") {
		t.Errorf("Content missing type; got: %s", mock.lastIn.Content)
	}
}

// TestParseDecisionFromJudgeJSON: covers the verdict-shape parser
// (structured JSON + legacy bare-word form + normalization).
func TestParseDecisionFromJudgeJSON(t *testing.T) {
	cases := []struct {
		name     string
		json     string
		conf     float32
		want     string
	}{
		{"structured aligned", `{"verdict":"aligned","confidence":0.92}`, 0.92, "aligned"},
		{"structured drift_detected", `{"verdict":"drift_detected","confidence":0.85}`, 0.85, "drift_detected"},
		{"structured needs_human", `{"verdict":"needs_human","confidence":0.7}`, 0.7, "needs_human"},
		{"structured with reasoning", `{"verdict":"aligned","reasoning":"spec matches","confidence":0.9}`, 0.9, "aligned"},
		{"bare aligned", "aligned\nreasoning here", 0.9, "aligned"},
		{"bare drift", `drift_detected`, 0.9, "drift_detected"},
		{"bare needs_human", `needs_human`, 0.9, "needs_human"},
		{"aligned low conf → needs_human", `{"verdict":"aligned","confidence":0.1}`, 0.1, "needs_human"},
		{"unknown → drift_detected (conservative)", `{"verdict":"weird_value"}`, 0.9, "drift_detected"},
		{"empty → skipped", "", 0, "skipped"},
		{"whitespace only → skipped", "   \n\t  ", 0, "skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseDecisionFromJudgeJSON(c.json, c.conf)
			if got != c.want {
				t.Errorf("parseDecisionFromJudgeJSON(%q, %v) = %q, want %q",
					c.json, c.conf, got, c.want)
			}
		})
	}
}

// TestNewChecker_DefaultsToOff: NewChecker with Strictness=0 must
// default to StrictnessOff (matches the zero-value behavior the
// constructor comment promises).
func TestNewChecker_DefaultsToOff(t *testing.T) {
	c := NewChecker(nil, nil, 0)
	if c.Strictness != StrictnessOff {
		t.Errorf("NewChecker(st, nil, 0).Strictness = %v, want StrictnessOff", c.Strictness)
	}
	if c.Logger == nil {
		t.Errorf("NewChecker should default Logger to non-nil")
	}
	if c.Now == nil {
		t.Errorf("NewChecker should default Now to non-nil")
	}
}

// _ = store.Store import is referenced indirectly via Checker.Store;
// the blank keeps the import live for the test compilation surface
// when future tests add real Store calls (GetSpec paths).
var _ store.Store