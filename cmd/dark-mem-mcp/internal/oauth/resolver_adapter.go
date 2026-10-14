// Package oauth — resolver_adapter.go: concrete implementation of
// the parent module's OAuthTokenResolver interface.
//
// # Why this file exists
//
// The orchestrator (parent module, internal/orchestration) cannot
// import the cmd sub-module (cmd/dark-mem-mcp/internal/oauth) —
// Go's import graph is parent → child only. To give the
// orchestrator a way to read OAuth tokens without breaking the
// layering rule, the parent module declares an interface
// (internal/orchestration/oauth_resolver.go) and this file
// provides the matching concrete type.
//
// Go's structural typing satisfies the interface implicitly: as
// long as RouterAdapter's methods have identical signatures, the
// orchestrator accepts it without an `implements` keyword or an
// import cycle.
//
// The factory in legacy_main.go wraps this adapter via
// orch.WithOAuthResolver(adapter) at boot time. Once wired,
// the orchestrator reaches into the OAuth subsystem only through
// this adapter — never directly into Router.
//
// Phase 23 Tier 1 (Sesión 7, 2026-10-14, spec_id 1992): the
// adapter is the final wiring piece closing the Tier 1 self-auth
// gap deferred from Sesión 6 (commit 8ed967a).
package oauth

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
)

// RouterAdapter implements internal/orchestration.OAuthTokenResolver
// on top of *Router. It is the production implementation; tests
// for the orchestrator's defaultDriftJudgeProvider can supply a
// stub instead.
//
// Construction: pass *Router. Construction is infallible; there
// is no error to return. legacy_main.go wires it unconditionally
// (no nil check needed).
type RouterAdapter struct {
	router *Router
}

// NewRouterAdapter wraps a Router into a RouterAdapter satisfying
// the parent module's OAuthTokenResolver interface.
//
// The router MUST be non-nil (legacy_main.go's boot order
// guarantees this; the orchestrator's nil-resolver path covers
// any pre-OAuth deployments).
func NewRouterAdapter(router *Router) *RouterAdapter {
	return &RouterAdapter{router: router}
}

// LoadActiveToken delegates to router.LoadActiveToken after
// converting the string provider name to the local Provider
// type. Unknown provider names return ErrNoConnection (same
// as a connected provider whose status row is missing — the
// orchestrator treats both as "skip this provider").
//
// On the happy path, the returned access_token is non-expired
// (router.LoadActiveToken auto-refreshes). On error, the
// sealed ErrNoConnection / ErrConnectionExpired from the router
// bubble up unchanged so the orchestrator can use errors.Is.
func (a *RouterAdapter) LoadActiveToken(ctx context.Context, providerName string) (string, string, string, error) {
	if a == nil || a.router == nil {
		// Defensive: never happens in production because
		// legacy_main.go constructs the adapter immediately
		// after the Router, but keeps tests + future
		// refactors safe.
		return "", "", "", orchestration.ErrNoConnection
	}
	p := Provider(providerName)
	switch p {
	case ProviderGoogle, ProviderHuggingFace:
		// ok
	default:
		return "", "", "", orchestration.ErrNoConnection
	}
	return a.router.LoadActiveToken(ctx, p)
}

// ProviderDisplayName returns the canonical display name from
// providerInfo for the given provider key. Unknown keys return
// the bare name so error messages degrade gracefully (better
// than "unknown" — at least the operator sees the key).
func (a *RouterAdapter) ProviderDisplayName(providerName string) string {
	switch Provider(providerName) {
	case ProviderGoogle:
		return displayNameFor(ProviderGoogle)
	case ProviderHuggingFace:
		return displayNameFor(ProviderHuggingFace)
	}
	return providerName
}
