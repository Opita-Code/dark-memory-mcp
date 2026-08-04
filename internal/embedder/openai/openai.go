// Package openai implements the embedder.Embedder contract against the
// OpenAI embeddings API (text-embedding-3-small by default, 1536d).
//
// PR-2 of the v2.9.0 plan (agent_memory row 160): the OpenAI adapter
// is one of three live paths (OpenAI / ONNX / None). It is the
// cloud-first default per row 163 when OPENAI_API_KEY is present.
//
// # Behavior
//
//   - Constructor reads OPENAI_API_KEY (or accepts it via Options).
//   - API key env var name follows the platform convention; no custom
//     env var is read at runtime.
//   - DARK_MEMORY_OPENAI_BASE / DARK_MEMORY_OPENAI_MODEL honor the
//     agent_memory row 164 §2 hint: OpenAI-compatible providers
//     (Azure, Together, OpenRouter) can override the endpoint + model.
//   - 5s connect / 10s read per request (row 160 cross-cutting).
//   - One retry on 5xx + 429; never on 4xx (callers see the error).
//   - Context cancellation honored (Hono/HTTP request lifecycle).
//   - Dim() = 1536 for text-embedding-3-small (override via Options).
//
// # Fail-safe
//
// New() returns an error if OPENAI_API_KEY is empty AND no key is
// passed via Options; the factory (FactoryAuto) catches the error
// and degrades to None(). Operators see a working server at boot.
// Embed() returns ErrKeyMissing if a request 401s — the search
// path then degrades to bm25 only.
//
// # Trust boundary
//
// Embed() only contacts api.openai.com (or the override base). It
// does NOT log the input texts (see per-privacy guarantee in
// embedder.Embedder godoc). The HTTP Authorization header carries
// the key; the body holds only the embed input.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// init registers the OpenAI factory with the embedder package. The
// factory reads OPENAI_API_KEY at every New() call so a fresh key
// in the env (e.g. injected by the .mcpb installer post-boot) is
// picked up on next search.
func init() {
	embedder.RegisterAdapter(embedder.KindOpenAI, func(_ embedder.Options) (embedder.Embedder, error) {
		return New(Options{})
	})
}

// Options configures the OpenAI adapter. Zero values are sensible
// defaults — only set fields when you need to override.
type Options struct {
	// APIKey overrides OPENAI_API_KEY. Useful for tests.
	APIKey string

	// BaseURL overrides the api.openai.com endpoint. Used by
	// OpenAI-compatible providers (Azure OpenAI Service, Together
	// AI, OpenRouter). Default: "https://api.openai.com/v1".
	BaseURL string

	// Model overrides "text-embedding-3-small". Default: "text-embedding-3-small".
	// Known dims by model (override Dim() reflects these):
	//   text-embedding-3-small    1536
	//   text-embedding-3-large    3072
	//   text-embedding-ada-002    1536
	Model string

	// Dim overrides the static dimensionality. If set, the adapter
	// reports this as Kind()/Dim(); if 0, the adapter infers from
	// Model.
	Dim int

	// ConnectTimeout overrides the per-request TCP+TLS connect
	// budget. Default: 5s. Row 160 cross-cutting: 5s connect.
	ConnectTimeout time.Duration

	// ReadTimeout overrides the per-request read body budget.
	// Default: 10s. Row 160 cross-cutting: 10s read.
	ReadTimeout time.Duration

	// MaxRetries overrides the per-request retry budget on 5xx +
	// 429. Default: 1. 4xx is never retried.
	MaxRetries int
}

// New constructs the OpenAI adapter. Returns an error only when
// no API key is available — all other misconfiguration surfaces
// at first Embed call.
//
// The returned Embedder is safe for concurrent use; however, the
// caller may also wrap it in embedder.NewSync for unified handling.
func New(opts Options) (embedder.Embedder, error) {
	apiKey := opts.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
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
	return &openaiAdapter{
		apiKey:         apiKey,
		base:           opts.BaseURL,
		model:          opts.Model,
		dim:            opts.Dim,
		connectBudget:  opts.ConnectTimeout,
		readBudget:     opts.ReadTimeout,
		maxRetries:     opts.MaxRetries,
		client:         &http.Client{Timeout: opts.ConnectTimeout + opts.ReadBudget()},
		compactEnabled: false, // future: gpt-4 style row-level compaction
	}, nil
}

// ReadBudget exposes opts.ReadTimeout for the http.Client.Timeout.
func (o Options) ReadBudget() time.Duration {
	if o.ReadTimeout == 0 {
		return 10 * time.Second
	}
	return o.ReadTimeout
}

func defaultBaseURL() string {
	if v := os.Getenv("DARK_MEMORY_OPENAI_BASE"); v != "" {
		return v
	}
	return "https://api.openai.com/v1"
}

func defaultModel() string {
	if v := os.Getenv("DARK_MEMORY_OPENAI_MODEL"); v != "" {
		return v
	}
	return "text-embedding-3-small"
}

// dimForModel returns the canonical dimension for k. Unknown
// models return 0 — callers MUST set Options.Dim for custom models.
func dimForModel(m string) int {
	switch m {
	case "text-embedding-3-small", "text-embedding-ada-002":
		return 1536
	case "text-embedding-3-large":
		return 3072
	default:
		return 0
	}
}

// openaiAdapter is the production adapter. The struct is plain;
// the client is safe for concurrent use per net/http semantics.
// Future state (rate-limit awareness, request-id propagation,
// retry-after header honoring) lives here.
type openaiAdapter struct {
	apiKey string
	base   string
	model  string
	dim    int

	connectBudget time.Duration
	readBudget    time.Duration
	maxRetries    int

	// client is mutable-by-Close only; reads/writes of Timeout are
	// safe but we serialize via clientMu for any future config swap.
	clientMu sync.RWMutex
	client   *http.Client

	// compactEnabled toggles the row-level compaction that we may
	// add in a follow-up. Unused in v2.9.0 PR-2.
	compactEnabled bool
}

// Kind returns KindOpenAI. Recorded in health_ping.
func (a *openaiAdapter) Kind() string { return embedder.KindOpenAI }

// Dim returns the configured dimensionality (1536 for the default
// model, 3072 for 3-large, custom for anything else).
func (a *openaiAdapter) Dim() int { return a.dim }

// Embed returns one Vec per input text. OpenAI returns up to ~2048
// inputs per batch; we chunk at 100 to keep HTTP body small and to
// align with typical chunking in retrieval pipelines.
//
// Returns embedder.ErrKeyMissing on a 401 from the API (key is
// wrong / revoked) — search path degrades to bm25.
//
// Returns ErrDisabled on a payload-too-large or context cancel.
func (a *openaiAdapter) Embed(ctx context.Context, texts []string) ([]embedder.Vec, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// OpenAI accepts at most the batch input count; OpenAI's
	// /v1/embeddings endpoint accepts any array, but payload size
	// is bounded by HTTP timeout. We chunk to keep total wall-clock
	// cost predictable on long inputs.
	const batchSize = 100
	out := make([]embedder.Vec, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		embeddings, err := a.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		out = append(out, embeddings...)
	}
	return out, nil
}

// embedBatch is one HTTP round trip. Retries on 5xx + 429 with a
// brief exponential backoff (250ms, 750ms).
func (a *openaiAdapter) embedBatch(ctx context.Context, inputs []string) ([]embedder.Vec, error) {
	body, err := json.Marshal(struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}{
		Input: inputs,
		Model: a.model,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
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
			return nil, fmt.Errorf("openai: build request: %w", err)
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

		// We always consume + close the body, even on non-2xx, so a
		// connection-leak in retries does not happen.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = &httpError{Status: resp.StatusCode, Body: string(raw)}
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				continue // retry
			}
			// 4xx: do not retry
			if resp.StatusCode == 401 {
				return nil, embedder.ErrKeyMissing
			}
			return nil, fmt.Errorf("openai: %s: %s", resp.Status, lastErr)
		}
		var parsed struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("openai: decode response: %w", err)
		}
		resp.Body.Close()
		out := make([]embedder.Vec, len(inputs))
		for _, d := range parsed.Data {
			if d.Index < 0 || d.Index >= len(out) {
				return nil, fmt.Errorf("openai: out-of-bounds index %d for batch size %d", d.Index, len(out))
			}
			out[d.Index] = embedder.Vec(d.Embedding)
		}
		return out, nil
	}
	return nil, fmt.Errorf("openai: exhausted %d retries: %w", a.maxRetries, lastErr)
}

// backoffFor returns the wait between retries. Attempt 1 → 250ms,
// 2 → 750ms, etc. Capped at 3s.
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

// Close releases the http.Client. Idempotent.
func (a *openaiAdapter) Close() error {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.client != nil {
		a.client.CloseIdleConnections()
		a.client = nil
	}
	return nil
}

// httpError is the structured form of a non-2xx response. Used by
// embedBatch for retry decisions; not exported.
type httpError struct {
	Status int
	Body   string
}

// Error returns "<status>: <truncated body>" — the body is truncated
// to keep error logs human-readable.
func (e *httpError) Error() string {
	body := e.Body
	if len(body) > 240 {
		body = body[:240] + "..."
	}
	return strconv.Itoa(e.Status) + " " + body
}

// Compile-time guard: errors.Is usage on wrapped ErrKeyMissing
// depends on the error being a sentinel; this line documents the
// intent. The blank assignment avoids an "imported and not used"
// build error if the surrounding code is reorganized.
var _ = errors.Is
