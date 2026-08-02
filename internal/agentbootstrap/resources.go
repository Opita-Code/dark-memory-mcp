// Package agentbootstrap - resources.go: MCP resource registration
// for the dark-agent self-bootstrap content.
//
// What this does:
//
//   - Registers 2 fixed-URI resources (system-prompt.md,
//     compatibility-matrix.md) plus 6 install/* URI templates plus
//     2 companions/* URI templates for a total of 10 resource slots.
//   - Each resource is served from the bootstrap filesystem returned
//     by LoadFS (embedded fallback, or operator override rooted at
//     $DARK_AGENT_BOOTSTRAP_DIR).
//   - Every resource has annotations audience=[assistant] priority=0.9
//     so harnesses that honor the spec will auto-include the bootstrap
//     content in the assistant's context.
//
// Why these specific URIs:
//
//   - dark-memory:// scheme (not file://, https://, or a registered MCP
//     URI prefix): a non-routable scheme makes the URI clearly belong
//     to this MCP, avoiding confusion with public web URLs.
//   - /docs/ path: signals the content is documentation/operating
//     manual, not a runtime data resource (DB rows, API responses, etc.).
//   - .md extension: signals the content is markdown text, MIME
//     type text/markdown in the metadata so harnesses render correctly.
//
// Why we expose these as Resources (not just tools):
//
//   - Per the MCP blog (modelcontextprotocol.io/posts/2025-11-03-using-server-instructions),
//     server instructions have unreliable support across harnesses:
//     opencode (#32856) discards them; some harnesses do honor them.
//   - Resources are the canonical path: harnesses can list them via
//     resources/list and read them via resources/read. This works
//     uniformly across all MCP-native harnesses.
//   - We also expose a tool (dark_memory_agent_bootstrap) that wraps
//     resources/read, so harnesses that don't auto-expose resources
//     can still fetch the content programmatically.
package agentbootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// URIScheme is the non-routable URI scheme we use for all dark-agent
// bootstrap resources. Distinct from https:// (no real network lookup)
// and from file:// (no filesystem coupling). The dark-memory:// scheme
// is the canonical "this URI is owned by the dark-memory MCP" marker.
const URIScheme = "dark-memory"

// URIPrefix is the path prefix for all dark-agent bootstrap resources.
// Resources always live under dark-memory://docs/...
const URIPrefix = "dark-memory://docs/"

// AudienceAssistant is the audience value we put on every resource's
// annotations. The MCP spec defines audience as a list of Roles that
// should receive the resource; we want only the assistant (LLM) to
// see the bootstrap content, not the human user.
var AudienceAssistant = []mcp.Role{mcp.RoleAssistant}

// PriorityHigh is the priority we put on every resource's annotations.
// 1.0 is the maximum per the spec; we use 0.9 so the harness can
// still demote the resource if its own context budget is tight.
const PriorityHigh = 0.9

// InstallClients is the canonical list of harnesses for which we ship
// an install guide. Order is stable for tests; do not reorder without
// updating install/ doc cross-references.
var InstallClients = []string{
	"claude-desktop",
	"claude-code",
	"opencode",
	"cline",
	"cursor",
	"continue",
}

// CompanionTools is the canonical list of companion MCPs we ship
// companion docs for. Order is stable for tests.
var CompanionTools = []string{
	"dark-research",
	"[FUTURE-MCP-N]",
}

// TotalResources returns the total number of resources + resource
// templates registered by RegisterAll. It is the sum of:
//
//   - fixedResources (currently 2: SYSTEM_PROMPT + COMPATIBILITY_MATRIX)
//   - InstallClients templates (one per harness; currently 6)
//   - CompanionTools templates (one per companion MCP; currently 2)
//
// Exposed so the AGENT_BOOTSTRAP tools (detect_environment) can report
// the live count without hardcoding it. If you add another resource
// group to RegisterAll, add a third term here and update the
// canonical_count in tests/wire/health_ping_test.go.
//
// Safe to call before RegisterAll: pure function over exported vars.
func TotalResources() int {
	return len(fixedResources) + len(InstallClients) + len(CompanionTools)
}

// fixedResource describes a fixed-URI resource (no template variable).
type fixedResource struct {
	uri  string
	name string
	desc string
	// path is the path inside the bootstrap fs (relative to LoadFS root).
	path string
}

// fixedResources is the canonical list of fixed-URI resources. Each
// entry corresponds to one file embedded under ./data.
var fixedResources = []fixedResource{
	{
		uri:  URIPrefix + "system-prompt.md",
		name: "dark-agent system prompt",
		desc: "Canonical dark-agent operating manual. Read this once on first connect, then on every MCP upgrade. Self-contained; no external docs needed.",
		path: "SYSTEM_PROMPT.md",
	},
	{
		uri:  URIPrefix + "compatibility-matrix.md",
		name: "dark-memory harness compatibility matrix",
		desc: "Per-harness capability matrix: which harness reads instructions, supports resources, has web fetch, has a real terminal. Read when unsure if a feature is supported.",
		path: "COMPATIBILITY_MATRIX.md",
	},
}

// RegisterAll wires every dark-agent bootstrap resource into the MCP
// server. Idempotent within the lifecycle of the server: registering
// the same URI twice is a programmer error (the SDK will panic).
//
// The bootstrap filesystem is captured at registration time so that
// the override env var is honored for the lifetime of the server
// process. Operators who change DARK_AGENT_BOOTSTRAP_DIR after the
// server is running must restart for the change to take effect.
func RegisterAll(srv *server.MCPServer) error {
	if srv == nil {
		return fmt.Errorf("agentbootstrap: RegisterAll: nil server")
	}
	fsys := LoadFS()

	// 1. Fixed-URI resources (system-prompt.md, compatibility-matrix.md).
	for _, r := range fixedResources {
		res := mcp.NewResource(
			r.uri,
			r.name,
			mcp.WithResourceDescription(r.desc),
			mcp.WithMIMEType("text/markdown"),
			mcp.WithAnnotations(AudienceAssistant, PriorityHigh, ""),
		)
		// makeHandler returns ResourceTemplateHandlerFunc; both
		// ResourceHandlerFunc and ResourceTemplateHandlerFunc have the
		// same underlying signature in mcp-go v0.56.0, so this cast
		// is type-safe.
		srv.AddResource(res, server.ResourceHandlerFunc(makeHandler(fsys, r.path, r.uri)))
	}

	// 2. Install-guide URI templates (one per harness).
	for _, client := range InstallClients {
		uri := URIPrefix + "install/" + client + ".md"
		name := "Install dark-memory-mcp on " + client
		desc := "Step-by-step install guide for the " + client + " harness. Covers dark-memory, optional companion MCPs (dark-research, [FUTURE-MCP-N]), and how to bootstrap the operating manual."
		fsPath := "install/" + client + ".md"

		tmpl := mcp.NewResourceTemplate(
			uri,
			name,
			mcp.WithTemplateDescription(desc),
			mcp.WithTemplateAnnotations(AudienceAssistant, PriorityHigh, ""),
		)
		srv.AddResourceTemplate(tmpl, makeHandler(fsys, fsPath, uri))
	}

	// 3. Companion-doc URI templates (one per companion tool).
	for _, tool := range CompanionTools {
		uri := URIPrefix + "companions/" + tool + ".md"
		name := "Companion tool: " + tool
		desc := "When and how to use the " + tool + " companion MCP. Read this to decide whether to install it, and to follow the install snippet."
		fsPath := "companions/" + tool + ".md"

		tmpl := mcp.NewResourceTemplate(
			uri,
			name,
			mcp.WithTemplateDescription(desc),
			mcp.WithTemplateAnnotations(AudienceAssistant, PriorityHigh, ""),
		)
		srv.AddResourceTemplate(tmpl, makeHandler(fsys, fsPath, uri))
	}

	return nil
}

// makeHandler returns a ResourceTemplateHandlerFunc that reads fsPath
// from fsys and returns the content as a TextResourceContents entry.
// The returned uri in the contents echoes the URI the harness
// requested (per MCP spec: contents[].uri must equal the request uri).
//
// We return server.ResourceTemplateHandlerFunc because its underlying
// type is identical to server.ResourceHandlerFunc (same signature),
// and a single named type can be assigned to both mcp-go AddResource
// and AddResourceTemplate methods via explicit conversion.
//
// The handler is non-blocking and has no side effects. A missing file
// at fsPath is treated as a configuration error and returns the
// error wrapped with the resource name so the harness surfaces it to
// the operator cleanly instead of silently returning empty content.
func makeHandler(fsys fs.FS, fsPath, requestURI string) server.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		// Defensive: verify the request URI matches what we registered for.
		// This guards against a misconfigured SDK or a programming error
		// in RegisterAll where the wrong URI was wired.
		if !strings.HasPrefix(req.Params.URI, URIPrefix) {
			return nil, fmt.Errorf("agentbootstrap: unexpected URI scheme: %q", req.Params.URI)
		}

		data, err := fs.ReadFile(fsys, fsPath)
		if err != nil {
			return nil, fmt.Errorf("agentbootstrap: read %s: %w (resource: %s)",
				path.Base(fsPath), err, requestURI)
		}

		_ = ctx // currently unused; reserved for future tracing/logging.
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      requestURI,
				MIMEType: "text/markdown",
				Text:     string(data),
			},
		}, nil
	}
}
