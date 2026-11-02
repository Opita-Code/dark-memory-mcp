// Package v4judge — llm_judge_endpoint_test.go
//
// Tests for the credential-scoped endpoint resolution of the LLM judge.
//
// The endpoint is a property of the CREDENTIAL: MiniMax mints
// region-scoped keys and each key authenticates only against the
// endpoint it was issued for. So the judge must talk to the catalog
// candidate that accepts the key in hand, keeping the trailing path
// (/chat/completions).
//
// The tests use a CUSTOM provider spec (not the catalog) so they stay
// hermetic: no network, no probe against a real provider.
package v4judge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
)

// newKeyedProbeServer returns a server that answers 200 to
// <base>/models when the bearer token is the accepted one, and 401
// otherwise (the shape of a real provider's model listing).
func newKeyedProbeServer(t *testing.T, accept string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestResolveJudgeEndpoint_PicksCandidateAcceptingCredential: the FIRST
// candidate rejects the key, the SECOND accepts it — the judge must be
// pointed at the second one (keeping the /chat/completions path).
func TestResolveJudgeEndpoint_PicksCandidateAcceptingCredential(t *testing.T) {
	good := newKeyedProbeServer(t, "judge-key-global")
	bad := newKeyedProbeServer(t, "judge-key-other")

	spec := &llm.ProviderSpec{
		ID:      "multi-judge",
		BaseURL: bad.URL + "/v1",
		Endpoints: []llm.EndpointSpec{
			{ID: "cn", BaseURL: bad.URL + "/v1", ProbePath: "/models", ProbeAuthMode: llm.ProbeAuthBearer},
			{ID: "global", BaseURL: good.URL + "/v1", ProbePath: "/models", ProbeAuthMode: llm.ProbeAuthBearer},
		},
	}
	cfg := ProviderConfig{
		ProviderID: "judge-multi",
		Endpoint:   bad.URL + "/v1/chat/completions",
		AuthToken:  "judge-key-global",
	}
	got, err := resolveJudgeEndpointForSpec(context.Background(), cfg, spec)
	if err != nil {
		t.Fatalf("resolveJudgeEndpointForSpec: %v", err)
	}
	want := good.URL + "/v1/chat/completions"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

// TestResolveJudgeEndpoint_KeepsConfiguredEndpointOutsideCatalog: an
// endpoint that is NOT a catalog candidate (self-hosted vLLM, corporate
// proxy, ...) is the operator's explicit choice and is left untouched.
func TestResolveJudgeEndpoint_KeepsConfiguredEndpointOutsideCatalog(t *testing.T) {
	good := newKeyedProbeServer(t, "judge-key-global")
	spec := &llm.ProviderSpec{
		ID: "multi-judge-2",
		Endpoints: []llm.EndpointSpec{
			{ID: "cn", BaseURL: "https://cn.example/v1", ProbePath: "/models", ProbeAuthMode: llm.ProbeAuthBearer},
			{ID: "global", BaseURL: good.URL + "/v1", ProbePath: "/models", ProbeAuthMode: llm.ProbeAuthBearer},
		},
	}
	cfg := ProviderConfig{
		ProviderID: "judge-multi",
		Endpoint:   "http://127.0.0.1:8000/v1/chat/completions",
		AuthToken:  "judge-key-global",
	}
	got, err := resolveJudgeEndpointForSpec(context.Background(), cfg, spec)
	if err != nil {
		t.Fatalf("resolveJudgeEndpointForSpec: %v", err)
	}
	if got != cfg.Endpoint {
		t.Fatalf("endpoint = %q, want the configured one (%q)", got, cfg.Endpoint)
	}
}

// TestResolveJudgeEndpoint_SingleCandidateKeepsEndpoint: a provider with
// ONE endpoint candidate has nothing to discover — the configured
// endpoint is kept and NO probe is made.
func TestResolveJudgeEndpoint_SingleCandidateKeepsEndpoint(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	spec := &llm.ProviderSpec{
		ID:        "single-judge",
		BaseURL:   srv.URL + "/v1",
		ProbePath: "/models",
	}
	cfg := ProviderConfig{
		ProviderID: "judge-single",
		Endpoint:   srv.URL + "/v1/chat/completions",
		AuthToken:  "any-key",
	}
	got, err := resolveJudgeEndpointForSpec(context.Background(), cfg, spec)
	if err != nil {
		t.Fatalf("resolveJudgeEndpointForSpec: %v", err)
	}
	if got != cfg.Endpoint {
		t.Fatalf("endpoint = %q, want the configured one (%q)", got, cfg.Endpoint)
	}
	if hits != 0 {
		t.Fatalf("probe requests = %d, want 0 (single candidate must not probe)", hits)
	}
}
