// llm_client_m3thinking_test.go — spec 1198 regression tests.
//
// The m3-thinking bug (2026-08-15): MiniMax-M3 via the Anthropic-
// compatible dialect with `thinking: {"type":"adaptive"}` returns the
// reasoning as a content block BEFORE the text block:
//
//	"content": [
//	  {"type":"thinking","thinking":"...","signature":"..."},
//	  {"type":"text","text":"{...verdict json...}"}
//	]
//
// The old parser read resp.Content[0].Text, which is EMPTY for a
// thinking block → empty VerdictJSON → default confidence 0.7 →
// `unknown` verdict from parseVerdict → spurious drift_detected.
//
// These tests lock the fix: the parser must pick the FIRST type=text
// block and the request builder must send thinking:adaptive for
// MiniMax providers.
package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestJudgeViaHTTP_ExtractsTextAfterThinkingBlock is the exact spec
// 1198 regression: a MiniMax-M3 response with a thinking block first
// must yield the verdict from the following text block, not the empty
// thinking block.
func TestJudgeViaHTTP_ExtractsTextAfterThinkingBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"test-1","type":"message","role":"assistant","model":"MiniMax-M3",
			"content":[
				{"type":"thinking","thinking":"Let me think about this carefully.","signature":"abc123"},
				{"type":"text","text":"{\"verdict\":\"aligned\",\"confidence\":0.95,\"reasoning\":\"matches spec\"}"}
			]
		}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "minimax", baseURL: srv.URL, model: "MiniMax-M3"}
	resp, err := c.judgeViaHTTP(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "some artifact text",
	}, srv.URL, "test-key")
	if err != nil {
		t.Fatalf("judgeViaHTTP() error: %v", err)
	}
	if !strings.Contains(resp.VerdictJSON, `"aligned"`) {
		t.Fatalf("VerdictJSON should contain the text-block verdict, got: %q", resp.VerdictJSON)
	}
	if resp.Confidence != 0.95 {
		t.Fatalf("confidence should come from the text block (0.95), got %v", resp.Confidence)
	}
}

// TestJudgeViaHTTP_ThinkingOnlyBlockFallback is the spec 1205 P0-bis
// regression: MiniMax-M3 can return content with ONLY a thinking block
// (no text block — rate-limit truncation or mid-reasoning cutoff). The
// pre-1205 parser left VerdictJSON empty → the pipeline read a spurious
// drift_detected (pre-45ab300 fail-safe). Now the thinking text becomes
// the VerdictJSON so the judge's reasoning survives.
func TestJudgeViaHTTP_ThinkingOnlyBlockFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"test-2","type":"message","role":"assistant","model":"MiniMax-M3",
			"content":[
				{"type":"thinking","thinking":"verdict: needs_human\nreasoning: insufficient information to ground verdict","signature":"abc456"}
			]
		}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "minimax", baseURL: srv.URL, model: "MiniMax-M3"}
	resp, err := c.judgeViaHTTP(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "some artifact text",
	}, srv.URL, "test-key")
	if err != nil {
		t.Fatalf("judgeViaHTTP() error: %v", err)
	}
	if resp.VerdictJSON == "" {
		t.Fatalf("VerdictJSON must NOT be empty when only a thinking block exists")
	}
	if !strings.Contains(resp.VerdictJSON, "needs_human") {
		t.Fatalf("VerdictJSON should carry the thinking-block verdict, got: %q", resp.VerdictJSON)
	}
	// The pipeline's parseVerdict must read the YAML verdict out of it.
	if got := parseVerdict("drift_judge", resp.VerdictJSON, resp.Confidence); got != "needs_human" {
		t.Fatalf("parseVerdict(thinking-only) = %q, want needs_human", got)
	}
}

// TestJudgeViaHTTP_SendsThinkingAdaptiveForMiniMax verifies the
// request body carries thinking:{"type":"adaptive"} when the provider
// is minimax (m3-thinking), and that non-MiniMax providers do NOT get
// the field (backwards compatibility).
func TestJudgeViaHTTP_SendsThinkingAdaptiveForMiniMax(t *testing.T) {
	var sawThinking atomic.Int32
	var sawMaxTokens atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Thinking *struct {
				Type string `json:"type"`
			} `json:"thinking"`
			MaxTokens int `json:"max_tokens"`
		}
		if err := jsonUnmarshalBody(r, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Thinking != nil && body.Thinking.Type == "adaptive" {
			sawThinking.Add(1)
		}
		if body.MaxTokens == 4096 {
			sawMaxTokens.Add(1)
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"verdict\":\"aligned\",\"confidence\":0.9}"}],"model":"MiniMax-M3"}`))
	}))
	defer srv.Close()

	// MiniMax provider → thinking:adaptive must be sent.
	c := &SelfHarnessClient{provider: "minimax", baseURL: srv.URL, model: "MiniMax-M3"}
	if _, err := c.judgeViaHTTP(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"}, srv.URL, "test-key"); err != nil {
		t.Fatalf("minimax judgeViaHTTP() error: %v", err)
	}
	if sawThinking.Load() != 1 {
		t.Fatal("minimax provider should send thinking:{\"type\":\"adaptive\"}")
	}
	// spec 1205 P0-bis: m3-thinking writes prose reasoning into the
	// thinking block BEFORE the JSON text block; 1024 max_tokens
	// truncates mid-reasoning and the text block never materialises.
	// MiniMax must get 4096 so the final JSON verdict survives.
	if sawMaxTokens.Load() != 1 {
		t.Fatal("minimax provider should send max_tokens=4096 (m3-thinking reasoning + JSON verdict budget)")
	}

	// Non-MiniMax provider → thinking must NOT be sent.
	sawThinking.Store(0)
	sawMaxTokens.Store(0)
	c2 := &SelfHarnessClient{provider: "anthropic", baseURL: srv.URL, model: "claude-sonnet-4-5"}
	if _, err := c2.judgeViaHTTP(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"}, srv.URL, "test-key"); err != nil {
		t.Fatalf("anthropic judgeViaHTTP() error: %v", err)
	}
	if sawThinking.Load() != 0 {
		t.Fatal("anthropic provider must NOT send thinking:adaptive (response shape unchanged)")
	}
	if sawMaxTokens.Load() != 0 {
		t.Fatal("anthropic provider must keep max_tokens=1024 (legacy compact JSON verdict)")
	}
}

// jsonUnmarshalBody decodes the request body from an httptest handler.
func jsonUnmarshalBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}
