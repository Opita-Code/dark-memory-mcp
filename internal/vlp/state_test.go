// Tests for atomic spec 2.1 (SessionState) — see docs/DMAP_V1_1.md §4 Layer 2.
//
// The acceptance test (TestTransition_AllValidPaths) is the ONE test that
// defines "done" for this spec: every valid (state, event, verdict)
// triple in the canonical workflow must round-trip through Transition().
// Other tests are property checks (cardinality, table reachability,
// terminal classification, round-trip, errors.As compatibility).
package vlp

import (
	"errors"
	"testing"
)

// TestTransition_AllValidPaths is the ACCEPTANCE TEST for spec 2.1.
// It enumerates the canonical VLP workflow end-to-end. If this test fails,
// the state machine does not match the spec.
func TestTransition_AllValidPaths(t *testing.T) {
	cases := []struct {
		name    string
		from    State
		event   Event
		verdict Verdict
		want    State
	}{
		// Happy path: full workflow
		{
			name: "idle+session_start → drafting_spec", from: StateIdle, event: EventSessionStart, verdict: VerdictUnknown, want: StateDraftingSpec,
		},
		{
			name: "drafting_spec+vibe_publish → spec_active", from: StateDraftingSpec, event: EventVibePublish, verdict: VerdictUnknown, want: StateSpecActive,
		},
		{
			name: "spec_active+artifact_log → drift_judging", from: StateSpecActive, event: EventArtifactLog, verdict: VerdictUnknown, want: StateDriftJudging,
		},
		{
			name: "drift_judging+drift_log(aligned) → complete", from: StateDriftJudging, event: EventDriftLog, verdict: VerdictAligned, want: StateComplete,
		},

		// Drift loop-back
		{
			name: "drift_judging+drift_log(drift_detected) → spec_active", from: StateDriftJudging, event: EventDriftLog, verdict: VerdictDriftDetected, want: StateSpecActive,
		},

		// Escalation
		{
			name: "drift_judging+drift_log(needs_human) → needs_human", from: StateDriftJudging, event: EventDriftLog, verdict: VerdictNeedsHuman, want: StateNeedsHuman,
		},

		// Aborts from EVERY non-terminal state (including idle — operator may
		// cancel a session before starting any work)
		{
			name: "idle+abort → aborted", from: StateIdle, event: EventAbort, verdict: VerdictUnknown, want: StateAborted,
		},
		{
			name: "drafting_spec+abort → aborted", from: StateDraftingSpec, event: EventAbort, verdict: VerdictUnknown, want: StateAborted,
		},
		{
			name: "spec_active+abort → aborted", from: StateSpecActive, event: EventAbort, verdict: VerdictUnknown, want: StateAborted,
		},
		{
			name: "drift_judging+abort → aborted", from: StateDriftJudging, event: EventAbort, verdict: VerdictUnknown, want: StateAborted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Transition(tc.from, tc.event, tc.verdict)
			if err != nil {
				t.Fatalf("Transition(%s, %s, %s): unexpected error: %v",
					tc.from, tc.event, tc.verdict, err)
			}
			if got != tc.want {
				t.Errorf("Transition(%s, %s, %s) = %s, want %s",
					tc.from, tc.event, tc.verdict, got, tc.want)
			}
		})
	}
}

// TestTransition_TableCardinality asserts the transitions table contains
// exactly expectedTransitionCount rows. Adding/removing a row requires
// updating both the table and the constant — intentional friction.
func TestTransition_TableCardinality(t *testing.T) {
	if got := len(transitions); got != expectedTransitionCount {
		t.Errorf("transitions table has %d rows, want %d (update table + constant if you meant to)",
			got, expectedTransitionCount)
	}
}

// TestTransition_AllRowsInTableAreReachable asserts every row in the
// transitions table is exercised by Transition(). Catches "added a row to
// the table but it's unreachable" or "removed a row but the test still
// references it" bugs.
func TestTransition_AllRowsInTableAreReachable(t *testing.T) {
	for i, t0 := range transitions {
		name := t0.From.String() + "+" + t0.Event.String() + "→" + t0.To.String()
		if t0.Verdict != VerdictUnknown {
			name += "(verdict=" + t0.Verdict.String() + ")"
		}
		t.Run(name, func(t *testing.T) {
			got, err := Transition(t0.From, t0.Event, t0.Verdict)
			if err != nil {
				t.Fatalf("table row %d unreachable: %v", i, err)
			}
			if got != t0.To {
				t.Errorf("table row %d: Transition() returned %s, want %s", i, got, t0.To)
			}
		})
	}
}

// TestTransition_InvalidPaths covers the rejection cases. Every (state,
// event, verdict) triple NOT in the transitions table must return
// ErrInvalidTransition or a payload-contract error.
func TestTransition_InvalidPaths(t *testing.T) {
	cases := []struct {
		name    string
		from    State
		event   Event
		verdict Verdict
	}{
		// Terminal states reject all events (except EventAbort which is a no-op)
		{"complete+session_start", StateComplete, EventSessionStart, VerdictUnknown},
		{"complete+abort", StateComplete, EventAbort, VerdictUnknown},
		{"needs_human+anything", StateNeedsHuman, EventSessionStart, VerdictUnknown},
		{"aborted+anything", StateAborted, EventSessionStart, VerdictUnknown},

		// Wrong events for state
		{"idle+vibe_publish", StateIdle, EventVibePublish, VerdictUnknown},
		{"spec_active+session_start", StateSpecActive, EventSessionStart, VerdictUnknown},
		{"drafting_spec+artifact_log", StateDraftingSpec, EventArtifactLog, VerdictUnknown},
		{"drift_judging+vibe_publish", StateDriftJudging, EventVibePublish, VerdictUnknown},

		// EventDriftLog requires verdict
		{"drift_judging+drift_log(no verdict)", StateDriftJudging, EventDriftLog, VerdictUnknown},

		// EventDriftLog with wrong verdict for state
		{"idle+drift_log(aligned)", StateIdle, EventDriftLog, VerdictAligned},

		// Other events reject verdict payload
		{"idle+session_start(verdict)", StateIdle, EventSessionStart, VerdictAligned},
		{"idle+abort(verdict)", StateIdle, EventAbort, VerdictAligned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Transition(tc.from, tc.event, tc.verdict)
			if err == nil {
				t.Fatalf("Transition(%s, %s, %s): expected error, got nil",
					tc.from, tc.event, tc.verdict)
			}
		})
	}
}

// TestState_Terminal verifies Terminal() classification for every state.
func TestState_Terminal(t *testing.T) {
	terminal := map[State]bool{
		StateComplete:   true,
		StateNeedsHuman: true,
		StateAborted:    true,
	}
	for _, s := range AllStates() {
		want := terminal[s]
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}

// TestState_StringRoundTrip verifies String() and ParseState() are inverses.
func TestState_StringRoundTrip(t *testing.T) {
	for _, s := range AllStates() {
		t.Run(s.String(), func(t *testing.T) {
			got, err := ParseState(s.String())
			if err != nil {
				t.Fatalf("ParseState(%q): %v", s.String(), err)
			}
			if got != s {
				t.Errorf("round-trip: got %s, want %s", got, s)
			}
		})
	}
}

func TestState_ParseUnknown(t *testing.T) {
	_, err := ParseState("nonsense")
	if err == nil {
		t.Fatal("expected error for unknown state")
	}
}

// TestEvent_StringRoundTrip verifies Event.String() and ParseEvent() are inverses.
func TestEvent_StringRoundTrip(t *testing.T) {
	for _, e := range AllEvents() {
		t.Run(e.String(), func(t *testing.T) {
			got, err := ParseEvent(e.String())
			if err != nil {
				t.Fatalf("ParseEvent(%q): %v", e.String(), err)
			}
			if got != e {
				t.Errorf("round-trip: got %s, want %s", got, e)
			}
		})
	}
}

// TestVerdict_StringRoundTrip verifies Verdict.String() and ParseVerdict() are inverses.
func TestVerdict_StringRoundTrip(t *testing.T) {
	for _, v := range AllVerdicts() {
		t.Run(v.String(), func(t *testing.T) {
			got, err := ParseVerdict(v.String())
			if err != nil {
				t.Fatalf("ParseVerdict(%q): %v", v.String(), err)
			}
			if got != v {
				t.Errorf("round-trip: got %s, want %s", got, v)
			}
		})
	}
}

// TestTransition_ErrInvalidTransition_AsError verifies the error type is
// usable with errors.As for callers that want to branch on it. ErrInvalidTransition
// is a value type, so errors.Is does not work directly — use errors.As.
func TestTransition_ErrInvalidTransition_AsError(t *testing.T) {
	_, err := Transition(StateComplete, EventSessionStart, VerdictUnknown)
	if err == nil {
		t.Fatal("expected error")
	}
	var target ErrInvalidTransition
	if !errors.As(err, &target) {
		t.Fatalf("error %v is not ErrInvalidTransition", err)
	}
	if target.From != StateComplete || target.Event != EventSessionStart {
		t.Errorf("unexpected error payload: %+v", target)
	}
}

// TestTransition_PayloadContractErrors verifies the payload validation
// returns clear errors (not ErrInvalidTransition) for caller bugs.
func TestTransition_PayloadContractErrors(t *testing.T) {
	t.Run("drift_log without verdict", func(t *testing.T) {
		_, err := Transition(StateDriftJudging, EventDriftLog, VerdictUnknown)
		if err == nil {
			t.Fatal("expected error")
		}
		// Should NOT be ErrInvalidTransition (it's a caller-bug error).
		var target ErrInvalidTransition
		if errors.As(err, &target) {
			t.Errorf("expected payload-contract error, got ErrInvalidTransition: %v", err)
		}
	})
	t.Run("non-drift event with verdict", func(t *testing.T) {
		_, err := Transition(StateIdle, EventSessionStart, VerdictAligned)
		if err == nil {
			t.Fatal("expected error")
		}
		var target ErrInvalidTransition
		if errors.As(err, &target) {
			t.Errorf("expected payload-contract error, got ErrInvalidTransition: %v", err)
		}
	})
	t.Run("EventUnknown rejected", func(t *testing.T) {
		_, err := Transition(StateIdle, EventUnknown, VerdictUnknown)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestTransition_Coverage asserts that every (State, Event) pair either
// has a transition OR returns a clear error. This catches "missing
// transition in the table" bugs.
func TestTransition_Coverage(t *testing.T) {
	for _, s := range AllStates() {
		for _, e := range AllEvents() {
			// For drift_log, try with each valid verdict
			if e == EventDriftLog {
				for _, v := range AllVerdicts() {
					_, err := Transition(s, e, v)
					if err == nil {
						continue // valid transition
					}
					var target ErrInvalidTransition
					if !errors.As(err, &target) {
						t.Errorf("Transition(%s, %s, %s) returned non-ErrInvalidTransition: %v",
							s, e, v, err)
					}
				}
			} else {
				_, err := Transition(s, e, VerdictUnknown)
				if err == nil {
					continue
				}
				var target ErrInvalidTransition
				if !errors.As(err, &target) {
					t.Errorf("Transition(%s, %s, VerdictUnknown) returned non-ErrInvalidTransition: %v",
						s, e, err)
				}
			}
		}
	}
}