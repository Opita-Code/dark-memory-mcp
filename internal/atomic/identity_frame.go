// IdentityFrame — the canonical implementation of the Identity frame
// kind per ACTIVE_MEMORY_RFC.md §3 (A2 table). IdentityFrame is the
// first frame composed at every gate-mediated tools/call (per
// composition order in internal/atomic.AllFrameKinds). It binds:
//
//   - Who is acting (Actor, Operator)
//   - Which session is in scope (SessionID)
//   - Which constitution is active (ConstitutionID + ConstitutionVer)
//   - Whether the canary check is currently active (CanaryActive)
//
// IdentityFrame is the FIRST frame composed and the LAST frame
// invalidated: if the constitution changes mid-session, every downstream
// frame must be recomposed. The composition step cross-checks
// SessionID and ConstitutionID against the active WriteContext before
// returning a valid frame.
//
// # Atomicity contract
//   - ONE type: IdentityFrame
//   - ONE constructor: NewIdentityFrame
//   - TWO invariants enforced in Validate:
//       1. Actor + Operator non-empty (empty = unconfigured session)
//       2. ConstitutionID + Ver match the active binding (resolved by the caller)
//   - THREE derived behaviors:
//       1. Hash determinism: same input => same SHA-256
//       2. Stale detection via ComposedAt() + MAX_FRAME_AGE (caller-side check)
//       3. Cross-session binding check via VerifyAgainstWriteAudit
package atomic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxIdentityFrameAge is the default staleness budget for IdentityFrame.
// Frames older than this MUST be recomposed before being attached to a
// response envelope. Default 15 minutes; operators may tune via env.
const MaxIdentityFrameAge = 15 * time.Minute

// IdentityFrame is the runtime implementation of Frame for the Identity kind.
type IdentityFrame struct {
	// ComposedAt is set by NewIdentityFrame; not user-supplied.
	ComposedAtValue time.Time `json:"composed_at"`

	// Required fields. Actor is the immediate actor (often the
	// orchestrator function name); Operator is the human/system who
	// owns the session (e.g. "dark-agent" in this codebase).
	Actor     string `json:"actor"`
	Operator  string `json:"operator"`
	SessionID string `json:"session_id"`

	// Required constitution binding. Composition cross-checks
	// ConstitutionID + Ver against the active WriteContext.
	ConstitutionID string `json:"constitution_id"`
	ConstitutionVer string `json:"constitution_ver"`

	// CanaryActive reports whether the canary check is currently
	// in scope (set true if INV-3 has been armed for this session,
	// false if canary was disabled by an admin call). The Gate uses
	// this to decide whether to invoke canary verification on payload
	// writes during the current call.
	CanaryActive bool `json:"canary_active"`
}

// Errors returned by IdentityFrame methods.
var (
	ErrEmptyActor           = errors.New("atomic: identity frame actor is empty")
	ErrEmptyOperator        = errors.New("atomic: identity frame operator is empty")
	ErrEmptySessionID       = errors.New("atomic: identity frame session_id is empty")
	ErrEmptyConstitutionID  = errors.New("atomic: identity frame constitution_id is empty")
	ErrEmptyConstitutionVer = errors.New("atomic: identity frame constitution_ver is empty")
	ErrZeroComposedAt       = errors.New("atomic: identity frame composed_at is zero")
	ErrStaleFrame           = errors.New("atomic: identity frame is stale (composed_at older than MaxIdentityFrameAge)")
	ErrCrossSessionBinding  = errors.New("atomic: identity frame session_id does not match write_audit row")
	ErrConstitutionMismatch = errors.New("atomic: identity frame constitution binding does not match write_audit row")
)

// NewIdentityFrame builds an IdentityFrame with the required fields.
// ComposedAt is set to time.Now() at construction; Composition is the
// canonical pivot for staleness checks.
//
// The function does NOT cross-check session_id or constitution against
// any external state (that's VerifyAgainstWriteAudit's job, called
// post-construction). Use this constructor for in-flight creation; use
// NewIdentityFrameFromWriteAudit for state-bound construction.
func NewIdentityFrame(actor, operator, sessionID, constitutionID, constitutionVer string, canaryActive bool) (*IdentityFrame, error) {
	if actor == "" {
		return nil, ErrEmptyActor
	}
	if operator == "" {
		return nil, ErrEmptyOperator
	}
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	if constitutionID == "" {
		return nil, ErrEmptyConstitutionID
	}
	if constitutionVer == "" {
		return nil, ErrEmptyConstitutionVer
	}
	return &IdentityFrame{
		ComposedAtValue:  time.Now(),
		Actor:            actor,
		Operator:         operator,
		SessionID:        sessionID,
		ConstitutionID:   constitutionID,
		ConstitutionVer:  constitutionVer,
		CanaryActive:     canaryActive,
	}, nil
}

// Kind implements Frame.
func (f *IdentityFrame) Kind() FrameKind { return FrameIdentity }

// ComposedAt implements Frame.
func (f *IdentityFrame) ComposedAt() time.Time { return f.ComposedAtValue }

// Validate implements Frame.
//
// Enforces:
//   - All required fields non-empty.
//   - ComposedAt is not the zero time.
//   - ComposedAt is not older than MaxIdentityFrameAge relative to time.Now().
//
// Returns nil for a healthy frame; first error encountered otherwise.
func (f *IdentityFrame) Validate() error {
	if f == nil {
		return ErrZeroComposedAt
	}
	if f.Actor == "" {
		return ErrEmptyActor
	}
	if f.Operator == "" {
		return ErrEmptyOperator
	}
	if f.SessionID == "" {
		return ErrEmptySessionID
	}
	if f.ConstitutionID == "" {
		return ErrEmptyConstitutionID
	}
	if f.ConstitutionVer == "" {
		return ErrEmptyConstitutionVer
	}
	if f.ComposedAtValue.IsZero() {
		return ErrZeroComposedAt
	}
	if age := time.Since(f.ComposedAtValue); age > MaxIdentityFrameAge {
		return fmt.Errorf("%w: age=%s budget=%s", ErrStaleFrame, age, MaxIdentityFrameAge)
	}
	return nil
}

// Hash implements Frame via the canonical JSON encoding.
// Two IdentityFrames with identical field values produce identical hashes.
func (f *IdentityFrame) Hash() ([32]byte, error) {
	return hashCanonical(f)
}

// Render implements Frame. Returns canonical JSON (sorted keys) of
// the frame. The bytes are suitable for persistence as FrameEnvelope.FrameJSON.
func (f *IdentityFrame) Render() ([]byte, error) {
	return json.Marshal(f)
}

// WriteAuditRef is the minimal projection of a write_audit row needed
// by VerifyAgainstWriteAudit. The Gate layer constructs one of these
// from the row fetched by store.GetWriteAudit(max_id_for_session).
type WriteAuditRef struct {
	SessionID        string `json:"session_id"`
	ConstitutionID   string `json:"constitution_id"`
	ConstitutionVer  string `json:"constitution_ver"`
}

// VerifyAgainstWriteAudit cross-checks the IdentityFrame against the
// most recent write_audit row for the session. Returns nil if the
// binding is consistent; specific error if the frame references a
// stale session or different constitution.
//
// This is the INV-7 + INV-4 cross-check: a frame claiming to belong to
// session S with constitution C@v MUST match the recent audit row.
// Without this check, a stale or hijacked frame could leak across
// sessions or constitutions.
func (f *IdentityFrame) VerifyAgainstWriteAudit(ref WriteAuditRef) error {
	if f.SessionID != ref.SessionID {
		return fmt.Errorf("%w: frame session=%q audit session=%q",
			ErrCrossSessionBinding, f.SessionID, ref.SessionID)
	}
	if f.ConstitutionID != ref.ConstitutionID || f.ConstitutionVer != ref.ConstitutionVer {
		return fmt.Errorf("%w: frame constitution=%s@%s audit constitution=%s@%s",
			ErrConstitutionMismatch, f.ConstitutionID, f.ConstitutionVer,
			ref.ConstitutionID, ref.ConstitutionVer)
	}
	return nil
}

// Equal reports whether two IdentityFrames carry identical identity-bearing
// fields. Compared structurally; ComposedAt is NOT compared (stale-but-equal
// frames are equal by content even if their timestamps differ — that's a
// hash concern, not an equality concern).
func (f *IdentityFrame) Equal(other *IdentityFrame) bool {
	if f == nil || other == nil {
		return f == other
	}
	return f.Actor == other.Actor &&
		f.Operator == other.Operator &&
		f.SessionID == other.SessionID &&
		f.ConstitutionID == other.ConstitutionID &&
		f.ConstitutionVer == other.ConstitutionVer &&
		f.CanaryActive == other.CanaryActive
}

// EqualBytes is a convenience: returns true iff Render() of both frames
// produces identical bytes. Useful for "did anything change?" comparisons
// during frame refresh.
func EqualBytes(a, b Frame) (bool, error) {
	if a == nil || b == nil {
		return a == b, nil
	}
	if a.Kind() != b.Kind() {
		return false, nil
	}
	ra, err := a.Render()
	if err != nil {
		return false, err
	}
	rb, err := b.Render()
	if err != nil {
		return false, err
	}
	return bytes.Equal(ra, rb), nil
}

// Wave 5X.2: compile-time guard that *IdentityFrame satisfies atomic.Frame.
var _ Frame = (*IdentityFrame)(nil)

