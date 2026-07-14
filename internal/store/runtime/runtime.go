// Package runtime is the entry point that selects the right store
// implementation. It lives in its own package to avoid an import
// cycle: internal/store/sqlite and internal/store/postgres both import
// the Store interface from internal/store, so the dispatch function
// cannot live in internal/store itself.
package runtime

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/postgres"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// Open returns the right Store implementation based on cfg.Driver.
// Both drivers construct themselves, apply pending migrations, and
// run the constitution watchdog (INV-4).
func Open(ctx context.Context, cfg store.Config) (store.Store, error) {
	switch cfg.Driver {
	case store.DriverSQLite:
		return sqlite.Open(ctx, cfg)
	case store.DriverPostgres:
		return postgres.Open(ctx, cfg)
	case "":
		return nil, store.ErrInvalidArgument
	default:
		return nil, store.ErrInvalidArgument
	}
}
