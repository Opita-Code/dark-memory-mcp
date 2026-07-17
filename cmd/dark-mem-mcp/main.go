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

	// v1.3.0: install the boot-time metadata that
	// dark_memory_health_ping reads (server version, name,
	// coexistence group, driver label, DSN path) BEFORE the
	// tool registry is built so the first health_ping call
	// already reports the correct values. Installing AFTER
	// RegisterAll is also OK (SetRuntimeContext uses atomic
	// stores; subsequent health_pings see the new values) but
	// doing it before is cleaner.
	bootState := srv.BootState()
	tools.SetRuntimeContext(tools.RuntimeContext{
		BootedAt:         bootState.Config.BootedAt,
		ServerVersion:    bootState.Config.ServerVersion,
		ServerName:       bootState.Config.ServerName,
		CoexistenceGroup: bootState.Config.CoexistenceGroup,
		DriverLabel:      string(bootState.Config.DBDriver),
		DSNPath:          bootState.Config.DBDSN,
	})

	// Register all 28 tools in canonical order (BRIDGE_AND_COEXISTENCE.md
	// §3 / spec 164 bridge.4 + DMAP v1.1 spec 193 Layer 6). RegisterAll
	// returns an error if any of the 28 expected tools is missing from
	// the registry — fail fast. v1.3.0: health_ping added to OBSERVABILITY
	// (3 → 4 tools); canonical count is now 28.
	if err := tools.RegisterAll(srv.Registry(), bootState.Orchestrator, bootState.Store); err != nil {
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
