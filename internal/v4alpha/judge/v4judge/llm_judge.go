package v4judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
	"github.com/dark-agents/dark-memory-mcp/internal/llm/chatwire"
)

// Sealed error set. Use errors.Is to classify; do not match strings.
// Mirrors internal/nli/errors (ChatProvider) so the drift_judge
// selector can use the same errors.Is classification, but is a
// separate package so v4judge does NOT import internal/nli (clean
// separation: v4judge is the primary path, nli is the legacy path).
var (
	// ErrProviderUnavailable: network failure, non-2xx (except 429),
	// auth failure (401/403). drift_judge → fall through to NLI path.
	ErrProviderUnavailable = errors.New("v4judge: provider unavailable")

	// ErrProviderTimeout: ctx deadline exceeded OR provider's own
	// timeout fired. drift_judge → fall through to NLI path.
	ErrProviderTimeout = errors.New("v4judge: provider timed out")

	// ErrProviderRateLimited: HTTP 429. drift_judge → fall through to
	// NLI path (transient error).
	ErrProviderRateLimited = errors.New("v4judge: provider rate limited")

	// ErrProviderBadResponse: 2xx but body is malformed (wrong shape,
	// missing verdict, score out of range, unmapped verdict). This is
	// a CONTRACT bug — fallback would have the same contract, so
	// drift_judge does NOT fall through to NLI. The verdict is
	// "needs_human" with Reasoning="v4judge: bad response".
	ErrProviderBadResponse = errors.New("v4judge: provider returned malformed response")

	// ErrInputTooLarge: artifact body exceeds the configured byte cap.
	// drift_judge → needs_human (caller bug; artifact pipeline should
	// enforce upstream).
	ErrInputTooLarge = errors.New("v4judge: input exceeds size cap")

	// ErrInputEmpty: spec_intent or artifact_body is empty. drift_judge
	// → needs_human (caller bug).
	ErrInputEmpty = errors.New("v4judge: spec_intent or artifact_body is empty")

	// ErrInvalidConfig: provider configuration rejected at construction
	// (empty endpoint, no auth token, invalid prefix). Distinct from
	// ErrProviderBadResponse so the operator can tell "I misconfigured"
	// apart from "the model misbehaved".
	ErrInvalidConfig = errors.New("v4judge: invalid configuration")

	// ErrProviderIDNotJudge: ProviderID does not start with "judge-".
	// The drift_judge selector uses this to fall through to the NLI
	// chain (chat-* providers continue to route there). Mirrors
	// internal/nli/chat.go::NewChatProvider which rejects "chat-".
	ErrProviderIDNotJudge = errors.New("v4judge: provider_id must start with 'judge-'")

	// ErrNoLLMBound: { retry attempt failed on parser contract bug };
	// drift_judge → verdict=needs_human with Reasoning. Distinct from
	// ErrProviderBadResponse so the operator can tell "the model
	// misbehaved" apart from "the model failed twice" (the second is
	// more severe).
	ErrNoLLMBound = errors.New("v4judge: no LLM judge bound")
)

// ProviderConfig is one LLM Judge provider's settings. Mirrors
// internal/nli.ProviderConfig (1:1) so projects.nli_config_json is the
// single source of truth for both NLI and judge bindings. The
// ProviderID prefix is the only routing signal:
//
//   - "judge-*" → LLMJudge (this package)
//   - "chat-*"  → internal/nli/chat.go::ChatProvider (NLI chain)
//   - "deberta*"|"minicheck*" → DeBERTa / MiniCheck (NLI chain)
//
// AuthToken is never echoed back from any tool result. Stored as
// cleartext in nli_config_json (encrypted-at-rest is the operator's
// job — full-disk encryption on the DB host). The T-303 Connect flow
// reads the auth token from the OS keyring via llm_key_list and
// persists the binding without ever exposing the token in the
// operator-facing input.
type ProviderConfig struct {
	// ProviderID is the logical id used for audit, logging, and the
	// judge-* prefix routing (e.g. "judge-minimax-cn",
	// "judge-openai", "judge-anthropic").
	ProviderID string

	// Endpoint is the full URL the client POSTs to (OpenAI-compatible
	// /v1/chat/completions). Example:
	// "https://api.minimaxi.com/v1/chat/completions"
	Endpoint string

	// AuthToken is the bearer token. Never logged; never echoed in
	// errors.
	AuthToken string

	// TimeoutMS is the per-call timeout. 0 → DefaultTimeoutMS (30s).
	// drift-judge prompts are larger than NLI prompts and reasoning
	// takes longer, hence 30s vs nli.DefaultTimeoutMS=10s.
	TimeoutMS int64

	// ModelRev is the model revision (e.g. "MiniMax-M3",
	// "claude-3-5-sonnet-20240620"). Empty means the provider uses
	// the default revision (server-side default model).
	ModelRev string

	// MaxArtifactBytes is the size cap for the artifact body. 0 →
	// DefaultMaxArtifactBytes (32768 = 32 KiB). drift-judge prompts are
	// larger than NLI prompts (verdict JSON + reasoning paragraph),
	// so the cap is smaller than nli.DefaultMaxPremiseBytes (64 KiB).
	MaxArtifactBytes int

	// MaxSpecIntentBytes is the size cap for the spec_intent. 0 →
	// DefaultMaxSpecIntentBytes (4096 = 4 KiB). spec_intent is
	// typically 1-3 paragraphs; 4 KiB is generous headroom.
	MaxSpecIntentBytes int
}

// DefaultTunables (A14 anti-pattern guard: concrete numbers, no zeros).
const (
	DefaultTimeoutMS          = 30_000 // 30s (vs nli.DefaultTTTimeoutMS=10s)
	DefaultMaxArtifactBytes   = 32_768 // 32 KiB
	DefaultMaxSpecIntentBytes = 4_096  // 4 KiB
)

// WithDefaults returns pc with zero-valued tunables replaced. It does
// NOT mutate the input.
func (pc ProviderConfig) WithDefaults() ProviderConfig {
	if pc.TimeoutMS == 0 {
		pc.TimeoutMS = DefaultTimeoutMS
	}
	if pc.MaxArtifactBytes == 0 {
		pc.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if pc.MaxSpecIntentBytes == 0 {
		pc.MaxSpecIntentBytes = DefaultMaxSpecIntentBytes
	}
	return pc
}

// Validate enforces the hard invariants. Returns nil if pc is
// well-formed, or a non-nil error (always wrapping a sealed sentinel)
// if any rule is violated.
//
// Rules:
//   - ProviderID non-empty AND starts with "judge-" (ErrProviderIDNotJudge)
//   - Endpoint non-empty
//   - TimeoutMS > 0 (0 = reject; use DefaultTimeoutMS)
//   - MaxArtifactBytes >= 64 (lower bound prevents degenerate configs)
//   - MaxSpecIntentBytes >= 16
//
// AuthToken is permitted to be empty (e.g. localhost self-hosted without
// auth) — the wire shape omits the Authorization header when empty.
func (pc ProviderConfig) Validate() error {
	if pc.ProviderID == "" {
		return fmt.Errorf("%w: provider_id required", ErrInvalidConfig)
	}
	if !strings.HasPrefix(pc.ProviderID, "judge-") {
		return fmt.Errorf("%w: %q", ErrProviderIDNotJudge, pc.ProviderID)
	}
	if pc.Endpoint == "" {
		return fmt.Errorf("%w: empty endpoint", ErrInvalidConfig)
	}
	pc = pc.WithDefaults()
	if pc.TimeoutMS <= 0 {
		return fmt.Errorf("%w: timeout_ms must be > 0", ErrInvalidConfig)
	}
	if pc.MaxArtifactBytes < 64 {
		return fmt.Errorf("%w: max_artifact_bytes must be >= 64", ErrInvalidConfig)
	}
	if pc.MaxSpecIntentBytes < 16 {
		return fmt.Errorf("%w: max_spec_intent_bytes must be >= 16", ErrInvalidConfig)
	}
	return nil
}

// LLMJudge is the v4judge provider. It implements the drift-judge
// task via OpenAI-compatible chat APIs (POST /v1/chat/completions).
//
// Thread-safety: LLMJudge is stateless after construction; all methods
// are safe for concurrent use. The orchestrator may cache one
// LLMJudge per active project (see drift_judge.go::ensureLLMJudge).
type LLMJudge struct {
	client     HTTPClient
	endpoint   string
	authToken  string
	timeout    time.Duration
	maxABytes  int
	maxSBytes  int
	modelRev   string
	providerID string
}

// HTTPClient is the subset of *http.Client that LLMJudge uses. Tests
// inject a stub via NewLLMJudge to bypass the real http.DefaultClient.
//
// Why this exists: the LLMJudge MUST be testable without spinning up
// a real HTTP server, AND without depending on the http.DefaultClient
// (which would leak test sockets via the package-level
// Transport). Mirrors internal/nli.HFInferenceClient.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewLLMJudge validates cfg and returns an LLMJudge. Returns an error
// if cfg.ProviderID does not start with "judge-", cfg.Endpoint is
// empty, the HTTP client is nil, or the size caps are negative.
//
// It is the context-free variant of NewLLMJudgeWithContext.
//
// The "judge-" prefix is required (not a convention) so the
// drift_judge selector (orchestration/drift_judge.go::ensureLLMJudge,
// Phase 14 T-302) can route operator configs unambiguously to
// LLMJudge vs internal/nli/chat.go::ChatProvider.
func NewLLMJudge(cfg ProviderConfig, hc HTTPClient) (*LLMJudge, error) {
	return NewLLMJudgeWithContext(context.Background(), cfg, hc)
}

// NewLLMJudgeWithContext validates cfg and returns an LLMJudge,
// resolving the endpoint AGAINST THE CREDENTIAL in cfg.AuthToken.
//
// The endpoint is a property of the credential, not of the provider:
// MiniMax mints region-scoped keys and each key authenticates only
// against the endpoint it was issued for. So when the configured
// endpoint is one of the catalog candidates for that provider, it is
// replaced by the candidate that accepts THIS key (the trailing path —
// /chat/completions — is preserved).
//
// A configured endpoint that is NOT a catalog candidate (self-hosted
// vLLM, corporate proxy, ...) is left untouched: it is the operator's
// explicit choice and no catalog candidate describes it.
//
// A failed resolution is NOT fatal: the configured endpoint is kept and
// the failure is logged — the judge keeps running.
func NewLLMJudgeWithContext(ctx context.Context, cfg ProviderConfig, hc HTTPClient) (*LLMJudge, error) {
	if hc == nil {
		return nil, fmt.Errorf("%w: nil HTTP client", ErrInvalidConfig)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.WithDefaults()
	endpoint := cfg.Endpoint
	if resolved, err := resolveJudgeEndpoint(ctx, cfg); err != nil {
		// The credential was not accepted by any candidate endpoint.
		// Keep the operator's endpoint and report it (no token in the
		// message — the token is never logged).
		log.Printf("v4judge: provider %s: no endpoint accepts this credential (%v) — keeping the configured endpoint",
			cfg.ProviderID, err)
	} else {
		endpoint = resolved
	}
	return &LLMJudge{
		client:     hc,
		endpoint:   endpoint,
		authToken:  cfg.AuthToken,
		timeout:    time.Duration(cfg.TimeoutMS) * time.Millisecond,
		maxABytes:  cfg.MaxArtifactBytes,
		maxSBytes:  cfg.MaxSpecIntentBytes,
		modelRev:   cfg.ModelRev,
		providerID: cfg.ProviderID,
	}, nil
}

// resolveJudgeEndpoint returns the endpoint the judge must POST to, for
// the credential carried by cfg.
//
//   - No catalog provider behind the provider id → the configured
//     endpoint (the operator's own deployment) is kept as is.
//   - No credential → the configured endpoint is kept as is (nothing to
//     resolve against).
//   - A provider with ONE endpoint candidate → the configured endpoint
//     is kept as is (there is nothing to discover).
//   - A configured endpoint that is NOT one of the candidates → kept as
//     is (self-hosted deployments, proxies, ...).
//   - Otherwise the candidate that accepts the credential wins, and the
//     configured path (/chat/completions) is preserved.
func resolveJudgeEndpoint(ctx context.Context, cfg ProviderConfig) (string, error) {
	if cfg.AuthToken == "" {
		return cfg.Endpoint, nil
	}
	id := strings.TrimPrefix(cfg.ProviderID, "judge-")
	if id == cfg.ProviderID || id == "" {
		return cfg.Endpoint, nil
	}
	canonical, _ := llm.ResolveID(id)
	spec := llm.SpecByID(canonical)
	if spec == nil {
		return cfg.Endpoint, nil
	}
	return resolveJudgeEndpointForSpec(ctx, cfg, spec)
}

// resolveJudgeEndpointForSpec is the testable core of
// resolveJudgeEndpoint: it resolves the endpoint of spec that accepts
// the credential in cfg.
func resolveJudgeEndpointForSpec(ctx context.Context, cfg ProviderConfig, spec *llm.ProviderSpec) (string, error) {
	if cfg.AuthToken == "" || spec == nil {
		return cfg.Endpoint, nil
	}
	candidates := spec.EndpointCandidates()
	if len(candidates) <= 1 {
		return cfg.Endpoint, nil
	}
	suffix, matched := candidatePathSuffix(candidates, cfg.Endpoint)
	if !matched {
		// The operator's endpoint is not a catalog endpoint: leave it.
		return cfg.Endpoint, nil
	}
	ep, err := llm.ResolveCredentialEndpoint(ctx, spec, cfg.AuthToken)
	if err != nil {
		return cfg.Endpoint, err
	}
	return strings.TrimRight(ep.BaseURL, "/") + suffix, nil
}

// candidatePathSuffix returns the path of endpoint relative to the
// candidate base it belongs to (e.g. "/chat/completions"), and whether
// endpoint belongs to one of the candidates at all.
func candidatePathSuffix(candidates []llm.EndpointSpec, endpoint string) (string, bool) {
	ep := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if ep == "" {
		return "", false
	}
	for _, c := range candidates {
		for _, base := range []string{c.BaseURL, c.AnthropicBaseURL} {
			base = strings.TrimRight(strings.TrimSpace(base), "/")
			if base == "" {
				continue
			}
			if ep == base {
				return "", true
			}
			if strings.HasPrefix(ep, base+"/") {
				return ep[len(base):], true
			}
		}
	}
	return "", false
}

// ID returns the provider id verbatim (e.g. "judge-minimax-cn").
// Provenance flows through the audit chain (drift_judge output
// includes ProviderID so the operator can trace any verdict back to
// the binding).
func (j *LLMJudge) ID() string { return j.providerID }

// Verdict is the canonical output of a single Judge call. Confidence
// is in [0, 1]. Verdict is one of the canonical 3 drift values
// ("aligned", "drift_detected", "needs_human"). Reasoning is a
// non-empty paragraph explaining the call.
//
// ProviderID is the provenance field; drift_judge output propagates
// it into the DriftJudgeOutput (which becomes a drift row in
// projects.drift_reports).
type Verdict struct {
	Verdict    string  // aligned | drift_detected | needs_human
	Confidence float64 // [0.0, 1.0]
	Reasoning  string  // non-empty
	ProviderID string  // e.g. "judge-minimax-cn"
	ModelRev   string  // best-effort; empty if unknown
	LatencyMS  int64   // observed wall-clock for this Judge call
}

// Judge sends the drift-judge prompt to the bound LLM and returns
// the parsed verdict. specIntent is the operator-written description
// of what the artifact should be. artifactBody is the resolved
// artifact (already size-capped by the artifact pipeline via
// artifact.Resolver).
//
// Errors:
//   - ErrInputEmpty if specIntent or artifactBody is empty
//   - ErrInputTooLarge if either exceeds the configured byte cap
//   - ErrProviderTimeout on context deadline exceeded
//   - ErrProviderRateLimited on HTTP 429
//   - ErrProviderUnavailable on network failure, HTTP 5xx, HTTP 401/403
//   - ErrProviderBadResponse on 2xx with malformed body (no retry)
//   - ErrNoLLMBound when retry-once also failed on parser contract
//
// On error, the returned Verdict is the zero value; callers should
// classify via errors.Is, not by checking Verdict == zero.
func (j *LLMJudge) Judge(ctx context.Context, specIntent, artifactBody string) (Verdict, error) {
	if specIntent == "" || artifactBody == "" {
		return Verdict{}, ErrInputEmpty
	}
	if len(specIntent) > j.maxSBytes {
		return Verdict{}, fmt.Errorf("%w: spec_intent=%d > %d", ErrInputTooLarge, len(specIntent), j.maxSBytes)
	}
	if len(artifactBody) > j.maxABytes {
		return Verdict{}, fmt.Errorf("%w: artifact_body=%d > %d", ErrInputTooLarge, len(artifactBody), j.maxABytes)
	}

	// Build the request payload (manually for stable field order).
	// The token budget is a property of the MODEL, not of this
	// client: a reasoning model (MiniMax-M3, DeepSeek-R1, Claude with
	// extended thinking) spends part of it on an internal thinking
	// block before answering.
	budget := chatwire.ResolveMaxTokens(j.modelRev, 0)
	payload, err := buildJudgePayload(j.modelRev, budget, false, specIntent, artifactBody)
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: marshal: %w", ErrProviderBadResponse, err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, j.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: build request: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if j.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+j.authToken)
	}

	start := time.Now()
	resp, err := j.client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return Verdict{LatencyMS: latency, ProviderID: j.ID()}, ErrProviderTimeout
		}
		return Verdict{LatencyMS: latency, ProviderID: j.ID()}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// HTTP error mapping (mirror of internal/nli/chat.go::Score).
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return Verdict{LatencyMS: latency, ProviderID: j.ID()}, ErrProviderRateLimited
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return Verdict{LatencyMS: latency, ProviderID: j.ID()}, fmt.Errorf("%w: HTTP %d", ErrProviderUnavailable, resp.StatusCode)
	case resp.StatusCode >= 500:
		return Verdict{LatencyMS: latency, ProviderID: j.ID()}, fmt.Errorf("%w: HTTP %d", ErrProviderUnavailable, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return Verdict{LatencyMS: latency, ProviderID: j.ID()}, fmt.Errorf("%w: HTTP %d", ErrProviderBadResponse, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Verdict{LatencyMS: latency, ProviderID: j.ID()}, fmt.Errorf("%w: read body: %v", ErrProviderUnavailable, err)
	}

	// Unwrap OpenAI wire shape (`{"choices":[{"message":{"content":"..."}}]}`)
	// if present. parseVerdictResponse sees the inner content string.
	// If the wire shape is absent (some lightweight chat servers
	// return the verdict JSON directly), unwrapOpenAIShape returns
	// the body unchanged.
	//
	// The finish reason travels with it: "length" means the model ran
	// out of budget, which for a reasoning model means it spent
	// everything thinking and never answered.
	content, finishReason, unwrapped := chatwire.UnwrapOpenAIShape(body)
	if unwrapped {
		body = []byte(content)
	}

	// Parse the JSON verdict from the response.
	parsed, parseErr := parseVerdictResponse(body)
	if parseErr != nil || chatwire.Truncated(finishReason) {
		// Either the answer did not honour the JSON contract, or the
		// model exhausted its budget while thinking. Retry ONCE with
		// a bigger budget and a reinforced system prompt (see
		// retryOnce for the exact wording). If the retry HTTP call
		// itself fails or returns bad shape, both attempts have
		// failed → ErrNoLLMBound (NOT ErrProviderBadResponse —
		// drift_judge maps ErrNoLLMB to NLI fallback).
		body2, retryErr := j.retryOnce(ctx, chatwire.RetryBudget(budget), specIntent, artifactBody)
		if retryErr != nil {
			return Verdict{LatencyMS: latency, ProviderID: j.ID()}, ErrNoLLMBound
		}
		// Retry HTTP succeeded: parse the new body.
		body2 = unwrapOpenAIShape(body2)
		parsed, parseErr = parseVerdictResponse(body2)
		if parseErr != nil {
			return Verdict{LatencyMS: latency, ProviderID: j.ID()}, ErrNoLLMBound
		}
	}

	parsed.LatencyMS = latency
	parsed.ProviderID = j.ID()
	parsed.ModelRev = j.modelRev
	if err := parsed.Validate(); err != nil {
		return Verdict{LatencyMS: latency, ProviderID: j.ID(), ModelRev: j.modelRev}, err
	}
	return parsed, nil
}

// retryOnce re-issues the request with a reinforced system prompt
// ("REPLY WITH ONLY JSON, NO COMMENTS, NO MARKDOWN"). Returns the new
// body bytes. The retry is parsed by the caller; this helper only
// handles the HTTP plumbing.
//
// On error, returns ErrNoLLMBound (the parser-failure-retry path
// wraps both contract bugs into one terminal error so drift_judge
// returns needs_human instead of falling through to NLI — see
// SPEC-alpha-11-phase14-llm-as-judge §3.2.2 step 5).
func (j *LLMJudge) retryOnce(ctx context.Context, maxTokens int, specIntent, artifactBody string) ([]byte, error) {
	// The retry asks for the JSON shape one more time AND gets a
	// bigger token budget: a reasoning model that exhausted its
	// budget thinking needs room for the answer too.
	payload, err := buildJudgePayload(j.modelRev, maxTokens, true, specIntent, artifactBody)
	if err != nil {
		return nil, fmt.Errorf("%w: build retry payload: %v", ErrNoLLMBound, err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, j.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: build retry request: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if j.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+j.authToken)
	}

	resp, err := j.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: retry: %v", ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrNoLLMBound
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: retry body: %v", ErrProviderUnavailable, err)
	}
	if len(body) == 0 {
		return nil, ErrNoLLMBound
	}
	return body, nil
}

// Validate enforces the canonical Verdict invariants. Used by Judge
// after parsing to catch contracts bugs that the parser missed
// (e.g. confidence > 1.0, verdict string not in VarkSet).
func (v Verdict) Validate() error {
	switch v.Verdict {
	case "aligned", "drift_detected", "needs_human":
	default:
		return fmt.Errorf("%w: unknown verdict %q", ErrProviderBadResponse, v.Verdict)
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return fmt.Errorf("%w: confidence %f out of [0,1]", ErrProviderBadResponse, v.Confidence)
	}
	if v.Reasoning == "" {
		return fmt.Errorf("%w: reasoning is empty", ErrProviderBadResponse)
	}
	return nil
}

// buildJudgePayload constructs the OpenAI-compatible request body.
// Manually marshaled for stable field order (test goldenfiles).
//
// We use a fixed temperature of 0 (deterministic drift verdicts).
//
// max_tokens comes from the per-model table (chatwire.ResolveMaxTokens)
// because a REASONING model spends part of its budget on an internal
// thinking block: MiniMax-M3 needs 1024, a plain chat model 256. The
// hardcoded 512 this used to send was below the floor of every
// reasoning model: the answer never arrived (finish_reason="length")
// and the verdict silently degraded to the NLI fallback.
//
// response_format={"type":"json_object"} enforces the JSON shape on
// providers that support it (OpenAI, DeepSeek, vLLM with
// --guided-decoding).
func buildJudgePayload(modelRev string, maxTokens int, reinforce bool, specIntent, artifactBody string) ([]byte, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type request struct {
		Model          string    `json:"model"`
		Temperature    float64   `json:"temperature"`
		MaxTokens      int       `json:"max_tokens"`
		ResponseFormat any       `json:"response_format"`
		Messages       []message `json:"messages"`
	}
	if maxTokens <= 0 {
		maxTokens = chatwire.DefaultFallbackMaxTokens
	}
	system := judgeSystemPrompt
	if reinforce {
		// The retry asks for the JSON shape one more time, in the
		// first words the model reads.
		system = "REPLY WITH ONLY JSON, NO COMMENTS, NO MARKDOWN. " + system
	}
	req := request{
		Model:       modelRev,
		Temperature: 0,
		MaxTokens:   maxTokens,
		ResponseFormat: map[string]string{
			"type": "json_object",
		},
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: buildJudgeUserPrompt(specIntent, artifactBody)},
		},
	}
	return json.Marshal(req)
}

// json is imported above; this var prevents the linter from removing
// the import for buildJudgePayload.
var _ = json.Marshal
