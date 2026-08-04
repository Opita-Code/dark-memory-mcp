// Package tools — session.go: the SESSION namespace (4 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_session_start
//	dark_memory_session_resume
//	dark_memory_session_status
//	dark_memory_session_close
//
// Maps to orchestrator O1 (SessionStart), O2 (SessionClose) + 2 new
// read-only helpers (Status reads via Store.GetSession; Resume
// validates session_id then sets active project).
package tools

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// closingSoonThresholdDefault is the default warn-within-N-seconds
// threshold for session_status.closing_soon. Matches the sweeper's
// idle_timeout default (15m) — enough warning for an operator
// to refresh a long session before the sweeper closes it. Kept
// modest (30s) so the warning is actionable, not noisy.
const closingSoonThresholdDefault = 30 * time.Second

// heartbeatTimeoutDefault mirrors the sweeper's default
// (orchestration/session_sweeper.go:42). Kept in sync via tests
// in tests/orchestration/session_status_test.go.
//
// v2.10.0 (2026-08-04): 300s → 60m — the old default closed ACTIVE
// sessions on interactive harnesses that do not emit periodic
// heartbeats (any thinking pause > 5 min → closed_aborted →
// ErrFrameStaleTooFar on the next tool call).
const heartbeatTimeoutDefault = 60 * time.Minute

// envDuration reads a Go-style duration env var (e.g. "30s", "5m",
// "300") with a default. Mirrors the helper in
// orchestration/session_sweeper.go so session_status surfaces the
// same numbers the sweeper enforces. (F45: in-process duplication
// of the sweeper's env contract; the sweeper owns the writes, the
// status helper owns the reads — split for boundary clarity.)
func envDuration(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return def
}

// closingSoonThresholdEnv + heartbeatTimeoutEnv mirror the sweeper
// env vars (orchestration/session_sweeper.go:40-45). Renamed here
// to give the status helper its own override surface — operators
// can warn earlier or later without touching sweeper cadence.
const (
	closingSoonThresholdEnv = "DARK_SESSION_CLOSING_SOON_THRESHOLD"
	heartbeatTimeoutEnv     = "DARK_SESSION_HEARTBEAT_TIMEOUT"
)

// resolveClosingSoonConfig reads both env vars with defaults.
// Exported for tests in tests/orchestration/session_status_test.go
// so the test suite can pin the contract independently of process
// env state.
func resolveClosingSoonConfig() (threshold, heartbeatTimeout time.Duration) {
	threshold = envDuration(closingSoonThresholdEnv, closingSoonThresholdDefault)
	heartbeatTimeout = envDuration(heartbeatTimeoutEnv, heartbeatTimeoutDefault)
	return
}

// RegisterSession wires the 4 SESSION tools into the registry.
// Caller passes the orchestrator + store so handlers can reach them
// without circular imports.
func RegisterSession(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// session_start — wraps O1 SessionStart orchestrator.
	reg.Add(BindOrchestrator("session_start",
		"Start an operational session. Returns the session_id. Use session_close to terminate.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"operator", "project_id"},
			"properties": map[string]any{
				"operator":         map[string]any{"type": "string", "description": "Operator id (human or agent) starting the session."},
				"project_id":       map[string]any{"type": "string", "description": "Project namespace (INV-7). Use 'default' for the legacy project."},
				"constitution_id":  map[string]any{"type": "string"},
				"constitution_ver": map[string]any{"type": "string"},
				"notes":            map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.SessionStartInput) (*orchestration.SessionStartOutput, error) {
			return orch.SessionStart(ctx, in)
		}))

	// session_resume — re-activate an existing session. Read-only
	// helper: validates session_id exists, sets active project to
	// match, returns the session row.
	reg.Add(BindStore("session_resume",
		"Resume an existing session by session_id. Validates the session exists, sets the active project to match, and returns the session row.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"session_id"},
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string", "description": "The session id to resume (format: sess-XXXXXXXX)."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in SessionResumeInput) (*SessionStatusResult, error) {
			sess, err := s.GetSession(ctx, in.SessionID)
			if err != nil {
				return nil, err
			}
			if sess == nil {
				return nil, store.ErrNotFound
			}
			// session_resume does not auto-set a project (project
			// scoping is INV-7 but session rows don't carry project_id
			// — see session/types.go). The caller should call
			// active_policy to learn the active project context if
			// needed.
			threshold, hb := resolveClosingSoonConfig()
			return sessionStatusFromSession(sess, threshold, hb, time.Now().UTC()), nil
		}))

	// session_status — read-only fetch of a session by id.
	reg.Add(BindStore("session_status",
		"Return the current state of a session (id, operator, project, status, timestamps). Read-only. Includes closing_soon + seconds_until_close so harnesses can warn before the sweeper closes an idle session.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"session_id"},
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string"},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in SessionStatusInput) (*SessionStatusResult, error) {
			sess, err := s.GetSession(ctx, in.SessionID)
			if err != nil {
				return nil, err
			}
			if sess == nil {
				return nil, store.ErrNotFound
			}
			threshold, hb := resolveClosingSoonConfig()
			return sessionStatusFromSession(sess, threshold, hb, time.Now().UTC()), nil
		}))

	// session_close — wraps O2 SessionClose orchestrator.
	reg.Add(BindOrchestrator("session_close",
		"Close the active session. Returns the write/run/item summary.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"session_id"},
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.SessionCloseInput) (*orchestration.SessionCloseOutput, error) {
			return orch.SessionClose(ctx, in)
		}))
}

// SessionResumeInput is the input for session_resume.
type SessionResumeInput struct {
	SessionID string `json:"session_id"`
}

// SessionStatusInput is the input for session_status.
type SessionStatusInput struct {
	SessionID string `json:"session_id"`
}

// SessionStatusResult is the shape returned by session_resume and
// session_status. It is intentionally a subset of the session row
// (the LLM-facing context projection — see RFC D-5).
//
// Row 168 (2026-08-03): added ClosingSoon + SecondsUntilClose so
// harnesses can warn operators when a session is about to be
// auto-closed by the sweeper. The fields are omitempty so closed
// sessions don't carry stale countdown data; harnesses that only
// want the canonical fields can ignore them.
type SessionStatusResult struct {
	SessionID         string `json:"session_id"`
	Operator          string `json:"operator,omitempty"`
	Status            string `json:"status"`
	ConstitutionID    string `json:"constitution_id,omitempty"`
	ConstitutionVer   string `json:"constitution_ver,omitempty"`
	ActiveMods        string `json:"active_mods,omitempty"`
	StartedAt         string `json:"started_at,omitempty"`
	ClosedAt          string `json:"closed_at,omitempty"`
	Notes             string `json:"notes,omitempty"`
	ParentSessionID   string `json:"parent_session_id,omitempty"`
	ClosingSoon       bool   `json:"closing_soon,omitempty"`
	SecondsUntilClose int    `json:"seconds_until_close,omitempty"`
}

// sessionStatusFromSession builds the LLM-facing projection from a
// session row. The closingSoonThreshold + heartbeatTimeout arguments
// are the sweeper-side timeouts (env-configurable), so the test
// suite can pin the contract independently of process env. now is
// injected the same way (testable without time.Now).
//
// ClosingSoon + SecondsUntilClose computation (row 168):
//
//   - closed sessions → no countdown data (omitempty zeros).
//   - open|idle sessions → deadline = last_heartbeat_at + heartbeatTimeout.
//     seconds_until_close = max(0, deadline - now). closing_soon = true
//     when seconds_until_close <= threshold (default 30s). If the
//     deadline is already in the past (sweeper hasn't run yet),
//     seconds_until_close=0 and closing_soon=true so the harness
//     knows the session is overdue for closure.
func sessionStatusFromSession(sess *session.Session, closingSoonThreshold, heartbeatTimeout time.Duration, now time.Time) *SessionStatusResult {
	r := &SessionStatusResult{
		SessionID:       sess.SessionID,
		Operator:        sess.Operator,
		Status:          sess.Status,
		ConstitutionID:  sess.ConstitutionID,
		ConstitutionVer: sess.ConstitutionVer,
		ActiveMods:      sess.ActiveMods,
		StartedAt:       sess.StartedAt,
		ClosedAt:        sess.ClosedAt,
		Notes:           sess.Notes,
		ParentSessionID: sess.ParentSessionID,
	}
	// Skip countdown for terminal sessions — the data would be
	// misleading (deadline is in the past; sweeper already ran or
	// the session was operator-closed). Status values per
	// internal/session/types.go:80-83 are "open", "idle",
	// "closed_clean" (terminal, NOT resurrectable),
	// "closed_aborted" (resurrectable — sweeper ran). Anything
	// not in {open, idle} is post-sweeper; skip.
	if sess.Status != string(session.StatusOpen) && sess.Status != string(session.StatusIdle) {
		return r
	}
	// last_heartbeat_at is RFC3339Nano; if the session row is
	// missing it (shouldn't happen for open|idle, but defensive),
	// skip the computation.
	if sess.LastHeartbeatAt == "" {
		return r
	}
	lastHB, err := time.Parse(time.RFC3339Nano, sess.LastHeartbeatAt)
	if err != nil {
		return r
	}
	deadline := lastHB.Add(heartbeatTimeout)
	remaining := deadline.Sub(now)
	secsLeft := int(remaining.Seconds())
	if secsLeft < 0 {
		// Sweeper hasn't run yet; the session is overdue.
		secsLeft = 0
	}
	r.SecondsUntilClose = secsLeft
	r.ClosingSoon = secsLeft <= int(closingSoonThreshold.Seconds())
	return r
}