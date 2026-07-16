module github.com/dark-agents/dark-memory-mcp

go 1.25.5

require (
	github.com/jackc/pgx/v5 v5.7.1
	github.com/mark3labs/mcp-go v0.56.0
	github.com/pelletier/go-toml/v2 v2.4.3
	modernc.org/sqlite v1.53.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
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
//   - This go.mod is the LIBRARY root. The library DOES depend on
//     github.com/mark3labs/mcp-go (the orchestrators + Tool struct
//     shape use mcp types for bridge.4 wire-format conformance).
//     The intent was to keep MCP-specific transport wiring (stdio,
//     recovery, filter, instructions) in cmd/dark-mem-mcp/ where
//     the binary lives — that part IS separated via the cmd
//     subdir's own go.mod.
//   - Review-w4-b02 / gate.2: the original "MUST NOT" comment was
//     aspirational. The library legitimately needs mcp types for
//     the orchestrators; only the stdio transport + middlewares
//     stay binary-local. Comment fixed to reflect reality.
