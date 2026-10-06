package recall

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/eventholder"
)

// C2TextRecall is the alpha.18 text-context retrieval strategy.
//
// SPEC-alpha-11-phase5.md §6.2:
//
//   - FTS5 + BGE-large text vector + 1-hop same-project graph.
//   - Weights: 0.40 FTS5, 0.50 vector, 0.10 graph.
//   - Decay: medium (180d).
//
// alpha.18 implementation notes:
//
//   - The "vector" signal is currently ABSORBED into FTS5 with
//     synonym expansion. The embedder adapter interface lands in
//     alpha.19 (per SPEC §2.2 non-goals). Until then, the strategy
//     uses FTS5 with operator-curated synonym expansion as a proxy
//     for semantic match — the operator gets correct behaviour
//     without paying the embedder cost.
//
//   - Phase 13 T-202: when an Embedder is configured (non-nil + non-
//     disabled), this strategy gets real cosine-similarity scoring
//     and the 0.50 vector weight starts contributing for rows that
//     have a stored embedding. Rows without a stored embedding
//     silently fall back to ftsScore (the alpha.18 stub behavior).
//
//   - The graph signal uses same-project neighbours (operators
//     working on the same project typically benefit from cross-
//     reference between their notes).
type C2TextRecall struct {
	// Synonyms is an operator-curated expansion map (term → OR'd
	// variants). Empty default: minimal expansion. alpha.19 will
	// add LLM-extracted per-domain synonyms.
	Synonyms map[string][]string
	// Embedder is the optional vector-axis plug (Phase 13 T-202).
	// When nil OR KindNone OR disabled, vector scores collapse to
	// ftsScore (legacy behavior). When non-nil, the strategy calls
	// Embed(ctx, [query]) once per Recall and computes cosine to
	// each candidate's stored embedding via decodeEmbeddingBlob.
	//
	// Best practice: callers wire embedder.FactoryAuto() at boot
	// and reuse the same instance across all RecallFor calls. The
	// factory's Sync wrapper makes concurrent use safe.
	Embedder embedder.Embedder
}

// VibeCase returns "text" (C2).
func (c *C2TextRecall) VibeCase() string { return VibeCaseText }

// Weights returns the canonical C2 blend.
func (c *C2TextRecall) Weights() Weights {
	return Weights{FTS5: 0.40, Vector: 0.50, Graph: 0.10}
}

// Recall runs the C2 pipeline:
//
//  1. Expand query with operator-curated synonyms.
//  2. FTS5 over kind IN ('observation', 'note', 'context', 'finding').
//  3. 1-hop graph expansion (same project).
//  4. Score = 0.40 × FTS5 rank + 0.50 × FTS5(broadened) + 0.10 × graph.
//
// Note on alpha.18 scoring: since "vector" is FTS5-expanded, the
// combined FTS5 weight is 0.40+0.50=0.90. The graph still gets 0.10.
// This is the closest we can get to the SPEC weights without an
// embedder, and it's what alpha.18 ships.
func (c *C2TextRecall) Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("C2TextRecall: db is nil")
	}
	if query == "" {
		return nil, nil
	}
	if projectID == "" {
		projectID = "default"
	}
	if topK <= 0 {
		topK = 10
	}

	expanded := expandTextQuery(query, c.Synonyms)

	seeds, err := c.fts5Recall(ctx, db, expanded, projectID, topK*3)
	if err != nil {
		return nil, fmt.Errorf("C2TextRecall fts5: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	graphScores, err := c.graphExpand(ctx, db, seeds, projectID, 1)
	if err != nil {
		return nil, fmt.Errorf("C2TextRecall graph: %w", err)
	}
	extra, err := c.hydrateGraphRows(ctx, db, graphScores, seeds, projectID)
	if err != nil {
		return nil, fmt.Errorf("C2TextRecall hydrate: %w", err)
	}
	candidates := append(seeds, extra...)
	// Phase 13 T-202: optional embedder-driven cosine scoring. When
	// c.Embedder is nil/KindNone/disabled the helper returns nil and
	// scoreFTSPlusGraph falls back to ftsScore for every row.
	vectorScores, vecErr := c.computeVectorScores(ctx, candidates, expanded)
	if vecErr != nil {
		// Best-effort: log + degrade to ftsScore. The recall pipeline
		// must not fail because the embedder provider is misbehaving.
		vectorScores = nil
	}
	ranked := scoreFTSPlusGraph(candidates, seeds, graphScores, c.Weights(), vectorScores)
	return topKRows(ranked, topK), nil
}

func (c *C2TextRecall) fts5Recall(ctx context.Context, db *sql.DB, query, projectID string, limit int) ([]AnnotatedRow, error) {
	q := `SELECT ` + annotatedColumns + `
		FROM agent_memory_fts
		JOIN agent_memory m ON m.id = agent_memory_fts.rowid
		WHERE agent_memory_fts MATCH ?
		  AND m.project_id = ?
		  AND m.kind IN ('observation', 'note', 'context', 'finding')
		ORDER BY rank
		LIMIT ?`
	rows, err := db.QueryContext(ctx, q, query, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnotatedRows(rows)
}

func (c *C2TextRecall) graphExpand(ctx context.Context, db *sql.DB, seeds []AnnotatedRow, projectID string, hops int) (map[int64]int, error) {
	// 1=1 because we accept any decision_state for the graph.
	return graphExpandShared(ctx, db, seeds, projectID, "1=1", hops)
}

func (c *C2TextRecall) hydrateGraphRows(ctx context.Context, db *sql.DB, graphScores map[int64]int, seeds []AnnotatedRow, projectID string) ([]AnnotatedRow, error) {
	return hydrateGraphRowsShared(ctx, db, graphScores, seeds, projectID, "1=1", annotatedColumns)
}

// expandTextQuery applies operator-curated synonym expansion.
// The expansion is OR'd into the FTS5 MATCH expression.
func expandTextQuery(query string, synonyms map[string][]string) string {
	cleaned := strings.TrimSpace(query)
	if cleaned == "" {
		return cleaned
	}
	out := []string{cleaned}
	lower := strings.ToLower(cleaned)
	for term, vars := range synonyms {
		if !strings.Contains(lower, strings.ToLower(term)) {
			continue
		}
		for _, v := range vars {
			out = append(out, v)
		}
	}
	return strings.Join(out, " OR ")
}

// computeVectorScores computes the per-row cosine similarity
// between the query and each candidate's stored embedding.
//
// Returns (nil, nil) when the embedder is missing or disabled —
// callers pass `nil` to scoreFTSPlusGraph, which falls back to
// ftsScore for every row (alpha.18 stub behavior, preserved for
// backward compat with no-embedder operators).
//
// Errors from the embedder (ErrKeyMissing, network timeout, etc.)
// surface here so callers can degrade gracefully without failing
// the recall. Rows whose BLOB cannot be decoded (malformed or
// dim-mismatched) are silently skipped — their vector signal will
// be the ftsScore fallback in scoreFTSPlusGraph.
//
// Phase 13 T-202 wire: when the embedder returns at least one
// query vector AND at least one candidate has a stored embedding,
// the helper emits an EmitEmbedderRefresh event via eventholder
// (8/8 of Phase 12 T-103a helpers now wired).
func (c *C2TextRecall) computeVectorScores(ctx context.Context, candidates []AnnotatedRow, query string) (map[int64]float64, error) {
	if c.Embedder == nil {
		return nil, nil
	}
	if c.Embedder.Kind() == embedder.KindNone {
		return nil, nil
	}
	if len(candidates) == 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	qVecs, err := c.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embedder.Embed: %w", err)
	}
	if len(qVecs) == 0 || qVecs[0] == nil {
		return nil, nil
	}
	qVec := qVecs[0]
	qDim := qVec.Dim()

	out := make(map[int64]float64, len(candidates))
	scored := 0
	for _, cand := range candidates {
		if len(cand.Embedding) == 0 {
			continue
		}
		// Prefer the row's declared EmbeddingDim when present
		// (zero = legacy / unset, decode tolerates any multiple of 4).
		expectedDim := cand.EmbeddingDim
		if expectedDim == 0 {
			expectedDim = qDim
		}
		cVec, derr := decodeEmbeddingBlob(cand.Embedding, expectedDim)
		if derr != nil {
			// Drop the row, don't pollute the map.
			continue
		}
		sim := cosineSimilarity(qVec, cVec)
		out[cand.ID] = sim
		scored++
	}

	// Phase 13 T-202: 8/8 AutoEmitter helpers wired. EmitEmbedderRefresh
	// fires when the recall consumed embeddings (audit trail for the
	// embedder consumption path; cosmetic per Q2 INDIAN, async).
	if scored > 0 {
		if ae := eventholder.Get(); ae != nil {
			ae.EmitEmbedderRefresh(ctx, 0, scored) // rowID=0 (process-wide aggregate)
		}
	}
	return out, nil
}
