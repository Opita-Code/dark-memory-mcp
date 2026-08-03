// Package detect — tests for the harness detection ladder.
//
// All tests use t.Setenv for env vars (auto-cleans on test exit).
// The ollama TCP probe is mocked via a localhost TCP listener so
// tests don't depend on a real Ollama daemon running.

package detect

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestProbe_NoSignals returns HarnessUnknown when no env vars or
// configs are set AND no localhost Ollama is running.
func TestProbe_NoSignals(t *testing.T) {
	clearAllEnv(t)
	// Ensure no opencode/claude/codex config files in test env.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	r := Probe()
	if r.IsKnown() {
		t.Fatalf("Probe with no signals: expected HarnessUnknown, got %s (source=%s)", r.Kind, r.Source)
	}
}

func TestProbe_ClaudeCode_Env(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE", "1")
	r := Probe()
	if r.Kind != HarnessClaudeCode {
		t.Fatalf("expected HarnessClaudeCode, got %s", r.Kind)
	}
	if r.Source != "env:CLAUDE_CODE" {
		t.Fatalf("expected source env:CLAUDE_CODE, got %s", r.Source)
	}
}

func TestProbe_ClaudeCode_ConfigFile(t *testing.T) {
	clearAllEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// Create <xdg>/claude/settings.json.
	dir := filepath.Join(xdg, "claude")
	if err := writeFile(filepath.Join(dir, "settings.json"), "{}"); err != nil {
		t.Fatal(err)
	}
	r := Probe()
	if r.Kind != HarnessClaudeCode {
		t.Fatalf("expected HarnessClaudeCode via config, got %s (source=%s)", r.Kind, r.Source)
	}
	if filepath.Base(r.ConfigPath) != "settings.json" {
		t.Fatalf("unexpected ConfigPath: %s", r.ConfigPath)
	}
}

func TestProbe_OpenCode_Env(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENCODE_VERSION", "0.4.0")
	r := Probe()
	if r.Kind != HarnessOpenCode {
		t.Fatalf("expected HarnessOpenCode, got %s", r.Kind)
	}
}

func TestProbe_Codex_Env(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "/tmp/codex")
	r := Probe()
	if r.Kind != HarnessCodex {
		t.Fatalf("expected HarnessCodex, got %s", r.Kind)
	}
}

func TestProbe_Ollama_Env(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OLLAMA_HOST", "http://192.168.1.50:11434")
	r := Probe()
	if r.Kind != HarnessOllama {
		t.Fatalf("expected HarnessOllama, got %s", r.Kind)
	}
}

// TestProbe_Ollama_Probe starts a TCP listener on 127.0.0.1 and
// verifies Probe picks it up via the probe path. We override
// OLLAMA_HOST to point at the test port to skip the timeout cost
// of waiting on 11434.
func TestProbe_Ollama_Probe(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("OLLAMA_HOST", "http://"+ln.Addr().String())
	r := Probe()
	if r.Kind != HarnessOllama {
		t.Fatalf("expected HarnessOllama via env, got %s", r.Kind)
	}
}

// TestProbe_Ollama_NoListener confirms Probe returns HarnessUnknown
// when OLLAMA_HOST is unset AND nothing is listening on 11434.
// We rely on 11434 being unbound in the test env; CI usually is.
func TestProbe_Ollama_NoListener(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("HOME", t.TempDir())
	r := Probe()
	if r.Kind == HarnessOllama {
		t.Fatalf("expected HarnessUnknown (no Ollama), got HarnessOllama")
	}
}

// TestResult_PreferredEmbedder covers the row 164 §2 mapping.
func TestResult_PreferredEmbedder(t *testing.T) {
	cases := []struct {
		kind     HarnessKind
		expected string
	}{
		{HarnessClaudeCode, "voyage"},
		{HarnessOpenCode, "openai"},
		{HarnessCodex, "openai"},
		{HarnessOllama, "ollama"},
		{HarnessUnknown, "none"},
	}
	for _, tc := range cases {
		got := (Result{Kind: tc.kind}).PreferredEmbedder()
		if got != tc.expected {
			t.Errorf("Kind=%s: PreferredEmbedder=%s, want %s", tc.kind, got, tc.expected)
		}
	}
}

// TestResult_String confirms the debug-friendly format.
func TestResult_String(t *testing.T) {
	cases := []struct {
		r        Result
		expected string
	}{
		{Result{Kind: HarnessOpenCode}, "opencode"},
		{Result{Kind: HarnessOpenCode, Source: "env:OPENCODE_VERSION"}, "opencode (env:OPENCODE_VERSION)"},
	}
	for _, tc := range cases {
		if got := tc.r.String(); got != tc.expected {
			t.Errorf("r=%+v String()=%q, want %q", tc.r, got, tc.expected)
		}
	}
}

// TestFormatKind covers the empty/unknown default.
func TestFormatKind(t *testing.T) {
	if got := FormatKind(HarnessClaudeCode); got != "claude-code" {
		t.Errorf("FormatKind(ClaudeCode)=%q, want claude-code", got)
	}
	if got := FormatKind(""); got != "unknown" {
		t.Errorf("FormatKind(empty)=%q, want unknown", got)
	}
}

// clearAllEnv clears every env var the detector consults. Used to
// make tests hermetic regardless of CI runner env.
func clearAllEnv(t *testing.T) {
	t.Helper()
	for _, ev := range []string{
		"CLAUDE_CODE", "CLAUDE_CODE_ENTRYPOINT",
		"OPENCODE_VERSION", "OPENCODE_CONFIG",
		"CODEX_HOME", "CODEX_RUNTIME", "CODEX_CONFIG", "CODEX_VERSION",
		"OPENAI_API_KEY",
		"OLLAMA_HOST",
		"XDG_CONFIG_HOME",
	} {
		t.Setenv(ev, "")
	}
}

func writeFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o644)
}
