// Package llm provides the LLM response cache and prompt-cache key helpers
// for Dark Memory MCP. INV-5 (cache integrity) is enforced at the Get layer:
// every stored response carries a SHA-256 of its text; on retrieval we
// re-hash and compare; mismatch = treat as miss + emit anomaly event.
//
// The cache is process-local by default. Pass an explicit *Cache with
// DARK_CACHE_DIR set to persist across processes; otherwise the cache is
// in-memory only and lost on shutdown.
//
// The cache key is FNV-1a64 over (model, system, user). Content address
// for the entry is a separate SHA-256 hash (independent from the key, so
// a collision on the key still doesn't let two distinct texts share an
// entry undetected).
package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CacheEntry is one cached LLM response.
type CacheEntry struct {
	Key         string    `json:"key"`          // FNV-1a64(model||system||user) hex
	ContentSHA  string    `json:"content_sha"`  // SHA-256(text) hex
	Text        string    `json:"text"`         // raw LLM response
	StoredAt    time.Time `json:"stored_at"`
	Model       string    `json:"model"`
}

// CacheStats reports aggregate metrics.
type CacheStats struct {
	Hits        int    `json:"hits"`
	Misses      int    `json:"misses"`
	Sets        int    `json:"sets"`
	Evictions   int    `json:"evictions"` // TTL expired or integrity failure
	IntegrityFails int `json:"integrity_fails"` // INV-5: stored text mismatch
	Size        int    `json:"size"`
	Path        string `json:"path"`
}

// AnomalySink is invoked when Cache detects a failure mode.
// Production wires this to safety.AnomalyDetector; tests leave it nil.
type AnomalySink func(kind, detail string)

type Cache struct {
	mu        sync.RWMutex
	path      string
	ttl       time.Duration
	anomalies AnomalySink

	entries map[string]CacheEntry
	stats   CacheStats
}

// NewCache opens (or creates) a cache at path. Empty path = in-memory only.
// ttl <= 0 = default 1h.
func NewCache(path string, ttl time.Duration) (*Cache, error) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	c := &Cache{
		path:    path,
		ttl:     ttl,
		entries: map[string]CacheEntry{},
		stats:   CacheStats{Path: path},
	}
	if path == "" {
		return c, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("llm cache: mkdir: %w", err)
	}
	if err := c.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("llm cache: load: %w", err)
	}
	return c, nil
}

// SetAnomalySink wires the cache to an anomaly detector. Pass nil to detach.
func (c *Cache) SetAnomalySink(sink AnomalySink) { c.anomalies = sink }

func (c *Cache) alert(kind, detail string) {
	if c.anomalies != nil {
		c.anomalies(kind, detail)
	}
}

// Key returns the cache key for a (model, system, user) tuple.
// FNV-1a64 over the concatenated triple, separators = \x00.
func Key(model, system, user string) string {
	h := fnv.New64a()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(user))
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the cached text for a (model, system, user) tuple. If the
// stored entry exists but its content SHA doesn't match the stored text,
// INV-5 fires: treat as miss, increment IntegrityFails, evict the entry,
// emit anomaly "cache_integrity_violation".
func (c *Cache) Get(model, system, user string) (string, bool, error) {
	if c == nil {
		return "", false, nil
	}
	key := Key(model, system, user)
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		c.recordMiss()
		return "", false, nil
	}
	if time.Since(entry.StoredAt) > c.ttl {
		c.evict(key, "ttl_expired")
		return "", false, nil
	}
	// INV-5: re-hash the stored text and compare to entry.ContentSHA.
	actual := HashText(entry.Text)
	if actual != entry.ContentSHA {
		c.evict(key, "integrity_violation")
		c.stats.IntegrityFails++
		c.alert("cache_integrity_violation",
			fmt.Sprintf("key=%s stored=%s computed=%s", key, entry.ContentSHA, actual))
		return "", false, nil
	}
	c.mu.Lock()
	c.stats.Hits++
	c.mu.Unlock()
	return entry.Text, true, nil
}

// Set stores a response. If a compatible entry exists, the stored_at
// is refreshed. Persistence (if path != "") is atomic via tmp+rename.
func (c *Cache) Set(model, system, user, text string) error {
	if c == nil {
		return nil
	}
	key := Key(model, system, user)
	entry := CacheEntry{
		Key:        key,
		ContentSHA: HashText(text),
		Text:       text,
		StoredAt:   time.Now().UTC(),
		Model:      model,
	}
	c.mu.Lock()
	c.entries[key] = entry
	c.stats.Sets++
	c.stats.Size = len(c.entries)
	c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	return c.persist()
}

// Stats returns a snapshot of the cache counters.
func (c *Cache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.stats
	s.Size = len(c.entries)
	return s
}

// Clear removes all entries. Persists if path != "". Returns Evictions count.
func (c *Cache) Clear() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	evicted := len(c.entries)
	c.entries = map[string]CacheEntry{}
	c.stats.Size = 0
	c.stats.Evictions += evicted
	c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	return c.persist()
}

// HashText returns SHA-256(text) hex.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ----- internal helpers -----

func (c *Cache) recordMiss() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.stats.Misses++
	c.mu.Unlock()
}

func (c *Cache) evict(key, reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, key)
	c.stats.Evictions++
	c.stats.Size = len(c.entries)
	c.mu.Unlock()
	if c.path != "" {
		_ = c.persist()
	}
}

// load reads the JSON file into memory, dropping expired entries.
func (c *Cache) load() error {
	if c.path == "" {
		return nil
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	var file struct {
		Entries map[string]CacheEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	now := time.Now()
	for k, e := range file.Entries {
		if now.Sub(e.StoredAt) <= c.ttl {
			c.entries[k] = e
		}
	}
	c.stats.Size = len(c.entries)
	return nil
}

// persist writes the entries atomically (tmp + rename).
func (c *Cache) persist() error {
	if c.path == "" {
		return nil
	}
	c.mu.RLock()
	snapshot := make(map[string]CacheEntry, len(c.entries))
	for k, v := range c.entries {
		snapshot[k] = v
	}
	c.mu.RUnlock()

	file := struct {
		Entries map[string]CacheEntry `json:"entries"`
	}{Entries: snapshot}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
