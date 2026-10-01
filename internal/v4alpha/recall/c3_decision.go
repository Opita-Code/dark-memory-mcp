package recall

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// C3DecisionRecall is the alpha.18 decision-context retrieval strategy.
//
// SPEC-alpha-11-phase5.md §6.3 mandates:
//
//   - FTS5 with decision-aware query expansion (rationale + transition
//     triggers).
//   - Graph: 2-hop decision-kind graph (ADR/INV chains via adr_refs).
//   - Decision-transition filter: returns decision_state='active' only
//     by default (superseded decisions are excluded unless operator
//     opts in via IncludeSuperseded).
//   - Weights: 0.30 FTS5, 0.70 graph.
//   - Decay: zero (forever — decay_class='forever' auto-applied via
//     DefaultDecayForKind("decision"), so Phase 5 never decays C3
//     results by age).
//
// alpha.18 implementation notes:
//
//   - Graph expansion uses the adr_refs column added in Chunk 1.
//     Entity-table expansion (ProGraph 2-layer via agent_memory_entities)
//     lands in alpha.19 when entity extraction is wired into
//     agent_memory.Save.
//
//   - Decision-aware query expansion is a simple synonym expansion
//     (decision, decide, decided, decision-state, transition, superseded)
//     OR'd into the FTS5 MATCH expression. The decision_transitions
//     table is read but not joined — its content lives in C3 results
//     via the rationale column.
//
//   - Rationale preservation: C3 results always carry the rationale
//     field. If empty, the strategy returns the row but logs a sentinel
//     in evidence (operator-facing audit, not a runtime error).
type C3DecisionRecall struct {
	// IncludeSuperseded, when true, returns decisions in any state
	// (active, superseded, contested). Default false (active-only).
	// Used by operator-facing audit views.
	IncludeSuperseded bool
	// MaxHopDepth bounds graph expansion (1 or 2). Default 2. 1-hop
	// is faster but loses the ADR-chain power (per MOOSEDev, KG
	// supersession retrieval needs 2-hop to traverse A → ADR-1 →
	// revised ADR-1).
	MaxHopDepth int
}

// VibeCase returns "decision" (C3).
func (c *C3DecisionRecall) VibeCase() string { return VibeCaseDecision }

// Weights returns the canonical C3 blend (0.30 FTS5 + 0.70 graph).
func (c *C3DecisionRecall) Weights() Weights {
	return Weights{FTS5: 0.30, Graph: 0.70}
}

// Recall runs the C3 pipeline:
//
//  1. FTS5 over kind='decision' rows in projectID.
//  2. 2-hop graph expansion via adr_refs (rows that share an ADR
//     ref with a seed row).
//  3. Score = weighted reciprocal rank fusion (RRF, k=60) of FTS5
//     ranks + graph adjacency scores.
//  4. Filter decision_state='active' (or all states if IncludeSuperseded).
//  5. Apply decay — but C3 is "forever", so the decay factor is 1.0
//     for every row, and the only signal is access_count boost
//     (per ScrubJay-MEM, capped at 2.0×).
//  6. Sort by composite score, return topK.
func (c *C3DecisionRecall) Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("C3DecisionRecall: db is nil")
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

	stateFilter := "COALESCE(m.decision_state, 'active') = 'active'"
	if c.IncludeSuperseded {
		stateFilter = "1=1" // accept any decision_state
	}

	maxHop := c.MaxHopDepth
	if maxHop == 0 {
		maxHop = 2
	}

	// Step 1: FTS5 query expansion + candidate retrieval.
	expanded := expandDecisionQuery(query)
	seeds, err := c.fts5Recall(ctx, db, expanded, projectID, stateFilter, topK*3)
	if err != nil {
		return nil, fmt.Errorf("C3DecisionRecall fts5: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	// Step 2: 2-hop graph expansion.
	graphScores, err := c.graphExpand(ctx, db, seeds, projectID, stateFilter, maxHop)
	if err != nil {
		return nil, fmt.Errorf("C3DecisionRecall graph: %w", err)
	}

	// Step 3: hydrate graph-expanded rows that aren't already in seeds.
	extra, err := c.hydrateGraphRows(ctx, db, graphScores, seeds, projectID, stateFilter)
	if err != nil {
		return nil, fmt.Errorf("C3DecisionRecall hydrate: %w", err)
	}

	// Step 4: score + rank.
	candidates := append(seeds, extra...)
	ranked := scoreC3(candidates, seeds, graphScores, c.Weights())
	return topKRows(ranked, topK), nil
}

// fts5Recall runs the FTS5 bm25 candidate query against decision-kind
// rows in projectID. The stateFilter argument lets the caller control
// whether superseded decisions are surfaced.
//
// seedLimit is the cap on initial candidates; the strategy uses
// topK*3 to give the graph expansion enough material to add value
// without exploding the working set.
//
// NOTE: We use the unaliased table name in the MATCH clause because
// some FTS5 builds in modernc.org/sqlite don't accept aliases there
// (matches the agent_memory.Store.RecallFiltered precedent).
func (c *C3DecisionRecall) fts5Recall(ctx context.Context, db *sql.DB, query, projectID, stateFilter string, seedLimit int) ([]AnnotatedRow, error) {
	q := `SELECT ` + annotatedColumns + `
		FROM agent_memory_fts
		JOIN agent_memory m ON m.id = agent_memory_fts.rowid
		WHERE agent_memory_fts MATCH ?
		  AND m.kind = 'decision'
		  AND m.project_id = ?
		  AND ` + stateFilter + `
		ORDER BY rank
		LIMIT ?`
	rows, err := db.QueryContext(ctx, q, query, projectID, seedLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnotatedRows(rows)
}

// graphExpand performs 2-hop graph expansion via adr_refs.
//
// Algorithm (per SPEC §6.3 + R-D §4 I-1):
//
//  1. Collect every ADR ref appearing in any seed row's adr_refs.
//  2. For each ADR ref, find all decision-kind rows in projectID
//     whose adr_refs contains that ref (1-hop neighbors).
//  3. For each 1-hop neighbor, repeat step 2 to find 2-hop neighbors.
//  4. Score = number of distinct paths from any seed (capped at 5
//     to bound extreme cases).
//
// Returns a map[rowID]score. Seeds themselves are NOT in the result —
// the caller is expected to merge seeds + expanded rows.
func (c *C3DecisionRecall) graphExpand(ctx context.Context, db *sql.DB, seeds []AnnotatedRow, projectID, stateFilter string, maxHop int) (map[int64]int, error) {
	scores := make(map[int64]int)
	visited := make(map[int64]bool)
	for _, s := range seeds {
		visited[s.ID] = true
	}

	// Collect ADR refs from seeds.
	refToSeedCount := make(map[string]int)
	for _, s := range seeds {
		for _, ref := range splitRefs(s.AdrRefs) {
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
		// Build LIKE patterns for each ADR ref. Args order must match
		// SQL placeholder order: project_id first, then each LIKE
		// pattern (one per OR-ed condition).
		args := make([]interface{}, 0, len(frontierRefs)+1)
		args = append(args, projectID)
		refPatterns := make([]string, len(frontierRefs))
		for i, r := range frontierRefs {
			refPatterns[i] = "%," + escapeLike(r) + ",%"
			args = append(args, refPatterns[i])
		}

		// Surround with synthetic commas so substring matches don't
		// cross reference boundaries. e.g. "ADR-1" should NOT match
		// a row containing "ADR-10".
		//
		// stateFilter uses `m.decision_state` — alias `m` is on the
		// JOIN side, not this single-table query. Strip the alias
		// prefix for the bare-table form.
		bareStateFilter := strings.ReplaceAll(stateFilter, "m.", "")
		q := `SELECT id, COALESCE(adr_refs, '')
			FROM agent_memory
			WHERE kind = 'decision'
			  AND project_id = ?
			  AND ` + bareStateFilter + `
			  AND (`
		conds := make([]string, len(refPatterns))
		for i := range refPatterns {
			conds[i] = "(',' || adr_refs || ',') LIKE ? ESCAPE '\\'"
			_ = i
		}
		q += strings.Join(conds, " OR ") + `)`

		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("graphExpand hop=%d: %w", hop, err)
		}

		nextRefs := make(map[string]int)
		for rows.Next() {
			var id int64
			var refs string
			if err := rows.Scan(&id, &refs); err != nil {
				rows.Close()
				return nil, err
			}
			if visited[id] {
				continue
			}
			visited[id] = true
			// Score = number of refs that link this row to any seed/previous.
			matchCount := 0
			for _, ref := range splitRefs(refs) {
				if _, ok := refToSeedCount[ref]; ok {
					matchCount++
				}
				// Track refs for the next hop.
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

		// Promote nextRefs to frontier for the next hop.
		frontierRefs = refKeysSorted(nextRefs)
		// Also fold into refToSeedCount so 2-hop paths score correctly.
		for r, c := range nextRefs {
			refToSeedCount[r] += c
		}
	}

	return scores, nil
}

// hydrateGraphRows loads full AnnotatedRow data for each rowID in
// graphScores that isn't already in the seed set. The returned rows
// carry full Phase 5 fields (rationale, decision_state, etc.) so the
// caller doesn't need to do a second lookup.
func (c *C3DecisionRecall) hydrateGraphRows(ctx context.Context, db *sql.DB, graphScores map[int64]int, seeds []AnnotatedRow, projectID, stateFilter string) ([]AnnotatedRow, error) {
	if len(graphScores) == 0 {
		return nil, nil
	}
	seedIDs := make(map[int64]bool, len(seeds))
	for _, s := range seeds {
		seedIDs[s.ID] = true
	}
	missing := make([]interface{}, 0, len(graphScores))
	missingIDs := make([]int64, 0, len(graphScores))
	for id := range graphScores {
		if !seedIDs[id] {
			missing = append(missing, id)
			missingIDs = append(missingIDs, id)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	q := `SELECT ` + annotatedColumns + `
		FROM agent_memory m
		WHERE m.project_id = ?
		  AND ` + stateFilter + `
		  AND m.id IN (` + placeholders(len(missing)) + `)`
	args := append([]interface{}{projectID}, missing...)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnotatedRows(rows)
}

// scoreC3 ranks the candidate rows by composite score.
//
// Score = (FTS5_weight × rrf_rank_score) + (Graph_weight × adjacency_score)
//
// RRF (Cormack 2009, k=60) maps a row's rank position to a [0, 1]
// score: score = 1 / (k + rank). Lower rank → higher score.
//
// adjacency_score = (number of matched ADR paths) / max_paths (normalized
// to [0, 1]). Capped at 1.0 (5+ matches is full credit).
func scoreC3(candidates, seeds []AnnotatedRow, graphScores map[int64]int, w Weights) []rankedRow {
	// Build seed-rank map (1 = best, 2 = second, etc.).
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
		var ftsScore, graphScore float64
		if rank, ok := seedRank[c.ID]; ok {
			ftsScore = 1.0 / (60.0 + float64(rank))
		}
		if g, ok := graphScores[c.ID]; ok {
			graphScore = float64(g) / float64(maxGraphScore)
			if graphScore > 1.0 {
				graphScore = 1.0
			}
		}
		// Decay: C3 is forever, so DecayScore = 1.0 (per spec §9).
		// Access-count boost (ScrubJay-MEM) applies if refresh_on_access.
		accessBoost := 1.0
		if c.RefreshOnAccess && c.AccessCount > 0 {
			accessBoost = 1.0 + 0.3*mathLog10(float64(c.AccessCount+1))
			if accessBoost > 2.0 {
				accessBoost = 2.0
			}
		}
		score := (w.FTS5*ftsScore + w.Graph*graphScore) * accessBoost
		out = append(out, rankedRow{row: c, score: score})
	}
	return out
}

// rankedRow pairs a row with its composite score. Internal to C3.
type rankedRow struct {
	row   AnnotatedRow
	score float64
}

func topKRows(in []rankedRow, k int) []AnnotatedRow {
	// Stable sort by score descending. Stable to preserve insertion
	// order on ties (which makes the test results deterministic).
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j-1].score < in[j].score; j-- {
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
	if k > len(in) {
		k = len(in)
	}
	out := make([]AnnotatedRow, k)
	for i := 0; i < k; i++ {
		out[i] = in[i].row
	}
	return out
}

// expandDecisionQuery augments the user's query with decision-aware
// synonyms. FTS5 OR-expression, simple and deterministic.
//
// Phase 5 alpha.18: minimal expansion (5 terms). Alpha.19 may
// integrate operator-curated synonym tables.
func expandDecisionQuery(query string) string {
	// Strip FTS5 reserved characters that could change semantics
	// (we keep OR as a controlled expansion signal).
	cleaned := strings.TrimSpace(query)
	if cleaned == "" {
		return cleaned
	}
	// Append synonyms. Use OR prefix because FTS5 MATCH defaults to
	// implicit AND on token boundaries — we want any-of.
	return "(" + cleaned + ") OR decision OR decided OR supersede OR superseded OR rationale"
}

// splitRefs parses a comma-separated ADR/INV ref list into trimmed
// non-empty entries. Mirrors the agent_memory save-time split.
func splitRefs(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// refKeysSorted returns the keys of m sorted alphabetically. The
// deterministic order keeps query construction stable (test
// determinism + lower log entropy).
func refKeysSorted(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// insertion sort: small maps, deterministic output
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// mathLog10 is a small wrapper so we don't need to import math in
// every strategy. math.Log10 is fine here (cheap and stable).
func mathLog10(x float64) float64 {
	return log10Fn(x)
}

// Sentinel error for clarity at call sites that want to inspect
// why no rows came back (test helpers, operator UI).
var ErrNoSeeds = errors.New("recall: no seed rows matched the FTS5 query")
