package daemon

// Spec 1176 §4.10 — MCP-over-socket unit test.
//
// The daemon must serve the NATIVE MCP wire over its socket (the
// bridge is a transparent byte proxy; opencode sends standard MCP
// JSON-RPC). This test verifies the full handshake — initialize,
// notifications/initialized, tools/list, tools/call — through
// handleConn with Config.OnConn wired to an mcp-go StdioServer.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// newTestMCPHandler builds a minimal mcp-go MCPServer with one tool
// ("echo") and returns an OnConn closure that serves it over a conn.
// This mirrors what cmd/dark-mem-mcp-daemon wires in production.
func newTestMCPHandler(t *testing.T) func(ctx context.Context, conn net.Conn) {
	t.Helper()
	mcpSrv := mcpserver.NewMCPServer("test-server", "0.0.0-test")
	tool := mcplib.NewTool(
		"echo",
		mcplib.WithDescription("echo tool"),
		mcplib.WithString("text"),
	)
	mcpSrv.AddTool(tool, func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		text := ""
		switch v := req.Params.Arguments.(type) {
		case map[string]any:
			text, _ = v["text"].(string)
		}
		return mcplib.NewToolResultText(fmt.Sprintf("echo:%s", text)), nil
	})
	return func(ctx context.Context, conn net.Conn) {
		ss := mcpserver.NewStdioServer(mcpSrv)
		_ = ss.Listen(ctx, conn, conn)
	}
}

// writeLine sends one newline-terminated JSON-RPC frame.
func writeLine(t *testing.T, w *bufio.Writer, payload any) {
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

// readLine reads one newline-terminated response frame with a
// deadline, returning the raw JSON string.
func readLine(t *testing.T, r *bufio.Reader) string {
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
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for response")
		return ""
	}
}

// idsEqual compares a JSON-RPC id (float64 from JSON decode, or the
// typed int/string we passed) ignoring numeric type differences.
func idsEqual(got, want any) bool {
	gf, gok := got.(float64)
	wf, wok := want.(float64)
	if gok && wok {
		return gf == wf
	}
	gi, giok := got.(int)
	wi, wiok := want.(int)
	if giok && wiok {
		return gi == wi
	}
	if giok && wok {
		return float64(gi) == wf
	}
	if gok && wiok {
		return gf == float64(wi)
	}
	return got == want
}

// expectResponse asserts the JSON-RPC response has the expected id
// and no error field, and returns the result object.
func expectResponse(t *testing.T, line string, wantID any) map[string]any {
	t.Helper()
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Result  any    `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	if !idsEqual(resp.ID, wantID) {
		t.Fatalf("response id = %v, want %v (line=%s)", resp.ID, wantID, line)
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

// TestHandleConn_ServesNativeMCPWire runs the full MCP handshake over
// a net.Pipe through handleConn + OnConn. This is the exact wire shape
// opencode emits on the bridge's stdio.
func TestHandleConn_ServesNativeMCPWire(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	d := &Daemon{
		cfg: Config{
			OnConn: newTestMCPHandler(t),
		},
		startedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.handleConn(ctx, server)

	// Client side: standard MCP JSON-RPC (2026-07-28 stateless shape,
	// but the classic initialize handshake still works via mcp-go).
	cw := bufio.NewWriter(client)
	cr := bufio.NewReader(client)

	// 1. initialize.
	writeLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2026-07-28",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test-harness", "version": "1.0"},
		},
	})
	initResp := expectResponse(t, readLine(t, cr), 1)
	if pv, ok := initResp["protocolVersion"].(string); !ok || pv == "" {
		t.Fatalf("initialize result missing protocolVersion: %v", initResp)
	}

	// 2. notifications/initialized (no response expected — but the
	// reader must not block on it; mcp-go doesn't reply to notif).
	writeLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	// 3. tools/list.
	writeLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	listResp := expectResponse(t, readLine(t, cr), 2)
	toolsAny, ok := listResp["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result missing tools array: %v", listResp)
	}
	if len(toolsAny) != 1 {
		t.Fatalf("tools/list len = %d, want 1: %v", len(toolsAny), listResp)
	}

	// 4. tools/call echo.
	writeLine(t, cw, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"text": "hola"},
		},
	})
	callResp := expectResponse(t, readLine(t, cr), 3)
	content, ok := callResp["content"].([]any)
	if !ok {
		t.Fatalf("tools/call result missing content: %v", callResp)
	}
	joined := fmt.Sprintf("%v", content)
	if !strings.Contains(joined, "echo:hola") {
		t.Fatalf("tools/call content does not contain echo:hola: %v", callResp)
	}

	// Close the client side -> server Listen returns -> handleConn
	// deferred Close cleans up.
	_ = client.Close()
}

// TestHandleConn_OnConnNil_RunsFrameLoop ensures the legacy Frame path
// still works when OnConn is nil (compat fallback).
func TestHandleConn_OnConnNil_RunsFrameLoop(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	d := &Daemon{
		cfg: Config{
			OnRequest: func(ctx context.Context, id, method string, params []byte) (any, error) {
				if method == "ping" {
					return map[string]any{"ok": true}, nil
				}
				return nil, fmt.Errorf("unsupported method: %s", method)
			},
		},
		startedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.handleConn(ctx, server)

	cw := bufio.NewWriter(client)
	cr := bufio.NewReader(client)

	req, err := NewRequestFrame("req-1", "ping", nil)
	if err != nil {
		t.Fatalf("NewRequestFrame: %v", err)
	}
	b, _ := MarshalFrame(req)
	if _, err := cw.Write(append(b, '\n')); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if err := cw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	line := readLine(t, cr)
	var resp struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Result struct {
			OK bool `json:"ok"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal frame response %q: %v", line, err)
	}
	if resp.ID != "req-1" || resp.Type != "rpc" || !resp.Result.OK {
		t.Fatalf("frame response mismatch: %s", line)
	}
}
