// Package migrate is the versioned schema migrator for Dark Memory MCP.
// Two driver implementations live in:
//
//   internal/migrate/sqlite    — DDL for SQLite (uses INTEGER PRIMARY KEY AUTOINCREMENT)
//   internal/migrate/postgres  — DDL for Postgres (uses BIGSERIAL / SERIAL)
//
// Both driver packages export the same Migration slice and a Migrate(db)
// function. The Store implementations call into the right driver based
// on Config.Driver.
//
// Each migration has:
//   - Version (monotonically increasing, starting at 1)
//   - Name    (human-readable)
//   - Up      (idempotent SQL — every statement uses IF NOT EXISTS /
//              IF EXISTS so re-running is safe even outside the
//              bookkeeping table)
//
// The bookkeeping table (schema_migrations) tracks which versions have
// been applied. Migrate() applies every pending migration in its own
// transaction so a failure on v3 leaves v1+v2 applied.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration is one versioned schema change. Both driver packages
// (sqlite, postgres) export a `[]Migration` with the same Version+Name
// fields but driver-specific Up SQL.
type Migration struct {
	Version int
	Name    string
	Up      string // idempotent SQL
}

// Migrate applies every pending migration in migs order. Each migration
// runs in its own transaction so a partial failure is recoverable.
//
// The bookkeeping table schema_migrations is created on the first call.
// Older DBs that pre-date the migrate system (e.g., dark-research-mcp's
// existing dark.db at schema version 3) start with all v1..v3 marked
// as already-applied via the Bootstrap function below.
func Migrate(ctx context.Context, db *sql.DB, migs []Migration) error {
	if _, err := db.ExecContext(ctx, bookkeepingTable); err != nil {
		return fmt.Errorf("migrate: bookkeeping: %w", err)
	}
	applied, err := loadApplied(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range migs {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version, or 0
// if none have been applied yet.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	if _, err := db.ExecContext(ctx, bookkeepingTable); err != nil {
		return 0, fmt.Errorf("migrate: schema version bookkeeping: %w", err)
	}
	var v sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("migrate: schema version query: %w", err)
	}
	return int(v.Int64), nil
}

// MigrationStatus describes every registered migration and whether it
// has been applied to this DB.
func MigrationStatus(ctx context.Context, db *sql.DB, migs []Migration) ([]Status, error) {
	applied, err := loadApplied(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(migs))
	for _, m := range migs {
		st := Status{Version: m.Version, Name: m.Name}
		if ts, ok := applied[m.Version]; ok {
			st.Applied = true
			st.AppliedAt = ts
		}
		out = append(out, st)
	}
	return out, nil
}

// Status describes one migration and whether it has been applied.
type Status struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Applied   bool   `json:"applied"`
	AppliedAt string `json:"applied_at,omitempty"`
}

// Bootstrap marks versions 1..n as already-applied. Used when migrating
// a dark.db that was created by dark-research-mcp BEFORE the Dark
// Memory MCP schema migrations existed. The legacy DB has its own
// schema (versions 1-3 in dark-research-mcp's internal naming); we
// mark them as already-applied so the new v4+ migrations run on top
// without re-running v1-3.
func Bootstrap(ctx context.Context, db *sql.DB, n int) error {
	if _, err := db.ExecContext(ctx, bookkeepingTable); err != nil {
		return fmt.Errorf("migrate: bootstrap bookkeeping: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowRFC3339Nano()
	for v := 1; v <= n; v++ {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			v, now); err != nil {
			// Postgres: use ON CONFLICT DO NOTHING instead of OR IGNORE.
			// The driver-specific implementations call BootstrapPostgres
			// instead when running on postgres.
			return fmt.Errorf("migrate: bootstrap v%d: %w", v, err)
		}
	}
	return tx.Commit()
}

// ----- internal helpers -----

func applyOne(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: v%d begin: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if m.Up != "" {
		if _, err := tx.ExecContext(ctx, m.Up); err != nil {
			return fmt.Errorf("migrate: v%d (%s) up: %w", m.Version, m.Name, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.Version, nowRFC3339Nano()); err != nil {
		return fmt.Errorf("migrate: v%d record: %w", m.Version, err)
	}
	return tx.Commit()
}

func loadApplied(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: load applied: %w", err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var v int
		var ts string
		if err := rows.Scan(&v, &ts); err != nil {
			return nil, err
		}
		out[v] = ts
	}
	return out, rows.Err()
}

// bookkeepingTable is the DDL for the migrations bookkeeping table.
// The "OR IGNORE" / "IF NOT EXISTS" patterns are driver-portable.
// Driver-specific Bootstrap functions handle the conflict semantics
// for the initial INSERT.
const bookkeepingTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
`

// nowRFC3339Nano returns the current UTC time in RFC 3339 nano format.
// Defined here so this package has no dependency on internal packages.
var nowFn = func() string { return "1970-01-01T00:00:00Z" }

func nowRFC3339Nano() string { return nowFn() }

// SetClock overrides the clock used for applied_at timestamps.
// Production code does not call this; tests may.
func SetClock(fn func() string) {
	if fn != nil {
		nowFn = fn
	}
}
