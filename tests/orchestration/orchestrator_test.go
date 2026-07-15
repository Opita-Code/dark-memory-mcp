// Package orchestration_test covers the workflow API. Each
// orchestrator method has at least one happy-path test + one error
// path test. Tests use the in-memory SQLite Store via runtime.Open,
// same pattern as tests/context and tests/project.
package orchestration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"

	"fmt"
)

func openOrchestratorTestEnv(t *testing.T) (*orchestration.Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return orchestration.New(s, nil), s
}

// O1: SessionStart — happy path. Creates a session bound to a project,
// returns a non-empty SessionID, project_id echoed, audit row emitted.
func TestSessionStart_HappyPath(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	out, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:        "nico",
		ProjectID:       "acme",
		ConstitutionID:  "v1",
		ConstitutionVer: "1.0.0",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if out.SessionID == "" || !strings.HasPrefix(out.SessionID, "sess-") {
		t.Fatalf("SessionID should be non-empty with 'sess-' prefix, got: %q", out.SessionID)
	}
	if out.ProjectID != "acme" {
		t.Fatalf("ProjectID should be 'acme', got: %q", out.ProjectID)
	}
	if out.ConstitutionID != "v1" {
		t.Fatalf("ConstitutionID should echo, got: %q", out.ConstitutionID)
	}
	if out.StartedAt.IsZero() {
		t.Fatalf("StartedAt should be non-zero")
	}

	// Active project must be set on the store.
	if got := s.ActiveProject(); got != "acme" {
		t.Fatalf("store active project should be 'acme', got %q", got)
	}

	// The session must be findable in the store.
	got, err := s.GetSession(ctx, out.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil || got.SessionID != out.SessionID || got.Operator != "nico" {
		t.Fatalf("session round-trip mismatch: %+v", got)
	}

	// Audit row emitted (SaveSession writes one).
	writes, _ := s.ListWrites(ctx, audit.ListFilters{SessionID: out.SessionID, Limit: 10})
	if len(writes) == 0 {
		t.Fatalf("expected write_audit row for session %q", out.SessionID)
	}
}

// O1: empty Operator rejected.
func TestSessionStart_MissingOperator(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)

	_, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "",
		ProjectID: "default",
	})
	if err == nil {
		t.Fatalf("expected error for missing Operator")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O1: empty ProjectID rejected.
func TestSessionStart_MissingProjectID(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)

	_, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "",
	})
	if err == nil {
		t.Fatalf("expected error for missing ProjectID")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O1: unknown ProjectID rejected at SetActiveProject (the orchestrator
// propagates ErrInvalidArgument from SetActiveProject).
func TestSessionStart_UnknownProject(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)

	_, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "ghost",
	})
	if err == nil {
		t.Fatalf("expected error for unknown project")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O1: "default" project (auto-seeded on Open) works without explicit CreateProject.
func TestSessionStart_DefaultProject(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)

	out, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "system",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart default: %v", err)
	}
	if out.ProjectID != "default" {
		t.Fatalf("ProjectID should be 'default', got %q", out.ProjectID)
	}
}

// O1: session can be Started and Closed back-to-back (drives O2 if
// implemented; here just verifies the session row reaches the store).
func TestSessionStart_RowReachStore(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)

	out, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	got, err := s.GetSession(ctx, out.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil || got.Status != string(session.StatusActive) {
		t.Fatalf("session should exist and be active, got: %+v", got)
	}
}

// Local errors.Is for sentinel comparison. Same pattern as
// tests/project/project_test.go.
func errIs(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// O2: SessionClose — happy path. Closes a session, returns summary.
// WritesTotal >= 1 (SaveSession emitted a write_audit row).
func TestSessionClose_HappyPath(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	start, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	out, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{
		SessionID: start.SessionID,
	})
	if err != nil {
		t.Fatalf("SessionClose: %v", err)
	}
	if out.SessionID != start.SessionID {
		t.Fatalf("SessionID mismatch: %q vs %q", out.SessionID, start.SessionID)
	}
	if out.ClosedAt.IsZero() {
		t.Fatalf("ClosedAt should be non-zero")
	}
	if out.WritesTotal < 1 {
		t.Fatalf("WritesTotal should be >=1 (SaveSession emitted at least 1), got %d", out.WritesTotal)
	}

	// Session in store should now be closed (status='closed').
	got, err := s.GetSession(ctx, start.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil {
		t.Fatalf("session disappeared")
	}
	if got.Status != "closed" {
		t.Fatalf("session status should be 'closed', got %q", got.Status)
	}
}

// O2: empty SessionID rejected.
func TestSessionClose_MissingSessionID(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)

	_, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{SessionID: ""})
	if err == nil {
		t.Fatalf("expected error for empty session_id")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O2: closing a non-existent session returns ErrNotFound (after the
// project is active).
func TestSessionClose_NotFound(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set default: %v", err)
	}

	_, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{
		SessionID: "sess-deadbeef",
	})
	if err == nil {
		t.Fatalf("expected error for unknown session")
	}
	if !errIs(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

// O2: closing the same session twice: first succeeds, second returns
// ErrNotFound (already closed).
func TestSessionClose_DoubleClose(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)

	start, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	if _, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{SessionID: start.SessionID}); err != nil {
		t.Fatalf("first close: %v", err)
	}
	_, err = orch.SessionClose(ctx, orchestration.SessionCloseInput{SessionID: start.SessionID})
	if err == nil {
		t.Fatalf("expected error on second close")
	}
	// CloseSession has WHERE status='active' filter; second close finds
	// no active row -> ErrNotFound.
	if !errIs(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on double-close, got: %v", err)
	}
}

// O2: closing a session belonging to a different project returns
// ErrNotFound (project filter blocks cross-project close).
func TestSessionClose_CrossProject(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "globex", DisplayName: "Globex"}); err != nil {
		t.Fatalf("create globex: %v", err)
	}

	// Open session in acme.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme: %v", err)
	}
	start, err := orch.SessionStart(ctx, orchestration.SessionStartInput{Operator: "nico", ProjectID: "acme"})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// Switch to globex; close from globex should fail.
	if err := s.SetActiveProject(ctx, "globex"); err != nil {
		t.Fatalf("set globex: %v", err)
	}
	_, err = orch.SessionClose(ctx, orchestration.SessionCloseInput{SessionID: start.SessionID})
	if err == nil {
		t.Fatalf("expected error closing cross-project session")
	}
	if !errIs(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound (project filter blocks), got: %v", err)
	}

	// Back in acme; close works.
	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set acme (back): %v", err)
	}
	out, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{SessionID: start.SessionID})
	if err != nil {
		t.Fatalf("close from acme: %v", err)
	}
	if out.SessionID != start.SessionID {
		t.Fatalf("session id mismatch on close")
	}
}

// O3: ResearchTopic — happy path with one mock backend returning 3 items.
func TestResearchTopic_OneBackend(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	backend := &orchestration.MockResearchBackend{
		Name_: "mock_web",
		Items: []research.Item{
			{Title: "Result A", Source: "mock_web", Confidence: 0.9},
			{Title: "Result B", Source: "mock_web", Confidence: 0.7},
			{Title: "Result C", Source: "mock_web", Confidence: 0.5},
		},
	}
	orch.WithBackend(backend)

	out, err := orch.ResearchTopic(ctx, orchestration.ResearchTopicInput{
		Query:  "supply chain attacks",
		Intent: "web",
	})
	if err != nil {
		t.Fatalf("ResearchTopic: %v", err)
	}
	if out.RunID == 0 {
		t.Fatalf("RunID should be > 0")
	}
	if out.ItemsCount != 3 {
		t.Fatalf("ItemsCount should be 3, got %d", out.ItemsCount)
	}
	if backend.Calls != 1 {
		t.Fatalf("backend should have been called once, got %d", backend.Calls)
	}
	if backend.LastQ != "supply chain attacks" {
		t.Fatalf("backend received wrong query: %q", backend.LastQ)
	}

	// Verify the run + items were persisted.
	got, err := s.GetRun(ctx, out.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.Query != "supply chain attacks" {
		t.Fatalf("run round-trip mismatch: %+v", got)
	}
	items, err := s.ListItems(ctx, out.RunID, "", 50)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("ListItems should return 3, got %d", len(items))
	}
}

// O3: ResearchTopic — MaxItems cap.
func TestResearchTopic_MaxItemsCap(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Backend returns 10 items; MaxItems=3 caps to 3.
	all := make([]research.Item, 10)
	for i := range all {
		all[i] = research.Item{Title: "X", Source: "mock", Confidence: 0.5}
	}
	orch.WithBackend(&orchestration.MockResearchBackend{Name_: "mock", Items: all})

	out, err := orch.ResearchTopic(ctx, orchestration.ResearchTopicInput{
		Query:    "Q",
		Intent:   "web",
		MaxItems: 3,
	})
	if err != nil {
		t.Fatalf("ResearchTopic: %v", err)
	}
	if out.ItemsCount != 3 {
		t.Fatalf("ItemsCount should be 3 (capped), got %d", out.ItemsCount)
	}
}

// O3: ResearchTopic — missing query rejected.
func TestResearchTopic_MissingQuery(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, err := orch.ResearchTopic(ctx, orchestration.ResearchTopicInput{Intent: "web"})
	if err == nil {
		t.Fatalf("expected error for missing query")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O3: ResearchTopic — backend error does not fail the call; logged into Errors.
func TestResearchTopic_BackendErrorGracefulDegradation(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	failing := &orchestration.MockResearchBackend{
		Name_: "mock_failing",
		Err:   fmt.Errorf("simulated backend failure"),
	}
	working := &orchestration.MockResearchBackend{
		Name_: "mock_working",
		Items: []research.Item{
			{Title: "ok-1", Source: "mock_working", Confidence: 0.9},
		},
	}
	orch.WithBackend(failing).WithBackend(working)

	out, err := orch.ResearchTopic(ctx, orchestration.ResearchTopicInput{Query: "Q", Intent: "web"})
	if err != nil {
		t.Fatalf("ResearchTopic should succeed despite one backend failing: %v", err)
	}
	if out.ItemsCount != 1 {
		t.Fatalf("should have 1 item from working backend, got %d", out.ItemsCount)
	}

	// Verify Errors slice has the failing backend logged.
	got, _ := s.GetRun(ctx, out.RunID)
	if got == nil {
		t.Fatalf("GetRun returned nil")
	}
	if len(got.Errors) != 1 || got.Errors[0].Backend != "mock_failing" {
		t.Fatalf("Errors should record failing backend, got: %+v", got.Errors)
	}
}

// O3: ResearchTopic — fan-out across multiple backends aggregates items.
func TestResearchTopic_MultipleBackendsAggregate(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	orch.WithBackend(&orchestration.MockResearchBackend{
		Name_: "b1", Items: []research.Item{{Title: "from-b1", Source: "b1", Confidence: 0.9}},
	}).WithBackend(&orchestration.MockResearchBackend{
		Name_: "b2", Items: []research.Item{
			{Title: "from-b2-a", Source: "b2", Confidence: 0.8},
			{Title: "from-b2-b", Source: "b2", Confidence: 0.7},
		},
	})

	out, err := orch.ResearchTopic(ctx, orchestration.ResearchTopicInput{Query: "Q", Intent: "web"})
	if err != nil {
		t.Fatalf("ResearchTopic: %v", err)
	}
	if out.ItemsCount != 3 {
		t.Fatalf("expected 3 items aggregated, got %d", out.ItemsCount)
	}

	got, _ := orch.Store.GetRun(ctx, out.RunID)
	if got == nil || len(got.BackendsTried) != 2 {
		t.Fatalf("BackendsTried should record both, got: %+v", got)
	}
}

// O2: summary counts reflect activity. Write a research run in the
// session, then close and check ItemsTotal > 0.
func TestSessionClose_SummaryCounts(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set: %v", err)
	}
	start, err := orch.SessionStart(ctx, orchestration.SessionStartInput{Operator: "nico", ProjectID: "acme"})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// Save a run + 3 items in this session.
	if _, err := s.SaveRun(ctx, store.WriteContext{
		Actor: "test", SessionID: start.SessionID, WritePath: "test",
	}, &research.ResearchRun{
		SessionID: start.SessionID,
		Query:     "Q1", Intent: "cve",
		Items: []research.Item{
			{Title: "i1", Source: "test", Confidence: 0.9},
			{Title: "i2", Source: "test", Confidence: 0.8},
			{Title: "i3", Source: "test", Confidence: 0.7},
		},
	}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	out, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{SessionID: start.SessionID})
	if err != nil {
		t.Fatalf("SessionClose: %v", err)
	}
	if out.RunsTotal != 1 {
		t.Fatalf("RunsTotal should be 1, got %d", out.RunsTotal)
	}
	if out.ItemsTotal != 3 {
		t.Fatalf("ItemsTotal should be 3, got %d", out.ItemsTotal)
	}
	if out.WritesTotal < 3 {
		// SaveRun emits 1 (run) + N (items) + audit via SaveRun. 3
		// items + 1 run audit = at least 4 audit rows, plus the
		// SessionStart and SessionClose rows.
		t.Fatalf("WritesTotal should be >=3, got %d", out.WritesTotal)
	}
}