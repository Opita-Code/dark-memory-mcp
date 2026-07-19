// O5E-recover: SessionRecover — read-only detection of the most-
// recent closed_aborted session for an operator within a lookback
// window. Returns the candidate WITHOUT creating a new session;
// the caller (harness adapter, operator UI) decides whether to
// follow up with dark_memory_session_resurrect.
//
// INV-8: a closed_aborted session is RESURRECTABLE per the lifecycle
// resilience invariant. SessionRecover is the discovery path; it
// never mutates state. The output requires_consent=true signals the
// caller that resurrection will create a new session row + audit
// trail — confirm before invoking SessionResurrect.
//
// Per RFC §4.2 + 5E.ii.
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/session"
)

// SessionRecoverInput is the request to detect a resurrectable
// session for an operator. Lookback is an RFC3339 duration string
// (e.g. "24h") — sessions closed before lookback are out of scope.
type SessionRecoverInput struct {
	// Operator is the human/agent identity to recover for (e.g.
	// "dark-agent"). Required.
	Operator string `json:"operator"`

	// Lookback bounds the search window. Defaults to "24h" when
	// empty. Accepted formats: RFC3339 duration (Go time.ParseDuration),
	// or simple duration shorthand "24h"/"7d"/"30m" (custom handled).
	Lookback string `json:"lookback,omitempty"`
}

// FramePreview is a lightweight projection of a session suitable for
// "should I resurrect this?" operator UI. Heavy fields (ActiveMods,
// Notes) are truncated / omitted.
type FramePreview struct {
	SessionID         string    `json:"session_id"`
	Status            string    `json:"status"` // always "closed_aborted" if returned
	Operator          string    `json:"operator"`
	ConstitutionID    string    `json:"constitution_id,omitempty"`
	ConstitutionVer   string    `json:"constitution_ver,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	ClosedAt          time.Time `json:"closed_at"`
	ResurrectedFrom   string    `json:"resurrected_from,omitempty"`
	ParentSessionID   string    `json:"parent_session_id,omitempty"`
}

// SessionRecoverOutput is the read-only detection result. Found=false
// when no closed_aborted session exists for the operator within
// lookback; RecoveredFrom then is nil + RequiresConsent=false.
type SessionRecoverOutput struct {
	Found            bool         `json:"found"`
	Candidate        *session.Session `json:"candidate,omitempty"`
	Preview          *FramePreview `json:"preview,omitempty"`
	RequiresConsent  bool         `json:"requires_consent"` // always true when Found=true
	LookbackApplied  string       `json:"lookback_applied"`
}

// SessionRecover detects the most-recent closed_aborted session for
// the operator within lookback. Read-only — never creates a session
// or emits a write_audit row.
//
// # Atomicity contract
//   - ONE public function
//   - TWO input/output shapes
//   - DEPENDS on Store.FindClosedAbortedForActor (5E.ii)
//   - NO write_audit (read-only)
//
// Returns ErrInvalidArgument if Operator is empty. The lookback is
// parsed leniently: defaults to 24h when empty/invalid.
func (o *Orchestrator) SessionRecover(ctx context.Context, in SessionRecoverInput) (*SessionRecoverOutput, error) {
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}

	lookback := in.Lookback
	if lookback == "" {
		lookback = "24h"
	}
	// Convert lookback shorthand to an RFC3339 cutoff. We accept
	// simple "Nh"/"Nd"/"Nm" patterns AND Go duration (e.g. "24h").
	cutoff := computeLookbackCutoff(lookback)

	activeProject := o.Store.ActiveProject()
	candidate, err := o.Store.FindClosedAbortedForActor(ctx, "", in.Operator, activeProject, cutoff)
	if err != nil {
		return nil, fmt.Errorf("session_recover: find: %w", err)
	}
	if candidate == nil {
		return &SessionRecoverOutput{
			Found:            false,
			Candidate:        nil,
			Preview:          nil,
			RequiresConsent:  false,
			LookbackApplied:  lookback,
		}, nil
	}

	preview := &FramePreview{
		SessionID:       candidate.SessionID,
		Status:          candidate.Status,
		Operator:        candidate.Operator,
		ConstitutionID:  candidate.ConstitutionID,
		ConstitutionVer: candidate.ConstitutionVer,
		ResurrectedFrom: candidate.ResurrectedFrom,
		ParentSessionID: candidate.ParentSessionID,
	}
	if startedAt, err := time.Parse(time.RFC3339Nano, candidate.StartedAt); err == nil {
		preview.StartedAt = startedAt
	}
	if closedAt, err := time.Parse(time.RFC3339Nano, candidate.ClosedAt); err == nil {
		preview.ClosedAt = closedAt
	}

	return &SessionRecoverOutput{
		Found:            true,
		Candidate:        candidate,
		Preview:          preview,
		RequiresConsent:  true, // resurrection always requires explicit consent
		LookbackApplied:  lookback,
	}, nil
}

// computeLookbackCutoff translates a lookback string into the RFC3339
// timestamp used in the SQL cutoff predicate. Accepts:
//   - "24h", "7d", "30m", "1h" — shorthand durations (h/d/m suffix)
//   - Go durations: "24h30m", etc.
//   - default: 24h when input is empty or unparseable
func computeLookbackCutoff(lookback string) string {
	now := time.Now().UTC()

	// Try the shorthand forms first (h / d / m).
	if d, ok := parseShorthandDuration(lookback); ok {
		return now.Add(-d).Format(time.RFC3339Nano)
	}

	// Fall back to Go's time.ParseDuration.
	if d, err := time.ParseDuration(lookback); err == nil && d > 0 {
		return now.Add(-d).Format(time.RFC3339Nano)
	}

	// Default: 24h.
	return now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
}

// parseShorthandDuration accepts "24h", "7d", "30m" (and combinations
// without the +). Returns false for anything else.
func parseShorthandDuration(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	last := s[len(s)-1]
	var unit time.Duration
	switch last {
	case 'h':
		unit = time.Hour
	case 'm':
		unit = time.Minute
	case 'd':
		unit = 24 * time.Hour
	case 's':
		unit = time.Second
	default:
		return 0, false
	}
	numPart := s[:len(s)-1]
	var n int
	if _, err := fmt.Sscanf(numPart, "%d", &n); err != nil {
		return 0, false
	}
	if n <= 0 {
		return 0, false
	}
	return time.Duration(n) * unit, true
}
