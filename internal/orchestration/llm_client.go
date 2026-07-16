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
	"context"
	"errors"
	"fmt"
	"os"
)

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

// SelfHarnessClient detects the harness's own LLM via env vars and
// delegates Judge calls to that LLM. This is the "self-judge"
// pattern: the same model that called the MCP tool acts as judge.
//
// Detection order (first match wins):
//
//   1. ANTHROPIC_API_KEY  → Anthropic Claude
//   2. OPENAI_API_KEY     → OpenAI GPT
//   3. GEMINI_API_KEY     → Google Gemini
//   4. DARK_SCRAPPER_URL  → [drift-judge-daemon] pool (delegated; requires
//                           [drift-judge-daemon] as a sidecar)
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
// NOTE: This is the SPEC layer. The actual HTTP/gRPC call to
// Anthropic / OpenAI / Gemini / [drift-judge-daemon] is NOT wired here
// because:
//
//   1. Each provider has a different API shape.
//   2. Real integration needs [drift-judge-daemon] or direct HTTP clients
//      (deferred to Wave 4+).
//   3. For now, SelfHarnessClient.Judge returns ErrNoLLMAvailable
//      so the orchestrator's fallback path is visible in production
//      today (rather than silently returning fake verdicts).
//
// When [drift-judge-daemon] integration lands, this method will call into
// the [drift-judge-daemon] client (which rotates virtual keys across the
// 4 providers). Until then, tests use MockLLMClient and production
// callers see ErrNoLLMAvailable.
func (s *SelfHarnessClient) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	if s == nil || s.provider == "" {
		return nil, ErrNoLLMAvailable
	}
	// Provider integration deferred to Wave 4. Wrap the error in
	// ErrNoLLMAvailable so callers can errors.Is against it.
	return nil, fmt.Errorf("%w: self_harness provider=%s model=%s — integration deferred to Wave 4 (when [drift-judge-daemon] client is wired)",
		ErrNoLLMAvailable, s.provider, s.model)
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