// Tests for internal/v4alpha/transport/mcp — JSON-RPC integration
// over an in-memory bytes.Buffer (no subprocess). Drives the
// full mcp-go handshake (initialize → notifications/initialized →
// tools/list → tools/call) so the contract is end-to-end.
//
// Each test sends a single JSON-RPC request after the handshake
// and asserts on the parsed response. Multi-request flows are
// built by concatenating NDJSON lines into one input buffer.
package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/docs_index"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/manifest"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/vibe"
)

// TestMain is the package-level setup. PRE-1 C2: skip the
// docs_index auto-index for the whole package so the 5
// docs_index rows don't pollute transport tests that hardcode
// row ids. Restored to false at end (though Go's test process
// dies after testing.M.Run(), so the restore is mostly
// defensive).
func TestMain(m *testing.M) {
	docs_index.SetTestMode(true)
	code := m.Run()
	docs_index.SetTestMode(false)
	os.Exit(code)
}

// newTestDB returns a *sql.DB with every v4alpha schema applied
// + a cleanup func. Mirrors the production boot path of
// cmd/dark-memory-v4::runServe so the transport package is
// tested in the same shape it ships in.
func newTestDB(t *testing.T) (cleanup func()) {
	t.Helper()
	// PRE-1 C2: skip the docs_index auto-index in tests so the
	// 5 docs_index rows don't get inserted before the test
	// starts (which would break tests that hardcode id=1).
	prev := docs_index.SetTestMode(true)
	t.Cleanup(func() { docs_index.SetTestMode(prev) })
	dsn := filepath.Join(t.TempDir(), "mcp_test.db")
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := applyAllSchemas(context.Background(), d); err != nil {
		t.Fatalf("applyAllSchemas: %v", err)
	}
	return func() { _ = d.Close() }
}

// applyAllSchemas is the test-friendly version of the cmd/dark-
// memory-v4 boot path. Centralised here so tests don't drift from
// production.
func applyAllSchemas(ctx context.Context, d *sql.DB) error {
	for _, fn := range []struct {
		name string
		f    func() error
	}{
		{"audit", func() error { return audit.CreateSchema(d) }},
		{"session", func() error { return session.CreateSchema(d) }},
		{"agent_memory", func() error { return agent_memory.CreateSchema(d) }},
		{"manifest/cap", func() error { return manifest.CreateCapSchema(ctx, d) }},
		{"manifest/meta", func() error { return manifest.CreateManifestSchema(ctx, d) }},
		{"vibe/spec", func() error { return vibe.CreateSpecSchema(d) }},
		{"vibe/artifact", func() error { return vibe.CreateArtifactSchema(d) }},
		{"vibe/drift", func() error { return vibe.CreateDriftSchema(d) }},
	} {
		if err := fn.f(); err != nil {
			return fmt.Errorf("%s schema: %w", fn.name, err)
		}
	}
	return nil
}

// newTestServer returns a fully-initialised *Server + cleanup
// func. Convenient for tests that need both.
func newTestServer(t *testing.T) (cleanup func(), srv *Server) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "srv_test.db")
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := applyAllSchemas(context.Background(), d); err != nil {
		t.Fatalf("applyAllSchemas: %v", err)
	}
	s, err := NewServer(d)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return func() { _ = d.Close() }, s
}

// mcpRequest is the JSON-RPC 2.0 wire format. We only model the
// fields the tests need.
type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// driveServer runs the MCP server on the given NDJSON input and
// returns the parsed response lines. Each input line is a
// JSON-RPC message; each output line is a JSON-RPC response.
//
// Cancels ctx after Listen returns or after timeout (whichever
// first). 5 s is enough for the test workload; the agent_memory
// FTS5 sync + audit_log write on Windows is the slow path.
func driveServer(t *testing.T, inputLines []string) []mcpResponse {
	t.Helper()
	// Open the DB ONCE per driveServer call with all schemas
	// applied. The DB is the one used by the mcp-go server.
	db, err := store.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "drive.db"))
	if err != nil {
		t.Fatalf("drive open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, fn := range []struct {
		name string
		f    func() error
	}{
		{"audit", func() error { return audit.CreateSchema(db) }},
		{"session", func() error { return session.CreateSchema(db) }},
		{"agent_memory", func() error { return agent_memory.CreateSchema(db) }},
		{"manifest/cap", func() error { return manifest.CreateCapSchema(ctx, db) }},
		{"manifest/meta", func() error { return manifest.CreateManifestSchema(ctx, db) }},
		{"vibe/spec", func() error { return vibe.CreateSpecSchema(db) }},
		{"vibe/artifact", func() error { return vibe.CreateArtifactSchema(db) }},
		{"vibe/drift", func() error { return vibe.CreateDriftSchema(db) }},
	} {
		if err := fn.f(); err != nil {
			t.Fatalf("%s schema: %v", fn.name, err)
		}
	}

	srv, err := NewServer(db)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return driveOn(t, srv, inputLines)
}

// driveOn runs an mcp-go server against the given input lines
// and returns the parsed NDJSON responses. The server is shared
// across multiple drives (state preserved between calls).
//
// targetIDs is an optional filter: if non-empty, only return
// responses whose ID matches one of the targets. Used by tests
// that need to inspect a specific response.
func driveOn(t *testing.T, srv *Server, inputLines []string, targetIDs ...float64) []mcpResponse {
	t.Helper()
	in := bytes.NewBufferString(strings.Join(inputLines, "\n") + "\n")
	out := &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ServeStdio(ctx, in, out)
	}()

	select {
	case <-errCh:
		// Listen returned (EOF or ctx timeout).
	case <-time.After(8 * time.Second):
		t.Logf("drive timed out; output so far:\n%s", out.String())
		t.Fatal("mcp.ServeStdio did not return within 8s")
	}

	// Parse NDJSON output.
	var responses []mcpResponse
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var r mcpResponse
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("parse response line %q: %v", line, err)
		}
		if len(targetIDs) > 0 {
			match := false
			for _, tid := range targetIDs {
				if r.ID == tid {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		responses = append(responses, r)
	}
	return responses
}

// handshake returns the standard initialize + initialized
// notification + tools/list triplet, suitable for prepending
// to a tools/call test.
func handshake() []string {
	return []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
}

// callTool encodes a tools/call request for the given tool.
func callTool(id any, name string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return mustJSON(mcpRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      name,
			"arguments": json.RawMessage(b),
		},
	})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// --- tests ---

// TestInitialize_RespondsWithServerInfo is the protocol smoke
// test: the server responds to initialize with its identity +
// protocol version.
func TestInitialize_RespondsWithServerInfo(t *testing.T) {
	responses := driveServer(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}`,
	})
	if len(responses) == 0 {
		t.Fatal("no response from initialize")
	}
	r := responses[0]
	if r.Error != nil {
		t.Fatalf("initialize error: %+v", r.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result["serverInfo"] == nil {
		t.Errorf("serverInfo missing")
	}
	sinfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo not an object: %T", result["serverInfo"])
	}
	if sinfo["name"] != ServerName {
		t.Errorf("server name = %v; want %s", sinfo["name"], ServerName)
	}
}

// TestToolsList_ReturnsSixTools confirms the BUG-7 MVP tool
// count. ADR-007 C2 adds 4 judge tools (judge + consensus +
// judgment_history + list_personas). PRE-1 C4 adds 2 tools
// (summarize_session + skill_loaded) — total now 41.
func TestToolsList_ReturnsSixTools(t *testing.T) {
	responses := driveServer(t, handshake())
	// handshake has 3 messages; the server emits responses for
	// initialize + tools/list (the notification has no response).
	if len(responses) < 2 {
		t.Fatalf("got %d responses; want >= 2 (initialize + tools/list)", len(responses))
	}
	list := responses[1]
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &result); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}
	if len(result.Tools) != 41 {
		t.Errorf("tool count = %d; want 41 (BUG-7 MVP + BUG-8 batch 1 + ADR-007 C2 judge + BUG-10 10a judge_util + research + PRE-1 C4 summarize)", len(result.Tools))
	}
	wantNames := map[string]bool{
		// BUG-7 MVP
		"dark_memory_health_ping":          false,
		"dark_memory_session_start":        false,
		"dark_memory_session_close":        false,
		"dark_memory_session_status":       false,
		"dark_memory_agent_memory_save":    false,
		"dark_memory_agent_memory_recall":  false,
		// BUG-8 batch 1
		"dark_memory_session_resume":       false,
		"dark_memory_session_heartbeat":    false,
		"dark_memory_agent_memory_list":    false,
		"dark_memory_agent_memory_get":     false,
		"dark_memory_agent_memory_update":  false,
		"dark_memory_agent_memory_archive": false,
		"dark_memory_memory_state":         false,
		"dark_memory_writes":               false,
		"dark_memory_anomalies":            false,
		"dark_memory_error_summary":        false,
		"dark_memory_error_list":           false,
		"dark_memory_error_get":            false,
		"dark_memory_error_resolve":        false,
		"dark_memory_active_policy":        false,
		"dark_memory_load_constitution":    false,
		"dark_memory_vibe_spec":            false,
		"dark_memory_vibe_publish":         false,
		"dark_memory_vibe_pipeline_status": false,
		"dark_memory_vibe_resolve_drift":   false,
		// ADR-007 C2 (LLM-backed judge surface)
		"dark_memory_judge":                 false,
		"dark_memory_consensus":             false,
		"dark_memory_judgment_history":      false,
		"dark_memory_judge_list_personas":   false,
	}
	for _, t1 := range result.Tools {
		if _, ok := wantNames[t1.Name]; ok {
			wantNames[t1.Name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("tools/list missing %s", name)
		}
	}
}

// TestHealthPing_ReportsStatus exercises the dark_memory_health_ping tool.
func TestHealthPing_ReportsStatus(t *testing.T) {
	lines := append(handshake(), callTool(3, "dark_memory_health_ping", nil))
	responses := driveServer(t, lines)

	// Find the health_ping response (id=3).
	var hp *mcpResponse
	for i := range responses {
		if responses[i].ID == float64(3) {
			hp = &responses[i]
			break
		}
	}
	if hp == nil {
		t.Fatalf("no response with id=3: %+v", responses)
	}
	if hp.Error != nil {
		t.Fatalf("health_ping error: %+v", hp.Error)
	}

	// mcp-go wraps tool results in {"content": [...], "isError": false}.
	// The actual JSON shape is in content[0].text.
	var wrapper struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(hp.Result, &wrapper); err != nil {
		t.Fatalf("parse health result: %v", err)
	}
	if wrapper.IsError {
		t.Fatalf("health_ping isError=true")
	}
	if len(wrapper.Content) == 0 {
		t.Fatal("health_ping result has no content")
	}
	var payload healthPingResponse
	if err := json.Unmarshal([]byte(wrapper.Content[0].Text), &payload); err != nil {
		t.Fatalf("parse health text: %v\ntext: %s", err, wrapper.Content[0].Text)
	}
	if payload.Status != "ok" {
		t.Errorf("status = %q; want ok", payload.Status)
	}
	if payload.ServerName != ServerName {
		t.Errorf("server_name = %q; want %q", payload.ServerName, ServerName)
	}
	if payload.ToolCount != 6 {
		t.Errorf("tool_count = %d; want 6", payload.ToolCount)
	}
	if !payload.DBOpen {
		t.Errorf("db_open = false; want true")
	}
}

// TestSessionStartClose_FullLifecycle drives the session_* tools
// end-to-end: start → status → close. Single drive so all
// requests share the same DB state and the same mcp-go server
// instance (cross-drive goroutine cleanup is unreliable).
//
// Note on session_id resolution: the session_id returned by
// start is needed by status + close. We use a 2-pass approach
// because mcp-go doesn't let us inspect a response mid-stream:
//
//   pass 1 — drive initialize + start, capture session_id
//   pass 2 — drive initialize + status(known-id) + close(known-id)
//
// Each pass uses its own driveServer call so the goroutines from
// pass 1 are fully drained before pass 2 starts. The DB is
// shared because we open it once per test, not per drive.
func TestSessionStartClose_FullLifecycle(t *testing.T) {
	// Open the DB ONCE for both passes, with every v4alpha schema
	// applied. Same setup as driveServer, inline.
	db, err := store.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, fn := range []struct {
		name string
		f    func() error
	}{
		{"audit", func() error { return audit.CreateSchema(db) }},
		{"session", func() error { return session.CreateSchema(db) }},
		{"agent_memory", func() error { return agent_memory.CreateSchema(db) }},
		{"manifest/cap", func() error { return manifest.CreateCapSchema(ctx, db) }},
		{"manifest/meta", func() error { return manifest.CreateManifestSchema(ctx, db) }},
		{"vibe/spec", func() error { return vibe.CreateSpecSchema(db) }},
		{"vibe/artifact", func() error { return vibe.CreateArtifactSchema(db) }},
		{"vibe/drift", func() error { return vibe.CreateDriftSchema(db) }},
	} {
		if err := fn.f(); err != nil {
			t.Fatalf("%s schema: %v", fn.name, err)
		}
	}

	srv, err := NewServer(db)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Pass 1: capture session_id.
	startResponses := driveOn(t, srv, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		callTool(3, "dark_memory_session_start", map[string]any{"operator": "nico"}),
	}, float64(3))
	if len(startResponses) == 0 {
		t.Fatal("pass 1: no session_start response")
	}
	startResp := startResponses[0]
	var startPayload sessionStartOutput
	if err := extractToolText(t, startResp.Result, &startPayload); err != nil {
		t.Fatalf("parse start: %v", err)
	}
	if startPayload.SessionID == "" {
		t.Fatal("start returned empty session_id")
	}
	if startPayload.Status != "open" {
		t.Errorf("start status = %q; want open", startPayload.Status)
	}

	// Pass 2: status + close using the captured session_id.
	sid := startPayload.SessionID
	responses := driveOn(t, srv, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		callTool(4, "dark_memory_session_status", map[string]any{"session_id": sid}),
		callTool(5, "dark_memory_session_close", map[string]any{"session_id": sid, "clean": true}),
	}, float64(4), float64(5))

	var statusResp, closeResp *mcpResponse
	for i := range responses {
		switch responses[i].ID {
		case float64(4):
			statusResp = &responses[i]
		case float64(5):
			closeResp = &responses[i]
		}
	}
	if statusResp == nil || statusResp.Error != nil {
		t.Fatalf("status failed: %+v", statusResp)
	}
	var statusPayload sessionStatusOutput
	if err := extractToolText(t, statusResp.Result, &statusPayload); err != nil {
		t.Fatalf("parse status: %v", err)
	}
	if statusPayload.Status != "open" {
		t.Errorf("status before close = %q; want open", statusPayload.Status)
	}

	if closeResp == nil || closeResp.Error != nil {
		t.Fatalf("close failed: %+v", closeResp)
	}
	var closePayload sessionCloseOutput
	if err := extractToolText(t, closeResp.Result, &closePayload); err != nil {
		t.Fatalf("parse close: %v", err)
	}
	if closePayload.Status != "closed_clean" {
		t.Errorf("close status = %q; want closed_clean", closePayload.Status)
	}
	if closePayload.ClosedAt == "" {
		t.Errorf("closed_at empty")
	}
}

// TestAgentMemorySaveAndRecall drives the memory_* tools: save
// two rows, recall one by FTS5 query.
func TestAgentMemorySaveAndRecall(t *testing.T) {
	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		// save row 1: decision about WAL
		callTool(3, "dark_memory_agent_memory_save", map[string]any{
			"operator": "nico",
			"kind":     "decision",
			"title":    "dark-db concurrency",
			"content":  "WAL + busy_timeout=5000 + bounded pool — INV-16",
			"tags":     "sqlite,concurrency",
			"pinned":   true,
		}),
		// save row 2: note about another topic
		callTool(4, "dark_memory_agent_memory_save", map[string]any{
			"operator": "nico",
			"kind":     "note",
			"title":    "cooking",
			"content":  "how to make empanadas",
			"tags":     "cooking,food",
		}),
		// recall for "busy_timeout"
		callTool(5, "dark_memory_agent_memory_recall", map[string]any{
			"operator": "nico",
			"query":    "busy_timeout",
		}),
	}
	responses := driveServer(t, lines)

	var save1, save2, recall *mcpResponse
	for i := range responses {
		switch responses[i].ID {
		case float64(3):
			save1 = &responses[i]
		case float64(4):
			save2 = &responses[i]
		case float64(5):
			recall = &responses[i]
		}
	}
	if save1 == nil || save1.Error != nil {
		t.Fatalf("save1 failed: %+v", save1)
	}
	if save2 == nil || save2.Error != nil {
		t.Fatalf("save2 failed: %+v", save2)
	}
	if recall == nil || recall.Error != nil {
		t.Fatalf("recall failed: %+v", recall)
	}

	var save1Payload, save2Payload agentMemorySaveOutput
	if err := extractToolText(t, save1.Result, &save1Payload); err != nil {
		t.Fatalf("parse save1: %v", err)
	}
	if err := extractToolText(t, save2.Result, &save2Payload); err != nil {
		t.Fatalf("parse save2: %v", err)
	}
	if save1Payload.ID == save2Payload.ID {
		t.Errorf("two saves returned same id %d", save1Payload.ID)
	}

	var recallPayload agentMemoryRecallOutput
	if err := extractToolText(t, recall.Result, &recallPayload); err != nil {
		t.Fatalf("parse recall: %v\nraw: %s", err, string(recall.Result))
	}
	if recallPayload.Count != 1 {
		t.Errorf("recall count = %d; want 1\nrecall payload: %+v\nrecall raw: %s", recallPayload.Count, recallPayload, string(recall.Result))
	}
	if len(recallPayload.Rows) == 0 {
		t.Fatal("recall returned 0 rows")
	}
	if recallPayload.Rows[0].Kind != "decision" {
		t.Errorf("recall kind = %q; want decision", recallPayload.Rows[0].Kind)
	}
	if !strings.Contains(recallPayload.Rows[0].Content, "busy_timeout") {
		t.Errorf("recall content = %q; want contains 'busy_timeout'", recallPayload.Rows[0].Content)
	}
}

// TestAgentMemorySave_RejectsInvalidKind — the Store's allow-list
// is wired through the transport layer.
func TestAgentMemorySave_RejectsInvalidKind(t *testing.T) {
	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		callTool(3, "dark_memory_agent_memory_save", map[string]any{
			"operator": "nico",
			"kind":     "bogus",
			"content":  "x",
		}),
	}
	responses := driveServer(t, lines)
	var resp *mcpResponse
	for i := range responses {
		if responses[i].ID == float64(3) {
			resp = &responses[i]
			break
		}
	}
	if resp == nil {
		t.Fatal("no response")
	}
	// mcp-go wraps tool errors as isError=true with the error
	// message in content[0].text.
	var wrapper struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &wrapper); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !wrapper.IsError {
		t.Fatalf("expected isError=true; got false")
	}
	if !strings.Contains(wrapper.Content[0].Text, "invalid kind") {
		t.Errorf("error text should mention 'invalid kind'; got %q", wrapper.Content[0].Text)
	}
}

// --- BUG-8 batch 1: agent_memory list/get/update/archive ---

func TestAgentMemoryListAndGet(t *testing.T) {
	cleanup, srv := newTestServer(t)
	defer cleanup()
	listResp := driveOn(t, srv, append(handshake(),
		callTool(10, "dark_memory_agent_memory_save", map[string]any{"operator": "nico", "kind": "note", "content": "row one", "pinned": true}),
		callTool(11, "dark_memory_agent_memory_save", map[string]any{"operator": "nico", "kind": "finding", "content": "row two"}),
		callTool(12, "dark_memory_agent_memory_list", map[string]any{"operator": "nico"}),
		callTool(13, "dark_memory_agent_memory_get", map[string]any{"id": float64(1)}),
		callTool(14, "dark_memory_agent_memory_get", map[string]any{"id": float64(9999)}),
	), 12, 13, 14)
	if len(listResp) != 3 {
		t.Fatalf("got %d responses; want 3 (list, get, get-miss)", len(listResp))
	}
	var listOut struct {
		Operator string `json:"operator"`
		Count    int    `json:"count"`
	}
	if err := extractToolText(t, listResp[0].Result, &listOut); err != nil {
		t.Fatalf("list: %v", err)
	}
	if listOut.Count != 2 {
		t.Errorf("list count = %d; want 2", listOut.Count)
	}
	var getOut struct {
		ID  int64 `json:"id"`
		Row *struct {
			Content string `json:"content"`
		} `json:"row"`
	}
	if err := extractToolText(t, listResp[1].Result, &getOut); err != nil {
		t.Fatalf("get: %v", err)
	}
	if getOut.Row == nil || getOut.Row.Content != "row one" {
		t.Errorf("get id=1 row = %+v; want content=row one", getOut.Row)
	}
	var missOut struct {
		Found bool `json:"found"`
	}
	if err := extractToolText(t, listResp[2].Result, &missOut); err != nil {
		t.Fatalf("get-miss: %v", err)
	}
	if missOut.Found {
		t.Errorf("get id=9999 should report found=false")
	}
}

func TestAgentMemoryUpdateAndArchive(t *testing.T) {
	cleanup, srv := newTestServer(t)
	defer cleanup()
	responses := driveOn(t, srv, append(handshake(),
		callTool(10, "dark_memory_agent_memory_save", map[string]any{"operator": "nico", "kind": "note", "content": "original", "tags": "a,b"}),
		callTool(11, "dark_memory_agent_memory_update", map[string]any{"id": float64(1), "operator": "nico", "content": "updated", "tags": "x,y"}),
		callTool(12, "dark_memory_agent_memory_recall", map[string]any{"operator": "nico", "query": "updated"}),
		callTool(13, "dark_memory_agent_memory_archive", map[string]any{"id": float64(1), "operator": "nico"}),
		callTool(14, "dark_memory_agent_memory_recall", map[string]any{"operator": "nico", "query": "updated"}),
	), 12, 14)
	if len(responses) != 2 {
		t.Fatalf("got %d; want 2", len(responses))
	}
	var recallBefore struct {
		Count int `json:"count"`
	}
	if err := extractToolText(t, responses[0].Result, &recallBefore); err != nil {
		t.Fatalf("recall before: %v", err)
	}
	if recallBefore.Count != 1 {
		t.Errorf("recall after update count = %d; want 1 (FTS5 sync)", recallBefore.Count)
	}
	var recallAfter struct {
		Count int `json:"count"`
	}
	if err := extractToolText(t, responses[1].Result, &recallAfter); err != nil {
		t.Fatalf("recall after: %v", err)
	}
	if recallAfter.Count != 0 {
		t.Errorf("recall after archive count = %d; want 0", recallAfter.Count)
	}
}

// --- BUG-8 batch 1: session resume + heartbeat ---

func TestSessionResumeAndHeartbeat(t *testing.T) {
	cleanup, srv := newTestServer(t)
	defer cleanup()
	startResp := driveOn(t, srv, append(handshake(),
		callTool(3, "dark_memory_session_start", map[string]any{"operator": "nico"}),
	), 3)
	if len(startResp) != 1 {
		t.Fatalf("pass 1: got %d; want 1", len(startResp))
	}
	var startOut sessionStartOutput
	if err := extractToolText(t, startResp[0].Result, &startOut); err != nil {
		t.Fatalf("start: %v", err)
	}
	if startOut.SessionID == "" {
		t.Fatal("empty session_id")
	}
	sid := startOut.SessionID
	resp2 := driveOn(t, srv, append(handshake(),
		callTool(4, "dark_memory_session_heartbeat", map[string]any{"session_id": sid}),
		callTool(5, "dark_memory_session_resume", map[string]any{"session_id": sid}),
	), 4, 5)
	var hbResp struct {
		Refreshed bool `json:"refreshed"`
	}
	if err := extractToolText(t, resp2[0].Result, &hbResp); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !hbResp.Refreshed {
		t.Errorf("heartbeat refreshed = false")
	}
	var resumeResp struct {
		Status string `json:"status"`
	}
	if err := extractToolText(t, resp2[1].Result, &resumeResp); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumeResp.Status != "open" {
		t.Errorf("resume status = %q; want open", resumeResp.Status)
	}
}

// --- BUG-8 batch 1: observability ---

func TestObservabilityTools(t *testing.T) {
	cleanup, srv := newTestServer(t)
	defer cleanup()
	responses := driveOn(t, srv, append(handshake(),
		callTool(10, "dark_memory_memory_state", map[string]any{}),
		callTool(11, "dark_memory_writes", map[string]any{"limit": 5}),
		callTool(12, "dark_memory_anomalies", map[string]any{}),
	), 10, 11, 12)
	if len(responses) != 3 {
		t.Fatalf("got %d; want 3", len(responses))
	}
	var state struct {
		DBOpen bool `json:"db_open"`
	}
	if err := extractToolText(t, responses[0].Result, &state); err != nil {
		t.Fatalf("memory_state: %v", err)
	}
	if !state.DBOpen {
		t.Errorf("memory_state db_open = false; want true")
	}
	var writes struct {
		Limit int `json:"limit"`
	}
	if err := extractToolText(t, responses[1].Result, &writes); err != nil {
		t.Fatalf("writes: %v", err)
	}
	if writes.Limit != 5 {
		t.Errorf("writes limit echo = %d; want 5", writes.Limit)
	}
	var anomalies struct {
		Count int `json:"count"`
	}
	if err := extractToolText(t, responses[2].Result, &anomalies); err != nil {
		t.Fatalf("anomalies: %v", err)
	}
	if anomalies.Count != 0 {
		t.Errorf("anomalies count = %d; want 0 (fresh DB)", anomalies.Count)
	}
}

// --- BUG-8 batch 1: error_obs ---

func TestErrorObsTools(t *testing.T) {
	cleanup, srv := newTestServer(t)
	defer cleanup()
	responses := driveOn(t, srv, append(handshake(),
		callTool(10, "dark_memory_error_summary", map[string]any{}),
		callTool(11, "dark_memory_error_list", map[string]any{}),
		callTool(12, "dark_memory_error_resolve", map[string]any{"id": float64(9999), "operator": "nico", "note": "missing"}),
	), 10, 11, 12)
	var summary struct {
		HoursWindow int `json:"hours_window"`
	}
	if err := extractToolText(t, responses[0].Result, &summary); err != nil {
		t.Fatalf("error_summary: %v", err)
	}
	if summary.HoursWindow != 1 {
		t.Errorf("error_summary hours_window = %d; want 1", summary.HoursWindow)
	}
	if responses[2].Error != nil {
		return
	}
	var wrapper struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(responses[2].Result, &wrapper); err != nil {
		t.Fatalf("resolve unmarshal: %v", err)
	}
	if !wrapper.IsError {
		t.Errorf("error_resolve on missing id should be isError=true")
	}
}

// --- BUG-8 batch 1: policy ---

func TestPolicyTools(t *testing.T) {
	cleanup, srv := newTestServer(t)
	defer cleanup()
	responses := driveOn(t, srv, append(handshake(),
		callTool(10, "dark_memory_active_policy", map[string]any{}),
		callTool(11, "dark_memory_load_constitution", map[string]any{}),
	), 10, 11)
	var policy struct {
		Driver string `json:"driver"`
		Active bool   `json:"active"`
	}
	if err := extractToolText(t, responses[0].Result, &policy); err != nil {
		t.Fatalf("active_policy: %v", err)
	}
	if policy.Driver != "sqlite" || !policy.Active {
		t.Errorf("active_policy = %+v; want driver=sqlite active=true", policy)
	}
	var constitution struct {
		Parsed map[string]any `json:"parsed"`
	}
	if err := extractToolText(t, responses[1].Result, &constitution); err != nil {
		t.Fatalf("load_constitution: %v", err)
	}
	if _, ok := constitution.Parsed["invariants"]; !ok {
		t.Errorf("load_constitution missing 'invariants' key")
	}
}

// --- BUG-8 batch 1: vibe spec → publish → status → resolve ---

func TestVibeSpecPublishStatusResolve(t *testing.T) {
	cleanup, srv := newTestServer(t)
	defer cleanup()
	responses := driveOn(t, srv, append(handshake(),
		callTool(10, "dark_memory_vibe_spec", map[string]any{
			"vibe_case": "C1",
			"intent":    "smoke test",
			"tasks":     []map[string]any{{"id": "t1", "description": "hello"}},
		}),
		callTool(11, "dark_memory_vibe_publish", map[string]any{
			"spec_id": float64(1),
			"type":    "text",
			"text":    "hello world",
		}),
		callTool(12, "dark_memory_vibe_pipeline_status", map[string]any{"artifact_id": float64(1)}),
		callTool(13, "dark_memory_vibe_resolve_drift", map[string]any{
			"drift_id": float64(1), "decision": "accept", "operator": "nico",
		}),
	), 10, 11, 12, 13)
	if len(responses) != 4 {
		t.Fatalf("got %d; want 4", len(responses))
	}
	var specResp struct {
		SpecID int64 `json:"spec_id"`
	}
	if err := extractToolText(t, responses[0].Result, &specResp); err != nil {
		t.Fatalf("vibe_spec: %v", err)
	}
	if specResp.SpecID != 1 {
		t.Errorf("spec_id = %d; want 1", specResp.SpecID)
	}
	var pubResp struct {
		ArtifactID int64   `json:"artifact_id"`
		DriftID    int64   `json:"drift_id"`
		Verdict    string  `json:"verdict"`
		Confidence float64 `json:"confidence"`
	}
	if err := extractToolText(t, responses[1].Result, &pubResp); err != nil {
		t.Fatalf("vibe_publish: %v", err)
	}
	if pubResp.ArtifactID != 1 || pubResp.DriftID != 1 {
		t.Errorf("publish ids = (%d, %d); want (1, 1)", pubResp.ArtifactID, pubResp.DriftID)
	}
	if pubResp.Verdict != "aligned" {
		t.Errorf("publish verdict = %q; want aligned (NoOp judge)", pubResp.Verdict)
	}
	if pubResp.Confidence != 1.0 {
		t.Errorf("publish confidence = %f; want 1.0", pubResp.Confidence)
	}
	var statusResp struct {
		Verdict string `json:"verdict"`
	}
	if err := extractToolText(t, responses[2].Result, &statusResp); err != nil {
		t.Fatalf("pipeline_status: %v", err)
	}
	if statusResp.Verdict != "aligned" {
		t.Errorf("status verdict = %q; want aligned", statusResp.Verdict)
	}
	var resolveResp struct {
		Decision string `json:"decision"`
	}
	if err := extractToolText(t, responses[3].Result, &resolveResp); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolveResp.Decision != "accept" {
		t.Errorf("resolve decision = %q; want accept", resolveResp.Decision)
	}
}

// --- helpers ---

// extractToolText pulls content[0].text from a mcp-go tool result
// envelope and unmarshals it into target. The envelope shape is
// defined by mcp-go's CallToolResult marshalling.
func extractToolText(t *testing.T, raw json.RawMessage, target any) error {
	t.Helper()
	var wrapper struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return err
	}
	if wrapper.IsError {
		if len(wrapper.Content) > 0 {
			return errExtract(wrapper.Content[0].Text)
		}
		return errExtract("isError=true with no content")
	}
	if len(wrapper.Content) == 0 {
		return errExtract("no content in result")
	}
	return json.Unmarshal([]byte(wrapper.Content[0].Text), target)
}

type errExtract string

func (e errExtract) Error() string { return string(e) }
