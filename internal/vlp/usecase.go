// Package vlp (continued) — atomic spec 2.5 (VLPLoopUseCase).
//
// VLPLoopUseCase is the end-to-end driver for the VLP state machine. It
// composes:
//
//   - vlp.Transition  (2.1)  — pure state-machine logic
//   - vlp.Persistence (2.3)  — Store-backed state
//   - vlp.Auditor     (2.4)  — transition-level audit (also written via Store)
//
// The harness (spec 6.x adapters) calls HandleEvent for every event in
// the loop. The UseCase is the SINGLE point that drives the loop:
//
//   1. Load current state from Persistence
//   2. Compute transition (from, event, verdict) → to
//   3. Persist + audit in ONE atomic tx via Store.SaveVLPStateWithTransition
//      (closes the 2.5 atomicity gap from spec 2.4)
//   4. Return new state + next-action hint
//
// Bootstrap semantics: the DB never persists StateIdle (it is a virtual
// anchor only). The first event for a session_id must be EventSessionStart;
// the UseCase synthesizes from=StateIdle, computes the transition to
// StateDraftingSpec, and persists StateDraftingSpec directly. The audit
// row records the virtual from=StateIdle for forensic reconstruction.
//
// Atomicity (debt-elimination commit): UseCase delegates to
// Store.SaveVLPStateWithTransition which writes the vlp_state row,
// the row-level audit, AND the transition-level audit in a single
// DB transaction. INV-1 is satisfied atomically: either all three
// rows land or none does. The Auditor.RecordTransition path is kept
// for callers that want explicit per-call control, but the UseCase
// no longer uses it (deprecated for VLP).
//
// Trust boundary: UseCase trusts Persistence + Auditor + Store to
// fail-fast on their respective error surfaces. UseCase itself does
// not validate transition validity (vlp.Transition does that in 2.1).
//
// Atomicity contract:
//   - ONE entry point: UseCase.HandleEvent
//   - ONE acceptance test: TestVLP_E2E_DraftToComplete (in usecase_test.go)
//   - ONE PR worth of work
//   - Direct deps: 2.1 (Transition) + 2.3 (Persistence) + 2.4 (Auditor) + Store
//   - Independently reviewable: no other v1.1 spec touched
package vlp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// HandleEventResult is what UseCase.HandleEvent returns to the caller
// (typically the harness adapter in spec 6.x).
type HandleEventResult struct {
	NewState   State  // state after this transition
	TurnCount  int    // turn count after this transition (incremented)
	NextAction string // canonical event name expected next; "" if terminal
	IsTerminal bool   // convenience: NewState.Terminal()
}

// UseCase is the end-to-end VLP loop driver. Composes Persistence (2.3)
// + Auditor (2.4). Construct with NewUseCase.
//
// Debt-elimination note: UseCase.HandleEvent now goes through
// Persistence's underlying Store directly (via reflection-free accessor)
// to call SaveVLPStateWithTransition, achieving single-tx atomicity.
// The Persistence.Save + Auditor.RecordTransition two-call pattern
// remains for callers that need finer control.
type UseCase struct {
	persistence *Persistence
	auditor     *Auditor
	store       store.Store // captured at construction for atomic SaveVLPStateWithTransition
}

// NewUseCase returns a UseCase composing the given Persistence and
// Auditor. Returns an error if either is nil — same defensive pattern
// as NewPersistence / NewAuditor.
func NewUseCase(p *Persistence, a *Auditor) (*UseCase, error) {
	if p == nil {
		return nil, fmt.Errorf("vlp: NewUseCase: persistence must not be nil")
	}
	if a == nil {
		return nil, fmt.Errorf("vlp: NewUseCase: auditor must not be nil")
	}
	return &UseCase{persistence: p, auditor: a, store: a.store}, nil
}

// HandleEvent is the SINGLE entry point for advancing the VLP loop.
//
// Parameters:
//   - ctx:        context for cancellation
//   - wc:         WriteContext shared by Store.SaveVLPStateWithTransition
//   - sessionID:  stable identifier for the loop
//   - event:      the event being applied (must NOT be EventUnknown)
//   - verdict:    payload for EventDriftLog (VerdictUnknown for other events)
//   - minset:     current minset mode (empty string if persona/minset not active)
//
// Returns HandleEventResult with new state, turn count, next-action hint,
// and IsTerminal flag. Returns error on:
//   - persistence Load failure (I/O error from Store)
//   - Store.SaveVLPStateWithTransition failure (any of: upsert, row-level
//     audit, transition-level audit — all rolled back on failure)
//   - vlp.Transition returning ErrInvalidTransition (caller bug — wrong event for current state)
//
// Bootstrap: if no row exists for sessionID, the first event MUST be
// EventSessionStart. The UseCase synthesizes from=StateIdle, transitions
// to StateDraftingSpec, and persists directly. Any other first event
// returns an error.
func (uc *UseCase) HandleEvent(ctx context.Context, wc store.WriteContext, sessionID string, event Event, verdict Verdict, minset string) (HandleEventResult, error) {
	if sessionID == "" {
		return HandleEventResult{}, fmt.Errorf("vlp: HandleEvent: sessionID is required")
	}
	if event == EventUnknown {
		return HandleEventResult{}, fmt.Errorf("vlp: HandleEvent: event must not be EventUnknown")
	}

	// 1. Load current state
	snap, err := uc.persistence.Load(ctx, sessionID)
	if err != nil {
		return HandleEventResult{}, fmt.Errorf("vlp: HandleEvent: load: %w", err)
	}

	// 2. Determine from-state and starting turn
	var from State
	var startTurn int
	if snap.Exists {
		from = snap.State
		startTurn = snap.TurnCount
	} else {
		// Bootstrap: virtual StateIdle, turn 0. Only EventSessionStart allowed.
		if event != EventSessionStart {
			return HandleEventResult{}, fmt.Errorf("vlp: HandleEvent: first event for session %q must be EventSessionStart, got %s", sessionID, event)
		}
		from = StateIdle
		startTurn = 0
	}

	// 3. Compute transition (pure, no I/O)
	to, err := Transition(from, event, verdict)
	if err != nil {
		return HandleEventResult{}, fmt.Errorf("vlp: HandleEvent: transition: %w", err)
	}

	// 4. Build the row to persist + the transition-level audit payload.
	newTurn := startTurn + 1
	row := &store.VLPStateRow{
		SessionID:       sessionID,
		State:           int(to),
		LastEvent:       eventToCanonicalString(event),
		LastVerdict:     verdictToCanonicalString(verdict),
		TurnCount:       newTurn,
		MinsetCurrent:   minset,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
	}
	transitionPayload := usecaseTransitionNotes(sessionID, TransitionRecord{
		From: from, Event: event, Verdict: verdict, To: to, Turn: newTurn,
	})

	// 5. Persist + emit BOTH audit rows in a single DB transaction.
	// Closes the 2.5 atomicity gap from spec 2.4 (was: 2 separate calls,
	// could fail between them and leave state without transition audit).
	if _, err := uc.store.SaveVLPStateWithTransition(ctx, wc, row, transitionPayload); err != nil {
		return HandleEventResult{}, fmt.Errorf("vlp: HandleEvent: save+audit: %w", err)
	}

	// 6. Return result with next-action hint
	return HandleEventResult{
		NewState:   to,
		TurnCount:  newTurn,
		NextAction: nextEventNameFor(to),
		IsTerminal: to.Terminal(),
	}, nil
}

// HandleAbort is a convenience wrapper for EventAbort. Equivalent to
// HandleEvent(ctx, wc, sessionID, EventAbort, VerdictUnknown, minset)
// but with explicit naming for harness code that needs to abort the
// loop from any non-terminal state.
func (uc *UseCase) HandleAbort(ctx context.Context, wc store.WriteContext, sessionID string, minset string) (HandleEventResult, error) {
	return uc.HandleEvent(ctx, wc, sessionID, EventAbort, VerdictUnknown, minset)
}

// eventToCanonicalString returns the canonical event name for persistence.
// EventUnknown → "" (don't store "unknown(0)" sentinel).
func eventToCanonicalString(e Event) string {
	if e == EventUnknown {
		return ""
	}
	return e.String()
}

// verdictToCanonicalString returns the canonical verdict name for persistence.
// VerdictUnknown → "".
func verdictToCanonicalString(v Verdict) string {
	if v == VerdictUnknown {
		return ""
	}
	return v.String()
}

// nextEventNameFor maps the post-transition state to the canonical
// event name the harness is expected to fire next. Returns "" for
// terminal states (Complete / NeedsHuman / Aborted) and for StateUnknown
// (defensive default — should never be reached in normal flow since
// StateUnknown is never persisted).
//
// Distinct from nextActionFor in spec 2.2 (package.go): that one returns
// action verbs ("stop", "unknown") for the package-level CallMappings
// table; this one returns canonical event names ("session_start",
// "vibe_publish", etc.) for harness consumption. The "" sentinel for
// terminal lets the harness loop use `result.NextAction != ""` as the
// "loop continues" indicator.
//
// This is a hint, not a contract — the harness can fire any event; the
// state machine in 2.1 will reject invalid transitions. The hint exists
// to make the harness loop ergonomic: after each HandleEvent call, the
// harness knows what to expect next without re-reading the state.
func nextEventNameFor(s State) string {
	switch s {
	case StateIdle:
		// Virtual anchor only (never persisted); defensive fallback.
		return EventSessionStart.String()
	case StateDraftingSpec:
		return EventVibePublish.String()
	case StateSpecActive:
		return EventArtifactLog.String()
	case StateDriftJudging:
		return EventDriftLog.String()
	case StateComplete, StateNeedsHuman, StateAborted:
		return ""
	default:
		return ""
	}
}

// usecaseTransitionNotes is the UseCase-specific JSON serializer for
// the transition-level audit row. Kept private (lowercase) to avoid
// name collision with auditor.go's exported marshalTransitionNotes.
//
// In v2, refactor both to a shared internal helper. For now, the two
// implementations produce byte-identical JSON for the same input.
func usecaseTransitionNotes(sessionID string, rec TransitionRecord) string {
	type jsonShape struct {
		SessionID string `json:"session_id"`
		From      int    `json:"from"`
		Event     int    `json:"event"`
		Verdict   int    `json:"verdict,omitempty"`
		To        int    `json:"to"`
		Turn      int    `json:"turn"`
	}
	s := jsonShape{
		SessionID: sessionID,
		From:      int(rec.From),
		Event:     int(rec.Event),
		To:        int(rec.To),
		Turn:      rec.Turn,
	}
	if rec.Verdict != VerdictUnknown {
		s.Verdict = int(rec.Verdict)
	}
	b, _ := json.Marshal(s)
	return string(b)
}