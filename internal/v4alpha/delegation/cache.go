// Package delegation — LLM result cache (Phase 7 alpha.19).
//
// Per SPEC §3.1 §3.1 operational cache key:
//
//	sha256(vibe_case + \x00 + task + \x00 + project_id + \x00 + model_floor)
//
// The cache is in-memory + agent_memory-backed (best-effort, like
// mindset.go's storeCachedMindset). On hit, returns the cached
// ExtractResult; on miss, returns nil + caller invokes LLM.
//
// Cache TTL: DARK_DELEGATION_CACHE_TTL (default 1h, matching
// DARK_MINDSET_CACHE_TTL convention).
package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultCacheTTL is the default cache lifetime when DARK_DELEGATION_CACHE_TTL
// is unset. Mirrors DARK_MINDSET_CACHE_TTL default.
const DefaultCacheTTL = time.Hour

// ExtractCache is the in-memory LRU cache for ExtractResult values.
// Thread-safe via sync.RWMutex. Falls back to no-cache when nil.
type ExtractCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	memTTL  time.Duration
}

type cacheEntry struct {
	result    ExtractResult
	expiresAt time.Time
}

// NewExtractCache returns an in-memory cache with the given TTL.
// Pass DefaultCacheTTL or read DARK_DELEGATION_CACHE_TTL via CacheTTLFromEnv.
func NewExtractCache(ttl time.Duration) *ExtractCache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &ExtractCache{
		entries: make(map[string]*cacheEntry),
		memTTL:  ttl,
	}
}

// CacheTTLFromEnv reads DARK_DELEGATION_CACHE_TTL from env (returns seconds).
// Default 3600 (1h). Clamped to [60, 86400].
func CacheTTLFromEnv() time.Duration {
	v := os.Getenv("DARK_DELEGATION_CACHE_TTL")
	if v == "" {
		return DefaultCacheTTL
	}
	n := 3600
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 60 || n > 86400 {
		return DefaultCacheTTL
	}
	return time.Duration(n) * time.Second
}

// CacheKey returns the canonical cache key for the (vibe_case, task,
// project_id, model_floor) tuple. Per SPEC §3.1 §3.1 operational cache key.
func CacheKey(vibeCase, task, projectID, modelFloor string) string {
	h := sha256.Sum256([]byte(vibeCase + "\x00" + task + "\x00" + projectID + "\x00" + modelFloor))
	return hex.EncodeToString(h[:])
}

// Get returns the cached ExtractResult for key, or nil if missing/expired.
func (c *ExtractCache) Get(key string) *ExtractResult {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return nil
	}
	if time.Now().After(e.expiresAt) {
		// Expired — caller should treat as miss and re-fetch.
		return nil
	}
	out := e.result
	out.CacheHit = true
	out.Verdict = "cached"
	return &out
}

// Set stores the result under key with the cache's TTL.
func (c *ExtractCache) Set(key string, result ExtractResult) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cacheEntry{
		result:    result,
		expiresAt: time.Now().Add(c.memTTL),
	}
}

// Invalidate removes the entry for key. Best-effort; no-op if absent.
func (c *ExtractCache) Invalidate(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Size returns the current entry count (for metrics / observability).
func (c *ExtractCache) Size() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// ---------- Agent-memory-backed persistent cache (best-effort) ----------

// AgentMemoryStore is the minimal interface we need from agent_memory.Store
// for the persistent cache. Mirrors the interface used in mindset.go.
// Full interface lives at internal/v4alpha/agent_memory (95+ methods);
// we only need RecallFiltered + Save.
type AgentMemoryStore interface {
	RecallFiltered(ctx context.Context, operator, query string, filter RecallFilter, limit int) ([]AgentMemoryRow, error)
	Save(ctx context.Context, audit any, operator, kind, title, content string, tags string, pinned bool) (int64, error)
}

// RecallFilter is the minimal subset of agent_memory.RecallFilter.
type RecallFilter struct {
	TagPrefix string
}

// AgentMemoryRow is the minimal subset of agent_memory.Row.
type AgentMemoryRow struct {
	ID      int64
	Title   string
	Content string
	Tags    string
}

// PersistentCacheKey is the canonical tag-prefix used in agent_memory
// for the persistent delegation cache. Matches the convention from
// mindset.go:lookupCachedMindset.
const PersistentCacheKey = "delegation:v1"

// PersistentLookup queries agent_memory for a cached result. Returns the
// cached ExtractResult on hit (CacheHit=true), nil on miss, nil+error on
// DB failure. Best-effort: failures do not affect the caller.
func PersistentLookup(ctx context.Context, store AgentMemoryStore, operator, cacheKey string) (*ExtractResult, error) {
	if store == nil {
		return nil, nil
	}
	rows, err := store.RecallFiltered(ctx, operator, cacheKey, RecallFilter{
		TagPrefix: PersistentCacheKey,
	}, 5)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if strings.Contains(r.Tags, "delegation_cache_key:"+cacheKey) {
			// We store the ExtractResult as JSON in Content. For now,
			// the persistent cache returns the raw Content and lets the
			// caller deserialize (out of scope — Chunk 7.1 uses the
			// in-memory cache only; persistent is alpha.20+ follow-up).
			return &ExtractResult{
				Decision:  "cached",
				Reasoning: "persistent cache hit (raw content)",
				CacheHit:  true,
				Verdict:   "cached",
			}, nil
		}
	}
	return nil, nil
}

// PersistentStore best-effort writes the ExtractResult to agent_memory.
// Returns error on failure (caller should ignore — best-effort).
func PersistentStore(ctx context.Context, store AgentMemoryStore, operator, cacheKey, vibeCase string, result ExtractResult) error {
	if store == nil {
		return nil
	}
	tags := fmt.Sprintf("%s,vibe_case:%s,cached:%d,delegation_cache_key:%s",
		PersistentCacheKey, vibeCase, time.Now().Unix(), cacheKey)
	// Stash a minimal JSON; full ExtractResult serialization is alpha.20.
	content := fmt.Sprintf(`{"decision":"%s","subtask_count":%d,"verdict":"%s"}`,
		result.Decision, len(result.Subtasks), result.Verdict)
	_, err := store.Save(ctx, nil, operator, "context", "delegation:"+cacheKey[:8], content, tags, false)
	return err
}

// ---------- TTL helpers ----------

// ParseDuration parses a duration string like "1h", "30m", "3600s".
// Returns DefaultCacheTTL on parse error. Used by env wrappers.
func ParseDuration(s string) time.Duration {
	if s == "" {
		return DefaultCacheTTL
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return DefaultCacheTTL
	}
	return d
}

// SecondsToDuration converts seconds (int) to time.Duration. Clamped to
// [60s, 24h]. Used by env wrappers.
func SecondsToDuration(s int) time.Duration {
	if s < 60 {
		s = 60
	}
	if s > 86400 {
		s = 86400
	}
	return time.Duration(s) * time.Second
}

// ParseIntOrDefault is a helper for parsing env var integers with a default.
func ParseIntOrDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}