package nli

import (
	"errors"
	"testing"
	"time"
)

// --- Key ------------------------------------------------------------------

func TestKey_String_DifferentForDifferentProviders(t *testing.T) {
	t.Parallel()
	a := Key{ProviderID: "deberta", Premise: "p", Hypothesis: "h"}.String()
	b := Key{ProviderID: "minicheck", Premise: "p", Hypothesis: "h"}.String()
	if a == b {
		t.Errorf("same key for different providers: %s", a)
	}
}

func TestKey_String_DifferentForDifferentPremise(t *testing.T) {
	t.Parallel()
	a := Key{ProviderID: "p", Premise: "alpha", Hypothesis: "h"}.String()
	b := Key{ProviderID: "p", Premise: "beta", Hypothesis: "h"}.String()
	if a == b {
		t.Errorf("same key for different premises: %s", a)
	}
}

func TestKey_String_DifferentForDifferentHypothesis(t *testing.T) {
	t.Parallel()
	a := Key{ProviderID: "p", Premise: "x", Hypothesis: "alpha"}.String()
	b := Key{ProviderID: "p", Premise: "x", Hypothesis: "beta"}.String()
	if a == b {
		t.Errorf("same key for different hypotheses: %s", a)
	}
}

func TestKey_String_NULSeparatorPreventsAmbiguity(t *testing.T) {
	t.Parallel()
	// Concatenation ambiguity: "a" + "bc" vs "ab" + "c" must give
	// different keys. The NUL separator ensures this even though
	// the inputs differ.
	a := Key{ProviderID: "p", Premise: "abc", Hypothesis: "def"}.String()
	b := Key{ProviderID: "p", Premise: "abcd", Hypothesis: "ef"}.String()
	if a == b {
		t.Errorf("concatenation ambiguity: %s == %s", a, b)
	}
	// And directly: same characters split differently.
	c := Key{ProviderID: "p", Premise: "abc", Hypothesis: "x"}.String()
	d := Key{ProviderID: "p", Premise: "abcx", Hypothesis: ""}.String()
	if c == "" || d == "" {
		t.Fatal("empty key for non-empty inputs")
	}
}

func TestKey_String_Stable(t *testing.T) {
	t.Parallel()
	k := Key{ProviderID: "p", Premise: "x", Hypothesis: "y"}
	if k.String() != k.String() {
		t.Errorf("Key.String not stable")
	}
	if len(k.String()) != 64 {
		t.Errorf("SHA-256 hex should be 64 chars, got %d", len(k.String()))
	}
}

func TestKey_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		k    Key
		want bool
	}{
		{"valid", Key{"p", "x", "y"}, true},
		{"empty ProviderID", Key{"", "x", "y"}, false},
		{"empty Premise", Key{"p", "", "y"}, false},
		{"empty Hypothesis", Key{"p", "x", ""}, false},
		{"all empty", Key{}, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.k.Validate()
			if (err == nil) != tc.want {
				t.Errorf("Validate(%v): err=%v, want valid=%v", tc.k, err, tc.want)
			}
		})
	}
}

// --- InMemoryLRU ---------------------------------------------------------

func TestNewInMemoryLRU_Validation(t *testing.T) {
	t.Parallel()
	tests := []int{-1, 0}
	for _, n := range tests {
		_, err := NewInMemoryLRU(n)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("NewInMemoryLRU(%d): err=%v, want ErrInvalidConfig", n, err)
		}
	}
	if c, err := NewInMemoryLRU(10); err != nil || c == nil {
		t.Errorf("NewInMemoryLRU(10): c=%v, err=%v", c, err)
	}
}

func TestInMemoryLRU_PutAndGet_Hit(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	want := Score{Label: LabelEntailment, Confidence: 0.9, ProviderID: "p", LatencyMS: 5}
	key := Key{"p", "prem", "hyp"}
	if _, err := c.Put(key, want, time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("Get miss after Put")
	}
	if got != want {
		t.Errorf("Get=%v, want %v", got, want)
	}
}

func TestInMemoryLRU_Get_Miss(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	got, ok := c.Get(Key{"p", "x", "y"})
	if ok || got != (Score{}) {
		t.Errorf("Get on empty: ok=%v, got=%v", ok, got)
	}
}

func TestInMemoryLRU_TTL_ExpiredEntryReturnsFalse(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	key := Key{"p", "x", "y"}
	score := Score{Label: LabelNeutral, Confidence: 0.5, ProviderID: "p"}
	// TTL 1ms + sleep > TTL.
	if _, err := c.Put(key, score, time.Millisecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	got, ok := c.Get(key)
	if ok || got != (Score{}) {
		t.Errorf("expired entry: ok=%v, got=%v", ok, got)
	}
	// The expired entry must have been silently evicted.
	if c.Size() != 0 {
		t.Errorf("Size=%d after expired Get, want 0 (lazy eviction)", c.Size())
	}
}

func TestInMemoryLRU_Put_InvalidTTL(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	_, err := c.Put(Key{"p", "x", "y"}, Score{}, 0)
	if !errors.Is(err, ErrCacheInvalidTTL) {
		t.Errorf("Put ttl=0: err=%v, want ErrCacheInvalidTTL", err)
	}
	_, err = c.Put(Key{"p", "x", "y"}, Score{}, -1)
	if !errors.Is(err, ErrCacheInvalidTTL) {
		t.Errorf("Put ttl=-1: err=%v, want ErrCacheInvalidTTL", err)
	}
}

func TestInMemoryLRU_Put_InvalidKey(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	_, err := c.Put(Key{}, Score{}, time.Hour)
	if err == nil {
		t.Errorf("Put empty Key: err=nil, want non-nil")
	}
}

func TestInMemoryLRU_Get_InvalidKey(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	if _, ok := c.Get(Key{}); ok {
		t.Errorf("Get empty Key: ok=true, want false")
	}
}

func TestInMemoryLRU_LRUEviction(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(3)
	for i, k := range []Key{{"p", "a", "1"}, {"p", "a", "2"}, {"p", "a", "3"}} {
		score := Score{Label: LabelEntailment, Confidence: float64(i), ProviderID: "p"}
		if _, err := c.Put(k, score, time.Hour); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if c.Size() != 3 {
		t.Fatalf("Size=%d, want 3", c.Size())
	}
	// Touch k1 (move to front).
	if _, ok := c.Get(Key{"p", "a", "1"}); !ok {
		t.Fatal("Get k1 miss")
	}
	// Insert k4 — must evict k2 (now LRU back).
	if _, err := c.Put(Key{"p", "a", "4"}, Score{Label: LabelEntailment, Confidence: 4, ProviderID: "p"}, time.Hour); err != nil {
		t.Fatalf("Put k4: %v", err)
	}
	if c.Size() != 3 {
		t.Errorf("Size=%d after Put k4, want 3", c.Size())
	}
	if _, ok := c.Get(Key{"p", "a", "2"}); ok {
		t.Errorf("k2 should be evicted (LRU), but was present")
	}
	// k1, k3, k4 must still be present.
	for _, k := range []Key{{"p", "a", "1"}, {"p", "a", "3"}, {"p", "a", "4"}} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("Get %v: ok=false, want true", k)
		}
	}
}

func TestInMemoryLRU_OverwritePreservesRecency(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(2)
	scoreA := Score{Label: LabelEntailment, Confidence: 0.1, ProviderID: "p"}
	scoreB := Score{Label: LabelContradiction, Confidence: 0.9, ProviderID: "p"}
	k := Key{"p", "x", "y"}
	if _, err := c.Put(k, scoreA, time.Hour); err != nil {
		t.Fatalf("Put A: %v", err)
	}
	if _, err := c.Put(Key{"p", "x", "y2"}, scoreB, time.Hour); err != nil {
		t.Fatalf("Put B: %v", err)
	}
	// Touch A (move to front).
	if _, ok := c.Get(Key{"p", "x", "y"}); !ok {
		t.Fatal("A miss")
	}
	// Overwrite A — must move to front, not become LRU.
	scoreA2 := Score{Label: LabelNeutral, Confidence: 0.5, ProviderID: "p"}
	if _, err := c.Put(k, scoreA2, time.Hour); err != nil {
		t.Fatalf("Overwrite A: %v", err)
	}
	// Insert a third key — must evict B, not A.
	if _, err := c.Put(Key{"p", "x", "y3"}, scoreA, time.Hour); err != nil {
		t.Fatalf("Put third: %v", err)
	}
	if _, ok := c.Get(Key{"p", "x", "y"}); !ok {
		t.Errorf("A should be present after overwrite (moved to front)")
	}
	if _, ok := c.Get(Key{"p", "x", "y2"}); ok {
		t.Errorf("B should have been evicted (was LRU after A moved)")
	}
}

func TestInMemoryLRU_Clear(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	for i := 0; i < 5; i++ {
		if _, err := c.Put(Key{"p", "x", string(rune('a' + i))}, Score{}, time.Hour); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	c.Clear()
	if c.Size() != 0 {
		t.Errorf("Size after Clear=%d, want 0", c.Size())
	}
}

func TestInMemoryLRU_MaxEntries(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(7)
	if c.MaxEntries() != 7 {
		t.Errorf("MaxEntries=%d, want 7", c.MaxEntries())
	}
}

func TestInMemoryLRU_PutTwice_ReplacesNotDoubles(t *testing.T) {
	t.Parallel()
	c, _ := NewInMemoryLRU(10)
	k := Key{"p", "x", "y"}
	if _, err := c.Put(k, Score{Label: LabelEntailment, Confidence: 0.1, ProviderID: "p"}, time.Hour); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	if _, err := c.Put(k, Score{Label: LabelContradiction, Confidence: 0.9, ProviderID: "p"}, time.Hour); err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if c.Size() != 1 {
		t.Errorf("Size after double-Put=%d, want 1", c.Size())
	}
	got, ok := c.Get(k)
	if !ok || got.Label != LabelContradiction || got.Confidence != 0.9 {
		t.Errorf("Get after overwrite: got=%v ok=%v, want contradiction 0.9", got, ok)
	}
}