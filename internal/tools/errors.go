// Package tools — errors.go: typed-error → ToolError translation.
//
// The orchestration layer surfaces typed sentinels (ErrSessionRequired,
// ErrInvalidArgument, ErrCanaryInPayload, etc.). The MCP layer needs
// to convert those into the structured ToolError{Code, Message, Hint}
// shape (RFC §5). This file is the single mapping table; all 7
// sentinels get a one-line Code + Message + Hint, and a catch-all
// handles unknown errors as ErrInternal.
package tools

import (
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// ErrInternal is the catch-all code for un-typed errors. We avoid
// leaking internal details; the underlying error is logged by the
// server layer (server.go) but not surfaced to the LLM.
const ErrInternal = "ErrInternal"

// ToToolError maps any error into a ToolError. Returns nil if err is
// nil. Known sentinels get a stable Code + helpful Hint; unknown
// errors map to ErrInternal with the error string in Message.
func ToToolError(err error) *ToolError {
	if err == nil {
		return nil
	}

	// Sentinel-by-sentinel. We compare with errors.Is for wrapped
	// errors; each mapping gives the LLM a stable Code it can branch
	// on and a Hint it can show the operator.
	switch {
	case errors.Is(err, store.ErrSessionRequired):
		return &ToolError{
			Code:    "ErrSessionRequired",
			Message: "No active session is set. The session lifecycle must be started before any read or write that requires project scoping.",
			Hint:    "Call dark_memory_session_start with operator and project_id, then retry.",
		}
	case errors.Is(err, store.ErrInvalidArgument):
		return &ToolError{
			Code:    "ErrInvalidArgument",
			Message: "One or more arguments failed validation. The offending field is described in the wrapped error.",
			Hint:    "Inspect the error message for the missing or malformed field, correct the input, and retry.",
		}
	case errors.Is(err, store.ErrNotFound):
		return &ToolError{
			Code:    "ErrNotFound",
			Message: "The requested resource does not exist (or is filtered out by project scoping).",
			Hint:    "Verify the id is correct and that the active session's project_id matches the resource's project_id.",
		}
	case errors.Is(err, store.ErrAlreadyExists):
		return &ToolError{
			Code:    "ErrAlreadyExists",
			Message: "A resource with the same unique key already exists in the active project.",
			Hint:    "Choose a different id or update the existing resource instead.",
		}
	case errors.Is(err, store.ErrCanaryInPayload):
		return &ToolError{
			Code:    "ErrCanaryInPayload",
			Message: "The payload contains the active canary token. Per INV-3, the transaction has been rolled back.",
			Hint:    "Remove the canary token from the payload and retry. The canary is a defensive tripwire, not user data.",
		}
	case errors.Is(err, store.ErrInvalidState):
		return &ToolError{
			Code:    "ErrInvalidState",
			Message: "The requested operation is not valid in the current state of the resource (e.g. resolving an already-resolved drift).",
			Hint:    "Inspect the resource state (e.g. via dark_memory_pipeline_status) and retry only if the state is the expected one.",
		}
	case errors.Is(err, store.ErrConstitutionDrift):
		return &ToolError{
			Code:    "ErrConstitutionDrift",
			Message: "The active constitution's SHA256 does not match the file on disk (INV-4). Migrations and writes are refused under drift.",
			Hint:    "Reload the constitution (dark_memory_load_constitution) or align the file with the stored SHA, then restart the server.",
		}
	default:
		// Catch-all. We do NOT leak the raw error string to the LLM
		// (it can contain file paths or partial payloads); the server
		// layer logs the original error for the operator.
		return &ToolError{
			Code:    ErrInternal,
			Message: fmt.Sprintf("Internal error (code=%s). See server logs for the underlying cause.", classifyUnknown(err)),
			Hint:    "Retry; if persistent, check the server logs and report to the operator.",
		}
	}
}

// classifyUnknown returns a short stable code for unknown errors so
// the LLM has something to branch on (we don't surface the raw error
// to avoid leaking file paths or partial payloads).
func classifyUnknown(err error) string {
	// Today: a single bucket "ErrInternal". Future: bucket by error
	// type (e.g. network, parse, IO) once we have more data.
	return ErrInternal
}