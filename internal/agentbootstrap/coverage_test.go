// Package agentbootstrap — coverage_test.go: closes the Phase 6
// Chunk 6.4 mutation gap.
//
// Targets (untested functions):
//   - clientinfo.GlobalStoreForTest
//   - instructions.CrossFeatureHints
//   - resources.TotalResources
//   - resources.RegisterAll (nil-server error + valid-server paths)
package agentbootstrap_test

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentbootstrap"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// === clientinfo ========================================================

func TestGlobalStoreForTest_ReturnsNonNil(t *testing.T) {
	gs := agentbootstrap.GlobalStoreForTest()
	if gs == nil {
		t.Fatal("GlobalStoreForTest returned nil")
	}
}

// === instructions ======================================================

func TestCrossFeatureHints_NonEmpty(t *testing.T) {
	hints := agentbootstrap.CrossFeatureHints("3.0.0-docfix")
	if hints == "" {
		t.Fatal("CrossFeatureHints returned empty string")
	}
	if !strings.Contains(hints, "3.0.0-docfix") {
		t.Errorf("CrossFeatureHints missing version: %q", hints)
	}
	if !strings.Contains(hints, "dark-memory-mcp") {
		t.Errorf("CrossFeatureHints missing project name: %q", hints)
	}
	if !strings.Contains(hints, "dark-research-mcp") {
		t.Errorf("CrossFeatureHints missing companion mention: %q", hints)
	}
}

// === resources =========================================================

func TestTotalResources_Stable(t *testing.T) {
	// Pure sum of 3 package-level vars. Assert > 0 and stable across calls.
	first := agentbootstrap.TotalResources()
	second := agentbootstrap.TotalResources()
	if first <= 0 {
		t.Errorf("TotalResources = %d; want > 0", first)
	}
	if first != second {
		t.Errorf("TotalResources not stable: %d vs %d", first, second)
	}
}

func TestRegisterAll_NilServer(t *testing.T) {
	err := agentbootstrap.RegisterAll(nil, agentbootstrap.BootstrapData{
		Version: "test",
	})
	if err == nil {
		t.Fatal("RegisterAll(nil) should return error")
	}
}

func TestRegisterAll_ValidServer(t *testing.T) {
	srv := mcpserver.NewMCPServer("test-server", "v0.0.1")
	err := agentbootstrap.RegisterAll(srv, agentbootstrap.BootstrapData{
		Version: "test-version",
	})
	if err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
}