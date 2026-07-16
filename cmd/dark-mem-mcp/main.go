// Package main is the dark-mem-mcp binary — the MCP server that exposes
// 26 dark_memory_* tools backed by the dark-memory-mcp library's
// orchestrators. See ../internal/server/server.go for boot + lifecycle
// logic; ../internal/tools/*.go for the per-namespace tool handlers.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

func main() {
	// Review-w4-002: panic recovery at the boot layer. mcp-go's
	// WithRecovery() only catches panics INSIDE tool handlers (per its
	// own docs). A panic during server.New or tools.RegisterAll would
	// still crash the binary with a stack trace, killing the opencode
	// subprocess and leaving the harness without a coherent error.
	// The CLI binary has the same protection; mirror it here.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "dark-mem-mcp: panic during boot: %v\n%s\n", r, debug.Stack())
			os.Exit(1)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := server.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: server.New failed: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	// Register all 26 tools in canonical order (BRIDGE_AND_COEXISTENCE.md
	// §3 / spec 164 bridge.4 + DMAP v1.1 spec 193 Layer 6). RegisterAll
	// returns an error if any of the 26 expected tools is missing from
	// the registry — fail fast.
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
