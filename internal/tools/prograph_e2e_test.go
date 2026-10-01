// Package tools — prograph_e2e_test.go: end-to-end test for the
// dark_memory_prograph_query tool. Uses a real SQLite store
// (Chunk 6.4 pattern), saves rows with entities via SaveAgentMemory
// (ExtractEntities=true), and verifies the BFS hits the right rows.
//
// 4 tests:
//   1. Depth=1 returns seeds + 1-hop entity-overlap neighbors.
//   2. Depth=2 returns seeds + 1-hop + 2-hop.
//   3. Cycle detection: A↔B entities do not infinite loop on a real DB.
//   4. Empty result for a query that matches nothing.
package tools

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/entity"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newPrographE2EStore opens a real SQLite store with project=default
// + active session, ready for SaveAgentMemory + SearchAgentMemory.
func newPrographE2EStore(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "prograph_e2e.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	// Active session is required for SaveAgentMemory's audit row.
	if _, err := st.SaveSession(ctx, store.WriteContext{
		Actor:     "operator-prograph-e2e",
		SessionID: "sess-prograph-e2e",
		WritePath: "TestSetup",
	}, &dummySession); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := st.SetActiveSession(ctx, "default", dummySession.SessionID); err != nil {
		t.Fatalf("SetActiveSession: %v", err)
	}
	return st
}

// dummySession is a stable session used by every prograph e2e test.
var dummySession = sessionForPrograph()

func sessionForPrograph() session.Session {
	return session.Session{
		SessionID: "sess-prograph-e2e",
		Operator:  "operator-prograph-e2e",
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Status:    "open",
	}
}

// saveRowWithEntities populates Entities via entity.Extract + SaveAgentMemory.
func saveRowWithEntities(t *testing.T, st store.Store, title, content, tags string) int64 {
	t.Helper()
	ctx := context.Background()
	ents := entity.Extract(content, title, tags)
	row := &agentmemory.AgentMemory{
		SessionID: dummySession.SessionID,
		Operator:  dummySession.Operator,
		ProjectID: "default",
		Title:     title,
		Content:   content,
		Tags:      tags,
		Kind:      "note",
		Entities:  ents,
	}
	wc := store.WriteContext{
		Actor:     dummySession.Operator,
		SessionID: dummySession.SessionID,
		WritePath: "SaveAgentMemory",
	}
	id, err := st.SaveAgentMemory(ctx, wc, row)
	if err != nil {
		t.Fatalf("SaveAgentMemory(%q): %v", title, err)
	}
	return id
}

func TestPrograph_E2E_DepthOne_EntityOverlap(t *testing.T) {
	st := newPrographE2EStore(t)
	ctx := context.Background()

	// Three rows. Each row's content is a single distinctive noun so
	// entity.Extract produces a clean entity list (no English glue
	// words like "about", "covers", "plus" that aren't in the
	// internal/entity stopword list). Row 1 mentions "widgetcraft" +
	// "frobnicator". Row 2 shares "frobnicator". Row 3 is unrelated.
	id1 := saveRowWithEntities(t, st, "Widgetcraft", "widgetcraft", "widgetcraft,frobnicator")
	id2 := saveRowWithEntities(t, st, "Frobnicator", "frobnicator", "frobnicator,gizmotron")
	id3 := saveRowWithEntities(t, st, "Plumbing",   "plumbing",   "plumbing,drainage")

	// BM25 should surface row 1 (the "alpha" match). ProGraph should
	// expand via the "alpha" + "beta" entities to row 2.
	reg := NewRegistry()
	if err := RegisterPrograph(reg, st); err != nil {
		t.Fatalf("RegisterPrograph: %v", err)
	}
	if tool := reg.Get("prograph_query"); tool == nil {
		t.Fatal("prograph_query not registered")
	}

	// Drive MultiHopRetrieve directly (registry stores closure, not bytes).
	hits, err := recall.MultiHopRetrieve(ctx, st, "widgetcraft", 1, recall.MultiHopOptions{
		SeedLimit: 5, HopLimit: 5, TotalLimit: 20,
	})
	if err != nil {
		t.Fatalf("MultiHopRetrieve: %v", err)
	}
	for _, h := range hits {
		t.Logf("hit: mem=%d depth=%d via=%q score=%v", h.MemID, h.Depth, h.ViaEntity, h.Score)
	}
	gotIDs := make([]int64, 0, len(hits))
	for _, h := range hits {
		gotIDs = append(gotIDs, h.MemID)
	}
	// Expect [id1, id2]; id3 must NOT appear.
	if !containsID(gotIDs, id1) {
		t.Errorf("result missing seed row %d: %v", id1, gotIDs)
	}
	if !containsID(gotIDs, id2) {
		t.Errorf("result missing 1-hop row %d: %v", id2, gotIDs)
	}
	if containsID(gotIDs, id3) {
		t.Errorf("result contains unrelated row %d: %v", id3, gotIDs)
	}
	// Row 1 must be depth=0, row 2 must be depth=1.
	for _, h := range hits {
		if h.MemID == id1 && h.Depth != 0 {
			t.Errorf("seed row %d Depth=%d, want 0", id1, h.Depth)
		}
		if h.MemID == id2 && h.Depth != 1 {
			t.Errorf("1-hop row %d Depth=%d, want 1", id2, h.Depth)
		}
	}
}

func TestPrograph_E2E_DepthTwo_ThreeLayers(t *testing.T) {
	st := newPrographE2EStore(t)
	ctx := context.Background()

	// Chain: row1 → row2 → row3 (each pair shares one entity). Single-
	// noun content + tags-only entity extraction avoids the stopword
	// gap (e.g. "about", "this", "plus" are not in the deterministic
	// stopword list — using single nouns keeps the entity set clean).
	id1 := saveRowWithEntities(t, st, "Widgetcraft", "widgetcraft", "widgetcraft,bridgecraft")
	id2 := saveRowWithEntities(t, st, "Bridgecraft", "bridgecraft", "bridgecraft,canyoncutter")
	id3 := saveRowWithEntities(t, st, "Canyoncutter","canyoncutter","canyoncutter,deltawing")
	idIsolated := saveRowWithEntities(t, st, "Plumbing",   "plumbing",  "plumbing,drainage")

	hits, err := recall.MultiHopRetrieve(ctx, st, "widgetcraft", 2, recall.MultiHopOptions{
		SeedLimit: 5, HopLimit: 5, TotalLimit: 20,
	})
	if err != nil {
		t.Fatalf("MultiHopRetrieve: %v", err)
	}
	gotIDs := make([]int64, 0, len(hits))
	for _, h := range hits {
		gotIDs = append(gotIDs, h.MemID)
	}
	// Expect id1, id2, id3 — NOT idIsolated.
	if !containsID(gotIDs, id1) || !containsID(gotIDs, id2) || !containsID(gotIDs, id3) {
		t.Errorf("result missing chain rows: got=%v want=[%d %d %d]", gotIDs, id1, id2, id3)
	}
	if containsID(gotIDs, idIsolated) {
		t.Errorf("result contains isolated row %d: %v", idIsolated, gotIDs)
	}
	depths := map[int64]int{}
	for _, h := range hits {
		depths[h.MemID] = h.Depth
	}
	if depths[id1] != 0 {
		t.Errorf("seed row %d Depth=%d, want 0", id1, depths[id1])
	}
	if depths[id2] != 1 {
		t.Errorf("1-hop row %d Depth=%d, want 1", id2, depths[id2])
	}
	if depths[id3] != 2 {
		t.Errorf("2-hop row %d Depth=%d, want 2", id3, depths[id3])
	}
}

func TestPrograph_E2E_CycleDoesNotLoop(t *testing.T) {
	st := newPrographE2EStore(t)
	ctx := context.Background()

	// 3-row cycle: all share entity "shared". Single-noun content.
	id1 := saveRowWithEntities(t, st, "Cycle Alpha", "widgetcraft", "widgetcraft,shared")
	id2 := saveRowWithEntities(t, st, "Cycle Beta",  "shared",      "shared,frobnicator")
	id3 := saveRowWithEntities(t, st, "Cycle Gamma", "shared",      "shared,gizmotron")

	hits, err := recall.MultiHopRetrieve(ctx, st, "widgetcraft", 2, recall.MultiHopOptions{
		SeedLimit: 5, HopLimit: 10, TotalLimit: 50,
	})
	if err != nil {
		t.Fatalf("MultiHopRetrieve: %v", err)
	}
	// Count occurrences of each id — must appear at most ONCE.
	counts := map[int64]int{}
	for _, h := range hits {
		counts[h.MemID]++
	}
	for _, id := range []int64{id1, id2, id3} {
		if counts[id] == 0 {
			t.Errorf("row %d missing from result", id)
		}
		if counts[id] > 1 {
			t.Errorf("row %d appeared %d times (cycle not detected on real DB)", id, counts[id])
		}
	}
}

func TestPrograph_E2E_NoMatches_EmptyResult(t *testing.T) {
	st := newPrographE2EStore(t)
	ctx := context.Background()

	// Save one row; query is unrelated.
	_ = saveRowWithEntities(t, st, "Plumbing", "plumbing", "plumbing,drainage")

	hits, err := recall.MultiHopRetrieve(ctx, st, "completelyunrelatedwidgetcraft", 1, recall.MultiHopOptions{
		SeedLimit: 5, HopLimit: 5, TotalLimit: 20,
	})
	if err != nil {
		t.Fatalf("MultiHopRetrieve: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected empty result for unrelated query, got %d hits: %+v", len(hits), hits)
	}
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// --- hygiene checks -------------------------------------------------------

// TestPrograph_E2E_EntityFiltering_AndSemantics verifies the
// underlying v3 applyEntityFilter still does AND semantics on
// SearchAgentMemory (the BM25 path). This is the regression check
// for ProGraph (which uses OR via the new ListAgentMemoryByAnyEntity
// method). If this test ever fails, someone has confused the two.
func TestPrograph_E2E_EntityFiltering_AndSemantics(t *testing.T) {
	st := newPrographE2EStore(t)
	ctx := context.Background()

// Two rows. Row A mentions "widgetcraft" only. Row B mentions
// "frobnicator" only. Filtering for BOTH should return NEITHER
// (AND semantics — no row has both). ProGraph would use OR and
// return BOTH; SearchAgentMemory.Entities uses AND. Need a Query
// for the BM25 path (the AND filter is applied post-rank); the
// query is non-restrictive (matches both rows' content).
	_ = saveRowWithEntities(t, st, "Row Widgetcraft", "widgetcraft", "widgetcraft")
	_ = saveRowWithEntities(t, st, "Row Frobnicator", "frobnicator", "frobnicator")

	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:    "widgetcraft frobnicator",
		Entities: []string{"widgetcraft", "frobnicator"},
		Mode:     "bm25",
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("SearchAgentMemory: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("AND filter returned %d hits, want 0 (no row has BOTH alpha and beta)", len(hits))
	}
}