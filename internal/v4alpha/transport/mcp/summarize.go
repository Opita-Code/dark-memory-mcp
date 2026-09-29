// summarize + skill_loaded tools (v4alpha) — PRE-1 C4.
//
// Two MCP tools that complete the session handoff story:
//   - dark_memory_summarize_session → session.Summarize
//   - dark_memory_skill_loaded      → session.SkillLoad
//
// dark_memory_summarize_session returns a markdown handoff
// document for the given session_id. Read-only; no audit
// emission (per the summarize package doc — INV-1 only
// requires audit on writes).
//
// dark_memory_skill_loaded records a skill load event in
// agent_memory. The convention (tags CSV, title format,
// kind=observation) is enforced by the underlying
// session.SkillLoad helper, not by the transport layer.
// This tool emits one INV-1 audit row per call (via
// agent_memory.Save → audit.Writer.WriteExec).
//
// INV-7 note: neither tool requires project_id at the
// transport layer. The session_id is the canonical scope.
// For tenant-isolated access, the harness should pass
// a session_id it owns.
package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	am "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
)

const (
	summarizeSessionToolName = "dark_memory_summarize_session"
	skillLoadedToolName      = "dark_memory_skill_loaded"
)

func registerSummarizeTools(s *Server) {
	registerSummarizeSession(s)
	registerSkillLoaded(s)
}

// --- summarize_session ---

// summarizeSessionInput is the wire shape. session_id is
// required; format defaults to "markdown" (only markdown
// is supported in v1 — JSON is a v2 enhancement).
type summarizeSessionInput struct {
	SessionID string `json:"session_id" jsonschema:"required" jsonschema_description:"Session to summarize. Returns a markdown handoff doc with metadata, pinned rows, open todos, recent writes (audit_log), and skills loaded."`
	Format    string `json:"format,omitempty" jsonschema_description:"Output format. v1 only supports 'markdown' (default). JSON is v2."`
}

// summarizeSessionOutput wraps the markdown in a struct so
// future JSON support can add a sibling field without
// changing the schema.
type summarizeSessionOutput struct {
	SessionID  string                 `json:"session_id"`
	Format     string                 `json:"format"`
	Markdown   string                 `json:"markdown"`
	Meta       summarizeSessionMeta   `json:"meta"`
	PinnedRows int                    `json:"pinned_rows_count"`
	OpenTodos  int                    `json:"open_todos_count"`
	AuditRows  int                    `json:"audit_rows_count"`
	Skills     int                    `json:"skills_loaded_count"`
}

// summarizeSessionMeta echoes the Session row's high-value
// fields so the operator can verify the right session was
// summarized without parsing the markdown.
type summarizeSessionMeta struct {
	Operator        string `json:"operator"`
	ProjectID       string `json:"project_id"`
	Status          string `json:"status"`
	StartedAt       string `json:"started_at"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
	ClosedAt        string `json:"closed_at,omitempty"`
}

func registerSummarizeSession(s *Server) {
	tool := mcp.NewTool(summarizeSessionToolName,
		mcp.WithDescription("Return a markdown handoff document for a session. Includes metadata, pinned rows, open todos, recent audit_log writes, and skills loaded. Read-only; no audit emission."),
		mcp.WithInputSchema[summarizeSessionInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in summarizeSessionInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.SessionID == "" {
			return mcp.NewToolResultError("summarize_session: session_id is required"), nil
		}
		format := in.Format
		if format == "" {
			format = "markdown"
		}
		if format != "markdown" {
			return mcp.NewToolResultError(fmt.Sprintf(
				"summarize_session: format=%q not supported in v1 (only 'markdown')",
				format)), nil
		}

		summary, err := session.Summarize(ctx, in.SessionID, s.session, s.memories, s.db)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("summarize_session: %v", err)), nil
		}

		md := summary.Markdown()
		out := summarizeSessionOutput{
			SessionID:  summary.Session.ID,
			Format:     "markdown",
			Markdown:   md,
			PinnedRows: len(summary.PinnedRows),
			OpenTodos:  len(summary.OpenTodos),
			AuditRows:  len(summary.AuditRows),
			Skills:     len(summary.SkillsLoaded),
			Meta: summarizeSessionMeta{
				Operator:        summary.Session.Operator,
				ProjectID:       summary.Session.ProjectID,
				Status:          summary.Session.Status,
				StartedAt:       summary.Session.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
				LastHeartbeatAt: summary.Session.LastHeartbeatAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
			},
		}
		if summary.Session.ClosedAt != nil {
			out.Meta.ClosedAt = summary.Session.ClosedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		return resultJSON(out)
	})
}

// --- skill_loaded ---

// skillLoadedInput is the wire shape. operator is required
// (INV-1 audit attribution). session_id is optional
// (skill_loaded can fire during harness startup before
// session_start, or in air-gapped tests without sessions).
// skill_name is required. version and source are optional.
type skillLoadedInput struct {
	Operator   string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
	SessionID  string `json:"session_id,omitempty" jsonschema_description:"Active session (optional). Empty = no session binding."`
	SkillName  string `json:"skill_name" jsonschema:"required" jsonschema_description:"Name of the skill loaded (e.g. 'dark-memory', 'fresh-osint')."`
	Version    string `json:"version,omitempty" jsonschema_description:"Skill version (e.g. 'v3.0.0-docfix'). Optional."`
	Source     string `json:"source,omitempty" jsonschema_description:"Who triggered the load (e.g. 'opencode-system', 'agent-auto', 'user-manual'). Optional."`
}

// skillLoadedOutput returns the saved row id so the harness
// can correlate later.
type skillLoadedOutput struct {
	SavedID   int64  `json:"saved_id"`
	Operator  string `json:"operator"`
	SkillName string `json:"skill_name"`
	Version   string `json:"version,omitempty"`
	Source    string `json:"source,omitempty"`
}

func registerSkillLoaded(s *Server) {
	tool := mcp.NewTool(skillLoadedToolName,
		mcp.WithDescription("Record a skill load event in agent_memory (kind=observation, tags=skill_loaded:v1+skill:<name>). Emits one INV-1 audit row per call. The skill_loaded convention is the explicit form of PRE-1 C2 L2."),
		mcp.WithInputSchema[skillLoadedInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in skillLoadedInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("skill_loaded: operator is required"), nil
		}
		if in.SkillName == "" {
			return mcp.NewToolResultError("skill_loaded: skill_name is required"), nil
		}

		auditMeta := &am.Audit{
			Actor:     in.Operator,
			SessionID: in.SessionID,
		}
		id, err := session.RecordSkillLoad(ctx, s.memories, auditMeta,
			in.Operator, in.SkillName, in.Version, in.Source)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("skill_loaded: %v", err)), nil
		}

		out := skillLoadedOutput{
			SavedID:   id,
			Operator:  in.Operator,
			SkillName: in.SkillName,
			Version:   in.Version,
			Source:    in.Source,
		}
		return resultJSON(out)
	})
}