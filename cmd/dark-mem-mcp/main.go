// Package main is the dark-mem-mcp binary — the MCP server that exposes
// 25 dark_memory_* tools backed by the dark-memory-mcp library's
// orchestrators. See ../internal/server/server.go for boot + lifecycle
// logic; ../internal/tools/*.go for the per-namespace tool handlers.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := server.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: server.New failed: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	// Register all 25 tools in canonical order (BRIDGE_AND_COEXISTENCE.md
	// §3 / spec 164 bridge.4). RegisterAll returns an error if any of
	// the 25 expected tools is missing from the registry — fail fast.
	if err := tools.RegisterAll(srv.Registry(), srv.BootState().Orchestrator, srv.BootState().Store); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: tools.RegisterAll failed: %v\n", err)
		os.Exit(1)
	}
	if err := srv.RegisterAll(); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: server.RegisterAll failed: %v\n", err)
		os.Exit(1)
	}

	if err := srv.ServeStdio(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: ServeStdio failed: %v\n", err)
		os.Exit(1)
	}
}