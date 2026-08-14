package orchestration

import (
	"errors"
	"strings"
	"testing"
)

// fakeReader is a stub ArtifactReader for validator tests.
type fakeReader struct {
	lines []string
	sum   string
}

func (f *fakeReader) ReadLine(line int) (string, error) {
	if line < 1 || line > len(f.lines) {
		return "", errors.New("out of range")
	}
	return f.lines[line-1], nil
}

func (f *fakeReader) Sha256() string     { return f.sum }
func (f *fakeReader) TotalLines() int   { return len(f.lines) }

// TestValidate_Aligned_MatchingQuote verifies the happy path.
func TestValidate_Aligned_MatchingQuote(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha", "beta", "gamma"}, sum: "abc"}
	raw := `{
		"verdict": "aligned",
		"confidence": 0.9,
		"evidence": [{"file": "a.md", "line": 2, "quote": "beta", "concern": "ok"}],
		"reasoning": "all checks pass",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	v, err := Validate(raw, r)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v.Verdict != VerdictAligned {
		t.Errorf("Verdict = %q, want aligned", v.Verdict)
	}
}

// TestValidate_QuoteMismatch rejects evidence where quote doesn't
// match artifact. This is the canonical "judge hallucinated" case.
func TestValidate_QuoteMismatch(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha", "beta"}, sum: "abc"}
	raw := `{
		"verdict": "aligned",
		"confidence": 0.9,
		"evidence": [{"file": "a.md", "line": 2, "quote": "WRONG", "concern": "ok"}],
		"reasoning": "all checks pass",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	_, err := Validate(raw, r)
	if err == nil {
		t.Fatal("expected error for quote mismatch")
	}
	if !errors.Is(err, ErrInvalidVerdict) {
		t.Errorf("expected ErrInvalidVerdict, got: %v", err)
	}
	if !strings.Contains(err.Error(), "WRONG") {
		t.Errorf("error should mention the cited quote, got: %v", err)
	}
}

// TestValidate_QuoteSubstringMatch allows substring match (LLM may
// quote a portion of the line).
func TestValidate_QuoteSubstringMatch(t *testing.T) {
	r := &fakeReader{lines: []string{"the quick brown fox jumps"}, sum: "abc"}
	raw := `{
		"verdict": "aligned",
		"confidence": 0.9,
		"evidence": [{"file": "a.md", "line": 1, "quote": "brown fox", "concern": "ok"}],
		"reasoning": "ok",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	v, err := Validate(raw, r)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v.Verdict != VerdictAligned {
		t.Errorf("Verdict = %q, want aligned", v.Verdict)
	}
}

// TestValidate_EmptyEvidence_Aligned rejected (must cite ≥1 quote).
func TestValidate_EmptyEvidence_Aligned(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha"}, sum: "abc"}
	raw := `{
		"verdict": "aligned",
		"confidence": 0.9,
		"evidence": [],
		"reasoning": "ok",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	_, err := Validate(raw, r)
	if err == nil {
		t.Fatal("expected error for empty evidence on aligned verdict")
	}
	if !errors.Is(err, ErrInvalidVerdict) {
		t.Errorf("expected ErrInvalidVerdict, got: %v", err)
	}
}

// TestValidate_EmptyEvidence_NeedsHuman accepted (judge couldn't
// ground a quote, so it escalated — that's the anti-hallucination
// anchor working correctly).
func TestValidate_EmptyEvidence_NeedsHuman(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha"}, sum: "abc"}
	raw := `{
		"verdict": "needs_human",
		"confidence": 0,
		"evidence": [],
		"reasoning": "insufficient information to ground verdict",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	v, err := Validate(raw, r)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v.Verdict != VerdictNeedsHuman {
		t.Errorf("Verdict = %q, want needs_human", v.Verdict)
	}
}

// TestValidate_LineOutOfRange rejects evidence citing a non-existent
// line.
func TestValidate_LineOutOfRange(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha"}, sum: "abc"}
	raw := `{
		"verdict": "aligned",
		"confidence": 0.9,
		"evidence": [{"file": "a.md", "line": 99, "quote": "alpha", "concern": "ok"}],
		"reasoning": "ok",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	_, err := Validate(raw, r)
	if err == nil {
		t.Fatal("expected error for out-of-range line")
	}
}

// TestValidateOrNeedsHuman_OnFailure_AlwaysNeedsHuman is the
// anti-hallucination escape hatch.
func TestValidateOrNeedsHuman_OnFailure_AlwaysNeedsHuman(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha"}, sum: "abc"}
	// Quote mismatch → fails validation → must return needs_human.
	raw := `{
		"verdict": "aligned",
		"confidence": 0.9,
		"evidence": [{"file": "a.md", "line": 1, "quote": "WRONG", "concern": "ok"}],
		"reasoning": "ok",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	v := ValidateOrNeedsHuman(raw, "judge-logical", "drift_judge", "opencode", "anthropic/claude-sonnet-4.5", r)
	if v.Verdict != VerdictNeedsHuman {
		t.Errorf("Verdict = %q, want needs_human", v.Verdict)
	}
	if !strings.Contains(v.Reasoning, "insufficient information") {
		t.Errorf("Reasoning should mention insufficient information, got: %q", v.Reasoning)
	}
}

// TestValidateOrNeedsHuman_OnSuccess_PreservesAligned verifies the
// happy path passes through.
func TestValidateOrNeedsHuman_OnSuccess_PreservesAligned(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha", "beta"}, sum: "abc"}
	raw := `{
		"verdict": "aligned",
		"confidence": 0.9,
		"evidence": [{"file": "a.md", "line": 2, "quote": "beta", "concern": "ok"}],
		"reasoning": "ok",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	v := ValidateOrNeedsHuman(raw, "judge-logical", "drift_judge", "opencode", "anthropic/claude-sonnet-4.5", r)
	if v.Verdict != VerdictAligned {
		t.Errorf("Verdict = %q, want aligned", v.Verdict)
	}
	if v.Reasoning != "ok" {
		t.Errorf("Reasoning = %q, want %q", v.Reasoning, "ok")
	}
}

// TestValidateOrNeedsHuman_MalformedJSON_AlwaysNeedsHuman verifies
// that completely malformed JSON is converted to needs_human.
func TestValidateOrNeedsHuman_MalformedJSON_AlwaysNeedsHuman(t *testing.T) {
	r := &fakeReader{lines: []string{"alpha"}, sum: "abc"}
	v := ValidateOrNeedsHuman("not json at all", "judge-logical", "drift_judge", "opencode", "anthropic/claude-sonnet-4.5", r)
	if v.Verdict != VerdictNeedsHuman {
		t.Errorf("Verdict = %q, want needs_human", v.Verdict)
	}
	if !strings.Contains(v.Reasoning, "insufficient information") {
		t.Errorf("Reasoning should mention insufficient information, got: %q", v.Reasoning)
	}
}
