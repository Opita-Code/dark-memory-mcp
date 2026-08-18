// Package drift — checker.go: the M6 drift interceptor.
//
// Checker.CheckArtifact runs drift_judge LLM-as-judge against the
// (spec, artifact) pair and returns a Verdict. The caller (the gate
// layer, policy.PostCheck) maps Verdict.Decision to ErrDriftAtWrite
// (strict mode) or log+continue (warn mode).
//
// # Atomicity contract (5A.vi, M6)
//   - CheckArtifact is ONE public method.
//   - DEPENDS on JudgeCaller interface (any type with Judge(ctx, in)
//     method — production uses *orchestration.Orchestrator; tests
//     use a MockLLMClient).
//   - DEPENDS on store.Store for GetSpec (to read the spec being
//     checked against).
//   - Strictness is read on every call (cheap enum lookup), not
//     captured at construction — operator can flip DARK_DRIFT_STRICTNESS
//     at runtime via setenv + process restart.
package drift

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/judgeparse"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Verdict is the result of one CheckArtifact call.
type Verdict struct {
	// Decision is one of "aligned" | "drift_detected" | "needs_human" |
	// "skipped" | "errored". Callers MUST switch on this string.
	Decision string `json:"decision"`

	// Confidence is the LLM-reported 0..1 score. 0 when Decision is
	// "skipped" or "errored".
	Confidence float32 `json:"confidence"`

	// Reasoning is the human-readable explanation. For
	// "skipped" / "errored" it carries the cause; for the other
	// three it carries the judge_reasoning (truncated by the LLM).
	Reasoning string `json:"reasoning,omitempty"`

	// EvaluationID is the ssd_evaluations row id written by Judge.
	// 0 when Decision is "skipped" or "errored".
	EvaluationID int64 `json:"evaluation_id,omitempty"`

	// StrictnessApplied is the Strictness value that was active
	// when the verdict was computed. Surfaced in the verdict so the
	// gate can confirm "warn mode was applied" without re-reading env.
	StrictnessApplied Strictness `json:"strictness_applied"`
}

// ArtifactInput is the minimal artifact description the Checker needs.
// Mirrors vibeflow.Artifact but only the fields actually consulted by
// drift_judge (URL + spec + body text).
type ArtifactInput struct {
	SpecID       int64
	ArtifactType string
	ArtifactURL  string
	Text         string
}

// JudgeInput is a copy of orchestration.JudgeInput's public shape.
// We don't import orchestration (would cycle through policy→orchestration
// in a future refactor) — the Checker accepts any JudgeCaller.
type JudgeInput struct {
	EvalType   string
	TargetType string
	TargetID   string
	Content    string
	Model      string
}

// JudgeOutput mirrors orchestration.JudgeOutput's public shape.
type JudgeOutput struct {
	EvaluationID int64
	VerdictJSON  string
	Confidence   float32
	Model        string
	Provider     string
}

// JudgeCaller is the minimal surface Checker needs from the orchestrator.
// Both *orchestration.Orchestrator (via server.DriftJudgeFromOrchestrator)
// and test mocks satisfy this.
type JudgeCaller interface {
	Judge(ctx context.Context, in JudgeInput) (*JudgeOutput, error)
}

// JudgeFunc adapts a plain function to JudgeCaller (t4, spec 1242).
// Boot paths wrap *orchestration.Orchestrator.Judge with it — the
// Orchestrator's own Judge signature uses orchestration.JudgeInput /
// orchestration.JudgeOutput, so an explicit conversion is required.
type JudgeFunc func(ctx context.Context, in JudgeInput) (*JudgeOutput, error)

// Judge implements JudgeCaller.
func (f JudgeFunc) Judge(ctx context.Context, in JudgeInput) (*JudgeOutput, error) {
	return f(ctx, in)
}

// Checker is the drift-at-write interceptor. Construct with
// NewChecker; call CheckArtifact on each artifact-creating tool.
type Checker struct {
	// Store is the data source. Required.
	Store store.Store
	// Judge runs drift_judge. Required. In production this is
	// *orchestration.Orchestrator (which satisfies JudgeCaller via
	// its Judge method).
	Judge JudgeCaller
	// Strictness controls off|warn|strict behavior.
	Strictness Strictness
	// Logger is the optional logger (nil = log.Default()).
	Logger *log.Logger
	// Now is the wall-clock source; nil = time.Now. Injected for tests
	// that want deterministic Content SHA computation.
	Now func() time.Time
}

// NewChecker constructs a Checker with required fields. Strictness
// defaults to StrictnessOff when left at zero value.
func NewChecker(st store.Store, judge JudgeCaller, strictness Strictness) *Checker {
	if strictness == 0 {
		strictness = StrictnessOff
	}
	return &Checker{
		Store:      st,
		Judge:      judge,
		Strictness: strictness,
		Logger:     log.Default(),
		Now:        func() time.Time { return time.Now().UTC() },
	}
}

// CheckArtifact runs drift_judge on the (spec, artifact) pair. Always
// returns a non-nil Verdict; on error the Verdict has Decision="errored"
// or Decision="skipped" depending on whether the error was fatal or
// recoverable (canary rejection, no-LLM-available, etc.).
//
// Decision semantics:
//
//   - "aligned"        — drift_judge reports aligned with conf >= 0.5
//   - "drift_detected" — drift_judge reports drift_detected
//   - "needs_human"    — drift_judge reports needs_human
//   - "skipped"        — StrictnessOff OR no spec OR no LLM OR
//     canary rejection. Verdict carries the
//     skip reason in Reasoning.
//   - "errored"        — judge call returned a non-recoverable error
//     (network, parse, internal).
//
// The CheckArtifact caller (policy.PostCheck) decides whether to
// refuse the save based on the Decision + Strictness pair.
func (c *Checker) CheckArtifact(ctx context.Context, in ArtifactInput) (*Verdict, error) {
	if c.Strictness == StrictnessOff {
		return &Verdict{
			Decision:          "skipped",
			Reasoning:         "drift strictness off (DARK_DRIFT_STRICTNESS=off)",
			StrictnessApplied: c.Strictness,
		}, nil
	}

	// No spec → skip. Drift is meaningless without a spec to compare
	// against. The caller can still validate the artifact via other
	// channels (brand_match, compliance_check in PublishVibe).
	if in.SpecID == 0 {
		return &Verdict{
			Decision:          "skipped",
			Reasoning:         "no spec_id; drift check requires an active spec",
			StrictnessApplied: c.Strictness,
		}, nil
	}

	// Build the judge content. The drift_judge prompt compares the
	// artifact body against its spec context; we include the type +
	// URL + body so the judge has full context. Matches the prompt
	// structure used by orchestration.PublishVibe's inline drift
	// check (consistency).
	content := buildJudgeContent(in)

	jOut, err := c.Judge.Judge(ctx, JudgeInput{
		EvalType:   "drift_judge",
		TargetType: "artifact",
		TargetID:   fmt.Sprintf("artifact_spec_%d", in.SpecID),
		Content:    content,
	})
	if err != nil {
		// Canary rejection (safety.ErrCanaryInPayload → store.ErrCanaryInPayload)
		// is a recoverable skip — the operator intentionally sent canary
		// in payload, the judge refuses. Drift check is irrelevant here.
		if strings.Contains(err.Error(), "canary") {
			return &Verdict{
				Decision:          "skipped",
				Reasoning:         fmt.Sprintf("canary in payload: %v", err),
				StrictnessApplied: c.Strictness,
			}, nil
		}
		// Judge unavailable (no API key, network error, transient
		// failure). The semantics differ by Strictness:
		//   - off / warn: return "skipped" verdict so the gate allows the
		//     save (the operator opted into permissive drift policy).
		//   - strict: propagate the error so the gate's strict-mode
		//     branch refuses the save. Per the design intent, an
		//     operator who enabled strict mode must not be able to
		//     bypass drift by disabling the LLM.
		if c.Logger != nil {
			c.Logger.Printf("drift: judge call failed: %v (strictness=%v)", err, c.Strictness)
		}
		if c.Strictness == StrictnessStrict {
			return &Verdict{
				Decision:          "errored",
				Reasoning:         fmt.Sprintf("judge unavailable under strict mode: %v", err),
				StrictnessApplied: c.Strictness,
			}, fmt.Errorf("drift: judge unavailable under strict mode: %w", err)
		}
		return &Verdict{
			Decision:          "skipped",
			Reasoning:         fmt.Sprintf("judge unavailable: %v", err),
			StrictnessApplied: c.Strictness,
		}, nil
	}

	decision := parseDecisionFromJudgeJSON(jOut.VerdictJSON, jOut.Confidence)
	return &Verdict{
		Decision:          decision,
		Confidence:        jOut.Confidence,
		Reasoning:         jOut.VerdictJSON,
		EvaluationID:      jOut.EvaluationID,
		StrictnessApplied: c.Strictness,
	}, nil
}

// buildJudgeContent formats the artifact input into a single text
// body for the LLM judge. Mirrors the prompt structure used by
// orchestration.PublishVibe's inline drift check (consistency).
func buildJudgeContent(in ArtifactInput) string {
	var b strings.Builder
	b.WriteString("ARTIFACT type=")
	b.WriteString(in.ArtifactType)
	b.WriteString(" url=")
	b.WriteString(in.ArtifactURL)
	b.WriteString(" spec_id=")
	b.WriteString(fmt.Sprintf("%d", in.SpecID))
	b.WriteString("\nBODY:\n")
	b.WriteString(strings.TrimSpace(in.Text))
	return b.String()
}

// parseDecisionFromJudgeJSON extracts the verdict string from the
// judge's structured response. t3 (spec 1242): delegates to the shared
// judgeparse package — the single canonical verdict parser used by the
// vibe pipeline, this gate, and judgment_history. Contract preserved:
// empty → skipped; structured JSON; bare-word legacy aliases
// (aligned/ok/pass/match → aligned, drift/fail/mismatch →
// drift_detected, human/review/uncertain → needs_human); conservative
// drift_detected on unknown (a misinterpreted verdict is more
// dangerous than a false positive); confidence floor 0.3.
func parseDecisionFromJudgeJSON(verdictJSON string, confidence float32) string {
	return judgeparse.ParseDecision(verdictJSON, confidence)
}

// Void-import guards. safety is imported so future strictness
// implementations can consult Safety directly (canary-aware strictness).
var _ = safety.Holder{}
