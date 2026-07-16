// Tests for atomic spec 2.2 (VLPPackage) — see docs/DMAP_V1_1.md §4 Layer 2.
//
// The acceptance test (TestVLPPackage_PrimitivesReturnTyped) is the ONE
// test that defines "done" for this spec: the compile-time assertion that
// *Package satisfies vlpPrimitives. Other tests cover property invariants:
// brief contents, propose validation, record state advances, complete
// terminal-only, full lifecycle chaining.
package vlp

import "testing"

// TestVLPPackage_PrimitivesReturnTyped is the ACCEPTANCE TEST for spec 2.2.
// The compile-time `var _ vlpPrimitives = (*Package)(nil)` at file level is
// the actual assertion; this test exists to document it and provide a
// runtime sanity check (no nil pointer).
func TestVLPPackage_PrimitivesReturnTyped(t *testing.T) {
	p := NewPackage()
	if p == nil {
		t.Fatal("NewPackage returned nil")
	}

	// Each primitive must return its declared typed output without panic.
	b, err := p.Brief(BriefInput{SessionID: "s1", CurrentState: StateIdle})
	if err != nil {
		t.Fatalf("Brief: unexpected error: %v", err)
	}
	if b.State != StateIdle {
		t.Errorf("Brief.State = %s, want %s", b.State, StateIdle)
	}

	pr, err := p.Propose(ProposeInput{SessionID: "s1", CurrentState: StateDraftingSpec})
	if err != nil {
		t.Fatalf("Propose: unexpected error: %v", err)
	}
	if pr.Approved == nil || pr.Rejected == nil || pr.Redirected == nil {
		t.Errorf("Propose output slices should be non-nil (even if empty): %+v", pr)
	}

	r, err := p.Record(RecordInput{
		SessionID:    "s1",
		CurrentState: StateSpecActive,
		Call:         ToolCall{Name: "dark_memory_artifact_log"},
	})
	if err != nil {
		t.Fatalf("Record: unexpected error: %v", err)
	}
	if r.NextState != StateDriftJudging {
		t.Errorf("Record.NextState = %s, want %s", r.NextState, StateDriftJudging)
	}

	c, err := p.Complete(CompleteInput{SessionID: "s1", CurrentState: StateComplete})
	if err != nil {
		t.Fatalf("Complete: unexpected error: %v", err)
	}
	if c.FinalState != StateComplete {
		t.Errorf("Complete.FinalState = %s, want %s", c.FinalState, StateComplete)
	}
}

// TestBrief_InputValidation asserts Brief rejects invalid inputs.
func TestBrief_InputValidation(t *testing.T) {
	p := NewPackage()
	cases := []struct {
		name string
		in   BriefInput
	}{
		{"empty session_id", BriefInput{CurrentState: StateIdle}},
		{"unknown state", BriefInput{SessionID: "s1", CurrentState: StateUnknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Brief(tc.in)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestBrief_ContextBudgetPerState verifies the per-state budget table.
func TestBrief_ContextBudgetPerState(t *testing.T) {
	p := NewPackage()
	cases := []struct {
		state State
		want  int
	}{
		{StateIdle, 2000},
		{StateDraftingSpec, 3000},
		{StateSpecActive, 4000},
		{StateDriftJudging, 3500},
		{StateComplete, 500},
		{StateNeedsHuman, 1500},
		{StateAborted, 500},
	}
	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			out, err := p.Brief(BriefInput{SessionID: "s1", CurrentState: tc.state})
			if err != nil {
				t.Fatal(err)
			}
			if out.ContextBudget != tc.want {
				t.Errorf("Brief.ContextBudget at %s = %d, want %d", tc.state, out.ContextBudget, tc.want)
			}
		})
	}
}

// TestPropose_ApprovesValidCalls covers the happy-path proposals.
func TestPropose_ApprovesValidCalls(t *testing.T) {
	p := NewPackage()
	cases := []struct {
		name  string
		state State
		calls []ToolCall
	}{
		{"session_start at idle", StateIdle, []ToolCall{{Name: "dark_memory_session_start"}}},
		{"vibe_publish at drafting_spec", StateDraftingSpec, []ToolCall{{Name: "dark_memory_vibe_publish"}}},
		{"artifact_log at spec_active", StateSpecActive, []ToolCall{{Name: "dark_memory_artifact_log"}}},
		{"drift_log(aligned) at drift_judging", StateDriftJudging, []ToolCall{
			{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "aligned"}},
		}},
		{"drift_log(drift_detected) at drift_judging", StateDriftJudging, []ToolCall{
			{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "drift_detected"}},
		}},
		{"drift_log(needs_human) at drift_judging", StateDriftJudging, []ToolCall{
			{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "needs_human"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := p.Propose(ProposeInput{SessionID: "s", CurrentState: tc.state, ProposedCalls: tc.calls})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Approved) != len(tc.calls) {
				t.Errorf("approved = %d, want %d (rejected: %v)", len(out.Approved), len(tc.calls), out.Rejected)
			}
			if len(out.Rejected) != 0 {
				t.Errorf("rejected should be 0, got %d: %v", len(out.Rejected), out.Rejected)
			}
		})
	}
}

// TestPropose_RejectsInvalidCalls covers the rejection paths.
func TestPropose_RejectsInvalidCalls(t *testing.T) {
	p := NewPackage()
	cases := []struct {
		name        string
		state       State
		calls       []ToolCall
		wantReject  int
	}{
		{"vibe_publish at idle (wrong state)", StateIdle, []ToolCall{{Name: "dark_memory_vibe_publish"}}, 1},
		{"artifact_log at idle (wrong state)", StateIdle, []ToolCall{{Name: "dark_memory_artifact_log"}}, 1},
		{"unknown tool at idle", StateIdle, []ToolCall{{Name: "dark_memory_nonsense"}}, 1},
		{"drift_log without verdict param", StateDriftJudging, []ToolCall{{Name: "dark_memory_drift_log"}}, 1},
		{"drift_log with invalid verdict", StateDriftJudging, []ToolCall{
			{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "maybe"}},
		}, 1},
		{"session_start at complete (terminal)", StateComplete, []ToolCall{{Name: "dark_memory_session_start"}}, 1},
		{"any call at aborted", StateAborted, []ToolCall{{Name: "dark_memory_session_start"}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := p.Propose(ProposeInput{SessionID: "s", CurrentState: tc.state, ProposedCalls: tc.calls})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Rejected) != tc.wantReject {
				t.Errorf("rejected = %d, want %d (approved: %v)", len(out.Rejected), tc.wantReject, out.Approved)
			}
			if len(out.Approved) != 0 {
				t.Errorf("approved should be 0 on rejection, got %d", len(out.Approved))
			}
		})
	}
}

// TestRecord_AdvancesState verifies Record transitions correctly via the
// state machine (delegates to spec 2.1).
func TestRecord_AdvancesState(t *testing.T) {
	p := NewPackage()
	cases := []struct {
		name      string
		from      State
		call      ToolCall
		wantState State
		wantAction string
	}{
		{"idle+session_start → drafting_spec", StateIdle,
			ToolCall{Name: "dark_memory_session_start"}, StateDraftingSpec, "vibe_publish"},
		{"drafting_spec+vibe_publish → spec_active", StateDraftingSpec,
			ToolCall{Name: "dark_memory_vibe_publish"}, StateSpecActive, "artifact_log"},
		{"spec_active+artifact_log → drift_judging", StateSpecActive,
			ToolCall{Name: "dark_memory_artifact_log"}, StateDriftJudging, "drift_log"},
		{"drift_judging+drift_log(aligned) → complete", StateDriftJudging,
			ToolCall{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "aligned"}},
			StateComplete, "stop"},
		{"drift_judging+drift_log(drift_detected) → spec_active (loop)", StateDriftJudging,
			ToolCall{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "drift_detected"}},
			StateSpecActive, "artifact_log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := p.Record(RecordInput{SessionID: "s", CurrentState: tc.from, Call: tc.call})
			if err != nil {
				t.Fatal(err)
			}
			if out.NextState != tc.wantState {
				t.Errorf("NextState = %s, want %s", out.NextState, tc.wantState)
			}
			if out.NextAction != tc.wantAction {
				t.Errorf("NextAction = %s, want %s", out.NextAction, tc.wantAction)
			}
		})
	}
}

// TestRecord_RejectsInvalidCalls covers Record's error paths.
func TestRecord_RejectsInvalidCalls(t *testing.T) {
	p := NewPackage()
	cases := []struct {
		name  string
		in    RecordInput
	}{
		{"empty session_id", RecordInput{CurrentState: StateIdle, Call: ToolCall{Name: "dark_memory_session_start"}}},
		{"unknown tool", RecordInput{SessionID: "s", CurrentState: StateIdle, Call: ToolCall{Name: "dark_memory_nonsense"}}},
		{"wrong state transition", RecordInput{
			SessionID: "s", CurrentState: StateIdle, Call: ToolCall{Name: "dark_memory_vibe_publish"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Record(tc.in)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestRecord_NextActionMapping verifies nextActionFor returns the right
// next-action hint for every state.
func TestRecord_NextActionMapping(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{StateIdle, "session_start"},
		{StateDraftingSpec, "vibe_publish"},
		{StateSpecActive, "artifact_log"},
		{StateDriftJudging, "drift_log"},
		{StateComplete, "stop"},
		{StateNeedsHuman, "stop"},
		{StateAborted, "stop"},
	}
	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			if got := nextActionFor(tc.state); got != tc.want {
				t.Errorf("nextActionFor(%s) = %s, want %s", tc.state, got, tc.want)
			}
		})
	}
}

// TestComplete_RequiresTerminalState verifies Complete only succeeds in
// terminal states (Complete, NeedsHuman, Aborted).
func TestComplete_RequiresTerminalState(t *testing.T) {
	p := NewPackage()
	nonTerminal := []State{StateIdle, StateDraftingSpec, StateSpecActive, StateDriftJudging}
	for _, s := range nonTerminal {
		t.Run("reject "+s.String(), func(t *testing.T) {
			_, err := p.Complete(CompleteInput{SessionID: "s", CurrentState: s})
			if err == nil {
				t.Errorf("Complete(%s) should error", s)
			}
		})
	}
	terminal := []State{StateComplete, StateNeedsHuman, StateAborted}
	for _, s := range terminal {
		t.Run("accept "+s.String(), func(t *testing.T) {
			out, err := p.Complete(CompleteInput{SessionID: "s", CurrentState: s})
			if err != nil {
				t.Errorf("Complete(%s) should succeed: %v", s, err)
				return
			}
			if out.FinalState != s {
				t.Errorf("FinalState = %s, want %s", out.FinalState, s)
			}
		})
	}
}

// TestComplete_SummaryIncludes verifies the summary map includes the
// final_state plus any caller-supplied metrics.
func TestComplete_SummaryIncludes(t *testing.T) {
	p := NewPackage()
	out, err := p.Complete(CompleteInput{
		SessionID:    "s1",
		CurrentState: StateComplete,
		Metrics: map[string]int{
			"specs_executed":     1,
			"artifacts_published": 1,
			"drifts_resolved":    1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary["final_state"] != "complete" {
		t.Errorf("Summary[final_state] = %v, want \"complete\"", out.Summary["final_state"])
	}
	for _, k := range []string{"specs_executed", "artifacts_published", "drifts_resolved"} {
		if out.Summary[k] != 1 {
			t.Errorf("Summary[%s] = %v, want 1", k, out.Summary[k])
		}
	}
	if out.Handoff == "" {
		t.Error("Handoff should be non-empty")
	}
}

// TestComplete_InputValidation covers Complete's input validation.
func TestComplete_InputValidation(t *testing.T) {
	p := NewPackage()
	_, err := p.Complete(CompleteInput{CurrentState: StateComplete})
	if err == nil {
		t.Error("Complete without SessionID should error")
	}
}

// TestPackage_FullLifecycle walks through a complete happy-path workflow:
// idle → drafting_spec → spec_active → drift_judging → complete.
func TestPackage_FullLifecycle(t *testing.T) {
	p := NewPackage()
	sessionID := "lifecycle-1"

	// 1. Brief at idle
	b, err := p.Brief(BriefInput{SessionID: sessionID, CurrentState: StateIdle, Intent: "test CVE-2026-XXXXX"})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if b.State != StateIdle || b.ContextBudget != 2000 {
		t.Errorf("Brief at idle: %+v", b)
	}

	// 2. Propose session_start → approved
	pr, err := p.Propose(ProposeInput{
		SessionID: sessionID, CurrentState: StateIdle,
		ProposedCalls: []ToolCall{{Name: "dark_memory_session_start"}},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(pr.Approved) != 1 {
		t.Fatalf("session_start not approved: %+v", pr)
	}

	// 3. Record session_start → drafting_spec
	r, err := p.Record(RecordInput{
		SessionID: sessionID, CurrentState: StateIdle,
		Call: ToolCall{Name: "dark_memory_session_start"},
	})
	if err != nil {
		t.Fatalf("Record session_start: %v", err)
	}
	if r.NextState != StateDraftingSpec {
		t.Fatalf("Record.NextState = %s, want drafting_spec", r.NextState)
	}

	// 4. Propose vibe_publish at drafting_spec → approved
	pr, _ = p.Propose(ProposeInput{
		SessionID: sessionID, CurrentState: StateDraftingSpec,
		ProposedCalls: []ToolCall{{Name: "dark_memory_vibe_publish"}},
	})
	if len(pr.Approved) != 1 {
		t.Fatalf("vibe_publish not approved: %+v", pr)
	}

	// 5. Record vibe_publish → spec_active
	r, _ = p.Record(RecordInput{
		SessionID: sessionID, CurrentState: StateDraftingSpec,
		Call: ToolCall{Name: "dark_memory_vibe_publish"},
	})
	if r.NextState != StateSpecActive {
		t.Fatalf("Record.NextState = %s, want spec_active", r.NextState)
	}

	// 6. Propose + Record artifact_log → drift_judging
	pr, _ = p.Propose(ProposeInput{
		SessionID: sessionID, CurrentState: StateSpecActive,
		ProposedCalls: []ToolCall{{Name: "dark_memory_artifact_log"}},
	})
	if len(pr.Approved) != 1 {
		t.Fatalf("artifact_log not approved: %+v", pr)
	}
	r, _ = p.Record(RecordInput{
		SessionID: sessionID, CurrentState: StateSpecActive,
		Call: ToolCall{Name: "dark_memory_artifact_log"},
	})
	if r.NextState != StateDriftJudging {
		t.Fatalf("Record.NextState = %s, want drift_judging", r.NextState)
	}

	// 7. Propose + Record drift_log(aligned) → complete
	pr, _ = p.Propose(ProposeInput{
		SessionID: sessionID, CurrentState: StateDriftJudging,
		ProposedCalls: []ToolCall{
			{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "aligned"}},
		},
	})
	if len(pr.Approved) != 1 {
		t.Fatalf("drift_log(aligned) not approved: %+v", pr)
	}
	r, _ = p.Record(RecordInput{
		SessionID: sessionID, CurrentState: StateDriftJudging,
		Call: ToolCall{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "aligned"}},
	})
	if r.NextState != StateComplete {
		t.Fatalf("Record.NextState = %s, want complete", r.NextState)
	}

	// 8. Complete at terminal state
	c, err := p.Complete(CompleteInput{
		SessionID: sessionID, CurrentState: StateComplete,
		Metrics: map[string]int{"specs_executed": 1, "artifacts_published": 1, "drifts_resolved": 1},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if c.FinalState != StateComplete {
		t.Errorf("FinalState = %s, want complete", c.FinalState)
	}
	if c.Summary["specs_executed"] != 1 {
		t.Errorf("Summary[specs_executed] = %v, want 1", c.Summary["specs_executed"])
	}
}

// TestPackage_DriftLoop exercises the drift_detected → spec_active loop.
func TestPackage_DriftLoop(t *testing.T) {
	p := NewPackage()
	// Get to drift_judging
	r, _ := p.Record(RecordInput{SessionID: "s", CurrentState: StateIdle, Call: ToolCall{Name: "dark_memory_session_start"}})
	r, _ = p.Record(RecordInput{SessionID: "s", CurrentState: r.NextState, Call: ToolCall{Name: "dark_memory_vibe_publish"}})
	r, _ = p.Record(RecordInput{SessionID: "s", CurrentState: r.NextState, Call: ToolCall{Name: "dark_memory_artifact_log"}})
	if r.NextState != StateDriftJudging {
		t.Fatalf("setup: expected drift_judging, got %s", r.NextState)
	}

	// Record drift_detected → spec_active (loop back)
	r, _ = p.Record(RecordInput{
		SessionID: "s", CurrentState: StateDriftJudging,
		Call: ToolCall{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "drift_detected"}},
	})
	if r.NextState != StateSpecActive {
		t.Errorf("drift_detected loop: NextState = %s, want spec_active", r.NextState)
	}

	// And can recover: artifact_log again → drift_judging, then aligned → complete
	r, _ = p.Record(RecordInput{
		SessionID: "s", CurrentState: StateSpecActive,
		Call: ToolCall{Name: "dark_memory_artifact_log"},
	})
	if r.NextState != StateDriftJudging {
		t.Errorf("after drift recovery: NextState = %s, want drift_judging", r.NextState)
	}
	r, _ = p.Record(RecordInput{
		SessionID: "s", CurrentState: StateDriftJudging,
		Call: ToolCall{Name: "dark_memory_drift_log", Params: map[string]interface{}{"verdict": "aligned"}},
	})
	if r.NextState != StateComplete {
		t.Errorf("final aligned: NextState = %s, want complete", r.NextState)
	}
}

// TestPackage_Abort verifies abort from any non-terminal state.
func TestPackage_Abort(t *testing.T) {
	p := NewPackage()
	cases := []State{StateIdle, StateDraftingSpec, StateSpecActive, StateDriftJudging}
	for _, s := range cases {
		t.Run(s.String(), func(t *testing.T) {
			r, err := p.Record(RecordInput{
				SessionID: "s", CurrentState: s,
				Call: ToolCall{Name: "dark_memory_session_abort"},
			})
			if err != nil {
				t.Fatalf("Record(session_abort) at %s: unexpected error: %v", s, err)
			}
			if r.NextState != StateAborted {
				t.Errorf("NextState = %s, want aborted", r.NextState)
			}
			if r.NextAction != "stop" {
				t.Errorf("NextAction = %s, want stop", r.NextAction)
			}
		})
	}
}

// TestEventForCall_DriftLog verifies verdict extraction from params. Also
// exercises the unknown-tool path (single test covering both branches keeps
// the file lean without losing coverage).
func TestEventForCall(t *testing.T) {
	t.Run("unknown_tool", func(t *testing.T) {
		_, _, err := eventForCall(ToolCall{Name: "totally_made_up"})
		if err == nil {
			t.Fatal("expected error for unknown tool")
		}
	})

	t.Run("drift_log_verdict_extraction", func(t *testing.T) {
		cases := []struct {
			name    string
			params  map[string]interface{}
			wantErr bool
			want    Verdict
		}{
			{"aligned", map[string]interface{}{"verdict": "aligned"}, false, VerdictAligned},
			{"drift_detected", map[string]interface{}{"verdict": "drift_detected"}, false, VerdictDriftDetected},
			{"needs_human", map[string]interface{}{"verdict": "needs_human"}, false, VerdictNeedsHuman},
			{"missing_verdict", map[string]interface{}{}, true, VerdictUnknown},
			{"wrong_type", map[string]interface{}{"verdict": 42}, true, VerdictUnknown},
			{"invalid_value", map[string]interface{}{"verdict": "maybe"}, true, VerdictUnknown},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				event, verdict, err := eventForCall(ToolCall{Name: "dark_memory_drift_log", Params: tc.params})
				if tc.wantErr {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if event != EventDriftLog {
					t.Errorf("event = %s, want drift_log", event)
				}
				if verdict != tc.want {
					t.Errorf("verdict = %s, want %s", verdict, tc.want)
				}
			})
		}
	})
}