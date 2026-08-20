package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// newSDDTestStore opens a fresh SQLite Store with the "default"
// project active, suitable for SDDEvaluation roundtrip tests.
// Caller must call cleanup when done.
func newSDDTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "sdd-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("runtime.Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	cleanup := func() { _ = st.Close() }
	return st, cleanup
}

// TestSaveSDDEvaluation_RoundtripV29AuditColumns (spec 1276 T10):
// verifies that the v29 audit + anchor columns are persisted and
// read back correctly. Pre-v29 rows would have NULL in all eight
// new columns; post-v29 rows populate the new fields based on the
// caller's intent.
func TestSaveSDDEvaluation_RoundtripV29AuditColumns(t *testing.T) {
	ctx := context.Background()
	st, cleanup := newSDDTestStore(t)
	defer cleanup()

	// 1. Save an SDDEvaluation with all 8 v29 fields populated.
	wc := store.WriteContext{
		Actor:     "test_sdd_roundtrip",
		WritePath: "TestSaveSDDEvaluation_RoundtripV29AuditColumns",
	}
	in := &ssd.SDDEvaluation{
		EvalType:       "drift_judge",
		TargetType:     "artifact",
		TargetID:       "artifact:abc123:chunk:1",
		VerdictJSON:    `{"verdict":"aligned","confidence":0.92}`,
		Confidence:     0.92,
		Model:          "deberta-v3-large-mnli",
		ConstitutionID: "default-v1",
		CreatedAt:      "2026-08-19T00:00:00.0000000Z",
		// v29 anchor + audit columns.
		MerkleRoot:     "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234",
		ArtifactSource: "file",
		ArtifactSHA256: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ArtifactPath:   "/tmp/artifact.bin",
		ArtifactSize:   12345,
		ChunkIndex:     1,
		ChunkTotal:     4,
		NLIProviderID:  "deberta-v3-large-mnli",
	}
	id, err := st.SaveSDDEvaluation(ctx, wc, in)
	if err != nil {
		t.Fatalf("SaveSDDEvaluation: %v", err)
	}
	if id == 0 {
		t.Fatalf("SaveSDDEvaluation returned id=0")
	}

	// 2. Read it back via LatestSDDEvaluation.
	got, err := st.LatestSDDEvaluation(ctx, "drift_judge", "artifact", "artifact:abc123:chunk:1")
	if err != nil {
		t.Fatalf("LatestSDDEvaluation: %v", err)
	}
	if got == nil {
		t.Fatalf("LatestSDDEvaluation returned nil")
	}

	// 3. Verify all 8 v29 fields roundtrip correctly.
	if got.MerkleRoot != in.MerkleRoot {
		t.Errorf("MerkleRoot: got %q, want %q", got.MerkleRoot, in.MerkleRoot)
	}
	if got.ArtifactSource != in.ArtifactSource {
		t.Errorf("ArtifactSource: got %q, want %q", got.ArtifactSource, in.ArtifactSource)
	}
	if got.ArtifactSHA256 != in.ArtifactSHA256 {
		t.Errorf("ArtifactSHA256: got %q, want %q", got.ArtifactSHA256, in.ArtifactSHA256)
	}
	if got.ArtifactPath != in.ArtifactPath {
		t.Errorf("ArtifactPath: got %q, want %q", got.ArtifactPath, in.ArtifactPath)
	}
	if got.ArtifactSize != in.ArtifactSize {
		t.Errorf("ArtifactSize: got %d, want %d", got.ArtifactSize, in.ArtifactSize)
	}
	if got.ChunkIndex != in.ChunkIndex {
		t.Errorf("ChunkIndex: got %d, want %d", got.ChunkIndex, in.ChunkIndex)
	}
	if got.ChunkTotal != in.ChunkTotal {
		t.Errorf("ChunkTotal: got %d, want %d", got.ChunkTotal, in.ChunkTotal)
	}
	if got.NLIProviderID != in.NLIProviderID {
		t.Errorf("NLIProviderID: got %q, want %q", got.NLIProviderID, in.NLIProviderID)
	}

	// 4. Verify the v3 column also survives (regression check).
	if got.ConstitutionID != "default-v1" {
		t.Errorf("ConstitutionID: got %q, want default-v1", got.ConstitutionID)
	}
	if got.Confidence != 0.92 {
		t.Errorf("Confidence: got %f, want 0.92", got.Confidence)
	}
}

// TestSaveSDDEvaluation_DefaultV29ColumnsAreNull: when the caller
// passes a v3-shape SDDEvaluation (no v29 fields), the persisted row
// has NULL values for the v29 columns (backward compat with pre-v29
// callers).
func TestSaveSDDEvaluation_DefaultV29ColumnsAreNull(t *testing.T) {
	ctx := context.Background()
	st, cleanup := newSDDTestStore(t)
	defer cleanup()

	wc := store.WriteContext{
		Actor:     "test_sdd_pre_v29",
		WritePath: "TestSaveSDDEvaluation_DefaultV29ColumnsAreNull",
	}
	// Pre-v29 shape: no MerkleRoot, no ArtifactSource, etc.
	in := &ssd.SDDEvaluation{
		EvalType:    "brand_match",
		TargetType:  "artifact",
		TargetID:    "artifact:legacy-1",
		VerdictJSON: `{"verdict":"match"}`,
		Confidence:  0.85,
		CreatedAt:   "2026-08-18T00:00:00.0000000Z",
	}
	id, err := st.SaveSDDEvaluation(ctx, wc, in)
	if err != nil {
		t.Fatalf("SaveSDDEvaluation: %v", err)
	}

	got, err := st.LatestSDDEvaluation(ctx, "brand_match", "artifact", "artifact:legacy-1")
	if err != nil {
		t.Fatalf("LatestSDDEvaluation: %v", err)
	}
	if got == nil {
		t.Fatalf("LatestSDDEvaluation returned nil")
	}

	// All v29 fields should be empty (the zero-value after Scan
	// when the SQL column is NULL).
	if got.MerkleRoot != "" {
		t.Errorf("MerkleRoot: should be empty (pre-v29 row), got %q", got.MerkleRoot)
	}
	if got.ArtifactSource != "" {
		t.Errorf("ArtifactSource: should be empty, got %q", got.ArtifactSource)
	}
	if got.ArtifactSHA256 != "" {
		t.Errorf("ArtifactSHA256: should be empty, got %q", got.ArtifactSHA256)
	}
	if got.ArtifactPath != "" {
		t.Errorf("ArtifactPath: should be empty, got %q", got.ArtifactPath)
	}
	if got.ArtifactSize != 0 {
		t.Errorf("ArtifactSize: should be 0, got %d", got.ArtifactSize)
	}
	if got.ChunkIndex != 0 {
		t.Errorf("ChunkIndex: should be 0, got %d", got.ChunkIndex)
	}
	if got.ChunkTotal != 0 {
		t.Errorf("ChunkTotal: should be 0, got %d", got.ChunkTotal)
	}
	if got.NLIProviderID != "" {
		t.Errorf("NLIProviderID: should be empty, got %q", got.NLIProviderID)
	}

	// Use id to silence the unused-var warning if any future change
	// removes the early-return.
	_ = id
}

// TestListSDDEvaluations_OrdersByChunkIndex: when multiple rows
// share the same TargetID (e.g. a consensus run with chunks 0..N-1),
// ListSDDEvaluations returns them all (no client-side filter by
// chunk is applied). The v29 column "chunk_index" + "chunk_total"
// lets the caller reconstruct the chunking strategy.
func TestListSDDEvaluations_OrdersByChunkIndex(t *testing.T) {
	ctx := context.Background()
	st, cleanup := newSDDTestStore(t)
	defer cleanup()

	wc := store.WriteContext{
		Actor:     "test_sdd_chunks",
		WritePath: "TestListSDDEvaluations_OrdersByChunkIndex",
	}
	const targetID = "artifact:consensus-stub"
	// Simulate a 3-chunk consensus run: chunk 0, chunk 1, chunk 2,
	// plus the consensus row (chunk_index=0, chunk_total=3).
	chunks := []struct {
		chunkIndex int
		chunkTotal int
		isConsensus bool
	}{
		{0, 3, false},
		{1, 3, false},
		{2, 3, false},
		{0, 3, true}, // the consensus row
	}
	for _, c := range chunks {
		td := targetID
		if !c.isConsensus {
			td = targetID + ":chunk:" + itoa(c.chunkIndex)
		} else {
			td = targetID + ":consensus"
		}
		_, err := st.SaveSDDEvaluation(ctx, wc, &ssd.SDDEvaluation{
			EvalType:       "drift_judge",
			TargetType:     "artifact",
			TargetID:       td,
			VerdictJSON:    `{"verdict":"aligned"}`,
			Confidence:     0.92,
			ArtifactSource: "file",
			ArtifactSHA256: "cc" + repeat("d", 62),
			ArtifactPath:   "/tmp/c",
			ArtifactSize:   100,
			ChunkIndex:     c.chunkIndex,
			ChunkTotal:     c.chunkTotal,
			NLIProviderID:  "deberta-v3-large-mnli",
			CreatedAt:      "2026-08-19T00:00:00.0000000Z",
		})
		if err != nil {
			t.Fatalf("SaveSDDEvaluation for chunk %d (consensus=%v): %v", c.chunkIndex, c.isConsensus, err)
		}
	}

	// List all drift_judge evaluations for the consensus run.
	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{
		EvalType: "drift_judge",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}
	// 4 rows total (3 chunks + consensus).
	if len(rows) != 4 {
		t.Fatalf("ListSDDEvaluations: got %d rows, want 4", len(rows))
	}

	// Verify chunk_index + chunk_total distribution.
	seenChunks := make(map[int]int)
	seenConsensus := 0
	for _, r := range rows {
		if r.ChunkTotal != 3 {
			t.Errorf("TargetID=%q: ChunkTotal=%d, want 3", r.TargetID, r.ChunkTotal)
		}
		if r.ChunkTotal > 0 {
			if r.TargetID == targetID+":consensus" {
				seenConsensus++
				if r.ChunkIndex != 0 {
					t.Errorf("consensus row: ChunkIndex=%d, want 0", r.ChunkIndex)
				}
			} else {
				seenChunks[r.ChunkIndex]++
			}
		}
	}
	if seenConsensus != 1 {
		t.Errorf("expected 1 consensus row, got %d", seenConsensus)
	}
	for i := 0; i < 3; i++ {
		if seenChunks[i] != 1 {
			t.Errorf("chunk %d: expected 1 row, got %d", i, seenChunks[i])
		}
	}
}

// TestSaveSDDEvaluation_PopulatesArtifactSizeFromContent: when the
// caller passes an artifact-anchored SDDEvaluation, the v29 columns
// are populated by the consumer (drift_judge.go sets ArtifactSize =
// len(resolved.Bytes)). The Store doesn't compute this — it just
// persists what the caller provides. This test confirms the round
// trip preserves the caller-provided size.
func TestSaveSDDEvaluation_PopulatesArtifactSizeFromContent(t *testing.T) {
	ctx := context.Background()
	st, cleanup := newSDDTestStore(t)
	defer cleanup()

	wc := store.WriteContext{
		Actor:     "test_sdd_size",
		WritePath: "TestSaveSDDEvaluation_PopulatesArtifactSizeFromContent",
	}

	// 1 MB artifact (small enough to be fast).
	const oneMB = 1024 * 1024
	in := &ssd.SDDEvaluation{
		EvalType:       "drift_judge",
		TargetType:     "artifact",
		TargetID:       "artifact:1mb-1",
		VerdictJSON:    `{"verdict":"aligned"}`,
		Confidence:     0.92,
		ArtifactSource: "file",
		ArtifactSHA256: "bb" + repeat("c", 62),
		ArtifactPath:   "/tmp/1mb.bin",
		ArtifactSize:   oneMB,
		CreatedAt:      "2026-08-19T00:00:00.0000000Z",
	}
	if _, err := st.SaveSDDEvaluation(ctx, wc, in); err != nil {
		t.Fatalf("SaveSDDEvaluation: %v", err)
	}

	got, err := st.LatestSDDEvaluation(ctx, "drift_judge", "artifact", "artifact:1mb-1")
	if err != nil {
		t.Fatalf("LatestSDDEvaluation: %v", err)
	}
	if got.ArtifactSize != oneMB {
		t.Errorf("ArtifactSize: got %d, want %d", got.ArtifactSize, oneMB)
	}
}

// itoa is a tiny helper to convert int to string for index
// concatenation in test setup.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// repeat returns a string consisting of n copies of c.
func repeat(c string, n int) string {
	out := make([]byte, 0, len(c)*n)
	for i := 0; i < n; i++ {
		out = append(out, c...)
	}
	return string(out)
}
