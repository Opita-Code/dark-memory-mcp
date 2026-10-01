package recall

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
//   - When alpha.19 ships, this strategy gets a real BGE-large
//     adapter and the 0.50 vector weight starts contributing real
//     cosine-similarity scores. The Weights literal stays the same.
//
//   - The graph signal uses same-project neighbours (operators
//     working on the same project typically benefit from cross-
//     reference between their notes).
type C2TextRecall struct {
	// Synonyms is an operator-curated expansion map (term → OR'd
	// variants). Empty default: minimal expansion. alpha.19 will
	// add LLM-extracted per-domain synonyms.
	Synonyms map[string][]string
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
	ranked := scoreFTSPlusGraph(candidates, seeds, graphScores, c.Weights())
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
