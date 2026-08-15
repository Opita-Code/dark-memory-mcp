// Package e2e — dispatcher_test.go: spec 1176 §4.10 final verification.
// Spawns the REAL dark-mem-mcp dispatcher binary in bridge mode (the
// exact process opencode launches). The dispatcher exec's into the
// bridge, which spawns the daemon on a custom socket. Feeding MCP
// JSON-RPC on stdin and reading the native MCP responses on stdout
// proves the whole production path end-to-end:
//
//	opencode → dark-mem-mcp.exe (dispatcher) → dark-mem-mcp-bridge.exe
//	  → dark-mem-mcp-daemon.exe (ServeStream MCP-over-socket)
//	  → response back to stdout
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

	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

func dispatcherBinaryPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Dir(filepath.Dir(wd))
	for _, name := range []string{"dark-mem-mcp.exe", "dark-mem-mcp"} {
		p := filepath.Join(root, "bin", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// TestDispatcherBinary_BridgeMode runs the full opencode-spawn path:
// the dispatcher decides bridge mode (bridge binary present, no
// DARK_MEM_BRIDGE=0), exec's the bridge, the bridge spawns the
// daemon, and the native MCP handshake works over stdio.
func TestDispatcherBinary_BridgeMode(t *testing.T) {
	dispatcherBin := dispatcherBinaryPath(t)
	if dispatcherBin == "" {
		t.Skip("dispatcher binary not found")
	}
	if bridgeBinaryPath(t) == "" {
		t.Skip("bridge binary not found alongside dispatcher")
	}

	socketPath, readyPath := e2eSocketPath(t)
	_ = os.Remove(readyPath)
	// The bridge spawns the daemon with daemon.DefaultReadyPath()
	// unless the env override is set; force it so WaitForReady in the
	// bridge waits on OUR ready file, not a stale default one.
	t.Setenv("DARK_MEM_DAEMON_READY", readyPath)
	// Explicitly leave DARK_MEM_BRIDGE unset → dispatcher must choose
	// bridge mode because the bridge binary sits alongside it.
	_ = os.Unsetenv("DARK_MEM_BRIDGE")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, dispatcherBin, "--socket", socketPath)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn dispatcher: %v", err)
	}
	// Kill the whole tree: dispatcher → bridge → daemon.
	defer func() {
		if cmd.Process != nil {
			_ = exec.Command("taskkill", "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F").Run()
		}
	}()

	cw := bufio.NewWriter(stdin)
	cr := bufio.NewReader(stdout)

	// 1. initialize through the dispatcher → bridge → daemon.
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2026-07-28",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e-dispatcher", "version": "1.0"},
		},
	})
	initResp := e2eExpectResult(t, e2eReadLine(t, cr), 1)
	if pv, ok := initResp["protocolVersion"].(string); !ok || pv == "" {
		t.Fatalf("initialize missing protocolVersion: %v", initResp)
	}
	if srv, ok := initResp["serverInfo"].(map[string]any); ok {
		if name, _ := srv["name"].(string); name == "" {
			t.Errorf("serverInfo.name empty")
		}
	}

	// 2. notifications/initialized.
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	// 3. tools/list.
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
		t.Fatalf("tools/list via dispatcher has %d tools, want >= %d", len(toolsAny), len(tools.CanonicalOrder()))
	}

	// 4. tools/call health_ping.
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
		t.Fatalf("health_ping via dispatcher missing schema_version 26: %s", joined)
	}
}
