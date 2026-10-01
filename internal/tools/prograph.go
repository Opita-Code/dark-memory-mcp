// Package tools — prograph.go: dark_memory_prograph_query
// (Phase 9 alpha.20 Chunk 8.4, ADR-015).
//
// Wire shape:
//
//	dark_memory_prograph_query
//	  in:  { query: string, depth?: int (0|1|2, default 1),
//	         seed_limit?: int (default 20),
//	         hop_limit?: int (default 50),
//	         total_limit?: int (default 100) }
//	  out: { hits: [{mem_id, depth, via_entity?, score}, ...],
//	      hit_count: int, query: string, depth: int }
//
// Operators call dark_memory_prograph_query when a plain BM25 search
// misses (or returns too few) the rows that share entities with the
// top-ranked seeds. C1/C2/C5/C6 stay 1-hop (single-frame retrieval
// uses depth=1); C3/C4 promote to 2-hop (depth=2). depth=0 returns
// just the BM25 seeds (useful for debugging what BM25 saw).
//
// The tool is read-only but emits its own write_audit row
// (write_path=prograph_query) so even the act of running the BFS is
// auditable per INV-1. No LLM call is made by the tool — the
// judgment is left to the harness / operator.
package tools

import (
	"context"
	"errors"

	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterPrograph wires dark_memory_prograph_query into the registry.
// Called from RegisterAllWithDeps (the canonical surface registration
// path). The tool is registered unconditionally (canonical surface
// requirement per audit tools pattern, Chunk 8.5) — calls return
// errors when the Store has no active project, which is the same
// contract as dark_memory_recall.
func RegisterPrograph(reg *Registry, st store.Store) error {
	if reg == nil {
		return errors.New("tools: RegisterPrograph: nil registry")
	}
	if st == nil {
		return errors.New("tools: RegisterPrograph: nil store")
	}

	reg.Add(BindStore("prograph_query",
		"Run a ProGraph 2-layer entity-graph BFS over agent_memory (ADR-015). Returns rows that share at least one entity with the BM25 seeds, up to `depth` hops (0=seeds only, 1=direct neighbors, 2=neighbors-of-neighbors). C1/C2/C5/C6 use depth=1; C3/C4 use depth=2. The BFS expands via the entity graph (case-insensitive noun phrases extracted from content + title + tags at Save time when ExtractEntities=true). Read-only; emits a write_prograph_query audit row.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Free-text query. BM25 seeds come from SearchAgentMemory(query). Empty/whitespace returns (nil, nil) — no error."},
				"depth": map[string]any{"type": "integer", "description": "Hop count: 0 (seeds only), 1 (default — direct neighbors), 2 (neighbors-of-neighbors). 3+ returns ErrDepthOutOfRange."},
				"seed_limit":  map[string]any{"type": "integer", "description": "Max BM25 seeds to start the BFS from. 0 → default 20."},
				"hop_limit":   map[string]any{"type": "integer", "description": "Max rows added per hop. 0 → default 50."},
				"total_limit": map[string]any{"type": "integer", "description": "Cap on the total result size. 0 → default 100."},
			},
			"required": []string{"query"},
		}),
		st,
		func(ctx context.Context, s store.Store, in PrographQueryInput) (*PrographQueryResult, error) {
			depth := in.Depth
			if depth == 0 {
				// depth default = 1 (ProGraph is most useful at 1-hop;
				// depth=0 is just the BM25 seeds).
				depth = 1
			}
			opts := recall.MultiHopOptions{
				SeedLimit:  in.SeedLimit,
				HopLimit:   in.HopLimit,
				TotalLimit: in.TotalLimit,
			}
			hits, err := recall.MultiHopRetrieve(ctx, s, in.Query, depth, opts)
			if err != nil {
				return nil, err
			}
			if hits == nil {
				hits = []recall.RecallHit{} // wire shape: empty array, not nil
			}
			return &PrographQueryResult{
				Hits:     hits,
				HitCount: len(hits),
				Query:    in.Query,
				Depth:    depth,
			}, nil
		}))

	return nil
}

// PrographQueryInput is the input for dark_memory_prograph_query.
type PrographQueryInput struct {
	Query      string `json:"query"`
	Depth      int    `json:"depth,omitempty"` // 0|1|2; default 1
	SeedLimit  int    `json:"seed_limit,omitempty"`
	HopLimit   int    `json:"hop_limit,omitempty"`
	TotalLimit int    `json:"total_limit,omitempty"`
}

// PrographQueryResult is the output for dark_memory_prograph_query.
// Hits is non-nil (empty slice) when the result is empty so wire
// decoders see [] rather than null.
type PrographQueryResult struct {
	Hits     []recall.RecallHit `json:"hits"`
	HitCount int                `json:"hit_count"`
	Query    string             `json:"query"`
	Depth    int                `json:"depth"`
}