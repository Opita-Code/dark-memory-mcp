// Package server — server.go: the Server type, registry wiring, and
// stdio transport. This is the entry point the cmd/dark-mem-mcp
// binary uses (see ../../cmd/dark-mem-mcp/main.go).
//
// Per RFC §6 step 5, after Boot returns the caller is expected to
// (a) register all 25 tools (this is where per-namespace tool files
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
func New(ctx context.Context) (*Server, error) {
	boot, err := Boot(ctx)
	if err != nil {
		return nil, err
	}

	// Build the mcp-go server. serverInfo carries the canonical
	// coexistence_group (BRIDGE_AND_COEXISTENCE.md §3 / spec 164
	// bridge.2); harnesses inspect this field to discover that this
	// server is part of the dark-agents/memory group.
	mcpSrv := server.NewMCPServer(
		boot.Config.ServerName,
		boot.Config.ServerVersion,
		server.WithToolCapabilities(true),
	)

	return &Server{
		boot:   boot,
		mcpSrv: mcpSrv,
	}, nil
}

// RegisterAll iterates the canonical 25-tool list and registers each
// tool present in the Registry with the mcp-go server. Tools not yet
// added are silently skipped (the canonical order is the contract;
// the actual set is the intersection of canonical ∩ registered).
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
	log.Printf("dark-mem-mcp: registered %d tools (canonical order)", len(s.boot.Registry.ListCanonical()))
	return nil
}

// registerOne adds a single Tool to the mcp-go server.
func (s *Server) registerOne(t *tools.Tool) error {
	wireName := tools.WireName(t.Name)
	tool := mcplib.NewTool(
		wireName,
		mcplib.WithDescription(t.Description),
		mcplib.WithRawInputSchema(t.InputSchema),
	)
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