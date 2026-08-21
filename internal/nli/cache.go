// Package nli — response cache (T06, spec 1276).
//
// Why a cache: drift_judge scores the same artifact against the same
// hypothesis dozens of times per session (spec evolution, retries,
// parallel subagents). Calling HF Inference / MiniCheck for every
// invocation wastes budget + latency.
//
// What is cached: Score values (Label, Confidence, ProviderID,
// LatencyMS, ModelRev) keyed by (ProviderID, premise, hypothesis).
// Errors are NEVER cached — the next call may succeed; the call that
// failed would otherwise be sticky.
//
// INV-7 (project isolation): the cache is a single-process LRU
// scoped to one Router. Multi-project deployment creates one Router
// per project (project_id is in the Project struct, not here).
//
// Thread-safety: Get, Put, Clear, Size are all guarded by a single
// sync.Mutex. RLock-dominant workloads: a sync.RWMutex is an option
// but we keep a plain Mutex for simplicity (lock is held briefly).
package nli

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrCacheFull is returned by Put when the cache is full and we
// cannot evict (e.g. eviction policy disabled). Reserved; current
// LRU never returns it.
var ErrCacheFull = errors.New("nli: cache full")

// ErrCacheInvalidTTL is returned when Put receives ttl <= 0.
var ErrCacheInvalidTTL = errors.New("nli: invalid TTL")

// Key is a typed wrapper around the cache key tuple. The NUL byte
// separator is critical: premises may legitimately contain newlines,
// spaces, or other delimiters, but they cannot contain \x00 (Go strings
// allow it, but JSON/HTTP payloads never will — and any payload with
// \x00 is malformed). Using a real separator prevents the
// concatenation ambiguity that plagues naive "ab"+"cd" vs "a"+"bcd"
// key collision.
type Key struct {
	ProviderID string
	Premise    string
	Hypothesis string
}

// String returns the canonical cache key: SHA-256(provider || \x00 || premise || \x00 || hypothesis),
// hex-encoded. The hash is one-way: the original strings cannot be
// recovered from the key (defense-in-depth against log leaks).
func (k Key) String() string {
	h := sha256.New()
	h.Write([]byte(k.ProviderID))
	h.Write([]byte{0})
	h.Write([]byte(k.Premise))
	h.Write([]byte{0})
	h.Write([]byte(k.Hypothesis))
	return hex.EncodeToString(h.Sum(nil))
}

// Validate rejects keys with empty fields — calling Score with empty
// premise/hypothesis is a contract violation upstream, but we defend
// at the cache boundary too.
func (k Key) Validate() error {
	if k.ProviderID == "" {
		return fmt.Errorf("%w: cache key missing ProviderID", ErrInvalidConfig)
	}
	if k.Premise == "" {
		return fmt.Errorf("%w: cache key missing Premise", ErrInvalidConfig)
	}
	if k.Hypothesis == "" {
		return fmt.Errorf("%w: cache key missing Hypothesis", ErrInvalidConfig)
	}
	return nil
}

// cacheEntry is the value half of the cache. The key field is duplicated
// here so eviction is O(1) (we don't have to scan the map to find the
// key for the LRU-back element).
type cacheEntry struct {
	key       string
	score     Score
	expiresAt time.Time // zero value = no TTL
}

// Cache is the abstract cache interface. Implementations: InMemoryLRU.
// The interface exists for testability — integration tests may swap in
// a fake.
type Cache interface {
	// Get returns the cached Score for key. Returns (zero, false) if
	// the key is absent, OR present but expired (lazy expiry).
	Get(key Key) (Score, bool)
	// Put inserts score under key with ttl. ttl <= 0 is rejected.
	// Returns the size after insertion. Triggers LRU eviction when full.
	Put(key Key, score Score, ttl time.Duration) (size int, err error)
	// Clear removes every entry.
	Clear()
	// Size returns the number of entries currently held (including
	// expired-but-not-yet-evicted ones — eviction is lazy on Get).
	Size() int
	// MaxEntries returns the configured cap.
	MaxEntries() int
}

// DefaultMaxCacheEntries is the default LRU cap. Tunable per project.
const DefaultMaxCacheEntries = 10_000

// DefaultCacheTTL is the default per-entry TTL. Tunable per project.
const DefaultCacheTTL = 24 * time.Hour

// InMemoryLRU is a bounded LRU cache with lazy TTL expiry. The
// container/list package provides O(1) move-to-front + remove-from-back.
type InMemoryLRU struct {
	mu         sync.Mutex
	maxEntries int
	order      *list.List               // front = most-recently-used; back = LRU
	index      map[string]*list.Element // key string → *list.Element holding *cacheEntry
}

// NewInMemoryLRU constructs a bounded LRU. maxEntries must be > 0.
// Default: 10_000.
func NewInMemoryLRU(maxEntries int) (*InMemoryLRU, error) {
	if maxEntries <= 0 {
		return nil, fmt.Errorf("%w: maxEntries must be > 0, got %d", ErrInvalidConfig, maxEntries)
	}
	return &InMemoryLRU{
		maxEntries: maxEntries,
		order:      list.New(),
		index:      make(map[string]*list.Element, maxEntries),
	}, nil
}

// Get returns the score and true on hit + non-expired. Returns (zero,
// false) on miss OR expired (and silently removes the expired entry).
func (c *InMemoryLRU) Get(key Key) (Score, bool) {
	if err := key.Validate(); err != nil {
		return Score{}, false
	}
	k := key.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[k]
	if !ok {
		return Score{}, false
	}
	entry := el.Value.(*cacheEntry)
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		// Expired — evict silently.
		c.order.Remove(el)
		delete(c.index, k)
		return Score{}, false
	}
	c.order.MoveToFront(el)
	return entry.score, true
}

// Put inserts or replaces. Returns the size after insertion. If the
// cache is full, evicts the LRU entry to make room.
func (c *InMemoryLRU) Put(key Key, score Score, ttl time.Duration) (int, error) {
	if err := key.Validate(); err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, ErrCacheInvalidTTL
	}
	k := key.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	expiresAt := time.Now().Add(ttl)
	if el, ok := c.index[k]; ok {
		// Replace in place; preserve LRU recency.
		el.Value.(*cacheEntry).score = score
		el.Value.(*cacheEntry).expiresAt = expiresAt
		c.order.MoveToFront(el)
		return c.order.Len(), nil
	}
	// Insert new entry.
	entry := &cacheEntry{key: k, score: score, expiresAt: expiresAt}
	el := c.order.PushFront(entry)
	c.index[k] = el
	// Evict LRU if over cap.
	for c.order.Len() > c.maxEntries {
		back := c.order.Back()
		if back == nil {
			break
		}
		backKey := back.Value.(*cacheEntry).key
		c.order.Remove(back)
		delete(c.index, backKey)
	}
	return c.order.Len(), nil
}

// Clear empties the cache.
func (c *InMemoryLRU) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order = list.New()
	c.index = make(map[string]*list.Element, c.maxEntries)
}

// Size returns the number of entries currently held (including
// expired-but-not-yet-evicted).
func (c *InMemoryLRU) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// MaxEntries returns the configured cap.
func (c *InMemoryLRU) MaxEntries() int {
	return c.maxEntries
}

// Compile-time guarantee.
var _ Cache = (*InMemoryLRU)(nil)