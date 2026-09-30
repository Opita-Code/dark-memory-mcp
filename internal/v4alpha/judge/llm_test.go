// Tests for llm.go (RealLLMClient — HTTP-based LLMClient impl).
//
// Strategy:
//   - Env var routing: t.Setenv for DARK_JUDGE_PROVIDER + per-provider
//     API keys. No real provider is contacted.
//   - Wire format dispatch: httptest.Server that records the request
//     and returns a canned response per dialect (anthropic, openai).
//   - Retry policy: server returns 503 N times then 200 (verifies
//     retry budget). Server returns 400 (verifies fail-fast).
//   - Response parsing: canned bodies for both dialects, including
//     the thinking-block-before-text case (m3-thinking, spec 1198).
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
)

// ---------- Env var routing ----------

func TestRealLLMClient_NewNoKey(t *testing.T) {
	// Clear all provider keys + DARK_JUDGE_PROVIDER.
	for _, k := range []string{
		"DARK_JUDGE_PROVIDER",
		"ANTHROPIC_API_KEY",
		"MINIMAX_API_KEY",
		"MINIMAX_API_KEY_CN",
		"DEEPSEEK_API_KEY",
	} {
		t.Setenv(k, "")
	}
	_, err := NewRealLLMClient()
	if err == nil {
		t.Fatal("NewRealLLMClient succeeded with no key; expected ErrLLMUnavailable")
	}
	if !strings.Contains(err.Error(), "no LLM key detected") {
		t.Errorf("error = %q; want 'no LLM key detected'", err.Error())
	}
}

func TestRealLLMClient_NewWithExplicitPin(t *testing.T) {
	t.Setenv("DARK_JUDGE_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "test-key-12345")
	t.Setenv("MINIMAX_API_KEY", "") // ensure minimax doesn't win auto-detect
	t.Setenv("DEEPSEEK_API_KEY", "")

	c, err := NewRealLLMClient()
	if err != nil {
		t.Fatalf("NewRealLLMClient: %v", err)
	}
	if c.provider != "anthropic" {
		t.Errorf("provider = %q; want anthropic", c.provider)
	}
	if c.key != "test-key-12345" {
		t.Errorf("key = %q; want test-key-12345", c.key)
	}
	if c.dialect != "anthropic" {
		t.Errorf("dialect = %q; want anthropic", c.dialect)
	}
}

func TestRealLLMClient_NewAutoDetect_Minimax(t *testing.T) {
	t.Setenv("DARK_JUDGE_PROVIDER", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "minimax-test-key")
	t.Setenv("MINIMAX_API_KEY_CN", "")
	t.Setenv("DEEPSEEK_API_KEY", "")

	c, err := NewRealLLMClient()
	if err != nil {
		t.Fatalf("NewRealLLMClient: %v", err)
	}
	if c.provider != "minimax" {
		t.Errorf("provider = %q; want minimax", c.provider)
	}
}

func TestRealLLMClient_UnsupportedProvider(t *testing.T) {
	// ADR-009 (Phase 3): openai is now in the supported list, so
	// pick a provider that isn't (mistral — not in the canonical
	// catalog). Verify the unsupported-provider error path.
	t.Setenv("DARK_JUDGE_PROVIDER", "mistral") // not in supportedProviderIDs
	_, err := NewRealLLMClient()
	if err == nil {
		t.Fatal("expected error for unsupported provider pin")
	}
	if !strings.Contains(err.Error(), "not in supportedProviderIDs") {
		t.Errorf("error = %q; expected unsupported provider message", err.Error())
	}
}

// llmDialectAnthropic is a tiny indirection so the tests don't have
// to import internal/llm directly. Avoids the import name collision
// when the package already imports it elsewhere.
func llmDialectAnthropic() string { return "anthropic" }
func llmDialectOpenAI() string    { return "openai" }

// keep helpers referenced (compile-time check that the package
// imports the right symbols).
var _ = llmDialectAnthropic
var _ = llmDialectOpenAI

// ---------- Wire format dispatch (httptest.Server) ----------

func TestRealLLMClient_Complete_Anthropic(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAuth += "|" + r.Header.Get("x-api-key")
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"{\"reasoning\":\"ok\",\"criteria\":[{\"name\":\"correctness\",\"score\":0.9,\"note\":\"good\"}]}"}],
			"model":"claude-test",
			"stop_reason":"end_turn"
		}`))
	}))
	defer srv.Close()

	c := mustRealClient(t)
	c.baseURL = srv.URL
	c.dialect = "anthropic"

	resp, err := c.Complete(context.Background(), LLMRequest{
		SystemPrompt:     "you are a judge",
		UserPrompt:       "evaluate this",
		StructuredInputs: []Evidence{{Source: "file://x.go:1-3", Snippet: "package x", Relevance: 0.9}},
		MaxTokens:        1024,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "anthropic" {
		t.Errorf("resp.Provider = %q; want anthropic", resp.Provider)
	}
	if resp.Model != "claude-test" {
		t.Errorf("resp.Model = %q; want claude-test", resp.Model)
	}
	if !strings.Contains(resp.Content, "criteria") {
		t.Errorf("resp.Content missing 'criteria': %s", resp.Content)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("URL path = %q; want /v1/messages", gotPath)
	}
	if !strings.Contains(gotAuth, "Bearer test-key") {
		t.Errorf("auth header missing Bearer test-key: %q", gotAuth)
	}
	if !strings.Contains(string(gotBody), "structured_inputs") &&
		!strings.Contains(string(gotBody), "Structured Evidence") {
		t.Errorf("request body missing structured evidence: %s", gotBody)
	}
}

func TestRealLLMClient_Complete_OpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"role":"system"`) {
			t.Errorf("OpenAI body missing system message: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{"content":"{\"reasoning\":\"ok\",\"criteria\":[{\"name\":\"correctness\",\"score\":0.9}]}"},
				"finish_reason":"stop"
			}],
			"model":"gpt-test"
		}`))
	}))
	defer srv.Close()

	c := mustRealClient(t)
	c.provider = "openai" // match dialect so resp.Provider is correct
	c.baseURL = srv.URL
	c.dialect = "openai"

	resp, err := c.Complete(context.Background(), LLMRequest{
		SystemPrompt: "you are a judge",
		UserPrompt:   "evaluate this",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "openai" {
		t.Errorf("resp.Provider = %q; want openai", resp.Provider)
	}
	if !strings.Contains(resp.Content, "criteria") {
		t.Errorf("resp.Content missing 'criteria'")
	}
}

func TestRealLLMClient_Complete_ThinkingBlockFirst(t *testing.T) {
	// m3-thinking (spec 1198): thinking block before text block.
	// Verdict must come from the TEXT block, not the thinking block.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"content":[
				{"type":"thinking","thinking":"let me think..."},
				{"type":"text","text":"{\"reasoning\":\"verdict\",\"criteria\":[]}"}
			],
			"model":"minimax-m3",
			"stop_reason":"end_turn"
		}`))
	}))
	defer srv.Close()

	c := mustRealClient(t)
	c.baseURL = srv.URL
	c.dialect = "anthropic"
	c.provider = "minimax"

	resp, err := c.Complete(context.Background(), LLMRequest{UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(resp.Content, "verdict") {
		t.Errorf("resp.Content missing 'verdict': %s", resp.Content)
	}
	if !resp.NonDeterministic {
		t.Error("NonDeterministic = false for minimax provider; want true (spec 1198)")
	}
}

func TestRealLLMClient_Complete_ThinkingOnly(t *testing.T) {
	// Edge case (spec 1205 P0-bis): only thinking block, no text.
	// Parser must fall back to last thinking block.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"content":[
				{"type":"thinking","thinking":"the answer is X"}
			],
			"model":"minimax-m3"
		}`))
	}))
	defer srv.Close()

	c := mustRealClient(t)
	c.baseURL = srv.URL
	c.dialect = "anthropic"
	c.provider = "minimax"

	resp, err := c.Complete(context.Background(), LLMRequest{UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(resp.Content, "the answer is X") {
		t.Errorf("resp.Content missing thinking fallback: %s", resp.Content)
	}
}

// ---------- Retry policy ----------

func TestRealLLMClient_RetryOn5xx(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"transient"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := mustRealClient(t)
	c.baseURL = srv.URL
	c.dialect = "openai"
	c.http.Timeout = 0

	// Speed up the test by reducing retry count + base timeout.
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "2")
	t.Setenv("DARK_JUDGE_TIMEOUT_MS", "500")

	resp, err := c.Complete(context.Background(), LLMRequest{UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("resp.Content = %q; want 'ok'", resp.Content)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("attempts = %d; want 3 (2 retries + 1 success)", attempts)
	}
}

func TestRealLLMClient_NoRetryOn4xx(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid request"}`))
	}))
	defer srv.Close()

	c := mustRealClient(t)
	c.baseURL = srv.URL
	c.dialect = "openai"

	_, err := c.Complete(context.Background(), LLMRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("attempts = %d; want 1 (no retry on 4xx)", attempts)
	}
}

func TestRealLLMClient_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := mustRealClient(t)
	c.baseURL = srv.URL
	c.dialect = "openai"

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Complete(ctx, LLMRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatal("expected error on ctx timeout")
	}
}

// ---------- Persona content injection ----------

func TestEnrichSystemPrompt_LegacyPersona_Unchanged(t *testing.T) {
	base := "you are a judge"
	got := EnrichSystemPrompt("judge-logical", base)
	if got != base {
		t.Errorf("EnrichSystemPrompt for legacy persona changed base: %q", got)
	}
}

func TestEnrichSystemPrompt_V4Persona_AddsContent(t *testing.T) {
	base := "you are a judge"
	got := EnrichSystemPrompt("judge-cross-modal", base)
	if got == base {
		t.Error("EnrichSystemPrompt for v4 persona returned unchanged base")
	}
	for _, want := range []string{"cross-modal", "Evaluation Lens", "Bias Controls", "Required Evidence"} {
		if !strings.Contains(got, want) {
			t.Errorf("enriched prompt missing %q: %s", want, got)
		}
	}
	// Base must still be present (preserves the persona's specific
	// instructions on top of the generic template).
	if !strings.Contains(got, base) {
		t.Errorf("enriched prompt lost base: %s", got)
	}
}

func TestEnrichSystemPrompt_EmptyPersona_Unchanged(t *testing.T) {
	base := "base prompt"
	if got := EnrichSystemPrompt("", base); got != base {
		t.Errorf("empty personaID changed base: %q", got)
	}
}

// ---------- ADR-009 (Phase 3): provider allow-list 4 → 9 ----------

// clearAllProviderKeys removes every LLM env var so the
// auto-detect chain has nothing to find. Helper for ADR-009 tests.
func clearAllProviderKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DARK_JUDGE_PROVIDER",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GEMINI_API_KEY",
		"MINIMAX_API_KEY",
		"MINIMAX_API_KEY_CN",
		"DEEPSEEK_API_KEY",
		"ZAI_API_KEY",
		"MOONSHOT_API_KEY",
		"DASHSCOPE_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

// TestNewRealLLMClient_OpenAI — provider=openai + OPENAI_API_KEY
// resolves to dialect=OpenAI, baseURL=https://api.openai.com/v1.
func TestNewRealLLMClient_OpenAI(t *testing.T) {
	clearAllProviderKeys(t)
	t.Setenv("DARK_JUDGE_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-test-openai-12345")

	c, err := NewRealLLMClient()
	if err != nil {
		t.Fatalf("NewRealLLMClient: %v", err)
	}
	if c.provider != "openai" {
		t.Errorf("provider = %q; want openai", c.provider)
	}
	if c.dialect != llm.DialectOpenAI {
		t.Errorf("dialect = %q; want OpenAI", c.dialect)
	}
	if c.baseURL != "https://api.openai.com/v1" {
		t.Errorf("baseURL = %q; want https://api.openai.com/v1", c.baseURL)
	}
}

// TestNewRealLLMClient_Google — provider=google + GEMINI_API_KEY
// resolves to dialect=OpenAI (Google's OpenAI-compat endpoint).
func TestNewRealLLMClient_Google(t *testing.T) {
	clearAllProviderKeys(t)
	t.Setenv("DARK_JUDGE_PROVIDER", "google")
	t.Setenv("GEMINI_API_KEY", "gem-test-12345")

	c, err := NewRealLLMClient()
	if err != nil {
		t.Fatalf("NewRealLLMClient: %v", err)
	}
	if c.provider != "google" {
		t.Errorf("provider = %q; want google", c.provider)
	}
	if c.dialect != llm.DialectOpenAI {
		t.Errorf("dialect = %q; want OpenAI (Google's OpenAI-compat endpoint)", c.dialect)
	}
	if c.baseURL != "https://generativelanguage.googleapis.com/v1beta/openai/" {
		t.Errorf("baseURL = %q; want googleapis openai-compat", c.baseURL)
	}
}

// TestNewRealLLMClient_Qwen_AnthropicDialect — provider=qwen +
// DARK_JUDGE_DIALECT=anthropic + DASHSCOPE_API_KEY resolves to
// dialect=Anthropic (qwen supports both).
func TestNewRealLLMClient_Qwen_AnthropicDialect(t *testing.T) {
	clearAllProviderKeys(t)
	t.Setenv("DARK_JUDGE_PROVIDER", "qwen")
	t.Setenv("DARK_JUDGE_DIALECT", "anthropic")
	t.Setenv("DASHSCOPE_API_KEY", "dash-test-12345")

	c, err := NewRealLLMClient()
	if err != nil {
		t.Fatalf("NewRealLLMClient: %v", err)
	}
	if c.provider != "qwen" {
		t.Errorf("provider = %q; want qwen", c.provider)
	}
	if c.dialect != llm.DialectAnthropic {
		t.Errorf("dialect = %q; want Anthropic (override)", c.dialect)
	}
	if c.baseURL == "" {
		t.Errorf("baseURL empty; want qwen AnthropicBaseURL")
	}
}

// TestNewRealLLMClient_UnsupportedProvider — DARK_JUDGE_PROVIDER=
// mistral returns ErrLLMUnavailable with the 9-provider list in
// the error message.
func TestNewRealLLMClient_UnsupportedProvider(t *testing.T) {
	clearAllProviderKeys(t)
	t.Setenv("DARK_JUDGE_PROVIDER", "mistral")

	_, err := NewRealLLMClient()
	if err == nil {
		t.Fatal("NewRealLLMClient with mistral returned nil error; want ErrLLMUnavailable")
	}
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Errorf("err is not ErrLLMUnavailable: %v", err)
	}
	for _, want := range []string{
		"mistral", "anthropic", "openai", "google", "qwen", "zhipu", "moonshot", "minimax", "deepseek",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q: %s", want, err.Error())
		}
	}
}

// TestNewRealLLMClient_AutoDetect_OpenAI — no explicit pin, only
// OPENAI_API_KEY set; auto-detect resolves to openai.
func TestNewRealLLMClient_AutoDetect_OpenAI(t *testing.T) {
	clearAllProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "sk-test-12345")

	c, err := NewRealLLMClient()
	if err != nil {
		t.Fatalf("NewRealLLMClient: %v", err)
	}
	if c.provider != "openai" {
		t.Errorf("auto-detect provider = %q; want openai", c.provider)
	}
}

// ---------- Helpers ----------

// mustRealClient returns a RealLLMClient with key set, ready to be
// re-pointed at a httptest.Server URL by individual tests.
func mustRealClient(t *testing.T) *RealLLMClient {
	t.Helper()
	return &RealLLMClient{
		provider: "anthropic",
		model:    "test-model",
		key:      "test-key",
		baseURL:  "http://example.invalid",
		dialect:  "anthropic",
		http: &http.Client{
			Transport: &http.Transport{},
		},
	}
}

// ---------- Silent-import checks (compile-time only) ----------

var _ = json.Marshal // keep encoding/json import even if unused in some configs
