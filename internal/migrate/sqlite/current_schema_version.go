// Package sqlite — current_schema_version.go: convenience accessor.
//
// Re-exports the highest VERSION available in this driver's migration
// slice, so callers can report the server's schema-version capability
// without iterating the slice themselves.
//
// Why this lives in the sqlite package (not just internal/migrate):
// the migrate package's CurrentSchemaVersion takes a []Migration, but
// callers (notably internal/tools/agent_bootstrap.go's
// detect_environment tool) want a single source of truth — "what's
// the highest version THIS binary knows about" — without having to
// import the migrations slice directly. This thin wrapper closes that
// gap and adds an obvious name to grep for.
//
// Posture: this file is intentionally tiny. If the contract grows
// (e.g., adding per-driver "supported features" beyond version number,
// or exposing the migration count), expand here rather than scattering
// helpers across internal/tools and the migrate package.
package sqlite

import "github.com/dark-agents/dark-memory-mcp/internal/migrate"

// CurrentVersion returns the highest migration VERSION in this
// driver's compiled-in Migrations slice. Equivalent to
// migrate.CurrentSchemaVersion(Migrations); the duplication is
// intentional so callers don't need to import the migration slice
// (which would couple them to internal migration DDL).
//
// Returns 0 if Migrations is empty (defensive — should never happen
// in production because the binary would have nothing to migrate to).
func CurrentVersion() int {
	return migrate.CurrentSchemaVersion(Migrations)
}
