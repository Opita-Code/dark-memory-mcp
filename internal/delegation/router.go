// Package delegation — router.go: the DECIDE stage of the
// DelegationRouter (Wave 5C).
//
// The router answers A1 (Memory decides) in its most operational
// form: does the LLM handle the intent itself, delegate it to
// sub-agents, or refuse it?
//
// Control flow is HYBRID (SOTA P7 — Stochastic Sandbox): deterministic
// rules per vibe_case cover ~70% of decisions with zero LLM calls;
// conditional cases (C1 code, C2 text in later iterations) use an
// LLM-bounded choice from a fixed menu of strategies. The MVP ships
// the deterministic rules for C7 mixed (always DELEGATE, parallel
// dispatch, no dependencies) and a safe HANDLE fallback for every
// other case.
//
// Per vibe-flow/main/DELEGATION_ARCHITECTURE.md §3:
//
//	C1 code   — CONDITIONAL (delegate if recon/refactor/test-heavy)
//	C2 text   — CONDITIONAL (delegate if specialized audience/tone)
//	C3 image  — REFUSE without an image provider capability
//	C4 video  — ALWAYS pipeline + EU AI Act compliance
//	C5 audio  — ALWAYS pipeline + EU AI Act compliance
//	C6 multi  — ALWAYS supervisor + per-modality workers
//	C7 mixed  — ALWAYS parallel dispatch (independent artifacts)
//
// MVP scope (spec 728): only C7 mixed is fully implemented. All
// other cases fall back to HandlerHandle (safe default: no
// sub-agents spawned, the orchestrator does the work inline).
package delegation

import (
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// RouterInput is what the DECIDE stage needs to make its decision.
// It is deliberately frame-shaped (Identity/Scope/Capabilities) but
// kept minimal for the MVP: the vibe_case + task_description are
// required; Capabilities drives REFUSE decisions in later
// iterations (e.g. C3 image needs an image-generator grant).
type RouterInput struct {
	Case vibecase.Case
	Task string
	// GrantedTools is the set of tool names the session can call
	// (CapabilitiesFrame). Empty in the MVP = "capabilities
	// unknown" → the router must not REFUSE based on missing
	// grants (defaults to HANDLE). When non-empty, REFUSE
	// decisions can be grounded on real gaps.
	GrantedTools []string
}

// Router is the DelegationRouter. Stateless by design: every Decide
// call is pure over the input. State (which subtasks were spawned,
// their bindings, their findings) lives in agent_memory / the store,
// not in the router.
type Router struct{}

// NewRouter returns a Router. Stateless — cheap to construct.
func NewRouter() *Router { return &Router{} }

// Decide runs the DECIDE stage. Returns a DelegationDecision with
// handler HANDLE | DELEGATE | REFUSE and, for DELEGATE, a Plan.
//
// MVP rules (spec 728):
//   - C7 mixed  → ALWAYS DELEGATE. Plan = the task decomposed into
//     independent subtasks (parallel dispatch). The decomposition
//     itself is delegated to the harness: the router emits a single
//     "bundle" subtask per task_description segment the caller
//     provides. When the caller provides no segments, the router
//     emits ONE subtask carrying the parent's case + task — the
//     harness is then expected to re-enter with a finer-grained
//     decomposition (or the orchestrator's PLAN stage handles it in
//     T4+).
//   - Everything else → HANDLE (safe fallback). C1-C6 rules arrive
//     in later iterations of Wave 5C.
//
// REFUSE is never returned in the MVP: without a real
// CapabilitiesFrame the router cannot ground a refusal, and
// refusing everything would be worse than handling inline.
func (r *Router) Decide(in RouterInput) DelegationDecision {
	switch in.Case {
	case vibecase.CaseMixed: // C7
		return r.decideMixed(in)
	case vibecase.CaseImage: // C3 — REFUSE when we KNOW there is no provider
		if hasTool(in.GrantedTools, "image_generate") {
			return DelegationDecision{
				Handler:   HandlerDelegate,
				Reasoning: "C3 image: image_generate capability present; delegating to the image sub-agent",
				Case:      in.Case.String(),
				Plan:      oneSubtaskPlan(in),
			}
		}
		if len(in.GrantedTools) > 0 {
			// Capabilities are known and image_generate is absent —
			// a grounded REFUSE (A1: se rehúsa).
			return DelegationDecision{
				Handler:   HandlerRefuse,
				Reasoning: "C3 image: no image_generate capability in the session's grant set",
				Case:      in.Case.String(),
			}
		}
		// Capabilities unknown → fall through to HANDLE.
		return handleDecision(in)
	case vibecase.CaseVideo, vibecase.CaseAudio, vibecase.CaseMultiModal:
		// C4/C5/C6 are ALWAYS-delegate in the architecture doc, but
		// their pipeline shape (sequential script→storyboard→render
		// + compliance) needs the PLAN stage's dependency batching
		// which lands in a later iteration. MVP: HANDLE.
		return handleDecision(in)
	default:
		// C1 code / C2 text — CONDITIONAL in the full design.
		// MVP: HANDLE (the orchestrator does the work inline; the
		// conditional LLM-bounded check is deferred).
		return handleDecision(in)
	}
}

// decideMixed returns the C7 decision. C7 is a coordinated bundle of
// independent artifacts — always delegable by definition, with
// maximal parallelism (all subtasks in batch 0).
func (r *Router) decideMixed(in RouterInput) DelegationDecision {
	if strings.TrimSpace(in.Task) == "" {
		return DelegationDecision{
			Handler:   HandlerHandle,
			Reasoning: "C7 mixed: empty task description; nothing to delegate",
			Case:      in.Case.String(),
		}
	}
	// A single bundle subtask carrying the parent's identity. The
	// orchestrator's PLAN stage (T4) can decompose this further via
	// mindset_apply per segment; for the MVP the harness receives
	// the bundle and decides how fine-grained to go.
	plan := &Plan{
		Subtasks: []SubTask{
			{
				ID:       "bundle",
				VibeCase: in.Case.String(),
				Task:     in.Task,
			},
		},
	}
	return DelegationDecision{
		Handler:   HandlerDelegate,
		Reasoning: "C7 mixed: always delegable — independent artifact bundle; parallel dispatch",
		Case:      in.Case.String(),
		Plan:      plan,
	}
}

// oneSubtaskPlan builds a single-subtask plan (used by the C3
// delegate path and as a general helper).
func oneSubtaskPlan(in RouterInput) *Plan {
	return &Plan{
		Subtasks: []SubTask{
			{
				ID:       "t1",
				VibeCase: in.Case.String(),
				Task:     in.Task,
			},
		},
	}
}

// handleDecision returns the safe HANDLE default.
func handleDecision(in RouterInput) DelegationDecision {
	return DelegationDecision{
		Handler:   HandlerHandle,
		Reasoning: "not a delegation candidate for the MVP ruleset; handling inline",
		Case:      in.Case.String(),
	}
}

// hasTool reports whether name is in the granted tool list
// (case-insensitive bare-name match, e.g. "image_generate").
func hasTool(tools []string, name string) bool {
	for _, t := range tools {
		if strings.EqualFold(strings.TrimSpace(t), name) {
			return true
		}
	}
	return false
}
