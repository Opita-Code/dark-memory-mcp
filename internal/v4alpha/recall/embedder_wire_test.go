// Phase 13 T-202 wire coverage. The unit-level happy paths
// (cosine math, BLOB decode round-trip, nil/disabled embedder
// fallback) are in vector_test.go. This file adds the
// integration-level coverage: an active embedder produces
// per-row cosine scores AND emits EmitEmbedderRefresh; missing
// embeddings skip rows without emitting; embedder errors
// degrade to ftsScore without surfacing. Mirrored for C4.
//
// The test embedder is internal/embedder/mock (deterministic,
// hash-based, no network). The eventholder is a tiny in-line
// recorder so this package stays self-contained.
package recall

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder/mock"
	"github.com/dark-agents/dark-memory-mcp/internal/eventholder"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// mockEmbedder returns a fresh deterministic mock embedder.
func mockEmbedder() embedder.Embedder {
	e, err := mock.New(mock.Options{})
	if err != nil {
		panic("mock.New: " + err.Error())
	}
	return e
}

// failingEmbedder is an embedder whose Embed call always returns
// an error. Used to verify best-effort fallback when the provider
// is misbehaving.
type failingEmbedder struct{ err error }

func (f *failingEmbedder) Kind() string { return "failing" }
func (f *failingEmbedder) Dim()  int    { return 32 }
func (f *failingEmbedder) Embed(ctx context.Context, _ []string) ([]embedder.Vec, error) {
	return nil, f.err
}
func (f *failingEmbedder) Close() error { return nil }

// mockEmitter records EmitEmbedderRefresh calls. The rest of the
// AutoEmitter surface is a no-op (we don't test those wires here).
// Inlined to avoid pulling the eventholder_test recorder into the
// recall package.
type mockEmitter struct {
	mu    sync.Mutex
	calls []embedderRefreshCall
}

type embedderRefreshCall struct {
	rowID          int64
	newEntityCount int
}

func (m *mockEmitter) EmitEmbedderRefresh(_ context.Context, rowID int64, newEntityCount int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, embedderRefreshCall{rowID: rowID, newEntityCount: newEntityCount})
}
func (m *mockEmitter) EmitCalibrationUpdate(_ context.Context, _ int64, _ float64, _ string) {}
func (m *mockEmitter) EmitCacheInvalidation(_ context.Context, _ string, _ int64, _ bool, _ string) {
}
func (m *mockEmitter) EmitDecayRefresh(_ context.Context, _, _ int64) {}
func (m *mockEmitter) EmitJudgeVerdictUpdate(_ context.Context, _ int64, _ string, _ float64) {
}
func (m *mockEmitter) EmitSchemaMigration(_ context.Context, _, _ int, _ string) {}
func (m *mockEmitter) EmitPersonaUpdate(_ context.Context, _, _ string)           {}
func (m *mockEmitter) EmitSupersede(_ context.Context, _, _ int64, _, _ string)    {}

func (m *mockEmitter) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockEmitter) last() (embedderRefreshCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return embedderRefreshCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// installEmitter wires a fresh mockEmitter to the global
// eventholder and registers a t.Cleanup to unset it.
func installEmitter(t *testing.T) *mockEmitter {
	t.Helper()
	e := &mockEmitter{}
	eventholder.Set(e)
	t.Cleanup(func() { eventholder.Set(nil) })
	return e
}

// rowWithEmbedding builds a candidate whose Embedding BLOB encodes
// the vector for `text` using the mock embedder's deterministic
// hash. EmbeddingDim is set so decodeEmbeddingBlob validates
// expected dim.
func rowWithEmbedding(t *testing.T, id int64, text string) AnnotatedRow {
	e := mockEmbedder()
	vecs, err := e.Embed(context.Background(), []string{text})
	if err != nil {
		t.Fatalf("mock.Embed(%q): %v", text, err)
	}
	return AnnotatedRow{
		Row:          agent_memory.Row{ID: id},
		Embedding:    encodeVecForTest(vecs[0]),
		EmbeddingDim: e.Dim(),
	}
}

// TestC2TextRecall_EmbedderActive_ComputesCosine verifies that
// when an Embedder is wired, computeVectorScores returns non-nil
// scores for candidates whose Embedding BLOB decodes cleanly.
// The deterministic mock embedder returns unit vectors so cosine
// collapses to a hash-dependent dot-product (always ≥ 0 for unit
// vectors). We assert "present in map" + value > 0 for the
// matching row.
func TestC2TextRecall_EmbedderActive_ComputesCosine(t *testing.T) {
	c := &C2TextRecall{Embedder: mockEmbedder()}
	cands := []AnnotatedRow{
		rowWithEmbedding(t, 1, "alpha version embedding text"),
		rowWithEmbedding(t, 2, "totally unrelated sentence about cooking pasta"),
	}
	scores, err := c.computeVectorScores(testCtx(t), cands, "alpha version embedding text")
	if err != nil {
		t.Fatalf("computeVectorScores: %v", err)
	}
	if len(scores) == 0 {
		t.Fatalf("active embedder → non-empty scores, got 0 entries")
	}
	if got, ok := scores[1]; !ok || got <= 0 {
		t.Errorf("cand 1 score: got %v ok=%v, want >0", got, ok)
	}
	if got, ok := scores[2]; !ok || got < 0 {
		t.Errorf("cand 2 score: got %v ok=%v, want >=0", got, ok)
	}
}

// TestC2TextRecall_EmbedderActive_EmitsEmbedderRefresh asserts
// that the wire point in computeVectorScores (c2_text.go:231-234)
// fires when the recall consumed at least one stored embedding.
// This closes Phase 12 deferred hole #2 (EmitEmbedderRefresh wire
// — was a no-op stub from Phase 12 T-103a).
func TestC2TextRecall_EmbedderActive_EmitsEmbedderRefresh(t *testing.T) {
	rec := installEmitter(t)
	c := &C2TextRecall{Embedder: mockEmbedder()}
	cands := []AnnotatedRow{rowWithEmbedding(t, 42, "wire text")}
	if _, err := c.computeVectorScores(testCtx(t), cands, "wire text"); err != nil {
		t.Fatalf("computeVectorScores: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("expected 1 EmitEmbedderRefresh, got %d", rec.count())
	}
	if last, ok := rec.last(); !ok || last.newEntityCount != 1 {
		t.Errorf("newEntityCount: got %+v ok=%v, want newEntityCount=1", last, ok)
	}
}

// TestC2TextRecall_NoCandidatesWithEmbedding_NoEmit verifies the
// complementary path: even with an active embedder, no
// EmitEmbedderRefresh fires when the candidates lack stored
// embeddings (legacy rows). This protects the audit-trail
// semantic ("emit iff we actually consumed vector signal").
func TestC2TextRecall_NoCandidatesWithEmbedding_NoEmit(t *testing.T) {
	rec := installEmitter(t)
	c := &C2TextRecall{Embedder: mockEmbedder()}
	cands := []AnnotatedRow{mockRow(7), mockRow(8)} // no Embedding BLOB
	if _, err := c.computeVectorScores(testCtx(t), cands, "anything"); err != nil {
		t.Fatalf("computeVectorScores: %v", err)
	}
	if rec.count() != 0 {
		t.Errorf("no embedding rows → 0 EmitEmbedderRefresh calls, got %d", rec.count())
	}
}

// TestC2TextRecall_EmbedderError_SurfacesForCallerFallback verifies
// the contract documented at c2_text.go:175-178: computeVectorScores
// returns the embedder error (does NOT swallow it). The caller —
// the Recall method at c2_text.go:112-116 — catches the error and
// degrades vectorScores to nil so scoreFTSPlusGraph falls back to
// ftsScore. This split exists so callers can choose whether to
// log/observe the degradation explicitly without forcing a hard
// failure on the recall path.
//
// This is the load-bearing invariant for the no-API-key
// deployment story: operators without an embedder configured get
// recalled results, never an opaque failure.
func TestC2TextRecall_EmbedderError_SurfacesForCallerFallback(t *testing.T) {
	failing := &failingEmbedder{err: errors.New("simulated network timeout")}
	c := &C2TextRecall{Embedder: failing}
	cands := []AnnotatedRow{rowWithEmbedding(t, 1, "any text")}
	scores, err := c.computeVectorScores(testCtx(t), cands, "any text")
	if err == nil {
		t.Fatalf("computeVectorScores must surface embedder error for caller to decide, got nil err")
	}
	if !errors.Is(err, failing.err) {
		t.Errorf("error chain must preserve failingEmbedder.err, got %v", err)
	}
	if scores != nil {
		t.Errorf("on embedder error → nil map (caller degrades), got %v", scores)
	}
}

// TestC4ResearchRecall_EmbedderActive_ComputesCosine — mirror of
// the C2 happy path for the research strategy.
func TestC4ResearchRecall_EmbedderActive_ComputesCosine(t *testing.T) {
	c := &C4ResearchRecall{Embedder: mockEmbedder()}
	cands := []AnnotatedRow{
		rowWithEmbedding(t, 11, "research alpha version"),
		rowWithEmbedding(t, 12, "completely different cooking topic"),
	}
	scores, err := c.computeVectorScores(testCtx(t), cands, "research alpha version")
	if err != nil {
		t.Fatalf("computeVectorScores: %v", err)
	}
	if len(scores) == 0 {
		t.Fatalf("active embedder → non-empty scores, got 0")
	}
	if got, ok := scores[11]; !ok || got <= 0 {
		t.Errorf("cand 11 score: got %v ok=%v, want >0", got, ok)
	}
}

// TestC4ResearchRecall_EmbedderActive_EmitsEmbedderRefresh —
// mirror of the C2 emit assertion for the research strategy.
func TestC4ResearchRecall_EmbedderActive_EmitsEmbedderRefresh(t *testing.T) {
	rec := installEmitter(t)
	c := &C4ResearchRecall{Embedder: mockEmbedder()}
	cands := []AnnotatedRow{rowWithEmbedding(t, 99, "research wire")}
	if _, err := c.computeVectorScores(testCtx(t), cands, "research wire"); err != nil {
		t.Fatalf("computeVectorScores: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("expected 1 EmitEmbedderRefresh, got %d", rec.count())
	}
}

// TestC4ResearchRecall_NoCandidatesWithEmbedding_NoEmit — mirror
// of the C2 negative path for the research strategy.
func TestC4ResearchRecall_NoCandidatesWithEmbedding_NoEmit(t *testing.T) {
	rec := installEmitter(t)
	c := &C4ResearchRecall{Embedder: mockEmbedder()}
	cands := []AnnotatedRow{mockRow(13)} // no Embedding BLOB
	if _, err := c.computeVectorScores(testCtx(t), cands, "anything"); err != nil {
		t.Fatalf("computeVectorScores: %v", err)
	}
	if rec.count() != 0 {
		t.Errorf("no embedding rows → 0 EmitEmbedderRefresh calls, got %d", rec.count())
	}
}

// TestScoreFTSPlusGraph_VectorScoresChangeRanking verifies that
// passing a populated vectorScores map (vs nil) actually affects
// the per-row score when the strategy weights include Vector > 0.
// This is the load-bearing test for "real embedder contributes
// to ranking" — without it the wire could rot silently and the
// recall would keep using ftsScore fallback. We use the same row
// shape across two passes so the only variable is the
// vectorScores map.
func TestScoreFTSPlusGraph_VectorScoresChangeRanking(t *testing.T) {
	w := Weights{FTS5: 0.40, Vector: 0.50, Graph: 0.10} // C2 canonical
	candidates := []AnnotatedRow{
		{Row: agent_memory.Row{ID: 1}},
		{Row: agent_memory.Row{ID: 2}},
	}
	seeds := candidates
	graph := map[int64]int{}

	withNil := scoreFTSPlusGraph(candidates, seeds, graph, w, nil)
	withVector := scoreFTSPlusGraph(candidates, seeds, graph, w, map[int64]float64{
		1: 0.99, // row 1 highly similar to the query
		2: 0.01, // row 2 orthogonal-ish
	})

	if len(withNil) != 2 || len(withVector) != 2 {
		t.Fatalf("len mismatch: nil=%d vector=%d", len(withNil), len(withVector))
	}
	var nilRow2, withRow2 float64
	for _, r := range withNil {
		if r.row.ID == 2 {
			nilRow2 = r.score
		}
	}
	for _, r := range withVector {
		if r.row.ID == 2 {
			withRow2 = r.score
		}
	}
	if withRow2 >= nilRow2 {
		t.Errorf("row 2: withVector=%v should be < nil=%v (vector scored 0.01 pulls it down)",
			withRow2, nilRow2)
	}
}