// Package project_test — SPEC-203: the project of a write comes from
// the session that asks for it, not from the global active-project slot.
//
// THE SCENARIO IS THE MEASURED ONE. The MCP server process is shared
// between several clients (measured on the operator's machine: three
// connections to one server=dark-memory process, with their
// session_start calls interleaved seconds apart). Each client works in
// its own project. Whoever started a session last owns the slot, and
// every write that does not name its session lands there — the agent's
// memory ends up in a project that is not its own.
//
// The fix: the session is the authority. A write that names session-A
// lands in session-A's project, whatever the slot says.
package project_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// SPEC-203 §6: the test of two concurrent clients, point by point.
//
//  3. A write that names S_A falls in A, not in B.
//  4. A write that names no session falls in B (the legacy slot).
//  5. agent_memory_list with A's session returns only A's rows.
//  6. agent_memory_list with B's project and A's session is refused.
func TestSpec203_SessionProject_WritesLandInTheirOwnProject(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Session A, in project acme.
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}

	// Session B, in project globex. THE SLOT NOW POINTS AT GLOBEX:
	// this is the state that used to swallow every write.
	s.SetActiveProject(ctx, "globex")
	wcB := store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"}
	if _, err := s.SaveSession(ctx, wcB, &session.Session{SessionID: "sess-B", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session B: %v", err)
	}

	// (3) The write that names session A lands in ACME.
	memA := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "acme memory"}
	wcWriteA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test"}
	if _, err := s.SaveAgentMemory(ctx, wcWriteA, memA); err != nil {
		t.Fatalf("save agent memory (session A): %v", err)
	}
	if memA.ProjectID != "acme" {
		t.Fatalf("SPEC-203: the write that names session A landed in %q, want %q", memA.ProjectID, "acme")
	}

	// (4) The write that names no session falls back to the slot
	// (globex). Documented behaviour, unchanged by the fix.
	memB := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "globex memory"}
	if _, err := s.SaveAgentMemory(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, memB); err != nil {
		t.Fatalf("save agent memory (no session): %v", err)
	}
	if memB.ProjectID != "globex" {
		t.Fatalf("the write without session landed in %q, want the active slot %q", memB.ProjectID, "globex")
	}

	// (5) The read with session A returns only acme's rows.
	rows, err := s.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		SessionID: "sess-A",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("list with session A: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("SPEC-203: list with session A returned %d rows, want 1", len(rows))
	}
	if rows[0].ProjectID != "acme" {
		t.Fatalf("SPEC-203: list with session A returned a row of %q", rows[0].ProjectID)
	}

	// (6) The read that asks for globex with session A is REFUSED. A
	// read never widens, and the refusal is explicit: an empty result
	// would be indistinguishable from "there is no memory".
	_, err = s.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		SessionID: "sess-A",
		ProjectID: "globex",
	})
	if err == nil {
		t.Fatalf("SPEC-203: reading another project with session A was allowed")
	}
	if !errors.Is(err, store.ErrCrossProjectAccess) {
		t.Fatalf("expected ErrCrossProjectAccess, got: %v", err)
	}
}

// SPEC-203: the write_audit row attributes the write to the session
// that asked for it. Before the fix, every agent_memory write landed
// in write_audit with a NULL session_id: the trail did not say who
// wrote, nor in which project.
func TestSpec203_SessionProject_AuditCarriesTheSession(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	s.SetActiveProject(ctx, "acme")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}
	s.SetActiveProject(ctx, "globex")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"},
		&session.Session{SessionID: "sess-B", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session B: %v", err)
	}

	mem := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "audited memory"}
	if _, err := s.SaveAgentMemory(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test"}, mem); err != nil {
		t.Fatalf("save agent memory: %v", err)
	}

	writes, err := s.ListWrites(ctx, audit.ListFilters{SessionID: "sess-A", Limit: 20})
	if err != nil {
		t.Fatalf("list writes: %v", err)
	}
	var found bool
	for _, w := range writes {
		if w.TableName == "agent_memory" {
			found = true
			if w.ProjectID != "acme" {
				t.Fatalf("SPEC-203: the audit row of the agent_memory write says project %q, want %q", w.ProjectID, "acme")
			}
			if w.SessionID != "sess-A" {
				t.Fatalf("SPEC-203: the audit row says session %q, want %q", w.SessionID, "sess-A")
			}
		}
	}
	if !found {
		t.Fatalf("SPEC-203: no write_audit row for the agent_memory write of session A")
	}
}

// SPEC-203: the search path obeys the same rule as the list path.
// Recall is the discovery path — it is the one an agent uses to find
// what it already knows — so a recall that follows the slot instead
// of the session is exactly the bug that hides another project's
// memories.
func TestSpec203_SessionProject_RecallScopedToTheSession(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	s.SetActiveProject(ctx, "acme")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}
	s.SetActiveProject(ctx, "globex")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"},
		&session.Session{SessionID: "sess-B", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session B: %v", err)
	}

	acmeMem := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "the acme secret"}
	if _, err := s.SaveAgentMemory(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test"}, acmeMem); err != nil {
		t.Fatalf("save acme memory: %v", err)
	}
	globexMem := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "the globex secret"}
	if _, err := s.SaveAgentMemory(ctx, store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test"}, globexMem); err != nil {
		t.Fatalf("save globex memory: %v", err)
	}

	// The recall of session A finds acme's memory and NOT globex's,
	// even though the slot points at globex.
	hits, err := s.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:     "secret",
		SessionID: "sess-A",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("search with session A: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SPEC-203: search with session A returned %d hits, want 1", len(hits))
	}
	if hits[0].Content != "the acme secret" {
		t.Fatalf("SPEC-203: search with session A returned %q", hits[0].Content)
	}
}

// SPEC-203: the two resolution helpers the orchestrator asks the
// store for. The lookup by session id is the piece that makes the
// rest possible: it does NOT filter by the active project.
func TestSpec203_ProjectIDForSession_IgnoresTheActiveSlot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	s.SetActiveProject(ctx, "acme")
	if _, err := s.SaveSession(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"},
		&session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}

	// The slot moves to globex. The lookup by session id still answers
	// for acme — that is the whole difference with GetSession.
	s.SetActiveProject(ctx, "globex")
	pid, err := s.ProjectIDForSession(ctx, "sess-A")
	if err != nil {
		t.Fatalf("project_id_for_session: %v", err)
	}
	if pid != "acme" {
		t.Fatalf("SPEC-203: ProjectIDForSession(sess-A) = %q, want %q", pid, "acme")
	}

	// An unknown session is not an error: it is the fallback signal.
	pid, err = s.ProjectIDForSession(ctx, "sess-nope")
	if err != nil {
		t.Fatalf("project_id_for_session (unknown): %v", err)
	}
	if pid != "" {
		t.Fatalf("SPEC-203: ProjectIDForSession(sess-nope) = %q, want empty", pid)
	}

	// ResolveProjectForCall: the session is the authority, then the
	// explicit project, then the slot.
	got, err := s.ResolveProjectForCall(ctx, "", "sess-A")
	if err != nil {
		t.Fatalf("resolve_project_for_call: %v", err)
	}
	if got != "acme" {
		t.Fatalf("SPEC-203: ResolveProjectForCall(sess-A) = %q, want %q", got, "acme")
	}
	got, err = s.ResolveProjectForCall(ctx, "acme", "")
	if err != nil {
		t.Fatalf("resolve_project_for_call (explicit): %v", err)
	}
	if got != "acme" {
		t.Fatalf("SPEC-203: ResolveProjectForCall(explicit acme) = %q, want %q", got, "acme")
	}
	got, err = s.ResolveProjectForCall(ctx, "", "")
	if err != nil {
		t.Fatalf("resolve_project_for_call (slot): %v", err)
	}
	if got != "globex" {
		t.Fatalf("SPEC-203: ResolveProjectForCall(no session) = %q, want the slot %q", got, "globex")
	}
}
