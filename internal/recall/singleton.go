// Package recall — singleton.go: the boot-time FrameSource construction.
//
// 5A.ii.b.2.c.1 (v2.0.1 follow-up): the recall cache is now built
// ONCE at Boot and shared between the dark_memory_recall tool and the
// Gate (server.GateMiddleware). Pre-2.0.1, recall constructed a fresh
// CachedSource per invocation, paying the cost of one Store.GetVLPState +
// one Store.ListSDDEvaluations + one Store.GetConstitution on every
// recall call. The singleton moves those reads to boot; per-call cost
// drops to one GetFrame (or cache hit).
//
// # Why singleton and not process-wide cache
//
// CachedSource has a TTL + INV-5 integrity check on every Get. A
// process-wide TTL cache would require explicit invalidation on every
// write (spec create, drift resolve, etc.). The singleton gets us
// most of the win (one construction per process instead of per call)
// without committing to a more complex invalidation contract. If
// 5A.ii.b.2.c.2 (next wave) introduces a process-wide TTL, it will
// be a separate type — not this one.
//
// # Lifecycle
//
//   - Server.Boot calls NewSingleton at the end of step 5 (after
//     tool registration). One instance per Server.
//   - The Server's GateMiddleware holds a reference to the same
//     singleton via its FrameSource field.
//   - dark_memory_recall reads the singleton via the closure captured
//     by RegisterRecall.
//   - Server.Shutdown does NOT close the singleton; it has no
//     resources of its own (the inner StoreSource shares Store
//     ownership with the rest of the server).

package recall

import (
	"fmt"
	"log"

	"github.com/dark-agents/dark-memory-mcp/internal/policy"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// NewSingleton builds the boot-time FrameSource: a CachedSource
// wrapping a StoreSource. The CachedSource layer caches the per-frame
// Get results against the INV-5 content hash; on hash mismatch it
// re-reads from Store and emits a cache_mismatch audit row.
//
// Returns a policy.FrameSource so callers can pass it to the gate
// without a type assertion.
func NewSingleton(st store.Store, safety *store.SafetyHolder, logger *log.Logger) (policy.FrameSource, error) {
	if st == nil {
		return nil, fmt.Errorf("recall.NewSingleton: nil store")
	}
	if logger == nil {
		logger = log.Default()
	}
	inner := NewStoreSource(st, nil)
	cached := NewCachedSource(inner, st, safety, nil, logger)
	return cached, nil
}
