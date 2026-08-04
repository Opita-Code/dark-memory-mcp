// Package ollama implements the embedder.Embedder contract against a
// local Ollama daemon's /api/embeddings endpoint.
//
// PR-2.1 of the v2.9.0 plan (row 164 §2). Ollama is the local-first
// rung of the ladder: when the harness detector sees OLLAMA_HOST or
// a reachable localhost:11434, the factory picks this adapter so
// the operator gets embeddings without any cloud egress.
//
// # Behavior
//
//   - Constructor reads OLLAMA_HOST (or accepts it via Options);
//     default "http://127.0.0.1:11434".
//   - Default model: nomic-embed-text (768d); override via Options.Model
//     or DARK_MEMORY_OLLAMA_MODEL.
//   - No API key. Ollama is local.
//   - 5s connect / 10s read per request.
//   - One retry on 5xx + 429; never on 4xx.
//   - Dim() = 768 for nomic-embed-text; 1024 for mxbai-embed-large.
//
// # Fail-safe
//
// New() returns embedder.ErrKeyMissing never (no auth). The adapter
// only errors if Ollama is unreachable at first Embed call. Factory
// does NOT pre-flight a TCP probe (that's detect.probeOllama's job;
// this adapter assumes detection succeeded).
//
// # Trust boundary
//
// Embed() only contacts the configured Ollama host. Inputs are sent
// verbatim; Ollama may log them on disk if its logging is enabled
// (operator's responsibility to disable).
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// init registers the Ollama factory with the embedder package.
func init() {
	embedder.RegisterAdapter(KindOllama, func(_ embedder.Options) (embedder.Embedder, error) {
		return New(Options{})
	})
}

// KindOllama is the canonical identifier for the Ollama adapter.
const KindOllama = "ollama"

// Options configures the Ollama adapter.
type Options struct {
	BaseURL        string
	Model          string
	Dim            int
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	MaxRetries     int
}

// New constructs the Ollama adapter. No API key check (Ollama is
// local). Returns immediately; first Embed call surfaces any
// network failures.
func New(opts Options) (embedder.Embedder, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL()
	}
	if opts.Model == "" {
		opts.Model = defaultModel()
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 5 * time.Second
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 10 * time.Second
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 1
	}
	if opts.Dim == 0 {
		opts.Dim = dimForModel(opts.Model)
	}
	return &ollamaAdapter{
		base:           opts.BaseURL,
		model:          opts.Model,
		dim:            opts.Dim,
		connectBudget:  opts.ConnectTimeout,
		readBudget:     opts.ReadTimeout,
		maxRetries:     opts.MaxRetries,
		client:         &http.Client{Timeout: opts.ConnectTimeout + opts.ReadTimeout},
	}, nil
}

func defaultBaseURL() string {
	if v := os.Getenv("OLLAMA_HOST"); v != "" {
		return v
	}
	if v := os.Getenv("DARK_MEMORY_OLLAMA_BASE"); v != "" {
		return v
	}
	return "http://127.0.0.1:11434"
}

func defaultModel() string {
	if v := os.Getenv("DARK_MEMORY_OLLAMA_MODEL"); v != "" {
		return v
	}
	return "nomic-embed-text"
}

func dimForModel(m string) int {
	switch m {
	case "nomic-embed-text":
		return 768
	case "mxbai-embed-large", "snowflake-arctic-embed":
		return 1024
	case "all-minilm":
		return 384
	default:
		return 0
	}
}

type ollamaAdapter struct {
	base  string
	model string
	dim   int

	connectBudget time.Duration
	readBudget    time.Duration
	maxRetries    int

	clientMu sync.RWMutex
	client   *http.Client
}

// Kind returns KindOllama.
func (a *ollamaAdapter) Kind() string { return KindOllama }

// Dim returns the configured dimensionality.
func (a *ollamaAdapter) Dim() int { return a.dim }

// Embed batches inputs. Ollama's /api/embeddings accepts a single
// string per call; we send them sequentially but in parallel via a
// bounded goroutine pool (semaphore).
func (a *ollamaAdapter) Embed(ctx context.Context, texts []string) ([]embedder.Vec, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([]embedder.Vec, len(texts))
	const parallelism = 4
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	errCh := make(chan error, len(texts))

	for i, txt := range texts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(idx int, text string) {
			defer wg.Done()
			defer func() { <-sem }()
			vec, err := a.embedOne(ctx, text)
			if err != nil {
				errCh <- fmt.Errorf("ollama[%d]: %w", idx, err)
				return
			}
			out[idx] = vec
		}(i, txt)
	}
	wg.Wait()
	close(errCh)
	if err, ok := <-errCh; ok {
		return nil, err
	}
	return out, nil
}

// embedOne calls /api/embeddings for a single text. Implements the
// 5xx + 429 retry policy.
func (a *ollamaAdapter) embedOne(ctx context.Context, text string) (embedder.Vec, error) {
	body, err := json.Marshal(struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}{
		Model:  a.model,
		Prompt: text,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= a.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoffFor(attempt)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", a.base+"/api/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("ollama: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		a.clientMu.RLock()
		client := a.client
		a.clientMu.RUnlock()

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = &httpError{Status: resp.StatusCode, Body: string(raw)}
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				continue
			}
			return nil, fmt.Errorf("ollama: %s: %s", resp.Status, lastErr)
		}
		var parsed struct {
			Embedding []float32 `json:"embedding"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("ollama: decode: %w", err)
		}
		resp.Body.Close()
		return embedder.Vec(parsed.Embedding), nil
	}
	return nil, fmt.Errorf("ollama: exhausted %d retries: %w", a.maxRetries, lastErr)
}

// Close releases idle HTTP connections.
func (a *ollamaAdapter) Close() error {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.client != nil {
		a.client.CloseIdleConnections()
		a.client = nil
	}
	return nil
}

func backoffFor(attempt int) time.Duration {
	base := 250 * time.Millisecond
	for i := 1; i < attempt; i++ {
		base *= 3
		if base >= 3*time.Second {
			base = 3 * time.Second
			break
		}
	}
	return base
}

type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string {
	body := e.Body
	if len(body) > 240 {
		body = body[:240] + "..."
	}
	return strconv.Itoa(e.Status) + " " + body
}

// Compile-time guard.
var _ embedder.Embedder = (*ollamaAdapter)(nil)
