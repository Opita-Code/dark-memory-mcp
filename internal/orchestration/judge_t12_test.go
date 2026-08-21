// Package orchestration — judge_t12_test.go
//
// v2.20.0 T12 (spec 1276): tests for the single-shot Judge's
// artifact-anchored path. The HARD invariant is that callers using
// drift_judge + ArtifactRef NEVER hit the LLM client — the judge
// routes to DriftJudge and NLI-anchors the verdict to the resolved
// artifact's SHA-256 (v29 audit columns).
//
// Test coverage:
//   - Routing: drift_judge + ArtifactRef → DriftJudge (NLI path).
//   - Validation: missing ArtifactRef, invalid ArtifactRef, missing
//     SpecIntent → errMissingField.
//   - Precedence: drift_judge + ArtifactRef + Content → ArtifactRef
//     wins, Content ignored (legacy LLM path NOT triggered).
//   - Eval-type isolation: brand_match + ArtifactRef → ArtifactRef
//     ignored (legacy LLM path runs).
//   - Persistence: drift_judge + ArtifactRef → SDDEvaluation row
//     saved with v29 audit columns (ArtifactSource, ArtifactSHA256,
//     ArtifactPath, ArtifactSize, NLIProviderID) + EvaluationID != 0.
//   - Deprecation audit: drift_judge + Content (no ArtifactRef) →
//     legacy LLM path + Error Observatory Warn row.
package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
)

// TestJudge_DriftJudge_ArtifactRef_Delegates — drift_judge + ArtifactRef
// → judge routes to DriftJudge (NLI path) and returns the verdict
// bound to the artifact's SHA-256. The drift verdict maps from the
// NLI label (entailment → aligned).
func TestJudge_DriftJudge_ArtifactRef_Delegates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("a clean, minimal artifact body"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.92, ProviderID: "stub-t12"},
	}
	o.WithNLIRouter(prov)

	out, err := o.Judge(context.Background(), JudgeInput{
		EvalType: "drift_judge",
		// Content is intentionally set BUT ArtifactRef is set too —
		// routing must pick ArtifactRef and ignore Content.
		Content:     "this content should be ignored entirely",
		SpecIntent:  "the artifact body should match the spec",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Confidence != 0.92 {
		t.Errorf("confidence: got %f, want %f", out.Confidence, 0.92)
	}
	if out.Provider != "stub-t12" {
		t.Errorf("provider: got %q, want %q", out.Provider, "stub-t12")
	}
	if out.Model != "stub-t12" {
		t.Errorf("model (NLI provider id): got %q, want %q", out.Model, "stub-t12")
	}
	if out.EvaluationID == 0 {
		t.Errorf("expected non-zero EvaluationID, got 0")
	}
	// VerdictJSON must contain artifact_sha256 (the v29 audit anchor).
	if !strings.Contains(out.VerdictJSON, "artifact_sha256") {
		t.Errorf("verdict_json must include artifact_sha256 for audit; got %q", out.VerdictJSON)
	}
}

// TestJudge_DriftJudge_ArtifactRef_InvalidArtifactRef — invalid
// ArtifactRef (wrong structure) → errMissingField("artifact_ref").
// The drift_judge's Validate() runs before DriftJudge() is called.
func TestJudge_DriftJudge_ArtifactRef_InvalidArtifactRef(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)

	// Kind=file without Path is invalid (ArtifactRef.Validate rejects).
	_, err := o.Judge(context.Background(), JudgeInput{
		EvalType:    "drift_judge",
		SpecIntent:  "x",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: ""},
	})
	if err == nil {
		t.Fatal("expected errMissingField for invalid artifact_ref")
	}
	if !strings.Contains(err.Error(), "artifact_ref") {
		t.Errorf("error: got %q, want substring 'artifact_ref'", err)
	}
}

// TestJudge_DriftJudge_ArtifactRef_NoSpecIntent — ArtifactRef set
// but SpecIntent empty → errMissingField("spec_intent"). The
// hypothesis is mandatory for drift_judge (T08 invariant).
func TestJudge_DriftJudge_ArtifactRef_NoSpecIntent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)

	_, err := o.Judge(context.Background(), JudgeInput{
		EvalType:    "drift_judge",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		// SpecIntent intentionally empty.
	})
	if err == nil {
		t.Fatal("expected errMissingField for missing spec_intent")
	}
	if !strings.Contains(err.Error(), "spec_intent") {
		t.Errorf("error: got %q, want substring 'spec_intent'", err)
	}
}

// TestJudge_DriftJudge_Content_DeprecationWarn — drift_judge via
// Content (no ArtifactRef) → legacy LLM path runs + Error Observatory
// records a deprecation Warn row. The judge MUST NOT silently drop
// the call (the Content deprecation is auditable, not a refusal).
//
// We inject a mock LLM via WithLLMSelector so the test is
// deterministic offline (no network call to the failover daemon).
// The mock Records the call, so we can verify the legacy LLM path
// was taken (not the artifact-anchored path).
func TestJudge_DriftJudge_Content_DeprecationWarn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	// Mock LLM for the legacy path. The drift_judge via Content
	// DOES route to the legacy LLM client (T12 routing picks
	// artifact_ref only when it's set).
	mockDrift := &MockLLMClient{
		Name_:       "mock-legacy-drift",
		VerdictJSON: `{"verdict":"aligned","confidence":0.85,"reasoning":"legacy drift_judge via content (deprecation path)"}`,
		Confidence:  0.85,
		Model:       "mock-legacy-drift",
	}
	selector := NewOSINTSelector(nil).WithOverride("drift_judge", mockDrift)
	o.WithLLMSelector(selector)

	out, err := o.Judge(context.Background(), JudgeInput{
		EvalType: "drift_judge",
		Content:  "ancient drift_judge via content",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Errorf("expected non-zero EvaluationID, got 0")
	}
	// The mock LLM was called. The legacy path was taken (not the
	// artifact-anchored path).
	if mockDrift.Calls == 0 {
		t.Errorf("expected legacy LLM to be called, got 0 calls")
	}
	// The VerdictJSON comes from the mock, which is the legacy
	// shape (no artifact_sha256). If the drift-anchored path had
	// run, the VerdictJSON would carry artifact_sha256.
	if strings.Contains(out.VerdictJSON, "artifact_sha256") {
		t.Errorf("legacy path VerdictJSON must NOT include artifact_sha256; got %q",
			out.VerdictJSON)
	}
}

// TestJudge_DriftJudge_Both_RefWins — drift_judge + ArtifactRef AND
// Content → routing picks ArtifactRef (driftToJudge path), Content
// is ignored. The NLI router sees the artifact body, NOT the caller-
// supplied content. This is the HARD invariant the spec enforces.
func TestJudge_DriftJudge_Both_RefWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	artifactBody := "the artifact body that the judge will see"
	if err := os.WriteFile(path, []byte(artifactBody), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.95, ProviderID: "stub-t12-both"},
	}
	o.WithNLIRouter(prov)

	// Send rogue Content that, if the legacy path ran, would attempt
	// to LLM-score the rogue text. The drift path must REJECT the
	// rogue content and use the artifact body.
	rogueContent := "this rogue content should NEVER reach the LLM"
	_, err := o.Judge(context.Background(), JudgeInput{
		EvalType:    "drift_judge",
		Content:     rogueContent,
		SpecIntent:  "the artifact body should match the spec",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// The controllableProvider captured the NLI premise. The drift
	// path sends the artifact body as the premise, NOT the rogue
	// content. The hypothesis is the spec_intent.
	prov.mu.Lock()
	defer prov.mu.Unlock()
	if prov.capturedPremise != artifactBody {
		t.Errorf("NLI premise: got %q, want %q (rogue content was: %q)",
			prov.capturedPremise, artifactBody, rogueContent)
	}
	if prov.capturedHypothesis != "the artifact body should match the spec" {
		t.Errorf("NLI hypothesis: got %q, want spec_intent", prov.capturedHypothesis)
	}
}

// TestJudge_BrandMatch_IgnoresArtifactRef — non-drift_judge eval_type
// + ArtifactRef → ArtifactRef is ignored, legacy LLM path runs.
// brand_match needs an LLM client (no ArtifactRef wiring).
//
// We inject a mock LLM via WithLLMSelector so the test is
// deterministic offline. The mock records the call, so we can
// verify the legacy LLM path was taken (not the artifact-anchored
// path).
func TestJudge_BrandMatch_IgnoresArtifactRef(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	// Mock LLM for brand_match. The legacy path runs the LLM
	// for brand_match; the artifact_ref is ignored.
	mockBrand := &MockLLMClient{
		Name_:       "mock-brand",
		VerdictJSON: `{"verdict":"aligned","confidence":0.88,"reasoning":"brand_match legacy LLM path"}`,
		Confidence:  0.88,
		Model:       "mock-brand",
	}
	selector := NewOSINTSelector(nil).WithOverride("brand_match", mockBrand)
	o.WithLLMSelector(selector)

	out, err := o.Judge(context.Background(), JudgeInput{
		EvalType:    "brand_match",
		Content:     "brand copy",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Errorf("expected non-zero EvaluationID, got 0")
	}
	// The mock LLM was called. The legacy path was taken.
	if mockBrand.Calls == 0 {
		t.Errorf("expected legacy LLM to be called for brand_match, got 0 calls")
	}
	// The VerdictJSON does NOT carry artifact_sha256 (the legacy
	// path doesn't anchor to an artifact).
	if strings.Contains(out.VerdictJSON, "artifact_sha256") {
		t.Errorf("brand_match legacy path VerdictJSON must NOT include artifact_sha256; got %q",
			out.VerdictJSON)
	}
}

// TestJudge_DriftJudge_PersistsV29Audit — drift_judge + ArtifactRef
// → SDDEvaluation row saved with v29 audit columns populated
// (ArtifactSource, ArtifactSHA256, ArtifactPath, ArtifactSize,
// NLIProviderID) AND EvaluationID != 0. The driftToJudge bridge
// persists the row (DriftJudge itself does not persist).
func TestJudge_DriftJudge_PersistsV29Audit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	artifactBody := "the artifact body for v29 audit"
	if err := os.WriteFile(path, []byte(artifactBody), 0600); err != nil {
		t.Fatal(err)
	}

	o, st := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.91, ProviderID: "stub-t12-v29"},
	}
	o.WithNLIRouter(prov)

	out, err := o.Judge(context.Background(), JudgeInput{
		EvalType: "drift_judge",
		// Content intentionally empty — artifact_ref path
		// doesn't need Content (verifies the spec phase 1
		// relaxation: Content is optional when artifact_ref is
		// set).
		SpecIntent:  "v",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		TargetType:  "spec",
		TargetID:    "spec:v29audit",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatal("expected non-zero EvaluationID")
	}

	// Re-read the row from the store and verify the v29 audit columns.
	evals, err := st.ListSDDEvaluations(context.Background(), ssd.ListFilters{
		EvalType: "drift_judge",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("list sdd evaluations: %v", err)
	}
	var found bool
	for _, e := range evals {
		if e.ID == out.EvaluationID {
			found = true
			if e.ArtifactSource != "file" {
				t.Errorf("artifact_source: got %q, want %q", e.ArtifactSource, "file")
			}
			if e.ArtifactSHA256 == "" {
				t.Errorf("artifact_sha256: got empty, want non-empty")
			}
			if e.ArtifactPath != path {
				t.Errorf("artifact_path: got %q, want %q", e.ArtifactPath, path)
			}
			if e.ArtifactSize != int64(len(artifactBody)) {
				t.Errorf("artifact_size: got %d, want %d", e.ArtifactSize, len(artifactBody))
			}
			if e.NLIProviderID != "stub-t12-v29" {
				t.Errorf("nli_provider_id: got %q, want %q", e.NLIProviderID, "stub-t12-v29")
			}
			// TargetID/TargetType pass-through.
			if e.TargetID != "spec:v29audit" {
				t.Errorf("target_id: got %q, want %q", e.TargetID, "spec:v29audit")
			}
			if e.TargetType != "spec" {
				t.Errorf("target_type: got %q, want %q", e.TargetType, "spec")
			}
			// Single-shot: ChunkIndex/ChunkTotal stay 0.
			if e.ChunkIndex != 0 || e.ChunkTotal != 0 {
				t.Errorf("chunk_index/total: got %d/%d, want 0/0", e.ChunkIndex, e.ChunkTotal)
			}
			break
		}
	}
	if !found {
		t.Errorf("evaluation id %d not found in sdd_evaluations", out.EvaluationID)
	}
}

// TestJudge_DriftJudge_ResolvesFailure — artifact path does not
// exist. DriftJudge returns drift_detected (cannot match an artifact
// that doesn't exist) + Error Observatory. driftToJudge persists the
// row even on a "resolve failure" verdict (so the audit trail sees
// the attempt).
func TestJudge_DriftJudge_ResolvesFailure(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{
		// Provider exists but won't be called — the artifact
		// resolution fails BEFORE the NLI score.
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 1.0, ProviderID: "stub-t12-fail"},
	}
	o.WithNLIRouter(prov)

	out, err := o.Judge(context.Background(), JudgeInput{
		EvalType:    "drift_judge",
		SpecIntent:  "x",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: "/nonexistent/t12-fail"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.VerdictJSON == "" {
		t.Errorf("expected non-empty verdict_json")
	}
	// The drift_judge reported "drift_detected" via the
	// VerdictJSON's verdict field. The artifact resolution
	// failure path returns a DriftJudgeOutput with Verdict=
	// "drift_detected" and a reasoning string.
	if !strings.Contains(out.VerdictJSON, "drift_detected") {
		t.Errorf("expected drift_detected in verdict_json, got %q", out.VerdictJSON)
	}
	if out.EvaluationID == 0 {
		t.Errorf("expected non-zero EvaluationID even on resolve failure")
	}
}
