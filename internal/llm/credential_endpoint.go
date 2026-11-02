// credential_endpoint.go — the ONE place where a consumer that holds a
// credential turns it into an endpoint.
//
// internal/llm/endpoint.go owns the DISCOVERY policy (operator override
// → cached resolution → probe of every candidate). This file owns the
// CONSUMER policy, which every consumer must apply identically so they
// all agree on the endpoint:
//
//   - A provider that declares ONE endpoint candidate has nothing to
//     discover: the only endpoint it has IS the endpoint that serves its
//     keys. Probing it would cost one extra HTTP request per client
//     construction for every provider (anthropic, openai, google,
//     deepseek, ...) and could not change the outcome. So the resolver
//     short-circuits to the default endpoint, no probe.
//
//   - A provider that declares MORE than one candidate (MiniMax: global
//
//   - China) mints REGION-SCOPED keys: each key authenticates only
//     against the endpoint it was issued for. There the endpoint must be
//     discovered against the credential in hand (see ResolveEndpoint).
//
// The operator override (DARK_LLM_BASE_URL_<PROVIDER>) always wins and
// is honoured for single-candidate providers too — it is the escape
// hatch that needs no recompile and no network.
package llm

import (
	"context"
	"fmt"
	"strings"
)

// ResolveCredentialEndpoint returns the endpoint of spec that accepts
// the given credential.
//
// It is ResolveEndpoint with the single-candidate short-circuit and the
// operator override applied up front (so a provider that needs no
// discovery never touches the network).
//
// Errors name every candidate that was tried — a wrong key and a wrong
// endpoint must not look the same (see ResolveEndpoint).
func ResolveCredentialEndpoint(ctx context.Context, spec *ProviderSpec, key string) (EndpointSpec, error) {
	if spec == nil {
		return EndpointSpec{}, fmt.Errorf("resolve endpoint: nil provider spec")
	}
	// 1. Operator override: explicit intent wins outright, no probe.
	if ep, ok := operatorOverrideEndpoint(spec); ok {
		return mergeEndpointDefaults(ep, spec.DefaultEndpoint()), nil
	}
	// 2. No credential → nothing to resolve against. The default
	//    endpoint is the only thing we know.
	if strings.TrimSpace(key) == "" {
		return spec.DefaultEndpoint(), nil
	}
	// 3. One candidate → it IS the endpoint for this credential.
	if len(spec.EndpointCandidates()) <= 1 {
		return spec.DefaultEndpoint(), nil
	}
	// 4. Several candidates → discover the one that accepts the key.
	return ResolveEndpoint(ctx, spec, key)
}

// mergeEndpointDefaults fills the EMPTY fields of ep with the ones of
// def.
//
// An operator override may set only ONE of the two URLs (e.g. only the
// Anthropic-compatible base). The other one falls back to the provider
// default instead of becoming an empty string that would break every
// request built on top of it.
func mergeEndpointDefaults(ep, def EndpointSpec) EndpointSpec {
	if ep.BaseURL == "" {
		ep.BaseURL = def.BaseURL
	}
	if ep.AnthropicBaseURL == "" {
		ep.AnthropicBaseURL = def.AnthropicBaseURL
	}
	if ep.ProbePath == "" {
		ep.ProbePath = def.ProbePath
	}
	if ep.ProbeAuthMode == "" {
		ep.ProbeAuthMode = def.ProbeAuthMode
	}
	return ep
}
