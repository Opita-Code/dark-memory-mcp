// Package tools_test covers the AGENT_BOOTSTRAP namespace end-to-end:
// each test calls the tool's Handler directly (BindSimple), bypassing
// the orchestrator and the store. The 3 tools are pure functions over
// the embedded bootstrap fs + the global clientInfo store; no Store
// or Orchestrator is needed.
//
// What this verifies:
//
//   - dark_memory_agent_bootstrap: reads the requested resource(s)
//     from the embedded fs (or override env var) and returns the
//     content map. Validates surface enum and target enum.
//   - dark_memory_agent_recommend_companions: reads the global
//     clientInfo store and returns structured companion advice.
//   - dark_memory_agent_detect_environment: returns what the MCP can
//     infer about the harness's runtime.
//
// Drift contract: when new surfaces / targets are added, this test
// must be updated to exercise them. When the canonical companion
// list changes, recommend_companions_test must be updated.
package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentbootstrap"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
	"github.com/mark3labs/mcp-go/mcp"
)

// callTool is a generic helper that round-trips a raw JSON payload
// through any tool's Handler and returns the decoded ToolResponse.
func callTool(t *testing.T, reg *tools.Registry, name string, payload string) *tools.ToolResponse {
	t.Helper()
	tool := reg.Get(name)
	if tool == nil {
		t.Fatalf("%s not registered", name)
	}
	resp, err := tool.Handler(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("%s handler returned error envelope: %v", name, err)
	}
	return resp
}

// newAgentBootstrapRegistry returns a fresh registry with the 3
// AGENT_BOOTSTRAP tools wired up. No Store, no Orchestrator — these
// are pure functions.
func newAgentBootstrapRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	tools.RegisterAgentBootstrap(reg)
	return reg
}

// ---------------------------------------------------------------------------
// dark_memory_agent_bootstrap tests
// ---------------------------------------------------------------------------

func TestAgentBootstrap_SystemPrompt(t *testing.T) {
	t.Setenv(agentbootstrap.EnvOverride, "")
	reg := newAgentBootstrapRegistry()

	resp := callTool(t, reg, "agent_bootstrap", `{"surface":"system_prompt"}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("response error: %+v", resp)
	}
	out, ok := resp.Data.(tools.AgentBootstrapOutput)
	if !ok {
		t.Fatalf("response.Data is not AgentBootstrapOutput: %T", resp.Data)
	}
	if out.Surface != "system_prompt" {
		t.Errorf("Surface = %q, want %q", out.Surface, "system_prompt")
	}
	if got, ok := out.Content["system_prompt"]; !ok || len(got) == 0 {
		t.Error("expected non-empty system_prompt content")
	}
	if out.Source != "embedded" {
		t.Errorf("Source = %q, want %q", out.Source, "embedded")
	}
}

func TestAgentBootstrap_CompatibilityMatrix(t *testing.T) {
	t.Setenv(agentbootstrap.EnvOverride, "")
	reg := newAgentBootstrapRegistry()

	resp := callTool(t, reg, "agent_bootstrap", `{"surface":"compatibility_matrix"}`)
	out := resp.Data.(tools.AgentBootstrapOutput)
	if out.Surface != "compatibility_matrix" {
		t.Errorf("Surface = %q, want %q", out.Surface, "compatibility_matrix")
	}
	if _, ok := out.Content["compatibility_matrix"]; !ok {
		t.Error("expected compatibility_matrix content key")
	}
}

func TestAgentBootstrap_InstallGuide(t *testing.T) {
	t.Setenv(agentbootstrap.EnvOverride, "")
	reg := newAgentBootstrapRegistry()

	for _, client := range agentbootstrap.InstallClients {
		t.Run(client, func(t *testing.T) {
			payload := `{"surface":"install_guide","target":"` + client + `"}`
			resp := callTool(t, reg, "agent_bootstrap", payload)
			out := resp.Data.(tools.AgentBootstrapOutput)
			if out.Target != client {
				t.Errorf("Target = %q, want %q", out.Target, client)
			}
			if _, ok := out.Content["install_guide"]; !ok {
				t.Errorf("missing install_guide content key for %s", client)
			}
		})
	}
}

func TestAgentBootstrap_Companion(t *testing.T) {
	t.Setenv(agentbootstrap.EnvOverride, "")
	reg := newAgentBootstrapRegistry()

	for _, tool := range agentbootstrap.CompanionTools {
		t.Run(tool, func(t *testing.T) {
			payload := `{"surface":"companion","target":"` + tool + `"}`
			resp := callTool(t, reg, "agent_bootstrap", payload)
			out := resp.Data.(tools.AgentBootstrapOutput)
			if out.Target != tool {
				t.Errorf("Target = %q, want %q", out.Target, tool)
			}
			if _, ok := out.Content["companion"]; !ok {
				t.Errorf("missing companion content key for %s", tool)
			}
		})
	}
}

func TestAgentBootstrap_All(t *testing.T) {
	t.Setenv(agentbootstrap.EnvOverride, "")
	reg := newAgentBootstrapRegistry()

	resp := callTool(t, reg, "agent_bootstrap", `{"surface":"all"}`)
	out := resp.Data.(tools.AgentBootstrapOutput)
	if out.Surface != "all" {
		t.Errorf("Surface = %q, want %q", out.Surface, "all")
	}
	// "all" should include every surface.
	wantKeys := []string{
		"system_prompt",
		"compatibility_matrix",
	}
	for _, k := range wantKeys {
		if _, ok := out.Content[k]; !ok {
			t.Errorf("missing content key %q in 'all' surface", k)
		}
	}
	// Plus every install_guide_<client> and companion_<name>.
	for _, c := range agentbootstrap.InstallClients {
		if _, ok := out.Content["install_guide_"+c]; !ok {
			t.Errorf("missing install_guide_%s in 'all' surface", c)
		}
	}
	for _, c := range agentbootstrap.CompanionTools {
		if _, ok := out.Content["companion_"+c]; !ok {
			t.Errorf("missing companion_%s in 'all' surface", c)
		}
	}
}

func TestAgentBootstrap_InvalidSurface(t *testing.T) {
	reg := newAgentBootstrapRegistry()
	resp, err := reg.Get("agent_bootstrap").Handler(context.Background(), json.RawMessage(`{"surface":"invalid"}`))
	if err == nil {
		t.Fatalf("expected error for invalid surface, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "invalid surface") {
		t.Errorf("error should mention 'invalid surface'; got %q", err.Error())
	}
}

func TestAgentBootstrap_InstallGuide_RequiresTarget(t *testing.T) {
	reg := newAgentBootstrapRegistry()
	_, err := reg.Get("agent_bootstrap").Handler(context.Background(), json.RawMessage(`{"surface":"install_guide"}`))
	if err == nil {
		t.Fatal("expected error when install_guide is missing target")
	}
	if !strings.Contains(err.Error(), "requires target") {
		t.Errorf("error should mention 'requires target'; got %q", err.Error())
	}
}

func TestAgentBootstrap_InstallGuide_InvalidTarget(t *testing.T) {
	reg := newAgentBootstrapRegistry()
	_, err := reg.Get("agent_bootstrap").Handler(context.Background(), json.RawMessage(`{"surface":"install_guide","target":"vim"}`))
	if err == nil {
		t.Fatal("expected error for invalid install_guide target")
	}
	if !strings.Contains(err.Error(), "vim") {
		t.Errorf("error should mention invalid target; got %q", err.Error())
	}
}

func TestAgentBootstrap_ForceEmbedded(t *testing.T) {
	// Even if DARK_AGENT_BOOTSTRAP_DIR is invalid, force_embedded=true
	// must use the embedded copy (no warning).
	t.Setenv(agentbootstrap.EnvOverride, "/nonexistent/path/that/never/exists")
	reg := newAgentBootstrapRegistry()

	resp := callTool(t, reg, "agent_bootstrap", `{"surface":"system_prompt","force_embedded":true}`)
	out := resp.Data.(tools.AgentBootstrapOutput)
	if out.Source != "embedded" {
		t.Errorf("Source = %q, want %q", out.Source, "embedded")
	}
	if out.Warning != "" {
		t.Errorf("Warning = %q, want empty (force_embedded should bypass warning)", out.Warning)
	}
}

func TestAgentBootstrap_InvalidOverride_SetsWarning(t *testing.T) {
	// Override env var points to nonexistent dir; tool should
	// fall back to embedded and set a warning.
	t.Setenv(agentbootstrap.EnvOverride, "/nonexistent/path/that/never/exists")
	reg := newAgentBootstrapRegistry()

	resp := callTool(t, reg, "agent_bootstrap", `{"surface":"system_prompt"}`)
	out := resp.Data.(tools.AgentBootstrapOutput)
	if out.Source != "embedded" {
		t.Errorf("Source = %q, want %q (fallback to embedded)", out.Source, "embedded")
	}
	if out.Warning == "" {
		t.Error("expected warning when override invalid")
	}
}

// ---------------------------------------------------------------------------
// dark_memory_agent_recommend_companions tests
// ---------------------------------------------------------------------------

func TestRecommendCompanions_NoClientInfo(t *testing.T) {
	// When no client has connected, the tool still returns
	// a valid response with harness.canonical="unknown".
	reg := newAgentBootstrapRegistry()
	resp := callTool(t, reg, "agent_recommend_companions", `{}`)
	if resp.Error != nil {
		t.Fatalf("response error: %+v", resp.Error)
	}
	out, ok := resp.Data.(tools.RecommendCompanionsOutput)
	if !ok {
		t.Fatalf("response.Data is not RecommendCompanionsOutput: %T", resp.Data)
	}
	if out.Harness.Canonical != "unknown" {
		t.Errorf("harness.canonical = %q, want %q", out.Harness.Canonical, "unknown")
	}
	if len(out.CompanionsPresent) != 1 || out.CompanionsPresent[0] != "dark-memory" {
		t.Errorf("companions_present = %v, want [dark-memory]", out.CompanionsPresent)
	}
	if len(out.CompanionsMissing) != 2 {
		t.Errorf("companions_missing = %v, want 2 companions", out.CompanionsMissing)
	}
	if len(out.Recommendations) != 2 {
		t.Errorf("recommendations = %v, want 2 entries", out.Recommendations)
	}
	if len(out.Limitations) == 0 {
		t.Error("limitations should be non-empty")
	}
}

func TestRecommendCompanions_WithClientInfo(t *testing.T) {
	// Manually set the global store to simulate a Claude Desktop client.
	agentbootstrap.GlobalStoreForTest().SetFromInitialize(&mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo: mcp.Implementation{
				Name: "claude-desktop", Title: "Claude Desktop", Version: "1.0.0",
			},
		},
	})

	reg := newAgentBootstrapRegistry()
	resp := callTool(t, reg, "agent_recommend_companions", `{}`)
	out := resp.Data.(tools.RecommendCompanionsOutput)

	if out.Harness.Name != "claude-desktop" {
		t.Errorf("harness.name = %q, want %q", out.Harness.Name, "claude-desktop")
	}
	if out.Harness.Canonical != "claude-desktop" {
		t.Errorf("harness.canonical = %q, want %q", out.Harness.Canonical, "claude-desktop")
	}
	if out.Harness.SpecDetected != "2025-06-18" {
		t.Errorf("harness.spec_detected = %q, want %q", out.Harness.SpecDetected, "2025-06-18")
	}
	if out.Harness.Source != "initialize.clientInfo" {
		t.Errorf("harness.client_info_source = %q, want %q", out.Harness.Source, "initialize.clientInfo")
	}

	// Each recommendation should have non-empty rationale + install snippet + docs URI.
	for _, rec := range out.Recommendations {
		if rec.Rationale == "" {
			t.Errorf("recommendation %q: empty rationale", rec.Name)
		}
		if rec.InstallSnippet == "" {
			t.Errorf("recommendation %q: empty install snippet", rec.Name)
		}
		if rec.DocsURI == "" {
			t.Errorf("recommendation %q: empty docs URI", rec.Name)
		}
		if !strings.HasPrefix(rec.DocsURI, agentbootstrap.URIPrefix) {
			t.Errorf("recommendation %q: docs URI %q should start with %q",
				rec.Name, rec.DocsURI, agentbootstrap.URIPrefix)
		}
	}
}

func TestRecommendCompanions_NewSpecSource(t *testing.T) {
	// Verify the new-spec source label shows up correctly.
	agentbootstrap.GlobalStoreForTest().SetFromMeta(&mcp.Meta{
		AdditionalFields: map[string]any{
			agentbootstrap.MetaClientInfoKey: mcp.Implementation{
				Name: "opencode", Title: "opencode", Version: "1.0.5",
			},
		},
	})

	reg := newAgentBootstrapRegistry()
	resp := callTool(t, reg, "agent_recommend_companions", `{}`)
	out := resp.Data.(tools.RecommendCompanionsOutput)

	if out.Harness.Source != "_meta.clientInfo" {
		t.Errorf("harness.client_info_source = %q, want %q", out.Harness.Source, "_meta.clientInfo")
	}
	if out.Harness.SpecDetected != "2026-07-28" {
		t.Errorf("harness.spec_detected = %q, want %q", out.Harness.SpecDetected, "2026-07-28")
	}
	if out.Harness.Canonical != "opencode" {
		t.Errorf("harness.canonical = %q, want %q", out.Harness.Canonical, "opencode")
	}
}

// ---------------------------------------------------------------------------
// dark_memory_agent_detect_environment tests
// ---------------------------------------------------------------------------

func TestDetectEnvironment_NoClientInfo(t *testing.T) {
	// Make sure the store has no recent clientInfo.
	agentbootstrap.GlobalStoreForTest().ClearForTest()

	reg := newAgentBootstrapRegistry()
	resp := callTool(t, reg, "agent_detect_environment", `{}`)
	out := resp.Data.(tools.DetectEnvironmentOutput)

	if out.ClientInfoSource != "none" {
		t.Errorf("client_info_source = %q, want %q", out.ClientInfoSource, "none")
	}
	if out.SpecVersionDetected != "unknown" {
		t.Errorf("spec_version_detected = %q, want %q", out.SpecVersionDetected, "unknown")
	}
	if out.Transport != "stdio" {
		t.Errorf("transport = %q, want %q", out.Transport, "stdio")
	}
	if out.Server.Name != "dark-memory-mcp" {
		t.Errorf("server.name = %q, want %q", out.Server.Name, "dark-memory-mcp")
	}
	if out.Server.ToolsTotal != len(tools.CanonicalOrder()) {
		t.Errorf("server.tools_total = %d, want %d", out.Server.ToolsTotal, len(tools.CanonicalOrder()))
	}
	if out.Server.ResourcesTotal != agentbootstrap.TotalResources() {
		t.Errorf("server.resources_total = %d, want %d", out.Server.ResourcesTotal, agentbootstrap.TotalResources())
	}
	if out.Server.SchemaVersion != sqlite.CurrentVersion() {
		t.Errorf("server.schema_version = %d, want %d (sqlite.CurrentVersion())",
			out.Server.SchemaVersion, sqlite.CurrentVersion())
	}
}

func TestDetectEnvironment_WithLegacyClientInfo(t *testing.T) {
	agentbootstrap.GlobalStoreForTest().SetFromInitialize(&mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo: mcp.Implementation{
				Name: "claude-code", Title: "Claude Code", Version: "2.0.0",
			},
		},
	})

	reg := newAgentBootstrapRegistry()
	resp := callTool(t, reg, "agent_detect_environment", `{}`)
	out := resp.Data.(tools.DetectEnvironmentOutput)

	if out.Client.Name != "claude-code" {
		t.Errorf("client.name = %q, want %q", out.Client.Name, "claude-code")
	}
	if out.SpecVersionDetected != "2025-06-18" {
		t.Errorf("spec_version_detected = %q, want %q", out.SpecVersionDetected, "2025-06-18")
	}
	if out.ClientInfoSource != "initialize.clientInfo" {
		t.Errorf("client_info_source = %q, want %q", out.ClientInfoSource, "initialize.clientInfo")
	}
}

func TestDetectEnvironment_NegotiatedCapabilities(t *testing.T) {
	out := runDetectEnvironment(t)
	// resources and tools MUST be true (we ARE a server with
	// resources + tools; the harness must support them per spec).
	if v, ok := out.NegotiatedCapabilities["resources"].(bool); !ok || !v {
		t.Errorf("negotiated_capabilities.resources = %v, want true", out.NegotiatedCapabilities["resources"])
	}
	if v, ok := out.NegotiatedCapabilities["tools"].(bool); !ok || !v {
		t.Errorf("negotiated_capabilities.tools = %v, want true", out.NegotiatedCapabilities["tools"])
	}
}

// runDetectEnvironment is a small helper to deduplicate the boilerplate.
func runDetectEnvironment(t *testing.T) tools.DetectEnvironmentOutput {
	t.Helper()
	reg := newAgentBootstrapRegistry()
	resp := callTool(t, reg, "agent_detect_environment", `{}`)
	return resp.Data.(tools.DetectEnvironmentOutput)
}

// ---------------------------------------------------------------------------
// Registry integration test
// ---------------------------------------------------------------------------

func TestAgentBootstrap_AllThreeToolsRegistered(t *testing.T) {
	reg := newAgentBootstrapRegistry()
	for _, name := range []string{"agent_bootstrap", "agent_recommend_companions", "agent_detect_environment"} {
		if reg.Get(name) == nil {
			t.Errorf("%s not registered in registry", name)
		}
	}
}
