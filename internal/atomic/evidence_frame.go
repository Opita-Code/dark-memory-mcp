// EvidenceFrame — the canonical implementation of the Evidence frame
// kind per ACTIVE_MEMORY_RFC.md §2 A2 (table). EvidenceFrame captures
// the recent writes (last N write_audit rows) and linked research items
// for the session, plus the last_seen_token that drives delta recall
// subscriptions.
//
// EvidenceFrame is composed AFTER ScopeFrame and BEFORE CapabilitiesFrame.
// The LastSeenToken is the operator under which delta pulls work in
// 5A.ii / 5A.iii.
//
// # Atomicity contract
//   - ONE type: EvidenceFrame
//   - ONE constructor: NewEvidenceFrame
//   - THREE invariants enforced in Validate
//   - THREE derived behaviors: Hash determinism, Render canonical,
//     LastSeenToken monotonic (read-only accessor)
package atomic

import (
	"errors"
	"fmt"
	"time"
)

// MaxEvidenceFrameAge is the default staleness budget for EvidenceFrame.
// Default 10 seconds — evidence state changes with every write.
const MaxEvidenceFrameAge = 10 * time.Second

// WriteRef is a projection of a write_audit row used in the frame body.
// The audit row lives in write_audit; this struct is the minimal
// runtime projection.
type WriteRef struct {
	WriteID        int64     `json:"write_id"`
	SessionID      string    `json:"session_id"`
	WritePath      string    `json:"write_path"`
	ContentSHA256  [32]byte  `json:"content_sha256"`
	CreatedAt      time.Time `json:"created_at"`
}

// ResearchRef is a projection of a research_items row linked to the
// session. Used for cross-MCP context (e.g. listing prior CVEs
// surfaced during a vibe_publish).
type ResearchRef struct {
	ItemID       int64   `json:"item_id"`
	URL          string  `json:"url,omitempty"`
	Title        string  `json:"title,omitempty"`
	Confidence   float32 `json:"confidence"`
	LastSeenToken int64  `json:"last_seen_token"`
}

// EvidenceFrame is the runtime implementation of Frame for the
// Evidence kind.
type EvidenceFrame struct {
	ComposedAtValue time.Time `json:"composed_at"`

	// INV-7: explicit project_id.
	ProjectID string `json:"project_id"`

	// SessionID links to IdentityFrame.SessionID.
	SessionID string `json:"session_id"`

	// RecentWrites is the most-recent N write_audit rows for the
	// session. Max 50 by default (configured by the cache layer).
	RecentWrites []WriteRef `json:"recent_writes,omitempty"`

	// LinkedResearch is research_items linked from this session's
	// scope. Order is most-recent-first.
	LinkedResearch []ResearchRef `json:"linked_research,omitempty"`

	// LastSeenToken is write_audit.max(id) at the time the frame was
	// composed. Used by dark_memory_recall(scope, since_token) to
	// compute the delta. Read-only; bumps on every recall.
	LastSeenToken int64 `json:"last_seen_token"`
}

// Errors returned by EvidenceFrame methods.
var (
	ErrEvidenceEmptyProjectID      = errors.New("atomic: evidence frame project_id is empty")
	ErrEvidenceEmptySessionID      = errors.New("atomic: evidence frame session_id is empty")
	ErrEvidenceNegativeToken       = errors.New("atomic: evidence frame last_seen_token is negative")
	ErrEvidenceZeroComposed        = errors.New("atomic: evidence frame composed_at is zero")
	ErrEvidenceStale               = errors.New("atomic: evidence frame is stale (older than MaxEvidenceFrameAge)")
)

// NewEvidenceFrame builds an EvidenceFrame. lastSeenToken defaults to 0
// if not provided as last_seen_token; the cache layer updates this on
// each recall.
func NewEvidenceFrame(projectID, sessionID string, recentWrites []WriteRef, linkedResearch []ResearchRef, lastSeenToken int64) (*EvidenceFrame, error) {
	if projectID == "" {
		return nil, ErrEvidenceEmptyProjectID
	}
	if sessionID == "" {
		return nil, ErrEvidenceEmptySessionID
	}
	if lastSeenToken < 0 {
		return nil, ErrEvidenceNegativeToken
	}
	return &EvidenceFrame{
		ComposedAtValue: time.Now(),
		ProjectID:       projectID,
		SessionID:       sessionID,
		RecentWrites:    recentWrites,
		LinkedResearch:  linkedResearch,
		LastSeenToken:   lastSeenToken,
	}, nil
}

// Kind implements Frame.
func (f *EvidenceFrame) Kind() FrameKind { return FrameEvidence }

// ComposedAt implements Frame.
func (f *EvidenceFrame) ComposedAt() time.Time { return f.ComposedAtValue }

// Validate implements Frame.
func (f *EvidenceFrame) Validate() error {
	if f == nil {
		return ErrEvidenceZeroComposed
	}
	if f.ProjectID == "" {
		return ErrEvidenceEmptyProjectID
	}
	if f.SessionID == "" {
		return ErrEvidenceEmptySessionID
	}
	if f.LastSeenToken < 0 {
		return ErrEvidenceNegativeToken
	}
	if f.ComposedAtValue.IsZero() {
		return ErrEvidenceZeroComposed
	}
	if age := time.Since(f.ComposedAtValue); age > MaxEvidenceFrameAge {
		return fmt.Errorf("%w: age=%s budget=%s", ErrEvidenceStale, age, MaxEvidenceFrameAge)
	}
	return nil
}

// Hash implements Frame via canonical JSON.
func (f *EvidenceFrame) Hash() ([32]byte, error) { return hashCanonical(f) }

// Render implements Frame.
func (f *EvidenceFrame) Render() ([]byte, error) { return jsonMarshal(f) }

// WriteCount returns the number of recent writes in the frame.
func (f *EvidenceFrame) WriteCount() int { return len(f.RecentWrites) }

// ResearchCount returns the number of linked research items.
func (f *EvidenceFrame) ResearchCount() int { return len(f.LinkedResearch) }

// Wave 5X.2: compile-time guard.
var _ Frame = (*EvidenceFrame)(nil)
