package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
)

// TestPersistDriftChunkRow_V29AuditColumns (spec 1276 T10): verifies
// that persistDriftChunkRow populates the v29 audit + anchor columns
// on the inserted SDDEvaluation row. The drift_judge_consensus
// pipeline now writes artifact provenance + chunk_index + chunk_total
// + NLIProviderID as first-class columns — not just inside the
// VerdictJSON blob.
func TestPersistDriftChunkRow_V29AuditColumns(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "chunk-test-1.bin")
	artifactBytes := []byte("the artifact body content for chunk testing v29 spec 1276 T10")
	if err := os.WriteFile(artifactPath, artifactBytes, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	orch, st := newDriftJudgeTestOrchestrator(t)
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment, nli.LabelEntailment, nli.LabelEntailment},
		provID: "deberta-v3-large-mnli",
	}
	orch.WithNLIRouter(prov)

	// Run a consensus on a small artifact (3 chunks of 4 KB on
	// the small body → 1 chunk; N=3 redundant shots).
	ref := artifact.ArtifactRef{
		Kind: artifact.KindFile,
		Path: artifactPath,
	}
	in := DriftJudgeConsensusInput{
		ArtifactRef: ref,
		SpecIntent:  "the artifact should contain v29 audit columns",
		TargetType:  "artifact",
		TargetID:    "drift_judge_consensus:v29_test",
		N:           3,
	}
	result, err := orch.DriftJudgeConsensus(ctx, in)
	if err != nil {
		t.Fatalf("DriftJudgeConsensus: %v", err)
	}
	if result == nil {
		t.Fatalf("DriftJudgeConsensus returned nil result")
	}

	// List all drift_judge evaluations for this target_id (chunk
	// rows + consensus row).
	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{
		EvalType: "drift_judge",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}

	// Compute expected sha256 of resolved bytes.
	expectedSHA := sha256OfBytes(artifactBytes)

	// Group rows by suffix: chunk vs consensus.
	var chunks []ssd.SDDEvaluation
	var consensus ssd.SDDEvaluation
	consensusFound := false
	for _, r := range rows {
		if r.TargetID == in.TargetID+":consensus" {
			consensus = r
			consensusFound = true
		} else if isChunkSuffix(r.TargetID, in.TargetID) {
			chunks = append(chunks, r)
		}
	}

	if !consensusFound {
		t.Fatalf("no consensus row found (target_id=%s:consensus)", in.TargetID)
	}
	// 1 chunk (small body) + 3 redundant shots → 3 chunk rows.
	if len(chunks) != 3 {
		t.Errorf("chunk rows: got %d, want 3 (1 chunk x N=3 redundant shots)", len(chunks))
	}

	// Verify every chunk row has the v29 columns populated.
	for i, c := range chunks {
		if c.ArtifactSource == "" {
			t.Errorf("chunk %d: ArtifactSource empty", i)
		}
		if c.ArtifactSource != "file" {
			t.Errorf("chunk %d: ArtifactSource=%q, want file", i, c.ArtifactSource)
		}
		if c.ArtifactSHA256 != expectedSHA {
			t.Errorf("chunk %d: ArtifactSHA256=%q, want %q", i, c.ArtifactSHA256, expectedSHA)
		}
		if c.ArtifactPath != artifactPath {
			t.Errorf("chunk %d: ArtifactPath=%q, want %q", i, c.ArtifactPath, artifactPath)
		}
		if c.ArtifactSize != int64(len(artifactBytes)) {
			t.Errorf("chunk %d: ArtifactSize=%d, want %d", i, c.ArtifactSize, len(artifactBytes))
		}
		// ChunkIndex (0 for the single chunk) + ChunkTotal (1 for
		// 1 chunk).
		if c.ChunkIndex != 0 {
			t.Errorf("chunk %d: ChunkIndex=%d, want 0 (single chunk)", i, c.ChunkIndex)
		}
		if c.ChunkTotal != 1 {
			t.Errorf("chunk %d: ChunkTotal=%d, want 1 (single chunk)", i, c.ChunkTotal)
		}
		// NLIProviderID should match the provider that scored.
		if c.NLIProviderID != "deberta-v3-large-mnli" {
			t.Errorf("chunk %d: NLIProviderID=%q, want deberta-v3-large-mnli", i, c.NLIProviderID)
		}
	}

	// Verify the consensus row has the v29 columns populated too.
	// Consensus row: ChunkIndex=0, ChunkTotal=NumChunks.
	if consensus.ArtifactSource != "file" {
		t.Errorf("consensus: ArtifactSource=%q, want file", consensus.ArtifactSource)
	}
	if consensus.ArtifactSHA256 != expectedSHA {
		t.Errorf("consensus: ArtifactSHA256=%q, want %q", consensus.ArtifactSHA256, expectedSHA)
	}
	if consensus.ArtifactPath != artifactPath {
		t.Errorf("consensus: ArtifactPath=%q, want %q", consensus.ArtifactPath, artifactPath)
	}
	if consensus.ArtifactSize != int64(len(artifactBytes)) {
		t.Errorf("consensus: ArtifactSize=%d, want %d", consensus.ArtifactSize, len(artifactBytes))
	}
	if consensus.ChunkIndex != 0 {
		t.Errorf("consensus: ChunkIndex=%d, want 0", consensus.ChunkIndex)
	}
	if consensus.ChunkTotal != 1 {
		t.Errorf("consensus: ChunkTotal=%d, want 1", consensus.ChunkTotal)
	}
	if consensus.NLIProviderID != "deberta-v3-large-mnli" {
		t.Errorf("consensus: NLIProviderID=%q, want deberta-v3-large-mnli", consensus.NLIProviderID)
	}

	// Result's anchor fields should match the row's columns
	// (proves the DriftJudgeConsensusResult → SDDEvaluation mapping
	// is consistent).
	if result.ArtifactSize != int64(len(artifactBytes)) {
		t.Errorf("result.ArtifactSize=%d, want %d", result.ArtifactSize, len(artifactBytes))
	}
}

// TestPersistDriftChunkRow_VerdictJSONConsistent: the JSON blob
// inside each chunk row carries the same chunk_start/chunk_end
// that the v29 columns (chunk_index/chunk_total) describe. The
// reader can use either — the JSON blob is the canonical shape
// pre-v29, the columns are the v29 audit layer.
func TestPersistDriftChunkRow_VerdictJSONConsistent(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "chunk-test-2.bin")
	// Body large enough to produce 2 chunks (>4 KB).
	artifactBytes := make([]byte, 9000)
	for i := range artifactBytes {
		artifactBytes[i] = byte('a' + (i % 26))
	}
	if err := os.WriteFile(artifactPath, artifactBytes, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	orch, st := newDriftJudgeTestOrchestrator(t)
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment, nli.LabelEntailment},
		provID: "deberta-v3-large-mnli",
	}
	orch.WithNLIRouter(prov)

	ref := artifact.ArtifactRef{
		Kind: artifact.KindFile,
		Path: artifactPath,
	}
	in := DriftJudgeConsensusInput{
		ArtifactRef: ref,
		SpecIntent:  "spec for v29 consistency",
		TargetType:  "artifact",
		TargetID:    "drift_judge_consensus:v29_consistency",
		// N=2 → 2 shots on 2 chunks (chunks=2, N=2).
		N: 2,
	}
	result, err := orch.DriftJudgeConsensus(ctx, in)
	if err != nil {
		t.Fatalf("DriftJudgeConsensus: %v", err)
	}

	// Result should have NumChunks=2 (one chunk per shot).
	if result.NumChunks != 2 {
		t.Errorf("NumChunks=%d, want 2", result.NumChunks)
	}

	// Read back the 2 chunk rows + 1 consensus row.
	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{
		EvalType: "drift_judge",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}
	var chunks []ssd.SDDEvaluation
	var consensus ssd.SDDEvaluation
	consensusFound := false
	for _, r := range rows {
		if r.TargetID == in.TargetID+":consensus" {
			consensus = r
			consensusFound = true
		} else if isChunkSuffix(r.TargetID, in.TargetID) {
			chunks = append(chunks, r)
		}
	}
	if !consensusFound {
		t.Fatalf("no consensus row found")
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk rows: got %d, want 2", len(chunks))
	}

	// Each chunk row's ChunkIndex should be 0 or 1 (one chunk per
	// shot), and ChunkTotal should be 2.
	for i, c := range chunks {
		if c.ChunkTotal != 2 {
			t.Errorf("chunk %d: ChunkTotal=%d, want 2", i, c.ChunkTotal)
		}
		// chunk_size: 9000 / 2 = 4500
		// chunk 0: start=0, end=4500
		// chunk 1: start=4500, end=9000
		var verdictJSON struct {
			ChunkStart int64 `json:"chunk_start"`
			ChunkEnd   int64 `json:"chunk_end"`
		}
		if err := json.Unmarshal([]byte(c.VerdictJSON), &verdictJSON); err != nil {
			t.Errorf("chunk %d: parse VerdictJSON: %v", i, err)
			continue
		}
		switch c.ChunkIndex {
		case 0:
			if verdictJSON.ChunkStart != 0 || verdictJSON.ChunkEnd != 4500 {
				t.Errorf("chunk 0: VerdictJSON chunk_start=%d chunk_end=%d, want 0..4500",
					verdictJSON.ChunkStart, verdictJSON.ChunkEnd)
			}
		case 1:
			if verdictJSON.ChunkStart != 4500 || verdictJSON.ChunkEnd != 9000 {
				t.Errorf("chunk 1: VerdictJSON chunk_start=%d chunk_end=%d, want 4500..9000",
					verdictJSON.ChunkStart, verdictJSON.ChunkEnd)
			}
		default:
			t.Errorf("chunk %d: ChunkIndex=%d, want 0 or 1", i, c.ChunkIndex)
		}
	}

	// Consensus row's ChunkIndex=0, ChunkTotal=2.
	if consensus.ChunkIndex != 0 {
		t.Errorf("consensus: ChunkIndex=%d, want 0", consensus.ChunkIndex)
	}
	if consensus.ChunkTotal != 2 {
		t.Errorf("consensus: ChunkTotal=%d, want 2", consensus.ChunkTotal)
	}
}

// TestPersistDriftChunkRow_EmptyArtifactStillPopulatesAudit: even
// when the artifact is empty (drift_detected), the consensus row
// still carries the v29 audit columns (so the audit trail captures
// "this artifact was 0 bytes").
func TestPersistDriftChunkRow_EmptyArtifactStillPopulatesAudit(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "empty.bin")
	if err := os.WriteFile(artifactPath, []byte{}, 0o644); err != nil {
		t.Fatalf("write empty artifact: %v", err)
	}

	orch, st := newDriftJudgeTestOrchestrator(t)
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment},
		provID: "deberta-v3-large-mnli",
	}
	orch.WithNLIRouter(prov)

	ref := artifact.ArtifactRef{
		Kind: artifact.KindFile,
		Path: artifactPath,
	}
	in := DriftJudgeConsensusInput{
		ArtifactRef: ref,
		SpecIntent:  "should not match because empty",
		TargetType:  "artifact",
		TargetID:    "drift_judge_consensus:v29_empty",
		N:           1,
	}
	result, err := orch.DriftJudgeConsensus(ctx, in)
	if err != nil {
		t.Fatalf("DriftJudgeConsensus: %v", err)
	}
	if result.Verdict != "drift_detected" {
		t.Errorf("verdict: got %q, want drift_detected (empty artifact)", result.Verdict)
	}
	if result.ArtifactSize != 0 {
		t.Errorf("result.ArtifactSize=%d, want 0", result.ArtifactSize)
	}

	// The consensus row should still carry the v29 columns.
	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{
		EvalType: "drift_judge",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}
	var consensus ssd.SDDEvaluation
	consensusFound := false
	for _, r := range rows {
		if r.TargetID == in.TargetID+":consensus" {
			consensus = r
			consensusFound = true
		}
	}
	if !consensusFound {
		t.Fatalf("no consensus row found")
	}
	if consensus.ArtifactSource != "file" {
		t.Errorf("consensus.ArtifactSource=%q, want file", consensus.ArtifactSource)
	}
	if consensus.ArtifactSize != 0 {
		t.Errorf("consensus.ArtifactSize=%d, want 0", consensus.ArtifactSize)
	}
	if consensus.ChunkTotal != 0 {
		t.Errorf("consensus.ChunkTotal=%d, want 0 (no chunks for empty artifact)", consensus.ChunkTotal)
	}
}

// isChunkSuffix returns true if the row's TargetID is "{base}:chunk:N".
func isChunkSuffix(rowTargetID, base string) bool {
	target := base + ":chunk:"
	return len(rowTargetID) >= len(target) && rowTargetID[:len(target)] == target
}

// sha256OfBytes is a tiny SHA-256 helper that returns the hex
// representation of the SHA-256 of the input bytes.
func sha256OfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	const hextable = "0123456789abcdef"
	out := make([]byte, 64)
	for i := 0; i < 32; i++ {
		out[i*2] = hextable[sum[i]>>4]
		out[i*2+1] = hextable[sum[i]&0x0f]
	}
	return string(out)
}
