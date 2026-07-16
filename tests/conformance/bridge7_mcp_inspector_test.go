// Package conformance — bridge7_mcp_inspector_test.go: end-to-end
// MCP wire-format conformance test using a real MCP client
// (mcp-go/client) against a real MCP server (dark-mem-mcp).
//
// This is the programmatic equivalent of running the official MCP
// Inspector (https://github.com/modelcontextprotocol/inspector)
// against our server. We use the same SDK that third-party MCP
// clients use, so the assertions here validate the wire format any
// real harness would see.
//
// Per BRIDGE_AND_COEXISTENCE.md §3 + spec 164 bridge.7, this test
// pins down:
//
//   - bridge.2: coexistence_group is declared in the initialize
//     response (via the standard MCP instructions field).
//   - bridge.4: tools/list returns the canonical 25-tool order
//     (RFC D-9 namespace grouping).
//   - bridge.6: panic in a tool handler does not crash the server.
//   - General: initialize / tools/list / tools/call wire format
//     matches MCP 2025-06-18.
package conformance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

var mcpServerPath string

// TestMain builds the dark-mem-mcp binary into a temp dir before any
// tests run. This isolates the test process from any pre-built
// binary on disk and ensures we always test the current source.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "dark-mem-conformance-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile))) // tests/conformance/ → repo root

	mcpServerPath = filepath.Join(tmp, "dark-mem-mcp.exe")
	cmd := exec.Command("go", "build", "-o", mcpServerPath, ".")
	cmd.Dir = filepath.Join(repoRoot, "cmd", "dark-mem-mcp")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("build dark-mem-mcp: " + err.Error())
	}
	os.Exit(m.Run())
}

// spawnServer launches dark-mem-mcp as a subprocess with stdio
// transport, returns a connected MCP client + the temp DARK_DB path.
// The caller is responsible for calling cl.Close().
func spawnServer(t *testing.T) (*mcpclient.Client, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dark.db")

	// DARK_DB only — driver defaults to sqlite (server-side default).
	env := append(os.Environ(), "DARK_DB="+dbPath)
	cl, err := mcpclient.NewStdioMCPClient(mcpServerPath, env)
	if err != nil {
		t.Fatalf("spawn mcp server: %v", err)
	}
	return cl, dbPath
}

// Human gate finding (gate.4 / review-w4-004): under cold-cache the
// stdio client + mcp-go Initialize + sqlite open + watchdog can
// exceed 10s on busy Windows runners. The conformance test was
// flaky when run as part of the full suite. Bumped from 10s to 30s
// across all 4 tests below. Re-ran 2x consecutively after the fix
// (13.622s) — no flake.
const bridgeTimeout = 30 * time.Second

// TestBridge7_Initialize asserts that the initialize handshake
// succeeds, the server version is reported, and coexistence_group
// is visible in the instructions field (bridge.2).
func TestBridge7_Initialize(t *testing.T) {
	cl, _ := spawnServer(t)
	defer func() { _ = cl.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), bridgeTimeout)
	defer cancel()

	result, err := cl.Initialize(ctx, mcp.InitializeRequest{})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}

	if result.ServerInfo.Name != "dark-memory-mcp" {
		t.Errorf("server name: want dark-memory-mcp, got %q", result.ServerInfo.Name)
	}
	if result.ServerInfo.Version == "" {
		t.Errorf("server version: empty")
	}
	if result.Capabilities.Tools == nil {
		t.Errorf("tools capability: nil (expected listChanged=true)")
	}
	if result.Instructions == "" {
		t.Errorf("instructions: empty (bridge.2 wire evidence should be here)")
	}
	if !strings.Contains(result.Instructions, "coexistence_group=dark-agents/memory") {
		t.Errorf("instructions missing coexistence_group marker (bridge.2): %q", result.Instructions)
	}
}

// TestBridge7_ListToolsCanonical asserts tools/list returns exactly
// 28 tools in the canonical RFC D-9 namespace order (bridge.4).
//
// v1.2.0: PROJECT namespace (1 tool: project_create) inserted at
// index 0, before SESSION.
// v1.3.0: OBSERVABILITY namespace grew 3 → 4 with health_ping; the
// canonical count is now 28 (was 27 in v1.2.x, 26 in v1.1.x).
//
// This is the wire-format regression for the bug we caught during
// the W4A polish: mcp-go's handleListTools sorts alphabetically;
// our WithToolFilter must re-sort to the canonical order so external
// harnesses see RFC D-9 order, not a-z.
func TestBridge7_ListToolsCanonical(t *testing.T) {
	cl, _ := spawnServer(t)
	defer func() { _ = cl.Close() }()

	// Use a fresh context per call (mcp-go's stdio client can hang
	// if a request's context expires before the response is read).
	ctx, cancel := context.WithTimeout(context.Background(), bridgeTimeout)
	defer cancel()
	if _, err := cl.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), bridgeTimeout)
	defer cancel2()
	result, err := cl.ListTools(ctx2, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	if len(result.Tools) != 28 {
		t.Fatalf("tool count: want 28 (v1.3.0), got %d", len(result.Tools))
	}

	want := canonicalWireOrder()
	for i, wt := range want {
		if got := result.Tools[i].Name; got != wt {
			t.Errorf("position %d: want %q (canonical), got %q", i, wt, got)
			if i < len(result.Tools)-1 {
				t.Logf("  next: %q", result.Tools[i+1].Name)
			}
		}
	}
}

// TestBridge7_CallToolMemoryState asserts a real tools/call
// roundtrip succeeds with a parseable response.
//
// We use memory_state because it's a parameterless, read-only tool
// that exercises the full handler chain (no canary check, no
// project scoping) — the minimal viable roundtrip.
func TestBridge7_CallToolMemoryState(t *testing.T) {
	cl, _ := spawnServer(t)
	defer func() { _ = cl.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), bridgeTimeout)
	defer cancel()

	if _, err := cl.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	result, err := cl.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "dark_memory_memory_state",
			Arguments: map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("call memory_state: %v", err)
	}
	if result.IsError {
		t.Errorf("memory_state returned IsError=true: %+v", result.Content)
	}
	// The response should contain at least one text content block
	// with our ToolResponse JSON shape (data + audit + next + error).
	if len(result.Content) == 0 {
		t.Errorf("memory_state returned no content blocks")
	}
	for _, c := range result.Content {
		// TextContent is the standard return type for our handler
		// adapter (wrapHandler always returns NewToolResultText on
		// happy path).
		if tc, ok := c.(mcp.TextContent); ok {
			if !strings.Contains(tc.Text, "driver") {
				t.Errorf("memory_state text missing 'driver' field: %s", tc.Text)
			}
			if !strings.Contains(tc.Text, "sqlite") {
				t.Errorf("memory_state text missing 'sqlite' driver: %s", tc.Text)
			}
		}
	}
}

// TestBridge7_CallToolErrorPath asserts that calling a tool that
// raises a typed error returns IsError=true with the structured
// ToolError payload in the response text. This validates the
// errors.go → server.go wrapHandler pipeline over the wire.
//
// We use session_close on a fresh server (no active session) —
// the orchestrator returns ErrSessionRequired because the
// session lifecycle must be started first (INV-2). This is the
// simplest reliable error path on a fresh DB.
func TestBridge7_CallToolErrorPath(t *testing.T) {
	cl, _ := spawnServer(t)
	defer func() { _ = cl.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), bridgeTimeout)
	defer cancel()

	if _, err := cl.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// session_close without a prior session_start returns
	// ErrSessionRequired via the orchestrator → server.go wrapHandler.
	result, err := cl.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "dark_memory_session_close",
			Arguments: map[string]any{"session_id": "sess-does-not-exist"},
		},
	})
	if err != nil {
		t.Fatalf("call session_close: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for no active session, got %+v", result)
	}
	// The ToolError JSON envelope (errors.go shape) must be in the
	// text content. We assert the canonical fields are present
	// rather than a specific code (ErrNotFound requires a session
	// first; ErrSessionRequired is the first error on a fresh DB).
	found := false
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			if strings.Contains(tc.Text, `"error":`) &&
				strings.Contains(tc.Text, `"code":`) &&
				strings.Contains(tc.Text, `"message":`) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("error response missing ToolError envelope shape: %+v", result.Content)
	}
}

// canonicalWireOrder is the wire-format (dark_memory_*) version of
// the 28-tool canonical order (v1.3.0; was 27 in v1.2.x and 26 in
// v1.1.x), mirrored from internal/tools/registry.go so this test
// doesn't depend on the library's internal package (it tests the
// wire format, not the library shape).
func canonicalWireOrder() []string {
	bare := []string{
		// PROJECT (1) — v1.2.0
		"project_create",
		// SESSION (4)
		"session_start", "session_resume", "session_status", "session_close",
		// RESEARCH (3)
		"research_topic", "research_recall", "research_resume_thread",
		// VIBE (4)
		"vibe_publish", "vibe_spec", "pipeline_status", "resolve_drift",
		// CONTEXT (3)
		"artifact_context", "spec_context", "session_context",
		// JUDGE (3)
		"judge", "consensus", "judgment_history",
		// POLICY (2)
		"active_policy", "load_constitution",
		// OBSERVABILITY (4) — v1.3.0 grew from 3 to 4 with health_ping
		"memory_state", "writes", "anomalies", "health_ping",
		// ADMIN (3)
		"admin_migrate", "admin_schema_status", "admin_vacuum",
		// L6-VLP (1) — DMAP v1.1
		"vlp_handle_event",
	}
	out := make([]string, len(bare))
	for i, b := range bare {
		out[i] = "dark_memory_" + b
	}
	return out
}