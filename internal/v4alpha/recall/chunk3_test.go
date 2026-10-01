package recall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// TestC1CodeRecall_WeightsValid verifies the canonical C1 weight literal.
func TestC1CodeRecall_WeightsValid(t *testing.T) {
	c := &C1CodeRecall{}
	w := c.Weights()
	if err := w.Validate(); err != nil {
		t.Errorf("C1 weights must sum to 1.0: %v", err)
	}
	if w.FTS5 != 0.55 || w.Graph != 0.45 {
		t.Errorf("C1 weights = %+v, want FTS5=0.55 Graph=0.45", w)
	}
}

// TestC1CodeRecall_BasicFTS5 seeds finding rows with code-relevant
// content and verifies the strategy surfaces them.
func TestC1CodeRecall_BasicFTS5(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "finding", "agent_memory.Store.Save handles FTS5 sync", "")
	id2 := saveRow(t, st, "finding", "agent_memory.Store.Recall returns topK", "")
	id3 := saveRow(t, st, "context", "Project structure: cmd/, internal/, docs/", "")
	_ = id3

	got, err := RecallFor(ctx, db, VibeCaseCode, "Save FTS5", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("expected at least 1 result, got 0")
	}
	if got[0].ID != id1 {
		t.Errorf("expected row 1 (Save FTS5) to rank first, got id=%d content=%q",
			got[0].ID, got[0].Content)
	}
	_ = id2
}

// TestC1CodeRecall_KindFilter verifies that kind='note' (not in the
// default whitelist) is NOT surfaced by C1.
func TestC1CodeRecall_KindFilter(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	saveRow(t, st, "note", "implementation note about Save method", "")

	got, err := RecallFor(ctx, db, VibeCaseCode, "Save method", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 rows (note kind is filtered), got %d", len(got))
	}
}

// TestC1CodeRecall_GraphExpansion seeds two findings sharing ADR-12.
// C1 should surface both via the 1-hop graph expansion.
func TestC1CodeRecall_GraphExpansion(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "finding", "Save method uses transactional tx", "")
	id2 := saveRow(t, st, "finding", "tx type wraps store.WithTx", "")
	id3 := saveRow(t, st, "finding", "with tx wraps tx context", "")
	setAdrRefs(t, db, id1, "ADR-12")
	setAdrRefs(t, db, id2, "ADR-12")
	setAdrRefs(t, db, id3, "ADR-13")

	got, err := RecallFor(ctx, db, VibeCaseCode, "transactional", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	// Row 1 (FTS5 match) + Row 2 (1-hop graph) should both appear.
	found := make(map[int64]bool)
	for _, r := range got {
		found[r.ID] = true
	}
	if !found[id1] {
		t.Errorf("expected row 1 (FTS5 seed) in result, got %d rows", len(got))
	}
	if !found[id2] {
		t.Errorf("expected row 2 (1-hop graph) in result, got %d rows", len(got))
	}
}

// TestC1CodeRecall_Tokenizer verifies the camelCase/snake_case splitter
// splits "DarkMemoryV4" into multiple FTS5 terms.
func TestC1CodeRecall_Tokenizer(t *testing.T) {
	tokens := tokenizeCodeQuery("DarkMemoryV4_saveAgent")
	want := []string{"Dark", "Memory", "V4", "save", "Agent"}
	if len(tokens) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(tokens), len(want), tokens)
	}
	for i, w := range want {
		if tokens[i] != w {
			t.Errorf("token[%d] = %q, want %q", i, tokens[i], w)
		}
	}
}

// TestC2TextRecall_WeightsValid verifies the canonical C2 weight literal.
func TestC2TextRecall_WeightsValid(t *testing.T) {
	c := &C2TextRecall{}
	w := c.Weights()
	if err := w.Validate(); err != nil {
		t.Errorf("C2 weights must sum to 1.0: %v", err)
	}
	if w.FTS5 != 0.40 || w.Vector != 0.50 || w.Graph != 0.10 {
		t.Errorf("C2 weights = %+v, want FTS5=0.40 Vector=0.50 Graph=0.10", w)
	}
}

// TestC2TextRecall_BasicFTS5 seeds observation rows and verifies C2
// surfaces them via FTS5.
func TestC2TextRecall_BasicFTS5(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "observation", "Operators prefer context-rich answers", "")
	id2 := saveRow(t, st, "observation", "Decisions should carry rationale", "")
	_ = id2

	got, err := RecallFor(ctx, db, VibeCaseText, "operators prefer context", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("expected at least 1 result, got 0")
	}
	if got[0].ID != id1 {
		t.Errorf("expected row 1 to win, got id=%d", got[0].ID)
	}
}

// TestC2TextRecall_SynonymExpansion verifies operator-curated synonyms
// are OR'd into the FTS5 query.
func TestC2TextRecall_SynonymExpansion(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	// Save a row containing only the synonym "decision", not "choose".
	_ = saveRow(t, st, "observation", "we made a decision about Phase 5 scope", "")

	c := &C2TextRecall{Synonyms: map[string][]string{
		"choose": {"decision", "selection"},
	}}
	got, err := c.Recall(ctx, db, "choose", "default", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) < 1 {
		t.Errorf("synonym expansion failed: query 'choose' should match 'decision' rows, got 0")
	}
}

// TestC4ResearchRecall_WeightsValid verifies the canonical C4 weight literal.
func TestC4ResearchRecall_WeightsValid(t *testing.T) {
	c := &C4ResearchRecall{}
	w := c.Weights()
	if err := w.Validate(); err != nil {
		t.Errorf("C4 weights must sum to 1.0: %v", err)
	}
	if w.FTS5 != 0.25 || w.Vector != 0.45 || w.Graph != 0.30 {
		t.Errorf("C4 weights = %+v, want FTS5=0.25 Vector=0.45 Graph=0.30", w)
	}
}

// TestC4ResearchRecall_BasicFTS5 seeds finding rows and verifies C4
// surfaces them via FTS5 with research-aware expansion.
func TestC4ResearchRecall_BasicFTS5(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "finding", "MOOSEDev paper on decision-context retrieval", "")
	id2 := saveRow(t, st, "finding", "HippoRAG paper on graph retrieval", "")
	_ = id2

	got, err := RecallFor(ctx, db, VibeCaseResearch, "MOOSEDev decision", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("expected at least 1 result, got 0")
	}
	if got[0].ID != id1 {
		t.Errorf("expected row 1 to win, got id=%d", got[0].ID)
	}
}

// TestC4ResearchRecall_2HopGraphExpansion seeds three findings chained
// via ADR refs (A→B→C). The 2-hop expansion should surface B from A
// and C from B.
func TestC4ResearchRecall_2HopGraphExpansion(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveRow(t, st, "finding", "HippoRAG seed paper on graph retrieval", "")
	id2 := saveRow(t, st, "finding", "HippoRAG follow-up paper on PPR", "")
	id3 := saveRow(t, st, "finding", "HippoRAG evaluation paper", "")
	setAdrRefs(t, db, id1, "ADR-100")
	setAdrRefs(t, db, id2, "ADR-100,ADR-101")
	setAdrRefs(t, db, id3, "ADR-101")

	got, err := RecallFor(ctx, db, VibeCaseResearch, "HippoRAG", "default", 10)
	if err != nil {
		t.Fatalf("RecallFor: %v", err)
	}
	found := make(map[int64]bool)
	for _, r := range got {
		found[r.ID] = true
	}
	// id1 is the FTS5 seed; id2 is 1-hop via ADR-100; id3 is 2-hop via ADR-101.
	if !found[id1] {
		t.Errorf("expected id1 (seed) in result")
	}
	if !found[id2] {
		t.Errorf("expected id2 (1-hop) in result")
	}
	if !found[id3] {
		t.Errorf("expected id3 (2-hop) in result")
	}
}

// TestAllStrategiesRegistered verifies init() registered C1, C2, C3, C4
// at package load time. C5/C6/C7 ship in Chunk 4.
func TestAllStrategiesRegistered(t *testing.T) {
	got := RegisteredVibeCases()
	want := map[string]bool{
		VibeCaseCode: false, VibeCaseText: false, VibeCaseDecision: false, VibeCaseResearch: false,
	}
	for _, v := range got {
		if _, ok := want[v]; ok {
			want[v] = true
		}
	}
	for k, registered := range want {
		if !registered {
			t.Errorf("vibe_case %q not registered. Got: %v", k, got)
		}
	}
}

// TestRecallFor_NoStrategyForRemaining verifies C5/C6/C7 still return
// ErrNoStrategyRegistered after Chunk 3 ships (they come in Chunk 4).
func TestRecallFor_NoStrategyForRemaining(t *testing.T) {
	db, _, _ := newTestDB(t)
	for _, vc := range []string{VibeCaseVideo, VibeCaseAudio, VibeCaseMulti} {
		_, err := RecallFor(context.Background(), db, vc, "anything", "default", 10)
		if !errors.Is(err, ErrNoStrategyRegistered) {
			t.Errorf("vibe_case=%q: expected ErrNoStrategyRegistered, got %v", vc, err)
		}
	}
}

// saveRow inserts a row of any kind (not just decision) for tests
// that need to exercise C1/C2/C4 kind whitelists.
func saveRow(t *testing.T, st *agent_memory.Store, kind, content, title string) int64 {
	t.Helper()
	id, err := st.Save(context.Background(), testAudit(), "nico", kind, title,
		content, "phase5,chunk3", false)
	if err != nil {
		t.Fatalf("Save(%s): %v", kind, err)
	}
	if title == "" {
		title = "auto-" + kind
	}
	// Ensure title is set (Save with empty title stores NULL).
	if _, err := st.Get(context.Background(), id); err != nil {
		t.Fatalf("Get after Save: %v", err)
	}
	// Title is irrelevant for tests; just need the content to be saved.
	_ = strings.Contains
	return id
}
