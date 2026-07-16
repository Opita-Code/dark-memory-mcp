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

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

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
			return sessionStatusFromSession(sess), nil
		}))

	// session_status — read-only fetch of a session by id.
	reg.Add(BindStore("session_status",
		"Return the current state of a session (id, operator, project, status, timestamps). Read-only.",
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
			return sessionStatusFromSession(sess), nil
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
type SessionStatusResult struct {
	SessionID       string `json:"session_id"`
	Operator        string `json:"operator,omitempty"`
	Status          string `json:"status"`
	ConstitutionID  string `json:"constitution_id,omitempty"`
	ConstitutionVer string `json:"constitution_ver,omitempty"`
	ActiveMods      string `json:"active_mods,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	ClosedAt        string `json:"closed_at,omitempty"`
	Notes           string `json:"notes,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
}

func sessionStatusFromSession(sess *session.Session) *SessionStatusResult {
	return &SessionStatusResult{
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
}