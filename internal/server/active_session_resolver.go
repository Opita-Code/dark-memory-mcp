// Package server — active_session_resolver.go: the real
// ActiveSessionResolver implementation (v2.0.2 gate fix).
//
// # Why this exists
//
// GateMiddleware (middleware.go) needs an ActiveSessionResolver
// implementation that returns the currently-active session_id for
// the incoming tool call's project. v2.0.1's Gate wired
// StaticSessionResolver{} (empty SessionID) — every call returned
// "" and the gate refused all tool calls. v2.0.2 replaces that
// with this StoreBackedActiveSessionResolver, which queries the
// projects.active_session_id column populated by session_start /
// session_resume (orchestration) and cleared by session_close.
//
// # Lookup-fn indirection
//
// The resolver takes a function (not the full store.Store
// interface) so unit tests can drive it with an in-memory fake
// without embedding a 70-method interface. main.go adapts the
// real Store via the `StoreBackedLookup` adapter below.
//
// # Cache strategy
//
// The resolver is called on every tool call via the gate. Without a
// cache, that's one DB read per tool call (per ActiveSessionID
// invocation). For a session-bound server doing N calls/sec, N
// reads/sec. We add a short in-process TTL cache (default 5s) so
// bursty workloads amortize the cost. The TTL is short enough
// that operators don't notice the lag when they switch sessions.
//
// Cache invalidation is TTL-only, NOT write-through. session_start
// and session_close write to the projects table but do not push
// to the resolver's cache. Worst case after a session_start: the
// gate uses the OLD active session id for up to TTL seconds. That's
// acceptable because:
//   - the gate's PreCheck then runs identity/capabilities validation
//     against the cached session, which fails (session doesn't exist
//     or doesn't have capability) → refusal, not silent success.
//   - the ORCHESTRATOR-level session_start already persisted and
//     emitted write_audit; the gate lag doesn't break the audit
//     trail.
//
// Operators that want write-through can layer their own cache
// invalidation outside this struct; Invalidate/InvalidateAll are
// exported for that purpose.
//
// # Multi-process / cross-instance
//
// This resolver is per-process. On a server farm, every instance
// has its own cache. Same correctness invariant as above — a stale
// cache across instances means a fresh session_start is invisible
// to peers for up to TTL, but the orchestrator's identity/cap checks
// defend against granting access via the wrong session_id.
//
// For HTTP transport + sticky-session routing (see microsoft/mcp-
// gateway for a reference), the cache is per-instance which is
// fine because the MCP session lifecycle already keeps all calls
// for one client on one server.
package server

import (
	"context"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// DefaultActiveSessionCacheTTL is the default in-process cache TTL.
// Override per-instance via WithCacheTTL when wiring in main.go.
const DefaultActiveSessionCacheTTL = 5 * time.Second

// ActiveSessionLookup is the minimal contract the resolver needs:
// look up the active session_id for a project_id. The function is
// expected to be side-effect-free; cache writes happen in the
// resolver, not in the lookup itself.
type ActiveSessionLookup func(ctx context.Context, projectID string) (string, error)

// StoreBackedLookup adapts the store.Store interface to
// ActiveSessionLookup. Constructed once in main.go at boot.
func StoreBackedLookup(s store.Store) ActiveSessionLookup {
	return func(ctx context.Context, projectID string) (string, error) {
		return s.GetActiveSession(ctx, projectID)
	}
}

// StoreBackedActiveSessionResolver is the production
// ActiveSessionResolver. Calls the lookup function (which the
// caller supplies) and caches the result for CacheTTL.
//
// Concurrent-safe: a sync.RWMutex guards the cache map; reads
// (cache hits) take the RLock, writes (cache miss path) take the
// Lock.
type StoreBackedActiveSessionResolver struct {
	Lookup ActiveSessionLookup

	// CacheTTL controls how long a cached entry stays valid. Zero
	// disables caching (every call hits the lookup). Negative values
	// are treated as zero.
	CacheTTL time.Duration

	// Now is the clock function (injected for tests). Defaults to
	// time.Now when nil.
	Now func() time.Time

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	sessionID string
	expires   time.Time
}

// NewStoreBackedActiveSessionResolver constructs a resolver with the
// given lookup function and the default cache TTL. main.go calls
// this with StoreBackedLookup(bootState.Store).
func NewStoreBackedActiveSessionResolver(lookup ActiveSessionLookup) *StoreBackedActiveSessionResolver {
	return &StoreBackedActiveSessionResolver{
		Lookup:   lookup,
		CacheTTL: DefaultActiveSessionCacheTTL,
		Now:      time.Now,
		cache:    make(map[string]cacheEntry),
	}
}

// WithCacheTTL replaces the cache TTL. Returns the receiver for
// fluent wiring in main.go.
func (r *StoreBackedActiveSessionResolver) WithCacheTTL(d time.Duration) *StoreBackedActiveSessionResolver {
	if d < 0 {
		d = 0
	}
	r.CacheTTL = d
	return r
}

// ActiveSessionID implements ActiveSessionResolver.
//
// Logic:
//  1. If the cache has a non-expired entry, return it. (RLock — many
//     concurrent readers, no contention.)
//  2. Otherwise, call Lookup. (Lock — single writer
//     during miss; we keep the lock window tight by computing "now"
//     before taking the write lock and then doing the read.)
//  3. Cache the result with expires = now + CacheTTL.
//
// Empty projectID is a legitimate case (session_start with no project
// context yet) — we return "" without a lookup call to avoid the
// obvious no-op query.
func (r *StoreBackedActiveSessionResolver) ActiveSessionID(ctx context.Context, projectID string) string {
	if projectID == "" {
		return ""
	}

	now := r.now()

	// Fast path: cache hit (read-only).
	if r.CacheTTL > 0 {
		r.mu.RLock()
		entry, ok := r.cache[projectID]
		r.mu.RUnlock()
		if ok && now.Before(entry.expires) {
			return entry.sessionID
		}
	}

	// Slow path: lookup + cache fill.
	sessID, err := r.Lookup(ctx, projectID)
	if err != nil {
		// Don't propagate resolver errors — the gate treats empty
		// SessionID as "no session" which the v2.0.2 per-tool
		// requirement check then routes correctly. The resolver
		// is hot-path code; logging at debug-level is a future
		// addition.
		sessID = ""
	}

	if r.CacheTTL > 0 {
		r.mu.Lock()
		r.cache[projectID] = cacheEntry{
			sessionID: sessID,
			expires:   now.Add(r.CacheTTL),
		}
		r.mu.Unlock()
	}

	return sessID
}

// Invalidate drops a single project from the cache. Not used by the
// default code paths (TTL-only invalidation); exported so
// operators writing custom orchestration can opt in to
// write-through caching by calling this from session_start /
// session_close handlers of their own.
func (r *StoreBackedActiveSessionResolver) Invalidate(projectID string) {
	if projectID == "" {
		return
	}
	r.mu.Lock()
	delete(r.cache, projectID)
	r.mu.Unlock()
}

// InvalidateAll clears the cache. Useful in tests and when an
// operator wants to force a fresh read after a manual DB edit.
func (r *StoreBackedActiveSessionResolver) InvalidateAll() {
	r.mu.Lock()
	r.cache = make(map[string]cacheEntry)
	r.mu.Unlock()
}

func (r *StoreBackedActiveSessionResolver) now() time.Time {
	if r.Now == nil {
		return time.Now().UTC()
	}
	return r.Now().UTC()
}
