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
	"fmt"
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
//
// v2.4.1: gained `default_agent_id` (optional). When set, session_start
// and publish_vibe resolve agent_id with priority (caller input >
// project default > empty). Multi-agent projects should set this to
// the primary agent_id; per-call overrides are accepted on session_start
// and publish_vibe.
//
// v2.20.0 T07: gained `nli_config` (optional, JSON object). When
// set, the project's drift_judge uses the configured primary (and
// optional fallback) NLI provider instead of falling through to
// nli.Config{}.DefaultsFor(). See project.NLIConfig.Validate() for
// the hard invariants. AuthToken is never echoed back in the
// ProjectCreateResult (the Store layer strips it on read).
func RegisterProject(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	reg.Add(BindStore("project_create",
		"Create a new project (INV-7 tenant primitive). Idempotent on project_id — re-creating an existing project returns the existing row. The 'default' project is seeded on Open and cannot be re-created (returns ErrAlreadyExists).",
		MustJSONSchema(map[string]any{
			"type":                 "object",
			"required":             []string{"project_id", "display_name"},
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
				"default_agent_id": map[string]any{
					"type":        "string",
					"maxLength":   128,
					"description": "Optional v2.4.1 Mem0 agent_id (LLM identity) that owns the project. session_start and publish_vibe resolve agent_id with priority (caller input > this default > empty). Empty = no default agent (v2.4.0 behavior). Set this for multi-agent projects so each LLM's ContextRecap and drift_judge enrichment filter to its own memories.",
				},
				"nli_config": map[string]any{
					"type":        "object",
					"description": "Optional v2.20.0 T07 per-project NLI configuration. When enabled, the project's drift_judge uses the configured primary (and optional fallback) NLI provider instead of falling through to nli.Config{}.DefaultsFor().",
					"properties": map[string]any{
						"enabled":             map[string]any{"type": "boolean"},
						"primary":             map[string]any{"$ref": "#/$defs/nli_primary"},
						"fallback":            map[string]any{"$ref": "#/$defs/nli_primary"},
						"fallback_enabled":    map[string]any{"type": "boolean"},
						"latency_budget_ms":   map[string]any{"type": "integer", "minimum": 1},
						"max_premise_bytes":   map[string]any{"type": "integer", "minimum": 64},
						"max_hypothesis_bytes": map[string]any{"type": "integer", "minimum": 16},
						"max_cache_entries":   map[string]any{"type": "integer", "minimum": 100},
						"cache_ttl_seconds":   map[string]any{"type": "integer", "minimum": 1},
					},
				},
			},
			"$defs": map[string]any{
				"nli_primary": map[string]any{
					"type":     "object",
					"required": []string{"provider_id", "endpoint"},
					"properties": map[string]any{
						"provider_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
						"endpoint":    map[string]any{"type": "string", "format": "uri"},
						"auth_token":  map[string]any{"type": "string", "maxLength": 4096, "description": "Bearer token; never echoed in tool results."},
						"timeout_ms":  map[string]any{"type": "integer", "minimum": 1},
						"model_rev":   map[string]any{"type": "string", "maxLength": 128},
					},
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
	// DefaultAgentID (v2.4.1) is the Mem0 agent_id (LLM identity)
	// that owns the project. See internal/project/types.go for the
	// resolution priority. Optional; empty = no default agent.
	DefaultAgentID string `json:"default_agent_id,omitempty"`
	// NLIConfig (v2.20.0 T07) is the per-project NLI configuration
	// for the drift_judge pipeline. nil = no project-level override
	// (router falls through to nli.Config{}.DefaultsFor()). Set
	// Enabled=true to take effect. AuthToken is preserved on input
	// but stripped on output (see ProjectCreateResult.NLIConfig).
	NLIConfig *project.NLIConfig `json:"nli_config,omitempty"`
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
	// DefaultAgentID (v2.4.1) echoes the resolved project-level
	// default_agent_id. On idempotent replay, this is the existing
	// row's value (the caller's input is treated as "leave unchanged"
	// when empty per the Store impl).
	DefaultAgentID string `json:"default_agent_id,omitempty"`
	// NLIConfig (v2.20.0 T07) echoes the resolved project-level
	// NLIConfig with AuthToken fields REDACTED. On idempotent
	// replay, this is the existing row's value (preserves the
	// "leave unchanged on empty" rule). nil = no config.
	NLIConfig *project.NLIConfig `json:"nli_config,omitempty"`
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
		DefaultAgentID:  in.DefaultAgentID,
		NLIConfig:       in.NLIConfig,
		CreatedAt:       now,
	}

	// Probe for an existing row first so we can return the original
	// created_at on idempotent replay instead of overwriting it with
	// "now". CreateProject itself is idempotent (INSERT OR IGNORE on
	// the unique index), so this read-before-write is purely a UX
	// concern, not a correctness one. v2.4.1: also surface the
	// existing default_agent_id on idempotent replay so callers can
	// verify what was already configured. v2.20.0 T07: also surface
	// the existing NLIConfig (with AuthToken redacted — the Store
	// layer strips it on read).
	if existing, err := s.GetProject(ctx, in.ProjectID); err == nil && existing != nil {
		return &ProjectCreateResult{
			ProjectID:        existing.ProjectID,
			DisplayName:      existing.DisplayName,
			Description:      existing.Description,
			ConstitutionID:   existing.ConstitutionID,
			ConstitutionVer:  existing.ConstitutionVer,
			DefaultAgentID:   existing.DefaultAgentID,
			NLIConfig:        existing.NLIConfig,
			CreatedAt:        existing.CreatedAt,
			IdempotentReplay: true,
		}, nil
	}

	if err := s.CreateProject(ctx, p); err != nil {
		return nil, err
	}
	// Strip AuthToken from the in-memory p before returning. The DB
	// keeps the token (encrypted-at-rest is the operator's job);
	// the tool result never echoes it.
	p.NLIConfig = p.NLIConfig.Redacted()
	return &ProjectCreateResult{
		ProjectID:        p.ProjectID,
		DisplayName:      p.DisplayName,
		Description:      p.Description,
		ConstitutionID:   p.ConstitutionID,
		ConstitutionVer:  p.ConstitutionVer,
		DefaultAgentID:   p.DefaultAgentID,
		NLIConfig:        p.NLIConfig,
		CreatedAt:        p.CreatedAt,
		IdempotentReplay: false,
	}, nil
}

// validateProjectCreateInput enforces the same kebab-case rule the
// store's PRIMARY KEY constraint expects (3-64 chars, lowercase
// alnum + hyphen, must start and end with alnum). Returning the
// ErrInvalidArgument directly keeps the ToolError mapping in
// errors.go unchanged.
//
// v2.20.0 T07: also delegates to project.NLIConfig.Validate() when
// nli_config is non-nil. nil = no project-level override.
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
	// v2.4.1: default_agent_id length matches the JSON Schema's
	// maxLength (128). Free-form string otherwise — no character
	// class restriction since LLM model names contain dots, dashes,
	// colons, and version numbers (e.g. "claude-sonnet-4.5",
	// "gpt-4o-2024-08-06").
	if len(in.DefaultAgentID) > 128 {
		return store.ErrInvalidArgument
	}
	// v2.20.0 T07: NLIConfig validation. nil = no override (allowed).
	// Non-nil with Enabled=true requires the full ProviderID+Endpoint
	// + positive tunables — see project.NLIConfig.Validate().
	if in.NLIConfig != nil {
		if err := in.NLIConfig.Validate(); err != nil {
			return fmt.Errorf("%w: %v", store.ErrInvalidArgument, err)
		}
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
