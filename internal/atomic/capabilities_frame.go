// CapabilitiesFrame — the canonical implementation of the
// Capabilities frame kind per ACTIVE_MEMORY_RFC.md §2 A2 (table).
// CapabilitiesFrame captures the capability grants resolved at
// session start: granted tools, granted scopes, and the expiration
// timestamp. Source enumerates where the grants came from
// (constitution, mod, or operator env DARK_GRANTS).
//
// CapabilitiesFrame is composed AFTER EvidenceFrame and BEFORE
// DriftFrame. The Gate checks capabilities at every tools/call;
// a missing grant yields ErrCapabilityNotGranted (per RFC §A4).
//
// # Atomicity contract
//   - ONE type: CapabilitiesFrame
//   - ONE constructor: NewCapabilitiesFrame
//   - FOUR invariants enforced in Validate
//   - THREE derived behaviors: Hash determinism, Render canonical,
//     HasGrant read-only accessor
package atomic

import (
	"errors"
	"fmt"
	"time"
)

// MaxCapabilitiesFrameAge is the default staleness budget for
// CapabilitiesFrame. Default 15 minutes — grants can be revoked
// mid-session but the recomposition cost is moderate.
const MaxCapabilitiesFrameAge = 15 * time.Minute

// ToolGrant is one entry in GrantedTools. A grant enables the LLM
// to call the named tool restricted to the named scope (project_id
// or "*" for global).
type ToolGrant struct {
	ToolName  string    `json:"tool_name"`
	Scope     string    `json:"scope"`             // project_id or "*"
	GrantedAt time.Time `json:"granted_at"`
}

// ScopeGrant is one entry in GrantedScopes. A scope grant enables
// the LLM to access the named project with the read-only-or-write mode.
type ScopeGrant struct {
	ProjectID string    `json:"project_id"`
	ReadOnly  bool      `json:"read_only"`
	GrantedAt time.Time `json:"granted_at"`
}

// CapabilitiesFrame is the runtime implementation of Frame for the
// Capabilities kind.
type CapabilitiesFrame struct {
	ComposedAtValue time.Time `json:"composed_at"`

	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`

	// GrantedTools is the resolved set of MCP tool names the LLM may
	// invoke this session. Empty means "no tools granted" — gate
	// refuses every call with ErrCapabilityNotGranted.
	GrantedTools []ToolGrant `json:"granted_tools,omitempty"`

	// GrantedScopes is the resolved set of project scopes the LLM
	// may access. Empty means "no scopes granted" — gate refuses
	// every call with ErrScopeRequired.
	GrantedScopes []ScopeGrant `json:"granted_scopes,omitempty"`

	// GrantedExpiresAt is when these grants expire. Zero means
	// "session-lifetime" — grants persist until session_close.
	GrantedExpiresAt time.Time `json:"granted_expires_at,omitempty"`

	// Source describes where the grants came from. One of:
	//   "constitution"        — derived from active constitution
	//   "mod:<mod_id>"        — added by a specific mod (e.g. red-team)
	//   "env:DARK_GRANTS"     — set by operator via env (most explicit)
	//   "default:<project>"   — project default fallback
	// Multiple sources concatenate into a comma-separated list.
	Source string `json:"source,omitempty"`
}

// Errors returned by CapabilitiesFrame methods.
var (
	ErrCapabilitiesEmptyProjectID    = errors.New("atomic: capabilities frame project_id is empty")
	ErrCapabilitiesEmptySessionID    = errors.New("atomic: capabilities frame session_id is empty")
	ErrCapabilitiesToolMissingName   = errors.New("atomic: capabilities frame tool_grant missing tool_name")
	ErrCapabilitiesScopeMissingPID   = errors.New("atomic: capabilities frame scope_grant missing project_id")
	ErrCapabilitiesZeroComposed      = errors.New("atomic: capabilities frame composed_at is zero")
	ErrCapabilitiesStale             = errors.New("atomic: capabilities frame is stale (older than MaxCapabilitiesFrameAge)")
)

// NewCapabilitiesFrame builds a CapabilitiesFrame with the required
// identity binding (ProjectID, SessionID). tools and scopes are the
// resolved grant sets; expiresAt is when grants lapse.
func NewCapabilitiesFrame(projectID, sessionID string, tools []ToolGrant, scopes []ScopeGrant, expiresAt time.Time, source string) (*CapabilitiesFrame, error) {
	if projectID == "" {
		return nil, ErrCapabilitiesEmptyProjectID
	}
	if sessionID == "" {
		return nil, ErrCapabilitiesEmptySessionID
	}
	for i, t := range tools {
		if t.ToolName == "" {
			return nil, fmt.Errorf("%w: index %d", ErrCapabilitiesToolMissingName, i)
		}
	}
	for i, s := range scopes {
		if s.ProjectID == "" {
			return nil, fmt.Errorf("%w: index %d", ErrCapabilitiesScopeMissingPID, i)
		}
	}
	return &CapabilitiesFrame{
		ComposedAtValue:  time.Now(),
		ProjectID:        projectID,
		SessionID:        sessionID,
		GrantedTools:     tools,
		GrantedScopes:    scopes,
		GrantedExpiresAt: expiresAt,
		Source:           source,
	}, nil
}

// Kind implements Frame.
func (f *CapabilitiesFrame) Kind() FrameKind { return FrameCapabilities }

// ComposedAt implements Frame.
func (f *CapabilitiesFrame) ComposedAt() time.Time { return f.ComposedAtValue }

// Validate implements Frame.
func (f *CapabilitiesFrame) Validate() error {
	if f == nil {
		return ErrCapabilitiesZeroComposed
	}
	if f.ProjectID == "" {
		return ErrCapabilitiesEmptyProjectID
	}
	if f.SessionID == "" {
		return ErrCapabilitiesEmptySessionID
	}
	if f.ComposedAtValue.IsZero() {
		return ErrCapabilitiesZeroComposed
	}
	if age := time.Since(f.ComposedAtValue); age > MaxCapabilitiesFrameAge {
		return fmt.Errorf("%w: age=%s budget=%s", ErrCapabilitiesStale, age, MaxCapabilitiesFrameAge)
	}
	return nil
}

// Hash implements Frame via canonical JSON.
func (f *CapabilitiesFrame) Hash() ([32]byte, error) { return hashCanonical(f) }

// Render implements Frame.
func (f *CapabilitiesFrame) Render() ([]byte, error) { return jsonMarshal(f) }

// HasGrant reports whether the named tool is granted to this session.
// Performs case-sensitive exact match on ToolName.
func (f *CapabilitiesFrame) HasGrant(toolName string) bool {
	for _, g := range f.GrantedTools {
		if g.ToolName == toolName {
			return true
		}
	}
	return false
}

// HasProjectAccess reports whether the named project_id is in
// GrantedScopes. "*"-prefixed scope grants are global (return true
// for any project check).
func (f *CapabilitiesFrame) HasProjectAccess(projectID string) bool {
	for _, s := range f.GrantedScopes {
		if s.ProjectID == "*" || s.ProjectID == projectID {
			return true
		}
	}
	return false
}

// IsExpired reports whether the grant expiration has passed at the
// given time. Zero expiresAt means "session-lifetime" (never expired).
func (f *CapabilitiesFrame) IsExpired(now time.Time) bool {
	if f.GrantedExpiresAt.IsZero() {
		return false
	}
	return !now.Before(f.GrantedExpiresAt)
}

// Wave 5X.2: compile-time guard that *CapabilitiesFrame satisfies
// atomic.Frame. Catches Hash() signature mismatches before they
// reach production (the previous mismatch was latent because no
// caller typed a variable as atomic.Frame).
var _ Frame = (*CapabilitiesFrame)(nil)

