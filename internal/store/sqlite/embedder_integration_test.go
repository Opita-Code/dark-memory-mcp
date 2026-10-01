// embedder_integration_test.go — Phase 9 alpha.20 Chunk 8.3.
//
// End-to-end integration tests for the embedder wiring path:
// Store.WithEmbedder → SearchAgentMemory Mode dispatch (bm25|vector|rrf).
//
// These tests use the deterministic mock embedder
// (internal/embedder/mock) so they:
//   - don't need OPENAI_API_KEY,
//   - don't need Ollama running,
//   - don't need libonnxruntime binary extracted,
//   - run identically in CI and locally.
//
// The mock embedder hashes the input text into a stable unit vector
// of the configured Dim; tests assert RELATIVE ordering between
// semantically-clustered documents rather than absolute scores.
//
// # Why pre-populate embeddings instead of auto-computing
//
// SaveAgentMemory does NOT auto-compute the embedding (design
// decision — see SaveAgentMemory doc comment at internal/store/sqlite/
// store.go:4330). The caller (orchestrator/recall) is responsible
// for invoking s.Embedder().Embed(ctx, []string{content}) and
// assigning the result to m.Embedding before save. The hybrid
// search path was designed for this caller contract.
//
// Run with `go test ./internal/store/sqlite -run TestEmbedderIntegration`.
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder/mock"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// newEmbedderTestStore creates a test Store wired up to the
// deterministic mock embedder (32-dim hash-based). Returns the store
// and a cleanup func.
func newEmbedderTestStore(t *testing.T, dim int) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "embedder-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	if dim <= 0 {
		dim = mock.DefaultDim
	}
	emb, err := mock.New(mock.Options{Dim: dim})
	if err != nil {
		t.Fatalf("mock.New: %v", err)
	}
	st.WithEmbedder(emb)
	cleanup := func() {
		_ = emb.Close()
		_ = st.Close()
	}
	return st, cleanup
}

// seedRowWithEmbedding saves an agent_memory row with a pre-computed
// embedding (the caller contract per SaveAgentMemory docs). Returns
// the row id. Uses a clean lexical profile (no hyphens+numbers) so
// FTS5 MATCH parsing doesn't interpret "19" as a column reference.
func seedRowWithEmbedding(t *testing.T, st store.Store, op, content string) int64 {
	t.Helper()
	emb := st.Embedder()
	vecs, err := emb.Embed(context.Background(), []string{content})
	if err != nil {
		t.Fatalf("Embed(%q): %v", content, err)
	}
	if len(vecs) == 0 {
		t.Fatalf("Embed(%q): returned 0 vectors", content)
	}
	id, err := st.SaveAgentMemory(context.Background(),
		store.WriteContext{Actor: op, WritePath: "TestEmbedderIntegration"},
		&agentmemory.AgentMemory{
			Operator:   op,
			Kind:       "observation",
			Content:    content,
			Tags:       "embedder-integration-test",
			Embedding:  vecs[0],
		})
	if err != nil {
		t.Fatalf("SaveAgentMemory(%q): %v", content, err)
	}
	return id
}

// --- Test 1: WithEmbedder wires the mock at boot ---

func TestEmbedderIntegration_WithEmbedder_KindNotNone(t *testing.T) {
	st, cleanup := newEmbedderTestStore(t, mock.DefaultDim)
	defer cleanup()
	got := st.Embedder().Kind()
	if got == embedder.KindNone {
		t.Errorf("Store.Embedder().Kind() = %q after WithEmbedder(mock); want non-none", got)
	}
	if got != embedder.KindMock {
		t.Errorf("Store.Embedder().Kind() = %q; want %q", got, embedder.KindMock)
	}
	if dim := st.Embedder().Dim(); dim != mock.DefaultDim {
		t.Errorf("Store.Embedder().Dim() = %d; want %d", dim, mock.DefaultDim)
	}
}

// --- Test 2: Mode=rrf fuses BM25 + vector arms via RRF ---

func TestEmbedderIntegration_RRFReturnsSemanticMatch(t *testing.T) {
	st, cleanup := newEmbedderTestStore(t, mock.DefaultDim)
	defer cleanup()
	ctx := context.Background()

	// Three rows. All share some lexical surface with the query
	// so BM25 arm contributes; the semantic cluster (vec-target)
	// is closest in cosine space so vector arm ranks it first;
	// RRF fuses both signals.
	target := "alpha deferred items completion status report"
	decoy := "how to cook lentils on a budget"
	neutral := "weekly meeting agenda distributed to team"
	seedRowWithEmbedding(t, st, "rrf-baseline", target)
	seedRowWithEmbedding(t, st, "rrf-decoy", decoy)
	seedRowWithEmbedding(t, st, "rrf-neutral", neutral)

	// Query shares SOME tokens with the target row ("complete",
	// "deferred", "items" overlap with "completion") so BM25 arm
	// contributes a hit, AND the semantic cluster is the closest
	// in vector space.
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "alpha deferred items completion",
		Mode:  "rrf",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("SearchAgentMemory(Mode=rrf): %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("Mode=rrf returned 0 hits; expected at least 1 from the BM25 or vector arm")
	}
	// The semantic target should appear in the top-3.
	foundTarget := false
	for i := 0; i < len(hits) && i < 3; i++ {
		if hits[i].Content == target {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		var dump []string
		for _, h := range hits {
			dump = append(dump, h.Content)
		}
		t.Errorf("Mode=rrf top-3 did not include the semantic target; got: %v", dump)
	}
}

// --- Test 3: Mode=vector returns cosine ranking when no lexical overlap ---

func TestEmbedderIntegration_VectorCosineRanking(t *testing.T) {
	st, cleanup := newEmbedderTestStore(t, mock.DefaultDim)
	defer cleanup()
	ctx := context.Background()

	vecA := "the quick brown fox jumps over the lazy dog"
	vecB := "a fast russet vulpine leaps above the idle hound"
	vecC := "completely unrelated topic about database migrations"
	seedRowWithEmbedding(t, st, "vec-A", vecA)
	seedRowWithEmbedding(t, st, "vec-B", vecB)
	seedRowWithEmbedding(t, st, "vec-C", vecC)

	// Query is lexically disjoint from all 3 (no BM5 hits) but
	// semantically adjacent to vec-A and vec-B (similar tokens
	// in the hash-based mock). vec-C should rank LAST.
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "fast brown fox jumps lazy dog",
		Mode:  "vector",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchAgentMemory(Mode=vector): %v", err)
	}
	if len(hits) < 3 {
		t.Fatalf("Mode=vector returned %d hits; want at least 3 (one per seeded row)", len(hits))
	}
	// The two semantically-related rows (vec-A, vec-B) should
	// appear BEFORE the unrelated one (vec-C) in the ranking.
	idxC := -1
	for i, h := range hits {
		if h.Content == vecC {
			idxC = i
			break
		}
	}
	if idxC == -1 {
		t.Errorf("Mode=vector: unrelated row (vec-C) not present in results; got %d hits", len(hits))
	} else {
		// Count how many semantic hits precede vec-C.
		semanticBeforeC := 0
		for i := 0; i < idxC; i++ {
			c := hits[i].Content
			if c == vecA || c == vecB {
				semanticBeforeC++
			}
		}
		if semanticBeforeC == 0 {
			t.Errorf("Mode=vector: unrelated row (vec-C) ranked before all semantic matches (idx=%d)", idxC)
		}
	}
}

// --- Test 4: FactoryAuto fail-safe behavior ---

func TestEmbedderIntegration_FactoryAutoLadder(t *testing.T) {
	// Step 1: typo in DARK_MEMORY_EMBEDDER → fail-safe to none.
	t.Setenv("DARK_MEMORY_EMBEDDER", "openai-beta")
	e := embedder.FactoryAuto()
	if e.Kind() != embedder.KindNone {
		t.Errorf("FactoryAuto with DARK_MEMORY_EMBEDDER=openai-beta: Kind()=%q, want %q (typo → fail-safe)",
			e.Kind(), embedder.KindNone)
	}
	_ = e.Close()

	// Step 2: DARK_MEMORY_EMBEDDER unset + OPENAI_API_KEY unset →
	// FactoryAuto() falls through to the stub (none) in the
	// absence of any reachable backend.
	t.Setenv("DARK_MEMORY_EMBEDDER", "")
	t.Setenv("OPENAI_API_KEY", "")
	e2 := embedder.FactoryAuto()
	if e2.Kind() != embedder.KindNone {
		t.Errorf("FactoryAuto with no env config: Kind()=%q, want %q (BM25-only fallback)",
			e2.Kind(), embedder.KindNone)
	}
	_ = e2.Close()
}

// --- Test 5: WithEmbedder(nil) restores the disabled stub ---

func TestEmbedderIntegration_WithEmbedderNilRestoresStub(t *testing.T) {
	st, cleanup := newEmbedderTestStore(t, mock.DefaultDim)
	defer cleanup()
	// First assert the mock is wired.
	if st.Embedder().Kind() == embedder.KindNone {
		t.Fatalf("precondition violated: mock not wired after WithEmbedder(mock)")
	}
	// Now pass nil — should restore the disabled stub.
	st.WithEmbedder(nil)
	if st.Embedder().Kind() != embedder.KindNone {
		t.Errorf("WithEmbedder(nil): Embedder().Kind()=%q, want %q (nil → none)",
			st.Embedder().Kind(), embedder.KindNone)
	}
}