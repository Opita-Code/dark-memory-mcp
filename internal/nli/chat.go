// Package nli — chat_provider.go
//
// ChatProvider implements nli.Provider via an OpenAI-compatible chat
// completions API. This is the dispatch path for the operator's
// configured `provider_id="chat-*"` (e.g. "chat-minimax-cn" against
// https://api.minimaxi.com/v1/chat/completions, "chat-deepseek" against
// https://api.deepseek.com/v1/chat/completions).
//
// Why this exists: the NLI provider dispatch in
// `orchestration/nli_wiring.go::buildNLIPrimary` previously supported
// ONLY `deberta*` (HuggingFace Inference) and `minicheck*` (self-hosted
// HTTP). Operators configured with a chat-completion API (the dominant
// 2026 deployment shape: MiniMax, DeepSeek, OpenAI, Anthropic via OpenAI
// adapters, vLLM-served chat models) hit
// `ErrInvalidConfig: unknown provider_id "chat-*"` and drift_judge
// returned needs_human@0 even though the config in the DB was valid
// (this is the Phase H5 / row 1370 gap closed by T-201 in Phase 13).
//
// # NLI via chat completion — prompt + response protocol
//
// We send the standard RAG-evaluation NLI prompt (see TruLens, RAGAS,
// LangChain evaluators): the model replies with ONE canonical word —
// "entailment", "contradiction", or "neutral". We parse that token
// (case-insensitive, with light stemming for common whitespace variants).
// Anything else → ErrProviderBadResponse (contract bug, do not retry).
//
// Temperature is fixed at 0 (deterministic; the harness wants reproducible
// verdicts for drift). Max tokens is fixed at 8 (the longest canonical
// label is "contradiction" = 12 chars; 8 is enough for the model to be
// slightly verbose but bounded).
//
// # Wire shape
//
// POST <endpoint>
// Content-Type: application/json
// Authorization: Bearer <auth_token>  (omitted when auth_token is empty)
//
// Request body:
//   {
//     "model": "<ModelRev>",
//     "temperature": 0,
//     "max_tokens": 8,
//     "messages": [
//       {"role": "system", "content": <System Prompt — see nliChatSystemPrompt>},
//       {"role": "user",   "content": <User Prompt — see buildNliPrompt>}
//     ]
//   }
//
// Response body (success):
//   {
//     "choices": [
//       {"message": {"role": "assistant", "content": "entailment"}}
//     ]
//   }
//
// Any deviation → ErrProviderBadResponse (HTTP 200 with wrong shape)
// or one of the standard HTTP error envelopes (401/403/429/5xx/timeouts)
// mapped to the sealed error set.
//
// # Hard invariants (sealed)
//
//   - ChatProvider.ID() returns cfg.ProviderID verbatim (e.g.
//     "chat-minimax-cn"). Provenance flows through the audit chain.
//   - The provider NEVER echoes auth_token, premise, or hypothesis in
//     errors. The score response is the only caller-visible artefact.
//   - max_premise_bytes / max_hypothesis_bytes from the project's
//     NLIConfig enforce size caps BEFORE the HTTP call (defense in
//     depth; the artifact pipeline already enforces its own caps).
//   - Timeout is read from cfg.TimeoutMS; default 10s (nli.DefaultTimeoutMS).
//
// # SOTA grounding
//
// TruLens / RAGAS / OpenAI Evals all use the same prompt template
// (one-word canonical label reply). The wire shape is OpenAI
// `/v1/chat/completions` per OpenAI's API reference (fetched 2026-08-19).
// max_tokens sizing rationale is at the reasoningModelMaxTokens table.
//
// # max_tokens — per-model resolution (T-407-c, v4.0.0-alpha.28)
//
// The previous single-value cap (8 → 64 → 256 across T-405/T-406) was
// insufficient for 2026 reasoning models. Anthropic Claude (extended
// thinking), DeepSeek-R1, OpenAI o-series, and MiniMax-M3 burn their
// entire completion budget on internal reasoning and emit
// `finish_reason="length"` + empty content when the cap is too low
// (TokenMix Q1 2026 wallet logs: 40% empty returns on DeepSeek-R1 +
// max_tokens=200; we observed 28% on MiniMax-M3 + max_tokens=256).
//
// T-407-c introduces 3-layer defensive behavior:
//
//  1. **Per-model defaults table** (resolveMaxTokens): maps ModelRev
//     to a safe budget based on tier-1 SOTA evidence.
//  2. **Operator override** (ProviderConfig.MaxTokensOverride): escape
//     hatch for models not in the table or workloads needing different
//     budgets than the table default.
//  3. **finish_reason="length" detection + retry-on-length** (in
//     Score + parseChatCompletionResponse): catches the long tail —
//     P99 reasoning models, new models not yet mapped, edge cases.
//     One retry at 4× budget, capped at MaxRetryBudgetCap (8192).
//
// See reasoningModelMaxTokens + resolveMaxTokens below. Cross-reference:
// T-407-c OSINT findings agent_memory row 2501.
package nli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatProvider scores a (premise, hypothesis) pair against an
// OpenAI-compatible chat completions API and maps the model's
// one-word reply into the canonical NLI 3-label space.

// ErrTruncatedResponse is the FIRST-CLASS failure mode (T-407-c,
// v4.0.0-alpha.28) where a reasoning model burned its entire
// max_tokens budget on internal thinking and returned
// finish_reason="length" with empty content.
//
// Per TokenMix 2026-04-25 SOTA evidence: ~40% of reasoning model calls
// hit this failure at max_tokens < 1000. We classify it as
// ErrTruncatedResponse so ChatProvider.Score can retry once at 4×
// budget (capped at MaxRetryBudgetCap). The retry is bounded (1 only)
// to prevent infinite loops on models whose thinking always exceeds
// the cap.
var ErrTruncatedResponse = errors.New("nli: chat response truncated — thinking tokens exhausted max_tokens")

// MaxRetryBudgetCap is the hard ceiling on retry budget. Above 8k
// tokens, model APIs hit timeout / network limits (Anthropic docs
// recommend batch processing above 32k; we conservatively cap retries
// at 8k to stay well below that).
const MaxRetryBudgetCap = 8192

// reasoningModelMaxTokens is the sealed defaults table for known
// reasoning + non-reasoning chat models (T-407-c). Lookup is by
// longest-prefix-match against ModelRev (so "claude-sonnet-4-20260101"
// matches "claude-sonnet-4").
//
// Sizing rationale (sources: T-407-c OSINT row 2501):
//   - Non-reasoning models: tight budgets (256-1024) to prevent prose.
//   - Reasoning models: 4× visible output (TokenMix 2026 rule) + P95 safety.
//   - Last entry (empty pattern) is the catch-all fallback.
//
// Adding a new row: append here + add test case in chat_test.go +
// bump CHANGELOG.
var reasoningModelMaxTokens = []modelMaxTokensEntry{
	// === Anthropic Claude (extended thinking + legacy) ===
	{pattern: "claude-opus-4-5", maxTokens: 2048, family: "reasoning"},
	{pattern: "claude-opus-4-6", maxTokens: 2048, family: "reasoning"},
	{pattern: "claude-sonnet-4", maxTokens: 2048, family: "reasoning"},
	{pattern: "claude-haiku-4-5", maxTokens: 1024, family: "reasoning"},
	{pattern: "claude-3-7-sonnet", maxTokens: 1024, family: "reasoning"},

	// === DeepSeek (R1 always reasons; V3.x + flash non-reasoning) ===
	{pattern: "deepseek-r1", maxTokens: 4096, family: "reasoning"},
	{pattern: "deepseek-reasoner", maxTokens: 4096, family: "reasoning"},
	{pattern: "deepseek-v3", maxTokens: 1024, family: "non-reasoning"},
	{pattern: "deepseek-flash", maxTokens: 512, family: "non-reasoning"},

	// === OpenAI o-series (reasoning_effort controls) ===
	{pattern: "o3-mini", maxTokens: 4096, family: "reasoning"},
	{pattern: "o4-mini", maxTokens: 4096, family: "reasoning"},
	{pattern: "o3", maxTokens: 8192, family: "reasoning"},
	{pattern: "o1", maxTokens: 8192, family: "reasoning"},

	// === OpenAI non-reasoning ===
	{pattern: "gpt-4o", maxTokens: 256, family: "non-reasoning"},
	{pattern: "gpt-4o-mini", maxTokens: 256, family: "non-reasoning"},
	{pattern: "gpt-5", maxTokens: 1024, family: "hybrid"}, // per OpenAI: GPT-5 reasoning is opt-in

	// === MiniMax (our active model + sibling — emits <think> blocks) ===
	{pattern: "MiniMax-M3", maxTokens: 1024, family: "reasoning"},
	{pattern: "MiniMax-M2", maxTokens: 1024, family: "reasoning"},

	// === Catch-all (must be last) ===
	// Conservative for unknown reasoning models per TokenMix P95 ceiling.
	{pattern: "", maxTokens: 1024, family: "unknown"},
}

// DefaultFallbackMaxTokens is returned by resolveMaxTokens when no
// pattern matches AND no override is set. Mirrors the last entry of
// reasoningModelMaxTokens — kept as a named const for testability.
const DefaultFallbackMaxTokens = 1024

// modelMaxTokensEntry is one row of the reasoningModelMaxTokens table.
type modelMaxTokensEntry struct {
	pattern   string
	maxTokens int
	family    string // "reasoning" | "non-reasoning" | "hybrid" | "unknown"
}

// resolveMaxTokens returns the max_tokens budget to send to the chat
// API for the given modelRev, honoring (in order):
//   1. override > 0 → use it (operator escape hatch)
//   2. longest-prefix-match in reasoningModelMaxTokens
//   3. DefaultFallbackMaxTokens (1024)
//
// This function is pure (no I/O, no allocation beyond lookup). Test
// cases live in chat_test.go (T-407-c.6).
func resolveMaxTokens(modelRev string, override int) int {
	if override > 0 {
		return override
	}
	bestMatch := ""
	bestTokens := DefaultFallbackMaxTokens
	for _, entry := range reasoningModelMaxTokens {
		if entry.pattern == "" {
			continue // catch-all, only used if nothing else matches
		}
		if strings.HasPrefix(modelRev, entry.pattern) {
			if len(entry.pattern) > len(bestMatch) {
				bestMatch = entry.pattern
				bestTokens = entry.maxTokens
			}
		}
	}
	if bestMatch == "" {
		// No specific match → fall through to last catch-all entry.
		for _, entry := range reasoningModelMaxTokens {
			if entry.pattern == "" {
				bestTokens = entry.maxTokens
			}
		}
	}
	return bestTokens
}

// ChatProvider scores a (premise, hypothesis) pair against an
// OpenAI-compatible chat completions API and maps the model's
// one-word reply into the canonical NLI 3-label space.
type ChatProvider struct {
	client            HFInferenceClient
	endpoint          string
	authToken         string
	timeout           time.Duration
	maxPBytes         int
	maxHBytes         int
	modelRev          string
	providerID        string
	maxTokensOverride int // 0 = use resolveMaxTokens; >0 = operator override
}

// NewChatProvider validates cfg and returns a ChatProvider.
// Returns an error if cfg.ProviderID does not start with "chat-",
// cfg.Endpoint is empty, the HTTP client is nil, or the size caps
// are negative.
//
// The "chat-" prefix is required (not a convention) so dispatch in
// orchestration/nli_wiring.go::buildNLIPrimary can route operator configs
// unambiguously to ChatProvider vs DeBERTaProvider / MiniCheckProvider.
func NewChatProvider(cfg ProviderConfig, hc HFInferenceClient, maxP, maxH int) (*ChatProvider, error) {
	if !strings.HasPrefix(cfg.ProviderID, "chat-") {
		return nil, fmt.Errorf("%w: ChatProvider requires provider_id starting with 'chat-' (got %q)",
			ErrInvalidConfig, cfg.ProviderID)
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("%w: empty Endpoint", ErrInvalidConfig)
	}
	if hc == nil {
		return nil, fmt.Errorf("%w: nil HTTP client", ErrInvalidConfig)
	}
	if maxP < 0 || maxH < 0 {
		return nil, fmt.Errorf("%w: negative size cap", ErrInvalidConfig)
	}
	if cfg.MaxTokensOverride < 0 {
		return nil, fmt.Errorf("%w: negative max_tokens_override", ErrInvalidConfig)
	}
	cfg = cfg.WithDefaults()
	return &ChatProvider{
		client:            hc,
		endpoint:          cfg.Endpoint,
		authToken:         cfg.AuthToken,
		timeout:           time.Duration(cfg.TimeoutMS) * time.Millisecond,
		maxPBytes:         maxP,
		maxHBytes:         maxH,
		modelRev:          cfg.ModelRev,
		providerID:        cfg.ProviderID,
		maxTokensOverride: cfg.MaxTokensOverride,
	}, nil
}

// ID returns the provider's logical id (the verbatim provider_id from
// the project's NLIConfig.Primary — e.g. "chat-minimax-cn"). Provenance
// flows through the audit chain.
func (p *ChatProvider) ID() string { return p.providerID }

// Score implements Provider.Score for OpenAI-compatible chat APIs.
//
// The model is asked to reply with ONE word: "entailment",
// "contradiction", or "neutral". Anything else is treated as a contract
// bug (ErrProviderBadResponse) — fallback would have the same contract,
// so the router does NOT retry.
//
// T-407-c (v4.0.0-alpha.28): retry-on-length for reasoning models.
// If the model returns finish_reason="length" with empty/incomplete
// content (the reasoning-tokens-consumed-budget failure mode), Score
// retries ONCE at 4× the original budget, capped at MaxRetryBudgetCap
// (8192). The retry is bounded to prevent infinite loops on models
// whose thinking always exceeds the cap.
func (p *ChatProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	if premise == "" || hypothesis == "" {
		return Score{}, ErrInputEmpty
	}
	if len(premise) > p.maxPBytes {
		return Score{}, fmt.Errorf("%w: premise=%d > %d", ErrInputTooLarge, len(premise), p.maxPBytes)
	}
	if len(hypothesis) > p.maxHBytes {
		return Score{}, fmt.Errorf("%w: hypothesis=%d > %d", ErrInputTooLarge, len(hypothesis), p.maxHBytes)
	}

	maxTokens := resolveMaxTokens(p.modelRev, p.maxTokensOverride)

	const maxRetries = 1
	const retryMultiplier = 4

	var lastErr error
	var lastLatency int64
	for attempt := 0; attempt <= maxRetries; attempt++ {
		score, err := p.scoreOnce(ctx, premise, hypothesis, maxTokens)
		lastLatency += score.LatencyMS
		if err == nil {
			return score, nil
		}
		lastErr = err
		// Only ErrTruncatedResponse is retryable. Any other error
		// (ErrProviderBadResponse, ErrProviderTimeout, etc.) bubbles up.
		if !errors.Is(err, ErrTruncatedResponse) {
			return score, err
		}
		// Compute next budget: 4× current, capped at MaxRetryBudgetCap.
		maxTokens = maxTokens * retryMultiplier
		if maxTokens > MaxRetryBudgetCap {
			maxTokens = MaxRetryBudgetCap
		}
	}
	// Both attempts truncated.
	return Score{LatencyMS: lastLatency, ProviderID: p.ID()}, fmt.Errorf("%w: %d retries exhausted (final budget %d): %v",
		ErrProviderBadResponse, maxRetries, maxTokens, lastErr)
}

// scoreOnce is the inner single-attempt Score (T-407-c extracted). The
// retry loop in Score wraps this. ScoreOnce preserves all pre-retry
// semantics: input validation, timeout, HTTP error classification,
// finish_reason detection, label parsing, score validation.
func (p *ChatProvider) scoreOnce(ctx context.Context, premise, hypothesis string, maxTokens int) (Score, error) {
	// Build the request payload manually for stable field order
	// (Go's encoding/json orders struct fields by declaration; we
	// want the same wire bytes for the same logical request so test
	// golden files don't drift).
	payload, err := buildChatCompletionPayload(p.modelRev, maxTokens, premise, hypothesis)
	if err != nil {
		return Score{}, fmt.Errorf("%w: marshal: %w", ErrProviderBadResponse, err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Score{}, fmt.Errorf("%w: build request: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.authToken)
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderTimeout
		}
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	// HTTP error classification (matches DeBERTaProvider / MiniCheckProvider).
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderRateLimited
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderUnavailable
	case resp.StatusCode >= 500:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderUnavailable
	case resp.StatusCode >= 400:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: HTTP %d", ErrProviderBadResponse, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: unexpected status %d", ErrProviderUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: read: %w", ErrProviderBadResponse, err)
	}

	label, confidence, err := parseChatCompletionResponse(body)
	if err != nil {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, err
	}

	score := Score{
		Label:      label,
		Confidence: confidence,
		LatencyMS:  latency,
		ProviderID: p.ID(),
		ModelRev:   p.modelRev,
	}
	if !score.Valid() {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: score failed invariant check", ErrProviderBadResponse)
	}
	return score, nil
}

// nliChatSystemPrompt is the system message sent to the chat model.
// The phrasing is the conventional RAG-eval prompt (RAGAS, TruLens) —
// asking the model to commit to ONE canonical word keeps parsing simple
// and the verdict reproducible.
const nliChatSystemPrompt = `You are a precise Natural Language Inference (NLI) classifier. ` +
	`Given a premise and a hypothesis, you reply with EXACTLY ONE of these three words, ` +
	`with no surrounding punctuation, no quotes, no explanation: ` +
	`"entailment" (the premise supports the hypothesis), ` +
	`"contradiction" (the premise refutes the hypothesis), or ` +
	`"neutral" (the premise neither supports nor refutes the hypothesis). ` +
	`Reply with only the word. No other output is valid.`

// buildNliPrompt composes the user-role payload sent to the chat model.
// Exposed (non-private) for tests that need to assert prompt shape.
func buildNliPrompt(premise, hypothesis string) string {
	// Using JSON-style delimiters so the model can clearly see where
	// premise ends and hypothesis begins. The double newline between
	// sections is intentional — most chat models treat \n\n as a
	// stronger section break than \n.
	return "Premise:\n" + premise + "\n\nHypothesis:\n" + hypothesis + "\n\nReply with one word."
}

// buildChatCompletionPayload constructs the OpenAI-compatible request body.
// Manually marshaled for stable field order (test goldenfiles).
//
// We use a fixed temperature of 0 (deterministic drift verdicts). The
// max_tokens budget is now a PARAMETER (T-407-c, v4.0.0-alpha.28):
// the caller (Score → scoreOnce) resolves it via resolveMaxTokens +
// ProviderConfig.MaxTokensOverride before invoking this function.
//
// History of the max_tokens field (audit trail):
//
//	8 → 64 → 256 (T-405+T-406, alpha.27-pre-2): bumped to give reasoning
//	models enough budget for <think>...</think> reasoning block PLUS
//	the canonical one-word label.
//	256 → per-model (T-407-c, alpha.28): single hardcoded value was
//	insufficient across model families. Anthropic Claude extended
//	thinking, DeepSeek-R1, OpenAI o-series, and MiniMax-M3 all burn
//	the completion budget on internal reasoning. Per-model defaults
//	table (resolveMaxTokens) + operator override (MaxTokensOverride) +
//	finish_reason="length" retry loop (in Score) replace the
//	hardcoded-256 pattern.
func buildChatCompletionPayload(modelRev string, maxTokens int, premise, hypothesis string) ([]byte, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type request struct {
		Model       string    `json:"model"`
		Messages    []message `json:"messages"`
		Temperature float64   `json:"temperature"`
		MaxTokens   int       `json:"max_tokens"`
	}
	req := request{
		Model:       modelRev,
		Temperature: 0,
		MaxTokens:   maxTokens,
		Messages: []message{
			{Role: "system", Content: nliChatSystemPrompt},
			{Role: "user", Content: buildNliPrompt(premise, hypothesis)},
		},
	}
	return json.Marshal(req)
}

// parseChatCompletionResponse extracts (label, confidence) from the
// OpenAI-compatible response body.
//
// Acceptable body shapes:
//
//	{"choices": [{"finish_reason": "stop",
//	              "message": {"role": "assistant", "content": "entailment"}}]}
//	{"choices": [{"text": "entailment"}]}        (legacy: text field)
//	{"content": "entailment"}                     (single-string shape, Anthropic-via-adapter)
//
// The reply is parsed case-insensitively with light whitespace trimming.
// Recognized canonical words map to the 3-label space with confidence 1.0
// (the model committed to one word; we trust it). Any other content
// (explanation, multi-word, JSON wrapper, etc.) → ErrProviderBadResponse.
//
// T-407-c (v4.0.0-alpha.28): when finish_reason="length" and content
// is empty/incomplete, return ErrTruncatedResponse so the caller's
// retry loop (Score → scoreOnce) can re-attempt at 4× budget. This is
// the "thinking tokens consumed the budget" failure mode (TokenMix
// Q1 2026: 40% of reasoning model calls at max_tokens<1000).
func parseChatCompletionResponse(body []byte) (Label, float64, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", 0, fmt.Errorf("%w: empty body", ErrProviderBadResponse)
	}

	// Try the OpenAI choices[].message.content shape first.
	var openaiShape struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"` // legacy fallback inside the same struct
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &openaiShape); err == nil && len(openaiShape.Choices) > 0 {
		raw := openaiShape.Choices[0].Message.Content
		if raw == "" {
			raw = openaiShape.Choices[0].Text
		}
		// T-407-c: detect reasoning-budget exhaustion BEFORE label parsing.
		// If the model hit its length cap with no usable content, the
		// caller needs to retry with a bigger budget.
		if openaiShape.Choices[0].FinishReason == "length" {
			// Strip <think> blocks to give a partial reply a chance to
			// parse (some models emit the label AFTER truncating).
			stripped := stripThinkBlocks(raw)
			if stripped == "" {
				return "", 0, fmt.Errorf("%w: content empty, finish_reason=length (thinking tokens exhausted budget)",
					ErrTruncatedResponse)
			}
			// Truncated mid-label — return whatever label survived.
			return parseCanonicalLabel(stripped)
		}
		return parseCanonicalLabel(raw)
	}

	// Anthropic-via-adapter shape: {"content": "entailment"}.
	var anthropicShape struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &anthropicShape); err == nil && anthropicShape.Content != "" {
		return parseCanonicalLabel(anthropicShape.Content)
	}

	// Plain text body (some lightweight chat servers).
	if body[0] != '{' && body[0] != '[' {
		return parseCanonicalLabel(string(body))
	}

	return "", 0, fmt.Errorf("%w: no recognized content field in body", ErrProviderBadResponse)
}

// stripThinkBlocks is a content-only helper used by
// parseChatCompletionResponse when finish_reason="length" and the
// raw reply contains only <think>...</think> reasoning with no
// visible label. We return the empty string in that case so the
// caller (Score) can distinguish "truncated" from "truncated mid-label".
//
// Extracted from parseCanonicalLabel to keep the public path stable
// while allowing T-407-c's finish_reason-aware parsing.
func stripThinkBlocks(raw string) string {
	word := strings.ToLower(strings.TrimSpace(raw))
	for {
		start := strings.Index(word, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(word[start:], "</think>")
		if end < 0 {
			// Unterminated block → no visible label.
			return ""
		}
		word = word[:start] + word[start+end+len("</think>"):]
		word = strings.TrimSpace(word)
	}
	word = strings.Trim(word, `"'.!?,;:`)
	return word
}

// parseCanonicalLabel maps a model's reply (case-insensitive, whitespace-
// tolerant) to the canonical 3-label space. Confidence is 1.0 — the
// model committed to one word; we trust the commitment.
//
// Recognized canonical words: "entailment", "contradiction", "neutral".
// Common variants (e.g. "entail", "support", "refute", "yes", "no")
// are NOT recognized — we keep the contract strict so an ambiguous
// reply surfaces as ErrProviderBadResponse instead of silently mapping
// to the wrong label.
//
// T-406 (v4.0.0-alpha.27-pre-2): Some 2026 chat-completion models
// (MiniMax-M3, Claude with extended-thinking, DeepSeek-R1) emit an
// explicit thinking block before the actual reply:
//
//	"<think>\n...reasoning...\n</think>\n\n<label>"
//
// The block is rendered as raw text in the assistant message field
// when reasoning is collapsed at the provider. We strip the block
// before the canonical-match so the operator's choice of model
// doesn't silently fail drift_judge.
func parseCanonicalLabel(raw string) (Label, float64, error) {
	word := stripThinkBlocks(raw)
	switch word {
	case "entailment":
		return LabelEntailment, 1.0, nil
	case "contradiction":
		return LabelContradiction, 1.0, nil
	case "neutral":
		return LabelNeutral, 1.0, nil
	default:
		return "", 0, fmt.Errorf("%w: unrecognized reply %q (expected: entailment, contradiction, or neutral)",
			ErrProviderBadResponse, raw)
	}
}