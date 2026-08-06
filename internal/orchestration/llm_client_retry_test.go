// llm_client_retry_test.go — v2.11.0 update tests for the judge call
// budget machinery: DARK_JUDGE_TIMEOUT_MS (per-eval-type timeout),
// DARK_JUDGE_RETRY_COUNT (transient-failure retries with backoff),
// retryable-error classification, and the shared HTTP client.
//
// No mocking inside the package: these run against httptest.Server
// endpoints that count requests, so the retry loop is exercised
// end-to-end (503 → 503 → 200 proves exactly 2 attempts).
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestJudgeViaHTTP_RetriesTransient503 verifies a transient 503 is
// retried (DARK_JUDGE_RETRY_COUNT=1 → 2 attempts) and the second
// attempt's success is returned.
func TestJudgeViaHTTP_RetriesTransient503(t *testing.T) {
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "1")
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"pool draining"}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"verdict\":\"aligned\",\"confidence\":0.9}"}],"model":"m1"}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL}
	resp, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "x",
	})
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("server should have seen 2 attempts, got %d", got)
	}
	if resp.VerdictJSON == "" {
		t.Fatal("expected a verdict from the retried attempt")
	}
}

// TestJudgeViaHTTP_RetriesExhausted verifies that when the endpoint
// keeps failing transiently, the error surfaces after retries are
// exhausted (and the last error is the 503, not a retry-loop error).
func TestJudgeViaHTTP_RetriesExhausted(t *testing.T) {
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "1")
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL}
	_, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "x",
	})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("server should have seen 2 attempts, got %d", got)
	}
	var st *judgeHTTPStatusError
	if !errors.As(err, &st) || st.status != http.StatusTooManyRequests {
		t.Fatalf("error should carry the 429 status, got: %v", err)
	}
}

// TestJudgeViaHTTP_NoRetryOn400 verifies 4xx (except 429) fails fast
// — a malformed request should not be retried.
func TestJudgeViaHTTP_NoRetryOn400(t *testing.T) {
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "3")
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL}
	_, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "x",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("4xx should not retry: server saw %d attempts, want 1", got)
	}
}

// TestJudgeViaHTTP_TimeoutRetries verifies a slow endpoint that
// exceeds the (tuned-down) per-attempt timeout is retried, and the
// retry's success wins.
func TestJudgeViaHTTP_TimeoutRetries(t *testing.T) {
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "1")
	t.Setenv("DARK_JUDGE_TIMEOUT_MS", "200")
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			// Sleep past the 200ms budget.
			time.Sleep(600 * time.Millisecond)
			return
		}
		_, _ = w.Write([]byte(`{"text":"{\"verdict\":\"aligned\"}"}`))
	}))
	defer srv.Close()

	c := &SelfHarnessClient{provider: "drift_judge_daemon", key: srv.URL}
	resp, err := c.judgeViaDriftJudgeDaemon(context.Background(), JudgeRequest{
		EvalType: "drift_judge",
		Content:  "x",
	})
	if err != nil {
		t.Fatalf("expected success after timeout retry, got: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("server should have seen 2 attempts, got %d", got)
	}
	if resp.VerdictJSON == "" {
		t.Fatal("expected verdict from retried attempt")
	}
}

// TestJudgeTimeoutForEval verifies the env-driven base timeout and
// per-eval_type multipliers.
func TestJudgeTimeoutForEval(t *testing.T) {
	// Default base (120s) with default multipliers.
	cases := []struct {
		evalType string
		min      time.Duration
		max      time.Duration
	}{
		{"drift_judge", 179 * time.Second, 181 * time.Second}, // 120 × 1.5
		{"compliance_check", 119 * time.Second, 121 * time.Second},
		{"pii_detect", 59 * time.Second, 61 * time.Second},     // 120 × 0.5
		{"unknown_type", 119 * time.Second, 121 * time.Second}, // default ×1
	}
	for _, tc := range cases {
		t.Run("default_"+tc.evalType, func(t *testing.T) {
			got := judgeTimeoutForEval(tc.evalType)
			if got < tc.min || got > tc.max {
				t.Errorf("judgeTimeoutForEval(%q) = %v, want in [%v, %v]", tc.evalType, got, tc.min, tc.max)
			}
		})
	}

	// Env override scales everything.
	t.Setenv("DARK_JUDGE_TIMEOUT_MS", "30000")
	if got := judgeTimeoutForEval("drift_judge"); got != 45*time.Second {
		t.Errorf("override drift_judge: want 45s, got %v", got)
	}
	if got := judgeTimeoutForEval("pii_detect"); got != 15*time.Second {
		t.Errorf("override pii_detect: want 15s, got %v", got)
	}

	// Garbage env falls back to default.
	t.Setenv("DARK_JUDGE_TIMEOUT_MS", "not-a-number")
	if got := judgeTimeoutForEval("compliance_check"); got != 120*time.Second {
		t.Errorf("garbage env: want 120s, got %v", got)
	}
}

// TestJudgeRetryCountEnv verifies parsing + clamping.
func TestJudgeRetryCountEnv(t *testing.T) {
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "")
	if got := judgeRetryCount(); got != 2 {
		t.Errorf("empty env: want default 2, got %d", got)
	}
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "0")
	if got := judgeRetryCount(); got != 0 {
		t.Errorf("0: want 0, got %d", got)
	}
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "99")
	if got := judgeRetryCount(); got != 5 {
		t.Errorf("99: want clamped 5, got %d", got)
	}
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "-1")
	if got := judgeRetryCount(); got != 2 {
		t.Errorf("-1: want default 2, got %d", got)
	}
	t.Setenv("DARK_JUDGE_RETRY_COUNT", "junk")
	if got := judgeRetryCount(); got != 2 {
		t.Errorf("junk: want default 2, got %d", got)
	}
}

// TestIsRetryableError covers the classification table.
func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"wrapped deadline", fmtErrorf("outer: %w", context.DeadlineExceeded), true},
		{"429", &judgeHTTPStatusError{status: http.StatusTooManyRequests, body: "slow down"}, true},
		{"503", &judgeHTTPStatusError{status: http.StatusServiceUnavailable, body: "drained"}, true},
		{"500", &judgeHTTPStatusError{status: http.StatusInternalServerError, body: "boom"}, true},
		{"400", &judgeHTTPStatusError{status: http.StatusBadRequest, body: "nope"}, false},
		{"404", &judgeHTTPStatusError{status: http.StatusNotFound, body: "missing"}, false},
		{"plain error", errors.New("boom"), false},
		{"net timeout", &net.OpError{Op: "dial", Err: context.DeadlineExceeded}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableError(tc.err); got != tc.want {
				t.Errorf("isRetryableError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestBackoffForAttempt verifies exponential growth with a cap and
// jitter bounds (±25% of the nominal value).
func TestBackoffForAttempt(t *testing.T) {
	cases := []struct {
		attempt int
		min     time.Duration
		max     time.Duration
	}{
		{1, 750 * time.Millisecond, 1250 * time.Millisecond},  // 1s ±25%
		{2, 1500 * time.Millisecond, 2500 * time.Millisecond}, // 2s ±25%
		{3, 3 * time.Second, 5 * time.Second},                 // 4s ±25%
		{10, 6 * time.Second, 10 * time.Second},               // capped at 8s ±25%
	}
	for _, tc := range cases {
		t.Run("attempt_"+strconv.Itoa(tc.attempt), func(t *testing.T) {
			for i := 0; i < 50; i++ {
				got := backoffForAttempt(tc.attempt)
				if got < tc.min || got > tc.max {
					t.Fatalf("backoffForAttempt(%d) = %v, want in [%v, %v]", tc.attempt, got, tc.min, tc.max)
				}
			}
		})
	}
}

// fmtErrorf is a tiny wrapper so the retryable test can exercise
// wrapped errors.
var fmtErrorf = fmt.Errorf
