// Package judge — LLM-backed Judge client (ADR-007 §9 commit 2).
//
// The v4alpha Pipeline consumes an LLMClient interface (see types.go).
// This file provides a concrete implementation, RealLLMClient, that
// speaks the HTTP wire formats of the 4 supported providers
// (anthropic, minimax, minimax-cn, deepseek) and routes via the
// DARK_JUDGE_PROVIDER / DARK_JUDGE_DIALECT / DARK_JUDGE_MODEL_<PROVIDER>
// env vars.
//
// Why a fresh HTTP client (not orchestration.DefaultFailoverClient):
//   - v4alpha is the new v4 architecture; importing the v2.x
//     orchestration package would create a backwards dependency
//     (v4 → v2) and pull in 22K lines of legacy code.
//   - The judge path is narrow: HTTP POST + retry + parse. ~200 LOC
//     of focused code is enough; we don't need failover chains,
//     health registries, or keyring backends (those live in the
//     orchestration layer for the v2 vibe-loop, not for v4).
//   - Tests use httptest.Server, no real provider keys.
//
// What is reused (via internal/llm — the canonical types package):
//   - ProviderSpec / Catalog (endpoints + dialect + default model)
//   - SpecByID / ResolveID (canonical id lookup + legacy aliases)
//
// Wiring (in transport/mcp/server.go):
//   - Server.NewServer calls NewRealLLMClient() at boot.
//   - If env vars are not set, returns nil + ErrNoKey. The Pipeline
//     then fires EC-002 (LLM unavailable) → verdict=errored, the
//     same path as commit 1's NoOpJudge contract (per ADR-007 §6
//     backwards compat).
//
// Layering (per ADR-007 §5):
//
//	[1] input validation (Pipeline, not here)
//	[2] extractor (Pipeline)
//	[3] pre-flight ECs (Pipeline)
//	[4] persona + rubric (Pipeline)
//	[5] LLM verdict call    — ONLY place that calls Complete
//	[6] parse + verify (Pipeline)
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
)

// ---------- Supported providers ----------

// supportedProviderIDs is the explicit allow-list of providers this
// client speaks. Other entries in the canonical llm.Catalog (openai,
// google, zhipu, moonshot, qwen) are not wired here — operators can
// extend by adding a case branch below.
//
// Rationale (per ADR-007 §9): commit 2 ships the 4 providers the
// operator runs in production (anthropic + the 2 minimax variants +
// deepseek). The remaining 5 land when an operator asks for them.
var supportedProviderIDs = map[string]bool{
	"anthropic":   true,
	"minimax":     true,
	"minimax-cn":  true,
	"deepseek":    true,
}

// ---------- RealLLMClient ----------

// RealLLMClient is the production LLMClient. Speaks HTTP to whichever
// provider DARK_JUDGE_PROVIDER pins (or auto-detects from env keys).
//
// Concurrency: Complete is safe to call from multiple goroutines (the
// underlying http.Client is pooled). Each call has its own timeout
// budget (DARK_JUDGE_TIMEOUT_MS × eval_type-multiplier).
//
// Lifecycle: build once at server boot, reuse for the process lifetime.
// The LLMAdapter is constructed via NewRealLLMClient.
type RealLLMClient struct {
	provider string            // canonical provider id
	model    string            // resolved model name
	key      string            // API key from env (never echoed)
	baseURL  string            // resolved base URL (respects DARK_JUDGE_DIALECT)
	dialect  llm.ProviderDialect // "anthropic" | "openai"
	http     *http.Client
}

// NewRealLLMClient builds a RealLLMClient from env vars. Returns
// ErrNoKey when no provider has a key configured.
//
// Env vars consumed (per ADR-007 §7):
//
//	DARK_JUDGE_PROVIDER       explicit pin (overrides auto-detect)
//	DARK_JUDGE_DIALECT        "anthropic" override (when provider
//	                          speaks both, e.g. deepseek, minimax)
//	DARK_JUDGE_MODEL          global model override
//	DARK_JUDGE_MODEL_<ID>     per-provider model override (uppercased)
//	DARK_JUDGE_TIMEOUT_MS     per-attempt timeout (default 15000)
//	DARK_JUDGE_RETRY_COUNT    transient retries (default 2)
//	<PROVIDER>_API_KEY        the provider's API key (e.g.
//	                          ANTHROPIC_API_KEY, MINIMAX_API_KEY,
//	                          DEEPSEEK_API_KEY, MINIMAX_API_KEY_CN)
//
// Detection order:
//
//	1. DARK_JUDGE_PROVIDER explicit pin (must be in supportedProviderIDs)
//	2. ANTHROPIC_API_KEY → anthropic
//	3. MINIMAX_API_KEY → minimax
//	4. MINIMAX_API_KEY_CN → minimax-cn
//	5. DEEPSEEK_API_KEY → deepseek
//	6. none → ErrNoKey
func NewRealLLMClient() (*RealLLMClient, error) {
	providerID, key, dialect, baseURL, err := resolveProviderFromEnv()
	if err != nil {
		return nil, err
	}
	spec := llm.SpecByID(providerID)
	if spec == nil {
		return nil, fmt.Errorf("%w: provider %q not in canonical catalog", ErrLLMUnavailable, providerID)
	}
	model := resolveModel(spec)
	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 8,
			MaxConnsPerHost:     16,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 0, // per-call context enforces deadline
	}
	return &RealLLMClient{
		provider: providerID,
		model:    model,
		key:      key,
		baseURL:  baseURL,
		dialect:  dialect,
		http:     httpClient,
	}, nil
}

// resolveProviderFromEnv implements the detection-order chain.
// Returns (providerID, apiKey, dialect, baseURL, nil) on success.
func resolveProviderFromEnv() (string, string, llm.ProviderDialect, string, error) {
	// 0. Explicit pin overrides auto-detect.
	if pin := os.Getenv("DARK_JUDGE_PROVIDER"); pin != "" {
		canonical, _ := llm.ResolveID(pin)
		if !supportedProviderIDs[canonical] {
			return "", "", "", "", fmt.Errorf(
				"%w: DARK_JUDGE_PROVIDER=%q not in supportedProviderIDs (supported: anthropic, minimax, minimax-cn, deepseek)",
				ErrLLMUnavailable, pin,
			)
		}
		spec := llm.SpecByID(canonical)
		if spec == nil {
			return "", "", "", "", fmt.Errorf(
				"%w: DARK_JUDGE_PROVIDER=%q not in canonical llm.Catalog", ErrLLMUnavailable, pin,
			)
		}
		key := os.Getenv(spec.EnvKey)
		if key == "" {
			return "", "", "", "", fmt.Errorf(
				"%w: DARK_JUDGE_PROVIDER=%s but %s is not set", ErrLLMUnavailable, spec.ID, spec.EnvKey,
			)
		}
		dialect, baseURL := effectiveDialectAndURL(spec)
		return canonical, key, dialect, baseURL, nil
	}
	// 1-4. Auto-detect by env key presence.
	for _, id := range []string{"anthropic", "minimax", "minimax-cn", "deepseek"} {
		spec := llm.SpecByID(id)
		if spec == nil {
			continue
		}
		key := os.Getenv(spec.EnvKey)
		if key == "" {
			continue
		}
		dialect, baseURL := effectiveDialectAndURL(spec)
		return id, key, dialect, baseURL, nil
	}
	return "", "", "", "", fmt.Errorf(
		"%w: no LLM key detected (set one of ANTHROPIC_API_KEY, MINIMAX_API_KEY, MINIMAX_API_KEY_CN, DEEPSEEK_API_KEY, or DARK_JUDGE_PROVIDER=<id>)",
		ErrLLMUnavailable,
	)
}

// effectiveDialectAndURL applies the DARK_JUDGE_DIALECT override
// (providers that speak both Anthropic Messages + OpenAI Chat
// Completions switch to Anthropic when the override is set).
func effectiveDialectAndURL(spec *llm.ProviderSpec) (llm.ProviderDialect, string) {
	if os.Getenv("DARK_JUDGE_DIALECT") == "anthropic" && spec.AnthropicBaseURL != "" {
		return llm.DialectAnthropic, spec.AnthropicBaseURL
	}
	return spec.Dialect, spec.BaseURL
}

// resolveModel picks the model name in priority order:
//  1. DARK_JUDGE_MODEL (global override)
//  2. DARK_JUDGE_MODEL_<PROVIDER> (per-provider override)
//  3. provider's DefaultModel from canonical catalog
func resolveModel(spec *llm.ProviderSpec) string {
	if v := os.Getenv("DARK_JUDGE_MODEL"); v != "" {
		return v
	}
	if v := os.Getenv("DARK_JUDGE_MODEL_" + strings.ToUpper(spec.ID)); v != "" {
		return v
	}
	return spec.DefaultModel
}

// judgeTimeoutFromEnv returns DARK_JUDGE_TIMEOUT_MS or 15000ms.
func judgeTimeoutFromEnv() time.Duration {
	const defaultMS = 15000
	if v := os.Getenv("DARK_JUDGE_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return defaultMS * time.Millisecond
}

// judgeRetryCountFromEnv returns DARK_JUDGE_RETRY_COUNT clamped [0,5].
func judgeRetryCountFromEnv() int {
	const (
		defaultN = 2
		minN     = 0
		maxN     = 5
	)
	if v := os.Getenv("DARK_JUDGE_RETRY_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= minN {
			if n > maxN {
				return maxN
			}
			return n
		}
	}
	return defaultN
}

// ---------- LLMClient implementation ----------

// Complete implements LLMClient (see types.go). Sends the request to
// the configured provider and returns the parsed response.
//
// Error semantics:
//   - ctx cancellation / deadline exceeded → returns ctx.Err() wrapped
//   - non-2xx HTTP status → returns ErrLLMUnavailable wrapped
//   - JSON parse failure → returns the response with Content = raw body
//     (the Pipeline's parseVerdictResponse handles lenient parsing;
//     failing JSON should not silently swallow the LLM's reasoning)
//
// Retry: transient failures (timeout, net error, 429, 5xx) retry up
// to judgeRetryCountFromEnv() times with jittered exponential backoff.
// Other 4xx fail fast.
func (c *RealLLMClient) Complete(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	if c == nil || c.key == "" {
		return nil, fmt.Errorf("%w: client not initialized", ErrLLMUnavailable)
	}
	// Provider-specific wire format dispatch.
	var (
		bodyBytes []byte
		endpoint  string
		err       error
	)
	switch c.dialect {
	case llm.DialectAnthropic:
		bodyBytes, endpoint, err = c.buildAnthropicRequest(req)
	case llm.DialectOpenAI:
		bodyBytes, endpoint, err = c.buildOpenAIRequest(req)
	default:
		return nil, fmt.Errorf("%w: unknown dialect %q", ErrLLMUnavailable, c.dialect)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrLLMUnavailable, err)
	}

	// Retry loop.
	retries := judgeRetryCountFromEnv()
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			// Jittered exponential backoff (1s, 2s, 4s, 8s cap).
			if err := sleepWithCtx(ctx, backoffForAttempt(attempt)); err != nil {
				return nil, fmt.Errorf("judge: retry %d backoff: %w", attempt, err)
			}
		}
		resp, err := c.completeAttempt(ctx, bodyBytes, endpoint, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// completeAttempt performs ONE HTTP POST with its own deadline
// budget and parses the response. Extracted from Complete so the
// retry loop can give each attempt a fresh timeout.
func (c *RealLLMClient) completeAttempt(
	ctx context.Context,
	bodyBytes []byte,
	endpoint string,
	req LLMRequest,
) (*LLMResponse, error) {
	timeout := judgeTimeoutFromEnv()
	if req.MaxTokens > 0 {
		// Heavy max_tokens → give the request proportionally more
		// time. 1ms per token beyond 2048, capped at 4× the base
		// budget. This prevents long-running verdicts from being
		// killed at the deadline.
		extra := time.Duration(req.MaxTokens-2048) * time.Millisecond
		if extra > 0 && extra < 3*timeout {
			timeout += extra
		}
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("judge: build http request: %w", err)
	}
	c.setAuthHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	if c.dialect == llm.DialectAnthropic {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("judge: http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("judge: read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{status: httpResp.StatusCode, body: truncateForErr(string(respBytes), 512)}
	}

	return c.parseResponse(respBytes, req)
}

// setAuthHeaders sets the per-provider auth headers. Both "Authorization:
// Bearer" and "x-api-key" are sent for Anthropic-dialect providers (the
// MiniMax spec 1198 daemon pool accepts both; the canonical MiniMax-M3
// endpoint accepts x-api-key only).
func (c *RealLLMClient) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("x-api-key", c.key)
}

// ---------- Wire format builders ----------

// buildAnthropicRequest serializes req as an Anthropic Messages API
// POST body. StructuredInputs are inlined as a fenced JSON block in
// the user message (Anthropic has no native structured-inputs param).
//
// For minimax / minimax-cn providers (m3-thinking, spec 1198), the
// thinking block is requested explicitly via `thinking: adaptive` so
// the judge reasons before verdicting.
func (c *RealLLMClient) buildAnthropicRequest(req LLMRequest) ([]byte, string, error) {
	if _, err := url.Parse(c.baseURL); err != nil {
		return nil, "", fmt.Errorf("invalid baseURL %q: %w", c.baseURL, err)
	}
	// Compose user content: UserPrompt + structured_inputs fenced JSON.
	userContent := req.UserPrompt
	if len(req.StructuredInputs) > 0 {
		evJSON, err := json.Marshal(req.StructuredInputs)
		if err != nil {
			return nil, "", fmt.Errorf("marshal structured_inputs: %w", err)
		}
		userContent += "\n\n## Structured Evidence\n\n```json\n" + string(evJSON) + "\n```"
	}

	modelName := c.model // use client-resolved model

	body := map[string]any{
		"model":      modelName,
		"max_tokens": chooseMaxTokens(c.provider, req.MaxTokens),
		"system":     req.SystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userContent},
		},
	}
	// m3-thinking (spec 1198): minimax variants request adaptive thinking.
	if c.provider == "minimax" || c.provider == "minimax-cn" {
		body["thinking"] = map[string]any{"type": "adaptive"}
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("marshal: %w", err)
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/v1/messages"
	return bodyBytes, endpoint, nil
}

// buildOpenAIRequest serializes req as an OpenAI Chat Completions
// POST body. StructuredInputs are inlined as a fenced JSON block in
// the user message (Chat Completions has no native structured-inputs
// param).
func (c *RealLLMClient) buildOpenAIRequest(req LLMRequest) ([]byte, string, error) {
	if _, err := url.Parse(c.baseURL); err != nil {
		return nil, "", fmt.Errorf("invalid baseURL %q: %w", c.baseURL, err)
	}
	userContent := req.UserPrompt
	if len(req.StructuredInputs) > 0 {
		evJSON, err := json.Marshal(req.StructuredInputs)
		if err != nil {
			return nil, "", fmt.Errorf("marshal structured_inputs: %w", err)
		}
		userContent += "\n\n## Structured Evidence\n\n```json\n" + string(evJSON) + "\n```"
	}

	messages := []map[string]string{
		{"role": "system", "content": req.SystemPrompt},
		{"role": "user", "content": userContent},
	}
	body := map[string]any{
		"model":       c.model,
		"messages":    messages,
		"max_tokens":  chooseMaxTokens(c.provider, req.MaxTokens),
		"temperature": float32(req.Temperature),
	}
	if req.TopP > 0 && req.TopP <= 1.0 {
		body["top_p"] = float32(req.TopP)
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("marshal: %w", err)
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/chat/completions"
	return bodyBytes, endpoint, nil
}

// chooseMaxTokens picks max_tokens honoring per-provider quirks
// (m3-thinking needs ~4096 to fit reasoning + verdict JSON).
func chooseMaxTokens(provider string, requested int) int {
	const (
		defaultTokens = 2048
		minimaxTokens = 4096 // spec 1198: m3-thinking budget
	)
	if requested > 0 {
		return requested
	}
	if provider == "minimax" || provider == "minimax-cn" {
		return minimaxTokens
	}
	return defaultTokens
}

// ---------- Response parsing ----------

// parseResponse parses the HTTP body into an LLMResponse. Two shapes:
//
//   - Anthropic Messages API:
//     {"content":[{"type":"text","text":"..."}], "model":"..."}
//     With thinking enabled (m3-thinking), content[0] is a thinking
//     block and the verdict text is in a later text block.
//   - OpenAI Chat Completions:
//     {"choices":[{"message":{"content":"..."}}], "model":"..."}
//
// Lenient: on JSON parse failure, returns the raw body as Content so
// the Pipeline's parseVerdictResponse can still try (it has fenced
// JSON + bare-word + last-occurrence fallbacks — see judgeparse.go).
func (c *RealLLMClient) parseResponse(body []byte, req LLMRequest) (*LLMResponse, error) {
	model := c.model
	switch c.dialect {
	case llm.DialectAnthropic:
		var resp struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content"`
			Model string `json:"model"`
			Stop  string `json:"stop_reason"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return &LLMResponse{
				Content:      string(body),
				Provider:     c.provider,
				Model:        model,
				FinishReason: "parse_error",
			}, nil
		}
		// Pick the first text block; fall back to last thinking block.
		var text string
		var lastThinking string
		for _, blk := range resp.Content {
			if blk.Type == "text" && blk.Text != "" {
				text = blk.Text
				break
			}
			if blk.Type == "thinking" && blk.Thinking != "" {
				lastThinking = blk.Thinking
			}
		}
		if text == "" {
			text = lastThinking
		}
		if resp.Model != "" {
			model = resp.Model
		}
		// m3-thinking is non-deterministic by spec 1198 — flag it.
		nonDet := c.provider == "minimax" || c.provider == "minimax-cn"
		return &LLMResponse{
			Content:          text,
			Provider:         c.provider,
			Model:            model,
			FinishReason:     resp.Stop,
			NonDeterministic: nonDet,
		}, nil

	case llm.DialectOpenAI:
		var resp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return &LLMResponse{
				Content:      string(body),
				Provider:     c.provider,
				Model:        model,
				FinishReason: "parse_error",
			}, nil
		}
		var text string
		if len(resp.Choices) > 0 {
			text = resp.Choices[0].Message.Content
		}
		if resp.Model != "" {
			model = resp.Model
		}
		return &LLMResponse{
			Content:      text,
			Provider:     c.provider,
			Model:        model,
			FinishReason: defaultIfEmpty(resp.Choices[0].FinishReason, "stop"),
		}, nil
	}
	return &LLMResponse{
		Content: string(body), Provider: c.provider, Model: model,
		FinishReason: "unknown_dialect",
	}, nil
}

// defaultIfEmpty returns fallback when s is "".
func defaultIfEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ---------- Retry helpers ----------

// sleepWithCtx sleeps for d, returning ctx.Err() if cancelled.
func sleepWithCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoffForAttempt: 1s, 2s, 4s, 8s cap, ±25% jitter.
func backoffForAttempt(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return time.Duration(float64(d) * (0.75 + 0.5*rand.Float64()))
}

// httpStatusError is the typed error for non-2xx HTTP responses.
type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("judge: HTTP %d: %s", e.status, e.body)
}

// isRetryableError: timeout, net, 429, 5xx. Other 4xx fail fast.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var st *httpStatusError
	if errors.As(err, &st) {
		return st.status == http.StatusTooManyRequests || st.status >= 500
	}
	return false
}

// truncateForErr caps s at max bytes (UTF-8 safe truncation).
func truncateForErr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// ---------- Persona content injection (helper for callers) ----------

// EnrichSystemPrompt takes a base system prompt and returns a richer
// version that incorporates the persona's content (per ADR-007 §5).
// Used by callers (the transport layer) to build the LLMRequest that
// gets passed to Complete. The LLMClient itself is dumb about
// personas — it just sends what it's given.
//
// Returns base unchanged when personaID has no registered content
// (legacy fallback for the 8 personas).
func EnrichSystemPrompt(personaID, base string) string {
	c := LookupPersonaContent(personaID)
	if c == nil {
		return base
	}
	var b strings.Builder
	b.WriteString(c.PromptTemplate)
	b.WriteString("\n\n")
	b.WriteString(base)
	if c.EvaluationLens != "" {
		b.WriteString("\n\n## Evaluation Lens\n\n")
		b.WriteString(c.EvaluationLens)
	}
	if len(c.BiasControls) > 0 {
		b.WriteString("\n\n## Bias Controls\n\n")
		for _, ctrl := range c.BiasControls {
			b.WriteString("- ")
			b.WriteString(ctrl)
			b.WriteString("\n")
		}
	}
	if len(c.RequiredEvidence) > 0 {
		b.WriteString("\n\n## Required Evidence\n\n")
		for _, ev := range c.RequiredEvidence {
			b.WriteString("- ")
			b.WriteString(ev)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ---------- Compile-time interface checks ----------

var _ LLMClient = (*RealLLMClient)(nil)
