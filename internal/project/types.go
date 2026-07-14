// Package project defines the Project namespace, the first-class
// multi-tenancy primitive of Dark Memory MCP. Every row in dark.db
// carries a `project_id` (migrations v7). A Project is a logical
// grouping of sessions, specs, artifacts, and research items — the
// boundary the LLM and the operator work inside.
//
// The "default" project is the catch-all. All 164 existing specs in
// dark.db live in project_id='default'. New projects can be created
// with `dark_memory_project_create`. The active project is part of
// the Session context (see session.Session.ActiveProjectID).
package project

// Project is the tenant unit. One Project = one vibe-flow workspace.
// Cross-project reads are opt-in (dark_research_*) but the default
// is strict isolation.
type Project struct {
	ID               int64  `json:"id"`
	ProjectID        string `json:"project_id"`        // public id, kebab-case, unique
	DisplayName      string `json:"display_name"`
	Description      string `json:"description,omitempty"`
	ConstitutionID   string `json:"constitution_id,omitempty"`
	ConstitutionVer  string `json:"constitution_ver,omitempty"`
	CreatedAt        string `json:"created_at"`
	ArchivedAt       string `json:"archived_at,omitempty"`        // soft delete
	ParentProjectID  string `json:"parent_project_id,omitempty"` // for sub-projects
}

// IsArchived returns true if the project has been soft-deleted.
func (p *Project) IsArchived() bool { return p.ArchivedAt != "" }

// Membership is the link between an operator (human or AI agent) and
// a Project. Not enforced at the database level yet — used for
// authorization at the orchestrator level in v1.0. Future versions
// will use this table for RLS-style policy via a per-tenant role.
type Membership struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	Operator    string `json:"operator"`
	Role        string `json:"role"` // "owner" | "editor" | "viewer"
	GrantedAt   string `json:"granted_at"`
	GrantedBy   string `json:"granted_by,omitempty"`
}

// ProjectFilter is the per-request filter applied to all Store reads.
// ActiveProjectID is mandatory; empty means "no project context" which
// is treated as ErrSessionRequired for all reads.
type ProjectFilter struct {
	ActiveProjectID string
	// CrossProject, when true, lifts the project filter (read-only
	// escape hatch for cross-project research). Writes still require
	// an active project.
	CrossProject bool
}

// DefaultFilter is the project filter for the "default" project —
// used by legacy code paths that pre-date the project namespace.
func DefaultFilter() ProjectFilter {
	return ProjectFilter{ActiveProjectID: "default"}
}

// IsValid reports whether the filter is well-formed. Empty
// ActiveProjectID is invalid (rejected at the Store boundary).
func (f ProjectFilter) IsValid() bool {
	return f.ActiveProjectID != ""
}
