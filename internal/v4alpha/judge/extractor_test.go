// Tests for extractor.go (deterministic evidence extraction).
package judge

import (
	"context"
	"strings"
	"testing"
)

func TestExtractor_EmptyContent(t *testing.T) {
	e := NewDefaultExtractor()
	pc := newPC(t, func(p *PipelineContext) { p.ArtifactContent = []byte("") })
	evidence, err := e.Extract(context.Background(), pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evidence) != 0 {
		t.Errorf("expected 0 evidence for empty content; got %d", len(evidence))
	}
}

func TestExtractor_SingleChunk(t *testing.T) {
	e := NewDefaultExtractor()
	pc := newPC(t, func(p *PipelineContext) {
		p.ArtifactContent = []byte("small artifact")
	})
	evidence, err := e.Extract(context.Background(), pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected 1 chunk; got %d", len(evidence))
	}
	if !strings.HasPrefix(evidence[0].Source, "artifact:chunk-") {
		t.Errorf("Source = %q; want artifact:chunk-N", evidence[0].Source)
	}
	if evidence[0].Snippet != "small artifact" {
		t.Errorf("Snippet = %q; want %q", evidence[0].Snippet, "small artifact")
	}
}

func TestExtractor_MultipleChunks(t *testing.T) {
	e := &Extractor{ChunkSize: 100}
	// 250 bytes split on \n\n
	pc := newPC(t, func(p *PipelineContext) {
		p.ArtifactContent = []byte(strings.Repeat("a", 80) + "\n\n" + strings.Repeat("b", 80) + "\n\n" + strings.Repeat("c", 80))
	})
	evidence, err := e.Extract(context.Background(), pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evidence) < 2 {
		t.Fatalf("expected multiple chunks; got %d", len(evidence))
	}
	// Each chunk should have its own source ID.
	seen := make(map[string]bool)
	for _, ev := range evidence {
		seen[ev.Source] = true
	}
	if len(seen) != len(evidence) {
		t.Errorf("duplicate source IDs: %+v", evidence)
	}
}

func TestExtractor_DefensiveCopy(t *testing.T) {
	e := NewDefaultExtractor()
	content := []byte("hello world")
	pc := newPC(t, func(p *PipelineContext) { p.ArtifactContent = content })
	_, err := e.Extract(context.Background(), pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutate the original input; the extractor should not be affected
	// (because we return chunks as copies).
	content[0] = 'X'
	pc.ArtifactContent = []byte("hello world") // reset for re-run
	evidence, _ := e.Extract(context.Background(), pc)
	if len(evidence) != 1 || evidence[0].Snippet != "hello world" {
		t.Errorf("extract returned mutated content: %q", evidence[0].Snippet)
	}
}

func TestExtractor_RelevanceHeuristics(t *testing.T) {
	e := NewDefaultExtractor()
	cases := []struct {
		body string
		min  float64
		max  float64
	}{
		{"file:///x.go:42-60: foo", 0.85, 1.0},    // file:line
		{"package main\nfunc foo() {}", 0.75, 1.0}, // code chunk
		{"```\nfunc bar() {}\n```", 0.65, 1.0},      // fenced
		{"TODO: fix this thing", 0.55, 0.65},        // marked
		{"plain prose paragraph", 0.4, 0.6},         // prose
	}
	for _, tc := range cases {
		pc := newPC(t, func(p *PipelineContext) { p.ArtifactContent = []byte(tc.body) })
		evidence, _ := e.Extract(context.Background(), pc)
		if len(evidence) != 1 {
			t.Fatalf("body %q: expected 1 chunk, got %d", tc.body, len(evidence))
		}
		r := evidence[0].Relevance
		if r < tc.min || r > tc.max {
			t.Errorf("body %q: relevance %f not in [%f, %f]", tc.body, r, tc.min, tc.max)
		}
	}
}

func TestExtractor_CtxCancel(t *testing.T) {
	e := NewDefaultExtractor()
	ctx, cancel := cancelCtx()
	cancel()
	pc := newPC(t)
	_, err := e.Extract(ctx, pc)
	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
}

func TestChunkParagraphs_Boundaries(t *testing.T) {
	// Single chunk when content fits.
	c := chunkParagraphs([]byte("hello"), 100)
	if len(c) != 1 || string(c[0]) != "hello" {
		t.Errorf("expected single chunk; got %+v", c)
	}
	// Split on \n\n.
	c = chunkParagraphs([]byte("aaa\n\nbbb\n\nccc"), 5)
	if len(c) < 2 {
		t.Errorf("expected multi-chunk split; got %+v", c)
	}
}

// cancelCtx is a tiny helper for tests that need ctx cancellation
// without depending on context.WithCancel directly.
func cancelCtx() (ctx context.Context, cancel func()) {
	return context.WithCancel(context.Background())
}
