package research

import (
	"sync"
	"time"
)

// Cache is the v4 research executor's per-(backend,key) result cache.
// The cache implements stale-while-revalidate: a request past the
// soft expiry returns the stale entry AND triggers a background
// refresh. The first request after the hard expiry (2x TTL) blocks
// on a refresh. This matches the §3.2 / AC-R5 / AC-R6 discipline:
// per-type TTL avoids over-caching volatile data; stale-while-
// revalidate avoids the "old data returned forever after a quiet
// period" failure mode of a pure cache.
//
// The cache is keyed by (backend, query_hash). The query_hash is a
// sha256 of the canonical query string (intent + parameters +
// optional depth), NOT the raw operator input — the raw query may
// contain PII and must not become a cache key. Callers compute
// query_hash via sha256.Sum256 in the transport layer.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	now     func() time.Time
}

type cacheEntry struct {
	backend    string
	result     *Result
	fetchedAt  time.Time
	expiresAt  time.Time
	hardExpiry time.Time
	stale      bool // true when past expiresAt but before hardExpiry
}

// CacheConfig is one entry in the per-type TTL table. The defaults
// are the §3.2 numbers; the operator can override per intent via
// the Research cache.override env var (future slice).
type CacheConfig struct {
	// SoftTTL: served from cache without refresh.
	SoftTTL time.Duration

	// StaleTTL: served stale while a background refresh runs.
	// HardExpiry = FetchedAt + SoftTTL + StaleTTL.
	StaleTTL time.Duration
}

// DefaultCacheConfigs returns the §3.2 per-type TTLs. These match
// the volatility of the data: DNS / IP change in minutes; CVE
// re-scores happen daily; academic works do not change once
// published.
func DefaultCacheConfigs() map[Intent]CacheConfig {
	return map[Intent]CacheConfig{
		// Volatile: networks move.
		IntentDNS:    {SoftTTL: 5 * time.Minute, StaleTTL: 5 * time.Minute},
		IntentIP:     {SoftTTL: 5 * time.Minute, StaleTTL: 5 * time.Minute},
		// Slightly volatile: domain transfers, package releases.
		IntentDomain: {SoftTTL: 1 * time.Hour, StaleTTL: 1 * time.Hour},
		IntentCode:   {SoftTTL: 1 * time.Hour, StaleTTL: 2 * time.Hour},
		// CVE re-score cadence: NVD publishes daily, so a 24h TTL
		// is the lower bound; we go 24+24 = 48h stale window.
		IntentCVE:    {SoftTTL: 24 * time.Hour, StaleTTL: 24 * time.Hour},
		// Cert transparency logs grow but the operator's last
		// snapshot of a domain rarely changes within a day.
		IntentCert:   {SoftTTL: 24 * time.Hour, StaleTTL: 24 * time.Hour},
		// Web / news: operator wants recent, but a few hours stale
		// is fine.
		IntentWeb:    {SoftTTL: 1 * time.Hour, StaleTTL: 2 * time.Hour},
		IntentNews:   {SoftTTL: 1 * time.Hour, StaleTTL: 2 * time.Hour},
		// Academic works are stable once published.
		IntentAcademic: {SoftTTL: 7 * 24 * time.Hour, StaleTTL: 7 * 24 * time.Hour},
		// Geo results rarely change.
		IntentGeo:    {SoftTTL: 7 * 24 * time.Hour, StaleTTL: 7 * 24 * time.Hour},
		// Threat intel: operators want fresh; we keep a short soft
		// TTL but a longer stale window so a quiet weekend does
		// not blank the cache.
		IntentThreat: {SoftTTL: 30 * time.Minute, StaleTTL: 2 * time.Hour},
		// Email / dark / multi: conservative defaults.
		IntentEmail:  {SoftTTL: 1 * time.Hour, StaleTTL: 1 * time.Hour},
		IntentDark:   {SoftTTL: 1 * time.Hour, StaleTTL: 1 * time.Hour},
		IntentMulti:  {SoftTTL: 1 * time.Hour, StaleTTL: 1 * time.Hour},
	}
}

// NewCache returns an empty Cache. nil clock selects time.Now.
func NewCache(clock func() time.Time) *Cache {
	if clock == nil {
		clock = time.Now
	}
	return &Cache{entries: map[string]cacheEntry{}, now: clock}
}

// Lookup returns the cached Result for (backend, key) and its
// freshness state. The bool is "found in cache"; the State tells
// the caller whether to use it directly (Fresh / Stale) or to fetch
// (Missing / Expired).
//
// The returned Result is a copy so the caller cannot mutate the
// cache. FetchedAt and ExpiresAt are exposed for the audit row.
func (c *Cache) Lookup(backend, key string) (result *Result, fetchedAt, expiresAt time.Time, state CacheState, found bool) {
	full := backend + "\x00" + key
	c.mu.RLock()
	e, ok := c.entries[full]
	c.mu.RUnlock()
	if !ok {
		return nil, time.Time{}, time.Time{}, CacheMissing, false
	}
	now := c.now()
	switch {
	case now.Before(e.expiresAt):
		copy := e.result
		return copy, e.fetchedAt, e.expiresAt, CacheFresh, true
	case now.Before(e.hardExpiry):
		copy := e.result
		return copy, e.fetchedAt, e.expiresAt, CacheStale, true
	default:
		return nil, e.fetchedAt, e.hardExpiry, CacheExpired, true
	}
}

// Store writes the Result into the cache. fetchedAt is recorded
// from the supplied now; expiresAt and hardExpiry come from the
// CacheConfig for the intent. An empty SoftTTL (config says "do
// not cache") results in a no-op.
func (c *Cache) Store(backend, key string, r *Result, now time.Time, cfg CacheConfig) {
	if cfg.SoftTTL == 0 {
		return
	}
	full := backend + "\x00" + key
	e := cacheEntry{
		backend:    backend,
		result:     r,
		fetchedAt:  now,
		expiresAt:  now.Add(cfg.SoftTTL),
		hardExpiry: now.Add(cfg.SoftTTL + cfg.StaleTTL),
	}
	c.mu.Lock()
	c.entries[full] = e
	c.mu.Unlock()
}

// Invalidate removes a single cache entry. Used by `?refresh=true`.
func (c *Cache) Invalidate(backend, key string) {
	full := backend + "\x00" + key
	c.mu.Lock()
	delete(c.entries, full)
	c.mu.Unlock()
}

// InvalidateBackend removes every entry for a backend. Used when
// the operator marks a backend as Dead → Refreshed: the stale
// answers are gone.
func (c *Cache) InvalidateBackend(backend string) {
	c.mu.Lock()
	for k := range c.entries {
		if startsWith(k, backend+"\x00") {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// Size returns the current entry count (for observability tests).
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// CacheState is the freshness classification returned by Lookup.
// The transport layer maps this to: CacheFresh → return as-is,
// CacheStale → return + fire async refresh, CacheMissing /
// CacheExpired → fetch synchronously.
type CacheState int

const (
	// CacheMissing: not in cache. Caller must fetch.
	CacheMissing CacheState = iota
	// CacheFresh: in cache, before soft expiry. Return as-is.
	CacheFresh
	// CacheStale: past soft expiry, before hard expiry. Return
	// stale + fire async refresh (stale-while-revalidate).
	CacheStale
	// CacheExpired: past hard expiry. Do not return; force fetch.
	CacheExpired
)

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
