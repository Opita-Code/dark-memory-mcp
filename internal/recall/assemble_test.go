// Package recall_test — assemble_test.go: guards the DefaultToolGrants
// invariant (v2.1.2 regression).
//
// DefaultToolGrants must cover every tool in the canonical
// registry order, AND every entry must use BARE names (no
// "dark_memory_" prefix). The gate's HasGrant check is exact-match
// against the bare name from GateInput.ToolName; prefixed entries
// silently fail to grant anything (this was the v2.1.0→v2.1.2 bug).
//
// Uses external test package (recall_test, not recall) so we can
// import internal/tools without an import cycle — internal/tools
// already imports internal/recall for the FrameSource registration.
package recall_test

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// TestDefaultToolGrants_CoversCanonicalOrder verifies that every
// canonical tool name (bare, no wire prefix) is present in
// DefaultToolGrants. This catches both omissions (e.g. forgetting
// to add the 5 agent_memory_* tools when they shipped) and
// typos in either direction.
func TestDefaultToolGrants_CoversCanonicalOrder(t *testing.T) {
	canonical := tools.CanonicalOrder()
	if len(canonical) == 0 {
		t.Fatal("CanonicalOrder returned empty; registry not initialized?")
	}

	granted := parseGrants(recall.DefaultToolGrants)
	grantedSet := make(map[string]bool, len(granted))
	for _, n := range granted {
		grantedSet[n] = true
	}

	// Every canonical tool must be granted.
	var missing []string
	for _, name := range canonical {
		if !grantedSet[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("DefaultToolGrants missing canonical tools: %v\n"+
			"  Add them to internal/recall/assemble.go DefaultToolGrants\n"+
			"  (the registry order is the source of truth: %d tools)",
			missing, len(canonical))
	}

	// Every entry must use a BARE name (no dark_memory_ prefix).
	// The wire prefix is added by the MCP server when registering
	// with mcp-go (see internal/tools/registry.go WirePrefix).
	const wirePrefix = "dark_memory_"
	var prefixed []string
	for _, n := range granted {
		if strings.HasPrefix(n, wirePrefix) {
			prefixed = append(prefixed, n)
		}
	}
	if len(prefixed) > 0 {
		t.Errorf("DefaultToolGrants has wire-prefixed entries: %v\n"+
			"  Strip the 'dark_memory_' prefix; HasGrant is exact-match on bare name.",
			prefixed)
	}
}

// TestDefaultToolGrants_NoDuplicates guards against accidental
// double-entries that would still pass the set comparison but
// bloat the constant.
func TestDefaultToolGrants_NoDuplicates(t *testing.T) {
	grants := parseGrants(recall.DefaultToolGrants)
	seen := make(map[string]int, len(grants))
	for _, n := range grants {
		seen[n]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("DefaultToolGrants has %q listed %d times", name, count)
		}
	}
}

// parseGrants splits the comma-separated DefaultToolGrants into a
// slice of trimmed, non-empty entries. Duplicates of the comma-
// delimiter are tolerated; whitespace around entries is stripped.
func parseGrants(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		out = append(out, n)
	}
	return out
}
