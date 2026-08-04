package wire

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// TestWire_RuntimeToolEnumeration checks the public tool surface.
//
// The contract:
//   - un-armed server: exactly len(tools.CanonicalOrder()) tools
//     (derived — never a hardcoded constant, so the test can't drift
//     from the source of truth in registry.go)
//   - armed server: canonical + redteam extras = canonical + 3
//
// The frozen CONTRACT LIST (explicit names, one per tool) lives in
// tests/conformance/bridge7_mcp_inspector_test.go's canonicalWireOrder().
// This test checks the COUNT against the live binary; that list checks
// the NAMES + ORDER. Both are needed; neither hardcodes a count.
//
// If this test fires, the binary is stale OR the surface changed:
// rebuild the binary first, then update canonicalWireOrder in the
// SAME commit that changed the surface.
func TestWire_RuntimeToolEnumeration(t *testing.T) {
	if os.Getenv("DARK_MEM_MCP_BIN") == "" {
		t.Skip("DARK_MEM_MCP_BIN not set; wire tests need the live binary")
	}

	// Derived from the live source of truth — the wire contract must
	// match the registry in THIS tree, not a frozen magic number.
	wantUnarmed := len(tools.CanonicalOrder())
	wantArmed := wantUnarmed + 3 // redteam extras (armed-mode only)

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
	if got != wantUnarmed && got != wantArmed {
		t.Fatalf("contract: tools/list returns %d tools, want %d (un-armed) or %d (armed)", got, wantUnarmed, wantArmed)
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
