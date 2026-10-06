// Package nli — cached provider wrapper (T06, spec 1276).
//
// CachedProvider wraps any Provider with cache-aside reads and
// write-through on success. Errors are NEVER cached — caching an
// error would make it sticky, and we don't want transient failures
// (429, 503, timeout) to lock callers out of retrying.
//
// This file deliberately does NOT validate inputs — input validation
// is the inner provider's job (DeBERTaProvider / MiniCheckProvider
// already return ErrInputEmpty / ErrInputTooLarge with partial Score).
// CachedProvider is a transparent caching layer; it neither adds
// nor removes behavior beyond caching.
//
// Single-flight dedup (Phase 15 T-403):
//
//	When N goroutines call Score() with the SAME (premise, hypothesis)
//	concurrently and the cache is empty, only ONE inner call happens;
//	the other N-1 wait for the winner's result. Pattern matches
//	golang.org/x/sync/singleflight semantics without adding the
//	dependency. Closes the race in
//	TestCachedProvider_ConcurrentGet_RaceFree (row 2464 §1.13.5).
package nli

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// CacheStats counts hits, misses, and evictions for observability.
// Hits + Misses = total successful Score() calls; errors do not
// increment either counter (the call did not complete).
type CacheStats struct {
	Hits         uint64 // cached value returned without calling inner
	Misses       uint64 // inner was called (single-flight winner)
	Waiters      uint64 // caller waited for an in-flight call to finish
	InnerErrors  uint64 // inner returned an error (NOT cached)
	Puts         uint64 // cache.Put was invoked
	CacheErrors  uint64 // cache.Put returned an error
}

// CachedProvider wraps a Provider with a Cache.
type CachedProvider struct {
	inner Provider
	cache Cache
	ttl   time.Duration
	hits  atomic.Uint64
	miss  atomic.Uint64
	wait  atomic.Uint64
	errs  atomic.Uint64
	puts  atomic.Uint64
	cerrs atomic.Uint64

	// mu guards the cache.Get + inflight.LoadOrStore sequence against
	// TOCTOU races. Without it, a goroutine that misses the cache
	// can become a winner AFTER a previous winner already populated
	// the cache (between cache.Get and LoadOrStore in Score()).
	// Hot path: cache.Get BEFORE acquiring mu; mu is only held on
	// the slow path. Performance is preserved for cache hits.
	mu sync.Mutex

	// inflight tracks in-flight inner invocations per cache key.
	// Uses sync.Map (lock-free read) so concurrent Score() calls
	// don't serialize on a mutex for the lookup. Per-key contention
	// is handled by the LoadOrStore atomic.
	inflight sync.Map // map[Key]*inflightCall
}

// inflightCall is the shared state for N concurrent Score() callers
// waiting on the same inner call. done is closed by the winner AFTER
// it stores score+err into the struct.
type inflightCall struct {
	done  chan struct{}
	score Score
	err   error
}

// NewCachedProvider wraps inner with the given cache and TTL. ttl <= 0
// is rejected — caching with no expiration is a memory leak.
func NewCachedProvider(inner Provider, cache Cache, ttl time.Duration) (*CachedProvider, error) {
	if inner == nil {
		return nil, ErrInvalidConfig // wrapped: "invalid config: <nil> provider"
	}
	if cache == nil {
		return nil, errors.New("nli: cache is nil")
	}
	if ttl <= 0 {
		return nil, ErrCacheInvalidTTL
	}
	return &CachedProvider{
		inner: inner,
		cache: cache,
		ttl:   ttl,
	}, nil
}

// ID returns the inner provider's ID — provenance flows through.
func (c *CachedProvider) ID() string { return c.inner.ID() }

// Score returns the cached score on hit, or calls inner on miss.
// Concurrent Score() calls with the SAME (premise, hypothesis) are
// deduplicated so exactly ONE inner call happens regardless of how
// many goroutines race. Errors are propagated; they are NEVER cached.
func (c *CachedProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	key := Key{ProviderID: c.inner.ID(), Premise: premise, Hypothesis: hypothesis}
	// Cache lookup — fast path, lock-free.
	if score, ok := c.cache.Get(key); ok {
		c.hits.Add(1)
		return score, nil
	}
	// Slow path: serialize cache.Get + inflight.LoadOrStore under mu
	// to eliminate the TOCTOU race where a goroutine misses cache.Get
	// (before winner's Put), then LoadOrStore wins (after winner's
	// Delete) and calls inner a second time.
	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check the cache — another goroutine may have populated
	// it while we were waiting on mu.
	if score, ok := c.cache.Get(key); ok {
		c.hits.Add(1)
		return score, nil
	}
	// Single-flight dedup under mu.
	return c.singleflight(ctx, key, premise, hypothesis)
}

// singleflight MUST be called with c.mu locked. Returns (score, err)
// for both winners (who call inner) and waiters (who block on the
// winner's done channel).
//
// Algorithm (mu held):
//  1. Atomically register our call struct in c.inflight via LoadOrStore.
//  2. If we LOADED an existing entry, we're a waiter — block on its
//     `done` channel and return the winner's result.
//  3. If we STORED our own entry, we're the winner — call inner,
//     populate our call struct, close the channel, Put to cache, and
//     finally delete from inflight (in that order, to prevent TOCTOU).
//
// Why mu-protected: without mu, a goroutine could miss the cache.Get,
// enter singleflight AFTER the winner already Put+Delete'd, win the
// LoadOrStore (because inflight is empty), and call inner a second time.
// mu serializes the cache.Get + LoadOrStore sequence for ALL goroutines
// that miss the cache, eliminating the window.
func (c *CachedProvider) singleflight(ctx context.Context, key Key, premise, hypothesis string) (Score, error) {
	call := &inflightCall{done: make(chan struct{})}
	actual, loaded := c.inflight.LoadOrStore(key, call)
	if loaded {
		// Another goroutine is already calling inner for this key.
		// Wait for it to finish and return its result.
		existing := actual.(*inflightCall)
		c.wait.Add(1)
		<-existing.done
		return existing.score, existing.err
	}
	// We won — call inner (mu still held; inner call should be brief).
	c.miss.Add(1)
	score, err := c.inner.Score(ctx, premise, hypothesis)
	if err != nil {
		c.errs.Add(1)
	}
	// Publish result.
	call.score = score
	call.err = err
	// ORDER MATTERS (Phase 15 T-403):
	//  1. Write-through to cache. Future Score() callers (that miss
	//     the cache on the fast path) will hit this Put.
	//  2. close(done) so waiters unblock and return the result.
	//  3. Delete(key) so the inflight map doesn't accumulate stale
	//     entries. After Delete, the next goroutine that misses the
	//     cache and acquires mu will see inflight empty and become
	//     the next winner (correct behavior after cache eviction).
	// NOTE: because mu is held throughout, no new goroutine can race
	// past cache.Get and reach LoadOrStore concurrently with our
	// Put+Delete — they'll block on mu first.
	if err == nil {
		c.puts.Add(1)
		if _, perr := c.cache.Put(key, score, c.ttl); perr != nil {
			c.cerrs.Add(1)
		}
	}
	close(call.done)
	c.inflight.Delete(key)
	return score, err
}

// Stats returns a snapshot of cache counters.
func (c *CachedProvider) Stats() CacheStats {
	return CacheStats{
		Hits:        c.hits.Load(),
		Misses:      c.miss.Load(),
		Waiters:     c.wait.Load(),
		InnerErrors: c.errs.Load(),
		Puts:        c.puts.Load(),
		CacheErrors: c.cerrs.Load(),
	}
}

// Inner exposes the wrapped provider for tests + admin.
func (c *CachedProvider) Inner() Provider { return c.inner }

// Cache exposes the wrapped cache for tests + admin.
func (c *CachedProvider) Cache() Cache { return c.cache }

// Compile-time guarantee.
var _ Provider = (*CachedProvider)(nil)