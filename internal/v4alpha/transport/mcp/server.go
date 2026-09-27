// Package mcp is the JSON-RPC transport for dark-memory-v4.
//
// BUG-7 (2026-09-27): the v4-alpha.1 transport wires the
// canonical mcp-go library (mark3labs/mcp-go v0.40.0, pinned by
// the parent module) into a thin wrapper that:
//
//   - constructs an mcp-go MCPServer with the BUG-7 MVP tool set
//     (health_ping, session_start/close/status, agent_memory_save/recall)
//   - drives the JSON-RPC loop on a configurable io.Reader/io.Writer
//     so the transport is testable in-process (production uses
//     os.Stdin/os.Stdout)
//
// The Server type holds shared dependencies (audit writer, session
// store, agent_memory store) so each tool handler is a small
// closure over the *sql.DB rather than re-wiring from scratch.
//
// The MVP tool set is deliberately small (6 tools) — the
// remaining 69 tools (vibe-loop, governance, observability,
// research, security, admin, update) land in subsequent BUG-8
// commits. The transport package structure stays the same; only
// the registration calls in NewServer grow.
package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"runtime"

	"github.com/mark3labs/mcp-go/server"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
)

// Version is the server's identity string. Set at package init
// from the linked binary's main.Version via ldflags — the parent
// cmd/dark-memory-v4 binary stamps it via:
//
//	go build -ldflags "-X github.com/dark-agents/dark-memory-mcp/internal/v4alpha/transport/mcp.Version=v4-alpha.1"
//
// Until that's wired, it resolves to the sentinel below.
var Version = "v4-alpha.1-dev"

// ServerName is the canonical server name baked into the MCP
// initialize response. The v3 dark-memory-mcp uses
// "dark-memory-mcp"; for v4 the binary is "dark-memory-v4" but
// the server identity preserves the operator-facing "dark-memory"
// prefix so persona harnesses don't break (spec 164 bridge.4
// canonicalNamespaces).
const ServerName = "dark-memory-v4"

// Server holds the MCP server + its dependency graph. Built by
// NewServer, used by ServeStdio.
type Server struct {
	mcpSrv    *server.MCPServer
	db        *sql.DB
	audit     *audit.Writer
	session   *session.Store
	memories  *agent_memory.Store
	startedAt string
}

// NewServer builds the MCPServer, registers the BUG-7 MVP tools,
// and returns a Server ready for ServeStdio.
//
// The DB must have had CreateSchema called on every v4alpha
// package (audit, session, manifest, vibe, agent_memory). The
// binary layer (cmd/dark-memory-v4/serve.go) calls applyAllSchemas
// before NewServer so this contract is upheld in production.
// Tests construct the DB by hand.
func NewServer(db *sql.DB) (*Server, error) {
	if db == nil {
		return nil, fmt.Errorf("mcp NewServer: db is nil")
	}

	auditW := audit.NewWriter(db)
	sessStore := session.NewStore(db, auditW)
	memStore := agent_memory.NewStore(db)

	mcpSrv := server.NewMCPServer(
		ServerName,
		Version,
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	s := &Server{
		mcpSrv:   mcpSrv,
		db:       db,
		audit:    auditW,
		session:  sessStore,
		memories: memStore,
	}

	// Register the BUG-7 MVP tool set.

	// Register the BUG-7 MVP tool set. The remaining 69 tools
	// (vibe, governance, observability, research, security,
	// admin, update) are added incrementally.
	registerHealthTool(s)
	registerSessionTools(s)
	registerAgentMemoryTools(s)

	return s, nil
}

// ServeStdio runs the JSON-RPC loop on the provided reader/writer.
// Production: pass os.Stdin / os.Stdout. Tests: pass bytes.Buffer /
// strings.Reader for in-process control.
//
// Returns nil on clean shutdown (ctx cancelled or stdin EOF).
//
// Worker pool is configured to SIZE 1 (serial) so tool calls
// execute in the order they're received. The default mcp-go
// worker pool runs them in parallel, which races against the
// read-after-write contract that FTS5 recall depends on
// (save + recall in one batch would otherwise see inconsistent
// state). Single-worker keeps the protocol simple at the cost
// of throughput, which is acceptable for v4-alpha.1 (operator-
// facing CLI, not a high-throughput service).
//
// We apply StdioOptions manually because Listen doesn't take
// them; only the package-level ServeStdio does (and that one
// hardcodes os.Stdin/os.Stdout). The option functions are
// exported, so applying them by hand is the documented escape
// hatch.
func (s *Server) ServeStdio(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	stdioSrv := server.NewStdioServer(s.mcpSrv)
	server.WithWorkerPoolSize(1)(stdioSrv)
	server.WithQueueSize(64)(stdioSrv)
	return stdioSrv.Listen(ctx, stdin, stdout)
}

// runtimeInfo is the structured response of health_ping. Defined
// here (not in health.go) because other tools echo fields like
// version + go_version into their response for diagnostic
// friendliness.
type runtimeInfo struct {
	ServerName    string `json:"server_name"`
	ServerVersion string `json:"server_version"`
	GoVersion     string `json:"go_version"`
	SchemaVersion string `json:"schema_version"`
}

// goRuntimeVersion is captured at package init for fast access.
var goRuntimeVersion = runtime.Version()
