// Package recall implements the scoped recall orchestrator per
// ACTIVE_MEMORY_RFC.md §3 M1 + §6.1. The FrameSource implementation
// here is what the gate (Wave 5A.iv) consumes when admitting a
// tools/call.
//
// # Atomicity contract
//   - ONE package: recall
//   - ONE public type: StoreSource (FrameSource impl)
//   - DEPENDS on store.Store (Wave 5A.ii.a + 5A.ii.b.1)
//   - DEPENDS on internal/atomic for frame types
//   - DEPENDS on internal/policy for the FrameSource interface
//
// # Trust boundary
// StoreSource trusts the Store. It does NOT fabricate identity,
// grants, or persona. If the Store doesn't have the data, the
// FrameSource returns (nil, nil) and the gate treats it as a miss.
// The gate then refuses per RFC §A3 (ReasonFrameStale,
// ReasonCapabilityDenied, ReasonPersonaMissing).
//
// # Wave placement
// This is 5A.ii.b.2.a. The cache layer (TTL + INV-5 integrity check)
// lands with 5A.ii.b.2.b. The dark_memory_recall MCP tool + delta
// computation land with 5A.ii.b.2.c.
//
// # Scope (5A.ii.b.2.a)
// Only IdentityFrame and CapabilitiesFrame are fully composed.
// ScopeFrame, DriftFrame, and PersonaFrame return (nil, nil) with
// deferred-to-later-wave comments. The gate tolerates missing scope
// + drift for read-only tools (PreCheck skips them in those paths).
// Missing persona IS a refusal — that's expected to clear when
// 5A.ii.b.2.c ships the constitution loader that PersonaFrame
// will use.
package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/policy"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
)

// DefaultToolGrants is the comma-separated list of MCP tools granted
// to a session when per-session grants are unavailable. Used by
// CapabilitiesFrame composition until Wave 5B ships the vibe_grants
// table with per-session grant resolution.
//
// # Wire-format note (v2.1.2 fix)
//
// The entries use BARE tool names (e.g. "session_start"), not the
// wire-format prefixed names ("dark_memory_session_start"). The
// gate's HasGrant check (internal/atomic/capabilities_frame.go)
// receives `in.ToolName` as bare name from GateInput.ToolName
// (see internal/server/middleware.go buildGateInput). The
// CapabilityTool struct is bare-name too — see
// internal/tools/registry.go:37 ("Name is the bare tool name
// WITHOUT the 'dark_memory_' prefix. The server prepends the
// prefix when registering with mcp-go").
//
// v2.1.0 shipped with the prefix in this constant, which meant
// NO tool was granted by the fallback. The bug went unnoticed
// because the e2e and dual_driver test suites call tool handlers
// directly (t.Handler) — bypassing the gate entirely. v2.1.2
// strips the prefix and adds the 5 new agent_memory_* tools.
//
// # Source of truth
//
// This list MUST stay in sync with internal/tools/registry.go
// canonicalToolOrder. If you add a new tool there, add it here
// too. The TestDefaultToolGrants_CoversCanonicalOrder test in
// internal/recall/assemble_test.go (added in v2.1.2) enforces the
// invariant via a set comparison.
//
// # Security note
//
// These defaults grant access to all canonical MCP tools plus global
// read-only project scope. This is INTERIM/DEVELOPMENT-ONLY
// behavior — operators MUST NOT enable this fallback in
// production. The expected production posture is: no fallback
// grants, sessions start with empty grants, and the operator
// uses dark_memory_grant_manage (5B) to add per-session grants.
//
// Source label for these defaults is "default:<project>" so
// operators can grep write_audit rows for sessions using the
// fallback.
const DefaultToolGrants = "project_create," + // PROJECT (1)
	"session_start,session_resume,session_status,session_close," + // SESSION (4)
	"research_topic,research_recall,research_resume_thread," + // RESEARCH (3)
	"agent_bootstrap,agent_recommend_companions,agent_detect_environment," + // AGENT_BOOTSTRAP (3) — v2.6.0
	"vibe_publish,vibe_spec,pipeline_status,resolve_drift," + // VIBE (4)
	"artifact_context,spec_context,session_context,recall," + // CONTEXT (4)
	"agent_memory_save,agent_memory_list,agent_memory_recall,agent_memory_get,agent_memory_update,agent_memory_archive,agent_memory_delegate,agent_memory_entities,subagent_register,subagent_unregister," + // AGENT_MEMORY (10) — v2.1.0 (5) + v2.3.0 (1: recall) + v2.8.0-alpha C2 (2: subagent register/unregister) + v2.9.0-alpha PR-3 (1: entities) + v2.9.3 (1: delegate)
	"mindset_apply," + // MINDSET (1) — v2.7.0-alpha
	"delegate_intent," + // DELEGATION (1) — Wave 5C (A1: handle/delegate/refuse)
	"judge,consensus,judgment_history," + // JUDGE (3)
	"active_policy,load_constitution," + // POLICY (2)
	"memory_state,writes,anomalies,health_ping," + // OBSERVABILITY (4)
	"error_list,error_get,error_summary,error_resolve," + // ERROR_OBS (4) — v2.11.0 (spec 757: Error Observatory backlog + triage)
	"admin_migrate,admin_schema_status,admin_vacuum," + // ADMIN (3)
	"vlp_handle_event," + // L6-VLP (1) — DMAP v1.1
	"embedder_setup_prompt" // EMBEDDER (1) — v2.9.0-alpha PR-2 (consent gate, row 164 §3)

// DefaultTone is the fallback persona tone when the active
// constitution doesn't have a [tone] section. Used by PersonaFrame.
const DefaultTone = "technical"

// DefaultVoice is the fallback persona voice. Used by PersonaFrame
// until 5A.ii.b.2.c adds constitution TOML parsing for [voice] /
// [claims] / [tone] sections.
const DefaultVoice = "Concise, structured, JSON-first. Headers for sections. Short paragraphs. Refuse speculation. Cite source IDs in tool invocations."

// DefaultClaimsPolicy is the fallback persona claims policy.
const DefaultClaimsPolicy = "Only claim what is grounded in dark.db state or session evidence. Refuse speculation. Cite source IDs in tool invocations."

// StoreSource is the canonical FrameSource implementation. It
// composes frames from the Store on every call (no cache — that's
// Wave 5A.ii.b.2.b). Returns (nil, nil) for any frame it cannot
// compose; the gate treats (nil, nil) as a cache miss.
//
// # Thread-safety
// StoreSource holds a Store reference; the Store is responsible for
// its own thread-safety (s.mu on sqlite, pool semantics on
// postgres). StoreSource itself has no mutable state beyond the
// injected Now clock.
type StoreSource struct {
	// Store is the data source. Required.
	Store store.Store
	// Now is the wall-clock source. Defaults to time.Now if nil.
	// Injected for tests that want to pin ComposedAt.
	Now func() time.Time
}

// NewStoreSource constructs a StoreSource. `now` defaults to
// time.Now when nil.
func NewStoreSource(st store.Store, now func() time.Time) *StoreSource {
	if now == nil {
		now = time.Now
	}
	return &StoreSource{Store: st, Now: now}
}

// IdentityFrame composes from the sessions row. Returns (nil, nil)
// if no session exists for sessionID (cache miss; gate refuses).
//
// The actor is the operator id from the session row. constitutionID
// + constitutionVer are inherited from the session row directly.
//
// # Security note (5A.ii.b.2.a scope)
//
// canaryActive is hardcoded to false for this wave. The canary
// state lives in safety.Holder (internal/safety), which
// 5A.ii.b.2.b will thread through. Until then, the gate's canary
// check (INV-3) is effectively unverified. Operators testing the
// gate pre-5A.ii.b.2.b should treat this as a dev-only posture.
func (s *StoreSource) IdentityFrame(ctx context.Context, sessionID string) (*atomic.IdentityFrame, error) {
	sess, err := s.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}
	// Constitution resolution. SessionStartInput makes constitution_id +
	// constitution_ver optional (omitempty), so a session may carry empty
	// constitution fields. PersonaFrame (below) already mirrors this
	// fallback to s.Store.ActiveConstitution(); IdentityFrame must do
	// the same or NewIdentityFrame returns ErrEmptyConstitutionID and
	// the gate refuses every session-bound tool call with "identity
	// unavailable for this session".
	constitutionID := sess.ConstitutionID
	constitutionVer := sess.ConstitutionVer
	if constitutionID == "" || constitutionVer == "" {
		constitutionID, constitutionVer, _ = s.Store.ActiveConstitution(ctx)
	}
	return atomic.NewIdentityFrame(
		sess.Operator, // actor (the user-facing principal)
		sess.Operator, // operator (same field — distinct semantic, same value)
		sessionID,
		constitutionID,
		constitutionVer,
		false, // canary — see comment above
	)
}

// ScopeFrame composes from vlp_state (OpenSpecID + tasks + last drift).
// Returns (nil, nil) only when no vlp_state row exists yet for the
// session (legitimate cache-miss path — session just started).
//
// # 5A.ii.b.2.c scope
// Minimal implementation: OpenSpecID + last drift verdict + composed_at.
// Tasks and evidence-pointers are populated as empty slices. A future
// wave can layer on full task/evidence fetching.
func (s *StoreSource) ScopeFrame(ctx context.Context, sessionID string) (*atomic.ScopeFrame, error) {
	state, err := s.Store.GetVLPState(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}
	// state.State is the int value of internal/vlp.State (e.g. StateSpecActive = 3).
	// We map to vlp_state.state and treat any non-zero as "spec open" for
	// scope-frame purposes. OpenSpecID is the Wave 5X.4 column
	// (vlp_state.open_spec_id) — the actual spec_id the session is
	// working on. Falls through to 0 when no spec is open.
	openSpecID := state.OpenSpecID
	lastVerdict := state.LastVerdict
	lastDriftAt := parseTimestamp(state.UpdatedAt)
	return atomic.NewScopeFrame(
		sessionID,
		openSpecID,
		nil, // open tasks — deferred
		nil, // evidence pointers — deferred
		lastVerdict,
		lastDriftAt,
	)
}

// CapabilitiesFrame composes from the default tool grant list +
// the active project scope. Real per-session grants land with
// Wave 5B (vibe_grants table). Returns (nil, nil) if the session
// doesn't exist.
//
// Source label is "default:<project>" so operators can grep
// write_audit rows for sessions using default grants.
func (s *StoreSource) CapabilitiesFrame(ctx context.Context, sessionID string) (*atomic.CapabilitiesFrame, error) {
	sess, err := s.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}
	projectID := s.Store.ActiveProject()
	grantedAt := s.Now()
	tools := make([]atomic.ToolGrant, 0, 24)
	for _, name := range strings.Split(DefaultToolGrants, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		tools = append(tools, atomic.ToolGrant{
			ToolName:  name,
			Scope:     "*", // default = global (all projects)
			GrantedAt: grantedAt,
		})
	}
	scopes := []atomic.ScopeGrant{
		{ProjectID: projectID, ReadOnly: false, GrantedAt: grantedAt},
		{ProjectID: "*", ReadOnly: true, GrantedAt: grantedAt},
	}
	return atomic.NewCapabilitiesFrame(
		projectID,
		sessionID,
		tools,
		scopes,
		time.Time{}, // zero = session-lifetime
		"default:"+projectID,
	)
}

// DriftFrame composes from the latest sdd_evaluations row for the
// session's active spec. Returns (nil, nil) when no drift verdict
// exists yet (legitimate cache-miss path).
//
// # 5A.ii.b.2.c scope
// Looks up vlp_state to find the active spec, then queries
// sdd_evaluations for the latest drift_judge verdict on that spec.
// Verdict + pendingItems are extracted from verdict_json. Reasoning
// is captured in pendingItems as a single string (truncated to 200
// chars); full reasoning is in the source sdd_evaluations row.
func (s *StoreSource) DriftFrame(ctx context.Context, sessionID string) (*atomic.DriftFrame, error) {
	state, err := s.Store.GetVLPState(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if state == nil || state.State == 0 {
		// No active spec → DriftFrame is a zero-value (no spec, no verdict).
		return atomic.NewDriftFrame(sessionID, 0, "", time.Time{}, nil)
	}
	// Find latest sdd_evaluations for this spec. target_id uses the
	// vlp_state row id as the scope anchor (same as ScopeFrame).
	evaluations, err := s.Store.ListSDDEvaluations(ctx, ssd.ListFilters{
		TargetType: "spec",
		TargetID:   fmt.Sprintf("%d", state.ID),
		Limit:      1,
	})
	if err != nil {
		return nil, fmt.Errorf("recall: DriftFrame ListSDDEvaluations: %w", err)
	}
	if len(evaluations) == 0 {
		return atomic.NewDriftFrame(sessionID, 0, "", time.Time{}, nil)
	}
	// Parse verdict_json to extract verdict + reasoning.
	latest := evaluations[0]
	verdict, reasoning, err := parseSDDVerdict(latest.VerdictJSON)
	if err != nil {
		return nil, fmt.Errorf("recall: DriftFrame parse verdict_json id=%d: %w", latest.ID, err)
	}
	if verdict == "" {
		return atomic.NewDriftFrame(sessionID, 0, "", time.Time{}, nil)
	}
	lastReconciledAt := parseTimestamp(latest.CreatedAt)
	pendingItems := []string{}
	if verdict == "drift_detected" && reasoning != "" {
		pendingItems = []string{truncate(reasoning, 200)}
	}
	return atomic.NewDriftFrame(sessionID, state.ID, verdict, lastReconciledAt, pendingItems)
}

// PersonaFrame composes from the active constitution. Returns (nil, nil)
// when no active constitution is bound to the session.
//
// # 5A.ii.b.2.c scope
// Loads constitution via Store.GetConstitution + parses the
// ParsedJSON blob for [persona] section (voice, claims_policy,
// tone). Falls back to the Default* constants when the section
// is missing or the constitution can't be loaded.
//
// Note: postgres Store.GetConstitution currently returns notImpl.
// On postgres, this method returns (nil, nil) — the gate's persona
// check will refuse on postgres until GetConstitution is implemented
// there. Tracked as a known gap, not a blocker (sqlite is the
// canonical driver).
func (s *StoreSource) PersonaFrame(ctx context.Context, sessionID string) (*atomic.PersonaFrame, error) {
	sess, err := s.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}
	// Use session-bound constitution id+ver; fall back to active.
	constitutionID := sess.ConstitutionID
	constitutionVer := sess.ConstitutionVer
	if constitutionID == "" || constitutionVer == "" {
		constitutionID, constitutionVer, _ = s.Store.ActiveConstitution(ctx)
	}
	if constitutionID == "" || constitutionVer == "" {
		return nil, nil
	}
	c, err := s.Store.GetConstitution(ctx, constitutionID, constitutionVer)
	if err != nil || c == nil {
		// Postgres notImpl case OR genuinely missing → use defaults
		// with the active constitution id+ver as the binding.
		return atomic.NewPersonaFrame(
			constitutionID,
			constitutionVer,
			"", // brand_id — empty until 5B
			DefaultVoice,
			DefaultClaimsPolicy,
			"", // refusal_pattern — empty until 5A.vi
			DefaultTone,
		)
	}
	// Parse ParsedJSON for [persona] section. Falls back to defaults.
	voice, claims, tone := parsePersonaFromConstitution(c.ParsedJSON)
	return atomic.NewPersonaFrame(
		constitutionID,
		constitutionVer,
		"", // brand_id — empty until 5B
		voice,
		claims,
		"", // refusal_pattern — empty until 5A.vi
		tone,
	)
}

// Compile-time check: StoreSource implements policy.FrameSource.
var _ policy.FrameSource = (*StoreSource)(nil)

// parseTimestamp tries RFC3339Nano first, then RFC3339, and returns
// time.Time{} on failure (not an error — drift verdicts may not have
// a parseable timestamp, which is treated as "unknown" not "broken").
func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// sddVerdictJSON is the shape of sdd_evaluations.verdict_json for
// drift_judge verdicts. The drift_judge evaluator writes this shape
// (see internal/ssd drift_judge evaluator). Other eval_types may use
// different shapes; parseSDDVerdict only handles drift_judge.
type sddVerdictJSON struct {
	Verdict  string `json:"verdict"`  // aligned | drift_detected | needs_human
	Reasoning string `json:"reasoning"`
}

// parseSDDVerdict extracts verdict + reasoning from a verdict_json
// blob. Returns ("", "", nil) if the JSON parses but doesn't have a
// recognized verdict (e.g. wrong eval_type). Returns an error if
// the JSON doesn't parse at all.
func parseSDDVerdict(verdictJSON string) (verdict, reasoning string, err error) {
	if verdictJSON == "" {
		return "", "", nil
	}
	var v sddVerdictJSON
	if err := json.Unmarshal([]byte(verdictJSON), &v); err != nil {
		return "", "", err
	}
	switch v.Verdict {
	case "aligned", "drift_detected", "needs_human":
		return v.Verdict, v.Reasoning, nil
	default:
		return "", "", nil
	}
}

// constitutionPersona is the shape of the [persona] section in the
// constitution TOML (parsed to JSON via the constitution loader).
// All fields optional; missing fields fall back to defaults.
type constitutionPersona struct {
	Voice        string `json:"voice"`
	ClaimsPolicy string `json:"claims_policy"`
	Tone         string `json:"tone"`
}

// parsePersonaFromConstitution extracts the [persona] section from
// a constitution's ParsedJSON blob. Returns defaults if the section
// is missing or the JSON doesn't parse.
func parsePersonaFromConstitution(parsedJSON string) (voice, claims, tone string) {
	voice = DefaultVoice
	claims = DefaultClaimsPolicy
	tone = DefaultTone
	if parsedJSON == "" {
		return voice, claims, tone
	}
	// The constitution ParsedJSON is the full TOML-as-JSON object.
	// Look for the top-level "persona" key.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(parsedJSON), &raw); err != nil {
		return voice, claims, tone
	}
	personaRaw, ok := raw["persona"]
	if !ok {
		return voice, claims, tone
	}
	var p constitutionPersona
	if err := json.Unmarshal(personaRaw, &p); err != nil {
		return voice, claims, tone
	}
	if p.Voice != "" {
		voice = p.Voice
	}
	if p.ClaimsPolicy != "" {
		claims = p.ClaimsPolicy
	}
	if p.Tone != "" {
		tone = p.Tone
	}
	return voice, claims, tone
}

// truncate caps a string at maxBytes. If truncation happens, the
// last 3 chars become "...". Used for pendingItems which has a
// 200-char budget.
func truncate(s string, maxBytes int) string {
	if maxBytes <= 3 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes-3] + "..."
}