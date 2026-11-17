// Package project_test — SPEC-204 §4: a resolution FAILURE is an
// error, and it is never degraded to the active-project slot.
//
// THE BUG THIS TEST FIXES. The store fails closed: when the session
// lookup fails (an I/O error), store.scopeProject returns the error and
// the chain stops. That is the correct behavior and the store says so
// in its own words:
//
//	"A lookup FAILURE is an error and is never degraded to the global
//	 slot (SPEC-204 §4): degrading would reintroduce the bug this chain
//	 exists to remove, and would do it silently."
//
// The orchestrator undid that. resolveCallProject swallowed the error
// and returned o.Store.ActiveProject() — the global slot. So the store
// failed closed and the orchestrator opened it again, one layer up,
// without leaving an error behind. That is the worst place for it: the
// write lands in whatever project the slot points at, which may belong
// to another client sharing this MCP process, and the caller never
// learns that anything went wrong.
//
// WHY IT MATTERS EVEN THOUGH NOTHING BROKE TODAY. The session lookup
// failing is an I/O error (the database is busy, the context was
// cancelled, the row is half-written). When the answer is "I don't
// know", the only safe answer for a WRITE is "I don't write". Writing
// to a project you could not resolve is how a row ends up in another
// tenant's memory.
//
// THE SHAPE OF THE TEST. A store that embeds the real interface and
// only overrides ResolveProjectForCall to fail. Everything else is the
// real sqlite store, so the test exercises the real chain and only
// simulates the failure it is about.
package project_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// failingResolutionStore is the real store with ONE method replaced:
// the project resolution always fails, the way a database error would.
//
// Embedding the interface (rather than writing a fake) keeps every
// other method real, so the test measures the orchestrator's behavior
// and not the fidelity of a hand-written double.
type failingResolutionStore struct {
	store.Store
	failErr error
}

// ResolveProjectForCall fails the way a store failure does.
func (f *failingResolutionStore) ResolveProjectForCall(ctx context.Context, projectID, sessionID string) (string, error) {
	return "", f.failErr
}

// SPEC-204 §4: when the project resolution fails, the write must FAIL.
// It must not land in the active-project slot.
func TestSpec204_ResolutionFailure_DoesNotDegradeToTheSlot(t *testing.T) {
	ctx := context.Background()

	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	real, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	// Two projects. The SLOT will point at "globex" — a project that
	// has nothing to do with the caller.
	if err := real.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := real.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}
	real.SetActiveProject(ctx, "globex")

	// A session that owns "acme".
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := real.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}

	// The store now fails the resolution.
	boom := errors.New("sqlite: project_id_for_session: context canceled")
	failing := &failingResolutionStore{Store: real, failErr: boom}
	orch := orchestration.New(failing, nil)

	// 1. The WRITE must fail. Before the fix it succeeded and landed
	//    in "globex" — the slot — with no error.
	mem := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "must not land in globex"}
	if _, err := orch.AgentMemorySave(ctx, orchestration.AgentMemorySaveInput{
		Operator:  "test",
		Kind:      "note",
		Content:   mem.Content,
		SessionID: "sess-A",
	}); err == nil {
		t.Fatalf("SPEC-204 §4: AgentMemorySave succeeded with a failing resolution; it must fail")
	} else if !errors.Is(err, boom) {
		t.Errorf("the error must carry the store failure: %v", err)
	}

	// 2. Nothing may have landed in the slot project.
	if rows := countRows(t, real, "globex"); rows != 0 {
		t.Fatalf("SPEC-204 §4: %d row(s) landed in the slot project (globex); a failed resolution must write nowhere", rows)
	}

	// 3. Nothing may have landed in the session's project either: the
	//    write had no resolved project, so it had no destination.
	if rows := countRows(t, real, "acme"); rows != 0 {
		t.Fatalf("SPEC-204 §4: %d row(s) landed in acme; a failed resolution must write nowhere", rows)
	}
}

// countRows counts the agent_memory rows of one project.
//
// The filter's ProjectID is a NARROWING that the store checks against
// the active project (INV-7), so the slot is pointed at the project
// being counted before the read. Without that, the read is refused
// with the cross-project error — which is the store working correctly,
// not a failure of the thing under test.
func countRows(t *testing.T, s store.Store, projectID string) int {
	t.Helper()
	s.SetActiveProject(context.Background(), projectID)
	rows, err := s.ListAgentMemory(context.Background(), agentmemory.AgentMemoryListFilters{
		ProjectID: projectID,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("list %s: %v", projectID, err)
	}
	return len(rows)
}

// SPEC-204 §4: the same rule for the reads. A read that cannot resolve
// its project must not read from the slot: that would expose another
// project's row (and, under the v2.8.0 flag, answer a question about
// a project the caller did not ask for).
func TestSpec204_ResolutionFailure_ReadsDoNotFallBackToTheSlot(t *testing.T) {
	ctx := context.Background()

	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	real, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	if err := real.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := real.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}
	real.SetActiveProject(ctx, "globex")

	boom := errors.New("sqlite: project_id_for_session: context canceled")
	failing := &failingResolutionStore{Store: real, failErr: boom}
	orch := orchestration.New(failing, nil)

	// The read must fail, not silently answer with the slot's content.
	if _, err := orch.AgentMemoryGet(ctx, orchestration.AgentMemoryGetInput{
		ID:        1,
		SessionID: "sess-A",
	}); err == nil {
		t.Fatalf("SPEC-204 §4: AgentMemoryGet succeeded with a failing resolution; it must fail")
	} else if !errors.Is(err, boom) {
		t.Errorf("the error must carry the store failure: %v", err)
	}
}
