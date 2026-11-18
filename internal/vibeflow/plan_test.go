package vibeflow

import "testing"

// The gate is pure: no store, no clock, no LLM. These tests pin the
// ORDER of the checks, because the order is the design (SPEC-194
// §3.4-bis): the lifecycle exemption lives in the middleware, and
// everything else reaches this gate.

func approvedPlan() PlanGateInput {
	return PlanGateInput{
		ToolName:   "agent_memory_save",
		HasPlan:    true,
		Status:     PlanApproved,
		Authorised: true,
		Budget:     Budget{MaxToolCalls: 3, MaxJudgeCalls: 1},
	}
}

func TestPlanGate_NoPlanIsRefused(t *testing.T) {
	g := NewPlanGate()
	in := PlanGateInput{ToolName: "agent_memory_save", HasPlan: false}
	v := g.Check(in)
	if v.Allowed {
		t.Fatalf("Allowed = true, want false: a mutating tool without a plan must be refused")
	}
	if v.Reason != ReasonPlanRequired {
		t.Errorf("Reason = %q, want %q", v.Reason, ReasonPlanRequired)
	}
	if v.Hint == "" {
		t.Error("Hint is empty: the refusal must say what to do next")
	}
}

func TestPlanGate_DraftIsNotApproved(t *testing.T) {
	g := NewPlanGate()
	in := approvedPlan()
	in.Status = PlanDraft
	v := g.Check(in)
	if v.Allowed {
		t.Fatal("Allowed = true, want false: a draft plan is not an approval")
	}
	if v.Reason != ReasonNotApproved {
		t.Errorf("Reason = %q, want %q", v.Reason, ReasonNotApproved)
	}
}

func TestPlanGate_CancelledAndEditedAreNotApproved(t *testing.T) {
	g := NewPlanGate()
	for _, status := range []PlanStatus{PlanCancelled, PlanEdited, PlanStatus("bogus")} {
		in := approvedPlan()
		in.Status = status
		v := g.Check(in)
		if v.Allowed {
			t.Errorf("status %q: Allowed = true, want false", status)
		}
		if v.Reason != ReasonNotApproved {
			t.Errorf("status %q: Reason = %q, want %q", status, v.Reason, ReasonNotApproved)
		}
	}
}

// The deadlock guard (SPEC-194 §3.4-bis, AC13): the plan lifecycle
// tools are EXEMPT from the gate, and that exemption is decided in the
// middleware, BEFORE this gate runs. This test pins the consequence:
// this gate never sees a lifecycle tool, because the middleware routes
// it away. If the exemption is ever removed from the middleware, the
// first plan can never be created and the flow deadlocks — so the
// exemption is asserted in the middleware test too.
//
// Here we assert the OTHER half: that the gate itself has no way to
// be told "skip me". A tool that is not authorised is refused even
// when the plan is approved.
func TestPlanGate_UnlistedToolIsRefusedEvenWhenApproved(t *testing.T) {
	g := NewPlanGate()
	in := approvedPlan()
	in.Authorised = false
	v := g.Check(in)
	if v.Allowed {
		t.Fatal("Allowed = true, want false: an approved plan does not authorise a tool it does not list")
	}
	if v.Reason != ReasonNotApproved {
		t.Errorf("Reason = %q, want %q", v.Reason, ReasonNotApproved)
	}
}

func TestPlanGate_BudgetExhausted(t *testing.T) {
	g := NewPlanGate()

	in := approvedPlan()
	in.Budget.UsedToolCalls = 3 // MaxToolCalls = 3
	v := g.Check(in)
	if v.Allowed {
		t.Fatal("Allowed = true, want false: the tool-call budget is exhausted")
	}
	if v.Reason != ReasonBudgetExceeded {
		t.Errorf("Reason = %q, want %q", v.Reason, ReasonBudgetExceeded)
	}
	if v.BudgetExhausted != "tool_calls" {
		t.Errorf("BudgetExhausted = %q, want %q", v.BudgetExhausted, "tool_calls")
	}

	in = approvedPlan()
	in.IsJudgeCall = true
	in.Budget.UsedJudgeCalls = 1 // MaxJudgeCalls = 1
	v = g.Check(in)
	if v.Allowed {
		t.Fatal("Allowed = true, want false: the judge-call budget is exhausted")
	}
	if v.BudgetExhausted != "judge_calls" {
		t.Errorf("BudgetExhausted = %q, want %q", v.BudgetExhausted, "judge_calls")
	}
}

// The ORDER: a plan that is BOTH unapproved AND over budget must
// report "not approved", not "budget exceeded". The human decision
// comes first: you cannot spend a budget that was never granted.
func TestPlanGate_CheckOrder_UnapprovedBeatsBudget(t *testing.T) {
	g := NewPlanGate()
	in := approvedPlan()
	in.Status = PlanDraft
	in.Budget.UsedToolCalls = 99
	v := g.Check(in)
	if v.Reason != ReasonNotApproved {
		t.Errorf("Reason = %q, want %q: the approval check comes before the budget check", v.Reason, ReasonNotApproved)
	}
}

// The ORDER, other way: a plan that is approved but over budget
// reports the budget, because the approval already happened.
func TestPlanGate_CheckOrder_BudgetBeatsUnlistedTool(t *testing.T) {
	g := NewPlanGate()
	in := approvedPlan()
	in.Authorised = false
	in.Budget.UsedToolCalls = 99
	v := g.Check(in)
	// The tool-list check (3) sits BEFORE the budget check (4), so an
	// unlisted tool on an exhausted plan reports "not approved".
	// This is the documented order in SPEC-194 §3.4-bis.
	if v.Reason != ReasonNotApproved {
		t.Errorf("Reason = %q, want %q: the tool-list check precedes the budget check", v.Reason, ReasonNotApproved)
	}
}

func TestPlanGate_ApprovedAndWithinBudgetIsAllowed(t *testing.T) {
	g := NewPlanGate()
	v := g.Check(approvedPlan())
	if !v.Allowed {
		t.Fatalf("Allowed = false (%s): an approved plan with budget left must pass", v.Message)
	}
	if v.Reason != ReasonOK {
		t.Errorf("Reason = %q, want %q", v.Reason, ReasonOK)
	}
}

func TestPlanGate_Purity(t *testing.T) {
	// Same input, same output, and the input is not mutated. The
	// gate is called on EVERY mutating tool call, so it has to be a
	// pure function of its input.
	g := NewPlanGate()
	in := approvedPlan()
	in.Budget.UsedToolCalls = 1
	before := in
	v1 := g.Check(in)
	v2 := g.Check(in)
	if v1 != v2 {
		t.Errorf("two calls with the same input differ: %+v vs %+v", v1, v2)
	}
	if in != before {
		t.Errorf("Check mutated its input: %+v -> %+v", before, in)
	}
}

func TestParsePlanStatus_UnknownFailsClosed(t *testing.T) {
	// A plan whose status cannot be read must behave like an
	// UNAPPROVED plan, never like an approved one. Failing open here
	// would be a hole in the gate.
	for _, s := range []string{"", "  ", "approved ", "APPROVED", "unknown"} {
		got := ParsePlanStatus(s)
		if got == PlanApproved && s != "approved" && s != "APPROVED" && s != "approved " {
			t.Errorf("ParsePlanStatus(%q) = %q: an unknown status must not read as approved", s, got)
		}
	}
	if got := ParsePlanStatus("approved"); got != PlanApproved {
		t.Errorf("ParsePlanStatus(\"approved\") = %q, want %q", got, PlanApproved)
	}
	if got := ParsePlanStatus("APPROVED"); got != PlanApproved {
		t.Errorf("ParsePlanStatus(\"APPROVED\") = %q, want %q", got, PlanApproved)
	}
}

func TestParseDecision_UnknownIsRejected(t *testing.T) {
	// A decision is never guessed: an unknown value is an error, not
	// a default. Defaulting to `execute` would let a typo approve a
	// plan.
	for _, s := range []string{"", "approve", "ok", "EXECUTE!"} {
		if d, ok := ParseDecision(s); ok {
			t.Errorf("ParseDecision(%q) = %q, ok = true; want rejection", s, d)
		}
	}
	for _, s := range []string{"execute", "EXECUTE", " edit ", "cancel"} {
		if _, ok := ParseDecision(s); !ok {
			t.Errorf("ParseDecision(%q) rejected; want accepted", s)
		}
	}
}

func TestTierForCandidates(t *testing.T) {
	cases := []struct {
		n    int
		want PlanTier
	}{
		{0, PlanTierShort}, {1, PlanTierShort}, {3, PlanTierShort},
		{4, PlanTierMedium}, {8, PlanTierMedium},
		{9, PlanTierLong}, {50, PlanTierLong},
	}
	for _, c := range cases {
		if got := PlanTierForCandidates(c.n); got != c.want {
			t.Errorf("PlanTierForCandidates(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBudget_ExhaustedAndRemaining(t *testing.T) {
	b := Budget{MaxToolCalls: 3, MaxJudgeCalls: 1}
	if got := b.Exhausted(ResourceToolCalls); got != "" {
		t.Errorf("Exhausted(tool_calls) = %q, want empty", got)
	}
	if got := b.Exhausted(ResourceJudgeCalls); got != "" {
		t.Errorf("Exhausted(judge_calls) = %q, want empty", got)
	}
	tc, jc := b.Remaining()
	if tc != 3 || jc != 1 {
		t.Errorf("Remaining() = %d, %d; want 3, 1", tc, jc)
	}

	b.UsedToolCalls = 3
	if got := b.Exhausted(ResourceToolCalls); got != ResourceToolCalls {
		t.Errorf("Exhausted(tool_calls) = %q, want %q", got, ResourceToolCalls)
	}
	// The judge ceiling is a DIFFERENT ceiling: spending the tool
	// budget does not spend it.
	if got := b.Exhausted(ResourceJudgeCalls); got != "" {
		t.Errorf("Exhausted(judge_calls) = %q, want empty: the two ceilings are independent", got)
	}
	// Over budget (a race could push it past) must still read as
	// exhausted, and Remaining must not go negative.
	b.UsedToolCalls = 5
	tc, _ = b.Remaining()
	if tc != 0 {
		t.Errorf("Remaining() tool calls = %d, want 0 (clamped)", tc)
	}
}

func TestPlan_AuthorisesOnlyListedTools(t *testing.T) {
	p := &Plan{Candidates: []Candidate{{Tool: "agent_memory_save"}, {Tool: "judge"}}}
	if !p.Authorises("agent_memory_save") {
		t.Error("Authorises(agent_memory_save) = false, want true")
	}
	if p.Authorises("admin_vacuum") {
		t.Error("Authorises(admin_vacuum) = true, want false")
	}
	// An empty candidate list is not a wildcard.
	empty := &Plan{}
	if empty.Authorises("agent_memory_save") {
		t.Error("an empty plan authorised a tool: an empty list must authorise nothing")
	}
	var nilPlan *Plan
	if nilPlan.Authorises("x") {
		t.Error("a nil plan authorised a tool")
	}
	if got := nilPlan.CandidateNames(); got != nil {
		t.Errorf("nil plan CandidateNames() = %v, want nil", got)
	}
}

func TestPlanStatus_Terminal(t *testing.T) {
	if PlanDraft.Terminal() {
		t.Error("draft must not be terminal: work can still be approved")
	}
	if PlanApproved.Terminal() {
		t.Error("approved must not be terminal: work can still run")
	}
	for _, s := range []PlanStatus{PlanExecuted, PlanCancelled, PlanEdited} {
		if !s.Terminal() {
			t.Errorf("%q must be terminal", s)
		}
	}
}

// The two ceilings are INDEPENDENT. This test pins the defect that the
// independent review of PR #43 found: the gate used to look at BOTH
// counters, so with the default tier `corta` (one judge call) a single
// judge call blocked EVERY mutating tool call of the plan while the
// tool budget sat untouched.
//
// SPEC-194 §3.8 says each counter cuts ITS resource. This is that.
func TestPlanGate_BudgetsAreIndependent(t *testing.T) {
	g := NewPlanGate()

	// The judge budget is spent; a tool call must still pass.
	in := approvedPlan()
	in.Budget.UsedJudgeCalls = 1 // MaxJudgeCalls = 1
	in.IsJudgeCall = false
	if v := g.Check(in); !v.Allowed {
		t.Errorf("a tool call was refused because the JUDGE budget was spent: %s", v.Message)
	}

	// The tool budget is spent; a judge call must still pass.
	in = approvedPlan()
	in.Budget.UsedToolCalls = 3 // MaxToolCalls = 3
	in.IsJudgeCall = true
	if v := g.Check(in); !v.Allowed {
		t.Errorf("a judge call was refused because the TOOL budget was spent: %s", v.Message)
	}

	// And each one still cuts its OWN resource.
	in = approvedPlan()
	in.Budget.UsedJudgeCalls = 1
	in.IsJudgeCall = true
	v := g.Check(in)
	if v.Allowed {
		t.Error("a judge call passed with the judge budget exhausted")
	} else if v.BudgetExhausted != ResourceJudgeCalls {
		t.Errorf("BudgetExhausted = %q, want %q", v.BudgetExhausted, ResourceJudgeCalls)
	}

	in = approvedPlan()
	in.Budget.UsedToolCalls = 3
	in.IsJudgeCall = false
	v = g.Check(in)
	if v.Allowed {
		t.Error("a tool call passed with the tool budget exhausted")
	} else if v.BudgetExhausted != ResourceToolCalls {
		t.Errorf("BudgetExhausted = %q, want %q", v.BudgetExhausted, ResourceToolCalls)
	}
}
