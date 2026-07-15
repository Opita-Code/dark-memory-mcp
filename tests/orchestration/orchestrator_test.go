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
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
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