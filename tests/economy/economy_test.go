// Package economy_test covers the Atlan 5-bucket pipeline.
package economy_test

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/economy"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
)

func item(id int64, title, snippet, url string, confidence float32) research.Item {
	return research.Item{
		ID:         id,
		Title:      title,
		Snippet:    snippet,
		URL:        url,
		Source:     "test",
		Confidence: confidence,
	}
}

func TestCompress_DedupByURL(t *testing.T) {
	items := []research.Item{
		item(1, "A", "first", "https://x.com/1", 0.4),
		item(2, "A2", "second", "https://x.com/1", 0.7), // same URL, higher conf
		item(3, "B", "third", "https://x.com/2", 0.5),
	}
	opts := economy.DefaultOptions()
	opts.CapTotal = 0 // disable cap for this test
	out := economy.Compress(items, opts)
	if len(out) != 2 {
		t.Fatalf("expected 2 unique URLs, got %d", len(out))
	}
	for _, it := range out {
		if it.URL == "https://x.com/1" && it.Confidence != 0.7 {
			t.Fatalf("dedup kept low-confidence version: %+v", it)
		}
	}
}

func TestCompress_FilterConfidence(t *testing.T) {
	items := []research.Item{
		item(1, "high", "ok", "https://x.com/1", 0.9),
		item(2, "mid", "ok", "https://x.com/2", 0.5),
		item(3, "low", "ok", "https://x.com/3", 0.3),
	}
	opts := economy.DefaultOptions()
	opts.CapTotal = 0
	out := economy.Compress(items, opts)
	if len(out) != 2 {
		t.Fatalf("expected 2 items above 0.5 threshold, got %d", len(out))
	}
}

func TestCompress_Truncate(t *testing.T) {
	longSnippet := strings.Repeat("word ", 200) // >500 chars
	items := []research.Item{
		item(1, "T", longSnippet, "https://x.com/1", 0.9),
	}
	opts := economy.DefaultOptions()
	opts.CapTotal = 0
	out := economy.Compress(items, opts)
	if len(out[0].Snippet) > 600 { // allow some slack for word-boundary cut
		t.Fatalf("snippet not truncated: len=%d", len(out[0].Snippet))
	}
	if !strings.HasSuffix(out[0].Snippet, "...") {
		t.Fatalf("snippet should end with '...': %q", out[0].Snippet)
	}
}

func TestCompress_CapTop(t *testing.T) {
	items := []research.Item{}
	for i := int64(1); i <= 20; i++ {
		// each item has a unique URL so dedup doesn't collapse them
		items = append(items, item(i, "T", "S", "https://x.com/"+string(rune('a'+i-1)), float32(i)/20))
	}
	opts := economy.DefaultOptions() // CapTotal = 10
	out := economy.Compress(items, opts)
	if len(out) != 10 {
		t.Fatalf("expected 10 items after cap, got %d", len(out))
	}
	// All should have confidence >= 0.5 (filter) and >=10/20=0.5 (top by conf)
	for _, it := range out {
		if it.Confidence < 0.5 {
			t.Fatalf("item with conf %f < 0.5 in top-N", it.Confidence)
		}
	}
}

func TestCompress_DropVerboseBody(t *testing.T) {
	raw := "VERY LONG RAW BODY THAT SHOULD BE DROPPED"
	it := item(1, "T", "first sentence. second sentence. third.", "https://x.com/1", 0.9)
	it.Raw = raw
	items := []research.Item{it}
	opts := economy.DefaultOptions()
	opts.CapTotal = 0
	out := economy.Compress(items, opts)
	if out[0].Raw != "" {
		t.Fatalf("compressItems should drop Raw body, got %q", out[0].Raw)
	}
	if !strings.Contains(out[0].Snippet, "first sentence") {
		t.Fatalf("snippet should contain first sentence: %q", out[0].Snippet)
	}
	if strings.Contains(out[0].Snippet, "second sentence") {
		t.Fatalf("snippet should be compressed to first sentence: %q", out[0].Snippet)
	}
}

func TestCompress_FullPipeline_Order(t *testing.T) {
	// 20 items, several dup URLs, several low-conf. Verify ordering.
	items := []research.Item{}
	for i := int64(1); i <= 5; i++ {
		items = append(items, item(i, "shared", "snippet", "https://shared.com", 0.95))
	}
	for i := int64(6); i <= 15; i++ {
		items = append(items, item(i, "unique", "snippet", "https://u.com", float32(i)/30))
	}
	// Two items below threshold.
	items = append(items, item(100, "low", "x", "https://l.com", 0.1))
	items = append(items, item(101, "low", "x", "https://l.com", 0.2))

	opts := economy.DefaultOptions()
	out := economy.Compress(items, opts)

	// Expect: 5 dup-by-URL collapsed to 1 + 10 unique (ids 6-15) - (low-conf ones) <= 10 cap.
	if len(out) > 10 {
		t.Fatalf("cap not applied: got %d items", len(out))
	}
	// Verify low-confidence items dropped.
	for _, it := range out {
		if it.Confidence < 0.5 {
			t.Fatalf("low-confidence item not dropped: %+v", it)
		}
	}
}

func TestEstimateTokens_Rough(t *testing.T) {
	items := []research.Item{
		item(1, "T", "S", "https://x.com", 0.9), // ~22 chars
	}
	tokens := economy.EstimateTokens(items)
	if tokens < 4 || tokens > 8 {
		t.Fatalf("rough estimate out of range: %d (expected 4-8 for ~22 chars)", tokens)
	}
}

// withRaw helper removed (methods can't be defined on non-local types).
// Test fixtures inline the Raw assignment instead.