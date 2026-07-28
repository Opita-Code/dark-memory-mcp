// Package tools - agent_bootstrap.go: the AGENT_BOOTSTRAP namespace
// (3 self-bootstrap tools introduced in v2.6.0).
//
// Per v2.6.0 spec:
//
//	dark_memory_agent_bootstrap
//	dark_memory_agent_recommend_companions
//	dark_memory_agent_detect_environment
//
// These tools let any harness discover the dark-memory operating
// manual (canonical + harness-specific), detect missing companion
// MCPs (dark-research, dark-copilot), and inspect what the MCP can
// infer about the harness's runtime (client name, version, transport,
// negotiated spec version).
//
// The 3 tools are READ-ONLY. They never invoke installs, never mutate
// state, never reach into the host filesystem. The non-invasive
// contract holds.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"

	"github.com/dark-agents/dark-memory-mcp/internal/agentbootstrap"
	"github.com/dark-agents/dark-memory-mcp/internal/version"
)

// ---------------------------------------------------------------------------
// AgentBootstrapInput / AgentBootstrapOutput (tool #1)
// ---------------------------------------------------------------------------

// AgentBootstrapInput is the input for dark_memory_agent_bootstrap.
//
// Surface selects which resource group to read:
//
//   - "system_prompt"        -> SYSTEM_PROMPT.md
//   - "compatibility_matrix" -> COMPATIBILITY_MATRIX.md
//   - "install_guide"        -> requires Target (one of InstallClients)
//   - "companion"            -> requires Target (one of CompanionTools)
//   - "all"                  -> returns every resource as a map
//
// Target is required for install_guide and companion surfaces; ignored
// otherwise.
//
// ForceEmbedded bypasses the DARK_AGENT_BOOTSTRAP_DIR override for
// this single call (diagnostics; mostly used by drift tests).
type AgentBootstrapInput struct {
	Surface       string `json:"surface"`
	Target        string `json:"target,omitempty"`
	ForceEmbedded bool   `json:"force_embedded,omitempty"`
}

// AgentBootstrapOutput is the result. Content is a map keyed by
// surface name; when Surface="all", every surface is present. When
// Surface is singular, Content has exactly one entry.
//
// Source is "embedded" or "override" depending on whether the call
// resolved to the embedded fallback or the operator override directory.
//
// Warning is non-empty only when ForceEmbedded=false and the operator
// override was requested but invalid (caller may surface to operator).
type AgentBootstrapOutput struct {
	Surface string            `json:"surface"`
	Target  string            `json:"target,omitempty"`
	Content map[string]string `json:"content"`
	Source  string            `json:"source"`
	Warning string            `json:"warning,omitempty"`
}

// ---------------------------------------------------------------------------
// RecommendCompanionsInput / RecommendCompanionsOutput (tool #2)
// ---------------------------------------------------------------------------

// RecommendCompanionsInput is the input for dark_memory_agent_recommend_companions.
// No arguments; the tool reads from the global clientInfo store.
type RecommendCompanionsInput struct{}

// HarnessInfo is the harness's identifying metadata as captured by
// the agentbootstrap clientInfo reader (see clientinfo.go).
type HarnessInfo struct {
	Name         string `json:"name"`
	Title        string `json:"title,omitempty"`
	Version      string `json:"version,omitempty"`
	Canonical    string `json:"canonical"` // normalized short name (e.g. "claude-desktop")
	SpecDetected string `json:"spec_detected"`
	Source       string `json:"client_info_source"`
}

// CompanionRecommendation describes one missing companion tool plus
// the install snippet and the docs URI the harness can read for
// rationale + setup steps.
type CompanionRecommendation struct {
	Name           string `json:"name"`
	Rationale      string `json:"rationale"`
	InstallSnippet string `json:"install_snippet"`
	DocsURI        string `json:"docs_uri"`
}

// RecommendCompanionsOutput is the result. CompanionsPresent lists
// the companion MCPs the tool could positively detect (today: only
// "dark-memory" because we cannot enumerate peer MCPs at runtime).
// CompanionsMissing lists the MCPs the harness should install for
// full functionality. Recommendations is the structured advice list
// with rationale + install snippet + docs URI per companion.
//
// Limitations is an honest list of constraints: we cannot detect
// peer MCP servers (no federated discovery in the spec), we cannot
// auto-install (non-invasive contract), and the spec version is
// inferred from where the clientInfo arrived (not authoritative).
type RecommendCompanionsOutput struct {
	Harness           HarnessInfo              `json:"harness"`
	CompanionsPresent []string                 `json:"companions_present"`
	CompanionsMissing []string                 `json:"companions_missing"`
	Recommendations   []CompanionRecommendation `json:"recommendations"`
	Limitations       []string                 `json:"limitations"`
}

// ---------------------------------------------------------------------------
// DetectEnvironmentInput / DetectEnvironmentOutput (tool #3)
// ---------------------------------------------------------------------------

// DetectEnvironmentInput is the input for dark_memory_agent_detect_environment.
// No arguments.
type DetectEnvironmentInput struct{}

// ServerInfo describes this MCP server's identity as known at runtime.
type ServerInfo struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	SchemaVersion  int    `json:"schema_version"`
	ToolsTotal     int    `json:"tools_total"`
	ResourcesTotal int    `json:"resources_total"`
}

// DetectEnvironmentOutput is the result. Captures:
//
//   - SpecVersionDetected: which MCP protocol version the harness
//     advertised (2025-06-18 or 2026-07-28). Inferred from where
//     clientInfo came in (handshake = legacy, per-request = new).
//   - ClientInfoSource: literal source ("initialize.clientInfo" or
//     "_meta.clientInfo" or "none").
//   - Client: the captured harness implementation.
//   - NegotiatedCapabilities: what the harness declared it supports.
//     Includes resources/tools/prompts/logging/roots/sampling etc.
//   - Transport: "stdio" today (MCP servers don't currently
//     introspect their own transport from inside; we use the
//     conventional default).
//   - Server: this MCP server's identity.
type DetectEnvironmentOutput struct {
	SpecVersionDetected    string                 `json:"spec_version_detected"`
	ClientInfoSource       string                 `json:"client_info_source"`
	Client                 HarnessInfo            `json:"client"`
	NegotiatedCapabilities map[string]interface{} `json:"negotiated_capabilities"`
	Transport              string                 `json:"transport"`
	Server                 ServerInfo             `json:"server"`
}

// ---------------------------------------------------------------------------
// Companion metadata (used by all 3 tools for recommendation rationale)
// ---------------------------------------------------------------------------

// companionMeta describes one companion MCP that the bootstrap tool
// recommends. Kept private to this file because it is internal data;
// the public shape is CompanionRecommendation (above).
type companionMeta struct {
	Name           string
	Rationale      string
	InstallSnippet string
	ResourcePath   string
}

// knownCompanions is the curated list of companions the MCP recommends.
// The user picked option A (just dark-research + dark-copilot; no
// generic mcpfinder-style fallback). Order is stable for tests.
var knownCompanions = []companionMeta{
	{
		Name:           "dark-research",
		Rationale:      "OSINT backing store. Provides web/academic/code/CVE/DNS/cert/IP/threat/geo/news searches without needing a separate WebFetch in the harness. Use this whenever you need to research a third-party product, version, CVE, IP, or paper.",
		InstallSnippet: "npm install -g @opitacode/dark-research-mcp",
		ResourcePath:   "companions/dark-research.md",
	},
	{
		Name:           "dark-copilot",
		Rationale:      "Real Chromium browser for JS-gated pages, interactive controls, and form submission. WebFetch alone can't handle CAPTCHAs, JS-gated dashboards, or interactive flows. Use this when web fetch fails or you need to click/type/scroll.",
		InstallSnippet: "npm install -g @opitacode/dark-copilot-mcp",
		ResourcePath:   "companions/dark-copilot.md",
	},
}

// ---------------------------------------------------------------------------
// Register function
// ---------------------------------------------------------------------------

// RegisterAgentBootstrap wires the 3 AGENT_BOOTSTRAP tools into the
// registry. Called from internal/tools/register.go's RegisterAll
// function. The exact canonical position is right before VIBE (so
// that bootstrap-aware harnesses can read it before any spec work).
//
// Per the namespace ordering convention (spec 164 bridge.4, spec 193
// Layer 6), we append AGENT_BOOTSTRAP as a NEW canonical namespace:
// PROJECT, SESSION, RESEARCH, AGENT_BOOTSTRAP, VIBE, CONTEXT,
// AGENT_MEMORY, RECALL, JUDGE, POLICY, OBSERVABILITY, ADMIN, VLP.
//
// Yes — bumping the namespace count is technically a contract
// change. But per the v2.6.0 spec the new namespace slots in BEFORE
// VIBE (because bootstrap precedes spec work) and the canonical tool
// order grows from 35 to 38 (35 existing + 3 new). All existing
// positions are preserved (no reordering), so any consumer that
// indexed by name (rather than position) is unaffected. Consumers
// that indexed by position will see a one-position shift starting at
// VIBE; this is the cost of additive change and is documented in
// the release notes.
func RegisterAgentBootstrap(reg *Registry) {
	reg.Add(BindSimple("agent_bootstrap",
		"Return the markdown content of a dark-agent self-bootstrap resource by surface+target. Surfaces: system_prompt | compatibility_matrix | install_guide (requires target) | companion (requires target) | all. The resource URIs are also exposed via resources/read for harnesses that prefer the standard MCP path.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"surface": map[string]any{
					"type":        "string",
					"enum":        []string{"system_prompt", "compatibility_matrix", "install_guide", "companion", "all"},
					"description": "Which resource group to read",
				},
				"target": map[string]any{
					"type":        "string",
					"description": "For install_guide: claude-desktop | claude-code | opencode | cline | cursor | continue. For companion: dark-research | dark-copilot. Required for those surfaces; ignored otherwise.",
				},
				"force_embedded": map[string]any{
					"type":        "boolean",
					"description": "If true, bypass DARK_AGENT_BOOTSTRAP_DIR and use the embedded copy. Diagnostic; mostly for drift tests.",
				},
			},
			"required": []string{"surface"},
		}),
		handlerAgentBootstrap))

	reg.Add(BindSimple("agent_recommend_companions",
		"Reads the harness's clientInfo and returns a structured list of recommended companion MCPs (dark-research, dark-copilot). Read-only: never auto-installs. Use this on first connect to learn what the harness should install for full functionality.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		handlerRecommendCompanions))

	reg.Add(BindSimple("agent_detect_environment",
		"Returns what the MCP can infer about the harness's runtime: spec version negotiated, client name/title/version, source of clientInfo (initialize handshake vs _meta per-request), negotiated capabilities (resources/tools/prompts/logging), transport. Use this when debugging harness compatibility or filing an issue.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		handlerDetectEnvironment))
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

// handlerAgentBootstrap is the implementation of dark_memory_agent_bootstrap.
//
// The flow:
//  1. Decode input (surface, target, force_embedded).
//  2. Validate: target required for install_guide/companion.
//  3. Resolve the bootstrap fs (LoadFS or EmbeddedFS depending on
//     force_embedded).
//  4. Read the requested resource(s) from the fs.
//  5. Return AgentBootstrapOutput with the content map and source.
func handlerAgentBootstrap(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
	var in AgentBootstrapInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("agent_bootstrap: invalid input: %w", err)
	}

	// Validate surface enum.
	switch in.Surface {
	case "system_prompt", "compatibility_matrix", "install_guide", "companion", "all":
	default:
		return nil, fmt.Errorf("agent_bootstrap: invalid surface %q (must be one of: system_prompt | compatibility_matrix | install_guide | companion | all)", in.Surface)
	}

	// Validate target requirement.
	if (in.Surface == "install_guide" || in.Surface == "companion") && in.Target == "" {
		return nil, fmt.Errorf("agent_bootstrap: surface=%q requires target", in.Surface)
	}

	// Validate target enum when supplied.
	if in.Target != "" {
		if in.Surface == "install_guide" {
			if !containsString(agentbootstrap.InstallClients, in.Target) {
				return nil, fmt.Errorf("agent_bootstrap: install_guide target %q not in known clients %v", in.Target, agentbootstrap.InstallClients)
			}
		}
		if in.Surface == "companion" {
			if !containsString(agentbootstrap.CompanionTools, in.Target) {
				return nil, fmt.Errorf("agent_bootstrap: companion target %q not in known companions %v", in.Target, agentbootstrap.CompanionTools)
			}
		}
	}

	// Resolve fs (embedded or override).
	var (
		fsys   fs.FS
		source string
		warn   string
	)
	if in.ForceEmbedded {
		fsys = agentbootstrap.EmbeddedFS()
		source = "embedded"
	} else {
		var err error
		fsys, err = agentbootstrap.LoadFSWithWarn()
		if err != nil {
			// Override was requested but invalid; we still return the
			// embedded content but tell the caller about the warning.
			fsys = agentbootstrap.EmbeddedFS()
			source = "embedded"
			warn = err.Error()
		} else if os.Getenv(agentbootstrap.EnvOverride) != "" {
			source = "override"
		} else {
			source = "embedded"
		}
	}

	// Read the requested resources.
	content := map[string]string{}
	switch in.Surface {
	case "system_prompt":
		body, err := fs.ReadFile(fsys, "SYSTEM_PROMPT.md")
		if err != nil {
			return nil, fmt.Errorf("agent_bootstrap: read SYSTEM_PROMPT.md: %w", err)
		}
		content["system_prompt"] = string(body)

	case "compatibility_matrix":
		body, err := fs.ReadFile(fsys, "COMPATIBILITY_MATRIX.md")
		if err != nil {
			return nil, fmt.Errorf("agent_bootstrap: read COMPATIBILITY_MATRIX.md: %w", err)
		}
		content["compatibility_matrix"] = string(body)

	case "install_guide":
		body, err := fs.ReadFile(fsys, "install/"+in.Target+".md")
		if err != nil {
			return nil, fmt.Errorf("agent_bootstrap: read install/%s.md: %w", in.Target, err)
		}
		content["install_guide"] = string(body)

	case "companion":
		body, err := fs.ReadFile(fsys, "companions/"+in.Target+".md")
		if err != nil {
			return nil, fmt.Errorf("agent_bootstrap: read companions/%s.md: %w", in.Target, err)
		}
		content["companion"] = string(body)

	case "all":
		// Return everything. Order is stable: system_prompt, matrix,
		// then each install guide in the canonical InstallClients
		// order, then each companion doc.
		if body, err := fs.ReadFile(fsys, "SYSTEM_PROMPT.md"); err == nil {
			content["system_prompt"] = string(body)
		}
		if body, err := fs.ReadFile(fsys, "COMPATIBILITY_MATRIX.md"); err == nil {
			content["compatibility_matrix"] = string(body)
		}
		for _, client := range agentbootstrap.InstallClients {
			if body, err := fs.ReadFile(fsys, "install/"+client+".md"); err == nil {
				content["install_guide_"+client] = string(body)
			}
		}
		for _, tool := range agentbootstrap.CompanionTools {
			if body, err := fs.ReadFile(fsys, "companions/"+tool+".md"); err == nil {
				content["companion_"+tool] = string(body)
			}
		}
	}

	out := AgentBootstrapOutput{
		Surface: in.Surface,
		Target:  in.Target,
		Content: content,
		Source:  source,
		Warning: warn,
	}
	_ = ctx
	return &ToolResponse{Data: out}, nil
}

// handlerRecommendCompanions is the implementation of dark_memory_agent_recommend_companions.
//
// The flow:
//  1. Read the current ClientInfoRecord from the agentbootstrap store.
//  2. Build the HarnessInfo with normalized canonical name + spec detection.
//  3. CompanionsPresent is always just ["dark-memory"] because we
//     cannot enumerate peer MCPs at runtime (no federated discovery).
//  4. CompanionsMissing lists every known companion (we recommend
//     installing all of them; the harness decides which to actually
//     pull in based on the rationale + their own needs).
//  5. Recommendations is the structured advice list.
//  6. Limitations is the honest list of constraints.
func handlerRecommendCompanions(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
	rec := agentbootstrap.CurrentClientInfo()

	harness := HarnessInfo{
		Name:         rec.Info.Name,
		Title:        rec.Info.Title,
		Version:      rec.Info.Version,
		Canonical:    agentbootstrap.NormalizeClientName(rec.Info),
		SpecDetected: inferSpecVersion(rec),
		Source:       rec.Source,
	}

	recs := make([]CompanionRecommendation, 0, len(knownCompanions))
	missing := make([]string, 0, len(knownCompanions))
	for _, k := range knownCompanions {
		missing = append(missing, k.Name)
		recs = append(recs, CompanionRecommendation{
			Name:           k.Name,
			Rationale:      k.Rationale,
			InstallSnippet: k.InstallSnippet,
			DocsURI:        agentbootstrap.URIPrefix + k.ResourcePath,
		})
	}

	out := RecommendCompanionsOutput{
		Harness:           harness,
		CompanionsPresent: []string{"dark-memory"},
		CompanionsMissing: missing,
		Recommendations:   recs,
		Limitations: []string{
			"MCP servers cannot enumerate peer servers at runtime (no federated discovery in the MCP spec). The 'companions_present' list reflects only what this server can detect from its own initialize handshake.",
			"This tool does NOT auto-install anything. The operator decides whether to install based on the rationale. The non-invasive contract holds.",
			"Spec version is inferred from where clientInfo arrived (initialize.clientInfo = 2025-06-18; _meta.clientInfo = 2026-07-28). Some 2026-07-28 era harnesses may still send clientInfo in both places; we trust the per-request _meta as the more authoritative source.",
		},
	}
	_ = ctx
	_ = raw
	return &ToolResponse{Data: out}, nil
}

// handlerDetectEnvironment is the implementation of dark_memory_agent_detect_environment.
//
// The flow:
//  1. Read current ClientInfoRecord.
//  2. Build HarnessInfo with normalized name.
//  3. Build ServerInfo (this MCP's identity).
//  4. NegotiatedCapabilities is a synthesized snapshot of what we
//     know about the harness (resources/tools/prompts/logging).
//     Today all 4 default to true because we cannot enumerate the
//     harness's ClientCapabilities; future enhancement could pipe
//     through what the SDK exposes.
//  5. Transport is "stdio" by convention (the SDK doesn't currently
//     introspect its own transport; future enhancement when the
//     2026-07-28 spec lands).
func handlerDetectEnvironment(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
	rec := agentbootstrap.CurrentClientInfo()

	client := HarnessInfo{
		Name:         rec.Info.Name,
		Title:        rec.Info.Title,
		Version:      rec.Info.Version,
		Canonical:    agentbootstrap.NormalizeClientName(rec.Info),
		SpecDetected: inferSpecVersion(rec),
		Source:       rec.Source,
	}

	caps := map[string]interface{}{
		"resources": true, // we ARE a server with resources, so the harness MUST support resources by spec
		"tools":     true, // every MCP harness supports tools by spec
		"prompts":   false,
		"logging":   false,
		"roots":     false, // deprecated in 2026-07-28
		"sampling":  false, // not yet exposed by this server
	}

	out := DetectEnvironmentOutput{
		SpecVersionDetected:    inferSpecVersion(rec),
		ClientInfoSource:       clientInfoSource(rec),
		Client:                 client,
		NegotiatedCapabilities: caps,
		Transport:              "stdio", // SDK limitation; documented
		Server: ServerInfo{
			Name: "dark-memory-mcp",
			// Version is resolved at runtime from the version package
			// (ldflags > buildinfo > "dev"). Never hardcode so drift
			// between this tool's report and the actual binary version
			// is impossible.
			Version: version.Resolve().Version,
			// SchemaVersion is the SQLite migration ledger's current
			// version. Hardcoded for now because there is no exported
			// constant; bumped manually whenever a new migration lands.
			// Follow-up: expose as `migrate.CurrentSchemaVersion()` in
			// a later patch.
			SchemaVersion: 20,
			// ToolsTotal and ResourcesTotal are derived from the live
			// registry + bootstrap resource counts so the report can
			// never drift from the actual server surface.
			ToolsTotal:     len(CanonicalOrder()),
			ResourcesTotal: agentbootstrap.TotalResources(),
		},
	}
	_ = ctx
	_ = raw
	return &ToolResponse{Data: out}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// containsString is a tiny helper because stdlib doesn't have one for
// string slices in a useful form. Avoids the slices.Contains import
// churn in this file (we use it in 3 places).
//
// Note: this is intentionally NOT named `contains` because health_test.go
// declares a `contains` with a different signature (takes []string vs
// string) that would otherwise collide if the test file is loaded
// alongside this one in the same compilation unit.
func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// inferSpecVersion maps the ClientInfoRecord.Source to a spec version
// string. Returns "unknown" when no clientInfo has been observed yet.
func inferSpecVersion(rec agentbootstrap.ClientInfoRecord) string {
	switch {
	case rec.Source == "_meta.clientInfo":
		return "2026-07-28"
	case rec.Source == "initialize.clientInfo":
		return "2025-06-18"
	default:
		return "unknown"
	}
}

// clientInfoSource normalizes the Source field to a stable display
// string. Used by detect_environment when the harness has sent no
// clientInfo (returns "none" instead of an empty string).
func clientInfoSource(rec agentbootstrap.ClientInfoRecord) string {
	if rec.Source == "" {
		return "none"
	}
	return rec.Source
}

// -----------------------------------------------------------------------------
// Note on the Store parameter
// -----------------------------------------------------------------------------
//
// The 3 agent-bootstrap tools are READ-ONLY and need no Store, Orchestrator,
// or SafetyHolder. They are pure functions over the global clientInfo store
// + the embedded/override fs. The signature of RegisterAgentBootstrap does
// NOT take these arguments, which is intentional: keeping the surface
// minimal makes the non-invasive contract obvious.
//
// If a future tool in this namespace needs Store, follow the pattern of
// admin.go (RegisterAdmin(reg, _ /* orch */ interface{}, st store.Store)).
// -----------------------------------------------------------------------------

// (no further code; the tools are registered in RegisterAll.)
