package recall

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// C1CodeRecall is the alpha.18 code-context retrieval strategy.
//
// SPEC-alpha-11-phase5.md §6.1:
//
//   - FTS5 with custom code-aware tokenizer (CamelCase, snake_case,
//     kebab-case, dot-split).
//   - Graph: 1-hop over ADR/INV refs + call graph (when available).
//   - Weights: 0.55 FTS5, 0.45 graph.
//   - Decay: medium (90d for snippets, ∞ for ADR rows; the strategy
//     does not enforce this — decay_class on the row does).
//   - No embedder (R-E: FTS5 + ADR/INV refs > any code embedder).
//
// alpha.18 implementation notes:
//
//   - "Call graph" expansion via agent_memory_links is alpha.19
//     (links are operator-flag, default OFF in alpha.18). For now
//     the strategy uses adr_refs / inv_refs as the graph axis.
//
//   - The custom tokenizer is implemented as a query-side pre-process:
//     we split the user's query on code delimiters and OR all tokens
//     into the FTS5 MATCH expression. SQLite's default FTS5 tokenizer
//     (unicode61) splits on whitespace + punctuation which is close
//     enough for our purposes.
//
//   - Weights are the canonical SPEC values. Tests assert weight
//     sum = 1.0 but do not assert exact ranking (alpha.18 has no
//     calibrated ground truth set yet).
type C1CodeRecall struct {
	// KindWhitelist limits the FTS5 candidate set to kinds that
	// typically appear in code-context retrieval. Default:
	// decision, finding, context. Empty means "all kinds".
	KindWhitelist []string
}

// VibeCase returns "code" (C1).
func (c *C1CodeRecall) VibeCase() string { return VibeCaseCode }

// Weights returns the canonical C1 blend.
func (c *C1CodeRecall) Weights() Weights {
	return Weights{FTS5: 0.55, Graph: 0.45}
}

// Recall runs the C1 pipeline:
//
//  1. Tokenise the query for FTS5 (code-aware pre-process).
//  2. FTS5 over candidate kinds (kind IN (...)).
//  3. 1-hop graph expansion via adr_refs / inv_refs.
//  4. Score = 0.55 × FTS5 rank + 0.45 × graph adjacency score.
//  5. topK.
func (c *C1CodeRecall) Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("C1CodeRecall: db is nil")
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

	kinds := c.KindWhitelist
	if len(kinds) == 0 {
		kinds = []string{"decision", "finding", "context"}
	}

	tokens := tokenizeCodeQuery(query)
	ftsQuery := strings.Join(tokens, " OR ")

	seeds, err := c.fts5Recall(ctx, db, ftsQuery, projectID, kinds, topK*3)
	if err != nil {
		return nil, fmt.Errorf("C1CodeRecall fts5: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	graphScores, err := c.graphExpand(ctx, db, seeds, projectID, 1)
	if err != nil {
		return nil, fmt.Errorf("C1CodeRecall graph: %w", err)
	}
	extra, err := c.hydrateGraphRows(ctx, db, graphScores, seeds, projectID)
	if err != nil {
		return nil, fmt.Errorf("C1CodeRecall hydrate: %w", err)
	}
	candidates := append(seeds, extra...)
	// Phase 13 T-202: C1 has no vector weight (FTS5 + Graph only).
	// Pass nil vectorScores; scoreFTSPlusGraph falls back to ftsScore
	// for every row, which is the alpha.18 behavior preserved.
	ranked := scoreFTSPlusGraph(candidates, seeds, graphScores, c.Weights(), nil)
	return topKRows(ranked, topK), nil
}

func (c *C1CodeRecall) fts5Recall(ctx context.Context, db *sql.DB, query, projectID string, kinds []string, limit int) ([]AnnotatedRow, error) {
	placeholders := make([]string, len(kinds))
	args := []interface{}{query, projectID}
	for i, k := range kinds {
		placeholders[i] = "?"
		args = append(args, k)
	}
	args = append(args, limit)
	q := `SELECT ` + annotatedColumns + `
		FROM agent_memory_fts
		JOIN agent_memory m ON m.id = agent_memory_fts.rowid
		WHERE agent_memory_fts MATCH ?
		  AND m.project_id = ?
		  AND m.kind IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY rank
		LIMIT ?`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnotatedRows(rows)
}

// graphExpand is shared with C3 but bounded to 1 hop (C1 is
// performance-sensitive; deep traversal is alpha.19).
//
// NOTE: For C1, the graph axis is adr_refs + inv_refs combined.
// The C3 strategy uses adr_refs only (decision-kind chains).
// Sharing the helper keeps the expansion logic in one place.
func (c *C1CodeRecall) graphExpand(ctx context.Context, db *sql.DB, seeds []AnnotatedRow, projectID string, hops int) (map[int64]int, error) {
	return graphExpandShared(ctx, db, seeds, projectID, "1=1", hops)
}

func (c *C1CodeRecall) hydrateGraphRows(ctx context.Context, db *sql.DB, graphScores map[int64]int, seeds []AnnotatedRow, projectID string) ([]AnnotatedRow, error) {
	return hydrateGraphRowsShared(ctx, db, graphScores, seeds, projectID, "1=1", annotatedColumns)
}
