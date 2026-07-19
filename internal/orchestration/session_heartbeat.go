// O5E-heartbeat: SessionHeartbeat — refreshes the session's
// last_heartbeat_at column. The harness adapters (Wave 5D) call this
// every ~30s. The sweeper (5E.iii) compares last_heartbeat_at against
// HEARTBEAT_TIMEOUT; stale sessions are promoted to closed_aborted.
//
// INV-9: every open|idle session whose last_heartbeat_at is older
// than HEARTBEAT_TIMEOUT seconds gets promoted to closed_aborted by
// either the sweeper goroutine or the boot reconciliation step.
//
// Per RFC §4.2 and 5E.ii.
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// SessionHeartbeatInput is the request to refresh a session's
// last_heartbeat_at. The session must be open or idle; closed sessions
// refuse with ErrNotFound (a heartbeat on a closed session is a
// client bug — operator should call _recover or _resume instead).
type SessionHeartbeatInput struct {
	SessionID string `json:"session_id"`
}

// SessionHeartbeatOutput reflects the new last_heartbeat_at after
// the call.
type SessionHeartbeatOutput struct {
	SessionID       string    `json:"session_id"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
}

// SessionHeartbeat refreshes a session's last_heartbeat_at.
//
// # Atomicity contract
//   - ONE public function
//   - TWO input/output shapes
//   - DEPENDS on Store.SaveHeartbeat (Wave 5E.ii; added in sqlite
//     and postgres Store implementations)
//   - Emits a write_audit row with session_event='heartbeat' (INV-1 + INV-9)
//
// Returns ErrInvalidArgument if SessionID is empty. Returns
// ErrNotFound if the session doesn't exist OR isn't open/idle.
func (o *Orchestrator) SessionHeartbeat(ctx context.Context, in SessionHeartbeatInput) (*SessionHeartbeatOutput, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, errMissingField("session_id")
	}

	wc := store.WriteContext{
		Actor:     "orchestrator_session_heartbeat",
		SessionID: in.SessionID,
		WritePath: "SessionHeartbeat",
		// ProjectID resolved by SaveHeartbeat via requireProject.
	}
	if err := o.Store.SaveHeartbeat(ctx, wc, in.SessionID); err != nil {
		return nil, fmt.Errorf("session_heartbeat: %w", err)
	}

	// Pull the session back to surface the new timestamp.
	sess, err := o.Store.GetSession(ctx, in.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session_heartbeat: get: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("%w: session vanished after heartbeat", store.ErrNotFound)
	}

	lastHB, _ := time.Parse(time.RFC3339Nano, sess.LastHeartbeatAt)
	if lastHB.IsZero() {
		lastHB = o.now()
	}
	return &SessionHeartbeatOutput{
		SessionID:       in.SessionID,
		LastHeartbeatAt: lastHB,
	}, nil
}
