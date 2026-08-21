// Package orchestration — drift_judge_test.go
//
// v2.20.0 T08 (spec 1276): tests for the artifact-anchored drift_judge
// pipeline. The HARD invariant is that the caller cannot influence
// the verdict by submitting arbitrary text — the judge ALWAYS
// resolves an ArtifactRef and scores the resolved bytes.
//
// Test coverage:
//
//   - Validation: empty ArtifactRef, missing spec_intent, wrong
//     eval_type, canary.
//   - Resolution: file, git_sha, spec_id, artifact_id (URL deferred
//     to T12 since harness URLFetcher is operator-wired).
//   - NLI label mapping: entailment → aligned, contradiction →
//     drift_detected, neutral → needs_human.
//   - Error classification: ErrInputTooLarge, ErrNoProvider,
//     ErrProviderBadResponse → verdict=needs_human + Error
//     Observatory.
//   - VerdictJSON shape: includes artifact_source, artifact_sha256,
//     provider_id, nli_label, nli_confidence.
package orchestration

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	sqlitestore "github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// TestDriftJudge_MissingArtifactRef — ArtifactRef is required. Empty
// or invalid → errMissingField. The caller cannot silently skip.
func TestDriftJudge_MissingArtifactRef(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	_, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		EvalType:   "drift_judge",
		SpecIntent: "artifact should match this spec",
	})
	if err == nil {
		t.Fatal("expected errMissingField for missing ArtifactRef")
	}
	if !strings.Contains(err.Error(), "artifact_ref") {
		t.Errorf("error: got %q, want substring 'artifact_ref'", err)
	}
}

// TestDriftJudge_MissingSpecIntent — SpecIntent is the hypothesis.
// Drift_judge requires it.
func TestDriftJudge_MissingSpecIntent(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	_, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		EvalType:    "drift_judge",
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: "/tmp/x"},
	})
	if err == nil {
		t.Fatal("expected error for missing spec_intent")
	}
}

// TestDriftJudge_InvalidEvalType — only "drift_judge" is accepted.
func TestDriftJudge_InvalidEvalType(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	_, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		EvalType:    "brand_match",
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: "/tmp/x"},
		SpecIntent:  "x",
	})
	if err == nil {
		t.Fatal("expected error for invalid eval_type")
	}
}

// TestDriftJudge_DefaultsEvalType — empty EvalType defaults to
// drift_judge.
func TestDriftJudge_DefaultsEvalType(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	// Skip: the validator requires a valid ArtifactRef. Provide
	// one that resolves to nothing (so we hit the resolve failure
	// path, which still tells us the eval_type defaulted correctly).
	_, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: "/nonexistent/abc"},
		SpecIntent:  "x",
	})
	if err != nil {
		// We expect a non-nil error path (the file does not exist),
		// which means validation passed.
		t.Fatalf("unexpected err: %v", err)
	}
}

// TestDriftJudge_ResolveFile_Entailment — the canonical happy path:
// the artifact body entails the spec intent → verdict=aligned.
func TestDriftJudge_ResolveFile_Entailment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("a clean, minimal artifact body"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.92, ProviderID: "stub"},
	})

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "the artifact body should match the spec",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Verdict != "aligned" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "aligned")
	}
	if out.Confidence != 0.92 {
		t.Errorf("confidence: got %f, want %f", out.Confidence, 0.92)
	}
	if out.ArtifactSHA256 == "" {
		t.Errorf("expected artifact sha256, got empty")
	}
	if out.ArtifactSource != "file" {
		t.Errorf("artifact source: got %q, want %q", out.ArtifactSource, "file")
	}
	if out.NLILabel != "entailment" {
		t.Errorf("nli label: got %q, want %q", out.NLILabel, "entailment")
	}
}

// TestDriftJudge_ResolveFile_Contradiction — verdict maps to
// drift_detected.
func TestDriftJudge_ResolveFile_Contradiction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		score: nli.Score{Label: nli.LabelContradiction, Confidence: 0.87, ProviderID: "stub"},
	})

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "this spec is long enough to pass principle 5 of self critique",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q (H6 principle 3 override: contradiction → needs_human, not drift_detected)", out.Verdict, "needs_human")
	}
	if !strings.Contains(out.CritiqueReason, "principle 3") {
		t.Errorf("expected CritiqueReason to mention 'principle 3'; got %q", out.CritiqueReason)
	}
}

// TestDriftJudge_ResolveFile_Neutral — verdict maps to needs_human.
func TestDriftJudge_ResolveFile_Neutral(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("z"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		score: nli.Score{Label: nli.LabelNeutral, Confidence: 0.55, ProviderID: "stub"},
	})
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "z",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "needs_human")
	}
}

// TestDriftJudge_ResolveFailure_ViaFileMissing — the artifact path
// does not exist. DriftJudge returns drift_detected (cannot match
// an artifact that doesn't exist) + Error Observatory.
func TestDriftJudge_ResolveFailure_ViaFileMissing(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: "/nonexistent/abc"},
		SpecIntent:  "x",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Verdict != "drift_detected" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "drift_detected")
	}
	if out.Reasoning == "" {
		t.Errorf("expected non-empty reasoning on resolve failure")
	}
}

// TestDriftJudge_NoProvider_NeedsHuman — when no provider is
// injected and the project has no NLIConfig, DriftJudge returns
// needs_human + Error Observatory.
func TestDriftJudge_NoProvider_NeedsHuman(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	// No WithNLIRouter; defaultDriftJudgeProvider returns an error
	// (no project NLIConfig). The drift_judge maps that to
	// needs_human.
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "y",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "needs_human")
	}
}

// TestDriftJudge_ScoreError_NoProvider — provider returns an error.
// The error is classified and the verdict is needs_human.
func TestDriftJudge_ScoreError_NoProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		err: nli.ErrNoProvider,
	})
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "y",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "needs_human")
	}
}

// TestDriftJudge_ScoreError_BadResponse — ErrProviderBadResponse
// is a contract bug. Drift_judge maps to needs_human.
func TestDriftJudge_ScoreError_BadResponse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		err: nli.ErrProviderBadResponse,
	})
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "y",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "needs_human")
	}
}

// TestDriftJudge_ResolveSpec — the spec_id kind resolves to the
// spec's spec block. Drift_judge then scores the spec body against
// the spec intent (which is the spec itself — invariantly entailment).
func TestDriftJudge_ResolveSpec(t *testing.T) {
	// Set up a SQLite-backed orchestrator with a saved spec.
	dir := t.TempDir()
	path := filepath.Join(dir, "drift.db")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         path,
		WALMode:     true,
		ForeignKeys: true,
	}
	st, err := testStoreOpen(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetActiveProject(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	// Persist a spec whose spec block is the spec intent.
	specID, err := st.SaveSpec(context.Background(), store.WriteContext{
		Actor: "test", WritePath: "SaveSpec",
	}, &vibeflow.Spec{
		VibeCase:  "C1",
		Spec:      "the artifact should match this spec",
		CreatedAt: "2026-08-20T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	o := newDriftJudgeTestOrchestratorWithStore(t, st)
	o.WithNLIRouter(&controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.95, ProviderID: "stub"},
	})

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindSpecID, SpecID: specID},
		SpecIntent:  "the artifact should match this spec",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "aligned" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "aligned")
	}
	if out.ArtifactSource != "spec_id" {
		t.Errorf("artifact source: got %q, want %q", out.ArtifactSource, "spec_id")
	}
}

// TestDriftJudge_NLIProjectWiring — when the project has an
// enabled NLIConfig, EnsureNLIRouter builds the provider from
// the config (cache + router + provider chain).
func TestDriftJudge_NLIProjectWiring(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drift.db")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         path,
		WALMode:     true,
		ForeignKeys: true,
	}
	st, err := testStoreOpen(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetActiveProject(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	// Create a project with NLIConfig enabled.
	if err := st.CreateProject(context.Background(), projectWithNLIConfig()); err != nil {
		t.Fatal(err)
	}

	artifactPath := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(artifactPath, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o := newDriftJudgeTestOrchestratorWithStore(t, st)
	// No WithNLIRouter — the project NLIConfig drives construction.
	o.WithNLIHTTPClient(stubHTTPClient())

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: artifactPath},
		SpecIntent:  "y",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// The artifact body is "x" and the spec intent is "y" — the
	// drift judge called the project-NLI-config provider. The
	// stub HTTP client returns 401 (no auth), which the provider
	// maps to ErrProviderUnavailable → the Router falls back →
	// both fail → verdict=needs_human.
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "needs_human")
	}
}

// TestDriftJudge_NLILabelMappingTable — sealed mapping: each
// canonical label maps to the expected verdict. Unknown labels
// map to needs_human (defensive).
func TestDriftJudge_NLILabelMappingTable(t *testing.T) {
	cases := []struct {
		label nli.Label
		want  string
	}{
		{nli.LabelEntailment, "aligned"},
		{nli.LabelContradiction, "drift_detected"},
		{nli.LabelNeutral, "needs_human"},
		{nli.Label("unknown"), "needs_human"},
		{nli.Label(""), "needs_human"},
	}
	for _, tc := range cases {
		got := nliLabelToDriftVerdict(tc.label)
		if got != tc.want {
			t.Errorf("label=%q: got %q, want %q", tc.label, got, tc.want)
		}
	}
}

// TestDriftJudge_VerdictJSONShape — the VerdictJSON has the
// canonical fields (verdict, confidence, artifact_source,
// artifact_sha256, provider_id, nli_label).
func TestDriftJudge_VerdictJSONShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		score: nli.Score{
			Label:      nli.LabelEntailment,
			Confidence: 0.95,
			ProviderID: "deberta-x",
			ModelRev:   "rev-1",
		},
	})
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "this spec is long enough to pass principle 5 of self critique",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{
		`"verdict":"aligned"`,
		`"artifact_source":"file"`,
		`"provider_id":"deberta-x"`,
		`"nli_label":"entailment"`,
		`"model_rev":"rev-1"`,
	} {
		if !strings.Contains(out.VerdictJSON, want) {
			t.Errorf("VerdictJSON missing %q", want)
		}
	}
}

// TestDriftJudge_NLICfgMissing_ReturnsNeedsHuman — when the project
// has no NLIConfig (or it's disabled), the default chain is
// errMissingField("project.nli_config ...") → needs_human. This
// matches the v2.20.0 contract: a project without NLIConfig
// registered MUST escalate to operator review.
func TestDriftJudge_NLICfgMissing_ReturnsNeedsHuman(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	// No WithNLIRouter, no project NLIConfig.
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "y",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "needs_human")
	}
}

// TestDriftJudge_ScoreError_InputTooLarge — ErrInputTooLarge.
// Verdict: needs_human (operator can shrink the artifact).
func TestDriftJudge_ScoreError_InputTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		err: nli.ErrInputTooLarge,
	})
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "y",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want %q", out.Verdict, "needs_human")
	}
}

// TestDriftJudge_VerifyCallerCannotInfluence — the architectural
// invariant: the caller passes Content, but the score is computed
// over the RESOLVED bytes, not the caller's words. The controllable
// provider captures the request and asserts the premise is the
// file body, not the spec intent.
func TestDriftJudge_VerifyCallerCannotInfluence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	expectedBody := "THE FILE BODY IS THIS STRING"
	if err := os.WriteFile(path, []byte(expectedBody), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 1.0, ProviderID: "stub"},
	}
	o.WithNLIRouter(prov)

	_, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "caller-controlled intent that should NOT be the premise",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prov.LastPremise() != expectedBody {
		t.Errorf("premise: got %q, want %q (the FILE body, not the caller's words)",
			prov.LastPremise(), expectedBody)
	}
	if prov.LastHypothesis() != "caller-controlled intent that should NOT be the premise" {
		t.Errorf("hypothesis: got %q, want caller-supplied spec intent", prov.LastHypothesis())
	}
}

// controllableProvider is a controllable nli.Provider for tests. It
// returns a fixed score OR a fixed error, and captures the inputs.
//
// v2.20.0 T09: added a mutex for the captured fields. T08's
// DriftJudge is single-shot, so the existing tests didn't race.
// T09's DriftJudgeConsensus runs N goroutines concurrently against
// the same provider, so all mutable fields must be guarded.
type controllableProvider struct {
	mu                  sync.Mutex
	score               nli.Score
	err                 error
	capturedPremises    []string // T09: slice for multi-shot capture
	capturedHypotheses  []string
	capturedPremise     string   // legacy (T08): single-shot capture
	capturedHypothesis  string   // legacy (T08)
	id_                 string
}

func (c *controllableProvider) Score(ctx context.Context, premise, hypothesis string) (nli.Score, error) {
	c.mu.Lock()
	c.capturedPremise = premise
	c.capturedHypothesis = hypothesis
	c.capturedPremises = append(c.capturedPremises, premise)
	c.capturedHypotheses = append(c.capturedHypotheses, hypothesis)
	err := c.err
	score := c.score
	c.mu.Unlock()
	if err != nil {
		return nli.Score{ProviderID: c.idOrDefault()}, err
	}
	return score, nil
}

func (c *controllableProvider) ID() string { return c.idOrDefault() }

func (c *controllableProvider) idOrDefault() string {
	if c.id_ != "" {
		return c.id_
	}
	if c.score.ProviderID != "" {
		return c.score.ProviderID
	}
	return "stub"
}

// LastPremise returns the last captured premise (T08 API preserved).
// Use lastPremiseLock for thread-safe access.
func (c *controllableProvider) LastPremise() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capturedPremise
}

func (c *controllableProvider) LastHypothesis() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capturedHypothesis
}

// helpers for the spec/artifact fixtures.

// newDriftJudgeTestOrchestrator spins up a minimal Orchestrator
// backed by SQLite in a temp dir. The active project is "default"
// (created automatically by the seed). No LLM client is wired — tests
// must inject what they need via WithNLIRouter / WithNLIHTTPClient.
func newDriftJudgeTestOrchestrator(t *testing.T) (*Orchestrator, store.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "drift.db")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         path,
		WALMode:     true,
		ForeignKeys: true,
	}
	st, err := testStoreOpen(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetActiveProject(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	o := newDriftJudgeTestOrchestratorWithStore(t, st)
	return o, st
}

// newDriftJudgeTestOrchestratorWithStore wraps an existing store.
// Tests that already have a store (e.g. for spec seeding) use this.
func newDriftJudgeTestOrchestratorWithStore(t *testing.T, st store.Store) *Orchestrator {
	t.Helper()
	// Empty safety holder — tests don't set canaries. We MUST
	// pass a non-nil holder because safety.Holder.Active() panics
	// on a nil receiver (calls into an atomic.Int32).
	o := New(st, &safety.Holder{})
	return o
}

// testStoreOpen is a thin wrapper around the sqlite store open.
// Defined as a closure so the test file can use the same pattern
// as publish_vibe_async_test.go.
func testStoreOpen(ctx context.Context, cfg store.Config) (store.Store, error) {
	return sqlitestore.Open(ctx, cfg)
}

// specWithIntent is a tiny helper that returns the intent string
// unchanged (the actual SaveSpec call is made inline in the test).
func specWithIntent(intent string) string {
	return intent
}

// silenceUnused ensures specWithIntent doesn't get auto-removed.
var _ = specWithIntent

// projectWithNLIConfig returns a project.Project with NLIConfig enabled.
// The config is the same shape as the DeBERTa default used by
// newProjectTestStore in tools/project_test.go.
func projectWithNLIConfig() *project.Project {
	return &project.Project{
		ProjectID:   "default",
		DisplayName: "default",
		NLIConfig: &project.NLIConfig{
			Enabled: true,
			Primary: project.NLIPrimary{
				ProviderID: "deberta-v3-large-mnli",
				Endpoint:   "http://localhost/deberta",
				AuthToken:  "tok",
				TimeoutMS:  5000,
				ModelRev:   "rev-1",
			},
			LatencyBudgetMS:    10000,
			MaxPremiseBytes:    65536,
			MaxHypothesisBytes: 8192,
			MaxCacheEntries:    0, // no cache: faster test
			CacheTTLSeconds:    86400,
		},
	}
}

// stubHTTPClient returns a no-op HTTP client. The 401 response maps
// to ErrProviderUnavailable in the DeBERTa provider.
func stubHTTPClient() nli.HFInferenceClient {
	return &http.Client{Transport: &countingTransport{}}
}

// silenceUnused ensures errors and project are imported.
var _ = errors.New
var _ = project.ErrNLIConfigInvalid

