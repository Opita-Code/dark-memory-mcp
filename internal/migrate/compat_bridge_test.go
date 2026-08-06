// Package migrate_test - compat_bridge_test.go: a test-only bridge file
// that exposes the Migrations slices from the sqlite and postgres
// driver subpackages to split_test.go's
// TestSplitStatements_BackwardCompat.
//
// This file uses the external test package (migrate_test) to avoid
// the import cycle that would happen if package migrate imported
// the sqlite/postgres subpackages.
package migrate_test

import (
	migrate "github.com/dark-agents/dark-memory-mcp/internal/migrate"
	pgmig "github.com/dark-agents/dark-memory-mcp/internal/migrate/postgres"
	sqlitemig "github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
)

// sqliteMigrationsForTest returns the Migrations slice from the
// sqlite driver package.
func sqliteMigrationsForTest() []migrate.Migration {
	return sqlitemig.Migrations
}

// postgresMigrationsForTest returns the Migrations slice from the
// postgres driver package.
func postgresMigrationsForTest() []migrate.Migration {
	return pgmig.Migrations
}
