package recall

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// C4ResearchRecall is the alpha.18 research-context retrieval strategy.
//
// SPEC-alpha-11-phase5.md §6.4:
//
//   - FTS5 + BGE-large + 2-hop citation graph.
//   - Weights: 0.25 FTS5, 0.45 vector, 0.30 graph.
//   - Decay: domain-aware (1095d AI/ML, 3650d math).
//
// alpha.18 implementation notes:
//
//   - Same vector stub as C2: BGE-large integration is alpha.19.
//     alpha.18 uses FTS5 with research-aware query expansion.
//
//   - 2-hop graph expansion via adr_refs is shared with the C3
//     decision strategy. C4 uses it for citation chains:
//
//     paper → cites ADR → ADR row → cites ADR-2 → ADR-2 row
//
//   - Domain-aware decay is deferred to alpha.19 (per the
//     per-vibe-case multiplier table in SPEC §9). alpha.18 applies
//     the kind-based default (per R-C §4 I-1).
type C4ResearchRecall struct{}

// VibeCase returns "research" (C4).
func (c *C4ResearchRecall) VibeCase() string { return VibeCaseResearch }

// Weights returns the canonical C4 blend.
func (c *C4ResearchRecall) Weights() Weights {
	return Weights{FTS5: 0.25, Vector: 0.45, Graph: 0.30}
}

// Recall runs the C4 pipeline:
//
//  1. Expand query with research-aware terms (paper, cite, source,
//     reference).
//  2. FTS5 over kind IN ('finding', 'context', 'observation', 'link').
//  3. 2-hop citation graph via adr_refs.
//  4. Score = weighted RRF.
//  5. topK.
func (c *C4ResearchRecall) Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("C4ResearchRecall: db is nil")
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

	expanded := expandResearchQuery(query)

	seeds, err := c.fts5Recall(ctx, db, expanded, projectID, topK*3)
	if err != nil {
		return nil, fmt.Errorf("C4ResearchRecall fts5: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	graphScores, err := c.graphExpand(ctx, db, seeds, projectID, 2)
	if err != nil {
		return nil, fmt.Errorf("C4ResearchRecall graph: %w", err)
	}
	extra, err := c.hydrateGraphRows(ctx, db, graphScores, seeds, projectID)
	if err != nil {
		return nil, fmt.Errorf("C4ResearchRecall hydrate: %w", err)
	}
	candidates := append(seeds, extra...)
	ranked := scoreFTSPlusGraph(candidates, seeds, graphScores, c.Weights())
	return topKRows(ranked, topK), nil
}

func (c *C4ResearchRecall) fts5Recall(ctx context.Context, db *sql.DB, query, projectID string, limit int) ([]AnnotatedRow, error) {
	q := `SELECT ` + annotatedColumns + `
		FROM agent_memory_fts
		JOIN agent_memory m ON m.id = agent_memory_fts.rowid
		WHERE agent_memory_fts MATCH ?
		  AND m.project_id = ?
		  AND m.kind IN ('finding', 'context', 'observation', 'link')
		ORDER BY rank
		LIMIT ?`
	rows, err := db.QueryContext(ctx, q, query, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnotatedRows(rows)
}

func (c *C4ResearchRecall) graphExpand(ctx context.Context, db *sql.DB, seeds []AnnotatedRow, projectID string, hops int) (map[int64]int, error) {
	return graphExpandShared(ctx, db, seeds, projectID, "1=1", hops)
}

func (c *C4ResearchRecall) hydrateGraphRows(ctx context.Context, db *sql.DB, graphScores map[int64]int, seeds []AnnotatedRow, projectID string) ([]AnnotatedRow, error) {
	return hydrateGraphRowsShared(ctx, db, graphScores, seeds, projectID, "1=1", annotatedColumns)
}

// expandResearchQuery adds research-aware synonyms to the FTS5 query.
// alpha.18 ships a minimal expansion; alpha.19 may swap in a domain-
// specific lexicon (per operator config).
func expandResearchQuery(query string) string {
	cleaned := strings.TrimSpace(query)
	if cleaned == "" {
		return cleaned
	}
	// Common research tokens. FTS5 OR semantics.
	return "(" + cleaned + ") OR paper OR cite OR cites OR cited OR reference OR references OR source OR sources OR arxiv OR doi"
}

// tokenizeCodeQuery splits a query on code delimiters (camelCase,
// snake_case, kebab-case, dots). Each token becomes its own FTS5
// OR'd term so e.g. "DarkMemoryV4" matches rows containing "Dark",
// "Memory", or "V4".
func tokenizeCodeQuery(query string) []string {
	if query == "" {
		return nil
	}
	// Replace common code delimiters with spaces.
	q := query
	for _, sep := range []string{".", "_", "-", "/", ":"} {
		q = strings.ReplaceAll(q, sep, " ")
	}
	// Split camelCase: "DarkMemoryV4" → "Dark Memory V4".
	var sb strings.Builder
	for i, r := range q {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(q[i-1])
			if prev >= 'a' && prev <= 'z' {
				sb.WriteByte(' ')
			}
		}
		sb.WriteRune(r)
	}
	parts := strings.Fields(sb.String())
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// graphExpandShared is the canonical 1- or 2-hop expansion helper.
// It factors out the adr_refs LIKE-walk logic from C1, C2, C3, C4.
// Each strategy calls it with its own state filter ("1=1" for C1/C2/C4,
// "decision_state='active'" for C3).
//
// maxHop=1 returns only direct neighbors; maxHop=2 walks one extra
// layer for C3/C4 chains.
func graphExpandShared(ctx context.Context, db *sql.DB, seeds []AnnotatedRow, projectID, stateFilter string, maxHop int) (map[int64]int, error) {
	scores := make(map[int64]int)
	visited := make(map[int64]bool)
	for _, s := range seeds {
		visited[s.ID] = true
	}

	refToSeedCount := make(map[string]int)
	for _, s := range seeds {
		for _, ref := range splitRefs(s.AdrRefs) {
			refToSeedCount[ref]++
		}
		for _, ref := range splitRefs(s.InvRefs) {
			refToSeedCount[ref]++
		}
	}
	if len(refToSeedCount) == 0 {
		return scores, nil
	}

	frontierRefs := refKeysSorted(refToSeedCount)
	for hop := 1; hop <= maxHop; hop++ {
		if len(frontierRefs) == 0 {
			break
		}
		args := []interface{}{projectID}
		refPatterns := make([]string, len(frontierRefs))
		for i, r := range frontierRefs {
			refPatterns[i] = "%," + escapeLike(r) + ",%"
			args = append(args, refPatterns[i])
		}
		bareState := strings.ReplaceAll(stateFilter, "m.", "")
		conds := make([]string, len(refPatterns))
		for i := range refPatterns {
			conds[i] = "(',' || adr_refs || ',') LIKE ? ESCAPE '\\'"
			_ = i
		}
		q := `SELECT id, COALESCE(adr_refs, ''), COALESCE(inv_refs, '')
			FROM agent_memory
			WHERE project_id = ?
			  AND ` + bareState + `
			  AND (` + strings.Join(conds, " OR ") + `)`
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("graphExpand hop=%d: %w", hop, err)
		}
		nextRefs := make(map[string]int)
		for rows.Next() {
			var id int64
			var adrRefs, invRefs string
			if err := rows.Scan(&id, &adrRefs, &invRefs); err != nil {
				rows.Close()
				return nil, err
			}
			if visited[id] {
				continue
			}
			visited[id] = true
			matchCount := 0
			for _, ref := range splitRefs(adrRefs) {
				if _, ok := refToSeedCount[ref]; ok {
					matchCount++
				}
				nextRefs[ref]++
			}
			for _, ref := range splitRefs(invRefs) {
				if _, ok := refToSeedCount[ref]; ok {
					matchCount++
				}
				nextRefs[ref]++
			}
			if matchCount > 0 {
				scores[id] = matchCount
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		frontierRefs = refKeysSorted(nextRefs)
		for r, c := range nextRefs {
			refToSeedCount[r] += c
		}
	}
	return scores, nil
}

// hydrateGraphRowsShared is the canonical hydration helper. It loads
// full AnnotatedRow data for each id in graphScores that's NOT in
// seeds, scoped to projectID + stateFilter.
//
// colList is the SELECT list (typically annotatedColumns) so callers
// can reuse the constant without re-stating it.
func hydrateGraphRowsShared(ctx context.Context, db *sql.DB, graphScores map[int64]int, seeds []AnnotatedRow, projectID, stateFilter, colList string) ([]AnnotatedRow, error) {
	if len(graphScores) == 0 {
		return nil, nil
	}
	seedIDs := make(map[int64]bool, len(seeds))
	for _, s := range seeds {
		seedIDs[s.ID] = true
	}
	missing := make([]interface{}, 0, len(graphScores))
	for id := range graphScores {
		if !seedIDs[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	bareState := strings.ReplaceAll(stateFilter, "m.", "")
	q := `SELECT ` + colList + `
		FROM agent_memory m
		WHERE m.project_id = ?
		  AND ` + bareState + `
		  AND m.id IN (` + placeholders(len(missing)) + `)`
	args := append([]interface{}{projectID}, missing...)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnotatedRows(rows)
}

// scoreFTSPlusGraph is the canonical score-blend for the FTS+Graph
// strategies (C1, C2, C3, C4). Score = Σ (weight_i × signal_i).
//
// alpha.18 caveat: "vector" weight is treated identically to FTS5
// weight (the alpha.19 embedder integration will replace this slot).
func scoreFTSPlusGraph(candidates, seeds []AnnotatedRow, graphScores map[int64]int, w Weights) []rankedRow {
	seedRank := make(map[int64]int, len(seeds))
	for i, s := range seeds {
		seedRank[s.ID] = i + 1
	}
	maxGraphScore := 0
	for _, s := range graphScores {
		if s > maxGraphScore {
			maxGraphScore = s
		}
	}
	if maxGraphScore == 0 {
		maxGraphScore = 1
	}

	out := make([]rankedRow, 0, len(candidates))
	for _, c := range candidates {
		var ftsScore, graphScore, vectorScore float64
		if rank, ok := seedRank[c.ID]; ok {
			ftsScore = 1.0 / (60.0 + float64(rank))
		}
		if g, ok := graphScores[c.ID]; ok {
			graphScore = float64(g) / float64(maxGraphScore)
			if graphScore > 1.0 {
				graphScore = 1.0
			}
		}
		// alpha.18 stub: vector signal = FTS5 signal. The alpha.19
		// embedder integration replaces this with cosine similarity.
		vectorScore = ftsScore

		// Apply decay + access boost (per ScrubJay-MEM).
		decay := 1.0
		if c.DecayClass == DecayClassForever {
			decay = 1.0
		} else if c.DecayTauDays > 0 {
			tau := float64(c.DecayTauDays)
			age := timeSince(c.CreatedAt) / 24.0
			decay = mathExp(-age / tau)
		}
		accessBoost := 1.0
		if c.RefreshOnAccess && c.AccessCount > 0 {
			accessBoost = 1.0 + 0.3*log10Fn(float64(c.AccessCount+1))
			if accessBoost > 2.0 {
				accessBoost = 2.0
			}
		}
		score := (w.FTS5*ftsScore + w.Vector*vectorScore + w.Graph*graphScore) * decay * accessBoost
		out = append(out, rankedRow{row: c, score: score})
	}
	return out
}
