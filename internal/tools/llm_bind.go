// Package tools — llm_bind.go: the LLM_BIND namespace (Phase 14 T-303).
// Operator-facing tools for binding + probing LLM providers against the
// active project's NLIConfig JSON blob. Wire format is OpenAI-compatible
// /v1/chat/completions (mirrors internal/nli/chat.go::ChatProvider and
// internal/v4alpha/judge/v4judge::LLMJudge — same endpoint, same auth
// header, just different provider_id prefixes and prompt shape).
//
//	llm_provider_bind(provider_id, endpoint, auth_token?, timeout_model?,
//	                  model_rev?) → persists to projects.nli_config_json
//	                    for the ACTIVE project. Validates provider_id starts
//	                    with "judge-" (LLMJudge path) OR "chat-" (NLI path).
//	                    Other prefixes are rejected (sealed boundary).
//
//	llm_provider_probe(provider_id?, endpoint?, auth_token?, timeout_ms?)
//	                    → tiny POST to /v1/chat/completions with
//	                    system="reply with pong", user="ping". Returns
//	                    latency_ms + truncated body excerpt + status. NO
//	                    persistence — pure read.
//
// SECURITY CONTRACT (sealed):
//   - AuthToken is NEVER echoed back. Bind returns the persisted
//     configuration EXCLUDING the auth_token field (same posture as
//     project_create).
//   - Probe returns the response body excerpt, but only when it
//     matches the expected "pong" payload. Malformed or unexpected
//     responses are returned as {ok:false, status:...} without the
//     body content.
//
// POSITIONING (Phase 14): between LLM_CONFIG (keystore mgmt) and
// JUDGE (the consumer). Same shape as LLM_CONFIG: pure operator
// over persisted state, no audit-row emission beyond what the Store
// project update emits.
//
// IDEMPOTENT: llm_provider_bind is a setter — calling it again with
// the same args is a no-op (same primary). Different args overwrite
// the primary.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// llmBindStore is the subset of store.Store the bind/probe tools need.
// Tests can substitute a stub.
type llmBindStore interface {
	ActiveProject() string
	// GetProjectRaw, not GetProject: both tools below need the real
	// bearer, and provider_bind merges partially against the loaded
	// config, so a redacted read would silently wipe the stored
	// token. Sealed exception -- see store.Store.GetProjectRaw.
	GetProjectRaw(ctx context.Context, projectID string) (*project.Project, error)
	CreateProject(ctx context.Context, p *project.Project) error
}

// llmBindCanonicalStore is set by RegisterAll so the handler closures
// have access to the canonical Store. nil-safe (the helpers return a
// helpful error).
var llmBindCanonicalStore llmBindStore

// resolveLLMBindStore returns the store the LLM_BIND tools should use.
// Mirrors resolveLLMConfigKS — falls through to the canonical Store
// from the package-level var (wired at boot).
func resolveLLMBindStore() (llmBindStore, error) {
	if llmBindStoreOverride != nil {
		return llmBindStoreOverride, nil
	}
	if llmBindCanonicalStore == nil {
		return nil, errors.New("llm_bind: no store configured (LLM_BIND tools require the canonical Store)")
	}
	return llmBindCanonicalStore, nil
}

// llmBindStoreOverride is the test seam — tests inject a stub.
var llmBindStoreOverride llmBindStore

// =====================================================================
// llm_provider_bind
// =====================================================================

// LLMProviderBindInput is the input for llm_provider_bind.
type LLMProviderBindInput struct {
	// ProviderID must start with "judge-" (LLMJudge path, Phase 14
	// T-301) or "chat-" (NLI path, Phase 13 T-201). Other prefixes
	// are rejected to enforce the routing boundary. Required.
	ProviderID string `json:"provider_id"`
	// Endpoint is the OpenAI-compatible /v1/chat/completions URL.
	// Required.
	Endpoint string `json:"endpoint"`
	// AuthToken is the bearer token. Optional (some endpoints allow
	// unauthenticated probe traffic for local dev). NEVER echoed
	// back in the result.
	AuthToken string `json:"auth_token,omitempty"`
	// TimeoutMS is the per-request timeout. Default 30000 (matches
	// v4judge.DefaultTimeoutMS). Must be > 0 if set.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	// ModelRev is best-effort (some providers return a model id from
	// the response, but this is operator-supplied). Optional.
	ModelRev string `json:"model_rev,omitempty"`
}

// LLMProviderBindResult is the persisted view of the bound provider
// (auth_token NEVER echoed). Mirrors project.NLIPrimary minus the
// secret.
type LLMProviderBindResult struct {
	ProjectID   string `json:"project_id"`
	ProviderID  string `json:"provider_id"`
	Endpoint    string `json:"endpoint"`
	TimeoutMS   int64  `json:"timeout_ms"`
	ModelRev    string `json:"model_rev,omitempty"`
	BoundAt     string `json:"bound_at"`     // RFC3339 UTC
	AuthPresent bool   `json:"auth_present"` // true if a token was stored
}

// =====================================================================
// llm_provider_probe
// =====================================================================

// LLMProviderProbeInput is the input for llm_provider_probe. Either
// ProviderID (resolves to the active project's primary) OR explicit
// Endpoint+AuthToken must be supplied.
type LLMProviderProbeInput struct {
	// ProviderID is the project's primary provider_id to probe
	// (looks up endpoint + auth_token from NLIConfig). Optional
	// when Endpoint is given.
	ProviderID string `json:"provider_id,omitempty"`
	// Endpoint overrides the resolved one. Required when ProviderID
	// is empty.
	Endpoint string `json:"endpoint,omitempty"`
	// AuthToken overrides the resolved one (used together with
	// Endpoint). Optional.
	AuthToken string `json:"auth_token,omitempty"`
	// TimeoutMS overrides the resolved one. Optional. If < 1,
	// defaults to 5000 (probes should be fast).
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// LLMProviderProbeResult is the read-only outcome of the probe. NO
// persisted state — pure observation.
type LLMProviderProbeResult struct {
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latency_ms"`
	StatusCode int    `json:"status_code"`
	BodyExcerpt string `json:"body_excerpt,omitempty"` // first 64 chars, only on success
	Reason     string `json:"reason,omitempty"`         // on failure
	ProbeAt    string `json:"probe_at"`                // RFC3339 UTC
}

// =====================================================================
// RegisterLLMBind
// =====================================================================

// RegisterLLMBind wires the 2 LLM_BIND tools. Positioned right after
// LLM_CONFIG (keystore mgmt) and before JUDGE (the consumer). Same
// shape as RegisterLLMConfig: pure operator over persisted state,
// no orchestrator dependency.
func RegisterLLMBind(reg *Registry) {
	reg.Add(BindSimple("llm_provider_bind",
		"Bind an LLM provider to the ACTIVE project's NLIConfig (Phase 14 T-303). provider_id must start with 'judge-' (LLMJudge path) or 'chat-' (NLI path). Persists endpoint + auth_token + timeout_ms + model_rev. auth_token is NEVER echoed in the result.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"provider_id", "endpoint"},
			"properties": map[string]any{
				"provider_id": map[string]any{
					"type":        "string",
					"minLength":   7, // "judge-x" / "chat-x"
					"maxLength":   128,
					"description": "Provider id. Must start with 'judge-' (LLMJudge, Phase 14 T-301) or 'chat-' (NLI, Phase 13 T-201). Other prefixes are rejected.",
				},
				"endpoint": map[string]any{
					"type":        "string",
					"format":      "uri",
					"description": "OpenAI-compatible /v1/chat/completions URL.",
				},
				"auth_token": map[string]any{
					"type":        "string",
					"maxLength":   4096,
					"description": "Bearer token. Optional for unauthenticated endpoints. NEVER echoed in the result.",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Per-request timeout in milliseconds. If 0/missing, defaults to 30000.",
				},
				"model_rev": map[string]any{
					"type":        "string",
					"maxLength":   128,
					"description": "Best-effort model revision identifier (e.g. 'MiniMax-M3'). Optional.",
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in LLMProviderBindInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("llm_provider_bind: parse: %w", err)
			}
			return runLLMProviderBind(ctx, in)
		}))

	reg.Add(BindSimple("llm_provider_probe",
		"Probe an LLM provider (Phase 14 T-303). Sends a tiny /v1/chat/completions request with system='reply with pong', user='ping'. Resolves endpoint+auth_token from active project's NLIConfig when provider_id is given; uses explicit endpoint+auth_token otherwise. Returns latency_ms + status + body excerpt. NO persistence.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"properties": map[string]any{
				"provider_id": map[string]any{
					"type":        "string",
					"maxLength":   128,
					"description": "Probe the active project's primary binding for this provider_id. Looks up endpoint + auth_token from NLIConfig.",
				},
				"endpoint": map[string]any{
					"type":        "string",
					"format":      "uri",
					"description": "Override endpoint. Required when provider_id is empty.",
				},
				"auth_token": map[string]any{
					"type":        "string",
					"maxLength":   4096,
					"description": "Override auth_token. Used together with endpoint when provider_id is empty.",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Per-request timeout in milliseconds. Defaults to 5000.",
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in LLMProviderProbeInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("llm_provider_probe: parse: %w", err)
			}
			return runLLMProviderProbe(ctx, in)
		}))
}

// =====================================================================
// Handlers
// =====================================================================

// validateProviderIDPrefix enforces the routing boundary.
// judge-* for LLMJudge (T-301), chat-* for NLI (T-201).
// Other prefixes are rejected.
func validateProviderIDPrefix(id string) error {
	if !strings.HasPrefix(id, "judge-") && !strings.HasPrefix(id, "chat-") {
		return fmt.Errorf("llm_bind: provider_id %q must start with 'judge-' (LLMJudge) or 'chat-' (NLI)", id)
	}
	return nil
}

// runLLMProviderBind persists the binding.
func runLLMProviderBind(ctx context.Context, in LLMProviderBindInput) (*ToolResponse, error) {
	if strings.TrimSpace(in.ProviderID) == "" {
		return nil, errors.New("llm_provider_bind: provider_id required")
	}
	if strings.TrimSpace(in.Endpoint) == "" {
		return nil, errors.New("llm_provider_bind: endpoint required")
	}
	if err := validateProviderIDPrefix(in.ProviderID); err != nil {
		return nil, err
	}
	if in.TimeoutMS < 0 {
		return nil, errors.New("llm_provider_bind: timeout_ms must be > 0 if set")
	}

	st, err := resolveLLMBindStore()
	if err != nil {
		return nil, err
	}
	active := st.ActiveProject()
	if active == "" {
		return nil, store.ErrSessionRequired
	}

	// Load current project (or create the scaffolding for a fresh
	// project that has no NLIConfig yet).
	proj, err := st.GetProjectRaw(ctx, active)
	if err != nil {
		return nil, fmt.Errorf("llm_provider_bind: get active project %q: %w", active, err)
	}
	if proj == nil {
		return nil, fmt.Errorf("llm_provider_bind: active project %q not found", active)
	}

	// Build the new primary. TimeoutMS defaults to 30000 when 0.
	timeout := in.TimeoutMS
	if timeout == 0 {
		timeout = 30000
	}
	primary := project.NLIPrimary{
		ProviderID: in.ProviderID,
		Endpoint:   in.Endpoint,
		AuthToken:  in.AuthToken,
		TimeoutMS:  timeout,
		ModelRev:   in.ModelRev,
	}

	// Merge into existing NLIConfig (preserve fallback if any).
	if proj.NLIConfig == nil {
		proj.NLIConfig = &project.NLIConfig{
			Enabled:           true,
			Primary:           primary,
			LatencyBudgetMS:   30000, // default; validate() requires > 0
			MaxPremiseBytes:   65536, // default for bind-driven NLIConfig
			MaxHypothesisBytes: 4096,  // default
			MaxCacheEntries:   1000,  // default; validate() requires >= 100
			CacheTTLSeconds:   300,   // default; validate() requires > 0
		}
	} else {
		proj.NLIConfig.Primary = primary
		proj.NLIConfig.Enabled = true
		if proj.NLIConfig.LatencyBudgetMS == 0 {
			proj.NLIConfig.LatencyBudgetMS = 30000
		}
		if proj.NLIConfig.MaxPremiseBytes < 64 {
			proj.NLIConfig.MaxPremiseBytes = 65536
		}
		if proj.NLIConfig.MaxHypothesisBytes < 16 {
			proj.NLIConfig.MaxHypothesisBytes = 4096
		}
		if proj.NLIConfig.MaxCacheEntries < 100 {
			proj.NLIConfig.MaxCacheEntries = 1000
		}
		if proj.NLIConfig.CacheTTLSeconds == 0 {
			proj.NLIConfig.CacheTTLSeconds = 300
		}
	}
	if err := proj.NLIConfig.Validate(); err != nil {
		return nil, fmt.Errorf("llm_provider_bind: nli_config validation: %w", err)
	}

	if err := st.CreateProject(ctx, proj); err != nil {
		return nil, fmt.Errorf("llm_provider_bind: persist: %w", err)
	}

	result := LLMProviderBindResult{
		ProjectID:   proj.ProjectID,
		ProviderID:  primary.ProviderID,
		Endpoint:    primary.Endpoint,
		TimeoutMS:   primary.TimeoutMS,
		ModelRev:    primary.ModelRev,
		BoundAt:     time.Now().UTC().Format(time.RFC3339),
		AuthPresent: in.AuthToken != "",
	}
	return &ToolResponse{Data: result}, nil
}

// runLLMProviderProbe sends a tiny POST and returns the outcome.
func runLLMProviderProbe(ctx context.Context, in LLMProviderProbeInput) (*ToolResponse, error) {
	// Resolve endpoint + auth_token (either explicit or from active project's NLIConfig).
	endpoint := in.Endpoint
	authToken := in.AuthToken
	timeout := in.TimeoutMS

	if in.ProviderID != "" {
		if endpoint != "" {
			return nil, errors.New("llm_provider_probe: provider_id and endpoint are mutually exclusive (provider_id resolves endpoint from NLIConfig)")
		}
		if err := validateProviderIDPrefix(in.ProviderID); err != nil {
			return nil, err
		}
		st, err := resolveLLMBindStore()
		if err != nil {
			return nil, err
		}
		active := st.ActiveProject()
		if active == "" {
			return nil, store.ErrSessionRequired
		}
		proj, err := st.GetProjectRaw(ctx, active)
		if err != nil {
			return nil, fmt.Errorf("llm_provider_probe: get active project %q: %w", active, err)
		}
		if proj == nil || proj.NLIConfig == nil {
			return nil, fmt.Errorf("llm_provider_probe: no NLIConfig on active project %q", active)
		}
		// Probe the matching primary or fallback binding.
		if proj.NLIConfig.Primary.ProviderID == in.ProviderID {
			endpoint = proj.NLIConfig.Primary.Endpoint
			authToken = proj.NLIConfig.Primary.AuthToken
			if timeout == 0 {
				timeout = proj.NLIConfig.Primary.TimeoutMS
			}
		} else if proj.NLIConfig.FallbackEnabled && proj.NLIConfig.Fallback.ProviderID == in.ProviderID {
			endpoint = proj.NLIConfig.Fallback.Endpoint
			authToken = proj.NLIConfig.Fallback.AuthToken
			if timeout == 0 {
				timeout = proj.NLIConfig.Fallback.TimeoutMS
			}
		} else {
			return nil, fmt.Errorf("llm_provider_probe: provider_id %q not bound on active project %q", in.ProviderID, active)
		}
	}

	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("llm_provider_probe: endpoint required (explicit or via provider_id lookup)")
	}
	if timeout <= 0 {
		timeout = 5000
	}

	result := probeLLMProvider(ctx, endpoint, authToken, timeout)
	return &ToolResponse{Data: result}, nil
}

// probeLLMProvider sends a tiny POST and returns the outcome.
// Exposed as a free function so tests can call it directly with a
// stub server.
func probeLLMProvider(ctx context.Context, endpoint, authToken string, timeoutMS int64) LLMProviderProbeResult {
	now := time.Now().UTC().Format(time.RFC3339)
	body := `{"model":"probe","messages":[{"role":"system","content":"reply with pong"},{"role":"user","content":"ping"}],"temperature":0,"max_tokens":4}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
	if err != nil {
		return LLMProviderProbeResult{OK: false, Reason: err.Error(), ProbeAt: now}
	}
	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	httpClient := &http.Client{Timeout: time.Duration(timeoutMS) * time.Millisecond}
	start := time.Now()
	resp, err := httpClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return LLMProviderProbeResult{OK: false, LatencyMS: latency, Reason: err.Error(), ProbeAt: now}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return LLMProviderProbeResult{OK: false, LatencyMS: latency, StatusCode: resp.StatusCode, Reason: "read body: " + err.Error(), ProbeAt: now}
	}
	excerpt := ""
	if len(raw) > 0 && len(raw) <= 64 {
		excerpt = string(raw)
	} else if len(raw) > 64 {
		excerpt = string(raw[:64]) + "..."
	}
	ok := resp.StatusCode == 200
	reason := ""
	if !ok {
		reason = fmt.Sprintf("status %d", resp.StatusCode)
	}
	return LLMProviderProbeResult{
		OK:          ok,
		LatencyMS:   latency,
		StatusCode:  resp.StatusCode,
		BodyExcerpt: excerpt,
		Reason:      reason,
		ProbeAt:     now,
	}
}