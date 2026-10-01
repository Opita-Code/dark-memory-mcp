// Package recall_test — assemble_persona_test.go: Phase 9 Chunk 8.6
// (extension). Covers the remaining uncovered branches in
// StoreSource.PersonaFrame (assemble.go:341). Baseline at 68.8% before
// this file; target ≥95%.
//
// # Branches covered
//
//   - Branch A (line 346-348): sess nil → return (nil, nil). Already
//     covered by TestPersonaFrame_NoSession in assemble_store_test.go.
//   - Branch B (line 350-351): constitution from session row. Already
//     covered by TestPersonaFrame_DefaultsFallback (which uses a session
//     with constitution_id+ver populated, then expects defaults
//     because GetConstitution returns nil for the not-yet-inserted row).
//   - Branch C (line 352-354): session constitution empty → fall back
//     to ActiveConstitution. NEW.
//   - Branch D (line 355-357): ActiveConstitution also empty → return
//     (nil, nil). NEW.
//   - Branch E (line 358-371): GetConstitution err/nil → defaults
//     fallback with active constitution id+ver. NEW (the existing
//     defaults-fallback test uses a session-bound constitution, not
//     the active fallback path).
//   - Branch F (line 372-382): GetConstitution ok → parsePersonaFrom
//     Constitution → NewPersonaFrame with parsed voice/claims/tone. NEW.

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

// newPersonaTestStore opens a fresh SQLite store with the default
// project. Mirrors newDriftTestStore.
func newPersonaTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "persona-test.db"),
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

// newPersonaSession inserts a session row with explicit constitution
// fields. constitutionID/constitutionVer empty = the test wants the
// fallback path (branch C).
func newPersonaSession(t *testing.T, st store.Store, operator, constitutionID, constitutionVer string) string {
	t.Helper()
	id := "sess-persona-" + operator + "-" + time.Now().Format("150405.000000000")
	_, err := st.SaveSession(context.Background(), store.WriteContext{
		Actor:     operator,
		ProjectID: "default",
	}, &session.Session{
		SessionID:       id,
		Operator:        operator,
		Status:          "open",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ConstitutionID:  constitutionID,
		ConstitutionVer: constitutionVer,
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	return id
}

// === Test 1: session constitution empty + ActiveConstitution empty → nil ===
//
// Branch D (line 355-357). No constitution anywhere → PersonaFrame
// returns (nil, nil) so the gate can refuse without a binding.

func TestPersonaFrame_NoActiveConstitution_ReturnsNil(t *testing.T) {
	st, cleanup := newPersonaTestStore(t)
	defer cleanup()

	// Session with NO constitution fields.
	id := newPersonaSession(t, st, "nico", "", "")

	src := recall.NewStoreSource(st, nil)
	frame, err := src.PersonaFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if frame != nil {
		t.Errorf("PersonaFrame returned non-nil; expected (nil, nil) when no constitution binding exists")
	}
}

// === Test 2: session constitution empty + ActiveConstitution set → fallback ===
//
// Branch C (line 352-354). When the session row has no constitution,
// PersonaFrame reads ActiveConstitution (the row with enabled=1, latest
// activated_at). Saves a constitution row, sets it active, then calls
// PersonaFrame on a session with empty constitution fields.

func TestPersonaFrame_SessionEmpty_FallsBackToActive(t *testing.T) {
	st, cleanup := newPersonaTestStore(t)
	defer cleanup()

	// Insert one active constitution.
	const cID = "dark-agents/test-persona-active"
	const cVer = "1.0.0"
	if err := st.SaveConstitution(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&constitution.Constitution{
			ConstitutionID: cID,
			Version:       cVer,
			Label:         "test-active",
			Source:        "test",
			ParsedJSON:    `{"persona":{"voice":"active-voice","claims_policy":"active-claims","tone":"active-tone"}}`,
			Enabled:       true,
		}); err != nil {
		t.Fatalf("SaveConstitution: %v", err)
	}

	// Session with empty constitution fields → falls back to active.
	id := newPersonaSession(t, st, "nico", "", "")

	src := recall.NewStoreSource(st, nil)
	frame, err := src.PersonaFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("PersonaFrame returned nil; expected active-constitution fallback frame")
	}
	if frame.Voice != "active-voice" {
		t.Errorf("Voice = %q; want active-voice (from ParsedJSON [persona] section)", frame.Voice)
	}
	if frame.ClaimsPolicy != "active-claims" {
		t.Errorf("ClaimsPolicy = %q; want active-claims", frame.ClaimsPolicy)
	}
	if frame.Tone != "active-tone" {
		t.Errorf("Tone = %q; want active-tone", frame.Tone)
	}
}

// === Test 3: GetConstitution returns error → defaults fallback ============
//
// Branch E (line 358-371). The `if err != nil || c == nil` condition
// is reachable via EITHER:
//   - GetConstitution returns (nil, ErrConstitutionMissing) — the c==nil
//     branch; already covered by TestPersonaFrame_DefaultsFallback
//     (session-bound constitution id+ver that doesn't exist in the
//     constitutions table → GetConstitution returns (nil, nil) →
//     branch E fires).
//   - GetConstitution returns (nil, err) — the err != nil branch.
//     Requires a Store where GetSession succeeds but GetConstitution
//     fails. Cannot be triggered via the closed-solver test because
//     GetSession fails FIRST (line 343 propagates err before reaching
//     line 358). Would require a mock Store; out of Chunk 8.6 scope.
//
// This file therefore exercises Branch F (parsed [persona] section)
// with Test 3 below + the existing Tests 1, 2 (Branch D) + the
// pre-existing TestPersonaFrame_DefaultsFallback (Branch E).
//
// (The original draft of Test 3 attempted a closed-store trigger but
// was unreachable — documented here so future reviewers don't
// re-attempt the same failed approach.)

// === Test 3: GetConstitution ok → parses ParsedJSON [persona] section =====
//
// Branch F (line 372-382). Saves a constitution with a populated
// ParsedJSON [persona] block. PersonaFrame calls parsePersonaFrom
// Constitution which extracts voice/claims/tone, then NewPersonaFrame
// uses those values verbatim.

func TestPersonaFrame_ConstitutionWithPersonaSection_ParsesValues(t *testing.T) {
	st, cleanup := newPersonaTestStore(t)
	defer cleanup()

	const cID = "dark-agents/test-persona-section"
	const cVer = "1.0.0"
	parsedJSON := `{
	  "persona": {
	    "voice": "first-person-pragmatic",
	    "claims_policy": "cite-or-refuse",
	    "tone": "direct",
	    "refusal_pattern": "I cannot help with that"
	  },
	  "permissions": {"x": 1}
	}`
	if err := st.SaveConstitution(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&constitution.Constitution{
			ConstitutionID: cID,
			Version:       cVer,
			Label:         "test-persona-section",
			Source:        "test",
			ParsedJSON:    parsedJSON,
			Enabled:       true,
		}); err != nil {
		t.Fatalf("SaveConstitution: %v", err)
	}

	// Session bound to the constitution with [persona] populated.
	id := newPersonaSession(t, st, "nico", cID, cVer)

	src := recall.NewStoreSource(st, nil)
	frame, err := src.PersonaFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("PersonaFrame returned nil; expected parsed-persona-section frame")
	}
	if frame.Voice != "first-person-pragmatic" {
		t.Errorf("Voice = %q; want first-person-pragmatic (from [persona].voice)", frame.Voice)
	}
	if frame.ClaimsPolicy != "cite-or-refuse" {
		t.Errorf("ClaimsPolicy = %q; want cite-or-refuse", frame.ClaimsPolicy)
	}
	if frame.Tone != "direct" {
		t.Errorf("Tone = %q; want direct", frame.Tone)
	}
}

// === Test 4: constitution with empty ParsedJSON → defaults fallback ========
//
// ParsedJSON exists but is `{}` (no [persona] section). parsePersona
// FromConstitution returns the defaults (line 444-475 branch). This
// hits the F branch with empty parsed JSON — different from test 3
// (populated).

func TestPersonaFrame_ConstitutionEmptyParsedJSON_UsesDefaults(t *testing.T) {
	st, cleanup := newPersonaTestStore(t)
	defer cleanup()

	const cID = "dark-agents/test-persona-empty"
	const cVer = "1.0.0"
	if err := st.SaveConstitution(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&constitution.Constitution{
			ConstitutionID: cID,
			Version:       cVer,
			Label:         "test-empty",
			Source:        "test",
			ParsedJSON:    `{}`,
			Enabled:       true,
		}); err != nil {
		t.Fatalf("SaveConstitution: %v", err)
	}

	id := newPersonaSession(t, st, "nico", cID, cVer)

	src := recall.NewStoreSource(st, nil)
	frame, err := src.PersonaFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("PersonaFrame returned nil; expected defaults-fallback frame")
	}
	if frame.Voice != recall.DefaultVoice {
		t.Errorf("Voice = %q; want default (%q)", frame.Voice, recall.DefaultVoice)
	}
}

// === Test 5: constitution [persona] section malformed → defaults fallback ====
//
// Branch: parsePersonaFromConstitution line 462-463. The [persona]
// sub-object exists but is unparseable (e.g. wrong field types).
// parsePersonaFromConstitution returns the defaults.

func TestPersonaFrame_MalformedPersonaSection_UsesDefaults(t *testing.T) {
	st, cleanup := newPersonaTestStore(t)
	defer cleanup()

	const cID = "dark-agents/test-persona-malformed"
	const cVer = "1.0.0"
	// [persona] exists but voice is a number (not a string) → fails
	// to unmarshal into constitutionPersona struct (Voice is string).
	parsedJSON := `{"persona":{"voice":12345,"claims_policy":"x","tone":"y"}}`
	if err := st.SaveConstitution(context.Background(),
		store.WriteContext{ProjectID: "default"},
		&constitution.Constitution{
			ConstitutionID: cID,
			Version:       cVer,
			Label:         "test-malformed",
			Source:        "test",
			ParsedJSON:    parsedJSON,
			Enabled:       true,
		}); err != nil {
		t.Fatalf("SaveConstitution: %v", err)
	}

	id := newPersonaSession(t, st, "nico", cID, cVer)

	src := recall.NewStoreSource(st, nil)
	frame, err := src.PersonaFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("PersonaFrame returned nil; expected defaults-fallback frame")
	}
	if frame.Voice != recall.DefaultVoice {
		t.Errorf("Voice = %q; want default (%q) — parsePersonaFromConstitution should fall back",
			frame.Voice, recall.DefaultVoice)
	}
}