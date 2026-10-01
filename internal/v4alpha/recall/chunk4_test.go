package recall

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// TestC5VideoRecall_WeightsValid verifies the canonical C5 weight literal.
func TestC5VideoRecall_WeightsValid(t *testing.T) {
	c := &C5VideoRecall{}
	w := c.Weights()
	if err := w.Validate(); err != nil {
		t.Errorf("C5 weights must sum to 1.0: %v", err)
	}
	if w.Graph != 0.20 || w.CrossModal != 0.80 {
		t.Errorf("C5 weights = %+v, want Graph=0.20 CrossModal=0.80", w)
	}
}

// TestC5VideoRecall_BasicFTS5 seeds link rows with video references
// and verifies C5 surfaces them via FTS5.
func TestC5VideoRecall_BasicFTS5(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "link", "Risograph halftone animation reference video", "")
	_ = saveRow(t, st, "link", "Watercolor indie animation clip", "")
	_ = id1

	got, err := RecallFor(ctx, db, VibeCaseVideo, "Risograph animation", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("expected at least 1 result, got 0")
	}
	if got[0].ID != id1 {
		t.Errorf("expected row 1 to win, got id=%d content=%q",
			got[0].ID, got[0].Content)
	}
}

// TestC6AudioRecall_WeightsValid verifies the canonical C6 weight literal.
func TestC6AudioRecall_WeightsValid(t *testing.T) {
	c := &C6AudioRecall{}
	w := c.Weights()
	if err := w.Validate(); err != nil {
		t.Errorf("C6 weights must sum to 1.0: %v", err)
	}
	if w.Graph != 0.20 || w.CrossModal != 0.80 {
		t.Errorf("C6 weights = %+v, want Graph=0.20 CrossModal=0.80", w)
	}
}

// TestC6AudioRecall_BasicFTS5 seeds link rows with audio references
// and verifies C6 surfaces them.
func TestC6AudioRecall_BasicFTS5(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "link", "DECAF timbre-only embed paper reference", "")
	_ = saveRow(t, st, "link", "wav2vec 2.0 voice model paper", "")
	_ = id1

	got, err := RecallFor(ctx, db, VibeCaseAudio, "DECAF timbre", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("expected at least 1 result, got 0")
	}
	if got[0].ID != id1 {
		t.Errorf("expected row 1 to win, got id=%d content=%q",
			got[0].ID, got[0].Content)
	}
}

// TestC6AudioRecall_VoiceEmbedKindFilter verifies the optional
// voice_embed_kind filter (alpha.18 in-memory).
func TestC6AudioRecall_VoiceEmbedKindFilter(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "link", "timbre-only audio reference", "")
	id2 := saveRow(t, st, "link", "full audio reference", "")

	// Set voice_embed_kind on each row.
	if _, err := db.ExecContext(ctx, `UPDATE agent_memory SET voice_embed_kind=? WHERE id=?`, "timbre", id1); err != nil {
		t.Fatalf("set timbre: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_memory SET voice_embed_kind=? WHERE id=?`, "full", id2); err != nil {
		t.Fatalf("set full: %v", err)
	}

	c := &C6AudioRecall{VoiceEmbedKind: []string{"timbre"}}
	got, err := c.Recall(ctx, db, "audio reference", "default", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row (timbre filter), got %d", len(got))
	}
	if got[0].ID != id1 {
		t.Errorf("expected timbre row id=%d, got id=%d", id1, got[0].ID)
	}
}

// TestC7MultiRecall_WeightsValid verifies C7 weights (no validation
// required since C7 weights are dynamic per the SPEC).
func TestC7MultiRecall_WeightsValid(t *testing.T) {
	c := &C7MultiRecall{}
	w := c.Weights()
	if err := w.Validate(); err != nil {
		t.Errorf("C7 weights must sum to 1.0: %v", err)
	}
}

// TestC7MultiRecall_SubTaskDispatch verifies C7 dispatches to the
// correct sub-strategies based on query content.
func TestC7MultiRecall_SubTaskDispatch(t *testing.T) {
	cases := []struct {
		query    string
		wantAny  []string
	}{
		{"decided to use Postgres for OLTP", []string{"decision"}},
		{"how does the code function work", []string{"code"}},
		{"research paper on HippoRAG", []string{"research"}},
		{"default query with no keywords", []string{"text"}},
	}
	for _, c := range cases {
		got := detectSubTaskVibes(c.query)
		foundAny := false
		for _, w := range c.wantAny {
			for _, g := range got {
				if g == w {
					foundAny = true
					break
				}
			}
		}
		if !foundAny {
			t.Errorf("detectSubTaskVibes(%q) = %v, want to contain %v", c.query, got, c.wantAny)
		}
	}
}

// TestC7MultiRecall_RRFMergesResults verifies C7 merges results
// from multiple sub-strategies via RRF.
//
// Note: FTS5 default tokenizer (unicode61) does NOT stem — "ship"
// and "shipping" are distinct terms. The test uses matching stems
// ("ship" + "shipping alpha" + "ship alpha") so all rows match the
// query and the merge is observable.
func TestC7MultiRecall_RRFMergesResults(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	// Save one decision and one observation sharing the query stem.
	_ = saveDecision(t, st, "decided to ship Phase 5 alpha.18", "")
	_ = saveRow(t, st, "observation", "ship alpha.18 in November 2026", "")

	c := &C7MultiRecall{SubTaskStrategies: []string{"decision", "text"}}
	got, err := c.Recall(ctx, db, "ship alpha", "default", 10)
	if err != nil {
		t.Fatalf("C7 Recall: %v", err)
	}
	if len(got) < 2 {
		t.Errorf("expected 2+ rows (decision + text merged), got %d: %v", len(got), rowContents(got))
	}
}

// TestC7MultiRecall_HandlesEmptyResults verifies graceful behaviour
// when no sub-task returns results.
func TestC7MultiRecall_HandlesEmptyResults(t *testing.T) {
	db, _, _ := newTestDB(t)
	ctx := context.Background()

	got, err := RecallFor(ctx, db, VibeCaseMulti, "zzzzznonexistent", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 results for non-matching query, got %d", len(got))
	}
}

// TestAllStrategiesRegisteredChunk4 verifies all 7 strategies are
// registered after Chunk 4 ships.
func TestAllStrategiesRegisteredChunk4(t *testing.T) {
	got := RegisteredVibeCases()
	have := make(map[string]bool, len(got))
	for _, v := range got {
		have[v] = true
	}
	for _, v := range []string{
		VibeCaseCode, VibeCaseText, VibeCaseDecision, VibeCaseResearch,
		VibeCaseVideo, VibeCaseAudio, VibeCaseMulti,
	} {
		if !have[v] {
			t.Errorf("vibe_case %q not registered after Chunk 4. Got: %v", v, got)
		}
	}
	if len(got) != 7 {
		t.Errorf("expected 7 registered strategies, got %d: %v", len(got), got)
	}
}

// TestC5VideoRecall_EmptyQuery verifies graceful no-op for empty query.
func TestC5VideoRecall_EmptyQuery(t *testing.T) {
	c := &C5VideoRecall{}
	db, _, _ := newTestDB(t)
	got, err := c.Recall(context.Background(), db, "", "default", 10)
	if err != nil {
		t.Errorf("empty query should be no-op: %v", err)
	}
	if got != nil {
		t.Errorf("empty query should return nil, got %v", got)
	}
}

// TestC6AudioRecall_EmptyQuery same as above for C6.
func TestC6AudioRecall_EmptyQuery(t *testing.T) {
	c := &C6AudioRecall{}
	db, _, _ := newTestDB(t)
	got, err := c.Recall(context.Background(), db, "", "default", 10)
	if err != nil {
		t.Errorf("empty query should be no-op: %v", err)
	}
	if got != nil {
		t.Errorf("empty query should return nil, got %v", got)
	}
}

// Suppress unused import linter when only some helpers are referenced.
var _ = strings.Contains
var _ = agent_memory.Row{}
