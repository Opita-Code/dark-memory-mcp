// health.go — ProviderHealthRegistry (spec 1188 T4).
//
// LiteLLM-style health tracking (docs.litellm.ai/docs/proxy/
// health_check_routing, consulted 2026-08-15):
//
//   - background probe loop removes unhealthy providers from the
//     routing pool PROACTIVELY (before a judge call hits a dead key);
//   - allowed_fails_policy per failure class: N means "cooldown on
//     the (N+1)th failure" (auth→0 = first auth failure cools down);
//   - cooldown_time must be > health_check_interval so failure
//     counters accumulate across cycles (LiteLLM caveat);
//   - safety net: if EVERY candidate is in cooldown, routing fails
//     open (returns all candidates) instead of hard-failing.
//
// Counters are shared between the background probe loop and judge
// request failures (RecordFailure is called by the failover client
// too), exactly like LiteLLM.
package llm

import (
	"context"
	"log"
	"sync"
	"time"
)

// FailClass is the failure taxonomy shared by probes and judge calls.
type FailClass string

const (
	// FailAuth = bad API key (401/403).
	FailAuth FailClass = "auth"
	// FailRate = rate limit (429).
	FailRate FailClass = "rate"
	// FailTimeout = deadline exceeded / 408.
	FailTimeout FailClass = "timeout"
	// FailServer = 5xx / transport failure.
	FailServer FailClass = "server"
)

// DefaultAllowedFailsPolicy is the spec 1188 policy.
var DefaultAllowedFailsPolicy = map[FailClass]int{
	FailAuth:    0, // cooldown on the 1st auth failure
	FailRate:    3, // cooldown on the 4th rate limit
	FailTimeout: 3, // cooldown on the 4th timeout
	FailServer:  1, // cooldown on the 2nd 5xx
}

// HealthOptions configures a HealthRegistry.
type HealthOptions struct {
	// AllowedFails maps failure class → tolerated failures before
	// cooldown (N = cooldown on failure N+1). Nil = default policy.
	AllowedFails map[FailClass]int
	// CooldownTime is how long a provider stays out of the pool.
	// Default 60s.
	CooldownTime time.Duration
	// Interval is the background probe cycle period. Default 30s.
	Interval time.Duration
	// IgnoreTransient (false) — when true, 429/408 failures never
	// count toward cooldown.
	IgnoreTransient bool
}

// ProviderHealthState is the exported per-provider health snapshot.
type ProviderHealthState struct {
	ProviderID    string           `json:"provider_id"`
	FailedCalls   map[string]int   `json:"failed_calls"`
	CooldownUntil *time.Time       `json:"cooldown_until,omitempty"`
	LastState     ProbeState       `json:"last_state"`
	LastCheckAt   *time.Time       `json:"last_check_at,omitempty"`
}

type providerHealth struct {
	failed        map[FailClass]int
	cooldownUntil time.Time
	lastState     ProbeState
	lastCheck     time.Time
}

// HealthRegistry tracks provider health in-process. Zero-value is not
// usable — construct via NewHealthRegistry.
type HealthRegistry struct {
	mu              sync.Mutex
	allowedFails    map[FailClass]int
	cooldownTime    time.Duration
	interval        time.Duration
	ignoreTransient bool

	providers map[string]*providerHealth

	// probeFn runs one probe for a provider id (injected for tests;
	// nil = registry runs no background probing).
	probeFn func(ctx context.Context, providerID string) ProbeResult

	cancel context.CancelFunc
	wg     sync.WaitGroup
	started bool
}

// NewHealthRegistry builds a registry with defaults applied.
func NewHealthRegistry(opts HealthOptions) *HealthRegistry {
	if opts.AllowedFails == nil {
		opts.AllowedFails = DefaultAllowedFailsPolicy
	}
	if opts.CooldownTime <= 0 {
		opts.CooldownTime = 60 * time.Second
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	return &HealthRegistry{
		allowedFails:    opts.AllowedFails,
		cooldownTime:    opts.CooldownTime,
		interval:        opts.Interval,
		ignoreTransient: opts.IgnoreTransient,
		providers:       map[string]*providerHealth{},
	}
}

// SetProbeFn wires the background probe (test seam). Must be called
// before Start.
func (h *HealthRegistry) SetProbeFn(fn func(ctx context.Context, providerID string) ProbeResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.probeFn = fn
}

// Start launches the background probe loop. Idempotent: a second call
// is a no-op. The loop stops on ctx cancellation or Stop().
func (h *HealthRegistry) Start(ctx context.Context) {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return
	}
	h.started = true
	loopCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.mu.Unlock()

	if h.probeFn == nil {
		return // nothing to probe; registry still tracks RecordFailure
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		t := time.NewTicker(h.interval)
		defer t.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-t.C:
				h.runProbeCycle(loopCtx)
			}
		}
	}()
}

// Stop cancels the background loop and waits for it to exit.
func (h *HealthRegistry) Stop() {
	h.mu.Lock()
	cancel := h.cancel
	h.cancel = nil
	h.started = false
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
}

// runProbeCycle probes every registered provider once.
func (h *HealthRegistry) runProbeCycle(ctx context.Context) {
	h.mu.Lock()
	probeFn := h.probeFn
	ids := make([]string, 0, len(h.providers))
	for id := range h.providers {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	if probeFn == nil {
		return
	}
	for _, id := range ids {
		res := probeFn(ctx, id)
		h.mu.Lock()
		ph := h.ensure(id)
		ph.lastCheck = time.Now()
		ph.lastState = res.State
		if res.State == ProbeValid || res.State == ProbeUnknown {
			// healthy (or unclassifiable — fail open).
			ph.failed = map[FailClass]int{}
			ph.cooldownUntil = time.Time{}
			h.mu.Unlock()
			continue
		}
		class := res.Class
		if class == "" {
			class = FailServer
		}
		if h.ignoreTransient && (class == FailRate || class == FailTimeout) {
			h.mu.Unlock()
			continue
		}
		ph.failed[class]++
		if ph.failed[class] > h.allowedFails[class] {
			ph.cooldownUntil = time.Now().Add(h.cooldownTime)
		}
		h.mu.Unlock()
	}
}

// RecordFailure accounts one request/probe failure for a provider.
// Cooldown triggers when the class counter exceeds the policy.
func (h *HealthRegistry) RecordFailure(providerID string, class FailClass) {
	if class == "" {
		class = FailServer
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ignoreTransient && (class == FailRate || class == FailTimeout) {
		return
	}
	ph := h.ensure(providerID)
	ph.failed[class]++
	if ph.failed[class] > h.allowedFails[class] {
		ph.cooldownUntil = time.Now().Add(h.cooldownTime)
	}
}

// RecordSuccess resets a provider's failure counters and cooldown.
func (h *HealthRegistry) RecordSuccess(providerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ph := h.ensure(providerID)
	ph.failed = map[FailClass]int{}
	ph.cooldownUntil = time.Time{}
	ph.lastState = ProbeValid
	ph.lastCheck = time.Now()
}

// InCooldown reports whether the provider is currently excluded.
func (h *HealthRegistry) InCooldown(providerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	ph, ok := h.providers[providerID]
	if !ok {
		return false
	}
	return time.Now().Before(ph.cooldownUntil)
}

// FilterCandidates returns the input ids minus those in cooldown.
// Safety net (LiteLLM): when EVERY input is in cooldown, returns all
// of them — fail open instead of hard-failing the whole judge chain.
func (h *HealthRegistry) FilterCandidates(ids []string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		ph, ok := h.providers[id]
		if !ok || now.After(ph.cooldownUntil) || ph.cooldownUntil.IsZero() {
			out = append(out, id)
		}
	}
	if len(out) == 0 && len(ids) > 0 {
		return append([]string(nil), ids...)
	}
	return out
}

// Snapshot returns the health state of every tracked provider.
func (h *HealthRegistry) Snapshot() []ProviderHealthState {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ProviderHealthState, 0, len(h.providers))
	for id, ph := range h.providers {
		s := ProviderHealthState{
			ProviderID:  id,
			FailedCalls: map[string]int{},
			LastState:   ph.lastState,
		}
		for c, n := range ph.failed {
			s.FailedCalls[string(c)] = n
		}
		if !ph.cooldownUntil.IsZero() {
			t := ph.cooldownUntil
			s.CooldownUntil = &t
		}
		if !ph.lastCheck.IsZero() {
			t := ph.lastCheck
			s.LastCheckAt = &t
		}
		out = append(out, s)
	}
	return out
}

func (h *HealthRegistry) ensure(providerID string) *providerHealth {
	ph, ok := h.providers[providerID]
	if !ok {
		ph = &providerHealth{failed: map[FailClass]int{}}
		h.providers[providerID] = ph
	}
	return ph
}

// logHealth is the package-level logger for failover warnings.
var logHealth = log.Printf
