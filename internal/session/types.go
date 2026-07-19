// Package session defines the session primitive. A session is one
// operational context — typically one user-facing chat session, one
// red-team run, or one vibe-flow pipeline execution. Sessions are
// first-class (have lifecycle: start / heartbeat / close / resurrect /
// recover / status) and carry the audit trail that ties together every
// write made within them.
//
// # Lifecycle (Wave 5E — pivote active-memory)
//
// Per ACTIVE_MEMORY_RFC.md §4 + INV-8 + INV-9, a session moves
// through five states. The pre-pivote `active` / `closed` / `abandoned`
// three-state enum is REPLACED by:
//
//   open (was active)
//     receiving tool calls; last_heartbeat_at is recent
//     transitions: idle (idle_timeout), closed_clean (_close reason=clean),
//                  closed_aborted (sweeper/boot reconciliation)
//   idle
//     was open; last_heartbeat_at is stale (no writes in IDLE_TIMEOUT)
//     transitions: open (heartbeat refresh), closed_clean, closed_aborted
//   closed_clean (terminal, NOT resurrectable)
//     operator called dark_memory_session_close(reason='clean')
//   closed_aborted (RESURRECTABLE)
//     operator called dark_memory_session_close(reason='aborted'), OR
//     sweeper/boot_reconcile promoted a stale open
//     transitions: open (via _resurrect) — frame state inherited
//   archived (terminal, NOT resurrectable)
//     operator called dark_memory_session_close(reason='archived')
//
// INV-8 (Resilience): only operator-chosen 'clean' or 'archived'
// closes are terminal-non-resurrectable. Every other close path keeps
// the session resurrectable.
//
// INV-9 (Heartbeat): sessions whose last_heartbeat_at is older than
// HEARTBEAT_TIMEOUT seconds get promoted to closed_aborted by the
// sweeper (internal/scope/sweeper.go, 5E.iii) or by boot_reconcile.
//
// Resurrection (Wave 5E.ii): dark_memory_session_resurrect(original_id)
// creates a NEW session row with parent_session_id=original_id and
// resurrected_from=original_id (or the deepest ancestor in the chain).
// Scope state + evidence pointers are inherited. Grants are re-derived
// from constitution + active mods (NOT copied). Frame composition
// re-runs against the new session id (5E.iv).
package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewSessionID generates a unique session ID with prefix "sess-" +
// 8 hex bytes of entropy. The prefix is kept short so the session
// table stays scannable; full uniqueness comes from the 16-hex-char
// suffix (~64 bits of entropy, fine for non-adversarial operator IDs).
// If you need cryptographic-strength uniqueness (e.g. cross-process
// dedup over network), replace with crypto/rand + uuid.
func NewSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should not fail on Linux/Windows; if it does,
		// fall back to a timestamp-derived ID. Logically distinct
		// process-started sessions will have distinct milliseconds.
		return fmt.Sprintf("sess-%x", time.Now().UnixNano())
	}
	return "sess-" + hex.EncodeToString(b[:])
}

// Status is the canonical session-lifecycle enum (Wave 5E — pivote).
// Five states per ACTIVE_MEMORY_RFC.md §4.1; persistence layer
// (internal/store/sqlite, internal/store/postgres) enforces via
// CHECK constraint after the v12 migration.
type Status string

// Canonical session lifecycle states. Order is meaningful: the enum
// is iterated (open → idle → terminal-clean | terminal-archived) and
// sweeper transitions id → aborted follow the documented chain.
const (
	StatusOpen         Status = "open"          // receiving tool calls
	StatusIdle         Status = "idle"          // stale heartbeat, awaiting timeout
	StatusClosedClean  Status = "closed_clean"  // terminal, NOT resurrectable
	StatusClosedAborted Status = "closed_aborted" // RESURRECTABLE
	StatusArchived     Status = "archived"      // terminal, NOT resurrectable
)

// AllStatuses returns the canonical Status slice. Use for validation
// and for iteration (e.g. dashboard listings).
func AllStatuses() []Status {
	return []Status{
		StatusOpen,
		StatusIdle,
		StatusClosedClean,
		StatusClosedAborted,
		StatusArchived,
	}
}

// Validate reports an error if s is not one of the canonical five.
func (s Status) Validate() error {
	for _, c := range AllStatuses() {
		if s == c {
			return nil
		}
	}
	return fmt.Errorf("session: invalid status %q", string(s))
}

// IsTerminal reports whether s is a non-resurrectable terminal state.
// True for closed_clean and archived.
func (s Status) IsTerminal() bool {
	return s == StatusClosedClean || s == StatusArchived
}

// IsResurrectable reports whether a session in state s can be brought
// back to open via dark_memory_session_resurrect. True for
// closed_aborted (and idle, since idle can be timed out to aborted).
func (s Status) IsResurrectable() bool {
	return s == StatusClosedAborted || s == StatusIdle
}

// IsOpen reports whether s accepts new tool calls.
func (s Status) IsOpen() bool {
	return s == StatusOpen
}

// CloseReason is the operator-provided reason at close time. Stored
// in write_audit.session_event (per the v12 migration) when a session
// row is updated. Drives the close_clean vs closed_aborted vs archived
// destination state.
type CloseReason string

// Canonical close reasons. The operator picks one of these on
// dark_memory_session_close. Each maps to a destination session
// status per INV-8 (only 'clean' and 'archived' are terminal).
const (
	ReasonClean    CloseReason = "clean"    // operator-initiated, terminal
	ReasonAborted  CloseReason = "aborted"  // operator-initiated, resurrectable
	ReasonArchived CloseReason = "archived" // operator-initiated, terminal
)

// Validate reports an error if r is not one of the canonical three.
// Unknown reasons (e.g. typos like "cleen") are REJECTED at the gate.
func (r CloseReason) Validate() error {
	switch r {
	case ReasonClean, ReasonAborted, ReasonArchived:
		return nil
	}
	return fmt.Errorf("session: invalid close reason %q", string(r))
}

// DestinationStatus returns the session.status that a close with the
// given reason produces. INV-8: only ReasonClean and ReasonArchived
// are terminal-non-resurrectable; ReasonAborted leaves the session
// resurrectable.
//
// Default CloseReason "" is treated as ReasonClean (safe default).
func (r CloseReason) DestinationStatus() Status {
	switch r {
	case ReasonAborted:
		return StatusClosedAborted
	case ReasonArchived:
		return StatusArchived
	default: // empty, "clean", or any unknown (after Validate)
		return StatusClosedClean
	}
}

// ParseCloseReason validates and canonicalises a close-reason string.
// Empty input is treated as ReasonClean (safe default; matches the
// pre-pivot tool signature). Unknown values return an error.
//
// The function form is used by Store impls (which don't yet know to
// construct a CloseReason directly); the CloseReason methods
// (Validate, DestinationStatus) are used by orchestrators.
func ParseCloseReason(s string) (CloseReason, error) {
	switch CloseReason(s) {
	case ReasonClean:
		return ReasonClean, nil
	case ReasonAborted:
		return ReasonAborted, nil
	case ReasonArchived:
		return ReasonArchived, nil
	case "":
		return ReasonClean, nil
	}
	return "", fmt.Errorf("session: invalid close reason %q (expected clean|aborted|archived)", s)
}

// SessionEventForReason returns the canonical write_audit.session_event
// string for a given close reason. Used by Store.CloseSession and
// Store.SaveHeartbeat / SaveResurrect to populate the audit breadcrumb
// (Wave 5E.ii INV-9 + INV-8).
func SessionEventForReason(r CloseReason) string {
	switch r {
	case ReasonClean:
		return "close_clean"
	case ReasonAborted:
		return "close_aborted"
	case ReasonArchived:
		return "archived"
	}
	return ""
}

// Session is the record persisted in the sessions table. The struct
// gained three new columns under Wave 5E: LastHeartbeatAt (INV-9),
// ParentSessionID (chain pointer to immediate predecessor for
// resurrection), ResurrectedFrom (chain pointer to the original
// ancestor — distinct from Parent because resurrection can chain).
type Session struct {
	ID              int64  `json:"id"`
	SessionID       string `json:"session_id"`
	Status          string `json:"status"`
	ConstitutionID  string `json:"constitution_id,omitempty"`
	ConstitutionVer string `json:"constitution_ver,omitempty"`
	ActiveMods      string `json:"active_mods,omitempty"` // JSON array of mod_id
	Operator        string `json:"operator,omitempty"`     // who started this session
	StartedAt       string `json:"started_at"`
	ClosedAt        string `json:"closed_at,omitempty"`

	// Lifecycle pivot (Wave 5E):
	LastHeartbeatAt string `json:"last_heartbeat_at,omitempty"` // INV-9; updated by _heartbeat
	ParentSessionID string `json:"parent_session_id,omitempty"`  // immediate predecessor in resurrection chain
	ResurrectedFrom string `json:"resurrected_from,omitempty"`  // original ancestor; same as Parent for non-chained chains

	// Free-form:
	Notes string `json:"notes,omitempty"`
}
