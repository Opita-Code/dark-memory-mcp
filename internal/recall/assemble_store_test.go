// Package recall_test — assemble_store_test.go: real-SQLite Store tests
// for StoreSource methods (Phase 6 Chunk 6.4, mutation coverage close).
//
// Coverage targets:
//   - StoreSource.NewStoreSource (init / now=nil / custom now)
//   - StoreSource.IdentityFrame (sess + constitution + fallback)
//   - StoreSource.ScopeFrame (state + drift verdict timestamp zeroing)
//   - StoreSource.CapabilitiesFrame (sess + scope grants)
//   - StoreSource.DriftFrame (no state / no eval / state=0 / state present)
//
// We use real SQLite (not a hand-rolled mock) because store.Store has
// 105+ methods and stubbing each one is fragile. SQLite in temp
// directory gives us a working Store for ~30 lines of setup.
//
// Pure functions parseTimestamp / parseSDDVerdict / parsePersonaFromConstitution /
// truncate already have 100% coverage via assemble_pure_test.go.
package recall_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newTestStore opens a fresh SQLite Store in a temp directory with
// the "default" project active.
func newTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	cleanup := func() { _ = st.Close() }
	return st, cleanup
}

// newSessionID inserts a minimal session row using a fixed ID.
func newSessionID(t *testing.T, st store.Store, operator string) string {
	t.Helper()
	ctx := context.Background()
	id := "sess-" + operator + "-" + time.Now().Format("150405.000000000")
	_, err := st.SaveSession(ctx, store.WriteContext{
		Actor:     operator,
		ProjectID: "default",
	}, &session.Session{
		SessionID:       id,
		Operator:        operator,
		Status:          "open",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ConstitutionID:  "dark-agents/dark-mem",
		ConstitutionVer: "1.0.0",
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	return id
}

// === NewStoreSource ====================================================

func TestNewStoreSource_NilNow(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	src := recall.NewStoreSource(st, nil)
	if src == nil {
		t.Fatal("NewStoreSource returned nil")
	}
	if src.Now == nil {
		t.Error("NewStoreSource did not default Now to time.Now when nil")
	}
}

func TestNewStoreSource_CustomNow(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	fixed := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	src := recall.NewStoreSource(st, func() time.Time { return fixed })
	if src == nil {
		t.Fatal("NewStoreSource returned nil")
	}
	if !src.Now().Equal(fixed) {
		t.Errorf("Now() = %v; want %v", src.Now(), fixed)
	}
}

// === IdentityFrame =====================================================

func TestIdentityFrame_SessionWithConstitution(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	id := newSessionID(t, st, "nico")
	src := recall.NewStoreSource(st, nil)
	frame, err := src.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("IdentityFrame returned nil for valid session")
	}
	if frame.Actor != "nico" {
		t.Errorf("Actor = %q; want nico", frame.Actor)
	}
	if frame.Operator != "nico" {
		t.Errorf("Operator = %q; want nico", frame.Operator)
	}
}

func TestIdentityFrame_NoSession(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	src := recall.NewStoreSource(st, nil)
	frame, err := src.IdentityFrame(context.Background(), "missing")
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if frame != nil {
		t.Error("IdentityFrame should return nil for missing session")
	}
}

// === ScopeFrame ======================================================

func TestScopeFrame_VerdictWithTimestamp(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	id := newSessionID(t, st, "nico")

	now := time.Now().UTC()
	if _, err := st.SaveVLPState(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&store.VLPStateRow{
			SessionID:   id,
			State:       3,
			LastEvent:   "vibe_publish",
			LastVerdict: "aligned",
			UpdatedAt:   now.Format(time.RFC3339Nano),
			CreatedAt:   now.Format(time.RFC3339Nano),
			OpenSpecID:  42,
			ProjectID:   "default",
		}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	src := recall.NewStoreSource(st, nil)
	frame, err := src.ScopeFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("ScopeFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("ScopeFrame returned nil for valid state")
	}
}

// TestScopeFrame_ZeroTimestampWhenNoVerdict verifies the spec 1200
// contract fix: last_verdict="" but updated_at set → zero the
// timestamp.
func TestScopeFrame_ZeroTimestampWhenNoVerdict(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	id := newSessionID(t, st, "nico")

	now := time.Now().UTC()
	if _, err := st.SaveVLPState(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&store.VLPStateRow{
			SessionID:   id,
			State:       3,
			LastEvent:   "session_start",
			LastVerdict: "",
			UpdatedAt:   now.Format(time.RFC3339Nano),
			CreatedAt:   now.Format(time.RFC3339Nano),
			OpenSpecID:  0,
			ProjectID:   "default",
		}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	src := recall.NewStoreSource(st, nil)
	frame, err := src.ScopeFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("ScopeFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("ScopeFrame returned nil for empty verdict fallback")
	}
}

func TestScopeFrame_NoState(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	src := recall.NewStoreSource(st, nil)
	frame, err := src.ScopeFrame(context.Background(), "missing")
	if err != nil {
		t.Fatalf("ScopeFrame: %v", err)
	}
	if frame != nil {
		t.Error("ScopeFrame should return nil for missing state")
	}
}

// === CapabilitiesFrame ===============================================

func TestCapabilitiesFrame_SessionPresent(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	id := newSessionID(t, st, "nico")

	src := recall.NewStoreSource(st, nil)
	frame, err := src.CapabilitiesFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("CapabilitiesFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("CapabilitiesFrame returned nil for valid session")
	}
}

func TestCapabilitiesFrame_NoSession(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	src := recall.NewStoreSource(st, nil)
	frame, err := src.CapabilitiesFrame(context.Background(), "missing")
	if err != nil {
		t.Fatalf("CapabilitiesFrame: %v", err)
	}
	if frame != nil {
		t.Error("CapabilitiesFrame should return nil for missing session")
	}
}

// === DriftFrame =======================================================

func TestDriftFrame_NoState(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	src := recall.NewStoreSource(st, nil)
	frame, err := src.DriftFrame(context.Background(), "missing")
	if err != nil {
		t.Fatalf("DriftFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("DriftFrame should not return nil; the zero-state frame is valid")
	}
}

func TestDriftFrame_StateZero(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	id := newSessionID(t, st, "nico")

	if _, err := st.SaveVLPState(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&store.VLPStateRow{
			SessionID: id,
			State:     0,
			ProjectID: "default",
		}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	src := recall.NewStoreSource(st, nil)
	frame, err := src.DriftFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("DriftFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("DriftFrame returned nil for State=0 path")
	}
}

func TestDriftFrame_NoEvaluations(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	id := newSessionID(t, st, "nico")

	if _, err := st.SaveVLPState(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&store.VLPStateRow{
			SessionID: id,
			State:     3,
			ProjectID: "default",
		}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	src := recall.NewStoreSource(st, nil)
	frame, err := src.DriftFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("DriftFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("DriftFrame returned nil for empty-evaluations path")
	}
}

// === PersonaFrame ====================================================

// TestPersonaFrame_DefaultsFallback — exercises the happy path: session
// has constitution fields, but no Constitution row was inserted into
// the store. PersonaFrame returns the defaults-fallback frame
// (line 360 in assemble.go).
func TestPersonaFrame_DefaultsFallback(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	id := newSessionID(t, st, "nico")

	src := recall.NewStoreSource(st, nil)
	frame, err := src.PersonaFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if frame == nil {
		t.Error("PersonaFrame returned nil; expected defaults-fallback frame")
	}
}

func TestPersonaFrame_NoSession(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	src := recall.NewStoreSource(st, nil)
	frame, err := src.PersonaFrame(context.Background(), "missing")
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if frame != nil {
		t.Error("PersonaFrame should return nil for missing session")
	}
}

// === NewSingleton =====================================================

func TestNewSingleton_NilStore(t *testing.T) {
	_, err := recall.NewSingleton(nil, nil, nil)
	if err == nil {
		t.Fatal("NewSingleton should error on nil store")
	}
}

func TestNewSingleton_ValidStore(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	src, err := recall.NewSingleton(st, nil, nil)
	if err != nil {
		t.Fatalf("NewSingleton: %v", err)
	}
	if src == nil {
		t.Fatal("NewSingleton returned nil source")
	}
}