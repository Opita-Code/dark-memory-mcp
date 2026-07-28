// Package agentbootstrap - instructions.go: cross-feature hints for
// the MCP server's `instructions` field.
//
// The MCP spec defines an `instructions` string on InitializeResult
// that harnesses MAY inject into the LLM's system prompt. Per the
// official MCP blog (2025-11-03):
//
//   - Keep it concise; do not write a manual.
//   - Cross-feature only; relationships between tools, not exhaustive
//     usage docs.
//   - Some harnesses (notably opencode, see issue #32856) discard
//     this field entirely. This is best-effort; the canonical bootstrap
//     path is the Resources + dark_memory_agent_bootstrap tool.
//
// The dark-memory-mcp server has TWO callers of BuildInstructions:
//
//  1. The internal/server package's BuildInstructions function, which
//     includes coexistence_group + version + spec 164 bridge.4
//     metadata in the SAME string. It appends CrossFeatureHints so the
//     final result is a single string with everything.
//
//  2. drift_judge tooling, which may want to inspect just the
//     cross-feature part for verdict purposes.
//
// Today only caller (1) is wired; the standalone accessor
// CrossFeatureHints exists so tests + future callers don't need to
// reconstruct the cross-feature text from scratch.
package agentbootstrap

import "fmt"

// CrossFeatureHints returns the concise cross-feature string that the
// MCP server embeds in the `instructions` field. Per MCP blog:
// cross-feature only, no manual.
//
// Content highlights:
//
//   - The resource URIs the harness can read directly.
//   - The 3 self-bootstrap tools introduced in v2.6.0.
//   - Cross-references to dark-research and dark-copilot companion MCPs.
//
// Operators who want to override the cross-feature text can do so by
// editing this function. The version is interpolated so the LLM
// always knows which MCP version emitted the hint.
func CrossFeatureHints(version string) string {
	return fmt.Sprintf(
		"dark-memory-mcp v%s self-bootstraps via resources (read dark-memory://docs/system-prompt.md) and via 3 tools: dark_memory_agent_bootstrap (returns the markdown content of any resource by surface+target), dark_memory_agent_recommend_companions (detects your harness and recommends missing companion MCPs), dark_memory_agent_detect_environment (returns what the MCP can infer about your runtime). Companion MCPs: @opitacode/dark-research-mcp (OSINT) and @opitacode/dark-copilot-mcp (real Chromium browser).",
		version,
	)
}
