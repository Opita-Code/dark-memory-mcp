// Package v4judge — live_test.go
//
// Live integration tests for LLMJudge. Gated behind
// V4JUDGE_LIVE=1 so CI doesn't accidentally hit the live API.
//
// Usage:
//
//	V4JUDGE_LIVE=1 MINIMAX_API_KEY_CN=sk-... go test -count=1 -p 1 -run "TestLive" ./internal/v4alpha/judge/v4judge/
//
// These tests are operator-facing verification for Phase 14
// T-301. They prove the LLMJudge struct + parser handle the
// real MiniMax wire shape (with think blocks + JSON inside
// message.content) end-to-end.
package v4judge

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// liveHTTPClient returns the real *http.Client. Tests use this
// instead of the stub. Returns nil when V4JUDGE_LIVE is unset
// (skip the tests).
func liveHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	if os.Getenv("V4JUDGE_LIVE") != "1" {
		t.Skip("V4JUDGE_LIVE not set; skipping live API test")
	}
	if os.Getenv("MINIMAX_API_KEY_CN") == "" && os.Getenv("MINIMAX_API_KEY") == "" {
		t.Skip("no MINIMAX_API_KEY_CN/MINIMAX_API_KEY; skipping live API test")
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// liveJudge builds an LLMJudge against the real
// api.minimaxi.com endpoint. Uses the CN key if available.
func liveJudge(t *testing.T) (*LLMJudge, string) {
	t.Helper()
	key := os.Getenv("MINIMAX_API_KEY_CN")
	if key == "" {
		key = os.Getenv("MINIMAX_API_KEY")
	}
	cfg := ProviderConfig{
		ProviderID: "judge-minimax-cn-live",
		Endpoint:   "https://api.minimaxi.com/v1/chat/completions",
		AuthToken:  key,
		TimeoutMS:  60000,
		ModelRev:   "MiniMax-M3",
	}
	j, err := NewLLMJudge(cfg, liveHTTPClient(t))
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	return j, key
}

// =====================================================================
// TestLive_PongProbe — minimal "reply with pong" probe, mirrors
// llm_provider_probe. Should always succeed against the live
// endpoint.
// =====================================================================
func TestLive_PongProbe(t *testing.T) {
	j, _ := liveJudge(t)
	body := `{"model":"MiniMax-M3","messages":[{"role":"system","content":"reply with pong"},{"role":"user","content":"ping"}],"temperature":0,"max_tokens":4}`
	// Build the same wire shape the probe would send.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.minimaxi.com/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+j.authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d (expected 200)", resp.StatusCode)
	}
	t.Logf("pong probe OK (status=200, model=%s)", j.modelRev)
}

// =====================================================================
// TestLive_Aligned — full Judge call with a known aligned
// artifact. Verifies the parser handles real think-block + JSON.
// =====================================================================
func TestLive_Aligned(t *testing.T) {
	j, _ := liveJudge(t)
	specIntent := "a simple hello world function in Go"
	artifactBody := `package main
import "fmt"
func main() { fmt.Println("Hello, World!") }`

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	v, err := j.Judge(ctx, specIntent, artifactBody)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	t.Logf("verdict=%s confidence=%.2f reasoning=%q",
		v.Verdict, v.Confidence, v.Reasoning)
	if v.Verdict != "aligned" {
		t.Errorf("verdict: got %q, want aligned", v.Verdict)
	}
	if v.Confidence < 0.5 {
		t.Errorf("confidence: got %f, want >= 0.5", v.Confidence)
	}
	if v.Reasoning == "" {
		t.Error("reasoning is empty")
	}
	if v.LatencyMS < 0 {
		t.Errorf("latency: got %d, want >= 0", v.LatencyMS)
	}
	if v.ProviderID != "judge-minimax-cn-live" {
		t.Errorf("provider_id: got %q, want judge-minimax-cn-live", v.ProviderID)
	}
}

// =====================================================================
// TestLive_DriftDetected — full Judge call with a clear drift
// scenario. Verifies drift_detected comes back with high
// confidence.
// =====================================================================
func TestLive_DriftDetected(t *testing.T) {
	j, _ := liveJudge(t)
	specIntent := "a JSON config file with three database connection strings"
	artifactBody := `{"foo": 1, "bar": 2}`

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	v, err := j.Judge(ctx, specIntent, artifactBody)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	t.Logf("verdict=%s confidence=%.2f reasoning=%q",
		v.Verdict, v.Confidence, v.Reasoning)
	if v.Verdict != "drift_detected" {
		t.Errorf("verdict: got %q, want drift_detected", v.Verdict)
	}
	if v.Confidence < 0.5 {
		t.Errorf("confidence: got %f, want >= 0.5", v.Confidence)
	}
}

// =====================================================================
// TestLive_NeedsHuman — ambiguous scenario (a spec that could
// match multiple artifacts). The model may legitimately return
// needs_human here; we just verify the verdict is one of the
// three canonical values + reasoning is non-empty.
// =====================================================================
func TestLive_Ambiguous_NeedsHumanOrAligned(t *testing.T) {
	j, _ := liveJudge(t)
	specIntent := "a function that processes data efficiently"
	artifactBody := `function add(a, b) { return a + b; }`

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	v, err := j.Judge(ctx, specIntent, artifactBody)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	t.Logf("verdict=%s confidence=%.2f reasoning=%q",
		v.Verdict, v.Confidence, v.Reasoning)
	switch v.Verdict {
	case "aligned", "drift_detected", "needs_human":
		// OK
	default:
		t.Errorf("verdict: got %q, want one of aligned/drift_detected/needs_human", v.Verdict)
	}
}

// =====================================================================
// TestLive_AuthNotLeakedInError — verify the auth token never
// appears in any error path when the API returns 401 (force a
// bad token scenario).
// =====================================================================
func TestLive_BadAuth_ReturnsProviderUnavailable(t *testing.T) {
	key := os.Getenv("MINIMAX_API_KEY_CN")
	if key == "" {
		key = os.Getenv("MINIMAX_API_KEY")
	}
	cfg := ProviderConfig{
		ProviderID: "judge-minimax-cn-bad",
		Endpoint:   "https://api.minimaxi.com/v1/chat/completions",
		AuthToken:  "sk-WRONG-AUTH-TOKEN-FOR-TEST-1234567890",
		TimeoutMS:  30000,
		ModelRev:   "MiniMax-M3",
	}
	j, err := NewLLMJudge(cfg, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	v, err := j.Judge(context.Background(), "x", "y")
	if err == nil {
		t.Fatal("expected error from bad auth")
	}
	if !strings.Contains(err.Error(), "provider unavailable") &&
		!strings.Contains(err.Error(), "401") &&
		!strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("error: got %v, want one of provider_unavailable/401/unauthorized", err)
	}
	// NEVER leak the auth token in any error message.
	if strings.Contains(err.Error(), "WRONG-AUTH-TOKEN") {
		t.Fatalf("SECURITY: bad auth token leaked in error: %v", err)
	}
	t.Logf("bad auth error: %v (no token leak)", err)
	_ = key
	_ = v
}

// =====================================================================
// TestLive_DumpRealResponse — diagnostic helper to print the real
// response body for the drift judge call. Useful when investigating
// parser regressions.
// =====================================================================
func TestLive_DumpRealResponse(t *testing.T) {
	j, _ := liveJudge(t)
	specIntent := "a simple hello world function in Go"
	artifactBody := `package main; import "fmt"; func main() { fmt.Println("Hello, World!") }`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	v, err := j.Judge(ctx, specIntent, artifactBody)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	fmt.Printf("=== Live Verification ===\n")
	fmt.Printf("Provider: %s\n", v.ProviderID)
	fmt.Printf("Latency: %d ms\n", v.LatencyMS)
	fmt.Printf("Verdict: %s\n", v.Verdict)
	fmt.Printf("Confidence: %.4f\n", v.Confidence)
	fmt.Printf("Reasoning: %s\n", v.Reasoning)
}