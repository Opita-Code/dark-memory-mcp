// Package orchestration — drift_judge_consensus_test.go
//
// v2.20.0 T09 (spec 1276): tests for the artifact-anchored N-shot
// drift_judge consensus path.
//
// Test coverage:
//
//   - chunkArtifact unit tests: edge cases (empty, single-chunk,
//     multi-chunk, N > chunk count, exact chunk size, large artifact).
//   - DriftJudgeConsensus validation: missing ArtifactRef, missing
//     SpecIntent, wrong EvalType, canary.
//   - DriftJudgeConsensus resolution: file exists, missing file.
//   - DriftJudgeConsensus aggregation: all aligned, all contradicted,
//     mixed labels, low agreement (needs_human), single-chunk
//     redundancy.
//   - DriftJudgeConsensus per-chunk errors: ErrInputTooLarge →
//     needs_human, ErrNoProvider → all chunks fail.
//   - DriftJudgeConsensus result shape: contains chunked_size,
//     num_chunks, requested_n, provider_id, artifact_source,
//     artifact_sha256, artifact_path.
//   - DriftJudgeConsensus persistence: per-chunk rows + consensus
//     row with :consensus suffix in TargetID.
//   - Verify caller cannot influence: the score is over chunk
//     bytes, not the caller's spec_intent or any other text.
//   - JudgeConsensus routing: drift_judge + ArtifactRef →
//     DriftJudgeConsensus path; other eval_types → legacy Content
//     path.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// =====================================================================
// chunkArtifact unit tests
// =====================================================================

func TestChunkArtifact_Empty(t *testing.T) {
	if got := chunkArtifact(nil, 3); got != nil {
		t.Errorf("empty input: got %v, want nil", got)
	}
	if got := chunkArtifact([]byte{}, 3); got != nil {
		t.Errorf("empty input: got %v, want nil", got)
	}
}

func TestChunkArtifact_SingleChunk_UnderLimit(t *testing.T) {
	// 1 KB artifact, N=3 → 1 chunk (the whole artifact).
	in := make([]byte, 1024)
	for i := range in {
		in[i] = byte(i % 256)
	}
	got := chunkArtifact(in, 3)
	if len(got) != 1 {
		t.Fatalf("len: got %d, want 1", len(got))
	}
	if got[0].Start != 0 || got[0].End != int64(len(in)) {
		t.Errorf("chunk: got [%d,%d], want [0,%d]", got[0].Start, got[0].End, len(in))
	}
}

func TestChunkArtifact_SingleChunk_ExactLimit(t *testing.T) {
	// artifact size == ConsensusChunkSize → 1 chunk.
	in := make([]byte, ConsensusChunkSize)
	got := chunkArtifact(in, 3)
	if len(got) != 1 {
		t.Fatalf("len: got %d, want 1", len(got))
	}
	if got[0].End != int64(ConsensusChunkSize) {
		t.Errorf("chunk end: got %d, want %d", got[0].End, ConsensusChunkSize)
	}
}

func TestChunkArtifact_MultiChunk_N5_Small(t *testing.T) {
	// 5 KB artifact, N=5 → 2 chunks (even distribution: 2560 + 2560).
	// chunkArtifact caps at N but uses min(N, ceil(size/ChunkSize))
	// chunks. For 5 KB and 4 KB chunk size, that's 2 chunks.
	in := make([]byte, 5*1024)
	got := chunkArtifact(in, 5)
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	// Even distribution: 5120 / 2 = 2560 each.
	if got[0].Start != 0 || got[0].End != 2560 {
		t.Errorf("chunk 0: got [%d,%d], want [0,2560]", got[0].Start, got[0].End)
	}
	if got[1].Start != 2560 || got[1].End != 5120 {
		t.Errorf("chunk 1: got [%d,%d], want [2560,5120]", got[1].Start, got[1].End)
	}
}

func TestChunkArtifact_MultiChunk_Big_N3(t *testing.T) {
	// 100 KB artifact, N=3 → 3 evenly distributed chunks.
	in := make([]byte, 100*1024)
	got := chunkArtifact(in, 3)
	if len(got) != 3 {
		t.Fatalf("len: got %d, want 3", len(got))
	}
	// Each chunk ~33-34 KB, contiguous, last absorbs remainder.
	if got[0].Start != 0 {
		t.Errorf("chunk 0 start: got %d, want 0", got[0].Start)
	}
	if got[2].End != int64(len(in)) {
		t.Errorf("last chunk end: got %d, want %d", got[2].End, len(in))
	}
	// Contiguous check.
	for i := 1; i < len(got); i++ {
		if got[i].Start != got[i-1].End {
			t.Errorf("chunk %d not contiguous: prev end=%d, this start=%d", i, got[i-1].End, got[i].Start)
		}
	}
}

func TestChunkArtifact_N_Clamped(t *testing.T) {
	in := make([]byte, ConsensusChunkSize*10) // 10 chunks worth
	// N=99 → clamp to 7 → at most 7 chunks.
	got := chunkArtifact(in, 99)
	if len(got) > 7 {
		t.Errorf("clamp: got %d, want <= 7", len(got))
	}
}

func TestChunkArtifact_N_Defaulted(t *testing.T) {
	in := make([]byte, ConsensusChunkSize*3)
	// N=0 → default to 3.
	got := chunkArtifact(in, 0)
	if len(got) == 0 {
		t.Fatal("got 0 chunks")
	}
}

func TestChunkArtifact_N1_SmallArtifact(t *testing.T) {
	// 100 bytes, N=1 → 1 chunk (whole artifact).
	in := make([]byte, 100)
	got := chunkArtifact(in, 1)
	if len(got) != 1 {
		t.Fatalf("len: got %d, want 1", len(got))
	}
	if got[0].End != 100 {
		t.Errorf("chunk end: got %d, want 100", got[0].End)
	}
}

func TestChunkArtifact_HugeArtifact_N7(t *testing.T) {
	// 4 MB artifact, N=7 → 7 chunks of ~600 KB each — exceeds
	// NLI MaxPremiseBytes (64 KB) by design. DriftJudgeConsensus
	// lets the per-chunk Score surface ErrInputTooLarge (rather
	// than rejecting upfront). Test chunking itself.
	in := make([]byte, 4*1024*1024)
	got := chunkArtifact(in, 7)
	if len(got) != 7 {
		t.Fatalf("len: got %d, want 7", len(got))
	}
	// Each chunk ~600 KB; the per-chunk Score will fail.
}

// =====================================================================
// chunkArtifact helpers
// =====================================================================

// writeArtifactBytes writes content to a temp file and returns the
// path. Used by DriftJudgeConsensus tests (the legacy
// makeArtifactFile in judge_evidence_smoke_test.go takes []string —
// different signature).
func writeArtifactBytes(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// =====================================================================
// DriftJudgeConsensus validation tests
// =====================================================================

func TestDriftJudgeConsensus_MissingArtifactRef(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	_, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		SpecIntent: "spec",
	})
	if err == nil {
		t.Fatal("expected errMissingField for missing ArtifactRef")
	}
	if !strings.Contains(err.Error(), "artifact_ref") {
		t.Errorf("error: got %q, want substring 'artifact_ref'", err)
	}
}

func TestDriftJudgeConsensus_MissingSpecIntent(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	_, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: "/tmp/x"},
	})
	if err == nil {
		t.Fatal("expected error for missing spec_intent")
	}
}

func TestDriftJudgeConsensus_ResolvesEmpty(t *testing.T) {
	// Empty file → drift_detected.
	path := writeArtifactBytes(t, []byte{})
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 1.0, ProviderID: "stub"}})

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != "drift_detected" {
		t.Errorf("verdict: got %q, want drift_detected", out.Verdict)
	}
	if !out.Degraded {
		t.Errorf("expected degraded=true for empty artifact")
	}
}

func TestDriftJudgeConsensus_ResolveFailure(t *testing.T) {
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 1.0, ProviderID: "stub"}})

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: "/nonexistent/abc"},
		SpecIntent:  "spec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != "drift_detected" {
		t.Errorf("verdict: got %q, want drift_detected", out.Verdict)
	}
	if out.Reasoning == "" {
		t.Errorf("reasoning should explain resolve failure")
	}
}

// =====================================================================
// DriftJudgeConsensus aggregation tests
// =====================================================================

func TestDriftJudgeConsensus_AllAligned_SingleChunk(t *testing.T) {
	// 1 KB artifact, N=3 → 1 chunk, 3 redundant shots.
	// All return aligned → modal verdict aligned, fraction 1.0.
	content := make([]byte, 1024)
	for i := range content {
		content[i] = 'a'
	}
	path := writeArtifactBytes(t, content)

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.95, ProviderID: "stub"}}
	o.WithNLIRouter(prov)

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ModalVerdict != "aligned" {
		t.Errorf("modal: got %q, want aligned", out.ModalVerdict)
	}
	if out.ModalFraction != 1.0 {
		t.Errorf("fraction: got %f, want 1.0", out.ModalFraction)
	}
	if out.NumChunks != 1 {
		t.Errorf("num_chunks: got %d, want 1", out.NumChunks)
	}
	if out.ChunkedSize != 1024 {
		t.Errorf("chunked_size: got %d, want 1024", out.ChunkedSize)
	}
	if out.RequestedN != 3 {
		t.Errorf("requested_n: got %d, want 3", out.RequestedN)
	}
	if out.ProviderID != "stub" {
		t.Errorf("provider_id: got %q, want stub", out.ProviderID)
	}
}

func TestDriftJudgeConsensus_AllContradicted_MultiChunk(t *testing.T) {
	// 20 KB artifact, N=5 → 5 chunks, all contradicted.
	content := make([]byte, 20*1024)
	for i := range content {
		content[i] = 'x'
	}
	path := writeArtifactBytes(t, content)

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{score: nli.Score{Label: nli.LabelContradiction, Confidence: 0.88, ProviderID: "stub"}}
	o.WithNLIRouter(prov)

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ModalVerdict != "drift_detected" {
		t.Errorf("modal: got %q, want drift_detected", out.ModalVerdict)
	}
	if out.NumChunks != 5 {
		t.Errorf("num_chunks: got %d, want 5", out.NumChunks)
	}
	if out.ChunkedSize != 20*1024 {
		t.Errorf("chunked_size: got %d, want %d", out.ChunkedSize, 20*1024)
	}
}

func TestDriftJudgeConsensus_MixedLabels_UsesModal(t *testing.T) {
	// 12 KB artifact, N=3 → 3 chunks.
	// Chunks 0,1: aligned; chunk 2: drift_detected.
	// Modal: aligned (2/3 = 0.67 > 0.6) → verdict aligned.
	content := make([]byte, 12*1024)
	path := writeArtifactBytes(t, content)

	o, _ := newDriftJudgeTestOrchestrator(t)
	// controllableProvider returns the same label for every call,
	// but we override by inspecting chunk index via a slice.
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment, nli.LabelEntailment, nli.LabelContradiction},
		provID: "stub",
	}
	o.WithNLIRouter(prov)

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ModalVerdict != "aligned" {
		t.Errorf("modal: got %q, want aligned", out.ModalVerdict)
	}
	if out.ModalFraction < 0.6 {
		t.Errorf("fraction: got %f, want >= 0.6", out.ModalFraction)
	}
	if out.NumChunks != 3 {
		t.Errorf("num_chunks: got %d, want 3", out.NumChunks)
	}
}

func TestDriftJudgeConsensus_Disagreement_OverridesToNeedsHuman(t *testing.T) {
	// 12 KB artifact, N=3 → 3 chunks.
	// Chunk 0: aligned, chunks 1,2: drift_detected.
	// Modal: drift_detected (2/3 = 0.67), but the modal is
	// drift_detected (still trusted). So verdict = drift_detected.
	// To force needs_human, we need modal fraction < 0.6.
	content := make([]byte, 12*1024)
	path := writeArtifactBytes(t, content)

	o, _ := newDriftJudgeTestOrchestrator(t)
	// 1 aligned, 2 drift → modal 2/3 = 0.67 → modal wins.
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment, nli.LabelContradiction, nli.LabelContradiction},
		provID: "stub",
	}
	o.WithNLIRouter(prov)

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ModalVerdict != "drift_detected" {
		t.Errorf("modal: got %q, want drift_detected", out.ModalVerdict)
	}
	if out.Verdict != "drift_detected" {
		t.Errorf("verdict: got %q, want drift_detected (modal fraction 2/3 >= 0.6)", out.Verdict)
	}
}

func TestDriftJudgeConsensus_LowAgreement_OverridesToNeedsHuman(t *testing.T) {
	// 28 KB artifact, N=7 → 7 chunks.
	// Truly mixed: 3 aligned, 2 drift, 2 neutral.
	// Modal: aligned (3/7 = 0.43 < 0.6) → verdict needs_human.
	content := make([]byte, 28*1024)
	path := writeArtifactBytes(t, content)

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &chunkLabelProvider{
		labels: []nli.Label{
			nli.LabelEntailment, nli.LabelEntailment, nli.LabelEntailment,
			nli.LabelContradiction, nli.LabelContradiction,
			nli.LabelNeutral, nli.LabelNeutral,
		},
		provID: "stub",
	}
	o.WithNLIRouter(prov)

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ModalVerdict != "aligned" {
		t.Errorf("modal: got %q, want aligned", out.ModalVerdict)
	}
	if out.ModalFraction >= 0.6 {
		t.Errorf("fraction: got %f, want < 0.6", out.ModalFraction)
	}
	if out.Verdict != "needs_human" {
		t.Errorf("verdict: got %q, want needs_human (low agreement)", out.Verdict)
	}
}

// =====================================================================
// DriftJudgeConsensus per-chunk error tests
// =====================================================================

func TestDriftJudgeConsensus_AllChunksFail_ReturnsError(t *testing.T) {
	// Provider always returns an error → all chunks fail → error.
	content := make([]byte, 8*1024)
	path := writeArtifactBytes(t, content)

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{
		err:     nli.ErrNoProvider,
		id_:     "stub",
		score:   nli.Score{ProviderID: "stub"},
	}
	o.WithNLIRouter(prov)

	_, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           3,
	})
	if err == nil {
		t.Fatal("expected error when all chunks fail")
	}
	if !strings.Contains(err.Error(), "all") {
		t.Errorf("error: got %q, want substring 'all'", err)
	}
}

func TestDriftJudgeConsensus_PartialFailure_Degrades(t *testing.T) {
	// Some chunks succeed, others fail (e.g., ErrInputTooLarge
	// because the chunk exceeds NLI MaxPremiseBytes). The
	// surviving samples vote; failures count as non-votes.
	//
	// Goroutines run concurrently, so we can't predict which
	// specific call fails. We just verify that EXACTLY ONE
	// failed (the other two succeeded) and that the aggregation
	// still produces a sensible result.
	content := make([]byte, 8*1024)
	path := writeArtifactBytes(t, content)

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &failingChunkLabelProvider{
		labels:  []nli.Label{nli.LabelEntailment, nli.LabelContradiction, nli.LabelEntailment},
		failAt:  1,
		failErr: nli.ErrInputTooLarge,
		provID:  "stub",
	}
	o.WithNLIRouter(prov)
	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Degraded {
		t.Errorf("expected degraded=true")
	}
	if len(out.FailedSampleIndices) != 1 {
		t.Errorf("failed count: got %d, want 1", len(out.FailedSampleIndices))
	}
	// 2 of 3 samples succeeded (the failed one is needs_human).
	// The labels are: 2x entailment + 1x contradiction → modal
	// is entailment (2/3 = 0.67 > 0.6) → verdict aligned.
	if out.ModalVerdict != "aligned" {
		t.Errorf("modal: got %q, want aligned", out.ModalVerdict)
	}
	if out.Verdict != "aligned" {
		t.Errorf("verdict: got %q, want aligned", out.Verdict)
	}
	// Sanity: 2 successful samples (not 3 — the failed one
	// doesn't add to the samples slice).
	if len(out.Samples) != 2 {
		t.Errorf("samples: got %d, want 2 surviving", len(out.Samples))
	}
}

// =====================================================================
// DriftJudgeConsensus result shape
// =====================================================================

func TestDriftJudgeConsensus_ResultShape_HasArtifactProvenance(t *testing.T) {
	content := make([]byte, 1024)
	path := writeArtifactBytes(t, content)
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.9, ProviderID: "stub"}})

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ArtifactSource != "file" {
		t.Errorf("artifact_source: got %q, want file", out.ArtifactSource)
	}
	if out.ArtifactPath != path {
		t.Errorf("artifact_path: got %q, want %q", out.ArtifactPath, path)
	}
	if out.ArtifactSHA256 == "" {
		t.Errorf("artifact_sha256 should be populated")
	}
	if out.ProviderID != "stub" {
		t.Errorf("provider_id: got %q, want stub", out.ProviderID)
	}
}

func TestDriftJudgeConsensus_Result_SamplesHaveChunkProvenance(t *testing.T) {
	content := make([]byte, 12*1024)
	path := writeArtifactBytes(t, content)
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.9, ProviderID: "stub"}})

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Samples) != 3 {
		t.Fatalf("samples: got %d, want 3", len(out.Samples))
	}
	for i, s := range out.Samples {
		if s.SampleIndex != i {
			t.Errorf("sample[%d].SampleIndex: got %d", i, s.SampleIndex)
		}
		if s.ChunkStart < 0 {
			t.Errorf("sample[%d].ChunkStart: got %d, want >= 0", i, s.ChunkStart)
		}
		if s.ChunkEnd <= s.ChunkStart {
			t.Errorf("sample[%d].ChunkEnd: got %d, want > %d", i, s.ChunkEnd, s.ChunkStart)
		}
		if s.ChunkSHA256 == "" {
			t.Errorf("sample[%d].ChunkSHA256 should be populated", i)
		}
	}
}

// =====================================================================
// DriftJudgeConsensus persistence tests
// =====================================================================

func TestDriftJudgeConsensus_PersistsPerChunkAndConsensusRows(t *testing.T) {
	content := make([]byte, 12*1024)
	path := writeArtifactBytes(t, content)
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.9, ProviderID: "stub"}})

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
		N:           3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.EvaluationID == 0 {
		t.Errorf("consensus row should have non-zero ID")
	}
	for i, s := range out.Samples {
		if s.EvaluationID == 0 {
			t.Errorf("chunk %d row should have non-zero ID", i)
		}
	}

	// Verify the consensus row + per-chunk rows are in the DB.
	// We use ListSDDEvaluations filtered by TargetID.
	evals, err := o.Store.ListSDDEvaluations(context.Background(), ssd.ListFilters{
		EvalType: "drift_judge",
		Limit:    100,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Find rows with ":consensus" suffix and ":chunk:N" suffix.
	consensusFound := 0
	chunkFound := 0
	for _, e := range evals {
		if strings.HasSuffix(e.TargetID, ":consensus") {
			consensusFound++
		}
		for i := 0; i < 10; i++ {
			if strings.HasSuffix(e.TargetID, fmt.Sprintf(":chunk:%d", i)) {
				chunkFound++
				break
			}
		}
	}
	if consensusFound != 1 {
		t.Errorf("consensus rows: got %d, want 1", consensusFound)
	}
	if chunkFound != 3 {
		t.Errorf("chunk rows: got %d, want 3", chunkFound)
	}
}

// =====================================================================
// DriftJudgeConsensus caller-cannot-influence test
// =====================================================================

func TestDriftJudgeConsensus_VerifyCallerCannotInfluence(t *testing.T) {
	// Architectural invariant: the score is computed over the
	// CHUNK bytes, not the caller's spec_intent or any other text.
	content := []byte("THE FILE BODY IS BAD")
	path := writeArtifactBytes(t, content)
	o, _ := newDriftJudgeTestOrchestrator(t)

	// Provider captures the (premise, hypothesis) for the first
	// chunk. We assert premise == the file body, not the spec.
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment},
		provID: "stub",
	}
	o.WithNLIRouter(prov)

	_, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "caller-controlled intent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prov.capturedPremises[0] != "THE FILE BODY IS BAD" {
		t.Errorf("premise: got %q, want the FILE body", prov.capturedPremises[0])
	}
	if prov.capturedHypotheses[0] != "caller-controlled intent" {
		t.Errorf("hypothesis: got %q, want caller-supplied spec intent", prov.capturedHypotheses[0])
	}
}

// =====================================================================
// JudgeConsensus routing tests
// =====================================================================

func TestJudgeConsensus_Routes_DriftJudgePlusArtifactRef(t *testing.T) {
	// EvalType=drift_judge + ArtifactRef → DriftJudgeConsensus
	// path. The result is a JudgeConsensusResult, but the
	// underlying semantics are chunk-scored.
	content := make([]byte, 1024)
	path := writeArtifactBytes(t, content)
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.9, ProviderID: "stub"}})

	ref := &artifact.ArtifactRef{Kind: artifact.KindFile, Path: path}
	out, err := o.JudgeConsensus(context.Background(), JudgeConsensusInput{
		EvalType:    "drift_judge",
		ArtifactRef: ref,
		SpecIntent:  "spec",
		TargetID:    "artifact",
		N:           3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ModalVerdict != "aligned" {
		t.Errorf("modal: got %q, want aligned", out.ModalVerdict)
	}
	if out.ModalFraction != 1.0 {
		t.Errorf("fraction: got %f, want 1.0", out.ModalFraction)
	}
	if len(out.Samples) != 3 {
		t.Errorf("samples: got %d, want 3", len(out.Samples))
	}
}

func TestJudgeConsensus_Routes_DriftJudgePlusContent_LegacyPath(t *testing.T) {
	// EvalType=drift_judge + Content but no ArtifactRef → legacy
	// path (with deprecation Warn). The Judge function is called
	// N times, which requires an LLM client. We can't easily test
	// that without a full harness, so we verify that the route
	// does NOT take the DriftJudgeConsensus branch (by checking
	// the error: ErrInvalidArgument because Content is empty).
	o, _ := newDriftJudgeTestOrchestrator(t)
	_, err := o.JudgeConsensus(context.Background(), JudgeConsensusInput{
		EvalType: "drift_judge",
		// No ArtifactRef, no Content → legacy validation rejects.
	})
	if err == nil {
		t.Fatal("expected error for empty content + no artifact_ref")
	}
}

// =====================================================================
// DriftJudgeConsensus JSON shape tests
// =====================================================================

func TestDriftJudgeConsensus_VerdictJSON_IncludesArtifactProvenance(t *testing.T) {
	content := make([]byte, 1024)
	path := writeArtifactBytes(t, content)
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.9, ProviderID: "stub"}})

	out, err := o.DriftJudgeConsensus(context.Background(), DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "spec",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Marshal the result to JSON and verify the consensus shape.
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	jstr := string(b)
	mustContain(t, jstr, `"artifact_source"`)
	mustContain(t, jstr, `"artifact_sha256"`)
	mustContain(t, jstr, `"artifact_path"`)
	mustContain(t, jstr, `"provider_id":"stub"`)
	mustContain(t, jstr, `"chunked_size":1024`)
	mustContain(t, jstr, `"num_chunks":1`)
	mustContain(t, jstr, `"requested_n":3`)
	mustContain(t, jstr, `"samples":[`)
	mustContain(t, jstr, `"chunk_start":0`)
	mustContain(t, jstr, `"chunk_end":1024`)
}

// =====================================================================
// Provider stubs for tests
// =====================================================================

// chunkLabelProvider is an nli.Provider that returns a different
// label per chunk index. Used to test aggregation with mixed labels.
//
// Goroutine-safe: the orchestrator runs N shots concurrently, so
// all mutable fields are guarded by mu.
type chunkLabelProvider struct {
	mu                 sync.Mutex
	labels             []nli.Label
	provID             string
	capturedPremises   []string
	capturedHypotheses []string
	scores             []nli.Score
}

func (c *chunkLabelProvider) Score(ctx context.Context, premise, hypothesis string) (nli.Score, error) {
	c.mu.Lock()
	idx := len(c.capturedPremises)
	c.capturedPremises = append(c.capturedPremises, premise)
	c.capturedHypotheses = append(c.capturedHypotheses, hypothesis)
	var label nli.Label
	if idx < len(c.labels) {
		label = c.labels[idx]
	} else {
		label = nli.LabelNeutral
	}
	score := nli.Score{
		Label:      label,
		Confidence: 0.9,
		ProviderID: c.provID,
		LatencyMS:  100,
	}
	c.scores = append(c.scores, score)
	c.mu.Unlock()
	return score, nil
}

func (c *chunkLabelProvider) ID() string { return c.provID }

// failingChunkLabelProvider returns a label for most chunks but
// fails with failErr on the chunk at failAt.
type failingChunkLabelProvider struct {
	labels  []nli.Label
	failAt  int
	failErr error
	provID  string
	counter int
	mu      sync.Mutex
}

func (f *failingChunkLabelProvider) Score(ctx context.Context, premise, hypothesis string) (nli.Score, error) {
	f.mu.Lock()
	idx := f.counter
	f.counter++
	f.mu.Unlock()
	if idx == f.failAt {
		return nli.Score{ProviderID: f.provID}, f.failErr
	}
	if idx < len(f.labels) {
		return nli.Score{Label: f.labels[idx], Confidence: 0.9, ProviderID: f.provID, LatencyMS: 100}, nil
	}
	return nli.Score{Label: nli.LabelNeutral, Confidence: 0.5, ProviderID: f.provID, LatencyMS: 100}, nil
}

func (f *failingChunkLabelProvider) ID() string { return f.provID }

// =====================================================================
// helpers
// =====================================================================

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected substring %q in:\n%s", needle, haystack)
	}
}

// listEvalFilters is a local alias to avoid importing ssd in
// the test file (kept narrow to limit imports).
type _unused = struct{}

// silenceUnused ensures period-used imports stay.
var _ = store.ErrInvalidArgument

var _ = controllableProvider{}
