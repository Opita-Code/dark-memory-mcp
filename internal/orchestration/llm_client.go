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
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// driftJudgeDaemonTimeout bounds the HTTP call to DARK_DRIFT_JUDGE_DAEMON_URL/v1/messages.
// Kept conservative; matches verify-llm-pipeline.ps1 stage 2 timeout.
const driftJudgeDaemonTimeout = 60 * time.Second

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
//   - provider = anything else (anthropic | openai | google): stub.
//     Direct HTTP clients for those providers remain deferred to
//     Wave 4+. Returning ErrNoLLMAvailable here is BY DESIGN and
//     preserves the source's stated philosophy of surfacing the gap
//     instead of silently returning fake verdicts.
//
// Self-judge pattern: the same model that called the MCP tool acts as judge.
//
// Detection order (first match wins):
//
//  1. ANTHROPIC_API_KEY  ÔåÆ Anthropic Claude (stub in this build)
//  2. OPENAI_API_KEY     ÔåÆ OpenAI GPT (stub in this build)
//  3. GEMINI_API_KEY     ÔåÆ Google Gemini (stub in this build)
//  4. DARK_DRIFT_JUDGE_DAEMON_URL  ÔåÆ [drift-judge-daemon] pool (WIRED)
//  5. none               ÔåÆ ErrNoLLMAvailable
//
// The model is auto-picked via the OSINTSelector for the eval_type
// (config-based today, real OSINT later ÔÇö see spec 173 O5).
type SelfHarnessClient struct {
	provider string
	model    string
	key      string // API key or DARK_DRIFT_JUDGE_DAEMON_URL
}

// NewSelfHarnessClient detects the available LLM via env vars.
// Returns nil + ErrNoLLMAvailable if nothing is set.
func NewSelfHarnessClient() (*SelfHarnessClient, error) {
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return &SelfHarnessClient{
			provider: "anthropic",
			model:    os.Getenv("DARK_JUDGE_MODEL_ANTHROPIC"), // optional override
			key:      key,
		}, nil
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return &SelfHarnessClient{
			provider: "openai",
			model:    os.Getenv("DARK_JUDGE_MODEL_OPENAI"),
			key:      key,
		}, nil
	}
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		return &SelfHarnessClient{
			provider: "google",
			model:    os.Getenv("DARK_JUDGE_MODEL_GOOGLE"),
			key:      key,
		}, nil
	}
	if url := os.Getenv("DARK_DRIFT_JUDGE_DAEMON_URL"); url != "" {
		return &SelfHarnessClient{
			provider: "drift_judge_daemon",
			model:    os.Getenv("DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON"),
			key:      url,
		}, nil
	}
	return nil, ErrNoLLMAvailable
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
// Wave 4 update (2026-07-18): provider=anthropic with SDD_LLM_BASE_URL set
// now routes through the configured Anthropic-compatible endpoint using the
// real ANTHROPIC_API_KEY. Probes confirmed that
// `https://api.minimax.io/anthropic/v1/messages` accepts both
// `x-api-key: <key>` and `Authorization: Bearer <key>`, so we send both
// for safety. Without SDD_LLM_BASE_URL, fall through to the explicit stub
// error to preserve the source's stated philosophy of surfacing the gap
// (not silently faking verdicts). openai / google remain stubs.
//
// The drift_judge_daemon path (DARK_DRIFT_JUDGE_DAEMON_URL with sentinel auth
// "ds-managed") is preserved verbatim for [drift-judge-daemon] compatibility.
// Wire format is identical (Anthropic Messages API), so verify-llm-pipeline.ps1
// stage 2/4 regression suite continues to apply.
func (s *SelfHarnessClient) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	if s == nil || s.provider == "" {
		return nil, ErrNoLLMAvailable
	}
	if s.provider == "drift_judge_daemon" {
		return s.judgeViaDriftJudgeDaemon(ctx, req)
	}
	// Wave 4: anthropic + SDD_LLM_BASE_URL set ÔåÆ real LLM via judgeViaHTTP.
	// Guarded so that absence of SDD_LLM_BASE_URL (or empty ANTHROPIC_API_KEY)
	// still surfaces the explicit gap error rather than silently faking.
	if s.provider == "anthropic" {
		if baseURL := os.Getenv("SDD_LLM_BASE_URL"); baseURL != "" && s.key != "" {
			return s.judgeViaHTTP(ctx, req, baseURL, s.key)
		}
	}
	// openai / google / no-env still return the explicit gap error.
	return nil, fmt.Errorf("%w: self_harness provider=%s model=%s ÔÇö direct HTTP for this provider deferred to Wave 4 (or set SDD_LLM_BASE_URL for anthropic)",
		ErrNoLLMAvailable, s.provider, s.model)
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

	// Resolve model. Caller hint > client config > safe default.
	model := req.Model
	if model == "" {
		model = s.model
	}
	if model == "" {
		model = "MiniMax-M3" // default; matches harness model
	}

	// Resolve system prompt. Caller override > per-eval_type default.
	system := req.SystemPrompt
	if system == "" {
		system = defaultSystemForEval(req.EvalType)
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("judge: build http request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+authValue)
	httpReq.Header.Set("x-api-key", authValue)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpClient := &http.Client{Timeout: driftJudgeDaemonTimeout}
	httpResp, err := httpClient.Do(httpReq)
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
		return nil, fmt.Errorf("judge: HTTP %d: %s", httpResp.StatusCode, truncateForErr(string(respBytes), 512))
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
type MockLLMClient struct {
	Name_       string
	VerdictJSON string
	Confidence  float32
	Model       string
	Err         error
	Calls       int
	LastReq     JudgeRequest
}

// Name implements LLMClient.
func (m *MockLLMClient) Name() string { return m.Name_ }

// Judge implements LLMClient.
func (m *MockLLMClient) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	m.Calls++
	m.LastReq = req
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
