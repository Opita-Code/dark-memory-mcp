// Package vlp (continued) — atomic spec 2.3 (VLPPersistence).
//
// Persistence is the VLP-aware wrapper over store.Store for vlp_state rows.
// Performs State ↔ int conversion since the Store interface uses raw int
// (avoids an import cycle between internal/store and internal/vlp).
//
// Atomicity contract:
//   - ONE interface: Persistence (Save / Load / List)
//   - ONE acceptance test: TestVLP_PersistenceRoundTrip (in persistence_test.go)
//   - ONE PR worth of work
//   - Direct deps: 2.1 (SessionState) + Layer 0 (Store)
//   - Independently reviewable: no other v1.1 spec touched
//
// Trust boundary: Persistence trusts caller-supplied current state. The
// atomic spec 2.5 (VLPLoopUseCase) will load state via Persistence.Load,
// pass it through 2.2 Package.Record, then Persistence.Save the result.
//
// Input normalization rules (bug-hunt 2.3 review):
//   - EventUnknown   → LastEvent   = "" (DB stores empty, NOT "unknown(0)")
//   - VerdictUnknown → LastVerdict = "" (DB stores empty, NOT "unknown(0)")
//   - StateUnknown   → rejected at Save time (zero-value sentinel)
//   - State enum     → converted to int via int(st) before reaching the Store
//   - canonical State names (e.g. "drafting_spec") for ListByState
//     are resolved to their numeric value here, not in the Store
package vlp

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Persistence is the VLP-aware Store wrapper. Construct with NewPersistence
// after the underlying Store is opened.
type Persistence struct {
	store store.Store
}

// NewPersistence returns a Persistence backed by the given Store. Returns
// an error if s is nil — without this check, every method would nil-panic
// at the first store call.
func NewPersistence(s store.Store) (*Persistence, error) {
	if s == nil {
		return nil, fmt.Errorf("vlp: NewPersistence: store must not be nil")
	}
	return &Persistence{store: s}, nil
}

// Save persists the current VLP state for a session. Upserts by
// (project_id, session_id). Writes a write_audit row in the same
// transaction as the UPSERT (INV-1).
//
// Parameters:
//   - sessionID:  stable identifier for the loop
//   - st:         current state (from spec 2.1 state machine); StateUnknown rejected
//   - lastEvent:  the most recent Event applied (EventUnknown → empty string in DB)
//   - lastVerdict: the verdict of the last EventDriftLog (VerdictUnknown → empty string)
//   - turn:       how many turns have elapsed (must be >= 0; negative rejected)
//   - minset:     current minset mode (empty string if persona/minset not active)
func (p *Persistence) Save(ctx context.Context, wc store.WriteContext, sessionID string, st State, lastEvent Event, lastVerdict Verdict, turn int, minset string) error {
	if sessionID == "" {
		return fmt.Errorf("vlp: persistence.Save: sessionID is required: %w", store.ErrInvalidArgument)
	}
	if st == StateUnknown {
		return fmt.Errorf("vlp: persistence.Save: refusing to persist StateUnknown: %w", store.ErrInvalidArgument)
	}
	if turn < 0 {
		return fmt.Errorf("vlp: persistence.Save: turn must be >= 0 (got %d): %w", turn, store.ErrInvalidArgument)
	}
	lastEventStr := ""
	if lastEvent != EventUnknown {
		lastEventStr = lastEvent.String()
	}
	lastVerdictStr := ""
	if lastVerdict != VerdictUnknown {
		lastVerdictStr = lastVerdict.String()
	}
	row := &store.VLPStateRow{
		SessionID:       sessionID,
		State:           int(st),
		LastEvent:       lastEventStr,
		LastVerdict:     lastVerdictStr,
		TurnCount:       turn,
		MinsetCurrent:   minset,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
	}
	if _, err := p.store.SaveVLPState(ctx, wc, row); err != nil {
		return fmt.Errorf("vlp: persistence.Save: %w", err)
	}
	return nil
}

// Snapshot is what Persistence.Load returns — the full row plus the
// typed State (decoded from the int).
type Snapshot struct {
	State     State
	TurnCount int
	Exists    bool               // false if no row found
	Row       *store.VLPStateRow // nil if Exists == false
}

// Load returns the current state for a session. If no row exists,
// returns Snapshot{Exists: false} and no error.
func (p *Persistence) Load(ctx context.Context, sessionID string) (Snapshot, error) {
	if sessionID == "" {
		return Snapshot{}, fmt.Errorf("vlp: persistence.Load: sessionID is required: %w", store.ErrInvalidArgument)
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

// ListByState returns all snapshots in the given state. state is the
// typed enum (StateDraftingSpec etc.); empty value StateUnknown means
// "all states". Limit <= 0 means no limit.
//
// Translates the State enum to its numeric form before calling the
// Store, so the caller never has to know about the int representation.
func (p *Persistence) ListByState(ctx context.Context, state State, limit int) ([]store.VLPStateRow, error) {
	if state == StateUnknown {
		return p.store.ListVLPStates(ctx, "", limit)
	}
	return p.store.ListVLPStates(ctx, strconv.Itoa(int(state)), limit)
}
