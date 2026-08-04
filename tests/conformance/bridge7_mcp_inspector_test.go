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
//   - bridge.4: tools/list returns the canonical 29-tool order
//     (RFC D-9 namespace grouping, v2.0.0 after pivot added recall).
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
	// Determinism: the mcp-go stdio transport spawns the server with
	// append(os.Environ(), extraEnv...) — the TEST process's env is
	// always inherited, so env vars that add EXTRA tools beyond the
	// canonical surface must be cleared HERE (not in spawnServer, which
	// only appends). DARK_REDTEAM=armed registers the 3 redteam extras;
	// DARK_FEDERATION_PEER_DSN registers the federation lookup. Without
	// this, TestBridge7_ListToolsCanonical fails on dev machines that
	// run with those vars set (52 = 49 + 3 redteam instead of 49).
	if err := os.Setenv("DARK_REDTEAM", ""); err != nil {
		panic(err)
	}
	if err := os.Setenv("DARK_FEDERATION_PEER_DSN", ""); err != nil {
		panic(err)
	}

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
	// Determinism: strip env vars that add EXTRA tools beyond the
	// canonical surface (redteam +3 when DARK_REDTEAM=armed, federation
	// when DARK_FEDERATION_PEER_DSN is set). The canonical-count tests
	// assert the 49-tool baseline; leaving the operator's armed-mode
	// env in place would make them fail on dev machines that run with
	// DARK_REDTEAM=armed. See bridge7 TestBridge7_ListToolsCanonical.
	env := []string{}
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "DARK_REDTEAM="),
			strings.HasPrefix(kv, "DARK_FEDERATION_PEER_DSN="):
			continue // strip — would register extras
		}
		env = append(env, kv)
	}
	env = append(env, "DARK_DB="+dbPath)
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
//
// v2.9.1-alpha bump: 30s → 60s. The bundled ONNX adapter (PR-2.1)
// adds libonnxruntime dlopen + libsqlite3 cold-open during boot,
// pushing the typical wall-clock to ~35-50s on a busy Windows
// runner with no `onnxruntime.dll` system cache. End-of-day
// consolidation: doc this in CONTRIBUTING.md so operators know
// why `go test ./tests/conformance/...` needs patience.
const bridgeTimeout = 60 * time.Second

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
// 49 tools in the canonical RFC D-9 namespace order (bridge.4).
//
// v1.2.0: PROJECT namespace (1 tool: project_create) inserted at
// index 0, before SESSION.
// v1.3.0: OBSERVABILITY namespace grew 3 to 4 with health_ping; the
// canonical count is 29 (was 28 in v1.3.x, 27 in v1.2.x, 26 in v1.1.x).
// v2.1.0: AGENT_MEMORY namespace (5 tools: save/list/get/update/archive)
// inserted between CONTEXT and JUDGE; canonical count is now 34.
// v2.3.0: AGENT_MEMORY namespace grew 5 to 6 with agent_memory_recall
// (the missing consumer for the data plane); canonical count is now 35.
// v2.6.0: AGENT_BOOTSTRAP namespace (3 tools: agent_bootstrap,
// agent_recommend_companions, agent_detect_environment) inserted
// between RESEARCH and VIBE; canonical count is now 38.
// v2.7.0-alpha: MINDSET namespace (1 tool: mindset_apply) inserted
// between AGENT_MEMORY and JUDGE; canonical count is now 39.
// v2.8.0-alpha: AGENT_MEMORY grew 6 → 8 with subagent_register +
// subagent_unregister (C2 subagent scope handoff bindings); canonical
// count is now 41.
// v2.9.0-alpha PR-2: EMBEDDER namespace (1 tool: embedder_setup_prompt
// consent gate, row 164 §3) appended; canonical count is now 42.
// v2.9.0-alpha PR-3: AGENT_MEMORY grew 8 → 9 with agent_memory_entities
// (id-only read of the agent_memory_entities side-table); canonical
// count is now 43.
// v2.9.3: AGENT_MEMORY grew 9 → 10 with agent_memory_delegate
// (sub-agent delegation context handoff); canonical count is now 44.
// v2.10.0: DELEGATION namespace (1 tool: delegate_intent, Wave 5C)
// inserted between MINDSET and JUDGE; canonical count is now 45.
// v2.11.0: ERROR_OBS namespace (4 tools: error_list, error_get,
// error_summary, error_resolve — Error Observatory, spec 757)
// inserted between OBSERVABILITY and ADMIN; canonical count is now 49.
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

	if len(result.Tools) != 49 {
		// Debug: print the extras so we can see what's registered.
		names := make([]string, len(result.Tools))
		for i, t := range result.Tools {
			names[i] = t.Name
		}
		t.Fatalf("tool count: want 49 (v2.11.0 added ERROR_OBS: error_list, error_get, error_summary, error_resolve — Error Observatory spec 757; was 45 in v2.10.0 with DELEGATION delegate_intent; 44 in v2.9.3; pre-v2.9.3 was 43 in v2.9.0-alpha PR-3 with entities; 42 in v2.9.0-alpha PR-2 with EMBEDDER; v2.8.0-alpha was 41 with subagent_register + subagent_unregister; v2.7.0-alpha was 39 with MINDSET; pre-v2.7.0 was 38 in v2.6.0, 35 in v2.3.0, 34 in v2.1.0), got %d\nALL: %v", len(result.Tools), names)
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
// the 49-tool canonical order (v2.11.0; was 45 in v2.10.0, 44 in
// v2.9.3, 43 in v2.9.0-alpha PR-3, 42 in v2.9.0-alpha PR-2, 41 in
// v2.8.0-alpha, 39 in v2.7.0-alpha, 38 in v2.6.0, 35 in v2.3.0, 34 in
// v2.1.x, 29 in v2.0.x, 28 in v1.3.x, 27 in v1.2.x, 26 in v1.1.x),
// mirrored from internal/tools/registry.go so this test doesn't
// depend on the library's internal package (it tests the wire
// format, not the
// library shape).
//
// v2.1.0: AGENT_MEMORY namespace (5 tools: save/list/get/update/archive)
// inserted between CONTEXT and JUDGE per spec D-12 /
// BRIDGE_AND_COEXISTENCE.md §3 (Mem0-aligned agent memory data plane).
//
// v2.3.0: AGENT_MEMORY namespace grew 5 to 6 with agent_memory_recall
// (the missing consumer for the data plane). New canonical count = 35.
// v2.6.0: AGENT_BOOTSTRAP namespace (3 tools: agent_bootstrap,
// agent_recommend_companions, agent_detect_environment) inserted
// between RESEARCH and VIBE. New canonical count = 38.
// v2.7.0-alpha: MINDSET namespace (1 tool: mindset_apply) inserted
// between AGENT_MEMORY and JUDGE. New canonical count = 39.
// v2.8.0-alpha: subagent_register + subagent_unregister appended to
// the AGENT_MEMORY namespace (C2 subagent-scope-handoff). New canonical
// count = 41.
// v2.9.0-alpha PR-2: EMBEDDER namespace (1 tool: embedder_setup_prompt,
// consent gate per row 164 §3) appended. New canonical count = 42.
// v2.9.0-alpha PR-3: AGENT_MEMORY grew 8 → 9 with agent_memory_entities
// (id-only read of the agent_memory_entities side-table). New canonical
// count = 43.
// v2.9.3: AGENT_MEMORY grew 9 → 10 with agent_memory_delegate
// (delegation context for sub-agent spawns). New canonical count = 44.
func canonicalWireOrder() []string {
	bare := []string{
		// PROJECT (1) — v1.2.0
		"project_create",
		// SESSION (4)
		"session_start", "session_resume", "session_status", "session_close",
		// RESEARCH (3)
		"research_topic", "research_recall", "research_resume_thread",
		// AGENT_BOOTSTRAP (3) — v2.6.0
		"agent_bootstrap", "agent_recommend_companions", "agent_detect_environment",
		// VIBE (4)
		"vibe_publish", "vibe_spec", "pipeline_status", "resolve_drift",
		// CONTEXT (4) — v2.0.0 grew from 3 to 4 with `recall`
		"artifact_context", "spec_context", "session_context", "recall",
		// AGENT_MEMORY (10) — v2.1.0 (5) + v2.3.0 (1: recall) +
		// v2.8.0-alpha C2 (2: subagent register/unregister) +
		// v2.9.0-alpha PR-3 (1: entities) + v2.9.3 (1: delegate).
		"agent_memory_save", "agent_memory_list", "agent_memory_recall", "agent_memory_get", "agent_memory_update", "agent_memory_archive", "agent_memory_delegate", "agent_memory_entities", "subagent_register", "subagent_unregister",
		// MINDSET (1) — v2.7.0-alpha
		"mindset_apply",
		// DELEGATION (1) — v2.10.0 (Wave 5C)
		"delegate_intent",
		// JUDGE (3)
		"judge", "consensus", "judgment_history",
		// POLICY (2)
		"active_policy", "load_constitution",
		// OBSERVABILITY (4) — v1.3.0 grew from 3 to 4 with health_ping
		"memory_state", "writes", "anomalies", "health_ping",
		// ERROR_OBS (4) — v2.11.0 (spec 757, Wave 5D): Error
		// Observatory backlog + triage.
		"error_list", "error_get", "error_summary", "error_resolve",
		// ADMIN (3)
		"admin_migrate", "admin_schema_status", "admin_vacuum",
		// L6-VLP (1) — DMAP v1.1
		"vlp_handle_event",
		// EMBEDDER (1) — v2.9.0-alpha PR-2 (consent gate, row 164 §3).
		"embedder_setup_prompt",
	}
	out := make([]string, len(bare))
	for i, b := range bare {
		out[i] = "dark_memory_" + b
	}
	return out
}