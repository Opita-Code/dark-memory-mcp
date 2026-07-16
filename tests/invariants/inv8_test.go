package invariants_test

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/server"
)

// TestServer_DefaultDSN_DoesNotCollideWithDarkResearch_INV8 enforces
// INV-8: dark-memory-mcp's defaultDB must not be dark.db (the file
// dark-research-mcp owns). Sharing that file produced v1.2.2 boot
// crashes from schema_migrations name collisions. The defaultDSN
// returns a project-specific filename; operators can override via
// DARK_DB= but the default must keep MCPs isolated.
func TestServer_DefaultDSN_DoesNotCollideWithDarkResearch_INV8(t *testing.T) {
	dsn := server.DefaultDSN()
	if strings.Contains(dsn, "dark-research") {
		t.Fatalf("INV-8 violated: dark-memory-mcp defaultDSN=%q must not reference 'dark-research'", dsn)
	}
	// Must differ from the historical shared-DB value used by
	// dark-research-mcp pre-v1.2.2.
	if dsn == "dark.db" {
		t.Fatalf("INV-8 violated: defaultDSN=%q — sharing 'dark.db' with dark-research-mcp is forbidden", dsn)
	}
	// Sanity check: project-specific substring is present so we
	// know the new default is intentional and not a degenerate
	// empty string.
	if !strings.Contains(dsn, "dark-memory") {
		t.Fatalf("INV-8 violated: defaultDSN=%q must contain 'dark-memory' substring to make isolation explicit", dsn)
	}
	t.Logf("INV-8 verified: defaultDSN=%q isolated from dark-research-mcp", dsn)
}


