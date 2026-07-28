// Package dual_driver_test: v2.4.2 Store-level kind filter test.
//
// v2.4.2 enriches brand_match with kind=decision + compliance_check
// with kind=decision+finding. The orchestrator-level
// (tests/orchestration/agent_memory_v2_4_2_test.go) tests verify the
// end-to-end LLM prompt contents. This Store-level test verifies the
// structural invariant: the Store's SearchAgentMemory Kind filter
// actually filters — defense in depth in case future refactors
// change the orchestrator helper but break the kind filter at the
// data plane.
//
// Assertions:
//   - SearchAgentMemory(Kind="decision") returns ONLY decision rows.
//   - SearchAgentMemory(Kind="finding") returns ONLY finding rows.
//   - SearchAgentMemory(Kind="") returns ALL rows (any kind).
//   - The same Kind filter composes with AgentID filter (additive,
//     same as v2.4.1 invariant).
package dual_driver_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// v242StoreLevelOrch returns an orch + store backed by a fresh SQLite
// DB with migrations applied + the 'acme' project as active. Mirrors
// the v2.4.1 dual_driver helper.
func v242StoreLevelOrch(t *testing.T) (*orchestration.Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "v242-store.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateProject(context.Background(), &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.SetActiveProject(context.Background(), "acme"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	return orchestration.New(s, nil), s
}

// v242StoreSave seeds an agent_memory row directly via the orchestrator.
func v242StoreSave(t *testing.T, orch *orchestration.Orchestrator, operator, kind, content, agentID string) int64 {
	t.Helper()
	out, err := orch.AgentMemorySave(context.Background(), orchestration.AgentMemorySaveInput{
		Operator: operator,
		Kind:     kind,
		Content:  content,
		AgentID:  agentID,
	})
	if err != nil {
		t.Fatalf("save %s: %v", kind, err)
	}
	return out.Row.ID
}

// TestV242_Store_SearchAgentMemory_FilterByKind_DefenseInDepth is
// the structural verification of the v2.4.2 enrichment's data-plane
// foundation. brand_match uses Kind="decision" and compliance_check
// uses Kind=["decision", "finding"] — both filters MUST exclude
// non-matching rows. If a future refactor accidentally drops the
// kind filter from SearchAgentMemory, this test will catch it.
//
// Assertions:
//
//  1. SearchAgentMemory(Kind="decision") returns ONLY decision rows
//     (no findings, notes, links).
//  2. SearchAgentMemory(Kind="finding") returns ONLY finding rows.
//  3. SearchAgentMemory(Kind="") returns ALL rows (any kind) — same
//     query without filter returns all kinds.
//  4. Kind filter composes with AgentID filter (additive, like v2.4.1).
//     SearchAgentMemory(Kind="decision", AgentID="X") returns ONLY
//     X's decision rows.
//
// This is the structural fix that prevents accidental kind leakage
// if the orchestrator-level helper is refactored in the future.
func TestV242_Store_SearchAgentMemory_FilterByKind_DefenseInDepth(t *testing.T) {
	orch, st := v242StoreLevelOrch(t)
	ctx := context.Background()

	// Seed: 4 rows, one per kind. All authored by claude-sonnet-4.
	// Query string "compliance" matches all of them so the kind
	// filter is the only thing differentiating the results.
	v242StoreSave(t, orch, "alice", "decision",
		"compliance decision: GDPR Article 13 disclosure required", "claude-sonnet-4")
	v242StoreSave(t, orch, "alice", "finding",
		"compliance finding: 2026-Q1 audit flagged missing EU AI Act disclosure", "claude-sonnet-4")
	v242StoreSave(t, orch, "alice", "note",
		"compliance note: project uses pnpm, not yarn", "claude-sonnet-4")
	v242StoreSave(t, orch, "alice", "link",
		"https://compliance.opitacode.com/eu-ai-act-checklist.pdf", "claude-sonnet-4")

	// Sanity: at least 4 rows exist (no filter).
	allHits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "compliance",
		Kind:  "",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("search all (no kind filter): %v", err)
	}
	if len(allHits) < 4 {
		t.Fatalf("expected >= 4 hits with no filter; got %d", len(allHits))
	}

	// Assertion 1: Kind="decision" returns ONLY decision rows.
	decHits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "compliance",
		Kind:  "decision",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("search Kind=decision: %v", err)
	}
	if len(decHits) == 0 {
		t.Fatalf("Kind=decision returned 0 hits; want >= 1 (the seeded decision row)")
	}
	for _, h := range decHits {
		if h.Kind != "decision" {
			t.Errorf("Kind=decision leaked row with Kind=%q", h.Kind)
		}
	}

	// Assertion 2: Kind="finding" returns ONLY finding rows.
	findHits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: "compliance",
		Kind:  "finding",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("search Kind=finding: %v", err)
	}
	if len(findHits) == 0 {
		t.Fatalf("Kind=finding returned 0 hits; want >= 1")
	}
	for _, h := range findHits {
		if h.Kind != "finding" {
			t.Errorf("Kind=finding leaked row with Kind=%q", h.Kind)
		}
	}

	// Assertion 3: empty Kind returns ALL kinds (cross-check).
	if len(allHits) < len(decHits)+len(findHits) {
		t.Errorf("empty-Kind hits (%d) should be >= sum of decision (%d) + finding (%d)",
			len(allHits), len(decHits), len(findHits))
	}

	// Assertion 4: Kind + AgentID compose (additive, like v2.4.1).
	decAgentHits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:   "compliance",
		Kind:    "decision",
		AgentID: "claude-sonnet-4",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("search Kind=decision AgentID=claude-sonnet-4: %v", err)
	}
	if len(decAgentHits) == 0 {
		t.Fatalf("Kind=decision AgentID=claude-sonnet-4 returned 0 hits")
	}
	for _, h := range decAgentHits {
		if h.Kind != "decision" {
			t.Errorf("additive filter leaked row with Kind=%q", h.Kind)
		}
		if h.AgentID != "claude-sonnet-4" {
			t.Errorf("additive filter leaked row with AgentID=%q", h.AgentID)
		}
	}

	// Negative: Kind=decision AgentID="gpt-4o" should return 0 hits
	// (no decisions by gpt-4o in this project).
	decOtherHits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:   "compliance",
		Kind:    "decision",
		AgentID: "gpt-4o",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("search Kind=decision AgentID=gpt-4o: %v", err)
	}
	if len(decOtherHits) != 0 {
		t.Errorf("Kind=decision AgentID=gpt-4o returned %d hits; want 0", len(decOtherHits))
	}
}
