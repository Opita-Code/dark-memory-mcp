// session_* tools — operator-facing session lifecycle. Maps the
// v4alpha session.Store methods to MCP tools:
//
//   - dark_memory_session_start    → session.Store.Start
//   - dark_memory_session_close    → session.Store.Close
//   - dark_memory_session_status   → session.Store.Read (read-only)
//   - dark_memory_session_resume   → session.Store.Read + Heartbeat (refresh)
//   - dark_memory_session_heartbeat → session.Store.Heartbeat (no-op refresh)
//
// session_recover and session_resurrect are deferred to BUG-9
// (they require the v3 closed_aborted detection + INV-8
// inheritance semantics, both deferred).
//
// INV-7 enforcement (project_id filter on read) is preserved by
// passing project_id explicitly to every tool. The default
// project_id is "dark-memory-v4" (the active project from the
// boot session), but operators can override per call to query
// sibling projects.
//
// INV-1 audit: every Start / Close emits one audit_log row via
// session.Store. The audit row carries operator + session_id,
// so cross-session replay composes correctly.
package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	am "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
)

const (
	sessionStartToolName     = "dark_memory_session_start"
	sessionCloseToolName     = "dark_memory_session_close"
	sessionStatusToolName    = "dark_memory_session_status"
	sessionResumeToolName    = "dark_memory_session_resume"
	sessionHeartbeatToolName = "dark_memory_session_heartbeat"
)

const defaultProjectID = "dark-memory-v4"

func registerSessionTools(s *Server) {
	registerSessionStart(s)
	registerSessionClose(s)
	registerSessionStatus(s)
	registerSessionResume(s)
	registerSessionHeartbeat(s)
}

// --- session_start ---

type sessionStartInput struct {
	Operator  string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
	ProjectID string `json:"project_id,omitempty" jsonschema_description:"Project namespace (INV-7); defaults to dark-memory-v4"`
}

// sessionStartOutput adds Loadout (PRE-1 C3) to the v1
// shape. The 5 original fields are unchanged. Loadout is
// always present (may be empty); LoadoutWarnings is a
// per-field failure signal (empty when clean).
type sessionStartOutput struct {
	SessionID       string   `json:"session_id"`
	Operator        string   `json:"operator"`
	ProjectID       string   `json:"project_id"`
	Status          string   `json:"status"`
	StartedAt       string   `json:"started_at"`
	Loadout         *Loadout `json:"loadout"`
	LoadoutWarnings []string `json:"loadout_warnings"`
}

func registerSessionStart(s *Server) {
	tool := mcp.NewTool(sessionStartToolName,
		mcp.WithDescription("Open a new session and emit one INV-1 audit row. Returns the session_id plus a Loadout of the operator's startup context (pinned rows, open todos, recent audit writes, constitution, schema version, server time). PRE-1 C3: loadout is best-effort; per-field failures surface in loadout_warnings."),
		mcp.WithInputSchema[sessionStartInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in sessionStartInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("session_start: operator is required"), nil
		}
		projectID := in.ProjectID
		if projectID == "" {
			projectID = defaultProjectID
		}

		sess, err := s.session.Start(ctx, in.Operator, projectID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session_start: %v", err)), nil
		}

		// PRE-1 C3: build the Loadout inline. Best-effort; the
		// session itself is already open. Build returns warnings
		// for any field whose query failed; we surface them so
		// the harness knows what's missing.
		builder := NewLoadoutBuilder(s.memories, s.db)
		loadout, warnings, buildErr := builder.Build(ctx, in.Operator)
		if buildErr != nil {
			// Build returned a hard error (nil store/db or
			// missing operator). The session itself is OK;
			// surface the error as a single warning.
			warnings = []string{buildErr.Error()}
			loadout = &Loadout{
				PinnedRows:   []am.Row{},
				OpenTodos:    []am.Row{},
				RecentWrites: []AuditLoadRow{},
			}
		}

		out := sessionStartOutput{
			SessionID:       sess.ID,
			Operator:        sess.Operator,
			ProjectID:       sess.ProjectID,
			Status:          sess.Status,
			StartedAt:       sess.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
			Loadout:         loadout,
			LoadoutWarnings: warnings,
		}
		return resultJSON(out)
	})
}

// --- session_close ---

type sessionCloseInput struct {
	SessionID string `json:"session_id" jsonschema:"required" jsonschema_description:"Session to terminalize"`
	Clean     bool   `json:"clean,omitempty" jsonschema_description:"true = closed_clean, false = closed_aborted. Defaults true."`
}

type sessionCloseOutput struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	ClosedAt  string `json:"closed_at"`
}

func registerSessionClose(s *Server) {
	tool := mcp.NewTool(sessionCloseToolName,
		mcp.WithDescription("Terminalize a session. Emits one INV-1 audit row."),
		mcp.WithInputSchema[sessionCloseInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in sessionCloseInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.SessionID == "" {
			return mcp.NewToolResultError("session_close: session_id is required"), nil
		}

		summary, err := s.session.Close(ctx, in.SessionID, in.Clean)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session_close: %v", err)), nil
		}
		_ = summary // WriteCount/RunCount/ItemCount/AuditIDAtClose — surfaced in BUG-9

		// Read the closed row for the response shape (operator
		// wants to see status + closed_at).
		sess, err := s.session.Read(ctx, in.SessionID, defaultProjectID)
		if err != nil {
			// Fall back to the summary-derived fields without
			// re-reading.
			out := sessionCloseOutput{
				SessionID: in.SessionID,
				Status:    closedStatus(in.Clean),
			}
			return resultJSON(out)
		}
		out := sessionCloseOutput{
			SessionID: sess.ID,
			Status:    sess.Status,
		}
		if sess.ClosedAt != nil {
			out.ClosedAt = sess.ClosedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		return resultJSON(out)
	})
}

func closedStatus(clean bool) string {
	if clean {
		return session.StatusClosedClean
	}
	return session.StatusClosedAborted
}

// --- session_status ---

type sessionStatusInput struct {
	SessionID string `json:"session_id" jsonschema:"required" jsonschema_description:"Session to read"`
	ProjectID string `json:"project_id,omitempty" jsonschema_description:"Project namespace (INV-7); defaults to dark-memory-v4"`
}

type sessionStatusOutput struct {
	SessionID       string  `json:"session_id"`
	Operator        string  `json:"operator"`
	ProjectID       string  `json:"project_id"`
	Status          string  `json:"status"`
	StartedAt       string  `json:"started_at"`
	LastHeartbeatAt string  `json:"last_heartbeat_at"`
	ClosedAt        *string `json:"closed_at,omitempty"`
}

func registerSessionStatus(s *Server) {
	tool := mcp.NewTool(sessionStatusToolName,
		mcp.WithDescription("Read a session's current state. INV-7 enforces project_id match."),
		mcp.WithInputSchema[sessionStatusInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in sessionStatusInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.SessionID == "" {
			return mcp.NewToolResultError("session_status: session_id is required"), nil
		}
		projectID := in.ProjectID
		if projectID == "" {
			projectID = defaultProjectID
		}

		sess, err := s.session.Read(ctx, in.SessionID, projectID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session_status: %v", err)), nil
		}
		out := sessionStatusOutput{
			SessionID:       sess.ID,
			Operator:        sess.Operator,
			ProjectID:       sess.ProjectID,
			Status:          sess.Status,
			StartedAt:       sess.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
			LastHeartbeatAt: sess.LastHeartbeatAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		if sess.ClosedAt != nil {
			s := sess.ClosedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
			out.ClosedAt = &s
		}
		return resultJSON(out)
	})
}

// --- session_resume ---

// session_resume is the canonical "rebind to existing session"
// primitive. It validates the (session_id, project_id) pair
// (INV-7) and refreshes the last_heartbeat_at so the sweeper
// doesn't auto-close it during a long reasoning pause.
//
// Returns ErrNotFound if the session doesn't exist or the
// project_id doesn't match.
type sessionResumeInput struct {
	SessionID string `json:"session_id" jsonschema:"required" jsonschema_description:"Existing session to rebind to"`
	ProjectID string `json:"project_id,omitempty" jsonschema_description:"Project namespace (INV-7); defaults to dark-memory-v4"`
}

type sessionResumeOutput struct {
	SessionID       string `json:"session_id"`
	Operator        string `json:"operator"`
	ProjectID       string `json:"project_id"`
	Status          string `json:"status"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
}

func registerSessionResume(s *Server) {
	tool := mcp.NewTool(sessionResumeToolName,
		mcp.WithDescription("Rebind to an existing session. Refreshes heartbeat; returns current state."),
		mcp.WithInputSchema[sessionResumeInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in sessionResumeInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.SessionID == "" {
			return mcp.NewToolResultError("session_resume: session_id is required"), nil
		}
		projectID := in.ProjectID
		if projectID == "" {
			projectID = defaultProjectID
		}

		// Refresh first (lightweight write), then read (state).
		if err := s.session.Heartbeat(ctx, in.SessionID); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session_resume: %v", err)), nil
		}
		sess, err := s.session.Read(ctx, in.SessionID, projectID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session_resume: %v", err)), nil
		}
		out := sessionResumeOutput{
			SessionID:       sess.ID,
			Operator:        sess.Operator,
			ProjectID:       sess.ProjectID,
			Status:          sess.Status,
			LastHeartbeatAt: sess.LastHeartbeatAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		return resultJSON(out)
	})
}

// --- session_heartbeat ---

// session_heartbeat is a no-op refresh: it bumps last_heartbeat_at
// without changing anything else. Operators call it during long
// reasoning pauses (>60s) so the sweeper doesn't auto-close the
// session.
type sessionHeartbeatInput struct {
	SessionID string `json:"session_id" jsonschema:"required" jsonschema_description:"Session to heartbeat"`
}

type sessionHeartbeatOutput struct {
	SessionID       string `json:"session_id"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
	Refreshed       bool  `json:"refreshed"`
}

func registerSessionHeartbeat(s *Server) {
	tool := mcp.NewTool(sessionHeartbeatToolName,
		mcp.WithDescription("Refresh session last_heartbeat_at. No-op if the session is closed."),
		mcp.WithInputSchema[sessionHeartbeatInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in sessionHeartbeatInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.SessionID == "" {
			return mcp.NewToolResultError("session_heartbeat: session_id is required"), nil
		}
		err := s.session.Heartbeat(ctx, in.SessionID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session_heartbeat: %v", err)), nil
		}
		// Read the row to surface the new heartbeat.
		sess, err := s.session.Get(ctx, in.SessionID)
		if err != nil {
			return resultJSON(sessionHeartbeatOutput{SessionID: in.SessionID, Refreshed: true})
		}
		return resultJSON(sessionHeartbeatOutput{
			SessionID:       sess.ID,
			LastHeartbeatAt: sess.LastHeartbeatAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
			Refreshed:       true,
		})
	})
}
