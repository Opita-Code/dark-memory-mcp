// Package tools — vlp.go: the L6 VLP wire tool (atomic spec 6.1).
//
// Per RFC D-9 + DMAP v1.1 spec 193 (Layer 6 — Harness Adapters), this is
// the single MCP entry point that drives the VLP state machine from any
// MCP-compatible harness (opencode, claude code, etc.).
//
// The tool is intentionally thin: it translates wire-format JSON
// (event/verdict as strings) into the typed vlp.Event / vlp.Verdict
// enums, delegates to vlp.UseCase.HandleEvent, and formats the
// HandleEventResult back to the wire. All atomicity guarantees live
// in vlp.UseCase + Store layer (F32/F33 hardening). All transition
// validity lives in vlp.Transition (spec 2.1). This tool wrapper does
// NOT enforce invariants — it just translates formats.
//
// Wire format:
//
//	Request:  {"session_id": "...", "event": "vibe_publish",
//	           "verdict": "aligned", "minset": "Recon"}
//	Response: ToolResponse{
//	           Data: &VLPHandleEventResult{
//	             SessionID, NewState, TurnCount, NextAction, IsTerminal},
//	           Next: &NextAction{Tool: "vlp_handle_event", Args: {...}}}
//
// Error cases:
//
//	ErrInvalidTransition: returned when (state, event, verdict) is not
//	  a valid transition. NOT an internal error — it's an expected
//	  runtime condition when the harness fires an event out of order.
//
//	ErrInvalidArgument: returned when event/verdict strings don't
//	  parse to known enum values.
//
// Canonical position: 26 (after the original 25, expanding the
// canonical order per spec 193 Layer 6).
package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// RegisterVLP wires the L6 VLP wire tool into the registry. uc is the
// pre-constructed UseCase; RegisterAll constructs it from the Store
// at boot time (see register.go). Panics on duplicate registration
// (Registry.Add enforces uniqueness).
func RegisterVLP(reg *Registry, uc *vlp.UseCase) {
	if reg == nil {
		panic("tools: RegisterVLP: nil registry")
	}
	if uc == nil {
		panic("tools: RegisterVLP: nil UseCase")
	}

	// vlp_handle_event — single MCP entry point for the VLP loop.
	reg.Add(BindSimple("vlp_handle_event",
		"Drive the VLP state machine by applying an event to a session. Returns the new state, turn count, and next-action hint. The single entry point that opencode/claude code/etc. use to drive a vibe-loop-protocol session through its lifecycle (idle → drafting_spec → spec_active → drift_judging → complete/needs_human/aborted).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"session_id", "event"},
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "The session id (matches dark_memory_session_start's return value).",
				},
				"event": map[string]any{
					"type":        "string",
					"enum":        []string{"session_start", "vibe_publish", "artifact_log", "drift_log", "abort"},
					"description": "Canonical event name. Use 'drift_log' with verdict=aligned/drift_detected/needs_human.",
				},
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []string{"aligned", "drift_detected", "needs_human"},
					"description": "Required for drift_log; ignored (or omit) for all other events.",
				},
				"minset": map[string]any{
					"type":        "string",
					"description": "Current minset mode (Recon/Exploit/Adjudicate/Synthesize/Hunt). Empty = none.",
				},
			},
		}),
		vlpHandleEventHandler(uc)))
}

// vlpHandleEventHandler returns the handler func for vlp_handle_event.
// Extracted so the handler logic can be unit-tested directly without
// going through the Registry.
func vlpHandleEventHandler(uc *vlp.UseCase) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
		var in VLPHandleEventInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "input JSON does not match expected schema for vlp_handle_event: " + err.Error(),
				Hint:    "Inspect the tool's input schema and ensure the payload matches the declared fields.",
			}}, nil
		}

		// Wire-layer input validation (defense in depth — UseCase also
		// validates these, but doing it here gives the LLM a clearer
		// error message and avoids spinning up a UseCase call for an
		// obviously-bad request).
		if in.SessionID == "" {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "session_id is required and must be non-empty.",
				Hint:    "Call dark_memory_session_start first to obtain a session_id, then pass it here.",
			}}, nil
		}

		// Translate string → typed enum. Both ParseEvent and ParseVerdict
		// return ErrInvalidArgument-shaped errors for unknown values.
		ev, err := vlp.ParseEvent(in.Event)
		if err != nil {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "invalid event: " + in.Event,
				Hint:    "Use one of: session_start, vibe_publish, artifact_log, drift_log, abort.",
			}}, nil
		}

		var verdict vlp.Verdict
		if in.Verdict != "" {
			verdict, err = vlp.ParseVerdict(in.Verdict)
			if err != nil {
				return &ToolResponse{Error: &ToolError{
					Code:    "ErrInvalidArgument",
					Message: "invalid verdict: " + in.Verdict,
					Hint:    "Use one of: aligned, drift_detected, needs_human. Required for drift_log; omit otherwise.",
				}}, nil
			}
		}

		// Build WriteContext. ProjectID is auto-filled by the Store
		// from the active project if left empty (INV-7). SessionID
		// here is the OPERATIONAL session id, which may differ from
		// the VLP session_id; we use the VLP session_id because it's
		// the one indexed by write_audit and ties the audit trail
		// together.
		wc := store.WriteContext{
			Actor:     "vlp_handle_event",
			SessionID: in.SessionID,
			WritePath: "vlp_handle_event",
		}

		res, err := uc.HandleEvent(ctx, wc, in.SessionID, ev, verdict, in.Minset)
		if err != nil {
			// Special-case ErrInvalidTransition: it's an EXPECTED
			// runtime condition (harness fired an event out of order),
			// not an internal error. Return a typed error the LLM
			// can branch on.
			var invalidTransition vlp.ErrInvalidTransition
			if errors.As(err, &invalidTransition) {
				return &ToolResponse{Error: &ToolError{
					Code:    "ErrInvalidTransition",
					Message: "no transition from current state on this event: " + invalidTransition.Error(),
					Hint:    "Read the VLP state via dark_memory_session_status or call vlp_handle_event with the canonical next event from the previous response's NextAction.",
				}}, nil
			}
			return &ToolResponse{Error: ToToolError(err)}, nil
		}

		out := &VLPHandleEventResult{
			SessionID:  in.SessionID,
			NewState:   res.NewState.String(),
			TurnCount:  res.TurnCount,
			NextAction: res.NextAction,
			IsTerminal: res.IsTerminal,
		}

		// Suggest the same tool as the next step (harness drives the
		// loop by calling vlp_handle_event repeatedly). When terminal,
		// omit Next so the harness knows the loop has converged.
		var next *NextAction
		if !res.IsTerminal && res.NextAction != "" {
			next = &NextAction{
				Tool: "vlp_handle_event",
				Args: map[string]any{"event": res.NextAction},
				When: NextActionAlways,
				Reason: "next VLP event in the canonical sequence for state " + res.NewState.String(),
			}
		}

		return &ToolResponse{Data: out, Next: next}, nil
	}
}

// VLPHandleEventInput is the input for vlp_handle_event.
type VLPHandleEventInput struct {
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
	Verdict   string `json:"verdict,omitempty"`
	Minset    string `json:"minset,omitempty"`
}

// VLPHandleEventResult is the output for vlp_handle_event.
type VLPHandleEventResult struct {
	SessionID  string `json:"session_id"`
	NewState   string `json:"new_state"`             // canonical state name (idle, drafting_spec, ...)
	TurnCount  int    `json:"turn_count"`            // incremented after each successful transition
	NextAction string `json:"next_action,omitempty"` // canonical event name expected next; "" if terminal
	IsTerminal bool   `json:"is_terminal"`           // true for Complete / NeedsHuman / Aborted
}
