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
package nli

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// CacheStats counts hits, misses, and evictions for observability.
// Hits + Misses = total successful Score() calls; errors do not
// increment either counter (the call did not complete).
type CacheStats struct {
	Hits        uint64 // cached value returned without calling inner
	Misses      uint64 // inner was called
	InnerErrors uint64 // inner returned an error (NOT cached)
	Puts        uint64 // cache.Put was invoked
	CacheErrors uint64 // cache.Put returned an error
}

// CachedProvider wraps a Provider with a Cache.
type CachedProvider struct {
	inner Provider
	cache Cache
	ttl   time.Duration
	hits  atomic.Uint64
	miss  atomic.Uint64
	errs  atomic.Uint64
	puts  atomic.Uint64
	cerrs atomic.Uint64
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
// Errors are propagated; they are NEVER cached.
func (c *CachedProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	key := Key{ProviderID: c.inner.ID(), Premise: premise, Hypothesis: hypothesis}
	// Cache lookup.
	if score, ok := c.cache.Get(key); ok {
		c.hits.Add(1)
		return score, nil
	}
	c.miss.Add(1)
	// Inner call.
	score, err := c.inner.Score(ctx, premise, hypothesis)
	if err != nil {
		c.errs.Add(1)
		return score, err
	}
	// Write-through. If Put fails, we still return the score — caching
	// is best-effort, not load-bearing.
	c.puts.Add(1)
	if _, perr := c.cache.Put(key, score, c.ttl); perr != nil {
		c.cerrs.Add(1)
	}
	return score, nil
}

// Stats returns a snapshot of cache counters.
func (c *CachedProvider) Stats() CacheStats {
	return CacheStats{
		Hits:        c.hits.Load(),
		Misses:      c.miss.Load(),
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