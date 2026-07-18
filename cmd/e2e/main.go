// e2e tool: boots the v1.3.0 binary against the operator's CANONICAL
// dark-memory.db and exercises every wire tool path that matters
// for production readiness. Run with:
//
//   DARK_MEM_MCP_BIN=path/to/dark-mem-mcp.exe go run ./cmd/e2e
//
// Prints ONE line per phase; non-zero exit on any failure.
//
// Why a Go program and not a wire test? Because the wire tests in
// tests/wire use a per-test isolated DARK_DB (t.TempDir()), which is
// the right shape for CI. This e2e talks to the operator's ACTUAL
// canonical DB to prove the binary doesn't corrupt operator state.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type phase struct {
	name   string
	call   map[string]any
	checks []string
}

func main() {
	bin := os.Getenv("DARK_MEM_MCP_BIN")
	if bin == "" {
		bin = `C:\Users\Nico\Documents\dark-memory-mcp\cmd\dark-mem-mcp\dark-mem-mcp.exe`
	}
	// Hardcode dark-memory's DB; the harness may have DARK_DB=dark.db
	// in its inherited env (from dark-research-mcp), which would
	// silently route us to the wrong store. Operators can override
	// with DARK_E2E_DB.
	dbPath := `C:\Users\Nico\AppData\Local\dark-agents\dark-memory.db`
	if override := os.Getenv("DARK_E2E_DB"); override != "" {
		dbPath = override
	}

	// Sanity: binary exists and is the v1.3.0 build.
	info, err := os.Stat(bin)
	if err != nil {
		fail("binary not found: %v", err)
	}
	if info.Size() < 1_000_000 {
		fail("binary too small (%d B) — not a real build?", info.Size())
	}

	// Boot the binary as a subprocess.
	cmd := exec.Command(bin)
	// Strip inherited DARK_DB* so the harness's dark-research
	// values can't silently override ours.
	cmd.Env = []string{}
	for _, e := range os.Environ() {
		if !startsWithEnv(e, "DARK_DB") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env,
		"DARK_DB="+dbPath,
		"DARK_DB_DRIVER=sqlite",
		"DARK_REDTEAM=armed",
		"DARK_REDTEAM_MODS_PATH=C:/Users/Nico/Documents/dark-memory-mcp/mods/redteam",
		"DARK_MOD_WHITELIST=redteam/prompt-injection-lab,redteam/jailbreak-taxonomy,redteam/llm-refusal-analysis",
		"DARK_CONSTITUTION_FILE=",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail("stdin: %v", err)
	}
	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		fail("stdout: %v", err)
	}
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		fail("stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		fail("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	stdout := bufio.NewReader(stdoutR)

	// Drain stderr in background; we keep the lines so we can print
	// them on failure (the binary's boot log + any panic trace).
	var stderrLines []string
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			stderrLines = append(stderrLines, scanner.Text())
		}
	}()
	defer func() {
		// Give the scanner a moment to flush before we kill.
		select {
		case <-stderrDone:
		case <-time.After(200 * time.Millisecond):
		}
	}()

	// Wait up to 30s for the binary to respond to a single initialize
	// request. Write ONE frame, then block on the read with a 30s
	// timeout. The previous loop-with-backoff variant would write
	// multiple initialize frames in rapid succession, which mcp-go
	// may silently drop or interleave on the read side.
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e", "version": "1.3.0"},
		},
	})
	_, _ = stdin.Write(append(raw, '\n'))

	initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer initCancel()
	initDone := make(chan []byte, 1)
	go func() {
		line, _ := stdout.ReadString('\n')
		initDone <- []byte(line)
	}()
	var initResp []byte
	select {
	case line := <-initDone:
		initResp = line
	case <-initCtx.Done():
		fmt.Fprintln(os.Stderr, "--- binary stderr ---")
		for _, l := range stderrLines {
			fmt.Fprintln(os.Stderr, l)
		}
		fail("initialize: no response within 30s")
	}
	if len(initResp) == 0 {
		fmt.Fprintln(os.Stderr, "--- binary stderr ---")
		for _, l := range stderrLines {
			fmt.Fprintln(os.Stderr, l)
		}
		fail("initialize: empty response")
	}

	var initEnv struct {
		Result struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Capabilities struct {
				Tools struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initResp, &initEnv); err != nil {
		fail("initialize response: %v\n  raw=%s", err, initResp)
	}
	if initEnv.Result.ServerInfo.Name != "dark-memory-mcp" {
		fail("server name=%q; want dark-memory-mcp", initEnv.Result.ServerInfo.Name)
	}
	if initEnv.Result.ServerInfo.Version != "1.3.0" {
		fail("server version=%q; want 1.3.0", initEnv.Result.ServerInfo.Version)
	}
	ok("initialize: server=%s v=%s capabilities.tools.listChanged=%t",
		initEnv.Result.ServerInfo.Name,
		initEnv.Result.ServerInfo.Version,
		initEnv.Result.Capabilities.Tools.ListChanged,
	)

	// Send notifications/initialized (no reply expected).
	notifRaw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
	_, _ = stdin.Write(append(notifRaw, '\n'))

	// Helper to send a JSON-RPC request and read the response line.
	rpc := func(id int, method string, params any) []byte {
		raw, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method, "params": params,
		})
		_, _ = stdin.Write(append(raw, '\n'))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan []byte, 1)
		go func() {
			line, _ := stdout.ReadString('\n')
			done <- []byte(line)
		}()
		select {
		case line := <-done:
			return line
		case <-ctx.Done():
			return []byte("<timeout>")
		}
	}

	// Helper to extract text from the mcp-go content envelope.
	// (tools/call responses come in this shape; tools/list uses a
	// different shape — see toolsListUnwrap below.)
	unwrap := func(raw []byte) (string, error) {
		var env struct {
			Result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return "", fmt.Errorf("envelope: %w", err)
		}
		if env.Error != nil {
			return "", fmt.Errorf("rpc error code=%d msg=%q", env.Error.Code, env.Error.Message)
		}
		if len(env.Result.Content) == 0 {
			return "", fmt.Errorf("no content")
		}
		return env.Result.Content[0].Text, nil
	}

	// tools/list returns {"result":{"tools":[...]}} directly (NOT
	// wrapped in content[] like tools/call responses).
	toolsListUnwrap := func(raw []byte) ([]string, error) {
		var env struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("tools/list parse: %w", err)
		}
		names := make([]string, 0, len(env.Result.Tools))
		for _, t := range env.Result.Tools {
			names = append(names, t.Name)
		}
		return names, nil
	}

	// Phase 1: tools/list — assert 28 canonical + 3 redteam = 31.
	listResp := rpc(2, "tools/list", map[string]any{})
	toolNames, err := toolsListUnwrap(listResp)
	if err != nil {
		fail("tools/list: %v\n  raw=%s", err, listResp)
	}
	if len(toolNames) != 31 {
		fail("tools/list count=%d; want 31 (28 canonical + 3 redteam)", len(toolNames))
	}
	hasHealth := false
	hasRedteam := false
	for _, n := range toolNames {
		if n == "dark_memory_health_ping" {
			hasHealth = true
		}
		if n == "dark_memory_redteam_list_mods" {
			hasRedteam = true
		}
	}
	if !hasHealth {
		fail("tools/list: dark_memory_health_ping MISSING (v1.3.0 canonical 28)")
	}
	if !hasRedteam {
		fail("tools/list: dark_memory_redteam_list_mods MISSING (DARK_REDTEAM=armed)")
	}
	ok("tools/list: 31 tools (28 canonical + 3 redteam); health_ping + redteam present")

	// Phase 2: dark_memory_health_ping (the v1.3.0 headline tool).
	hpResp := rpc(3, "tools/call", map[string]any{
		"name": "dark_memory_health_ping", "arguments": map[string]any{},
	})
	hpText, err := unwrap(hpResp)
	if err != nil {
		fail("health_ping unwrap: %v\n  raw=%s", err, hpResp)
	}
	var hpData struct {
		Data struct {
			Server struct {
				Name             string `json:"name"`
				Version          string `json:"version"`
				CoexistenceGroup string `json:"coexistence_group"`
				RedTeamArmed     bool   `json:"redteam_armed"`
			} `json:"server"`
			DB struct {
				Driver        string `json:"driver"`
				DSNPath       string `json:"dsn_path"`
				Live          bool   `json:"live"`
				SchemaVersion int    `json:"schema_version"`
				CanaryPresent bool   `json:"canary_present"`
			} `json:"db"`
			Runtime struct {
				UptimeSeconds float64 `json:"uptime_seconds"`
				PID           int     `json:"pid"`
			} `json:"runtime"`
			Registry struct {
				CanonicalTools int `json:"canonical_tools"`
				ExtraTools     int `json:"extra_tools"`
			} `json:"registry"`
			LatencyMS float64 `json:"latency_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(hpText), &hpData); err != nil {
		fail("health_ping body: %v\n  body=%s", err, hpText)
	}
	if hpData.Data.Server.Version != "1.3.0" {
		fail("health_ping server.version=%q; want 1.3.0", hpData.Data.Server.Version)
	}
	if hpData.Data.Server.CoexistenceGroup != "dark-agents/memory" {
		fail("coexistence_group=%q; want dark-agents/memory", hpData.Data.Server.CoexistenceGroup)
	}
	if !hpData.Data.Server.RedTeamArmed {
		fail("server.redteam_armed=false; want true (DARK_REDTEAM=armed)")
	}
	if hpData.Data.DB.Driver != "sqlite" {
		fail("db.driver=%q; want sqlite", hpData.Data.DB.Driver)
	}
	if !hpData.Data.DB.Live {
		fail("db.live=false; want true (just-booted DB ping succeeded)")
	}
	if hpData.Data.DB.SchemaVersion < 10 {
		fail("db.schema_version=%d; want >=10 (current)", hpData.Data.DB.SchemaVersion)
	}
	if !hpData.Data.DB.CanaryPresent {
		fail("db.canary_present=false; want true (canary installed at boot step4)")
	}
	if hpData.Data.Runtime.UptimeSeconds <= 0 {
		fail("runtime.uptime_seconds=%v; want >0", hpData.Data.Runtime.UptimeSeconds)
	}
	if hpData.Data.Runtime.PID != os.Getpid() && hpData.Data.Runtime.PID <= 0 {
		fail("runtime.pid=%d; want >0", hpData.Data.Runtime.PID)
	}
	if hpData.Data.Registry.CanonicalTools != 28 {
		fail("registry.canonical_tools=%d; want 28 (v1.3.0 contract)", hpData.Data.Registry.CanonicalTools)
	}
	if hpData.Data.Registry.ExtraTools != 3 {
		fail("registry.extra_tools=%d; want 3 (redteam armed)", hpData.Data.Registry.ExtraTools)
	}
	if hpData.Data.LatencyMS <= 0 {
		fail("latency_ms=%v; want >0 (>=0.001ms with floor)", hpData.Data.LatencyMS)
	}
	// H1 PII redaction check
	operatorUser := os.Getenv("USERNAME")
	if operatorUser == "" {
		operatorUser = os.Getenv("USER")
	}
	if operatorUser != "" && contains(hpData.Data.DB.DSNPath, operatorUser) {
		fail("PII leak (H1): db.dsn_path=%q contains operator username=%q", hpData.Data.DB.DSNPath, operatorUser)
	}
	if !contains(hpData.Data.DB.DSNPath, "<USER>") {
		fail("PII redaction (H1): expected <USER> token in dsn_path; got %q", hpData.Data.DB.DSNPath)
	}
	ok("dark_memory_health_ping: v%s coexistence=%s driver=%s schema=v%d canonical=%d extras=%d latency=%.2fms dsn_redacted=%t",
		hpData.Data.Server.Version,
		hpData.Data.Server.CoexistenceGroup,
		hpData.Data.DB.Driver,
		hpData.Data.DB.SchemaVersion,
		hpData.Data.Registry.CanonicalTools,
		hpData.Data.Registry.ExtraTools,
		hpData.Data.LatencyMS,
		contains(hpData.Data.DB.DSNPath, "<USER>"),
	)

	// Phase 3: dark_memory_memory_state (the canonical diagnostic).
	msResp := rpc(4, "tools/call", map[string]any{
		"name": "dark_memory_memory_state", "arguments": map[string]any{},
	})
	msText, err := unwrap(msResp)
	if err != nil {
		fail("memory_state unwrap: %v\n  raw=%s", err, msResp)
	}
	var msData struct {
		Data struct {
			Driver        string `json:"driver"`
			SchemaVersion int    `json:"schema_version"`
			Tables        []string `json:"tables"`
			Counts        struct {
				RunsTotal int `json:"runs_total"`
			} `json:"counts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(msText), &msData); err != nil {
		fail("memory_state body: %v\n  body=%s", err, msText)
	}
	if msData.Data.Driver != "sqlite" {
		fail("memory_state driver=%q; want sqlite", msData.Data.Driver)
	}
	if msData.Data.SchemaVersion != hpData.Data.DB.SchemaVersion {
		fail("memory_state schema_version=%d != health_ping schema_version=%d (inconsistent)",
			msData.Data.SchemaVersion, hpData.Data.DB.SchemaVersion)
	}
	if len(msData.Data.Tables) < 10 {
		fail("memory_state tables=%d; want >=10", len(msData.Data.Tables))
	}
	ok("dark_memory_memory_state: driver=%s schema=v%d tables=%d runs_total=%d",
		msData.Data.Driver, msData.Data.SchemaVersion, len(msData.Data.Tables), msData.Data.Counts.RunsTotal)

	// Phase 4: dark_memory_redteam_list_mods (proves armed-mode extras work).
	rtResp := rpc(5, "tools/call", map[string]any{
		"name": "dark_memory_redteam_list_mods", "arguments": map[string]any{},
	})
	rtText, err := unwrap(rtResp)
	if err != nil {
		fail("redteam_list_mods unwrap: %v\n  raw=%s", err, rtResp)
	}
	var rtData struct {
		Data struct {
			Mods []struct {
				ModID string `json:"mod_id"`
				Path  string `json:"path"`
			} `json:"mods"`
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(rtText), &rtData); err != nil {
		fail("redteam_list_mods body: %v\n  body=%s", err, rtText)
	}
	if rtData.Data.Count == 0 {
		fail("redteam_list_mods count=0; DARK_REDTEAM=armed should expose >=1 mod")
	}
	ok("dark_memory_redteam_list_mods: %d mods loaded", rtData.Data.Count)

	// Phase 5: dark_memory_session_start (write path — touches audit).
	ssResp := rpc(6, "tools/call", map[string]any{
		"name": "dark_memory_session_start",
		"arguments": map[string]any{
			"operator":   "e2e-probe",
			"project_id": "default",
		},
	})
	ssText, err := unwrap(ssResp)
	if err != nil {
		fail("session_start unwrap: %v\n  raw=%s", err, ssResp)
	}
	var ssData struct {
		Data struct {
			SessionID string `json:"session_id"`
			ProjectID string `json:"project_id"`
			StartedAt string `json:"started_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(ssText), &ssData); err != nil {
		fail("session_start body: %v\n  body=%s", err, ssText)
	}
	if !startsWith(ssData.Data.SessionID, "sess-") {
		fail("session_start returned session_id=%q; want sess-* prefix", ssData.Data.SessionID)
	}
	// Note: the wire response does not surface an AuditRef for
	// session_start (the orchestrator delegates write_audit emission
	// to SaveSession per INV-1; we don't double-emit at the
	// orchestrator layer). INV-1 is verified by the session row
	// being created AND the audit row being visible via
	// dark_memory_writes which we exercise in a separate test.
	ok("dark_memory_session_start: session=%s project=%s started_at=%s",
		ssData.Data.SessionID, ssData.Data.ProjectID, ssData.Data.StartedAt)

	// Phase 6: close the session we just opened (cleanup).
	closeResp := rpc(7, "tools/call", map[string]any{
		"name":      "dark_memory_session_close",
		"arguments": map[string]any{"session_id": ssData.Data.SessionID},
	})
	_, err = unwrap(closeResp)
	if err != nil {
		fail("session_close unwrap: %v\n  raw=%s", err, closeResp)
	}
	ok("dark_memory_session_close: session=%s closed cleanly", ssData.Data.SessionID)

	fmt.Println("\n=== E2E COMPLETE — all phases green ===")
}

// helpers

func ok(format string, args ...any) {
	fmt.Println("✓ " + fmt.Sprintf(format, args...))
}

func fail(format string, args ...any) {
	fmt.Fprintln(os.Stderr, "✗ "+fmt.Sprintf(format, args...))
	os.Exit(1)
}

func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	return indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

var _ = filepath.Join

func startsWithEnv(e, prefix string) bool {
	return len(e) >= len(prefix) && e[:len(prefix)] == prefix
}