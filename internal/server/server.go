// Package server — server.go: the Server type, registry wiring, and
// stdio transport. This is the entry point the cmd/dark-mem-mcp
// binary uses (see ../../cmd/dark-mem-mcp/main.go).
//
// Per RFC §6 step 5, after Boot returns the caller is expected to
// (a) register all 26 tools (this is where per-namespace tool files
// come in — see internal/tools/*.go), then (b) call ServeStdio to
// block on the stdio MCP transport.
//
// Tool registration is explicit (Register* functions below) rather
// than init-time magic. This keeps the boot sequence greppable and
// test-friendly: tests can construct a Server with only a subset of
// tools to exercise specific code paths.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"

	"github.com/dark-agents/dark-memory-mcp/internal/tools"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server is the MCP server. It wraps the BootState (Store + Safety +
// Orchestrator + Registry) and the mcp-go server instance.
type Server struct {
	boot   *BootState
	mcpSrv *server.MCPServer
}

// New constructs the Server. Does NOT start the stdio transport; call
// ServeStdio for that. Tool registration must happen between New and
// ServeStdio (or via RegisterAll called from New — see flag below).
//
// Three mcp-go options are wired here:
//   - WithToolCapabilities(true): advertises listChanged so harnesses
//     can hot-reload the tool list.
//   - WithRecovery(): catches panics in any tool handler so one bad
//     call can't bring down the server.
//   - WithToolFilter(canonicalOrderFilter): mcp-go's handleListTools
//     sorts tools alphabetically by name (verified by reading the
//     upstream source at v0.40.0). Our canonical order per RFC D-9
//     is namespace-grouped (SESSION → RESEARCH → VIBE → CONTEXT →
//     JUDGE → POLICY → OBSERVABILITY → ADMIN); the filter re-sorts
//     to that order so tools/list emits the contract promised by
//     spec 164 bridge.4.
//   - WithInstructions(...): bake coexistence_group into the
//     initialize response so harnesses can detect dark-agents/memory
//     membership via the standard MCP instructions field (bridge.2).
func New(ctx context.Context) (*Server, error) {
	boot, err := Boot(ctx)
	if err != nil {
		return nil, err
	}

	// Canonical-order position map (built once at boot; reused on
	// every tools/list request via the filter closure).
	canonicalPos := make(map[string]int, 32)
	for i, n := range tools.CanonicalOrder() {
		canonicalPos[tools.WireName(n)] = i
	}
	canonicalOrderFilter := func(_ context.Context, listed []mcplib.Tool) []mcplib.Tool {
		// Stable sort by canonical position; tools not in canonical
		// (shouldn't happen given RegisterAll sanity-check) sink to
		// the end in alphabetical order.
		sort.SliceStable(listed, func(i, j int) bool {
			pi, oki := canonicalPos[listed[i].Name]
			pj, okj := canonicalPos[listed[j].Name]
			switch {
			case oki && okj:
				return pi < pj
			case oki:
				return true
			case okj:
				return false
			default:
				return listed[i].Name < listed[j].Name
			}
		})
		return listed
	}

	mcpSrv := server.NewMCPServer(
		boot.Config.ServerName,
		boot.Config.ServerVersion,
		server.WithToolCapabilities(true),
		server.WithRecovery(),
		server.WithToolFilter(canonicalOrderFilter),
		// coexistence_group baked into the standard MCP instructions
		// field. mcp-go v0.40.0's Implementation struct doesn't carry
		// custom fields, so we use the instructions channel (visible
		// to all MCP-native harnesses that read initialize response).
		// The exact string is documented in BRIDGE_AND_COEXISTENCE.md §3.
		server.WithInstructions(fmt.Sprintf(
			"dark-memory-mcp server. coexistence_group=%s (spec 164 bridge.2). Canonical 26-tool order preserved per spec 164 bridge.4 + DMAP v1.1 spec 193 Layer 6. This server is part of the dark-agents/memory coexistence group; harnesses detecting another dark-agents/* server should prefer the local dark_memory_* tools over dark_mem_*.",
			boot.Config.CoexistenceGroup,
		)),
	)

	return &Server{
		boot:   boot,
		mcpSrv: mcpSrv,
	}, nil
}

// RegisterAll iterates the canonical 26-tool list and registers each
// tool present in the Registry with the mcp-go server. Tools not yet
// added are silently skipped (the canonical order is the contract;
// the actual set is the intersection of canonical ∩ registered).
//
// It also registers any "extra" tools — registered tools that are
// NOT in the canonical order. These are armed-mode namespaces (e.g.
// L7-REDTEAM) that appear only when the operator has flipped the
// relevant env flag. The public surface in the un-armed server stays
// at 26; the armed server gets 26 + extras.
//
// The handler adapter converts tools.HandlerFunc (our internal
// shape) into the mcp-go handler signature: it pulls Arguments from
// the CallToolRequest, calls our handler, and packages the
// ToolResponse back into a CallToolResult with structured content.
func (s *Server) RegisterAll() error {
	for _, t := range s.boot.Registry.ListCanonical() {
		if err := s.registerOne(t); err != nil {
			return fmt.Errorf("server.RegisterAll: tool %q: %w", t.Name, err)
		}
	}
	canonicalCount := len(s.boot.Registry.ListCanonical())
	extras := s.boot.Registry.ListExtras()
	for _, t := range extras {
		if err := s.registerOne(t); err != nil {
			return fmt.Errorf("server.RegisterAll: extra tool %q: %w", t.Name, err)
		}
	}
	if len(extras) > 0 {
		log.Printf("dark-mem-mcp: registered %d canonical + %d extras (armed-mode namespaces)",
			canonicalCount, len(extras))
	} else {
		log.Printf("dark-mem-mcp: registered %d tools (canonical order)", canonicalCount)
	}
	return nil
}

// registerOne adds a single Tool to the mcp-go server.
//
// We use NewToolWithRawSchema (not NewTool + WithRawInputSchema)
// because mcp-go's Tool.MarshalJSON (verified in v0.56.0 mcp/tools.go
// lines 678-687) still has an intentional conflict check that errors
// when both InputSchema.Type and RawInputSchema are set:
//
//	if t.RawInputSchema != nil {
//	    if t.InputSchema.Type != "" {
//	        return nil, fmt.Errorf("tool %s has both InputSchema
//	            and RawInputSchema set: %w", t.Name, errToolSchemaConflict)
//	    }
//	    m["inputSchema"] = t.RawInputSchema
//	}
//
// NewTool initializes InputSchema.Type="object" by default, so
// using NewTool + WithRawInputSchema trips that check. NewToolWithRawSchema
// starts with a clean Tool (InputSchema.Type="") and sets
// RawInputSchema directly — bypassing the conflict. The upstream
// ToolInputSchema MarshalJSON was fixed in v0.53.0 (PR #858), but
// that fix is independent of the Tool-level conflict check which
// is intentional.
//
// Discovered via the bridge.7 conformance test: tools/list returned
// a serialization error and the client never received a response
// (the server logged "failed to write response: ... tool
// dark_memory_active_policy has both InputSchema and RawInputSchema set").
func (s *Server) registerOne(t *tools.Tool) error {
	wireName := tools.WireName(t.Name)
	tool := mcplib.NewToolWithRawSchema(wireName, t.Description, t.InputSchema)
	handler := s.wrapHandler(t)
	s.mcpSrv.AddTool(tool, handler)
	return nil
}

// wrapHandler converts our tools.HandlerFunc to the mcp-go handler
// signature. It is the single point where the two shapes meet:
//
//	our ToolResponse {Data, Audit, Next, Error}
//	    ↔
//	mcp-go CallToolResult {Content, IsError}
//
// On error: we emit a single TextContent block with the structured
// ToolError JSON; the mcp-go IsError marker is set so the harness
// can branch on it.
func (s *Server) wrapHandler(t *tools.Tool) func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		// mcp-go returns Arguments as map[string]any; we re-marshal
		// to raw JSON so our handler signature (json.RawMessage) is
		// uniform regardless of whether the harness pre-decoded.
		rawMap := req.GetArguments()
		var rawJSON json.RawMessage
		if rawMap != nil {
			b, err := json.Marshal(rawMap)
			if err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("internal: cannot re-marshal args: %v", err)), nil
			}
			rawJSON = b
		}
		resp, err := t.Handler(ctx, rawJSON)
		if err != nil {
			// Handler returned a Go error (shouldn't happen for
			// BindOrchestrator adapters, which surface errors as
			// ToolError). Map to a generic internal error.
			resp = &tools.ToolResponse{
				Error: &tools.ToolError{
					Code:    tools.ErrInternal,
					Message: err.Error(),
				},
			}
		}
		// Marshal the ToolResponse. On error: emit as IsError text.
		if resp.Error != nil {
			body, _ := json.Marshal(resp)
			return mcplib.NewToolResultError(string(body)), nil
		}
		body, err := json.Marshal(resp)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("internal: marshal ToolResponse: %v", err)), nil
		}
		return mcplib.NewToolResultText(string(body)), nil
	}
}

// ServeStdio starts the stdio transport and blocks until ctx is
// cancelled or the transport fails. Defers the boot Shutdown so
// resources are released on exit.
func (s *Server) ServeStdio(ctx context.Context) error {
	log.Printf("dark-mem-mcp: serving stdio (server=%s v%s coexistence_group=%s)",
		s.boot.Config.ServerName, s.boot.Config.ServerVersion, s.boot.Config.CoexistenceGroup)
	defer s.boot.Shutdown(ctx)
	return server.ServeStdio(s.mcpSrv)
}

// Close releases boot resources. Idempotent.
func (s *Server) Close() {
	if s == nil || s.boot == nil {
		return
	}
	_ = s.boot.Shutdown(context.Background())
}

// Registry exposes the tool registry so callers (e.g. tests, the
// RegisterAll helper) can register tools between New and ServeStdio.
func (s *Server) Registry() *tools.Registry {
	return s.boot.Registry
}

// BootState returns the boot state (for tests that need direct
// access to the Store / Orchestrator without going through MCP).
func (s *Server) BootState() *BootState {
	return s.boot
}
