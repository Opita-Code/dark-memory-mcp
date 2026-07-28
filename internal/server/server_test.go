// server_test.go — cx.v3 conformance tests for dark-memory-mcp.
//
// Per BRIDGE_AND_COEXISTENCE.md v2.0.0 §5.4 test #4:
// "initialize from dark-memory-mcp declares coexistence_group=
// dark-agents/memory and policy_gateway=true".
//
// mcp-go v0.56.0's MCPServer stores `instructions` as an unexported
// field, so we cannot inspect the wire response directly. We assert
// the format string returned by the exported BuildInstructions helper,
// which is what the MCP server bakes into the initialize envelope.
package server

import (
	"strings"
	"testing"
)

// TestBuildInstructions_DeclaresCoexistenceGroup verifies that the
// initialize envelope includes the cx.v3 coexistence_group.
func TestBuildInstructions_DeclaresCoexistenceGroup(t *testing.T) {
	got := BuildInstructions("dark-agents/memory", "0.0.0-test")
	want := "coexistence_group=dark-agents/memory"
	if !strings.Contains(got, want) {
		t.Errorf("BuildInstructions missing %q\n  got: %s", want, got)
	}
}

// TestBuildInstructions_DeclaresPolicyGateway verifies that the
// initialize envelope declares policy_gateway=true (dark-memory IS
// the gateway — see BRIDGE §2.1).
func TestBuildInstructions_DeclaresPolicyGateway(t *testing.T) {
	got := BuildInstructions("dark-agents/memory", "0.0.0-test")
	want := "policy_gateway=true"
	if !strings.Contains(got, want) {
		t.Errorf("BuildInstructions missing %q\n  got: %s", want, got)
	}

	// Defensive: assert that dark-memory does NOT advertise itself
	// with policy_gateway=false (would mean we accidentally demoted
	// ourselves to a backing, breaking cx.v3 gateway semantics per
	// BRIDGE §3.3). Note: the substring "policy_gateway=false" IS
	// legitimately present in the instructions as a description of
	// the dark-research-mcp backing — so we check the SHAPE around
	// it (it must be preceded by "dark-research-mcp with" or
	// similar), not the raw substring.
	if strings.HasPrefix(got, "dark-memory-mcp server. coexistence_group=dark-agents/memory policy_gateway=false") {
		t.Errorf("BuildInstructions wrongly starts with policy_gateway=false (dark-memory MUST NOT demote itself)\n  got: %s", got)
	}
}

// TestBuildInstructions_StampsCxV3 verifies the BRIDGE spec reference
// (spec 164 bridge.2 cx.v3) is present, so harness implementers
// grepping for the spec version can find it.
func TestBuildInstructions_StampsCxV3(t *testing.T) {
	got := BuildInstructions("dark-agents/memory", "0.0.0-test")
	want := "spec 164 bridge.2 cx.v3"
	if !strings.Contains(got, want) {
		t.Errorf("BuildInstructions missing %q (harness grep target)\n  got: %s", want, got)
	}
}

// TestBuildInstructions_ReferencesBacking ensures the gateway's
// instructions point at dark-research-mcp v0.8.0+ as the backing
// (coexistence_group=dark-agents/research, policy_gateway=false).
// This is what lets the harness know how to find its sibling.
func TestBuildInstructions_ReferencesBacking(t *testing.T) {
	got := BuildInstructions("dark-agents/memory", "0.0.0-test")
	want := "dark-research-mcp with coexistence_group=dark-agents/research, policy_gateway=false"
	if !strings.Contains(got, want) {
		t.Errorf("BuildInstructions missing %q (harness needs the backing's coexistence_group + policy_gateway to know the routing rules)\n  got: %s", want, got)
	}
}

// TestBuildInstructions_IncludesVersion ensures the version string
// the caller passed is reflected in the instructions.
func TestBuildInstructions_IncludesVersion(t *testing.T) {
	const ver = "0.0.0-unittest"
	got := BuildInstructions("dark-agents/memory", ver)
	want := "Version=" + ver
	if !strings.Contains(got, want) {
		t.Errorf("BuildInstructions missing %q\n  got: %s", want, got)
	}
}
