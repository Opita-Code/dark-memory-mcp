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
//
// Reason defaults to "clean" (operator-initiated, terminal) when
// empty. Allowed values: "clean" (terminal, NOT resurrectable),
// "aborted" (resurrectable via dark_memory_session_resurrect),
// "archived" (terminal, NOT resurrectable, operator-vetted).
// See session.CloseReason.Validate.
type SessionCloseInput struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason,omitempty"` // empty -> "clean"
}

// SessionCloseOutput summarises the session at close.
type SessionCloseOutput struct {
	SessionID   string    `json:"session_id"`
	ClosedAt    time.Time `json:"closed_at"`
	WritesTotal int       `json:"writes_total"`
	// RunsTotal / ItemsTotal are scoped to the active project, not
	// specifically to this session (the Store doesn't expose a
	// session-scoped runs/items query yet — see spec 173 O2 notes).
	// Wave 5E.v: both counts come from indexed COUNT(*) queries
	// (CountRunsForProject / CountItemsForProject), replacing the
	// previous ListRuns + N×ListItems N+1 pattern.
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
	if err := o.Store.CloseSession(ctx, wc, in.SessionID, in.Reason); err != nil {
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

	// Wave 5E.v: replaced ListRuns + N×ListItems (N+1 query) with two
	// indexed COUNT(*) queries. For a project with N runs, this drops
	// the work from O(N) round-trips to O(1). Both queries hit the
	// project_id index; O(log n) lookup, constant memory.
	activeProject := o.Store.ActiveProject()
	runsTotal, err := o.Store.CountRunsForProject(ctx, activeProject)
	if err != nil {
		return nil, fmt.Errorf("session_close: count runs: %w", err)
	}
	itemsTotal, err := o.Store.CountItemsForProject(ctx, activeProject)
	if err != nil {
		return nil, fmt.Errorf("session_close: count items: %w", err)
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
		RunsTotal:   runsTotal,
		ItemsTotal:  itemsTotal,
	}, nil
}