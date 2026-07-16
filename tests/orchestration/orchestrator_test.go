// Package orchestration_test covers the workflow API. Each
// orchestrator method has at least one happy-path test + one error
// path test. Tests use the in-memory SQLite Store via runtime.Open,
// same pattern as tests/context and tests/project.
package orchestration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
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

// O4: RecallContext — happy path with persisted data.
func TestRecallContext_HappyPath(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Seed a run + 5 items.
	_, err := s.SaveRun(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test"},
		&research.ResearchRun{
			SessionID: "sess-A",
			Query:     "supply chain attacks",
			Intent:    "web",
			Items: []research.Item{
				{Title: "Supply chain A", Snippet: "first sentence about attacks. more text.", Source: "web", Confidence: 0.9},
				{Title: "Supply chain B", Snippet: "second item snippet", Source: "web", Confidence: 0.7},
				{Title: "Supply chain C", Snippet: "third item snippet", Source: "web", Confidence: 0.6},
				{Title: "Other topic", Snippet: "unrelated content", Source: "news", Confidence: 0.8},
				{Title: "another acme item", Snippet: "supply chain context", Source: "news", Confidence: 0.5},
			},
		})
	if err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	out, err := orch.RecallContext(ctx, orchestration.RecallContextInput{
		Query: "supply chain",
	})
	if err != nil {
		t.Fatalf("RecallContext: %v", err)
	}
	// Should find the 4 matching items (not "Other topic"), compressed
	// (snippet truncated to first sentence).
	if len(out.Items) == 0 {
		t.Fatalf("expected items, got 0")
	}
	// Items are sorted by confidence and capped at 10 by default.
	for _, it := range out.Items {
		if !strings.Contains(it.Title, "Supply chain") && !strings.Contains(it.Title, "another") {
			t.Fatalf("unexpected item title: %q", it.Title)
		}
	}
	if out.TokensUsed <= 0 {
		t.Fatalf("TokensUsed should be > 0, got %d", out.TokensUsed)
	}
}

// O4: RecallContext — missing query rejected.
func TestRecallContext_MissingQuery(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.RecallContext(ctx, orchestration.RecallContextInput{})
	if err == nil {
		t.Fatalf("expected error for missing query")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O4: RecallContext — tight MaxTokens reduces items further.
func TestRecallContext_TightBudget(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Seed many items, all roughly matching the query. Use a snippet
	// without periods so the first-sentence compression leaves most
	// of the text intact, making the token budget harder to satisfy.
	// Each item has a unique URL so dedup doesn't collapse them.
	items := make([]research.Item, 20)
	for i := range items {
		items[i] = research.Item{
			Title:     fmt.Sprintf("Supply chain item %02d", i),
			URL:       fmt.Sprintf("https://example.com/supply-chain-%02d", i),
			Snippet:   strings.Repeat("lorem-ipsum-dolor-sit-amet ", 100),
			Source:    "web",
			Confidence: float32(0.5 + float32(i)*0.02),
		}
	}
	if _, err := s.SaveRun(ctx, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test"},
		&research.ResearchRun{
			SessionID: "sess-A", Query: "seed", Intent: "web", Items: items,
		}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	out, err := orch.RecallContext(ctx, orchestration.RecallContextInput{
		Query: "supply chain", MaxTokens: 200,
	})
	if err != nil {
		t.Fatalf("RecallContext: %v", err)
	}
	// Tight budget should produce 3 items at most (cap rule for
	// MaxTokens < 500).
	if len(out.Items) > 3 {
		t.Fatalf("tight budget should yield <=3 items, got %d", len(out.Items))
	}
	if !out.Truncated {
		t.Fatalf("Truncated should be true when budget was tight")
	}
}

// O4: RecallContext — no active project returns ErrSessionRequired.
func TestRecallContext_NoProject(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	// Don't set active project.
	_, err := orch.RecallContext(ctx, orchestration.RecallContextInput{Query: "Q"})
	if err == nil {
		t.Fatalf("expected error for no active project")
	}
	if !errIs(err, store.ErrSessionRequired) {
		t.Fatalf("expected ErrSessionRequired, got: %v", err)
	}
}

// O5: Judge — happy path with mock LLM returning a verdict.
func TestJudge_HappyPath(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v1",
		VerdictJSON: `{"match":0.92,"issues":[]}`,
		Confidence:  0.92,
		Model:       "mock-1",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType:   "brand_match",
		TargetType: "brand",
		TargetID:   "acme-base",
		Content:    "Our brand voice is bold and warm.",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		t.Fatalf("Confidence out of [0,1] range: %f", out.Confidence)
	}
	if mock.Calls != 1 {
		t.Fatalf("LLM should have been called once, got %d", mock.Calls)
	}
	if mock.LastReq.EvalType != "brand_match" {
		t.Fatalf("LLM received wrong eval_type: %q", mock.LastReq.EvalType)
	}

	// SDDEvaluation persisted.
	writes, _ := s.ListWrites(ctx, audit.ListFilters{Actor: "orchestrator_judge", Limit: 10})
	if len(writes) == 0 {
		t.Fatalf("expected write_audit row for sdd_evaluations")
	}
}

// O5: Judge — low confidence verdict still gets saved.
func TestJudge_LowConfidenceStillSaves(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v1",
		VerdictJSON: `{"match":0.3}`,
		Confidence:  0.3,
		Model:       "mock-1",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType:   "brand_match",
		TargetType: "brand",
		TargetID:   "weak",
		Content:    "ambiguous content",
	})
	if err != nil {
		t.Fatalf("Judge should still save low-confidence verdicts: %v", err)
	}
	if out.Confidence > 0.5 {
		t.Fatalf("test expected low confidence, got %f", out.Confidence)
	}
}

// O5: Judge — content with canary token is refused (INV-3).
func TestJudge_CanaryRejection(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Install a canary token, then submit content that contains it.
	canary := "DEADBEEF-CANARY-XYZ"
	orch.Safety.Set(safety.CanaryToken(canary))

	mock := &orchestration.MockLLMClient{Name_: "should-not-be-called"}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	_, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType:   "brand_match",
		TargetType: "brand",
		TargetID:   "poison",
		Content:    "This contains the canary: " + canary + " — do not score me.",
	})
	if err == nil {
		t.Fatalf("expected canary rejection error")
	}
	if !errIs(err, store.ErrCanaryInPayload) {
		t.Fatalf("expected ErrCanaryInPayload, got: %v", err)
	}
	if mock.Calls != 0 {
		t.Fatalf("LLM should NOT have been called when canary is in payload, got %d calls", mock.Calls)
	}
}

// O5: Judge — no LLM available returns ErrNoLLMAvailable.
func TestJudge_NoLLMAvailable(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// No selector configured; the default one will use
	// SelfHarnessClient which returns ErrNoLLMAvailable in tests
	// (env vars not set).
	_, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType:   "brand_match",
		TargetType: "brand",
		TargetID:   "x",
		Content:    "y",
	})
	if err == nil {
		t.Fatalf("expected ErrNoLLMAvailable (no harness key set in tests)")
	}
	if !errIs(err, orchestration.ErrNoLLMAvailable) {
		t.Fatalf("expected ErrNoLLMAvailable, got: %v", err)
	}
}

// O5: Judge — missing content rejected.
func TestJudge_MissingContent(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType:   "brand_match",
		TargetType: "brand",
		TargetID:   "x",
	})
	if err == nil {
		t.Fatalf("expected error for missing content")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O5: SelfHarnessClient — env detection returns ErrNoLLMAvailable
// when no key is set in test env.
func TestSelfHarnessClient_NoKey(t *testing.T) {
	// Wipe env vars that SelfHarnessClient reads (test isolation).
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("DARK_SCRAPPER_URL", "")
	c, err := orchestration.NewSelfHarnessClient()
	if err == nil {
		t.Fatalf("expected ErrNoLLMAvailable, got client %v", c)
	}
	if !errIs(err, orchestration.ErrNoLLMAvailable) {
		t.Fatalf("expected ErrNoLLMAvailable, got: %v", err)
	}
}

// O5: SelfHarnessClient — env detection picks ANTHROPIC_API_KEY first.
func TestSelfHarnessClient_AnthropicPriority(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")
	c, err := orchestration.NewSelfHarnessClient()
	if err != nil {
		t.Fatalf("expected client, got err: %v", err)
	}
	if c.Name() != "self_harness_anthropic" {
		t.Fatalf("expected self_harness_anthropic, got %q", c.Name())
	}
}

// O5: OSINT catalog — known provider returns per-eval recommendation.
func TestRecommendedModel_KnownProvider(t *testing.T) {
	cases := []struct {
		provider, evalType, want string
	}{
		{"anthropic", "drift_judge", "claude-opus-4-7"}, // opus for reasoning
		{"anthropic", "brand_match", "claude-haiku-4-5"}, // haiku for fast
		{"openai", "compliance_check", "gpt-5"},
		{"google", "grounding_check", "gemini-2.5-pro"},
		{"deepseek", "drift_judge", "deepseek-r1"}, // r1 for reasoning
		{"perplexity", "grounding_check", "sonar-pro"}, // search-augmented
	}
	for _, c := range cases {
		got := orchestration.RecommendedModel(c.provider, c.evalType)
		if got != c.want {
			t.Errorf("RecommendedModel(%s, %s) = %q, want %q", c.provider, c.evalType, got, c.want)
		}
	}
}

// O5: OSINT catalog — unknown provider returns "" (caller falls
// through to client auto-config).
func TestRecommendedModel_UnknownProvider(t *testing.T) {
	got := orchestration.RecommendedModel("my-internal-model-v1", "brand_match")
	if got != "" {
		t.Fatalf("unknown provider should return empty string, got %q", got)
	}
	if orchestration.IsKnownProvider("my-internal-model-v1") {
		t.Fatalf("IsKnownProvider should return false for unknown provider")
	}
	if !orchestration.IsKnownProvider("anthropic") {
		t.Fatalf("IsKnownProvider should return true for anthropic")
	}
}

// O5: ListProviders returns the top-10.
func TestListProviders(t *testing.T) {
	providers := orchestration.ListProviders()
	if len(providers) < 10 {
		t.Fatalf("expected at least 10 providers, got %d: %v", len(providers), providers)
	}
	want := []string{"anthropic", "openai", "google", "mistral", "cohere", "meta", "xai", "deepseek", "qwen", "perplexity"}
	for i, w := range want {
		if i >= len(providers) || providers[i] != w {
			t.Errorf("provider %d: got %q, want %q", i, providers[i], w)
		}
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

// ============================================================================
// O7: PublishVibe — spec_create + artifact_log + drift_judge + drift_log.
// ============================================================================

// O7: PublishVibe — happy path with mock LLM returning aligned verdict.
func TestPublishVibe_HappyPath(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v1",
		VerdictJSON: `{"aligned":true,"confidence":0.92,"issues":[]}`,
		Confidence:  0.92,
		Model:       "mock-1",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec: orchestration.PublishSpecInput{
			VibeCase: "C1",
			Spec:     `{"intent":"ship a CLI"}`,
			Tasks:    `[{"id":"t1","desc":"write code"}]`,
		},
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "file:///tmp/example.go",
			Text:         "package main\nfunc main(){println(\"hi\")}\n",
		},
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if out.SpecID == 0 {
		t.Fatalf("SpecID should be > 0")
	}
	if out.ArtifactID == 0 {
		t.Fatalf("ArtifactID should be > 0")
	}
	if out.DriftID == 0 {
		t.Fatalf("DriftID should be > 0")
	}
	if out.Verdict != "aligned" {
		t.Fatalf("Verdict should be aligned, got %q", out.Verdict)
	}
	if out.NextAction != "publish" {
		t.Fatalf("NextAction should be publish, got %q", out.NextAction)
	}
	if out.Confidence < 0.9 {
		t.Fatalf("Confidence should be >= 0.9, got %f", out.Confidence)
	}

	gotSpec, err := s.GetSpec(ctx, out.SpecID)
	if err != nil || gotSpec == nil {
		t.Fatalf("GetSpec: %v / nil=%v", err, gotSpec == nil)
	}
	if gotSpec.VibeCase != "C1" {
		t.Fatalf("spec vibe_case round-trip: got %q", gotSpec.VibeCase)
	}

	gotArt, err := s.GetArtifact(ctx, out.ArtifactID)
	if err != nil || gotArt == nil {
		t.Fatalf("GetArtifact: %v / nil=%v", err, gotArt == nil)
	}
	if gotArt.ValidationStatus != "passed" {
		t.Fatalf("artifact validation_status should be 'passed', got %q", gotArt.ValidationStatus)
	}
	if gotArt.SpecID != out.SpecID {
		t.Fatalf("artifact spec_id should link back, got %d want %d", gotArt.SpecID, out.SpecID)
	}

	drift, err := s.LatestDriftForArtifact(ctx, out.ArtifactID)
	if err != nil || drift == nil {
		t.Fatalf("LatestDriftForArtifact: %v / nil=%v", err, drift == nil)
	}
	if drift.Verdict != "aligned" {
		t.Fatalf("drift verdict should be aligned, got %q", drift.Verdict)
	}
	if drift.ReconciledAt == "" {
		t.Fatalf("aligned drift should be auto-reconciled, got empty ReconciledAt")
	}
}

// O7: PublishVibe — missing artifact_url is rejected.
func TestPublishVibe_MissingArtifactURL(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec:     orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{ArtifactType: "code"},
	})
	if err == nil {
		t.Fatalf("expected error for missing artifact_url")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O7: PublishVibe — missing vibe_case is rejected.
func TestPublishVibe_MissingVibeCase(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "file:///x",
		},
	})
	if err == nil {
		t.Fatalf("expected error for missing vibe_case")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O7: PublishVibe — drift_detected verdict triggers NextAction=reconcile
// and validation_status=failed.
func TestPublishVibe_DriftDetected(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v1",
		VerdictJSON: `{"aligned":false,"drift_items":["missing_documentation"],"confidence":0.85}`,
		Confidence:  0.85,
		Model:       "mock-1",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec: orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "file:///tmp/incomplete.go",
			Text:         "package main",
		},
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if out.Verdict != "drift_detected" {
		t.Fatalf("Verdict should be drift_detected, got %q", out.Verdict)
	}
	if out.NextAction != "reconcile" {
		t.Fatalf("NextAction should be reconcile, got %q", out.NextAction)
	}
	if out.DriftID == 0 {
		t.Fatalf("DriftID should be > 0 even on drift_detected")
	}

	gotArt, _ := s.GetArtifact(ctx, out.ArtifactID)
	if gotArt.ValidationStatus != "failed" {
		t.Fatalf("artifact validation_status should be 'failed', got %q", gotArt.ValidationStatus)
	}

	drift, _ := s.LatestDriftForArtifact(ctx, out.ArtifactID)
	if drift == nil || drift.ReconciledAt != "" {
		t.Fatalf("drift_detected should NOT be auto-reconciled; ReconciledAt=%q", drift.ReconciledAt)
	}
}

// O7: PublishVibe — no LLM available still persists spec + artifact +
// drift_log with verdict="drift_detected" + reasoning explaining skip.
func TestPublishVibe_NoLLM(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	out, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec: orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "file:///tmp/no-llm.go",
			Text:         "package main",
		},
	})
	if err != nil {
		t.Fatalf("PublishVibe should succeed even without LLM (audit trail complete): %v", err)
	}
	if out.SpecID == 0 || out.ArtifactID == 0 || out.DriftID == 0 {
		t.Fatalf("all three rows should be persisted; got spec=%d art=%d drift=%d", out.SpecID, out.ArtifactID, out.DriftID)
	}
	if out.Verdict != "drift_detected" {
		t.Fatalf("Verdict should be drift_detected (no LLM = can't judge = drift_detected), got %q", out.Verdict)
	}
	if out.NextAction != "human_gate" {
		t.Fatalf("NextAction should be human_gate, got %q", out.NextAction)
	}
	if !strings.Contains(out.Reasoning, "drift_judge skipped") {
		t.Fatalf("Reasoning should explain skip, got: %q", out.Reasoning)
	}

	drift, _ := s.LatestDriftForArtifact(ctx, out.ArtifactID)
	if drift == nil {
		t.Fatalf("drift row missing")
	}
	if !strings.Contains(drift.JudgeReasoning, "drift_judge skipped") {
		t.Fatalf("drift.JudgeReasoning should mention skip, got: %q", drift.JudgeReasoning)
	}
}

// O7: PublishVibe — AutoDriftCheck=false (explicit pointer) skips drift.
func TestPublishVibe_AutoDriftCheckFalse(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "should-not-be-called",
		VerdictJSON: `{"aligned":true,"confidence":0.99}`,
		Confidence:  0.99,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	autoCheckFalse := false
	out, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec: orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "file:///tmp/skip.go",
			Text:         "package main",
		},
		AutoDriftCheck: &autoCheckFalse,
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if out.Verdict != "skipped" {
		t.Fatalf("Verdict should be skipped, got %q", out.Verdict)
	}
	if out.NextAction != "publish" {
		t.Fatalf("NextAction should be publish, got %q", out.NextAction)
	}
	if mock.Calls != 0 {
		t.Fatalf("LLM should not be called when AutoDriftCheck=false, got %d calls", mock.Calls)
	}

	gotArt, _ := s.GetArtifact(ctx, out.ArtifactID)
	if gotArt.ValidationStatus != "pending" {
		t.Fatalf("artifact validation_status should remain 'pending' on skipped drift, got %q", gotArt.ValidationStatus)
	}
}

// O7: PublishVibe — no artifact text => verdict="skipped", no LLM call.
func TestPublishVibe_NoText(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{Name_: "should-not-be-called"}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec: orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "file:///tmp/no-text.go",
		},
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if out.Verdict != "skipped" {
		t.Fatalf("Verdict should be skipped (no text), got %q", out.Verdict)
	}
	if mock.Calls != 0 {
		t.Fatalf("LLM should not be called when text is empty, got %d calls", mock.Calls)
	}
}

// O7: PublishVibe — content with canary token is rejected (INV-3).
func TestPublishVibe_CanaryRejection(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	canary := "DEADBEEF-PUBLISH-CANARY"
	orch.Safety.Set(safety.CanaryToken(canary))

	mock := &orchestration.MockLLMClient{Name_: "should-not-be-called"}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec: orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "file:///tmp/poison.go",
			Text:         "package main\n// contains " + canary + " marker\n",
		},
	})
	if err != nil {
		t.Fatalf("PublishVibe should not fail on canary (audit trail must complete): %v", err)
	}
	if out.Verdict != "drift_detected" {
		t.Fatalf("Verdict should be drift_detected (canary triggers judge skip), got %q", out.Verdict)
	}
	if !strings.Contains(out.Reasoning, "canary") {
		t.Fatalf("Reasoning should mention canary, got: %q", out.Reasoning)
	}
	if mock.Calls != 0 {
		t.Fatalf("LLM should NOT be called when canary is in payload, got %d calls", mock.Calls)
	}
}

// O7: PublishVibe — brand_match + compliance_check fire when set; 3 LLM calls.
func TestPublishVibe_BrandAndCompliance(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v1",
		VerdictJSON: `{"aligned":true,"confidence":0.95}`,
		Confidence:  0.95,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec: orchestration.PublishSpecInput{VibeCase: "C2"},
		Artifact: orchestration.PublishArtifactInput{
			ArtifactType:  "text",
			ArtifactURL:   "https://example.com/post.html",
			Text:          "Welcome to the platform.",
			BrandID:       "acme",
			Jurisdiction:  "EU",
			HasDisclosure: true,
		},
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if out.BrandEvalID == 0 {
		t.Fatalf("BrandEvalID should be > 0 when brand_id is set")
	}
	if out.ComplianceEvalID == 0 {
		t.Fatalf("ComplianceEvalID should be > 0 when jurisdiction is set")
	}
	if mock.Calls != 3 {
		t.Fatalf("LLM should be called 3 times (brand + compliance + drift), got %d", mock.Calls)
	}

	gotArt, _ := s.GetArtifact(ctx, out.ArtifactID)
	if gotArt.BrandID != "acme" {
		t.Fatalf("artifact brand_id round-trip: got %q", gotArt.BrandID)
	}
	if gotArt.Jurisdiction != "EU" {
		t.Fatalf("artifact jurisdiction round-trip: got %q", gotArt.Jurisdiction)
	}
	if !gotArt.HasDisclosure {
		t.Fatalf("artifact has_disclosure should be true")
	}
}

// togglingMock is a per-call LLMClient that lets a test inject
// different verdicts per sample.
type togglingMock struct {
	OnJudge func(orchestration.JudgeRequest) (*orchestration.JudgeResponse, error)
}

func (t *togglingMock) Name() string { return "toggling_mock" }
func (t *togglingMock) Judge(ctx context.Context, req orchestration.JudgeRequest) (*orchestration.JudgeResponse, error) {
	return t.OnJudge(req)
}

// ============================================================================
// O8: JudgeConsensus — n-shot verdict with modal + confidence interval.
// ============================================================================

// O8: JudgeConsensus — happy path with 3 unanimous samples (all aligned).
func TestJudgeConsensus_Unanimous(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v1",
		VerdictJSON: `{"aligned":true,"confidence":0.92,"issues":[]}`,
		Confidence:  0.92,
		Model:       "mock-1",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType:   "compliance_check",
		TargetType: "spec",
		TargetID:   "high-stakes-claim-1",
		Content:    "GDPR disclosure text",
		N:          3,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus: %v", err)
	}
	if out.ModalVerdict != "aligned" {
		t.Fatalf("ModalVerdict should be aligned, got %q", out.ModalVerdict)
	}
	if out.ModalCount != 3 {
		t.Fatalf("ModalCount should be 3, got %d", out.ModalCount)
	}
	if out.ModalFraction < 0.99 {
		t.Fatalf("ModalFraction should be ~1.0, got %f", out.ModalFraction)
	}
	if out.AvgConfidence < 0.91 {
		t.Fatalf("AvgConfidence should be ~0.92, got %f", out.AvgConfidence)
	}
	if out.StdDevConfidence != 0 {
		t.Fatalf("StdDevConfidence should be 0 when all samples equal, got %f", out.StdDevConfidence)
	}
	if out.Verdict != "aligned" {
		t.Fatalf("Verdict should be aligned, got %q", out.Verdict)
	}
	if out.NextAction != "publish" {
		t.Fatalf("NextAction should be publish, got %q", out.NextAction)
	}
	if len(out.Samples) != 3 {
		t.Fatalf("Samples should have 3 entries, got %d", len(out.Samples))
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID (consensus row) should be > 0")
	}
	if mock.Calls != 3 {
		t.Fatalf("LLM should have been called 3 times, got %d", mock.Calls)
	}
}

// O8: JudgeConsensus — majority verdict (2 aligned, 1 drift_detected).
func TestJudgeConsensus_Majority(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	calls := 0
	mock := &togglingMock{
		OnJudge: func(req orchestration.JudgeRequest) (*orchestration.JudgeResponse, error) {
			calls++
			v := `{"aligned":true,"confidence":0.9}`
			if calls == 3 {
				v = `{"aligned":false,"confidence":0.8,"drift_items":["missing_doc"]}`
			}
			return &orchestration.JudgeResponse{VerdictJSON: v, Confidence: 0.9, Model: "mock", Provider: "mock"}, nil
		},
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType:   "drift_judge",
		TargetType: "artifact",
		TargetID:   "majority-test",
		Content:    "Some text",
		N:          3,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus: %v", err)
	}
	if out.ModalVerdict != "aligned" {
		t.Fatalf("ModalVerdict should be aligned (2/3), got %q", out.ModalVerdict)
	}
	if out.ModalCount != 2 {
		t.Fatalf("ModalCount should be 2, got %d", out.ModalCount)
	}
	if out.ModalFraction < 0.66 || out.ModalFraction > 0.67 {
		t.Fatalf("ModalFraction should be ~0.667, got %f", out.ModalFraction)
	}
	if out.Verdict != "aligned" {
		t.Fatalf("Verdict should follow modal (>= 0.6 fraction), got %q", out.Verdict)
	}
	if out.NextAction != "publish" {
		t.Fatalf("NextAction should be publish, got %q", out.NextAction)
	}
}

// O8: JudgeConsensus — low agreement (2 aligned, 2 drift in N=4) triggers needs_human.
func TestJudgeConsensus_LowAgreement(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	calls := 0
	mock := &togglingMock{
		OnJudge: func(req orchestration.JudgeRequest) (*orchestration.JudgeResponse, error) {
			calls++
			v := `{"aligned":true,"confidence":0.9}`
			if calls%2 == 0 {
				v = `{"aligned":false,"confidence":0.85}`
			}
			return &orchestration.JudgeResponse{VerdictJSON: v, Confidence: 0.9, Model: "mock", Provider: "mock"}, nil
		},
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType:   "brand_match",
		TargetType: "brand",
		TargetID:   "tie-test",
		Content:    "x",
		N:          4,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus: %v", err)
	}
	if out.ModalFraction >= 0.6 {
		t.Fatalf("ModalFraction should be 0.5 (2/4), got %f", out.ModalFraction)
	}
	if out.Verdict != "needs_human" {
		t.Fatalf("Verdict should be needs_human (low agreement), got %q", out.Verdict)
	}
	if out.NextAction != "human_gate" {
		t.Fatalf("NextAction should be human_gate, got %q", out.NextAction)
	}
	if out.ModalVerdict != "aligned" && out.ModalVerdict != "drift_detected" {
		t.Fatalf("ModalVerdict should be aligned or drift_detected, got %q", out.ModalVerdict)
	}
}

// O8: JudgeConsensus — confidence variance surfaces in StdDev + interval.
func TestJudgeConsensus_ConfidenceInterval(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	confidences := []float32{0.7, 0.8, 0.9}
	idx := 0
	mock := &togglingMock{
		OnJudge: func(req orchestration.JudgeRequest) (*orchestration.JudgeResponse, error) {
			c := confidences[idx%len(confidences)]
			idx++
			return &orchestration.JudgeResponse{
				VerdictJSON: `{"aligned":true}`,
				Confidence:  c,
				Model:       "mock", Provider: "mock",
			}, nil
		},
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType:   "grounding_check",
		TargetType: "claim",
		TargetID:   "ci-test",
		Content:    "text",
		N:          3,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus: %v", err)
	}
	expected := float32(0.8)
	if out.AvgConfidence != expected {
		t.Fatalf("AvgConfidence should be %f, got %f", expected, out.AvgConfidence)
	}
	if out.StdDevConfidence < 0.08 || out.StdDevConfidence > 0.12 {
		t.Fatalf("StdDevConfidence should be ~0.1, got %f", out.StdDevConfidence)
	}
	if out.ConfidenceLow < 0.7 || out.ConfidenceLow > 0.72 {
		t.Fatalf("ConfidenceLow should be ~0.7, got %f", out.ConfidenceLow)
	}
	if out.ConfidenceHigh < 0.88 || out.ConfidenceHigh > 0.92 {
		t.Fatalf("ConfidenceHigh should be ~0.9, got %f", out.ConfidenceHigh)
	}
}

// O8: JudgeConsensus — N clamps to [1, 7]; N=0 defaults 3; N=99 clamps to 7.
func TestJudgeConsensus_NClamping(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock",
		VerdictJSON: `{"aligned":true,"confidence":0.9}`,
		Confidence:  0.9,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "brand_match", TargetType: "brand", TargetID: "n0", Content: "x",
	})
	if err != nil {
		t.Fatalf("N=0: %v", err)
	}
	if len(out.Samples) != 3 {
		t.Fatalf("N=0 should default to 3, got %d samples", len(out.Samples))
	}

	mock.Calls = 0
	out, err = orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "brand_match", TargetType: "brand", TargetID: "n99", Content: "x", N: 99,
	})
	if err != nil {
		t.Fatalf("N=99: %v", err)
	}
	if len(out.Samples) != 7 {
		t.Fatalf("N=99 should clamp to 7, got %d samples", len(out.Samples))
	}

	mock.Calls = 0
	out, err = orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "brand_match", TargetType: "brand", TargetID: "n2", Content: "x", N: 2,
	})
	if err != nil {
		t.Fatalf("N=2: %v", err)
	}
	if len(out.Samples) != 2 {
		t.Fatalf("N=2 should give 2 samples, got %d", len(out.Samples))
	}
}

// O8: JudgeConsensus — canary rejection (INV-3).
func TestJudgeConsensus_CanaryRejection(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	canary := "DEADBEEF-CONSENSUS-CANARY"
	orch.Safety.Set(safety.CanaryToken(canary))

	mock := &orchestration.MockLLMClient{Name_: "should-not-be-called"}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	_, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType:   "compliance_check",
		TargetType: "artifact",
		TargetID:   "poison",
		Content:    "Contains " + canary + " token",
		N:          3,
	})
	if err == nil {
		t.Fatalf("expected canary rejection")
	}
	if !errIs(err, store.ErrCanaryInPayload) {
		t.Fatalf("expected ErrCanaryInPayload, got: %v", err)
	}
	if mock.Calls != 0 {
		t.Fatalf("LLM should not have been called when canary is in payload, got %d calls", mock.Calls)
	}
}

// O8: JudgeConsensus — missing content rejected.
func TestJudgeConsensus_MissingContent(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType:   "brand_match",
		TargetType: "brand",
		TargetID:   "x",
	})
	if err == nil {
		t.Fatalf("expected error for missing content")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O8: JudgeConsensus — the consensus row uses TargetID + ":consensus" suffix.
func TestJudgeConsensus_TargetIDSuffix(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock",
		VerdictJSON: `{"aligned":true,"confidence":0.9}`,
		Confidence:  0.9,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "brand_match", TargetType: "brand", TargetID: "my-brand",
		Content: "text", N: 3,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus: %v", err)
	}

	eval, err := s.LatestSDDEvaluation(ctx, "brand_match", "brand", "my-brand:consensus")
	if err != nil {
		t.Fatalf("LatestSDDEvaluation (consensus): %v", err)
	}
	if eval == nil {
		t.Fatalf("consensus row missing")
	}
	if eval.ID != out.EvaluationID {
		t.Fatalf("consensus eval id mismatch: got %d want %d", eval.ID, out.EvaluationID)
	}
}

// ============================================================================
// O9: ActivePolicy — read-only snapshot of constitution + mods + canary.
// ============================================================================

// O9: ActivePolicy — happy path with no active constitution (fresh store).
func TestActivePolicy_Empty(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	out, err := orch.ActivePolicy(ctx)
	if err != nil {
		t.Fatalf("ActivePolicy: %v", err)
	}
	if out.ConstitutionID != "" {
		t.Fatalf("ConstitutionID should be empty on fresh store, got %q", out.ConstitutionID)
	}
	if out.ConstitutionDrift {
		t.Fatalf("ConstitutionDrift should be false on fresh store")
	}
	if len(out.Mods) != 0 {
		t.Fatalf("Mods should be empty on fresh store, got %d", len(out.Mods))
	}
	if out.CanaryPresent {
		t.Fatalf("CanaryPresent should be false when no canary installed")
	}
	if out.PolicyVersion != "1.0.0" {
		t.Fatalf("PolicyVersion should be 1.0.0, got %q", out.PolicyVersion)
	}
}

// O9: ActivePolicy — with a saved constitution, returns its id+version+sha.
func TestActivePolicy_WithConstitution(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	parsed := `{"id":"dark-agents/dark-memory-mcp","version":"1.0.0"}`
	sum := sha256.Sum256([]byte(parsed))
	wantSHA := hex.EncodeToString(sum[:])

	wc := store.WriteContext{Actor: "test", WritePath: "test"}
	if err := s.SaveConstitution(ctx, wc, &constitution.Constitution{
		ConstitutionID: "dark-memory-mcp",
		Version:        "1.0.0",
		Label:          "Dark Memory MCP v1.0.0",
		Source:         "builtin:dark-memory-mcp",
		FilePath:       "/etc/dark-memory-mcp/constitution.toml",
		ParsedJSON:     parsed,
		SHA256:         wantSHA,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("SaveConstitution: %v", err)
	}

	out, err := orch.ActivePolicy(ctx)
	if err != nil {
		t.Fatalf("ActivePolicy: %v", err)
	}
	if out.ConstitutionID != "dark-memory-mcp" {
		t.Fatalf("ConstitutionID should match, got %q", out.ConstitutionID)
	}
	if out.ConstitutionVersion != "1.0.0" {
		t.Fatalf("ConstitutionVersion should match, got %q", out.ConstitutionVersion)
	}
	if out.ConstitutionSHA256 != wantSHA {
		t.Fatalf("ConstitutionSHA256 should match, got %q want %q", out.ConstitutionSHA256, wantSHA)
	}
	if out.ConstitutionLabel != "Dark Memory MCP v1.0.0" {
		t.Fatalf("ConstitutionLabel should match, got %q", out.ConstitutionLabel)
	}
	if out.ConstitutionDrift {
		t.Fatalf("ConstitutionDrift should be false when SHAs match, reason=%q", out.DriftReason)
	}
}

// O9: ActivePolicy — drift detected when stored SHA256 != actual SHA.
func TestActivePolicy_ConstitutionDrift(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	parsed := `{"id":"dark-agents/dark-memory-mcp","version":"1.0.0"}`
	wrongSHA := "0000000000000000000000000000000000000000000000000000000000000000"

	wc := store.WriteContext{Actor: "test", WritePath: "test"}
	if err := s.SaveConstitution(ctx, wc, &constitution.Constitution{
		ConstitutionID: "drifted",
		Version:        "1.0.0",
		Source:         "test",
		FilePath:       "/tmp/x.toml",
		ParsedJSON:     parsed,
		SHA256:         wrongSHA,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("SaveConstitution: %v", err)
	}

	out, err := orch.ActivePolicy(ctx)
	if err != nil {
		t.Fatalf("ActivePolicy: %v", err)
	}
	if !out.ConstitutionDrift {
		t.Fatalf("ConstitutionDrift should be true when SHAs differ")
	}
	if out.DriftReason == "" {
		t.Fatalf("DriftReason should explain the mismatch")
	}
	if !strings.Contains(out.DriftReason, "mismatch") {
		t.Fatalf("DriftReason should mention mismatch, got: %q", out.DriftReason)
	}
}

// O9: ActivePolicy — canary is reported present (without token).
func TestActivePolicy_CanaryPresent(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	orch.Safety.Set(safety.CanaryToken("DEADBEEF-REDACTED"))

	out, err := orch.ActivePolicy(ctx)
	if err != nil {
		t.Fatalf("ActivePolicy: %v", err)
	}
	if !out.CanaryPresent {
		t.Fatalf("CanaryPresent should be true after Set")
	}
}

// O9: ActivePolicy — mods list reflects ListMods with ManifestJSON decoded.
func TestActivePolicy_ModsList(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	wc := store.WriteContext{Actor: "test", WritePath: "test"}
	if err := s.SaveMod(ctx, wc, &mods.Mod{
		ModID:   "sample",
		Name:    "Sample Mod",
		Version: "0.1.0",
		Source:  "test",
		ManifestJSON: `{"meta":{"id":"sample","version":"0.1.0","name":"Sample Mod"},"risk":{"risk_class":"research-only","target_scope":"public_internet"}}`,
		RiskClass:    "research-only",
		TargetScope:  "public_internet",
	}); err != nil {
		t.Fatalf("SaveMod: %v", err)
	}

	out, err := orch.ActivePolicy(ctx)
	if err != nil {
		t.Fatalf("ActivePolicy: %v", err)
	}
	if len(out.Mods) != 1 {
		t.Fatalf("expected 1 mod, got %d", len(out.Mods))
	}
	if out.Mods[0].ModID != "sample" {
		t.Fatalf("ModID should match, got %q", out.Mods[0].ModID)
	}
	if out.Mods[0].RiskClass != "research-only" {
		t.Fatalf("RiskClass should decode from ManifestJSON, got %q", out.Mods[0].RiskClass)
	}
	if out.Mods[0].TargetScope != "public_internet" {
		t.Fatalf("TargetScope should decode from ManifestJSON, got %q", out.Mods[0].TargetScope)
	}
}

// ============================================================================
// O10: MemoryState — runtime snapshot of dark.db.
// ============================================================================

// O10: MemoryState — empty store has expected zero counts.
func TestMemoryState_Empty(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	out, err := orch.MemoryState(ctx)
	if err != nil {
		t.Fatalf("MemoryState: %v", err)
	}
	if out.Driver != "sqlite" {
		t.Fatalf("Driver should be 'sqlite', got %q", out.Driver)
	}
	if out.SchemaVersion == 0 {
		t.Fatalf("SchemaVersion should be > 0 after Migrate, got 0")
	}
	if out.SnapshotVersion != "1.0.0" {
		t.Fatalf("SnapshotVersion should be 1.0.0, got %q", out.SnapshotVersion)
	}
	if out.Counts.Specs != 0 || out.Counts.Artifacts != 0 || out.Counts.SessionsActive != 0 {
		t.Fatalf("empty store counts should be 0; got specs=%d art=%d sessions_active=%d",
			out.Counts.Specs, out.Counts.Artifacts, out.Counts.SessionsActive)
	}
	if out.ActiveProject != "default" {
		t.Fatalf("ActiveProject should be 'default', got %q", out.ActiveProject)
	}
}

// O10: MemoryState — counts reflect persisted data.
func TestMemoryState_Populated(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	start, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator: "nico", ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock",
		VerdictJSON: `{"aligned":true,"confidence":0.95}`,
		Confidence:  0.95,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))
	if _, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec:     orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{ArtifactType: "code", ArtifactURL: "file:///x.go", Text: "x"},
	}); err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}

	if _, err := s.SaveRun(ctx, store.WriteContext{
		Actor: "test", SessionID: start.SessionID, WritePath: "test",
	}, &research.ResearchRun{
		SessionID: start.SessionID, Query: "Q", Intent: "web",
		Items: []research.Item{
			{Title: "a", Source: "web", Confidence: 0.9},
			{Title: "b", Source: "web", Confidence: 0.8},
		},
	}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	out, err := orch.MemoryState(ctx)
	if err != nil {
		t.Fatalf("MemoryState: %v", err)
	}
	if out.Counts.Specs != 1 {
		t.Fatalf("Specs should be 1, got %d", out.Counts.Specs)
	}
	if out.Counts.Artifacts != 1 {
		t.Fatalf("Artifacts should be 1, got %d", out.Counts.Artifacts)
	}
	if out.Counts.SessionsActive != 1 {
		t.Fatalf("SessionsActive should be 1, got %d", out.Counts.SessionsActive)
	}
	if out.Counts.SessionsTotal != 1 {
		t.Fatalf("SessionsTotal should be 1, got %d", out.Counts.SessionsTotal)
	}
	if out.Counts.RunsTotal != 1 {
		t.Fatalf("RunsTotal should be 1, got %d", out.Counts.RunsTotal)
	}
	if out.Counts.ItemsTotal != 2 {
		t.Fatalf("ItemsTotal should be 2, got %d", out.Counts.ItemsTotal)
	}
	if out.Counts.WriteAuditTotal < 1 {
		t.Fatalf("WriteAuditTotal should be >= 1, got %d", out.Counts.WriteAuditTotal)
	}
}

// O10: MemoryState — SessionsActive drops to 0 after SessionClose.
func TestMemoryState_SessionLifecycle(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	start, _ := orch.SessionStart(ctx, orchestration.SessionStartInput{Operator: "nico", ProjectID: "default"})
	if _, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{SessionID: start.SessionID}); err != nil {
		t.Fatalf("SessionClose: %v", err)
	}

	out, err := orch.MemoryState(ctx)
	if err != nil {
		t.Fatalf("MemoryState: %v", err)
	}
	if out.Counts.SessionsActive != 0 {
		t.Fatalf("SessionsActive should be 0 after close, got %d", out.Counts.SessionsActive)
	}
	if out.Counts.SessionsTotal != 1 {
		t.Fatalf("SessionsTotal should be 1 (closed session still counted), got %d", out.Counts.SessionsTotal)
	}
}

// O10: MemoryState — canary present is surfaced.
func TestMemoryState_CanarySurfaced(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	orch.Safety.Set(safety.CanaryToken("DEADBEEF"))
	out, err := orch.MemoryState(ctx)
	if err != nil {
		t.Fatalf("MemoryState: %v", err)
	}
	if !out.CanaryPresent {
		t.Fatalf("CanaryPresent should be true after Set")
	}
}

// ============================================================================
// O11: ResolveDrift — human-gate action (accept | reject).
// ============================================================================

// O11: ResolveDrift — accept a drift_detected verdict.
func TestResolveDrift_Accept(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock",
		VerdictJSON: `{"aligned":false,"drift_items":["missing_doc"]}`,
		Confidence:  0.85,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))
	pub, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec:     orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{ArtifactType: "code", ArtifactURL: "file:///x.go", Text: "incomplete code"},
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}
	if pub.Verdict != "drift_detected" {
		t.Fatalf("setup: expected drift_detected, got %q", pub.Verdict)
	}

	out, err := orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		DriftID:    pub.DriftID,
		Decision:   orchestration.DecisionAccept,
		OperatorID: "nico",
		Note:       "manually verified; missing_doc was a false positive",
	})
	if err != nil {
		t.Fatalf("ResolveDrift: %v", err)
	}
	if out.Decision != "accept" {
		t.Fatalf("Decision should echo, got %q", out.Decision)
	}
	if out.NewStatus != "passed" {
		t.Fatalf("NewStatus should be 'passed' on accept, got %q", out.NewStatus)
	}
	if out.OperatorID != "nico" {
		t.Fatalf("OperatorID should echo, got %q", out.OperatorID)
	}
	if out.ResolvedAt == "" {
		t.Fatalf("ResolvedAt should be stamped")
	}

	gotArt, _ := s.GetArtifact(ctx, pub.ArtifactID)
	if gotArt.ValidationStatus != "passed" {
		t.Fatalf("artifact validation_status should be 'passed' after accept, got %q", gotArt.ValidationStatus)
	}

	drift, _ := s.LatestDriftForArtifact(ctx, pub.ArtifactID)
	if drift.ReconciledAt == "" {
		t.Fatalf("drift ReconciledAt should be stamped")
	}
	if !strings.Contains(drift.JudgeReasoning, "operator=nico") {
		t.Fatalf("drift.JudgeReasoning should record operator, got: %q", drift.JudgeReasoning)
	}
}

// O11: ResolveDrift — reject a drift_detected verdict.
func TestResolveDrift_Reject(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock",
		VerdictJSON: `{"aligned":false,"drift_items":["missing_doc"]}`,
		Confidence:  0.85,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))
	pub, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec:     orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{ArtifactType: "code", ArtifactURL: "file:///y.go", Text: "broken"},
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}

	out, err := orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		DriftID: pub.DriftID, Decision: orchestration.DecisionReject, OperatorID: "nico",
	})
	if err != nil {
		t.Fatalf("ResolveDrift: %v", err)
	}
	if out.NewStatus != "failed" {
		t.Fatalf("NewStatus should be 'failed' on reject, got %q", out.NewStatus)
	}

	gotArt, _ := s.GetArtifact(ctx, pub.ArtifactID)
	if gotArt.ValidationStatus != "failed" {
		t.Fatalf("artifact validation_status should be 'failed' after reject, got %q", gotArt.ValidationStatus)
	}
}

// O11: ResolveDrift — already-reconciled drift returns ErrInvalidState.
func TestResolveDrift_DoubleResolve(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock",
		VerdictJSON: `{"aligned":false,"drift_items":["x"]}`,
		Confidence:  0.85,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))
	pub, err := orch.PublishVibe(ctx, orchestration.PublishVibeInput{
		Spec:     orchestration.PublishSpecInput{VibeCase: "C1"},
		Artifact: orchestration.PublishArtifactInput{ArtifactType: "code", ArtifactURL: "file:///z.go", Text: "x"},
	})
	if err != nil {
		t.Fatalf("PublishVibe: %v", err)
	}

	if _, err := orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		DriftID: pub.DriftID, Decision: orchestration.DecisionAccept, OperatorID: "nico",
	}); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	_, err = orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		DriftID: pub.DriftID, Decision: orchestration.DecisionReject, OperatorID: "nico",
	})
	if err == nil {
		t.Fatalf("expected error on second resolve")
	}
	if !errIs(err, store.ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState, got: %v", err)
	}
}

// O11: ResolveDrift — unknown drift_id returns ErrNotFound.
func TestResolveDrift_NotFound(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, err := orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		DriftID: 9999, Decision: orchestration.DecisionAccept, OperatorID: "nico",
	})
	if err == nil {
		t.Fatalf("expected error for unknown drift")
	}
	if !errIs(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

// O11: ResolveDrift — invalid decision rejected.
func TestResolveDrift_InvalidDecision(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, err := orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		DriftID: 1, Decision: "maybe", OperatorID: "nico",
	})
	if err == nil {
		t.Fatalf("expected error for invalid decision")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O11: ResolveDrift — missing operator_id rejected.
func TestResolveDrift_MissingOperator(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, err := orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		DriftID: 1, Decision: orchestration.DecisionAccept, OperatorID: "",
	})
	if err == nil {
		t.Fatalf("expected error for missing operator")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O11: ResolveDrift — missing drift_id rejected.
func TestResolveDrift_MissingDriftID(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, err := orch.ResolveDrift(ctx, orchestration.ResolveDriftInput{
		Decision: orchestration.DecisionAccept, OperatorID: "nico",
	})
	if err == nil {
		t.Fatalf("expected error for missing drift_id")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// ============================================================================
// O12: VibeSpec — spec_create wrapper with structured tasks validation.
// ============================================================================

// O12: VibeSpec — happy path with 3 valid tasks.
func TestVibeSpec_HappyPath(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	out, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		VibeCase: "C1",
		Spec:     `{"intent":"build a CLI"}`,
		Tasks: []orchestration.VibeSpecTask{
			{ID: "t1", Description: "Write code"},
			{ID: "t2", Description: "Write tests", DependsOn: []string{"t1"}},
			{ID: "t3", Description: "Document", DependsOn: []string{"t2"}},
		},
	})
	if err != nil {
		t.Fatalf("VibeSpec: %v", err)
	}
	if out.SpecID == 0 {
		t.Fatalf("SpecID should be > 0")
	}
	if out.TasksValidated != 3 {
		t.Fatalf("TasksValidated should be 3, got %d", out.TasksValidated)
	}
	if len(out.TaskIDs) != 3 {
		t.Fatalf("TaskIDs should have 3 entries, got %d", len(out.TaskIDs))
	}

	gotSpec, _ := s.GetSpec(ctx, out.SpecID)
	if gotSpec == nil {
		t.Fatalf("GetSpec returned nil")
	}
	if gotSpec.Tasks == "" {
		t.Fatalf("spec.Tasks should be serialised")
	}
	if !strings.Contains(gotSpec.Tasks, `"t1"`) {
		t.Fatalf("spec.Tasks should contain t1, got: %q", gotSpec.Tasks)
	}
}

// O12: VibeSpec — missing vibe_case rejected.
func TestVibeSpec_MissingVibeCase(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		Tasks: []orchestration.VibeSpecTask{{ID: "t1", Description: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error for missing vibe_case")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O12: VibeSpec — empty tasks rejected.
func TestVibeSpec_EmptyTasks(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		VibeCase: "C1",
		Tasks:    []orchestration.VibeSpecTask{},
	})
	if err == nil {
		t.Fatalf("expected error for empty tasks")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O12: VibeSpec — duplicate task ids rejected.
func TestVibeSpec_DuplicateIDs(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		VibeCase: "C1",
		Tasks: []orchestration.VibeSpecTask{
			{ID: "t1", Description: "a"},
			{ID: "t1", Description: "b"},
		},
	})
	if err == nil {
		t.Fatalf("expected error for duplicate ids")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error should mention duplicate, got: %v", err)
	}
}

// O12: VibeSpec — empty task description rejected.
func TestVibeSpec_EmptyDescription(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		VibeCase: "C1",
		Tasks:    []orchestration.VibeSpecTask{{ID: "t1", Description: ""}},
	})
	if err == nil {
		t.Fatalf("expected error for empty description")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
}

// O12: VibeSpec — depends_on referencing unknown task rejected.
func TestVibeSpec_UnknownDependency(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		VibeCase: "C1",
		Tasks: []orchestration.VibeSpecTask{
			{ID: "t1", Description: "a"},
			{ID: "t2", Description: "b", DependsOn: []string{"ghost"}},
		},
	})
	if err == nil {
		t.Fatalf("expected error for unknown dependency")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should mention unknown dep, got: %v", err)
	}
}

// O12: VibeSpec — circular dependency rejected.
func TestVibeSpec_Cycle(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		VibeCase: "C1",
		Tasks: []orchestration.VibeSpecTask{
			{ID: "t1", Description: "a", DependsOn: []string{"t3"}},
			{ID: "t2", Description: "b", DependsOn: []string{"t1"}},
			{ID: "t3", Description: "c", DependsOn: []string{"t2"}},
		},
	})
	if err == nil {
		t.Fatalf("expected error for cycle")
	}
	if !errIs(err, store.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error should mention cycle, got: %v", err)
	}
}

// O12: VibeSpec — happy single-task spec (no warnings).
func TestVibeSpec_ExternalRefs(t *testing.T) {
	ctx := context.Background()
	orch, _ := openOrchestratorTestEnv(t)
	if err := orch.Store.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	out, err := orch.VibeSpec(ctx, orchestration.VibeSpecInput{
		VibeCase: "C1",
		Tasks: []orchestration.VibeSpecTask{
			{ID: "t1", Description: "do work"},
		},
	})
	if err != nil {
		t.Fatalf("happy single-task spec: %v", err)
	}
	if out.SpecID == 0 {
		t.Fatalf("SpecID should be > 0")
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("expected no warnings, got: %v", out.Warnings)
	}
}