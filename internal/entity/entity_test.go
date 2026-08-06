package entity

import (
	"reflect"
	"testing"
)

// TestEntity_Extract_Empty verifies that empty inputs return nil
// (no error, no panic).
func TestEntity_Extract_Empty(t *testing.T) {
	if got := Extract("", "", ""); got != nil {
		t.Errorf("Extract all-empty: got %d entities, want nil", len(got))
	}
	if got := Extract("  \t\n ", "", ""); got != nil {
		t.Errorf("Extract whitespace: got %d entities, want nil", len(got))
	}
}

// TestEntity_Extract_Basic asserts the deterministic property:
// same input → same output, sorted by frequency DESC then value ASC.
func TestEntity_Extract_Basic(t *testing.T) {
	got := Extract("dark memory is a dark memory tool", "Dark Memory", "memory,tool")
	if len(got) == 0 {
		t.Fatalf("got no entities")
	}
	// Order: "memory" (count 4: content x2 + title x1 + tag x1),
	//        "dark" (count 3: content x2 + title x1),
	//        "tool" (count 2: content x1 + tag x1).
	wantValues := []string{"memory", "dark", "tool"}
	gotValues := make([]string, len(got))
	for i, e := range got {
		gotValues[i] = e.Value
	}
	if !reflect.DeepEqual(gotValues, wantValues) {
		t.Errorf("order: got %v, want %v", gotValues, wantValues)
	}
	// All entities carry the deterministic source tag.
	for _, e := range got {
		if e.Source != SourceDeterministic {
			t.Errorf("source: got %q, want %q", e.Source, SourceDeterministic)
		}
		if e.Confidence != 1.0 {
			t.Errorf("confidence: got %f, want 1.0", e.Confidence)
		}
	}
}

// TestEntity_Extract_DedupCaseFold asserts that "Dark" and
// "dark" collapse to the same canonical token (case-folded +
// frequency-summed).
func TestEntity_Extract_DedupCaseFold(t *testing.T) {
	got := Extract("Dark DARK dark DaRk", "", "")
	if len(got) != 1 {
		t.Errorf("dedup: got %d entities, want 1", len(got))
	}
	if len(got) > 0 && got[0].Value != "dark" {
		t.Errorf("dedup value: got %q, want dark", got[0].Value)
	}
}

// TestEntity_Extract_Stopwords asserts the canonical English
// stopword list drops expected noise tokens.
func TestEntity_Extract_Stopwords(t *testing.T) {
	got := Extract("the dark memory of the system is on the bus", "", "")
	for _, e := range got {
		switch e.Value {
		case "the", "of", "is", "on":
			t.Errorf("stopword leaked: %q", e.Value)
		}
	}
	// "dark", "memory", "system", "bus" should survive.
	want := map[string]bool{"dark": true, "memory": true, "system": true, "bus": true}
	for _, e := range got {
		delete(want, e.Value)
	}
	if len(want) > 0 {
		t.Errorf("missing entities: %v (got %v)", want, got)
	}
}

// TestEntity_Extract_MinLen asserts tokens shorter than 3 chars
// are dropped (catches "v1", "id", "db").
func TestEntity_Extract_MinLen(t *testing.T) {
	got := Extract("dark memory v1 id db ok", "", "")
	for _, e := range got {
		if len(e.Value) < 3 {
			t.Errorf("short token leaked: %q", e.Value)
		}
	}
}

// TestEntity_Extract_Limit asserts maxEntities caps the result.
func TestEntity_Extract_Limit(t *testing.T) {
	got := ExtractWithLimit(
		"alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu",
		"", "", 3)
	if len(got) != 3 {
		t.Errorf("limit: got %d, want 3", len(got))
	}
	// 0 → default (20).
	got0 := ExtractWithLimit("alpha beta gamma", "", "", 0)
	if len(got0) != 3 {
		t.Errorf("limit=0 default 20: got %d, want 3 (the input has 3 tokens)", len(got0))
	}
}

// TestEntity_Extract_TagsAsTokens asserts that comma-separated
// tags split on the comma and tokenize like plain text. Hyphens
// are token separators (PR-3 minimum), so "vector-search" yields
// TWO entities ("vector", "search"); operators combine them via
// the search filter's AND semantics.
func TestEntity_Extract_TagsAsTokens(t *testing.T) {
	got := Extract("dark memory MCP server", "", "openai, vector-search, vibe-coder")
	want := map[string]bool{
		"dark": true, "memory": true, "mcp": true, "server": true,
		"openai": true, "vector": true, "search": true, "vibe": true, "coder": true,
	}
	for _, e := range got {
		delete(want, e.Value)
	}
	if len(want) > 0 {
		t.Errorf("missing entities: %v (got %v)", want, got)
	}
}

// TestEntity_Extract_DeterministicAcrossRuns asserts the same
// input produces byte-equal output across repeated calls (the
// property row 160 PR-3 promises).
func TestEntity_Extract_DeterministicAcrossRuns(t *testing.T) {
	in := "the dark memory project of the operator is on the disk and the dark memory is dark"
	a := Extract(in, "Dark Memory", "memory,project")
	b := Extract(in, "Dark Memory", "memory,project")
	if !reflect.DeepEqual(a, b) {
		t.Errorf("non-deterministic across runs: %v != %v", a, b)
	}
}

// TestEntity_Extract_PunctuationSplit asserts that ASCII
// punctuation + symbols act as token separators.
func TestEntity_Extract_PunctuationSplit(t *testing.T) {
	got := Extract("openai/vector: search.v1 — it's live!", "", "")
	want := map[string]bool{"openai": true, "vector": true, "search": true, "live": true}
	for _, e := range got {
		delete(want, e.Value)
	}
	if len(want) > 0 {
		t.Errorf("punctuation split missing: %v (got %v)", want, got)
	}
}

// TestEntity_Extract_OnlyTitle asserts that Extract with only a
// title produces entities from the title alone (kills mutations
// that flip the title != "" condition at line 33).
func TestEntity_Extract_OnlyTitle(t *testing.T) {
	got := Extract("", "Dark Memory MCP", "")
	if len(got) == 0 {
		t.Fatalf("title-only: got 0 entities, want > 0")
	}
	values := make(map[string]bool)
	for _, e := range got {
		values[e.Value] = true
	}
	for _, want := range []string{"dark", "memory", "mcp"} {
		if !values[want] {
			t.Errorf("title-only: missing %q (got %v)", want, got)
		}
	}
}

// TestEntity_Extract_OnlyTags asserts that Extract with only tags
// produces entities from the tags alone (kills mutations that flip
// the tags != "" condition at line 36).
func TestEntity_Extract_OnlyTags(t *testing.T) {
	got := Extract("", "", "dark, memory, mcp")
	if len(got) == 0 {
		t.Fatalf("tags-only: got 0 entities, want > 0")
	}
	values := make(map[string]bool)
	for _, e := range got {
		values[e.Value] = true
	}
	for _, want := range []string{"dark", "memory", "mcp"} {
		if !values[want] {
			t.Errorf("tags-only: missing %q (got %v)", want, got)
		}
	}
}

// TestEntity_Extract_NoTitleNoTags asserts that Extract with only
// content works correctly (title="" and tags="" independently
// from non-empty content). Kills mutations that make the
// title/tags branches unconditional.
func TestEntity_Extract_NoTitleNoTags(t *testing.T) {
	got := Extract("alpha beta gamma", "", "")
	if len(got) != 3 {
		t.Fatalf("content-only: got %d entities, want 3", len(got))
	}
}

// TestEntity_ExtractWithLimit_MaxEntitiesEdgeCases asserts the
// maxEntities boundary: <=0 defaults to 20, exactly 1 returns 1,
// and a value larger than the token count returns all tokens.
func TestEntity_ExtractWithLimit_MaxEntitiesEdgeCases(t *testing.T) {
	// maxEntities <= 0 → default 20 (kills mutation at line 27-30
	// that flips <= to > or removes the default assignment).
	got := ExtractWithLimit("alpha beta gamma delta epsilon", "", "", -1)
	if len(got) != 5 {
		t.Errorf("maxEntities=-1: got %d, want 5 (default 20, not truncated)", len(got))
	}
	got0 := ExtractWithLimit("alpha beta gamma", "", "", 0)
	if len(got0) != 3 {
		t.Errorf("maxEntities=0: got %d, want 3", len(got0))
	}
	// maxEntities=1 caps at 1.
	got1 := ExtractWithLimit("alpha beta gamma", "", "", 1)
	if len(got1) != 1 {
		t.Errorf("maxEntities=1: got %d, want 1", len(got1))
	}
	// maxEntities=100 with only 3 tokens returns all 3.
	got100 := ExtractWithLimit("alpha beta gamma", "", "", 100)
	if len(got100) != 3 {
		t.Errorf("maxEntities=100: got %d, want 3", len(got100))
	}
}

// TestEntity_Extract_ConfidenceAlwaysOne asserts the PR-3 contract:
// every entity carries Confidence=1.0 and Source="deterministic".
// Kills mutations that reorder struct field assignments or change
// the Confidence constant.
func TestEntity_Extract_ConfidenceAlwaysOne(t *testing.T) {
	got := Extract("any text here to generate entities test", "", "")
	for i, e := range got {
		if e.Confidence != 1.0 {
			t.Errorf("entity[%d].Confidence = %f, want 1.0", i, e.Confidence)
		}
		if e.Source != SourceDeterministic {
			t.Errorf("entity[%d].Source = %q, want %q", i, e.Source, SourceDeterministic)
		}
	}
}

// TestEntity_Extract_TiesSortedAlphabetically asserts the sort
// contract: when two tokens have the same frequency, they sort
// alphabetically (ASC). Kills mutations that flip < to > in the
// sort comparator.
func TestEntity_Extract_TiesSortedAlphabetically(t *testing.T) {
	// "delta" and "alpha" both appear once → alpha < delta
	got := Extract("delta alpha", "", "")
	if len(got) != 2 {
		t.Fatalf("want 2 entities, got %d", len(got))
	}
	if got[0].Value != "alpha" {
		t.Errorf("tie sort: [0] = %q, want alpha (alphabetical asc)", got[0].Value)
	}
	if got[1].Value != "delta" {
		t.Errorf("tie sort: [1] = %q, want delta", got[1].Value)
	}
}

// TestEntity_Extract_SortDeterministic10Elements asserts the sort
// comparator is actually in effect, not just lucky map iteration.
// With 10 tokens all at frequency=1, the probability of random
// order being alphabetical is 1/10! ≈ 0.0000003. If the sort is
// removed (mutant 18), this test will fail with near-certainty.
func TestEntity_Extract_SortDeterministic10Elements(t *testing.T) {
	// 10 distinct tokens, each appearing once → all freq=1.
	// Sort must produce alphabetical order: alpha, beta, delta,
	// epsilon, gamma, iota, kappa, lambda, theta, zeta.
	input := "zeta kappa iota theta epsilon gamma lambda alpha delta beta"
	got := Extract(input, "", "")
	if len(got) != 10 {
		t.Fatalf("want 10 entities, got %d", len(got))
	}
	want := []string{
		"alpha", "beta", "delta", "epsilon", "gamma",
		"iota", "kappa", "lambda", "theta", "zeta",
	}
	for i, w := range want {
		if got[i].Value != w {
			t.Errorf("position %d: got %q, want %q (sort broken or map iteration lucky)", i, got[i].Value, w)
		}
	}
}

// TestEntity_Extract_SpacesInTitleHandled asserts that a title
// with leading/trailing whitespace is trimmed and joined correctly
// with content (kills mutation at line 34 that removes the join
// or changes the space separator).
func TestEntity_Extract_SpacesInTitleHandled(t *testing.T) {
	got := Extract("hello world", "  extra title  ", "")
	values := make(map[string]bool)
	for _, e := range got {
		values[e.Value] = true
	}
	if !values["extra"] || !values["title"] {
		t.Errorf("title tokens missing: got %v", got)
	}
}
