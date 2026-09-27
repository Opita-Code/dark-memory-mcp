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
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/manifest"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/vibe"
)

// newTestDB returns a *sql.DB with every v4alpha schema applied
// + a cleanup func. Mirrors the production boot path of
// cmd/dark-memory-v4::runServe so the transport package is
// tested in the same shape it ships in.
func newTestDB(t *testing.T) (cleanup func()) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "mcp_test.db")
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
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
			t.Fatalf("%s schema: %v", fn.name, err)
		}
	}
	return func() { _ = d.Close() }
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
// count.
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
	if len(result.Tools) != 6 {
		t.Errorf("tool count = %d; want 6 (BUG-7 MVP)", len(result.Tools))
	}
	wantNames := map[string]bool{
		"dark_memory_health_ping":          false,
		"dark_memory_session_start":        false,
		"dark_memory_session_close":        false,
		"dark_memory_session_status":       false,
		"dark_memory_agent_memory_save":    false,
		"dark_memory_agent_memory_recall":  false,
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
