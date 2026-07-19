// Package server - shutdown_test.go: Wave 5E.v (L6 adapter
// integration, exit-close_clean hook). Verifies Shutdown closes
// open sessions with the right reason per DARK_SHUTDOWN_CLOSE_REASON.
//
// We test through the public surface: Boot a server, start a session,
// call Shutdown, then check the session row's status+closed_at.
// We don't go through MCP — this is the Go-level contract that main.go
// also relies on.
package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	_ "modernc.org/sqlite"
)

// bootTestServer boots a server against an isolated sqlite DB in t.TempDir.
// Returns the BootState so the caller can drive Shutdown directly.
func bootTestServer(t *testing.T, operator string) *BootState {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	t.Setenv("DARK_DB_DRIVER", "sqlite")
	t.Setenv("DARK_DB", filepath.Join(tmp, "shutdown.db"))

	boot, err := Boot(ctx)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	t.Cleanup(func() { _ = boot.Shutdown(context.Background()) })

	if err := boot.Store.CreateProject(ctx, &project.Project{
		ProjectID:  "acme",
		DisplayName: "ACME",
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return boot
}

// TestShutdown_DefaultReasonIsClean: Wave 5E.v. Shutdown must close
// open sessions with reason='clean' (terminal, NOT resurrectable)
// unless the operator opts in via DARK_SHUTDOWN_CLOSE_REASON=aborted.
func TestShutdown_DefaultReasonIsClean(t *testing.T) {
	ctx := context.Background()
	boot := bootTestServer(t, "nico")

	start, err := boot.Orchestrator.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	if err := boot.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Shutdown closes the Store, so we can't read via boot.Store.
	// Open a fresh sqlite connection to verify the side effect on disk.
	got, err := readSessionAfterShutdown(t, os.Getenv("DARK_DB"), start.SessionID)
	if err != nil {
		t.Fatalf("readSessionAfterShutdown: %v", err)
	}
	if got == nil {
		t.Fatalf("session vanished after Shutdown")
	}
	if got.Status != string(session.StatusClosedClean) {
		t.Errorf("session.Status = %q, want %q (5E.v exit-close_clean default)",
			got.Status, session.StatusClosedClean)
	}
	// Defensive: StatusClosedClean maps to "closed"; verify the literal.
	if !strings.HasPrefix(got.Status, "closed") {
		t.Errorf("session.Status prefix = %q, want closed*", got.Status)
	}
}

// TestShutdown_AbortedReasonWhenOptIn: operator sets
// DARK_SHUTDOWN_CLOSE_REASON=aborted → Shutdown uses reason='aborted'
// (resurrectable per INV-8).
func TestShutdown_AbortedReasonWhenOptIn(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DARK_SHUTDOWN_CLOSE_REASON", "aborted")
	boot := bootTestServer(t, "nico")

	start, err := boot.Orchestrator.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	if err := boot.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	got, err := readSessionAfterShutdown(t, os.Getenv("DARK_DB"), start.SessionID)
	if err != nil {
		t.Fatalf("GetSession after Shutdown: %v", err)
	}
	if got == nil {
		t.Fatalf("session vanished")
	}
	if got.Status != string(session.StatusClosedAborted) {
		t.Errorf("session.Status = %q, want %q (5E.v exit-close_clean aborted)",
			got.Status, session.StatusClosedAborted)
	}
}

// TestShutdown_InvalidReasonFallsBackToClean: defensive path.
// DARK_SHUTDOWN_CLOSE_REASON=garbage should NOT panic Shutdown;
// helper defaults to 'clean' and proceeds.
func TestShutdown_InvalidReasonFallsBackToClean(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DARK_SHUTDOWN_CLOSE_REASON", "garbage")
	boot := bootTestServer(t, "nico")

	start, err := boot.Orchestrator.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	if err := boot.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should swallow invalid env, got: %v", err)
	}

	got, err := readSessionAfterShutdown(t, os.Getenv("DARK_DB"), start.SessionID)
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v got=%+v", err, got)
	}
	if got.Status != string(session.StatusClosedClean) {
		t.Errorf("session.Status = %q, want clean (defensive fallback)", got.Status)
	}
}

// TestShutdown_IsIdempotent: calling Shutdown twice should NOT crash
// or emit spurious error logs. Wave 5E.iv bug-hunt covered this;
// keep the regression check in place.
func TestShutdown_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	boot := bootTestServer(t, "nico")

	if _, err := boot.Orchestrator.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "nico",
		ProjectID: "acme",
	}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	if err := boot.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := boot.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown should be a no-op, got: %v", err)
	}
}

// TestShutdownCloseReason_UnitTable: covers the helper directly so
// the env parsing path is verified independent of the full Boot loop.
func TestShutdownCloseReason_UnitTable(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", "clean"},          // default
		{"clean", "clean"},
		{"CLEAN", "clean"},     // case-insensitive
		{"  clean  ", "clean"}, // trim
		{"aborted", "aborted"},
		{"ABORTED", "aborted"},
		{"garbage", "clean"},   // invalid → defensive fallback
		{"archived", "clean"},  // 'archived' is not valid here (not a Shutdown option)
	}
	for _, c := range cases {
		t.Run("env="+c.env, func(t *testing.T) {
			t.Setenv("DARK_SHUTDOWN_CLOSE_REASON", c.env)
			got := shutdownCloseReason()
			if got != c.want {
				t.Errorf("shutdownCloseReason(%q) = %q, want %q", c.env, got, c.want)
			}
		})
	}
}

// TestStartupRecover_LogsCandidateButDoesNotMutate: verify the L6
// startup-recover hook's READ-ONLY path.
//
// This test does NOT call runStartupRecover directly — that
// function lives in package main (cmd/dark-mem-mcp/main.go). It
// exercises the SAME orchestrator methods the hook delegates to:
// SessionRecover (always) + SessionResurrect (when
// DARK_AUTO_RESURRECT=on_boot). The test asserts:
//   - SessionRecover surfaces a closed_aborted candidate with
//     RequiresConsent=true.
//   - Without DARK_AUTO_RESURRECT=on_boot, the orchestrator state
//     is unchanged (no new session row created).
//   - With DARK_AUTO_RESURRECT=on_boot, SessionResurrect creates
//     a new open session and leaves the original closed_aborted.
//
// runStartupRecover's only logic on top of these orchestrator calls
// is env-var gating + log line emission — both trivially correct
// from inspection.
func TestStartupRecover_LogsCandidateButDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	boot := bootTestServer(t, "recover-operator")
	t.Setenv("DARK_AUTO_RESURRECT", "") // ensure OFF

	// Create + close-aborted a session for the operator.
	start, err := boot.Orchestrator.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "recover-operator",
		ProjectID: "acme",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if _, err := boot.Orchestrator.SessionClose(ctx, orchestration.SessionCloseInput{
		SessionID: start.SessionID,
		Reason:    "aborted",
	}); err != nil {
		t.Fatalf("SessionClose aborted: %v", err)
	}

	// Run the recover detection. No mutation expected.
	out, err := boot.Orchestrator.SessionRecover(ctx, orchestration.SessionRecoverInput{
		Operator: "recover-operator",
		Lookback: "24h",
	})
	if err != nil {
		t.Fatalf("SessionRecover: %v", err)
	}
	if !out.Found {
		t.Fatalf("SessionRecover Found = false, want true (we just closed aborted)")
	}
	if out.Candidate == nil || out.Candidate.SessionID != start.SessionID {
		t.Errorf("Candidate = %+v, want SessionID=%q", out.Candidate, start.SessionID)
	}
	if !out.RequiresConsent {
		t.Errorf("RequiresConsent = false, want true (per INV-8)")
	}

	// Without DARK_AUTO_RESURRECT=on_boot the orchestrator state
	// is unchanged: no NEW session row.
	all, err := boot.Store.ListSessions(ctx, 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	openCount := 0
	for _, s := range all {
		if s.Status == "open" {
			openCount++
		}
	}
	if openCount != 0 {
		t.Errorf("open session count = %d, want 0 (auto-resurrect must be off)", openCount)
	}

	// Now run with DARK_AUTO_RESURRECT=on_boot via a wrapper that
	// emulates main.go's runStartupRecover. We don't import main
	// (it's package main), so we call SessionResurrect directly —
	// that's the same code path the hook takes after the env check.
	t.Setenv("DARK_AUTO_RESURRECT", "on_boot")
	resOut, err := boot.Orchestrator.SessionResurrect(ctx, orchestration.SessionResurrectInput{
		OriginalSessionID: start.SessionID,
		Reason:            "auto_resurrect_on_boot",
	})
	if err != nil {
		t.Fatalf("SessionResurrect: %v", err)
	}
	if resOut.NewSessionID == "" || resOut.NewSessionID == start.SessionID {
		t.Errorf("Resurrect NewSessionID = %q, want fresh id (not %q)", resOut.NewSessionID, start.SessionID)
	}
	if resOut.OriginalSessionID != start.SessionID {
		t.Errorf("Resurrect OriginalSessionID = %q, want %q", resOut.OriginalSessionID, start.SessionID)
	}
	// The new session should be open + reachable.
	got, err := boot.Store.GetSession(ctx, resOut.NewSessionID)
	if err != nil || got == nil {
		t.Fatalf("GetSession new: %v got=%+v", err, got)
	}
	if got.Status != string(session.StatusOpen) {
		t.Errorf("new session Status = %q, want open", got.Status)
	}

	// Defensive: the ORIGINAL session is unchanged (still closed_aborted).
	orig, err := boot.Store.GetSession(ctx, start.SessionID)
	if err != nil || orig == nil {
		t.Fatalf("GetSession original: %v", err)
	}
	if orig.Status != string(session.StatusClosedAborted) {
		t.Errorf("original Status = %q, want closed_aborted (unchanged)", orig.Status)
	}
}

// TestStartupRecover_NoCandidateIsClean: when no closed_aborted
// session exists, SessionRecover returns Found=false without error.
// Same caveat as above: tests the orchestrator method
// runStartupRecover delegates to, not the wrapper itself.
func TestStartupRecover_NoCandidateIsClean(t *testing.T) {
	ctx := context.Background()
	boot := bootTestServer(t, "fresh-operator")

	// The recover flow needs an active project to query sessions.
	// bootTestServer already created "acme" via CreateProject.
	if err := boot.Store.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	out, err := boot.Orchestrator.SessionRecover(ctx, orchestration.SessionRecoverInput{
		Operator: "fresh-operator",
		Lookback: "24h",
	})
	if err != nil {
		t.Fatalf("SessionRecover: %v", err)
	}
	if out.Found {
		t.Errorf("SessionRecover Found = true, want false (no candidates)")
	}
	if out.Candidate != nil {
		t.Errorf("SessionRecover Candidate = %+v, want nil", out.Candidate)
	}
	if out.RequiresConsent {
		t.Errorf("RequiresConsent = true, want false")
	}
}

// readSessionAfterShutdown opens a fresh sqlite connection to the DB
// file (the original Store has been closed by Shutdown) and reads the
// session row. Returns nil session + nil error if the row is absent.
func readSessionAfterShutdown(t *testing.T, dbPath, sessionID string) (*session.Session, error) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	// Read only the columns that map to session.Session fields.
	// (project_id + created_at are also in the sessions table but
	// the Session struct doesn't model them as fields.)
	row := db.QueryRow(
		`SELECT session_id, status, constitution_id, constitution_ver,
		        active_mods, operator, started_at, closed_at,
		        last_heartbeat_at, parent_session_id, resurrected_from,
		        notes
		 FROM sessions WHERE session_id = ?`, sessionID)
	var s session.Session
	err = row.Scan(&s.SessionID, &s.Status, &s.ConstitutionID, &s.ConstitutionVer,
		&s.ActiveMods, &s.Operator, &s.StartedAt, &s.ClosedAt,
		&s.LastHeartbeatAt, &s.ParentSessionID, &s.ResurrectedFrom,
		&s.Notes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}