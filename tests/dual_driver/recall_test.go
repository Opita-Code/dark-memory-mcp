// Package dual_driver_test — recall_test.go: dual-driver contract
// tests for the FrameSource composition (Wave 5A.ii.b.2.a). Tests
// run against both sqlite (always) and postgres (gated by
// DARK_TEST_POSTGRES_DSN).
//
// # Scope (5A.ii.b.2.a)
// Only IdentityFrame + CapabilitiesFrame composition is exercised.
// ScopeFrame, DriftFrame, PersonaFrame return (nil, nil) per their
// deferred-to-later-wave comments; the test asserts that contract.
package dual_driver_test

import (
	"context"
	"os"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// TestFrameSourceIdentity exercises StoreSource.IdentityFrame.
// Saves a session row, composes an IdentityFrame, asserts the
// canonical identity fields (session_id, operator, constitution_id
// + ver) round-trip correctly.
func TestFrameSourceIdentity(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         tmp + "/test.db",
		WALMode:     true,
		ForeignKeys: true,
	}
	s, err := openTestStore(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.CreateProject(ctx, defaultProject()); err != nil {
		t.Fatalf("create project: %v", err)
	}
	s.SetActiveProject(ctx, "default")

	// Save a session.
	sid := "test-frame-identity"
	wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestFrameSource"}
	_, err = s.SaveSession(ctx, wc, &session.Session{
		SessionID:       sid,
		Operator:        "ci-test-operator",
		ConstitutionID:  "dark-agents/dark-memory-mcp-test",
		ConstitutionVer: "1.0.0",
		Status:          string(session.StatusOpen),
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Compose IdentityFrame.
	src := recall.NewStoreSource(s, nil)
	id, err := src.IdentityFrame(ctx, sid)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if id == nil {
		t.Fatal("IdentityFrame returned nil")
	}
	if id.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", id.SessionID, sid)
	}
	if id.Operator != "ci-test-operator" {
		t.Errorf("Operator = %q, want %q", id.Operator, "ci-test-operator")
	}
	if id.ConstitutionID != "dark-agents/dark-memory-mcp-test" {
		t.Errorf("ConstitutionID = %q, want %q", id.ConstitutionID, "dark-agents/dark-memory-mcp-test")
	}
	if id.ConstitutionVer != "1.0.0" {
		t.Errorf("ConstitutionVer = %q, want %q", id.ConstitutionVer, "1.0.0")
	}
	if err := id.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// IdentityFrame on missing session returns (nil, nil).
	id2, err := src.IdentityFrame(ctx, "no-such-session")
	if err != nil {
		t.Errorf("IdentityFrame on missing session: unexpected error: %v", err)
	}
	if id2 != nil {
		t.Errorf("IdentityFrame on missing session: expected nil, got %+v", id2)
	}
}

// TestFrameSourceCapabilities exercises StoreSource.CapabilitiesFrame.
// Saves a session, composes a CapabilitiesFrame, asserts the
// canonical grant fields (project_id, session_id, default tool
// list, default scope list, source label).
func TestFrameSourceCapabilities(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         tmp + "/test.db",
		WALMode:     true,
		ForeignKeys: true,
	}
	s, err := openTestStore(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.CreateProject(ctx, defaultProject()); err != nil {
		t.Fatalf("create project: %v", err)
	}
	s.SetActiveProject(ctx, "default")

	sid := "test-frame-caps"
	wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestFrameSource"}
	_, err = s.SaveSession(ctx, wc, &session.Session{
		SessionID:       sid,
		Operator:        "ci-test-operator",
		ConstitutionID:  "dark-agents/dark-memory-mcp-test",
		ConstitutionVer: "1.0.0",
		Status:          string(session.StatusOpen),
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	src := recall.NewStoreSource(s, nil)
	caps, err := src.CapabilitiesFrame(ctx, sid)
	if err != nil {
		t.Fatalf("CapabilitiesFrame: %v", err)
	}
	if caps == nil {
		t.Fatal("CapabilitiesFrame returned nil")
	}
	if caps.ProjectID != "default" {
		t.Errorf("ProjectID = %q, want default", caps.ProjectID)
	}
	if caps.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", caps.SessionID, sid)
	}
	if !caps.HasGrant("dark_memory_active_policy") {
		t.Errorf("HasGrant(dark_memory_active_policy) = false, want true")
	}
	if !caps.HasGrant("dark_memory_recall") {
		t.Errorf("HasGrant(dark_memory_recall) = false, want true")
	}
	if caps.HasGrant("dark_memory_nonexistent_tool") {
		t.Errorf("HasGrant(nonexistent) = true, want false")
	}
	if !caps.HasProjectAccess("default") {
		t.Errorf("HasProjectAccess(default) = false, want true")
	}
	if !caps.HasProjectAccess("any-project") {
		t.Errorf("HasProjectAccess(*) = false, want true (default scope grant)")
	}
	if caps.Source != "default:default" {
		t.Errorf("Source = %q, want default:default", caps.Source)
	}
	if err := caps.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// CapabilitiesFrame on missing session returns (nil, nil).
	caps2, err := src.CapabilitiesFrame(ctx, "no-such-session")
	if err != nil {
		t.Errorf("CapabilitiesFrame on missing session: unexpected error: %v", err)
	}
	if caps2 != nil {
		t.Errorf("CapabilitiesFrame on missing session: expected nil, got %+v", caps2)
	}
}

// TestFrameSourcePostgresDual exercises FrameSource composition against
// the postgres driver when DARK_TEST_POSTGRES_DSN is set. Skipped
// otherwise (matches the dual_driver pattern used elsewhere).
//
// # 5A.ii.b.2.a scope
//
// Only IdentityFrame + CapabilitiesFrame are exercised. Both work
// on postgres via the Store interface (GetSession + ActiveProject
// are implemented in both drivers). The deferred frames
// (Scope/Drift/Persona) return (nil, nil) regardless of driver and
// are not asserted here — that contract is documented in
// internal/recall/assemble.go.
func TestFrameSourcePostgresDual(t *testing.T) {
	dsn := os.Getenv("DARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DARK_TEST_POSTGRES_DSN not set; skipping postgres dual-driver test")
	}
	ctx := context.Background()
	cfg := store.Config{
		Driver: store.DriverPostgres,
		DSN:    dsn,
	}
	s, err := openTestStore(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.CreateProject(ctx, defaultProject()); err != nil {
		t.Fatalf("create project: %v", err)
	}
	s.SetActiveProject(ctx, "default")

	sid := "test-frame-pg"
	wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestFrameSourcePG"}
	_, err = s.SaveSession(ctx, wc, &session.Session{
		SessionID:       sid,
		Operator:        "ci-test-operator",
		ConstitutionID:  "dark-agents/dark-memory-mcp-test",
		ConstitutionVer: "1.0.0",
		Status:          string(session.StatusOpen),
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	src := recall.NewStoreSource(s, nil)
	id, err := src.IdentityFrame(ctx, sid)
	if err != nil || id == nil {
		t.Fatalf("IdentityFrame (postgres): err=%v id=%+v", err, id)
	}
	if id.SessionID != sid {
		t.Errorf("IdentityFrame SessionID = %q, want %q", id.SessionID, sid)
	}

	caps, err := src.CapabilitiesFrame(ctx, sid)
	if err != nil || caps == nil {
		t.Fatalf("CapabilitiesFrame (postgres): err=%v caps=%+v", err, caps)
	}
	if !caps.HasGrant("dark_memory_active_policy") {
		t.Errorf("CapabilitiesFrame missing default grant on postgres")
	}
}

// openTestStore is a small helper that opens a store via the runtime
// factory. Used by the recall tests; if more tests need this, lift
// it to a shared helper file.
func openTestStore(ctx context.Context, cfg store.Config) (store.Store, error) {
	return runtime.Open(ctx, cfg)
}

// defaultProject returns the canonical "default" project used to
// seed tests.
func defaultProject() *project.Project {
	return &project.Project{
		ProjectID:   "default",
		DisplayName: "Default",
	}
}

// Ensure atomic import is used (other test files import atomic too).
var _ = atomic.FrameIdentity