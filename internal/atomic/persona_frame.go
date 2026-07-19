// PersonaFrame — the canonical implementation of the Persona frame
// kind per ACTIVE_MEMORY_RFC.md §2 A2 (table). PersonaFrame captures
// the constitution-applied voice + claims policy + refusal pattern,
// resolved from the active constitution + brand at the session level.
// The Gate applies the persona at the response shaping step
// (post-hook) so the LLM never sees raw orchestrator output
// (per ACTIVE_MEMORY_RFC.md §A5).
//
// PersonaFrame is the LAST frame composed (composition order per
// internal/atomic.AllFrameKinds). It must agree with the
// IdentityFrame's constitution_id for the same composition cycle;
// cross-check is VerifyAgainstIdentityFrame.
//
// # Atomicity contract
//   - ONE type: PersonaFrame
//   - ONE constructor: NewPersonaFrame
//   - FOUR invariants enforced in Validate
//   - THREE derived behaviors: Hash determinism, Render canonical,
//     VerifyAgainstIdentityFrame cross-check
package atomic

import (
	"errors"
	"fmt"
	"time"
)

// MaxPersonaFrameAge is the default staleness budget for PersonaFrame.
// Default 15 minutes — persona is resolved at session start and is
// stable for the session lifetime; rare recalibrations happen on
// constitution or brand changes.
const MaxPersonaFrameAge = 15 * time.Minute

// PersonaFrame is the runtime implementation of Frame for the
// Persona kind.
type PersonaFrame struct {
	ComposedAtValue time.Time `json:"composed_at"`

	// ConstitutionID + Version are the binding the persona was
	// resolved from. Must match IdentityFrame.ConstitutionID/Ver.
	ConstitutionID  string `json:"constitution_id"`
	ConstitutionVer string `json:"constitution_ver"`

	// BrandID is the active brand (optional — empty means
	// "constitution-only, no brand voice").
	BrandID string `json:"brand_id,omitempty"`

	// Voice is the multi-paragraph prose voice description from
	// the constitution's [tone] + brand's [voice]. May be long.
	Voice string `json:"voice"`

	// ClaimsPolicy is what the LLM is allowed to claim about the
	// project, the user, and the work product. Single paragraph or
	// short structured text.
	ClaimsPolicy string `json:"claims_policy"`

	// RefusalPattern is a JSON-encoded refusa pattern (5A.vi / 5B).
	// Empty string means "use the default refusal pattern from
	// the constitution" — no further constraints.
	RefusalPattern string `json:"refusal_pattern,omitempty"`

	// Tone is a short single-word or single-phrase tone label
	// ("formal", "casual", "technical", "paternal", "didactic", ...).
	// Used by the persona applier to choose between response templates.
	Tone string `json:"tone"`
}

// Errors returned by PersonaFrame methods.
var (
	ErrPersonaEmptyConstitutionID  = errors.New("atomic: persona frame constitution_id is empty")
	ErrPersonaEmptyConstitutionVer = errors.New("atomic: persona frame constitution_ver is empty")
	ErrPersonaEmptyVoice           = errors.New("atomic: persona frame voice is empty")
	ErrPersonaEmptyClaimsPolicy    = errors.New("atomic: persona frame claims_policy is empty")
	ErrPersonaEmptyTone            = errors.New("atomic: persona frame tone is empty")
	ErrPersonaZeroComposed         = errors.New("atomic: persona frame composed_at is zero")
	ErrPersonaStale                = errors.New("atomic: persona frame is stale (older than MaxPersonaFrameAge)")
	ErrPersonaIdentityMismatch     = errors.New("atomic: persona frame constitution binding does not match identity frame")
)

// NewPersonaFrame builds a PersonaFrame with all required fields set.
// BrandID and RefusalPattern are optional; the rest are required.
func NewPersonaFrame(constitutionID, constitutionVer, brandID, voice, claimsPolicy, refusalPattern, tone string) (*PersonaFrame, error) {
	if constitutionID == "" {
		return nil, ErrPersonaEmptyConstitutionID
	}
	if constitutionVer == "" {
		return nil, ErrPersonaEmptyConstitutionVer
	}
	if voice == "" {
		return nil, ErrPersonaEmptyVoice
	}
	if claimsPolicy == "" {
		return nil, ErrPersonaEmptyClaimsPolicy
	}
	if tone == "" {
		return nil, ErrPersonaEmptyTone
	}
	return &PersonaFrame{
		ComposedAtValue:  time.Now(),
		ConstitutionID:   constitutionID,
		ConstitutionVer:  constitutionVer,
		BrandID:          brandID,
		Voice:            voice,
		ClaimsPolicy:     claimsPolicy,
		RefusalPattern:   refusalPattern,
		Tone:             tone,
	}, nil
}

// Kind implements Frame.
func (f *PersonaFrame) Kind() FrameKind { return FramePersona }

// ComposedAt implements Frame.
func (f *PersonaFrame) ComposedAt() time.Time { return f.ComposedAtValue }

// Validate implements Frame.
//
// Enforces:
//   - ConstitutionID + Version non-empty.
//   - Voice, ClaimsPolicy, Tone non-empty.
//   - ComposedAt non-zero and not stale.
//   - BrandID and RefusalPattern may be empty (optional).
func (f *PersonaFrame) Validate() error {
	if f == nil {
		return ErrPersonaZeroComposed
	}
	if f.ConstitutionID == "" {
		return ErrPersonaEmptyConstitutionID
	}
	if f.ConstitutionVer == "" {
		return ErrPersonaEmptyConstitutionVer
	}
	if f.Voice == "" {
		return ErrPersonaEmptyVoice
	}
	if f.ClaimsPolicy == "" {
		return ErrPersonaEmptyClaimsPolicy
	}
	if f.Tone == "" {
		return ErrPersonaEmptyTone
	}
	if f.ComposedAtValue.IsZero() {
		return ErrPersonaZeroComposed
	}
	if age := time.Since(f.ComposedAtValue); age > MaxPersonaFrameAge {
		return fmt.Errorf("%w: age=%s budget=%s", ErrPersonaStale, age, MaxPersonaFrameAge)
	}
	return nil
}

// Hash implements Frame via canonical JSON.
func (f *PersonaFrame) Hash() ([32]byte, error) { return hashCanonical(f) }

// Render implements Frame.
func (f *PersonaFrame) Render() ([]byte, error) { return jsonMarshal(f) }

// HasBrand reports whether a brand voice was applied. True when
// BrandID is non-empty (the persona was resolved against a brand
// in addition to the constitution).
func (f *PersonaFrame) HasBrand() bool { return f.BrandID != "" }

// HasRefusalPattern reports whether a custom refusal pattern is set.
// True when RefusalPattern is non-empty (the persona overrides the
// constitution's default refusal pattern).
func (f *PersonaFrame) HasRefusalPattern() bool { return f.RefusalPattern != "" }

// VerifyAgainstIdentityFrame cross-checks the PersonaFrame's
// constitution binding against the IdentityFrame for the same
// composition cycle. Without this, personas could leak across
// constitution versions (the INV-4 watchdog invariant).
func (f *PersonaFrame) VerifyAgainstIdentityFrame(identity *IdentityFrame) error {
	if identity == nil {
		return ErrPersonaIdentityMismatch
	}
	if f.ConstitutionID != identity.ConstitutionID || f.ConstitutionVer != identity.ConstitutionVer {
		return fmt.Errorf("%w: persona constitution=%s@%s identity constitution=%s@%s",
			ErrPersonaIdentityMismatch, f.ConstitutionID, f.ConstitutionVer,
			identity.ConstitutionID, identity.ConstitutionVer)
	}
	return nil
}

// Wave 5X.2: compile-time guard.
var _ Frame = (*PersonaFrame)(nil)
