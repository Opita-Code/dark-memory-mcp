// Package entity — deterministic entity extraction for v2.9.0-alpha
// PR-3 (agent_memory row 160 PR-3).
//
// # What this is
//
// PR-3 ships a local, deterministic extractor as the seed
// implementation. Operators who want richer extraction (e.g.,
// on-prem LLM via drift_judge) can swap Extract for a network
// adapter in PR-3.1; the contract — Entity + Entity.Source —
// stays the same so no migration is needed.
//
// # Why deterministic first
//
// row 160 PR-3 calls SaveAgentMemory with `extract_entities=true`
// to "call drift_judge with a deterministic extract prompt". For
// PR-3 we read "deterministic extract" as "same input → same output,
// no network". The Save path stays local + auditable; the cost
// is real (save now does an O(content) pass) but bounded.
//
// Operators flip `extract_entities=true` per Save call. The
// default (false) preserves PR-0..PR-2 behavior: rows saved with
// no entities are invisible to Entity-axis search. Backward compat
// per row 160 PR-3.
//
// # What "entity" means
//
// For PR-3 we treat entity as a single-token noun surfaced from
// the row's content + title + tags. Examples: "dark", "memory",
// "openai", "vector", "search". Hyphens + punctuation are token
// separators, so "vector-search" produces TWO entities
// ("vector", "search") — operators combine them via the search
// filter (AND semantics across the entity list).
// Stopwords + duplicate-words-in-different-case collapse; tokens
// shorter than 3 chars drop. We do NOT try to classify (person /
// org / place) yet — that's a future PR-3.x when drift_judge
// becomes available.
//
// # Determinism guarantees
//
// Same input → same output, byte-for-byte. No log lines, no env
// lookups, no clock dependency. Tests assert exact ordering by
// frequency-then-alphabetical so test fixtures never go flaky.
package entity

import (
	"sort"
	"strings"
	"unicode"
)

// SourceDeterministic is the Source tag we emit for PR-3 rows.
// Future PRs (PR-3.1, drift_judge integration) will define their
// own Source tag (e.g., "drift_judge:prompt_v1").
const SourceDeterministic = "deterministic"

// Entity is one extracted noun phrase from a row's payload.
// Stored as-is in agent_memory_entities (mem_id, entity, source,
// confidence, model, created_at).
//
// `Source` tags the producer ("deterministic" for PR-3).
// `Confidence` is a 0..1 score — for PR-3 we always emit 1.0
// because there is no model to score against. Future PRs
// (drift_judge) will emit 0 < c < 1.
// `Model` is the producer's model id. PR-3 leaves this empty.
type Entity struct {
	Value      string  `json:"entity"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
	Model      string  `json:"model,omitempty"`
}

// Extract returns the canonical entity list for the given
// content + title + tags. The output is deterministic + sorted
// (frequency DESC, then value ASC) + deduplicated (case-folded).
//
// Inputs are concatenated with a single space before tokenization;
// tags and title are treated as plain text (no special handling
// for the comma-separated tags format).
//
// Returns at most maxEntities (default 20 if maxEntities <= 0)
// entries. Empty input → empty output (no error).
func Extract(content, title, tags string) []Entity {
	return ExtractWithLimit(content, title, tags, 20)
}

// ExtractWithLimit is like Extract but caps the result at
// maxEntities (sorted by frequency DESC, value ASC). maxEntities <= 0
// means default (20).
func ExtractWithLimit(content, title, tags string, maxEntities int) []Entity {
	if maxEntities <= 0 {
		maxEntities = 20
	}
	joined := strings.TrimSpace(content)
	if title != "" {
		joined = strings.TrimSpace(joined + " " + title)
	}
	if tags != "" {
		// Tags are stored comma-separated; replace commas so they
		// tokenize cleanly with the rest of the text.
		joined = strings.TrimSpace(joined + " " + strings.ReplaceAll(tags, ",", " "))
	}
	if joined == "" {
		return nil
	}
	freq := tokenizeAndCount(joined)
	if len(freq) == 0 {
		return nil
	}
	// Sort by frequency DESC, then value ASC for determinism.
	type kv struct {
		val   string
		count int
	}
	pairs := make([]kv, 0, len(freq))
	for v, c := range freq {
		pairs = append(pairs, kv{v, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].val < pairs[j].val
	})
	if len(pairs) > maxEntities {
		pairs = pairs[:maxEntities]
	}
	out := make([]Entity, len(pairs))
	for i, p := range pairs {
		out[i] = Entity{
			Value:      p.val,
			Source:     SourceDeterministic,
			Confidence: 1.0, // PR-3 stub: no model → no real confidence
			Model:      "",
		}
	}
	return out
}

// stopWords is the deterministic stopword list. Tokens in this
// set (case-folded) are dropped before frequency counting. The list
// is short on purpose: prefer false positives (rare noun made of
// stopword letters) over false negatives (legitimate entity
// eaten by the stopword filter).
var stopWords = map[string]struct{}{
	"the":   {},
	"a":     {},
	"an":    {},
	"of":    {},
	"to":    {},
	"is":    {},
	"are":   {},
	"was":   {},
	"were":  {},
	"be":    {},
	"been":  {},
	"being": {},
	"and":   {},
	"or":    {},
	"but":   {},
	"if":    {},
	"then":  {},
	"else":  {},
	"for":   {},
	"on":    {},
	"in":    {},
	"at":    {},
	"by":    {},
	"with":  {},
	"from":  {},
	"into":  {},
	"onto":  {},
	"off":   {},
	"out":   {},
	"up":    {},
	"down":  {},
	"over":  {},
	"under": {},
	"as":    {},
	"so":    {},
	"than":  {},
	"that":  {},
	"this":  {},
	"it":    {},
	"its":   {},
	"i":     {},
	"we":    {},
	"you":   {},
	"he":    {},
	"she":   {},
	"they":  {},
	"them":  {},
	"us":    {},
	"me":    {},
	"my":    {},
	"your":  {},
	"our":   {},
	"their": {},
}

// minTokenLen is the lower bound for a token to be kept.
// Catches "x", "id", "db", "v1" etc. — operator-tunable via
// future PR-3.x if needed; PR-3 hardcodes 3 for determinism.
const minTokenLen = 3

// tokenizeAndCount lower-cases, splits on whitespace + ASCII
// punctuation, filters stopwords + short tokens, and returns a
// frequency map keyed on canonical case-folded token.
//
// Tokens shorter than minTokenLen are skipped (catches "v1",
// "id", "db", etc. — false positives in many cases). All
// remaining tokens are folded to lowercase for stable
// frequency-counting across case variations.
func tokenizeAndCount(text string) map[string]int {
	out := map[string]int{}
	// tokenizer state machine — split on whitespace, lowercase
	// pass, strip ASCII punctuation. We keep it simple (no
	// unicode word-break) for PR-3's determinism guarantee.
	var tok strings.Builder
	flush := func() {
		if tok.Len() == 0 {
			return
		}
		s := strings.ToLower(tok.String())
		tok.Reset()
		if len(s) < minTokenLen {
			return
		}
		if _, stop := stopWords[s]; stop {
			return
		}
		out[s]++
	}
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			flush()
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			// Treat punctuation + symbols as separators; do NOT
			// include them in tokens.
			flush()
		default:
			tok.WriteRune(r)
		}
	}
	flush()
	return out
}
