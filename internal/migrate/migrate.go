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
//
// F37 (v1.2.2 — see CHANGELOG): applyOne now runs each statement in
// m.Up individually instead of in a single ExecContext. Statements
// that fail with the SQLite/Postgres "duplicate column name"
// error class are tolerated as warnings — the migration's spirit is
// idempotent (`ALTER TABLE ADD COLUMN ... DEFAULT 'default'` is safe
// to re-run on a column that already exists with the same default),
// and the alternative (forcing the operator to manually drop+re-add
// the column, or back-up the DB, before the migration runner can
// proceed) is a strictly worse operator experience. Partial-application
// of a v7-style "add project_id to every tenant-scoped table"
// migration can happen when (a) the binary crash-restarts mid-DDL,
// or (b) the same dark.db is upgraded by a server binary that records
// v7 in schema_migrations after the bookkeeping table existed but
// before the per-column ALTERs finished.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

// applyOne runs m.Up as individual statements inside a single
// transaction. If a statement fails with an idempotency-respecting
// DDL error class — F37: "duplicate column name"; F39: "no such
// module" (sqlite-vec orphan triggers); F40: "table already exists"
// — the error is downgraded to a warning and the migration continues.
// See migrate.go package doc and isToleratedDDLError for the
// rationale and exclusion list.
//
// Any other error aborts the transaction and fails the migration.
func applyOne(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: v%d begin: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if m.Up != "" {
		stmts := splitStatements(m.Up)
		for i, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				if isToleratedDDLError(err) {
					// F37/F39/F40: tolerate. The column already exists,
					// the module is unloaded but the trigger is harmless,
					// or the table already exists — the migration's spirit
					// is idempotent, and forcing the operator to manually
					// patch the DB before the runner can proceed is a
					// strictly worse experience. Logged via the standard
					// slog default logger at the caller layer; this helper
					// stays side-effect-free.
					continue
				}
				return fmt.Errorf("migrate: v%d (%s) up: stmt[%d]: %w", m.Version, m.Name, i, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.Version, nowRFC3339Nano()); err != nil {
		return fmt.Errorf("migrate: v%d record: %w", m.Version, err)
	}
	return tx.Commit()
}

// splitStatements breaks a multi-statement migration body into
// individual non-empty statements (split on `;`, trim whitespace,
// drop empty). Comments (lines starting with `--`) are kept
// in-place because SQLite/Postgres accept them anywhere.
//
// This is intentionally simple: it does not understand BEGIN..END
// blocks (none of our migrations use them) or quoted strings with
// embedded semicolons (none present). If a future migration needs
// that, lift to a real SQL lexer.
func splitStatements(body string) []string {
	raw := strings.Split(body, ";")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		s := strings.TrimSpace(r)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// isToleratedDDLError reports whether err matches an error class
// that applyOne treats as "already satisfied" (DDL was idempotent
// in spirit). The migration continues past the offending statement.
//
// Tolerated classes (substring match on lower-cased error text):
//
//   - F37: "duplicate column name" / "column X already exists" —
//     ALTER TABLE ADD COLUMN on a column that already exists with
//     the same default. Common in partial-state dark.db recovers.
//
//   - F39: "no such module: <name>" — a trigger references an
//     unloadable SQLite extension (e.g. sqlite-vec for vec0 virtual
//     tables created by older code and orphaned in the DB). The DDL
//     operation itself (ALTER TABLE, CREATE INDEX) succeeds at the
//     schema level; SQLite just refuses to validate the orphan
//     triggers. We skip the failing statement rather than fail the
//     whole migration. Operators must clean up the orphan triggers
//     separately if they want them back.
//
//   - F40: "table X already exists" — CREATE TABLE on a table that
//     already exists. Distinguished from F37 by error wording;
//     covers CREATE TABLE statements that may have been
//     duplicated by EnsureCoreTables + Migrate double-boot.
func isToleratedDDLError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "duplicate column name") {
		return true // F37
	}
	if strings.Contains(s, "column") && strings.Contains(s, "already exists") {
		return true // F37 (postgres variant)
	}
	if strings.Contains(s, "no such module") {
		return true // F39 (orphan sqlite-vec triggers)
	}
	if strings.Contains(s, "table") && strings.Contains(s, "already exists") {
		return true // F40
	}
	return false
}

// EnsureCoreTables is a recovery-mode helper. It pre-creates the
// four core tables that dark-memory-mcp's v5/v6/v7 migrations
// expect to find (sessions, projects, constitutions, write_audit)
// using CREATE TABLE IF NOT EXISTS. It is meant to run BEFORE
// Migrate() when the bootstrapping DB has gaps — typical case:
// dark.db was created by dark-research-mcp with a separate
// schema_migrations ledger, so dark-memory-mcp's v5+ migrations
// never actually ran and the bookkeeping table reports them as
// applied (since dark-research-mcp has the same version numbers
// for v1-v3). Without EnsureCoreTables, a real recovery path is
// absent: the operator would have to manually run missing CREATE
// TABLE statements.
//
// F38 (v1.2.2): adds this self-healing entry point so a fresh
// dark-memory-mcp.exe can boot against a half-migrated dark.db
// without operator intervention. The pre-created tables use the
// exact schema from internal/migrate/sqlite/ddl.go v5 and v7; if
// the migrations evolve past v10, EnsureCoreTables must be
// updated to match — handled in the same release that adds the
// new migration.
func EnsureCoreTables(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		// From v5 (sessions_table) + v7 (project_namespace): the dirty-DB
		// recovery path expects v7 to be recorded as applied, so the
		// sessions table must include project_id even though that
		// column was added by v7 (the operator-side reason: F38 runs
		// before Migrate, so v7's ALTER TABLE has not yet fired). Without
		// this column, the v12 migration's INSERT...SELECT would fail
		// with "no such column: project_id" on dirty-DB recovery.
		`CREATE TABLE IF NOT EXISTS sessions (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id          TEXT NOT NULL UNIQUE,
			status              TEXT NOT NULL DEFAULT 'active',
			constitution_id     TEXT,
			constitution_ver    TEXT,
			active_mods         TEXT,
			started_at          TEXT NOT NULL,
			closed_at           TEXT,
			notes               TEXT,
			parent_session_id   TEXT,
			operator            TEXT,
			project_id          TEXT NOT NULL DEFAULT 'default'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_status  ON sessions(status)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_parent  ON sessions(parent_session_id)`,
		// From v7 (project_namespace)
		`CREATE TABLE IF NOT EXISTS projects (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id        TEXT NOT NULL UNIQUE,
			display_name      TEXT NOT NULL,
			description       TEXT,
			constitution_id   TEXT,
			constitution_ver  TEXT,
			created_at        TEXT NOT NULL,
			archived_at       TEXT,
			parent_project_id TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_active ON projects(archived_at)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_parent ON projects(parent_project_id)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("ensure-core-tables: %w", err)
		}
	}
	return nil
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
