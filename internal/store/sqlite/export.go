// Package sqlite is the SQLite implementation of store.Store.
// Backed by modernc.org/sqlite (pure-Go, no cgo).
//
// Concurrency: SQLite is single-writer. s.mu serializes every write.
// Reads are concurrent-safe (modernc uses the connection pool internally).
// busy_timeout(5000ms) prevents transient "database is locked" errors.
//
// INV-1: every Save* method emits a write_audit row in the SAME tx as
// the data write. If the canary check on the payload fails (INV-3),
// the tx is rolled back and no write_audit row is created.
package sqlite

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Open is the package's exported constructor. Called by
// internal/store/factory.Open.
func Open(ctx context.Context, cfg store.Config) (store.Store, error) {
	return openSQLite(ctx, cfg)
}
