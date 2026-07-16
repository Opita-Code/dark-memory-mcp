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
						Error: &ToolError{
							Code:    "ErrInvalidArgument",
							Message: fmt.Sprintf("input JSON does not match expected schema for %s: %v", name, err),
							Hint:    "Inspect the tool's input schema and ensure the payload matches the declared fields.",
						},
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