package recall

import (
	"context"
	"database/sql"
	"fmt"
)

// C6AudioRecall is the alpha.18 audio-context retrieval strategy.
//
// SPEC-alpha-11-phase5.md §6.6:
//
//   - ImageBind audio encoder (cross-modal 0.80).
//   - FTS5 + voice profile graph (timbre embedding).
//   - voice_embed_kind column (timbre | full | prosody+timbre).
//   - Weights: 0.00 FTS5, 0.00 vector, 0.20 graph, 0.80 cross-modal.
//   - Decay: medium (365d).
//
// alpha.18 implementation notes:
//
//   - Same cross-modal stub as C5: alpha.18 uses FTS5 over kind=link
//     + 1-hop graph. ImageBind integration is alpha.19.
//
//   - voice_embed_kind is captured per-row but the C6 strategy
//     does not (yet) differentiate between timbre vs full vs
//     prosody+timbre. Alpha.19 will surface a different ranking
//     for each kind.
type C6AudioRecall struct {
	// VoiceEmbedKind filter — only return rows whose voice_embed_kind
	// matches one of these. Empty = no filter.
	VoiceEmbedKind []string
}

// VibeCase returns "audio" (C6).
func (c *C6AudioRecall) VibeCase() string { return VibeCaseAudio }

// Weights returns the canonical C6 blend.
func (c *C6AudioRecall) Weights() Weights {
	return Weights{Graph: 0.20, CrossModal: 0.80}
}

// Recall runs the C6 pipeline:
//
//  1. FTS5 over kind=link rows (alpha.18 stub for cross-modal).
//  2. Optional voice_embed_kind filter.
//  3. 1-hop graph expansion.
//  4. Score = 0.20 × graph + 0.80 × FTS5 (alpha.18 stub).
//  5. topK.
func (c *C6AudioRecall) Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("C6AudioRecall: db is nil")
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
		return nil, fmt.Errorf("C6AudioRecall fts5: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	// Apply voice_embed_kind filter post-FTS5 (alpha.18: in-memory).
	if len(c.VoiceEmbedKind) > 0 {
		filter := make(map[string]struct{}, len(c.VoiceEmbedKind))
		for _, k := range c.VoiceEmbedKind {
			filter[k] = struct{}{}
		}
		filtered := seeds[:0]
		for _, s := range seeds {
			if _, ok := filter[s.VoiceEmbedKind]; ok {
				filtered = append(filtered, s)
			}
		}
		seeds = filtered
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	graphScores, err := c.graphExpand(ctx, db, seeds, projectID, 1)
	if err != nil {
		return nil, fmt.Errorf("C6AudioRecall graph: %w", err)
	}
	extra, err := c.hydrateGraphRows(ctx, db, graphScores, seeds, projectID)
	if err != nil {
		return nil, fmt.Errorf("C6AudioRecall hydrate: %w", err)
	}
	candidates := append(seeds, extra...)
	// C6 has no vector weight (Graph + CrossModal only). Phase 13
	// T-202: nil vectorScores; falls back to ftsScore (unused).
	ranked := scoreFTSPlusGraph(candidates, seeds, graphScores, c.Weights(), nil)
	return topKRows(ranked, topK), nil
}

func (c *C6AudioRecall) fts5Recall(ctx context.Context, db *sql.DB, query, projectID string, limit int) ([]AnnotatedRow, error) {
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

func (c *C6AudioRecall) graphExpand(ctx context.Context, db *sql.DB, seeds []AnnotatedRow, projectID string, hops int) (map[int64]int, error) {
	return graphExpandShared(ctx, db, seeds, projectID, "1=1", hops)
}

func (c *C6AudioRecall) hydrateGraphRows(ctx context.Context, db *sql.DB, graphScores map[int64]int, seeds []AnnotatedRow, projectID string) ([]AnnotatedRow, error) {
	return hydrateGraphRowsShared(ctx, db, graphScores, seeds, projectID, "1=1", annotatedColumns)
}
