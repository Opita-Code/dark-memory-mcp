//go:build test

// Package tools harness — httptest-based MCP harness for the 55
// canonical tools. Per SPEC-alpha-11-phase7.md §3.5 (Chunk 7.5).
//
// NewTestServer builds a real mcp-go StreamableHTTPServer wrapped in
// httptest.NewServer, with the canonical tool surface registered via
// tools.RegisterAll. Each test gets its own t.TempDir() SQLite DB,
// a fresh safety.Holder (canary installed), and a real *Orchestrator.
//
// The harness validates routing + JSON-RPC parsing for every
// canonical tool — it does NOT test deep business logic (that's
// covered by per-handler tests in transport/mcp/project_test.go and
// the v4alpha test suite). The goal is to raise coverage from 22.2%
// → ~75% by exercising the registration + serialization paths in
// internal/tools.
//
// Build tag: `//go:build test` ensures this file is compiled ONLY
// during `go test`. Production binaries never see this code path.
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// TestHarness is the result of NewTestServer: it bundles the running
// httptest server + the resolved handles tests may need (Store,
// Orchestrator, Registry). Tests use the HTTP client to drive
// JSON-RPC, never call the Store directly.
type TestHarness struct {
	// URL is the full mcp endpoint (e.g. http://127.0.0.1:54321/mcp).
	URL string

	// Client is the http.Client bound to the httptest server
	// (cookies + TLS config pre-resolved).
	Client *http.Client

	// Store is the open *sqlite.Store. Use it in tests that want
	// to assert on side effects (e.g. session row + audit row).
	Store store.Store

	// Orchestrator is the live *Orchestrator wired to the Store.
	Orchestrator *orchestration.Orchestrator

	// Registry is the populated tools.Registry (55 canonical tools).
	Registry *Registry

	// Server is the running *httptest.Server. The harness t.Cleanup
	// closes it automatically.
	Server *httptest.Server
}

// NewTestServer builds a complete MCP harness with all 55 canonical
// tools registered and an httptest.Server exposing /mcp.
//
// Build tag `//go:build test` ensures this file is compiled ONLY
// during `go test`. Production binaries never see this code path.
//
// The construction sequence (mirrors internal/server/lifecycle.go's
// Boot) is:
//
//  1. Open SQLite in t.TempDir() (mirrors Chunk 6.4 strategy).
//  2. Install canary on the Store (INV-3 enforcement).
//  3. Build the *Orchestrator with the Store + SafetyHolder.
//  4. Build tools.Registry + tools.RegisterAll.
//  5. Build the mcp-go MCPServer + register each canonical tool.
//  6. Wrap the MCPServer in a StreamableHTTPServer.
//  7. Wrap the StreamableHTTPServer in httptest.NewServer.
func NewTestServer(t *testing.T) *TestHarness {
	t.Helper()
	ctx := context.Background()

	// 1. SQLite in t.TempDir (mirrors Chunk 6.4 strategy).
	dsn := filepath.Join(t.TempDir(), "harness.db")
	st, err := runtime.Open(ctx, store.Config{
		Driver: store.DriverSQLite,
		DSN:    dsn,
	})
	if err != nil {
		t.Fatalf("runtime.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// 2. Install canary (INV-3). Mirror internal/server/lifecycle.go:342.
	safe := &safety.Holder{}
	safe.Set(safety.NewCanary())
	st.SetCanary(safe.Active().String())

	// 3. Build orchestrator.
	orch := orchestration.New(st, safe)

	// Build the store.SafetyHolder (function-field struct) used by
	// tools.RegisterAll → tools.RegisterRecall. Mirrors the
	// internal/server/lifecycle.go:148 wiring.
	safetyHolder := &store.SafetyHolder{
		SetCanary: func(s string) { st.SetCanary(s) },
		Active:    func() string { return safe.Active().String() },
		// ValidatePayload: no-op for harness — the real Store exposes
		// canary validation as a save-time check inside store.WithTx;
		// the SafetyHolder field is only consulted by policy frame
		// sources (recall), not by tool handlers. Mirrors the real
		// installCanary path which only sets Active.
		ValidatePayload: func(string) error { return nil },
	}

	// 4. Build registry + RegisterAll.
	reg := NewRegistry()
	if _, err := RegisterAll(reg, orch, st, safetyHolder); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// 5. Build mcp-go MCPServer + register each canonical tool.
	mcpSrv := server.NewMCPServer("dark-mem-mcp-harness", "test")
	for _, tool := range reg.ListCanonical() {
		mcpTool := mcplib.NewToolWithRawSchema(
			WireName(tool.Name),
			tool.Description,
			tool.InputSchema,
		)
		mcpSrv.AddTool(mcpTool, wrapHarnessHandler(tool))
	}

	// 6. Wrap in StreamableHTTPServer (default endpoint /mcp).
	// WithStateLess(true) bypasses session-id generation/validation
	// so tests don't need to thread the Mcp-Session-Id header from
	// initialize to tools/call. Mirrors production behavior for
	// per-call stateless transports.
	httpSrv := server.NewStreamableHTTPServer(mcpSrv,
		server.WithStateLess(true))

	// 7. Wrap in httptest.NewServer.
	ts := httptest.NewServer(httpSrv)
	t.Cleanup(ts.Close)

	return &TestHarness{
		URL:          ts.URL + "/mcp",
		Client:       ts.Client(),
		Store:        st,
		Orchestrator: orch,
		Registry:     reg,
		Server:       ts,
	}
}

// wrapHarnessHandler converts tools.HandlerFunc to mcp-go handler
// signature. Mirrors internal/server/server.go:275 wrapHandler but
// without the gate (gate is policy middleware — out of scope for
// harness-level routing tests).
//
// On error: the ToolError is serialized into the response body so
// tests can decode it via json.Unmarshal. The mcp-go IsError marker
// is set on the result so tests can branch on success vs failure.
func wrapHarnessHandler(t *Tool) func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		rawMap := req.GetArguments()
		var rawJSON json.RawMessage
		if rawMap != nil {
			b, err := json.Marshal(rawMap)
			if err != nil {
				return mcplib.NewToolResultError("internal: cannot re-marshal args: " + err.Error()), nil
			}
			rawJSON = b
		}
		resp, err := t.Handler(ctx, rawJSON)
		if err != nil {
			resp = &ToolResponse{
				Error: &ToolError{
					Code:    ErrInternal,
					Message: err.Error(),
				},
			}
		}
		if resp.Error != nil {
			body, _ := json.Marshal(resp)
			return mcplib.NewToolResultError(string(body)), nil
		}
		body, err := json.Marshal(resp)
		if err != nil {
			return mcplib.NewToolResultError("internal: marshal ToolResponse: " + err.Error()), nil
		}
		return mcplib.NewToolResultText(string(body)), nil
	}
}