// Package orchestration — nli_wiring_test.go
//
// v2.20.0 T08 (spec 1276): tests for nliProviderForConfig. The provider
// chain construction is the seam between Project.NLIConfig (T07) and
// the drift_judge pipeline. Regression tests pin down:
//
//   - nil / disabled NLIConfig → (nil, nil) so the caller falls through.
//   - enabled primary only → DeBERTa or MiniCheck selected by prefix.
//   - enabled primary + fallback → nli.NewRouter wraps with policy.
//   - cache layer (MaxCacheEntries > 0) → nli.CachedProvider.
//   - unknown provider_id prefix → ErrInvalidConfig.
//   - empty ProviderID / Endpoint → ErrInvalidConfig.
//   - default http client when none injected.
package orchestration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
)

// validDebConfig returns a minimal valid NLIConfig with the DeBERTa
// provider. Tests modify fields to exercise boundaries.
func validDebConfig() *project.NLIConfig {
	cfg := &project.NLIConfig{
		Enabled: true,
		Primary: project.NLIPrimary{
			ProviderID: "deberta-v3-large-mnli",
			Endpoint:   "http://localhost/deberta",
			AuthToken:  "test-token",
			TimeoutMS:  5000,
			ModelRev:   "rev-abc",
		},
		LatencyBudgetMS:    10000,
		MaxPremiseBytes:    65536,
		MaxHypothesisBytes: 8192,
		MaxCacheEntries:    1000,
		CacheTTLSeconds:    86400,
	}
	return cfg
}

// TestNLIProviderForConfig_NilConfig_ReturnsNil — nil config is the
// signal "no project override; use defaults". The caller decides
// what defaults mean.
func TestNLIProviderForConfig_NilConfig_ReturnsNil(t *testing.T) {
	p, err := nliProviderForConfig(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil provider for nil config, got %T", p)
	}
}

// TestNLIProviderForConfig_DisabledConfig_ReturnsNil — Enabled=false
// means the operator explicitly disabled NLIConfig. Same as nil.
func TestNLIProviderForConfig_DisabledConfig_ReturnsNil(t *testing.T) {
	cfg := validDebConfig()
	cfg.Enabled = false
	p, err := nliProviderForConfig(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil provider for disabled config, got %T", p)
	}
}

// TestNLIProviderForConfig_DeBERTaPrimary_BuildsProvider verifies
// that a primary DeBERTa config produces a DeBERTa-backed Provider
// (the cache layer is the immediate wrapper, so caching passes
// through to the inner DeBERTa and the ID comes from it).
func TestNLIProviderForConfig_DeBERTaPrimary_BuildsProvider(t *testing.T) {
	cfg := validDebConfig()
	hc := &http.Client{Transport: &countingTransport{}}
	p, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if got := p.ID(); got != "deberta-v3-large-mnli" {
		t.Errorf("ID: got %q, want %q", got, "deberta-v3-large-mnli")
	}
}

// TestNLIProviderForConfig_MiniCheckPrimary_BuildsProvider verifies
// the ProviderID prefix dispatch.
func TestNLIProviderForConfig_MiniCheckPrimary_BuildsProvider(t *testing.T) {
	cfg := validDebConfig()
	cfg.Primary.ProviderID = "minicheck-flan-t5-large"
	cfg.Primary.Endpoint = "http://localhost/minicheck"
	hc := &http.Client{Transport: &countingTransport{}}
	p, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if got := p.ID(); got != "minicheck-flan-t5-large" {
		t.Errorf("ID: got %q, want %q", got, "minicheck-flan-t5-large")
	}
}

// TestNLIProviderForConfig_Fallback_BuildsRouter verifies that
// FallbackEnabled=true causes the chain to be wrapped in nli.NewRouter.
// The Router's ID() is the primary's ID (provenance flows through).
func TestNLIProviderForConfig_Fallback_BuildsRouter(t *testing.T) {
	cfg := validDebConfig()
	cfg.FallbackEnabled = true
	cfg.Fallback = project.NLIPrimary{
		ProviderID: "minicheck-flan-t5-large",
		Endpoint:   "http://localhost/minicheck",
		AuthToken:  "fb-token",
		TimeoutMS:  5000,
	}
	hc := &http.Client{Transport: &countingTransport{}}
	p, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if got := p.ID(); got != "deberta-v3-large-mnli" {
		t.Errorf("ID: got %q, want %q (primary's ID flows through)", got, "deberta-v3-large-mnli")
	}
	// Sanity: the underlying type is *nli.Router (not the bare
	// DeBERTa provider). We can spot-check by ensuring the
	// RouterStats are exposed.
	if _, ok := p.(*nli.Router); !ok {
		t.Errorf("expected *nli.Router, got %T", p)
	}
}

// TestNLIProviderForConfig_NoCache_RawProvider — MaxCacheEntries=0
// means no cache layer. The provider is the bare DeBERTa.
func TestNLIProviderForConfig_NoCache_RawProvider(t *testing.T) {
	cfg := validDebConfig()
	cfg.MaxCacheEntries = 0
	hc := &http.Client{Transport: &countingTransport{}}
	p, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	// Without cache + without fallback, the provider is the raw
	// DeBERTa (not wrapped in CachedProvider or Router).
	if _, ok := p.(*nli.DeBERTaProvider); !ok {
		t.Errorf("expected *nli.DeBERTaProvider, got %T", p)
	}
}

// TestNLIProviderForConfig_UnknownProviderID_Rejects verifies that
// unknown provider prefixes are rejected at construction. The operator
// must use deberta* or minicheck* today.
func TestNLIProviderForConfig_UnknownProviderID_Rejects(t *testing.T) {
	cfg := validDebConfig()
	cfg.Primary.ProviderID = "gpt-jury-1"
	hc := &http.Client{Transport: &countingTransport{}}
	_, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err == nil {
		t.Fatal("expected error for unknown provider_id")
	}
	if !strings.Contains(err.Error(), "unknown provider_id") {
		t.Errorf("error: got %q, want substring 'unknown provider_id'", err)
	}
}

// TestNLIProviderForConfig_EmptyProviderID_Rejects — the Provider
// factory validates empty fields. Calling NewDeBERTaProvider with
// empty ProviderID returns ErrInvalidConfig.
func TestNLIProviderForConfig_EmptyProviderID_Rejects(t *testing.T) {
	cfg := validDebConfig()
	cfg.Primary.ProviderID = ""
	hc := &http.Client{Transport: &countingTransport{}}
	_, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err == nil {
		t.Fatal("expected error for empty provider_id")
	}
}

// TestNLIProviderForConfig_EmptyEndpoint_Rejects.
func TestNLIProviderForConfig_EmptyEndpoint_Rejects(t *testing.T) {
	cfg := validDebConfig()
	cfg.Primary.Endpoint = ""
	hc := &http.Client{Transport: &countingTransport{}}
	_, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}

// TestNLIProviderForConfig_FallbackEmptyFields_Rejects — fallback
// fields are validated when FallbackEnabled=true.
func TestNLIProviderForConfig_FallbackEmptyFields_Rejects(t *testing.T) {
	cfg := validDebConfig()
	cfg.FallbackEnabled = true
	cfg.Fallback = project.NLIPrimary{
		ProviderID: "", // empty
		Endpoint:   "http://localhost/minicheck",
		TimeoutMS:  5000,
	}
	hc := &http.Client{Transport: &countingTransport{}}
	_, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err == nil {
		t.Fatal("expected error for empty fallback provider_id")
	}
}

// TestNLIProviderForConfig_NilHTTPClient_DefaultsToDefaultClient —
// nil → http.DefaultClient. The Provider chain still builds.
func TestNLIProviderForConfig_NilHTTPClient_DefaultsToDefaultClient(t *testing.T) {
	cfg := validDebConfig()
	cfg.MaxCacheEntries = 0 // skip cache to avoid the in-memory cache dependency
	p, err := nliProviderForConfig(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

// TestBuildNLIPrimary_DispatchesByPrefix — sanity: each prefix
// yields the corresponding Provider type.
func TestBuildNLIPrimary_DispatchesByPrefix(t *testing.T) {
	hc := &http.Client{Transport: &countingTransport{}}
	deb, err := buildNLIPrimary(project.NLIPrimary{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   "http://x/deberta",
		TimeoutMS:  5000,
	}, hc, 1024, 1024)
	if err != nil {
		t.Fatalf("deberta: %v", err)
	}
	if _, ok := deb.(*nli.DeBERTaProvider); !ok {
		t.Errorf("deberta: got %T", deb)
	}

	minicheck, err := buildNLIPrimary(project.NLIPrimary{
		ProviderID: "minicheck-roberta-large",
		Endpoint:   "http://x/minicheck",
		TimeoutMS:  5000,
	}, hc, 1024, 1024)
	if err != nil {
		t.Fatalf("minicheck: %v", err)
	}
	if _, ok := minicheck.(*nli.MiniCheckProvider); !ok {
		t.Errorf("minicheck: got %T", minicheck)
	}
}

// countingTransport is a no-op http.RoundTripper that increments
// a counter on every call. Useful for verifying that the HTTP
// client was actually wired (the chain's underlying transport).
type countingTransport struct {
	calls int
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls++
	return &http.Response{
		StatusCode: 200,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// TestNLIProviderForConfig_TLSPinnedEndpoint — the production
// Provider config often uses HTTPS URLs. The wiring must pass
// them through verbatim (no scheme mutation).
func TestNLIProviderForConfig_TLSPinnedEndpoint(t *testing.T) {
	cfg := validDebConfig()
	cfg.Primary.Endpoint = "https://router.huggingface.co/hf-inference/models/microsoft/deberta-v3-large-mnli"
	cfg.MaxCacheEntries = 0
	hc := &http.Client{Transport: &countingTransport{}}
	p, err := nliProviderForConfig(context.Background(), cfg, hc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	// Hijack the underlying http client via stdout? No — the Provider
	// only exposes Score(). Sanity: the type is DeBERTaProvider.
	if _, ok := p.(*nli.DeBERTaProvider); !ok {
		t.Errorf("expected *nli.DeBERTaProvider, got %T", p)
	}
}

// TestNLIPrimaryToProviderConfig_FieldMapping — verify the
// field-by-field mapping (no field dropped, no field added).
func TestNLIPrimaryToProviderConfig_FieldMapping(t *testing.T) {
	src := project.NLIPrimary{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   "https://api.example.com",
		AuthToken:  "secret",
		TimeoutMS:  12345,
		ModelRev:   "rev-xyz",
	}
	got := nliPrimaryToProviderConfig(src)
	if got.ProviderID != src.ProviderID {
		t.Errorf("ProviderID: got %q, want %q", got.ProviderID, src.ProviderID)
	}
	if got.Endpoint != src.Endpoint {
		t.Errorf("Endpoint: got %q, want %q", got.Endpoint, src.Endpoint)
	}
	if got.AuthToken != src.AuthToken {
		t.Errorf("AuthToken: got %q, want %q", got.AuthToken, src.AuthToken)
	}
	if got.TimeoutMS != src.TimeoutMS {
		t.Errorf("TimeoutMS: got %d, want %d", got.TimeoutMS, src.TimeoutMS)
	}
	if got.ModelRev != src.ModelRev {
		t.Errorf("ModelRev: got %q, want %q", got.ModelRev, src.ModelRev)
	}
	// AuthToken is preserved going INTO the provider — never echoed back
	// in errors (the DeBERTa provider's error path is sealed: errors
	// are wrapped but the auth_token is never inscribed). This is a
	// separate invariant; verified in nli/deberta_test.go.
}

// TestOrchestrator_WithNLIRouter_RoundTrip — verify the setter
// stores the Provider and the getter returns the same instance.
func TestOrchestrator_WithNLIRouter_RoundTrip(t *testing.T) {
	o := &Orchestrator{}
	stub := &stubProvider{id: "stub-1"}
	o.WithNLIRouter(stub)
	got, err := o.EnsureNLIRouter(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != stub {
		t.Errorf("EnsureNLIRouter: got %v, want %v", got, stub)
	}
}

// stubProvider is a minimal nli.Provider for testing setters.
type stubProvider struct {
	id string
}

func (s *stubProvider) ID() string { return s.id }
func (s *stubProvider) Score(ctx context.Context, premise, hypothesis string) (nli.Score, error) {
	return nli.Score{
		Label:      nli.LabelEntailment,
		Confidence: 1.0,
		ProviderID: s.id,
	}, nil
}

// silenceUnused ensures httptest is imported even when no test
// uses it directly (some test suites may add live-server tests later).
var _ = httptest.NewServer
