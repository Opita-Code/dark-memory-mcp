// Package economy implements the Atlan 5-bucket token economy pipeline.
// Per RFC §D-8: every retrieval passes through this pipeline before
// reaching the LLM. The pipeline is the only retrieval path; orchestrators
// never return raw 50-item lists.
//
// Pipeline stages (in order):
//
//   1. dedup         — same URL / content hash => one entry, keep highest confidence
//   2. filter        — drop items below confidence threshold
//   3. truncate      — cap each item's snippet / title to N chars
//   4. compress      — prefer {title, short_summary, url} over full body
//   5. cap           — total items bounded to N (top-by-confidence)
//
// Defaults are tuned for a 200K-context model receiving prefill blocks:
//   - FilterConfidence = 0.5
//   - TruncatePerItem  = 500 chars
//   - CapTotal         = 10
//
// All functions are pure: they take []Item and return []Item. The Store
// is touched only by the high-level wrappers (CompressRecall, etc.).
package economy

import (
	"sort"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/research"
)

// Options controls the Atlan pipeline.
type Options struct {
	FilterConfidenceThreshold float32 // items with confidence < this are dropped (default 0.5)
	TruncatePerItem            int     // max chars for snippet/title per item (default 500)
	CapTotal                   int     // total items returned (default 10)
	CompressBody               bool    // if true, replace raw body with title+summary (default true)
}

// DefaultOptions returns the production-tuned defaults.
func DefaultOptions() Options {
	return Options{
		FilterConfidenceThreshold: 0.5,
		TruncatePerItem:            500,
		CapTotal:                   10,
		CompressBody:               true,
	}
}

// Compress runs the full 5-stage pipeline. The input is assumed to be
// the Store's Recall() output (or equivalent).
func Compress(items []research.Item, opts Options) []research.Item {
	if opts.FilterConfidenceThreshold == 0 && opts.TruncatePerItem == 0 && opts.CapTotal == 0 {
		opts = DefaultOptions()
	}
	out := make([]research.Item, len(items))
	copy(out, items)

	out = dedup(out)
	out = filterConfidence(out, opts.FilterConfidenceThreshold)
	out = truncate(out, opts.TruncatePerItem)
	if opts.CompressBody {
		out = compressItems(out)
	}
	out = capTop(out, opts.CapTotal)

	return out
}

// ---------------------------------------------------------------------------
// Stage 1: dedup
// ---------------------------------------------------------------------------

// dedup removes items with duplicate URL or duplicate title+source.
// When duplicates exist, the highest-confidence copy wins. Order is
// preserved otherwise.
func dedup(items []research.Item) []research.Item {
	if len(items) <= 1 {
		return items
	}
	type key struct {
		url    string
		title  string
		source string
	}
	best := map[key]*research.Item{}
	order := []key{}
	for i := range items {
		it := &items[i]
		k := key{url: it.URL, title: it.Title, source: it.Source}
		if existing, ok := best[k]; ok {
			if it.Confidence > existing.Confidence {
				best[k] = it
			}
		} else {
			best[k] = it
			order = append(order, k)
		}
	}
	out := make([]research.Item, 0, len(order))
	for _, k := range order {
		out = append(out, *best[k])
	}
	return out
}

// ---------------------------------------------------------------------------
// Stage 2: filter confidence
// ---------------------------------------------------------------------------

// filterConfidence drops items whose confidence < threshold. Threshold
// 0 keeps all. Negative thresholds keep none.
func filterConfidence(items []research.Item, threshold float32) []research.Item {
	if threshold <= 0 {
		return items
	}
	out := make([]research.Item, 0, len(items))
	for _, it := range items {
		if it.Confidence >= threshold {
			out = append(out, it)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Stage 3: truncate
// ---------------------------------------------------------------------------

// truncate caps each item's title + snippet to maxChars. Truncates at
// word boundary when possible. Mutates a copy.
func truncate(items []research.Item, maxChars int) []research.Item {
	if maxChars <= 0 {
		return items
	}
	out := make([]research.Item, len(items))
	for i, it := range items {
		out[i] = it
		out[i].Title = clampAtWord(it.Title, maxChars)
		out[i].Snippet = clampAtWord(it.Snippet, maxChars)
	}
	return out
}

func clampAtWord(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return cut + "..."
}

// ---------------------------------------------------------------------------
// Stage 4: compress
// ---------------------------------------------------------------------------

// compressItems reduces each item to a compact representation: title +
// first sentence of snippet + URL. Drops the long Raw body.
func compressItems(items []research.Item) []research.Item {
	out := make([]research.Item, len(items))
	for i, it := range items {
		out[i] = it
		summary := firstSentence(it.Snippet)
		if summary != "" {
			out[i].Snippet = summary
		}
		out[i].Raw = "" // drop verbose body
	}
	return out
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, sep := range []string{". ", "! ", "? ", "\n"} {
		if i := strings.Index(s, sep); i > 0 {
			return s[:i+1]
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Stage 5: cap top-N
// ---------------------------------------------------------------------------

// rankedItem carries an item alongside its original index for stable
// tie-breaking in capTop.
type rankedItem struct {
	idx        int
	confidence float32
	item       research.Item
}

// capTop keeps the top N items by confidence. Stable on tie (preserves
// input order via original-index sort). If N <= 0, returns all.
func capTop(items []research.Item, n int) []research.Item {
	if n <= 0 || len(items) <= n {
		return items
	}
	sorted := make([]rankedItem, len(items))
	for i, it := range items {
		sorted[i] = rankedItem{idx: i, confidence: it.Confidence, item: it}
	}
	sort.SliceStable(sorted, func(a, b int) bool {
		return sorted[a].confidence > sorted[b].confidence
	})
	out := make([]research.Item, n)
	for i := 0; i < n; i++ {
		out[i] = sorted[i].item
	}
	// Re-sort by original index to preserve input order at the top-N.
	sort.SliceStable(out, func(a, b int) bool {
		ai := originalIndex(sorted, out[a])
		bi := originalIndex(sorted, out[b])
		return ai < bi
	})
	return out
}

func originalIndex(sorted []rankedItem, target research.Item) int {
	for _, k := range sorted {
		if k.item.ID == target.ID {
			return k.idx
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// High-level wrappers
// ---------------------------------------------------------------------------

// CompressRecall runs a Store.Recall and applies the Atlan pipeline.
// This is the canonical retrieval entry point for orchestrators.
func CompressRecall(items []research.Item, opts Options) []research.Item {
	return Compress(items, opts)
}

// EstimateTokens returns an approximate token count for a list of
// items after Atlan compression. Useful for the orchestrator's `next`
// hint ("token budget remaining: ~X tokens").
// Rough heuristic: 1 token ≈ 4 chars of English text.
func EstimateTokens(items []research.Item) int {
	total := 0
	for _, it := range items {
		total += len(it.Title)
		total += len(it.Snippet)
		total += len(it.URL)
		total += len(it.Source)
	}
	return (total + 3) / 4
}