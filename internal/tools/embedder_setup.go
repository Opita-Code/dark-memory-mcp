// Package tools - embedder_setup.go: the embedder consent gate (v2.9.0-alpha
// PR-2, agent_memory row 164 §3).
//
// Single MCP tool: dark_memory_embedder_setup_prompt. Returns the consent
// question the harness's LLM should surface to the user when dark-memory
// boots without a detected provider AND OPENAI_API_KEY unset.
//
// Why a tool (and not a deep-link in another tool): the consent is a
// PROJECT-LIFECYCLE event, fired once per project. Tools.list surfaces
// it so the harness's LLM can decide when to call it (at first
// search/list call after boot, per row 164 §3 timing).
//
// # Failure modes
//
//   - Embedder is openai or onnx configured   → returns status="auto"
//     (no prompt needed; config is sufficient).
//   - Embedder is none + OPENAI_API_KEY unset → returns status="ask" +
//     the prompt text.
//   - Embedder is openai but key 401s later  → not detected here;
//     first .Embed call surfaces embedder.ErrKeyMissing wrapped.
//
// The response is intentionally LLM-readable (small, structured) so
// the harness's LLM can either surface verbatim or paraphrase per
// row 164 §3's "verbatim" guidance.
package tools

import (
	"context"
	"encoding/json"
	"os"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// ConsentStatus is the structured response shape. JSON-tagged so
// schema drift is visible at the wire boundary.
type ConsentStatus struct {
	// Status is "auto" (no prompt needed; embedder active) or "ask"
	// (prompt required; user must choose). Future "dismissed" when
	// the operator has answered (per row 164 §3, persisted to
	// agent_memory).
	Status string `json:"status"`

	// Kind is the active embedder's Kind() string. "none" when no
	// embedder is configured.
	Kind string `json:"kind"`

	// Dim is the active embedder's dimensionality. 0 when none.
	Dim int `json:"dim"`

	// Prompt is the consent text the harness's LLM should surface
	// to the user verbatim (per row 164 §3). Empty when Status=auto.
	Prompt string `json:"prompt,omitempty"`

	// Choices is the canonical choice list. v2.9.0 freezes these
	// three; future PR-2.1 may add bundled-ONNX inference.
	Choices []ConsentChoice `json:"choices,omitempty"`
}

// ConsentChoice is one branch the user can take. Value is the
// canonical token the harness's LLM should send back in the
// embedder_setup_choose tool (future, post-PR-2).
type ConsentChoice struct {
	// Value is the canonical token.
	Value string `json:"value"`
	// Label is the user-visible description (English, vibe-coder).
	Label string `json:"label"`
}

// consentPromptVerbatim is the row 164 §3 prompt. Kept as a single
// constant so a future PR-2.1 translation pass can swap it per
// language without touching the handler logic.
const consentPromptVerbatim = `dark-memory didn't detect an LLM provider from your harness config.
Do you want to:
  1. Provide an API key manually (paste one in your harness's settings, do NOT type it in chat)
  2. Have me auto-configure dark-memory to use bundled local embeddings (works offline, ~22 MB model)
  3. Skip embeddings entirely and use the BM25 + Porter (PR-1) baseline only`

// RegisterEmbedderSetup wires the embedder_setup_prompt tool into the
// registry. Called from RegisterAll per the canonical wiring flow.
func RegisterEmbedderSetup(reg *Registry, st storeEmbedderIntrospector) {
	reg.Add(BindSimple("embedder_setup_prompt",
		"Return the embedder consent prompt (v2.9.0-alpha PR-2, row 164 §3). Tool surfaces a structured status (auto | ask) + the verbatim prompt text the harness's LLM should surface to the user when dark-memory boots without a detected provider. Single call per project lifecycle; the choice is persisted to agent_memory so dark-memory never asks again.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			out := buildConsentStatus(st)
			return &ToolResponse{Data: out}, nil
		}))
}

// storeEmbedderIntrospector is the minimal interface RegisterEmbedderSetup
// needs. Declared locally so tools does not depend on the full
// store.Store — tests can pass a stub.
type storeEmbedderIntrospector interface {
	Embedder() embedder.Embedder
}

// buildConsentStatus inspects the embedder's Kind() and decides
// auto/ask. The function is pure (no I/O); it does NOT log the
// env state — operators can re-derive it via env.HealthProbe or
// active_policy when needed.
func buildConsentStatus(st storeEmbedderIntrospector) ConsentStatus {
	var kind string
	var dim int
	if st != nil {
		e := st.Embedder()
		if e != nil {
			kind = e.Kind()
			dim = e.Dim()
		}
	}
	if kind == "" {
		kind = embedder.KindNone
	}
	// Auto-active paths: any non-none configured embedder means the
	// harness already set up an integration — no consent needed.
	if kind != embedder.KindNone && kind != "" {
		return ConsentStatus{
			Status: "auto",
			Kind:   kind,
			Dim:    dim,
		}
	}
	// OPENAI_API_KEY env var check: even with embedder=none the env
	// var is the easiest signal that the user has wire-but-not-yet
	// configured. We treat that as auto-active when env present.
	if os.Getenv("OPENAI_API_KEY") != "" {
		return ConsentStatus{
			Status: "auto",
			Kind:   embedder.KindOpenAI,
			Dim:    1536,
			Prompt: "OPENAI_API_KEY detected; the embedder will activate on first search. No consent prompt required.",
		}
	}
	// Ask path.
	return ConsentStatus{
		Status: "ask",
		Kind:   embedder.KindNone,
		Dim:    0,
		Prompt: consentPromptVerbatim,
		Choices: []ConsentChoice{
			{Value: "api_key", Label: "Provide an API key manually (paste it in the harness's settings, NOT in chat)."},
			{Value: "bundled_onnx", Label: "Auto-configure dark-memory to use bundled local ONNX embeddings (~22 MB)."},
			{Value: "skip_embeddings", Label: "Skip embeddings entirely; BM25 + Porter (PR-1) baseline only."},
		},
	}
}
