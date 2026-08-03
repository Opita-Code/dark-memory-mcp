// Package voyage implements the embedder.Embedder contract against the
// Voyage AI embeddings API (voyage-3 by default, 1024d).
//
// PR-2.1 of the v2.9.0 plan (row 164 §2). Voyage is Anthropic's
// recommended embedding partner, so it's the preferred rung when
// the harness detector identifies Claude Code as the host.
//
// # Behavior
//
//   - Constructor reads VOYAGE_API_KEY (or accepts it via Options).
//   - 5s connect / 10s read per request (row 160 cross-cutting).
//   - One retry on 5xx + 429; never on 4xx (callers see the error).
//   - Context cancellation honored (Hono/HTTP request lifecycle).
//   - Dim() = 1024 for voyage-3; 512 for voyage-3-lite; 1024 for
//     voyage-code-3.
//
// # Fail-safe
//
// New() returns embedder.ErrKeyMissing when VOYAGE_API_KEY is unset
// AND no key is passed via Options. FactoryAuto() catches this and
// falls back to the next rung.
//
// # Trust boundary
//
// Embed() only contacts api.voyageai.com (or the override base).
// It does NOT log the input texts.
package voyage

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

// init registers the Voyage factory with the embedder package.
func init() {
	embedder.RegisterAdapter(KindVoyage, func(_ embedder.Options) (embedder.Embedder, error) {
		return New(Options{})
	})
}

// KindVoyage is the canonical identifier for the Voyage adapter.
const KindVoyage = "voyage"

// Options configures the Voyage adapter.
type Options struct {
	APIKey         string
	BaseURL        string
	Model          string
	Dim            int
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	MaxRetries     int
}

// New constructs the Voyage adapter.
func New(opts Options) (embedder.Embedder, error) {
	apiKey := opts.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("VOYAGE_API_KEY")
	}
	if apiKey == "" {
		return nil, embedder.ErrKeyMissing
	}
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
	return &voyageAdapter{
		apiKey:         apiKey,
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
	if v := os.Getenv("DARK_MEMORY_VOYAGE_BASE"); v != "" {
		return v
	}
	return "https://api.voyageai.com/v1"
}

func defaultModel() string {
	if v := os.Getenv("DARK_MEMORY_VOYAGE_MODEL"); v != "" {
		return v
	}
	return "voyage-3"
}

func dimForModel(m string) int {
	switch m {
	case "voyage-3", "voyage-code-3":
		return 1024
	case "voyage-3-lite":
		return 512
	case "voyage-large-2":
		return 1536
	default:
		return 0
	}
}

type voyageAdapter struct {
	apiKey string
	base   string
	model  string
	dim    int

	connectBudget time.Duration
	readBudget    time.Duration
	maxRetries    int

	clientMu sync.RWMutex
	client   *http.Client
}

// Kind returns KindVoyage.
func (a *voyageAdapter) Kind() string { return KindVoyage }

// Dim returns the configured dimensionality.
func (a *voyageAdapter) Dim() int { return a.dim }

// Embed batches inputs at 100 (matches Voyage's /v1/embeddings
// payload sweet spot; no chunking needed for typical inputs).
func (a *voyageAdapter) Embed(ctx context.Context, texts []string) ([]embedder.Vec, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	const batchSize = 100
	out := make([]embedder.Vec, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vecs, err := a.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (a *voyageAdapter) embedBatch(ctx context.Context, inputs []string) ([]embedder.Vec, error) {
	body, err := json.Marshal(struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}{
		Input: inputs,
		Model: a.model,
	})
	if err != nil {
		return nil, fmt.Errorf("voyage: marshal: %w", err)
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
		req, err := http.NewRequestWithContext(ctx, "POST", a.base+"/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("voyage: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
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
			if resp.StatusCode == 401 {
				return nil, embedder.ErrKeyMissing
			}
			return nil, fmt.Errorf("voyage: %s: %s", resp.Status, lastErr)
		}
		var parsed struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("voyage: decode: %w", err)
		}
		resp.Body.Close()
		out := make([]embedder.Vec, len(inputs))
		for _, d := range parsed.Data {
			if d.Index < 0 || d.Index >= len(out) {
				return nil, fmt.Errorf("voyage: out-of-bounds index %d", d.Index)
			}
			out[d.Index] = embedder.Vec(d.Embedding)
		}
		return out, nil
	}
	return nil, fmt.Errorf("voyage: exhausted %d retries: %w", a.maxRetries, lastErr)
}

// Close releases idle HTTP connections.
func (a *voyageAdapter) Close() error {
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
var _ embedder.Embedder = (*voyageAdapter)(nil)
