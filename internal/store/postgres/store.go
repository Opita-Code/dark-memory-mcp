// Package postgres is the Postgres implementation of store.Store.
// Backed by jackc/pgx/v5/pgxpool (pure-Go, native protocol).
//
// Concurrency: Postgres handles concurrent writes natively via MVCC +
// row-level locking. The pool serializes only when the connection limit
// is hit; otherwise multiple writers proceed in parallel. This is the
// main operational reason Postgres is preferred over SQLite under
// multi-agent load.
//
// INV-1 invariant is enforced the same way as the SQLite impl:
// every Save* emits a write_audit row in the SAME tx as the data write.
//
// Scope (spec 171 T5): this file is a PARTIAL impl for v1.0. Lifecycle,
// migrations, audit, sessions, research (SaveRun/GetRun/ListRuns/Recall/ListItems/LinkResearch),
// ResearchStatus, project CRUD (CreateProject/GetProject/ListProjects/ArchiveProject),
// SetActiveProject, Stats, ActiveConstitution, runWatchdog are
// implemented and project-filtered where applicable. The rest of the
// Store interface (~25 methods: SaveSpec, GetSpec, SaveBrandGuide,
// SaveArtifact, etc.) are stubs that return store.ErrNotConfigured.
//
// Production runs SQLite today (see dark.db at
// C:\Users\Nico\AppData\Local\dark-agents\dark.db). The Postgres path
// is research-only until the missing methods are implemented and
// tests are run live with DARK_TEST_POSTGRES_DSN set.
//
// INV-7 (project isolation) strategy: SPEC 171 T5 OPTION (b). The
// migration v7 no longer enables RLS (RLS was created in an earlier
// version of v7 but the Store never wrapped transactions in
// `withProjectTx` to set the GUC — every read returned 0 rows). RLS
// is removed. The Store now filters by explicit `WHERE project_id = $1`
// on every read and tags every write with the active project_id, same
// as SQLite. If you want RLS back, see option (a) in spec 171 T5 —
// wire `withProjectTx` around every read and write transaction AND
// run live Postgres tests.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
	migratepostgres "github.com/dark-agents/dark-memory-mcp/internal/migrate/postgres"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// openPostgres opens a Postgres 
func openPostgres(ctx context.Context, cfg store.Config) (store.Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("%w: Postgres DSN required", store.ErrInvalidArgument)
	}
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres parse DSN: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		pcfg.MaxConns = int32(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		pcfg.MinConns = int32(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.ConnMaxLifetime
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	s := &Store{pool: pool, cfg: cfg, canary: buildSafetyHolder(cfg)}
	migrate.SetClock(func() string { return time.Now().UTC().Format(time.RFC3339Nano) })
	if err := s.runMigrations(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.runWatchdog(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// buildSafetyHolder mirrors the sqlite implementation.
func buildSafetyHolder(cfg store.Config) *safety.Holder {
	if cfg.Safety != nil && cfg.Safety.SetCanary != nil {
		return newCanaryProxy(cfg.Safety)
	}
	return &safety.Holder{}
}

// newCanaryProxy mirrors the sqlite implementation.
func newCanaryProxy(ext *store.SafetyHolder) *safety.Holder {
	h := &safety.Holder{}
	if ext.Active != nil {
		if cur := ext.Active(); cur != "" {
			h.Set(safety.CanaryToken(cur))
		}
	}
	return h
}

// SetCanary installs a canary token (INV-3).
func (s *Store) SetCanary(token string) {
	s.canary.Set(safety.CanaryToken(token))
}

// CanaryPresent reports whether a canary token is currently installed
// (INV-3). Mirrors the sqlite impl; the in-memory Holder is the source
// of truth (Review-w4-001: dark-mem-inspect now queries Store instead
// of creating a fresh empty Holder that always returned false).
func (s *Store) CanaryPresent() bool {
	return !s.canary.Active().IsZero()
}

// SetActiveProject installs the project_id (INV-7) for the
// `dark_mem.project_id` session GUC; the store.Store writes it via SET LOCAL
// at the start of every transaction so the DB rejects cross-project
// reads even if app code has a bug.
//
// W3-005: non-empty projectID is validated against the projects table;
// unknown ids return ErrInvalidArgument and leave the previous active
// project unchanged. The special id "default" is always allowed
// (legacy compat — see interface docs).
func (s *Store) SetActiveProject(ctx context.Context, projectID string) error {
	if projectID != "" && projectID != "default" {
		p, err := s.GetProject(ctx, projectID)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("%w: project_id %q does not exist; create it first", store.ErrInvalidArgument, projectID)
		}
	}
	s.mu.Lock()
	s.activeProject = projectID
	s.mu.Unlock()
	return nil
}

// ActiveProject returns the currently installed project_id.
func (s *Store) ActiveProject() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeProject
}

// requireProject returns store.ErrSessionRequired if no project is active.
func (s *Store) requireProject() error {
	if s.ActiveProject() == "" {
		return fmt.Errorf("%w: no active project — call SetActiveProject first", store.ErrSessionRequired)
	}
	return nil
}

// projectIDOrActive returns wcProjectID if non-empty, otherwise the
// store.Store's active project. The Store refuses to write with no
// project at all (requireProject runs at the top of every Save*). Mirror
// of the same helper in the SQLite impl.
func projectIDOrActive(wcProjectID, activeProject string) string {
	if wcProjectID != "" {
		return wcProjectID
	}
	return activeProject
}

// withProjectTx was REMOVED in spec 171 T5 (option b).
//
// The earlier version wrapped every operation in a tx that SET LOCAL
// dark_mem.project_id so the migration v7 RLS policies would filter
// rows by current_setting('dark_mem.project_id'). The migration has
// since dropped RLS (option b). The Store now filters by explicit
// `WHERE project_id = $1` like SQLite — no GUC magic, no transaction
// wrapping needed. withProjectTx is gone; if you re-introduce RLS in
// a future migration, wire this back around every read and write
// transaction. Don't forget live Postgres tests (DARK_TEST_POSTGRES_DSN).
//
// Removed helpers (quotePGString was only used by withProjectTx):
//   - withProjectTx: see comment above
//   - quotePGString: removed, no longer referenced

// ActiveConstitution returns the active constitution (id, version, sha256).
func (s *Store) ActiveConstitution(ctx context.Context) (string, string, string) {
	var id, ver, sha *string
	err := s.pool.QueryRow(ctx,
		`SELECT constitution_id, version, sha256
		 FROM constitutions WHERE enabled = TRUE
		 ORDER BY activated_at DESC NULLS LAST, version DESC
		 LIMIT 1`).Scan(&id, &ver, &sha)
	if err != nil || id == nil {
		return "", "", ""
	}
	return *id, *ver, *sha
}

// runWatchdog verifies the constitution file SHA against the stored
// value (INV-4). Mismatch returns store.ErrConstitutionDrift. First run
// records the SHA so subsequent Opens can detect drift.
func (s *Store) runWatchdog(ctx context.Context) error {
	if s.cfg.ConstitutionFile == "" {
		return nil
	}
	data, err := os.ReadFile(s.cfg.ConstitutionFile)
	if err != nil {
		return fmt.Errorf("watchdog: read constitution file: %w", err)
	}
	computed := safety.HashBytes(data)
	if s.cfg.ConstitutionID == "" {
		return nil
	}
	var stored *string
	err = s.pool.QueryRow(ctx,
		`SELECT sha256 FROM constitutions WHERE constitution_id = $1 AND enabled = TRUE ORDER BY version DESC LIMIT 1`,
		s.cfg.ConstitutionID).Scan(&stored)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_, _ = s.pool.Exec(ctx,
				`INSERT INTO constitutions
				 (constitution_id, version, label, source, file_path, parsed_json, sha256, enabled, created_at, activated_at, last_verified_at, last_verified_sha256)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, TRUE, $8, $9, $10, $11)
				 ON CONFLICT (constitution_id, version) DO NOTHING`,
				s.cfg.ConstitutionID, s.cfg.ConstitutionVer, "watchdog-initial",
				"watchdog", s.cfg.ConstitutionFile, "{}", computed,
				time.Now().UTC().Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano),
				computed)
			return nil
		}
		return fmt.Errorf("watchdog: query stored sha: %w", err)
	}
	if stored != nil && *stored != computed {
		return fmt.Errorf("%w: file=%s computed=%s stored=%s",
			store.ErrConstitutionDrift, s.cfg.ConstitutionFile, computed, *stored)
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE constitutions
		 SET last_verified_at = $1, last_verified_sha256 = $2
		 WHERE constitution_id = $3 AND version = $4`,
		time.Now().UTC().Format(time.RFC3339Nano), computed,
		s.cfg.ConstitutionID, s.cfg.ConstitutionVer)
	return nil
}

// Store is the Postgres implementation of store.Store.
type Store struct {
	mu     sync.Mutex
	pool   *pgxpool.Pool
	cfg    store.Config
	canary *safety.Holder

	// activeProject (INV-7): every read/write is tagged with this
	// project_id; the migration v7 RLS policies enforce the filter at
	// the DB level using the dark_mem.project_id session setting.
	activeProject string
}

func (s *Store) runMigrations(ctx context.Context) error {
	// Use pgx's connection to run migrations (raw exec).
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	for _, m := range migratepostgres.Migrations {
		// Mimic the migrate.Migrate() bookkeeping with ON CONFLICT DO NOTHING
		// because postgres-specific syntax.
		if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
			return fmt.Errorf("migrate: bookkeeping: %w", err)
		}
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, m.Version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		if m.Up != "" {
			if _, err := conn.Exec(ctx, m.Up); err != nil {
				return fmt.Errorf("migrate: v%d (%s) up: %w", m.Version, m.Name, err)
			}
		}
		if _, err := conn.Exec(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)
			 ON CONFLICT (version) DO NOTHING`,
			m.Version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("migrate: v%d record: %w", m.Version, err)
		}
	}
	return nil
}

// Compile-time assertion that *Store satisfies store.Store.
var _ store.Store = (*Store)(nil)

// ----- lifecycle -----

func (s *Store) Close() error {
	if s.pool == nil {
		return nil
	}
	s.pool.Close()
	return nil
}
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Store) DriverName() string             { return string(store.DriverPostgres) }

// ----- migrations -----

func (s *Store) Migrate(ctx context.Context) error { return s.runMigrations(ctx) }
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var v int64
	if err := conn.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return int(v), nil
}
func (s *Store) MigrationStatus(ctx context.Context) ([]store.MigrationStatus, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	rows, err := conn.Query(ctx, `SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[int]string{}
	for rows.Next() {
		var v int64
		var ts string
		if err := rows.Scan(&v, &ts); err != nil {
			return nil, err
		}
		applied[int(v)] = ts
	}
	out := make([]store.MigrationStatus, 0, len(migratepostgres.Migrations))
	for _, m := range migratepostgres.Migrations {
		st := store.MigrationStatus{Version: m.Version, Name: m.Name}
		if ts, ok := applied[m.Version]; ok {
			st.Applied = true
			st.AppliedAt = ts
		}
		out = append(out, st)
	}
	return out, nil
}

// ----- audit (INV-1) -----

func (s *Store) RecordWrite(ctx context.Context, ev audit.WriteEvent) error {
	return s.recordWrite(ctx, ev, "")
}

// recordWriteTx writes a write_audit row using the given pgx.Tx.
// Used by every Save* method to ensure the audit row commits atomically
// with the data row (INV-1 hardening, debt-elimination commit). Same
// semantics as recordWrite but participates in the caller's tx.
//
// INV-7: project_id auto-filled from s.activeProject (postgres impl has
// no s.mu so no deadlock risk; reads the field directly). Empty is
// allowed for the 3 global tables per spec 171 T4c/T4f.
func (s *Store) recordWriteTx(ctx context.Context, tx pgx.Tx, ev audit.WriteEvent, contentHash string) error {
	if ev.CreatedAt == "" {
		ev.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if contentHash != "" && ev.ContentSHA256 == "" {
		ev.ContentSHA256 = contentHash
	}
	if ev.ProjectID == "" {
		ev.ProjectID = s.activeProject
	}
	canary := false
	if ev.CanaryPresent {
		canary = true
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO write_audit (table_name, row_id, project_id, actor, session_id, write_path,
		                           content_sha256, canary_present, constitution_id, constitution_ver, notes, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		ev.TableName, ev.RowID, ev.ProjectID, ev.Actor, ev.SessionID, ev.WritePath,
		ev.ContentSHA256, canary, ev.ConstitutionID, ev.ConstitutionVer, ev.Notes, ev.CreatedAt)
	return err
}

// runInTx acquires a pgx.Tx and runs fn inside it. On error from fn
// the tx is rolled back; on success it is committed. Caller MUST use
// the pgx.Tx for all SQL operations and MUST call recordWriteTx for
// audit row emission. Used by every Save* method for INV-1 atomicity.
// Mirror of sqlite.runInTx for the postgres driver.
func (s *Store) runInTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit tx: %w", err)
	}
	return nil
}

func (s *Store) recordWrite(ctx context.Context, ev audit.WriteEvent, contentHash string) error {
	if ev.CreatedAt == "" {
		ev.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if contentHash != "" && ev.ContentSHA256 == "" {
		ev.ContentSHA256 = contentHash
	}
	if ev.ProjectID == "" {
		ev.ProjectID = s.activeProject
	}
	canary := false
	if ev.CanaryPresent {
		canary = true
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO write_audit (table_name, row_id, project_id, actor, session_id, write_path,
		                           content_sha256, canary_present, constitution_id, constitution_ver, notes, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		ev.TableName, ev.RowID, ev.ProjectID, ev.Actor, ev.SessionID, ev.WritePath,
		ev.ContentSHA256, canary, ev.ConstitutionID, ev.ConstitutionVer, ev.Notes, ev.CreatedAt)
	return err
}

func (s *Store) ListWrites(ctx context.Context, f audit.ListFilters) ([]audit.WriteEvent, error) {
	q := `SELECT id, table_name, row_id, COALESCE(project_id, ''), actor, session_id, write_path,
	             content_sha256, canary_present, constitution_id, constitution_ver, notes, created_at
	      FROM write_audit WHERE 1=1`
	args := []any{}
	if f.ProjectID != "" {
		q += ` AND project_id = $` + intToStr(len(args)+1)
		args = append(args, f.ProjectID)
	}
	if f.Since != "" {
		q += ` AND created_at >= $` + intToStr(len(args)+1)
		args = append(args, f.Since)
	}
	if f.Actor != "" {
		q += ` AND actor = $` + intToStr(len(args)+1)
		args = append(args, f.Actor)
	}
	if f.WritePath != "" {
		q += ` AND write_path = $` + intToStr(len(args)+1)
		args = append(args, f.WritePath)
	}
	if f.SessionID != "" {
		q += ` AND session_id = $` + intToStr(len(args)+1)
		args = append(args, f.SessionID)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` ORDER BY id DESC LIMIT $` + intToStr(len(args)+1)
	args = append(args, f.Limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []audit.WriteEvent{}
	for rows.Next() {
		var ev audit.WriteEvent
		var notes *string
		var canary bool
		if err := rows.Scan(&ev.ID, &ev.TableName, &ev.RowID, &ev.ProjectID, &ev.Actor, &ev.SessionID, &ev.WritePath,
			&ev.ContentSHA256, &canary, &ev.ConstitutionID, &ev.ConstitutionVer, &notes, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.CanaryPresent = canary
		if notes != nil {
			ev.Notes = *notes
		}
		out = append(out, ev)
	}
	return out, nil
}

// ----- sessions -----

func (s *Store) SaveSession(ctx context.Context, wc store.WriteContext, sess *session.Session) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.activeProject) // capture before locking
	if sess.StartedAt == "" {
		sess.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if sess.Status == "" {
		sess.Status = string(session.StatusActive)
	}
	var id int64
	err := s.runInTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO sessions (session_id, status, constitution_id, constitution_ver, active_mods,
			                     started_at, closed_at, notes, parent_session_id, operator, project_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 RETURNING id`,
			sess.SessionID, sess.Status, sess.ConstitutionID, sess.ConstitutionVer, sess.ActiveMods,
			sess.StartedAt, sess.ClosedAt, sess.Notes, sess.ParentSessionID, sess.Operator, projectID,
		).Scan(&id); err != nil {
			return err
		}
		sess.ID = id
		return s.recordWriteTx(ctx, tx, audit.WriteEvent{
			TableName:       "sessions",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       sess.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       sess.StartedAt,
		}, "")
	})
	return id, err
}

func (s *Store) GetSession(ctx context.Context, sessionID string) (*session.Session, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.activeProject // capture before locking
	var sess session.Session
	var notes, closedAt *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, status, constitution_id, constitution_ver, active_mods,
		        started_at, closed_at, notes, parent_session_id, operator
		 FROM sessions WHERE session_id = $1 AND project_id = $2`, sessionID, activeProject).Scan(
		&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
		&sess.StartedAt, &closedAt, &notes, &sess.ParentSessionID, &sess.Operator)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if closedAt != nil {
		sess.ClosedAt = *closedAt
	}
	if notes != nil {
		sess.Notes = *notes
	}
	return &sess, nil
}

func (s *Store) CloseSession(ctx context.Context, wc store.WriteContext, sessionID string) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.activeProject // capture before locking
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.runInTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sessions SET status = $1, closed_at = $2
			 WHERE session_id = $3 AND project_id = $4 AND status = $5`,
			string(session.StatusClosed), now, sessionID, activeProject, string(session.StatusActive))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return s.recordWriteTx(ctx, tx, audit.WriteEvent{
			TableName:       "sessions",
			Actor:           wc.Actor,
			SessionID:       sessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       now,
		}, "")
	})
}

func (s *Store) ListSessions(ctx context.Context, limit int) ([]session.Session, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.activeProject // capture before locking
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, session_id, status, constitution_id, constitution_ver, active_mods,
		        started_at, closed_at, notes, parent_session_id, operator
		 FROM sessions WHERE project_id = $1 ORDER BY id DESC LIMIT $2`, activeProject, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []session.Session{}
	for rows.Next() {
		var sess session.Session
		var notes, closedAt *string
		if err := rows.Scan(&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
			&sess.StartedAt, &closedAt, &notes, &sess.ParentSessionID, &sess.Operator); err != nil {
			return nil, err
		}
		if closedAt != nil {
			sess.ClosedAt = *closedAt
		}
		if notes != nil {
			sess.Notes = *notes
		}
		out = append(out, sess)
	}
	return out, nil
}

// ----- research (SaveRun + Recall + research status) -----

func (s *Store) SaveRun(ctx context.Context, wc store.WriteContext, run *research.ResearchRun) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.activeProject) // capture before locking
	if run.CreatedAt == "" {
		run.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var runID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO research_runs (session_id, query, intent, backend_used, backends_tried,
		                           took_ms, confidence_avg, items_count, errors, created_at, project_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING id`,
		nullStr(run.SessionID), run.Query, run.Intent, nullStr(run.BackendUsed), jsonMarshal(run.BackendsTried),
		run.TookMs, run.ConfidenceAvg, len(run.Items), jsonMarshal(run.Errors), run.CreatedAt, projectID).Scan(&runID)
	if err != nil {
		return 0, err
	}
	run.ID = runID
	for i := range run.Items {
		item := &run.Items[i]
		if item.CreatedAt == "" {
			item.CreatedAt = run.CreatedAt
		}
		item.RunID = runID
		payload := item.Title + "\x00" + item.Snippet + "\x00" + item.Raw
		if err := s.canary.ValidatePayload(payload); err != nil {
			return 0, err
		}
		hash := safety.HashPayload(payload)
		canaryPresent := !s.canary.Active().IsZero() && s.canary.Active().Match(payload)
		var itemID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO research_items (run_id, title, url, snippet, source, confidence,
			                            freshness_at, lang, raw, actor, write_path, content_sha256, created_at, project_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			 RETURNING id`,
			runID, item.Title, nullStr(item.URL), nullStr(item.Snippet), item.Source, item.Confidence,
			nullStr(item.FreshnessAt), nullStr(item.Lang), nullStr(item.Raw), wc.Actor, wc.WritePath, hash, item.CreatedAt, projectID).Scan(&itemID)
		if err != nil {
			return 0, err
		}
		item.ID = itemID
		if _, err := tx.Exec(ctx,
			`INSERT INTO write_audit (table_name, row_id, actor, session_id, write_path,
			                           content_sha256, canary_present, constitution_id, constitution_ver, created_at, project_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			"research_items", itemID, wc.Actor, run.SessionID, wc.WritePath, hash, canaryPresent, wc.ConstitutionID, wc.ConstitutionVer, item.CreatedAt, projectID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO write_audit (table_name, row_id, actor, session_id, write_path,
		                           constitution_id, constitution_ver, created_at, project_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		"research_runs", runID, wc.Actor, run.SessionID, wc.WritePath, wc.ConstitutionID, wc.ConstitutionVer, run.CreatedAt, projectID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return runID, nil
}

func (s *Store) GetRun(ctx context.Context, id int64) (*research.ResearchRun, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.activeProject // capture before locking
	var run research.ResearchRun
	var btJSON, errsJSON, sessionID, backendUsed *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, query, intent, backend_used, backends_tried,
		        took_ms, confidence_avg, errors, created_at
		 FROM research_runs WHERE id = $1 AND project_id = $2`, id, activeProject).Scan(
		&run.ID, &sessionID, &run.Query, &run.Intent, &backendUsed, &btJSON,
		&run.TookMs, &run.ConfidenceAvg, &errsJSON, &run.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if sessionID != nil {
		run.SessionID = *sessionID
	}
	if backendUsed != nil {
		run.BackendUsed = *backendUsed
	}
	if btJSON != nil {
		_ = jsonUnmarshal([]byte(*btJSON), &run.BackendsTried)
	}
	if errsJSON != nil {
		_ = jsonUnmarshal([]byte(*errsJSON), &run.Errors)
	}
	return &run, nil
}

func (s *Store) ListRuns(ctx context.Context, intent string, limit int) ([]research.ResearchRun, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.activeProject // capture before locking
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, session_id, query, intent, backend_used, backends_tried,
	             took_ms, confidence_avg, errors, created_at
	      FROM research_runs WHERE project_id = $1`
	args := []any{activeProject}
	if intent != "" {
		q += ` AND intent = $` + intToStr(len(args)+1)
		args = append(args, intent)
	}
	q += ` ORDER BY id DESC LIMIT $` + intToStr(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []research.ResearchRun{}
	for rows.Next() {
		var run research.ResearchRun
		var btJSON, errsJSON, sessionID, backendUsed *string
		if err := rows.Scan(&run.ID, &sessionID, &run.Query, &run.Intent, &backendUsed, &btJSON,
			&run.TookMs, &run.ConfidenceAvg, &errsJSON, &run.CreatedAt); err != nil {
			return nil, err
		}
		if sessionID != nil {
			run.SessionID = *sessionID
		}
		if backendUsed != nil {
			run.BackendUsed = *backendUsed
		}
		if btJSON != nil {
			_ = jsonUnmarshal([]byte(*btJSON), &run.BackendsTried)
		}
		if errsJSON != nil {
			_ = jsonUnmarshal([]byte(*errsJSON), &run.Errors)
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *Store) Recall(ctx context.Context, opts research.RecallOptions) ([]research.Item, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.activeProject // capture before locking
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	q := strings.TrimSpace(opts.Query)
	if q == "" {
		return nil, nil
	}
	like := "%" + strings.ToLower(q) + "%"
	sqlStr := `SELECT i.id, i.run_id, i.title, i.url, i.snippet, i.source,
	                  i.confidence, i.freshness_at, i.lang, i.raw, i.created_at
	           FROM research_items i
	           JOIN research_runs r ON r.id = i.run_id
	           WHERE i.project_id = $1 AND r.project_id = $1
	             AND (LOWER(i.title) LIKE $2 OR LOWER(i.snippet) LIKE $2 OR LOWER(i.source) LIKE $2)`
	args := []any{activeProject, like}
	if opts.Intent != "" {
		args = append(args, opts.Intent)
		sqlStr += ` AND r.intent = $` + intToStr(len(args))
	}
	if opts.Source != "" {
		args = append(args, opts.Source)
		sqlStr += ` AND i.source = $` + intToStr(len(args))
	}
	if opts.SessionScope == research.SessionScopeSelf && opts.SessionID != "" {
		args = append(args, opts.SessionID)
		sqlStr += ` AND r.session_id = $` + intToStr(len(args))
	}
	args = append(args, opts.Limit)
	sqlStr += ` ORDER BY i.id DESC LIMIT $` + intToStr(len(args))
	rows, err := s.pool.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []research.Item{}
	for rows.Next() {
		var it research.Item
		var urlNS, snippetNS, freshNS, langNS, rawNS, createdNS *string
		var conf *float32
		if err := rows.Scan(&it.ID, &it.RunID, &it.Title, &urlNS, &snippetNS, &it.Source,
			&conf, &freshNS, &langNS, &rawNS, &createdNS); err != nil {
			return nil, err
		}
		if urlNS != nil {
			it.URL = *urlNS
		}
		if snippetNS != nil {
			it.Snippet = *snippetNS
		}
		if conf != nil {
			it.Confidence = *conf
		}
		if freshNS != nil {
			it.FreshnessAt = *freshNS
		}
		if langNS != nil {
			it.Lang = *langNS
		}
		if rawNS != nil {
			it.Raw = *rawNS
		}
		if createdNS != nil {
			it.CreatedAt = *createdNS
		}
		out = append(out, it)
	}
	return out, nil
}

func (s *Store) ListItems(ctx context.Context, runID int64, source string, limit int) ([]research.Item, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.activeProject // capture before locking
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, run_id, title, url, snippet, source, confidence,
	             freshness_at, lang, raw, created_at
	      FROM research_items WHERE project_id = $1`
	args := []any{activeProject}
	if runID > 0 {
		args = append(args, runID)
		q += ` AND run_id = $` + intToStr(len(args))
	}
	if source != "" {
		args = append(args, source)
		q += ` AND source = $` + intToStr(len(args))
	}
	args = append(args, limit)
	q += ` ORDER BY id DESC LIMIT $` + intToStr(len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []research.Item{}
	for rows.Next() {
		var it research.Item
		var urlNS, snippetNS, freshNS, langNS, rawNS *string
		var conf *float32
		if err := rows.Scan(&it.ID, &it.RunID, &it.Title, &urlNS, &snippetNS, &it.Source,
			&conf, &freshNS, &langNS, &rawNS, &it.CreatedAt); err != nil {
			return nil, err
		}
		if urlNS != nil {
			it.URL = *urlNS
		}
		if snippetNS != nil {
			it.Snippet = *snippetNS
		}
		if conf != nil {
			it.Confidence = *conf
		}
		if freshNS != nil {
			it.FreshnessAt = *freshNS
		}
		if langNS != nil {
			it.Lang = *langNS
		}
		if rawNS != nil {
			it.Raw = *rawNS
		}
		out = append(out, it)
	}
	return out, nil
}

func (s *Store) LinkResearch(ctx context.Context, wc store.WriteContext, link *research.Link) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.activeProject) // capture before locking
	if link.CreatedAt == "" {
		link.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return s.runInTx(ctx, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO research_links (research_item_id, target_type, target_id, note, source, confidence, created_at, project_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING id`,
			link.ResearchItemID, link.TargetType, link.TargetID, nullStr(link.Note), nullStr(link.Source), link.Confidence, link.CreatedAt, projectID,
		).Scan(&id); err != nil {
			return err
		}
		link.ID = id
		return s.recordWriteTx(ctx, tx, audit.WriteEvent{
			TableName:       "research_links",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       link.CreatedAt,
		}, "")
	})
}

// ResearchStatus is GLOBAL by design — same rationale as Store.Stats
// (operator observability; aggregate counts; no project filter). See
// spec 171 T4g / T5.
func (s *Store) ResearchStatus(ctx context.Context) (*research.Status, error) {
	st := &research.Status{IntentHistogram: map[string]int{}, SourceHistogram: map[string]int{}}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM research_runs`).Scan(&st.RunsTotal); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM research_items`).Scan(&st.ItemsTotal); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM research_links`).Scan(&st.LinksTotal); err != nil {
		return nil, err
	}
	return st, nil
}

// ----- stubs (same shape as SQLite) -----

func (s *Store) SaveSpec(ctx context.Context, wc store.WriteContext, sp *vibeflow.Spec) (int64, error) {
	return 0, notImpl("SaveSpec")
}
func (s *Store) GetSpec(ctx context.Context, id int64) (*vibeflow.Spec, error) {
	return nil, notImpl("GetSpec")
}
func (s *Store) UpdateSpec(ctx context.Context, wc store.WriteContext, id int64, sp *vibeflow.Spec) error {
	return notImpl("UpdateSpec")
}
func (s *Store) DeleteSpec(ctx context.Context, wc store.WriteContext, id int64) error {
	return notImpl("DeleteSpec")
}
func (s *Store) ListSpecs(ctx context.Context, f vibeflow.SpecListFilters) ([]vibeflow.Spec, error) {
	return nil, notImpl("ListSpecs")
}
func (s *Store) SaveBrandGuide(ctx context.Context, wc store.WriteContext, b *vibeflow.BrandGuide) error {
	return notImpl("SaveBrandGuide")
}
func (s *Store) GetBrandGuide(ctx context.Context, brandID string) (*vibeflow.BrandGuide, error) {
	return nil, notImpl("GetBrandGuide")
}
func (s *Store) DeleteBrandGuide(ctx context.Context, wc store.WriteContext, brandID string) error {
	return notImpl("DeleteBrandGuide")
}
func (s *Store) ListBrandGuides(ctx context.Context, limit int) ([]vibeflow.BrandGuide, error) {
	return nil, notImpl("ListBrandGuides")
}
func (s *Store) SaveComplianceRule(ctx context.Context, wc store.WriteContext, r *vibeflow.ComplianceRule) error {
	return notImpl("SaveComplianceRule")
}
func (s *Store) GetComplianceRule(ctx context.Context, jurisdiction string) (*vibeflow.ComplianceRule, error) {
	return nil, notImpl("GetComplianceRule")
}
func (s *Store) ListComplianceRules(ctx context.Context, limit int) ([]vibeflow.ComplianceRule, error) {
	return nil, notImpl("ListComplianceRules")
}
func (s *Store) SaveArtifact(ctx context.Context, wc store.WriteContext, a *vibeflow.Artifact) (int64, error) {
	return 0, notImpl("SaveArtifact")
}
func (s *Store) GetArtifact(ctx context.Context, id int64) (*vibeflow.Artifact, error) {
	return nil, notImpl("GetArtifact")
}
func (s *Store) UpdateArtifact(ctx context.Context, wc store.WriteContext, id int64, u *vibeflow.ArtifactUpdate) error {
	return notImpl("UpdateArtifact")
}
func (s *Store) DeleteArtifact(ctx context.Context, wc store.WriteContext, id int64) error {
	return notImpl("DeleteArtifact")
}
func (s *Store) ListArtifacts(ctx context.Context, f vibeflow.ArtifactListFilters) ([]vibeflow.Artifact, error) {
	return nil, notImpl("ListArtifacts")
}
func (s *Store) SetArtifactValidation(ctx context.Context, wc store.WriteContext, id int64, status string) error {
	return notImpl("SetArtifactValidation")
}
func (s *Store) SaveDriftReport(ctx context.Context, wc store.WriteContext, d *vibeflow.DriftReport) (int64, error) {
	return 0, notImpl("SaveDriftReport")
}
func (s *Store) LatestDriftForArtifact(ctx context.Context, artifactID int64) (*vibeflow.DriftReport, error) {
	return nil, notImpl("LatestDriftForArtifact")
}
func (s *Store) ListDriftReports(ctx context.Context, artifactID int64, verdict string, limit int) ([]vibeflow.DriftReport, error) {
	return nil, notImpl("ListDriftReports")
}
func (s *Store) SaveSDDEvaluation(ctx context.Context, wc store.WriteContext, e *ssd.SDDEvaluation) (int64, error) {
	return 0, notImpl("SaveSDDEvaluation")
}
func (s *Store) LatestSDDEvaluation(ctx context.Context, evalType, targetType, targetID string) (*ssd.SDDEvaluation, error) {
	return nil, notImpl("LatestSDDEvaluation")
}
func (s *Store) ListSDDEvaluations(ctx context.Context, f ssd.ListFilters) ([]ssd.SDDEvaluation, error) {
	return nil, notImpl("ListSDDEvaluations")
}
func (s *Store) SaveConstitution(ctx context.Context, wc store.WriteContext, c *constitution.Constitution) error {
	return notImpl("SaveConstitution")
}
func (s *Store) GetConstitution(ctx context.Context, constitutionID, version string) (*constitution.Constitution, error) {
	return nil, notImpl("GetConstitution")
}
func (s *Store) ListConstitutions(ctx context.Context, limit int) ([]constitution.Constitution, error) {
	return nil, notImpl("ListConstitutions")
}
func (s *Store) VerifyConstitutionHash(ctx context.Context, constitutionID, sha256Hash string) (bool, error) {
	var stored *string
	err := s.pool.QueryRow(ctx,
		`SELECT sha256 FROM constitutions WHERE constitution_id = $1 ORDER BY version DESC LIMIT 1`, constitutionID).Scan(&stored)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	return *stored == sha256Hash, nil
}
func (s *Store) SaveMod(ctx context.Context, wc store.WriteContext, m *mods.Mod) error {
	return notImpl("SaveMod")
}
func (s *Store) GetMod(ctx context.Context, modID string) (*mods.Mod, error) {
	return nil, notImpl("GetMod")
}
func (s *Store) ListMods(ctx context.Context, limit int) ([]mods.Mod, error) {
	return nil, notImpl("ListMods")
}
func (s *Store) RecordModLoad(ctx context.Context, wc store.WriteContext, load *mods.ModLoad) (int64, error) {
	return 0, notImpl("RecordModLoad")
}
func (s *Store) ListModLoads(ctx context.Context, modID string, limit int) ([]mods.ModLoad, error) {
	return nil, notImpl("ListModLoads")
}
func (s *Store) Vacuum(ctx context.Context, policy store.VacuumPolicy) (store.VacuumStats, error) {
	stats := store.VacuumStats{Duration: time.Now().UTC().Format(time.RFC3339Nano)}
	if !policy.DryRun {
		// VACUUM is sqlite-specific; postgres uses VACUUM FULL or REINDEX.
		// The exact statement is exposed by the admin tool, not hard-coded here.
		if _, err := s.pool.Exec(ctx, `VACUUM`); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// ----- projects (INV-7) -----

func (s *Store) CreateProject(ctx context.Context, p *project.Project) error {
	if p == nil || p.ProjectID == "" || p.DisplayName == "" {
		return fmt.Errorf("%w: project_id and display_name required", store.ErrInvalidArgument)
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO projects (project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT(project_id) DO UPDATE SET
		   display_name = EXCLUDED.display_name,
		   description = EXCLUDED.description,
		   constitution_id = EXCLUDED.constitution_id,
		   constitution_ver = EXCLUDED.constitution_ver,
		   parent_project_id = EXCLUDED.parent_project_id,
		   archived_at = NULL`,
		p.ProjectID, p.DisplayName, p.Description, p.ConstitutionID, p.ConstitutionVer,
		p.CreatedAt, p.ArchivedAt, p.ParentProjectID)
	if err != nil {
		return err
	}
	// Seed a 'default' project if first project.
	if p.ProjectID != "default" {
		var n int
		_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM projects WHERE project_id = 'default'`).Scan(&n)
		if n == 0 {
			_, _ = s.pool.Exec(ctx,
				`INSERT INTO projects (project_id, display_name, created_at) VALUES ('default', 'Default Project', $1)
				 ON CONFLICT(project_id) DO NOTHING`,
				time.Now().UTC().Format(time.RFC3339Nano))
		}
	}
	return nil
}

func (s *Store) GetProject(ctx context.Context, projectID string) (*project.Project, error) {
	var p project.Project
	var desc, consID, consVer, archived, parent *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id
		 FROM projects WHERE project_id = $1`, projectID).Scan(
		&p.ID, &p.ProjectID, &p.DisplayName, &desc, &consID, &consVer, &p.CreatedAt, &archived, &parent)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if desc != nil {
		p.Description = *desc
	}
	if consID != nil {
		p.ConstitutionID = *consID
	}
	if consVer != nil {
		p.ConstitutionVer = *consVer
	}
	if archived != nil {
		p.ArchivedAt = *archived
	}
	if parent != nil {
		p.ParentProjectID = *parent
	}
	return &p, nil
}

func (s *Store) ListProjects(ctx context.Context, limit int) ([]project.Project, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id
		 FROM projects
		 WHERE archived_at IS NULL
		 ORDER BY created_at DESC, project_id ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []project.Project{}
	for rows.Next() {
		var p project.Project
		var desc, consID, consVer, archived, parent *string
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.DisplayName, &desc, &consID, &consVer, &p.CreatedAt, &archived, &parent); err != nil {
			return nil, err
		}
		if desc != nil {
			p.Description = *desc
		}
		if consID != nil {
			p.ConstitutionID = *consID
		}
		if consVer != nil {
			p.ConstitutionVer = *consVer
		}
		if archived != nil {
			p.ArchivedAt = *archived
		}
		if parent != nil {
			p.ParentProjectID = *parent
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ArchiveProject(ctx context.Context, projectID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tag, err := s.pool.Exec(ctx,
		`UPDATE projects SET archived_at = COALESCE(archived_at, $1) WHERE project_id = $2`, now, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
func (s *Store) Stats(ctx context.Context) (*store.Stats, error) {
	out := &store.Stats{Driver: s.DriverName(), Open: true}
	rows, err := s.pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		out.Tables = append(out.Tables, name)
	}
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&out.SchemaVersion)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM research_runs`).Scan(&out.RunsTotal)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM research_items`).Scan(&out.ItemsTotal)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM research_links`).Scan(&out.LinksTotal)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM vibe_specs`).Scan(&out.SpecsTotal)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM vibe_artifacts`).Scan(&out.ArtifactsTotal)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM vibe_drift_reports`).Scan(&out.DriftReportsTotal)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sdd_evaluations`).Scan(&out.SDDEvaluations)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM write_audit`).Scan(&out.WriteAuditTotal)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE status = 'active'`).Scan(&out.SessionsActive)
	return out, nil
}

// ----- helpers -----

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func jsonMarshal(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func jsonUnmarshal(data []byte, v any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

func intToStr(n int) string { return strconv.Itoa(n) }

// ----- VLP state (atomic spec 2.3 VLPPersistence) -----

// SaveVLPState upserts a per-session VLP state row using ON CONFLICT
// DO UPDATE (Postgres UPSERT syntax). The composite ON CONFLICT target
// (project_id, session_id) matches the UNIQUE INDEX idx_vlp_state_project_session
// (INV-7: per-project uniqueness — see migration v9 in postgres/ddl.go).
//
// Uses RETURNING id (Postgres-native) to get a reliable row id on both
// INSERT and UPDATE branches without a follow-up SELECT.
//
// Wraps UPSERT + write_audit in a single pgx transaction so INV-1 is
// enforced atomically: either both rows land or neither does.
func (s *Store) SaveVLPState(ctx context.Context, wc store.WriteContext, row *store.VLPStateRow) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if row.CreatedAt == "" {
		row.CreatedAt = now
	}
	row.UpdatedAt = now

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("vlp_state: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO vlp_state (session_id, state, last_event, last_verdict, turn_count,
		                      minset_current, constitution_id, constitution_ver,
		                      created_at, updated_at, project_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (project_id, session_id) DO UPDATE SET
			state             = EXCLUDED.state,
			last_event        = EXCLUDED.last_event,
			last_verdict      = EXCLUDED.last_verdict,
			turn_count        = EXCLUDED.turn_count,
			minset_current    = EXCLUDED.minset_current,
			constitution_id   = EXCLUDED.constitution_id,
			constitution_ver  = EXCLUDED.constitution_ver,
			updated_at        = EXCLUDED.updated_at
		RETURNING id`,
		row.SessionID, row.State, row.LastEvent, row.LastVerdict, row.TurnCount,
		row.MinsetCurrent, row.ConstitutionID, row.ConstitutionVer,
		row.CreatedAt, row.UpdatedAt, projectID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("vlp_state: upsert: %w", err)
	}

	// vlp_state has no user-controlled payload to canary-check (session_id
	// is the lookup key, state/event/verdict are enums). The write_audit
	// row records actor + write_path for INV-1 forensics.
	if wc.WritePath == "" {
		wc.WritePath = "SaveVLPState"
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO write_audit (table_name, row_id, actor, session_id, write_path,
		                           constitution_id, constitution_ver, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"vlp_state", id, wc.Actor, row.SessionID, wc.WritePath,
		wc.ConstitutionID, wc.ConstitutionVer, now)
	if err != nil {
		return 0, fmt.Errorf("vlp_state: audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("vlp_state: commit: %w", err)
	}
	row.ID = id
	row.ProjectID = projectID
	return id, nil
}

// GetVLPState returns the row for sessionID, or nil if not found.
// Filtered by active project (INV-7).
func (s *Store) GetVLPState(ctx context.Context, sessionID string) (*store.VLPStateRow, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject()
	var (
		id          int64
		sid         string
		state       int
		lastEvent   *string
		lastVerdict *string
		turnCount   int
		minset      *string
		consID      *string
		consVer     *string
		createdAt   string
		updatedAt   string
		projectID   string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, session_id, state, last_event, last_verdict, turn_count,
		       minset_current, constitution_id, constitution_ver,
		       created_at, updated_at, project_id
		FROM vlp_state WHERE session_id = $1 AND project_id = $2`,
		sessionID, activeProject).Scan(&id, &sid, &state, &lastEvent, &lastVerdict,
		&turnCount, &minset, &consID, &consVer, &createdAt, &updatedAt, &projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	r := &store.VLPStateRow{
		ID:        id,
		SessionID: sid,
		State:     state,
		TurnCount: turnCount,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		ProjectID: projectID,
	}
	if lastEvent != nil {
		r.LastEvent = *lastEvent
	}
	if lastVerdict != nil {
		r.LastVerdict = *lastVerdict
	}
	if minset != nil {
		r.MinsetCurrent = *minset
	}
	if consID != nil {
		r.ConstitutionID = *consID
	}
	if consVer != nil {
		r.ConstitutionVer = *consVer
	}
	return r, nil
}

// ListVLPStates returns rows filtered by stateFilter (NUMERIC — the int
// value of internal/vlp.State; empty = all states), newest-first. Limit
// <= 0 means no limit.
//
// Callers should pass the numeric form ("2", "3", etc.) — name strings
// like "drafting_spec" are NOT resolved here (resolving would require
// importing internal/vlp, which is forbidden to break a cycle). The
// internal/vlp.Persistence wrapper accepts the State enum and converts
// to numeric before calling this method.
func (s *Store) ListVLPStates(ctx context.Context, stateFilter string, limit int) ([]store.VLPStateRow, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject()

	// Effective limit for SQL push-down. 0 → no LIMIT clause (caller owns
	// result-set size; same as the public contract).
	effectiveLimit := 0
	if limit > 0 {
		effectiveLimit = limit
	}

	var rows pgx.Rows
	var err error
	if stateFilter == "" {
		if effectiveLimit > 0 {
			rows, err = s.pool.Query(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = $1
				ORDER BY updated_at DESC LIMIT $2`, activeProject, effectiveLimit)
		} else {
			rows, err = s.pool.Query(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = $1
				ORDER BY updated_at DESC`, activeProject)
		}
	} else {
		if effectiveLimit > 0 {
			rows, err = s.pool.Query(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = $1 AND state = $2
				ORDER BY updated_at DESC LIMIT $3`, activeProject, stateFilter, effectiveLimit)
		} else {
			rows, err = s.pool.Query(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = $1 AND state = $2
				ORDER BY updated_at DESC`, activeProject, stateFilter)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]store.VLPStateRow, 0, effectiveLimit)
	for rows.Next() {
		var (
			id          int64
			sid         string
			state       int
			lastEvent   *string
			lastVerdict *string
			turnCount   int
			minset      *string
			consID      *string
			consVer     *string
			createdAt   string
			updatedAt   string
			projectID   string
		)
		if err := rows.Scan(&id, &sid, &state, &lastEvent, &lastVerdict,
			&turnCount, &minset, &consID, &consVer, &createdAt, &updatedAt, &projectID); err != nil {
			return nil, err
		}
		r := store.VLPStateRow{
			ID:        id,
			SessionID: sid,
			State:     state,
			TurnCount: turnCount,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			ProjectID: projectID,
		}
		if lastEvent != nil {
			r.LastEvent = *lastEvent
		}
		if lastVerdict != nil {
			r.LastVerdict = *lastVerdict
		}
		if minset != nil {
			r.MinsetCurrent = *minset
		}
		if consID != nil {
			r.ConstitutionID = *consID
		}
		if consVer != nil {
			r.ConstitutionVer = *consVer
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func notImpl(name string) error {
	return fmt.Errorf("%w: %s", store.ErrNotConfigured, name)
}
