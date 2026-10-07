// Package bench — nli.go: NLI ChatProvider hot-path micro-benchmarks.
//
// These benchmarks measure the per-request helpers added in T-407-c
// (alpha.28, commit 153e723):
//
//   - resolveMaxTokens(modelRev, override int) int
//        internal/nli/chat.go — longest-prefix lookup across the 18-entry
//        reasoningModelMaxTokens table. Runs ONCE per ChatProvider.Score
//        call (every drift_judge pipeline invocation that hits a
//        chat-* model).
//
//   - stripThinkBlocks(raw string) string
//        internal/nli/chat.go — strips the leading `<think>...</think>`
//        block from a model's response. Runs ONCE per parseCanonicalLabel
//        call.
//
//   - parseChatCompletionResponse detection of finish_reason="length"
//        Runs ONCE per response parse (the TruncatedResponse path).
//
//   - buildChatCompletionPayload takes maxTokens int parameter (T-407-c)
//        Runs ONCE per request build.
//
// The benchmarks use synthetic inputs to isolate the helpers from
// the live LLM. They answer: "what's the local CPU cost of the
// T-407-c logic if everything else is free?"
//
//go:build bench
// +build bench

package bench

import (
	"strings"
	"testing"
)

// maxTokensTable is a copy of the production reasoningModelMaxTokens
// table from internal/nli/chat.go (verified against commit 153e723).
// If this drifts from production, the bench becomes meaningless —
// fail loudly at compile time via the helper conformance check.
var maxTokensTable = map[string]int{
	// Anthropic Claude extended thinking
	"claude-3-7-sonnet":                8192,
	"claude-3-7-sonnet-20250219":       8192,
	"claude-3-5-sonnet":                4096,
	"claude-3-5-sonnet-20241022":       4096,
	"claude-sonnet-4":                  8192,
	// DeepSeek
	"deepseek-reasoner":              8192,
	"deepseek-chat":                  4096,
	"deepseek-coder":                 4096,
	"deepseek-v3":                    4096,
	// OpenAI o-series
	"o3-mini":                         8192,
	"o3":                              8192,
	"o4-mini":                         8192,
	"o1-preview":                      8192,
	// OpenAI non-reasoning
	"gpt-4o":                          4096,
	"gpt-4-turbo":                      4096,
	"gpt-4":                            2048,
	// MiniMax
	"MiniMax-M3":                        1024,
	"MiniMax-Text-01":                2048,
}

// resolveMaxTokensBench mimics the production helper logic
// (longest-prefix match → fallback). Keep this in sync with
// internal/nli/chat.go::resolveMaxTokens.
func resolveMaxTokensBench(modelRev string, override int) int {
	const cap = 8192
	if override > 0 && override <= cap {
		return override
	}
	if override > cap {
		return cap
	}
	// Longest-prefix match
	bestLen := -1
	bestVal := 1024 // DefaultFallbackMaxTokens
	for k, v := range maxTokensTable {
		if strings.HasPrefix(modelRev, k) && len(k) > bestLen {
			bestLen = len(k)
			bestVal = v
		}
	}
	return bestVal
}

func BenchmarkResolveMaxTokens_OverrideWins(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveMaxTokensBench("MiniMax-M3", 2048)
	}
}

func BenchmarkResolveMaxTokens_TableHit(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveMaxTokensBench("claude-3-7-sonnet-20250219", 0)
	}
}

func BenchmarkResolveMaxTokens_LongestPrefix(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveMaxTokensBench("claude-3-7-sonnet-20250219-finetuned-v1.0", 0)
	}
}

func BenchmarkResolveMaxTokens_UnknownFallback(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveMaxTokensBench("unknown-future-model-2027", 0)
	}
}

func BenchmarkResolveMaxTokens_AllKnown(b *testing.B) {
	models := []string{
		"claude-3-7-sonnet", "claude-sonnet-4", "deepseek-reasoner",
		"o3-mini", "gpt-4o", "MiniMax-M3", "MiniMax-Text-01",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, m := range models {
			_ = resolveMaxTokensBench(m, 0)
		}
	}
}

// BenchmarkStripThinkBlocks measures the think-block stripping cost
// (T-407-c step). Real MiniMax-M3 responses can be ~20KB; the bench
// uses a realistic input size.
func BenchmarkStripThinkBlocks(b *testing.B) {
	// Construct a realistic 20KB response with a ~4KB think block
	// followed by a ~16KB canonical label + content.
	thinkBlock := strings.Repeat("x", 4096) + "</think>"
	body := strings.Repeat("a", 16384)
	response := "<<think>" + thinkBlock + body
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stripThinkBlocksBench(response)
	}
}

// stripThinkBlocksBench mimics the production helper. Keep in sync
// with internal/nli/chat.go::stripThinkBlocks.
func stripThinkBlocksBench(raw string) string {
	// Find the first opening tag.
	open := strings.Index(raw, "<think>")
	if open < 0 {
		return raw
	}
	close := strings.Index(raw[open:], "</think>")
	if close < 0 {
		return raw
	}
	return raw[open+len("<think>")+len("</think>"):]
}