// Package tools — wiring.go: orchestrator → Tool adapter helpers.
//
// Each tool namespace file (session.go, research.go, vibe.go, ...)
// calls BindOrchestrator (or BindSimple) to wrap an orchestrator
// method as a Tool. The adapter is intentionally tiny: the input
// JSON is unmarshalled into a typed struct, the orchestrator method
// is called, and the typed result is wrapped in ToolResponse{Data: ...}.
//
// We don't try to be clever with reflection-driven schema generation
// (that bloats the binary and hides contract drift). Instead, each
// tool hand-supplies its InputSchema as a json.RawMessage — the
// schema lives next to the tool, easy to read and audit.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// BindSimple is the simplest possible adapter: it takes a handler
// that already returns a ToolResponse and wraps it with the
// JSON-decode → call → JSON-encode envelope mcp-go expects.
//
// Use this when the handler does its own decode/encode (e.g. for
// tools with non-trivial schema). For most orchestrator-backed tools
// prefer BindOrchestrator.
func BindSimple(name, description string, schema json.RawMessage, h HandlerFunc) *Tool {
	return &Tool{
		Name:        name,
		Description: description,
		InputSchema: schema,
		Handler:     h,
	}
}

// BindOrchestrator wraps a typed orchestrator method as a Tool. The
// orchestratorFn signature is:
//
//	func(ctx context.Context, in InType) (*OutType, error)
//
// On call, the adapter:
//
//  1. Decodes the raw JSON input into InType (json.Unmarshal).
//  2. Calls orchestratorFn(ctx, in).
//  3. Wraps the typed *OutType as ToolResponse{Data: out}.
//  4. On error, maps via ToToolError → ToolResponse{Error: ...}.
//  5. Returns ToolResponse, nil (the MCP envelope; never returns a
//     Go error from here — errors are surfaced as ToolError).
//
// Audit and Next are NOT populated by this adapter; tools that
// produce audit rows or next-step hints should call the higher-level
// BindOrchestratorWithAudit (added in a follow-up if needed).
//
// Error reporting (F35 — see CHANGELOG v1.2.0): when json.Unmarshal
// fails with a *json.UnmarshalTypeError, the adapter promotes the
// field path + expected type into the structured ToolError so the
// caller (LLM or operator) can pinpoint the offending key without
// parsing free-form Message text. Example ToolError on a type
// mismatch at `tasks[2].depends_on`:
//
//	{
//	  "code": "ErrInvalidArgument",
//	  "message": "input JSON does not match expected schema for vibe_spec: json: cannot unmarshal string into Go struct field VibeSpecTask.tasks.depends_on of type []string",
//	  "field":  "tasks[2].depends_on",
//	  "expected_type": "array",
//	  "hint":   "tasks[2].depends_on must be an array of strings, not a single string. Omit the field entirely when there are no dependencies (omitempty).",
//	}
func BindOrchestrator[In any, Out any](
	name, description string,
	schema json.RawMessage,
	orchestratorFn func(ctx context.Context, in In) (*Out, error),
) *Tool {
	return &Tool{
		Name:        name,
		Description: description,
		InputSchema: schema,
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in In
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &in); err != nil {
					return &ToolResponse{
						Error: typeMismatchToolError(name, err),
					}, nil
				}
			}
			out, err := orchestratorFn(ctx, in)
			if err != nil {
				return &ToolResponse{Error: ToToolError(err)}, nil
			}
			return &ToolResponse{Data: out}, nil
		},
	}
}

// typeMismatchToolError maps a json.Unmarshal error to a structured
// ToolError. When the error is a *json.UnmarshalTypeError (i.e. a
// type mismatch, the most common schema-vs-struct drift), the field
// path and expected type are surfaced as discrete fields on
// ToolError so callers can render targeted fix-up hints instead of
// parsing free-form Message strings.
//
// For other error kinds (syntax errors, unknown fields, EOF), the
// function falls back to the legacy generic shape (Message + Hint)
// to keep backwards compatibility with callers that pattern-match on
// Message.
func typeMismatchToolError(toolName string, err error) *ToolError {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = "(root)"
		}
		return &ToolError{
			Code:          "ErrInvalidArgument",
			Message:       fmt.Sprintf("input JSON does not match expected schema for %s: %v", toolName, err),
			Field:         field,
			ExpectedType:  typeErr.Type.String(),
			ActualType:    typeErr.Value,
			Hint:          fmt.Sprintf("Field %q must be of type %s, got %s. Update the input payload to match the tool's input schema.", field, typeErr.Type.String(), typeErr.Value),
			SchemaHintURL: "", // reserved for future: link to the per-tool schema doc
		}
	}
	// Fallback: generic ErrInvalidArgument. Includes the raw error in
	// Message (preserved for backwards compatibility) and the empty
	// Field/Type fields (zero values are omitted by json.Marshal's
	// omitempty tags on ToolError).
	return &ToolError{
		Code:    "ErrInvalidArgument",
		Message: fmt.Sprintf("input JSON does not match expected schema for %s: %v", toolName, err),
		Hint:    "Inspect the tool's input schema and ensure the payload matches the declared fields.",
	}
}

// BindStore wraps a method that takes a Store + a typed In and
// returns (*Out, error). It is the lowest-level adapter; useful for
// admin / observability tools that bypass the orchestrator layer.
func BindStore[In any, Out any](
	name, description string,
	schema json.RawMessage,
	s store.Store,
	fn func(ctx context.Context, s store.Store, in In) (*Out, error),
) *Tool {
	return BindOrchestrator(name, description, schema,
		func(ctx context.Context, in In) (*Out, error) {
			return fn(ctx, s, in)
		})
}

// MustJSONSchema panics if v cannot be marshaled to JSON. Use with
// struct literals whose fields are JSON-tagged — a programming
// error should fail fast at boot, not at request time.
func MustJSONSchema(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("tools: JSON schema marshal failed: %v", err))
	}
	return b
}