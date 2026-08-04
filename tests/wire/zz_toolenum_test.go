package wire

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// TestWire_RuntimeToolEnumeration freezes the public tool surface.
//
// The contract is:
//   - un-armed server: exactly 49 tools (v2.0.0 29 + v2.1.0 5 +
//     v2.3.0 1 + v2.6.0 3 AGENT_BOOTSTRAP + v2.7.0-alpha 1 MINDSET +
//     v2.8.0-alpha 2 subagent + v2.9.0 2 entities+embedder + v2.9.3 1
//     delegate + v2.10.0 1 DELEGATION + v2.11.0 4 ERROR_OBS)
//   - armed server:    49 + 3 redteam extras = 52
//
// If this test fires, the contract changed and README.md +
// DECISION_MATRIX.md + CONTRIBUTING.md must be updated in the SAME
// commit.
func TestWire_RuntimeToolEnumeration(t *testing.T) {
	if os.Getenv("DARK_MEM_MCP_BIN") == "" {
		t.Skip("DARK_MEM_MCP_BIN not set; wire tests need the live binary")
	}

	s := startWireSession(t)
	// do NOT defer close - startWireSession registers t.Cleanup.

	// Send tools/list and read the raw reply.
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      s.nextID(),
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	if _, err := s.stdin.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write tools/list: %v", err)
	}
	respBytes, err := s.stdout.readOne()
	if err != nil {
		t.Fatalf("read tools/list reply: %v", err)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("tools/list body not JSON: %v\n  body=%s", err, respBytes)
	}
	got := len(resp.Result.Tools)
	const wantUnarmed = 49
	const wantArmed = 52
	if got != wantUnarmed && got != wantArmed {
		t.Fatalf("contract: tools/list returns %d tools, frozen at %d (un-armed) or %d (armed)", got, wantUnarmed, wantArmed)
	}

	has_redteam := got == wantArmed
	has_health := false
	for _, tn := range resp.Result.Tools {
		if tn.Name == "dark_memory_health_ping" {
			has_health = true
		}
	}
	if !has_health {
		t.Fatalf("dark_memory_health_ping missing from tools/list -- ops have no liveness probe")
	}

	fmt.Fprintf(os.Stderr, "=== runtime tools/list returned %d tools (health present, redteam=%t) ===\n", got, has_redteam)
	for i, tn := range resp.Result.Tools {
		fmt.Fprintf(os.Stderr, "  [%02d] %s\n", i, tn.Name)
	}
}
