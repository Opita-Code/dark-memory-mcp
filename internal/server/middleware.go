// Package server — middleware.go: the gate-aware tool wrapper.
//
// This file is the missing link between the gate (PreCheck + PostCheck,
// defined in internal/policy/gate.go) and the MCP server's tool
// dispatch (defined in server.go wrapHandler). Pre-2.0.1, the gate
// existed as a unit-testable library but was not invoked on the
// transport path — every tool call went straight to its Handler,
// bypassing scope / capability / persona / drift checks.
//
// # Atomicity contract
//   - ONE public type: GateMiddleware
//   - ONE public method: Wrap(toolName string, args json.RawMessage,
//     inner func(ctx, raw) (*tools.ToolResponse, error)) (*tools.ToolResponse, error)
//   - DEPENDS on internal/policy (PreCheck, PostCheck, GateInput,
//     PostCheckInput, Reason → ToolError translation)
//   - DEPENDS on internal/drift (drift.Checker via the DriftChecker field)
//   - DEPENDS on internal/tools (ToolResponse, ToolError)
//
// # Per-tool configuration
//
// PreCheck is unconditional: every tool call is checked against
// identity / capability / persona / scope. PostCheck (drift-at-write)
// is conditional on IsArtifactCreating(toolName) — only artifact-creating
// tools (vibe_publish, vibe_spec, resolve_drift, judge, consensus, etc.)
// run drift. Read-only introspection tools (health_ping, memory_state,
// session_status, pipeline_status, etc.) skip drift.
//
// # SessionID resolution
//
// Some tools take session_id as an explicit input arg; others inherit
// the active session from the active SessionHandle (the resolved
// session_id at the request envelope level — set by the harness before
// dispatch, or by dark_memory_session_start). The Middleware consults
// the args first, then falls back to ActiveSessionID.
//
// # Why a middleware type here and not in policy/
//
// policy/ already imports atomic + drift. Adding tools import there
// would create a cycle (tools/recall.go uses policy.FrameSource).
// server/ is the natural glue layer — it already imports both
// policy (via the gate) and tools (via the registry). The Middleware
// keeps policy decisions in policy/ and transport glue here.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/drift"
	"github.com/dark-agents/dark-memory-mcp/internal/plan"
	"github.com/dark-agents/dark-memory-mcp/internal/policy"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// ActiveSessionResolver returns the currently-active session_id for
// the project scope, or "" if no session is active. Used by
// GateMiddleware when a tool's args don't carry session_id.
//
// v2.0.2 added ctx: the StoreBackedActiveSessionResolver needs a
// context for its DB read. StaticSessionResolver ignores the
// context.
//
// Implementations live in the server layer (which has access to the
// per-project active session table); the gate itself only depends
// on the interface so it can be tested with stubs.
type ActiveSessionResolver interface {
	ActiveSessionID(ctx context.Context, projectID string) string
}

// StaticSessionResolver returns the same session_id for every project.
// Used by tests and by harnesses that bind a single session per
// server lifetime (e.g. the opencode-style one-session-per-server).
type StaticSessionResolver struct {
	SessionID string
}

// ActiveSessionID implements ActiveSessionResolver. Ignores ctx.
func (s StaticSessionResolver) ActiveSessionID(_ context.Context, _ string) string {
	return s.SessionID
}

// GateMiddleware wires PreCheck + PostCheck around a tool handler.
// One GateMiddleware is constructed per Server (BootState carries it
// as `Gate *server.GateMiddleware`).
type GateMiddleware struct {
	FrameSource   policy.FrameSource
	DriftChecker  *drift.Checker
	ActiveSession ActiveSessionResolver

	// PlanGate is the SPEC-194 gate of the plan. It is OPTIONAL in
	// the same way DriftChecker is optional: when it is nil, the
	// server behaves exactly as it did before this change (no plan is
	// checked). A server that does not wire it keeps working.
	//
	// It is a field of the middleware and not a step of
	// policy.PreCheck on purpose. policy.PreCheck is a pure function
	// over FRAMES: it composes identity, capabilities, persona and
	// scope and refuses when one of them is missing. The plan is not
	// a frame: it lives in the store, it changes with every decision,
	// and the gate has to CHARGE it (a write). A store write does not
	// belong inside a function whose contract is "compose frames and
	// decide".
	//
	// The middleware is the only place where a tool call is a real
	// call with a real session, so it is the only place that can
	// both ask the gate and pay for the answer.
	PlanGate *plan.Gate

	// PlanStrictness is the operator's switch (SPEC-194 T4,
	// DARK_PLAN_STRICTNESS): off | warn | strict.
	//
	//   - off: the plan is not checked at all.
	//   - warn: the plan IS checked; a refusal is RECORDED (in the
	//     Error Observatory, through RecordRefusal) and the call is
	//     let through. This is the default of the switch: the gate is
	//     born measuring, not born blocking.
	//   - strict: a refusal stops the call.
	//
	// The default of the switch is warn; the zero value of the field
	// is off, so a middleware built without asking the environment
	// does not start refusing calls.
	PlanStrictness plan.Strictness
	// ActiveProject returns the currently-active project_id (mirrors
	// bootState.Store.ActiveProject). Used as a fallback by
	// buildGateInput when args lacks project_id — many session-required
	// tools (agent_memory_*, session_status, session_close) don't carry
	// project_id in args and derive it from the active project at the
	// orchestrator layer (INV-7). v2.1.1 added this field to fix the
	// regression where these tools refused with ErrFrameStaleTooFar
	// because buildGateInput was passing projectID="" to the resolver,
	// which short-circuited without consulting the store.
	ActiveProject func() string
	// SessionProject returns the project that owns the given session_id
	// (SPEC-203). It is how the gate resolves the scope of a call that
	// names its session: the session is the authority, and the global
	// slot cannot answer for it.
	//
	// Optional: when nil, the project falls back to ActiveProject.
	SessionProject     func(ctx context.Context, sessionID string) string
	ActiveConstitution func() (id, ver string)
	Now                func() time.Time
	// RecordRefusal (v2.11.0, spec 757) is the optional error-telemetry
	// hook. When set, the middleware calls it for every PreCheck /
	// PostCheck refusal BEFORE returning the refusal ToolError — the
	// gate's leading-indicator refusals (ErrFrameStaleTooFar,
	// ErrDriftAtWrite, scope/capability expiry) land in the Error
	// Observatory durably. Boot wires this to
	// orchestration.Orchestrator.RecordError. Best-effort by contract:
	// a telemetry failure must never change the refusal path.
	RecordRefusal func(ctx context.Context, toolName, sessionID, code, message string)
}

// Wrap runs PreCheck → inner → optional PostCheck around the given
// tool handler. The returned ToolResponse carries either the inner
// response (allowed), a PreCheck refusal ToolError (pre), or a
// PostCheck drift refusal ToolError (post).
//
// Decision matrix:
//
//	PreCheck !Allowed
//	    → refusal ToolError (no inner call, no PostCheck).
//	PreCheck Allowed, !IsArtifactCreating(toolName)
//	    → call inner; return result verbatim.
//	PreCheck Allowed, IsArtifactCreating(toolName), DriftChecker == nil
//	    → call inner; return result verbatim (legacy v2.0.0 behavior).
//	PreCheck Allowed, IsArtifactCreating(toolName), DriftChecker != nil
//	    → call inner; run PostCheck; on Allowed return inner result;
//	      on refused return drift refusal ToolError.
//
// All refusals preserve errors.Is / errors.As semantics: callers
// can still branch on the reason codes (ErrGateOK, ErrScopeRequired,
// etc.) via the Error.Code field.
func (m *GateMiddleware) Wrap(
	ctx context.Context,
	toolName string,
	args json.RawMessage,
	inner func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error),
) (*tools.ToolResponse, error) {
	if m == nil {
		// No middleware wired (legacy path or test bypass): just call
		// inner. Defensive — production always constructs a GateMiddleware
		// at boot; this branch protects callers that build a Server
		// without going through Boot (e.g. one-off tests).
		return inner(ctx, args)
	}

	// --- PreCheck ---
	now := m.now()
	gateIn := m.buildGateInput(ctx, toolName, args, now)

	pre, err := policy.PreCheck(ctx, m.FrameSource, gateIn)
	if err != nil {
		// PreCheck itself returned an error (e.g. ctx cancelled).
		// Surface as ErrInternal — the gate contract says it
		// returns Allowed=false with a Reason on policy refusal,
		// not a Go error.
		// v2.11.0 (spec 757): capture durably (was wire-only).
		m.recordRefusal(ctx, toolName, gateIn.SessionID, "ErrInternal", fmt.Sprintf("pre_check error: %v", err))
		return refusalResponse(policy.ReasonOK, fmt.Sprintf("pre_check error: %v", err), ""), nil
	}
	if !pre.Allowed {
		// v2.11.0 (spec 757): gate refusals are a leading indicator
		// of drift (stale sessions, expired capabilities, scope
		// violations) — previously they existed only on the wire.
		// Now they land in the Error Observatory (domain=gate).
		m.recordRefusal(ctx, toolName, gateIn.SessionID, pre.Reason.ErrorKind(), pre.Message)
		return refusalResponse(pre.Reason, pre.Message, pre.Hint), nil
	}

	// --- Inner ---
	//
	// SPEC-204: the identity resolved above travels with the call.
	//
	// The middleware is where the session is resolved (buildGateInput
	// already did it for the gate). Until now that answer died here:
	// the store resolved the same question again, on its own, and the
	// two answers could disagree — which is how a row written in the
	// project of the session became invisible to a later read.
	//
	// The scope carries the project OF THE SESSION (the authority),
	// not the explicit project_id of the call: an explicit project may
	// contradict the session and is a narrowing, never a widening.
	//
	// From here on, NO tool has to declare session_id for its write to
	// land in the project of the session that asked for it.
	scope := session.Scope{SessionID: gateIn.SessionID}
	if gateIn.SessionID != "" && m.SessionProject != nil {
		scope.ProjectID = m.SessionProject(ctx, gateIn.SessionID)
	}
	ctx = session.WithScope(ctx, scope)

	// --- The gate of the plan (SPEC-194, T2) ---
	//
	// It sits HERE, after the scope is resolved and before inner,
	// for two reasons:
	//
	//   - The scope is needed: the charge writes to the project of
	//     the session (INV-7), and the WriteContext has to carry it.
	//   - The charge has to happen BEFORE the call runs, and the
	//     call is inner.
	//
	// The order of the checks is the one of SPEC-194 §3.4-bis:
	//
	//   1. is the tool exempt (the lifecycle of the plan)? -> pass
	//   2. does the tool demand a plan?                  -> if not, pass
	//   3. the gate decides: plan_required / not_approved /
	//      budget_exceeded
	//
	// In `warn` mode a refusal is RECORDED and the call goes
	// through: the gate is born measuring, not born blocking
	// (SPEC-194 §3.6, D8). In `strict` mode the refusal stops the
	// call. In `off` mode nothing is checked at all.
	if m.PlanStrictness != plan.StrictnessOff && m.PlanGate != nil && !PlanLifecycleExempt(toolName) && IsPlanRequired(toolName) {
		decision, err := m.PlanGate.Check(ctx, gateIn.SessionID, toolName,
			isJudgeCallTool(toolName), m.planWriteContext(ctx, gateIn, toolName))
		if err != nil {
			// A broken store is not a refusal: it is an error.
			// Under `strict` the call stops (the gate could not
			// be evaluated, and "could not evaluate" is not
			// "allowed"); under `warn` it goes through and the
			// error is recorded, because a store that cannot be
			// read must not stop the work.
			m.recordRefusal(ctx, toolName, gateIn.SessionID, "ErrInternal",
				fmt.Sprintf("plan gate: %v", err))
			if m.PlanStrictness == plan.StrictnessStrict {
				return refusalResponse(policy.ReasonOK,
					fmt.Sprintf("plan gate: %v", err),
					"Operator: the plan gate could not read the plan; check the store"), nil
			}
		} else if decision != nil && !decision.Allowed {
			code := planReasonCode(decision.Reason)
			// The refusal is recorded in BOTH modes: the Error
			// Observatory (which is where the KPI of T10 reads
			// from) and, in `warn`, the call goes through.
			//
			// The charge did NOT happen: the gate refuses BEFORE
			// it charges, so a call that the plan did not
			// authorise does not consume the plan's allowance.
			// Charging it would mean the unauthorised calls eat
			// the budget of the authorised ones.
			m.recordRefusal(ctx, toolName, gateIn.SessionID, code, decision.Message)
			if m.PlanStrictness == plan.StrictnessStrict {
				return &tools.ToolResponse{
					Error: &tools.ToolError{
						Code:    code,
						Message: decision.Message,
						Field:   "plan",
						Hint:    decision.Hint,
					},
				}, nil
			}
		}
	}

	resp, err := inner(ctx, args)
	if err != nil {
		// Inner returned a Go error. wrapHandler maps this to
		// ToolError{Code:ErrInternal}, but we keep the convention
		// here too so callers can branch.
		return &tools.ToolResponse{
			Error: &tools.ToolError{
				Code:    tools.ErrInternal,
				Message: err.Error(),
			},
		}, nil
	}

	// --- PostCheck (drift-at-write on artifact-creating tools) ---
	if m.DriftChecker != nil && IsArtifactCreating(toolName) && resp.Error == nil {
		artifact := buildArtifactInput(toolName, args, resp)
		post := policy.PostCheck(ctx, policy.PostCheckInput{
			Pre:           pre,
			Response:      resp.Data,
			ComposedAt:    now,
			DriftChecker:  m.DriftChecker,
			DriftArtifact: artifact,
		})
		if !post.Allowed {
			// v2.11.0 (spec 757): drift-at-write refusals captured
			// durably (was wire-only).
			m.recordRefusal(ctx, toolName, gateIn.SessionID, "ErrDriftAtWrite", post.Message)
			return &tools.ToolResponse{
				Error: &tools.ToolError{
					Code:    "ErrDriftAtWrite",
					Message: post.Message,
					Field:   "drift",
					Hint:    post.Hint,
				},
			}, nil
		}
	}

	return resp, nil
}

// buildGateInput assembles the GateInput from the tool name + args.
//
// Resolution order (v2.1.1 update):
//
//	project_id: args.project_id if present and non-empty,
//	            else m.ActiveProject() (the bootState.Store.ActiveProject),
//	            else "" (bootstrap case).
//	session_id: args.session_id if present and non-empty,
//	            else m.ActiveSession.ActiveSessionID(ctx, project_id)
//	            (uses the resolved project_id so the resolver
//	            doesn't short-circuit on "").
//
// The ActiveProject fallback fixes the v2.1.0 regression where
// session-required tools without project_id in args (agent_memory_*,
// session_status, session_close) refused with ErrFrameStaleTooFar:
// the resolver's "projectID == ”" short-circuit returned "" without
// consulting the store, so GateInput had SessionID="" which PreCheck
// reads as "no session".
//
// The bootstrap case (no args.project_id AND no ActiveProject) is
// preserved: SessionID="" + ProjectID="" means PreCheck refuses with
// ReasonFrameStale — which is correct for session_start itself,
// where the session_id is the OUTPUT, not an input.
func (m *GateMiddleware) buildGateInput(ctx context.Context, toolName string, args json.RawMessage, now time.Time) policy.GateInput {
	in := policy.GateInput{
		ToolName: toolName,
		Now:      now,
		Args:     decodeArgsMap(args),
	}

	// 1. Resolve session_id FIRST (args wins, then the resolver).
	//
	// SPEC-203: the order matters. The session is the authority for
	// its own scope, so it has to be known before the project: the
	// project of a call that names its session is the project that
	// owns it, not whatever the global slot points at (the slot is
	// moved by every client of the server process).
	if id, ok := in.Args["session_id"].(string); ok && id != "" {
		in.SessionID = id
	} else if m.ActiveSession != nil {
		// The resolver needs a project to ask about; the active one
		// is the only sensible guess when the caller named none.
		in.SessionID = m.ActiveSession.ActiveSessionID(ctx, m.activeProject())
	}

	// 2. Resolve project_id: args wins; then the project of the named
	// session; then the active project.
	projectID := ""
	if pid, ok := in.Args["project_id"].(string); ok && pid != "" {
		projectID = pid
	} else if in.SessionID != "" && m.SessionProject != nil {
		projectID = m.SessionProject(ctx, in.SessionID)
	}
	if projectID == "" {
		projectID = m.activeProject()
	}
	in.ProjectID = projectID

	// constitution_id/ver come from the active constitution at the
	// server level. The gate doesn't read env directly; the caller
	// (Boot) populates these when constructing the GateMiddleware.
	if cid, ok := in.Args["constitution_id"].(string); ok && cid != "" {
		in.ConstitutionID = cid
	} else if m.ActiveConstitution != nil {
		in.ConstitutionID, in.ConstitutionVer = m.ActiveConstitution()
	}
	if cv, ok := in.Args["constitution_ver"].(string); ok && cv != "" {
		in.ConstitutionVer = cv
	}

	return in
}

// now returns the current time, preferring the injected clock.
func (m *GateMiddleware) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

// activeProject returns the active project id, tolerating a nil hook.
func (m *GateMiddleware) activeProject() string {
	if m.ActiveProject == nil {
		return ""
	}
	return m.ActiveProject()
}

// recordRefusal forwards a gate refusal to the Error Observatory
// hook when wired (v2.11.0, spec 757). Nil-safe: no hook → no-op.
// Best-effort by contract — the refusal path must never change
// because telemetry failed.
func (m *GateMiddleware) recordRefusal(ctx context.Context, toolName, sessionID, code, message string) {
	if m.RecordRefusal == nil {
		return
	}
	m.RecordRefusal(ctx, toolName, sessionID, code, message)
}

// decodeArgsMap extracts the top-level string→any map from the
// raw JSON. Returns an empty map on parse failure or empty input
// (defensive — gate checks treat missing args as "no constraint").
func decodeArgsMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// refusalResponse builds the ToolResponse emitted on PreCheck refusal.
// The Reason code is mapped to its canonical error_kind via Reason.ErrorKind().
func refusalResponse(r policy.Reason, message, hint string) *tools.ToolResponse {
	if r == policy.ReasonOK {
		// defensive: shouldn't happen, but if a caller passes OK as
		// the "refusal" reason, surface as ErrInternal instead of
		// claiming success.
		r = "ErrInternal"
	}
	return &tools.ToolResponse{
		Error: &tools.ToolError{
			Code:    r.ErrorKind(),
			Message: message,
			Hint:    hint,
		},
	}
}

// IsPlanRequired lists the tools that MUTATE the state of the kernel
// and therefore demand an approved plan (SPEC-194, T2).
//
// It is the THIRD set of tools, next to the two that already existed:
//
//   - IsArtifactCreating: the tools whose OUTPUT goes to the judge.
//   - RequiresActiveSession: the tools that need an identity.
//
// This one is about the tools that CHANGE something, and it is what
// the plan gate watches.
//
// The list is hardcoded on purpose, for the same reason
// IsArtifactCreating is: it is part of the wiring contract. Adding a
// tool here is a change of contract for the harnesses that observe
// ErrPlanRequired.
//
// # The list is deliberately INCOMPLETE on the mutating side
//
// Two decisions that look like omissions and are not:
//
//   - session_close is NOT here. If closing a session demanded an
//     approved plan, a session whose plan is still in draft could
//     never be closed, and the plan could never be approved either
//     (the decision needs a live session). That is a trap, and a gate
//     that can trap the operator is a gate with an escape hatch
//     nobody told you about. Closing a session is always allowed.
//
//   - The tools of the plan's own lifecycle (plan_create, plan_get,
//     plan_decide) are not here either, because they ARE the gate's
//     exception (see PlanLifecycleExempt). Listing them here and
//     exempting them below would be two lists to keep in sync for
//     no reason.
//
// The other omission is on purpose too: this list names the tools
// that MUTATE THE KERNEL. The tools that only READ (agent_memory_list,
// agent_memory_recall, active_policy, memory_state, the context
// projections, the judge utilities) are not here, and there is no
// "read-only" flag on the tools to derive it from: the repo has no
// such notion, so the list IS the notion.
func IsPlanRequired(toolName string) bool {
	switch toolName {
	case "vibe_publish", // publishes the artifact
		"resolve_drift",         // decides about a verdict
		"judge",                 // spends the judge budget
		"consensus",             // spends the judge budget
		"admin_migrate",         // migrates the schema
		"admin_vacuum",          // compacts the database
		"vlp_handle_event",      // moves the VLP state machine
		"agent_memory_save",     // writes memory
		"agent_memory_update",   // writes memory
		"agent_memory_archive",  // writes memory
		"agent_memory_delegate", // creates a subagent binding
		"subagent_register",
		"subagent_unregister",
		"llm_key_add", // touches the OS keyring
		"llm_key_remove",
		"llm_provider_bind", // changes the judge of the project
		"oauth_connect",
		"oauth_disconnect",
		"project_create":
		return true
	}
	return false
}

// PlanLifecycleExempt lists the tools that take part in the
// lifecycle of the plan and therefore do NOT demand an approved plan.
//
// It is the ONLY place where the plan gate can be skipped, and it is
// a CLOSED list. The gate itself has no "skip" parameter on purpose:
// a flag in the arguments would be a door left open, and the one
// caller that must not use it is exactly the one that would.
//
// # Without this list the gate does not start
//
// The plan hangs from a row of vibe_specs (SavePlan refuses a plan
// without a spec), so creating the FIRST plan needs a spec. If
// creating the spec needed an approved plan, the gate would deadlock:
// to make a plan you need a spec, and to make a spec you need a plan.
//
// MEASURED: the independent review of PR #43 found exactly this
// deadlock in the spec, where vibe_spec was in IsPlanRequired and
// not in this list. The spec was corrected (SPEC-194 §3.4-bis) and
// the exemption is here.
//
// The cost of exempting vibe_spec is written down in the spec: it can
// create a spec (and, with DARK_MEMORY_V280=1, its auto-saved todos)
// without a plan. Those are bounded, audited writes, and the work
// that matters keeps demanding an approved plan.
func PlanLifecycleExempt(toolName string) bool {
	switch toolName {
	case "plan_create", // proposes the plan (it is born in "draft")
		"plan_get",    // reads the plan
		"plan_decide", // the person decides about the plan
		"vibe_spec",   // creates the spec the plan hangs from
		"health_ping",
		"memory_state",
		"active_policy":
		return true
	}
	return false
}

// IsArtifactCreating lists tools whose successful response should be
// drift-checked before returning to the LLM (per ACTIVE_MEMORY_RFC.md
// §A6). Read-only introspection tools are NOT in this set.
//
// Exported (capital I) so wire tests can assert against the same set
// the gate uses. The list is intentionally hardcoded (not derived
// from a per-tool metadata field) because it is part of the wire
// contract — adding a tool here is a breaking change for downstream
// drift-judge integrations that observe ErrDriftAtWrite on the
// named tools.
func IsArtifactCreating(toolName string) bool {
	switch toolName {
	case "vibe_publish",
		"vibe_spec",
		"resolve_drift",
		"judge",
		"consensus",
		"load_constitution",
		"admin_migrate",
		"vlp_handle_event":
		return true
	}
	return false
}

// buildArtifactInput packages the minimal artifact description the
// drift.Checker needs. Per the drift.ArtifactInput contract we
// only need SpecID, ArtifactType, ArtifactURL, Text. For tools that
// don't expose a spec_id, the checker returns Verdict.Decision="skipped"
// (no spec to drift against).
func buildArtifactInput(toolName string, args json.RawMessage, resp *tools.ToolResponse) *drift.ArtifactInput {
	argsMap := decodeArgsMap(args)

	artifact := &drift.ArtifactInput{
		ArtifactType: toolName,
	}

	if id, ok := argsMap["spec_id"]; ok {
		switch v := id.(type) {
		case float64:
			artifact.SpecID = int64(v)
		case int64:
			artifact.SpecID = v
		case int:
			artifact.SpecID = int64(v)
		}
	}

	// ArtifactURL + Text are best-effort from the response. Many
	// tools return a JSON object with arbitrary keys; we copy the
	// canonical "url" + "text"/"body" keys when present.
	if resp != nil && resp.Data != nil {
		if dataMap, ok := resp.Data.(map[string]any); ok {
			if u, ok := dataMap["url"].(string); ok {
				artifact.ArtifactURL = u
			}
			for _, key := range []string{"text", "body", "spec"} {
				if t, ok := dataMap[key].(string); ok {
					artifact.Text = t
					break
				}
			}
		}
	}

	return artifact
}

// isJudgeCallTool says whether a tool call spends the JUDGE budget
// instead of the tool-call budget (SPEC-194 §3.8).
//
// The two budgets are distinct ceilings and charging the wrong one
// would refuse a call whose own allowance was still available: a
// judge call charged to the tool budget would eat the allowance of
// the tool calls, and the other way around.
//
// The list is short on purpose: only the two tools that actually
// call the judge as a TOOL spend the judge budget. The judge that
// validates the shortlist of a plan is an INTERNAL call of the
// orchestrator (PlanJudge), not a tool of the harness, and it does
// not pass through this gate.
func isJudgeCallTool(toolName string) bool {
	switch toolName {
	case "judge", "consensus":
		return true
	}
	return false
}

// planReasonCode maps the string reason of the plan gate to the
// canonical error_kind of the gate.
//
// The two packages do not import each other on purpose: the plan
// gate lives with the plan (it needs the store) and the policy
// reasons live with the policy (they are the wire contract). The
// mapping is here, in the layer that knows both, and it is
// exhaustive over the three reasons of SPEC-194: an unknown reason
// maps to ErrPlanRequired, which is the SAFEST of the three (it
// asks for a plan, and a plan cannot be worked around).
func planReasonCode(reason string) string {
	switch reason {
	case plan.ReasonNotApproved:
		return "ErrNotApproved"
	case plan.ReasonBudgetExceeded:
		return "ErrBudgetExceeded"
	case plan.ReasonOK:
		return "ErrGateOK"
	default:
		return "ErrPlanRequired"
	}
}

// planWriteContext builds the WriteContext of the charge of a plan
// call.
//
// The actor is the TOOL NAME, not the operator: this row says "this
// tool call was charged to the plan", and the operator is already in
// the session. The write_path is "plan_charge", which is distinct
// from the "plan_create" / "plan_decide" of the lifecycle, so the
// three kinds of row about a plan can be told apart in the audit.
func (m *GateMiddleware) planWriteContext(ctx context.Context, gateIn policy.GateInput, toolName string) store.WriteContext {
	wc := store.WriteContext{
		Actor:     toolName,
		SessionID: gateIn.SessionID,
		WritePath: plan.WritePathCharge,
	}
	if sc, ok := session.ScopeFromContext(ctx); ok {
		wc.ProjectID = sc.ProjectID
	}
	return wc
}
