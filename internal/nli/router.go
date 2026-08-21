package nli

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// Router selects primary or fallback per Policy and emits Score with
// the actual provider id (provenance). It NEVER retries on
// ErrProviderBadResponse (contract bug — fallback would have the same
// contract). It DOES retry on ErrProviderTimeout / ErrProviderUnavailable /
// ErrProviderRateLimited when FallbackEnabled is true.
//
// Hard invariants:
//   - Score returned by Router ALWAYS has ProviderID set (one of the
//     configured providers' ID()), or returns a sealed error.
//   - Router does not modify Score.Label / Confidence (those are
//     determined by the provider). It MAY set Score.LatencyMS to the
//     total wall-clock across both providers.
//   - If the input fails validation (empty, oversized), Router
//     forwards ErrInputEmpty / ErrInputTooLarge without trying either
//     provider. Validation runs once on the entry to the router, not
//     again per provider (the byte caps are identical).
type Router struct {
	primary   Provider
	fallback  Provider // nil if !FallbackEnabled
	budgetMS  int64
	maxPBytes int
	maxHBytes int

	// Stats counters; accessible via Stats() snapshot. Tests use them
	// to verify fallback was taken.
	primaryOK        atomic.Uint64
	primaryFail      atomic.Uint64
	fallbackOK       atomic.Uint64
	fallbackFail     atomic.Uint64
	fallbackSkipped  atomic.Uint64 // ErrProviderBadResponse — we do not retry
}

// RouterStats is the observability snapshot for a Router.
type RouterStats struct {
	PrimarySuccesses   uint64
	PrimaryFailures    uint64
	FallbackSuccesses  uint64
	FallbackFailures   uint64
	FallbackSkipped    uint64 // ErrProviderBadResponse from primary
}

// NewRouter validates cfg and returns a *Router. cfg.Primary must have a
// non-nil Provider; cfg.Fallback's Provider may be nil if !FallbackEnabled.
// Returns ErrInvalidConfig if anything is wrong.
func NewRouter(primary, fallback Provider, cfg Config) (*Router, error) {
	if primary == nil {
		return nil, fmt.Errorf("%w: primary is nil", ErrInvalidConfig)
	}
	if cfg.FallbackEnabled && fallback == nil {
		return nil, fmt.Errorf("%w: FallbackEnabled but fallback is nil", ErrInvalidConfig)
	}
	if primary.ID() == "" {
		return nil, fmt.Errorf("%w: primary has empty ID", ErrInvalidConfig)
	}
	if fallback != nil && fallback.ID() == "" {
		return nil, fmt.Errorf("%w: fallback has empty ID", ErrInvalidConfig)
	}
	if fallback != nil && primary.ID() == fallback.ID() {
		return nil, fmt.Errorf("%w: primary and fallback have the same ID", ErrInvalidConfig)
	}
	cfg = cfg.DefaultsFor()
	return &Router{
		primary:   primary,
		fallback:  fallback,
		budgetMS:  cfg.LatencyBudgetMS,
		maxPBytes: cfg.MaxPremiseBytes,
		maxHBytes: cfg.MaxHypothesisBytes,
	}, nil
}

// Stats returns a snapshot of the router's counters.
func (r *Router) Stats() RouterStats {
	return RouterStats{
		PrimarySuccesses:  r.primaryOK.Load(),
		PrimaryFailures:   r.primaryFail.Load(),
		FallbackSuccesses: r.fallbackOK.Load(),
		FallbackFailures:  r.fallbackFail.Load(),
		FallbackSkipped:   r.fallbackSkipped.Load(),
	}
}

// ID returns the primary provider's ID. The Router is a transparent
// policy gate around the primary Provider; provenance flows to the
// underlying provider (T05 invariant). When the primary is wrapped in
// a CachedProvider (T06), ID() passes through to the inner provider.
func (r *Router) ID() string {
	return r.primary.ID()
}

// Reset clears the counters. Tests use this between cases; production
// never resets.
func (r *Router) Reset() {
	r.primaryOK.Store(0)
	r.primaryFail.Store(0)
	r.fallbackOK.Store(0)
	r.fallbackFail.Store(0)
	r.fallbackSkipped.Store(0)
}

// Score runs primary → optional fallback → returns Score with provenance.
func (r *Router) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	// Pre-flight: validate inputs once. Providers also validate, but
	// Router is the policy gate — refuse to call out for inputs we
	// already know are bad.
	if premise == "" || hypothesis == "" {
		return Score{}, ErrInputEmpty
	}
	if len(premise) > r.maxPBytes {
		return Score{}, fmt.Errorf("%w: premise=%d > %d", ErrInputTooLarge, len(premise), r.maxPBytes)
	}
	if len(hypothesis) > r.maxHBytes {
		return Score{}, fmt.Errorf("%w: hypothesis=%d > %d", ErrInputTooLarge, len(hypothesis), r.maxHBytes)
	}

	primaryStart := time.Now()
	primaryCtx, cancel := context.WithTimeout(ctx, time.Duration(r.budgetMS)*time.Millisecond)
	score, err := r.primary.Score(primaryCtx, premise, hypothesis)
	cancel()
	primaryElapsed := time.Since(primaryStart).Milliseconds()

	if err == nil {
		r.primaryOK.Add(1)
		// Score.LatencyMS from the provider is the primary call's
		// latency. Replace with the router-observed latency to keep
		// the audit honest (includes connect / TLS / cancellation
		// overhead inside the budget).
		score.LatencyMS = primaryElapsed
		return score, nil
	}

	// Primary failed.
	r.primaryFail.Add(1)

	// BadResponse is a contract bug; do not retry.
	if errors.Is(err, ErrProviderBadResponse) {
		r.fallbackSkipped.Add(1)
		return Score{}, fmt.Errorf("%w: primary returned bad response", ErrNoProvider)
	}

	// No fallback → return primary's error wrapped with ErrNoProvider.
	if !r.hasFallback() {
		return Score{}, fmt.Errorf("%w: primary failed and no fallback: %w", ErrNoProvider, err)
	}

	// ErrInput* — same input, fallback will reject too. Skip.
	if errors.Is(err, ErrInputEmpty) || errors.Is(err, ErrInputTooLarge) {
		return Score{}, fmt.Errorf("%w: %v", ErrNoProvider, err)
	}

	// Retryable: ErrProviderTimeout / ErrProviderUnavailable / ErrProviderRateLimited.
	fallbackStart := time.Now()
	fbScore, fbErr := r.fallback.Score(ctx, premise, hypothesis)
	fallbackElapsed := time.Since(fallbackStart).Milliseconds()
	if fbErr != nil {
		r.fallbackFail.Add(1)
		return Score{}, fmt.Errorf("%w: primary=%v, fallback=%v", ErrNoProvider, err, fbErr)
	}
	r.fallbackOK.Add(1)
	// Latency: report the fallback call's latency, not the sum.
	// (Provenance is fallback.ProviderID; the caller's audit row is
	// tagged with ProviderID — the latency field is a sibling.)
	fbScore.LatencyMS = fallbackElapsed
	return fbScore, nil
}

func (r *Router) hasFallback() bool { return r.fallback != nil }