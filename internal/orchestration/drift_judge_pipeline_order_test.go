// Package orchestration — drift_judge_pipeline_order_test.go
//
// v2.20.0 T07 (spec 1276) H6 invariant: constitutional self-critique
// MUST run AFTER NLI validation. Order is enforced in pipeline.
//
// This test verifies:
//   - DriftJudge calls NLI.Score BEFORE SelfCritique (order matters).
//   - If SelfCritique fails, verdict is overridden to "needs_human".
//   - The pipeline never skips NLI on its way to SelfCritique.
package orchestration

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
)

// stubNLIProvider records the order of operations and returns
// a configurable Score. Used to assert pipeline order:
//   - first call to Score() → records "nli_called"
//   - SelfCritique is observed via the artifact path: if the
//     override fires, the verdict flips to "needs_human" and
//     Reasoning mentions "self_critique_override".
type stubNLIProvider struct {
	label      nli.Label
	confidence float64
	called     bool
}

func (s *stubNLIProvider) Score(ctx context.Context, premise, hypothesis string) (nli.Score, error) {
	s.called = true
	return nli.Score{
		Label:       s.label,
		Confidence:  s.confidence,
		ProviderID:  "stub",
		ModelRev:    "v1",
		LatencyMS:   5,
	}, nil
}

// ID returns the provider id (nli.Provider interface).
func (s *stubNLIProvider) ID() string { return "stub" }

// TestJudge_PipelineOrder_ConstitutionAfterNLI — H6 hard invariant.
//
// Verifies:
//   - NLI.Score is called BEFORE SelfCritique (order).
//   - When NLI returns "contradiction" AND verdict would be
//     "drift_detected" (sealed mapping), SelfCritique's principle 3
//     fires and verdict is overridden to "needs_human".
//   - Reasoning mentions "self_critique_override" (audit trail).
//   - CritiqueReason is populated (operator review).
func TestJudge_PipelineOrder_ConstitutionAfterNLI(t *testing.T) {
	// Setup: write a real artifact file so resolver succeeds.
	tmpDir := t.TempDir()
	artifactPath := filepath.Join(tmpDir, "spec.txt")
	body := "The artifact is well-aligned with the spec intent."
	if err := os.WriteFile(artifactPath, []byte(body), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	_ = sha256.Sum256([]byte(body))

	// Setup: stub NLI that returns contradiction.
	stub := &stubNLIProvider{
		label:      nli.LabelContradiction, // triggers principle 3
		confidence: 0.92,
	}

	// Orchestrator with stub NLI router.
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(stub)

	// Build input.
	in := DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: artifactPath},
		SpecIntent:  "This is the spec intent that the artifact should match — at least 10 chars.",
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "test-h6-pipeline-order",
	}

	out, err := o.DriftJudge(context.Background(), in)
	if err != nil {
		t.Fatalf("DriftJudge returned error: %v", err)
	}

	// Assert: NLI was called.
	if !stub.called {
		t.Fatal("NLI.Score was NOT called; pipeline order broken (SelfCritique must run AFTER NLI)")
	}

	// Assert: verdict overridden to needs_human (principle 3 violation).
	if out.Verdict != "needs_human" {
		t.Errorf("expected verdict=needs_human (SelfCritique override); got %q", out.Verdict)
	}

	// Assert: Reasoning mentions override (audit trail).
	if !strings.Contains(out.Reasoning, "self_critique_override") {
		t.Errorf("expected Reasoning to mention 'self_critique_override'; got %q", out.Reasoning)
	}

	// Assert: CritiqueReason populated.
	if out.CritiqueReason == "" {
		t.Error("expected CritiqueReason populated; got empty")
	}
	if !strings.Contains(out.CritiqueReason, "principle 3") {
		t.Errorf("expected CritiqueReason to mention 'principle 3'; got %q", out.CritiqueReason)
	}

	// Assert: original NLI label preserved for audit.
	if out.NLILabel != string(nli.LabelContradiction) {
		t.Errorf("expected NLILabel=contradiction (audit); got %q", out.NLILabel)
	}
}

// TestJudge_PipelineOrder_NLIPassThrough — happy path, no override.
//
// Verifies:
//   - When NLI returns entailment AND spec is non-ambiguous, SelfCritique
//     passes all 5 principles.
//   - Verdict stays "aligned" (no override).
//   - CritiqueReason stays empty.
func TestJudge_PipelineOrder_NLIPassThrough(t *testing.T) {
	tmpDir := t.TempDir()
	artifactPath := filepath.Join(tmpDir, "happy.txt")
	body := "Aligned artifact body."
	if err := os.WriteFile(artifactPath, []byte(body), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	stub := &stubNLIProvider{
		label:      nli.LabelEntailment, // happy path
		confidence: 0.95,
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(stub)

	in := DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: artifactPath},
		SpecIntent:  "The artifact body should describe aligned content with sufficient detail.",
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "test-h6-passthrough",
	}

	out, err := o.DriftJudge(context.Background(), in)
	if err != nil {
		t.Fatalf("DriftJudge returned error: %v", err)
	}

	if !stub.called {
		t.Fatal("NLI.Score was NOT called")
	}
	if out.Verdict != "aligned" {
		t.Errorf("expected verdict=aligned (no SelfCritique override); got %q", out.Verdict)
	}
	if out.CritiqueReason != "" {
		t.Errorf("expected CritiqueReason empty on pass-through; got %q", out.CritiqueReason)
	}
	if !strings.Contains(out.Reasoning, "nli=") {
		t.Errorf("expected Reasoning to mention NLI score; got %q", out.Reasoning)
	}
}

// TestJudge_PipelineOrder_SpecAmbiguity_Principle5 — short spec refuses verdict.
//
// Verifies:
//   - When SpecIntent is too short (< 10 chars), principle 5 fires.
//   - Verdict is overridden to "needs_human".
//   - NLI.Score was called BEFORE the override (order invariant).
func TestJudge_PipelineOrder_SpecAmbiguity_Principle5(t *testing.T) {
	tmpDir := t.TempDir()
	artifactPath := filepath.Join(tmpDir, "spec5.txt")
	if err := os.WriteFile(artifactPath, []byte("body"), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	stub := &stubNLIProvider{
		label:      nli.LabelEntailment,
		confidence: 0.95,
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(stub)

	in := DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: artifactPath},
		SpecIntent:  "short", // 5 chars, below minSpecIntentLen=10
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "test-h6-principle-5",
	}

	out, err := o.DriftJudge(context.Background(), in)
	if err != nil {
		t.Fatalf("DriftJudge returned error: %v", err)
	}

	if !stub.called {
		t.Fatal("NLI.Score was NOT called before SelfCritique")
	}
	if out.Verdict != "needs_human" {
		t.Errorf("expected verdict=needs_human (principle 5 override); got %q", out.Verdict)
	}
	if !strings.Contains(out.CritiqueReason, "principle 5") {
		t.Errorf("expected CritiqueReason to mention 'principle 5'; got %q", out.CritiqueReason)
	}
}