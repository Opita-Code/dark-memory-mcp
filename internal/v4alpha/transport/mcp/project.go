// project_* tools — namespace primitive (Phase 4 Chunk 4.2).
//
// Per docs/sota-critique.md §7.6.9 threat model: project_id is a
// SOFT workstream namespace, NOT a multi-tenant primitive. HARD
// isolation is coexistence_group (per-MCP dark.db). These tools
// expose the v4.0.0-alpha.17 project lifecycle to operators:
//
//   - dark_memory_project_create → project.Store.Create
//   - dark_memory_project_lookup → project.Store.Lookup
//
// project_create is idempotent on project_id: replay returns the
// existing row with no mutation (matches v3.0.0-docfix behavior,
// ADR-008 invariant). Mutable fields (display_name, description,
// default_agent_id) are NOT updated on replay — operator must
// Archive + re-Create to rename.
//
// project_lookup returns the canonical Project for a project_id,
// or an empty `project: null` field for not-found (matches
// agent_memory_get pattern; callers distinguish via presence).
//
// Reserved ids ('default', 'dark') are rejected by Create via
// ErrReservedProjectID. Invalid kebab-case ids are rejected via
// ErrInvalidProjectID. The Store handles the canonical
// validation (types.go:Validate).
//
// Future chunks:
//   - Phase 4 Chunk 4.3: thread project_id through audit/agent_memory/
//     vibe/session callsites; session.Store.Start validates
//     project_id exists.
//   - Phase 4 Chunk 4.4: project_list, project_archive, project_update
//     tools (alpha.18 scope; current 2 cover the 80% use case).
package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
)

const (
	projectCreateToolName = "dark_memory_project_create"
	projectLookupToolName = "dark_memory_project_lookup"
)

func registerProjectTools(s *Server) {
	registerProjectCreate(s)
	registerProjectLookup(s)
}

// --- create ---

// projectCreateInput is the wire-shape for dark_memory_project_create.
// project_id is kebab-case, 3-64 chars. Reserved ids ('default',
// 'dark') rejected by Store.Create with ErrReservedProjectID.
type projectCreateInput struct {
	ProjectID      string `json:"project_id" jsonschema:"required" jsonschema_description:"Kebab-case project id (lowercase alnum + hyphen, 3-64 chars). Reserved ids 'default' and 'dark' are rejected."`
	DisplayName    string `json:"display_name" jsonschema:"required" jsonschema_description:"Human-readable label (1-128 chars)"`
	Description    string `json:"description,omitempty" jsonschema_description:"Optional free-form description (≤512 chars)"`
	DefaultAgentID string `json:"default_agent_id,omitempty" jsonschema_description:"Optional Mem0 agent_id (LLM identity) for this project (≤128 chars)"`
	Operator       string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; reused as audit_log.actor)"`
	SessionID      string `json:"session_id,omitempty" jsonschema_description:"Optional active session id for audit_log.session_id"`
}

// projectCreateOutput is the wire-shape returned by dark_memory_project_create.
// `created` distinguishes first-call (true) from replay (false).
type projectCreateOutput struct {
	ProjectID   string  `json:"project_id"`
	DisplayName string  `json:"display_name"`
	Created     bool    `json:"created"`
	Archived    bool    `json:"archived"`
	Project     *project.Project `json:"project"`
}

func registerProjectCreate(s *Server) {
	tool := mcp.NewTool(projectCreateToolName,
		mcp.WithDescription("Create or replay a project. Idempotent on project_id (first-writer wins; mutating display_name/description/default_agent_id requires Archive + re-Create). Reserved ids 'default' and 'dark' are rejected. Emits one INV-1 audit_log row on first insert."),
		mcp.WithInputSchema[projectCreateInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in projectCreateInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("project_create: operator is required (INV-1)"), nil
		}
		if in.ProjectID == "" {
			return mcp.NewToolResultError("project_create: project_id is required"), nil
		}
		if in.DisplayName == "" {
			return mcp.NewToolResultError("project_create: display_name is required"), nil
		}

		// Build the Project value the Store will validate + INSERT.
		// CreatedAt + ArchivedAt are server-side (set by Store.Create
		// after the INSERT or fetched on replay). Caller-supplied
		// values are ignored (matches audit.Writer posture).
		p := &project.Project{
			ProjectID:      in.ProjectID,
			DisplayName:    in.DisplayName,
			Description:    in.Description,
			DefaultAgentID: in.DefaultAgentID,
		}

		// Detect first-call vs replay by snapshotting the row
		// BEFORE the Create call (the Store.Create overwrites p
		// with the canonical row on success).
		existed := false
		if existing, lookupErr := s.projects.Lookup(ctx, in.ProjectID); lookupErr == nil {
			existed = !existing.CreatedAt.IsZero()
		}

		if err := s.projects.Create(ctx, p); err != nil {
			// Surface reserved-id + invalid-id errors as tool errors
			// (not as 500s). The Store wraps with %w so errors.Is works.
			if errors.Is(err, project.ErrReservedProjectID) {
				return mcp.NewToolResultError(fmt.Sprintf("project_create: %v (reserved id; cannot be re-created)", err)), nil
			}
			if errors.Is(err, project.ErrInvalidProjectID) {
				return mcp.NewToolResultError(fmt.Sprintf("project_create: %v (must be kebab-case, 3-64 chars)", err)), nil
			}
			if errors.Is(err, project.ErrInvalidProject) {
				return mcp.NewToolResultError(fmt.Sprintf("project_create: %v", err)), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("project_create: %v", err)), nil
		}

		// Note: SessionID is NOT yet threaded through audit (deferred
		// to Chunk 4.3). The Create emits an audit row with
		// session_id="" — the Operator (in.Operator) is the audit
		// actor. Future Chunk 4.3: pass in.SessionID to a future
		// CreateWithSession variant.

		return resultJSON(projectCreateOutput{
			ProjectID:   p.ProjectID,
			DisplayName: p.DisplayName,
			Created:     !existed, // false on replay, true on first insert
			Archived:    p.IsArchived(),
			Project:     p,
		})
	})
}

// --- lookup ---

// projectLookupInput is the wire-shape for dark_memory_project_lookup.
// project_id is kebab-case. Returns `project: null` when not found
// (matches agent_memory_get pattern).
type projectLookupInput struct {
	ProjectID string `json:"project_id" jsonschema:"required" jsonschema_description:"Project id to look up"`
}

// projectLookupOutput is the wire-shape returned by dark_memory_project_lookup.
// Project is nil when not found; caller distinguishes via presence.
type projectLookupOutput struct {
	ProjectID string           `json:"project_id"`
	Found     bool             `json:"found"`
	Project   *project.Project `json:"project"`
}

func registerProjectLookup(s *Server) {
	tool := mcp.NewTool(projectLookupToolName,
		mcp.WithDescription("Look up one project by id. Returns `project: null` when not found (no error). Includes archived projects; check `is_archived` to filter."),
		mcp.WithInputSchema[projectLookupInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in projectLookupInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.ProjectID == "" {
			return mcp.NewToolResultError("project_lookup: project_id is required"), nil
		}

		p, err := s.projects.Lookup(ctx, in.ProjectID)
		if err != nil {
			if errors.Is(err, project.ErrProjectNotFound) {
				// Not-found is NOT an error — wire shape returns
				// `project: null` so callers can distinguish from
				// server errors via the presence of `found: true`.
				return resultJSON(projectLookupOutput{
					ProjectID: in.ProjectID,
					Found:     false,
					Project:   nil,
				})
			}
			return mcp.NewToolResultError(fmt.Sprintf("project_lookup: %v", err)), nil
		}

		return resultJSON(projectLookupOutput{
			ProjectID: p.ProjectID,
			Found:     true,
			Project:   p,
		})
	})
}