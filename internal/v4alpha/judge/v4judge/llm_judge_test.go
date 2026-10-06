package v4judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubHTTPClient is a test-only HTTPClient. Each test injects a
// canned response (status + body). Captures the request for
// assertions (auth header, body shape).
type stubHTTPClient struct {
	responses  []stubResponse
	calls      int
	requests   []*http.Request
	bodies     [][]byte
	maxDur     time.Duration // optional: simulate slow response
	slowForced bool
}

type stubResponse struct {
	status int
	body   []byte
}

func (s *stubHTTPClient) Do(req *http.Request) (*http.Response, error) {
	s.calls++
	s.requests = append(s.requests, req)
	if s.maxDur > 0 {
		time.Sleep(s.maxDur)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	s.bodies = append(s.bodies, body)
	idx := s.calls - 1
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	resp := s.responses[idx]
	r := httptest.NewRecorder()
	r.Code = resp.status
	_, _ = r.Body.Write(resp.body)
	r.Header().Set("Content-Type", "application/json")
	out := r.Result()
	return out, nil
}

// makeJudge builds an LLMJudge with a stub HTTP client. cfg.Endpoint
// is overridden to a non-empty string (the stub doesn't dial).
func makeJudge(t *testing.T, cfg ProviderConfig, stub *stubHTTPClient) *LLMJudge {
	t.Helper()
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://stub.test/v1/chat/completions"
	}
	j, err := NewLLMJudge(cfg, stub)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	return j
}

// =====================================================================
// TestNewLLMJudge — 5 tests on validation + construction
// =====================================================================

func TestNewLLMJudge_ValidConfig(t *testing.T) {
	stub := &stubHTTPClient{}
	cfg := ProviderConfig{
		ProviderID: "judge-minimax-cn",
		Endpoint:   "https://api.minimaxi.com/v1/chat/completions",
		AuthToken:  "sk-cp-test",
		TimeoutMS:  30000,
		ModelRev:   "MiniMax-M3",
	}
	j, err := NewLLMJudge(cfg, stub)
	if err != nil {
		t.Fatalf("expected valid config, got err: %v", err)
	}
	if j.ID() != "judge-minimax-cn" {
		t.Errorf("ID() = %q, want judge-minimax-cn", j.ID())
	}
}

func TestNewLLMJudge_NilHTTPClient(t *testing.T) {
	cfg := ProviderConfig{
		ProviderID: "judge-test",
		Endpoint:   "https://x.test/v1/chat/completions",
	}
	if _, err := NewLLMJudge(cfg, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestNewLLMJudge_RejectsChatPrefix(t *testing.T) {
	// "chat-" prefix belongs to internal/nli/chat.go::ChatProvider.
	// v4judge rejects this prefix to keep the routing boundary clean.
	stub := &stubHTTPClient{}
	cfg := ProviderConfig{
		ProviderID: "chat-minimax-cn",
		Endpoint:   "https://api.minimaxi.com/v1/chat/completions",
	}
	_, err := NewLLMJudge(cfg, stub)
	if !errors.Is(err, ErrProviderIDNotJudge) {
		t.Errorf("expected ErrProviderIDNotJudge, got %v", err)
	}
}

func TestNewLLMJudge_EmptyEndpoint(t *testing.T) {
	stub := &stubHTTPClient{}
	cfg := ProviderConfig{ProviderID: "judge-test", Endpoint: ""}
	if _, err := NewLLMJudge(cfg, stub); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestNewLLMJudge_AppliesDefaults(t *testing.T) {
	stub := &stubHTTPClient{}
	cfg := ProviderConfig{
		ProviderID: "judge-test",
		Endpoint:   "https://x.test/v1/chat/completions",
		// TimeoutMS, MaxArtifactBytes, MaxSpecIntentBytes all zero
	}
	j, err := NewLLMJudge(cfg, stub)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	if j.timeout != DefaultTimeoutMS*time.Millisecond {
		t.Errorf("timeout = %v, want %v", j.timeout, DefaultTimeoutMS*time.Millisecond)
	}
	if j.maxABytes != DefaultMaxArtifactBytes {
		t.Errorf("maxABytes = %d, want %d", j.maxABytes, DefaultMaxArtifactBytes)
	}
	if j.maxSBytes != DefaultMaxSpecIntentBytes {
		t.Errorf("maxSBytes = %d, want %d", j.maxSBytes, DefaultMaxSpecIntentBytes)
	}
}

// =====================================================================
// TestJudge — happy path, error mapping, retry, parser edge cases
// =====================================================================

func TestJudge_HappyPath(t *testing.T) {
	canned := `{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"aligned\",\"confidence\":0.92,\"reasoning\":\"The artifact matches the spec intent.\"}"}}]}`
	stub := &stubHTTPClient{
		responses: []stubResponse{{status: 200, body: []byte(canned)}},
	}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)

	v, err := j.Judge(context.Background(), "spec intent", "artifact body")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Verdict != "aligned" {
		t.Errorf("Verdict = %q, want aligned", v.Verdict)
	}
	if v.Confidence != 0.92 {
		t.Errorf("Confidence = %f, want 0.92", v.Confidence)
	}
	if v.Reasoning == "" {
		t.Error("Reasoning is empty")
	}
	if v.ProviderID != "judge-test" {
		t.Errorf("ProviderID = %q, want judge-test", v.ProviderID)
	}
	if v.LatencyMS < 0 {
		t.Errorf("LatencyMS = %d, want >= 0", v.LatencyMS)
	}
}

func TestJudge_EmptySpecIntent(t *testing.T) {
	stub := &stubHTTPClient{}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	_, err := j.Judge(context.Background(), "", "artifact body")
	if !errors.Is(err, ErrInputEmpty) {
		t.Errorf("expected ErrInputEmpty, got %v", err)
	}
}

func TestJudge_EmptyArtifactBody(t *testing.T) {
	stub := &stubHTTPClient{}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	_, err := j.Judge(context.Background(), "spec", "")
	if !errors.Is(err, ErrInputEmpty) {
		t.Errorf("expected ErrInputEmpty, got %v", err)
	}
}

func TestJudge_ArtifactTooLarge(t *testing.T) {
	stub := &stubHTTPClient{}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	big := strings.Repeat("x", DefaultMaxArtifactBytes+1)
	_, err := j.Judge(context.Background(), "spec", big)
	if !errors.Is(err, ErrInputTooLarge) {
		t.Errorf("expected ErrInputTooLarge, got %v", err)
	}
}

func TestJudge_HTTP429(t *testing.T) {
	stub := &stubHTTPClient{
		responses: []stubResponse{{status: 429, body: []byte(`{"error":"rate limited"}`)}},
	}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	_, err := j.Judge(context.Background(), "spec", "body")
	if !errors.Is(err, ErrProviderRateLimited) {
		t.Errorf("expected ErrProviderRateLimited, got %v", err)
	}
}

func TestJudge_HTTP401(t *testing.T) {
	stub := &stubHTTPClient{
		responses: []stubResponse{{status: 401, body: []byte(`{"error":"unauthorized"}`)}},
	}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	_, err := j.Judge(context.Background(), "spec", "body")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}
}

func TestJudge_HTTP500(t *testing.T) {
	stub := &stubHTTPClient{
		responses: []stubResponse{{status: 500, body: []byte(`{"error":"internal"}`)}},
	}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	_, err := j.Judge(context.Background(), "spec", "body")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}
}

// =====================================================================
// TestJudge_RetryOnce — T-301 retry on parser contract bug
// =====================================================================

func TestJudge_RetryOnceOnBadJSON(t *testing.T) {
	// First response: malformed (not JSON). Retry: valid JSON.
	stub := &stubHTTPClient{
		responses: []stubResponse{
			{status: 200, body: []byte(`not a JSON object at all`)},
			{status: 200, body: []byte(`{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"aligned\",\"confidence\":0.85,\"reasoning\":\"OK after retry\"}"}}]}`)},
		},
	}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	v, err := j.Judge(context.Background(), "spec", "body")
	if err != nil {
		t.Fatalf("expected retry to recover, got %v", err)
	}
	if v.Verdict != "aligned" || v.Confidence != 0.85 {
		t.Errorf("verdict=%s confidence=%f, want aligned/0.85", v.Verdict, v.Confidence)
	}
	if stub.calls != 2 {
		t.Errorf("expected 2 HTTP calls (1 retry), got %d", stub.calls)
	}
}

func TestJudge_RetryBothFailReturnsNoLLMBound(t *testing.T) {
	// Both responses malformed. Final error must be ErrNoLLMBound.
	stub := &stubHTTPClient{
		responses: []stubResponse{
			{status: 200, body: []byte(`nope 1`)},
			{status: 200, body: []byte(`nope 2`)},
		},
	}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-test"}, stub)
	_, err := j.Judge(context.Background(), "spec", "body")
	if !errors.Is(err, ErrNoLLMBound) {
		t.Errorf("expected ErrNoLLMBound, got %v", err)
	}
	if stub.calls != 2 {
		t.Errorf("expected 2 HTTP calls (1 retry), got %d", stub.calls)
	}
}

// =====================================================================
// TestParser — JSON shape edge cases (the real parser surface)
// =====================================================================

func TestParseVerdictResponse_DirectJSON(t *testing.T) {
	body := []byte(`{"verdict":"drift_detected","confidence":0.7,"reasoning":"x"}`)
	v, err := parseVerdictResponse(body)
	if err != nil {
		t.Fatalf("parseVerdictResponse: %v", err)
	}
	if v.Verdict != "drift_detected" || v.Confidence != 0.7 {
		t.Errorf("verdict=%s conf=%f, want drift_detected/0.7", v.Verdict, v.Confidence)
	}
}

func TestParseVerdictResponse_MarkdownFence(t *testing.T) {
	body := []byte("```json\n" + `{"verdict":"aligned","confidence":0.95,"reasoning":"x"}` + "\n```")
	v, err := parseVerdictResponse(body)
	if err != nil {
		t.Fatalf("parseVerdictResponse: %v", err)
	}
	if v.Verdict != "aligned" || v.Confidence != 0.95 {
		t.Errorf("verdict=%s conf=%f, want aligned/0.95", v.Verdict, v.Confidence)
	}
}

func TestParseVerdictResponse_ProsePrefix(t *testing.T) {
	body := []byte(`Here is the verdict: ` + `{"verdict":"needs_human","confidence":0.5,"reasoning":"x"}`)
	v, err := parseVerdictResponse(body)
	if err != nil {
		t.Fatalf("parseVerdictResponse: %v", err)
	}
	if v.Verdict != "needs_human" {
		t.Errorf("verdict=%s, want needs_human", v.Verdict)
	}
}

func TestParseVerdictResponse_EmptyBody(t *testing.T) {
	_, err := parseVerdictResponse(nil)
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("expected ErrProviderBadResponse, got %v", err)
	}
}

func TestParseVerdictResponse_BadJSON(t *testing.T) {
	body := []byte(`not even close to JSON`)
	_, err := parseVerdictResponse(body)
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("expected ErrProviderBadResponse, got %v", err)
	}
}

func TestParseVerdictResponse_BracesInString(t *testing.T) {
	// Reasoning contains {nested} braces — must not break depth count.
	body := []byte(`{"verdict":"aligned","confidence":0.8,"reasoning":"Use {} placeholders"}`)
	v, err := parseVerdictResponse(body)
	if err != nil {
		t.Fatalf("parseVerdictResponse: %v", err)
	}
	if v.Verdict != "aligned" {
		t.Errorf("verdict=%s, want aligned", v.Verdict)
	}
}

// =====================================================================
// TestNoAuthLeak — security invariant from spec §6 G7
// =====================================================================

func TestJudge_NeverLogsAuthToken(t *testing.T) {
	canned := `{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"aligned\",\"confidence\":0.9,\"reasoning\":\"x\"}"}}]}`
	stub := &stubHTTPClient{
		responses: []stubResponse{{status: 200, body: []byte(canned)}},
	}
	cfg := ProviderConfig{
		ProviderID: "judge-test",
		Endpoint:   "https://stub.test/v1/chat/completions",
		AuthToken:  "sk-cp-SECRET-MUST-NOT-LEAK",
	}
	j := makeJudge(t, cfg, stub)

	// Inject a fake error path: malformed response so Judge
	// returns an error. The error message MUST NOT contain the
	// auth token. (The actual implementation never includes auth
	// tokens in any error path. If a regression introduces it, this
	// test catches it.)
	bad := &stubHTTPClient{
		responses: []stubResponse{
			{status: 500, body: []byte(`internal server error`)},
		},
	}
	j2 := makeJudge(t, cfg, bad)
	_, err := j2.Judge(context.Background(), "spec", "body")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if strings.Contains(err.Error(), "SECRET-MUST-NOT-LEAK") {
		t.Fatalf("SECURITY: auth token leaked in error: %v", err)
	}

	// Sanity: the happy path still works and headers carry the token
	// (only on the wire, never in errors).
	_, _ = j.Judge(context.Background(), "spec", "body")
	if !strings.Contains(stub.requests[0].Header.Get("Authorization"), "SECRET-MUST-NOT-LEAK") {
		t.Errorf("Authorization header missing token; wire shape broken")
	}
}

// =====================================================================
// TestRequestShape — payload structure sanity
// =====================================================================

func TestJudge_RequestShape(t *testing.T) {
	canned := `{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"aligned\",\"confidence\":0.9,\"reasoning\":\"x\"}"}}]}`
	stub := &stubHTTPClient{
		responses: []stubResponse{{status: 200, body: []byte(canned)}},
	}
	j := makeJudge(t, ProviderConfig{
		ProviderID: "judge-minimax-cn",
		ModelRev:   "MiniMax-M3",
	}, stub)

	_, err := j.Judge(context.Background(), "the spec", "the artifact")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}

	// Assert request body shape.
	var payload map[string]any
	if err := json.Unmarshal(stub.bodies[0], &payload); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if payload["temperature"].(float64) != 0 {
		t.Errorf("temperature = %v, want 0", payload["temperature"])
	}
	if payload["model"] != "MiniMax-M3" {
		t.Errorf("model = %v, want MiniMax-M3", payload["model"])
	}
	msgs := payload["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Errorf("first message role = %v, want system", sys["role"])
	}
	if sys["content"] == "" || !strings.Contains(sys["content"].(string), "drift judge") {
		t.Errorf("system prompt doesn't contain 'drift judge'")
	}
	usr := msgs[1].(map[string]any)
	if usr["role"] != "user" {
		t.Errorf("second message role = %v, want user", usr["role"])
	}
	if !strings.Contains(usr["content"].(string), "the spec") {
		t.Errorf("user prompt missing spec_intent")
	}
	if !strings.Contains(usr["content"].(string), "the artifact") {
		t.Errorf("user prompt missing artifact_body")
	}
}

// helper for clarity
var _ = fmt.Sprintf