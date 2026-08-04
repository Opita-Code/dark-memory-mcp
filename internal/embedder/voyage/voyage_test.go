// Package voyage — tests using httptest to mock api.voyageai.com.
//
// Covers the API key check, the retry policy, and the response
// decoding. Tests run against a local httptest server; no real
// Voyage calls are made.

package voyage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

func TestNew_MissingAPIKey(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	_, err := New(Options{})
	if err != embedder.ErrKeyMissing {
		t.Fatalf("expected ErrKeyMissing, got %v", err)
	}
}

func TestNew_WithAPIKey(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	a, err := New(Options{APIKey: "test-key"})
	if err != nil {
		t.Fatalf("New with Options.APIKey: %v", err)
	}
	if a.Kind() != KindVoyage {
		t.Errorf("Kind=%q, want %q", a.Kind(), KindVoyage)
	}
	if a.Dim() != 1024 {
		t.Errorf("Dim=%d, want 1024", a.Dim())
	}
}

func TestDimForModel(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"voyage-3", 1024},
		{"voyage-3-lite", 512},
		{"voyage-code-3", 1024},
		{"voyage-large-2", 1536},
		{"unknown", 0},
	}
	for _, tc := range cases {
		got := dimForModel(tc.model)
		if got != tc.want {
			t.Errorf("dimForModel(%q)=%d, want %d", tc.model, got, tc.want)
		}
	}
}

// TestEmbed_HappyPath mocks the Voyage /embeddings endpoint and
// verifies the response decoding + ordering.
func TestEmbed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path=%q, want /embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization=%q, want Bearer test-key", got)
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		// Respond with index-aligned embeddings.
		out := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			out[i] = map[string]any{
				"index":     i,
				"embedding": []float32{0.1, 0.2, 0.3},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
	}))
	defer srv.Close()

	a, err := New(Options{
		APIKey:         "test-key",
		BaseURL:        srv.URL,
		ConnectTimeout: 2 * time.Second,
		ReadTimeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	vecs, err := a.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vecs, want 2", len(vecs))
	}
	if len(vecs[0]) != 3 || len(vecs[1]) != 3 {
		t.Errorf("dim mismatch: %v", vecs)
	}
}

// TestEmbed_401FallsBackToKeyMissing verifies that a 401 returns
// embedder.ErrKeyMissing so the factory ladder can fall through.
func TestEmbed_401FallsBackToKeyMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer srv.Close()

	a, _ := New(Options{APIKey: "bad-key", BaseURL: srv.URL})
	defer a.Close()
	_, err := a.Embed(context.Background(), []string{"hi"})
	if err != embedder.ErrKeyMissing {
		t.Fatalf("expected ErrKeyMissing on 401, got %v", err)
	}
}

// TestEmbed_RetriesOn5xx verifies the retry policy kicks in on
// 5xx and stops at the second success.
func TestEmbed_RetriesOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		out := []map[string]any{{"index": 0, "embedding": []float32{1.0}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
	}))
	defer srv.Close()

	a, _ := New(Options{
		APIKey:         "k",
		BaseURL:        srv.URL,
		MaxRetries:     2,
		ConnectTimeout: 2 * time.Second,
		ReadTimeout:    2 * time.Second,
	})
	defer a.Close()
	_, err := a.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatalf("Embed after retry: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls=%d, want 2 (1 fail + 1 success)", got)
	}
}

// TestEmbed_NoRetryOn4xx verifies 4xx (other than 401) does NOT
// retry — callers see the error immediately.
func TestEmbed_NoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()

	a, _ := New(Options{APIKey: "k", BaseURL: srv.URL, MaxRetries: 3})
	defer a.Close()
	_, err := a.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatalf("expected error on 400")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls=%d, want 1 (no retry on 400)", got)
	}
}

// TestEmbed_OutOfBoundsIndex verifies the decoder rejects bad
// response indices.
func TestEmbed_OutOfBoundsIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":99,"embedding":[1.0]}]}`))
	}))
	defer srv.Close()

	a, _ := New(Options{APIKey: "k", BaseURL: srv.URL})
	defer a.Close()
	_, err := a.Embed(context.Background(), []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "out-of-bounds") {
		t.Fatalf("expected out-of-bounds error, got %v", err)
	}
}

// TestEmbed_EmptyInputs returns nil vecs + nil err for empty input.
func TestEmbed_EmptyInputs(t *testing.T) {
	a, _ := New(Options{APIKey: "k", BaseURL: "http://localhost:0"})
	defer a.Close()
	vecs, err := a.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil input err: %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("nil input vecs=%v, want empty", vecs)
	}
}

// TestEmbed_ContextCancel verifies context cancellation propagates.
func TestEmbed_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // longer than test timeout
	}))
	defer srv.Close()

	a, _ := New(Options{APIKey: "k", BaseURL: srv.URL, ConnectTimeout: 1 * time.Second})
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Embed(ctx, []string{"hi"})
	if err == nil {
		t.Fatalf("expected context cancel error")
	}
}

// Compile-time guard.
var _ embedder.Embedder = (*voyageAdapter)(nil)
