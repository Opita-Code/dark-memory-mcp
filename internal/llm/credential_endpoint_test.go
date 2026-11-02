package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveCredentialEndpoint_SingleCandidateSkipsProbe is the
// "do not change the behaviour of single-endpoint providers" rule: a
// provider with ONE candidate has nothing to discover, so the resolver
// returns it WITHOUT touching the network. anthropic, openai, google,
// deepseek, ... must not pay for an extra probe request.
func TestResolveCredentialEndpoint_SingleCandidateSkipsProbe(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	spec := &ProviderSpec{
		ID:            "single",
		BaseURL:       srv.URL + "/v1",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
	}
	ep, err := ResolveCredentialEndpoint(context.Background(), spec, "any-key")
	if err != nil {
		t.Fatalf("ResolveCredentialEndpoint: %v", err)
	}
	if ep.BaseURL != srv.URL+"/v1" {
		t.Fatalf("base URL = %q, want %q", ep.BaseURL, srv.URL+"/v1")
	}
	if hits != 0 {
		t.Fatalf("probe requests = %d, want 0 (single candidate must not probe)", hits)
	}
}

// TestResolveCredentialEndpoint_OperatorOverrideWins: the operator
// override wins outright — no probe, and it is honoured even for a
// single-candidate provider.
func TestResolveCredentialEndpoint_OperatorOverrideWins(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DARK_LLM_BASE_URL_SINGLE", "https://operator.example/v1")

	spec := &ProviderSpec{
		ID:            "single",
		BaseURL:       srv.URL + "/v1",
		ProbePath:     "/models",
		ProbeAuthMode: ProbeAuthBearer,
	}
	ep, err := ResolveCredentialEndpoint(context.Background(), spec, "any-key")
	if err != nil {
		t.Fatalf("ResolveCredentialEndpoint: %v", err)
	}
	if ep.BaseURL != "https://operator.example/v1" {
		t.Fatalf("base URL = %q, want the operator override", ep.BaseURL)
	}
	if hits != 0 {
		t.Fatalf("probe requests = %d, want 0 (override must not probe)", hits)
	}
}

// TestResolveCredentialEndpoint_DiscoversAmongCandidates: a provider
// with SEVERAL candidates resolves against the credential in hand.
//
// NOTE: the provider id + credential pair is unique to this test: the
// resolver caches per (provider, credential) for the whole process, so
// reusing a pair from another test would hit that cache and probe a
// server that is already closed.
func TestResolveCredentialEndpoint_DiscoversAmongCandidates(t *testing.T) {
	good := newProbeServer(t, "cred-key-global", nil)
	bad := newProbeServer(t, "cred-key-other", nil)

	spec := &ProviderSpec{
		ID:      "multi-cred",
		BaseURL: bad.URL + "/v1",
		Endpoints: []EndpointSpec{
			{ID: "cn", BaseURL: bad.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
			{ID: "global", BaseURL: good.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
		},
	}
	ep, err := ResolveCredentialEndpoint(context.Background(), spec, "cred-key-global")
	if err != nil {
		t.Fatalf("ResolveCredentialEndpoint: %v", err)
	}
	if ep.ID != "global" {
		t.Fatalf("resolved endpoint = %q, want global", ep.ID)
	}
}

// TestResolveCredentialEndpoint_NoCandidateAcceptsKey: the error names
// every candidate that was tried — a wrong key and a wrong endpoint
// must not look the same.
func TestResolveCredentialEndpoint_NoCandidateAcceptsKey(t *testing.T) {
	bad := newProbeServer(t, "cred-key-accepted", nil)
	spec := &ProviderSpec{
		ID:      "multi-cred-nobody",
		BaseURL: bad.URL + "/v1",
		Endpoints: []EndpointSpec{
			{ID: "cn", BaseURL: bad.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
			{ID: "global", BaseURL: bad.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
		},
	}
	_, err := ResolveCredentialEndpoint(context.Background(), spec, "cred-key-nobody")
	if err == nil {
		t.Fatal("ResolveCredentialEndpoint: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "cn") || !strings.Contains(err.Error(), "global") {
		t.Fatalf("error does not name every candidate: %v", err)
	}
}

// TestResolveCredentialEndpoint_EmptyKey: without a credential there is
// nothing to resolve against — the default endpoint is returned.
func TestResolveCredentialEndpoint_EmptyKey(t *testing.T) {
	spec := &ProviderSpec{
		ID:      "single",
		BaseURL: "https://api.example/v1",
	}
	ep, err := ResolveCredentialEndpoint(context.Background(), spec, "")
	if err != nil {
		t.Fatalf("ResolveCredentialEndpoint: %v", err)
	}
	if ep.BaseURL != "https://api.example/v1" {
		t.Fatalf("base URL = %q, want the default endpoint", ep.BaseURL)
	}
}
