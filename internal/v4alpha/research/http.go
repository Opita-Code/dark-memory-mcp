package research

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/security"
)

// HTTP client configuration. Per-tier defaults match the §3.2
// service-specific rate limits observed in the 2026-09-27 probe:
// OSV / NVD / RDAP are fast (5-8s budget), DDG HTML scraping is
// slow (12s budget), Nominatim has the 1-req-per-second policy
// baked into the bucket.
type HTTPConfig struct {
	// PerCallTimeoutMs bounds a single backend call. Default 12s.
	// The contract: a backend that times out consumes a Bucket
	// token (so a slow backend is also a rate-limited one) and
	// counts as an error for the health state machine.
	PerCallTimeoutMs int

	// MaxParallel is the errgroup.SetLimit value. Default 4.
	// The executor never issues more than 4 concurrent backend
	// calls per Top-level Topic call; the rest wait or get
	// ErrBusyBackoff.
	MaxParallel int

	// MaxBodyBytes is the hard cap on response body. The default
	// is 2 MiB (covers most legitimate responses; rejects the
	// 500 KB crt.sh / 440 KB crates.io pathological case). The
	// per-call budget can be smaller; this is the maximum.
	MaxBodyBytes int64
}

// DefaultHTTPConfig returns the executor's default. Per-call
// overrides are passed by the executor when it knows the intent
// (e.g. crt.sh gets a 64 KB cap, OSV gets 256 KB).
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		PerCallTimeoutMs: 12_000,
		MaxParallel:      4,
		MaxBodyBytes:     2 * 1024 * 1024,
	}
}

// Sentinel errors for the HTTP layer. The executor maps these
// to: ErrSSRFBlocked → fail-closed (no fallback), ErrTimeout /
// ErrBusyBackoff → try next backend, ErrBodyTooLarge → partial
// result with truncation marker.
var (
	ErrSSRFBlocked  = errors.New("research: SSRF guard rejected URL")
	ErrTimeout      = errors.New("research: backend call timed out")
	ErrBusyBackoff  = errors.New("research: too many parallel calls")
	ErrBodyTooLarge = errors.New("research: response body exceeds cap")
)

// HTTPClient is the single chokepoint for all backend HTTP calls.
// Every backend goes through HTTPClient.Do; the SSRF guard runs
// before any DNS lookup; the body cap runs after every read; the
// timeout is enforced at the net/http layer; the parallel limit
// is enforced at the errgroup layer.
type HTTPClient struct {
	cfg     HTTPConfig
	limiter *errgroup.Group
	inFlight int64
}

// NewHTTPClient returns a ready client. The per-call errgroup is
// recreated on every Do call so cancelled goroutines do not leak;
// the call counter is per-client.
func NewHTTPClient(cfg HTTPConfig) *HTTPClient {
	if cfg.PerCallTimeoutMs == 0 {
		cfg.PerCallTimeoutMs = 12_000
	}
	if cfg.MaxParallel == 0 {
		cfg.MaxParallel = 4
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 2 * 1024 * 1024
	}
	return &HTTPClient{cfg: cfg}
}

// Do executes one HTTP GET against the URL and returns the body
// (capped at cfg.MaxBodyBytes), the response status, and any error.
// The method is the single chokepoint for SSRF: the guard runs
// before the call, the timeout runs through context.WithTimeout,
// the body cap runs at the read side.
//
// The errgroup here is local to a single Do call: this method
// does not fan out across backends (the executor does that in
// its own errgroup). The pool limit MaxParallel is enforced
// across the executor's lifetime by checking the inFlight
// counter and returning ErrBusyBackoff when full.
func (c *HTTPClient) Do(ctx context.Context, rawURL string) (body []byte, status int, err error) {
	// AC-S1: SSRF chokepoint. Every URL the executor builds goes
	// through this guard. There is no escape hatch; backends
	// cannot dial internal hosts by accident.
	if err := security.ValidateURL(rawURL); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrSSRFBlocked, err)
	}

	// AC-O3: backpressure. If we are already at the in-flight
	// limit, fail fast so the executor can try the next backend
	// instead of queueing silently.
	if atomic.LoadInt64(&c.inFlight) >= int64(c.cfg.MaxParallel) {
		return nil, 0, ErrBusyBackoff
	}
	atomic.AddInt64(&c.inFlight, 1)
	defer atomic.AddInt64(&c.inFlight, -1)

	// AC-O4: timeout per call. The contract: a backend that does
	// not answer in 12 s has its goroutine released; the call is
	// logged as a timeout error to the executor's health state.
	cctx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.PerCallTimeoutMs)*time.Millisecond)
	defer cancel()

	// AC-S3: TLS strict. InsecureSkipVerify is false. The default
	// Transport is fine; we set it explicitly so a future
	// contributor cannot silently flip the bit.
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		Proxy:           http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: tr}

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", "dark-memory-v4/0.3 (+research)")
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.1")

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, ErrTimeout
		}
		return nil, 0, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	// AC-O1: body cap. The Read is bounded by MaxBodyBytes + 1 so
	// the caller can detect the truncation and surface a marker.
	limited := io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1)
	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, limited)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	if n > c.cfg.MaxBodyBytes {
		return buf.Bytes()[:c.cfg.MaxBodyBytes], resp.StatusCode, ErrBodyTooLarge
	}
	return buf.Bytes(), resp.StatusCode, nil
}

// InFlight returns the current in-flight call count (for
// observability tests).
func (c *HTTPClient) InFlight() int64 {
	return atomic.LoadInt64(&c.inFlight)
}

// NormalizeURL is a helper for the backends: it parses raw,
// validates via security.ValidateURL, and returns the parsed
// *url.URL. Used when a backend needs to construct a URL from a
// template and a target.
func NormalizeURL(raw string) (*url.URL, error) {
	if err := security.ValidateURL(raw); err != nil {
		return nil, err
	}
	return url.Parse(raw)
}
