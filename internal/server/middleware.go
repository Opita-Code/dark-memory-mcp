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
	"github.com/dark-agents/dark-memory-mcp/internal/policy"
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
func (s StaticSessionResolver) ActiveSessionID(_ context.Context, _ string) string { return s.SessionID }

// GateMiddleware wires PreCheck + PostCheck around a tool handler.
// One GateMiddleware is constructed per Server (BootState carries it
// as `Gate *server.GateMiddleware`).
type GateMiddleware struct {
	FrameSource         policy.FrameSource
	DriftChecker        *drift.Checker
	ActiveSession       ActiveSessionResolver
	// ActiveProject returns the currently-active project_id (mirrors
	// bootState.Store.ActiveProject). Used as a fallback by
	// buildGateInput when args lacks project_id — many session-required
	// tools (agent_memory_*, session_status, session_close) don't carry
	// project_id in args and derive it from the active project at the
	// orchestrator layer (INV-7). v2.1.1 added this field to fix the
	// regression where these tools refused with ErrFrameStaleTooFar
	// because buildGateInput was passing projectID="" to the resolver,
	// which short-circuited without consulting the store.
	ActiveProject       func() string
	ActiveConstitution  func() (id, ver string)
	Now                 func() time.Time
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
		return refusalResponse(policy.ReasonOK, fmt.Sprintf("pre_check error: %v", err), ""), nil
	}
	if !pre.Allowed {
		return refusalResponse(pre.Reason, pre.Message, pre.Hint), nil
	}

	// --- Inner ---
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
// the resolver's "projectID == ''" short-circuit returned "" without
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

	// 1. Resolve project_id (args wins, ActiveProject fallback, else "").
	projectID := ""
	if pid, ok := in.Args["project_id"].(string); ok && pid != "" {
		projectID = pid
	} else if m.ActiveProject != nil {
		projectID = m.ActiveProject()
	}
	in.ProjectID = projectID

	// 2. Resolve session_id (args wins, then resolver using the
	//    resolved project_id so the resolver doesn't short-circuit
	//    on an empty projectID).
	if id, ok := in.Args["session_id"].(string); ok && id != "" {
		in.SessionID = id
	} else if m.ActiveSession != nil {
		in.SessionID = m.ActiveSession.ActiveSessionID(ctx, projectID)
	}

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