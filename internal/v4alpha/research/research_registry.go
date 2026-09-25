// This file defines the adapter Registry: which BackendManifest serves
// each Intent, and the per-backend token bucket that enforces the rate
// budget from the manifest (M7: per-backend in-process token-bucket with
// operator-configurable budgets). Buckets derive capacity and refill from
// the manifest RateLimits grammar ("<n>/<window>"), so the budget the
// operator configures is exactly the budget the registry enforces.
// Result envelopes and the output gate live in research_result.go (cycle 3)
// and research_gate.go (cycle 4).
package research

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Sentinel errors for the Registry and Bucket. All callers should use
// errors.Is.
var (
	// ErrRegistryDuplicate is returned when Register sees an Intent that
	// already has a manifest.
	ErrRegistryDuplicate = errors.New("research: intent already registered")

	// ErrRegistryUnknown is returned when Get or Allow names an Intent or
	// backend the registry does not know.
	ErrRegistryUnknown = errors.New("research: unknown intent or backend")

	// ErrRegistryBadRate is returned when ConfigureBucket gets a rate spec
	// outside the "<n>/<window>" grammar.
	ErrRegistryBadRate = errors.New("research: bucket rate spec is malformed")

	// ErrBucketBadConfig is returned by NewBucket for non-positive
	// capacity or refill.
	ErrBucketBadConfig = errors.New("research: bucket capacity or refill is not positive")
)

// Clock supplies time to a Bucket. Tests inject a manual clock for
// deterministic refill; production passes nil for time.Now.
type Clock func() time.Time

// Bucket is a token-bucket rate limiter: capacity burst tokens, refilled
// continuously at refillPerSec. Zero value is unusable; build with
// NewBucket. Safe for concurrent use.
type Bucket struct {
	mu           sync.Mutex
	capacity     float64
	tokens       float64
	refillPerSec float64
	last         time.Time
	now          func() time.Time
}

// NewBucket builds a Bucket with the given burst capacity and refill rate
// (tokens per second). A nil clock selects time.Now. Non-positive capacity
// or refill yields ErrBucketBadConfig.
func NewBucket(capacity int, refillPerSec float64, clock Clock) (*Bucket, error) {
	if capacity <= 0 || refillPerSec <= 0 {
		return nil, ErrBucketBadConfig
	}
	now := time.Now
	if clock != nil {
		now = clock
	}
	return &Bucket{
		capacity:     float64(capacity),
		tokens:       float64(capacity),
		refillPerSec: refillPerSec,
		last:         now(),
		now:          now,
	}, nil
}

// Allow reports whether one call may proceed now, consuming a token when
// it may. Elapsed time refills the bucket up to capacity.
func (b *Bucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * b.refillPerSec
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Tokens returns the current whole-token balance for tests and observability.
func (b *Bucket) Tokens() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.tokens)
}

// Registry maps each Intent to its BackendManifest and enforces one token
// bucket per backend. Manifests are validated on Register and stored as
// copies; Get returns copies so callers cannot mutate registry state.
// Safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	manifests map[Intent]BackendManifest
	buckets   map[string]*Bucket
	clock     Clock
}

// NewRegistry builds an empty Registry. A nil clock selects time.Now for
// every bucket the registry creates.
func NewRegistry(clock Clock) *Registry {
	return &Registry{
		manifests: map[Intent]BackendManifest{},
		buckets:   map[string]*Bucket{},
		clock:     clock,
	}
}

// Register validates m and binds it to m.Intent. A duplicate Intent yields
// ErrRegistryDuplicate; an invalid manifest yields its Validate error.
func (r *Registry) Register(m BackendManifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.manifests[m.Intent]; dup {
		return ErrRegistryDuplicate
	}
	r.manifests[m.Intent] = m
	return nil
}

// Get returns the manifest for i, or ErrRegistryUnknown.
func (r *Registry) Get(i Intent) (BackendManifest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.manifests[i]
	if !ok {
		return BackendManifest{}, ErrRegistryUnknown
	}
	return m, nil
}

// List returns every registered manifest ordered by intent for
// deterministic output.
func (r *Registry) List() []BackendManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]BackendManifest, 0, len(r.manifests))
	for _, m := range r.manifests {
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Intent < out[b].Intent })
	return out
}

// ResolveBackends returns the ordered backend list for i (primary first),
// or ErrRegistryUnknown.
func (r *Registry) ResolveBackends(i Intent) ([]string, error) {
	m, err := r.Get(i)
	if err != nil {
		return nil, err
	}
	return m.ResolveBackends(), nil
}

// ConfigureBucket parses a "<n>/<window>" rate spec (the manifest
// RateLimits grammar) and installs the bucket for backend: burst n,
// refilled at n per window. A malformed spec yields ErrRegistryBadRate.
func (r *Registry) ConfigureBucket(backend, spec string) error {
	n, window, ok := parseRate(spec)
	if !ok || backend == "" {
		return ErrRegistryBadRate
	}
	b, err := NewBucket(n, float64(n)/window.Seconds(), r.clock)
	if err != nil {
		return ErrRegistryBadRate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buckets[backend] = b
	return nil
}

// Allow reports whether backend may be called now under its configured
// budget. An unconfigured backend yields ErrRegistryUnknown: callers must
// ConfigureBucket (or derive it from the manifest) before the first call,
// so no backend ever runs unbudgeted by accident.
func (r *Registry) Allow(backend string) (bool, error) {
	r.mu.RLock()
	b, ok := r.buckets[backend]
	r.mu.RUnlock()
	if !ok {
		return false, ErrRegistryUnknown
	}
	return b.Allow(), nil
}
