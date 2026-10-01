// Package recall_test — assemble_final_test.go: Phase 9 Chunk 8.6
// (final extension). Covers the last 3 reachable branches in
// StoreSource before the package reaches ≥95% coverage.
//
// # Branches covered
//
//   - IdentityFrame (assemble.go:161) branch at 178-180: session has
//     empty constitution fields → fall back to ActiveConstitution.
//   - ScopeFrame (assemble.go:199) branch at 222-224: state has empty
//     LastVerdict → zero the LastDriftAt (ErrScopeVerdictWithoutTime
//     mitigation).
//   - CapabilitiesFrame (assemble.go:242) branch at 247-248: sess nil
//     → return (nil, nil).
//
// The remaining gaps after this file are defensive paths with
// unreachable branches:
//   - cache.go: Render/Hash failures (IdentityFrame.Render/Hash
//     cannot fail on JSON-safe struct types)
//   - cache.go: CapabilitiesFrame / IdentityFrame persist-error
//     capture paths (requires Store mock where GetFrame succeeds
//     and SaveFrame fails)
//   - cache.go: recordCacheErr.Logger.Printf fallback
//     (SaveErrorEvent swallows errors internally → serr is never
//     non-nil in practice)

package recall_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newFinalTestStore is a copy of newPersonaTestStore — separate name
// for clarity in the test grouping. Same SQLite-in-tempdir pattern.
func newFinalTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "final-test.db"),
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
	return st, func() { _ = st.Close() }
}

// === Test 1: IdentityFrame session constitution empty → ActiveConstitution fallback ===
//
// Branch: assemble.go:178-180. Session row has empty
// constitution_id+ver; an active constitution row exists. IdentityFrame
// must use the active constitution (else NewIdentityFrame returns
// ErrEmptyConstitutionID and the gate refuses every tool call).

func TestIdentityFrame_EmptySessionConstitution_FallsBackToActive(t *testing.T) {
	st, cleanup := newFinalTestStore(t)
	defer cleanup()

	const cID = "dark-agents/test-identity-active"
	const cVer = "1.0.0"
	if err := st.SaveConstitution(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&constitution.Constitution{
			ConstitutionID: cID,
			Version:       cVer,
			Label:         "test-identity-active",
			Source:        "test",
			ParsedJSON:    `{}`,
			Enabled:       true,
		}); err != nil {
		t.Fatalf("SaveConstitution: %v", err)
	}

	// Session with empty constitution fields → IdentityFrame must
	// resolve to the active constitution.
	id := "sess-identity-fallback-" + time.Now().Format("150405.000000000")
	if _, err := st.SaveSession(context.Background(),
		store.WriteContext{ProjectID: "default", Actor: "nico"},
		&session.Session{
			SessionID: id,
			Operator:  "nico",
			Status:    "open",
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
			// ConstitutionID + ConstitutionVer deliberately empty.
		}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	src := recall.NewStoreSource(st, nil)
	frame, err := src.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("IdentityFrame returned nil; expected active-constitution fallback frame")
	}
	if frame.ConstitutionID != cID {
		t.Errorf("ConstitutionID = %q; want %q (from ActiveConstitution)", frame.ConstitutionID, cID)
	}
	if frame.ConstitutionVer != cVer {
		t.Errorf("ConstitutionVer = %q; want %q", frame.ConstitutionVer, cVer)
	}
}

// === Test 2: ScopeFrame LastVerdict empty → zero LastDriftAt ================
//
// Branch: assemble.go:222-224. State row has LastVerdict="" but
// UpdatedAt non-zero (a session that just started or never received
// a drift verdict). NewScopeFrame rejects non-zero LastDriftAt
// paired with empty LastDriftVerdict (ErrScopeVerdictWithoutTime);
// IdentityFrame must zero the timestamp to compose cleanly.

func TestScopeFrame_LastVerdictEmpty_ZeroesLastDriftAt(t *testing.T) {
	st, cleanup := newFinalTestStore(t)
	defer cleanup()

	id := "sess-scope-no-verdict-" + time.Now().Format("150405.000000000")
	if _, err := st.SaveSession(context.Background(),
		store.WriteContext{ProjectID: "default", Actor: "nico"},
		&session.Session{
			SessionID: id,
			Operator:  "nico",
			Status:    "open",
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// VLP state with State != 0, LastVerdict="", UpdatedAt non-zero.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.SaveVLPState(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&store.VLPStateRow{
			SessionID:  id,
			State:      3, // active
			LastVerdict: "", // empty — no drift run yet
			UpdatedAt:  now,
			ProjectID:  "default",
		}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	src := recall.NewStoreSource(st, nil)
	frame, err := src.ScopeFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("ScopeFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("ScopeFrame returned nil; expected frame with zeroed LastDriftAt")
	}
	if frame.LastDriftVerdict != "" {
		t.Errorf("LastDriftVerdict = %q; want empty", frame.LastDriftVerdict)
	}
	if !frame.LastDriftAt.IsZero() {
		t.Errorf("LastDriftAt = %v; want zero (empty verdict branch zeroes the timestamp)",
			frame.LastDriftAt)
	}
}

// === Test 3: CapabilitiesFrame sess nil → return (nil, nil) ==================
//
// Branch: assemble.go:247-248. When the session doesn't exist,
// CapabilitiesFrame returns (nil, nil) — same pattern as IdentityFrame
// and PersonaFrame.

func TestCapabilitiesFrame_NoSession_ReturnsNil(t *testing.T) {
	st, cleanup := newFinalTestStore(t)
	defer cleanup()

	src := recall.NewStoreSource(st, nil)
	frame, err := src.CapabilitiesFrame(context.Background(), "missing-session-id")
	if err != nil {
		t.Fatalf("CapabilitiesFrame: %v", err)
	}
	if frame != nil {
		t.Errorf("CapabilitiesFrame returned non-nil; expected (nil, nil) for missing session")
	}
}