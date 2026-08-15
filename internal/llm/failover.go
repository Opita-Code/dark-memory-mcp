// failover.go — FailoverClient (spec 1188 T5).
//
// The pre-v2.20.0 selection was first-match-wins: NewSelfHarnessClient
// returned the FIRST catalog provider whose env var was non-empty, and
// if that provider's judge call failed, the verdict degraded to
// needs_human — infra masquerading as drift. FailoverClient replaces
// that with an explicit chain:
//
//  1. candidates = catalog order, filtered to providers with a key
//     (keystore: OS vault first, env second);
//  2. DARK_JUDGE_PROVIDER pin moves its provider to the front (it is
//     a preference, not a hard requirement — spec D6);
//  3. providers in cooldown are skipped (health registry);
//  4. each candidate gets ONE Judge attempt (the per-provider client
//     already applies its own retry/backoff on transient errors);
//  5. on failure: classify → RecordFailure → log WARN → next
//     candidate; on success: RecordSuccess → return.
//  6. all failed → aggregated ErrNoLLMAvailable.
//
// Safety net (LiteLLM): when every candidate is in cooldown,
// FilterCandidates fails open and the chain still attempts them.
package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
)

// FailoverOptions configures a FailoverClient.
type FailoverOptions struct {
	// Specs is the candidate list in priority order (usually
	// llm.Catalog, order preserved).
	Specs []*ProviderSpec
	// KS provides keys (composite keyring+env by default).
	KS llmkeystore.KeyStore
	// Health tracks cooldowns. Optional — nil disables cooldown
	// filtering and failure recording.
	Health *HealthRegistry
	// Pin is the DARK_JUDGE_PROVIDER preference ("" = none). The
	// pinned provider is moved to the front when it has a key.
	Pin string
	// Factory builds the concrete JudgeClient for one provider+key.
	// The orchestration package wires newCatalogClient here.
	Factory func(spec *ProviderSpec, key string) (JudgeClient, error)
	// Classify maps a client error to a FailClass. Nil = default
	// classifier (timeouts/deadlines → FailTimeout, else FailServer).
	Classify func(err error) FailClass
	// Logf receives failover transition warnings (nil = log.Printf).
	Logf func(format string, args ...any)
}

// FailoverClient implements JudgeClient over a provider chain.
type FailoverClient struct {
	specs    []*ProviderSpec
	ks       llmkeystore.KeyStore
	health   *HealthRegistry
	pin      string
	factory  func(spec *ProviderSpec, key string) (JudgeClient, error)
	classifier func(err error) FailClass
	logf     func(format string, args ...any)

	mu             sync.RWMutex
	lastProviderID string
}

// NewFailoverClient builds the chain. Returns an error when no factory
// is wired (nothing to call) — callers surface ErrNoLLMAvailable at
// Judge time instead when specs/keys are empty.
func NewFailoverClient(opts FailoverOptions) (*FailoverClient, error) {
	if opts.Factory == nil {
		return nil, errors.New("llm failover: Factory is required")
	}
	if opts.KS == nil {
		opts.KS = llmkeystore.EnvStore(nil)
	}
	if opts.Logf == nil {
		opts.Logf = logHealth
	}
	return &FailoverClient{
		specs:    opts.Specs,
		ks:       opts.KS,
		health:   opts.Health,
		pin:      opts.Pin,
		factory:  opts.Factory,
		classifier: opts.Classify,
		logf:     opts.Logf,
	}, nil
}

// Name implements JudgeClient.
func (f *FailoverClient) Name() string { return "failover" }

// LastProviderID returns the id of the provider that answered the most
// recent successful Judge call ("" before any success).
func (f *FailoverClient) LastProviderID() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.lastProviderID
}

// defaultClassifyError maps common error shapes to failure classes.
func defaultClassifyError(err error) FailClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return FailTimeout
	}
	return FailServer
}

// classify runs the configured classifier (or the default).
func (f *FailoverClient) classify(err error) FailClass {
	if f.classifier != nil {
		if c := f.classifier(err); c != "" {
			return c
		}
	}
	return defaultClassifyError(err)
}

// orderedCandidates returns the provider specs with keys, in failover
// order (catalog order, pin first), de-duplicated.
func (f *FailoverClient) orderedCandidates() []*ProviderSpec {
	seen := map[string]bool{}
	var out []*ProviderSpec

	pinned := ""
	if f.pin != "" {
		pinned, _ = ResolveID(f.pin)
	}
	appendSpec := func(spec *ProviderSpec) {
		if spec == nil || seen[spec.ID] {
			return
		}
		if _, err := f.ks.Get(spec.ID); err != nil {
			return // no key → not a candidate
		}
		seen[spec.ID] = true
		out = append(out, spec)
	}

	if pinned != "" {
		appendSpec(SpecByID(pinned))
	}
	for _, s := range f.specs {
		appendSpec(s)
	}
	return out
}

// Judge implements JudgeClient with the failover chain.
func (f *FailoverClient) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	candidates := f.orderedCandidates()
	if len(candidates) == 0 {
		return nil, ErrNoLLMAvailable
	}

	// Cooldown filter with fail-open safety net.
	ids := make([]string, 0, len(candidates))
	byID := make(map[string]*ProviderSpec, len(candidates))
	for _, s := range candidates {
		ids = append(ids, s.ID)
		byID[s.ID] = s
	}
	if f.health != nil {
		kept := f.health.FilterCandidates(ids)
		filtered := make([]*ProviderSpec, 0, len(kept))
		for _, id := range kept {
			if s, ok := byID[id]; ok {
				filtered = append(filtered, s)
			}
		}
		candidates = filtered
		if len(candidates) == 0 {
			return nil, ErrNoLLMAvailable
		}
	}

	var lastErr error
	for _, spec := range candidates {
		key, err := f.ks.Get(spec.ID)
		if err != nil {
			continue
		}
		client, err := f.factory(spec, key)
		if err != nil {
			lastErr = fmt.Errorf("provider %s: build client: %w", spec.ID, err)
			if f.health != nil {
				f.health.RecordFailure(spec.ID, FailServer)
			}
			continue
		}
		resp, err := client.Judge(ctx, req)
		if err == nil {
			if f.health != nil {
				f.health.RecordSuccess(spec.ID)
			}
			f.mu.Lock()
			f.lastProviderID = spec.ID
			f.mu.Unlock()
			return resp, nil
		}
		lastErr = err
		class := f.classify(err)
		if f.health != nil {
			f.health.RecordFailure(spec.ID, class)
		}
		f.logf("dark-mem-mcp: failover provider=%s reason=%s err=%v", spec.ID, class, err)
	}
	return nil, fmt.Errorf("%w: all %d candidate providers failed (last: %v)", ErrNoLLMAvailable, len(candidates), lastErr)
}

// CandidateStatus is the exported per-provider routing view used by
// llm_provider_status.
type CandidateStatus struct {
	ProviderID string `json:"provider_id"`
	HasKey     bool   `json:"has_key"`
	KeySource  string `json:"key_source"`
	InCooldown bool   `json:"in_cooldown"`
	Pinned     bool   `json:"pinned"`
}

// Status returns the routing view over the whole catalog (not only
// candidates with keys).
func (f *FailoverClient) Status() []CandidateStatus {
	out := make([]CandidateStatus, 0, len(f.specs))
	pinned, _ := ResolveID(f.pin)
	for _, s := range f.specs {
		cs := CandidateStatus{
			ProviderID: s.ID,
			HasKey:     f.ks.Has(s.ID),
			KeySource:  f.ks.Source(s.ID),
			Pinned:     f.pin != "" && s.ID == pinned,
		}
		if f.health != nil {
			cs.InCooldown = f.health.InCooldown(s.ID)
		}
		out = append(out, cs)
	}
	return out
}
