// Package server - active_session_resolver_test.go: cache +
// miss-path tests for StoreBackedActiveSessionResolver.
//
// v2.0.2 regression: the resolver's lookup function is pluggable
// (ActiveSessionLookup), so unit tests can substitute an in-memory
// counter without embedding the 70-method store.Store interface.
package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingLookup returns a session_id if its values map has it, ""
// otherwise. Records the number of calls in atomic.Int64 so the
// test can assert hit rate.
type countingLookup struct {
	mu     sync.Mutex
	values map[string]string
	calls  atomic.Int64
}

func (l *countingLookup) lookup(_ context.Context, projectID string) (string, error) {
	l.calls.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.values[projectID], nil
}

func (l *countingLookup) set(projectID, sessID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.values == nil {
		l.values = make(map[string]string)
	}
	if sessID == "" {
		delete(l.values, projectID)
	} else {
		l.values[projectID] = sessID
	}
}

func (l *countingLookup) callCount() int64 {
	return l.calls.Load()
}

// TestStoreBackedResolver_CacheHitWithinTTL: N calls within CacheTTL
// must touch the underlying lookup ONCE.
func TestStoreBackedResolver_CacheHitWithinTTL(t *testing.T) {
	lookup := &countingLookup{}
	lookup.set("default", "sess-XYZ")

	r := NewStoreBackedActiveSessionResolver(lookup.lookup)
	r.WithCacheTTL(1 * time.Second)

	// First call: cache miss → lookup. Subsequent: cache hits.
	for i := 0; i < 100; i++ {
		got := r.ActiveSessionID(context.Background(), "default")
		if got != "sess-XYZ" {
			t.Fatalf("call %d: want sess-XYZ, got %q", i, got)
		}
	}
	if got := lookup.callCount(); got != 1 {
		t.Errorf("Lookup call count within TTL: want 1, got %d", got)
	}
}

// TestStoreBackedResolver_CacheExpiresAfterTTL: after CacheTTL elapses,
// the next call must re-hit the lookup.
func TestStoreBackedResolver_CacheExpiresAfterTTL(t *testing.T) {
	lookup := &countingLookup{}
	lookup.set("default", "sess-XYZ")

	// Use a controllable clock so the test doesn't actually sleep.
	var now time.Time
	r := NewStoreBackedActiveSessionResolver(lookup.lookup)
	r.CacheTTL = 1 * time.Second
	r.Now = func() time.Time { return now }

	r.ActiveSessionID(context.Background(), "default") // call 1: miss
	now = now.Add(500 * time.Millisecond)
	r.ActiveSessionID(context.Background(), "default") // call 2: hit (still in TTL)

	if got := lookup.callCount(); got != 1 {
		t.Errorf("after 2 calls within TTL: want lookup called 1 time, got %d", got)
	}

	now = now.Add(600 * time.Millisecond) // total 1.1s past first call
	r.ActiveSessionID(context.Background(), "default") // call 3: TTL expired → miss

	if got := lookup.callCount(); got != 2 {
		t.Errorf("after TTL expiry: want lookup called 2 times total, got %d", got)
	}
}

// TestStoreBackedResolver_EmptyProjectIDReturnsEmptyWithoutLookup:
// passing "" must short-circuit without calling the lookup at all.
func TestStoreBackedResolver_EmptyProjectIDReturnsEmptyWithoutLookup(t *testing.T) {
	lookup := &countingLookup{}
	lookup.set("default", "sess-XYZ")
	r := NewStoreBackedActiveSessionResolver(lookup.lookup)
	got := r.ActiveSessionID(context.Background(), "")
	if got != "" {
		t.Fatalf("empty projectID: want empty string, got %q", got)
	}
	if got := lookup.callCount(); got != 0 {
		t.Errorf("empty projectID should NOT call lookup; got %d calls", got)
	}
}

// TestStoreBackedResolver_LookupErrorTreatedAsEmpty: when the lookup
// returns an error, the resolver returns "" and DOES NOT cache the
// error (the next call tries again).
func TestStoreBackedResolver_LookupErrorTreatedAsEmpty(t *testing.T) {
	lookup := func(_ context.Context, _ string) (string, error) {
		return "", errAlways
	}
	r := NewStoreBackedActiveSessionResolver(lookup)
	r.WithCacheTTL(1 * time.Hour) // long TTL — we'd notice if the error got cached

	got := r.ActiveSessionID(context.Background(), "default")
	if got != "" {
		t.Errorf("error path: want empty string, got %q", got)
	}
	// Second call should ALSO hit the lookup (no error-cache).
	got = r.ActiveSessionID(context.Background(), "default")
	if got != "" {
		t.Errorf("second call: want empty string, got %q", got)
	}
}

// errAlways is a sentinel for the lookup-error test above.
var errAlways = lookupErr("test: lookup always fails")

type lookupErr string

func (e lookupErr) Error() string { return string(e) }

// TestStoreBackedResolver_InvalidateClearsEntry: Invalidate(projectID)
// forces the next call to miss the cache.
func TestStoreBackedResolver_InvalidateClearsEntry(t *testing.T) {
	lookup := &countingLookup{}
	lookup.set("default", "sess-XYZ")

	r := NewStoreBackedActiveSessionResolver(lookup.lookup)
	r.WithCacheTTL(1 * time.Hour)

	r.ActiveSessionID(context.Background(), "default") // call 1: miss
	if got := lookup.callCount(); got != 1 {
		t.Fatalf("precondition: expected 1 lookup call, got %d", got)
	}

	r.Invalidate("default")
	lookup.set("default", "sess-NEW")

	got := r.ActiveSessionID(context.Background(), "default")
	if got != "sess-NEW" {
		t.Fatalf("after Invalidate + session swap: want sess-NEW, got %q", got)
	}
	if got := lookup.callCount(); got != 2 {
		t.Errorf("after Invalidate: expected 2 lookup calls, got %d", got)
	}
}

// TestStoreBackedResolver_DisabledCache: CacheTTL=0 disables caching
// — every call hits the lookup.
func TestStoreBackedResolver_DisabledCache(t *testing.T) {
	lookup := &countingLookup{}
	lookup.set("default", "sess-XYZ")

	r := NewStoreBackedActiveSessionResolver(lookup.lookup)
	r.WithCacheTTL(0) // disabled

	for i := 0; i < 5; i++ {
		r.ActiveSessionID(context.Background(), "default")
	}
	if got := lookup.callCount(); got != 5 {
		t.Errorf("CacheTTL=0 must hit lookup every time: want 5, got %d", got)
	}
}
