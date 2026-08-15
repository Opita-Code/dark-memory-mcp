// Package tools — llm_config.go: the LLM_CONFIG namespace (spec 1188
// T7). Four operator-facing tools for managing LLM provider keys
// WITHOUT editing opencode.jsonc or touching env vars:
//
//	llm_key_add(provider, key, validate=true)   — store a key in the
//	    OS keyring (validated against the provider's free probe by
//	    default).
//	llm_key_list(provider?)                      — per-provider status
//	    (has_key, source, last_validation, state). NEVER returns the
//	    key value.
//	llm_key_remove(provider)                     — delete a stored key
//	    (env fallback stays — the env is owned by the harness).
//	llm_provider_status()                        — routing view: which
//	    provider is active, cooldowns, health, pin.
//
// Security contract: NO tool ever returns a key value. llm_key_list
// and llm_provider_status only expose boolean/string state.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
)

// llmConfigKS / llmConfigFailover are test seams: the 4 tools resolve
// their dependencies through these vars so unit tests can inject a
// memory keystore + a no-op failover without touching the OS keyring
// or the background health loop. nil = production path (the
// orchestration singletons).
var (
	llmConfigKS       llmkeystore.KeyStore
	llmConfigFailover llmConfigFailoverer
)

// llmConfigFailoverer is the subset of *llm.FailoverClient the tools
// need. Tests can substitute a stub.
type llmConfigFailoverer interface {
	Status() []llm.CandidateStatus
	LastProviderID() string
}

// resolveLLMConfigKS returns the keystore the tools should use.
func resolveLLMConfigKS() (llmkeystore.KeyStore, error) {
	if llmConfigKS != nil {
		return llmConfigKS, nil
	}
	return orchestration.DefaultKeyStore()
}

// resolveLLMConfigFailover returns the failover client (for status).
func resolveLLMConfigFailover() (llmConfigFailoverer, error) {
	if llmConfigFailover != nil {
		return llmConfigFailover, nil
	}
	fc, err := orchestration.DefaultFailoverClient()
	if err != nil {
		return nil, err
	}
	return fc, nil
}

// RegisterLLMConfig wires the 4 LLM_CONFIG tools. Positioned between
// DELEGATION and JUDGE in the canonical order (config before use).
func RegisterLLMConfig(reg *Registry) {	reg.Add(BindSimple("llm_key_add",
		"Store an LLM provider API key in the OS keyring (Windows Credential Manager / Keychain / Secret Service). Validates the key against the provider's free probe by default. The key value is never returned by any tool.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"provider", "key"},
			"properties": map[string]any{
				"provider": map[string]any{
					"type":        "string",
					"description": "Canonical provider id: anthropic, openai, google, deepseek, minimax, minimax-cn, zhipu, moonshot, qwen.",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "The API key value.",
				},
				"validate": map[string]any{
					"type":        "boolean",
					"description": "Run the free probe (GET /models) before storing. Default true.",
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in struct {
				Provider string `json:"provider"`
				Key      string `json:"key"`
				Validate *bool  `json:"validate"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("llm_key_add: parse: %w", err)
			}
			spec := llm.SpecByID(in.Provider)
			if spec == nil {
				return nil, fmt.Errorf("llm_key_add: unknown provider %q (supported: %s)", in.Provider, strings.Join(llm.CanonicalIDs(), ", "))
			}
			if strings.TrimSpace(in.Key) == "" {
				return nil, fmt.Errorf("llm_key_add: empty key")
			}

			// Optional hot validation (free probe, never a completion).
			if in.Validate == nil || *in.Validate {
				probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
				res := llm.ValidateProvider(probeCtx, spec, in.Key)
				cancel()
				if res.State == llm.ProbeAuthError {
					return nil, fmt.Errorf("llm_key_add: %s rejected the key (probe: %s)", spec.ID, res.Error)
				}
				if res.State == llm.ProbeUnreachable {
					return nil, fmt.Errorf("llm_key_add: %s unreachable (probe: %s) — key NOT stored; check network/baseURL", spec.ID, res.Error)
				}
				// rate_limited / unknown → store anyway (key likely ok,
				// provider throttling or probe path unsupported).
			}

			ks, err := resolveLLMConfigKS()
			if err != nil {
				return nil, fmt.Errorf("llm_key_add: keystore unavailable: %w", err)
			}
			if err := ks.Set(spec.ID, in.Key); err != nil {
				return nil, fmt.Errorf("llm_key_add: store: %w", err)
			}
			return &ToolResponse{Data: map[string]any{
				"provider":     spec.ID,
				"stored":       true,
				"source":       ks.Source(spec.ID),
				"validated":    in.Validate == nil || *in.Validate,
				"model":        spec.DefaultModel,
				"hint":         "The key is now in the OS keyring. Env-var keys still take precedence in the composite only if the keyring read fails.",
			}}, nil
		}))

	reg.Add(BindSimple("llm_key_list",
		"List LLM provider key status. NEVER returns the key value — only whether a key exists, its source (keyring/env/none), and validation state.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"properties": map[string]any{
				"provider": map[string]any{
					"type":        "string",
					"description": "Optional filter: one canonical provider id. Empty = all.",
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in struct {
				Provider string `json:"provider"`
			}
			if len(raw) > 0 && string(raw) != "{}" {
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("llm_key_list: parse: %w", err)
				}
			}
			ks, err := resolveLLMConfigKS()
			if err != nil {
				return nil, fmt.Errorf("llm_key_list: keystore unavailable: %w", err)
			}
			providers := llm.Catalog
			if in.Provider != "" {
				if spec := llm.SpecByID(in.Provider); spec != nil {
					providers = []llm.ProviderSpec{*spec}
				} else {
					return nil, fmt.Errorf("llm_key_list: unknown provider %q", in.Provider)
				}
			}
			out := make([]map[string]any, 0, len(providers))
			for _, spec := range providers {
				out = append(out, map[string]any{
					"provider": spec.ID,
					"has_key":  ks.Has(spec.ID),
					"source":   ks.Source(spec.ID),
					"model":    spec.DefaultModel,
				})
			}
			return &ToolResponse{Data: map[string]any{
				"providers": out,
				"note":      "Key values are never returned. source=keyring means the OS vault; source=env means an env var.",
			}}, nil
		}))

	reg.Add(BindSimple("llm_key_remove",
		"Delete a stored LLM provider key from the OS keyring. If the provider also has an env-var key, that one stays (the env is owned by the harness and cannot be modified here).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"provider"},
			"properties": map[string]any{
				"provider": map[string]any{
					"type":        "string",
					"description": "Canonical provider id to remove from the keyring.",
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in struct {
				Provider string `json:"provider"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("llm_key_remove: parse: %w", err)
			}
			spec := llm.SpecByID(in.Provider)
			if spec == nil {
				return nil, fmt.Errorf("llm_key_remove: unknown provider %q", in.Provider)
			}
			ks, err := resolveLLMConfigKS()
			if err != nil {
				return nil, fmt.Errorf("llm_key_remove: keystore unavailable: %w", err)
			}
			if err := ks.Delete(spec.ID); err != nil {
				return nil, fmt.Errorf("llm_key_remove: %w", err)
			}
			envStill := llmkeystore.EnvStore(llm.EnvKeyForProvider).Has(spec.ID)
			return &ToolResponse{Data: map[string]any{
				"provider":         spec.ID,
				"removed_from":     "keyring",
				"env_fallback":     envStill,
				"hint":             "Use llm_key_list to confirm the current source.",
			}}, nil
		}))

	reg.Add(BindSimple("llm_provider_status",
		"Return the LLM provider routing view: active provider (last successful judge call), per-provider key/cooldown/health, and the DARK_JUDGE_PROVIDER pin. Read-only, no key values.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			fc, err := resolveLLMConfigFailover()
			if err != nil {
				return nil, fmt.Errorf("llm_provider_status: %w", err)
			}
			candidates := fc.Status()
			return &ToolResponse{Data: map[string]any{
				"active_provider": fc.LastProviderID(),
				"candidates":      candidates,
				"note":            "active_provider is the provider that answered the most recent successful judge call (\"\" = none yet).",
			}}, nil
		}))
}
