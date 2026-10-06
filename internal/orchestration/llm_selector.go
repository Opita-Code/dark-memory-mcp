// LLMSelector picks the right LLMClient for a given eval_type. The
// default implementation is OSINT-driven: at startup it queries
// (locally, from config) which model is best for each task and
// caches the result. Production deployments refresh the cache
// periodically via dark_research_*_recommend tools.
//
// Spec 173 O5: the OSINT layer is currently a config table (see
// recommended_models.go — RecommendedModels). Replacing it with
// live OSINT feeds is a Wave 4+ task — same shape, different data
// source.
package orchestration

import (
	"context"
	"fmt"
	"sync"
)

// LLMSelector returns the LLMClient to use for a given eval_type.
// The selector is consulted once per Judge call; implementations
// may cache, load-balance, or fail-over as needed.
type LLMSelector interface {
	// Select returns the client for the given eval_type, or an error
	// if no suitable client is available.
	Select(ctx context.Context, evalType string) (LLMClient, error)
	// ProviderFor returns the human-readable provider name the
	// selector would pick for the given eval_type, without actually
	// invoking the LLM. Useful for diagnostics and audit.
	ProviderFor(evalType string) string
	// RecommendedModelFor returns the OSINT-recommended model for
	// (provider, eval_type), or "" if provider is not in the
	// catalog (caller falls through to the client's own auto-config).
	RecommendedModelFor(provider, evalType string) string
}

// OSINTSelector is the default LLMSelector. It maps eval_type to a
// preferred client, with an optional override map.
//
// Why "OSINTSelector": in production we intend to populate
// Recommendations from OSINT feeds (pricing, benchmarks, freshness).
// For now the map is seeded with sensible defaults tuned for the
// dark-agents-v2 reference deployment (see RecommendedModels).
type OSINTSelector struct {
	mu       sync.RWMutex
	fallback LLMClient
	perType  map[string]LLMClient // override per eval_type
}

// NewOSINTSelector returns a selector that uses `fallback` for any
// eval_type not in the perType map. `fallback` is typically the
// SelfHarnessClient (auto-detected harness LLM).
func NewOSINTSelector(fallback LLMClient) *OSINTSelector {
	return &OSINTSelector{
		fallback: fallback,
		perType:  map[string]LLMClient{},
	}
}

// WithOverride sets a specific client for an eval_type. Returns
// the selector for chaining.
func (o *OSINTSelector) WithOverride(evalType string, c LLMClient) *OSINTSelector {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.perType[evalType] = c
	return o
}

// Select implements LLMSelector.
func (o *OSINTSelector) Select(ctx context.Context, evalType string) (LLMClient, error) {
	o.mu.RLock()
	client := o.fallback
	if c, ok := o.perType[evalType]; ok {
		client = c
	}
	o.mu.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("%w: no client for eval_type=%s", ErrNoLLMAvailable, evalType)
	}
	return client, nil
}

// ProviderFor implements LLMSelector.
func (o *OSINTSelector) ProviderFor(evalType string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if c, ok := o.perType[evalType]; ok && c != nil {
		return c.Name()
	}
	if o.fallback != nil {
		return o.fallback.Name()
	}
	return ""
}

// RecommendedModelFor implements LLMSelector. Delegates to the
// package-level RecommendedModel from recommended_models.go.
func (o *OSINTSelector) RecommendedModelFor(provider, evalType string) string {
	return RecommendedModel(provider, evalType)
}

// Void-import guard for context (Select takes ctx for future
// OSINT-query implementations that may need to fetch).
var _ = context.Background

// NoLLMSelector returns ErrNoLLMAvailable from Select regardless of
// eval_type. Tests pass this to WithLLMSelector (or the
// newAsyncTestOrchestrator helper) when they want deterministic
// "no LLM available" semantics: the judge pipeline runs to
// completion (verdict=needs_human), the LLM client never opens a
// socket, and no HealthRegistry goroutine is launched.
//
// This is the Phase 14 lesson applied: env-var coupling ("test
// must clear API keys before running") is a code smell. The
// explicit injection removes the coupling AND the goroutine leak
// that comes with the package-level DefaultFailoverClient
// singleton (HealthRegistry.Start launches a probe loop the test
// cannot stop — see test_hang analysis in v4-status §1.12.8).
//
// Why exported: tests across the orchestration package reuse this
// selector. The zero-value struct is valid; no constructor needed.
type NoLLMSelector struct{}

// Select implements LLMSelector. Returns ErrNoLLMAvailable with the
// eval_type attached so callers can log the intended target even
// when no client is wired.
func (NoLLMSelector) Select(_ context.Context, evalType string) (LLMClient, error) {
	return nil, fmt.Errorf("%w: no client for eval_type=%s (NoLLMSelector injected for tests)",
		ErrNoLLMAvailable, evalType)
}

// ProviderFor implements LLMSelector. Returns "" — no provider is
// selected when no LLM is available.
func (NoLLMSelector) ProviderFor(string) string { return "" }

// RecommendedModelFor implements LLMSelector. Returns "" — no
// recommendation when no provider is selected.
func (NoLLMSelector) RecommendedModelFor(string, string) string { return "" }

// Compile-time guard: NoLLMSelector satisfies LLMSelector.
var _ LLMSelector = NoLLMSelector{}
