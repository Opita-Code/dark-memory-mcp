// Package tools - bootstrap_data.go: builds the agentbootstrap
// template context from the live sources of truth.
//
// This is the ONLY place that populates agentbootstrap.BootstrapData.
// Everything it fills is derived — never hardcoded:
//
//	Version          <- version.Resolve() (ldflags > buildinfo > "dev")
//	SchemaVersion    <- sqlite.CurrentVersion() (compiled-in migrations)
//	CanonicalTools   <- len(canonicalToolOrder)
//	NamespaceCount   <- len(canonicalNamespaces)
//	NamespaceCounts  <- canonicalNamespaces (per-group len)
//	TotalResources   <- agentbootstrap.TotalResources()
//
// The whole point of this indirection: adding a tool to
// canonicalNamespaces, a migration to the sqlite driver, or bumping
// the version injects the new value into every bootstrap document
// (SYSTEM_PROMPT.md, COMPATIBILITY_MATRIX.md, install guides,
// companion docs) with ZERO manual edits.
package tools

import (
	"github.com/dark-agents/dark-memory-mcp/internal/agentbootstrap"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/version"
)

// BuildBootstrapData assembles the agentbootstrap.BootstrapData from
// the live sources of truth. Cheap (no I/O beyond version.Resolve's
// sync.Once cache); safe to call per request.
func BuildBootstrapData() agentbootstrap.BootstrapData {
	return agentbootstrap.BootstrapData{
		Version:          version.Resolve().Version,
		BootstrapVersion: agentbootstrap.BootstrapVersion,
		CanonicalTools:   len(canonicalToolOrder),
		SchemaVersion:    sqlite.CurrentVersion(),
		TotalResources:   agentbootstrap.TotalResources(),
		NamespaceCount:   len(canonicalNamespaces),
		NamespaceCounts:  NamespaceCounts(),
	}
}
