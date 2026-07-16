// Package tools — project.go: the PROJECT namespace (1 tool).
//
// Per RFC §5 / D-9 (and INV-7 multi-tenancy): a Project is the
// first-class tenant primitive of dark.db. Every row in vibe_specs,
// vibe_artifacts, vibe_drift_reports, sessions, research_runs, and
// research_items carries a project_id column (migrations v7+). A
// session is bound to exactly one project at a time (the
// active_project), and reads/writes from one project cannot bleed
// into another (verified by tests/project/project_test.go's
// `TestProject_Isolation_WriteAQueryB_Empty`).
//
// Prior to v1.2.0, the only way to obtain a non-"default" project_id
// was to insert a row directly into the `projects` table out of band
// (psql / sqlite3 CLI). That forced operators to leave the MCP surface
// to bootstrap a tenant, which in turn forced the
// `dark_memory_session_start` orchestrator to fail with
// `ErrSessionRequired` whenever a caller passed a non-default,
// non-existent project_id (the symptom that prompted this tool).
//
// `dark_memory_project_create` closes the loop: callers can now
// provision a tenant from inside the MCP surface, then immediately
// call `dark_memory_session_start` with that project_id. The
// operation is idempotent on (project_id) — re-creating an existing
// project is a no-op success that returns the existing row, which
// matches the Store.CreateProject semantics (see
// internal/store/sqlite/store.go's CreateProject implementation).
package tools

import (
	"context"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterProject wires the 1 PROJECT tool into the registry.
//
// `project_create` is intentionally placed in the canonical order
// (see internal/tools/registry.go, canonicalToolOrder) BEFORE
// `session_start`. Operators that follow the recommended bootstrap
// flow (project_create → session_start → …) hit project_create first
// in tools/list, which is the positionally-natural discovery order.
//
// RegisterProject is a no-op for un-provisioned stores that pre-date
// the migrations/v7 projects table — Store.CreateProject itself
// surfaces the migration error, and we propagate it verbatim.
func RegisterProject(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	reg.Add(BindStore("project_create",
		"Create a new project (INV-7 tenant primitive). Idempotent on project_id — re-creating an existing project returns the existing row. The 'default' project is seeded on Open and cannot be re-created (returns ErrAlreadyExists).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"project_id", "display_name"},
			"additionalProperties": false,
			"properties": map[string]any{
				"project_id": map[string]any{
					"type":        "string",
					"pattern":     "^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$",
					"description": "Kebab-case public id (3-64 chars, lowercase alnum + hyphen, must start and end with alnum).",
				},
				"display_name": map[string]any{
					"type":        "string",
					"minLength":   1,
					"maxLength":   128,
					"description": "Human-readable label (1-128 chars).",
				},
				"description": map[string]any{
					"type":        "string",
					"maxLength":   512,
					"description": "Optional free-form description (max 512 chars).",
				},
				"constitution_id": map[string]any{
					"type":        "string",
					"description": "Optional constitution id to bind the project to (INV-7 constitution scoping).",
				},
				"constitution_ver": map[string]any{
					"type":        "string",
					"description": "Optional constitution version (paired with constitution_id).",
				},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in ProjectCreateInput) (*ProjectCreateResult, error) {
			return runProjectCreate(ctx, s, in)
		}))
}

// ProjectCreateInput is the input for project_create.
type ProjectCreateInput struct {
	ProjectID       string `json:"project_id"`
	DisplayName     string `json:"display_name"`
	Description     string `json:"description,omitempty"`
	ConstitutionID  string `json:"constitution_id,omitempty"`
	ConstitutionVer string `json:"constitution_ver,omitempty"`
}

// ProjectCreateResult is the output for project_create. On idempotent
// re-create, both `created` and `idempotent_replay` are false and
// `created_at` echoes the row's existing timestamp.
type ProjectCreateResult struct {
	ProjectID        string `json:"project_id"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description,omitempty"`
	ConstitutionID   string `json:"constitution_id,omitempty"`
	ConstitutionVer  string `json:"constitution_ver,omitempty"`
	CreatedAt        string `json:"created_at"`
	IdempotentReplay bool   `json:"idempotent_replay"` // true when (project_id) already existed
}

// runProjectCreate validates input, dispatches to Store.CreateProject,
// and shapes the result. Separated from the BindStore closure so
// tests can call it directly without spinning up an MCP server.
func runProjectCreate(ctx context.Context, s store.Store, in ProjectCreateInput) (*ProjectCreateResult, error) {
	if err := validateProjectCreateInput(in); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	p := &project.Project{
		ProjectID:       in.ProjectID,
		DisplayName:     in.DisplayName,
		Description:     in.Description,
		ConstitutionID:  in.ConstitutionID,
		ConstitutionVer: in.ConstitutionVer,
		CreatedAt:       now,
	}

	// Probe for an existing row first so we can return the original
	// created_at on idempotent replay instead of overwriting it with
	// "now". CreateProject itself is idempotent (INSERT OR IGNORE on
	// the unique index), so this read-before-write is purely a UX
	// concern, not a correctness one.
	if existing, err := s.GetProject(ctx, in.ProjectID); err == nil && existing != nil {
		return &ProjectCreateResult{
			ProjectID:        existing.ProjectID,
			DisplayName:      existing.DisplayName,
			Description:      existing.Description,
			ConstitutionID:   existing.ConstitutionID,
			ConstitutionVer:  existing.ConstitutionVer,
			CreatedAt:        existing.CreatedAt,
			IdempotentReplay: true,
		}, nil
	}

	if err := s.CreateProject(ctx, p); err != nil {
		return nil, err
	}
	return &ProjectCreateResult{
		ProjectID:        p.ProjectID,
		DisplayName:      p.DisplayName,
		Description:      p.Description,
		ConstitutionID:   p.ConstitutionID,
		ConstitutionVer:  p.ConstitutionVer,
		CreatedAt:        p.CreatedAt,
		IdempotentReplay: false,
	}, nil
}

// validateProjectCreateInput enforces the same kebab-case rule the
// store's PRIMARY KEY constraint expects (3-64 chars, lowercase
// alnum + hyphen, must start and end with alnum). Returning the
// ErrInvalidArgument directly keeps the ToolError mapping in
// errors.go unchanged.
func validateProjectCreateInput(in ProjectCreateInput) error {
	if strings.TrimSpace(in.ProjectID) == "" {
		return store.ErrInvalidArgument
	}
	if len(in.ProjectID) < 3 || len(in.ProjectID) > 64 {
		return store.ErrInvalidArgument
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		return store.ErrInvalidArgument
	}
	if len(in.DisplayName) > 128 {
		return store.ErrInvalidArgument
	}
	if len(in.Description) > 512 {
		return store.ErrInvalidArgument
	}
	// The JSON Schema's `pattern` is the primary validator; this is
	// a defensive second-line check for callers that bypass the
	// schema (e.g. Go tests calling runProjectCreate directly).
	for i, r := range in.ProjectID {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		isHyphen := r == '-'
		if !isLower && !isDigit && !isHyphen {
			return store.ErrInvalidArgument
		}
		if isHyphen && (i == 0 || i == len(in.ProjectID)-1) {
			return store.ErrInvalidArgument
		}
	}
	return nil
}