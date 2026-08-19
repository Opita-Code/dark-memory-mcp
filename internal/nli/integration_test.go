package nli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestIntegration_RouterDeBERTaThenMiniCheck spins up two real
// httptest servers (DeBERTa primary, MiniCheck fallback) and wires a
// Router end-to-end. This is the production wiring the operator will
// use: real HTTP, real JSON, real selection logic.
func TestIntegration_RouterDeBERTaThenMiniCheck(t *testing.T) {
	t.Parallel()

	// DeBERTa returns 503 → triggers fallback.
	debertaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hf_token" {
			t.Errorf("DeBERTa auth=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("DeBERTa Content-Type=%q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"model loading"}`))
	}))
	defer debertaSrv.Close()

	// MiniCheck returns a clean 200 with high entailment probability.
	var minicheckCalls atomic.Int32
	minicheckSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		minicheckCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"score":0.92,"label":1}`))
	}))
	defer minicheckSrv.Close()

	deberta, err := NewDeBERTaProvider(ProviderConfig{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   debertaSrv.URL,
		AuthToken:  "hf_token",
	}, http.DefaultClient, 64*1024, 8*1024)
	if err != nil {
		t.Fatalf("NewDeBERTaProvider: %v", err)
	}
	minicheck, err := NewMiniCheckProvider(ProviderConfig{
		ProviderID: "minicheck-flan-t5-large",
		Endpoint:   minicheckSrv.URL,
		AuthToken:  "mc_token",
	}, http.DefaultClient, 64*1024, 8*1024)
	if err != nil {
		t.Fatalf("NewMiniCheckProvider: %v", err)
	}
	r, err := NewRouter(deberta, minicheck, Config{
		FallbackEnabled: true,
		LatencyBudgetMS: 5000,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	got, err := r.Score(context.Background(), "the document", "the claim")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ProviderID != "minicheck-flan-t5-large" {
		t.Errorf("ProviderID=%q, want fallback (minicheck)", got.ProviderID)
	}
	if got.Label != LabelEntailment {
		t.Errorf("Label=%v, want entailment (0.92 >= 0.7)", got.Label)
	}
	if got.Confidence != 0.92 {
		t.Errorf("Confidence=%v, want 0.92", got.Confidence)
	}
	if minicheckCalls.Load() != 1 {
		t.Errorf("MiniCheck calls=%d, want 1", minicheckCalls.Load())
	}
	if !got.Valid() {
		t.Errorf("Score failed invariant: %+v", got)
	}
}

// TestIntegration_ConcurrentEndToEnd exercises the wiring under load.
func TestIntegration_ConcurrentEndToEnd(t *testing.T) {
	t.Parallel()
	debertaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"label":"LABEL_2","score":0.85}]`))
	}))
	defer debertaSrv.Close()
	deberta, err := NewDeBERTaProvider(ProviderConfig{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   debertaSrv.URL,
		AuthToken:  "hf_token",
	}, http.DefaultClient, 1024, 1024)
	if err != nil {
		t.Fatalf("NewDeBERTaProvider: %v", err)
	}
	r, err := NewRouter(deberta, nil, Config{FallbackEnabled: false, LatencyBudgetMS: 5000})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := r.Score(ctx, "p", "h")
			if err != nil {
				t.Errorf("Score: %v", err)
				return
			}
			if !got.Valid() {
				t.Errorf("Score invalid: %+v", got)
			}
		}()
	}
	wg.Wait()
	st := r.Stats()
	if st.PrimarySuccesses != N {
		t.Errorf("PrimarySuccesses=%d, want %d", st.PrimarySuccesses, N)
	}
}