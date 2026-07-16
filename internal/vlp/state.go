// Package vlp implements the Vibe-Loop Protocol state machine for DMAP v1.1.
//
// Atomic spec 2.1 (SessionState) — see docs/DMAP_V1_1.md §4 Layer 2.
//
// This package owns ONE responsibility: define the per-session state machine
// that drives the VLP loop. It does NOT persist state, audit transitions,
// or compute side effects — those are atomic specs 2.3 (VLPPersistence) and
// 2.4 (VLPAuditor).
//
// Trust boundary: Transition trusts the caller-supplied `from` state.
// Persistence specs (2.3) MUST load authoritative state and perform an
// atomic compare-and-swap; never expose `from` as an unchecked MCP
// caller-controlled value.
//
// Atomicity contract:
//   - ONE function contract: Transition(from, event, verdict) → (to, error)
//   - ONE acceptance test: TestTransition_AllValidPaths
//   - ONE PR worth of work (~250 LoC)
//   - Direct deps: Layer 0 (Foundation) only
//   - Independently reviewable: no integration with other v1.1 specs
package vlp

import "fmt"

// State is the per-session lifecycle state. Encoded as int so it persists
// cheaply in SQLite/Postgres and round-trips through JSON without enum
// stringification surprises. StateUnknown is the zero value, which protects
// against uninitialised rows.
//
// APPEND-ONLY: never reorder or remove constants. Numeric values may be
// persisted in vlp_state rows by spec 2.3 (VLPPersistence). New states
// must be appended at the end of the const block.
type State int

const (
	StateUnknown State = iota
	StateIdle
	StateDraftingSpec
	StateSpecActive
	StateDriftJudging
	StateComplete
	StateNeedsHuman
	StateAborted
)

// stateNames maps State → canonical string. String() and ParseState() use
// this table. Stable across versions — values are persisted by spec 2.3
// (VLPPersistence) and audited by spec 2.4 (VLPAuditor). Spec 2.1 itself
// is pure in-memory and does not write to storage.
var stateNames = map[State]string{
	StateIdle:         "idle",
	StateDraftingSpec: "drafting_spec",
	StateSpecActive:   "spec_active",
	StateDriftJudging: "drift_judging",
	StateComplete:     "complete",
	StateNeedsHuman:   "needs_human",
	StateAborted:      "aborted",
}

// String returns the canonical state name.
func (s State) String() string {
	if name, ok := stateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", int(s))
}

// ParseState reverses String. Returns error on unknown state names.
func ParseState(s string) (State, error) {
	for state, name := range stateNames {
		if name == s {
			return state, nil
		}
	}
	return StateUnknown, fmt.Errorf("vlp: unknown state %q", s)
}

// Terminal returns true if no further transitions are possible. Complete,
// NeedsHuman, and Aborted are terminal. NeedsHuman is terminal in-place;
// recovery requires a new session.
func (s State) Terminal() bool {
	return s == StateComplete || s == StateNeedsHuman || s == StateAborted
}

// Event is an external trigger that may cause a state transition. Like
// State, encoded as int for cheap persistence.
//
// APPEND-ONLY: never reorder or remove constants.
type Event int

const (
	EventUnknown Event = iota
	EventSessionStart // harness: dark_memory_session_start
	EventVibePublish  // harness: dark_memory_vibe_publish (spec is now valid)
	EventArtifactLog  // harness: dark_memory_artifact_log (artifact done)
	EventDriftLog     // harness: dark_memory_drift_log (verdict attached as payload)
	EventAbort        // operator: stop the loop immediately
)

var eventNames = map[Event]string{
	EventSessionStart: "session_start",
	EventVibePublish:  "vibe_publish",
	EventArtifactLog:  "artifact_log",
	EventDriftLog:     "drift_log",
	EventAbort:        "abort",
}

// String returns the canonical event name.
func (e Event) String() string {
	if name, ok := eventNames[e]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", int(e))
}

// ParseEvent reverses Event.String.
func ParseEvent(s string) (Event, error) {
	for event, name := range eventNames {
		if name == s {
			return event, nil
		}
	}
	return EventUnknown, fmt.Errorf("vlp: unknown event %q", s)
}

// Verdict is the drift_judge's decision. Used as payload for EventDriftLog.
// Other events do not carry a Verdict payload (caller must pass VerdictUnknown).
//
// APPEND-ONLY: never reorder or remove constants.
type Verdict int

const (
	VerdictUnknown Verdict = iota
	VerdictAligned       // artifact matches spec → StateComplete
	VerdictDriftDetected // artifact diverges → StateSpecActive (loop back for regen)
	VerdictNeedsHuman    // judge confidence below threshold → StateNeedsHuman
)

var verdictNames = map[Verdict]string{
	VerdictAligned:       "aligned",
	VerdictDriftDetected: "drift_detected",
	VerdictNeedsHuman:    "needs_human",
}

// String returns the canonical verdict name.
func (v Verdict) String() string {
	if name, ok := verdictNames[v]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", int(v))
}

// ParseVerdict reverses Verdict.String.
func ParseVerdict(s string) (Verdict, error) {
	for verdict, name := range verdictNames {
		if name == s {
			return verdict, nil
		}
	}
	return VerdictUnknown, fmt.Errorf("vlp: unknown verdict %q", s)
}

// transition is a (state, event, verdict) → state mapping. The transitions
// table is the SINGLE SOURCE OF TRUTH for the state machine. Adding a new
// transition = adding one row here + one case in TestTransition_AllValidPaths
// + updating expectedTransitionCount in state_test.go.
//
// Verdict semantics in the transitions table:
//   - VerdictUnknown: matches when verdict is VerdictUnknown (i.e., no payload)
//   - Specific Verdict (Aligned, DriftDetected, NeedsHuman): matches only that verdict
//
// In practice, every row either has VerdictUnknown (no-payload events) or a
// specific Verdict (EventDriftLog). The payload contract checks in Transition()
// reject mismatched calls before the table is consulted.
type transition struct {
	From    State
	Event   Event
	Verdict Verdict // zero = no payload; specific = drift_log payload
	To      State
}

// expectedTransitionCount is the normative cardinality of the transitions
// table. TestTransition_TableCardinality asserts this; updating the table
// requires updating both the constant and the test (intentional friction).
const expectedTransitionCount = 10

// transitions is the canonical transition table. ORDER MATTERS for
// documentation: keep grouped by From state.
var transitions = []transition{
	// From: idle
	{StateIdle, EventSessionStart, VerdictUnknown, StateDraftingSpec},
	{StateIdle, EventAbort, VerdictUnknown, StateAborted},

	// From: drafting_spec
	{StateDraftingSpec, EventVibePublish, VerdictUnknown, StateSpecActive},
	{StateDraftingSpec, EventAbort, VerdictUnknown, StateAborted},

	// From: spec_active
	{StateSpecActive, EventArtifactLog, VerdictUnknown, StateDriftJudging},
	{StateSpecActive, EventAbort, VerdictUnknown, StateAborted},

	// From: drift_judging (verdict-dependent — payload disambiguates)
	{StateDriftJudging, EventDriftLog, VerdictAligned, StateComplete},
	{StateDriftJudging, EventDriftLog, VerdictDriftDetected, StateSpecActive},
	{StateDriftJudging, EventDriftLog, VerdictNeedsHuman, StateNeedsHuman},
	{StateDriftJudging, EventAbort, VerdictUnknown, StateAborted},

	// From: needs_human (terminal — operator must create new session)
	// From: complete (terminal)
	// From: aborted (terminal)
}

// ErrInvalidTransition is returned when (state, event, verdict) has no
// matching row in the transitions table. Use errors.As to extract; this is
// a value type so errors.Is does not work directly.
type ErrInvalidTransition struct {
	From    State
	Event   Event
	Verdict Verdict
}

func (e ErrInvalidTransition) Error() string {
	if e.Verdict != VerdictUnknown {
		return fmt.Sprintf("vlp: no transition from %s on %s with verdict %s",
			e.From, e.Event, e.Verdict)
	}
	return fmt.Sprintf("vlp: no transition from %s on %s", e.From, e.Event)
}

// Transition applies (event, verdict) to from and returns the next state.
// Returns ErrInvalidTransition if no matching row in transitions.
//
// Payload contract:
//   - EventDriftLog REQUIRES a specific Verdict (Aligned, DriftDetected, NeedsHuman)
//   - All other events MUST have VerdictUnknown (caller bug if not)
func Transition(from State, event Event, verdict Verdict) (State, error) {
	if event == EventUnknown {
		return StateUnknown, fmt.Errorf("vlp: event unknown is not a valid input")
	}
	if event == EventDriftLog {
		if verdict == VerdictUnknown {
			return StateUnknown, fmt.Errorf("vlp: event drift_log requires verdict (aligned, drift_detected, or needs_human)")
		}
	} else if verdict != VerdictUnknown {
		return StateUnknown, fmt.Errorf("vlp: event %s does not accept verdict payload (got %s)", event, verdict)
	}

	for _, t := range transitions {
		if t.From != from {
			continue
		}
		if t.Event != event {
			continue
		}
		if t.Verdict != VerdictUnknown && t.Verdict != verdict {
			continue
		}
		return t.To, nil
	}
	return StateUnknown, ErrInvalidTransition{From: from, Event: event, Verdict: verdict}
}

// AllStates returns the canonical list of valid states (excludes StateUnknown).
// Used by tests, validation, and UI rendering.
func AllStates() []State {
	return []State{
		StateIdle, StateDraftingSpec, StateSpecActive, StateDriftJudging,
		StateComplete, StateNeedsHuman, StateAborted,
	}
}

// AllEvents returns the canonical list of valid events (excludes EventUnknown).
func AllEvents() []Event {
	return []Event{
		EventSessionStart, EventVibePublish, EventArtifactLog,
		EventDriftLog, EventAbort,
	}
}

// AllVerdicts returns the canonical list of valid verdicts (excludes VerdictUnknown).
func AllVerdicts() []Verdict {
	return []Verdict{VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman}
}