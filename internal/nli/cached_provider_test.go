package nli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubProvider is the test-only double for Provider. Lets us count calls
// and inject errors without spinning up a real HTTP server.
type stubProvider struct {
	mu          sync.Mutex
	id          string
	calls       atomic.Int32
	scoreToRet  Score
	errToRet    error
	delay       time.Duration
	capturedPrem string
	capturedHyp  string
}

func (s *stubProvider) ID() string { return s.id }

func (s *stubProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.capturedPrem = premise
	s.capturedHyp = hypothesis
	s.mu.Unlock()
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return Score{ProviderID: s.id}, ctx.Err()
		}
	}
	return s.scoreToRet, s.errToRet
}

// --- CachedProvider -------------------------------------------------------

func TestNewCachedProvider_Validation(t *testing.T) {
	t.Parallel()
	good, _ := NewInMemoryLRU(10)
	if _, err := NewCachedProvider(nil, good, time.Hour); err == nil {
		t.Errorf("nil inner: err=nil, want non-nil")
	}
	stub := &stubProvider{id: "p"}
	if _, err := NewCachedProvider(stub, nil, time.Hour); err == nil {
		t.Errorf("nil cache: err=nil, want non-nil")
	}
	if _, err := NewCachedProvider(stub, good, 0); !errors.Is(err, ErrCacheInvalidTTL) {
		t.Errorf("ttl=0: err=%v, want ErrCacheInvalidTTL", err)
	}
	if _, err := NewCachedProvider(stub, good, -1); !errors.Is(err, ErrCacheInvalidTTL) {
		t.Errorf("ttl=-1: err=%v, want ErrCacheInvalidTTL", err)
	}
	if c, err := NewCachedProvider(stub, good, time.Hour); err != nil || c == nil {
		t.Errorf("valid args: c=%v err=%v", c, err)
	}
}

func TestCachedProvider_HitDoesNotCallInner(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "p", scoreToRet: Score{Label: LabelEntailment, Confidence: 0.9, ProviderID: "p"}}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	// Prime cache.
	if _, err := c.Score(context.Background(), "p", "h"); err != nil {
		t.Fatalf("first Score: %v", err)
	}
	if stub.calls.Load() != 1 {
		t.Fatalf("calls after first Score=%d, want 1", stub.calls.Load())
	}
	// Second call: must hit cache.
	got, err := c.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("second Score: %v", err)
	}
	if stub.calls.Load() != 1 {
		t.Errorf("inner called on cache hit: calls=%d, want 1", stub.calls.Load())
	}
	if got.Label != LabelEntailment || got.Confidence != 0.9 {
		t.Errorf("hit returned wrong score: %v", got)
	}
	st := c.Stats()
	if st.Hits != 1 {
		t.Errorf("Stats.Hits=%d, want 1", st.Hits)
	}
	if st.Misses != 1 {
		t.Errorf("Stats.Misses=%d, want 1", st.Misses)
	}
}

func TestCachedProvider_DifferentKeysHitDifferentCacheSlots(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "p", scoreToRet: Score{Label: LabelEntailment, Confidence: 0.5, ProviderID: "p"}}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	_, _ = c.Score(context.Background(), "a", "x")
	_, _ = c.Score(context.Background(), "b", "y")
	_, _ = c.Score(context.Background(), "a", "x") // duplicate of first
	if stub.calls.Load() != 2 {
		t.Errorf("calls=%d, want 2 (third should hit cache)", stub.calls.Load())
	}
}

func TestCachedProvider_ErrorsAreNotCached(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "p", errToRet: ErrProviderUnavailable}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	_, err := c.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("first: err=%v, want ErrProviderUnavailable", err)
	}
	_, err = c.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("second: err=%v, want ErrProviderUnavailable (errors not cached)", err)
	}
	if stub.calls.Load() != 2 {
		t.Errorf("calls=%d, want 2 (errors must not be cached)", stub.calls.Load())
	}
	st := c.Stats()
	if st.InnerErrors != 2 {
		t.Errorf("InnerErrors=%d, want 2", st.InnerErrors)
	}
	if st.Puts != 0 {
		t.Errorf("Puts=%d, want 0 (no successful Put)", st.Puts)
	}
}

func TestCachedProvider_ThenSuccessPopulatesCache(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "p", errToRet: ErrProviderUnavailable}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	_, _ = c.Score(context.Background(), "p", "h") // error
	stub.mu.Lock()
	stub.errToRet = nil
	stub.scoreToRet = Score{Label: LabelNeutral, Confidence: 0.5, ProviderID: "p"}
	stub.mu.Unlock()
	_, err := c.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("third: err=%v", err)
	}
	if stub.calls.Load() != 2 {
		t.Errorf("calls=%d, want 2 (third must hit cache)", stub.calls.Load())
	}
}

func TestCachedProvider_PassesInputsUnchangedToInner(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "p", scoreToRet: Score{Label: LabelEntailment, ProviderID: "p"}}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	_, _ = c.Score(context.Background(), "the premise", "the hyp")
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.capturedPrem != "the premise" || stub.capturedHyp != "the hyp" {
		t.Errorf("captured=%q/%q, want the premise/the hyp", stub.capturedPrem, stub.capturedHyp)
	}
}

func TestCachedProvider_ID(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "the-provider"}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	if c.ID() != "the-provider" {
		t.Errorf("ID=%q, want the-provider", c.ID())
	}
}

func TestCachedProvider_Stats_AcrossManyCalls(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "p", scoreToRet: Score{Label: LabelEntailment, ProviderID: "p"}}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	for i := 0; i < 5; i++ {
		_, _ = c.Score(context.Background(), "p", "h") // miss on i=0, hits on i=1..4
	}
	st := c.Stats()
	if st.Hits != 4 {
		t.Errorf("Hits=%d, want 4", st.Hits)
	}
	if st.Misses != 1 {
		t.Errorf("Misses=%d, want 1", st.Misses)
	}
	if st.Puts != 1 {
		t.Errorf("Puts=%d, want 1", st.Puts)
	}
	if st.InnerErrors != 0 {
		t.Errorf("InnerErrors=%d, want 0", st.InnerErrors)
	}
}

func TestCachedProvider_ConcurrentGet_RaceFree(t *testing.T) {
	t.Parallel()
	stub := &stubProvider{id: "p", scoreToRet: Score{Label: LabelEntailment, ProviderID: "p"}}
	cache, _ := NewInMemoryLRU(100)
	c, _ := NewCachedProvider(stub, cache, time.Hour)
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := c.Score(context.Background(), "p", "h")
			if err != nil {
				t.Errorf("Score: %v", err)
			}
		}()
	}
	wg.Wait()
	// Race detector catches data races; one inner call expected if cache works.
	if stub.calls.Load() != 1 {
		t.Errorf("calls=%d, want 1 (race winner)", stub.calls.Load())
	}
}

// --- Integration: real DeBERTa via httptest + cache ----------------------

func TestCachedProvider_IntegrationWithRealDeBERTa(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"label":"LABEL_2","score":0.88}]`))
	}))
	defer srv.Close()
	deberta, err := NewDeBERTaProvider(ProviderConfig{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   srv.URL,
		AuthToken:  "hf",
	}, http.DefaultClient, 64*1024, 8*1024)
	if err != nil {
		t.Fatalf("NewDeBERTaProvider: %v", err)
	}
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(deberta, cache, time.Hour)
	// 5 identical calls — only 1 inner hit.
	for i := 0; i < 5; i++ {
		got, err := c.Score(context.Background(), "p", "h")
		if err != nil {
			t.Fatalf("Score %d: %v", i, err)
		}
		if got.Label != LabelEntailment || got.Confidence != 0.88 {
			t.Errorf("Score %d: %v", i, got)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls=%d, want 1", calls.Load())
	}
	if c.Stats().Hits != 4 {
		t.Errorf("Hits=%d, want 4", c.Stats().Hits)
	}
}

func TestCachedProvider_Integration_ErrorThenSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"label":"LABEL_0","score":0.92}]`))
	}))
	defer srv.Close()
	deberta, _ := NewDeBERTaProvider(ProviderConfig{
		ProviderID: "deberta", Endpoint: srv.URL, AuthToken: "hf",
	}, http.DefaultClient, 1024, 1024)
	cache, _ := NewInMemoryLRU(10)
	c, _ := NewCachedProvider(deberta, cache, time.Hour)
	_, err := c.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("first call err=%v, want Unavailable", err)
	}
	got, err := c.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got.Label != LabelContradiction {
		t.Errorf("Label=%v, want Contradiction", got.Label)
	}
	// Third call must hit cache.
	got, err = c.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("HTTP calls=%d, want 2 (errors not cached, third is cache hit)", calls.Load())
	}
}