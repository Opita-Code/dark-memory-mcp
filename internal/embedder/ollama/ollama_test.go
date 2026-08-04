// Package ollama — tests using httptest to mock the Ollama daemon.
//
// Covers the localhost default, retry policy, and 404 / 500
// handling. Tests run against a local httptest server; no real
// Ollama is required.

package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

func TestNew_NoKeyRequired(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	a, err := New(Options{})
	if err != nil {
		t.Fatalf("New with no key: %v", err)
	}
	if a.Kind() != KindOllama {
		t.Errorf("Kind=%q, want %q", a.Kind(), KindOllama)
	}
	if a.Dim() != 768 {
		t.Errorf("Dim=%d, want 768 (nomic-embed-text)", a.Dim())
	}
}

func TestDimForModel(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"nomic-embed-text", 768},
		{"mxbai-embed-large", 1024},
		{"snowflake-arctic-embed", 1024},
		{"all-minilm", 384},
		{"unknown", 0},
	}
	for _, tc := range cases {
		if got := dimForModel(tc.model); got != tc.want {
			t.Errorf("dimForModel(%q)=%d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestEmbed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("path=%q, want /api/embeddings", r.URL.Path)
		}
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.5, 0.6}})
	}))
	defer srv.Close()

	a, _ := New(Options{BaseURL: srv.URL})
	defer a.Close()

	vecs, err := a.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vecs, want 2", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 2 {
			t.Errorf("vec[%d] dim=%d, want 2", i, len(v))
		}
	}
}

func TestEmbed_RetriesOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1.0}})
	}))
	defer srv.Close()

	a, _ := New(Options{BaseURL: srv.URL, MaxRetries: 2})
	defer a.Close()
	_, err := a.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatalf("Embed after retry: %v", err)
	}
	// 2 calls expected (1 fail + 1 success); Embed uses parallelism=4
	// so 2 sequential per text.
	if got := calls.Load(); got < 2 {
		t.Errorf("calls=%d, want >=2", got)
	}
}

func TestEmbed_NoRetryOn404(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a, _ := New(Options{BaseURL: srv.URL, MaxRetries: 3})
	defer a.Close()
	_, err := a.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatalf("expected error on 404")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls=%d, want 1 (no retry on 404)", got)
	}
}

func TestEmbed_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	a, _ := New(Options{BaseURL: srv.URL, ConnectTimeout: 1 * time.Second})
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Embed(ctx, []string{"hi"})
	if err == nil {
		t.Fatalf("expected context cancel error")
	}
}

func TestEmbed_EmptyInputs(t *testing.T) {
	a, _ := New(Options{BaseURL: "http://localhost:0"})
	defer a.Close()
	vecs, err := a.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil input err: %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("nil input vecs=%v", vecs)
	}
}

// Compile-time guard.
var _ embedder.Embedder = (*ollamaAdapter)(nil)
