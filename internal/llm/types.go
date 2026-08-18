// types.go — shared LLM-as-judge types for the internal/llm package.
//
// Spec 1188 (LLM Provider Architecture v2.20.0): JudgeRequest /
// JudgeResponse / JudgeClient / ErrNoLLMAvailable moved here from
// internal/orchestration so the failover chain, the keystore, the
// health registry and the hot probe can share them without an import
// cycle. The orchestration package re-exports them as type aliases,
// so every existing call site keeps compiling unchanged.
package llm

import (
	"context"
	"errors"
)

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
	// UserPrompt (TD-J6 P0 fix) is the composed user-side prompt
	// from the JudgePromptBuilder (spec intent + required output
	// schema). When non-empty, LLM clients send it BEFORE the raw
	// Content section so the judge receives both halves of the
	// spec-vs-artifact pair. Empty = legacy behavior (content only).
	UserPrompt string `json:"user_prompt,omitempty"`
	// VibeCase (v2.12.0) is the vibe-flow case of the artifact
	// (C1=code, ..., C7=mixed). Empty = legacy generic prompt.
	VibeCase string `json:"vibe_case,omitempty"`
}

// JudgeResponse is the LLM's verdict.
type JudgeResponse struct {
	VerdictJSON string  `json:"verdict_json"` // JSON-encoded per eval_type schema
	Confidence  float32 `json:"confidence"`   // 0..1
	Model       string  `json:"model"`        // which model answered (e.g. "claude-sonnet-4-5", "gpt-5")
	Provider    string  `json:"provider"`     // anthropic | openai | google | ...
}

// ComposeUserContent builds the user-role message content sent to the
// LLM. When the composed UserPrompt is present (spec intent + output
// schema from the JudgePromptBuilder), it precedes the raw content so
// the judge receives both halves of the spec-vs-artifact pair. Empty
// UserPrompt = legacy behavior (content only). TD-J6 P0 fix.
func (r JudgeRequest) ComposeUserContent() string {
	if r.UserPrompt == "" {
		return r.Content
	}
	return r.UserPrompt + "\n\n## Content (verbatim)\n\n" + r.Content
}

// JudgeClient is one judge endpoint. Implementations: SelfHarnessClient
// (orchestration), FailoverClient (this package), MockLLMClient (tests).
type JudgeClient interface {
	// Name returns a stable identifier (e.g. "self_harness_anthropic",
	// "failover", "mock_v1").
	Name() string
	// Judge performs one LLM-as-judge call.
	Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error)
}

// ErrNoLLMAvailable is returned when no LLM key is detected AND no
// fallback was configured. The orchestrator wraps this with a
// user-facing hint.
var ErrNoLLMAvailable = errors.New("no LLM available: no provider has a usable key (configure via llm_key_add or env vars ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, DEEPSEEK_API_KEY, MINIMAX_API_KEY, MINIMAX_API_KEY_CN, MOONSHOT_API_KEY, ZAI_API_KEY, DASHSCOPE_API_KEY, or DARK_DRIFT_JUDGE_DAEMON_URL)")
