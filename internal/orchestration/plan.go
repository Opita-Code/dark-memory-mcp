package orchestration

// SPEC-194 (T7, T8): the plan tools.
//
// Three tools, and they are the PLAN surface:
//
//	plan_create  — proposes a plan (candidates, tier, budget)
//	plan_get     — reads the plan of a spec
//	plan_decide  — the human decides: execute | edit | cancel
//
// # The lifecycle exemption
//
// These three tools are EXEMPT from the plan gate (SPEC-194 §3.4-bis,
// AC13). Without that exemption there is a DEADLOCK: plan_decide is
// the tool that RECORDS the approval, so requiring an approved plan
// in order to approve the first plan is impossible.
//
// The exemption is a CLOSED LIST in the middleware
// (internal/server: PlanLifecycleExempt), not a flag in the
// arguments: a flag could be passed by any caller, and the one
// caller that must not use it is exactly the one that would.
//
// # What plan_create does and does not do
//
// It PROPOSES. It does not choose: the deterministic ranker produces a
// shortlist, the judge validates it, the human approves it. The three
// authorities are kept apart on purpose (SPEC-194 §11).
//
// It also does not EXECUTE: creating a plan does no work. The work
// starts when the plan is approved, and there the gate makes the
// decisions.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// PlanCreateInput is the input of dark_memory_plan_create.
type PlanCreateInput struct {
	// SpecID is the spec the plan describes. The plan is a spec with
	// a plan on it: it has no identity of its own.
	SpecID int64 `json:"spec_id"`

	// Intent is the "what". It is the text the candidates are
	// ranked against, and it is what the judge validates.
	Intent string `json:"intent"`

	// SessionID names the session, for the write's project
	// resolution (SPEC-204: the write lands in the project of the
	// session, never in a mutable global slot).
	SessionID string `json:"session_id,omitempty"`

	// MaxCandidates caps the shortlist. 0 means the tier's default.
	MaxCandidates int `json:"max_candidates,omitempty"`

	// MaxToolCalls / MaxJudgeCalls override the tier's proposed
	// budget. 0 means "use the tier's proposal".
	MaxToolCalls  int `json:"max_tool_calls,omitempty"`
	MaxJudgeCalls int `json:"max_judge_calls,omitempty"`
}

// PlanCreateResult is the output of dark_memory_plan_create.
type PlanCreateResult struct {
	// SpecID is the spec the plan belongs to.
	SpecID int64 `json:"spec_id"`

	// Status is the gate state of the new plan. It is ALWAYS
	// "draft": creating a plan does not approve it.
	Status string `json:"status"`

	// Plan is the proposed plan.
	Plan vibeflow.Plan `json:"plan"`

	// Tier is the proposed depth.
	Tier string `json:"tier"`

	// Candidates is the shortlist, for convenience.
	Candidates []vibeflow.Candidate `json:"candidates"`

	// Budget is the proposed budget.
	Budget vibeflow.Budget `json:"budget"`

	// Judged says whether the judge validated the shortlist.
	Judged bool `json:"judged"`

	// JudgeNote carries the judge's reason, when it ran.
	JudgeNote string `json:"judge_note,omitempty"`
}

// PlanGetInput is the input of dark_memory_plan_get.
type PlanGetInput struct {
	// SpecID is the spec to read the plan of. 0 means "the open spec
	// of the session".
	SpecID int64 `json:"spec_id,omitempty"`

	// SessionID names the session.
	SessionID string `json:"session_id,omitempty"`
}

// PlanGetResult is the output of dark_memory_plan_get.
type PlanGetResult struct {
	// SpecID is the spec the plan belongs to.
	SpecID int64 `json:"spec_id"`

	// Status is the gate state.
	Status string `json:"status"`

	// Plan is the stored plan.
	Plan vibeflow.Plan `json:"plan"`

	// Decision / DecidedBy / DecidedAt are the human verdict.
	Decision  string `json:"decision,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`

	// Budget is the EFFECTIVE budget: the plan's maximums with the
	// store's authoritative counters applied.
	Budget vibeflow.Budget `json:"budget"`

	// Found says whether there IS a plan. A plan-less spec is a
	// normal state, not an error.
	Found bool `json:"found"`
}

// PlanDecideInput is the input of dark_memory_plan_decide.
type PlanDecideInput struct {
	// SpecID is the spec whose plan is being decided. 0 means "the
	// open spec of the session".
	SpecID int64 `json:"spec_id,omitempty"`

	// Decision is execute | edit | cancel.
	Decision string `json:"decision"`

	// EditPrompt is the reviewer's prompt. It is REQUIRED for
	// decision=edit and ignored otherwise.
	EditPrompt string `json:"edit_prompt,omitempty"`

	// SessionID names the session.
	SessionID string `json:"session_id,omitempty"`

	// DecidedBy is who decided. Defaults to the session's operator.
	DecidedBy string `json:"decided_by,omitempty"`
}

// PlanDecideResult is the output of dark_memory_plan_decide.
type PlanDecideResult struct {
	// SpecID is the spec whose plan was decided.
	SpecID int64 `json:"spec_id"`

	// Decision is the applied decision.
	Decision string `json:"decision"`

	// Status is the gate state AFTER the decision.
	Status string `json:"status"`

	// DecidedBy / DecidedAt.
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`

	// Revision is the revision counter. It increases on `edit`.
	Revision int `json:"revision"`

	// Plan is the plan after the decision.
	Plan vibeflow.Plan `json:"plan"`
}

// ToolRanker is the source of the candidate shortlist. It is an
// interface so the orchestrator does not depend on the registry (the
// registry lives in internal/tools, which imports the orchestrator:
// the dependency cannot be inverted at the package level).
//
// The production implementation is plan.Ranker over the tool registry.
type ToolRanker interface {
	// Rank returns the shortlist for the intent, ordered by score
	// descending.
	Rank(intent string, k int) []vibeflow.Candidate
}

// PlanJudge validates a shortlist. It is an interface for the same
// reason: the orchestrator must not import the judge machinery, and
// the judge is optional (a plan without a judge is still a plan, it
// is just not VALIDATED yet).
type PlanJudge interface {
	// ValidateShortlist returns (validated, note, error). A judge
	// that cannot run returns (false, reason, nil): the plan is
	// still created, and the fact that it was not validated is
	// visible in the result.
	ValidateShortlist(ctx context.Context, intent string, candidates []vibeflow.Candidate) (bool, string, error)
}

// SetPlanRanker wires the candidate source.
func (o *Orchestrator) SetPlanRanker(r ToolRanker) { o.planRanker = r }

// SetPlanJudge wires the shortlist validator.
func (o *Orchestrator) SetPlanJudge(j PlanJudge) { o.planJudge = j }

// PlanCreate proposes a plan for a spec.
//
// The order of operations matters and is deliberate:
//
//  1. resolve the project from the session (SPEC-204) — the write
//     MUST land in the right project;
//  2. read the spec and its intent;
//  3. rank the candidates (the deterministic proposal);
//  4. validate the shortlist with the judge (if there is one);
//  5. derive the tier and the budget from the shortlist;
//  6. persist the plan in status "draft".
//
// The plan is NEVER created in "approved" state: approving is a human
// act, and plan_decide is the only way to reach it.
func (o *Orchestrator) PlanCreate(ctx context.Context, in PlanCreateInput) (*PlanCreateResult, error) {
	if in.SpecID <= 0 {
		return nil, errMissingField("spec_id")
	}
	if strings.TrimSpace(in.Intent) == "" {
		return nil, errMissingField("intent")
	}

	projectID, err := o.resolveCallProject(ctx, "", in.SessionID)
	if err != nil {
		return nil, err
	}

	spec, err := o.Store.GetSpec(ctx, in.SpecID)
	if err != nil {
		return nil, err
	}
	if spec == nil {
		return nil, store.NewFieldError(store.ErrInvalidArgument,
			fmt.Sprintf("plan: spec %d does not exist in this project", in.SpecID))
	}

	// 3. The deterministic proposal.
	var candidates []vibeflow.Candidate
	if o.planRanker != nil {
		k := in.MaxCandidates
		if k <= 0 {
			k = defaultMaxCandidates
		}
		candidates = o.planRanker.Rank(in.Intent, k)
	}

	// 4. The judge validates the shortlist. It does not choose it.
	judged := false
	judgeNote := ""
	if o.planJudge != nil && len(candidates) > 0 {
		ok, note, jerr := o.planJudge.ValidateShortlist(ctx, in.Intent, candidates)
		if jerr != nil {
			// A judge failure does NOT block the plan: the plan is a
			// proposal, and the human still has to approve it. But
			// the fact that it was not validated is REPORTED, not
			// hidden.
			judgeNote = "el juez no pudo validar el shortlist: " + jerr.Error()
		} else {
			judged = ok
			judgeNote = note
			if !ok {
				// The judge rejected the shortlist: the candidates
				// are dropped and the plan starts with an EMPTY
				// shortlist. An empty shortlist authorises nothing,
				// so the plan cannot be used to smuggle a tool past
				// the gate.
				candidates = nil
			}
		}
	}

	// 5. The tier and the budget follow from the shortlist.
	tier := vibeflow.PlanTierForCandidates(len(candidates))
	budget := vibeflow.DefaultBudgetForPlanTier(tier)
	if in.MaxToolCalls > 0 {
		budget.MaxToolCalls = in.MaxToolCalls
	}
	if in.MaxJudgeCalls > 0 {
		budget.MaxJudgeCalls = in.MaxJudgeCalls
	}

	planDoc := vibeflow.Plan{
		Intent:     in.Intent,
		Candidates: candidates,
		PlanTier:   tier,
		Budget:     budget,
		Revision:   0,
	}
	row := &vibeflow.PlanRow{
		SpecID:    in.SpecID,
		SessionID: in.SessionID,
		Plan:      planDoc,
		Status:    vibeflow.PlanDraft,
	}

	// 6. Persist. The write path is "plan_create", so the row lands
	// in write_audit (INV-1) with the decision's actor.
	if err := o.Store.SavePlan(ctx, o.planWriteContext(ctx, in.SessionID, "plan_create", projectID), in.SpecID, row); err != nil {
		return nil, err
	}

	return &PlanCreateResult{
		SpecID:     in.SpecID,
		Status:     string(vibeflow.PlanDraft),
		Plan:       planDoc,
		Tier:       string(tier),
		Candidates: candidates,
		Budget:     budget,
		Judged:     judged,
		JudgeNote:  judgeNote,
	}, nil
}

// PlanGet reads the plan of a spec, or of the open spec of the session.
func (o *Orchestrator) PlanGet(ctx context.Context, in PlanGetInput) (*PlanGetResult, error) {
	var row *vibeflow.PlanRow
	var err error
	if in.SpecID > 0 {
		row, err = o.Store.GetPlan(ctx, in.SpecID)
	} else {
		if strings.TrimSpace(in.SessionID) == "" {
			return nil, errMissingField("session_id")
		}
		row, err = o.Store.GetOpenPlan(ctx, in.SessionID)
	}
	if err != nil {
		return nil, err
	}
	if row == nil {
		return &PlanGetResult{Found: false}, nil
	}
	return &PlanGetResult{
		SpecID:    row.SpecID,
		Status:    string(row.Status),
		Plan:      row.Plan,
		Decision:  string(row.Decision),
		DecidedBy: row.DecidedBy,
		DecidedAt: row.DecidedAt,
		Budget:    row.EffectiveBudget(),
		Found:     true,
	}, nil
}

// PlanDecide records the human decision on a plan.
//
// This is the tool that makes the approval real, and therefore the one
// that MUST leave a row in write_audit (INV-1): the decision is the
// moment where the kernel stops proposing and starts obeying a human.
//
// The three decisions:
//
//	execute — approve as it is. The plan becomes "approved".
//	edit    — reopen the plan with the reviewer's prompt. The plan
//	          becomes "edited" (historical) and its live state goes
//	          back to "draft": the reviewer changed the proposal, so
//	          it has to be approved AGAIN. An edit is NOT a rejection
//	          (Zapier HITL: the reviewer can edit the proposal before
//	          approving; Jira: rejecting can stop OR redirect).
//	cancel  — stop the plan. The plan becomes "cancelled".
func (o *Orchestrator) PlanDecide(ctx context.Context, in PlanDecideInput) (*PlanDecideResult, error) {
	// SPEC-204: resolve the project from the named session BEFORE
	// anything else. If the resolution fails, the operation fails: it
	// must never degrade to the mutable global slot (H-1).
	projectID, err := o.resolveCallProject(ctx, "", in.SessionID)
	if err != nil {
		return nil, err
	}

	decision, ok := vibeflow.ParseDecision(in.Decision)
	if !ok {
		return nil, store.NewFieldError(store.ErrInvalidArgument,
			"decision: must be one of execute, edit, cancel")
	}
	if decision == vibeflow.DecisionEdit && strings.TrimSpace(in.EditPrompt) == "" {
		return nil, errMissingField("edit_prompt")
	}

	var row *vibeflow.PlanRow
	if in.SpecID > 0 {
		row, err = o.Store.GetPlan(ctx, in.SpecID)
	} else {
		if strings.TrimSpace(in.SessionID) == "" {
			return nil, errMissingField("session_id")
		}
		row, err = o.Store.GetOpenPlan(ctx, in.SessionID)
	}
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, store.NewFieldError(store.ErrInvalidArgument, "plan: there is no plan to decide")
	}

	decidedBy := in.DecidedBy
	if decidedBy == "" {
		decidedBy = "human"
	}
	decidedAt := o.now().UTC().Format(time.RFC3339Nano)

	row.Decision = decision
	row.DecidedBy = decidedBy
	row.DecidedAt = decidedAt

	switch decision {
	case vibeflow.DecisionExecute:
		row.Status = vibeflow.PlanApproved
	case vibeflow.DecisionEdit:
		// The reviewer changed the proposal. The live plan goes back
		// to draft so that it has to be approved again, and the
		// revision counter records that a revision happened.
		row.Plan.Revision++
		row.Plan.Intent = strings.TrimSpace(in.EditPrompt)
		row.Status = vibeflow.PlanEdited
	case vibeflow.DecisionCancel:
		row.Status = vibeflow.PlanCancelled
	}

	// The write. write_audit gets the row with write_path
	// "plan_decide" and the actor (INV-1).
	if err := o.Store.SavePlan(ctx, o.planWriteContext(ctx, in.SessionID, "plan_decide", projectID), row.SpecID, row); err != nil {
		return nil, err
	}

	return &PlanDecideResult{
		SpecID:    row.SpecID,
		Decision:  string(decision),
		Status:    string(row.Status),
		DecidedBy: decidedBy,
		DecidedAt: decidedAt,
		Revision:  row.Plan.Revision,
		Plan:      row.Plan,
	}, nil
}

// planWriteContext builds the WriteContext of a plan write. The
// WritePath is the TOOL name, so write_audit says which tool made the
// change.
func (o *Orchestrator) planWriteContext(ctx context.Context, sessionID, writePath, projectID string) store.WriteContext {
	wc := store.WriteContext{
		Actor:     writePath,
		SessionID: sessionID,
		WritePath: writePath,
		// SPEC-204: the project resolved from the session is the
		// project the write lands in. It is carried explicitly so the
		// store applies the SAME identity the orchestrator resolved,
		// instead of resolving it a second time and possibly getting a
		// different answer.
		ProjectID: projectID,
	}
	if wc.SessionID == "" {
		wc.SessionID = o.sessionIDFromContext(ctx)
	}
	return wc
}

// sessionIDFromContext returns the session that owns the call.
//
// The gate resolves the identity ONCE and puts it in the context
// (SPEC-204: "the identity is resolved in one place and travels to
// where it is applied"). Reading it from here is the same fact the
// store will use, so the write cannot land in another project.
//
// When the context carries no scope (a direct orchestrator call in a
// test, for instance), it returns the empty string and the store
// applies its own chain.
func (o *Orchestrator) sessionIDFromContext(ctx context.Context) string {
	if sc, ok := session.ScopeFromContext(ctx); ok {
		return sc.SessionID
	}
	return ""
}

// defaultMaxCandidates is the size of the shortlist when the caller
// does not say. It is deliberately small: the shortlist is a
// PROPOSAL, and a long one defeats the purpose (measured: the number
// of tools interacts negatively with system performance, arXiv
// 2512.08296).
const defaultMaxCandidates = 8
