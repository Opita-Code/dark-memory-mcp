// Package vlp (continued) — atomic spec 2.3 (VLPPersistence).
//
// Persistence is the VLP-aware wrapper over store.Store for vlp_state rows.
// Performs State ↔ int conversion since the Store interface uses raw int
// (avoids an import cycle between internal/store and internal/vlp).
//
// Atomicity contract:
//   - ONE interface: Persistence (Save / Load / List)
//   - ONE acceptance test: TestVLP_PersistenceRoundTrip (in persistence_test.go)
//   - ONE PR worth of work (~120 LoC for this file; ~100 for tests)
//   - Direct deps: 2.1 (SessionState) + Layer 0 (Store)
//   - Independently reviewable: no other v1.1 spec touched
//
// Trust boundary: Persistence trusts caller-supplied current state. The
// atomic spec 2.5 (VLPLoopUseCase) will load state via Persistence.Load,
// pass it through 2.2 Package.Record, then Persistence.Save the result.
package vlp

import (
	"context"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Persistence is the VLP-aware Store wrapper. Construct with NewPersistence
// after the underlying Store is opened.
type Persistence struct {
	store store.Store
}

// NewPersistence returns a Persistence backed by the given Store.
func NewPersistence(s store.Store) *Persistence {
	return &Persistence{store: s}
}

// Save persists the current VLP state for a session. Upserts by session_id.
// Writes a write_audit row (INV-1) via the Store.
//
// Parameters:
//   - sessionID:  stable identifier for the loop
//   - st:         current state (from spec 2.1 state machine)
//   - lastEvent:  the most recent Event applied (may be EventUnknown for first save)
//   - lastVerdict: the verdict of the last EventDriftLog (empty for non-drift events)
//   - turn:       how many turns have elapsed (0 for first save)
//   - minset:     current minset mode (empty string if persona/minset not active)
func (p *Persistence) Save(ctx context.Context, wc store.WriteContext, sessionID string, st State, lastEvent Event, lastVerdict Verdict, turn int, minset string) error {
	if sessionID == "" {
		return fmt.Errorf("vlp: persistence.Save: sessionID is required")
	}
	if st == StateUnknown {
		return fmt.Errorf("vlp: persistence.Save: refusing to persist StateUnknown")
	}
	row := &store.VLPStateRow{
		SessionID:       sessionID,
		State:           int(st),
		LastEvent:       lastEvent.String(),
		LastVerdict:     lastVerdict.String(),
		TurnCount:       turn,
		MinsetCurrent:   minset,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
	}
	_, err := p.store.SaveVLPState(ctx, wc, row)
	if err != nil {
		return fmt.Errorf("vlp: persistence.Save: %w", err)
	}
	return nil
}

// Snapshot is what Persistence.Load returns — the full row plus the
// typed State (decoded from the int).
type Snapshot struct {
	State     State
	TurnCount int
	Exists    bool   // false if no row found
	Row       *store.VLPStateRow // nil if Exists == false
}

// Load returns the current state for a session. If no row exists,
// returns Snapshot{Exists: false} and no error.
func (p *Persistence) Load(ctx context.Context, sessionID string) (Snapshot, error) {
	if sessionID == "" {
		return Snapshot{}, fmt.Errorf("vlp: persistence.Load: sessionID is required")
	}
	row, err := p.store.GetVLPState(ctx, sessionID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("vlp: persistence.Load: %w", err)
	}
	if row == nil {
		return Snapshot{Exists: false}, nil
	}
	return Snapshot{
		State:     State(row.State),
		TurnCount: row.TurnCount,
		Exists:    true,
		Row:       row,
	}, nil
}

// ListByState returns all snapshots in the given state. stateName is the
// canonical state name (e.g. "drafting_spec"); empty string returns all.
// Limit <= 0 means no limit.
func (p *Persistence) ListByState(ctx context.Context, stateName string, limit int) ([]store.VLPStateRow, error) {
	return p.store.ListVLPStates(ctx, stateName, limit)
}