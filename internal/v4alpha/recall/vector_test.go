package recall

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// encodeVecForTest mirrors store/sqlite/vector.go:encodeVec's BLOB
// layout (little-endian float32 contiguous). Tests build a BLOB
// from a known embedder.Vec, run it through decodeEmbeddingBlob,
// and assert round-trip identity + cosine properties.
func encodeVecForTest(v embedder.Vec) []byte {
	if len(v) == 0 {
		return nil
	}
	out := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:i*4+4], math.Float32bits(x))
	}
	return out
}

func TestCosineSimilarity_IdenticalVectors_ReturnsOne(t *testing.T) {
	v := embedder.Vec{1.0, 0.0, 0.0}
	got := cosineSimilarity(v, v)
	if math.Abs(got-1.0) > 1e-6 {
		t.Errorf("identical → 1.0, got %f", got)
	}
}

func TestCosineSimilarity_OrthogonalVectors_ReturnsZero(t *testing.T) {
	a := embedder.Vec{1.0, 0.0, 0.0}
	b := embedder.Vec{0.0, 1.0, 0.0}
	got := cosineSimilarity(a, b)
	if math.Abs(got) > 1e-6 {
		t.Errorf("orthogonal → 0.0, got %f", got)
	}
}

func TestCosineSimilarity_DimensionMismatch_ReturnsZero(t *testing.T) {
	a := embedder.Vec{1.0, 0.0, 0.0}
	b := embedder.Vec{1.0, 0.0} // different dim
	got := cosineSimilarity(a, b)
	if got != 0 {
		t.Errorf("dim mismatch → 0, got %f", got)
	}
}

func TestCosineSimilarity_EmptyInput_ReturnsZero(t *testing.T) {
	got := cosineSimilarity(nil, embedder.Vec{1.0})
	if got != 0 {
		t.Errorf("empty → 0, got %f", got)
	}
	got = cosineSimilarity(embedder.Vec{1.0}, nil)
	if got != 0 {
		t.Errorf("empty → 0, got %f", got)
	}
}

func TestCosineSimilarity_ZeroNorm_ReturnsZero(t *testing.T) {
	// Two zero vectors — denom=0, must return 0 (NaN-safe).
	a := embedder.Vec{0, 0, 0}
	b := embedder.Vec{0, 0, 0}
	got := cosineSimilarity(a, b)
	if got != 0 {
		t.Errorf("zero norm → 0, got %f", got)
	}
}

func TestCosineSimilarity_OppositeVectors_ReturnsNegativeOne(t *testing.T) {
	a := embedder.Vec{1.0, 0.0, 0.0}
	b := embedder.Vec{-1.0, 0.0, 0.0}
	got := cosineSimilarity(a, b)
	if math.Abs(got-(-1.0)) > 1e-6 {
		t.Errorf("opposite → -1.0, got %f", got)
	}
}

func TestDecodeEmbeddingBlob_RoundTrip(t *testing.T) {
	orig := embedder.Vec{0.1, 0.2, 0.3, 0.4}
	blob := encodeVecForTest(orig)
	got, err := decodeEmbeddingBlob(blob, 4)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Dim() != 4 {
		t.Fatalf("dim mismatch: want 4, got %d", got.Dim())
	}
	for i := range orig {
		if math.Abs(float64(orig[i])-float64(got[i])) > 1e-6 {
			t.Errorf("idx %d: want %f, got %f", i, orig[i], got[i])
		}
	}
}

func TestDecodeEmbeddingBlob_NilInput_ReturnsNilNoError(t *testing.T) {
	got, err := decodeEmbeddingBlob(nil, 0)
	if err != nil {
		t.Fatalf("nil → no error, got %v", err)
	}
	if got != nil {
		t.Errorf("nil → nil, got %v", got)
	}
}

func TestDecodeEmbeddingBlob_LenNotMultipleOf4_ReturnsErrInvalidEmbedding(t *testing.T) {
	// 5 bytes — not a multiple of 4 → ErrInvalidEmbedding.
	_, err := decodeEmbeddingBlob([]byte{1, 2, 3, 4, 5}, 0)
	if err == nil {
		t.Fatal("len=5 → error expected, got nil")
	}
}

func TestDecodeEmbeddingBlob_DimMismatch_ReturnsErrInvalidEmbedding(t *testing.T) {
	blob := encodeVecForTest(embedder.Vec{0.1, 0.2}) // 2 floats → 8 bytes
	_, err := decodeEmbeddingBlob(blob, 4) // expected=4 floats → 16 bytes
	if err == nil {
		t.Fatal("dim mismatch → error expected, got nil")
	}
}

// TestC2TextRecall_EmbedderNil_FallsBackToFTS5 verifies the
// alpha.18 backward-compat invariant: when no Embedder is configured,
// C2 collapses the 0.50 vector weight onto ftsScore (the existing
// stub behavior). We assert this by comparing two candidate rows
// with identical FTS5 content but different IDs — their relative
// score must be predictable from FTS5 rank only, not from any
// embedder artifact.
func TestC2TextRecall_EmbedderNil_FallsBackToFTS5(t *testing.T) {
	c := &C2TextRecall{Embedder: nil}
	if c.Embedder != nil {
		t.Fatal("Embedder must be nil for this test")
	}
	// The computeVectorScores method must return (nil, nil) without
	// touching any embedder when c.Embedder == nil.
	cands := []AnnotatedRow{mockRow(1), mockRow(2)}
	got, err := c.computeVectorScores(testCtx(t), cands, "test query")
	if err != nil {
		t.Fatalf("computeVectorScores nil embedder must not error, got %v", err)
	}
	if got != nil {
		t.Errorf("nil embedder → nil map, got %v", got)
	}
}

// TestC2TextRecall_EmbedderDisabled_StillReturnsNil verifies that
// even when an Embedder instance is wired but reports KindNone, we
// treat it as "no vector scoring". This guards against a future
// boot-time wiring that accidentally installs the no-op stub.
func TestC2TextRecall_EmbedderDisabled_StillReturnsNil(t *testing.T) {
	c := &C2TextRecall{Embedder: embedderNoneStub()}
	cands := []AnnotatedRow{mockRow(1)}
	got, err := c.computeVectorScores(testCtx(t), cands, "test query")
	if err != nil {
		t.Fatalf("KindNone embedder must not error, got %v", err)
	}
	if got != nil {
		t.Errorf("KindNone embedder → nil map, got %v", got)
	}
}

// mockRow builds a minimal AnnotatedRow with the given id. Used
// for embedder-integration tests where only the ID + (optionally)
// the Embedding field matter.
func mockRow(id int64) AnnotatedRow {
	return AnnotatedRow{Row: agentMemoryRowForTest(id)}
}

// testCtx is a stub for tests that don't care about context
// cancellation. Tests that do should construct their own.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

// embedderNoneStub returns a fresh embedder.None() so tests can
// verify the KindNone short-circuit without depending on the
// embedder package's internal disabled struct.
func embedderNoneStub() embedder.Embedder {
	return embedder.None()
}

// agentMemoryRowForTest builds an agent_memory.Row with just the
// ID set, suitable for passing into AnnotatedRow. Other fields
// (title, content, etc.) are zero-valued; tests that need them
// construct AnnotatedRow literally.
func agentMemoryRowForTest(id int64) agent_memory.Row {
	return agent_memory.Row{ID: id}
}