// Package oauth — tools.go: wire tools that expose the OAuth router to
// MCP clients (harnesses like opencode). 4 tools added to the canonical
// registry, bringing the total from 73 → 77 canonical tools.
//
//   dark_memory_judge_options     — list supported OAuth providers + their free tier info
//   dark_memory_oauth_connect     — initiate an OAuth flow; returns auth_url + state
//   dark_memory_oauth_status      — list connected providers + token expiry
//   dark_memory_oauth_disconnect  — revoke + delete tokens for a provider
//
// These are registered via RegisterOAuth which the cmd/dark-mem-mcp
// boot calls after the rest of the tools are registered.
package oauth

import (
	"context"
	"encoding/json"

	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// RegisterOAuth wires the 4 OAuth tools into the registry.
//
// Note: we deliberately use a small custom binder here rather than
// tools.BindSimple/BindStore because the wire tools here need access
// to the router singleton created earlier in boot. We package the
// router in a closure passed via RegisterOAuth.
//
// Tool shapes follow the existing BindOrchestrator pattern (input is
// a typed struct, output is a JSON-encoded struct). Schemas are
// strictly defined with `additionalProperties: false` per the
// project's F35/F36 wire-conformance contract.
func RegisterOAuth(reg *tools.Registry, router *Router) {
	registerJudgeOptions(reg, router)
	registerOAuthConnect(reg, router)
	registerOAuthStatus(reg, router)
	registerOAuthDisconnect(reg, router)
}

// addTool is a small helper that registers a bare-metal tool without
// going through the orchestrator. We use the simpler shape since
// OAuth is a leaf-level concern (no store, no orchestrator).
func addTool(reg *tools.Registry, name, description string, schema json.RawMessage, handler tools.HandlerFunc) {
	reg.Add(&tools.Tool{
		Name:        name,
		Description: description,
		InputSchema: schema,
		Handler:     handler,
	})
}

// JudgeOptionsInput is the input for dark_memory_judge_options.
type JudgeOptionsInput struct{}

// JudgeOptionsResult is the output: list of providers + free tier info.
type JudgeOptionsResult struct {
	Providers []ProviderInfo `json:"providers"`
}

func registerJudgeOptions(reg *tools.Registry, router *Router) {
	addTool(reg, "judge_options",
		"List the OAuth providers available for self-authenticated drift_judge. Each provider includes display name, supported flows, scopes, endpoint, model, and free tier limits. After reviewing the list, call dark_memory_oauth_connect({provider}) to initiate authentication with your own provider account.",
		mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
			return &tools.ToolResponse{
				Data: JudgeOptionsResult{Providers: router.Providers()},
			}, nil
		})
}

// OAuthConnectInput starts an OAuth flow.
type OAuthConnectInput struct {
	Provider string   `json:"provider"`
	Scopes   []string `json:"scopes,omitempty"`
}

// OAuthConnectResult is the auth_url + state the user opens in browser.
type OAuthConnectResult struct {
	AuthURL       string   `json:"auth_url"`
	State         string   `json:"state"`
	ExpiresAt     string   `json:"expires_at"`
	Provider      string   `json:"provider"`
	CallbackPort  int      `json:"callback_port"`
	Instructions  string   `json:"instructions"`
}

func registerOAuthConnect(reg *tools.Registry, router *Router) {
	addTool(reg, "oauth_connect",
		"Initiate an OAuth 2.0 + PKCE flow for the given provider (google|huggingface). Returns auth_url and state — open auth_url in any browser, approve, and return to your harness. The callback server listens on localhost:9335 (configurable via DARK_OAUTH_CALLBACK_PORT) and accepts the redirect automatically. If the callback arrives, tokens are persisted to the OS keyring (or env-var fallback) and the next dark_memory_vibe_publish / dark_memory_judge call uses the provider's free tier.",
		mustJSON(map[string]any{
			"type":                 "object",
			"required":             []string{"provider"},
			"additionalProperties": false,
			"properties": map[string]any{
				"provider": map[string]any{
					"type": "string",
					"enum": []string{"google", "huggingface"},
					"description": "OAuth provider to authenticate with. 'google' = Google Gemini (60 RPM, 1500 RPD free). 'huggingface' = HuggingFace Hub (1000 RPD free, supports Device Code Flow for headless).",
				},
				"scopes": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Override default scopes for this provider. Leave empty to use defaults (generative-language.retriever for Google; openid profile inference-api for HuggingFace).",
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
			var in OAuthConnectInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			// Fire-and-forget: start the flow, return auth_url.
			// The flow continues in background; user returns to
			// harness when ready.
			resp, err := router.Connect(ctx, ConnectRequest{
				Provider: Provider(in.Provider),
				Scopes:   in.Scopes,
			})
			if err != nil {
				return nil, err
			}
			return &tools.ToolResponse{
				Data: OAuthConnectResult{
					AuthURL:      resp.AuthURL,
					State:        resp.State,
					ExpiresAt:    resp.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
					Provider:     string(resp.Provider),
					CallbackPort: resp.CallbackPort,
					Instructions: resp.Instructions,
				},
			}, nil
		})
}

// OAuthStatusInput filters by provider (optional).
type OAuthStatusInput struct {
	Provider string `json:"provider,omitempty"`
}

// OAuthStatusResult lists connections + their state.
type OAuthStatusResult struct {
	Connections []ConnectionDetail `json:"connections"`
}

// ConnectionDetail is the wire shape of a single OAuth connection.
type ConnectionDetail struct {
	Provider      Provider         `json:"provider"`
	DisplayName   string           `json:"display_name"`
	Status        ConnectionStatus `json:"status"`
	AccountEmail  string           `json:"account_email,omitempty"`
	ExpiresAt     string           `json:"expires_at,omitempty"`
	HasRefresh    bool             `json:"has_refresh"`
	FreeTier      string           `json:"free_tier,omitempty"`
	Model         string           `json:"model,omitempty"`
}

func registerOAuthStatus(reg *tools.Registry, router *Router) {
	addTool(reg, "oauth_status",
		"List the OAuth connections currently stored on this machine. Each connection shows status (connected|expired|error|disconnected), expiry timestamp, and free-tier limits. Use this to verify dark_memory_oauth_connect succeeded before publishing an artifact with drift_judge.",
		mustJSON(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"provider": map[string]any{
					"type":        "string",
					"description": "Optional: filter by provider. Empty = list all.",
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
			var in OAuthStatusInput
			_ = json.Unmarshal(raw, &in)
			providers := router.Providers()
			var conns []ConnectionDetail
			for _, info := range providers {
				if in.Provider != "" && string(info.Provider) != in.Provider {
					continue
				}
				ts, _, _ := router.keystore.LoadToken(info.Provider)
				conns = append(conns, ConnectionDetail{
					Provider:     info.Provider,
					DisplayName:  info.DisplayName,
					Status:       info.Status,
					AccountEmail: ts.AccountEmail,
					ExpiresAt:    ts.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
					HasRefresh:   ts.CanRefresh(),
					FreeTier:     info.FreeTier,
					Model:        info.Model,
				})
			}
			return &tools.ToolResponse{Data: OAuthStatusResult{Connections: conns}}, nil
		})
}

// OAuthDisconnectInput specifies which provider to disconnect.
type OAuthDisconnectInput struct {
	Provider string `json:"provider"`
}

// OAuthDisconnectResult confirms the disconnect.
type OAuthDisconnectResult struct {
	Provider    Provider `json:"provider"`
	Disconnected bool    `json:"disconnected"`
}

func registerOAuthDisconnect(reg *tools.Registry, router *Router) {
	addTool(reg, "oauth_disconnect",
		"Disconnect an OAuth provider: revoke the access_token (best-effort, depends on provider) and delete all keyring entries (access, refresh, metadata). The next dark_memory_vibe_publish call will fall back to the next available tier (BYOK or self-hosted).",
		mustJSON(map[string]any{
			"type":                 "object",
			"required":             []string{"provider"},
			"additionalProperties": false,
			"properties": map[string]any{
				"provider": map[string]any{
					"type": "string",
					"enum": []string{"google", "huggingface"},
				},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
			var in OAuthDisconnectInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			err := router.Disconnect(ctx, Provider(in.Provider))
			return &tools.ToolResponse{
				Data: OAuthDisconnectResult{
					Provider:     Provider(in.Provider),
					Disconnected: err == nil,
				},
			}, nil
		})
}

// mustJSON marshals the given value to JSON or panics. Used for
// inline schema definitions in tool registration.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}