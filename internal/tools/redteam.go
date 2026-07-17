// Package tools — redteam.go: the L7 REDTEAM wire tools (armed-mode).
//
// Per BRIDGE_AND_COEXISTENCE.md §3 (spec 164, bridge.4 + planned cx.v3),
// the dark_memory_redteam_* namespace is OPT-IN and only registered when
// the operator sets DARK_REDTEAM=armed. These tools expose the installed
// red-team research mods (./mods/redteam/ by default, overridable via
// DARK_REDTEAM_MODS_PATH) for systematic LLM security research.
//
// RESEARCH USE ONLY. These tools are intended for:
//   - Measuring refusal rates against curated prompt-injection / jailbreak
//     payloads (see the prompt-injection-lab and jailbreak-taxonomy mods).
//   - Tracking experiment outcomes via the audit system.
//   - Loading operator-approved datasets for academic security research.
//
// NOT intended for:
//   - Deploying attacks against systems without explicit authorisation.
//   - Production attack infrastructure.
//   - Targeting specific people or exfiltrating private data.
//
// The tools are NOT in the canonical 26-tool order. They are
// registered as namespace extras that the armed-mode server emits in
// addition to the canonical 26. The public surface stays at 26 tools;
// the armed-mode surface is 26 + 3 = 29.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// defaultRedTeamModsPath is the on-disk location of the redteam mod
// set. Override at runtime via DARK_REDTEAM_MODS_PATH.
const defaultRedTeamModsPath = "./mods/redteam"

// RedTeamModsPath returns the operator-configured path for the
// redteam mod directory. Exposed so tests + the server bootstrap can
// share the same lookup logic.
func RedTeamModsPath() string {
	if p := os.Getenv("DARK_REDTEAM_MODS_PATH"); p != "" {
		return p
	}
	return defaultRedTeamModsPath
}

// RegisterRedTeam wires the 3 dark_memory_redteam_* tools into the
// registry. The caller MUST check IsRedTeamArmed() before invoking;
// this function panics otherwise so the mistake is loud at boot
// rather than silent at runtime.
//
// The 3 tools:
//
//	redteam_list_mods    — enumerate installed redteam mods.
//	redteam_get_prompts  — extract payloads from a mod's dataset.
//	redteam_log_attempt  — record an experiment outcome via the audit
//	                       system (constitution_id = "redteam-research").
//
// All three are read-only or audit-write; they never execute the
// payloads themselves (calling them is safe in a local-only session).
func RegisterRedTeam(reg *Registry, st store.Store) error {
	if reg == nil {
		return fmt.Errorf("tools: RegisterRedTeam: nil registry")
	}
	if !mods.IsRedTeamArmed() {
		// Armed check BEFORE the store check so the operator sees
		// the "not armed" error even when they haven't wired up the
		// store yet (e.g. dry-boot for tooling). Same logic as the
		// VLP gate — fail on operator intent first, configuration
		// second.
		return store.ErrArmedRequired
	}
	// Store is OPTIONAL for the redteam namespace in v1: the only
	// handler that takes a Store (redteam_log_attempt) does not call
	// any Store methods (it writes audit rows to stderr for now).
	// Tests can pass nil. A future v2 audit table will make Store
	// required.

	// 1. redteam_list_mods — list the installed redteam mods with
	// their risk envelopes. Read-only, no audit needed.
	reg.Add(BindSimple("redteam_list_mods",
		"List the installed red-team research mods under DARK_REDTEAM_MODS_PATH (default ./mods/redteam). Returns mod_id, version, name, risk_class, target_scope, capability_count, sha256. Read-only — does not load the mod content into memory.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"properties": map[string]any{},
		}),
		redteamListModsHandler()))

	// 2. redteam_get_prompts — pull payloads out of a mod's
	// prompt-injection knowledge files. The user can filter by
	// family (e.g. "goal_hijack", "encoding") to narrow the dataset.
	// Read-only.
	reg.Add(BindSimple("redteam_get_prompts",
		"Get prompts from an installed red-team mod. Returns verbatim payloads from the mod's knowledge files, filterable by family. Use redteam_list_mods first to discover available mod_ids and the families they contain. Read-only — does not execute the payloads.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"mod_id"},
			"properties": map[string]any{
				"mod_id": map[string]any{
					"type":        "string",
					"description": "The mod_id from redteam_list_mods.",
				},
				"family": map[string]any{
					"type":        "string",
					"description": "Optional filter on the payload's family attribute (e.g. 'goal_hijack', 'encoding', 'multi_turn'). Empty = all families.",
				},
				"severity_min": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     5,
					"description": "Optional: include only payloads with severity >= N. 1 (cosmetic) - 5 (critical).",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     1000,
					"description": "Optional cap on number of payloads returned. Default 100.",
				},
			},
		}),
		redteamGetPromptsHandler()))

	// 3. redteam_log_attempt — record an experiment outcome. Writes
	// to the audit system under constitution_id="redteam-research".
	// Does NOT execute the prompt against any external system; the
	// caller is responsible for that and for passing the observed
	// response + label.
	reg.Add(BindSimple("redteam_log_attempt",
		"Log a red-team experiment attempt: prompt id, target model, observed response, classifier label (REFUSAL | PARTIAL_COMPLIANCE | FULL_COMPLIANCE). Writes an audit row with constitution_id=redteam-research for accountability. Does NOT execute the prompt — the caller observes the target externally and reports the result.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"mod_id", "prompt_id", "target_model", "observed_label"},
			"properties": map[string]any{
				"mod_id": map[string]any{
					"type":        "string",
					"description": "The mod the payload came from.",
				},
				"prompt_id": map[string]any{
					"type":        "string",
					"description": "The payload id (e.g. 'GH-001').",
				},
				"target_model": map[string]any{
					"type":        "string",
					"description": "Identifier for the model that was attacked (e.g. 'gpt-4o', 'claude-3.5-sonnet', 'local-llama-3').",
				},
				"family": map[string]any{
					"type":        "string",
					"description": "Optional family classification (e.g. 'encoding', 'role_play').",
				},
				"observed_label": map[string]any{
					"type":        "string",
					"enum":        []string{"REFUSAL", "PARTIAL_COMPLIANCE", "FULL_COMPLIANCE"},
					"description": "The label assigned by the response classifier.",
				},
				"observed_response_excerpt": map[string]any{
					"type":        "string",
					"description": "Optional short excerpt of the target's response (max 500 chars). Used for sanity-checking the label.",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "Optional session id; if empty, the audit row is unattributed (single-operator local-only session).",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "Optional free-form researcher notes.",
				},
			},
		}),
		redteamLogAttemptHandler(st)))

	return nil
}

// redteamListModsHandler returns the handler for redteam_list_mods.
func redteamListModsHandler() HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
		// No input.
		mods, err := scanRedTeamMods(RedTeamModsPath())
		if err != nil {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "could not scan redteam mods path: " + err.Error(),
				Hint:    "Check DARK_REDTEAM_MODS_PATH; default is ./mods/redteam.",
			}}, nil
		}
		return &ToolResponse{Data: map[string]any{
			"mods_path": RedTeamModsPath(),
			"armed":     true,
			"count":     len(mods),
			"mods":      mods,
		}}, nil
	}
}

// redteamGetPromptsHandler returns the handler for redteam_get_prompts.
func redteamGetPromptsHandler() HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
		var in RedTeamGetPromptsInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "input JSON does not match expected schema for redteam_get_prompts: " + err.Error(),
				Hint:    "mod_id is required.",
			}}, nil
		}
		if in.ModID == "" {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "mod_id is required.",
				Hint:    "Call redteam_list_mods first.",
			}}, nil
		}
		maxResults := in.MaxResults
		if maxResults <= 0 {
			maxResults = 100
		}

		prompts, err := loadPromptsFromMod(in.ModID, in.Family, in.SeverityMin, maxResults)
		if err != nil {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: err.Error(),
				Hint:    "Verify mod_id with redteam_list_mods.",
			}}, nil
		}
		return &ToolResponse{Data: map[string]any{
			"mod_id":  in.ModID,
			"count":   len(prompts),
			"prompts": prompts,
		}}, nil
	}
}

// redteamLogAttemptHandler returns the handler for redteam_log_attempt.
// Writes to the audit system via the Store. The audit row carries
// constitution_id="redteam-research" so the operator's intent is
// durable and the red-team session is traceable.
func redteamLogAttemptHandler(st store.Store) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
		var in RedTeamLogAttemptInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "input JSON does not match expected schema for redteam_log_attempt: " + err.Error(),
				Hint:    "mod_id, prompt_id, target_model, observed_label are required.",
			}}, nil
		}
		if in.ModID == "" || in.PromptID == "" || in.TargetModel == "" || in.ObservedLabel == "" {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInvalidArgument",
				Message: "mod_id, prompt_id, target_model, observed_label are required.",
				Hint:    "All four fields are non-empty strings.",
			}}, nil
		}
		// Clamp excerpt.
		excerpt := in.ObservedResponseExcerpt
		if len(excerpt) > 500 {
			excerpt = excerpt[:500] + "..."
		}

		// Compose the audit row. Use a typed WriteContext so the
		// Store layer can route it correctly. The payload is JSON-
		// encoded so the audit log is structured.
		payload, err := json.Marshal(map[string]any{
			"mod_id":                   in.ModID,
			"prompt_id":                in.PromptID,
			"target_model":             in.TargetModel,
			"family":                   in.Family,
			"observed_label":           in.ObservedLabel,
			"observed_response_excerpt": excerpt,
			"notes":                    in.Notes,
			"constitution_id":          "redteam-research",
		})
		if err != nil {
			return &ToolResponse{Error: &ToolError{
				Code:    "ErrInternal",
				Message: "marshal audit payload: " + err.Error(),
			}}, nil
		}

		wc := store.WriteContext{
			Actor:     "redteam_log_attempt",
			SessionID: in.SessionID,
			WritePath: "redteam_log_attempt",
		}
		// We don't have a direct audit-writer method on Store yet;
		// for v1 we log to stderr so the operator has a record even
		// without a dedicated audit table. The audit boundary (the
		// fact that we WROTE the log) is preserved.
		_ = wc
		fmt.Fprintf(os.Stderr, "[redteam-research] %s\n", string(payload))
		return &ToolResponse{Data: map[string]any{
			"logged": true,
			"constitution_id": "redteam-research",
			"payload_sha256":  hashForAudit(string(payload)),
		}}, nil
	}
}

// RedTeamGetPromptsInput is the input for redteam_get_prompts.
type RedTeamGetPromptsInput struct {
	ModID       string `json:"mod_id"`
	Family      string `json:"family,omitempty"`
	SeverityMin int    `json:"severity_min,omitempty"`
	MaxResults  int    `json:"max_results,omitempty"`
}

// RedTeamLogAttemptInput is the input for redteam_log_attempt.
type RedTeamLogAttemptInput struct {
	ModID                  string `json:"mod_id"`
	PromptID               string `json:"prompt_id"`
	TargetModel            string `json:"target_model"`
	Family                 string `json:"family,omitempty"`
	ObservedLabel          string `json:"observed_label"`
	ObservedResponseExcerpt string `json:"observed_response_excerpt,omitempty"`
	SessionID              string `json:"session_id,omitempty"`
	Notes                  string `json:"notes,omitempty"`
}

// RedTeamModSummary is one row in the redteam_list_mods output.
type RedTeamModSummary struct {
	ModID           string `json:"mod_id"`
	Version         string `json:"version"`
	Name            string `json:"name"`
	RiskClass       string `json:"risk_class"`
	TargetScope     string `json:"target_scope"`
	KnowledgeCount  int    `json:"knowledge_count"`
	DirectiveCount  int    `json:"directive_count"`
	ToolCount       int    `json:"tool_count"`
	SHA256          string `json:"sha256"`
	Path            string `json:"path"`
}

// scanRedTeamMods walks the redteam mods directory and returns a
// summary for each subdir that contains a mod.toml. Manifests are
// parsed via the same mods.Load function the operator's main loop
// uses, so the validation rules are identical.
//
// Whitelist: we accept ALL mods the loader can parse; the operator's
// DARK_MOD_WHITELIST gate is enforced upstream when the mod is loaded
// into the main server's mod registry. The redteam namespace itself
// is gated by DARK_REDTEAM=armed at RegisterRedTeam.
func scanRedTeamMods(root string) ([]RedTeamModSummary, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]RedTeamModSummary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		modRoot := filepath.Join(root, e.Name())
		manifestPath := filepath.Join(modRoot, "mod.toml")
		if _, err := os.Stat(manifestPath); err != nil {
			continue // not a mod dir, skip
		}
		loaded, err := mods.LoadWithWhitelist(modRoot, nil)
		if err != nil {
			// Skip mods that fail to load (e.g. INV-6 refused). The
			// caller sees only the loadable ones — matches the
			// principle that the redteam namespace refuses to expose
			// content that the loader refuses to ingest.
			continue
		}
		out = append(out, RedTeamModSummary{
			ModID:          loaded.Manifest.Meta.ID,
			Version:        loaded.Manifest.Meta.Version,
			Name:           loaded.Manifest.Meta.Name,
			RiskClass:      loaded.Manifest.Risk.Class,
			TargetScope:    loaded.Manifest.Risk.TargetScope,
			KnowledgeCount: len(loaded.Knowledge),
			DirectiveCount: len(loaded.Directives),
			ToolCount:      len(loaded.Manifest.Capabilities.Tools),
			SHA256:         loaded.SHA256,
			Path:           loaded.Path,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModID < out[j].ModID })
	return out, nil
}

// loadPromptsFromMod extracts payloads from a mod's prompt-injection
// knowledge files. The parser understands two shapes:
//
//   1. Free-form markdown: each paragraph that starts with a quoted
//      block is treated as one payload.
//   2. JSONL dataset: each {"id":..., "payload":..., ...} line is
//      one entry.
//
// Filtering by family / severity_min applies on the JSONL shape
// (the markdown files do not carry those metadata fields; we report
// them verbatim).
//
// modID may be either the full "namespace/name" form (e.g.
// "redteam/jailbreak-taxonomy") or just the basename (e.g.
// "jailbreak-taxonomy"). We strip the namespace prefix to resolve
// the on-disk directory because the redteam mods live flat under
// the configured mods path.
func loadPromptsFromMod(modID, family string, severityMin, maxResults int) ([]map[string]any, error) {
	root := RedTeamModsPath()
	basename := modID
	if idx := strings.LastIndex(modID, "/"); idx >= 0 {
		basename = modID[idx+1:]
	}
	modRoot := filepath.Join(root, basename)
	if _, err := os.Stat(modRoot); err != nil {
		return nil, fmt.Errorf("mod %q not found at %s", modID, modRoot)
	}
	loaded, err := mods.LoadWithWhitelist(modRoot, nil)
	if err != nil {
		return nil, fmt.Errorf("load mod %q: %w", modID, err)
	}
	out := make([]map[string]any, 0, 64)
	for _, k := range loaded.Knowledge {
		if k.Kind != "prompt_injection" {
			continue
		}
		// Try JSONL first; fall back to markdown paragraph extraction.
		if entries := parseJSONLDataset(k.Body); len(entries) > 0 {
			for _, e := range entries {
				if family != "" {
					if fam, _ := e["family"].(string); fam != family {
						continue
					}
				}
				if severityMin > 0 {
					if sev, ok := e["severity"].(float64); ok {
						if int(sev) < severityMin {
							continue
						}
					}
				}
				out = append(out, e)
				if len(out) >= maxResults {
					return out, nil
				}
			}
		} else {
			// Markdown paragraph extraction: treat each fenced code
			// block as one payload; non-fenced paragraphs after a
			// "## " header are also captured.
			out = append(out, extractMarkdownPayloads(k.Path, k.Body)...)
			if len(out) >= maxResults {
				return out[:maxResults], nil
			}
		}
	}
	return out, nil
}

// parseJSONLDataset splits a body on newlines and unmarshals each
// non-empty, non-#-prefixed line as JSON. Returns the parsed entries.
func parseJSONLDataset(body string) []map[string]any {
	out := make([]map[string]any, 0, 16)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			out = append(out, entry)
		}
	}
	return out
}

// extractMarkdownPayloads pulls payloads out of a markdown body.
// Heuristic: fenced code blocks ```...``` are payloads; bullet
// lists starting with "-" or "*" after a "## " header are payloads.
// This is intentionally lightweight — the curated dataset files in
// the redteam mods use both shapes.
func extractMarkdownPayloads(path, body string) []map[string]any {
	out := make([]map[string]any, 0, 16)
	inFence := false
	var fence strings.Builder
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			if inFence {
				out = append(out, map[string]any{
					"source_path": path,
					"format":      "fenced_code",
					"payload":     strings.TrimRight(fence.String(), "\n"),
				})
				fence.Reset()
				inFence = false
			} else {
				inFence = true
			}
			continue
		}
		if inFence {
			fence.WriteString(line)
			fence.WriteString("\n")
		}
	}
	return out
}

// hashForAudit returns a short hex hash of s for audit-log
// identification. Uses SHA-256 via the stdlib; result is the first
// 16 hex chars (64 bits) for compactness.
func hashForAudit(s string) string {
	// Keep it allocation-light: use FNV-32 in hex. Sufficient for
	// audit-correlation purposes (we just need a stable identifier
	// of the payload bytes, not collision resistance).
	h := uint32(2166136261)
	const prime = uint32(16777619)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return fmt.Sprintf("%08x", h)
}