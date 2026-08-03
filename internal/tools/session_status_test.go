// Package tools (internal): session_status closing_soon unit tests
// (row 168, 2026-08-03). Same-package test so it can reach the
// unexported sessionStatusFromSession + resolveClosingSoonConfig
// helpers directly. Wire-level (BindStore) coverage of the new
// fields lives in tests/orchestration/agent_memory_wire_update_test.go
// + tests/conformance/bridge7_*.
//
// Row 168 was filed 2026-08-02 describing the symptom:
//
//	Sessions auto-close after ~5 minutes of zero tool activity;
//	ErrFrameStaleTooFar hits subsequent writes during long
//	reasoning pauses. 3 wasted restarts in one synthesis session.
//
// Suggested fix (adopted here): surface the warning in
// session_status so harnesses can refresh a session BEFORE the
// sweeper closes it. Implementation at internal/tools/session.go:
//
//	ClosingSoon bool   `json:"closing_soon,omitempty"`
//	SecondsUntilClose int `json:"seconds_until_close,omitempty"`
//
// Deadline = last_heartbeat_at + heartbeat_timeout (env
// DARK_SESSION_HEARTBEAT_TIMEOUT, default 300s). closing_soon=true
// when seconds_until_close <= threshold (env
// DARK_SESSION_CLOSING_SOON_THRESHOLD, default 30s).
package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/session"
)

// sessionStatusJSONRoundTrip marshals + unmarshals the result so
// the wire-shape contract (json tags + omitempty) is exercised
// alongside the helper logic.
func sessionStatusJSONRoundTrip(t *testing.T, r *SessionStatusResult) SessionStatusResult {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SessionStatusResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// makeSession builds a session row with a given last_heartbeat_at
// for the countdown tests. Status is "open" by default; pass
// "closed_clean" or "closed_aborted" to test the terminal-status
// skip path.
func makeSession(id, op, status, lastHB string) *session.Session {
	return &session.Session{
		SessionID:       id,
		Operator:        op,
		Status:          status,
		LastHeartbeatAt: lastHB,
		StartedAt:       "2026-08-03T00:00:00Z",
	}
}

// TestSessionStatus_FreshHeartbeat_NoCountdown verifies that a
// session whose last_heartbeat_at is `now` returns
// seconds_until_close ≈ heartbeat_timeout (full countdown, no
// closing_soon warning).
func TestSessionStatus_FreshHeartbeat_NoCountdown(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	sess := makeSession("sess-fresh", "alice", string(session.StatusOpen),
		now.Format(time.RFC3339Nano))

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)
	roundTripped := sessionStatusJSONRoundTrip(t, r)

	if roundTripped.SecondsUntilClose != 300 {
		t.Errorf("fresh heartbeat: seconds_until_close want 300, got %d", roundTripped.SecondsUntilClose)
	}
	if roundTripped.ClosingSoon {
		t.Errorf("fresh heartbeat: closing_soon want false, got true")
	}
}

// TestSessionStatus_NearDeadline_ClosingSoon verifies the warning
// fires when seconds_until_close drops below the threshold.
// Heartbeat 285s ago + 300s timeout = 15s left (≤ 30s threshold).
func TestSessionStatus_NearDeadline_ClosingSoon(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	lastHB := now.Add(-285 * time.Second)
	sess := makeSession("sess-near", "alice", string(session.StatusOpen),
		lastHB.Format(time.RFC3339Nano))

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)

	if r.SecondsUntilClose != 15 {
		t.Errorf("near deadline: seconds_until_close want 15, got %d", r.SecondsUntilClose)
	}
	if !r.ClosingSoon {
		t.Errorf("near deadline: closing_soon want true, got false")
	}
}

// TestSessionStatus_OverdueSession_ClosingSoon verifies that a
// session past the heartbeat timeout (sweeper hasn't run yet)
// returns seconds_until_close=0 and closing_soon=true. Without
// this, the harness wouldn't know the session is on borrowed time.
func TestSessionStatus_OverdueSession_ClosingSoon(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	lastHB := now.Add(-400 * time.Second) // 100s past deadline
	sess := makeSession("sess-overdue", "alice", string(session.StatusOpen),
		lastHB.Format(time.RFC3339Nano))

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)

	if r.SecondsUntilClose != 0 {
		t.Errorf("overdue: seconds_until_close want 0 (clamped), got %d", r.SecondsUntilClose)
	}
	if !r.ClosingSoon {
		t.Errorf("overdue: closing_soon want true, got false")
	}
}

// TestSessionStatus_IdleSession_StillCountsDown verifies the
// countdown still works for `idle` sessions. The sweeper promotes
// open → idle at idle_timeout; the next sweep tick closes idle
// sessions past heartbeat_timeout. Same deadline formula applies.
func TestSessionStatus_IdleSession_StillCountsDown(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	lastHB := now.Add(-100 * time.Second) // 200s left
	sess := makeSession("sess-idle", "alice", string(session.StatusIdle),
		lastHB.Format(time.RFC3339Nano))

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)

	if r.SecondsUntilClose != 200 {
		t.Errorf("idle: seconds_until_close want 200, got %d", r.SecondsUntilClose)
	}
	if r.ClosingSoon {
		t.Errorf("idle: closing_soon want false (200s > 30s threshold), got true")
	}
}

// TestSessionStatus_ClosedClean_NoCountdown verifies terminal
// sessions return omitempty zeros (no stale countdown data).
// The helper must NOT compute a deadline for closed_clean rows.
func TestSessionStatus_ClosedClean_NoCountdown(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	// last_heartbeat_at is set in the past; the test confirms the
	// status filter blocks the countdown computation, not the
	// timestamp.
	lastHB := now.Add(-1 * time.Hour)
	sess := makeSession("sess-closed-clean", "alice", string(session.StatusClosedClean),
		lastHB.Format(time.RFC3339Nano))

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)
	roundTripped := sessionStatusJSONRoundTrip(t, r)

	if roundTripped.SecondsUntilClose != 0 {
		t.Errorf("closed_clean: seconds_until_close want 0 (omitempty), got %d", roundTripped.SecondsUntilClose)
	}
	if roundTripped.ClosingSoon {
		t.Errorf("closed_clean: closing_soon want false, got true")
	}
	// Wire shape: confirm omitempty actually dropped both fields.
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "seconds_until_close") {
		t.Errorf("closed_clean: wire JSON should omit seconds_until_close; got %s", b)
	}
	if strings.Contains(string(b), "closing_soon") {
		t.Errorf("closed_clean: wire JSON should omit closing_soon; got %s", b)
	}
}

// TestSessionStatus_ClosedAborted_NoCountdown verifies that the
// sweeper-closed (but resurrectable) state also skips the
// countdown — the session is post-sweeper, not in-flight.
func TestSessionStatus_ClosedAborted_NoCountdown(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	lastHB := now.Add(-1 * time.Hour)
	sess := makeSession("sess-aborted", "alice", string(session.StatusClosedAborted),
		lastHB.Format(time.RFC3339Nano))

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)

	if r.SecondsUntilClose != 0 || r.ClosingSoon {
		t.Errorf("closed_aborted: want zeros, got seconds=%d closing_soon=%v",
			r.SecondsUntilClose, r.ClosingSoon)
	}
}

// TestSessionStatus_EmptyLastHB_NoCountdown is a defensive
// coverage test: if a row somehow has empty last_heartbeat_at
// (shouldn't happen for open|idle, but defensive), the helper
// must not crash.
func TestSessionStatus_EmptyLastHB_NoCountdown(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	sess := makeSession("sess-empty-hb", "alice", string(session.StatusOpen), "")

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)

	if r.SecondsUntilClose != 0 || r.ClosingSoon {
		t.Errorf("empty last_hb: want zeros, got seconds=%d closing_soon=%v",
			r.SecondsUntilClose, r.ClosingSoon)
	}
}

// TestSessionStatus_MalformedLastHB_NoCrash is the second
// defensive coverage test: if last_heartbeat_at is malformed
// (not RFC3339Nano), the helper must not panic. It returns
// zeros silently — better than crashing the session_status
// tool during a long session.
func TestSessionStatus_MalformedLastHB_NoCrash(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	hbTimeout := 300 * time.Second
	warnThreshold := 30 * time.Second

	sess := makeSession("sess-bad-hb", "alice", string(session.StatusOpen), "not-a-timestamp")

	r := sessionStatusFromSession(sess, warnThreshold, hbTimeout, now)

	if r.SecondsUntilClose != 0 || r.ClosingSoon {
		t.Errorf("malformed last_hb: want zeros, got seconds=%d closing_soon=%v",
			r.SecondsUntilClose, r.ClosingSoon)
	}
}

// TestSessionStatus_ConfigDefaults verifies that
// resolveClosingSoonConfig returns the expected defaults when
// no env vars are set. Pinning the contract so a future default
// change is caught here.
func TestSessionStatus_ConfigDefaults(t *testing.T) {
	t.Setenv("DARK_SESSION_CLOSING_SOON_THRESHOLD", "")
	t.Setenv("DARK_SESSION_HEARTBEAT_TIMEOUT", "")

	threshold, hb := resolveClosingSoonConfig()
	if threshold != 30*time.Second {
		t.Errorf("default threshold want 30s, got %s", threshold)
	}
	if hb != 300*time.Second {
		t.Errorf("default heartbeat_timeout want 300s, got %s", hb)
	}
}

// TestSessionStatus_ConfigEnvOverrides verifies env vars are
// honored when set. Uses t.Setenv for automatic cleanup.
func TestSessionStatus_ConfigEnvOverrides(t *testing.T) {
	t.Setenv("DARK_SESSION_CLOSING_SOON_THRESHOLD", "60")
	t.Setenv("DARK_SESSION_HEARTBEAT_TIMEOUT", "600")

	threshold, hb := resolveClosingSoonConfig()
	if threshold != 60*time.Second {
		t.Errorf("env threshold want 60s, got %s", threshold)
	}
	if hb != 600*time.Second {
		t.Errorf("env heartbeat_timeout want 600s, got %s", hb)
	}
}

// TestSessionStatus_ConfigEnvOverrides_GoSyntax verifies Go-style
// duration strings ("60s", "5m") parse correctly via
// time.ParseDuration.
func TestSessionStatus_ConfigEnvOverrides_GoSyntax(t *testing.T) {
	t.Setenv("DARK_SESSION_CLOSING_SOON_THRESHOLD", "1m")
	t.Setenv("DARK_SESSION_HEARTBEAT_TIMEOUT", "10m")

	threshold, hb := resolveClosingSoonConfig()
	if threshold != 60*time.Second {
		t.Errorf("go-syntax threshold want 60s, got %s", threshold)
	}
	if hb != 600*time.Second {
		t.Errorf("go-syntax heartbeat_timeout want 600s, got %s", hb)
	}
}