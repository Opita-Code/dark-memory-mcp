// Package postgres is the Postgres implementation of store.Store.
// Backed by jackc/pgx/v5/pgxpool (pure-Go, native protocol).
package postgres

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Open is the package's exported constructor. Called by
// internal/store/factory.Open.
func Open(ctx context.Context, cfg store.Config) (store.Store, error) {
	return openPostgres(ctx, cfg)
}
