// endpoint.go — credential-scoped endpoint resolution (spec 1188 follow-up).
//
// # THE PROBLEM
//
// A provider id does not determine a unique endpoint. MiniMax, for
// instance, mints REGION-SCOPED keys: the global region
// (api.minimax.io) and the China region (api.minimaxi.com) issue
// DIFFERENT keys, and each key authenticates ONLY against its own
// endpoint (the other returns HTTP 401 "invalid api key"). The
// catalog used to hardcode one BaseURL per provider, so the moment
// the operator rotated keys, used a key from another region, or moved
// between the harness-injected key and their own, every call broke
// with an authentication error that looked like "bad key".
//
// # THE MODEL
//
// The endpoint is a property of the CREDENTIAL, not of the provider.
// Therefore:
//
//   - the catalog declares CANDIDATES (ProviderSpec.Endpoints), not a
//     single endpoint;
//   - the effective endpoint is RESOLVED against the key in hand, in
//     one place, so every consumer agrees;
//   - the resolution is CACHED, and persisted with the credential, so
//     the discovery cost is paid once.
//
// RESOLUTION ORDER
//
//  1. OPERATOR OVERRIDE — DARK_LLM_BASE_URL_<PROVIDER> (and the
//     anthropic twin). Explicit, wins over everything, no probe.
//
//  2. CACHED CREDENTIAL — the credential record in the OS keyring
//     carries the endpoint that was resolved for that key. If the key
//     still matches, the endpoint is reused (no network call).
//
//  3. PROBE DISCOVERY — try each candidate in preference order with
//     the cheap auth probe (GET <base><probePath>). The first one
//     that authenticates wins. This is the path that fixes the
//     region-scoped-key problem without any hardcoded guess.
//
//  4. FAIL LOUDLY — if no candidate accepts the key, the error names
//     every candidate and what each one answered. A wrong key and a
//     wrong endpoint must not look the same.
//
// This file is POLICY. internal/llm/catalog.go is DATA.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// endpointProbeTimeout bounds one candidate probe. Probes are free
// (a model listing, not a completion) but must not stall a judge call.
const endpointProbeTimeout = 6 * time.Second

// endpointCacheTTL is how long a discovered endpoint stays trusted
// without re-probing. Long enough to avoid a probe per judge call,
// short enough that a rotated key is noticed within a day.
const endpointCacheTTL = 24 * time.Hour

// resolvedEndpoint is one cached resolution.
type resolvedEndpoint struct {
	endpoint   EndpointSpec
	resolvedAt time.Time
	// keyFingerprint identifies the credential the endpoint was
	// resolved FOR, so a rotated key forces a fresh discovery.
	keyFingerprint string
}

// endpointCache is the process-wide cache of credential-scoped
// resolutions.
var endpointCache = struct {
	sync.RWMutex
	entries map[string]resolvedEndpoint
}{entries: map[string]resolvedEndpoint{}}

// keyFingerprint derives a stable, non-reversible identifier for a
// credential so the cache can tell "same key" from "rotated key"
// without ever storing the key itself.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// cacheKey identifies one resolution slot: provider + credential.
func cacheKey(providerID, key string) string {
	return providerID + "|" + keyFingerprint(key)
}

// SetResolvedEndpointHint records that a credential authenticates
// against a given endpoint, so the next resolution for that
// credential skips discovery. The keyring credential record calls
// this when it loads a stored credential.
func SetResolvedEndpointHint(providerID, endpointID string) {
	if providerID == "" || endpointID == "" {
		return
	}
	spec := SpecByID(providerID)
	if spec == nil {
		return
	}
	ep, ok := spec.EndpointByID(endpointID)
	if !ok {
		return
	}
	endpointCache.Lock()
	defer endpointCache.Unlock()
	endpointCache.entries[cacheKey(providerID, "")] = resolvedEndpoint{
		endpoint:   ep,
		resolvedAt: time.Now(),
	}
}

// operatorOverrideEndpoint returns the endpoint forced by the
// operator through the environment, if any.
//
//	DARK_LLM_BASE_URL_<PROVIDER>            -> BaseURL
//	DARK_LLM_ANTHROPIC_BASE_URL_<PROVIDER>  -> AnthropicBaseURL
//
// <PROVIDER> is the canonical id uppercased with dashes replaced by
// underscores (e.g. DARK_LLM_BASE_URL_MINIMAX).
func operatorOverrideEndpoint(spec *ProviderSpec) (EndpointSpec, bool) {
	if spec == nil {
		return EndpointSpec{}, false
	}
	suffix := strings.ToUpper(strings.ReplaceAll(spec.ID, "-", "_"))
	base := strings.TrimSpace(os.Getenv("DARK_LLM_BASE_URL_" + suffix))
	anthropic := strings.TrimSpace(os.Getenv("DARK_LLM_ANTHROPIC_BASE_URL_" + suffix))
	if base == "" && anthropic == "" {
		return EndpointSpec{}, false
	}
	return EndpointSpec{
		ID:               "env",
		BaseURL:          base,
		AnthropicBaseURL: anthropic,
		ProbePath:        spec.ProbePath,
		ProbeAuthMode:    spec.ProbeAuthMode,
	}, true
}

// cachedEndpointFor returns the cached resolution for this exact
// credential, if it is still fresh.
func cachedEndpointFor(spec *ProviderSpec, key string) (EndpointSpec, bool) {
	endpointCache.RLock()
	entry, ok := endpointCache.entries[cacheKey(spec.ID, key)]
	endpointCache.RUnlock()
	if !ok {
		return EndpointSpec{}, false
	}
	if time.Since(entry.resolvedAt) > endpointCacheTTL {
		return EndpointSpec{}, false
	}
	return entry.endpoint, true
}

// storeResolvedEndpoint caches a resolution for this credential.
func storeResolvedEndpoint(spec *ProviderSpec, key string, ep EndpointSpec) {
	endpointCache.Lock()
	defer endpointCache.Unlock()
	endpointCache.entries[cacheKey(spec.ID, key)] = resolvedEndpoint{
		endpoint:       ep,
		resolvedAt:     time.Now(),
		keyFingerprint: keyFingerprint(key),
	}
}

// probeEndpoint checks whether a credential authenticates against one
// endpoint, using the provider's cheap probe path (a model listing,
// never a completion). Returns nil when the key is accepted.
//
// A 401/403 means "this endpoint does not serve this key" — the
// definitive answer for discovery. Any other failure (network, 5xx,
// 429) is reported but treated as "cannot tell", so the caller keeps
// probing the remaining candidates.
func probeEndpoint(ctx context.Context, ep EndpointSpec, key string) error {
	if strings.TrimSpace(ep.BaseURL) == "" {
		return fmt.Errorf("endpoint %q has no base URL", ep.ID)
	}
	probePath := ep.ProbePath
	if probePath == "" {
		probePath = "/models"
	}
	url := strings.TrimRight(ep.BaseURL, "/") + probePath
	probeCtx, cancel := context.WithTimeout(ctx, endpointProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	switch ep.ProbeAuthMode {
	case ProbeAuthXAPIKey:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("HTTP %d: the key is not valid for this endpoint", resp.StatusCode)
	case http.StatusNotFound:
		// Probe path unsupported here: not evidence against the key.
		return fmt.Errorf("HTTP 404: probe path unsupported by this endpoint")
	case http.StatusTooManyRequests:
		return fmt.Errorf("HTTP 429: rate limited")
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// ResolveEndpoint returns the endpoint of spec that accepts the given
// credential.
//
// Resolution order: operator override -> cached resolution for this
// credential -> probe discovery over the provider's candidates.
//
// The returned endpoint is the one that must be used for every call
// made with that key.
func ResolveEndpoint(ctx context.Context, spec *ProviderSpec, key string) (EndpointSpec, error) {
	if spec == nil {
		return EndpointSpec{}, fmt.Errorf("resolve endpoint: nil provider spec")
	}
	if strings.TrimSpace(key) == "" {
		return EndpointSpec{}, fmt.Errorf("resolve endpoint: empty key")
	}
	if ep, ok := operatorOverrideEndpoint(spec); ok {
		return ep, nil
	}
	if ep, ok := cachedEndpointFor(spec, key); ok {
		return ep, nil
	}

	candidates := spec.EndpointCandidates()
	if len(candidates) == 0 {
		return EndpointSpec{}, fmt.Errorf("resolve endpoint: provider %q declares no endpoint", spec.ID)
	}

	var failures []string
	for _, ep := range candidates {
		err := probeEndpoint(ctx, ep, key)
		if err == nil {
			storeResolvedEndpoint(spec, key, ep)
			return ep, nil
		}
		failures = append(failures, fmt.Sprintf("%s (%s): %v", ep.ID, ep.BaseURL, err))
	}
	return EndpointSpec{}, fmt.Errorf(
		"resolve endpoint: no endpoint of provider %q accepts this credential; tried: %s",
		spec.ID, strings.Join(failures, "; "))
}

// ResolveEndpointCached behaves like ResolveEndpoint but consults the
// process-wide cache first (the cache is also written by
// ResolveEndpoint, so this is an alias kept for call-site clarity).
func ResolveEndpointCached(ctx context.Context, spec *ProviderSpec, key string) (EndpointSpec, error) {
	return ResolveEndpoint(ctx, spec, key)
}

// EndpointForCredential resolves the endpoint for a credential and
// returns the base URL to use for the provider's PRIMARY dialect.
func EndpointForCredential(ctx context.Context, spec *ProviderSpec, key string) (string, error) {
	ep, err := ResolveEndpoint(ctx, spec, key)
	if err != nil {
		return "", err
	}
	return ep.BaseURL, nil
}

// AnthropicEndpointForCredential resolves the endpoint for a
// credential and returns its Anthropic-compatible base URL. Empty
// string when the endpoint does not offer one.
func AnthropicEndpointForCredential(ctx context.Context, spec *ProviderSpec, key string) (string, error) {
	ep, err := ResolveEndpoint(ctx, spec, key)
	if err != nil {
		return "", err
	}
	return ep.AnthropicBaseURL, nil
}
