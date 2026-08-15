// Package wire — tool_gate_contract_test.go
//
// THE INTEGRATION CONTRACT (spec 1188 follow-up, 2026-08-15):
//
// Every canonical tool must be reachable through the REAL gate path
// (JSON-RPC wire → GateMiddleware.PreCheck → tool handler). This test
// exists because the LLM_CONFIG tools shipped in spec 1188 were
// registered + unit-tested green (via t.Handler, which BYPASSES the
// gate) but the gate refused them with ErrCapabilityNotGranted —
// DefaultToolGrants had not been updated. The unit tests could not
// catch it; only a wire-level call through the gate could.
//
// Contract per canonical tool (three independent checks):
//   1. present in tools/list (registered on the real wire)
//   2. present in recall.DefaultToolGrants (the gate's default grant
//      set — missing ⇒ ErrCapabilityNotGranted). Checked statically
//      for EVERY tool.
//   3. (light tools) a real tools/call through the gate returns a
//      NON-wiring response — either success or a semantic
//      argument-validation error, BOTH of which prove the gate let
//      the call through.
//
// Heavy tools (judge, mindset, delegate, research backends, VLP
// state machine, publish) are covered by checks 1+2 statically +
// their own unit tests; a smoke call with {} args would hang or
// trigger real LLM/backend work, so it is excluded (documented in
// smokeSkipTools).
package wire

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// smokeToolArgs maps a canonical bare tool name to a minimal
// argument set that passes argument VALIDATION for the wire smoke
// call. Tools absent from this map get {}.
var smokeToolArgs = map[string]map[string]any{
	"project_create":       {"project_id": "contract-test", "display_name": "Contract Test"},
	"session_start":        {"operator": "contract-test", "project_id": "default"},
	"agent_memory_save":    {"operator": "contract-test", "kind": "note", "content": "contract smoke"},
	"agent_memory_recall":  {"operator": "contract-test", "query": "contract"},
	"agent_memory_list":    {"operator": "contract-test"},
	"agent_bootstrap":      {"surface": "system_prompt"},
	"vibe_spec":            {"vibe_case": "C1", "tasks": "[{\"id\":\"T1\",\"description\":\"contract\"}]"},
	"llm_key_add":          {"provider": "deepseek", "key": "sk-contract-test", "validate": false},
	"llm_key_remove":       {"provider": "deepseek"},
	"research_recall":      {"query": "contract"},
	"recall":               {"scope": "session"},
	"health_ping":          {},
	"memory_state":         {},
	"active_policy":        {},
	"llm_key_list":         {},
	"llm_provider_status":  {},
	"agent_detect_environment": {},
	"agent_recommend_companions": {},
	"judge_list_personas":  {},
	"admin_schema_status":  {},
	"embedder_setup_prompt": {},
}

// smokeSkipTools: statically covered (registry + grant + wire presence)
// but NOT smoke-called — a {} smoke call would hang or do heavy work.
var smokeSkipTools = map[string]bool{
	"judge": true, "consensus": true, "mindset_apply": true, "delegate_intent": true,
	"research_topic": true, "research_resume_thread": true, "vibe_publish": true,
	"admin_vacuum": true, "vlp_handle_event": true,
	// session lifecycle: smoke-calling close/resurrect/recover with
	// bogus ids churns the active session or scans the store.
	"session_close": true, "session_resume": true, "session_heartbeat": true,
	"session_recover": true, "session_resurrect": true, "session_status": true,
	// agent_memory writes/reads with missing ids would error semantically
	// but are safer covered statically + unit tests.
	"agent_memory_get": true, "agent_memory_update": true, "agent_memory_archive": true,
	"agent_memory_delegate": true, "agent_memory_entities": true,
	"subagent_register": true, "subagent_unregister": true,
	"pipeline_status": true, "resolve_drift": true,
	"artifact_context": true, "spec_context": true, "session_context": true,
	"judgment_history": true,
	"load_constitution": true, "writes": true, "anomalies": true,
	"error_list": true, "error_get": true, "error_summary": true, "error_resolve": true,
	"admin_migrate": true,
}

// wiringErrorCodes are the gate/wiring failures this test treats as
// integration breakage.
var wiringErrorCodes = []string{
	"ErrCapabilityNotGranted",
	"ErrFrameStaleTooFar",
	"ErrToolNotFound",
	"MethodNotFound",
}

// TestWire_ToolGateContract verifies every canonical tool is
// registered, granted, and (for light tools) reachable through the
// real gate.
func TestWire_ToolGateContract(t *testing.T) {
	if os.Getenv("DARK_MEM_MCP_BIN") == "" {
		t.Skip("DARK_MEM_MCP_BIN not set; wire tests need the live binary")
	}

	// --- check 1+2 (static, EVERY canonical tool) ---
	canonical := tools.CanonicalOrder()
	if len(canonical) == 0 {
		t.Fatal("tools.CanonicalOrder() empty")
	}
	grantSet := map[string]bool{}
	for _, name := range strings.Split(recall.DefaultToolGrants, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			grantSet[name] = true
		}
	}
	var ungranted []string
	for _, name := range canonical {
		if !grantSet[name] {
			ungranted = append(ungranted, name)
		}
	}
	if len(ungranted) > 0 {
		t.Errorf("INTEGRATION BREAK: canonical tools missing from DefaultToolGrants (gate will refuse them): %v", ungranted)
	}

	s := startWireSession(t)

	// tools/list → live wire name set.
	var wireNames map[string]bool = map[string]bool{}
	{
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.nextID(), "method": "tools/list", "params": map[string]any{}})
		if _, err := s.stdin.Write(append(raw, '\n')); err != nil {
			t.Fatalf("write tools/list: %v", err)
		}
		respBytes, err := s.stdout.readOne()
		if err != nil {
			t.Fatalf("read tools/list: %v", err)
		}
		var resp struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			t.Fatalf("tools/list not JSON: %v", err)
		}
		for _, tn := range resp.Result.Tools {
			wireNames[tn.Name] = true
		}
	}

	// Every canonical tool must be on the wire.
	var missingWire []string
	for _, bare := range canonical {
		if !wireNames["dark_memory_"+bare] {
			missingWire = append(missingWire, "dark_memory_"+bare)
		}
	}
	if len(missingWire) > 0 {
		t.Errorf("INTEGRATION BREAK: canonical tools MISSING from wire tools/list: %v", missingWire)
	}

	// Bind a session so session-requiring tools pass PreCheck.
	if out, err := callToolWire(s, "session_start", map[string]any{"operator": "contract-wiring-test", "project_id": "default"}); err != nil {
		t.Fatalf("session_start for contract: %v (body=%s)", err, out)
	}

	// --- check 3 (smoke through the gate, LIGHT tools only) ---
	var wiringFails []string
	var argsErr []string
	for _, bare := range canonical {
		if smokeSkipTools[bare] {
			continue
		}
		args, ok := smokeToolArgs[bare]
		if !ok {
			args = map[string]any{}
		}
		body, err := callToolWire(s, bare, args)
		if err != nil {
			t.Fatalf("tools/call dark_memory_%s: wire error: %v", bare, err)
		}
		if isWiringError(body) {
			wiringFails = append(wiringFails, fmt.Sprintf("dark_memory_%s → %s", bare, summarizeErr(body)))
		} else if isSemanticError(body) {
			argsErr = append(argsErr, fmt.Sprintf("dark_memory_%s → %s", bare, summarizeErr(body)))
		}
	}

	if len(wiringFails) > 0 {
		t.Errorf("INTEGRATION BREAK — %d tool(s) refused by the gate or missing on the wire:\n  %s\n  Fix: add to DefaultToolGrants (internal/recall/assemble.go) or fix wiring.", len(wiringFails), strings.Join(wiringFails, "\n  "))
	}
	if len(argsErr) > 0 {
		fmt.Fprintf(os.Stderr, "note: %d light tool(s) returned semantic validation errors through the gate (expected for minimal args — proves reachability):\n  %s\n", len(argsErr), strings.Join(argsErr, "\n  "))
	}
}

// callToolWire invokes a canonical bare-name tool over the live wire.
func callToolWire(s *wireSession, bareName string, args map[string]any) (string, error) {
	argsJSON, _ := json.Marshal(args)
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      s.nextID(),
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "dark_memory_" + bareName,
			"arguments": json.RawMessage(argsJSON),
		},
	})
	if _, err := s.stdin.Write(append(raw, '\n')); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	respBytes, err := s.stdout.readOne()
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return string(respBytes), nil
}

// isWiringError reports whether a tools/call response body is a
// gate/wiring failure (integration breakage), not a semantic error.
func isWiringError(body string) bool {
	for _, code := range wiringErrorCodes {
		if strings.Contains(body, code) {
			return true
		}
	}
	return false
}

// isSemanticError reports whether the body carries a structured
// argument-validation error (semantic — the gate let the call through).
func isSemanticError(body string) bool {
	return strings.Contains(body, "ErrInvalidArgument") ||
		strings.Contains(body, "ErrInvalidParams") ||
		strings.Contains(body, "invalid") ||
		strings.Contains(body, "missing") ||
		strings.Contains(body, "required") ||
		strings.Contains(body, "not found") ||
		strings.Contains(body, "unknown provider")
}

// summarizeErr extracts a short error fragment from a response body.
func summarizeErr(body string) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	_ = json.Unmarshal([]byte(body), &parsed)
	if parsed.Error.Message != "" {
		return truncate(parsed.Error.Message, 90)
	}
	if len(parsed.Result.Content) > 0 {
		return truncate(parsed.Result.Content[0].Text, 90)
	}
	return truncate(body, 90)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
