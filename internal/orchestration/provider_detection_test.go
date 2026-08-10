package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// unsetAllLLMKeys clears every LLM-related env var so each detection
// test starts from a clean slate. t.Setenv with "" restores the
// original (usually unset) value after the test.
func unsetAllLLMKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DARK_JUDGE_PROVIDER", "DARK_JUDGE_DIALECT",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"DEEPSEEK_API_KEY", "MINIMAX_API_KEY", "MOONSHOT_API_KEY",
		"ZAI_API_KEY", "DASHSCOPE_API_KEY",
		"DARK_DRIFT_JUDGE_DAEMON_URL", "DARK_SCRAPPER_URL",
		"DARK_JUDGE_MODEL_DEEPSEEK", "DARK_JUDGE_MODEL_MINIMAX",
		"DARK_JUDGE_MODEL_ANTHROPIC",
	} {
		t.Setenv(k, "")
	}
}

// TestNewSelfHarnessClient_NoKeys verifies the no-key path still
// returns ErrNoLLMAvailable.
func TestNewSelfHarnessClient_NoKeys(t *testing.T) {
	unsetAllLLMKeys(t)
	client, err := NewSelfHarnessClient()
	if err == nil {
		t.Fatalf("expected ErrNoLLMAvailable, got client %+v", client)
	}
	if !strings.Contains(err.Error(), "no LLM available") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNewSelfHarnessClient_DetectsEveryProvider verifies each catalog
// env key maps to the right provider + dialect + default model.
func TestNewSelfHarnessClient_DetectsEveryProvider(t *testing.T) {
	cases := []struct {
		envKey, provider, dialect, model string
	}{
		{"ANTHROPIC_API_KEY", "anthropic", "anthropic", "claude-sonnet-4-5"},
		{"OPENAI_API_KEY", "openai", "openai", "gpt-5"},
		{"GEMINI_API_KEY", "google", "openai", "gemini-3.6-flash"},
		{"DEEPSEEK_API_KEY", "deepseek", "openai", "deepseek-v4-flash"},
		{"MINIMAX_API_KEY", "minimax", "openai", "MiniMax-M3"},
		{"MOONSHOT_API_KEY", "moonshot", "openai", "kimi-k3"},
		{"ZAI_API_KEY", "zhipu", "openai", "glm-5.2"},
		{"DASHSCOPE_API_KEY", "qwen", "openai", "qwen3.8-max"},
	}
	for _, c := range cases {
		t.Run(c.envKey, func(t *testing.T) {
			unsetAllLLMKeys(t)
			t.Setenv(c.envKey, "test-key-123")
			client, err := NewSelfHarnessClient()
			if err != nil {
				t.Fatalf("NewSelfHarnessClient: %v", err)
			}
			if client.provider != c.provider {
				t.Errorf("provider = %q, want %q", client.provider, c.provider)
			}
			if string(client.dialect) != c.dialect {
				t.Errorf("dialect = %q, want %q", client.dialect, c.dialect)
			}
			if client.model != c.model {
				t.Errorf("model = %q, want %q", client.model, c.model)
			}
			if client.key != "test-key-123" {
				t.Errorf("key not propagated")
			}
		})
	}
}

// TestNewSelfHarnessClient_Precedence verifies detection order: an
// earlier catalog provider wins when multiple keys are set.
func TestNewSelfHarnessClient_Precedence(t *testing.T) {
	unsetAllLLMKeys(t)
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")
	client, err := NewSelfHarnessClient()
	if err != nil {
		t.Fatalf("NewSelfHarnessClient: %v", err)
	}
	if client.provider != "openai" {
		t.Errorf("provider = %q, want openai (ANTHROPIC absent, OPENAI first)", client.provider)
	}
}

// TestNewSelfHarnessClient_ProviderOverride verifies
// DARK_JUDGE_PROVIDER pins a catalog provider even when other keys
// are present.
func TestNewSelfHarnessClient_ProviderOverride(t *testing.T) {
	unsetAllLLMKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("DARK_JUDGE_PROVIDER", "deepseek")
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")
	client, err := NewSelfHarnessClient()
	if err != nil {
		t.Fatalf("NewSelfHarnessClient: %v", err)
	}
	if client.provider != "deepseek" {
		t.Errorf("provider = %q, want deepseek (override)", client.provider)
	}
	if client.baseURL != "https://api.deepseek.com" {
		t.Errorf("baseURL = %q, want deepseek OpenAI base", client.baseURL)
	}
}

// TestNewSelfHarnessClient_OverrideUnknown verifies an unknown
// DARK_JUDGE_PROVIDER fails with a helpful list.
func TestNewSelfHarnessClient_OverrideUnknown(t *testing.T) {
	unsetAllLLMKeys(t)
	t.Setenv("DARK_JUDGE_PROVIDER", "not-a-provider")
	_, err := NewSelfHarnessClient()
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "not in the provider catalog") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNewSelfHarnessClient_OverrideMissingKey verifies
// DARK_JUDGE_PROVIDER without the matching env key fails cleanly.
func TestNewSelfHarnessClient_OverrideMissingKey(t *testing.T) {
	unsetAllLLMKeys(t)
	t.Setenv("DARK_JUDGE_PROVIDER", "deepseek")
	_, err := NewSelfHarnessClient()
	if err == nil {
		t.Fatal("expected error when provider pinned but key missing")
	}
	if !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNewSelfHarnessClient_ModelOverride verifies
// DARK_JUDGE_MODEL_<PROVIDER> overrides the catalog default.
func TestNewSelfHarnessClient_ModelOverride(t *testing.T) {
	unsetAllLLMKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "k")
	t.Setenv("DARK_JUDGE_MODEL_DEEPSEEK", "deepseek-v4-pro")
	client, err := NewSelfHarnessClient()
	if err != nil {
		t.Fatalf("NewSelfHarnessClient: %v", err)
	}
	if client.model != "deepseek-v4-pro" {
		t.Errorf("model = %q, want deepseek-v4-pro", client.model)
	}
}

// TestJudgeViaOpenAIHTTP_RequestShapeAndParse verifies the Chat
// Completions dialect: request path, auth header, system message
// prepended, and choices[0].message.content parsing.
func TestJudgeViaOpenAIHTTP_RequestShapeAndParse(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"verdict\":\"aligned\",\"confidence\":0.95}"}}],"model":"deepseek-v4-flash"}`))
	}))
	defer srv.Close()

	client := &SelfHarnessClient{
		provider: "deepseek",
		model:    "deepseek-v4-flash",
		key:      "sk-test",
		dialect:  DialectOpenAI,
		baseURL:  srv.URL,
	}

	resp, err := client.Judge(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "artifact text",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}

	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q, want Bearer sk-test", gotAuth)
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok {
		t.Fatalf("messages not a list: %T", gotBody["messages"])
	}
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2 (system + user)", len(msgs))
	}
	sys, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] not an object: %T", msgs[0])
	}
	if sys["role"] != "system" {
		t.Errorf("messages[0].role = %v, want system", sys["role"])
	}
	if !strings.Contains(sys["content"].(string), "LLM-as-judge") {
		t.Errorf("system prompt missing default drift_judge template")
	}
	user, ok := msgs[1].(map[string]any)
	if !ok {
		t.Fatalf("messages[1] not an object: %T", msgs[1])
	}
	if user["content"] != "artifact text" {
		t.Errorf("user content = %v, want artifact text", user["content"])
	}

	if !strings.Contains(resp.VerdictJSON, `"verdict":"aligned"`) {
		t.Errorf("verdict = %q, want aligned JSON", resp.VerdictJSON)
	}
	if resp.Provider != "deepseek" {
		t.Errorf("provider = %q, want deepseek", resp.Provider)
	}
}

// TestJudgeViaOpenAIHTTP_Non200 verifies non-200 responses surface
// the status error (no silent verdict).
func TestJudgeViaOpenAIHTTP_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	client := &SelfHarnessClient{
		provider: "deepseek",
		model:    "deepseek-v4-flash",
		key:      "sk-bad",
		dialect:  DialectOpenAI,
		baseURL:  srv.URL,
	}

	_, err := client.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err == nil {
		t.Fatal("expected 401 error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRecommendedModels_Updated2026Q3 verifies the catalog-aligned
// model picks for the providers that changed in v2.13.0.
func TestRecommendedModels_Updated2026Q3(t *testing.T) {
	cases := []struct {
		provider, evalType, want string
	}{
		{"deepseek", "drift_judge", "deepseek-v4-pro"},
		{"deepseek", "brand_match", "deepseek-v4-flash"},
		{"minimax", "drift_judge", "MiniMax-M3"},
		{"zhipu", "drift_judge", "glm-5.2"},
		{"moonshot", "drift_judge", "kimi-k3"},
		{"qwen", "drift_judge", "qwen3.8-max"},
		{"qwen", "pii_detect", "qwen3.7-flash"},
		{"google", "drift_judge", "gemini-3.6-pro"},
	}
	for _, c := range cases {
		got := RecommendedModel(c.provider, c.evalType)
		if got != c.want {
			t.Errorf("RecommendedModel(%s, %s) = %q, want %q", c.provider, c.evalType, got, c.want)
		}
	}
}

// TestListProviders_CatalogAligned verifies ListProviders mirrors the
// catalog (no stale providers).
func TestListProviders_CatalogAligned(t *testing.T) {
	providers := ListProviders()
	catalog := catalogProviderIDs()
	if len(providers) != len(catalog) {
		t.Fatalf("ListProviders len = %d, catalog len = %d", len(providers), len(catalog))
	}
	for i := range catalog {
		if providers[i] != catalog[i] {
			t.Errorf("ListProviders[%d] = %q, catalog[%d] = %q", i, providers[i], i, catalog[i])
		}
	}
}
