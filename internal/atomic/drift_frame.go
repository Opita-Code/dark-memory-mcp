// DriftFrame — the canonical implementation of the Drift frame kind
// per ACTIVE_MEMORY_RFC.md §2 A2 (table). DriftFrame captures the
// most-recent drift verdict on the session's active spec plus the
// pending_items list (drift issues awaiting reconciliation).
//
// DriftFrame is composed AFTER CapabilitiesFrame and BEFORE
// PersonaFrame. The Gate consumes it on the post-hook to drive
// drift-at-write checks (per ACTIVE_MEMORY_RFC.md §A6 and M6).
//
// # Atomicity contract
//   - ONE type: DriftFrame
//   - ONE constructor: NewDriftFrame
//   - FOUR invariants enforced in Validate
//   - THREE derived behaviors: Hash determinism, Render canonical,
//     IsAligned / HasPendingItems read-only accessors
package atomic

import (
	"errors"
	"fmt"
	"time"
)

// MaxDriftFrameAge is the default staleness budget for DriftFrame.
// Default 30 seconds — drift verdicts update on each write.
const MaxDriftFrameAge = 30 * time.Second

// DriftFrame is the runtime implementation of Frame for the Drift kind.
type DriftFrame struct {
	ComposedAtValue time.Time `json:"composed_at"`

	// SessionID links to IdentityFrame.SessionID.
	SessionID string `json:"session_id"`

	// SpecID is the vibe_specs.id of the spec this drift verdict
	// references. 0 if no spec is open or no drift run yet.
	SpecID int64 `json:"spec_id"`

	// LastVerdict is one of: aligned, drift_detected, needs_human.
	// Required when SpecID > 0.
	LastVerdict string `json:"last_verdict"`

	// LastReconciledAt is when the LastVerdict was issued. Zero
	// when LastVerdict is "needs_human" or empty.
	LastReconciledAt time.Time `json:"last_reconciled_at,omitempty"`

	// PendingItems is the list of drift_items still awaiting
	// reconciliation when LastVerdict is "drift_detected". Empty
	// for aligned or needs_human.
	PendingItems []string `json:"pending_items,omitempty"`
}

// Errors returned by DriftFrame methods.
var (
	ErrDriftEmptySessionID     = errors.New("atomic: drift frame session_id is empty")
	ErrDriftInvalidSpecID      = errors.New("atomic: drift frame spec_id is negative")
	ErrDriftSpecWithoutVerdict = errors.New("atomic: drift frame spec_id > 0 requires non-empty last_verdict")
	ErrDriftVerdictUnknown     = errors.New("atomic: drift frame last_verdict must be aligned/drift_detected/needs_human")
	ErrDriftVerdictWithoutTime = errors.New("atomic: drift frame last_verdict in {aligned, drift_detected} requires non-zero last_reconciled_at")
	ErrDriftAlignedWithItems   = errors.New("atomic: drift frame with last_verdict=aligned must have empty pending_items")
	ErrDriftDriftWithoutItems  = errors.New("atomic: drift frame with last_verdict=drift_detected must have non-empty pending_items")
	ErrDriftZeroComposed       = errors.New("atomic: drift frame composed_at is zero")
	ErrDriftStale              = errors.New("atomic: drift frame is stale (older than MaxDriftFrameAge)")
)

// NewDriftFrame builds a DriftFrame. lastVerdict must be empty when
// SpecID is 0, and must be one of the canonical three when non-empty.
// pendingItems is meaningful only when lastVerdict=drift_detected.
func NewDriftFrame(sessionID string, specID int64, lastVerdict string, lastReconciledAt time.Time, pendingItems []string) (*DriftFrame, error) {
	if sessionID == "" {
		return nil, ErrDriftEmptySessionID
	}
	if specID < 0 {
		return nil, ErrDriftInvalidSpecID
	}
	if specID > 0 && lastVerdict == "" {
		return nil, ErrDriftSpecWithoutVerdict
	}
	if lastVerdict != "" &&
		lastVerdict != "aligned" &&
		lastVerdict != "drift_detected" &&
		lastVerdict != "needs_human" {
		return nil, ErrDriftVerdictUnknown
	}
	// Cross-consistency: aligned/drift_detected require a timestamp; needs_human may or may not.
	if (lastVerdict == "aligned" || lastVerdict == "drift_detected") && lastReconciledAt.IsZero() {
		return nil, ErrDriftVerdictWithoutTime
	}
	if lastVerdict == "aligned" && len(pendingItems) > 0 {
		return nil, ErrDriftAlignedWithItems
	}
	if lastVerdict == "drift_detected" && len(pendingItems) == 0 {
		return nil, ErrDriftDriftWithoutItems
	}
	return &DriftFrame{
		ComposedAtValue:  time.Now(),
		SessionID:        sessionID,
		SpecID:           specID,
		LastVerdict:      lastVerdict,
		LastReconciledAt: lastReconciledAt,
		PendingItems:     pendingItems,
	}, nil
}

// Kind implements Frame.
func (f *DriftFrame) Kind() FrameKind { return FrameDrift }

// ComposedAt implements Frame.
func (f *DriftFrame) ComposedAt() time.Time { return f.ComposedAtValue }

// Validate implements Frame.
func (f *DriftFrame) Validate() error {
	if f == nil {
		return ErrDriftZeroComposed
	}
	if f.SessionID == "" {
		return ErrDriftEmptySessionID
	}
	if f.SpecID < 0 {
		return ErrDriftInvalidSpecID
	}
	if f.SpecID > 0 && f.LastVerdict == "" {
		return ErrDriftSpecWithoutVerdict
	}
	if f.LastVerdict != "" &&
		f.LastVerdict != "aligned" &&
		f.LastVerdict != "drift_detected" &&
		f.LastVerdict != "needs_human" {
		return ErrDriftVerdictUnknown
	}
	if (f.LastVerdict == "aligned" || f.LastVerdict == "drift_detected") && f.LastReconciledAt.IsZero() {
		return ErrDriftVerdictWithoutTime
	}
	if f.LastVerdict == "aligned" && len(f.PendingItems) > 0 {
		return ErrDriftAlignedWithItems
	}
	if f.LastVerdict == "drift_detected" && len(f.PendingItems) == 0 {
		return ErrDriftDriftWithoutItems
	}
	if f.ComposedAtValue.IsZero() {
		return ErrDriftZeroComposed
	}
	if age := time.Since(f.ComposedAtValue); age > MaxDriftFrameAge {
		return fmt.Errorf("%w: age=%s budget=%s", ErrDriftStale, age, MaxDriftFrameAge)
	}
	return nil
}

// Hash implements Frame via canonical JSON.
func (f *DriftFrame) Hash() ([32]byte, error) { return hashCanonical(f) }

// Render implements Frame.
func (f *DriftFrame) Render() ([]byte, error) { return jsonMarshal(f) }

// IsAligned reports whether the most-recent verdict is "aligned".
func (f *DriftFrame) IsAligned() bool { return f.LastVerdict == "aligned" }

// HasPendingItems reports whether the verdict is "drift_detected"
// with items awaiting reconciliation.
func (f *DriftFrame) HasPendingItems() bool {
	return f.LastVerdict == "drift_detected" && len(f.PendingItems) > 0
}

// Wave 5X.2: compile-time guard.
var _ Frame = (*DriftFrame)(nil)
