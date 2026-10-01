package recall

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// C7MultiRecall is the alpha.18 multi-vibe ensemble dispatcher.
//
// SPEC-alpha-11-phase5.md §6.7:
//
//   - Ensemble dispatcher: detects sub-tasks, delegates to C1-C6
//     strategies, merges via RRF.
//   - Per-subtask weight tuning (operator-configurable).
//   - Decay: subtask-aware (max of subtask decays).
//
// alpha.18 implementation notes:
//
//   - "Detect sub-tasks" is alpha.19 (LLM-extracted). alpha.18 uses
//     a deterministic token-based router: the strategy inspects the
//     query and routes to one of C1/C2/C3/C4 based on dominant
//     keyword family. C5/C6 are reached via explicit operator flags
//     (operator adds "video:" / "audio:" prefix).
//
//   - The RRF merge is the same Cormack k=60 formula used by the
//     other strategies.
//
//   - The dispatcher composes with the strategy registry: it calls
//     RecallFor() for each subtask and merges.
type C7MultiRecall struct {
	// SubTaskStrategies overrides the auto-detection. If non-empty,
	// the dispatcher uses exactly these vibe_cases (left-to-right
	// order, equal weight). Empty = use the auto-detection.
	SubTaskStrategies []string
	// SubTaskWeights overrides equal weight per subtask. Must match
	// len(SubTaskStrategies) if non-empty. nil = equal weights.
	SubTaskWeights []float64
}

// VibeCase returns "multi" (C7).
func (c *C7MultiRecall) VibeCase() string { return VibeCaseMulti }

// Weights returns an even blend across the 4 strategies auto-detected
// from the query. The exact blend isn't a fixed contract — C7 is the
// only strategy whose weights depend on the input.
func (c *C7MultiRecall) Weights() Weights {
	// We don't validate this — the canonical weight is dynamic.
	// Returning 1/4 each as a placeholder for any code that asks.
	return Weights{FTS5: 0.25, Vector: 0.25, Graph: 0.25, CrossModal: 0.25}
}

// Recall runs the C7 pipeline:
//
//  1. Detect sub-task vibe_cases (or use SubTaskStrategies override).
//  2. For each, run RecallFor() at topK*2 to give the merge enough
//     material.
//  3. Merge via RRF across sub-task results.
//  4. Return topK.
func (c *C7MultiRecall) Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("C7MultiRecall: db is nil")
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

	strategies := c.SubTaskStrategies
	if len(strategies) == 0 {
		strategies = detectSubTaskVibes(query)
	}
	if len(strategies) == 0 {
		// Default fallback: text + decision.
		strategies = []string{VibeCaseText, VibeCaseDecision}
	}
	weights := c.SubTaskWeights
	if len(weights) != len(strategies) {
		// Equal weights.
		weights = make([]float64, len(strategies))
		w := 1.0 / float64(len(strategies))
		for i := range weights {
			weights[i] = w
		}
	}

	// Per-subtask recall.
	type subtaskResult struct {
		rows  []AnnotatedRow
		ranks map[int64]int
		weight float64
	}
	results := make([]subtaskResult, 0, len(strategies))
	for i, vc := range strategies {
		if !IsValidVibeCase(vc) {
			continue
		}
		rows, err := RecallFor(ctx, db, vc, query, projectID, topK*2)
		if err != nil {
			// Skip strategies that fail (e.g., not yet registered).
			continue
		}
		ranks := make(map[int64]int, len(rows))
		for j, r := range rows {
			ranks[r.ID] = j + 1
		}
		results = append(results, subtaskResult{rows: rows, ranks: ranks, weight: weights[i]})
	}
	if len(results) == 0 {
		return nil, nil
	}

	// RRF merge across subtask results.
	merged := make(map[int64]*rankedRow)
	for _, res := range results {
		for _, r := range res.rows {
			rank := res.ranks[r.ID]
			rrf := 1.0 / (60.0 + float64(rank))
			if existing, ok := merged[r.ID]; ok {
				existing.score += res.weight * rrf
			} else {
				score := res.weight * rrf
				merged[r.ID] = &rankedRow{row: r, score: score}
			}
		}
	}

	out := make([]rankedRow, 0, len(merged))
	for _, r := range merged {
		out = append(out, *r)
	}
	return topKRows(out, topK), nil
}

// detectSubTaskVibes routes a query to one of the canonical 4
// strategies based on dominant keyword family. C5/C6 are not
// auto-detected (operator must opt in via prefix).
//
// Heuristic (alpha.18):
//   - "decided", "decision", "should we" → decision
//   - "code", "function", "method", "class" → code
//   - "paper", "study", "research", "arxiv" → research
//   - default → text
func detectSubTaskVibes(query string) []string {
	lower := strings.ToLower(query)
	out := []string{}
	seen := make(map[string]bool)

	add := func(v string) {
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}

	if containsAny(lower, "decide", "decided", "decision", "should we", "rationale") {
		add(VibeCaseDecision)
	}
	if containsAny(lower, "code", "function", "method", "class", "module", "package") {
		add(VibeCaseCode)
	}
	if containsAny(lower, "paper", "study", "research", "arxiv", "doi", "cited") {
		add(VibeCaseResearch)
	}
	if containsAny(lower, "video:", "animation", "clip", "frame") {
		add(VibeCaseVideo)
	}
	if containsAny(lower, "audio:", "voice", "speaker", "timbre") {
		add(VibeCaseAudio)
	}

	// Always include text as a fallback.
	if len(out) == 0 {
		add(VibeCaseText)
	}
	return out
}

// containsAny returns true if s contains any of the substrings.
// Case-sensitive (caller pre-lowercases).
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
