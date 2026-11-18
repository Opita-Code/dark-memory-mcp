// Package vibeflow — SPEC-194: the PLAN as an artifact.
//
// This file defines the plan: the thing that says, before any work
// happens, WHICH tools are going to be called, at WHAT depth, and with
// WHAT budget. The kernel today judges the RESULT (drift-at-write) and
// the CALL (policy gate); nobody judges the PATH. The plan closes that
// gap.
//
// # Why the plan is not a subsystem
//
// The plan is a row of vibe_specs with new columns, not a new table and
// not a new state machine. Two measured reasons:
//
//   - A separate `plans` table would duplicate vibe_specs and break the
//     SPEC-204 rule (resolve one thing in one place), plus it would
//     need read/write migrations in BOTH stores.
//   - vlp_state cannot hold it: its index is UNIQUE on
//     (project_id, session_id), i.e. ONE row per SESSION, while the plan
//     is PER SPEC (a session opens and closes several specs, each with
//     its own plan and its own human decision).
//
// # The three authorities
//
// The plan deliberately splits authority, and no layer decides what
// belongs to another:
//
//   - the deterministic part (BM25 over the tool registry) PROPOSES a
//     shortlist. It does not choose: routers that rely on the task
//     description rank BELOW the large model (arXiv 2602.02751).
//   - the judge VALIDATES the shortlist. It does not choose either.
//   - the human APPROVES, via plan_decide (execute | edit | cancel).
//
// # Pure by construction
//
// PlanGate.Check is a PURE function: it takes a plan and a call and
// returns a verdict. It does NOT touch the store, does NOT call the
// LLM, and does NOT read the clock. That is what makes it testable
// without a database and what keeps the gate cheap enough to run on
// EVERY mutating tool call.
//
// The store (internal/store) owns persistence and the counter; the
// orchestrator owns the tool surface; the gate middleware owns the
// refusal. This file owns the vocabulary.
package vibeflow

import (
	"fmt"
	"strings"
)

// PlanStatus is the gate state of a plan. Five values, and the
// distinction that matters is between "nobody approved it yet" and
// "somebody decided against it".
type PlanStatus string

const (
	// PlanDraft means the plan exists and nobody approved it. A
	// mutating tool call is refused with ErrPlanRequired (or
	// ErrNotApproved, depending on which check fires first).
	PlanDraft PlanStatus = "draft"

	// PlanApproved means the human decided `execute`. The tools
	// declared in the plan may run, within the budget.
	PlanApproved PlanStatus = "approved"

	// PlanExecuted means the plan ran to completion or to its budget
	// ceiling. Terminal.
	PlanExecuted PlanStatus = "executed"

	// PlanCancelled means the human cancelled it. Mutating tools are
	// refused until another plan exists. Terminal.
	PlanCancelled PlanStatus = "cancelled"

	// PlanEdited is HISTORICAL: the reviewer changed the plan with a
	// prompt, and the live plan went back to `draft`. It is kept as a
	// state so the trail shows that a revision happened.
	PlanEdited PlanStatus = "edited"
)

// Terminal reports whether the plan status admits no further progress
// on its own. PlanEdited is terminal as a RECORDED state but the live
// plan re-opens as PlanDraft, so the caller must check the revision
// counter to know whether work is still possible.
func (s PlanStatus) Terminal() bool {
	switch s {
	case PlanExecuted, PlanCancelled, PlanEdited:
		return true
	}
	return false
}

// ParsePlanStatus reverses PlanStatus.String. Unknown values fall back
// to PlanDraft: a plan whose status cannot be read must behave like an
// unapproved plan, never like an approved one. Failing open here
// would be a hole in the gate.
func ParsePlanStatus(s string) PlanStatus {
	switch PlanStatus(strings.TrimSpace(strings.ToLower(s))) {
	case PlanApproved:
		return PlanApproved
	case PlanExecuted:
		return PlanExecuted
	case PlanCancelled:
		return PlanCancelled
	case PlanEdited:
		return PlanEdited
	default:
		return PlanDraft
	}
}

// PlanTier is the DEPTH of the task, not its width. Measured: depth is
// decisive and width saturates (arXiv 2510.04311, task complexity), and
// the number of tools interacts NEGATIVELY with system performance
// (arXiv 2512.08296, beta=-0.141, p<0.001). So the plan proposes a
// tier and the human may edit it.
type PlanTier string

const (
	// PlanTierShort is one tool plus one check.
	PlanTierShort PlanTier = "corta"
	// PlanTierMedium is a tool, a verification and one retry.
	PlanTierMedium PlanTier = "media"
	// PlanTierLong is work with milestones and more than one verification.
	PlanTierLong PlanTier = "larga"
)

// PlanTierForCandidates maps the size of the shortlist to a proposed tier.
// This is a PROPOSAL carried by the plan, not a constant of the
// system: the human can edit it through plan_decide(edit).
func PlanTierForCandidates(n int) PlanTier {
	switch {
	case n <= 3:
		return PlanTierShort
	case n <= 8:
		return PlanTierMedium
	default:
		return PlanTierLong
	}
}

// DefaultBudgetForPlanTier returns the PROPOSED budget for a tier. These
// numbers are a starting point, not a law: they live in the plan and
// the human can change them. The KPI of obligation (§6.2 of SPEC-194)
// is what says whether they are calibrated: if most plans exhaust their
// budget, the tier is mis-sized.
func DefaultBudgetForPlanTier(t PlanTier) Budget {
	switch t {
	case PlanTierShort:
		return Budget{MaxToolCalls: 3, MaxJudgeCalls: 1}
	case PlanTierMedium:
		return Budget{MaxToolCalls: 8, MaxJudgeCalls: 2}
	case PlanTierLong:
		return Budget{MaxToolCalls: 20, MaxJudgeCalls: 4}
	default:
		return Budget{MaxToolCalls: 3, MaxJudgeCalls: 1}
	}
}

// Candidate is one proposed tool with its deterministic weight.
//
// The weight PROPOSES. It does not choose: the judge validates the
// shortlist and the human approves it. ScoreSource records WHICH
// layer produced the score, so an auditor can tell a deterministic
// proposal from a judged or human one.
type Candidate struct {
	// Tool is the bare tool name (no `dark_memory_` prefix).
	Tool string `json:"tool"`

	// Weight is the deterministic score (BM25 over the tool registry
	// with the full doc).
	Weight float64 `json:"weight"`

	// Rationale is why this candidate is here, in one line.
	Rationale string `json:"rationale,omitempty"`

	// ScoreSource is "bm25_docs" | "llm_judge" | "human".
	ScoreSource string `json:"score_source,omitempty"`
}

// Score source constants.
const (
	ScoreSourceBM25  = "bm25_docs"
	ScoreSourceJudge = "llm_judge"
	ScoreSourceHuman = "human"
)

// Budget is the consumable allowance of a plan. It has TWO counters
// because the two resources are distinct and differently scarce: tool
// calls and judge calls. Measured: giving more tool-call budget
// WITHOUT budget awareness improves nothing (arXiv 2511.17006, BATS),
// so the budget has to be a live counter, not a ceiling to look at.
type Budget struct {
	MaxToolCalls   int `json:"max_tool_calls"`
	UsedToolCalls  int `json:"used_tool_calls"`
	MaxJudgeCalls  int `json:"max_judge_calls"`
	UsedJudgeCalls int `json:"used_judge_calls"`
}

// The two resources of the budget. They are DISTINCT ceilings, and a
// call that spends one does not have to be refused because the other
// ran out.
const (
	// ResourceToolCalls names the tool-call budget.
	ResourceToolCalls = "tool_calls"
	// ResourceJudgeCalls names the judge-call budget.
	ResourceJudgeCalls = "judge_calls"
)

// Exhausted reports whether the GIVEN resource ran out. The returned
// string is empty when that resource still has room.
//
// The resource is a parameter, and that is the whole point: the two
// budgets are two ceilings. A version of this function that looked at
// BOTH counters refused a tool call because the JUDGE budget was spent,
// and refused a judge call because the TOOL budget was spent. With the
// default tier `corta` (one judge call), a single judge call blocked
// every mutating tool call of the plan while the tool budget sat
// untouched. That defect was found by the independent review of PR #43
// and it contradicted SPEC-194 §3.8, which says each counter cuts ITS
// resource.
func (b Budget) Exhausted(resource string) string {
	switch resource {
	case ResourceJudgeCalls:
		if b.UsedJudgeCalls >= b.MaxJudgeCalls {
			return ResourceJudgeCalls
		}
	case ResourceToolCalls:
		if b.UsedToolCalls >= b.MaxToolCalls {
			return ResourceToolCalls
		}
	}
	return ""
}

// Remaining returns how many calls of each kind are left. Negative
// values are impossible in practice but are clamped to zero so a
// caller cannot read a negative allowance and misbehave.
func (b Budget) Remaining() (toolCalls, judgeCalls int) {
	toolCalls = b.MaxToolCalls - b.UsedToolCalls
	if toolCalls < 0 {
		toolCalls = 0
	}
	judgeCalls = b.MaxJudgeCalls - b.UsedJudgeCalls
	if judgeCalls < 0 {
		judgeCalls = 0
	}
	return toolCalls, judgeCalls
}

// Plan is the artifact: the intent, the proposed shortlist with its
// weights, the depth tier and the budget. Persisted in vibe_specs
// (plan_json) with the gate state in its own columns.
type Plan struct {
	// Intent is the "what": the goal the plan serves.
	Intent string `json:"intent"`

	// Candidates are the proposed tools with their weights.
	Candidates []Candidate `json:"candidates"`

	// PlanTier is the proposed depth.
	PlanTier PlanTier `json:"tier"`

	// Budget is the consumable allowance.
	Budget Budget `json:"budget"`

	// Revision counts how many times the plan was reopened by an
	// `edit` decision. It is what makes "edited" traceable.
	Revision int `json:"revision,omitempty"`
}

// CandidateNames returns the bare tool names of the candidates, in
// order. Used by the gate to know WHICH tools the plan authorises.
func (p *Plan) CandidateNames() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Candidates))
	for _, c := range p.Candidates {
		if c.Tool != "" {
			out = append(out, c.Tool)
		}
	}
	return out
}

// Authorises reports whether the plan lists the given tool. A plan
// that lists nothing authorises nothing: an empty candidate list is
// not a wildcard.
func (p *Plan) Authorises(tool string) bool {
	for _, name := range p.CandidateNames() {
		if name == tool {
			return true
		}
	}
	return false
}

// Decision is the human verdict on a plan.
type Decision string

const (
	// DecisionExecute approves the plan as it is.
	DecisionExecute Decision = "execute"
	// DecisionEdit reopens the plan with the reviewer's prompt. It is
	// NOT a rejection: it is a redirection (Zapier HITL: the reviewer
	// can EDIT the proposal before approving; Jira: rejecting can stop
	// OR redirect).
	DecisionEdit Decision = "edit"
	// DecisionCancel stops the plan.
	DecisionCancel Decision = "cancel"
)

// ParseDecision reverses Decision.String. Unknown values are rejected
// by the caller: this function returns ("", false) rather than
// defaulting, because a decision must never be guessed.
func ParseDecision(s string) (Decision, bool) {
	switch Decision(strings.TrimSpace(strings.ToLower(s))) {
	case DecisionExecute:
		return DecisionExecute, true
	case DecisionEdit:
		return DecisionEdit, true
	case DecisionCancel:
		return DecisionCancel, true
	}
	return "", false
}

// PlanGateVerdict is the outcome of a gate check.
type PlanGateVerdict struct {
	// Allowed is true when the call may proceed.
	Allowed bool `json:"allowed"`

	// Reason is the canonical refusal code ("ok" when allowed).
	Reason string `json:"reason"`

	// Message is the human-readable explanation.
	Message string `json:"message,omitempty"`

	// Hint tells the caller what to do next.
	Hint string `json:"hint,omitempty"`

	// BudgetExhausted names the resource that ran out, when that is
	// the cause.
	BudgetExhausted string `json:"budget_exhausted,omitempty"`
}

// Canonical refusal reasons. These mirror the three policy reasons
// added by SPEC-194 (internal/policy: ReasonPlanRequired,
// ReasonNotApproved, ReasonBudgetExceeded) and use the same strings,
// so the refusal is legible from either side.
const (
	ReasonPlanRequired   = "plan_required"
	ReasonNotApproved    = "not_approved"
	ReasonBudgetExceeded = "budget_exceeded"
	ReasonOK             = "ok"
)

// PlanGateInput is everything the pure gate needs. It is a value type
// on purpose: no store handle, no clock, no LLM.
type PlanGateInput struct {
	// ToolName is the bare name of the tool being called.
	ToolName string

	// HasPlan says whether a plan is open for this session.
	HasPlan bool

	// Status is the gate state of that plan.
	Status PlanStatus

	// Budget is the plan's budget at the time of the call.
	Budget Budget

	// Authorised says whether the plan lists this tool. A plan that
	// does not list the tool does not authorise it.
	Authorised bool

	// IsJudgeCall says whether this call spends the judge budget.
	// It is LOAD-BEARING: the gate checks the ceiling of the
	// resource being spent, and the two ceilings are independent.
	IsJudgeCall bool
}

// PlanGate is the pure gate. It answers one question: may this call
// proceed, and if not, why.
//
// The order of the checks is EXPLICIT and it matters:
//
//  1. is there a plan at all?      -> plan_required
//  2. is the plan approved?         -> not_approved
//  3. does the plan list the tool?  -> not_approved
//  4. is there budget left?         -> budget_exceeded
//
// Check 3 sits between the approval and the budget on purpose: a
// plan that was approved but does not list the tool being called is
// not an approval to run that tool. Running it would be spending the
// budget of a plan that never asked for it.
//
// # Why there is no "skip" parameter
//
// The gate takes a value type with no escape hatch. A flag in the
// arguments would be a door left open: any caller could pass it, and
// the one caller that must not use it is exactly the one that would.
// The lifecycle exemption (plan_create, plan_get, plan_decide) is a
// CLOSED LIST, checked before this gate, in the middleware.
type PlanGate struct{}

// NewPlanGate returns the gate. It takes no dependencies on purpose.
func NewPlanGate() *PlanGate { return &PlanGate{} }

// Check evaluates one call against one plan. Pure: same inputs, same
// output, no clock and no store.
func (g *PlanGate) Check(in PlanGateInput) PlanGateVerdict {
	// 1. No plan at all.
	if !in.HasPlan {
		return PlanGateVerdict{
			Allowed: false,
			Reason:  ReasonPlanRequired,
			Message: fmt.Sprintf("tool %q requires an approved plan, and no plan is open for this session", in.ToolName),
			Hint:    "create a plan with dark_memory_plan_create and have it approved with dark_memory_plan_decide",
		}
	}

	// 2. The plan exists but nobody approved it (or it was cancelled,
	// or it was edited and is waiting again).
	if in.Status != PlanApproved {
		return PlanGateVerdict{
			Allowed: false,
			Reason:  ReasonNotApproved,
			Message: fmt.Sprintf("tool %q requires an approved plan, and the open plan is %q", in.ToolName, in.Status),
			Hint:    "approve the plan with dark_memory_plan_decide (execute), edit it, or cancel it",
		}
	}

	// 3. The plan does not list this tool.
	if !in.Authorised {
		return PlanGateVerdict{
			Allowed: false,
			Reason:  ReasonNotApproved,
			Message: fmt.Sprintf("the approved plan does not list tool %q", in.ToolName),
			Hint:    "add the tool to the plan's candidates, or approve a plan that includes it",
		}
	}

	// 4. The budget of the RESOURCE BEING SPENT is exhausted. Only
	// that one: the tool-call ceiling and the judge-call ceiling are
	// independent, and spending one does not spend the other.
	resource := ResourceToolCalls
	if in.IsJudgeCall {
		resource = ResourceJudgeCalls
	}
	if exhausted := in.Budget.Exhausted(resource); exhausted != "" {
		return PlanGateVerdict{
			Allowed:         false,
			Reason:          ReasonBudgetExceeded,
			BudgetExhausted: exhausted,
			Message:         fmt.Sprintf("the plan's %s budget is exhausted (%d/%d)", exhausted, budgetUsed(in, exhausted), budgetMax(in, exhausted)),
			Hint:            "raise the plan's budget with dark_memory_plan_decide (edit) or open a new plan",
		}
	}

	return PlanGateVerdict{Allowed: true, Reason: ReasonOK}
}

func budgetUsed(in PlanGateInput, resource string) int {
	if resource == ResourceJudgeCalls {
		return in.Budget.UsedJudgeCalls
	}
	return in.Budget.UsedToolCalls
}

func budgetMax(in PlanGateInput, resource string) int {
	if resource == ResourceJudgeCalls {
		return in.Budget.MaxJudgeCalls
	}
	return in.Budget.MaxToolCalls
}

// PlanRow is a plan as it lives in the store: the Plan document plus
// the gate state that lives in its own columns.
//
// The split is deliberate. The Plan is a DOCUMENT (it evolves with the
// spec, so it stays JSON), while the gate state is QUERIED on every
// mutating tool call and therefore lives in columns (SPEC-194 §3.2).
//
// The budget counter lives here and NOT inside the Plan document,
// because it has to be incremented atomically with the check that
// reads it (AC11). A counter inside a JSON document would need the
// whole document rewritten, which is a read-modify-write race.
type PlanRow struct {
	// SpecID is the vibe_specs row this plan belongs to. The plan has
	// no identity of its own: it IS a spec with a plan on it.
	SpecID int64 `json:"spec_id"`

	// SessionID is the session that opened the spec.
	SessionID string `json:"session_id,omitempty"`

	// Plan is the document.
	Plan Plan `json:"plan"`

	// Status is the gate state.
	Status PlanStatus `json:"status"`

	// Decision is the human verdict that moved the plan to its
	// current status.
	Decision Decision `json:"decision,omitempty"`

	// DecidedBy is who decided.
	DecidedBy string `json:"decided_by,omitempty"`

	// DecidedAt is when.
	DecidedAt string `json:"decided_at,omitempty"`

	// BudgetUsed is the number of TOOL calls already charged to this
	// plan. It is the authoritative counter: the Plan.Budget copy is
	// a snapshot for humans.
	BudgetUsed int `json:"budget_used"`

	// JudgeUsed is the number of JUDGE calls already charged to this
	// plan. It is a SEPARATE counter because the two resources are
	// distinct and differently scarce: one column cannot enforce two
	// ceilings. (Found by the store test, which failed against a
	// one-counter implementation.)
	JudgeUsed int `json:"judge_used"`

	// CreatedAt / UpdatedAt.
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// EffectiveBudget returns the budget as the gate must see it: the
// PLAN's maximums with the STORE's authoritative counter applied.
//
// The split matters: MaxToolCalls and MaxJudgeCalls live in the Plan
// document (the human can edit them), while UsedToolCalls comes from
// the column that the store increments atomically. Merging them here,
// in ONE place, is what keeps the gate from reading a stale count.
func (r *PlanRow) EffectiveBudget() Budget {
	b := r.Plan.Budget
	b.UsedToolCalls = r.BudgetUsed
	b.UsedJudgeCalls = r.JudgeUsed
	return b
}
