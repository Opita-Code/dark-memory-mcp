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
	"fmt"
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
}

// New constructs an Orchestrator with the given Store and Safety
// holder. Safety may be nil (orchestrator will construct an empty
// Holder). now is injectable for deterministic tests; pass nil to
// default to time.Now. Use WithBackends to register research backends.
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

// errMissingField is a small helper that wraps ErrInvalidArgument.
func errMissingField(field string) error {
	return fmt.Errorf("%w: %s is required", store.ErrInvalidArgument, field)
}