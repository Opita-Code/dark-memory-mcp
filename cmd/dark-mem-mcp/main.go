// Package main is the dark-mem-mcp binary entry. As of v2.19.0
// (spec 1176), this binary is a DISPATCHER:
//
//   - DARK_MEM_BRIDGE=0 (or unset BUT the bridge binary exists):
//     run as the legacy single-binary MCP server (today's behavior).
//   - DARK_MEM_BRIDGE=1 (default), or --bridge flag: exec into the
//     dark-mem-mcp-bridge binary which forwards stdio to the daemon.
//
// The dispatcher is intentionally minimal: it doesn't import any of
// the heavy orchestrator code. The heavy code path (single-binary
// MCP server) lives in the legacy `main` function below the bridge
// dispatch.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/dark-agents/dark-memory-mcp/internal/daemon"
)

func main() {
	// v2.19.0 dispatcher: bridge mode is the new default. Operators
	// can set DARK_MEM_BRIDGE=0 (or pass --legacy) to opt into the
	// single-binary mode (legacy behavior).
	if shouldRunLegacy() {
		runLegacyMain()
		return
	}
	runBridge()
}

// shouldRunLegacy returns true when the operator has explicitly opted
// out of the bridge (DARK_MEM_BRIDGE=0 or --legacy flag) OR when the
// bridge binary is not available alongside the dispatcher (defensive
// fallback for environments like CI where only the dispatcher was
// shipped).
func shouldRunLegacy() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--legacy" {
			return true
		}
	}
	if v := os.Getenv("DARK_MEM_BRIDGE"); v == "0" || v == "false" || v == "no" {
		return true
	}
	// Defensive fallback: if the bridge binary is NOT alongside the
	// dispatcher, the operator likely shipped only the dispatcher
	// (e.g. CI). Fall back to legacy single-binary mode so the
	// wire conformance tests still pass.
	if findBridgeBinary() == "" {
		return true
	}
	return false
}

// runBridge exec's into the bridge binary alongside the dispatcher.
func runBridge() {
	bin := findBridgeBinary()
	if bin == "" {
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp: bridge binary not found alongside %s.\n"+
			"  Build it with: go build -o bin/dark-mem-mcp-bridge.exe ./cmd/dark-mem-mcp-bridge\n"+
			"  Or set DARK_MEM_BRIDGE=0 to run in legacy single-binary mode.\n",
			os.Args[0])
		os.Exit(1)
	}
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: bridge exec failed: %v\n", err)
		os.Exit(1)
	}
}

// findBridgeBinary looks for dark-mem-mcp-bridge alongside the
// dispatcher binary. The same directory is where `go build -o bin/`
// places both binaries.
func findBridgeBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(dir, "dark-mem-mcp-bridge.exe"),
		filepath.Join(dir, "dark-mem-mcp-bridge"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// runLegacyMain is the v2.18.0-and-earlier single-binary MCP server
// path. Kept verbatim here under the dispatcher so existing
// deployments that set DARK_MEM_BRIDGE=0 continue to work unchanged.
//
// This function lives in cmd/dark-mem-mcp/legacy_main.go to keep
// the dispatcher file (cmd/dark-mem-mcp/main.go) thin.
func runLegacyMain() {
	legacyMain()
	// legacyMain() never returns; it calls os.Exit on error. Keep
	// a safety net here.
	_ = context.Background()
	_ = daemon.DefaultSocketPath()
}
