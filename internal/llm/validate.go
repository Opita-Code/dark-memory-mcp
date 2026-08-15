// validate.go — hot provider validation (spec 1188 T3).
//
// ValidateProvider proves a key works WITHOUT spending tokens: it hits
// the provider's free models/balance endpoint (never a completion).
//
//	OpenAI-compat  -> GET {BaseURL}{ProbePath}  (Authorization: Bearer)
//	Anthropic      -> GET {BaseURL}{ProbePath}  (x-api-key + anthropic-version)
//	DeepSeek bonus -> GET {BaseURL}/user/balance (also free)
//
// Classification:
//
//	200            -> valid
//	401/403        -> auth_error   (dead key)
//	429            -> rate_limited (key ok, provider throttling)
//	404            -> unknown      (probe path unsupported; do NOT
//	                                 penalize — fail open)
//	408 / timeout  -> unreachable (class timeout)
//	5xx / net err  -> unreachable (class server)
package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ProbeState is the outcome of a hot probe.
type ProbeState string

const (
	// ProbeValid: endpoint answered 200 — key works.
	ProbeValid ProbeState = "valid"
	// ProbeAuthError: 401/403 — key rejected.
	ProbeAuthError ProbeState = "auth_error"
	// ProbeRateLimited: 429 — key works but provider throttling.
	ProbeRateLimited ProbeState = "rate_limited"
	// ProbeUnreachable: timeout, 5xx, or network failure.
	ProbeUnreachable ProbeState = "unreachable"
	// ProbeUnknown: 404 or another response we cannot classify — the
	// probe path is unsupported by this provider. Treated as healthy
	// (fail open) by the health registry.
	ProbeUnknown ProbeState = "unknown"
)

// ProbeResult is the outcome of ValidateProvider for one provider.
type ProbeResult struct {
	ProviderID string     `json:"provider_id"`
	State      ProbeState `json:"state"`
	// Class is the failure class for the health registry (empty when
	// State == valid || unknown).
	Class     FailClass `json:"class,omitempty"`
	LatencyMs int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// probeTimeout is the per-probe budget (spec: 5s).
const probeTimeout = 5 * time.Second

// probeHTTPClient is a small dedicated client for probes. No global
// pool sharing with the judge client (probes must never compete with
// real judge calls for connections).
var probeHTTPClient = &http.Client{
	Timeout: probeTimeout,
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 2,
		MaxConnsPerHost:     4,
		IdleConnTimeout:     30 * time.Second,
	},
}

// ValidateProvider runs the free probe for one provider+key. ctx can
// additionally bound the call (caller may pass a shorter deadline).
func ValidateProvider(ctx context.Context, spec *ProviderSpec, key string) ProbeResult {
	start := time.Now()
	res := ProbeResult{
		ProviderID: spec.ID,
		State:      ProbeUnreachable,
		CheckedAt:  time.Now().UTC(),
	}
	if spec == nil {
		res.Error = "nil provider spec"
		return res
	}
	if strings.TrimSpace(key) == "" {
		res.Error = "empty key"
		return res
	}

	endpoint := strings.TrimRight(spec.BaseURL, "/") + spec.ProbePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		res.Error = fmt.Sprintf("build probe request: %v", err)
		return res
	}
	switch spec.ProbeAuthMode {
	case ProbeAuthXAPIKey:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}

	httpResp, err := probeHTTPClient.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.State = ProbeUnreachable
		res.Class = FailTimeout
		res.Error = fmt.Sprintf("probe request: %v", err)
		return res
	}
	defer httpResp.Body.Close()

	switch httpResp.StatusCode {
	case http.StatusOK:
		res.State = ProbeValid
	case http.StatusUnauthorized, http.StatusForbidden:
		res.State = ProbeAuthError
		res.Class = FailAuth
		res.Error = fmt.Sprintf("HTTP %d", httpResp.StatusCode)
	case http.StatusTooManyRequests:
		res.State = ProbeRateLimited
		res.Class = FailRate
		res.Error = "HTTP 429"
	case http.StatusRequestTimeout:
		res.State = ProbeUnreachable
		res.Class = FailTimeout
		res.Error = "HTTP 408"
	case http.StatusNotFound:
		// Probe path unsupported by this provider — not evidence the
		// key is dead. Fail open.
		res.State = ProbeUnknown
		res.Error = "HTTP 404 (probe path unsupported)"
	default:
		res.State = ProbeUnreachable
		res.Class = FailServer
		res.Error = fmt.Sprintf("HTTP %d", httpResp.StatusCode)
	}
	return res
}

// ValidateProviderWithBalance runs the standard probe and, when the
// spec declares a BalancePath, also fetches it for extra evidence.
// The verdict stays driven by the primary probe; the balance fetch is
// best-effort and only fills res.Error detail when it fails.
func ValidateProviderWithBalance(ctx context.Context, spec *ProviderSpec, key string) ProbeResult {
	res := ValidateProvider(ctx, spec, key)
	if spec.BalancePath == "" {
		return res
	}
	bEndpoint := strings.TrimRight(spec.BaseURL, "/") + spec.BalancePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bEndpoint, nil)
	if err != nil {
		return res
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if resp, err := probeHTTPClient.Do(req); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			res.Error = fmt.Sprintf("balance probe: HTTP %d", resp.StatusCode)
		}
	}
	return res
}
