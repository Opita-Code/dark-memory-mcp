// Command dark-mem-mcp is the dark-memory MCP server.
//
// As of t6 (spec 1242, 2026-08-18) this binary is the single boot
// path. The v2.19.0 split into dispatcher→bridge→daemon (spec 1176
// §4.10) is removed from active use: the daemon never implemented
// initialize/tools/call, and production runs the single-binary
// legacy path that every opencode restart loads.
//
// The heavy legacy server boot (orchestrator + 49 tools + drift gate
// + federation peer + sweeper + startup-recover) lives in
// legacy_main.go to keep this file minimal. The bridge and daemon
// packages in cmd/ remain in the source tree as a frozen archive
// for future §4.10 re-activation; see ARCHITECTURE.md §boot path.
package main

func main() {
	legacyMain()
}
