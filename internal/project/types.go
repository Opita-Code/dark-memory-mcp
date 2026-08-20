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

import (
	"fmt"
)

// Project is the tenant unit. One Project = one vibe-flow workspace.
// Cross-project reads are opt-in (dark_research_*) but the default
// is strict isolation.
type Project struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id"` // public id, kebab-case, unique
	DisplayName     string `json:"display_name"`
	Description     string `json:"description,omitempty"`
	ConstitutionID  string `json:"constitution_id,omitempty"`
	ConstitutionVer string `json:"constitution_ver,omitempty"`
	CreatedAt       string `json:"created_at"`
	ArchivedAt      string `json:"archived_at,omitempty"`       // soft delete
	ParentProjectID string `json:"parent_project_id,omitempty"` // for sub-projects

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

	// NLIConfig (v28, v2.20.0 T07) is the per-project NLI
	// configuration for the drift_judge pipeline. Pointer-typed so
	// nil means "no project-level override; fall through to
	// nli.Config{}.DefaultsFor()" at the wiring layer (T08).
	//
	// Stored as one JSON-encoded TEXT column (nli_config_json). One
	// column, not 8 — atomic, no migration fragility, schema-driven
	// schema. Idempotent replay preserves the existing JSON if
	// caller passes nil/empty (mirroring default_agent_id pattern).
	//
	// Reads: parse failures are logged and the field is left nil
	// (graceful degradation — same posture as the other per-project
	// tunables). Operator can fix the JSON via project_create replay
	// once the column is populated correctly.
	NLIConfig *NLIConfig `json:"nli_config,omitempty"`
}

// IsArchived returns true if the project has been soft-deleted.
func (p *Project) IsArchived() bool { return p.ArchivedAt != "" }

// NLIConfig is the per-project NLI configuration (v2.20.0 T07, spec
// 1276). One JSON-encoded TEXT column in projects (migration v28).
//
// # Hard invariants (immutable)
//
//   - When Enabled == true, the Primary MUST have non-empty
//     ProviderID + Endpoint; Tunables must be positive.
//   - When Enabled == false, the Router at T08 falls through to
//     nli.Config{}.DefaultsFor() with no project override.
//
// # Tunable (per-project)
//
//   - Primary{ProviderID, Endpoint, AuthToken, TimeoutMS, LabelMap,
//     ModelRev}: identity of the primary NLI provider.
//   - Fallback{...}: identity of the optional fallback provider.
//   - FallbackEnabled: false → Router does not retry on transient
//     errors (single-shot). true → Router retries on Unavailable /
//     Timeout / RateLimited (NOT on BadResponse — that's a contract
//     bug both providers would have, per nli.ErrProviderBadResponse
//     invariant in T05).
//   - LatencyBudgetMS: 0 → nli.DefaultLatencyBudgetMS (10000).
//   - MaxPremiseBytes: 0 → nli.DefaultMaxPremiseBytes (65536).
//   - MaxHypothesisBytes: 0 → nli.DefaultMaxHypothesisBytes (8192).
//   - MaxCacheEntries: 0 → nli.DefaultMaxCacheEntries (10000). Set
//     to 0 to disable caching (we still keep the LRU available;
//     a follow-up may add Enable bool, but v2.20.0 keeps the
//     surface flat).
//   - CacheTTLSeconds: 0 → nli.DefaultCacheTTL (24h = 86400).
//     int64 not time.Duration so the JSON wire format is portable.
//
// # Threat model
//
// NLIConfig.Endpoint + AuthToken together grant the project's drift
// judge to invoke the LLM endpoint. The AuthToken field is the
// HUGGINGFACE_TOKEN or self-hosted bearer; never logged, never
// returned in any tool result (v2.20.0 hygiene rule from T05).
type NLIConfig struct {
	Enabled           bool       `json:"enabled"`
	Primary           NLIPrimary `json:"primary"`
	Fallback          NLIPrimary `json:"fallback,omitempty"`
	FallbackEnabled   bool       `json:"fallback_enabled,omitempty"`
	LatencyBudgetMS   int64      `json:"latency_budget_ms,omitempty"`
	MaxPremiseBytes   int        `json:"max_premise_bytes,omitempty"`
	MaxHypothesisBytes int       `json:"max_hypothesis_bytes,omitempty"`
	MaxCacheEntries   int        `json:"max_cache_entries,omitempty"`
	CacheTTLSeconds   int64      `json:"cache_ttl_seconds,omitempty"`
}

// NLIPrimary is one provider's settings within NLIConfig.
//
// Mirrors nli.ProviderConfig 1:1 minus LabelMap (a map serializes
// poorly to JSON and is operationally a per-model factory default;
// the actual LabelMap is looked up by ProviderID at T08 wiring from
// the embedded nli.Config default map). ModelRev is best-effort;
// empty means "provider uses the default revision".
//
// AuthToken is never echoed back from any tool result. Stored as
// cleartext in nli_config_json (encrypted-at-rest is the operator's
// job — full-disk encryption on the DB host). On Postgres the
// operator may encrypt the column via pgcrypto in a follow-up.
type NLIPrimary struct {
	ProviderID string `json:"provider_id"`
	Endpoint   string `json:"endpoint"`
	AuthToken  string `json:"auth_token,omitempty"`
	TimeoutMS  int64  `json:"timeout_ms,omitempty"`
	ModelRev   string `json:"model_rev,omitempty"`
}

// Validate enforces the hard invariants. Returns nil if NLIConfig
// is well-formed, or a non-nil error (always wrapping a sealed
// sentinel) if any rule is violated.
//
// Rules:
//   - Nil receiver → return nil (nil means "no project-level
//     override, fall through to defaults" — see nli.Config.DefaultsFor()).
//   - Enabled false → no validation needed (router falls through).
//   - Enabled true:
//       Primary.ProviderID non-empty
//       Primary.Endpoint   non-empty
//       Primary.TimeoutMS  > 0   (0 = reject; use nli.DefaultTimeoutMS)
//       LatencyBudgetMS    > 0
//       MaxPremiseBytes    >= 64 (lower bound prevents degenerate configs)
//       MaxHypothesisBytes >= 16
//       MaxCacheEntries    >= 100
//       CacheTTLSeconds    > 0
//       If FallbackEnabled: Fallback.ProviderID + Endpoint non-empty.
func (c *NLIConfig) Validate() error {
	if c == nil {
		return nil // nil = no override; fall through to defaults
	}
	if !c.Enabled {
		return nil
	}
	if c.Primary.ProviderID == "" {
		return fmt.Errorf("nli_config: %w: primary.provider_id required", ErrNLIConfigInvalid)
	}
	if c.Primary.Endpoint == "" {
		return fmt.Errorf("nli_config: %w: primary.endpoint required", ErrNLIConfigInvalid)
	}
	if c.Primary.TimeoutMS <= 0 {
		return fmt.Errorf("nli_config: %w: primary.timeout_ms must be > 0", ErrNLIConfigInvalid)
	}
	if c.LatencyBudgetMS <= 0 {
		return fmt.Errorf("nli_config: %w: latency_budget_ms must be > 0", ErrNLIConfigInvalid)
	}
	if c.MaxPremiseBytes < 64 {
		return fmt.Errorf("nli_config: %w: max_premise_bytes must be >= 64", ErrNLIConfigInvalid)
	}
	if c.MaxHypothesisBytes < 16 {
		return fmt.Errorf("nli_config: %w: max_hypothesis_bytes must be >= 16", ErrNLIConfigInvalid)
	}
	if c.MaxCacheEntries < 100 {
		return fmt.Errorf("nli_config: %w: max_cache_entries must be >= 100", ErrNLIConfigInvalid)
	}
	if c.CacheTTLSeconds <= 0 {
		return fmt.Errorf("nli_config: %w: cache_ttl_seconds must be > 0", ErrNLIConfigInvalid)
	}
	if c.FallbackEnabled {
		if c.Fallback.ProviderID == "" {
			return fmt.Errorf("nli_config: %w: fallback.provider_id required when fallback_enabled", ErrNLIConfigInvalid)
		}
		if c.Fallback.Endpoint == "" {
			return fmt.Errorf("nli_config: %w: fallback.endpoint required when fallback_enabled", ErrNLIConfigInvalid)
		}
	}
	return nil
}

// Redacted returns a copy of c with AuthToken fields cleared. Use
// this when serializing NLIConfig for tool OUTPUT (project_create
// result, GetProject, ListProjects) so the operator-facing surface
// never echoes the secret.
//
// The Store layer's GetProject / ListProjects apply this to *every*
// returned row. The Tool layer's runProjectCreate applies this to
// the result. The stored JSON retains the token (encrypted-at-rest
// is the operator's responsibility) so T08 can wire the provider.
func (c *NLIConfig) Redacted() *NLIConfig {
	if c == nil {
		return nil
	}
	r := *c
	r.Primary.AuthToken = ""
	r.Fallback.AuthToken = ""
	return &r
}

// ErrNLIConfigInvalid is the sealed sentinel for validation failures.
// Callers classify via errors.Is — do not match strings.
type NLIConfigInvalid struct{}

func (e *NLIConfigInvalid) Error() string { return "nli_config: invalid" }

// ErrNLIConfigInvalid wrapper. We use a struct so errors.Is can
// match via pointer identity; struct value cannot be a sentinel
// directly. The wrapper is in package project because NLIConfig is
// there.
var ErrNLIConfigInvalid error = &NLIConfigInvalid{}

// Membership is the link between an operator (human or AI agent) and
// a Project. Not enforced at the database level yet — used for
// authorization at the orchestrator level in v1.0. Future versions
// will use this table for RLS-style policy via a per-tenant role.
type Membership struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	Operator  string `json:"operator"`
	Role      string `json:"role"` // "owner" | "editor" | "viewer"
	GrantedAt string `json:"granted_at"`
	GrantedBy string `json:"granted_by,omitempty"`
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
