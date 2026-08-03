// Package detect — runtime harness introspection for embedder factory
// dispatch (PR-2.1, row 164 §2).
//
// At FactoryAuto() boot time, the embedder factory consults this
// detector to figure out which AI-coding harness is hosting
// dark-memory, so the factory can pick the right preferred embedder
// rung (e.g., claude-code → Voyage AI; opencode → OpenAI; ollama →
// local Ollama; else → bundled ONNX).
//
// # Detection strategy
//
// Cheap-first: env vars (no I/O), then config-file stat (one syscall
// each), then a localhost probe for Ollama (cheap TCP dial with a
// 500ms timeout). Anything not detected → HarnessUnknown, which the
// factory treats as "no harness preference; pick default rung".
//
// # Why not auto-discover for every harness
//
// The ladder is intentionally small (4 harnesses). Adding a 5th
// detection rule pays for itself only when that harness has a
// materially different preferred rung. Today, every harness we ship
// prefers OpenAI-compatible endpoints, so the differential is just
// claude-code → Voyage (Anthropic's recommended embedder partner).
//
// # Privacy posture
//
// Detection reads only env vars + paths + a single localhost probe.
// No network calls. No file content reads. The detector never logs
// the detected harness name; operators can read it from health_ping.
package detect

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HarnessKind is the canonical identifier for the host harness. Used
// in health_ping, the embedder factory ladder, and the consent prompt
// surface (embedder_setup_prompt includes Harness so the LLM can
// suggest the harness-native rung).
type HarnessKind string

const (
	// HarnessUnknown means no harness signature detected. Factory
	// falls through to the default rung (bundled ONNX).
	HarnessUnknown HarnessKind = "unknown"

	// HarnessClaudeCode — Claude Code CLI (Anthropic). Prefers
	// Voyage AI voyage-3.
	HarnessClaudeCode HarnessKind = "claude-code"

	// HarnessOpenCode — opencode CLI (the user's own). Prefers
	// OpenAI text-embedding-3-small (matches its default provider).
	HarnessOpenCode HarnessKind = "opencode"

	// HarnessCodex — OpenAI Codex CLI. Prefers OpenAI.
	HarnessCodex HarnessKind = "codex"

	// HarnessOllama — Local Ollama daemon (any client using it).
	// Prefers Ollama's local embedder endpoints.
	HarnessOllama HarnessKind = "ollama"
)

// Result is the detection outcome. ConfigPath is filled when the
// detector located a config file; the embedder factory may parse it
// later to extract the actual provider/API key (future work; PR-2.1
// only uses Kind).
type Result struct {
	// Kind is the detected harness.
	Kind HarnessKind

	// ConfigPath is the path to the harness config file when one
	// was found. Empty if detection used only env vars.
	ConfigPath string

	// Source is the detection signal that fired (env var name,
	// config path, or "probe:<addr>"). Useful for debugging when
	// operators see a wrong detection.
	Source string
}

// IsKnown reports whether r.Kind is one of the supported harnesses.
// Unknown and empty both map to "no harness preference".
func (r Result) IsKnown() bool {
	return r.Kind != HarnessUnknown && r.Kind != ""
}

// PreferredEmbedder returns the embedder kind this harness should
// use, per row 164 §2 ladder. Returns embedder.KindNone when no
// harness is detected.
func (r Result) PreferredEmbedder() string {
	switch r.Kind {
	case HarnessClaudeCode:
		return "voyage"
	case HarnessOpenCode, HarnessCodex:
		return "openai"
	case HarnessOllama:
		return "ollama"
	default:
		return "none"
	}
}

// Probe runs the full detection ladder. Cheap: ~1ms typical on
// local disk + no network unless the ollama localhost probe fires
// (which has a 500ms timeout).
//
// Always returns a Result; failure modes just leave Kind as
// HarnessUnknown + Source set to the failure reason.
func Probe() Result {
	// Order matches row 164 §2 ladder top-down (the harness detector
	// walks the ladder and picks the first match; the factory then
	// walks the embedder preference for that harness).
	if r := probeClaudeCode(); r.IsKnown() {
		return r
	}
	if r := probeOpenCode(); r.IsKnown() {
		return r
	}
	if r := probeCodex(); r.IsKnown() {
		return r
	}
	if r := probeOllama(); r.IsKnown() {
		return r
	}
	return Result{Kind: HarnessUnknown, Source: "no harness signal"}
}

// probeClaudeCode checks CLAUDE_CODE env vars + the canonical
// settings.json location. Env vars first because they don't require
// I/O and are the most reliable signal when the operator launched
// the harness from a script.
func probeClaudeCode() Result {
	for _, ev := range []string{"CLAUDE_CODE", "CLAUDE_CODE_ENTRYPOINT"} {
		if os.Getenv(ev) != "" {
			return Result{
				Kind:   HarnessClaudeCode,
				Source: "env:" + ev,
			}
		}
	}
	for _, p := range configPaths("claude") {
		if pathExists(p) {
			return Result{
				Kind:       HarnessClaudeCode,
				ConfigPath: p,
				Source:     "config:" + p,
			}
		}
	}
	return Result{Kind: HarnessUnknown}
}

// probeOpenCode checks OPENCODE_VERSION + opencode config dirs.
// OPENCODE_VERSION is set by the opencode launcher; we use it as
// the primary signal.
func probeOpenCode() Result {
	if os.Getenv("OPENCODE_VERSION") != "" || os.Getenv("OPENCODE_CONFIG") != "" {
		return Result{
			Kind:   HarnessOpenCode,
			Source: "env:OPENCODE_VERSION",
		}
	}
	for _, p := range configPaths("opencode") {
		if pathExists(p) {
			return Result{
				Kind:       HarnessOpenCode,
				ConfigPath: p,
				Source:     "config:" + p,
			}
		}
	}
	return Result{Kind: HarnessUnknown}
}

// probeCodex checks CODEX_HOME + Codex config locations.
func probeCodex() Result {
	if os.Getenv("CODEX_HOME") != "" || os.Getenv("OPENAI_API_KEY") != "" && isCodexMarkerPresent() {
		return Result{
			Kind:   HarnessCodex,
			Source: "env:CODEX_HOME",
		}
	}
	for _, p := range configPaths("codex") {
		if pathExists(p) {
			return Result{
				Kind:       HarnessCodex,
				ConfigPath: p,
				Source:     "config:" + p,
			}
		}
	}
	return Result{Kind: HarnessUnknown}
}

// isCodexMarkerPresent is a soft check for OpenAI Codex-specific
// env vars or markers. We use it as a tie-breaker because
// OPENAI_API_KEY alone is too generic (any OpenAI tool sets it).
func isCodexMarkerPresent() bool {
	for _, ev := range []string{"CODEX_RUNTIME", "CODEX_CONFIG", "CODEX_VERSION"} {
		if os.Getenv(ev) != "" {
			return true
		}
	}
	return false
}

// probeOllama checks OLLAMA_HOST env var + a localhost probe at the
// default Ollama port (11434). The probe is a single TCP dial with
// a 500ms timeout — bounded latency, no HTTP.
func probeOllama() Result {
	host := os.Getenv("OLLAMA_HOST")
	if host != "" {
		return Result{
			Kind:   HarnessOllama,
			Source: "env:OLLAMA_HOST",
		}
	}
	const defaultHost = "127.0.0.1:11434"
	if probeTCP(defaultHost, 500*time.Millisecond) {
		return Result{
			Kind:   HarnessOllama,
			Source: "probe:" + defaultHost,
		}
	}
	return Result{Kind: HarnessUnknown}
}

// probeTCP opens a single TCP connection to addr with the given
// timeout. Returns true on successful connect, false on any error
// (refused, timeout, unreachable).
func probeTCP(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// configPaths returns the canonical config-file locations for a
// harness name, in priority order. Used by the probe* functions.
//
// XDG-aware: $XDG_CONFIG_HOME takes precedence over $HOME/.config.
// Falls back to ~/.<harness> for tools that follow the legacy dotfile
// convention.
func configPaths(harness string) []string {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")

	var base string
	if xdg != "" {
		base = xdg
	} else if home != "" {
		base = filepath.Join(home, ".config")
	}

	switch harness {
	case "claude":
		var paths []string
		if base != "" {
			paths = append(paths, filepath.Join(base, "claude", "settings.json"))
			paths = append(paths, filepath.Join(base, "claude-code", "settings.json"))
		}
		if home != "" {
			paths = append(paths, filepath.Join(home, ".claude", "settings.json"))
			paths = append(paths, filepath.Join(home, ".claude.json"))
		}
		return paths
	case "opencode":
		var paths []string
		if base != "" {
			paths = append(paths, filepath.Join(base, "opencode", "config.toml"))
			paths = append(paths, filepath.Join(base, "opencode", "config.json"))
		}
		if home != "" {
			paths = append(paths, filepath.Join(home, ".opencode", "config.toml"))
			paths = append(paths, filepath.Join(home, ".config", "opencode", "config.toml"))
		}
		return paths
	case "codex":
		var paths []string
		if base != "" {
			paths = append(paths, filepath.Join(base, "codex", "config.toml"))
		}
		if home != "" {
			paths = append(paths, filepath.Join(home, ".codex", "config.toml"))
			paths = append(paths, filepath.Join(home, ".codex", "config.json"))
		}
		// CODEX_HOME is the canonical Codex config root.
		if ch := os.Getenv("CODEX_HOME"); ch != "" {
			paths = append(paths, filepath.Join(ch, "config.toml"))
		}
		return paths
	}
	return nil
}

// pathExists returns true if path exists and is readable.
func pathExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// FormatKind returns the canonical display name for a HarnessKind.
// Used in health_ping + the consent prompt.
func FormatKind(k HarnessKind) string {
	if k == "" {
		return string(HarnessUnknown)
	}
	return string(k)
}

// String returns a debug-friendly representation of the detection
// result, including the source. Used in health_ping.
func (r Result) String() string {
	if r.Source == "" {
		return string(r.Kind)
	}
	return string(r.Kind) + " (" + r.Source + ")"
}

// containsAny is a helper for env-var substring matches; reserved
// for future probes (e.g., claude-code MCP-installed env hints).
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
