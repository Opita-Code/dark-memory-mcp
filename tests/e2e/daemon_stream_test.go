// Package e2e — daemon_stream_test.go: spec 1176 §4.10 integration
// test. Verifies the daemon serves the NATIVE MCP wire over its
// socket (MCP-over-socket) using the real Server surface: initialize
// handshake, tools/list (canonical order), and tools/call
// dark_memory_health_ping over a real dialed connection (named pipe
// on Windows, unix socket elsewhere).
//
// This is the wire shape opencode sees through the transparent bridge
// proxy: {"jsonrpc":"2.0",...} frames, not the legacy Frame protocol.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/daemon"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// e2eSocketPath returns a unique socket path for this test: a named
// pipe on Windows, a unix socket in a temp dir elsewhere.
func e2eSocketPath(t *testing.T) (socketPath, readyPath string) {
	t.Helper()
	base := fmt.Sprintf("dark-mem-e2e-%d", time.Now().UnixNano())
	if isWindowsTest() {
		return `\\.\pipe\` + base, filepath.Join(t.TempDir(), "daemon.ready")
	}
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	return sock, filepath.Join(t.TempDir(), "daemon.ready")
}

func isWindowsTest() bool {
	return strings.Contains(strings.ToLower(socketPathSeparator()), `\`) || socketPathSeparator() == `\`
}

func socketPathSeparator() string {
	return string(filepath.Separator)
}

// e2eWriteLine sends one newline-terminated JSON-RPC frame.
func e2eWriteLine(t *testing.T, w *bufio.Writer, payload any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// e2eReadLine reads one response frame with a deadline.
func e2eReadLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read response: %v", res.err)
		}
		return strings.TrimSpace(res.line)
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for response")
		return ""
	}
}

// e2eExpectResult asserts a JSON-RPC response with matching id and no
// error, returns the result object.
func e2eExpectResult(t *testing.T, line string, wantID any) map[string]any {
	t.Helper()
	var resp struct {
		ID     any `json:"id"`
		Result any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	if resp.Error != nil {
		t.Fatalf("response error: %d %s (line=%s)", resp.Error.Code, resp.Error.Message, line)
	}
	if resp.Result == nil {
		t.Fatalf("response has no result (line=%s)", line)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %T (line=%s)", resp.Result, line)
	}
	return m
}

// TestDaemon_ServesNativeMCPWire over a real socket runs the full MCP
// handshake against the real Server (53 tools registered) through the
// daemon's MCP-over-socket path (spec 1176 §4.10).
func TestDaemon_ServesNativeMCPWire(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	// Register the Registry tools into the mcp-go MCPServer (the
	// stdio path the daemon serves). newTestServer only populates
	// the Registry; Server.RegisterAll wires the mcp-go surface.
	if err := ts.srv.RegisterAll(); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	socketPath, readyPath := e2eSocketPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := daemon.NewDaemon(daemon.Config{
		SocketPath:  socketPath,
		ReadyPath:   readyPath,
		IdleTimeout: 5 * time.Minute,
		Version:     "e2e-test",
		OnConn: func(cctx context.Context, conn net.Conn) {
			if err := ts.srv.ServeStream(cctx, conn); err != nil {
				t.Logf("serve stream: %v", err)
			}
		},
	})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	go func() {
		_ = d.Run(ctx)
	}()
	if err := daemon.WaitForReady(readyPath, 5*time.Second); err != nil {
		t.Fatalf("daemon not ready: %v", err)
	}
	defer cancel()

	// Dial like the bridge does.
	conn, err := daemon.DialSocket(socketPath, 5*time.Second)
	if err != nil {
		t.Fatalf("DialSocket: %v", err)
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
			"clientInfo":      map[string]any{"name": "e2e-harness", "version": "1.0"},
		},
	})
	initResp := e2eExpectResult(t, e2eReadLine(t, cr), 1)
	if pv, ok := initResp["protocolVersion"].(string); !ok || pv == "" {
		t.Fatalf("initialize missing protocolVersion: %v", initResp)
	}
	// The server must advertise the dark-agents coexistence group.
	if ins, ok := initResp["instructions"].(string); ok && !strings.Contains(ins, "coexistence_group=dark-agents/memory") {
		t.Errorf("initialize instructions missing coexistence_group: %v", initResp)
	}

	// 2. notifications/initialized (no reply expected).
	e2eWriteLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	// 3. tools/list — must be the FULL canonical surface (53) in
	// canonical order.
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
	// The full canonical surface must be present (extras, e.g.
	// armed-mode red-team tools, may also appear — the test asserts
	// the canonical subset, not an exact count).
	wireNames := make(map[string]bool, len(toolsAny))
	for _, tt := range toolsAny {
		if m, ok := tt.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				wireNames[name] = true
			}
		}
	}
	if len(wireNames) < len(tools.CanonicalOrder()) {
		t.Fatalf("tools/list has %d distinct tools, want at least %d canonical", len(wireNames), len(tools.CanonicalOrder()))
	}
	for _, bare := range tools.CanonicalOrder() {
		if !wireNames[tools.WireName(bare)] {
			t.Errorf("tools/list missing canonical tool %s", tools.WireName(bare))
		}
	}
	// First tool must be dark_memory_project_create (canonical order,
	// spec 164 bridge.4).
	firstTool, _ := toolsAny[0].(map[string]any)
	if name, _ := firstTool["name"].(string); name != "dark_memory_project_create" {
		t.Errorf("first tool = %q, want dark_memory_project_create", name)
	}

	// 4. tools/call dark_memory_health_ping.
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
	// The health_ping ToolResponse JSON is embedded in the text
	// content. Check it carries schema_version 26.
	joined := ""
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if txt, ok := m["text"].(string); ok {
				joined += txt
			}
		}
	}
	if !strings.Contains(joined, `"schema_version":26`) {
		t.Fatalf("health_ping response missing schema_version 26: %s", joined)
	}
	if !strings.Contains(joined, `"live":true`) {
		t.Fatalf("health_ping response missing db live true: %s", joined)
	}
	if !strings.Contains(joined, `"canary_present":true`) {
		t.Fatalf("health_ping response missing canary_present true: %s", joined)
	}
}

var _ = server.New
