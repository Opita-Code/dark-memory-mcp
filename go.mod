module github.com/dark-agents/dark-memory-mcp

go 1.25.0

require (
	github.com/jackc/pgx/v5 v5.7.1
	github.com/pelletier/go-toml/v2 v2.4.3
	modernc.org/sqlite v1.53.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.27.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	modernc.org/libc v1.73.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// Dark Memory MCP — sibling of dark-research-mcp.
//
// Dark Memory MCP is a standalone Go module that owns the persistent
// memory + workflow layer for dark-agents-v2. dark-research-mcp is
// its peer; it owns OSINT acquisition. Neither depends on the other
// at the binary level. Either can be installed independently via
// opencode.jsonc.
//
// Tool naming convention: prefix `dark_memory_*`. This is intentionally
// distinct from dark-research-mcp's internal `dark_mem_*` namespace
// (which is now DEPRECATED — see its `internal/tools/dark_mem.go`
// for the deprecation shim). Tool prefixes per binary:
//
//   dark-research-mcp   →   dark_research_*   +   dark_ssd_*
//                          + dark_mem_*       (DEPRECATED, no maintenance)
//
//   dark-memory-mcp     →   dark_memory_*     (canonical, this module)
//
// Both can coexist in opencode.jsonc. The dark-recall plugin v2
// prefers Dark Memory MCP calls when its server is configured.
//
// NOTE on dependency policy:
//   - This go.mod is the LIBRARY root. The library MUST NOT depend on
//     github.com/mark3labs/mcp-go. Anything MCP-specific lives in
//     cmd/dark-memory-mcp/ with its own go.mod that requires this
//     library plus mcp-go.
//   - This split lets other tools (e.g. dark-research-mcp, custom
//     agents) import the library in-process without dragging in MCP
//     machinery, AND lets the standalone MCP server talk to clients
//     that prefer stdio-based integration.
