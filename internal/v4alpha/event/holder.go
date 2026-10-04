package event

import "github.com/dark-agents/dark-memory-mcp/internal/eventholder"

// SetAutoEmitter — Phase 12 T-103a-extension: thin wrapper over
// eventholder.Set. The process-wide AutoEmitter is held in
// internal/eventholder (a leaf package) so lower-level packages
// (internal/store/sqlite) can read it without an import cycle.
//
// SetAutoEmitter is the canonical "wire at boot" entry point. Main.go
// calls it once after constructing the AutoEmitter. First writer wins.
//
// Deprecated alias for cross-package wiring — new code should use
// eventholder.Set directly. Kept here so the existing
// internal/v4alpha/event API is preserved for tests.
func SetAutoEmitter(ae *AutoEmitter) {
	if ae == nil {
		return
	}
	eventholder.Set(ae)
}

// GetAutoEmitter returns the process-wide AutoEmitter, or nil when
// no emitter is configured.
//
// Deprecated alias for cross-package wiring — new code should use
// eventholder.Get directly. Kept here so the existing
// internal/v4alpha/event API is preserved for tests.
func GetAutoEmitter() *AutoEmitter {
	ae := eventholder.Get()
	if ae == nil {
		return nil
	}
	return ae.(*AutoEmitter)
}