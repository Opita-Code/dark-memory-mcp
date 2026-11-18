package plan

import (
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// The ranker is pure: same corpus + same query = same shortlist. The
// weights end up in an audit trail, so a ranker that reorders itself
// between runs would make that trail a lie.

func corpus() []Document {
	return []Document{
		{Tool: "vibe_publish", Doc: "Publish a new spec with structured task validation and drift judging"},
		{Tool: "agent_memory_save", Doc: "Save a memory row for the agent: kind, content, title and tags"},
		{Tool: "agent_memory_recall", Doc: "Recall memories by BM25 full-text search over content, title and tags"},
		{Tool: "judge", Doc: "Run the drift judge over an artifact and get a verdict with confidence"},
		{Tool: "health_ping", Doc: "Return the runtime health snapshot: version, schema, uptime and tool count"},
		{Tool: "session_start", Doc: "Start a session in a project, binding the identity for every later call"},
	}
}

func TestRanker_PicksTheRelevantTools(t *testing.T) {
	r := NewRanker(corpus())
	got := r.Rank("save a memory row with tags", 3)
	if len(got) == 0 {
		t.Fatal("Rank returned no candidates for a query that clearly matches")
	}
	if got[0].Tool != "agent_memory_save" {
		t.Errorf("top candidate = %q, want agent_memory_save", got[0].Tool)
	}
	for _, c := range got {
		if c.ScoreSource != vibeflow.ScoreSourceBM25 {
			t.Errorf("candidate %q: ScoreSource = %q, want %q", c.Tool, c.ScoreSource, vibeflow.ScoreSourceBM25)
		}
		if c.Weight <= 0 {
			t.Errorf("candidate %q: Weight = %v, want > 0", c.Tool, c.Weight)
		}
		if c.Rationale == "" {
			t.Errorf("candidate %q: Rationale is empty", c.Tool)
		}
	}
}

func TestRanker_ExcludesNonMatchingDocuments(t *testing.T) {
	r := NewRanker(corpus())
	got := r.Rank("save a memory row with tags", 10)
	for _, c := range got {
		if c.Tool == "health_ping" {
			t.Error("health_ping matched 'save a memory row with tags': a document that matches nothing is not a candidate")
		}
	}
}

func TestRanker_IsDeterministic(t *testing.T) {
	docs := corpus()
	a := NewRanker(docs).Rank("publish a spec and judge it", 5)
	b := NewRanker(docs).Rank("publish a spec and judge it", 5)
	if len(a) != len(b) {
		t.Fatalf("two runs returned %d and %d candidates", len(a), len(b))
	}
	for i := range a {
		if a[i].Tool != b[i].Tool {
			t.Errorf("position %d: %q vs %q: the shortlist reordered between runs", i, a[i].Tool, b[i].Tool)
		}
		if a[i].Weight != b[i].Weight {
			t.Errorf("position %d (%s): weight %v vs %v", i, a[i].Tool, a[i].Weight, b[i].Weight)
		}
	}
}

// Ties must be broken deterministically, by tool name.
func TestRanker_TiesBreakByName(t *testing.T) {
	// Two documents with IDENTICAL text: identical scores.
	docs := []Document{
		{Tool: "zebra_tool", Doc: "identical documentation text"},
		{Tool: "alpha_tool", Doc: "identical documentation text"},
	}
	got := NewRanker(docs).Rank("identical documentation", 2)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Tool != "alpha_tool" || got[1].Tool != "zebra_tool" {
		t.Errorf("tie order = %q, %q; want alpha_tool, zebra_tool (name order)", got[0].Tool, got[1].Tool)
	}
}

func TestRanker_EmptyQueryReturnsNothing(t *testing.T) {
	r := NewRanker(corpus())
	if got := r.Rank("", 5); len(got) != 0 {
		t.Errorf("Rank(\"\") returned %d candidates, want 0", len(got))
	}
	if got := r.Rank("   ", 5); len(got) != 0 {
		t.Errorf("Rank(whitespace) returned %d candidates, want 0", len(got))
	}
}

// A query made only of stopwords tokenises to nothing, so there is
// nothing to score against.
func TestRanker_StopwordOnlyQueryReturnsNothing(t *testing.T) {
	r := NewRanker(corpus())
	if got := r.Rank("the and of", 5); len(got) != 0 {
		t.Errorf("Rank(stopwords only) returned %d candidates, want 0", len(got))
	}
}

func TestRanker_EmptyCorpusIsSafe(t *testing.T) {
	r := NewRanker(nil)
	if got := r.Rank("anything", 5); len(got) != 0 {
		t.Errorf("Rank on an empty corpus returned %d candidates, want 0", len(got))
	}
	if got := r.Score("anything", 0); got != 0 {
		t.Errorf("Score on an empty corpus = %v, want 0", got)
	}
}

func TestRanker_RespectsTheLimit(t *testing.T) {
	r := NewRanker(corpus())
	got := r.Rank("memory spec judge session health", 2)
	if len(got) > 2 {
		t.Errorf("Rank with k=2 returned %d candidates", len(got))
	}
}

// A repeated query term must not inflate a document's score: the
// same term twice in a query is one piece of evidence, not two.
func TestRanker_RepeatedQueryTermDoesNotDoubleCount(t *testing.T) {
	docs := []Document{
		{Tool: "one", Doc: "memory save"},
		{Tool: "two", Doc: "memory recall"},
	}
	r := NewRanker(docs)
	s1 := r.Score("memory", 0)
	s2 := r.Score("memory memory memory", 0)
	if s1 != s2 {
		t.Errorf("score with a repeated term = %v, want %v (the same as the single-term query)", s2, s1)
	}
}

// The full doc is the corpus: a term that appears in the SCHEMA part
// of the doc must count. That is the measured configuration (the full
// doc of the tool was the best one).
func TestRanker_MatchesTermsAnywhereInTheDoc(t *testing.T) {
	docs := []Document{
		{Tool: "with_schema", Doc: "name\ndescription\ninput_schema: {\"properties\": {\"spec_id\": {}}}"},
		{Tool: "without_schema", Doc: "name\ndescription\nno schema terms here"},
	}
	got := NewRanker(docs).Rank("spec_id", 5)
	if len(got) == 0 || got[0].Tool != "with_schema" {
		t.Errorf("got %+v, want with_schema first", got)
	}
}

func TestRanker_WeightIsRoundedToFourDecimals(t *testing.T) {
	r := NewRanker(corpus())
	got := r.Rank("save a memory row with tags", 1)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	w := got[0].Weight
	if w != round4(w) {
		t.Errorf("Weight = %v, which is not rounded to 4 decimals", w)
	}
}
