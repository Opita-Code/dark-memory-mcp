// ScopeFrame — the canonical implementation of the Scope frame kind per
// ACTIVE_MEMORY_RFC.md §2 A2 (table). ScopeFrame captures the session's
// current scope state: which spec is open, which tasks are in-flight,
// which evidence is linked, and the most recent drift verdict on the
// active spec.
//
// ScopeFrame is composed AFTER IdentityFrame and BEFORE EvidenceFrame
// (composition order per internal/atomic.AllFrameKinds). The SessionID
// field MUST match the IdentityFrame.SessionID for the same composition
// cycle; the cross-check is the VerifyAgainstIdentityFrame method.
//
// # Atomicity contract
//   - ONE type: ScopeFrame
//   - ONE constructor: NewScopeFrame
//   - FIVE invariants enforced in Validate
//   - THREE derived behaviors: Hash determinism, Render canonical,
//     cross-check via VerifyAgainstIdentityFrame
package atomic

import (
	"errors"
	"fmt"
	"time"
)

// MaxScopeFrameAge is the default staleness budget for ScopeFrame.
// Frames older than this MUST be recomposed. Default 30 seconds —
// scope state changes more rapidly than identity (per ACTIVE_MEMORY_RFC.md
// §8 acceptance criteria per-frame TTL).
const MaxScopeFrameAge = 30 * time.Second

// TaskRef is one open task in the session's scope. Tasks live in
// vibe_specs.tasks JSON; this struct is the runtime projection used
// in the frame body.
type TaskRef struct {
	TaskID    string `json:"task_id"`
	SpecID    int64  `json:"spec_id"`
	Owner     string `json:"owner,omitempty"`
	Status    string `json:"status"` // "open" | "in_progress" | "pending_review" | "closed"
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// EvidenceRef is one artifact linked to the active scope. The artifact
// lives in vibe_artifacts; this struct is the runtime projection.
type EvidenceRef struct {
	ArtifactID int64     `json:"artifact_id"`
	Kind       string    `json:"kind"` // "code" | "text" | "image" | ...
	URL        string    `json:"url,omitempty"`
	ComposedAt time.Time `json:"composed_at,omitempty"`
}

// ScopeFrame is the runtime implementation of Frame for the Scope kind.
type ScopeFrame struct {
	ComposedAtValue time.Time `json:"composed_at"`

	SessionID string `json:"session_id"`

	// OpenSpecID is the vibe_specs.id of the spec currently in scope.
	// 0 means "no spec open" (session is idle or starting).
	OpenSpecID int64 `json:"open_spec_id"`

	// OpenTasks are the in-flight tasks from the active spec. Empty
	// when OpenSpecID=0. Order is not significant; consumers sort.
	OpenTasks []TaskRef `json:"open_tasks,omitempty"`

	// EvidencePointers are the artifacts linked to this scope. Order
	// is most-recent-first by ComposedAt.
	EvidencePointers []EvidenceRef `json:"evidence_pointers,omitempty"`

	// LastDriftVerdict is the verdict from the most recent drift_judge
	// on the active spec. One of {aligned, drift_detected, needs_human}.
	// Empty string if no drift_judge has run on this spec.
	LastDriftVerdict string `json:"last_drift_verdict,omitempty"`

	// LastDriftAt is when the verdict above was issued. Zero when
	// LastDriftVerdict is empty.
	LastDriftAt time.Time `json:"last_drift_at,omitempty"`
}

// Errors returned by ScopeFrame methods.
var (
	ErrScopeEmptySessionID       = errors.New("atomic: scope frame session_id is empty")
	ErrScopeInvalidSpecID        = errors.New("atomic: scope frame open_spec_id is negative")
	ErrScopeTaskMissingTaskID    = errors.New("atomic: scope frame task is missing task_id")
	ErrScopeTaskMissingSpecID    = errors.New("atomic: scope frame task is missing spec_id (no open spec)")
	ErrScopeVerdictUnknown       = errors.New("atomic: scope frame last_drift_verdict must be aligned/drift_detected/needs_human or empty")
	ErrScopeVerdictWithoutTime   = errors.New("atomic: scope frame last_drift_verdict requires non-zero last_drift_at (and vice versa)")
	ErrScopeZeroComposed         = errors.New("atomic: scope frame composed_at is zero")
	ErrScopeStale                = errors.New("atomic: scope frame is stale (older than MaxScopeFrameAge)")
	ErrScopeIdentityMismatch     = errors.New("atomic: scope frame session_id does not match identity frame")
)

// NewScopeFrame builds a ScopeFrame with the required identity binding
// (SessionID). OpenSpecID, tasks, evidence, last drift are optional;
// the constructor takes them as additional arguments.
func NewScopeFrame(sessionID string, openSpecID int64, tasks []TaskRef, evidence []EvidenceRef, lastDriftVerdict string, lastDriftAt time.Time) (*ScopeFrame, error) {
	if sessionID == "" {
		return nil, ErrScopeEmptySessionID
	}
	if openSpecID < 0 {
		return nil, ErrScopeInvalidSpecID
	}
	if (lastDriftVerdict == "") != lastDriftAt.IsZero() {
		return nil, ErrScopeVerdictWithoutTime
	}
	if lastDriftVerdict != "" &&
		lastDriftVerdict != "aligned" &&
		lastDriftVerdict != "drift_detected" &&
		lastDriftVerdict != "needs_human" {
		return nil, ErrScopeVerdictUnknown
	}
	// Validate tasks cross-reference the open spec (when one is set).
	if openSpecID > 0 {
		for i, t := range tasks {
			if t.TaskID == "" {
				return nil, fmt.Errorf("%w: task[%d]", ErrScopeTaskMissingTaskID, i)
			}
			if t.SpecID == 0 {
				return nil, fmt.Errorf("%w: task[%d] task_id=%q",
					ErrScopeTaskMissingSpecID, i, t.TaskID)
			}
		}
	}
	return &ScopeFrame{
		ComposedAtValue:  time.Now(),
		SessionID:        sessionID,
		OpenSpecID:       openSpecID,
		OpenTasks:        tasks,
		EvidencePointers: evidence,
		LastDriftVerdict: lastDriftVerdict,
		LastDriftAt:      lastDriftAt,
	}, nil
}

// Kind implements Frame.
func (f *ScopeFrame) Kind() FrameKind { return FrameScope }

// ComposedAt implements Frame.
func (f *ScopeFrame) ComposedAt() time.Time { return f.ComposedAtValue }

// Validate implements Frame.
//
// Enforces:
//   - SessionID non-empty (links to IdentityFrame).
//   - OpenSpecID non-negative.
//   - last_drift_verdict + last_drift_at cross-consistent (both set or both empty).
//   - If last_drift_verdict set, it must be one of the canonical three.
//   - ComposedAt non-zero and not stale.
func (f *ScopeFrame) Validate() error {
	if f == nil {
		return ErrScopeZeroComposed
	}
	if f.SessionID == "" {
		return ErrScopeEmptySessionID
	}
	if f.OpenSpecID < 0 {
		return ErrScopeInvalidSpecID
	}
	if (f.LastDriftVerdict == "") != f.LastDriftAt.IsZero() {
		return ErrScopeVerdictWithoutTime
	}
	if f.LastDriftVerdict != "" &&
		f.LastDriftVerdict != "aligned" &&
		f.LastDriftVerdict != "drift_detected" &&
		f.LastDriftVerdict != "needs_human" {
		return ErrScopeVerdictUnknown
	}
	if f.ComposedAtValue.IsZero() {
		return ErrScopeZeroComposed
	}
	if age := time.Since(f.ComposedAtValue); age > MaxScopeFrameAge {
		return fmt.Errorf("%w: age=%s budget=%s", ErrScopeStale, age, MaxScopeFrameAge)
	}
	return nil
}

// Hash implements Frame via canonical JSON.
func (f *ScopeFrame) Hash() ([32]byte, error) { return hashCanonical(f) }

// Render implements Frame.
func (f *ScopeFrame) Render() ([]byte, error) { return jsonMarshal(f) }

// HasOpenSpec reports whether the scope points at a specific spec.
func (f *ScopeFrame) HasOpenSpec() bool { return f.OpenSpecID > 0 }

// TaskCount returns the number of in-flight tasks. O(len(OpenTasks)).
func (f *ScopeFrame) TaskCount() int { return len(f.OpenTasks) }

// EvidenceCount returns the number of linked evidence pointers.
func (f *ScopeFrame) EvidenceCount() int { return len(f.EvidencePointers) }

// VerifyAgainstIdentityFrame enforces that the ScopeFrame and the
// IdentityFrame for the same composition cycle agree on session_id.
// Without this, scopes could leak across sessions. Returns nil on
// agreement; ErrScopeIdentityMismatch otherwise.
func (f *ScopeFrame) VerifyAgainstIdentityFrame(identity *IdentityFrame) error {
	if identity == nil {
		return ErrScopeIdentityMismatch
	}
	if f.SessionID != identity.SessionID {
		return fmt.Errorf("%w: scope session=%q identity session=%q",
			ErrScopeIdentityMismatch, f.SessionID, identity.SessionID)
	}
	return nil
}

// Wave 5X.2: compile-time guard that *ScopeFrame satisfies atomic.Frame.
var _ Frame = (*ScopeFrame)(nil)
