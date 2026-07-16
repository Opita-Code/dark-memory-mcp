// Package opencode is the DMAP v1.1 Layer 6.1 adapter for the OpenCode
// harness (https://opencode.ai). It demonstrates the harness integration
// pattern: the adapter wraps vlp.UseCase.HandleEvent with the wire-format
// JSON envelope that an MCP-compatible harness sends to dark-memory-mcp's
// dark_memory_vlp_handle_event tool.
//
// This package is INTENTIONALLY a thin demo:
//
//   - No HTTP/stdio transport — that's the mcp-go server's job
//     (cmd/dark-mem-mcp). The adapter is a Go-level client.
//   - No retry, circuit breaker, or connection pooling — the harness
//     can layer those on top.
//   - No new types — reuses internal/vlp + internal/store types so the
//     adapter is a Go reference impl, not a parallel API.
//
// The adapter's job is to translate between three layers:
//
//	harness (LLM agent)
//	    | JSON-RPC over stdio
//	    v
//	mcp-go server (cmd/dark-mem-mcp)
//	    | dark_memory_vlp_handle_event
//	    v
//	adapter (this package)  ← wraps UseCase for testing/demo
//	    |
//	    v
//	vlp.UseCase (Layer 2)
//	    |
//	    v
//	store.Store (Layer 0)
//
// Trust boundary: the adapter does NOT validate state transitions
// (UseCase does that). The adapter DOES validate wire-format JSON shape
// and surfaces harness-facing errors as typed values.
//
// Atomicity contract (atomic spec 6.1):
//   - ONE entry point: Adapter.DriveSession
//   - ONE acceptance test: TestAdapter_EndToEnd
//   - ONE PR worth of work (~120 LoC impl + ~120 LoC test)
//   - Direct deps: vlp.UseCase, vlp.ParseEvent, vlp.ParseVerdict
//   - Independently reviewable: no other v1.1 spec touched
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// Adapter wraps a vlp.UseCase with the wire-format JSON envelope that
// an OpenCode (or similar MCP) harness would send. It is the demo
// integration pattern for atomic spec 6.1.
type Adapter struct {
	uc *vlp.UseCase
}

// NewAdapter returns an Adapter bound to the given UseCase. Returns
// an error if uc is nil — same defensive pattern as vlp.NewUseCase.
func NewAdapter(uc *vlp.UseCase) (*Adapter, error) {
	if uc == nil {
		return nil, fmt.Errorf("opencode: NewAdapter: UseCase must not be nil")
	}
	return &Adapter{uc: uc}, nil
}

// DriveRequest is the wire-format envelope an OpenCode harness would
// send. It mirrors the dark_memory_vlp_handle_event tool's input schema.
//
// Event and Verdict are strings (canonical names) on the wire so the
// LLM-facing surface stays human-readable. The adapter translates them
// to vlp.Event / vlp.Verdict enums before calling UseCase.
type DriveRequest struct {
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
	Verdict   string `json:"verdict,omitempty"`
	Minset    string `json:"minset,omitempty"`
}

// DriveResponse is the wire-format envelope returned to the harness.
// Mirrors dark_memory_vlp_handle_event's VLPHandleEventResult.
type DriveResponse struct {
	SessionID  string `json:"session_id"`
	NewState   string `json:"new_state"`
	TurnCount  int    `json:"turn_count"`
	NextAction string `json:"next_action,omitempty"`
	IsTerminal bool   `json:"is_terminal"`
}

// DriveError mirrors dark_memory_vlp_handle_event's ToolError. Used
// by adapters that prefer the typed-error path over error codes.
type DriveError struct {
	Code    string
	Message string
	Hint    string
}

func (e *DriveError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// DriveSession is the single adapter entry point. It takes a wire-format
// JSON envelope (matching what an MCP harness would send), translates
// it to the typed vlp API, and returns a wire-format response envelope.
//
// Wire format: the input is the bytes of the JSON object that an OpenCode
// harness would post to dark_memory_vlp_handle_event's Arguments field.
// Output is the JSON of DriveResponse (or the adapter returns *DriveError).
//
// Error semantics: DriveError.Code matches the MCP ToolError.Code values
// (ErrInvalidArgument, ErrInvalidTransition, ErrInternal). This lets
// the harness reuse the same error-handling code path for both the
// wire-protocol call (via MCP) and the direct adapter call (via Go).
func (a *Adapter) DriveSession(ctx context.Context, rawRequest json.RawMessage) (*DriveResponse, error) {
	var req DriveRequest
	if err := json.Unmarshal(rawRequest, &req); err != nil {
		return nil, &DriveError{
			Code:    "ErrInvalidArgument",
			Message: "input JSON does not match DriveRequest schema: " + err.Error(),
			Hint:    "Inspect the harness wire format and ensure the payload matches DriveRequest fields.",
		}
	}

	if req.SessionID == "" {
		return nil, &DriveError{
			Code:    "ErrInvalidArgument",
			Message: "session_id is required and must be non-empty.",
			Hint:    "OpenCode should call dark_memory_session_start first to obtain a session_id.",
		}
	}

	ev, err := vlp.ParseEvent(req.Event)
	if err != nil {
		return nil, &DriveError{
			Code:    "ErrInvalidArgument",
			Message: "invalid event: " + req.Event,
			Hint:    "Use one of: session_start, vibe_publish, artifact_log, drift_log, abort.",
		}
	}

	var verdict vlp.Verdict
	if req.Verdict != "" {
		verdict, err = vlp.ParseVerdict(req.Verdict)
		if err != nil {
			return nil, &DriveError{
				Code:    "ErrInvalidArgument",
				Message: "invalid verdict: " + req.Verdict,
				Hint:    "Use one of: aligned, drift_detected, needs_human. Required for drift_log; omit otherwise.",
			}
		}
	}

	wc := store.WriteContext{
		Actor:     "opencode-adapter",
		SessionID: req.SessionID,
		WritePath: "vlp_handle_event",
	}

	res, err := a.uc.HandleEvent(ctx, wc, req.SessionID, ev, verdict, req.Minset)
	if err != nil {
		var invalidTransition vlp.ErrInvalidTransition
		if asInvalidTransition(err, &invalidTransition) {
			return nil, &DriveError{
				Code:    "ErrInvalidTransition",
				Message: "no transition from current state on this event: " + invalidTransition.Error(),
				Hint:    "OpenCode should call vlp_handle_event with the canonical next event from the previous response's NextAction.",
			}
		}
		return nil, &DriveError{
			Code:    "ErrInternal",
			Message: err.Error(),
			Hint:    "Retry; if persistent, report to the operator.",
		}
	}

	return &DriveResponse{
		SessionID:  req.SessionID,
		NewState:   res.NewState.String(),
		TurnCount:  res.TurnCount,
		NextAction: res.NextAction,
		IsTerminal: res.IsTerminal,
	}, nil
}

// asInvalidTransition wraps errors.As for vlp.ErrInvalidTransition.
// Kept private — the adapter's public API surfaces only DriveError.
func asInvalidTransition(err error, target *vlp.ErrInvalidTransition) bool {
	return errors.As(err, target)
}
