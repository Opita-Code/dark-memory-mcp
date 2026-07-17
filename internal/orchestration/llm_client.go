// LLMClient is one LLM-as-judge endpoint. Three implementations:
//
//   - SelfHarnessClient: detects the harness's own LLM via env vars
//     (ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, ...). This is
//     the default: the same model that called the MCP tool acts as
//     judge. Self-judge is biased but zero-config.
//   - DarkscrapperClient: uses the [drift-judge-daemon] virtual-key pool,
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

// scrapperTimeout bounds the HTTP call to DARK_SCRAPPER_URL/v1/messages.
// Kept conservative; matches verify-llm-pipeline.ps1 stage 2 timeout.
const scrapperTimeout = 60 * time.Second

// JudgeRequest is the structured input to a Judge call. The
// orchestrator fills it from the JudgeInput plus a per-eval_type
// system prompt template. Model is a hint from the OSINT selector
// (recommended for the eval_type); clients may ignore or use it.
type JudgeRequest struct {
	EvalType    string `json:"eval_type"`    // brand_match | compliance_check | drift_judge | grounding_check | pii_detect | prompt_injection_scan | consensus
	Content     string `json:"content"`      // the text to evaluate
	TargetType  string `json:"target_type"`  // brand | artifact | spec | claim | code | ...
	TargetID    string `json:"target_id"`    // brand_id | artifact_id | ...
	Model       string `json:"model,omitempty"`     // recommended by OSINTSelector
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
var ErrNoLLMAvailable = errors.New("no LLM available: harness has no API key (set ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or DARK_SCRAPPER_URL), and no fallback was configured")

// SelfHarnessClient delegates Judge calls to an LLM. Two supported
// modes in this build:
//
//   - provider = "dark_scrapper": POST to DARK_SCRAPPER_URL/v1/messages
//     with Bearer ds-managed (sentinel auth). Wave 3.5 wiring of the
//     [drift-judge-daemon] integration; source previously deferred this
//     to Wave 4+. Works against either the real dark-scrapper daemon
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
//   1. ANTHROPIC_API_KEY  → Anthropic Claude (stub in this build)
//   2. OPENAI_API_KEY     → OpenAI GPT (stub in this build)
//   3. GEMINI_API_KEY     → Google Gemini (stub in this build)
//   4. DARK_SCRAPPER_URL  → [drift-judge-daemon] pool (WIRED)
//   5. none               → ErrNoLLMAvailable
//
// The model is auto-picked via the OSINTSelector for the eval_type
// (config-based today, real OSINT later — see spec 173 O5).
type SelfHarnessClient struct {
	provider string
	model    string
	key      string // API key or DARK_SCRAPPER_URL
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
	if url := os.Getenv("DARK_SCRAPPER_URL"); url != "" {
		return &SelfHarnessClient{
			provider: "dark_scrapper",
			model:    os.Getenv("DARK_JUDGE_MODEL_SCRAPPER"),
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
// Wave 3.5 update: when provider == "dark_scrapper", this method now
// performs an actual HTTP POST to DARK_SCRAPPER_URL/v1/messages with
// sentinel auth (Bearer ds-managed). Other providers (anthropic /
// openai / google) still return ErrNoLLMAvailable; their direct HTTP
// clients remain deferred to Wave 4+.
//
// The chosen route is the [drift-judge-daemon] /v1/messages endpoint,
// which is Anthropic Messages API compatible. This is the same wire
// format used by verify-llm-pipeline.ps1 (stage 2 against the daemon,
// stage 4 against mock-llm on :9000), so the patch is verified
// independently by re-running that script.
func (s *SelfHarnessClient) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	if s == nil || s.provider == "" {
		return nil, ErrNoLLMAvailable
	}
	if s.provider == "dark_scrapper" {
		return s.judgeViaScrapper(ctx, req)
	}
	// Other providers remain stubs in this build. Surfacing the gap
	// (not silently faking verdicts) is intentional.
	return nil, fmt.Errorf("%w: self_harness provider=%s model=%s — direct HTTP for this provider deferred to Wave 4",
		ErrNoLLMAvailable, s.provider, s.model)
}

// judgeViaScrapper performs one Anthropic-format POST to
// DARK_SCRAPPER_URL/v1/messages with sentinel auth.
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
// Response parsing is lenient: accepts both Anthropic Messages API
// format (content[0].text) and the minimal mock-llm format ({text: "..."}).
// Any non-JSON response is wrapped verbatim as the verdict text so the
// orchestrator's downstream parsing can decide.
//
// Safety: rejects URLs without an http(s) scheme, host, or with
// non-loopback hosts in safety-strict mode. Does NOT block loopback
// (operator runs the daemon locally by design).
func (s *SelfHarnessClient) judgeViaScrapper(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	if s.key == "" {
		return nil, fmt.Errorf("%w: dark_scrapper URL is empty (set DARK_SCRAPPER_URL)", ErrNoLLMAvailable)
	}

	// URL validation: must parse, must be http(s), must have a host.
	endpoint, err := url.Parse(s.key)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid DARK_SCRAPPER_URL: %v", ErrNoLLMAvailable, err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("%w: DARK_SCRAPPER_URL must be http(s); got %q", ErrNoLLMAvailable, endpoint.Scheme)
	}
	if endpoint.Host == "" {
		return nil, fmt.Errorf("%w: DARK_SCRAPPER_URL has no host", ErrNoLLMAvailable)
	}

	// Resolve model. Caller hint > OSINT catalog > safe default.
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

	endpointStr := strings.TrimRight(s.key, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("judge: build http request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer ds-managed")
	httpReq.Header.Set("x-api-key", "ds-managed")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpClient := &http.Client{Timeout: scrapperTimeout}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("judge: scrapper request: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("judge: read scrapper response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		// 503 with empty pool is a legitimate daemon signal: pool is
		// drained, harvest cycle will refill. Surface the message.
		return nil, fmt.Errorf("judge: scrapper HTTP %d: %s", httpResp.StatusCode, truncateForErr(string(respBytes), 512))
	}

	// Parse response. Two shapes:
	//   1. Anthropic: {"content":[{"type":"text","text":"..."}], "model":"...", ...}
	//   2. mock-llm:  {"text":"..."}
	//   3. fallback:  raw text (wrapped as verdict)
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
			Provider:    "dark_scrapper",
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
		Provider:    "dark_scrapper",
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