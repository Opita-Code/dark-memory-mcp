// Unit tests for the executor. The executor is the highest-level
// orchestration in the research package; it pulls together the
// HTTP client, the cache, the health map, the gate, the parsers,
// and the merge step. The tests cover the orchestration without
// doing real network I/O — we use httptest.
package research

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/security"
)

// TestMain flips the SSRF test bypass on so httptest.NewServer
// (which binds to 127.0.0.1 over plain http) is reachable from
// the executor. The bypass is the documented escape hatch; the
// security package's own tests run without it (separate
// process, separate flag).
func TestMain(m *testing.M) {
	if os.Getenv("DARK_TEST_SSRF_BYPASS") != "1" {
		security.SetTestMode(true)
	}
	os.Exit(m.Run())
}

func TestExecutor_AppliesVibeCaseFilter(t *testing.T) {
	// All backends in the test slice serve "C1" only; C2 must
	// be rejected before any HTTP call.
	calls := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// RDAP requires ldhName; provide one.
		_, _ = w.Write([]byte(`{"ldhName":"example.com","status":["active"],"events":[{"eventAction":"registration","eventDate":"2020-01-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	backends := []Backend{
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{Name: "test-only-c1", Intent: "domain"},
				ManifestV4:      ManifestV4{Tier: 1, ServesVibeCases: []VibeCase{VibeC1Code}, URLTemplate: srv.URL + "/{name}"},
			},
			ParserName: "rdap",
		},
	}
	httpClient := NewHTTPClient(HTTPConfig{PerCallTimeoutMs: 1000, MaxParallel: 1, MaxBodyBytes: 1 << 16})
	exec := NewExecutorWithBackends(backends, httpClient)

	// Diagnose: applicable backends.
	applicable := applicableBackends(backends, "domain", VibeC1Code)
	t.Logf("applicable: %d backends", len(applicable))
	for _, b := range applicable {
		t.Logf("  %s intent=%q tier=%d parser=%q", b.Manifest.Name, b.Manifest.Intent, b.Manifest.Tier, b.ParserName)
	}
	fullURL, _ := substituteTemplate(backends[0].Manifest.URLTemplate, "example.com")
	t.Logf("fullURL: %q", fullURL)

	// C1: should hit the backend.
	evidence, err := exec.Execute(context.Background(), "domain", VibeC1Code, "example.com", DepthStandard)
	if err != nil {
		t.Fatalf("C1: %v", err)
	}
	t.Logf("C1 evidence: %+v", evidence)
	if len(evidence) == 0 {
		t.Errorf("C1 produced no evidence (expected 1)")
	}
	if calls.Load() == 0 {
		t.Errorf("C1 did not hit the HTTP server")
	}

	// C2: should be filtered out before any HTTP call.
	callsBeforeC2 := calls.Load()
	evidence, err = exec.Execute(context.Background(), "domain", VibeC2Text, "example.com", DepthStandard)
	if err != nil {
		t.Fatalf("C2: %v", err)
	}
	if len(evidence) != 0 {
		t.Errorf("C2 produced evidence (expected 0 due to vibe_case filter)")
	}
	if calls.Load() != callsBeforeC2 {
		t.Errorf("C2 incremented HTTP calls: before=%d, after=%d", callsBeforeC2, calls.Load())
	}
}

func TestExecutor_DepthMaxCalls(t *testing.T) {
	if DepthShallow.MaxCalls() != 1 {
		t.Errorf("shallow: got %d, want 1", DepthShallow.MaxCalls())
	}
	if DepthStandard.MaxCalls() != 2 {
		t.Errorf("standard: got %d, want 2", DepthStandard.MaxCalls())
	}
	if DepthDeep.MaxCalls() != 4 {
		t.Errorf("deep: got %d, want 4", DepthDeep.MaxCalls())
	}
}

func TestExecutor_DepthByteCap(t *testing.T) {
	if DepthShallow.ByteCap() != 16*1024 {
		t.Errorf("shallow: got %d, want %d", DepthShallow.ByteCap(), 16*1024)
	}
	if DepthStandard.ByteCap() != 32*1024 {
		t.Errorf("standard: got %d, want %d", DepthStandard.ByteCap(), 32*1024)
	}
	if DepthDeep.ByteCap() != 64*1024 {
		t.Errorf("deep: got %d, want %d", DepthDeep.ByteCap(), 64*1024)
	}
}

func TestExecutor_DepthMaxTier(t *testing.T) {
	if DepthShallow.MaxTier() != Tier1Authoritative {
		t.Error("shallow should be T1 only")
	}
	if DepthStandard.MaxTier() != Tier2Corroborant {
		t.Error("standard should be T1+T2")
	}
	if DepthDeep.MaxTier() != Tier3Discovery {
		t.Error("deep should be T1+T2+T3")
	}
}

func TestExecutor_DepthString(t *testing.T) {
	if DepthShallow.String() != "shallow" {
		t.Errorf("shallow.String = %q", DepthShallow.String())
	}
	if DepthStandard.String() != "standard" {
		t.Errorf("standard.String = %q", DepthStandard.String())
	}
	if DepthDeep.String() != "deep" {
		t.Errorf("deep.String = %q", DepthDeep.String())
	}
	if Depth(99).String() != "unknown" {
		t.Errorf("unknown.String = %q", Depth(99).String())
	}
}

func TestApplicableBackends_TierAndVibeCaseOrdering(t *testing.T) {
	backends := []Backend{
		{Manifest: ExtendedManifest{BackendManifest: BackendManifest{Name: "t2", Intent: "cve"}, ManifestV4: ManifestV4{Tier: 2, ServesVibeCases: []VibeCase{VibeC1Code}, URLTemplate: "u"}}},
		{Manifest: ExtendedManifest{BackendManifest: BackendManifest{Name: "t1-a", Intent: "cve"}, ManifestV4: ManifestV4{Tier: 1, ServesVibeCases: []VibeCase{VibeC1Code}, URLTemplate: "u"}}},
		{Manifest: ExtendedManifest{BackendManifest: BackendManifest{Name: "t3", Intent: "cve"}, ManifestV4: ManifestV4{Tier: 3, ServesVibeCases: []VibeCase{VibeC1Code}, URLTemplate: "u"}}},
		{Manifest: ExtendedManifest{BackendManifest: BackendManifest{Name: "t1-b", Intent: "cve"}, ManifestV4: ManifestV4{Tier: 1, ServesVibeCases: []VibeCase{VibeC2Text}, URLTemplate: "u"}}},
		{Manifest: ExtendedManifest{BackendManifest: BackendManifest{Name: "t1-c", Intent: "domain"}, ManifestV4: ManifestV4{Tier: 1, ServesVibeCases: []VibeCase{VibeC1Code}, URLTemplate: "u"}}},
	}
	got := applicableBackends(backends, "cve", VibeC1Code)
	names := []string{}
	for _, b := range got {
		names = append(names, b.Manifest.Name)
	}
	want := []string{"t1-a", "t2", "t3"}
	if !sliceEq(names, want) {
		t.Errorf("C1+cve: got %v, want %v", names, want)
	}

	got = applicableBackends(backends, "cve", VibeC2Text)
	names = []string{}
	for _, b := range got {
		names = append(names, b.Manifest.Name)
	}
	if !sliceEq(names, []string{"t1-b"}) {
		t.Errorf("C2+cve: got %v, want [t1-b]", names)
	}

	got = applicableBackends(backends, "cve", "")
	names = []string{}
	for _, b := range got {
		names = append(names, b.Manifest.Name)
	}
	want = []string{"t1-a", "t1-b", "t2", "t3"}
	if !sliceEq(names, want) {
		t.Errorf("noVibeCase+cve: got %v, want %v", names, want)
	}
}

func TestSubstituteTemplate(t *testing.T) {
	cases := map[string]struct {
		tpl, target, want string
	}{
		"no placeholder":     {"https://example.com/api", "x", "https://example.com/api"},
		"name placeholder":   {"https://example.com/{name}", "do main.com", "https://example.com/do+main.com"},
		"id placeholder":     {"https://example.com/{id}", "CVE-2026-12345", "https://example.com/CVE-2026-12345"},
		"ip placeholder":     {"https://example.com/{ip}", "1.2.3.4", "https://example.com/1.2.3.4"},
		"q placeholder":      {"https://example.com/?q={q}", "hello world", "https://example.com/?q=hello+world"},
		"prefix placeholder": {"https://example.com/{prefix}", "AB12C", "https://example.com/AB12C"},
		"unknown":            {"https://example.com/{not_a_placeholder}", "x", "https://example.com/{not_a_placeholder}"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := substituteTemplate(c.tpl, c.target)
			if err != nil {
				t.Fatalf("substituteTemplate: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestIntentKey(t *testing.T) {
	a := intentKey("cve", "CVE-2026-12345")
	b := intentKey("cve", "CVE-2026-12345")
	c := intentKey("domain", "CVE-2026-12345")
	if a != b {
		t.Error("same intent+target should produce same key")
	}
	if a == c {
		t.Error("different intent should produce different key")
	}
}

func TestExecutor_CacheFreshOnSecondCall(t *testing.T) {
	calls := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"handle":"EXAMPLE","status":["active"]}`))
	}))
	defer srv.Close()

	backends := []Backend{
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{Name: "test-c1", Intent: "domain"},
				ManifestV4:      ManifestV4{Tier: 1, ServesVibeCases: []VibeCase{VibeC1Code}, URLTemplate: srv.URL + "/{name}"},
			},
			ParserName: "rdap",
		},
	}
	httpClient := NewHTTPClient(HTTPConfig{PerCallTimeoutMs: 1000, MaxParallel: 1, MaxBodyBytes: 1 << 16})
	exec := NewExecutorWithBackends(backends, httpClient)

	// First call: cache miss, hits HTTP.
	_, err := exec.Execute(context.Background(), "domain", VibeC1Code, "example.com", DepthStandard)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first := calls.Load()
	if first == 0 {
		t.Fatal("first call did not hit HTTP")
	}

	// Second call: cache fresh, should NOT hit HTTP.
	_, err = exec.Execute(context.Background(), "domain", VibeC1Code, "example.com", DepthStandard)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	second := calls.Load()
	if second != first {
		t.Errorf("second call hit HTTP (was %d, now %d); cache did not prevent fan-out", first, second)
	}
}

func TestExecutor_DeadBackendSkipped(t *testing.T) {
	// Mark the only backend as dead via the health map; the
	// executor must skip it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"handle":"X","status":["active"]}`))
	}))
	defer srv.Close()

	backends := []Backend{
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{Name: "test-c1", Intent: "domain"},
				ManifestV4:      ManifestV4{Tier: 1, ServesVibeCases: []VibeCase{VibeC1Code}, URLTemplate: srv.URL + "/{name}"},
			},
			ParserName: "rdap",
		},
	}
	httpClient := NewHTTPClient(HTTPConfig{PerCallTimeoutMs: 1000, MaxParallel: 1, MaxBodyBytes: 1 << 16})
	exec := NewExecutorWithBackends(backends, httpClient)
	// Kill it.
	now := time.Now()
	for i := 0; i < 11; i++ {
		exec.healths["test-c1"].RecordError(nil, now)
	}
	if exec.healths["test-c1"].IsCallable() {
		t.Fatal("test setup: backend should be dead after 10 errors")
	}

	evidence, err := exec.Execute(context.Background(), "domain", VibeC1Code, "example.com", DepthStandard)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(evidence) != 0 {
		t.Errorf("dead backend should be skipped, got %d evidence", len(evidence))
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// _ = strings.HasPrefix is a guard to keep strings import in
// case a future test wants it without re-importing.
var _ = strings.HasPrefix
