// Package session defines the session primitive. A session is one
// operational context — typically one user-facing chat session, one
// red-team run, or one vibe-flow pipeline execution. Sessions are
// first-class (have lifecycle: start/close/status) and carry the
// audit trail that ties together every write made within them.
package session

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
