package server

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/drift"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
)

// orchJudge is the minimal Judge surface DriftJudgeFromOrchestrator
// needs from *orchestration.Orchestrator. Declared as an interface so
// the adapter is unit-testable with a fake (t4, spec 1242).
type orchJudge interface {
	Judge(ctx context.Context, in orchestration.JudgeInput) (*orchestration.JudgeOutput, error)
}

// DriftJudgeFromOrchestrator adapts the Orchestrator's Judge method to
// the drift.JudgeCaller surface (t4, spec 1242 — M6 drift-at-write
// wiring). The Orchestrator's Judge signature uses
// orchestration.JudgeInput / orchestration.JudgeOutput; drift defines
// structurally-identical copies of its own (deliberately, to avoid a
// policy→orchestration cycle). This converter is the single translation
// point used by both boot paths (cmd/dark-mem-mcp/legacy_main.go and
// cmd/dark-mem-mcp-daemon/main.go).
func DriftJudgeFromOrchestrator(o orchJudge) drift.JudgeCaller {
	return drift.JudgeFunc(func(ctx context.Context, in drift.JudgeInput) (*drift.JudgeOutput, error) {
		out, err := o.Judge(ctx, convertDriftToOrchInput(in))
		if err != nil {
			return nil, err
		}
		return convertOrchToDriftOutput(out), nil
	})
}

// convertDriftToOrchInput maps drift.JudgeInput onto orchestration's
// structurally-identical shape. Extracted for unit testing.
func convertDriftToOrchInput(in drift.JudgeInput) orchestration.JudgeInput {
	return orchestration.JudgeInput{
		EvalType:   in.EvalType,
		TargetType: in.TargetType,
		TargetID:   in.TargetID,
		Content:    in.Content,
		Model:      in.Model,
	}
}

// convertOrchToDriftOutput maps orchestration.JudgeOutput onto drift's
// structurally-identical shape. Extracted for unit testing.
func convertOrchToDriftOutput(out *orchestration.JudgeOutput) *drift.JudgeOutput {
	return &drift.JudgeOutput{
		EvaluationID: out.EvaluationID,
		VerdictJSON:  out.VerdictJSON,
		Confidence:   out.Confidence,
		Model:        out.Model,
		Provider:     out.Provider,
	}
}
