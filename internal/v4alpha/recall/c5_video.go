package recall

import (
	"context"
	"database/sql"
	"fmt"
)

// C5VideoRecall is the alpha.18 video-context retrieval strategy.
//
// SPEC-alpha-11-phase5.md §6.5:
//
//   - ImageBind image encoder (cross-modal 0.80).
//   - FTS5 + style ref graph.
//   - Weights: 0.00 FTS5, 0.00 vector, 0.20 graph, 0.80 cross-modal.
//   - Decay: high (90d).
//   - Lazy indexing for legacy rows (alpha.18); save-time pre-embed
//     for new rows.
//
// alpha.18 implementation notes:
//
//   - The "cross-modal" signal is alpha.19 (ImageBind integration).
//     alpha.18 uses FTS5 over kind=link rows (which is how video
//     references surface today) as a proxy.
//
//   - Style ref graph = the kind=link rows that point to the same
//     adr_refs (a video's style decision shares an ADR with related
//     videos). 1-hop expansion via adr_refs.
//
//   - Voice embed kind is irrelevant for video; voice_embed_kind
//     column is C6-only (R-B I-2).
type C5VideoRecall struct{}

// VibeCase returns "video" (C5).
func (c *C5VideoRecall) VibeCase() string { return VibeCaseVideo }

// Weights returns the canonical C5 blend.
func (c *C5VideoRecall) Weights() Weights {
	return Weights{Graph: 0.20, CrossModal: 0.80}
}

// Recall runs the C5 pipeline:
//
//  1. FTS5 over kind=link rows (alpha.18 stub for cross-modal).
//  2. 1-hop graph expansion via adr_refs.
//  3. Score = 0.20 × graph + 0.80 × FTS5 (alpha.18 vector stub).
//  4. topK.
func (c *C5VideoRecall) Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("C5VideoRecall: db is nil")
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

	seeds, err := c.fts5Recall(ctx, db, query, projectID, topK*3)
	if err != nil {
		return nil, fmt.Errorf("C5VideoRecall fts5: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	graphScores, err := c.graphExpand(ctx, db, seeds, projectID, 1)
	if err != nil {
		return nil, fmt.Errorf("C5VideoRecall graph: %w", err)
	}
	extra, err := c.hydrateGraphRows(ctx, db, graphScores, seeds, projectID)
	if err != nil {
		return nil, fmt.Errorf("C5VideoRecall hydrate: %w", err)
	}
	candidates := append(seeds, extra...)
	// C5 has no vector weight (Graph + CrossModal only). Phase 13
	// T-202: pass nil vectorScores; scoreFTSPlusGraph falls back
	// to ftsScore, which is unused for C5's Weights blend.
	ranked := scoreFTSPlusGraph(candidates, seeds, graphScores, c.Weights(), nil)
	return topKRows(ranked, topK), nil
}

func (c *C5VideoRecall) fts5Recall(ctx context.Context, db *sql.DB, query, projectID string, limit int) ([]AnnotatedRow, error) {
	q := `SELECT ` + annotatedColumns + `
		FROM agent_memory_fts
		JOIN agent_memory m ON m.id = agent_memory_fts.rowid
		WHERE agent_memory_fts MATCH ?
		  AND m.project_id = ?
		  AND m.kind = 'link'
		ORDER BY rank
		LIMIT ?`
	rows, err := db.QueryContext(ctx, q, query, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnotatedRows(rows)
}

func (c *C5VideoRecall) graphExpand(ctx context.Context, db *sql.DB, seeds []AnnotatedRow, projectID string, hops int) (map[int64]int, error) {
	return graphExpandShared(ctx, db, seeds, projectID, "1=1", hops)
}

func (c *C5VideoRecall) hydrateGraphRows(ctx context.Context, db *sql.DB, graphScores map[int64]int, seeds []AnnotatedRow, projectID string) ([]AnnotatedRow, error) {
	return hydrateGraphRowsShared(ctx, db, graphScores, seeds, projectID, "1=1", annotatedColumns)
}
