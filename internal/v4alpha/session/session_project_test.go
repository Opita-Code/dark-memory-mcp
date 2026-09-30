// Phase 4 Chunk 4.3 — session.Store project_id validation tests.
//
// Three tests cover the INV-7 hard isolation at the session
// boundary:
//
//  1. TestSessionStart_UnknownProject_Rejected — Start fails
//     fast with ErrUnknownProject (no session row inserted).
//  2. TestSessionStart_KnownProject_Accepted — Start succeeds
//     after a project is registered.
//  3. TestSessionStart_NilValidatorAcceptsAny — backwards
//     compat: when SetProjectsForTest is NOT called, validation
//     is skipped (pre-Phase-4 contract preserved).
package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// stubValidator is a ProjectValidator that responds with the
// configured map. Used by TestSessionStart_* to drive the
// validation paths without spinning up the full project package.
type stubValidator struct {
	known map[string]bool
}

func (s stubValidator) Lookup(ctx context.Context, projectID string) (any, error) {
	if s.known[projectID] {
		return &project.Project{ProjectID: projectID, DisplayName: projectID}, nil
	}
	return nil, project.ErrProjectNotFound
}

// newStoreWithValidator opens the DB and wires the validator.
func newStoreWithValidator(t *testing.T, v session.ProjectValidator) *session.Store {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := session.CreateSchema(db); err != nil {
		t.Fatalf("session.CreateSchema: %v", err)
	}
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project.ApplyProjectIDColumns: %v", err)
	}
	w := audit.NewWriter(db)
	s := session.NewStore(db, w)
	if v != nil {
		s.SetProjectsForTest(v)
	}
	return s
}

func TestSessionStart_UnknownProject_Rejected(t *testing.T) {
	v := stubValidator{known: map[string]bool{"proj-known": true}}
	s := newStoreWithValidator(t, v)
	ctx := context.Background()

	_, err := s.Start(ctx, "operator-nico", "proj-unknown")
	if err == nil {
		t.Fatal("Start with unknown project_id succeeded; want ErrUnknownProject")
	}
	if !errors.Is(err, session.ErrUnknownProject) {
		t.Errorf("err = %v; want errors.Is(ErrUnknownProject) match", err)
	}
}

func TestSessionStart_KnownProject_Accepted(t *testing.T) {
	v := stubValidator{known: map[string]bool{"proj-huila": true}}
	s := newStoreWithValidator(t, v)
	ctx := context.Background()

	sess, err := s.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.ProjectID != "proj-huila" {
		t.Errorf("ProjectID = %q; want proj-huila", sess.ProjectID)
	}
}

func TestSessionStart_NilValidatorAcceptsAny(t *testing.T) {
	// When SetProjectsForTest is NOT called (legacy path),
	// validation is skipped so pre-Phase-4 callers keep working.
	s := newStoreWithValidator(t, nil)
	ctx := context.Background()

	sess, err := s.Start(ctx, "operator-nico", "anything-goes")
	if err != nil {
		t.Fatalf("Start with nil validator failed: %v", err)
	}
	if sess.ProjectID != "anything-goes" {
		t.Errorf("ProjectID = %q; want anything-goes", sess.ProjectID)
	}
}
