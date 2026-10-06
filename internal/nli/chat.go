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
type ChatProvider struct {
	client    HFInferenceClient
	endpoint  string
	authToken string
	timeout   time.Duration
	maxPBytes int
	maxHBytes int
	modelRev  string
	providerID string
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
	cfg = cfg.WithDefaults()
	return &ChatProvider{
		client:    hc,
		endpoint:  cfg.Endpoint,
		authToken: cfg.AuthToken,
		timeout:   time.Duration(cfg.TimeoutMS) * time.Millisecond,
		maxPBytes: maxP,
		maxHBytes: maxH,
		modelRev:  cfg.ModelRev,
		providerID: cfg.ProviderID,
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

	// Build the request payload manually for stable field order
	// (Go's encoding/json orders struct fields by declaration; we
	// want the same wire bytes for the same logical request so test
	// golden files don't drift).
	payload, err := buildChatCompletionPayload(p.modelRev, premise, hypothesis)
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
// We use a fixed temperature of 0 (deterministic drift verdicts) and
// a tight max_tokens of 8 (the longest canonical label is 12 chars; 8
// is enough headroom for the model to add a period or whitespace but
// bounded to prevent the model from "explaining" itself).
func buildChatCompletionPayload(modelRev, premise, hypothesis string) ([]byte, error) {
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
		MaxTokens:   8,
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
//	{"choices": [{"message": {"role": "assistant", "content": "entailment"}}]}
//	{"choices": [{"text": "entailment"}]}        (legacy: text field)
//	{"content": "entailment"}                     (single-string shape, Anthropic-via-adapter)
//
// The reply is parsed case-insensitively with light whitespace trimming.
// Recognized canonical words map to the 3-label space with confidence 1.0
// (the model committed to one word; we trust it). Any other content
// (explanation, multi-word, JSON wrapper, etc.) → ErrProviderBadResponse.
func parseChatCompletionResponse(body []byte) (Label, float64, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", 0, fmt.Errorf("%w: empty body", ErrProviderBadResponse)
	}

	// Try the OpenAI choices[].message.content shape first.
	var openaiShape struct {
		Choices []struct {
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

// parseCanonicalLabel maps a model's reply (case-insensitive, whitespace-
// tolerant) to the canonical 3-label space. Confidence is 1.0 — the
// model committed to one word; we trust the commitment.
//
// Recognized canonical words: "entailment", "contradiction", "neutral".
// Common variants (e.g. "entail", "support", "refute", "yes", "no")
// are NOT recognized — we keep the contract strict so an ambiguous
// reply surfaces as ErrProviderBadResponse instead of silently mapping
// to the wrong label.
func parseCanonicalLabel(raw string) (Label, float64, error) {
	word := strings.ToLower(strings.TrimSpace(raw))
	// Strip surrounding punctuation that some models add despite the
	// system prompt asking them not to.
	word = strings.Trim(word, `"'.!?,;:`)
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