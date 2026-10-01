// Package recall — prograph_test.go: MultiHopRetrieve hermetic
// unit tests using a hand-rolled ProGraphSource mock (no SQLite).
//
// 12 tests:
//   1. Depth=0 returns only the BM25 seeds.
//   2. Depth=1 returns seeds + 1-hop neighbors (entity overlap).
//   3. Depth=2 returns seeds + 1-hop + 2-hop.
//   4. Cycle detection: A↔B entities do not infinite loop.
//   5. Depth limit: depth=2 does not visit depth=3.
//   6. Visited set: a row reachable by multiple paths stays at the
//      shortest depth.
//   7. Empty query returns (nil, nil) without error.
//   8. ErrDepthOutOfRange for depth<0 or depth>2.
//   9. ErrNilStore for nil store.
//  10. Entity dedup: "Dark" and "dark" collapse in the index.
//  11. Score ordering: depth ASC, score DESC, mem_id ASC.
//  12. tokenizeQuery reuses internal/entity.Extract (no duplicate logic).
package recall

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/entity"
)

// fakeProGraph is a ProGraphSource mock that serves a fixed
// (mem_id → entities) map + a query → hits map. No disk, no
// concurrency. Used only by these tests.
type fakeProGraph struct {
	// entities maps mem_id → list of entity strings (will be lower-cased
	// at lookup time to mirror the store contract).
	entities map[int64][]string
	// hits maps a query substring → ordered list of seed mem_ids.
	// The mock's SearchAgentMemory returns hits[i] for any query that
	// contains hits' key substring.
	hits map[string][]int64
	// activeProject (unused by MultiHopRetrieve but kept for interface
	// parity in case future tests need it).
	activeProject string
}

func newFakeProGraph() *fakeProGraph {
	return &fakeProGraph{
		entities:      make(map[int64][]string),
		hits:          make(map[string][]int64),
		activeProject: "default",
	}
}

func (f *fakeProGraph) addRow(memID int64, entities ...string) {
	f.entities[memID] = append(f.entities[memID], entities...)
}

func (f *fakeProGraph) addQuery(query string, memIDs ...int64) {
	f.hits[query] = append(f.hits[query], memIDs...)
}

func (f *fakeProGraph) SearchAgentMemory(_ context.Context, sf agentmemory.SearchFilters) ([]agentmemory.SearchHit, error) {
	// Match any registered query substring.
	for q, ids := range f.hits {
		if strings.Contains(sf.Query, q) || q == sf.Query {
			out := make([]agentmemory.SearchHit, 0, len(ids))
			for rank, id := range ids {
				out = append(out, agentmemory.SearchHit{
					AgentMemory: agentmemory.AgentMemory{ID: id},
					Rank:        float64(rank + 1),
				})
			}
			return out, nil
		}
	}
	return nil, nil
}

func (f *fakeProGraph) GetAgentMemoryEntities(_ context.Context, memID int64) ([]agentmemory.Entity, error) {
	vals, ok := f.entities[memID]
	if !ok || len(vals) == 0 {
		return nil, nil
	}
	out := make([]agentmemory.Entity, 0, len(vals))
	for _, v := range vals {
		out = append(out, agentmemory.Entity{
			Value:      strings.ToLower(v),
			Source:     "deterministic",
			Confidence: 1.0,
		})
	}
	return out, nil
}

func (f *fakeProGraph) ListAgentMemoryByAnyEntity(_ context.Context, values []string) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	// Lowercase + dedup input.
	seenInput := make(map[string]struct{}, len(values))
	wanted := make([]string, 0, len(values))
	for _, v := range values {
		lv := strings.ToLower(strings.TrimSpace(v))
		if lv == "" {
			continue
		}
		if _, dup := seenInput[lv]; dup {
			continue
		}
		seenInput[lv] = struct{}{}
		wanted = append(wanted, lv)
	}
	// Find every row whose entity list contains at least one wanted value.
	seenRows := make(map[int64]struct{})
	for memID, ents := range f.entities {
		for _, e := range ents {
			le := strings.ToLower(e)
			for _, w := range wanted {
				if le == w {
					seenRows[memID] = struct{}{}
					break
				}
			}
			if _, ok := seenRows[memID]; ok {
				break
			}
		}
	}
	if len(seenRows) == 0 {
		return nil, nil
	}
	out := make([]int64, 0, len(seenRows))
	for id := range seenRows {
		out = append(out, id)
	}
	// Sort ASC for determinism (mirrors the Store contract).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

// --- Test 1: Depth=0 returns only seeds ------------------------------------

func TestMultiHopRetrieve_DepthZero_SeedsOnly(t *testing.T) {
	f := newFakeProGraph()
	f.addQuery("alpha", 1, 2)
	f.addRow(1, "alpha", "beta")
	f.addRow(2, "alpha", "gamma")
	f.addRow(3, "delta", "epsilon") // unrelated; must NOT appear

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 0, MultiHopOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantIDs := []int64{1, 2}
	gotIDs := hitMemIDs(got)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("Depth=0 mem_ids = %v, want %v", gotIDs, wantIDs)
	}
	for _, h := range got {
		if h.Depth != 0 {
			t.Errorf("Depth=0 hit has Depth=%d", h.Depth)
		}
		if h.Score != 1.0 {
			t.Errorf("Depth=0 hit has Score=%v, want 1.0", h.Score)
		}
	}
}

// --- Test 2: Depth=1 returns seeds + 1-hop neighbors -----------------------

func TestMultiHopRetrieve_DepthOne_EntityOverlap(t *testing.T) {
	f := newFakeProGraph()
	f.addQuery("alpha", 1)
	f.addRow(1, "alpha", "beta")
	// Row 2 shares "beta" with row 1 — depth=1 hit.
	f.addRow(2, "beta", "gamma")
	// Row 3 has no entity overlap with row 1 — must NOT appear.
	f.addRow(3, "delta", "epsilon")
	// Row 4 shares "alpha" with row 1 — depth=1 hit.
	f.addRow(4, "alpha", "kappa")

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 1, MultiHopOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect: [1 (depth=0), 2 (depth=1), 4 (depth=1)] in that order
	// (depth ASC, then mem_id ASC within depth=1 ties).
	wantIDs := []int64{1, 2, 4}
	gotIDs := hitMemIDs(got)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("Depth=1 mem_ids = %v, want %v", gotIDs, wantIDs)
	}
	for _, h := range got {
		if h.Depth > 1 {
			t.Errorf("Depth=1 hit has Depth=%d", h.Depth)
		}
	}
	// depth=1 hits must have a non-empty ViaEntity.
	for _, h := range got {
		if h.Depth == 1 && h.ViaEntity == "" {
			t.Errorf("depth=1 hit MemID=%d has empty ViaEntity", h.MemID)
		}
	}
}

// --- Test 3: Depth=2 returns seeds + 1-hop + 2-hop -------------------------

func TestMultiHopRetrieve_DepthTwo_ThreeLayers(t *testing.T) {
	f := newFakeProGraph()
	// 3-row chain: 1 → 2 → 3 (each pair shares one entity).
	// Use tokens ≥3 chars (EntityStore's minTokenLen mirrors internal/entity).
	f.addQuery("alpha", 1)
	f.addRow(1, "alpha", "first")
	f.addRow(2, "first", "second") // depth=1
	f.addRow(3, "second", "third") // depth=2
	// Isolated row — must NOT appear.
	f.addRow(99, "unrelated", "noise")

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 2, MultiHopOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantIDs := []int64{1, 2, 3}
	gotIDs := hitMemIDs(got)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("Depth=2 mem_ids = %v, want %v", gotIDs, wantIDs)
	}
	depths := map[int64]int{}
	for _, h := range got {
		depths[h.MemID] = h.Depth
	}
	if depths[1] != 0 {
		t.Errorf("mem 1 Depth=%d, want 0 (seed)", depths[1])
	}
	if depths[2] != 1 {
		t.Errorf("mem 2 Depth=%d, want 1", depths[2])
	}
	if depths[3] != 2 {
		t.Errorf("mem 3 Depth=%d, want 2", depths[3])
	}
	// mem 99 must not be visited.
	if _, ok := depths[99]; ok {
		t.Errorf("isolated mem 99 should not appear; depths=%v", depths)
	}
}

// --- Test 4: Cycle detection — A↔B does not infinite loop ------------------

func TestMultiHopRetrieve_CycleDoesNotLoop(t *testing.T) {
	f := newFakeProGraph()
	// 2-row cycle: 1 ↔ 2 share entity "shared".
	f.addQuery("alpha", 1)
	f.addRow(1, "alpha", "shared")
	f.addRow(2, "shared", "beta")
	f.addRow(3, "shared", "gamma") // also reachable via "shared"; should appear at depth=1.

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 2, MultiHopOptions{TotalLimit: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each row must appear at most ONCE.
	seen := map[int64]int{}
	for _, h := range got {
		seen[h.MemID]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("mem %d appeared %d times (cycle not detected?)", id, count)
		}
	}
	// 1, 2, 3 should all be present (BFS terminated cleanly).
	wantSet := map[int64]bool{1: true, 2: true, 3: true}
	for id := range wantSet {
		if seen[id] == 0 {
			t.Errorf("mem %d missing from result", id)
		}
	}
}

// --- Test 5: Depth limit honored (depth=2 does not visit depth=3) ---------

func TestMultiHopRetrieve_DepthLimitHonored(t *testing.T) {
	f := newFakeProGraph()
	// 4-row chain: 1 → 2 → 3 → 4 (each pair shares one entity).
	f.addQuery("alpha", 1)
	f.addRow(1, "alpha", "first")
	f.addRow(2, "first", "second") // depth=1
	f.addRow(3, "second", "third") // depth=2
	f.addRow(4, "third", "fourth") // depth=3 — must NOT appear when depth=2

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 2, MultiHopOptions{TotalLimit: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, h := range got {
		if h.MemID == 4 {
			t.Errorf("mem 4 (depth=3) appeared with Depth=%d; depth limit violated", h.Depth)
		}
	}
	for _, h := range got {
		if h.Depth > 2 {
			t.Errorf("Depth > 2 in result: %+v", h)
		}
	}
}

// --- Test 6: Visited set — shortest depth wins ----------------------------

func TestMultiHopRetrieve_VisitedShortestDepthWins(t *testing.T) {
	f := newFakeProGraph()
	// Row 3 is reachable via TWO paths:
	//   path A: 1 → 2 → 3 (depth=2)
	//   path B: 1 → 3 (depth=1, via "shared" with row 1)
	// Row 3 should appear at Depth=1, NOT Depth=2.
	f.addQuery("alpha", 1)
	f.addRow(1, "alpha", "shared")
	f.addRow(2, "shared", "shared2") // depth=1 via "shared"
	f.addRow(3, "shared", "shared2") // reachable from both 1 (depth=1) and 2 (depth=2)

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 2, MultiHopOptions{TotalLimit: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	depths := map[int64]int{}
	for _, h := range got {
		depths[h.MemID] = h.Depth
	}
	if depths[3] != 1 {
		t.Errorf("mem 3 Depth=%d, want 1 (shortest path)", depths[3])
	}
}

// --- Test 7: Empty query → (nil, nil) -------------------------------------

func TestMultiHopRetrieve_EmptyQuery_NoError(t *testing.T) {
	f := newFakeProGraph()
	got, err := MultiHopRetrieve(context.Background(), f, "", 2, MultiHopOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("empty query returned %v, want nil", got)
	}
	// Whitespace-only also returns nil.
	got, err = MultiHopRetrieve(context.Background(), f, "   \t\n", 2, MultiHopOptions{})
	if err != nil {
		t.Fatalf("whitespace query error: %v", err)
	}
	if got != nil {
		t.Errorf("whitespace query returned %v, want nil", got)
	}
}

// --- Test 8: ErrDepthOutOfRange --------------------------------------------

func TestMultiHopRetrieve_DepthOutOfRange(t *testing.T) {
	f := newFakeProGraph()
	for _, d := range []int{-1, 3, 100} {
		_, err := MultiHopRetrieve(context.Background(), f, "alpha", d, MultiHopOptions{})
		if !errors.Is(err, ErrDepthOutOfRange) {
			t.Errorf("depth=%d returned err=%v, want ErrDepthOutOfRange", d, err)
		}
	}
}

// --- Test 9: ErrNilStore --------------------------------------------------

func TestMultiHopRetrieve_NilStore(t *testing.T) {
	_, err := MultiHopRetrieve(context.Background(), nil, "alpha", 1, MultiHopOptions{})
	if !errors.Is(err, ErrNilStore) {
		t.Errorf("nil store returned err=%v, want ErrNilStore", err)
	}
}

// --- Test 10: Entity dedup — case-insensitive -----------------------------

func TestMultiHopRetrieve_EntityDedupCaseInsensitive(t *testing.T) {
	f := newFakeProGraph()
	f.addQuery("alpha", 1)
	// Mixed-case entities should collapse.
	f.addRow(1, "Alpha", "BETA")
	f.addRow(2, "beta", "Gamma") // shares "beta" with row 1 — depth=1

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 1, MultiHopOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantIDs := []int64{1, 2}
	gotIDs := hitMemIDs(got)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("case-insensitive dedup mem_ids = %v, want %v", gotIDs, wantIDs)
	}
}

// --- Test 11: Score + sort ordering ---------------------------------------

func TestMultiHopRetrieve_SortOrder(t *testing.T) {
	f := newFakeProGraph()
	// Build a graph where the BFS would naturally produce:
	//   mem 1 (depth=0, score 1.0) — seed
	//   mem 2 (depth=1, score 0.5) — shares entity with seed
	//   mem 3 (depth=1, score 0.5) — shares entity with seed (higher mem_id)
	//   mem 4 (depth=2, score 0.33) — 2-hop from seed
	// Use tokens ≥3 chars (EntityStore's minTokenLen).
	f.addQuery("alpha", 1)
	f.addRow(1, "alpha", "shared")
	f.addRow(2, "shared", "extra")
	f.addRow(3, "shared", "other")
	f.addRow(4, "extra", "other") // reachable from 2 and 3 at depth=2

	got, err := MultiHopRetrieve(context.Background(), f, "alpha", 2, MultiHopOptions{TotalLimit: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Sort must be (depth ASC, score DESC, mem_id ASC):
	//   1 (0, 1.0)
	//   2 (1, 0.5)
	//   3 (1, 0.5)   ← 2 < 3
	//   4 (2, 0.33)
	wantIDs := []int64{1, 2, 3, 4}
	gotIDs := hitMemIDs(got)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("sort order mem_ids = %v, want %v (depth ASC, score DESC, mem_id ASC)", gotIDs, wantIDs)
	}
}

// --- Test 12: tokenizeQuery reuses internal/entity.Extract ----------------

func TestTokenizeQuery_MatchesEntityExtract(t *testing.T) {
	cases := []struct {
		in   string
		want []string // expected subset (order-stable)
	}{
		{"dark memory mcp", []string{"dark", "memory", "mcp"}},
		{"chunk 8.4 audit verify", []string{"chunk", "audit", "verify"}},
		{"the quick brown fox", []string{"quick", "brown", "fox"}}, // "the" is stopword
	}
	for _, c := range cases {
		got := tokenizeQuery(c.in)
		// Each expected token must be present (order may differ; we just
		// verify the rule set is consistent with entity.Extract).
		// Run entity.Extract independently for cross-check.
		refEnts := entity.Extract(c.in, "", "")
		ref := make([]string, 0, len(refEnts))
		for _, e := range refEnts {
			ref = append(ref, e.Value)
		}
		if !reflect.DeepEqual(got, ref) {
			t.Errorf("tokenizeQuery(%q) = %v, entity.Extract gave %v", c.in, got, ref)
		}
		for _, w := range c.want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("tokenizeQuery(%q) missing %q", c.in, w)
			}
		}
	}
}

// hitMemIDs extracts the mem_id field of a RecallHit slice in order.
func hitMemIDs(hits []RecallHit) []int64 {
	out := make([]int64, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.MemID)
	}
	return out
}