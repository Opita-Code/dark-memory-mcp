// Package chatwire holds the wire-level concerns that EVERY
// chat-completions client in this MCP shares: how to size the
// max_tokens budget for a model, how to strip a reasoning block, and
// how to unwrap the OpenAI-compatible response envelope.
//
// It exists because those concerns are not the property of one client.
// The NLI chain (internal/nli) and the LLM judge
// (internal/v4alpha/judge/v4judge) talk to the same endpoints and hit
// the same provider behaviours. When each kept its own copy, the judge
// kept a hardcoded max_tokens of 512 and failed on EVERY reasoning
// model: MiniMax-M3 spends the whole budget on its <think> block and
// answers with finish_reason="length" and no verdict at all. The
// verdict then degraded to the NLI fallback, and when that was not
// bound either, to needs_human — a verdict nobody ever read as a
// model judgement.
//
// Layering: internal/llm/chatwire imports NOTHING from the MCP. Both
// internal/nli and internal/v4alpha/judge/v4judge import it, and they
// still do not import each other.
package chatwire

import (
	"encoding/json"
	"strings"
)

// Model family classification used by the max_tokens table.
const (
	// FamilyReasoning: the model always emits an internal thinking
	// block before the answer.
	FamilyReasoning = "reasoning"
	// FamilyNonReasoning: the model answers directly.
	FamilyNonReasoning = "non-reasoning"
	// FamilyHybrid: reasoning is opt-in (e.g. GPT-5).
	FamilyHybrid = "hybrid"
	// FamilyUnknown: not in the table.
	FamilyUnknown = "unknown"
)

// ModelMaxTokensEntry is one row of the per-model max_tokens table.
type ModelMaxTokensEntry struct {
	// Pattern is matched as a prefix against the model revision.
	Pattern string
	// MaxTokens is the budget sent to the provider.
	MaxTokens int
	// Family is one of the Family* constants.
	Family string
}

// DefaultFallbackMaxTokens is the budget used when no pattern matches
// and no override is set.
const DefaultFallbackMaxTokens = 1024

// MaxRetryBudgetCap is the hard ceiling on a retry budget. Above 8k
// tokens, model APIs hit timeout / network limits, so a retry never
// exceeds it.
const MaxRetryBudgetCap = 8192

// RetryBudgetMultiplier is how much the budget grows when a reasoning
// model truncated its answer.
const RetryBudgetMultiplier = 4

// modelMaxTokens is the sealed per-model table.
//
// Sizing rationale:
//   - Non-reasoning models: tight budgets (256-1024) to prevent prose.
//   - Reasoning models: the thinking block is INVISIBLE output, so the
//     budget must cover thinking AND answer. 4× the visible output is
//     the rule of thumb, with a safety margin.
//   - The last entry (empty pattern) is the catch-all.
//
// Adding a row: append here + add a case to the matrix test +
// mention it in the CHANGELOG.
var modelMaxTokens = []ModelMaxTokensEntry{
	// Anthropic Claude (extended thinking + legacy).
	{Pattern: "claude-opus-4-5", MaxTokens: 2048, Family: FamilyReasoning},
	{Pattern: "claude-opus-4-6", MaxTokens: 2048, Family: FamilyReasoning},
	{Pattern: "claude-sonnet-4", MaxTokens: 2048, Family: FamilyReasoning},
	{Pattern: "claude-haiku-4-5", MaxTokens: 1024, Family: FamilyReasoning},
	{Pattern: "claude-3-7-sonnet", MaxTokens: 1024, Family: FamilyReasoning},

	// DeepSeek (R1 always reasons; V3.x + flash do not).
	{Pattern: "deepseek-r1", MaxTokens: 4096, Family: FamilyReasoning},
	{Pattern: "deepseek-reasoner", MaxTokens: 4096, Family: FamilyReasoning},
	{Pattern: "deepseek-v3", MaxTokens: 1024, Family: FamilyNonReasoning},
	{Pattern: "deepseek-flash", MaxTokens: 512, Family: FamilyNonReasoning},

	// OpenAI o-series (reasoning_effort controls).
	{Pattern: "o3-mini", MaxTokens: 4096, Family: FamilyReasoning},
	{Pattern: "o4-mini", MaxTokens: 4096, Family: FamilyReasoning},
	{Pattern: "o3", MaxTokens: 8192, Family: FamilyReasoning},
	{Pattern: "o1", MaxTokens: 8192, Family: FamilyReasoning},

	// OpenAI non-reasoning.
	{Pattern: "gpt-4o", MaxTokens: 256, Family: FamilyNonReasoning},
	{Pattern: "gpt-4o-mini", MaxTokens: 256, Family: FamilyNonReasoning},
	{Pattern: "gpt-5", MaxTokens: 1024, Family: FamilyHybrid},

	// MiniMax (emits <think> blocks).
	{Pattern: "MiniMax-M3", MaxTokens: 1024, Family: FamilyReasoning},
	{Pattern: "MiniMax-M2", MaxTokens: 1024, Family: FamilyReasoning},

	// Catch-all (must be last).
	{Pattern: "", MaxTokens: 1024, Family: FamilyUnknown},
}

// ResolveMaxTokens returns the max_tokens budget to send for the given
// model revision, honoring (in order):
//  1. override > 0 → the operator's escape hatch.
//  2. the longest-prefix match in the table.
//  3. DefaultFallbackMaxTokens.
//
// Pure: no I/O.
func ResolveMaxTokens(modelRev string, override int) int {
	if override > 0 {
		return override
	}
	bestMatch := ""
	bestTokens := DefaultFallbackMaxTokens
	for _, entry := range modelMaxTokens {
		if entry.Pattern == "" {
			continue
		}
		if strings.HasPrefix(modelRev, entry.Pattern) && len(entry.Pattern) > len(bestMatch) {
			bestMatch = entry.Pattern
			bestTokens = entry.MaxTokens
		}
	}
	return bestTokens
}

// RetryBudget returns the budget for a second attempt after the model
// truncated its answer: RetryBudgetMultiplier times the base budget,
// capped at MaxRetryBudgetCap.
func RetryBudget(base int) int {
	if base <= 0 {
		base = DefaultFallbackMaxTokens
	}
	next := base * RetryBudgetMultiplier
	if next > MaxRetryBudgetCap {
		return MaxRetryBudgetCap
	}
	return next
}

// IsReasoningModel reports whether the model revision belongs to the
// reasoning family (it spends part of its budget on an internal
// thinking block).
func IsReasoningModel(modelRev string) bool {
	bestMatch := ""
	family := FamilyUnknown
	for _, entry := range modelMaxTokens {
		if entry.Pattern == "" {
			continue
		}
		if strings.HasPrefix(modelRev, entry.Pattern) && len(entry.Pattern) > len(bestMatch) {
			bestMatch = entry.Pattern
			family = entry.Family
		}
	}
	return family == FamilyReasoning
}

// StripThinkBlocks removes the reasoning block that reasoning models
// (MiniMax-M3, DeepSeek-R1, Claude with extended thinking) put in front
// of their answer:
//
//	<think>...reasoning...</think>
//
// The block is INVISIBLE output: it must not reach the caller as the
// answer, and it must not confuse a JSON extractor that looks for the
// first '{' — the thinking often quotes the expected JSON shape.
//
// The function is content-only: it does not look at finish_reason.
func StripThinkBlocks(raw string) string {
	for {
		start := strings.Index(raw, "<think>")
		if start < 0 {
			return raw
		}
		rest := raw[start+len("<think>"):]
		end := strings.Index(rest, "</think>")
		if end < 0 {
			// Unterminated block: the model was cut off mid-thinking.
			// Drop everything from the opening tag on.
			return raw[:start]
		}
		raw = raw[:start] + rest[end+len("</think>"):]
	}
}

// ModelMaxTokensTable returns a COPY of the per-model max_tokens table.
//
// It exists so callers that document or test the table (the NLI chain
// does) can read it without this package exporting mutable state.
func ModelMaxTokensTable() []ModelMaxTokensEntry {
	out := make([]ModelMaxTokensEntry, len(modelMaxTokens))
	copy(out, modelMaxTokens)
	return out
}

// UnwrapOpenAIShape extracts the assistant content and the finish
// reason from an OpenAI-compatible chat response:
//
//	{"choices":[{"message":{"content":"..."},"finish_reason":"stop"}]}
//
// ok is false when the body does not carry that shape (the caller
// keeps the raw body — some lightweight servers answer with the
// payload directly).
//
// The finish reason matters: "length" means the model ran out of
// budget, which for a reasoning model means it spent everything on
// internal thinking and never produced the answer.
func UnwrapOpenAIShape(body []byte) (content string, finishReason string, ok bool) {
	var shape struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Text         string `json:"text"` // legacy fallback inside the same struct
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &shape); err != nil {
		return "", "", false
	}
	if len(shape.Choices) == 0 {
		return "", "", false
	}
	content = shape.Choices[0].Message.Content
	if content == "" {
		content = shape.Choices[0].Text
	}
	if content == "" {
		return "", shape.Choices[0].FinishReason, false
	}
	return content, shape.Choices[0].FinishReason, true
}

// Truncated reports whether a finish reason means the model ran out of
// budget ("length" / "max_tokens" / "MAX_TOKENS").
func Truncated(finishReason string) bool {
	switch strings.ToLower(strings.TrimSpace(finishReason)) {
	case "length", "max_tokens", "maxtokens":
		return true
	}
	return false
}
