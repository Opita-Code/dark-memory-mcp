// Package plan — SPEC-194 (T9): ranking the tool candidates.
//
// This package ranks the tools of the registry against the intent of a
// plan, and produces the SHORTLIST the judge validates and the human
// approves.
//
// # Why BM25 and not embeddings
//
// Measured on this machine: the embedder of the kernel is at
// kind=none, dim=0, so the search is LEXICAL only. Beyond that, the
// literature is explicit: BM25 over the tool registry is ~40x faster
// than BGE-large on CPU, and the FULL DOC of the tool was the best
// configuration (ToolRet: 200 questions, up to 3794 tools).
//
// # Why the ranker PROPOSES and does not choose
//
// The score is a proposal. Measured: routers that rely on the task
// description rank BELOW the large model (Strategy Auctions,
// arXiv 2602.02751), and retrieve-and-rerank beats choosing from a
// whole catalogue (SkillRouter, arXiv 2603.22455, 74.0 % top-1). So
// the deterministic part produces a shortlist, the judge validates it,
// and the human approves it.
//
// # Why this is a separate package
//
// The ranker is a PURE function of (query, documents). No store, no
// clock, no LLM. That makes it testable against known scores, and it
// keeps the tool-handling layer free of ranking logic.
package plan

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/entity"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// Document is one tool, as the ranker sees it.
type Document struct {
	// Tool is the bare tool name (no prefix).
	Tool string `json:"tool"`

	// Doc is the FULL documentation of the tool: name, description
	// and schema, flattened. The full doc is the configuration the
	// literature reports as the best one.
	Doc string `json:"doc"`
}

// BM25 parameters. The defaults are the textbook ones (k1=1.2, b=0.75)
// and they are exposed so a future calibration can change them without
// touching the algorithm.
const (
	// BM25K1 is the term-frequency saturation parameter.
	BM25K1 = 1.2

	// BM25B is the length-normalisation parameter. 0.75 is the
	// standard value: it normalise for document length, so a long
	// tool description does not win merely by having more words.
	BM25B = 0.75
)

// Ranker ranks documents against a query with BM25.
type Ranker struct {
	// Docs is the corpus.
	Docs []Document

	// docFreq counts how many documents contain each term.
	docFreq map[string]int

	// docLen is the token length of each document, parallel to Docs.
	docLen []int

	// avgLen is the average document length.
	avgLen float64

	// tf holds the term frequencies per document.
	tf []map[string]int
}

// NewRanker builds a ranker over the corpus. It tokenises once: the
// corpus is fixed for the lifetime of the registry, so re-tokenising
// it on every plan would be wasted work.
func NewRanker(docs []Document) *Ranker {
	r := &Ranker{
		Docs:    docs,
		docFreq: make(map[string]int),
		docLen:  make([]int, len(docs)),
		tf:      make([]map[string]int, len(docs)),
	}
	total := 0
	for i, d := range docs {
		tokens := entity.Tokenize(d.Doc)
		counts := entity.TokenizeSet(d.Doc)
		r.tf[i] = counts
		r.docLen[i] = len(tokens)
		total += len(tokens)
		for term := range counts {
			r.docFreq[term]++
		}
	}
	if len(docs) > 0 {
		r.avgLen = float64(total) / float64(len(docs))
	}
	return r
}

// Score is one document's BM25 score against a query.
func (r *Ranker) Score(query string, docIdx int) float64 {
	if r == nil || docIdx < 0 || docIdx >= len(r.Docs) {
		return 0
	}
	queryTerms := entity.Tokenize(query)
	if len(queryTerms) == 0 {
		return 0
	}
	n := float64(len(r.Docs))
	if n == 0 {
		return 0
	}
	avgLen := r.avgLen
	if avgLen == 0 {
		avgLen = 1
	}
	dl := float64(r.docLen[docIdx])
	var score float64
	seen := make(map[string]bool, len(queryTerms))
	for _, term := range queryTerms {
		if seen[term] {
			// A repeated query term must not inflate the score.
			continue
		}
		seen[term] = true
		f := float64(r.tf[docIdx][term])
		if f == 0 {
			continue
		}
		df := float64(r.docFreq[term])
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		norm := f * (BM25K1 + 1) / (f + BM25K1*(1-BM25B+BM25B*dl/avgLen))
		score += idf * norm
	}
	return score
}

// Rank returns the top-k documents for the query, ordered by score
// DESCENDING.
//
// The ordering is deterministic: documents with the SAME score are
// ordered by tool name, so two runs of the same plan produce the same
// shortlist. A shortlist that reorders itself between runs would make
// the audit trail a lie.
//
// k <= 0 means "no limit".
func (r *Ranker) Rank(query string, k int) []vibeflow.Candidate {
	if r == nil || len(r.Docs) == 0 {
		return nil
	}
	type scored struct {
		idx   int
		score float64
	}
	all := make([]scored, 0, len(r.Docs))
	for i := range r.Docs {
		all = append(all, scored{idx: i, score: r.Score(query, i)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return r.Docs[all[i].idx].Tool < r.Docs[all[j].idx].Tool
	})
	if k > 0 && len(all) > k {
		all = all[:k]
	}
	out := make([]vibeflow.Candidate, 0, len(all))
	for _, s := range all {
		if s.score <= 0 {
			// A document that matches nothing is not a candidate.
			// Keeping it would pad the shortlist with noise and
			// inflate the tier for no reason.
			continue
		}
		out = append(out, vibeflow.Candidate{
			Tool:        r.Docs[s.idx].Tool,
			Weight:      round4(s.score),
			Rationale:   rationaleFor(query, r.Docs[s.idx].Tool, s.score),
			ScoreSource: vibeflow.ScoreSourceBM25,
		})
	}
	return out
}

// rationaleFor builds the one-line explanation that goes with a
// candidate. It names the tool and the score, and it does not invent a
// reason: a rationale that claims more than the score supports would
// be a lie in the audit trail.
func rationaleFor(query, tool string, score float64) string {
	q := strings.TrimSpace(query)
	if len(q) > 80 {
		q = q[:80] + "…"
	}
	return "BM25(" + tool + ") = " + fmtScore(score) + " para la intención: " + q
}

func fmtScore(s float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmtFloat(s), "0"), ".")
}

func fmtFloat(s float64) string {
	// Four decimals, matching the stored weight.
	return strconv.FormatFloat(s, 'f', 4, 64)
}

// round4 rounds to four decimals so the stored weight is stable and
// comparable.
func round4(s float64) float64 {
	return math.Round(s*10000) / 10000
}
