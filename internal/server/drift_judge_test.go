package server

import (
	"context"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/drift"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
)

// fakeJudge implements the minimal orchJudge surface so the real
// DriftJudgeFromOrchestrator can be exercised without constructing a
// full Orchestrator (t4, spec 1242).
type fakeJudge struct {
	calls int
	last  orchestration.JudgeInput
	err   error
}

func (j *fakeJudge) Judge(ctx context.Context, in orchestration.JudgeInput) (*orchestration.JudgeOutput, error) {
	j.calls++
	j.last = in
	if j.err != nil {
		return nil, j.err
	}
	return &orchestration.JudgeOutput{
		EvaluationID: 42,
		VerdictJSON:  `{"verdict":"aligned"}`,
		Confidence:   0.91,
		Model:        "fake-model",
		Provider:     "fake",
	}, nil
}

func TestConvertDriftToOrchInput(t *testing.T) {
	got := convertDriftToOrchInput(drift.JudgeInput{
		EvalType:   "drift_judge",
		TargetType: "artifact",
		TargetID:   "artifact_1174",
		Content:    "artifact body",
		Model:      "override-model",
	})
	want := orchestration.JudgeInput{
		EvalType:   "drift_judge",
		TargetType: "artifact",
		TargetID:   "artifact_1174",
		Content:    "artifact body",
		Model:      "override-model",
	}
	if got != want {
		t.Fatalf("convertDriftToOrchInput = %+v, want %+v", got, want)
	}
}

func TestConvertOrchToDriftOutput(t *testing.T) {
	got := convertOrchToDriftOutput(&orchestration.JudgeOutput{
		EvaluationID: 42,
		VerdictJSON:  `{"verdict":"aligned"}`,
		Confidence:   0.91,
		Model:        "fake-model",
		Provider:     "fake",
	})
	want := &drift.JudgeOutput{
		EvaluationID: 42,
		VerdictJSON:  `{"verdict":"aligned"}`,
		Confidence:   0.91,
		Model:        "fake-model",
		Provider:     "fake",
	}
	if *got != *want {
		t.Fatalf("convertOrchToDriftOutput = %+v, want %+v", *got, *want)
	}
}

func TestDriftJudgeFromOrchestratorAdapter(t *testing.T) {
	fake := &fakeJudge{}
	adapter := DriftJudgeFromOrchestrator(fake)

	out, err := adapter.Judge(context.Background(), drift.JudgeInput{
		EvalType:   "drift_judge",
		TargetType: "artifact",
		TargetID:   "artifact_1174",
		Content:    "artifact body",
		Model:      "override-model",
	})
	if err != nil {
		t.Fatalf("Judge error: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1", fake.calls)
	}
	if fake.last.EvalType != "drift_judge" || fake.last.TargetType != "artifact" ||
		fake.last.TargetID != "artifact_1174" || fake.last.Content != "artifact body" ||
		fake.last.Model != "override-model" {
		t.Fatalf("input not passed through: %+v", fake.last)
	}
	if out.EvaluationID != 42 || out.Confidence != 0.91 || out.Provider != "fake" ||
		out.Model != "fake-model" || out.VerdictJSON != `{"verdict":"aligned"}` {
		t.Fatalf("output not mapped: %+v", out)
	}

	// Error propagation.
	fake.err = errors.New("boom")
	if _, err := adapter.Judge(context.Background(), drift.JudgeInput{}); err == nil {
		t.Fatal("expected error propagation, got nil")
	}
}
