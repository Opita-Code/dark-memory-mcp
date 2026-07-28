// Package dual_driver - agent_memory_v2_3_0_test.go: regression tests
// for the v2.3.0 save-decouple + INV-10 + agent_memory_recall work.
//
// Four tests cover the v2.3.0 wire-contract deltas:
//
//   - TestListAgentMemory_ScopeOperator_UsesCallerOperatorNotAuditActor
//     Pre-v2.3.0 scope=operator resolved via SELECT actor FROM
//     write_audit ORDER BY id DESC LIMIT 1, which returned the most
//     recent audit actor (often the session_sweeper), NOT the
//     operator who actually saved. v2.3.0 uses the explicit Operator
//     field on AgentMemoryListFilters.
//
//   - TestSaveAgentMemory_NoAutoBind_PersistsAcrossSessionClose
//     Pre-v2.3.0 the Store auto-bound session_id from the active
//     session. Closing the session made rows invisible via
//     scope=session. v2.3.0 honors INV-10: rows survive session
//     close.
//
//   - TestListAgentMemory_ScopeAgent_FiltersByAgentID
//     The new scope=agent uses row.agent_id == filter.AgentID
//     (Mem0 agent_id semantics).
//
//   - TestAgentMemory_Save_AcceptsAgentIDAndMemoryType +
//     TestAgentMemory_Recall_BasicSmoke +
//     TestAgentMemory_Update_AcceptsMemoryType
//     Round-trip the two new columns through Save/Get/Update +
//     Search and assert the FTS5 path picks up the filters.
package dual_driver

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
)

// TestListAgentMemory_ScopeOperator_UsesCallerOperatorNotAuditActor
// is the regression test for the v2.3.0 INV-10 fix.
//
// Alice and Bob each save some rows. The pre-v2.3.0 scope=operator
// path resolved operator via "latest write_audit.actor", which would
// have returned Bob's actor (Bob's row was saved last) when the
// caller asked for scope=operator=alice. v2.3.0 uses the explicit
// Operator field on the filter; this test asserts that
// scope=operator=alice returns alice's rows and scope=operator=bob
// returns bob's rows, with no cross-contamination.
func TestListAgentMemory_ScopeOperator_UsesCallerOperatorNotAuditActor(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	// Alice saves three notes.
	for _, content := range []string{"alice alpha", "alice beta", "alice gamma"} {
		row := makeRow("alice", "note", content)
		if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test_op"), row); err != nil {
			t.Fatalf("alice save %q: %v", content, err)
		}
	}
	// Bob saves one note LAST. Pre-v2.3.0 this would have made
	// scope=operator=alice return 0 rows because the audit lookup
	// would have returned bob's actor.
	if _, err := st.SaveAgentMemory(ctx, wcFor("bob", "test_op"),
		makeRow("bob", "note", "bob only")); err != nil {
		t.Fatalf("bob save: %v", err)
	}

	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:    agentmemory.ScopeOperator,
		Operator: "alice",
	})
	if err != nil {
		t.Fatalf("list scope=operator alice: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("scope=operator alice: got %d rows, want 3 (regression: pre-v2.3.0 returned 0 because bob was the latest audit actor)", len(rows))
		for i, r := range rows {
			t.Logf("  row[%d] id=%d operator=%q", i, r.ID, r.Operator)
		}
	}
	for _, r := range rows {
		if r.Operator != "alice" {
			t.Errorf("scope=operator alice returned operator=%q row", r.Operator)
		}
	}

	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:    agentmemory.ScopeOperator,
		Operator: "bob",
	})
	if err != nil {
		t.Fatalf("list scope=operator bob: %v", err)
	}
	if len(rows) != 1 || rows[0].Operator != "bob" {
		t.Errorf("scope=operator bob: got %d rows, want 1 from bob", len(rows))
	}

	// Empty Operator on scope=operator must fail-safe.
	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:    agentmemory.ScopeOperator,
		Operator: "",
	})
	if err != nil {
		t.Fatalf("list scope=operator empty: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("scope=operator empty Operator: got %d rows, want 0 (fail-safe)", len(rows))
	}
}

// TestSaveAgentMemory_NoAutoBind_PersistsAcrossSessionClose is the
// INV-10 save-decouple regression test. Set an active session,
// save without bind_session, close the session, assert the row is
// still visible via scope=project.
func TestSaveAgentMemory_NoAutoBind_PersistsAcrossSessionClose(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	// Set an active session so pre-v2.3.0 auto-bind WOULD have kicked in.
	if err := st.SetActiveSession(ctx, "default", "sess-test-1"); err != nil {
		t.Fatalf("set active session: %v", err)
	}

	// Save WITHOUT bind_session (default). Pre-v2.3.0 this would
	// have populated session_id="sess-test-1"; v2.3.0 leaves it empty.
	row := makeRow("alice", "note", "persistent memory")
	row.SessionID = "" // explicit empty mimics caller not setting bind_session
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test_lifecycle"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.GetAgentMemory(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "" {
		t.Errorf("session_id after save with no bind: got %q, want empty (v2.3.0 no-auto-bind)", got.SessionID)
	}

	// Close the session. ClearActiveSession is CAS-style; we pass
	// the expected current session id.
	if err := st.ClearActiveSession(ctx, "default", "sess-test-1"); err != nil {
		t.Fatalf("clear active session: %v", err)
	}

	// Row must be accessible via scope=project.
	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope: agentmemory.ScopeProject,
	})
	if err != nil {
		t.Fatalf("list scope=project after session close: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("scope=project after session close: got %d rows, want 1 (INV-10)", len(rows))
	}
	if len(rows) > 0 && rows[0].ID != id {
		t.Errorf("returned wrong row id: got %d, want %d", rows[0].ID, id)
	}

	// And, separately, scope=session now returns empty (no active
	// session to filter by; the row's session_id was empty pre-
	// close and is still empty). This proves the row is in scope=
	// project but NOT in scope=session — exactly INV-10 behavior.
	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope: agentmemory.ScopeSession,
	})
	if err != nil {
		t.Fatalf("list scope=session after close: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("scope=session after close: got %d rows, want 0 (no active session)", len(rows))
	}
}

// TestListAgentMemory_ScopeAgent_FiltersByAgentID is the new
// scope=agent wire-contract delta. rows.agent_id is the Mem0 agent_id
// field; scope=agent requires the caller to supply AgentID.
func TestListAgentMemory_ScopeAgent_FiltersByAgentID(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	rowA := makeRow("alice", "note", "from agent A")
	rowA.AgentID = "claude-sonnet-4"
	if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), rowA); err != nil {
		t.Fatalf("agent A save: %v", err)
	}
	rowB := makeRow("bob", "note", "from agent B")
	rowB.AgentID = "claude-haiku-3"
	if _, err := st.SaveAgentMemory(ctx, wcFor("bob", "test"), rowB); err != nil {
		t.Fatalf("agent B save: %v", err)
	}

	rows, err := st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:   agentmemory.ScopeAgent,
		AgentID: "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("scope=agent sonnet: %v", err)
	}
	if len(rows) != 1 || rows[0].AgentID != "claude-sonnet-4" {
		t.Errorf("scope=agent sonnet: got %d rows, want 1 from claude-sonnet-4", len(rows))
	}

	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:   agentmemory.ScopeAgent,
		AgentID: "claude-haiku-3",
	})
	if err != nil {
		t.Fatalf("scope=agent haiku: %v", err)
	}
	if len(rows) != 1 || rows[0].AgentID != "claude-haiku-3" {
		t.Errorf("scope=agent haiku: got %d rows, want 1 from claude-haiku-3", len(rows))
	}

	rows, err = st.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:   agentmemory.ScopeAgent,
		AgentID: "",
	})
	if err != nil {
		t.Fatalf("scope=agent empty: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("scope=agent empty AgentID: got %d rows, want 0", len(rows))
	}
}

// TestAgentMemory_Save_AcceptsAgentIDAndMemoryType covers the new
// columns round-tripping through SaveAgentMemory + GetAgentMemory.
func TestAgentMemory_Save_AcceptsAgentIDAndMemoryType(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "observation", "kestrel launch sequence notes")
	row.AgentID = "claude-sonnet-4"
	row.MemoryType = agentmemory.MemoryTypeEpisodic
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.GetAgentMemory(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentID != "claude-sonnet-4" {
		t.Errorf("agent_id roundtrip: got %q, want %q", got.AgentID, "claude-sonnet-4")
	}
	if got.MemoryType != agentmemory.MemoryTypeEpisodic {
		t.Errorf("memory_type roundtrip: got %q, want %q", got.MemoryType, agentmemory.MemoryTypeEpisodic)
	}

	// Save with invalid memory_type is rejected.
	bad := makeRow("alice", "note", "should fail")
	bad.MemoryType = "invalid_class"
	if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), bad); err == nil {
		t.Errorf("save invalid memory_type: got nil error, want one")
	} else if !strings.Contains(err.Error(), "memory_type") {
		t.Errorf("save invalid memory_type error: %v (should mention memory_type)", err)
	}
}

// TestAgentMemory_Recall_BasicSmoke exercises the new
// dark_memory_agent_memory_recall tool's underlying Store call
// (SearchAgentMemory with Operator + AgentID + MemoryType filters).
// The agent_memory_recall orchestrator + tool layer just delegates
// to SearchAgentMemory; we test the Store end here.
func TestAgentMemory_Recall_BasicSmoke(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row1 := makeRow("alice", "observation", "project kestrel launch sequence")
	row1.AgentID = "claude-sonnet-4"
	row1.MemoryType = agentmemory.MemoryTypeEpisodic
	if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row1); err != nil {
		t.Fatalf("row1: %v", err)
	}

	row2 := makeRow("alice", "decision", "we picked postgres over sqlite for the audit log")
	row2.AgentID = "claude-sonnet-4"
	row2.MemoryType = agentmemory.MemoryTypeSemantic
	if _, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row2); err != nil {
		t.Fatalf("row2: %v", err)
	}

	row3 := makeRow("bob", "note", "postgres has a memory trick")
	row3.AgentID = "claude-haiku-3"
	row3.MemoryType = agentmemory.MemoryTypeSemantic
	if _, err := st.SaveAgentMemory(ctx, wcFor("bob", "test"), row3); err != nil {
		t.Fatalf("row3: %v", err)
	}

	// recall "postgres" no filters: 2 hits
	hits, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{Query: "postgres"})
	if err != nil {
		t.Fatalf("recall no filter: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("recall no filter: got %d hits, want 2", len(hits))
	}

	// recall "postgres" + operator=alice: 1 hit (row2)
	hits, err = st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:    "postgres",
		Operator: "alice",
	})
	if err != nil {
		t.Fatalf("recall op=alice: %v", err)
	}
	if len(hits) != 1 || hits[0].Operator != "alice" {
		t.Errorf("recall op=alice: got %d hits, want 1 from alice", len(hits))
	}

	// recall "postgres" + agent_id=claude-haiku-3: 1 hit (row3)
	hits, err = st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:   "postgres",
		AgentID: "claude-haiku-3",
	})
	if err != nil {
		t.Fatalf("recall agent=haiku: %v", err)
	}
	if len(hits) != 1 || hits[0].AgentID != "claude-haiku-3" {
		t.Errorf("recall agent=haiku: got %d hits, want 1 from claude-haiku-3", len(hits))
	}

	// recall "postgres" + memory_type=episodic: 0 hits (none of the
	// postgres rows are episodic)
	hits, err = st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:      "postgres",
		MemoryType: agentmemory.MemoryTypeEpisodic,
	})
	if err != nil {
		t.Fatalf("recall mtype=episodic: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("recall mtype=episodic: got %d hits, want 0", len(hits))
	}

	// Invalid memory_type is rejected.
	if _, err := st.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:      "postgres",
		MemoryType: "foo",
	}); err == nil {
		t.Errorf("recall mtype=foo: got nil error, want one")
	}
}

// TestAgentMemory_Update_AcceptsMemoryType covers the Update path on
// the new memory_type column, including the empty-string = clear
// convention.
func TestAgentMemory_Update_AcceptsMemoryType(t *testing.T) {
	st, cleanup := openAgentMemoryDB(t)
	defer cleanup()
	ctx := context.Background()

	row := makeRow("alice", "note", "test memory_type update")
	row.MemoryType = agentmemory.MemoryTypeSemantic
	id, err := st.SaveAgentMemory(ctx, wcFor("alice", "test"), row)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Update to procedural.
	proc := agentmemory.MemoryTypeProcedural
	updated, err := st.UpdateAgentMemory(ctx, wcFor("alice", "test"), id, &agentmemory.AgentMemoryUpdate{
		MemoryType: &proc,
	})
	if err != nil {
		t.Fatalf("update mtype=procedural: %v", err)
	}
	if updated.MemoryType != agentmemory.MemoryTypeProcedural {
		t.Errorf("update mtype: got %q, want %q", updated.MemoryType, agentmemory.MemoryTypeProcedural)
	}

	// Update to empty (clear).
	empty := ""
	updated, err = st.UpdateAgentMemory(ctx, wcFor("alice", "test"), id, &agentmemory.AgentMemoryUpdate{
		MemoryType: &empty,
	})
	if err != nil {
		t.Fatalf("update mtype=empty: %v", err)
	}
	if updated.MemoryType != "" {
		t.Errorf("update mtype=empty: got %q, want empty", updated.MemoryType)
	}

	// Update with invalid memory_type rejected.
	bad := "invalid_class"
	_, err = st.UpdateAgentMemory(ctx, wcFor("alice", "test"), id, &agentmemory.AgentMemoryUpdate{
		MemoryType: &bad,
	})
	if err == nil {
		t.Errorf("update invalid mtype: got nil error, want one")
	}
}
