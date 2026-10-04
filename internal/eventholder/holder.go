// Package eventholder — Phase 12 T-103a-extension: leaf package that
// owns the process-wide AutoEmitter interface. Decouples the holder
// from internal/v4alpha/event so callers in lower-level packages
// (internal/store/sqlite) can read the AutoEmitter without creating
// an import cycle.
//
// Why a separate package?
//
//   - internal/v4alpha/event imports internal/store/sqlite (for the
//     Event type + sqliteStore interface used by Writer.New).
//   - internal/store/sqlite also needs to call AutoEmitter
//     helpers (for EmitSupersede in MarkSupersededAgentMemory).
//   - If bitemporal.go imports v4alpha/event, the cycle is
//     internal/store/sqlite → internal/v4alpha/event → internal/store/sqlite.
//   - This package has NO imports from store/sqlite or v4alpha/event
//     (just std lib atomic). It defines the AutoEmitter interface
//     and the Set/Get singletons. v4alpha/event's *AutoEmitter
//     satisfies the interface; main.go wires it once at boot.
//
// Pattern (matches internal/tools/federation.go):
//
//	main.go:
//     eventholder.Set(event.NewAutoEmitter(...))
//
//	call site (any package):
//     if ae := eventholder.Get(); ae != nil {
//         ae.EmitSupersede(...)
//     }
package eventholder

import (
	"context"
	"sync/atomic"
)

// AutoEmitter is the cross-package interface for the 8 Phase 12 T-103a
// helpers. Defined here so any package can hold an AutoEmitter
// reference without importing internal/v4alpha/event.
//
// Every method is fire-and-forget by design: failures are logged
// internally, never propagated to the caller. The primary action
// (data write, cache eviction, etc.) is the responsibility of the
// caller; the event is for forensic traceability.
//
// Returns context + int64/string parameters per helper (see
// internal/v4alpha/event/auto_emit.go for full documentation).
type AutoEmitter interface {
	EmitSupersede(ctx context.Context, oldMemID, newMemID int64, trigger, reason string)
	EmitDecayRefresh(ctx context.Context, rowID, newAccessCount int64)
	EmitSchemaMigration(ctx context.Context, fromVersion, toVersion int, rationale string)
	EmitEmbedderRefresh(ctx context.Context, rowID int64, newEntityCount int)
	EmitCalibrationUpdate(ctx context.Context, evalID int64, newPointEstimate float64, method string)
	EmitCacheInvalidation(ctx context.Context, cacheTable string, rowID int64, semantic bool, reason string)
	EmitJudgeVerdictUpdate(ctx context.Context, evalID int64, verdict string, confidence float64)
	EmitPersonaUpdate(ctx context.Context, personaID, rationale string)
}

// holder is the process-wide AutoEmitter pointer. Lock-free read via
// atomic.Pointer (the hot path is the read at every event emit point).
var holder atomic.Pointer[AutoEmitter]

// Set installs the process-wide AutoEmitter. Called once at boot from
// main.go after the underlying *event.AutoEmitter is constructed.
// First writer wins (matches SetFederationPeer in internal/tools).
//
// Pass nil to uninstall (Get will return nil). Set is concurrency-safe.
func Set(ae AutoEmitter) {
	holder.Store(&ae)
}

// Get returns the process-wide AutoEmitter, or nil when no emitter
// is configured. Lock-free via atomic.Pointer. Safe for concurrent use.
func Get() AutoEmitter {
	p := holder.Load()
	if p == nil {
		return nil
	}
	return *p
}