// Package policy implements the gate interceptor per
// ACTIVE_MEMORY_RFC.md §3 M2 and §A3 (the refusal table).
//
// The gate is the inversion-of-control primitive (A1): every tools/call
// from the LLM traverses three phases (Pre / Mid / Post) before reaching
// an orchestrator. Gate refuses with typed Reasons when the call is
// out of scope, the GrantedTools don't include the named tool, the
// persona can't be resolved, the frame is too stale, or the response
// drifts on write (A6). Gate emits per-call audit data (INV-1).
//
// # Atomicity contract
//   - ONE package: policy
//   - TWO public functions: PreCheck, PostCheck
//   - FIVE refusal Reasons (matching the RFC §A3 table; ErrSessionNotResurrectable
//     and ErrPolicyGatewayDown are session- and harness-layer concerns, not gate)
//   - DEPENDS on internal/atomic for the Frame types; provides the
//     decision layer that consumes them.
//   - DEPENDS on FrameSource (interface); production wiring uses the
//     recall cache (Wave 5A.ii); tests can use an in-memory map.
//
// # Trust boundary
// Gate trusts its FrameSource — it does not fabricate identity/persona
// itself. The MCP server (4A') is responsible for resolving the
// source at session-start and threading it through every tool call.
// Gate refuses when the source doesn't have a needed frame, never
// invents one.
//
// # Wave placement
// This is 5A.iv. Drift interception (post-hook M6) lands in
// Wave 5A.vi (M6, internal/drift). PostCheck now accepts an
// optional *drift.Checker; when nil, the gate behaves identically
// to the pre-5A.vi stub. When provided, PostCheck runs
// Checker.CheckArtifact for artifact-creating tools and refuses
// with Reason=ReasonDriftAtWrite under strict mode.
// Wiring into the MCP server wraps every tool call with PreCheck→Invoke→PostCheck
// and lands in Wave 4A'.
package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/drift"
)

// Reason is the canonical refusal reason code returned from Gate
// decisions. Persisted in write_audit.notes + drift_log.notes so
// operators can correlate refusals with sessions.
type Reason string

// Canonical reason codes per ACTIVE_MEMORY_RFC.md §A3 (and §A6 for the
// post-hook drift case). Keep in sync with the constitution's
// [refusal] body which lists the 6 typed errors + ErrDriftAtWrite.
const (
	ReasonOK              Reason = "ok"
	ReasonScopeRequired   Reason = "scope_required"
	ReasonCapabilityDenied Reason = "capability_not_granted"
	ReasonPersonaMissing  Reason = "persona_not_resolvable"
	ReasonFrameStale      Reason = "frame_stale_too_far"
	ReasonDriftAtWrite    Reason = "drift_at_write"
)

// ErrorKind returns the canonical error_kind string used in the
// MCP error envelope's `data.error_kind` field. Reverse mapping is
// defined here so MCP wrappers (4A') don't have to know the Reason
// enum.
func (r Reason) ErrorKind() string {
	switch r {
	case ReasonScopeRequired:
		return "ErrScopeRequired"
	case ReasonCapabilityDenied:
		return "ErrCapabilityNotGranted"
	case ReasonPersonaMissing:
		return "ErrPersonaNotResolvable"
	case ReasonFrameStale:
		return "ErrFrameStaleTooFar"
	case ReasonDriftAtWrite:
		return "ErrDriftAtWrite"
	default:
		return "ErrGateOK"
	}
}

// GateInput is the request envelope passed to PreCheck.
type GateInput struct {
	// SessionID identifies the session (links to IdentityFrame.SessionID).
	SessionID string

	// ProjectID identifies the project for INV-7.
	ProjectID string

	// ConstitutionID + Ver are the active binding (links to
	// IdentityFrame.ConstitutionID/Ver).
	ConstitutionID  string
	ConstitutionVer string

	// ToolName is the dark_memory_* (or shadowed via gateway) tool
	// being invoked. Must be in GrantedTools[] for allowed result.
	ToolName string

	// Args are the tool's arguments. Read by gate for scope inference
	// (e.g., a publish with `project_id` arg implies scope on that
	// project).
	Args map[string]any

	// Now is the wall-clock time for staleness checks. Injected
	// (rather than read from `time.Now()`) so tests can pin the
	// clock.
	Now time.Time
}

// FrameSource supplies pre-composed frames. In production the source
// is the recall cache (Wave 5A.ii); the gate does not know or care
// how the frames were composed. The source may return nil + nil
// for any frame, in which case the gate treats the frame as missing.
type FrameSource interface {
	IdentityFrame(ctx context.Context, sessionID string) (*atomic.IdentityFrame, error)
	ScopeFrame(ctx context.Context, sessionID string) (*atomic.ScopeFrame, error)
	CapabilitiesFrame(ctx context.Context, sessionID string) (*atomic.CapabilitiesFrame, error)
	DriftFrame(ctx context.Context, sessionID string) (*atomic.DriftFrame, error)
	PersonaFrame(ctx context.Context, sessionID string) (*atomic.PersonaFrame, error)
}

// PreCheckResult is the outcome of a pre-hook invocation. When
// Allowed is false, Reason tells the caller which gate condition
// failed and Message explains; Hint suggests the next action.
type PreCheckResult struct {
	Allowed bool
	Reason  Reason
	Message string
	Hint    string
	Frames  *ComposedFrames // nil if !Allowed
}

// ComposedFrames is the bundle of frames the gate attached to the
// request envelope. Identity is required (gate refuses if missing);
// the others are optional (their absence is itself a refusal reason
// if the gate's checks require them).
type ComposedFrames struct {
	Identity     *atomic.IdentityFrame
	Scope        *atomic.ScopeFrame
	Capabilities *atomic.CapabilitiesFrame
	Drift        *atomic.DriftFrame
	Persona      *atomic.PersonaFrame
}

// PreCheck composes the frames from src and verifies that the
// requested tool is admissible for the session. Returns Allowed=true
// with the composed frames attached on success; Allowed=false with
// a Reason + Message + Hint on any refusal condition.
//
// Refusal priority (first matching check wins):
//  1. Identity missing / invalid / stale / constitution mismatch
//     → ReasonFrameStale
//  2. Capabilities missing / doesn't grant ToolName
//     → ReasonCapabilityDenied
//  3. Persona missing / constitution-mismatch with identity
//     → ReasonPersonaMissing
//  4. Scope required by tool but absent, OR scope present but
//     project not in GrantedScopes, OR scope session_id mismatch
//     → ReasonScopeRequired
//
// On Allowed=true, every available frame is in Frames; gate does
// not fail the call when scope/drift/persona are absent if the
// tool doesn't require them (e.g. session_resume needs identity
// only).
func PreCheck(ctx context.Context, src FrameSource, in GateInput) (*PreCheckResult, error) {
	if in.SessionID == "" || in.ProjectID == "" {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonFrameStale,
			Message: "session or project not bound",
			Hint:    "Call dark_memory_session_start first",
		}, nil
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	// 1. Identity — required, must be valid + bound to active constitution + fresh.
	identity, err := src.IdentityFrame(ctx, in.SessionID)
	if err != nil || identity == nil {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonFrameStale,
			Message: "identity unavailable for this session",
			Hint:    "Operator: review dark_memory_active_policy or session_start",
		}, nil
	}
	if err := identity.Validate(); err != nil {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonFrameStale,
			Message: fmt.Sprintf("identity invalid: %v", err),
		}, nil
	}
	if identity.ConstitutionID != in.ConstitutionID || identity.ConstitutionVer != in.ConstitutionVer {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonFrameStale,
			Message: fmt.Sprintf("constitution mismatch: identity=%s@%s, input=%s@%s",
				identity.ConstitutionID, identity.ConstitutionVer,
				in.ConstitutionID, in.ConstitutionVer),
			Hint:    "Session needs dark_memory_session_resurrect or constitution rebind",
		}, nil
	}
	if age := in.Now.Sub(identity.ComposedAt()); age > atomic.MaxIdentityFrameAge {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonFrameStale,
			Message: fmt.Sprintf("identity frame stale: age=%s budget=%s", age, atomic.MaxIdentityFrameAge),
			Hint:    "Operator: refresh via dark_memory_recall(scope='session')",
		}, nil
	}
	frames := &ComposedFrames{Identity: identity}

	// 2. Capabilities — required (gate ensures the session has grants).
	caps, err := src.CapabilitiesFrame(ctx, in.SessionID)
	if err != nil || caps == nil {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonCapabilityDenied,
			Message: "no capabilities granted for this session",
			Hint:    "Operator: extend DARK_GRANTS or active mods to grant tools",
		}, nil
	}
	if err := caps.Validate(); err != nil {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonCapabilityDenied,
			Message: fmt.Sprintf("capabilities invalid: %v", err),
		}, nil
	}
	frames.Capabilities = caps
	if !caps.HasGrant(in.ToolName) {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonCapabilityDenied,
			Message: fmt.Sprintf("tool %q not in GrantedTools", in.ToolName),
			Hint:    "Operator: add the tool to DARK_GRANTS or the relevant mod's grant set",
		}, nil
	}

	// 3. Persona — required (the gate applies persona shaping to the
	// response in PostCheck).
	persona, err := src.PersonaFrame(ctx, in.SessionID)
	if err != nil || persona == nil {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonPersonaMissing,
			Message: "no persona resolved for this session (no constitution+brand binding)",
			Hint:    "Operator: review dark_memory_active_policy for active brand binding",
		}, nil
	}
	if err := persona.Validate(); err != nil {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonPersonaMissing,
			Message: fmt.Sprintf("persona invalid: %v", err),
		}, nil
	}
	if err := persona.VerifyAgainstIdentityFrame(identity); err != nil {
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonPersonaMissing,
			Message: fmt.Sprintf("persona-identity mismatch: %v", err),
		}, nil
	}
	frames.Persona = persona

	// 4. Scope — only required for tools that mutate the active spec.
	scope, err := src.ScopeFrame(ctx, in.SessionID)
	switch {
	case err == nil && scope != nil:
		frames.Scope = scope
		// Cross-check session_id with identity.
		if err := scope.VerifyAgainstIdentityFrame(identity); err != nil {
			return &PreCheckResult{
				Allowed: false,
				Reason:  ReasonScopeRequired,
				Message: fmt.Sprintf("scope-identity mismatch: %v", err),
			}, nil
		}
		// Check that scope's project is reachable.
		if scope.HasOpenSpec() && !caps.HasProjectAccess(in.ProjectID) {
			return &PreCheckResult{
				Allowed: false,
				Reason:  ReasonScopeRequired,
				Message: fmt.Sprintf("project %q not in GrantedScopes for this session", in.ProjectID),
				Hint:    "Operator: extend DARK_GRANTS or relevant mod to grant the project",
			}, nil
		}
	case isToolScopeRequired(in.ToolName):
		return &PreCheckResult{
			Allowed: false,
			Reason:  ReasonScopeRequired,
			Message: fmt.Sprintf("tool %q requires scope but no ScopeFrame available", in.ToolName),
			Hint:    "Operator: open a spec via dark_memory_vibe_spec first",
		}, nil
	}

	// 5. Drift — informational at pre-hook; the actual drift check
	// happens at PostCheck (A6). We attach it to the frame bundle so
	// downstream consumers know the prior state.
	drift, err := src.DriftFrame(ctx, in.SessionID)
	if err == nil && drift != nil {
		frames.Drift = drift
	}

	return &PreCheckResult{Allowed: true, Frames: frames}, nil
}

// isToolScopeRequired returns true for tools that need an open spec
// in the session's scope (mutators). Read-only introspection tools
// (session_status, active_policy, health_ping) don't need a scope.
func isToolScopeRequired(toolName string) bool {
	switch toolName {
	case "dark_memory_vibe_publish", "dark_memory_vibe_spec", "dark_memory_resolve_drift":
		return true
	default:
		return false
	}
}

// PostCheckInput is the response envelope passed to PostCheck.
type PostCheckInput struct {
	Pre        *PreCheckResult
	Response   any
	ComposedAt time.Time

	// DriftChecker (Wave 5A.vi, M6) is the optional drift-at-write
	// interceptor. When nil, PostCheck skips drift checking (legacy
	// stub behavior preserved for callers that don't wire M6 yet).
	DriftChecker *drift.Checker

	// DriftArtifact (Wave 5A.vi, M6) is the artifact input the
	// DriftChecker should evaluate. Required when DriftChecker is
	// non-nil AND the tool is artifact-creating (see
	// isToolArtifactCreating). When DriftChecker is set but
	// DriftArtifact is nil, PostCheck skips drift checking.
	DriftArtifact *drift.ArtifactInput
}

// PostCheckResult is the outcome of post-hook drift checking.
type PostCheckResult struct {
	Allowed      bool
	Reason       Reason
	Message      string
	Hint         string
	DriftVerdict string // "aligned" | "drift_detected" | "needs_human" | "skipped"
}

// PostCheck runs drift-at-write on the orchestrator response per
// ACTIVE_MEMORY_RFC.md §A6.
//
// # Behavior matrix (Wave 5A.vi, M6)
//
// DriftChecker=nil OR DriftArtifact=nil OR tool not artifact-creating:
//   → DriftVerdict="skipped", Allowed=true (legacy stub path).
//
// DriftChecker set, tool is artifact-creating, Strictness=off:
//   → DriftVerdict="skipped", Allowed=true.
//
// Strictness=warn, drift_detected or needs_human:
//   → DriftVerdict=verdict, Allowed=true (caller tags
//     validation_status="drift_pending" out-of-band).
//
// Strictness=strict, drift_detected:
//   → Allowed=false, Reason=ReasonDriftAtWrite, Hint="resolve drift
//     via dark_memory_resolve_drift before retrying".
//
// Strictness=strict, needs_human:
//   → Allowed=false, Reason=ReasonDriftAtWrite (conservative — treat
//     human-gate the same as drift_detected under strict mode so the
//     operator gets a clear signal rather than a silent approval).
//
// Strictness=strict, aligned:
//   → DriftVerdict="aligned", Allowed=true.
//
// # Compatibility
// The pre-5A.vi signature had PostCheckInput{Pre, Response, ComposedAt}.
// Adding DriftChecker + DriftArtifact as omitempty fields preserves
// backward compat — existing callers compile unchanged and get the
// "skipped" stub behavior until they opt in to M6.
func PostCheck(ctx context.Context, in PostCheckInput) *PostCheckResult {
	if in.Pre == nil || !in.Pre.Allowed {
		// Pre-hook failed; PostCheck just echoes.
		return &PostCheckResult{
			Allowed: false,
			Reason:  ReasonFrameStale,
			Message: "pre-check did not allow; post-check cannot proceed",
		}
	}

	// Wave 5A.vi (M6): run the drift interceptor if wired. The
	// pre-5A.vi behavior (DriftChecker nil → "skipped") is preserved
	// so existing callers keep working.
	if in.DriftChecker == nil || in.DriftArtifact == nil {
		return &PostCheckResult{
			Allowed:      true,
			DriftVerdict: "skipped",
			Message:      "drift interceptor not wired (Wave 5A.vi M6 opt-in)",
		}
	}

	verdict, err := in.DriftChecker.CheckArtifact(ctx, *in.DriftArtifact)
	if err != nil {
		// Checker returned a hard error (e.g. spec lookup failed).
		// Be conservative under strict mode: refuse. Be permissive
		// under warn mode: log via verdict.Reasoning, allow.
		if in.DriftChecker.Strictness == drift.StrictnessStrict {
			return &PostCheckResult{
				Allowed:      false,
				Reason:       ReasonDriftAtWrite,
				DriftVerdict: "errored",
				Message:      fmt.Sprintf("drift checker errored under strict mode: %v", err),
				Hint:         "Operator: investigate the drift checker; review dark_memory_active_policy",
			}
		}
		return &PostCheckResult{
			Allowed:      true,
			DriftVerdict: "skipped",
			Message:      fmt.Sprintf("drift checker errored; skipped under warn/off: %v", err),
		}
	}

	switch verdict.Decision {
	case "aligned":
		return &PostCheckResult{
			Allowed:      true,
			DriftVerdict: "aligned",
			Message:      verdict.Reasoning,
		}
	case "drift_detected", "needs_human":
		if in.DriftChecker.Strictness == drift.StrictnessStrict {
			return &PostCheckResult{
				Allowed:      false,
				Reason:       ReasonDriftAtWrite,
				DriftVerdict: verdict.Decision,
				Message:      verdict.Reasoning,
				Hint:         "Operator: resolve drift via dark_memory_resolve_drift before retrying the save",
			}
		}
		// Warn mode: allow + log. Caller tags validation_status
		// "drift_pending" out-of-band.
		return &PostCheckResult{
			Allowed:      true,
			DriftVerdict: verdict.Decision,
			Message:      fmt.Sprintf("drift detected (warn mode; saving with drift_pending): %s", verdict.Reasoning),
		}
	default:
		// "skipped" / "errored" / unknown — pass through.
		return &PostCheckResult{
			Allowed:      true,
			DriftVerdict: verdict.Decision,
			Message:      verdict.Reasoning,
		}
	}
}

// isToolArtifactCreating identifies tools that produce artifacts and
// therefore need the drift check. Kept here (rather than in drift/)
// because it's a gate-policy concern, not a drift-package concern.
// Mirrors isToolScopeRequired — same mutator-vs-reader distinction.
func isToolArtifactCreating(toolName string) bool {
	switch toolName {
	case "dark_memory_vibe_publish", "dark_memory_artifact_log":
		return true
	default:
		return false
	}
}
