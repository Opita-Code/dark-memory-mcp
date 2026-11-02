package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newProbeServer returns a server that answers 200 to <base>/models
// when the bearer token is one of the accepted keys, and 401
// otherwise.
func newProbeServer(t *testing.T, want string, hits *int) *httptest.Server {
	t.Helper()
	accepted := map[string]bool{want: true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		if !strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !accepted[token] {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"MiniMax-M3"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestResolveEndpoint_DiscoversCandidateAcceptingKey is the core of the
// credential-scoped endpoint model: the FIRST candidate rejects the
// key, the SECOND accepts it, and the resolver must return the second
// one instead of failing.
func TestResolveEndpoint_DiscoversCandidateAcceptingKey(t *testing.T) {
	good := newProbeServer(t, "key-global", nil)
	bad := newProbeServer(t, "something-else", nil)

	spec := &ProviderSpec{
		ID:      "minimax",
		BaseURL: bad.URL + "/v1",
		Endpoints: []EndpointSpec{
			{ID: "cn", BaseURL: bad.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
			{ID: "global", BaseURL: good.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
		},
	}

	ep, err := ResolveEndpoint(context.Background(), spec, "key-global")
	if err != nil {
		t.Fatalf("ResolveEndpoint: unexpected error: %v", err)
	}
	if ep.ID != "global" {
		t.Fatalf("resolved endpoint = %q, want %q", ep.ID, "global")
	}
	if ep.BaseURL != good.URL+"/v1" {
		t.Fatalf("resolved base URL = %q, want %q", ep.BaseURL, good.URL+"/v1")
	}
}

// TestResolveEndpoint_CachesPerCredential: a second resolution with the
// SAME credential must not probe again; a different credential must
// probe again (the key changed → the endpoint may change too).
func TestResolveEndpoint_CachesPerCredential(t *testing.T) {
	hits := 0
	srv := newProbeServer(t, "key-a", &hits)
	// The rotated key is accepted too: the point of the assertion is
	// that a CHANGED credential forces a fresh probe, not that the
	// rotation is rejected.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if !strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "key-a" && token != "key-b" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"MiniMax-M3"}]}`))
	})

	spec := &ProviderSpec{
		ID: "minimax",
		Endpoints: []EndpointSpec{
			{ID: "only", BaseURL: srv.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
		},
	}

	if _, err := ResolveEndpoint(context.Background(), spec, "key-a"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if hits != 1 {
		t.Fatalf("probe hits after first resolve = %d, want 1", hits)
	}
	if _, err := ResolveEndpoint(context.Background(), spec, "key-a"); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if hits != 1 {
		t.Fatalf("probe hits after cached resolve = %d, want 1 (cache miss)", hits)
	}
	if _, err := ResolveEndpoint(context.Background(), spec, "key-b"); err != nil {
		t.Fatalf("resolve with rotated key: %v", err)
	}
	if hits != 2 {
		t.Fatalf("probe hits after key rotation = %d, want 2 (cache must be per-credential)", hits)
	}
}

// TestResolveEndpoint_NoCandidateAcceptsKey: the error must name every
// candidate, so a wrong key and a wrong endpoint are distinguishable.
func TestResolveEndpoint_NoCandidateAcceptsKey(t *testing.T) {
	bad := newProbeServer(t, "another-key", nil)
	spec := &ProviderSpec{
		ID: "minimax",
		Endpoints: []EndpointSpec{
			{ID: "cn", BaseURL: bad.URL + "/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
		},
	}
	_, err := ResolveEndpoint(context.Background(), spec, "wrong-key")
	if err == nil {
		t.Fatal("expected an error when no endpoint accepts the key")
	}
	if !strings.Contains(err.Error(), "cn") {
		t.Fatalf("error must name the tried candidate, got: %v", err)
	}
}

// TestResolveEndpoint_OperatorOverrideWins: the env override is
// explicit and must not be probed away.
func TestResolveEndpoint_OperatorOverrideWins(t *testing.T) {
	t.Setenv("DARK_LLM_BASE_URL_MINIMAX", "https://operator.example/v1")
	spec := &ProviderSpec{
		ID: "minimax",
		Endpoints: []EndpointSpec{
			{ID: "global", BaseURL: "https://catalog.example/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer},
		},
	}
	ep, err := ResolveEndpoint(context.Background(), spec, "any-key")
	if err != nil {
		t.Fatalf("ResolveEndpoint: %v", err)
	}
	if ep.BaseURL != "https://operator.example/v1" {
		t.Fatalf("override base URL = %q, want the operator's", ep.BaseURL)
	}
	if ep.ID != "env" {
		t.Fatalf("override endpoint id = %q, want %q", ep.ID, "env")
	}
}

// TestEndpointCandidates_SingleProviderHasOneCandidate: providers
// without an explicit candidate list expose their own BaseURL.
func TestEndpointCandidates_SingleProviderHasOneCandidate(t *testing.T) {
	spec := &ProviderSpec{ID: "openai", BaseURL: "https://api.openai.com/v1", ProbePath: "/models"}
	cands := spec.EndpointCandidates()
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1", len(cands))
	}
	if cands[0].BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("candidate base URL = %q", cands[0].BaseURL)
	}
}

// TestCatalog_MinimaxDeclaresBothRegions: the catalog must not pin a
// single region for a provider whose keys are region-scoped.
func TestCatalog_MinimaxDeclaresBothRegions(t *testing.T) {
	spec := SpecByID("minimax")
	if spec == nil {
		t.Fatal("minimax missing from the catalog")
	}
	ids := map[string]bool{}
	for _, e := range spec.EndpointCandidates() {
		ids[e.ID] = true
	}
	if !ids["global"] || !ids["cn"] {
		t.Fatalf("minimax candidates = %v, want both global and cn", ids)
	}
	cn := SpecByID("minimax-cn")
	if cn == nil {
		t.Fatal("minimax-cn missing from the catalog")
	}
	if got := len(cn.EndpointCandidates()); got != 1 {
		t.Fatalf("minimax-cn candidates = %d, want 1 (CN region only)", got)
	}
}

// TestDefaultModelForURL_ResolvesThroughCandidates: the probe needs a
// real model name for whichever endpoint it ends up calling.
func TestDefaultModelForURL_ResolvesThroughCandidates(t *testing.T) {
	spec := SpecByID("minimax")
	if spec == nil {
		t.Fatal("minimax missing from the catalog")
	}
	for _, e := range spec.EndpointCandidates() {
		if got := DefaultModelForURL(e.BaseURL + "/chat/completions"); got != spec.DefaultModel {
			t.Fatalf("DefaultModelForURL(%q) = %q, want %q", e.BaseURL, got, spec.DefaultModel)
		}
	}
}
