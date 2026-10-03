module github.com/dark-agents/dark-memory-mcp/cmd/dark-mem-mcp-supervisor

go 1.25.5

// The supervisor is a sibling module (like dark-mem-mcp itself).
// It does NOT import the parent library — it only needs the
// standard library. This keeps the binary tiny (~5MB) and
// avoids compile-time coupling.
