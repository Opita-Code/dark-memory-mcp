package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// fakePlanStore is an in-memory PlanStore. It exists so the gate can
// be tested WITHOUT a database: the gate is the thing under test, not
// the SQL (the SQL is tested in internal/store/sqlite).
type fakePlanStore struct {
	row *vibeflow.PlanRow

	// getErr, when set, is returned by GetOpenPlan.
	getErr error

	// chargeErr, when set, is returned by ChargePlanCall.
	chargeErr error

	// charges counts the calls to ChargePlanCall.
	charges int

	// judgeCharges counts the calls that charged the judge budget.
	judgeCharges int

	// refusals records the audit rows of the refusals.
	refusals []audit.WriteEvent

	// recordErr, when set, is returned by RecordWrite.
	recordErr error
}

// RecordWrite records the trail of a refusal.
func (f *fakePlanStore) RecordWrite(ctx context.Context, ev audit.WriteEvent) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.refusals = append(f.refusals, ev)
	return nil
}

func (f *fakePlanStore) GetOpenPlan(ctx context.Context, sessionID string) (*vibeflow.PlanRow, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.row, nil
}

func (f *fakePlanStore) ChargePlanCall(ctx context.Context, wc store.WriteContext, specID int64, judgeCall bool) (*vibeflow.PlanRow, error) {
	f.charges++
	if judgeCall {
		f.judgeCharges++
	}
	if f.chargeErr != nil {
		return nil, f.chargeErr
	}
	// The charge moves the counter of the resource being spent, and
	// the row comes back with the new value (the store reads it back
	// AFTER the charge).
	out := *f.row
	out.BudgetUsed = f.row.BudgetUsed
	out.JudgeUsed = f.row.JudgeUsed
	if judgeCall {
		out.JudgeUsed = f.row.JudgeUsed + 1
	} else {
		out.BudgetUsed = f.row.BudgetUsed + 1
	}
	return &out, nil
}

// anApprovedPlan returns a row with an approved plan that lists the
// given tools, with the given ceilings.
func anApprovedPlan(tools []string, maxTool, maxJudge int) *vibeflow.PlanRow {
	candidates := make([]vibeflow.Candidate, 0, len(tools))
	for _, t := range tools {
		candidates = append(candidates, vibeflow.Candidate{Tool: t})
	}
	return &vibeflow.PlanRow{
		SpecID: 42,
		Status: vibeflow.PlanApproved,
		Plan: vibeflow.Plan{
			Intent:     "do the thing",
			Candidates: candidates,
			Budget: vibeflow.Budget{
				MaxToolCalls:  maxTool,
				MaxJudgeCalls: maxJudge,
			},
		},
		BudgetUsed: 0,
		JudgeUsed:  0,
	}
}

// A call that the gate REFUSES must not charge anything. If it did,
// a refused call would consume the allowance of the calls that were
// allowed, and the budget would stop meaning "what was authorised".
func TestGate_RefusedCallDoesNotCharge(t *testing.T) {
	cases := []struct {
		name string
		row  *vibeflow.PlanRow
		tool string
	}{
		{"no plan at all", nil, "agent_memory_save"},
		{"the plan is draft", func() *vibeflow.PlanRow {
			r := anApprovedPlan([]string{"agent_memory_save"}, 3, 1)
			r.Status = vibeflow.PlanDraft
			return r
		}(), "agent_memory_save"},
		{"the plan does not list the tool", anApprovedPlan([]string{"agent_memory_save"}, 3, 1), "vibe_publish"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &fakePlanStore{row: c.row}
			g := NewGate(st)

			d, err := g.Check(context.Background(), "sess-1", c.tool, false, store.WriteContext{})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if d.Allowed {
				t.Fatal("the gate allowed a call it should have refused")
			}
			if st.charges != 0 {
				t.Errorf("the refused call charged %d times: a refusal must not consume budget", st.charges)
			}
		})
	}
}

// A call the gate ALLOWS charges exactly once, and only the counter
// of the resource being spent.
func TestGate_AllowedCallChargesOnce(t *testing.T) {
	st := &fakePlanStore{row: anApprovedPlan([]string{"agent_memory_save"}, 3, 1)}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("the gate refused an allowed call: %s (%s)", d.Message, d.Reason)
	}
	if st.charges != 1 {
		t.Errorf("charges = %d, want 1: a call must be charged exactly once", st.charges)
	}
	if st.judgeCharges != 0 {
		t.Errorf("judgeCharges = %d, want 0: a tool call does not spend the judge budget", st.judgeCharges)
	}
}

// The two budgets are independent, and the gate charges the RIGHT
// one. This is the defect the independent review of PR #43 found on
// the pure gate: a version that looked at both counters refused a
// tool call because the JUDGE budget was spent.
func TestGate_JudgeCallChargesTheJudgeBudget(t *testing.T) {
	st := &fakePlanStore{row: anApprovedPlan([]string{"judge"}, 3, 1)}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "judge", true, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("the gate refused a judge call: %s (%s)", d.Message, d.Reason)
	}
	if st.judgeCharges != 1 {
		t.Errorf("judgeCharges = %d, want 1", st.judgeCharges)
	}
	if st.charges-st.judgeCharges != 0 {
		t.Errorf("a judge call also charged the TOOL budget: charges=%d judgeCharges=%d",
			st.charges, st.judgeCharges)
	}
}

// The budget of the TOOL resource spent must cut the call, and the
// refusal has to name the resource.
func TestGate_ExhaustedBudgetRefusesAndNamesTheResource(t *testing.T) {
	row := anApprovedPlan([]string{"agent_memory_save"}, 3, 1)
	row.BudgetUsed = 3 // the tool ceiling is reached
	st := &fakePlanStore{row: row}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatal("the gate allowed a call with the tool budget exhausted")
	}
	if d.Reason != vibeflow.ReasonBudgetExceeded {
		t.Errorf("reason = %q, want %q", d.Reason, vibeflow.ReasonBudgetExceeded)
	}
	if d.BudgetExhausted != vibeflow.ResourceToolCalls {
		t.Errorf("budget_exhausted = %q, want %q", d.BudgetExhausted, vibeflow.ResourceToolCalls)
	}
	if st.charges != 0 {
		t.Errorf("a refused call charged %d times", st.charges)
	}
}

// The judge budget spent does NOT refuse a tool call. This is the
// crossing the test of PR #43 pinned on the pure gate, and it has to
// hold on the bridge too.
func TestGate_JudgeBudgetSpentDoesNotRefuseAToolCall(t *testing.T) {
	row := anApprovedPlan([]string{"agent_memory_save"}, 3, 1)
	row.JudgeUsed = 1 // the judge ceiling is reached, the tool one is not
	st := &fakePlanStore{row: row}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("a tool call was refused because the JUDGE budget was spent: %s", d.Message)
	}
}

// And the mirror: the tool budget spent does not refuse a judge call.
func TestGate_ToolBudgetSpentDoesNotRefuseAJudgeCall(t *testing.T) {
	row := anApprovedPlan([]string{"judge"}, 3, 1)
	row.BudgetUsed = 3 // the tool ceiling is reached, the judge one is not
	st := &fakePlanStore{row: row}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "judge", true, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("a judge call was refused because the TOOL budget was spent: %s", d.Message)
	}
}

// The counters the gate reads are the COLUMNS, not the JSON copy of
// the plan. Reading the JSON would always report zero spent and the
// budget would never cut: that is a real bug this test pins.
func TestGate_ReadsTheColumnCountersNotTheJSONCopy(t *testing.T) {
	row := anApprovedPlan([]string{"agent_memory_save"}, 3, 1)
	// The JSON copy says nothing was spent...
	row.Plan.Budget.UsedToolCalls = 0
	row.Plan.Budget.UsedJudgeCalls = 0
	// ...but the column says the tool ceiling is reached.
	row.BudgetUsed = 3

	st := &fakePlanStore{row: row}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatal("the gate read the JSON copy of the budget and ignored the column")
	}
	if d.Reason != vibeflow.ReasonBudgetExceeded {
		t.Errorf("reason = %q, want %q", d.Reason, vibeflow.ReasonBudgetExceeded)
	}
}

// The race between the check and the charge: the check sees budget,
// and the charge finds the ceiling reached by a concurrent call. That
// is a REFUSAL with its own reason, not an error.
func TestGate_ConcurrentExhaustionIsARefusalNotAnError(t *testing.T) {
	st := &fakePlanStore{
		row: anApprovedPlan([]string{"agent_memory_save"}, 3, 1),
		chargeErr: store.NewFieldError(store.ErrBudgetExceeded,
			"plan: the plan is no longer approved or no longer has budget"),
	}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check returned an error instead of a refusal: %v", err)
	}
	if d.Allowed {
		t.Fatal("the gate allowed a call whose charge was refused")
	}
	if d.Reason != vibeflow.ReasonBudgetExceeded {
		t.Errorf("reason = %q, want %q", d.Reason, vibeflow.ReasonBudgetExceeded)
	}
}

// A broken store is an ERROR, not a refusal: the caller has to know
// the difference between "you may not do this" and "I could not find
// out".
func TestGate_StoreErrorIsAnErrorNotARefusal(t *testing.T) {
	st := &fakePlanStore{getErr: errors.New("the disk is on fire")}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err == nil {
		t.Fatalf("Check returned no error and no refusal: %+v", d)
	}
	if d != nil {
		t.Errorf("Check returned a decision AND an error: %+v", d)
	}
}

// A gate with no store is not wired: it says nothing, and the caller
// decides what that means (the middleware treats it as legacy
// behaviour).
func TestGate_NoStoreIsNotWired(t *testing.T) {
	var g *Gate
	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d != nil {
		t.Errorf("Check = %+v, want nil (not wired)", d)
	}
}

// A refusal leaves a trail in write_audit with its OWN write_path.
//
// Two reasons this matters:
//
//  1. The row describes an ATTEMPT, not a write, so it must never be
//     confused with the row of a tool that really wrote something.
//     The write_path is what tells them apart.
//  2. The KPI of SPEC-194 §6.2 ("negatives by reason") is a GROUP BY
//     over write_path + the reason in the notes. Without its own
//     write_path, the negatives would be mixed with the charges.
func TestGate_RefusalLeavesATrailWithItsOwnWritePath(t *testing.T) {
	st := &fakePlanStore{row: nil} // no plan at all
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false,
		store.WriteContext{SessionID: "sess-1", ProjectID: "default"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatal("the gate allowed a call without a plan")
	}
	if len(st.refusals) != 1 {
		t.Fatalf("recorded refusals = %d, want 1", len(st.refusals))
	}
	ev := st.refusals[0]
	if ev.WritePath != WritePathRefusal {
		t.Errorf("write_path = %q, want %q", ev.WritePath, WritePathRefusal)
	}
	if ev.Actor != "agent_memory_save" {
		t.Errorf("actor = %q, want the tool name", ev.Actor)
	}
	if ev.SessionID != "sess-1" {
		t.Errorf("session_id = %q, want sess-1", ev.SessionID)
	}
	// The reason has to be in the notes, because the KPI counts by
	// reason and the write_path is the same for the three of them.
	if !strings.Contains(ev.Notes, vibeflow.ReasonPlanRequired) {
		t.Errorf("notes %q do not carry the reason", ev.Notes)
	}
	// With no plan there is no spec to point at: row_id 0 says
	// exactly that, instead of pointing at somebody else's row.
	if ev.RowID != 0 {
		t.Errorf("row_id = %d, want 0 (there is no spec to point at)", ev.RowID)
	}
}

// The trail of a refusal does not change the decision: a broken audit
// must not become a different answer.
func TestGate_ABrokenTrailDoesNotChangeTheDecision(t *testing.T) {
	st := &fakePlanStore{row: nil, recordErr: errors.New("the disk is on fire")}
	g := NewGate(st)

	d, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{})
	if err != nil {
		t.Fatalf("Check returned an error because the trail failed: %v", err)
	}
	if d.Allowed {
		t.Fatal("the gate allowed a call without a plan")
	}
	if d.Reason != vibeflow.ReasonPlanRequired {
		t.Errorf("reason = %q, want %q", d.Reason, vibeflow.ReasonPlanRequired)
	}
}

// An ALLOWED call does NOT leave a refusal row: the trail of the
// charge is a different row (the one the store writes inside the
// transaction of the charge).
func TestGate_AllowedCallLeavesNoRefusalRow(t *testing.T) {
	st := &fakePlanStore{row: anApprovedPlan([]string{"agent_memory_save"}, 3, 1)}
	g := NewGate(st)

	if _, err := g.Check(context.Background(), "sess-1", "agent_memory_save", false, store.WriteContext{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(st.refusals) != 0 {
		t.Errorf("recorded refusals = %d, want 0 for an allowed call", len(st.refusals))
	}
}
