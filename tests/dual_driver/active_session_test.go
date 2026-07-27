// Package dual_driver_test — active_session_test.go: roundtrip tests
// for the v17 ActiveSessionTracking methods on the Store interface.
//
// The Postgres driver is stubbed on this host (production runs
// SQLite per dark.db), so this test only runs against SQLite. The
// Postgres path is exercised when DARK_TEST_POSTGRES_DSN is set.
package dual_driver_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// freshActiveSessionStore opens a fresh SQLite-backed Store with the
// default project created + active. Returned to callers for active
// session roundtrip tests.
func freshActiveSessionStore(t *testing.T, ctx context.Context) store.Store {
	t.Helper()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("create default: %v", err)
	}
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set default active: %v", err)
	}
	return s
}

// TestSQLiteStore_ActiveSession_Roundtrip covers Set / Get / Clear
// for the v17 ActiveSessionTracking methods. Each test step
// asserts the post-condition directly on the Store (no
// orchestrator layer involved) so a regression in the SQL or the
// CAS semantics is caught here.
func TestSQLiteStore_ActiveSession_Roundtrip(t *testing.T) {
	ctx := context.Background()
	s := freshActiveSessionStore(t, ctx)

	// Step 1: Get on a fresh store returns "".
	got, err := s.GetActiveSession(ctx, "default")
	if err != nil {
		t.Fatalf("Get on fresh store: %v", err)
	}
	if got != "" {
		t.Errorf("Get on fresh store: want empty string, got %q", got)
	}

	// Step 2: Set populates the column.
	if err := s.SetActiveSession(ctx, "default", "sess-AAA"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, _ = s.GetActiveSession(ctx, "default")
	if got != "sess-AAA" {
		t.Errorf("after Set sess-AAA: want %q, got %q", "sess-AAA", got)
	}

	// Step 3: Set is overwrite-on-write (last session_start wins).
	if err := s.SetActiveSession(ctx, "default", "sess-BBB"); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, _ = s.GetActiveSession(ctx, "default")
	if got != "sess-BBB" {
		t.Errorf("after Set sess-BBB: want %q, got %q", "sess-BBB", got)
	}

	// Step 4: Idempotent Set (same id) is fine.
	if err := s.SetActiveSession(ctx, "default", "sess-BBB"); err != nil {
		t.Fatalf("Set idempotent: %v", err)
	}
	got, _ = s.GetActiveSession(ctx, "default")
	if got != "sess-BBB" {
		t.Errorf("after idempotent Set: want %q, got %q", "sess-BBB", got)
	}
}

// TestSQLiteStore_ActiveSession_CAS validates the compare-and-set
// semantics on Clear: clearing only succeeds when the current
// active session matches expectedSessionID. A stale clear (where
// the active session has been replaced) is a no-op, not an error.
func TestSQLiteStore_ActiveSession_CAS(t *testing.T) {
	ctx := context.Background()
	s := freshActiveSessionStore(t, ctx)

	// Setup: Set sess-XYZ as the active.
	if err := s.SetActiveSession(ctx, "default", "sess-XYZ"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Stale clear: expected "sess-old", current is "sess-XYZ" → no-op.
	if err := s.ClearActiveSession(ctx, "default", "sess-old"); err != nil {
		t.Fatalf("stale clear should not error: %v", err)
	}
	got, _ := s.GetActiveSession(ctx, "default")
	if got != "sess-XYZ" {
		t.Errorf("stale clear should NOT have removed sess-XYZ; got %q", got)
	}

	// Correct clear: expected "sess-XYZ", current is "sess-XYZ" → cleared.
	if err := s.ClearActiveSession(ctx, "default", "sess-XYZ"); err != nil {
		t.Fatalf("matching clear: %v", err)
	}
	got, _ = s.GetActiveSession(ctx, "default")
	if got != "" {
		t.Errorf("matching clear should have removed sess-XYZ; got %q", got)
	}

	// Subsequent clear of a stale id is still a no-op.
	if err := s.ClearActiveSession(ctx, "default", "sess-XYZ"); err != nil {
		t.Fatalf("second stale clear: %v", err)
	}
}

// TestSQLiteStore_ActiveSession_RaceNewSessionWins models the
// "session_start while session_close is mid-flight" race that
// motivated the compare-and-set design. The newer session_start
// must win; the older session_close becomes a no-op.
func TestSQLiteStore_ActiveSession_RaceNewSessionWins(t *testing.T) {
	ctx := context.Background()
	s := freshActiveSessionStore(t, ctx)

	// Setup: sess-OLD is the active one.
	if err := s.SetActiveSession(ctx, "default", "sess-OLD"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Concurrent operator: starts a new session (wins the active
	// pointer), then closes the OLD session (must no-op).
	if err := s.SetActiveSession(ctx, "default", "sess-NEW"); err != nil {
		t.Fatalf("SetActiveSession sess-NEW: %v", err)
	}
	// OLD's close arrives late:
	if err := s.ClearActiveSession(ctx, "default", "sess-OLD"); err != nil {
		t.Fatalf("late clear: %v", err)
	}
	// Active should still be sess-NEW (older close was no-op).
	got, _ := s.GetActiveSession(ctx, "default")
	if got != "sess-NEW" {
		t.Errorf("race resolution: want sess-NEW, got %q", got)
	}
}

// TestSQLiteStore_ActiveSession_ProjectNotFound verifies that Set
// returns the typed ErrProjectNotFound for non-existent project_ids
// — important because session_start's SetActiveProject precedes
// SetActiveSession and a stale SetActiveProject would otherwise
// silently write a dangling pointer.
func TestSQLiteStore_ActiveSession_ProjectNotFound(t *testing.T) {
	ctx := context.Background()
	s := freshActiveSessionStore(t, ctx)
	err := s.SetActiveSession(ctx, "non-existent", "sess-X")
	if err == nil {
		t.Fatalf("SetActiveSession on non-existent project: want error, got nil")
	}
	if !errors.Is(err, store.ErrProjectNotFound) {
		t.Errorf("want store.ErrProjectNotFound, got %v", err)
	}
}
