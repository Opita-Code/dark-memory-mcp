// Package e2e — daemon_binary_test.go: spec 1176 §4.10 verification
// against the REAL compiled daemon binary. Spawns
// bin/dark-mem-mcp-daemon.exe (skip if absent), dials the named pipe
// / unix socket, and runs the full MCP handshake (initialize →
// tools/list → tools/call health_ping). This proves the shipped
// binary — not just the in-process Daemon — serves the native MCP
// wire over its socket.
package e2e

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/daemon"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// daemonBinaryPath locates the compiled daemon binary relative to the
// repo root (repoRoot is ../.. from tests/e2e).
func daemonBinaryPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// wd is .../tests/e2e; repo root is two levels up.
	root := filepath.Dir(filepath.Dir(wd))
	for _, name := range []string{"dark-mem-mcp-daemon.exe", "dark-mem-mcp-daemon"} {
		p := filepath.Join(root, "bin", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// TestDaemonBinary_ServesNativeMCPWire spawns the real daemon binary
// and verifies the MCP-over-socket path (spec 1176 §4.10). Skips when
// the binary has not been built (CI without bin/).
func TestDaemonBinary_ServesNativeMCPWire(t *testing.T) {
	daemonBin := daemonBinaryPath(t)
	if daemonBin == "" {
		t.Skip("daemon binary not found (build with: go build -o bin/dark-mem-mcp-daemon.exe ./cmd/dark-mem-mcp-daemon)")
	}

	socketPath, readyPath := e2eSocketPath(t)
	_ = os.Remove(readyPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, daemonBin,
		"--socket", socketPath,
		"--ready", readyPath,
		"--idle", "2m",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn daemon: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	if err := daemon.WaitForReady(readyPath, 10*time.Second); err != nil {
		t.Fatalf("daemon never ready: %v", err)
	}

	conn, err := daemon.DialSocket(socketPath, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	cw := bufio.NewWriter(conn)
	cr := bufio.NewReader(conn)

	// 1. initialize.
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2026-07-28",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e-binary", "version": "1.0"},
		},
	})
	initResp := e2eExpectResult(t, e2eReadLine(t, cr), 1)
	if pv, ok := initResp["protocolVersion"].(string); !ok || pv == "" {
		t.Fatalf("initialize missing protocolVersion: %v", initResp)
	}

	// 2. notifications/initialized.
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	// 3. tools/list — full canonical surface present.
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	listResp := e2eExpectResult(t, e2eReadLine(t, cr), 2)
	toolsAny, ok := listResp["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list missing tools: %v", listResp)
	}
	wireNames := make(map[string]bool, len(toolsAny))
	for _, tt := range toolsAny {
		if m, ok := tt.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				wireNames[name] = true
			}
		}
	}
	if len(wireNames) < len(tools.CanonicalOrder()) {
		t.Fatalf("tools/list has %d distinct tools, want >= %d", len(wireNames), len(tools.CanonicalOrder()))
	}

	// 4. tools/call health_ping — schema v26.
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "dark_memory_health_ping",
			"arguments": map[string]any{},
		},
	})
	callResp := e2eExpectResult(t, e2eReadLine(t, cr), 3)
	content, ok := callResp["content"].([]any)
	if !ok {
		t.Fatalf("tools/call missing content: %v", callResp)
	}
	joined := ""
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if txt, ok := m["text"].(string); ok {
				joined += txt
			}
		}
	}
	if !strings.Contains(joined, `"schema_version":26`) {
		t.Fatalf("health_ping missing schema_version 26: %s", joined)
	}
}
