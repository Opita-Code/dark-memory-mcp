// session_* tools — operator-facing session lifecycle. Maps the
// v4alpha session.Store methods to MCP tools:
//
//   - dark_memory_session_start   → session.Store.Start
//   - dark_memory_session_close   → session.Store.Close
//   - dark_memory_session_status  → session.Store.Read (read-only)
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

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
)

const (
	sessionStartToolName  = "dark_memory_session_start"
	sessionCloseToolName  = "dark_memory_session_close"
	sessionStatusToolName = "dark_memory_session_status"
)

const defaultProjectID = "dark-memory-v4"

func registerSessionTools(s *Server) {
	registerSessionStart(s)
	registerSessionClose(s)
	registerSessionStatus(s)
}

// --- session_start ---

type sessionStartInput struct {
	Operator  string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
	ProjectID string `json:"project_id,omitempty" jsonschema_description:"Project namespace (INV-7); defaults to dark-memory-v4"`
}

type sessionStartOutput struct {
	SessionID string `json:"session_id"`
	Operator  string `json:"operator"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
}

func registerSessionStart(s *Server) {
	tool := mcp.NewTool(sessionStartToolName,
		mcp.WithDescription("Open a new session and emit one INV-1 audit row. Returns the session_id."),
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
		out := sessionStartOutput{
			SessionID: sess.ID,
			Operator:  sess.Operator,
			ProjectID: sess.ProjectID,
			Status:    sess.Status,
			StartedAt: sess.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
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
