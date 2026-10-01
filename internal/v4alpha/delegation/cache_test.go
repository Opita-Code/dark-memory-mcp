// Package delegation — tests for cache.go.
//
// Tests cover:
//   - CacheKey determinism (same inputs → same key)
//   - Get returns nil on miss / expired
//   - Set + Get round-trip
//   - Invalidate removes entry
//   - TTL via env (DARK_DELEGATION_CACHE_TTL)
//   - Thread-safety smoke test
package delegation

import (
	"sync"
	"testing"
	"time"
)

// TestCacheKey_Deterministic verifies that the same inputs produce the
// same sha256 key.
func TestCacheKey_Deterministic(t *testing.T) {
	k1 := CacheKey("C7", "task here", "default", "inherit")
	k2 := CacheKey("C7", "task here", "default", "inherit")
	if k1 != k2 {
		t.Errorf("CacheKey not deterministic: %q vs %q", k1, k2)
	}
	if len(k1) != 64 {
		t.Errorf("CacheKey length = %d; want 64 (sha256 hex)", len(k1))
	}
}

// TestCacheKey_DifferentInputs verifies that different inputs produce
// different keys.
func TestCacheKey_DifferentInputs(t *testing.T) {
	k1 := CacheKey("C7", "task one", "default", "inherit")
	k2 := CacheKey("C7", "task two", "default", "inherit")
	if k1 == k2 {
		t.Error("CacheKey produced same value for different tasks")
	}
	k3 := CacheKey("C7", "task one", "default", "sonnet")
	if k1 == k3 {
		t.Error("CacheKey did not differentiate model_floor")
	}
}

// TestCache_GetMissReturnsNil verifies that an empty cache returns nil.
func TestCache_GetMissReturnsNil(t *testing.T) {
	c := NewExtractCache(DefaultCacheTTL)
	if got := c.Get("missing-key"); got != nil {
		t.Errorf("Get(missing) = %+v; want nil", got)
	}
}

// TestCache_SetGetRoundtrip verifies the happy path: Set then Get returns
// the same data + CacheHit=true.
func TestCache_SetGetRoundtrip(t *testing.T) {
	c := NewExtractCache(DefaultCacheTTL)
	res := ExtractResult{
		Decision:  "delegate",
		Subtasks:  []Subtask{{ID: "s1", Description: "first subtask description here"}},
		Reasoning: "test",
		Verdict:   "aligned",
	}
	key := "test-key"
	c.Set(key, res)
	got := c.Get(key)
	if got == nil {
		t.Fatal("Get after Set returned nil")
	}
	if !got.CacheHit {
		t.Error("CacheHit = false; want true")
	}
	if got.Verdict != "cached" {
		t.Errorf("Verdict = %q; want cached", got.Verdict)
	}
	if got.Decision != "delegate" {
		t.Errorf("Decision = %q; want delegate", got.Decision)
	}
	if len(got.Subtasks) != 1 {
		t.Errorf("Subtasks = %d; want 1", len(got.Subtasks))
	}
}

// TestCache_ExpiresAfterTTL verifies that an expired entry returns nil
// (treated as miss).
func TestCache_ExpiresAfterTTL(t *testing.T) {
	// Use a very short TTL.
	c := NewExtractCache(10 * time.Millisecond)
	res := ExtractResult{Decision: "delegate", Verdict: "aligned"}
	key := "expiring-key"
	c.Set(key, res)
	// Immediate Get → hit.
	if got := c.Get(key); got == nil {
		t.Fatal("immediate Get returned nil; want hit")
	}
	// Wait past TTL.
	time.Sleep(20 * time.Millisecond)
	if got := c.Get(key); got != nil {
		t.Errorf("post-TTL Get = %+v; want nil (expired)", got)
	}
}

// TestCache_Invalidate removes an entry.
func TestCache_Invalidate(t *testing.T) {
	c := NewExtractCache(DefaultCacheTTL)
	key := "test-key"
	c.Set(key, ExtractResult{Decision: "delegate"})
	c.Invalidate(key)
	if got := c.Get(key); got != nil {
		t.Errorf("Get after Invalidate = %+v; want nil", got)
	}
}

// TestCache_NilSafe verifies that operations on a nil pointer are no-ops.
func TestCache_NilSafe(t *testing.T) {
	var c *ExtractCache
	c.Set("k", ExtractResult{})            // no-op
	if got := c.Get("k"); got != nil {     // no-op
		t.Errorf("nil Get = %+v; want nil", got)
	}
	c.Invalidate("k") // no-op
	if size := c.Size(); size != 0 {
		t.Errorf("nil Size = %d; want 0", size)
	}
}

// TestCache_ConcurrentSafe verifies that the cache is safe under
// concurrent reads + writes. Smoke test only — full race coverage via
// `go test -race` in CI.
func TestCache_ConcurrentSafe(t *testing.T) {
	c := NewExtractCache(DefaultCacheTTL)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "key-" + itoa(n)
			c.Set(key, ExtractResult{Decision: "delegate"})
			_ = c.Get(key)
		}(i)
	}
	wg.Wait()
	if size := c.Size(); size != 100 {
		t.Errorf("Size after 100 concurrent sets = %d; want 100", size)
	}
}

// TestCacheTTLFromEnv verifies the env wrapper.
func TestCacheTTLFromEnv(t *testing.T) {
	// Default when unset.
	t.Setenv("DARK_DELEGATION_CACHE_TTL", "")
	if d := CacheTTLFromEnv(); d != DefaultCacheTTL {
		t.Errorf("unset env: TTL = %v; want %v", d, DefaultCacheTTL)
	}
	// Parsed when set.
	t.Setenv("DARK_DELEGATION_CACHE_TTL", "1800")
	if d := CacheTTLFromEnv(); d != 30*time.Minute {
		t.Errorf("1800s env: TTL = %v; want 30m", d)
	}
	// Out-of-range falls back to default.
	t.Setenv("DARK_DELEGATION_CACHE_TTL", "999999")
	if d := CacheTTLFromEnv(); d != DefaultCacheTTL {
		t.Errorf("out-of-range env: TTL = %v; want %v (default)", d, DefaultCacheTTL)
	}
}