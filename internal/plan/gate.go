// Package plan (continuation) — SPEC-194 (T2): the gate that reads the
// plan and charges the budget.
//
// This file is the BRIDGE between three pieces that must not know
// about each other:
//
//   - internal/vibeflow: the PURE gate (PlanGate.Check) and the plan
//     document. It knows nothing about the store.
//   - internal/store: the persistence. It knows nothing about the
//     gate.
//   - internal/server: the middleware, which is the only place where
//     a tool call is a real call with a real session.
//
// The bridge lives here, in the package that owns the plan, and it
// holds the store as an interface so it can be tested without a
// database.
//
// # The order of the charge, and why it is BEFORE the call
//
// The budget is charged by ChargePlanCall, which is atomic: it
// increments the counter and refuses when the ceiling is reached, in
// ONE transaction with a conditional UPDATE.
//
// The question is WHEN to charge, and the answer is: BEFORE the tool
// runs, and ONLY if the gate is going to let it through. There are
// three possible orderings and two of them are wrong:
//
//  1. Charge AFTER the tool ran. Then a tool that fails inside still
//     cost nothing, and the budget stops meaning "how many calls were
//     authorised" and starts meaning "how many calls happened".
//     Worse: the ceiling can be exceeded, because N concurrent calls
//     can all pass the check before any of them charges.
//
//  2. Charge in the tool itself (inside the orchestrator). Then the
//     charge is invisible to the gate, the gate cannot tell "this
//     call is the one that exhausted the budget" from "the budget was
//     already exhausted", and every tool would have to remember to
//     charge. A tool that forgot would charge nothing and the budget
//     would be a fiction.
//
//  3. Charge in the gate, BEFORE the call (this file). The gate is
//     the only place that decides whether a call happens, so it is
//     the only place that can charge honestly.
//
// The consequence of (3) is deliberate and it is written down here
// because it is a policy decision, not an accident: a call that the
// gate ALLOWS and that then FAILS inside has still consumed its
// budget unit. The budget measures ATTEMPTS, not successes.
//
// Why that is the right semantics: the budget exists to bound the
// cost of the work, and the cost of a failed call is not zero (the
// tool ran, it consumed a round-trip, it may have written something
// before failing). A budget that gave the unit back on failure would
// also give an unbounded number of failing calls for free.
//
// And the reason it cannot be charged twice: the charge happens in
// exactly one place, in one transaction, and a refused call never
// reaches it (the refusal returns before the charge).
package plan

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// The canonical reasons of the plan gate, re-exported.
//
// They are defined in internal/vibeflow (which owns the pure gate)
// and re-exported here because the consumers of the gate — the
// middleware, the wire tests — should not have to know that the pure
// gate lives in another package. The strings are the same in both
// places, so a refusal is legible from either side.
const (
	// ReasonPlanRequired means there is no plan at all.
	ReasonPlanRequired = vibeflow.ReasonPlanRequired
	// ReasonNotApproved means there is a plan but nobody approved it.
	ReasonNotApproved = vibeflow.ReasonNotApproved
	// ReasonBudgetExceeded means the plan was approved and the
	// allowance is spent.
	ReasonBudgetExceeded = vibeflow.ReasonBudgetExceeded
	// ReasonOK means the call may proceed.
	ReasonOK = vibeflow.ReasonOK
)

// recordRefusal writes the trail of a refusal.
//
// It is best-effort BY CONTRACT: a failure to write the trail must
// not change the decision (the call is refused either way, and a
// broken audit must not become a different answer). The error goes
// to the log and nowhere else.
//
// The row says what was attempted, not what was written: that is
// why the write_path is its own thing and not the name of a tool.
func (g *Gate) recordRefusal(ctx context.Context, wc store.WriteContext, toolName string, verdict vibeflow.PlanGateVerdict, row *vibeflow.PlanRow) {
	if g == nil || g.Store == nil {
		return
	}
	var specID int64
	if row != nil {
		specID = row.SpecID
	}
	ev := audit.WriteEvent{
		TableName:       "vibe_specs",
		RowID:           specID,
		ProjectID:       wc.ProjectID,
		Actor:           toolName,
		SessionID:       wc.SessionID,
		WritePath:       WritePathRefusal,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		Notes:           "reason=" + verdict.Reason + " " + verdict.Message,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := g.Store.RecordWrite(ctx, ev); err != nil {
		log.Printf("dark-memory: plan gate: recording the refusal failed: %v", err)
	}
}

// WritePathRefusal is the write_path of the row that records a
// REFUSAL of the plan gate.
//
// It is its own thing and not the name of a tool, for two reasons:
// a row of write_audit describes a WRITE, and a refusal is not one;
// and the KPI of SPEC-194 §6.2 counts the negatives by reason, which
// needs them to be distinguishable from the charges (whose write_path
// is WritePathCharge) and from the writes of the tools themselves.
const WritePathRefusal = "plan_gate_refusal"

// WritePathCharge is the write_path of the row that records the
// CHARGE of a call to the budget of a plan.
//
// It is the row that the store writes inside the transaction of the
// charge, so it is the proof that the unit was really spent.
const WritePathCharge = "plan_gate_charge"

// PlanStore is the part of the store the gate needs. It is an
// interface so the gate can be tested without a database, and so the
// dependency points at what the gate USES instead of at the whole
// store.
type PlanStore interface {
	// GetOpenPlan reads the plan of the OPEN spec of a session. It
	// returns (nil, nil) when there is no open spec or the open spec
	// has no plan: "no plan" is an answer, not an error.
	GetOpenPlan(ctx context.Context, sessionID string) (*vibeflow.PlanRow, error)

	// ChargePlanCall atomically charges ONE call to the plan's
	// budget and returns the row with the new counters.
	ChargePlanCall(ctx context.Context, wc store.WriteContext, specID int64, judgeCall bool) (*vibeflow.PlanRow, error)

	// RecordWrite appends a row to write_audit WITHOUT writing any
	// data. It is how a REFUSAL leaves a trail: the refusal is not a
	// write, but it is an ATTEMPT, and the plan of a session has to
	// be auditable ("who called what without an approved plan").
	RecordWrite(ctx context.Context, ev audit.WriteEvent) error
}

// Gate answers "may this call proceed" for the tools that mutate the
// kernel, and charges the budget of the calls it allows.
//
// It is deliberately NOT the pure vibeflow.PlanGate: that one is a
// function of (input, plan), and this one is a function of
// (input, store). The pure one decides; this one decides AND pays.
type Gate struct {
	// Store is where the plan and the counters live.
	Store PlanStore

	// Pure is the decision function. It is a field so a test can
	// substitute it, and so the pure logic keeps exactly ONE home.
	Pure *vibeflow.PlanGate
}

// NewGate builds the gate over a store.
func NewGate(st PlanStore) *Gate {
	return &Gate{Store: st, Pure: vibeflow.NewPlanGate()}
}

// Decision is the outcome of a gate check.
type Decision struct {
	// Allowed says whether the call may proceed.
	Allowed bool

	// Reason is the canonical reason code (one of the three of
	// SPEC-194, or "ok"). It is the STRING form, matching
	// vibeflow's reasons; the middleware maps it to the policy
	// enum, because the two packages must not import each other.
	Reason string

	// Message explains the refusal.
	Message string

	// Hint suggests the next action.
	Hint string

	// BudgetExhausted names the resource that ran out, when that is
	// the cause.
	BudgetExhausted string

	// SpecID is the spec the plan belongs to. Zero when there is no
	// plan.
	SpecID int64
}

// Check evaluates one tool call for one session and, when it lets
// the call through, CHARGES the plan's budget.
//
// The charge happens here, inside Check, AFTER the pure gate allowed
// the call and BEFORE the caller runs it. See the package comment for
// why the order is this one and not another.
//
// judgeCall says whether this call spends the judge budget instead of
// the tool-call budget. The two ceilings are independent (SPEC-194
// §3.8) and charging the wrong one would refuse a call whose own
// allowance was still available.
//
// A read error is NOT a refusal: it returns (nil, err) so the caller
// can decide. Under the operator's switch, an unreadable plan in
// `warn` mode lets the call through (the measurement is lost, but
// the work is not stopped) and in `strict` mode it refuses.
func (g *Gate) Check(ctx context.Context, sessionID, toolName string, judgeCall bool, wc store.WriteContext) (*Decision, error) {
	if g == nil || g.Store == nil {
		// No store wired: the gate cannot check. The caller decides
		// what that means (the middleware treats a nil gate as "not
		// wired", which is the legacy behaviour).
		return nil, nil
	}

	row, err := g.Store.GetOpenPlan(ctx, sessionID)
	if err != nil {
		// A store that does not SUPPORT plans is not a broken
		// store: it is a store without this feature. The postgres
		// driver has the four plan methods as notImpl (they return
		// ErrNotConfigured), and a gate that turned that into a
		// refusal would refuse EVERY mutating call in strict mode.
		//
		// So "not configured" means "the gate does not apply", and
		// the caller gets no decision at all (nil, nil) — the same
		// shape as a gate that is not wired.
		if errors.Is(err, store.ErrNotConfigured) {
			return nil, nil
		}
		return nil, fmt.Errorf("plan gate: reading the open plan: %w", err)
	}

	// The pure gate decides. It never touches the store.
	//
	// The budget comes from EffectiveBudget(), NOT from
	// row.Plan.Budget: the maxima live in the plan document (the
	// human can edit them) but the COUNTERS live in the columns the
	// store increments atomically. Reading the JSON copy would always
	// report zero spent, and the budget would never cut.
	verdict := g.Pure.Check(vibeflow.PlanGateInput{
		ToolName:    toolName,
		HasPlan:     row != nil,
		Status:      planStatus(row),
		Budget:      effectiveBudget(row),
		Authorised:  planAuthorises(row, toolName),
		IsJudgeCall: judgeCall,
	})

	if !verdict.Allowed {
		// The refusal leaves a trail in write_audit. It is an
		// ATTEMPT, not a write: the row has its own write_path
		// ("plan_gate_refusal") so it can never be confused with a
		// row that describes a real write, and it is what makes the
		// KPI of SPEC-194 §6.2 ("negatives by reason") consultable
		// with a GROUP BY.
		//
		// When there is no plan at all, the row points at row_id 0:
		// there is no spec to point at, and 0 says exactly that.
		g.recordRefusal(ctx, wc, toolName, verdict, row)
		return &Decision{
			Allowed:         false,
			Reason:          verdict.Reason,
			Message:         verdict.Message,
			Hint:            verdict.Hint,
			BudgetExhausted: verdict.BudgetExhausted,
		}, nil
	}

	// Allowed: charge. The charge is atomic and it refuses by
	// itself when the ceiling is reached (the race between the check
	// above and this charge is closed by the conditional UPDATE).
	charged, err := g.Store.ChargePlanCall(ctx, wc, row.SpecID, judgeCall)
	if err != nil {
		// The check said there was budget and the charge says
		// there was not: a concurrent call took the last unit
		// between the two. That is a REFUSAL, not an error, and it
		// has its own reason so the caller can tell it apart from
		// a broken store.
		if isBudgetExceeded(err) {
			return &Decision{
				Allowed: false,
				Reason:  vibeflow.ReasonBudgetExceeded,
				Message: fmt.Sprintf("the plan's %s budget was exhausted by a concurrent call", resourceName(judgeCall)),
				Hint:    "raise the plan's budget with dark_memory_plan_decide (edit) or open a new plan",
				SpecID:  row.SpecID,
			}, nil
		}
		return nil, fmt.Errorf("plan gate: charging the plan budget: %w", err)
	}

	// The charge moved the counter of the RESOURCE being spent. If it
	// moved the other one, the two budgets would be coupled again and
	// a call would refuse the other resource's ceiling (the defect
	// the independent review of PR #43 found). The invariant is
	// checked HERE, on every call, because a check that only lives
	// in a test is a check that rots.
	if charged == nil {
		return nil, fmt.Errorf("plan gate: charging the plan budget: the store returned no row")
	}
	if err := verifyCharge(charged, judgeCall); err != nil {
		return nil, err
	}

	return &Decision{
		Allowed: true,
		Reason:  vibeflow.ReasonOK,
		SpecID:  row.SpecID,
	}, nil
}

// verifyCharge checks that the charge moved the counter of the
// resource that was spent, and only that one.
//
// It reads the counters of the row the store returned AFTER the
// charge. The comparison is against the row BEFORE the charge: the
// gate has it in hand (it read it to decide), so it can tell "the
// charge moved my resource" from "the charge moved the other one".
func verifyCharge(charged *vibeflow.PlanRow, judgeCall bool) error {
	// The counters of the row, not of the plan document: the
	// authoritative numbers are the ones the store just wrote.
	b := charged.EffectiveBudget()
	if judgeCall {
		if b.UsedJudgeCalls < 1 {
			return fmt.Errorf("plan gate: the judge charge did not move the judge counter (used=%d)",
				b.UsedJudgeCalls)
		}
		return nil
	}
	if b.UsedToolCalls < 1 {
		return fmt.Errorf("plan gate: the tool charge did not move the tool counter (used=%d)",
			b.UsedToolCalls)
	}
	return nil
}

// planStatus reads the gate state of the row. A missing row is
// "no plan", and the pure gate handles that through HasPlan.
func planStatus(row *vibeflow.PlanRow) vibeflow.PlanStatus {
	if row == nil {
		return vibeflow.PlanDraft
	}
	return row.Status
}

// effectiveBudget reads the budget of the row as the gate must see
// it: the plan's maximums with the store's authoritative counters.
//
// A missing row has an empty budget: the pure gate refuses before it
// ever looks at it.
func effectiveBudget(row *vibeflow.PlanRow) vibeflow.Budget {
	if row == nil {
		return vibeflow.Budget{}
	}
	return row.EffectiveBudget()
}

// planAuthorises says whether the plan lists the tool.
//
// The candidates of a plan are the tools it PROPOSES. A plan that
// does not list the tool does not authorise it: the gate would
// otherwise be a formality, since the budget alone would still allow
// any tool.
func planAuthorises(row *vibeflow.PlanRow, toolName string) bool {
	if row == nil {
		return false
	}
	for _, c := range row.Plan.Candidates {
		if c.Tool == toolName {
			return true
		}
	}
	return false
}

// resourceName names the budget resource a call spends.
func resourceName(judgeCall bool) string {
	if judgeCall {
		return vibeflow.ResourceJudgeCalls
	}
	return vibeflow.ResourceToolCalls
}

// isBudgetExceeded reports whether the error is the store's budget
// refusal. The store wraps it in a FieldError, so the check goes
// through errors.Is.
func isBudgetExceeded(err error) bool {
	return err != nil && errors.Is(err, store.ErrBudgetExceeded)
}
