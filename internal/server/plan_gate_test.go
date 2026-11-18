package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/plan"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// fakePlanStore is an in-memory plan.PlanStore for the middleware
// tests: the middleware only needs to know what the plan says, not
// how it is stored.
type fakePlanStore struct {
	row *vibeflow.PlanRow

	charges int
	judge   int

	// refusals records the audit rows of the refusals.
	refusals []audit.WriteEvent
}

func (f *fakePlanStore) GetOpenPlan(ctx context.Context, sessionID string) (*vibeflow.PlanRow, error) {
	return f.row, nil
}

func (f *fakePlanStore) RecordWrite(ctx context.Context, ev audit.WriteEvent) error {
	f.refusals = append(f.refusals, ev)
	return nil
}

func (f *fakePlanStore) ChargePlanCall(ctx context.Context, wc store.WriteContext, specID int64, judgeCall bool) (*vibeflow.PlanRow, error) {
	f.charges++
	if judgeCall {
		f.judge++
	}
	out := *f.row
	if judgeCall {
		out.JudgeUsed = f.row.JudgeUsed + 1
	} else {
		out.BudgetUsed = f.row.BudgetUsed + 1
	}
	return &out, nil
}

// anApprovedPlanWith returns a plan that lists the given tools.
func anApprovedPlanWith(tools ...string) *vibeflow.PlanRow {
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
				MaxToolCalls:  5,
				MaxJudgeCalls: 2,
			},
		},
	}
}

// planMiddleware builds a middleware with the plan gate wired and the
// given strictness.
//
// The frames grant EVERY tool that these tests use, because they are
// about the gate of the plan and not about the gate of capabilities:
// a refusal of ErrCapabilityNotGranted would mask the reason under
// test.
func planMiddleware(st plan.PlanStore, strictness plan.Strictness) (*GateMiddleware, *[]string) {
	refusals := &[]string{}
	m := &GateMiddleware{
		FrameSource:        grantAllFrames(),
		ActiveSession:      StaticSessionResolver{SessionID: "sess-test"},
		ActiveProject:      func() string { return "default" },
		ActiveConstitution: staticConstitution(),
		PlanGate:           plan.NewGate(st),
		PlanStrictness:     strictness,
		RecordRefusal: func(ctx context.Context, toolName, sessionID, code, message string) {
			*refusals = append(*refusals, code)
		},
	}
	return m, refusals
}

// grantedTools are the tools the tests of the plan gate call. The
// capabilities frame grants them one by one: HasGrant compares names
// literally and does not understand a wildcard.
var grantedTools = []string{
	"agent_memory_save",
	"agent_memory_list",
	"agent_memory_update",
	"vibe_publish",
	"vibe_spec",
	"plan_create",
	"plan_get",
	"plan_decide",
	"judge",
	"consensus",
	"session_close",
}

// grantAllFrames returns frames that grant every tool of grantedTools,
// so that the gate of capabilities never masks the gate of the plan.
func grantAllFrames() *stubFrameSource {
	now := time.Now().UTC()
	id, _ := atomic.NewIdentityFrame("op-test", "op-test", "sess-test", "cerebro", "1.1.0", false)
	id.ComposedAtValue = now
	persona, _ := atomic.NewPersonaFrame("cerebro", "1.1.0", "default",
		"test voice", "test claims policy", "test refusal pattern", "test tone")
	grants := make([]atomic.ToolGrant, 0, len(grantedTools))
	for _, t := range grantedTools {
		grants = append(grants, atomic.ToolGrant{ToolName: t, Scope: "*", GrantedAt: now})
	}
	return &stubFrameSource{
		identity: id,
		caps: &atomic.CapabilitiesFrame{
			ComposedAtValue:  now,
			ProjectID:        "default",
			SessionID:        "sess-test",
			GrantedTools:     grants,
			GrantedScopes:    []atomic.ScopeGrant{{ProjectID: "default", GrantedAt: now}},
			GrantedExpiresAt: now.Add(1 * time.Hour),
			Source:           "test",
		},
		persona: persona,
	}
}

// A tool that MUTATES and is not exempt: in `strict` mode it stops
// with the right error code, and the inner handler never runs.
func TestPlanGate_StrictRefusesAMutatingToolWithoutAPlan(t *testing.T) {
	st := &fakePlanStore{row: nil} // no plan at all
	m, _ := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "should not happen"}, nil
	}

	resp, err := m.Wrap(context.Background(), "agent_memory_save",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if innerCalled {
		t.Fatal("the tool ran: the gate should have refused it before")
	}
	if resp.Error == nil {
		t.Fatalf("expected a refusal; got %+v", resp)
	}
	if resp.Error.Code != "ErrPlanRequired" {
		t.Errorf("Error.Code = %q, want ErrPlanRequired", resp.Error.Code)
	}
}

// A plan that exists but is not approved: not_approved, and the
// reason is DIFFERENT from the "no plan" one, because the fix is
// different (approve it, do not create one).
func TestPlanGate_StrictRefusesAnUnapprovedPlan(t *testing.T) {
	row := anApprovedPlanWith("agent_memory_save")
	row.Status = vibeflow.PlanDraft
	st := &fakePlanStore{row: row}
	m, _ := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "should not happen"}, nil
	}

	resp, err := m.Wrap(context.Background(), "agent_memory_save",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if innerCalled {
		t.Fatal("the tool ran: the gate should have refused it before")
	}
	if resp.Error == nil || resp.Error.Code != "ErrNotApproved" {
		t.Errorf("expected ErrNotApproved; got %+v", resp.Error)
	}
}

// An approved plan that does NOT list the tool: also not_approved,
// because a plan that does not list a tool does not authorise it.
func TestPlanGate_StrictRefusesAToolThePlanDoesNotList(t *testing.T) {
	st := &fakePlanStore{row: anApprovedPlanWith("agent_memory_save")}
	m, _ := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "should not happen"}, nil
	}

	resp, err := m.Wrap(context.Background(), "vibe_publish",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if innerCalled {
		t.Fatal("the tool ran: the gate should have refused it before")
	}
	if resp.Error == nil || resp.Error.Code != "ErrNotApproved" {
		t.Errorf("expected ErrNotApproved; got %+v", resp.Error)
	}
}

// The budget is spent: budget_exceeded. And the refusal does NOT
// charge: a refused call does not consume the allowance.
func TestPlanGate_StrictRefusesWhenTheBudgetIsSpent(t *testing.T) {
	row := anApprovedPlanWith("agent_memory_save")
	row.BudgetUsed = 5 // the ceiling
	st := &fakePlanStore{row: row}
	m, _ := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "should not happen"}, nil
	}

	resp, err := m.Wrap(context.Background(), "agent_memory_save",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if innerCalled {
		t.Fatal("the tool ran: the gate should have refused it before")
	}
	if resp.Error == nil || resp.Error.Code != "ErrBudgetExceeded" {
		t.Errorf("expected ErrBudgetExceeded; got %+v", resp.Error)
	}
	if st.charges != 0 {
		t.Errorf("a refused call charged %d times", st.charges)
	}
}

// In `warn` mode the gate CHECKS and RECORDS, but the call goes
// through. This is the default of the switch and the reason the gate
// is born measuring and not born blocking.
func TestPlanGate_WarnRecordsAndLetsTheCallThrough(t *testing.T) {
	st := &fakePlanStore{row: nil} // no plan at all
	m, refusals := planMiddleware(st, plan.StrictnessWarn)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "ok"}, nil
	}

	resp, err := m.Wrap(context.Background(), "agent_memory_save",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !innerCalled {
		t.Fatal("the tool did not run: in warn mode the call goes through")
	}
	if resp.Error != nil {
		t.Errorf("the response carries an error in warn mode: %+v", resp.Error)
	}
	if len(*refusals) != 1 {
		t.Fatalf("recorded refusals = %v, want exactly one", *refusals)
	}
	if (*refusals)[0] != "ErrPlanRequired" {
		t.Errorf("recorded code = %q, want ErrPlanRequired", (*refusals)[0])
	}
}

// In `off` mode nothing is checked at all: no refusal, no charge.
func TestPlanGate_OffDoesNotCheck(t *testing.T) {
	st := &fakePlanStore{row: nil}
	m, refusals := planMiddleware(st, plan.StrictnessOff)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "ok"}, nil
	}

	resp, err := m.Wrap(context.Background(), "agent_memory_save",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !innerCalled {
		t.Fatal("the tool did not run")
	}
	if resp.Error != nil {
		t.Errorf("the response carries an error in off mode: %+v", resp.Error)
	}
	if len(*refusals) != 0 {
		t.Errorf("recorded refusals = %v, want none in off mode", *refusals)
	}
	if st.charges != 0 {
		t.Errorf("charges = %d, want 0 in off mode", st.charges)
	}
}

// An ALLOWED call charges the plan exactly once, and the tool runs.
func TestPlanGate_AllowedCallChargesOnceAndRuns(t *testing.T) {
	st := &fakePlanStore{row: anApprovedPlanWith("agent_memory_save")}
	m, refusals := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "ok"}, nil
	}

	resp, err := m.Wrap(context.Background(), "agent_memory_save",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !innerCalled {
		t.Fatal("the tool did not run")
	}
	if resp.Error != nil {
		t.Errorf("the response carries an error: %+v", resp.Error)
	}
	if st.charges != 1 {
		t.Errorf("charges = %d, want 1: the call must be charged exactly once", st.charges)
	}
	if len(*refusals) != 0 {
		t.Errorf("recorded refusals = %v, want none for an allowed call", *refusals)
	}
}

// T12-bis: THE DEADLOCK TEST.
//
// The plan hangs from a row of vibe_specs, so creating the FIRST plan
// needs a spec, and creating a spec must not need an approved plan.
// This test fixes the exemption of SPEC-194 §3.4-bis:
//
//   - vibe_spec without a plan -> OK (it is exempt)
//   - plan_create without a plan -> OK (it is exempt)
//   - plan_get without a plan -> OK (it is exempt)
//   - plan_decide without a plan -> OK (it is exempt)
//   - a tool that mutates and is NOT exempt -> ErrPlanRequired
//
// Without the exemption, the first four would be refused and there
// would be no way to ever create a plan.
func TestPlanGate_NoDeadlockInTheLifecycleOfThePlan(t *testing.T) {
	st := &fakePlanStore{row: nil} // no plan at all
	m, _ := planMiddleware(st, plan.StrictnessStrict)

	exempt := []string{"vibe_spec", "plan_create", "plan_get", "plan_decide"}
	for _, toolName := range exempt {
		innerCalled := false
		inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
			innerCalled = true
			return &tools.ToolResponse{Data: "ok"}, nil
		}
		resp, err := m.Wrap(context.Background(), toolName,
			json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
		if err != nil {
			t.Fatalf("%s: Wrap: %v", toolName, err)
		}
		if resp.Error != nil {
			t.Errorf("%s was refused (%s): the lifecycle of the plan must not deadlock", toolName, resp.Error.Code)
		}
		if !innerCalled {
			t.Errorf("%s did not run: the lifecycle of the plan must not deadlock", toolName)
		}
	}

	// And a tool that mutates and is not exempt still stops.
	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "should not happen"}, nil
	}
	resp, err := m.Wrap(context.Background(), "agent_memory_save",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if innerCalled {
		t.Fatal("a mutating tool ran without a plan")
	}
	if resp.Error == nil || resp.Error.Code != "ErrPlanRequired" {
		t.Errorf("expected ErrPlanRequired; got %+v", resp.Error)
	}
}

// session_close is NOT a tool that demands a plan. If it were, a
// session whose plan is still in draft could never be closed, and the
// plan could never be approved either (the decision needs a live
// session). That is a trap, and this test fixes it shut.
func TestPlanGate_SessionCloseIsAlwaysAllowed(t *testing.T) {
	st := &fakePlanStore{row: nil}
	m, refusals := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "ok"}, nil
	}
	resp, err := m.Wrap(context.Background(), "session_close",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("session_close was refused (%s): it must always be allowed", resp.Error.Code)
	}
	if !innerCalled {
		t.Error("session_close did not run")
	}
	if len(*refusals) != 0 {
		t.Errorf("recorded refusals = %v, want none for session_close", *refusals)
	}
}

// A READ-ONLY tool does not demand a plan: there is no mutation to
// authorise.
func TestPlanGate_ReadOnlyToolDoesNotDemandAPlan(t *testing.T) {
	st := &fakePlanStore{row: nil}
	m, refusals := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "ok"}, nil
	}
	resp, err := m.Wrap(context.Background(), "agent_memory_list",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("a read-only tool was refused (%s)", resp.Error.Code)
	}
	if !innerCalled {
		t.Error("a read-only tool did not run")
	}
	if len(*refusals) != 0 {
		t.Errorf("recorded refusals = %v, want none for a read-only tool", *refusals)
	}
}

// The judge spends the JUDGE budget, and the two budgets are
// independent: a judge call is charged to the judge counter.
func TestPlanGate_JudgeCallIsChargedToTheJudgeBudget(t *testing.T) {
	st := &fakePlanStore{row: anApprovedPlanWith("judge")}
	m, _ := planMiddleware(st, plan.StrictnessStrict)

	innerCalled := false
	inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
		innerCalled = true
		return &tools.ToolResponse{Data: "ok"}, nil
	}
	resp, err := m.Wrap(context.Background(), "judge",
		json.RawMessage(`{"session_id":"sess-test","project_id":"default"}`), inner)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("the judge call was refused: %+v", resp.Error)
	}
	if !innerCalled {
		t.Fatal("the judge call did not run")
	}
	if st.judge != 1 {
		t.Errorf("judge charges = %d, want 1", st.judge)
	}
	if st.charges-st.judge != 0 {
		t.Errorf("a judge call also charged the TOOL budget: charges=%d judge=%d", st.charges, st.judge)
	}
}
