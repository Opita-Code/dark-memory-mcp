// Package recall — prograph.go: ProGraph 2-layer entity extraction
// engine (Phase 9 alpha.20 Chunk 8.4, ADR-015).
//
// # Scope
//
// ProGraph is the v3 in-memory entity graph that BFS-expands from
// BM25 seeds through entity-overlap. It is a different graph from
// the v4alpha citation graph (which uses adr_refs / inv_refs via
// internal/v4alpha/recall/graphExpandShared) — ProGraph is the
// entity-overlap graph ProGraph-paper-style. Both can coexist.
//
// # Two layers
//
//   - Layer 1 (entities): the typed noun phrases extracted from
//     each row's content + title + tags. Source: internal/entity
//     (deterministic for v3; LLM-driven for alpha.21+).
//   - Layer 2 (rows that share entities): the rows themselves,
//     connected when they share at least one entity value (OR
//     semantics, case-insensitive).
//
// Multi-hop traversal walks Layer 1 → Layer 2 → Layer 1 → ...
// with visited-row tracking to prevent cycles.
//
// # Public API
//
//   - ExtractEntities(content, title, tags) []Entity
//     Thin wrapper over internal/entity.Extract that returns the
//     recall.Entity shape (with the ID + Type fields layered on top).
//
//   - MultiHopRetrieve(ctx, store, query, depth, opts) ([]RecallHit, error)
//     Run the BFS. depth must be 0, 1, or 2; >2 returns
//     ErrDepthOutOfRange. The Store must have an active project.
//
//   - RecallHit: one BFS result. Depth is the hop count (0 for
//     seeds, 1 for direct neighbors, 2 for neighbors-of-neighbors).
//     ViaEntity is the lower-cased entity value that connected this
//     row to the prior frontier (empty for seeds).
//
// # Determinism
//
// BFS visits rows in mem_id ASC order at each hop. Results are
// sorted by (depth ASC, score DESC, mem_id ASC) so callers get
// the same hit list across runs with the same input. Tests rely
// on the ordering for non-flaky assertions.
//
// # Cycle detection
//
// Visited set tracks mem_ids added to the result. A row already
// in the visited set is skipped even if a shorter path would
// reach it later. The set is keyed on mem_id, not entity value,
// so revisiting an entity that connects to already-seen rows is
// harmless (BFS terminates when no NEW row is added at a hop).
package recall

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/entity"
)

// RecallHit is one BFS result from MultiHopRetrieve.
//
// Depth semantics:
//
//   - depth == 0: the row was a BM25 seed (returned for the query).
//   - depth == 1: the row shares at least one entity with a seed.
//   - depth == 2: the row shares at least one entity with a depth-1 row.
//
// ViaEntity is the lower-cased entity value that connected this row
// to the prior frontier. Empty for seeds.
//
// Score is the BM25 rank for seeds; for entity-expanded hits it is
// 1.0 / float64(depth+1) (depth == 1 → 0.5, depth == 2 → 0.33).
// Operators reading the result can prefer shallower hits via Depth
// (sort is depth ASC then score DESC then mem_id ASC).
type RecallHit struct {
	MemID     int64   `json:"mem_id"`
	Depth     int     `json:"depth"`
	ViaEntity string  `json:"via_entity,omitempty"`
	Score     float64 `json:"score"`
}

// ProGraphSource is the minimal Store seam ProGraph needs. It is a
// 3-method subset of store.Store so hermetic tests can supply a fake
// without implementing the full 100-method interface. The concrete
// store.Store (sqlite + postgres) satisfies it automatically.
type ProGraphSource interface {
	SearchAgentMemory(ctx context.Context, f agentmemory.SearchFilters) ([]agentmemory.SearchHit, error)
	GetAgentMemoryEntities(ctx context.Context, memID int64) ([]agentmemory.Entity, error)
	// ListAgentMemoryByAnyEntity returns the deduped mem_id list (in
	// the active project) whose entity list contains at least one of
	// the given values (OR semantics). Empty input or no match → nil.
	// Postgres returns notImpl (mirrors the rest of the entity surface).
	ListAgentMemoryByAnyEntity(ctx context.Context, entityValues []string) ([]int64, error)
}

// MultiHopOptions configures MultiHopRetrieve. Zero-value uses
// sensible defaults:
//
//   - SeedLimit: 20   (BM25 seeds returned by the Store)
//   - HopLimit:  50   (max rows added per hop)
//   - TotalLimit:100  (cap on the result slice size)
//
// Negative or zero values are replaced with the default at call
// time. Operators can tune these for large stores (HopLimit=200,
// TotalLimit=500 are reasonable for 10k+ row databases).
type MultiHopOptions struct {
	SeedLimit  int
	HopLimit   int
	TotalLimit int
}

func (o *MultiHopOptions) applyDefaults() {
	if o.SeedLimit <= 0 {
		o.SeedLimit = 20
	}
	if o.HopLimit <= 0 {
		o.HopLimit = 50
	}
	if o.TotalLimit <= 0 {
		o.TotalLimit = 100
	}
}

// ErrDepthOutOfRange is returned when depth is not 0, 1, or 2.
// ProGraph is empirically calibrated at 1-2 hops (MemHop benchmark
// median); 3+ hops is alpha.21+ with explicit operator opt-in.
var ErrDepthOutOfRange = errors.New("recall: MultiHopRetrieve: depth must be 0, 1, or 2 (3+ hops reserved for alpha.21+)")

// ErrNilStore is returned when MultiHopRetrieve is called with a
// nil store.Store.
var ErrNilStore = errors.New("recall: MultiHopRetrieve: nil store")

// ExtractEntities wraps internal/entity.Extract to produce the
// recall.Entity shape. Same dedup + normalization rules as the
// upstream extractor (lowercase, stopword filter, minTokenLen=3).
//
// Empty input → empty output (no error).
func ExtractEntities(content, title, tags string) []Entity {
	upstream := entity.Extract(content, title, tags)
	if len(upstream) == 0 {
		return nil
	}
	out := make([]Entity, 0, len(upstream))
	for _, u := range upstream {
		out = append(out, Entity{
			ID:         0, // populated at Save time by the Store
			Type:       "", // v3 PR-3 deterministic does not classify
			Value:      u.Value,
			Confidence: u.Confidence,
		})
	}
	return out
}

// MultiHopRetrieve runs the ProGraph BFS.
//
// Algorithm:
//
//  1. BM25 seeds via Store.SearchAgentMemory(query, Mode="bm25").
//  2. For each seed, fetch its entities via Store.GetAgentMemoryEntities.
//  3. Build an EntityStore index from (mem_id, entities) tuples.
//  4. BFS up to `depth` hops:
//     - depth=0: just the seeds.
//     - depth=1: add rows that share ANY entity with seeds.
//     - depth=2: add rows that share ANY entity with depth-1 rows.
//  5. visited set prevents cycles (keyed on mem_id).
//  6. Each hop expansion is capped at opts.HopLimit; total result
//     is capped at opts.TotalLimit.
//
// Returns (nil, nil) for an empty query or no active project
// (callers do not need to distinguish "no matches" from "no project"
// for read-only callers — both are "result absorbed").
//
// Errors:
//
//   - ErrNilStore if st is nil.
//   - ErrDepthOutOfRange if depth < 0 or depth > 2.
//   - Store errors propagated (wrapped) so callers can branch on
//     ErrNotConfigured (Postgres stub) etc.
func MultiHopRetrieve(ctx context.Context, st ProGraphSource, query string, depth int, opts MultiHopOptions) ([]RecallHit, error) {
	if st == nil {
		return nil, ErrNilStore
	}
	if depth < 0 || depth > 2 {
		return nil, ErrDepthOutOfRange
	}
	opts.applyDefaults()

	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	// 1. BM25 seeds.
	seeds, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: query,
		Mode:  "bm25",
		Limit: opts.SeedLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("recall: MultiHopRetrieve: BM25 seeds: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	// 2. Fetch each seed's entities.
	es := NewEntityStore()
	for _, s := range seeds {
		ents, err := st.GetAgentMemoryEntities(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("recall: MultiHopRetrieve: fetch entities for seed %d: %w", s.ID, err)
		}
		if len(ents) > 0 {
			es.AddFromAgentMemory(s.ID, ents)
		}
	}

	// 3. Initialize visited + result with the seeds.
	visited := make(map[int64]struct{}, opts.TotalLimit)
	hits := make([]RecallHit, 0, opts.TotalLimit)
	frontier := make([]int64, 0, opts.SeedLimit)
	for _, s := range seeds {
		if _, ok := visited[s.ID]; ok {
			continue
		}
		visited[s.ID] = struct{}{}
		// Seeds use a normalized 1.0 score — we sort by (depth ASC,
		// score DESC, mem_id ASC) and seeds should always tie at 1.0
		// so the mem_id ASC tiebreaker is deterministic. FTS5's raw
		// BM25 Rank (lower=better, monotonic) is intentionally NOT
		// surfaced — it would dominate the sort and obscure the
		// entity-overlap signal we're trying to measure.
		hits = append(hits, RecallHit{
			MemID: s.ID,
			Depth: 0,
			Score: 1.0,
		})
		frontier = append(frontier, s.ID)
	}
	if depth == 0 {
		return sortedHits(hits), nil
	}

	// 4. BFS by entity overlap. At each hop we ask the Store for rows
// containing any of the frontier's entity values (OR semantics),
// rather than only consulting the in-memory index — the in-memory
// index only covers rows we've already visited, so a Store-first
// expansion is required to discover new rows. The in-memory index
// is still useful: it tracks which entities each frontier row has,
// and provides O(1) via-entity attribution for the new hits.
	for d := 1; d <= depth; d++ {
		if len(frontier) == 0 {
			break
		}
		// Collect entity values across the frontier (in-memory lookup).
		var entityValues []string
		seenEntity := make(map[string]struct{})
		for _, id := range frontier {
			for _, v := range es.EntitiesForRow(id) {
				if _, dup := seenEntity[v]; dup {
					continue
				}
				seenEntity[v] = struct{}{}
				entityValues = append(entityValues, v)
			}
		}
		// Also include the original query's tokens as entity candidates —
		// the BM25 path already used them but new entities on the frontier
		// (depth=2) might bridge to query-relevant rows the seed set missed.
		if d == 1 {
			queryTokens := tokenizeQuery(query)
			for _, tok := range queryTokens {
				if _, dup := seenEntity[tok]; dup {
					continue
				}
				seenEntity[tok] = struct{}{}
				entityValues = append(entityValues, tok)
			}
		}
		sort.Strings(entityValues) // deterministic frontier expansion

		// Ask the Store for rows containing ANY of those entities.
		// This is the FULL-GRAPH expansion — it returns rows we have
		// NOT indexed yet, which is what we want at depth>1.
		candidates, err := st.ListAgentMemoryByAnyEntity(ctx, entityValues)
		if err != nil {
			return nil, fmt.Errorf("recall: MultiHopRetrieve: ListAgentMemoryByAnyEntity (depth=%d): %w", d, err)
		}

		// Filter visited + cap to HopLimit.
		nextFrontier := make([]int64, 0, len(candidates))
		for _, id := range candidates {
			if _, ok := visited[id]; ok {
				continue
			}
			if len(nextFrontier) >= opts.HopLimit {
				break
			}
			if len(hits) >= opts.TotalLimit {
				break
			}
			visited[id] = struct{}{}
			// Fetch the row's entities for via-entity attribution +
			// adding to the in-memory index so depth+1 can use them.
			rowEnts, err := st.GetAgentMemoryEntities(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("recall: MultiHopRetrieve: fetch entities for hit %d: %w", id, err)
			}
			// Find the entity that connected this row to the prior
			// frontier: first (alphabetically) entity value that
			// appears in BOTH the row's entities AND entityValues.
			rowSet := make(map[string]struct{}, len(rowEnts))
			for _, e := range rowEnts {
				rowSet[e.Value] = struct{}{}
			}
			var viaEntity string
			for _, ev := range entityValues {
				if _, ok := rowSet[ev]; ok {
					viaEntity = ev
					break
				}
			}
			if len(rowEnts) > 0 {
				es.AddFromAgentMemory(id, rowEnts)
			}
			hits = append(hits, RecallHit{
				MemID:     id,
				Depth:     d,
				ViaEntity: viaEntity,
				Score:     1.0 / float64(d+1),
			})
			nextFrontier = append(nextFrontier, id)
		}

		frontier = nextFrontier
		if len(hits) >= opts.TotalLimit {
			break
		}
	}

	return sortedHits(hits), nil
}

// sortedHits orders hits by depth ASC, score DESC, mem_id ASC.
// Used at every exit point to keep MultiHopRetrieve deterministic.
func sortedHits(hits []RecallHit) []RecallHit {
	if len(hits) <= 1 {
		return hits
	}
	out := make([]RecallHit, len(hits))
	copy(out, hits)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].MemID < out[j].MemID
	})
	return out
}

// tokenizeQuery extracts simple noun-phrase candidates from a free-
// text query. Used at depth=1 to bridge query terms to entity values
// (the BM25 path already covered exact matches; this catches rows
// that match a query token but were not in the BM25 top-K seed set).
//
// Delegates to internal/entity.Extract so the stopword filter,
// minTokenLen, and dedup rules are identical to the row-side
// extractor — no second tokenize implementation to drift.
func tokenizeQuery(q string) []string {
	ents := entity.Extract(q, "", "")
	if len(ents) == 0 {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Value)
	}
	return out
}