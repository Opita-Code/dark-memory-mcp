// Package tools - embedder_setup.go: the embedder consent gate (v2.9.1-alpha
// PR-2.1, agent_memory row 164 §3).
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
//     the prompt text + harness context (so the LLM can suggest the
//     harness-native rung).
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
	"fmt"
	"os"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder/detect"
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

	// Harness is the detected host harness kind. PR-2.1 (row 164 §2):
	// the LLM uses this to suggest the harness-native rung
	// (claude-code → Voyage, opencode → OpenAI, etc).
	Harness string `json:"harness,omitempty"`

	// HarnessSource is the detection signal that fired (env var
	// name, config path, "probe:127.0.0.1:11434"). Useful for
	// debugging when operators see a wrong detection.
	HarnessSource string `json:"harness_source,omitempty"`

	// Prompt is the consent text the harness's LLM should surface
	// to the user verbatim (per row 164 §3). Empty when Status=auto.
	Prompt string `json:"prompt,omitempty"`

	// Choices is the canonical choice list. PR-2.1 expands the
	// PR-2 list to include harness-native rungs (voyage, ollama)
	// when a harness is detected.
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
	// Recommended is true when this choice is the harness-native
	// rung (PR-2.1 addition). The LLM can highlight it but should
	// still surface all choices verbatim per row 164 §3.
	Recommended bool `json:"recommended,omitempty"`
}

// consentPromptVerbatim is the row 164 §3 prompt. Kept as a single
// constant so a future translation pass can swap it per language
// without touching the handler logic. PR-2.1 expands the choices
// based on harness detection (see buildConsentStatus).
const consentPromptVerbatim = `dark-memory didn't detect an embedder provider from your harness config.
Detected host: %s
Do you want to:
  1. Provide an API key manually (paste one in your harness's settings, do NOT type it in chat)
  2. Have me auto-configure dark-memory to use bundled local embeddings (works offline, ~22 MB model)
  3. Skip embeddings entirely and use the BM25 + Porter (PR-1) baseline only`

// RegisterEmbedderSetup wires the embedder_setup_prompt tool into the
// registry. Called from RegisterAll per the canonical wiring flow.
func RegisterEmbedderSetup(reg *Registry, st storeEmbedderIntrospector) {
	reg.Add(BindSimple("embedder_setup_prompt",
		"Return the embedder consent prompt (v2.9.1-alpha PR-2.1, row 164 §3). Tool surfaces a structured status (auto | ask) + the verbatim prompt text the harness's LLM should surface to the user when dark-memory boots without a detected provider. Single call per project lifecycle; the choice is persisted to agent_memory so dark-memory never asks again. PR-2.1: response includes Harness + HarnessSource so the LLM can suggest the harness-native rung.",
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
//
// PR-2.1: also surfaces the detected harness + source so the LLM
// can recommend the harness-native rung (e.g., claude-code → Voyage
// AI). The prompt template interpolates the harness name; the
// choice list flags the recommended rung.
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

	// PR-2.1: harness detection (cheap; ~1ms typical).
	harness := detect.Probe()

	// Auto-active paths: any non-none configured embedder means the
	// harness already set up an integration — no consent needed.
	if kind != embedder.KindNone && kind != "" {
		return ConsentStatus{
			Status:        "auto",
			Kind:          kind,
			Dim:           dim,
			Harness:       string(harness.Kind),
			HarnessSource: harness.Source,
		}
	}
	// OPENAI_API_KEY env var check: even with embedder=none the env
	// var is the easiest signal that the user has wire-but-not-yet
	// configured. We treat that as auto-active when env present.
	if os.Getenv("OPENAI_API_KEY") != "" {
		return ConsentStatus{
			Status:        "auto",
			Kind:          embedder.KindOpenAI,
			Dim:           1536,
			Harness:       string(harness.Kind),
			HarnessSource: harness.Source,
			Prompt:        "OPENAI_API_KEY detected; the embedder will activate on first search. No consent prompt required.",
		}
	}

	// Ask path. Build the prompt with harness context + a per-harness
	// recommended rung flagged in the choices list.
	harnessName := string(harness.Kind)
	if harnessName == "" || harnessName == "unknown" {
		harnessName = "(none detected)"
	}
	recommendedKind := harness.PreferredEmbedder()
	choices := []ConsentChoice{
		{Value: "api_key", Label: "Provide an API key manually (paste it in the harness's settings, NOT in chat)."},
		{Value: "bundled_onnx", Label: "Auto-configure dark-memory to use bundled local ONNX embeddings (~22 MB)."},
		{Value: "skip_embeddings", Label: "Skip embeddings entirely; BM25 + Porter (PR-1) baseline only."},
	}
	// PR-2.1: if harness prefers a non-default rung, surface it as a
	// recommended choice. The harness-native rung is always flagged
	// Recommended=true so the LLM can highlight it without violating
	// the "surface verbatim" rule (row 164 §3).
	if recommendedKind != "" && recommendedKind != "none" && recommendedKind != embedder.KindNone {
		label := recommendedKindLabel(recommendedKind)
		if label != "" {
			choices = append(choices, ConsentChoice{
				Value:       recommendedKind,
				Label:       label,
				Recommended: true,
			})
		}
	}
	return ConsentStatus{
		Status:        "ask",
		Kind:          embedder.KindNone,
		Dim:           0,
		Harness:       string(harness.Kind),
		HarnessSource: harness.Source,
		Prompt:        fmt.Sprintf(consentPromptVerbatim, harnessName),
		Choices:       choices,
	}
}

// recommendedKindLabel returns a user-visible label for the harness
// recommended kind, with a short install hint. Empty when the kind
// has no UI string.
func recommendedKindLabel(kind string) string {
	switch kind {
	case embedder.KindVoyage:
		return "Recommended: use Voyage AI voyage-3 (Anthropic's partner embedder). Set VOYAGE_API_KEY in your harness's settings."
	case embedder.KindOllama:
		return "Recommended: use local Ollama (no cloud egress). Make sure `ollama serve` is running with nomic-embed-text."
	case embedder.KindOpenAI:
		return "Recommended: use OpenAI text-embedding-3-small. Set OPENAI_API_KEY in your harness's settings."
	}
	return ""
}
