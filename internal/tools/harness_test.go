//go:build test

// Package tools harness tests — JSON-RPC helpers + smoke tests.
//
// harness_test.go defines the test-only callTool / toolsList
// helpers used by handlers_test.go. The helpers POST JSON-RPC 2.0
// requests to the harness's /mcp endpoint and return the parsed
// response.
//
// Build tag `//go:build test` keeps these helpers out of production
// binaries. The harness.go + harness_test.go pair is a pure test-
// time fixture.
package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// jsonRPCRequest is the wire shape we POST to /mcp. Mirrors the
// mcp-go mcp.JSONRPCRequest struct but only includes the fields
// the harness uses (jsonrpc + id + method + params).
type jsonRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// jsonRPCResponse is the parsed response. mcp-go returns either
// {"jsonrpc":"2.0","id":N,"result":{...}} (success) or
// {"jsonrpc":"2.0","id":N,"error":{...}} (error).
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// callTool POSTs a tools/call JSON-RPC request to the harness and
// returns the parsed response.
func callTool(t *testing.T, h *TestHarness, name string, args any) *jsonRPCResponse {
	t.Helper()
	return callRPC(t, h, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
}

// extractFirstTextContent pulls the first TextContent.text payload
// from a mcp-go CallToolResult envelope (the response.Result field
// from callTool). Returns the inner string. Use this when the test
// only needs to assert on the JSON content (e.g. string contains),
// not the full structure.
func extractFirstTextContent(result json.RawMessage) string {
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return ""
	}
	for _, c := range envelope.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// callToolUnwrapped calls a tool and unwraps the mcp-go
// CallToolResult envelope (content[0].text) into the inner
// ToolResponse JSON. Tests that want to assert on ToolResponse.Data
// fields should use this helper instead of decoding Result directly.
//
// Returns the inner ToolResponse JSON as json.RawMessage. t.Fatal
// if the envelope is malformed.
func callToolUnwrapped(t *testing.T, h *TestHarness, name string, args any) json.RawMessage {
	t.Helper()
	resp := callTool(t, h, name, args)
	if resp.Error != nil {
		t.Fatalf("%s JSON-RPC error: %+v", name, resp.Error)
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &envelope); err != nil {
		t.Fatalf("unmarshal CallToolResult: %v\nraw: %s", err, string(resp.Result))
	}
	if len(envelope.Content) == 0 {
		t.Fatalf("%s: empty content array", name)
	}
	// Find the first TextContent item.
	for _, c := range envelope.Content {
		if c.Type == "text" {
			return json.RawMessage(c.Text)
		}
	}
	t.Fatalf("%s: no text content in envelope (types: %v)", name, envelope.Content)
	return nil
}

// toolsList POSTs a tools/list JSON-RPC request to the harness.
func toolsList(t *testing.T, h *TestHarness) *jsonRPCResponse {
	t.Helper()
	return callRPC(t, h, "tools/list", nil)
}

// callRPC is the generic JSON-RPC POST helper.
func callRPC(t *testing.T, h *TestHarness, method string, params map[string]any) *jsonRPCResponse {
	t.Helper()
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := h.Client.Do(httpReq)
	if err != nil {
		t.Fatalf("POST %s: %v", h.URL, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, rawBody)
	}

	// mcp-go StreamableHTTPServer may return SSE-formatted responses
	// (text/event-stream) for tools/call, but plain JSON for
	// tools/list + initialize. Detect SSE and parse the first
	// data: event payload.
	bodyStr := string(rawBody)
	if strings.HasPrefix(strings.TrimSpace(bodyStr), "data: ") {
		bodyStr = strings.TrimPrefix(strings.TrimSpace(bodyStr), "data: ")
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal([]byte(bodyStr), &rpcResp); err != nil {
		t.Fatalf("parse JSON-RPC response: %v\nbody: %s", err, bodyStr)
	}
	return &rpcResp
}

// initializeSession sends the `initialize` JSON-RPC method to the
// harness. Required before tools/call per the MCP spec.
func initializeSession(t *testing.T, h *TestHarness) *jsonRPCResponse {
	t.Helper()
	return callRPC(t, h, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "harness-test-client",
			"version": "0.0.0",
		},
	})
}

// TestHarness_Smoke verifies NewTestServer returns a working
// harness: tools/list returns all canonical tools; initialize
// returns OK. The harness-level acceptance test; without it the
// other handlers tests would silently no-op.
func TestHarness_Smoke(t *testing.T) {
	h := NewTestServer(t)
	if h.URL == "" {
		t.Fatal("URL is empty")
	}
	if h.Registry == nil {
		t.Fatal("Registry is nil")
	}
	got := len(h.Registry.ListCanonical())
	want := len(CanonicalOrder())
	if got != want {
		t.Fatalf("ListCanonical count = %d; want %d", got, want)
	}

	toolsResp := toolsList(t, h)
	if toolsResp.Error != nil {
		t.Fatalf("tools/list error: %+v", toolsResp.Error)
	}
	var listResult struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(toolsResp.Result, &listResult); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if len(listResult.Tools) != want {
		t.Errorf("tools/list returned %d tools; want %d", len(listResult.Tools), want)
	}
	for _, tl := range listResult.Tools {
		if tl.Name == "" {
			t.Errorf("tool with empty name in tools/list")
		}
		if len(tl.InputSchema) == 0 {
			t.Errorf("tool %q has empty inputSchema", tl.Name)
		}
	}
}

// TestHarness_Initialize verifies the MCP initialize handshake
// succeeds.
func TestHarness_Initialize(t *testing.T) {
	h := NewTestServer(t)
	resp := initializeSession(t, h)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	if len(resp.Result) == 0 {
		t.Fatal("initialize returned empty Result")
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &initResult); err != nil {
		t.Fatalf("unmarshal initialize: %v\nraw: %s", err, string(resp.Result))
	}
	if initResult.ServerInfo.Name != "dark-mem-mcp-harness" {
		t.Errorf("ServerInfo.Name = %q; want dark-mem-mcp-harness", initResult.ServerInfo.Name)
	}
}

// newRawRequest posts a raw body (no JSON marshaling). Used by the
// malformed-JSON / empty-body tests in handlers_test.go.
func newRawRequest(url string, rawBody string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req, nil
}