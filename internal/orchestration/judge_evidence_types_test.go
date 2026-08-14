package orchestration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeArtifactReader is a minimal ArtifactReader for unit tests.
type fakeArtifactReader struct {
	lines []string
	sum   string
}

func (f *fakeArtifactReader) ReadLine(line int) (string, error) {
	if line < 1 || line > len(f.lines) {
		return "", errFake("line out of range")
	}
	return f.lines[line-1], nil
}

func (f *fakeArtifactReader) Sha256() string { return f.sum }
func (f *fakeArtifactReader) TotalLines() int { return len(f.lines) }

type errFake string

func (e errFake) Error() string { return string(e) }

func TestVerdictValue_IsValid(t *testing.T) {
	cases := []struct {
		in   VerdictValue
		want bool
	}{
		{VerdictAligned, true},
		{VerdictDriftDetected, true},
		{VerdictNeedsHuman, true},
		{VerdictValue("bogus"), false},
		{VerdictValue(""), false},
	}
	for _, c := range cases {
		if got := c.in.IsValid(); got != c.want {
			t.Errorf("VerdictValue(%q).IsValid() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestVerdictFromJSON_Aligned(t *testing.T) {
	raw := `{
		"verdict": "aligned",
		"confidence": 0.95,
		"evidence": [
			{"file": "src/foo.go", "line": 42, "quote": "hello world", "concern": "test"}
		],
		"reasoning": "All checks pass",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	v, err := VerdictFromJSON(raw)
	if err != nil {
		t.Fatalf("VerdictFromJSON: %v", err)
	}
	if v.Verdict != VerdictAligned {
		t.Errorf("Verdict = %q, want aligned", v.Verdict)
	}
	if v.Confidence != 0.95 {
		t.Errorf("Confidence = %f, want 0.95", v.Confidence)
	}
	if len(v.Evidence) != 1 {
		t.Fatalf("Evidence len = %d, want 1", len(v.Evidence))
	}
	if v.Evidence[0].File != "src/foo.go" || v.Evidence[0].Line != 42 || v.Evidence[0].Quote != "hello world" {
		t.Errorf("Evidence[0] = %+v", v.Evidence[0])
	}
}

func TestVerdictFromJSON_NeedsHuman_EmptyEvidence(t *testing.T) {
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
	v, err := VerdictFromJSON(raw)
	if err != nil {
		t.Fatalf("VerdictFromJSON: %v", err)
	}
	if v.Verdict != VerdictNeedsHuman {
		t.Errorf("Verdict = %q, want needs_human", v.Verdict)
	}
	if len(v.Evidence) != 0 {
		t.Errorf("Evidence len = %d, want 0", len(v.Evidence))
	}
}

func TestVerdictFromJSON_MalformedJSON(t *testing.T) {
	_, err := VerdictFromJSON("not json")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid verdict JSON") {
		t.Errorf("error message should mention invalid verdict JSON, got: %v", err)
	}
}

func TestVerdictFromJSON_InvalidVerdict(t *testing.T) {
	raw := `{
		"verdict": "bogus",
		"confidence": 0.5,
		"evidence": [],
		"reasoning": "test",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	_, err := VerdictFromJSON(raw)
	if err == nil {
		t.Fatal("expected error for invalid verdict enum")
	}
}

func TestVerdictFromJSON_ConfidenceOutOfRange(t *testing.T) {
	raw := `{
		"verdict": "aligned",
		"confidence": 1.5,
		"evidence": [],
		"reasoning": "test",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	_, err := VerdictFromJSON(raw)
	if err == nil {
		t.Fatal("expected error for confidence > 1")
	}
}

func TestVerdictFromJSON_EmptyReasoning(t *testing.T) {
	raw := `{
		"verdict": "aligned",
		"confidence": 0.5,
		"evidence": [{"file": "a", "line": 1, "quote": "x", "concern": "y"}],
		"reasoning": "",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	_, err := VerdictFromJSON(raw)
	if err == nil {
		t.Fatal("expected error for empty reasoning")
	}
}

func TestJudgeVerdict_MarshalJSON_RoundTrip(t *testing.T) {
	original := &JudgeVerdict{
		Verdict:    VerdictAligned,
		Confidence: 0.88,
		Evidence: []EvidenceItem{
			{File: "a.go", Line: 10, Quote: "verbatim", Concern: "test"},
		},
		Reasoning:  "ok",
		PersonaID:  "judge-logical",
		EvalType:   "drift_judge",
		HarnessID:  "opencode",
		ModelUsed:  "anthropic/claude-sonnet-4.5",
		Timestamp:  time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	roundTrip, err := VerdictFromJSON(string(data))
	if err != nil {
		t.Fatalf("VerdictFromJSON: %v", err)
	}
	if roundTrip.Verdict != original.Verdict {
		t.Errorf("Verdict mismatch: %q vs %q", roundTrip.Verdict, original.Verdict)
	}
	if roundTrip.Confidence != original.Confidence {
		t.Errorf("Confidence mismatch: %f vs %f", roundTrip.Confidence, original.Confidence)
	}
}
