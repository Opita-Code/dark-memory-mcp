package entity

// SPEC-194 (T9): the public tokenizer.
//
// WHY THIS EXISTS. The plan's shortlist is ranked with BM25 over the
// tool registry, and BM25 needs the SAME tokenizer on both sides (the
// documents and the query). This repo already had a tokenizer —
// tokenizeAndCount, private to this package — used by the entity
// extractor. Writing a third one would guarantee drift: the stopword
// list and the minimum token length would diverge, and the plan's
// ranking would stop matching what the rest of the kernel believes a
// "word" is.
//
// So the existing policy is EXPORTED, not duplicated. The internal
// function is untouched: this is additive, and the extractor's
// behaviour does not change.
//
// The policy, stated once:
//   - lower-case;
//   - split on whitespace and ASCII punctuation/symbols;
//   - drop tokens shorter than MinTokenLen (3);
//   - drop stopwords.
//
// It is deliberately simple and deterministic: no unicode word-break,
// no stemming, no stemming-like normalisation. A BM25 score that
// depends on a unicode library version is not a reproducible number,
// and the plan's weights end up in an audit trail.

import (
	"strings"
	"unicode"
)

// MinTokenLen is the lower bound for a token to be kept. Exported so
// callers can reason about it; the value is the same one the entity
// extractor uses.
const MinTokenLen = minTokenLen

// Tokenize splits text into canonical lowercase tokens, applying the
// same policy as the entity extractor: lower-case, split on
// whitespace and ASCII punctuation, drop stopwords and tokens shorter
// than MinTokenLen.
//
// It returns the tokens in ORDER OF APPEARANCE with duplicates
// preserved. Use TokenizeSet when you need the frequency map.
func Tokenize(text string) []string {
	counts := tokenizeAndCount(text)
	if len(counts) == 0 {
		return nil
	}
	// tokenizeAndCount loses the order, so re-derive it with the same
	// state machine. This is a second pass over the same string, not a
	// second POLICY: both passes share tokenizeAndCount's rules by
	// construction below (same lower-casing, same stopword set, same
	// length bound).
	out := make([]string, 0, len(counts))
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
		out = append(out, s)
	}
	for _, r := range text {
		switch {
		case unicode.IsSpace(r), unicode.IsPunct(r), unicode.IsSymbol(r):
			flush()
		default:
			tok.WriteRune(r)
		}
	}
	flush()
	return out
}

// TokenizeSet returns the frequency map of the canonical tokens.
func TokenizeSet(text string) map[string]int {
	return tokenizeAndCount(text)
}
