package audit_test

// Shared helpers for audit tests (L1 + L2). The property test uses
// rapid.T which doesn't have *testing.T's Helper(), so we keep
// helpers thin and side-effect-free.

import (
	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// sqlOpenMemory opens an in-memory SQLite database. Caller is
// responsible for closing (typically via t.Cleanup).
func sqlOpenMemory() (*sql.DB, error) {
	return sql.Open("sqlite", ":memory:")
}

// newTestWriterAny is the unified factory for both *testing.T and
// *rapid.T. We use a tiny interface to avoid duplicating the open +
// schema logic.
type writerCleanupper interface {
	Cleanup(func())
	Fatalf(format string, args ...any)
}

func newTestWriterAny(t writerCleanupper) *audit.Writer {
	db, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	return audit.NewWriter(db)
}
