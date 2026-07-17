// Package orchestration contains the workflow API that wraps the
// Store with safety (INV-3), audit (INV-1), and economy (Atlan).
// Each Orchestrator method is one workflow operation that the MCP
// server (or any external caller) can invoke. They are the typed,
// reusable counterpart of the untyped Store interface.
//
// Conventions (spec 173):
//   - All methods take ctx context.Context as first param.
//   - All methods validate inputs and return error typed by the
//     store layer (ErrSessionRequired, ErrInvalidArgument, etc.).
//   - All writes emit write_audit atomically with the data write
//     (Store enforces this; orchestrator just supplies WriteContext).
//   - All reads require an active project (Store.requireProject).
//
// Layering:
//
//     MCP server (Wave 4)
//         |
//         v
//     Orchestrator  <-- this package
//         |
//         v
//     Store (sqlite or postgres partial impl)
//
// Safety:
//   - The canary check (INV-3) is invoked inside Store.Save*, so
//     orchestrators don't need to call it explicitly. They DO need
//     to populate the WriteContext so the audit row carries the
//     orchestrator's actor name.
package orchestration

import (
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Orchestrator is the typed workflow API. Construct with New().
type Orchestrator struct {
	Store    store.Store
	Safety   *safety.Holder
	now      func() time.Time // injectable for tests
	backends []ResearchBackend // registered research backends (O3)
	selector LLMSelector       // LLM selector for O5 Judge
}

// New constructs an Orchestrator with the given Store and Safety
// holder. Safety may be nil (orchestrator will construct an empty
// Holder). now is injectable for deterministic tests; pass nil to
// default to time.Now. Use WithBackends to register research backends
// and WithLLMSelector to wire the Judge pipeline.
func New(s store.Store, safe *safety.Holder) *Orchestrator {
	if safe == nil {
		h := &safety.Holder{}
		safe = h
	}
	return &Orchestrator{
		Store:  s,
		Safety: safe,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// WithLLMSelector attaches an LLMSelector to the orchestrator. Used
// by O5 Judge. If not set, NewSelfHarnessClient is called at Judge time
// (auto-detect from env).
func (o *Orchestrator) WithLLMSelector(s LLMSelector) *Orchestrator {
	o.selector = s
	return o
}

// ensureLLMSelector lazily constructs a default OSINTSelector backed
// by SelfHarnessClient (auto-detect harness LLM). Called at Judge time.
func (o *Orchestrator) ensureLLMSelector() LLMSelector {
	if o.selector != nil {
		return o.selector
	}
	// Default: use the harness's LLM. If no key is set, the
	// selector will return ErrNoLLMAvailable on Select.
	client, _ := NewSelfHarnessClient()
	return NewOSINTSelector(client)
}

// fieldError carries the offending field name AND the sentinel it
// wraps, so the tools layer (ToToolError) can populate the structured
// ToolError.Field field path for the operator. F35 wire-propagation.
//
// The error string is preserved for backward-compat fallback with
// log scrapers that grep on `Errorf("%w: %s is required", ...)`, but
// callers SHOULD errors.As(err, &fieldError{}) instead of string-parsing.
type fieldError struct {
	store error
	Field string
}

func (e *fieldError) Error() string {
	if e.Field == "" {
		return e.store.Error()
	}
	return e.store.Error() + ": field=" + e.Field
}

func (e *fieldError) Unwrap() error { return e.store }

// errMissingField produces a structured error that carries the field
// name. The tools layer extracts it via errors.As and populates
// ToolError.Field so the harness's error path renders a precise
// fix-up hint instead of a generic message.
func errMissingField(field string) error {
	return &fieldError{store: store.ErrInvalidArgument, Field: field}
}