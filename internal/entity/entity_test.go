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
