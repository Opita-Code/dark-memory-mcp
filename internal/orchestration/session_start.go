// O1: SessionStart — opens a session for an operator and binds it to
// a project. The active project is set on the Store so subsequent
// operations land in the right tenant.
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// SessionStartInput is the request to open a session.
//
// ProjectID is required and must already exist (or be "default" — the
// catch-all project seeded on Open). ConstitutionID is optional; if
// set it is recorded on the session for runWatchdog provenance.
type SessionStartInput struct {
	Operator        string `json:"operator"`
	ProjectID       string `json:"project_id"`
	ConstitutionID  string `json:"constitution_id,omitempty"`
	ConstitutionVer string `json:"constitution_ver,omitempty"`
	Notes           string `json:"notes,omitempty"`
}

// SessionStartOutput is what SessionStart returns. SessionID is the
// new opaque ID; subsequent operations carry it.
type SessionStartOutput struct {
	SessionID      string    `json:"session_id"`
	ProjectID      string    `json:"project_id"`
	StartedAt      time.Time `json:"started_at"`
	ConstitutionID string    `json:"constitution_id,omitempty"`
}

// SessionStart opens a session for the given operator + project.
// Returns ErrInvalidArgument if Operator or ProjectID is empty.
// Returns ErrSessionRequired if the project could not be set active
// (defensive — should not happen with a projectID that just passed
// SetActiveProject validation).
func (o *Orchestrator) SessionStart(ctx context.Context, in SessionStartInput) (*SessionStartOutput, error) {
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return nil, errMissingField("project_id")
	}

	// Set the active project. SetActiveProject validates the project
	// exists; special-cases "default" for legacy compat.
	if err := o.Store.SetActiveProject(ctx, in.ProjectID); err != nil {
		return nil, fmt.Errorf("session_start: set active project: %w", err)
	}

	now := o.now().Format(time.RFC3339Nano)
	sess := &session.Session{
		SessionID:       session.NewSessionID(), // see session/types.go
		Status:          string(session.StatusActive),
		ConstitutionID:  in.ConstitutionID,
		ConstitutionVer: in.ConstitutionVer,
		Notes:           in.Notes,
		Operator:        in.Operator,
		StartedAt:       now,
	}

	wc := store.WriteContext{
		Actor:          "orchestrator_session_start",
		SessionID:      sess.SessionID,
		WritePath:      "SessionStart",
		ConstitutionID: in.ConstitutionID,
		ConstitutionVer: in.ConstitutionVer,
		ProjectID:      in.ProjectID,
	}
	if _, err := o.Store.SaveSession(ctx, wc, sess); err != nil {
		return nil, fmt.Errorf("session_start: save: %w", err)
	}

	// Note: SaveSession itself emits a write_audit row (INV-1). No
	// second audit row needed here. The orchestrator-level audit
	// signal is the SaveSession call itself.

	startedAt, _ := time.Parse(time.RFC3339Nano, now)
	return &SessionStartOutput{
		SessionID:      sess.SessionID,
		ProjectID:      in.ProjectID,
		StartedAt:      startedAt,
		ConstitutionID: in.ConstitutionID,
	}, nil
}