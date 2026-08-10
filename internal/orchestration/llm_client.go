// LLMClient is one LLM-as-judge endpoint. Three implementations:
//
//   - SelfHarnessClient: detects the harness's own LLM via env vars
//     (ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, ...). This is
//     the default: the same model that called the MCP tool acts as
//     judge. Self-judge is biased but zero-config.
//   - DriftJudgeDaemonClient: uses the [drift-judge-daemon] virtual-key pool,
//     rotating across providers for cost/quality optimisation. Used
//     when the orchestrator's OSINT selector says "this eval_type is
//     better served by a different model".
//   - MockLLMClient: deterministic canned verdicts for tests.
//
// All three implement the same LLMClient interface so the Judge
// orchestrator doesn't care which is in use.
package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Judge call budget configuration (v2.11.0 update):
//
//   - DARK_JUDGE_TIMEOUT_MS — base per-attempt timeout in
//     milliseconds (default 120000 = 2min; was a hardcoded 60s for
//     every eval_type). The per-attempt timeout is base × eval_type
//     multiplier (see judgeTimeoutForEval), so a heavy drift_judge
//     gets more headroom than a quick pii_detect.
//   - DARK_JUDGE_RETRY_COUNT — transient-failure retries per call
//     (default 2 → up to 3 attempts total; clamped [0, 5]).
//     Retried: timeout, net errors, HTTP 429, HTTP 5xx. Not
//     retried: 4xx (except 429), URL/marshal errors.
//
// Both are read at serve time (per call) so operators can tune
// without a restart.
const (
	defaultJudgeTimeoutMS  = 120000
	defaultJudgeRetryCount = 2
	maxJudgeRetryCount     = 5
)

// judgeBaseTimeout returns the base per-attempt timeout from
// DARK_JUDGE_TIMEOUT_MS (default 2min).
func judgeBaseTimeout() time.Duration {
	ms := defaultJudgeTimeoutMS
	if v := os.Getenv("DARK_JUDGE_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// judgeTimeoutMultipliers scales the base timeout per eval_type.
// Reasoning-heavy eval_types get more headroom; pattern-matching
// eval_types fail fast instead of hanging.
var judgeTimeoutMultipliers = map[string]float64{
	"drift_judge":           1.5,
	"compliance_check":      1.0,
	"brand_match":           0.8,
	"grounding_check":       1.2,
	"pii_detect":            0.5,
	"prompt_injection_scan": 0.5,
	"mindset_compose":       1.5,
	"mindset_quality":       1.0,
}

// judgeTimeoutForEval is the per-attempt timeout for one eval_type.
func judgeTimeoutForEval(evalType string) time.Duration {
	m := judgeTimeoutMultipliers[evalType]
	if m <= 0 {
		m = 1.0
	}
	return time.Duration(float64(judgeBaseTimeout()) * m)
}

// judgeRetryCount returns the transient-failure retry budget from
// DARK_JUDGE_RETRY_COUNT (default 2, clamped [0, 5]).
func judgeRetryCount() int {
	n := defaultJudgeRetryCount
	if v := os.Getenv("DARK_JUDGE_RETRY_COUNT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 0 {
			n = p
		}
	}
	if n > maxJudgeRetryCount {
		n = maxJudgeRetryCount
	}
	return n
}

// backoffForAttempt returns the sleep before retry attempt N
// (attempt >= 1): 1s, 2s, 4s, ... capped at 8s, with ±25% jitter so
// concurrent retries don't thundering-herd the endpoint.
func backoffForAttempt(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return time.Duration(float64(d) * (0.75 + 0.5*rand.Float64()))
}

// sleepWithContext sleeps for d, or returns ctx.Err() if the context
// is done first (abortable backoff — the harness can cancel a retry
// storm).
func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// judgeHTTPStatusError carries a non-200 HTTP response so the retry
// machinery can classify it (429 + 5xx retryable; other 4xx fail
// fast). Error() keeps the historical "judge: HTTP N: body" shape.
type judgeHTTPStatusError struct {
	status int
	body   string
}

func (e *judgeHTTPStatusError) Error() string {
	return fmt.Sprintf("judge: HTTP %d: %s", e.status, e.body)
}

// isRetryableError reports whether a judge call failure is transient
// enough to retry: deadline exceeded, net timeouts, HTTP 429, or
// HTTP 5xx. Everything else (4xx, URL errors, marshal errors) fails
// fast.
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
	var st *judgeHTTPStatusError
	if errors.As(err, &st) {
		return st.status == http.StatusTooManyRequests || st.status >= 500
	}
	return false
}

// judgeHTTPClient is the shared HTTP client for all judge calls. No
// Timeout on the client — the per-attempt deadline comes from the
// per-attempt context (judgeTimeoutForEval), so every retry gets a
// fresh budget. The transport is pooled across eval_types and
// baseURLs (was a fresh &http.Client per call).
var judgeHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 8,
		MaxConnsPerHost:     16,
		IdleConnTimeout:     90 * time.Second,
	},
}

// JudgeRequest is the structured input to a Judge call. The
// orchestrator fills it from the JudgeInput plus a per-eval_type
// system prompt template. Model is a hint from the OSINT selector
// (recommended for the eval_type); clients may ignore or use it.
type JudgeRequest struct {
	EvalType     string `json:"eval_type"`               // brand_match | compliance_check | drift_judge | grounding_check | pii_detect | prompt_injection_scan | consensus
	Content      string `json:"content"`                 // the text to evaluate
	TargetType   string `json:"target_type"`             // brand | artifact | spec | claim | code | ...
	TargetID     string `json:"target_id"`               // brand_id | artifact_id | ...
	Model        string `json:"model,omitempty"`         // recommended by OSINTSelector
	SystemPrompt string `json:"system_prompt,omitempty"` // optional override
	// VibeCase (v2.12.0) is the vibe-flow case of the artifact
	// (C1=code, ..., C7=mixed). Empty = legacy generic prompt.
	VibeCase string `json:"vibe_case,omitempty"`
}

// JudgeResponse is the LLM's verdict.
type JudgeResponse struct {
	VerdictJSON string  `json:"verdict_json"` // JSON-encoded per eval_type schema
	Confidence  float32 `json:"confidence"`   // 0..1
	Model       string  `json:"model"`        // which model answered (e.g. "claude-opus-4-7", "gpt-5")
	Provider    string  `json:"provider"`     // anthropic | openai | google | ...
}

// LLMClient is one judge endpoint.
type LLMClient interface {
	// Name returns a stable identifier (e.g. "self_harness_anthropic",
	// "mock_v1").
	Name() string
	// Judge performs one LLM-as-judge call.
	Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error)
}

// ErrNoLLMAvailable is returned when no LLM key is detected AND no
// fallback was configured. The orchestrator wraps this with a
// user-facing hint.
var ErrNoLLMAvailable = errors.New("no LLM available: harness has no API key (set ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or DARK_DRIFT_JUDGE_DAEMON_URL), and no fallback was configured")

// SelfHarnessClient delegates Judge calls to an LLM. Two supported
// modes in this build:
//
//   - provider = "drift_judge_daemon": POST to DARK_DRIFT_JUDGE_DAEMON_URL/v1/messages
//     with Bearer ds-managed (sentinel auth). Wave 3.5 wiring of the
//     [drift-judge-daemon] integration; source previously deferred this
//     to Wave 4+. Works against either the real [drift-judge-daemon]
//     at :8901 (when pool is non-empty) or the deterministic mock-llm
//     at :9000 (dev iteration).
//
//   - provider in the catalog (anthropic | openai | google | deepseek |
//     minimax | zhipu | moonshot | qwen): direct HTTP via the catalog's
//     dialect. Anthropic-dialect providers POST /v1/messages with
//     x-api-key + anthropic-version; OpenAI-dialect providers POST
//     /chat/completions with Authorization: Bearer. v2.13.0: the
//     catalog (provider_catalog.go) is the single source of endpoints,
//     verified against primary docs on 2026-08-10 (row 587).
//
// Self-judge pattern: the same model that called the MCP tool acts as judge.
//
// Detection order (first match wins):
//
//  1. DARK_JUDGE_PROVIDER  ÔåÆ explicit operator pick (any catalog id)
//  2. ANTHROPIC_API_KEY    ÔåÆ anthropic (Anthropic Messages)
//  3. OPENAI_API_KEY       ÔåÆ openai (Chat Completions)
//  4. GEMINI_API_KEY       ÔåÆ google (OpenAI-compat)
//  5. DEEPSEEK_API_KEY     ÔåÆ deepseek (OpenAI + /anthropic)
//  6. MINIMAX_API_KEY      ÔåÆ minimax (OpenAI + /anthropic)
//  7. MOONSHOT_API_KEY     ÔåÆ moonshot (OpenAI)
//  8. ZAI_API_KEY          ÔåÆ zhipu (OpenAI)
//  9. DASHSCOPE_API_KEY    ÔåÆ qwen (OpenAI + /anthropic)
// 10. DARK_DRIFT_JUDGE_DAEMON_URL  ÔåÆ [drift-judge-daemon] pool
// 11. legacy DARK_SCRAPPER_URL     ÔåÆ [drift-judge-daemon] (deprecated)
// 12. none                ÔåÆ ErrNoLLMAvailable
//
// The model is auto-picked via the OSINTSelector for the eval_type
// (config-based today, real OSINT later ÔÇö see spec 173 O5).
type SelfHarnessClient struct {
	provider string
	model    string
	key      string // API key or DARK_DRIFT_JUDGE_DAEMON_URL
	dialect  ProviderDialect
	baseURL  string
}

// NewSelfHarnessClient detects the available LLM via env vars.
// Returns nil + ErrNoLLMAvailable if nothing is set.
func NewSelfHarnessClient() (*SelfHarnessClient, error) {
	// 0. Explicit operator override: DARK_JUDGE_PROVIDER pins a
	// catalog provider regardless of which keys are present.
	if override := os.Getenv("DARK_JUDGE_PROVIDER"); override != "" {
		spec := providerSpecByID(override)
		if spec == nil {
			return nil, fmt.Errorf("%w: DARK_JUDGE_PROVIDER=%q is not in the provider catalog (supported: %s)",
				ErrNoLLMAvailable, override, strings.Join(catalogProviderIDs(), ", "))
		}
		key := os.Getenv(spec.EnvKey)
		if key == "" {
			return nil, fmt.Errorf("%w: DARK_JUDGE_PROVIDER=%s but %s is not set", ErrNoLLMAvailable, spec.ID, spec.EnvKey)
		}
		return newCatalogClient(spec, key), nil
	}

	// 1-9. Auto-detection: first catalog provider whose key is set wins.
	for _, spec := range providerCatalog {
		if key := os.Getenv(spec.EnvKey); key != "" {
			return newCatalogClient(&spec, key), nil
		}
	}
	// 10. [drift-judge-daemon] sentinel pool.
	if url := os.Getenv("DARK_DRIFT_JUDGE_DAEMON_URL"); url != "" {
		return &SelfHarnessClient{
			provider: "drift_judge_daemon",
			model:    os.Getenv("DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON"),
			key:      url,
			dialect:  DialectAnthropic,
			baseURL:  url,
		}, nil
	}
	// 11. v2.0.1 (follow-up 3 of 3): legacy-env shim for operators who
	// haven't migrated their `.env` from the v1.x env name. When
	// DARK_SCRAPPER_URL is set AND DARK_DRIFT_JUDGE_DAEMON_URL is not,
	// fall through to the legacy value and log a one-line notice at
	// startup so the operator sees the deprecation in their boot log
	// without the runtime path having to do the lookup twice.
	if legacyURL := os.Getenv("DARK_SCRAPPER_URL"); legacyURL != "" {
		log.Printf("dark-mem-mcp: DARK_SCRAPPER_URL is set but DARK_DRIFT_JUDGE_DAEMON_URL is not — using the legacy env (deprecated, will be removed in v2.1.0). Run `sed -i 's/DARK_SCRAPPER_URL/DARK_DRIFT_JUDGE_DAEMON_URL/g' .env` to migrate.")
		return &SelfHarnessClient{
			provider: "drift_judge_daemon",
			model:    os.Getenv("DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON"), // new name; legacy env had no model override
			key:      legacyURL,
			dialect:  DialectAnthropic,
			baseURL:  legacyURL,
		}, nil
	}
	return nil, ErrNoLLMAvailable
}

// newCatalogClient builds a SelfHarnessClient for a catalog provider,
// honoring DARK_JUDGE_MODEL_<PROVIDER> overrides and the optional
// DARK_JUDGE_DIALECT=anthropic switch for providers that speak both
// dialects.
func newCatalogClient(spec *ProviderSpec, key string) *SelfHarnessClient {
	dialect := providerDialect(spec)
	baseURL := spec.BaseURL
	if dialect == DialectAnthropic && spec.AnthropicBaseURL != "" {
		baseURL = spec.AnthropicBaseURL
	}
	model := os.Getenv("DARK_JUDGE_MODEL_" + strings.ToUpper(spec.ID))
	if model == "" {
		model = spec.DefaultModel
	}
	return &SelfHarnessClient{
		provider: spec.ID,
		model:    model,
		key:      key,
		dialect:  dialect,
		baseURL:  baseURL,
	}
}

// Name implements LLMClient.
func (s *SelfHarnessClient) Name() string {
	if s == nil {
		return "self_harness_unconfigured"
	}
	return "self_harness_" + s.provider
}

// Judge implements LLMClient.
//
// v2.13.0 dispatch (provider_catalog.go is the endpoint source):
//   - dialect anthropic → POST {base}/v1/messages (Anthropic Messages)
//   - dialect openai    → POST {base}/chat/completions (Chat Completions)
//   - provider drift_judge_daemon → sentinel-auth daemon pool (preserved)
//
// The drift_judge_daemon path is preserved verbatim for
// [drift-judge-daemon] compatibility. Wire format is identical
// (Anthropic Messages API), so verify-llm-pipeline.ps1 stage 2/4
// regression suite continues to apply.
func (s *SelfHarnessClient) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	if s == nil || s.provider == "" {
		return nil, ErrNoLLMAvailable
	}
	if s.provider == "drift_judge_daemon" {
		return s.judgeViaDriftJudgeDaemon(ctx, req)
	}
	switch s.dialect {
	case DialectOpenAI:
		return s.judgeViaOpenAIHTTP(ctx, req)
	case DialectAnthropic:
		return s.judgeViaHTTP(ctx, req, s.baseURL, s.key)
	default:
		return nil, fmt.Errorf("%w: self_harness provider=%s has no dialect (provider catalog misconfigured)",
			ErrNoLLMAvailable, s.provider)
	}
}

// judgeViaDriftJudgeDaemon is the [drift-judge-daemon] HTTP path. It posts to
// DARK_DRIFT_JUDGE_DAEMON_URL/v1/messages with sentinel auth ("ds-managed" in both
// Authorization and x-api-key headers) so the daemon's pool router can
// attribute the request to the harness without leaking the real key.
//
// Kept verbatim for the legacy harness. New code paths (provider=anthropic
// + SDD_LLM_BASE_URL) call judgeViaHTTP directly with the real key.
func (s *SelfHarnessClient) judgeViaDriftJudgeDaemon(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	if s.key == "" {
		return nil, fmt.Errorf("%w: drift-judge-daemon URL is empty (set DARK_DRIFT_JUDGE_DAEMON_URL)", ErrNoLLMAvailable)
	}
	return s.judgeViaHTTP(ctx, req, s.key, "ds-managed")
}

// judgeViaHTTP performs one Anthropic-format POST and parses the response.
// It is the generic worker for both the drift-judge-daemon sentinel path and the
// Wave 4 anthropic-direct path; the only differences between callers are the
// baseURL and the authValue that goes into both `Authorization: Bearer` and
// `x-api-key` headers.
//
// Request shape (Anthropic Messages API):
//
//	{
//	  "model": "...",
//	  "max_tokens": 1024,
//	  "system": "...",
//	  "messages": [{"role":"user","content":"..."}]
//	}
//
// Response parsing is lenient: accepts both Anthropic Messages API format
// (content[0].text) and the minimal mock-llm format ({text: "..."}). Any
// non-JSON response is wrapped verbatim as the verdict text so the
// orchestrator's downstream parsing can decide.
//
// Safety: rejects URLs without an http(s) scheme, host, or with non-loopback
// hosts in safety-strict mode. Does NOT block loopback (operator runs the
// daemon locally by design).
func (s *SelfHarnessClient) judgeViaHTTP(ctx context.Context, req JudgeRequest, baseURL, authValue string) (*JudgeResponse, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("judge: baseURL is empty")
	}

	// URL validation: must parse, must be http(s), must have a host.
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("judge: invalid baseURL %q: %w", baseURL, err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("judge: baseURL must be http(s); got %q", endpoint.Scheme)
	}
	if endpoint.Host == "" {
		return nil, fmt.Errorf("judge: baseURL %q has no host", baseURL)
	}

	// Resolve model. Caller hint > client config (catalog default or
	// DARK_JUDGE_MODEL_<PROVIDER> override). No hardcoded fallback:
	// if no model is set, the provider's own server-side default is
	// used (the field is omitted from the body).
	model := req.Model
	if model == "" {
		model = s.model
	}

	// Resolve system prompt. Caller override > per-eval_type default
	// > vibe-case rubric (v2.12.0).
	system := req.SystemPrompt
	if system == "" {
		system = defaultSystemForEval(req.EvalType)
		if req.VibeCase != "" {
			if rubric := rubricPromptFor(req.VibeCase, req.EvalType); rubric != "" {
				system += "\n\n" + rubric
			}
		}
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": 1024,
		"system":     system,
		"messages": []map[string]string{
			{"role": "user", "content": req.Content},
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("judge: marshal request: %w", err)
	}

	endpointStr := strings.TrimRight(baseURL, "/") + "/v1/messages"

	// Retry loop (v2.11.0): transient failures (timeout, net, 429,
	// 5xx) retry up to judgeRetryCount() times with jittered
	// exponential backoff. Each attempt gets its own fresh timeout
	// budget via judgeViaHTTPAttempt. Non-retryable errors fail
	// fast.
	retries := judgeRetryCount()
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if err := sleepWithContext(ctx, backoffForAttempt(attempt)); err != nil {
				return nil, fmt.Errorf("judge: retry %d backoff: %w", attempt, err)
			}
		}
		resp, err := s.judgeViaHTTPAttempt(ctx, bodyBytes, endpointStr, authValue, judgeTimeoutForEval(req.EvalType), model)
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

// judgeViaHTTPAttempt performs ONE POST with its own timeout budget
// and parses the response. Extracted from judgeViaHTTP so the retry
// loop can give every attempt a fresh deadline.
func (s *SelfHarnessClient) judgeViaHTTPAttempt(ctx context.Context, bodyBytes []byte, endpointStr, authValue string, timeout time.Duration, model string) (*JudgeResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpointStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("judge: build http request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+authValue)
	httpReq.Header.Set("x-api-key", authValue)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := judgeHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("judge: http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("judge: read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		// 503 with empty pool is a legitimate daemon signal: pool is
		// drained, harvest cycle will refill. Surface the message.
		return nil, &judgeHTTPStatusError{status: httpResp.StatusCode, body: truncateForErr(string(respBytes), 512)}
	}

	// Parse response. Three shapes:
	//   1. Anthropic Messages API: {"content":[{"type":"text","text":"..."}], ...}
	//   2. mock-llm minimal:        {"text":"..."}
	//   3. fallback:                raw text wrapped as verdict
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Model string `json:"model"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return &JudgeResponse{
			VerdictJSON: string(respBytes),
			Confidence:  0.7,
			Model:       model,
			Provider:    s.provider,
		}, nil
	}

	var text string
	switch {
	case len(resp.Content) > 0:
		text = resp.Content[0].Text
	case resp.Text != "":
		text = resp.Text
	}

	if resp.Model != "" {
		model = resp.Model
	}

	// Best-effort confidence extraction from verdict JSON.
	confidence := float32(0.7)
	if c := extractConfidence(text); c > 0 {
		confidence = c
	}

	return &JudgeResponse{
		VerdictJSON: text,
		Confidence:  confidence,
		Model:       model,
		Provider:    s.provider,
	}, nil
}

// judgeViaOpenAIHTTP performs one OpenAI Chat Completions call for
// OpenAI-dialect providers (openai, deepseek, minimax-v1, zhipu,
// moonshot, qwen, google). It mirrors judgeViaHTTP's retry/backoff/
// timeout machinery; the only differences are the request shape
// (chat/completions, messages[] with role/content, no system field —
// the system prompt is sent as the first system-role message) and the
// response parsing (choices[0].message.content).
func (s *SelfHarnessClient) judgeViaOpenAIHTTP(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	if s == nil || s.provider == "" {
		return nil, ErrNoLLMAvailable
	}
	if s.baseURL == "" {
		return nil, fmt.Errorf("%w: provider=%s has empty baseURL (provider catalog misconfigured)", ErrNoLLMAvailable, s.provider)
	}
	endpoint, err := url.Parse(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("judge: invalid baseURL %q: %w", s.baseURL, err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("judge: baseURL must be http(s); got %q", endpoint.Scheme)
	}
	if endpoint.Host == "" {
		return nil, fmt.Errorf("judge: baseURL %q has no host", s.baseURL)
	}

	// Resolve model. Caller hint > client config (catalog default or
	// DARK_JUDGE_MODEL_<PROVIDER>). No hardcoded fallback — omit the
	// field when unset so the provider uses its server-side default.
	model := req.Model
	if model == "" {
		model = s.model
	}

	// Resolve system prompt. Same per-eval_type defaults + vibe-case
	// rubrics as the Anthropic path.
	system := req.SystemPrompt
	if system == "" {
		system = defaultSystemForEval(req.EvalType)
		if req.VibeCase != "" {
			if rubric := rubricPromptFor(req.VibeCase, req.EvalType); rubric != "" {
				system += "\n\n" + rubric
			}
		}
	}

	// Chat Completions shape. The system prompt goes in as the first
	// system-role message (OpenAI dialect has no top-level "system"
	// field).
	messages := []map[string]string{{"role": "user", "content": req.Content}}
	if system != "" {
		messages = append([]map[string]string{{"role": "system", "content": system}}, messages...)
	}
	body := map[string]any{
		"messages": messages,
	}
	if model != "" {
		body["model"] = model
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("judge: marshal request: %w", err)
	}

	endpointStr := strings.TrimRight(s.baseURL, "/") + "/chat/completions"

	retries := judgeRetryCount()
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if err := sleepWithContext(ctx, backoffForAttempt(attempt)); err != nil {
				return nil, fmt.Errorf("judge: retry %d backoff: %w", attempt, err)
			}
		}
		resp, err := s.judgeViaOpenAIHTTPAttempt(ctx, bodyBytes, endpointStr, judgeTimeoutForEval(req.EvalType), model)
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

// judgeViaOpenAIHTTPAttempt performs ONE Chat Completions POST with
// its own timeout budget and parses choices[0].message.content.
func (s *SelfHarnessClient) judgeViaOpenAIHTTPAttempt(ctx context.Context, bodyBytes []byte, endpointStr string, timeout time.Duration, model string) (*JudgeResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpointStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("judge: build http request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+s.key)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := judgeHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("judge: http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("judge: read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, &judgeHTTPStatusError{status: httpResp.StatusCode, body: truncateForErr(string(respBytes), 512)}
	}

	// Parse Chat Completions: {"choices":[{"message":{"content":"..."}}],"model":"..."}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"` // legacy: some OpenAI-compat servers use choices[].text
		} `json:"choices"`
		Model string `json:"model"`
		Text  string `json:"text"` // minimal mock shape
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return &JudgeResponse{
			VerdictJSON: string(respBytes),
			Confidence:  0.7,
			Model:       model,
			Provider:    s.provider,
		}, nil
	}

	var text string
	if len(resp.Choices) > 0 {
		if resp.Choices[0].Message.Content != "" {
			text = resp.Choices[0].Message.Content
		} else {
			text = resp.Choices[0].Text
		}
	}
	if text == "" {
		text = resp.Text
	}

	if resp.Model != "" {
		model = resp.Model
	}

	confidence := float32(0.7)
	if c := extractConfidence(text); c > 0 {
		confidence = c
	}

	return &JudgeResponse{
		VerdictJSON: text,
		Confidence:  confidence,
		Model:       model,
		Provider:    s.provider,
	}, nil
}

// defaultSystemForEval returns a system prompt tailored to each eval_type.
// Keeps the LLM in JSON-output mode so downstream parsing is predictable.
func defaultSystemForEval(evalType string) string {
	const base = "You are an LLM-as-judge. Reply ONLY with JSON. No prose, no markdown fences."
	switch evalType {
	case "drift_judge":
		return base + ` Schema: {"verdict":"aligned"|"drift_detected"|"needs_human","confidence":0.0-1.0,"reasoning":"..."}`
	case "brand_match":
		return base + ` Schema: {"verdict":"match"|"drift_detected","score":0.0-1.0,"issues":[...]}`
	case "compliance_check":
		return base + ` Schema: {"verdict":"compliant"|"non_compliant","issues":[...],"required_disclosures":[...]}`
	case "pii_detect":
		return base + ` Schema: {"pii_found":true|false,"items":[{"kind":"email|phone|ip|...","value":"...","span":[start,end]}]}`
	case "prompt_injection_scan":
		return base + ` Schema: {"injection_found":true|false,"evidence":"..."}`
	case "grounding_check":
		return base + ` Schema: {"grounded":true|false,"confidence":0.0-1.0,"evidence_quote":"..."}`
	case "mindset_compose":
		// v2.7.0-alpha: GENERATIVE call — the LLM synthesizes a subagent
		// system_prompt given vibe_case + task_description. This is NOT a
		// verdict in the usual sense; the "verdict" slot carries the
		// proposed system_prompt as JSON.
		return "You are a meta-system-prompt engineer. Produce ONLY JSON. No prose, no markdown fences. " +
			`Output schema: {"role":"...","goal":"...","backstory":"...","constraints":["don't...","don't...","don't..."],"tools_recommended":["Read","Grep",...],"model_recommended":"sonnet"}`
	case "mindset_quality":
		// v2.7.0-alpha: VALIDATIVE call — judge whether a proposed
		// subagent system_prompt is well-formed against 5 pass criteria.
		return base + ` Schema: {"verdict":"aligned"|"drift_detected"|"needs_human","confidence":0.0-1.0,"reasoning":"...","criteria_failed":["OVER_QUALIFIED"|"TASK_APPROPRIATE"|"CONSTRAINT_PRIMED"|"MINIMAL_TOOLS"|"NO_LEAKAGE"]}`
	default:
		return base + ` Schema: {"verdict":"...","confidence":0.0-1.0}`
	}
}

// extractConfidence looks for a "confidence" field in a JSON verdict
// blob. Returns 0 if not found or not parseable.
func extractConfidence(verdictJSON string) float32 {
	var v struct {
		Confidence float32 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(verdictJSON), &v); err != nil {
		return 0
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return 0
	}
	return v.Confidence
}

// rubricPromptFor (v2.12.0) returns a G-Eval-style rubric appendix for a
// (vibe_case, eval_type) pair, or "" when no rubric applies. When non-empty,
// the caller appends it to the default system prompt so the LLM evaluates
// against an explicit checklist and produces the verdict as the conclusion
// of that checklist (research-informed: G-Eval, self-consistency, JudgeBench;
// see vibe-flow/main/JUDGE_RUBRICS.md + agent_memory row 497).
//
// vibe_case taxonomy: C1=code, C2=text, C3=image, C4=video, C5=audio,
// C6=multimodal, C7=mixed (mindset_meta_prompts.go).
func rubricPromptFor(vibeCase, evalType string) string {
	// Empty vibe_case = legacy behavior (no rubric). This is the
	// retrocompat contract: callers that don't pass vibe_case get
	// exactly the pre-v2.12.0 prompt.
	if vibeCase == "" {
		return ""
	}
	var criteria []string
	switch vibeCase {
	case "C1": // code — technical rubric (JudgeBench / arxiv 2508.14419)
		criteria = []string{
			"CORRECTNESS: does the code compile and behave as the spec requires? Check missing functions, wrong return types, off-by-one errors, wrong control flow. Quote exact lines.",
			"SECURITY: objectively verify — SQL injection (string-concatenated queries), command injection, hardcoded secrets, unsafe deserialization, missing auth checks, path traversal. Quote vulnerable lines or state none found.",
			"MAINTAINABILITY: naming clarity, function length, dead code, duplicated logic, missing error handling. Quote specific lines.",
			"SPEC_CONFORMANCE: for each spec requirement, is it implemented? Identify missing/partial/contradicted requirements. Quote spec requirement + implementing code.",
		}
	case "C2": // text — communication rubric
		criteria = []string{
			"COHERENCE: internally consistent? Do claims contradict? Does the conclusion follow the body? Quote evidence.",
			"RELEVANCE: on-topic vs the spec/ask? Any filler or off-topic content? Quote it.",
			"FLUENCY: grammar, spelling, sentence structure, register consistency. Quote errors.",
			"BRAND_ALIGNMENT: matches brand voice, terminology, disclosure requirements? Quote examples.",
		}
	default:
		// C3-C7 or unknown — generic fallback rubric.
		criteria = []string{
			"SPEC_ALIGNMENT: does the artifact match the spec? Identify missing or contradicted requirements. Quote evidence.",
			"INTERNAL_CONSISTENCY: are claims internally consistent? Quote contradictions.",
			"QUALITY_FLOOR: complete, usable, free of obvious defects? Quote any defects.",
		}
	}

	// brand_match specializes the rubric for brand evaluation.
	if evalType == "brand_match" && vibeCase == "C1" {
		criteria = []string{
			"TOKEN_CORRECTNESS: does the code implement the brand guide's technical requirements (token usage, API contracts, naming)?",
		}
	}
	if evalType == "brand_match" && vibeCase == "C2" {
		criteria = []string{
			"VOICE_MATCH: compare the text's voice (formality, jargon, register) to the brand guide. Quote match/drift evidence.",
		}
	}

	var b strings.Builder
	b.WriteString("VIBE-CASE RUBRIC (evaluate each criterion, quote exact evidence):\n")
	for _, c := range criteria {
		b.WriteString("  - ")
		b.WriteString(c)
		b.WriteString("\n")
	}
	b.WriteString("\nPROCEDURE: evaluate each criterion with quoted evidence; then decide the verdict as the LOGICAL CONCLUSION of your checklist. If any criterion FAILS with concrete evidence -> verdict=drift_detected. If all pass -> verdict=aligned. If evidence is insufficient -> verdict=needs_human.\n")
	b.WriteString("CONSISTENCY: your verdict MUST match your reasoning. Do NOT report drift_detected while your reasoning says there is no drift.")
	return b.String()
}

// truncateForErr caps a string for inclusion in an error message.
func truncateForErr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// MockLLMClient is a deterministic in-memory LLMClient for tests.
// It returns the configured verdict + confidence on every Judge call.
// If Err is set, returns the error (used for "canary rejection" tests).
//
// Concurrency (v2.11.0): JudgeConsensus runs N samples in parallel,
// so a single mock may be invoked from multiple goroutines. Calls is
// incremented atomically (reads/writes with untyped constants keep
// working); LastReq is mutex-guarded last-write-wins.
type MockLLMClient struct {
	Name_       string
	VerdictJSON string
	Confidence  float32
	Model       string
	Err         error
	Calls       int32
	LastReq     JudgeRequest

	mu sync.Mutex // guards LastReq
}

// Name implements LLMClient.
func (m *MockLLMClient) Name() string { return m.Name_ }

// Judge implements LLMClient.
func (m *MockLLMClient) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	atomic.AddInt32(&m.Calls, 1)
	m.mu.Lock()
	m.LastReq = req
	m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	return &JudgeResponse{
		VerdictJSON: m.VerdictJSON,
		Confidence:  m.Confidence,
		Model:       m.Model,
		Provider:    "mock",
	}, nil
}
