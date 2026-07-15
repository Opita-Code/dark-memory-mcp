// O2: SessionClose — closes a session and returns a summary of
// activity. WritesTotal comes from the write_audit table filtered by
// session_id. RunsTotal and ItemsTotal are derived from ListRuns and
// ListItems filtered by the active project (the session's project,
// which CloseSession keeps current via its project-id filter).
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// SessionCloseInput is the request to close a session.
type SessionCloseInput struct {
	SessionID string `json:"session_id"`
}

// SessionCloseOutput summarises the session at close.
type SessionCloseOutput struct {
	SessionID   string    `json:"session_id"`
	ClosedAt    time.Time `json:"closed_at"`
	WritesTotal int       `json:"writes_total"`
	// RunsTotal / ItemsTotal are scoped to the active project, not
	// specifically to this session (the Store doesn't expose a
	// session-scoped runs/items query yet — see spec 173 O2 notes).
	RunsTotal  int `json:"runs_total"`
	ItemsTotal int `json:"items_total"`
}

// SessionClose closes a session and returns a summary of activity.
// Returns ErrInvalidArgument if SessionID is empty. Returns
// ErrNotFound if the session doesn't exist or doesn't belong to the
// active project. Returns any other error from the Store.
func (o *Orchestrator) SessionClose(ctx context.Context, in SessionCloseInput) (*SessionCloseOutput, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, errMissingField("session_id")
	}

	wc := store.WriteContext{
		Actor:     "orchestrator_session_close",
		SessionID: in.SessionID,
		WritePath: "SessionClose",
		// ProjectID is resolved by CloseSession via requireProject and
		// projectIDOrActive; we don't have to set it.
	}
	if err := o.Store.CloseSession(ctx, wc, in.SessionID); err != nil {
		return nil, fmt.Errorf("session_close: close: %w", err)
	}

	// Pull the closed session back to surface ClosedAt + status.
	closedSess, err := o.Store.GetSession(ctx, in.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session_close: get: %w", err)
	}
	if closedSess == nil {
		// Should not happen — we just closed it — but defend.
		return nil, fmt.Errorf("%w: session vanished after close", store.ErrNotFound)
	}

	// Summary: count writes from this session.
	writes, err := o.Store.ListWrites(ctx, audit.ListFilters{
		SessionID: in.SessionID,
		Limit:     10000,
	})
	if err != nil {
		return nil, fmt.Errorf("session_close: list writes: %w", err)
	}

	// Runs/items counts via the active project's list methods.
	runs, err := o.Store.ListRuns(ctx, "", 10000)
	if err != nil {
		return nil, fmt.Errorf("session_close: list runs: %w", err)
	}

	var itemsTotal int
	for _, r := range runs {
		items, err := o.Store.ListItems(ctx, r.ID, "", 10000)
		if err != nil {
			return nil, fmt.Errorf("session_close: list items (run %d): %w", r.ID, err)
		}
		itemsTotal += len(items)
	}

	closedAt, _ := time.Parse(time.RFC3339Nano, closedSess.ClosedAt)
	if closedAt.IsZero() {
		// Defensive: close succeeded but the timestamp wasn't set
		// (shouldn't happen, but fall back to "now").
		closedAt = o.now()
	}
	return &SessionCloseOutput{
		SessionID:   in.SessionID,
		ClosedAt:    closedAt,
		WritesTotal: len(writes),
		RunsTotal:   len(runs),
		ItemsTotal:  itemsTotal,
	}, nil
}