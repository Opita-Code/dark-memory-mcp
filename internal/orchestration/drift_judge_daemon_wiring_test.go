// drift_judge_daemon_wiring_test.go - Wave 3.5 integration test for the
// SelfHarnessClient drift_judge_daemon wiring patch.
//
// Verifies (NO mocking inside the package, NO test-only stubs):
//
//   1. judgeViaDriftJudgeDaemon builds a valid Anthropic-format request and
//      sends it to DARK_DRIFT_JUDGE_DAEMON_URL/v1/messages.
//   2. Bearer ds-managed + x-api-key ds-managed headers are set.
//   3. Anthropic-format response (content[0].text) is mapped to
//      JudgeResponse.VerdictJSON.
//   4. mock-llm minimal format ({text: "..."}) is also accepted.
//   5. Confidence is extracted from the verdict JSON when present.
//   6. Non-200 response (daemon "pool empty" 503) is surfaced as
//      a real error, NOT swallowed as ErrNoLLMAvailable (preserves
//      the visibility invariant from the original source comment).
//   7. URL with no host, no scheme, or empty value is rejected.
//   8. SelfHarnessClient.Judge dispatches drift_judge_daemon correctly
//      and still returns ErrNoLLMAvailable for anthropic/openai/google.
//
// These tests run against an httptest.Server; they do NOT require
// the [drift-judge-daemon] to be running.
package orchestration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestJudgeViaDriftJudgeDaemon_AnthropicFormat verifies the canonical
// Anthropic Messages API response shape is parsed correctly.
func TestJudgeViaDriftJudgeDaemon_AnthropicFormat(t *testing.T) {
	var capturedPath, capturedAuth, capturedXAPIKey, capturedAnthropic string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedXAPIKey = r.Header.Get("x-api-key")
		capturedAnthropic = r.Header.Get("anthropic-version")
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "claude-opus-4-7",
			"content": [{"type": "text", "text": "{\"verdict\":\"aligned\",\"confidence\":0.92,\"reasoning\":\"matches spec\"}"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 100, "output_tokens": 30}
		}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL, model: "claude-opus-4-7"}
	resp, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "sample content",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request assertions
	if capturedPath != "/v1/messages" {
		t.Errorf("path: want /v1/messages, got %q", capturedPath)
	}
	if capturedAuth != "Bearer ds-managed" {
		t.Errorf("Authorization: want Bearer ds-managed, got %q", capturedAuth)
	}
	if capturedXAPIKey != "ds-managed" {
		t.Errorf("x-api-key: want ds-managed, got %q", capturedXAPIKey)
	}
	if capturedAnthropic != "2023-06-01" {
		t.Errorf("anthropic-version: want 2023-06-01, got %q", capturedAnthropic)
	}

	// Body assertions: Anthropic format
	var sent map[string]any
	if err := json.Unmarshal(capturedBody, &sent); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if sent["model"] != "claude-opus-4-7" {
		t.Errorf("model: want claude-opus-4-7, got %v", sent["model"])
	}
	if _, ok := sent["messages"]; !ok {
		t.Error("messages field missing")
	}
	if _, ok := sent["system"]; !ok {
		t.Error("system field missing")
	}

	// Response assertions
	if resp.Provider != "drift_judge_daemon" {
		t.Errorf("Provider: want drift_judge_daemon, got %q", resp.Provider)
	}
	if resp.Model != "claude-opus-4-7" {
		t.Errorf("Model: want claude-opus-4-7 (from response), got %q", resp.Model)
	}
	if !strings.Contains(resp.VerdictJSON, `"verdict":"aligned"`) {
		t.Errorf("VerdictJSON missing verdict: %q", resp.VerdictJSON)
	}
	if resp.Confidence < 0.91 || resp.Confidence > 0.93 {
		t.Errorf("Confidence: want ~0.92 (extracted from JSON), got %v", resp.Confidence)
	}
}

// TestJudgeViaDriftJudgeDaemon_MockLLMFormat verifies the minimal mock-llm
// response shape ({text:"..."}) is accepted (verify-llm-pipeline.ps1
// stage 4 uses this format).
func TestJudgeViaDriftJudgeDaemon_MockLLMFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text": "{\"verdict\":\"aligned\",\"confidence\":0.99}"}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL}
	resp, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "x",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.VerdictJSON, `"verdict":"aligned"`) {
		t.Errorf("VerdictJSON missing verdict: %q", resp.VerdictJSON)
	}
	if resp.Confidence < 0.98 {
		t.Errorf("Confidence: want ~0.99, got %v", resp.Confidence)
	}
}

// TestJudgeViaDriftJudgeDaemon_PoolEmpty503 verifies that a 503 (daemon
// pool drained) is surfaced as a real error, NOT ErrNoLLMAvailable.
// This preserves the original source philosophy of surfacing failures
// instead of silently degrading.
func TestJudgeViaDriftJudgeDaemon_PoolEmpty503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"all keys failed"}}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL}
	_, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "x",
	})
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("error should mention HTTP 503, got: %v", err)
	}
	if !strings.Contains(err.Error(), "all keys failed") {
		t.Errorf("error should include daemon message, got: %v", err)
	}
}

// TestJudgeViaDriftJudgeDaemon_RejectsBadURLs verifies URL validation.
func TestJudgeViaDriftJudgeDaemon_RejectsBadURLs(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"file scheme (R5-like injection attempt)", "file:///etc/passwd"},
		{"no scheme", "127.0.0.1:9000"},
		{"no host", "http:///foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &SelfHarnessClient{provider: "drift_judge_daemon", key: tc.url}
			_, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
				EvalType: "drift_judge",
				Content:  "x",
			})
			if err == nil {
				t.Errorf("expected error for URL %q, got nil", tc.url)
			}
		})
	}
}

// TestJudge_DispatchesDriftJudgeDaemon verifies the dispatch layer.
func TestJudge_DispatchesDriftJudgeDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"verdict\":\"aligned\"}"}],"model":"x"}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL}
	resp, err := c.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.Provider != "drift_judge_daemon" {
		t.Errorf("Provider: want drift_judge_daemon, got %q", resp.Provider)
	}
}

// TestJudge_OtherProvidersStillStubs verifies the negative test from
// the original source comment: anthropic/openai/google still return
// ErrNoLLMAvailable, do NOT silently return fake verdicts.
func TestJudge_OtherProvidersStillStubs(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "google"} {
		t.Run(provider, func(t *testing.T) {
			c := &SelfHarnessClient{provider: provider, key: "anything"}
			_, err := c.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
			if err == nil {
				t.Errorf("%s: expected ErrNoLLMAvailable, got nil", provider)
			}
			if !strings.Contains(err.Error(), "Wave 4") {
				t.Errorf("%s: error should mention Wave 4 deferral: %v", provider, err)
			}
		})
	}
}

// TestJudge_NilOrEmptyProvider still returns ErrNoLLMAvailable.
func TestJudge_NilOrEmptyProvider(t *testing.T) {
	c := &SelfHarnessClient{provider: ""}
	_, err := c.Judge(context.Background(), JudgeRequest{EvalType: "x", Content: "x"})
	if err == nil {
		t.Error("expected error for empty provider")
	}
}

// TestDefaultSystemForEval_AllBranches ensures every eval_type has
// a system prompt (no panic on unknown eval_type).
func TestDefaultSystemForEval_AllBranches(t *testing.T) {
	types := []string{
		"drift_judge", "brand_match", "compliance_check",
		"pii_detect", "prompt_injection_scan", "grounding_check",
		"unknown_type",
	}
	for _, et := range types {
		t.Run(et, func(t *testing.T) {
			s := defaultSystemForEval(et)
			if s == "" {
				t.Errorf("system prompt empty for %s", et)
			}
			if !strings.Contains(s, "JSON") {
				t.Errorf("system prompt for %s should mention JSON output", et)
			}
		})
	}
}

// TestExtractConfidence verifies confidence extraction from verdict JSON.
func TestExtractConfidence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float32
	}{
		{"valid", `{"verdict":"x","confidence":0.85}`, 0.85},
		{"no confidence field", `{"verdict":"x"}`, 0},
		{"out of range high", `{"confidence":1.5}`, 0},
		{"out of range negative", `{"confidence":-0.1}`, 0},
		{"invalid JSON", `not json`, 0},
		{"empty", ``, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractConfidence(tc.in)
			if got != tc.want {
				t.Errorf("extractConfidence(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}