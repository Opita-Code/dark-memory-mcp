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
//   - un-armed server: exactly 29 tools (28 from v1.3.x + 1 recall
//     added in v2.0.0 — pivot wave 5A.ii.b.2.c)
//   - armed server:    29 + 3 redteam extras = 32
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
	const wantUnarmed = 29
	const wantArmed = 32
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
