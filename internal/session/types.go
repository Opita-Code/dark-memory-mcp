// Package session defines the session primitive. A session is one
// operational context â typically one user-facing chat session, one
// red-team run, or one vibe-flow pipeline execution. Sessions are
// first-class (have lifecycle: start/close/status) and carry the
// audit trail that ties together every write made within them.
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

// Status is one of "active" | "closed" | "abandoned".
type Status string

const (
	StatusActive    Status = "active"
	StatusClosed    Status = "closed"
	StatusAbandoned Status = "abandoned"
)

// Session is the record persisted in the sessions table.
type Session struct {
	ID                int64  `json:"id"`
	SessionID         string `json:"session_id"`
	Status            string `json:"status"`
	ConstitutionID    string `json:"constitution_id,omitempty"`
	ConstitutionVer   string `json:"constitution_ver,omitempty"`
	ActiveMods        string `json:"active_mods,omitempty"` // JSON array of mod_id
	StartedAt         string `json:"started_at"`
	ClosedAt          string `json:"closed_at,omitempty"`
	Notes             string `json:"notes,omitempty"`
	ParentSessionID   string `json:"parent_session_id,omitempty"` // for sub-sessions
	Operator          string `json:"operator,omitempty"`          // who started this session
}
