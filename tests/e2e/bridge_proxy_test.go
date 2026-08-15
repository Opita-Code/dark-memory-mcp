// Package e2e — bridge_proxy_test.go: spec 1176 §4.10 end-to-end
// verification through the REAL bridge binary. The bridge is the
// transparent byte proxy opencode spawns: it forwards stdio bytes
// verbatim to the daemon socket. This test pre-spawns the daemon
// (child of the test process, so cleanup is deterministic), then
// spawns the bridge with --no-spawn, feeds it MCP JSON-RPC on stdin,
// and asserts the native MCP responses come back on stdout — proving
// the full production path (bridge → daemon → ServeStream) works.
package e2e

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/daemon"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

func bridgeBinaryPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Dir(filepath.Dir(wd))
	for _, name := range []string{"dark-mem-mcp-bridge.exe", "dark-mem-mcp-bridge"} {
		p := filepath.Join(root, "bin", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// TestBridgeBinary_ForwardsNativeMCPWire spawns the real daemon (child
// of the test process), then the real bridge in --no-spawn mode, and
// runs the MCP handshake through bridge stdio. Skips when the bridge
// binary is absent.
func TestBridgeBinary_ForwardsNativeMCPWire(t *testing.T) {
	bridgeBin := bridgeBinaryPath(t)
	if bridgeBin == "" {
		t.Skip("bridge binary not found (build with: go build -o bin/dark-mem-mcp-bridge.exe ./cmd/dark-mem-mcp-bridge)")
	}
	daemonBin := daemonBinaryPath(t)
	if daemonBin == "" {
		t.Skip("daemon binary not found")
	}

	socketPath, readyPath := e2eSocketPath(t)
	_ = os.Remove(readyPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Pre-spawn the daemon as a direct child of the test process.
	daemonCmd := exec.CommandContext(ctx, daemonBin,
		"--socket", socketPath,
		"--ready", readyPath,
		"--idle", "2m",
	)
	daemonCmd.Stdout = os.Stderr
	daemonCmd.Stderr = os.Stderr
	if err := daemonCmd.Start(); err != nil {
		t.Fatalf("spawn daemon: %v", err)
	}
	defer func() {
		if daemonCmd.Process != nil {
			_ = daemonCmd.Process.Kill()
		}
	}()
	if err := daemon.WaitForReady(readyPath, 10*time.Second); err != nil {
		t.Fatalf("daemon never ready: %v", err)
	}

	// 2. Spawn the bridge with --no-spawn (it must dial the daemon we
	// just started).
	bridgeCmd := exec.CommandContext(ctx, bridgeBin,
		"--socket", socketPath,
		"--no-spawn",
	)
	bridgeCmd.Stderr = os.Stderr
	stdin, err := bridgeCmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := bridgeCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := bridgeCmd.Start(); err != nil {
		t.Fatalf("spawn bridge: %v", err)
	}
	defer func() {
		if bridgeCmd.Process != nil {
			_ = exec.Command("taskkill", "/PID", fmt.Sprint(bridgeCmd.Process.Pid), "/T", "/F").Run()
		}
	}()

	cw := bufio.NewWriter(stdin)
	cr := bufio.NewReader(stdout)

	// 1. initialize through the bridge.
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2026-07-28",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e-bridge", "version": "1.0"},
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

	// 3. tools/list through the bridge.
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
	if len(toolsAny) < len(tools.CanonicalOrder()) {
		t.Fatalf("tools/list via bridge has %d tools, want >= %d", len(toolsAny), len(tools.CanonicalOrder()))
	}

	// 4. tools/call health_ping through the bridge.
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
		t.Fatalf("health_ping via bridge missing schema_version 26: %s", joined)
	}
}
