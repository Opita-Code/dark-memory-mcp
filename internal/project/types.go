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

	// DriftStrictness (Wave 5X.3) overrides the drift-at-write
	// interceptor (5A.vi M6) on a per-project basis. Allowed values:
	//   "" / "default" — use DARK_DRIFT_STRICTNESS env (default behavior)
	//   "off"    — skip drift check entirely
	//   "warn"   — drift_judge + log + allow + tag drift_pending
	//   "strict" — drift_judge + refuse ErrDriftAtWrite on drift_detected
	// Empty string is normalized to "default" by the drift package.
	DriftStrictness string `json:"drift_strictness,omitempty"`

	// ActiveSessionID (v17, v2.0.2 gate fix) is the session_id that
	// session_start most recently bound to this project. The
	// StoreBackedActiveSessionResolver (server/middleware) reads
	// this on every PreCheck so tools that take args.session_id
	// and tools that rely on the resolver can both find a session
	// without the orchestrator having to plumb session_id through
	// every tool call.
	//
	// Set by session_start / session_resume, cleared by session_close
	// (compare-and-set so concurrent session_start for a different
	// session wins over a stale close).
	//
	// Populated only after v17. Pre-v17 rows have empty string
	// (treated as "no active session").
	ActiveSessionID string `json:"active_session_id,omitempty"`

	// ActiveSessionSetAt is the wall-clock time ActiveSessionID was
	// bound. The sweeper (a follow-up) uses it to age out sessions
	// whose HEARTBEAT_TIMEOUT (5E.iii) has elapsed. Empty when no
	// active session.
	ActiveSessionSetAt string `json:"active_session_set_at,omitempty"`

	// DefaultAgentID (v20, v2.4.1) is the Mem0 agent_id (LLM
	// identity) that owns the project. session_start and publish_vibe
	// resolve agent_id with priority:
	//   1. caller-supplied AgentID on the request (per-call override)
	//   2. projects.default_agent_id (this field, per-project default)
	//   3. empty string (no agent filter; project-wide scope — v2.4.0
	//      behavior)
	//
	// Set at tenant provisioning via
	// dark_memory_project_create(default_agent_id="claude-sonnet-4.5").
	// Distinct from Operator (human/agent identity per INV-1 audit).
	// Single-agent flows may use the same value for both; multi-agent
	// flows (e.g. a project used by both claude-sonnet-4.5 and
	// gpt-4o) set this to one and override per-call.
	//
	// Wired into ContextRecap (session_start) and drift_judge
	// enrichment (publish_vibe). When set, VLP integrations filter
	// agent_memory by this agent_id so each LLM sees only its own
	// decisions / findings, not those of other LLMs writing to the
	// same project — closing the cross-agent leakage that v2.4.0
	// introduced when it integrated agent_memory project-wide.
	DefaultAgentID string `json:"default_agent_id,omitempty"`
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
