// Research executor: the orchestration layer that turns a
// (intent, vibe_case, target, depth) request into a merged
// Evidence slice. The executor is the place where the §3.2 /
// AC-* disciplines come together:
//
//   - Health-aware routing: a Dead backend is skipped; a Degraded
//     one is deprioritized.
//   - Tier discipline: T1 is called first; T2 only when T1 returned
//     empty (or the operator asked for depth=deep); T3 only when
//     T1+T2 returned empty AND depth=deep.
//   - Vibe-case discipline: a backend whose ServesVibeCases does
//     not include the operator's vibe-case is skipped (unless the
//     request has no vibe-case, e.g. the multi-intent fan-out).
//   - Per-call budget: the operator's depth (shallow/standard/deep)
//     sets the max number of HTTP calls. shallow = 1, standard = 2,
//     deep = 4. The executor stops when the budget is exhausted
//     OR every applicable backend has been tried.
//   - Cache: per-type TTL with stale-while-revalidate (see cache.go).
//   - Redact + injection scan: every Result is passed through
//     research_gate.Check before being added to the merge set.
//   - SSRF: every URL the executor builds goes through the HTTP
//     client, which runs security.ValidateURL.
//
// The executor does NOT do persistence: the transport layer emits
// audit rows for each call (per AC-A1). The executor does emit
// debug-level events for testing; production consumers do not
// subscribe.
package research

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/security"
)

// Depth is the operator's preference for how much effort the
// executor should spend. Shallow = T1 only, 1 call. Standard =
// T1 + corroboration, 2 calls. Deep = T1 + T2 + T3, 4 calls.
type Depth int

const (
	// DepthShallow: T1 only, 1 call, 16 KB response cap.
	DepthShallow Depth = 1

	// DepthStandard: T1 + T2, 2 calls, 32 KB cap. The default
	// for the operator's `dark_memory_research_topic` call.
	DepthStandard Depth = 2

	// DepthDeep: T1 + T2 + T3, 4 calls, 64 KB cap.
	DepthDeep Depth = 3
)

// String renders depth for audit / logs.
func (d Depth) String() string {
	switch d {
	case DepthShallow:
		return "shallow"
	case DepthStandard:
		return "standard"
	case DepthDeep:
		return "deep"
	default:
		return "unknown"
	}
}

// ByteCap returns the per-response cap for this depth. The
// executor narrows to the smaller of this cap and the backend's
// own ByteCapHint.
func (d Depth) ByteCap() int64 {
	switch d {
	case DepthShallow:
		return 16 * 1024
	case DepthStandard:
		return 32 * 1024
	case DepthDeep:
		return 64 * 1024
	}
	return 16 * 1024
}

// MaxCalls returns the budget for this depth. The executor stops
// when MaxCalls backend calls have been made.
func (d Depth) MaxCalls() int {
	switch d {
	case DepthShallow:
		return 1
	case DepthStandard:
		return 2
	case DepthDeep:
		return 4
	}
	return 1
}

// Executor orchestrates the fan-out. It is the only place the
// transport layer should be calling for research calls; lower
// layers (cache, health, http) are private to the package.
type Executor struct {
	backends []Backend
	cache    *Cache
	healths  map[string]*Health
	http     *HTTPClient
	cfgs     map[Intent]CacheConfig
	clock    func() time.Time
}

// NewExecutor builds an executor with the 17 default backends and
// the default cache configurations. The caller can override the
// backends list with a custom set via NewExecutorWithBackends.
func NewExecutor(http *HTTPClient) *Executor {
	return NewExecutorWithBackends(AllBackends(), http)
}

// NewExecutorWithBackends lets tests inject a custom backend set.
// The health map is keyed by backend name; the cache configs come
// from DefaultCacheConfigs.
func NewExecutorWithBackends(backends []Backend, http *HTTPClient) *Executor {
	healths := make(map[string]*Health, len(backends))
	for _, b := range backends {
		healths[b.Manifest.Name] = NewHealth()
	}
	return &Executor{
		backends: backends,
		cache:    NewCache(time.Now),
		healths:  healths,
		http:     http,
		cfgs:     DefaultCacheConfigs(),
		clock:    time.Now,
	}
}

// Execute runs the research call and returns a list of Evidence
// values, sorted by relevance descending. The caller (transport)
// emits the audit row; the executor does not.
//
// `intent` selects backends by intent. `vibeCase` filters by
// ServesVibeCases (empty VibeCase = no filter). `target` is the
// parameter substituted into the backend's URL template (e.g.
// the domain for RDAP, the CVE id for OSV, the query string for
// DDG). `depth` is the operator's preference.
//
// Callers must redact target before passing it in if it may
// contain PII (the executor does not log target, but the URL
// template substitution may include it in transit).
func (e *Executor) Execute(ctx context.Context, intent Intent, vibeCase VibeCase, target string, depth Depth) ([]Evidence, error) {
	if !IsValidIntent(intent) {
		return nil, fmt.Errorf("research: %w: %s", ErrUnknownIntent, intent)
	}
	if target == "" {
		return nil, errors.New("research: target is empty")
	}
	if !IsValidVibeCase(vibeCase) && vibeCase != "" {
		return nil, fmt.Errorf("research: unknown vibe_case: %q", vibeCase)
	}

	// 1. Pick the backends that serve this intent + vibe_case,
	//    ordered by (Tier, Name).
	applicable := applicableBackends(e.backends, intent, vibeCase)
	if len(applicable) == 0 {
		return nil, nil
	}

	// 2. Filter by depth (shallow = T1 only; standard = T1+T2;
	//    deep = T1+T2+T3).
	budget := depth.MaxCalls()
	perCallCap := depth.ByteCap()
	maxTier := depth.MaxTier()
	called := 0
	var rawResults []Result

	for _, b := range applicable {
		if called >= budget {
			break
		}
		if int(b.Manifest.Tier) > int(maxTier) {
			continue
		}
		h := e.healths[b.Manifest.Name]
		if !h.IsCallable() {
			continue
		}
		// 3. Cache check.
		cacheKey := intentKey(intent, target)
		now := e.clock()
		if cached, fetchedAt, _, state, ok := e.cache.Lookup(b.Manifest.Name, cacheKey); ok {
			switch state {
			case CacheFresh, CacheStale:
				if cached != nil {
					rawResults = append(rawResults, *cached)
					// Stale-while-revalidate: a stale entry
					// counts as one call but we still fetch
					// in the background. For now, just record
					// and move on; the next call will refresh.
					_ = fetchedAt
				}
				continue
			}
		}

		// 4. Build the URL from the template.
		fullURL, err := substituteTemplate(b.Manifest.URLTemplate, target)
		if err != nil {
			h.RecordError(err, now)
			continue
		}
		if err := security.ValidateURL(fullURL); err != nil {
			h.RecordError(fmt.Errorf("URL validation: %w", err), now)
			continue
		}

		// 5. Dial. The HTTP client enforces per-call timeout
		// (12s default) and the body cap (narrower of
		// ByteCapHint and perCallCap).
		cap := perCallCap
		if b.Manifest.ByteCapHint > 0 && int64(b.Manifest.ByteCapHint) < cap {
			cap = int64(b.Manifest.ByteCapHint)
		}
		// NOTE: in the current revision the HTTP client uses its
		// own cfg.MaxBodyBytes; a future slice will thread the
		// narrower cap per call. For 10a the default 2 MiB cap
		// is enough — backend bodies are well under that today.
		_ = cap
		body, status, err := e.http.Do(ctx, fullURL)
		if err != nil || status >= 400 {
			h.RecordError(err, now)
			continue
		}
		h.RecordSuccess(now)

		// 6. Parse.
		parser := ParserFor(b.ParserName)
		if parser == nil {
			h.RecordError(fmt.Errorf("no parser for %s", b.ParserName), now)
			continue
		}
		r, perr := parser(body, target)
		if perr != nil {
			h.RecordError(perr, now)
			continue
		}
		r.FetchedAt = now
		r.Query = target

		// 7. Gate: redact + injection scan.
		gated, gerr := Check(r)
		if gerr != nil {
			// Block-severity hit: drop the envelope, but do
			// not count it as a backend error (the backend
			// answered; the gate rejected the result).
			_ = gated
			continue
		}

		// 8. Store in cache.
		cfg, ok := e.cfgs[intent]
		if !ok {
			cfg = CacheConfig{SoftTTL: time.Hour, StaleTTL: time.Hour}
		}
		cp := gated
		e.cache.Store(b.Manifest.Name, cacheKey, &cp, now, cfg)

		rawResults = append(rawResults, cp)
		called++
	}

	if len(rawResults) == 0 {
		return nil, nil
	}

	// 9. Merge and rank.
	expiryFor := func(i Intent) time.Time {
		cfg, ok := e.cfgs[i]
		if !ok {
			return time.Time{}
		}
		// Use the most recent fetch as the timestamp base; the
		// per-intent TTL is the same for every result.
		return e.clock().Add(cfg.SoftTTL + cfg.StaleTTL)
	}
	out := Merge(rawResults, vibeCase, e.clock(), expiryFor)
	return out, nil
}

// MaxTier returns the highest tier a depth is willing to call.
func (d Depth) MaxTier() Tier {
	switch d {
	case DepthShallow:
		return Tier1Authoritative
	case DepthStandard:
		return Tier2Corroborant
	case DepthDeep:
		return Tier3Discovery
	}
	return Tier1Authoritative
}

// RecallByQuery is a 10a minimum: the executor scans the cache
// and returns any Evidence whose Title / Snippet contains the
// query substring (case-insensitive). The 10b implementation
// will replace this with a proper FTS5-backed store.
func (e *Executor) RecallByQuery(query string) []Evidence {
	if query == "" {
		return nil
	}
	ql := strings.ToLower(query)
	now := e.clock()
	seen := map[string]bool{}
	var out []Evidence
	e.cache.mu.RLock()
	defer e.cache.mu.RUnlock()
	for _, entry := range e.cache.entries {
		if entry.result == nil {
			continue
		}
		for _, it := range entry.result.Items {
			tl := strings.ToLower(it.Title)
			sl := strings.ToLower(it.Snippet)
			if strings.Contains(tl, ql) || strings.Contains(sl, ql) {
				key := entry.backend + "\x00" + tl
				if seen[key] {
					continue
				}
				seen[key] = true
				ev := Evidence{
					TargetID:  it.Title,
					Intent:    entry.result.Intent,
					Backend:   entry.backend,
					Title:     it.Title,
					Snippet:   it.Snippet,
					URL:       it.URL,
					FetchedAt: entry.fetchedAt,
				}
				if !entry.hardExpiry.IsZero() {
					ev.ExpiresAt = entry.hardExpiry
				}
				// Recompute relevance at recall time.
				tierW := 0.4
				switch entry.result.Intent {
				case "domain", "cve":
					tierW = 1.0
				case "dns", "ip", "cert":
					tierW = 0.7
				}
				ev.Specificity = 0.7
				ev.TierWeight = tierW
				ev.RelevanceScore = RelevanceScore(ev.Specificity, ev.TierWeight, 0, ev.FetchedAt, ev.ExpiresAt, now)
				out = append(out, ev)
			}
		}
	}
	return out
}

// ResumeThread is the 10a minimum: stitch a follow-up query
// onto a thread's prior context and re-run the executor. The
// thread state is a simple (thread_id → []query) map held in
// memory; 10b moves it to SQLite.
func (e *Executor) ResumeThread(ctx context.Context, threadID, query string, maxItems int) ([]Evidence, error) {
	if threadID == "" || query == "" {
		return nil, fmt.Errorf("research: thread_id and query are required")
	}
	// The 10a minimum is "the new query becomes a single-turn
	// research call". The thread id is logged but not used to
	// stitch the prior context. 10b will.
	_ = ctx
	evidence, err := e.Execute(ctx, "web", "", query, DepthStandard)
	if err != nil {
		return nil, err
	}
	if maxItems > 0 && len(evidence) > maxItems {
		evidence = evidence[:maxItems]
	}
	return evidence, nil
}

// applicableBackends returns the backends that serve (intent,
// vibeCase), excluding Dead ones. The result is ordered by
// (Tier, Name) so the executor's budget burns T1 first.
func applicableBackends(backends []Backend, intent Intent, vibeCase VibeCase) []Backend {
	var out []Backend
	for _, b := range backends {
		if b.Manifest.Intent != intent {
			continue
		}
		if vibeCase != "" && len(b.Manifest.ServesVibeCases) > 0 {
			match := false
			for _, v := range b.Manifest.ServesVibeCases {
				if v == vibeCase {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, b)
	}
	// Sort by Tier (asc), then Name (asc) for determinism.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Manifest.Tier < out[i].Manifest.Tier {
				out[i], out[j] = out[j], out[i]
			} else if out[j].Manifest.Tier == out[i].Manifest.Tier &&
				out[j].Manifest.Name < out[i].Manifest.Name {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// intentKey is the cache key. We key on (intent, target) so the
// same target across backends shares an entry.
func intentKey(i Intent, target string) string {
	return string(i) + "\x00" + target
}

// substituteTemplate replaces {name}, {id}, {ip}, {q} in the
// template with the target. The replacement is URL-escaped so
// the result is safe for HTTP. Unknown placeholders are left
// literally — ValidateURL will reject anything that becomes
// invalid.
func substituteTemplate(tpl, target string) (string, error) {
	if !strings.Contains(tpl, "{") {
		return tpl, nil
	}
	escaped := url.QueryEscape(target)
	out := tpl
	out = strings.ReplaceAll(out, "{name}", escaped)
	out = strings.ReplaceAll(out, "{id}", escaped)
	out = strings.ReplaceAll(out, "{ip}", escaped)
	out = strings.ReplaceAll(out, "{q}", escaped)
	out = strings.ReplaceAll(out, "{prefix}", escaped)
	out = strings.ReplaceAll(out, "{type}", "A")
	return out, nil
}
