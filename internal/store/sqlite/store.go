// Package sqlite is the SQLite implementation of 
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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
	migratesqlite "github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// newCanaryProxy returns a *safety.Holder that delegates to the
// externally-injected holder (typically from a test). The proxy is
// what the store.Store uses as its canary source.
func newCanaryProxy(ext *store.SafetyHolder) *safety.Holder {
	h := &safety.Holder{}
	if ext.Active != nil {
		if cur := ext.Active(); cur != "" {
			h.Set(safety.CanaryToken(cur))
		}
	}
	return h
}

// openSQLite opens a SQLite  Called by runtime.Open.
//
// Watchdog (INV-4): if cfg.ConstitutionFile is set, we hash the file
// and verify it against the stored sha256 in the constitutions table
// (after migrations). Mismatch returns store.ErrConstitutionDrift. First-run
// (no row yet) records the computed sha.
func openSQLite(ctx context.Context, cfg store.Config) (store.Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("%w: SQLite DSN (file path) required", store.ErrInvalidArgument)
	}
	busyMs := cfg.BusyTimeout.Milliseconds()
	if busyMs == 0 {
		busyMs = 5000
	}
	pragmaParts := []string{
		fmt.Sprintf("busy_timeout(%d)", busyMs),
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	}
	if cfg.WALMode {
		pragmaParts = append(pragmaParts, "journal_mode(WAL)")
	}
	dsn := fmt.Sprintf("file:%s?_pragma=%s", cfg.DSN, strings.Join(pragmaParts, "&_pragma="))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite ping: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, cfg: cfg, canary: buildSafetyHolder(cfg)}
	migrate.SetClock(func() string { return time.Now().UTC().Format(time.RFC3339Nano) })
	if err := migrate.Migrate(ctx, db, migratesqlite.Migrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	// W3-004 (T3): After migration v7, ensure the 'default' project row
	// exists. Backward compat — existing data (164+ specs) sits in
	// project_id='default' via the column DEFAULT, and SetActiveProject
	// special-cases 'default' so legacy callers work. Auto-seeding makes
	// the row materialise so ListProjects / GetProject('default') return
	// non-empty on first open. Idempotent via INSERT OR IGNORE.
	if err := s.ensureDefaultProject(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ensure default project: %w", err)
	}
	if err := s.runWatchdog(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// ensureDefaultProject is called from openSQLite after migrations.
// Idempotent: if a 'default' row already exists (e.g. second open of
// the same DB file), this is a no-op. Safe to call repeatedly.
func (s *Store) ensureDefaultProject(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE project_id = 'default'`).Scan(&n); err != nil {
		return fmt.Errorf("count default: %w", err)
	}
	if n > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO projects (project_id, display_name, created_at) VALUES ('default', 'Default Project', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert default: %w", err)
	}
	return nil
}

// buildSafetyHolder prefers cfg.Safety (test-injected) over a fresh
// default Holder. Provides a uniform shape both impls need.
func buildSafetyHolder(cfg store.Config) *safety.Holder {
	if cfg.Safety != nil && cfg.Safety.SetCanary != nil {
		return newCanaryProxy(cfg.Safety)
	}
	return &safety.Holder{}
}

// SetCanary installs a canary token (INV-3).
func (s *Store) SetCanary(token string) {
	s.canary.Set(safety.CanaryToken(token))
}

// SetActiveProject installs the project_id (INV-7) that the store.Store uses
// to filter every read and tag every write. Empty string clears and
// causes subsequent reads to return store.ErrSessionRequired.
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

// ActiveProject returns the currently installed project_id. Empty if
// none has been set.
func (s *Store) ActiveProject() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeProject
}

// requireProject returns store.ErrSessionRequired if no project is active.
// The store.Store refuses to read or write without an active project — that
// is INV-7's enforcement.
func (s *Store) requireProject() error {
	if s.ActiveProject() == "" {
		return fmt.Errorf("%w: no active project — call SetActiveProject first", store.ErrSessionRequired)
	}
	return nil
}

// projectIDOrActive returns wcProjectID if non-empty, otherwise the
// store.Store's active project. The Store refuses to write with no
// project at all (requireProject runs at the top of every Save*). This
// is the last-mile enforcement of INV-7.
func projectIDOrActive(wcProjectID, activeProject string) string {
	if wcProjectID != "" {
		return wcProjectID
	}
	return activeProject
}

// ActiveConstitution returns the active constitution's id, version, sha256.
// Returns empty strings if no constitution is registered.
func (s *Store) ActiveConstitution(ctx context.Context) (string, string, string) {
	var id, ver, sha string
	row := s.db.QueryRowContext(ctx,
		`SELECT constitution_id, version, sha256
		 FROM constitutions
		 WHERE enabled = 1
		 ORDER BY activated_at DESC, version DESC
		 LIMIT 1`)
	_ = row.Scan(&id, &ver, &sha)
	return id, ver, sha
}

// runWatchdog verifies the constitution file SHA256 against the stored
// value (INV-4). On mismatch: returns store.ErrConstitutionDrift. First run
// (no stored row): records the file's SHA and returns nil.
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
		// No active constitution ID configured — cannot verify; skip.
		return nil
	}
	var stored string
	err = s.db.QueryRowContext(ctx,
		`SELECT sha256 FROM constitutions WHERE constitution_id = ? AND enabled = 1 ORDER BY version DESC LIMIT 1`,
		s.cfg.ConstitutionID).Scan(&stored)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No stored constitution yet. Write the watchdog initial
			// row so subsequent Opens can detect drift.
			_, _ = s.db.ExecContext(ctx,
				`INSERT OR IGNORE INTO constitutions
				 (constitution_id, version, label, source, file_path, parsed_json, sha256, enabled, created_at, activated_at, last_verified_at, last_verified_sha256)
				 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
				s.cfg.ConstitutionID,
				s.cfg.ConstitutionVer,
				"watchdog-initial",
				"watchdog",
				s.cfg.ConstitutionFile,
				"{}",
				computed,
				time.Now().UTC().Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano),
				computed)
			return nil
		}
		return fmt.Errorf("watchdog: query stored sha: %w", err)
	}
	if stored != computed {
		return fmt.Errorf("%w: file=%s computed=%s stored=%s",
			store.ErrConstitutionDrift, s.cfg.ConstitutionFile, computed, stored)
	}
	// Healthy; update last_verified columns.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE constitutions
		 SET last_verified_at = ?, last_verified_sha256 = ?
		 WHERE constitution_id = ? AND version = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), computed,
		s.cfg.ConstitutionID, s.cfg.ConstitutionVer)
	return nil
}

// Store is the SQLite implementation of store.Store.
type Store struct {
	mu     sync.Mutex
	db     *sql.DB
	cfg    store.Config
	canary *safety.Holder

	// activeProject is the project_id used to filter every read and
	// tag every write (INV-7: project isolation). Empty means "no
	// project context"; reads return store.ErrSessionRequired. Set via
	// SetActiveProject. Defaults to "default" on first Open.
	activeProject string
}

// Compile-time assertion that *Store satisfies store.Store.
var _ store.Store = (*Store)(nil)

// ----- lifecycle -----

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) DriverName() string             { return string(store.DriverSQLite) }

// ----- migrations -----

func (s *Store) Migrate(ctx context.Context) error {
	return migrate.Migrate(ctx, s.db, migratesqlite.Migrations)
}
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return migrate.SchemaVersion(ctx, s.db)
}
func (s *Store) MigrationStatus(ctx context.Context) ([]store.MigrationStatus, error) {
	ms, err := migrate.MigrationStatus(ctx, s.db, migratesqlite.Migrations)
	if err != nil {
		return nil, err
	}
	out := make([]store.MigrationStatus, 0, len(ms))
	for _, m := range ms {
		out = append(out, store.MigrationStatus{Version: m.Version, Name: m.Name, Applied: m.Applied, AppliedAt: m.AppliedAt})
	}
	return out, nil
}

// ----- audit (INV-1) -----

func (s *Store) RecordWrite(ctx context.Context, ev audit.WriteEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordWriteLocked(ctx, ev, "")
}

func (s *Store) recordWriteLocked(ctx context.Context, ev audit.WriteEvent, contentHash string) error {
	if ev.CreatedAt == "" {
		ev.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if contentHash != "" && ev.ContentSHA256 == "" {
		ev.ContentSHA256 = contentHash
	}
	canary := 0
	if ev.CanaryPresent {
		canary = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO write_audit (table_name, row_id, actor, session_id, write_path,
		                           content_sha256, canary_present, constitution_id, constitution_ver, notes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.TableName, ev.RowID, ev.Actor, ev.SessionID, ev.WritePath,
		ev.ContentSHA256, canary, ev.ConstitutionID, ev.ConstitutionVer, ev.Notes, ev.CreatedAt)
	return err
}

func (s *Store) ListWrites(ctx context.Context, f audit.ListFilters) ([]audit.WriteEvent, error) {
	q := `SELECT id, table_name, row_id, actor, session_id, write_path,
	             COALESCE(content_sha256, ''), canary_present, COALESCE(constitution_id, ''), COALESCE(constitution_ver, ''), COALESCE(notes, ''), created_at
	      FROM write_audit WHERE 1=1`
	args := []any{}
	if f.Since != "" {
		q += ` AND created_at >= ?`
		args = append(args, f.Since)
	}
	if f.Actor != "" {
		q += ` AND actor = ?`
		args = append(args, f.Actor)
	}
	if f.WritePath != "" {
		q += ` AND write_path = ?`
		args = append(args, f.WritePath)
	}
	if f.SessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []audit.WriteEvent{}
	for rows.Next() {
		var ev audit.WriteEvent
		var canary int
		if err := rows.Scan(&ev.ID, &ev.TableName, &ev.RowID, &ev.Actor, &ev.SessionID, &ev.WritePath,
			&ev.ContentSHA256, &canary, &ev.ConstitutionID, &ev.ConstitutionVer, &ev.Notes, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.CanaryPresent = canary != 0
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ----- sessions -----

func (s *Store) SaveSession(ctx context.Context, wc store.WriteContext, sess *session.Session) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject()) // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.StartedAt == "" {
		sess.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if sess.Status == "" {
		sess.Status = string(session.StatusActive)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (session_id, status, constitution_id, constitution_ver, active_mods,
		                     started_at, closed_at, notes, parent_session_id, operator, project_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.SessionID, sess.Status, sess.ConstitutionID, sess.ConstitutionVer, sess.ActiveMods,
		sess.StartedAt, sess.ClosedAt, sess.Notes, sess.ParentSessionID, sess.Operator, projectID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	sess.ID = id
	return id, s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "sessions",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       sess.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       sess.StartedAt,
	}, "")
}

func (s *Store) GetSession(ctx context.Context, sessionID string) (*session.Session, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, status, constitution_id, constitution_ver, active_mods,
		        started_at, closed_at, notes, parent_session_id, operator
		 FROM sessions WHERE session_id = ? AND project_id = ?`, sessionID, activeProject)
	var sess session.Session
	var notes, closedAt sql.NullString
	if err := row.Scan(&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
		&sess.StartedAt, &closedAt, &notes, &sess.ParentSessionID, &sess.Operator); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if closedAt.Valid {
		sess.ClosedAt = closedAt.String
	}
	if notes.Valid {
		sess.Notes = notes.String
	}
	return &sess, nil
}

func (s *Store) CloseSession(ctx context.Context, wc store.WriteContext, sessionID string) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET status = ?, closed_at = ? WHERE session_id = ? AND project_id = ? AND status = ?`,
		string(session.StatusClosed), now, sessionID, activeProject, string(session.StatusActive))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "sessions",
		Actor:           wc.Actor,
		SessionID:       sessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       now,
	}, "")
}

func (s *Store) ListSessions(ctx context.Context, limit int) ([]session.Session, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, status, constitution_id, constitution_ver, active_mods,
		        started_at, closed_at, notes, parent_session_id, operator
		 FROM sessions WHERE project_id = ? ORDER BY id DESC LIMIT ?`, activeProject, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []session.Session{}
	for rows.Next() {
		var sess session.Session
		var notes, closedAt sql.NullString
		if err := rows.Scan(&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
			&sess.StartedAt, &closedAt, &notes, &sess.ParentSessionID, &sess.Operator); err != nil {
			return nil, err
		}
		if closedAt.Valid {
			sess.ClosedAt = closedAt.String
		}
		if notes.Valid {
			sess.Notes = notes.String
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ----- research -----

func (s *Store) SaveRun(ctx context.Context, wc store.WriteContext, run *research.ResearchRun) (int64, error) {
	// Extract project BEFORE taking the lock — ActiveProject() also locks.
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject())

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if run.CreatedAt == "" {
		run.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	btJSON, _ := json.Marshal(run.BackendsTried)
	errsJSON, _ := json.Marshal(run.Errors)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO research_runs (project_id, session_id, query, intent, backend_used, backends_tried,
		                           took_ms, confidence_avg, items_count, errors, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, nullStr(run.SessionID), run.Query, run.Intent, nullStr(run.BackendUsed), btJSON,
		run.TookMs, run.ConfidenceAvg, len(run.Items), errsJSON, run.CreatedAt)
	if err != nil {
		return 0, err
	}
	runID, err := res.LastInsertId()
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
		res, err := tx.ExecContext(ctx,
			`INSERT INTO research_items (project_id, run_id, title, url, snippet, source, confidence,
			                            freshness_at, lang, raw, actor, write_path, content_sha256, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, runID, item.Title, nullStr(item.URL), nullStr(item.Snippet), item.Source, item.Confidence,
			nullStr(item.FreshnessAt), nullStr(item.Lang), nullStr(item.Raw), wc.Actor, wc.WritePath, hash, item.CreatedAt)
		if err != nil {
			return 0, err
		}
		itemID, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		item.ID = itemID
		canary := 0
		if canaryPresent {
			canary = 1
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO write_audit (table_name, row_id, actor, session_id, write_path,
			                           content_sha256, canary_present, constitution_id, constitution_ver, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"research_items", itemID, wc.Actor, run.SessionID, wc.WritePath, hash, canary, wc.ConstitutionID, wc.ConstitutionVer, item.CreatedAt)
		if err != nil {
			return 0, err
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO write_audit (table_name, row_id, actor, session_id, write_path,
		                           constitution_id, constitution_ver, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"research_runs", runID, wc.Actor, run.SessionID, wc.WritePath, wc.ConstitutionID, wc.ConstitutionVer, run.CreatedAt)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return runID, nil
}

func (s *Store) GetRun(ctx context.Context, id int64) (*research.ResearchRun, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, query, intent, backend_used, backends_tried,
		        took_ms, confidence_avg, errors, created_at
		 FROM research_runs WHERE id = ? AND project_id = ?`, id, activeProject)
	var run research.ResearchRun
	var btJSON, errsJSON, sessionID, backendUsed sql.NullString
	if err := row.Scan(&run.ID, &sessionID, &run.Query, &run.Intent, &backendUsed, &btJSON,
		&run.TookMs, &run.ConfidenceAvg, &errsJSON, &run.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if sessionID.Valid {
		run.SessionID = sessionID.String
	}
	if backendUsed.Valid {
		run.BackendUsed = backendUsed.String
	}
	if btJSON.Valid && btJSON.String != "" {
		_ = json.Unmarshal([]byte(btJSON.String), &run.BackendsTried)
	}
	if errsJSON.Valid && errsJSON.String != "" {
		_ = json.Unmarshal([]byte(errsJSON.String), &run.Errors)
	}
	return &run, nil
}

func (s *Store) ListRuns(ctx context.Context, intent string, limit int) ([]research.ResearchRun, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	q := `SELECT id, session_id, query, intent, backend_used, backends_tried,
	             took_ms, confidence_avg, errors, created_at
	      FROM research_runs WHERE project_id = ?`
	args := []any{activeProject}
	if intent != "" {
		q += ` AND intent = ?`
		args = append(args, intent)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []research.ResearchRun{}
	for rows.Next() {
		var run research.ResearchRun
		var btJSON, errsJSON, sessionID, backendUsed sql.NullString
		if err := rows.Scan(&run.ID, &sessionID, &run.Query, &run.Intent, &backendUsed, &btJSON,
			&run.TookMs, &run.ConfidenceAvg, &errsJSON, &run.CreatedAt); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			run.SessionID = sessionID.String
		}
		if backendUsed.Valid {
			run.BackendUsed = backendUsed.String
		}
		if btJSON.Valid && btJSON.String != "" {
			_ = json.Unmarshal([]byte(btJSON.String), &run.BackendsTried)
		}
		if errsJSON.Valid && errsJSON.String != "" {
			_ = json.Unmarshal([]byte(errsJSON.String), &run.Errors)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Recall implements INV-2 (per-session scoping) and INV-7 (project isolation).
// Filters by s.activeProject at the Store level; SQLite has no RLS so this
// is the application-layer enforcement.
func (s *Store) Recall(ctx context.Context, opts research.RecallOptions) ([]research.Item, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
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
	           WHERE i.project_id = ? AND (LOWER(i.title) LIKE ? OR LOWER(i.snippet) LIKE ? OR LOWER(i.source) LIKE ?)`
	args := []any{activeProject, like, like, like}
	if opts.Intent != "" {
		sqlStr += ` AND r.intent = ?`
		args = append(args, opts.Intent)
	}
	if opts.Source != "" {
		sqlStr += ` AND i.source = ?`
		args = append(args, opts.Source)
	}
	if opts.SessionScope == research.SessionScopeSelf && opts.SessionID != "" {
		sqlStr += ` AND r.session_id = ?`
		args = append(args, opts.SessionID)
	}
	sqlStr += ` ORDER BY i.id DESC LIMIT ?`
	args = append(args, opts.Limit)
	rows, err := s.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []research.Item{}
	for rows.Next() {
		var it research.Item
		var urlNS, snippetNS, freshNS, langNS, rawNS, createdNS sql.NullString
		var conf sql.NullFloat64
		if err := rows.Scan(&it.ID, &it.RunID, &it.Title, &urlNS, &snippetNS, &it.Source,
			&conf, &freshNS, &langNS, &rawNS, &createdNS); err != nil {
			return nil, err
		}
		if urlNS.Valid {
			it.URL = urlNS.String
		}
		if snippetNS.Valid {
			it.Snippet = snippetNS.String
		}
		if conf.Valid {
			it.Confidence = float32(conf.Float64)
		}
		if freshNS.Valid {
			it.FreshnessAt = freshNS.String
		}
		if langNS.Valid {
			it.Lang = langNS.String
		}
		if rawNS.Valid {
			it.Raw = rawNS.String
		}
		if createdNS.Valid {
			it.CreatedAt = createdNS.String
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) ListItems(ctx context.Context, runID int64, source string, limit int) ([]research.Item, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, run_id, title, url, snippet, source, confidence,
	             freshness_at, lang, raw, created_at
	      FROM research_items WHERE project_id = ?`
	args := []any{activeProject}
	if runID > 0 {
		q += ` AND run_id = ?`
		args = append(args, runID)
	}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, source)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []research.Item{}
	for rows.Next() {
		var it research.Item
		var urlNS, snippetNS, freshNS, langNS, rawNS sql.NullString
		var conf sql.NullFloat64
		if err := rows.Scan(&it.ID, &it.RunID, &it.Title, &urlNS, &snippetNS, &it.Source,
			&conf, &freshNS, &langNS, &rawNS, &it.CreatedAt); err != nil {
			return nil, err
		}
		if urlNS.Valid {
			it.URL = urlNS.String
		}
		if snippetNS.Valid {
			it.Snippet = snippetNS.String
		}
		if conf.Valid {
			it.Confidence = float32(conf.Float64)
		}
		if freshNS.Valid {
			it.FreshnessAt = freshNS.String
		}
		if langNS.Valid {
			it.Lang = langNS.String
		}
		if rawNS.Valid {
			it.Raw = rawNS.String
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) LinkResearch(ctx context.Context, wc store.WriteContext, link *research.Link) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if link.CreatedAt == "" {
		link.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO research_links (research_item_id, target_type, target_id, note, source, confidence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		link.ResearchItemID, link.TargetType, link.TargetID, nullStr(link.Note), nullStr(link.Source), link.Confidence, link.CreatedAt)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	link.ID = id
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "research_links",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       link.CreatedAt,
	}, "")
}

func (s *Store) ResearchStatus(ctx context.Context) (*research.Status, error) {
	st := &research.Status{IntentHistogram: map[string]int{}, SourceHistogram: map[string]int{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_runs`).Scan(&st.RunsTotal); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_items`).Scan(&st.ItemsTotal); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_links`).Scan(&st.LinksTotal); err != nil {
		return nil, err
	}
	return st, nil
}

// ----- vibeflow implementations -----
// All Save* methods emit a write_audit row in the same lock acquisition.
// All read methods are concurrent-safe (no mutex needed; modernc/sqlite
// serves multiple readers via the connection pool).
//
// Note: append/array/json ops use COALESCE for portable NULL handling
// because SQLite scan into a typed struct field rejects NULL on a string.

// ----- specs -----

func (s *Store) SaveSpec(ctx context.Context, wc store.WriteContext, sp *vibeflow.Spec) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject()) // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp.CreatedAt == "" {
		sp.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if sp.UpdatedAt == "" {
		sp.UpdatedAt = sp.CreatedAt
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_specs (vibe_case, session_id, constitution_json, spec_json, tasks_json, created_at, updated_at, project_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sp.VibeCase, nullStr(sp.SessionID), nullStr(sp.Constitution), nullStr(sp.Spec), nullStr(sp.Tasks),
		sp.CreatedAt, sp.UpdatedAt, projectID)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	sp.ID = id
	return id, s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_specs",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       nullStrOr(sp.SessionID, wc.SessionID),
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       sp.CreatedAt,
	}, "")
}

func (s *Store) GetSpec(ctx context.Context, id int64) (*vibeflow.Spec, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, vibe_case, session_id, constitution_json, spec_json, tasks_json, created_at, updated_at
		 FROM vibe_specs WHERE id = ? AND project_id = ?`, id, activeProject)
	var sp vibeflow.Spec
	var sessionID, constitution, specJSON, tasks, updatedAt sql.NullString
	if err := row.Scan(&sp.ID, &sp.VibeCase, &sessionID, &constitution, &specJSON, &tasks, &sp.CreatedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if sessionID.Valid {
		sp.SessionID = sessionID.String
	}
	if constitution.Valid {
		sp.Constitution = constitution.String
	}
	if specJSON.Valid {
		sp.Spec = specJSON.String
	}
	if tasks.Valid {
		sp.Tasks = tasks.String
	}
	if updatedAt.Valid {
		sp.UpdatedAt = updatedAt.String
	}
	return &sp, nil
}

func (s *Store) UpdateSpec(ctx context.Context, wc store.WriteContext, id int64, sp *vibeflow.Spec) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE vibe_specs SET
			vibe_case         = COALESCE(NULLIF(?, ''), vibe_case),
			session_id        = COALESCE(NULLIF(?, ''), session_id),
			constitution_json = COALESCE(NULLIF(?, ''), constitution_json),
			spec_json         = COALESCE(NULLIF(?, ''), spec_json),
			tasks_json        = COALESCE(NULLIF(?, ''), tasks_json),
			updated_at        = ?
		 WHERE id = ? AND project_id = ?`,
		sp.VibeCase, sp.SessionID, sp.Constitution, sp.Spec, sp.Tasks, now, id, activeProject)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_specs",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       now,
	}, "")
}

func (s *Store) DeleteSpec(ctx context.Context, wc store.WriteContext, id int64) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM vibe_specs WHERE id = ? AND project_id = ?`, id, activeProject)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_specs",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}, "")
}

func (s *Store) ListSpecs(ctx context.Context, f vibeflow.SpecListFilters) ([]vibeflow.Spec, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q := `SELECT id, vibe_case, session_id, constitution_json, spec_json, tasks_json, created_at, updated_at
	      FROM vibe_specs WHERE project_id = ?`
	args := []any{activeProject}
	if f.VibeCase != "" {
		q += ` AND vibe_case = ?`
		args = append(args, f.VibeCase)
	}
	if f.SessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []vibeflow.Spec{}
	for rows.Next() {
		var sp vibeflow.Spec
		var sessionID, constitution, specJSON, tasks, updatedAt sql.NullString
		if err := rows.Scan(&sp.ID, &sp.VibeCase, &sessionID, &constitution, &specJSON, &tasks, &sp.CreatedAt, &updatedAt); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			sp.SessionID = sessionID.String
		}
		if constitution.Valid {
			sp.Constitution = constitution.String
		}
		if specJSON.Valid {
			sp.Spec = specJSON.String
		}
		if tasks.Valid {
			sp.Tasks = tasks.String
		}
		if updatedAt.Valid {
			sp.UpdatedAt = updatedAt.String
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ----- brand guides -----

func (s *Store) SaveBrandGuide(ctx context.Context, wc store.WriteContext, b *vibeflow.BrandGuide) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject()) // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.CreatedAt == "" {
		b.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_brands (brand_id, voice_json, visual_json, narrative_json, compliance_json, created_at, updated_at, project_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, brand_id) DO UPDATE SET
		   voice_json = excluded.voice_json,
		   visual_json = excluded.visual_json,
		   narrative_json = excluded.narrative_json,
		   compliance_json = excluded.compliance_json,
		   updated_at = excluded.updated_at`,
		b.BrandID, nullStr(b.Voice), nullStr(b.Visual), nullStr(b.Narrative), nullStr(b.Compliance),
		b.CreatedAt, b.UpdatedAt, projectID)
	if err != nil {
		return err
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_brands",
		RowID:           0,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       b.UpdatedAt,
		Notes:           "brand_id=" + b.BrandID + " project_id=" + projectID,
	}, "")
}

func (s *Store) GetBrandGuide(ctx context.Context, brandID string) (*vibeflow.BrandGuide, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT brand_id, voice_json, visual_json, narrative_json, compliance_json, created_at, updated_at
		 FROM vibe_brands WHERE brand_id = ? AND project_id = ?`, brandID, activeProject)
	var b vibeflow.BrandGuide
	var voice, visual, narrative, compliance, updatedAt sql.NullString
	if err := row.Scan(&b.BrandID, &voice, &visual, &narrative, &compliance, &b.CreatedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if voice.Valid {
		b.Voice = voice.String
	}
	if visual.Valid {
		b.Visual = visual.String
	}
	if narrative.Valid {
		b.Narrative = narrative.String
	}
	if compliance.Valid {
		b.Compliance = compliance.String
	}
	if updatedAt.Valid {
		b.UpdatedAt = updatedAt.String
	}
	return &b, nil
}

func (s *Store) DeleteBrandGuide(ctx context.Context, wc store.WriteContext, brandID string) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	// Path-safety: prevent slashes / NUL / whitespace in brand_id.
	if strings.ContainsAny(brandID, "/\\\x00\n\r") {
		return fmt.Errorf("%w: brand_id contains invalid characters", store.ErrInvalidArgument)
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM vibe_brands WHERE brand_id = ? AND project_id = ?`, brandID, activeProject)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_brands",
		RowID:           0,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Notes:           "brand_id=" + brandID + " project_id=" + activeProject,
	}, "")
}

func (s *Store) ListBrandGuides(ctx context.Context, limit int) ([]vibeflow.BrandGuide, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT brand_id, voice_json, visual_json, narrative_json, compliance_json, created_at, updated_at
		 FROM vibe_brands WHERE project_id = ? ORDER BY updated_at DESC, brand_id ASC LIMIT ?`, activeProject, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []vibeflow.BrandGuide{}
	for rows.Next() {
		var b vibeflow.BrandGuide
		var voice, visual, narrative, compliance, updatedAt sql.NullString
		if err := rows.Scan(&b.BrandID, &voice, &visual, &narrative, &compliance, &b.CreatedAt, &updatedAt); err != nil {
			return nil, err
		}
		if voice.Valid {
			b.Voice = voice.String
		}
		if visual.Valid {
			b.Visual = visual.String
		}
		if narrative.Valid {
			b.Narrative = narrative.String
		}
		if compliance.Valid {
			b.Compliance = compliance.String
		}
		if updatedAt.Valid {
			b.UpdatedAt = updatedAt.String
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ----- compliance rules -----
// IMPORTANT (spec 171 T4c): vibe_compliance is GLOBAL by jurisdiction.
// A rule for "EU" is visible from any project — jurisdiction is a
// property of law (GDPR is GDPR), not of project. Save / Get / List
// ignore project_id on purpose. If multi-jurisdiction-per-project
// isolation ever becomes a requirement, swap PK to (project_id,
// jurisdiction) via a follow-up migration; current behaviour is
// preserved here on purpose.

func (s *Store) SaveComplianceRule(ctx context.Context, wc store.WriteContext, r *vibeflow.ComplianceRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Jurisdiction == "" || r.Rules == "" {
		return fmt.Errorf("%w: jurisdiction and rules required", store.ErrInvalidArgument)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_compliance (jurisdiction, rules_json, effective_at, source_url, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(jurisdiction) DO UPDATE SET
		   rules_json = excluded.rules_json,
		   effective_at = excluded.effective_at,
		   source_url = excluded.source_url`,
		r.Jurisdiction, r.Rules, nullStr(r.EffectiveAt), nullStr(r.SourceURL), r.CreatedAt)
	if err != nil {
		return err
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_compliance",
		RowID:           0,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       r.CreatedAt,
		Notes:           "jurisdiction=" + r.Jurisdiction + " (global)",
	}, "")
}

func (s *Store) GetComplianceRule(ctx context.Context, jurisdiction string) (*vibeflow.ComplianceRule, error) {
	// Global by design — see T4c decision.
	row := s.db.QueryRowContext(ctx,
		`SELECT jurisdiction, rules_json, effective_at, source_url, created_at
		 FROM vibe_compliance WHERE jurisdiction = ?`, jurisdiction)
	var r vibeflow.ComplianceRule
	var rules, effectiveAt, sourceURL sql.NullString
	if err := row.Scan(&r.Jurisdiction, &rules, &effectiveAt, &sourceURL, &r.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if rules.Valid {
		r.Rules = rules.String
	}
	if effectiveAt.Valid {
		r.EffectiveAt = effectiveAt.String
	}
	if sourceURL.Valid {
		r.SourceURL = sourceURL.String
	}
	return &r, nil
}

func (s *Store) ListComplianceRules(ctx context.Context, limit int) ([]vibeflow.ComplianceRule, error) {
	// Global by design — see T4c decision.
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT jurisdiction, rules_json, effective_at, source_url, created_at
		 FROM vibe_compliance ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []vibeflow.ComplianceRule{}
	for rows.Next() {
		var r vibeflow.ComplianceRule
		var rules, effectiveAt, sourceURL sql.NullString
		if err := rows.Scan(&r.Jurisdiction, &rules, &effectiveAt, &sourceURL, &r.CreatedAt); err != nil {
			return nil, err
		}
		if rules.Valid {
			r.Rules = rules.String
		}
		if effectiveAt.Valid {
			r.EffectiveAt = effectiveAt.String
		}
		if sourceURL.Valid {
			r.SourceURL = sourceURL.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ----- artifacts -----

func (s *Store) SaveArtifact(ctx context.Context, wc store.WriteContext, a *vibeflow.Artifact) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject()) // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.CreatedAt == "" {
		a.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if a.ValidationStatus == "" {
		a.ValidationStatus = "pending"
	}
	hasDisclosure := 0
	if a.HasDisclosure {
		hasDisclosure = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_artifacts (session_id, vibe_case, spec_id, artifact_url, artifact_type,
		                           brand_id, jurisdiction, has_disclosure, validation_status, created_at, project_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullStr(a.SessionID), a.VibeCase, nullInt(a.SpecID), nullStr(a.ArtifactURL), a.ArtifactType,
		nullStr(a.BrandID), nullStr(a.Jurisdiction), hasDisclosure, a.ValidationStatus, a.CreatedAt, projectID)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	a.ID = id
	return id, s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_artifacts",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       nullStrOr(a.SessionID, wc.SessionID),
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       a.CreatedAt,
	}, "")
}

func (s *Store) GetArtifact(ctx context.Context, id int64) (*vibeflow.Artifact, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, vibe_case, spec_id, artifact_url, artifact_type,
		        brand_id, jurisdiction, has_disclosure, validation_status, created_at
		 FROM vibe_artifacts WHERE id = ? AND project_id = ?`, id, activeProject)
	return scanArtifact(row)
}

func (s *Store) UpdateArtifact(ctx context.Context, wc store.WriteContext, id int64, u *vibeflow.ArtifactUpdate) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	var hasDisclosure *int
	if u.HasDisclosure != nil {
		v := 0
		if *u.HasDisclosure {
			v = 1
		}
		hasDisclosure = &v
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE vibe_artifacts SET
			session_id        = COALESCE(?, session_id),
			spec_id           = COALESCE(?, spec_id),
			artifact_url      = COALESCE(?, artifact_url),
			brand_id          = COALESCE(?, brand_id),
			jurisdiction      = COALESCE(?, jurisdiction),
			has_disclosure    = COALESCE(?, has_disclosure),
			validation_status = COALESCE(?, validation_status)
		 WHERE id = ? AND project_id = ?`,
		u.SessionID, u.SpecID, u.ArtifactURL, u.BrandID, u.Jurisdiction, hasDisclosure, u.ValidationStatus, id, activeProject)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_artifacts",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}, "")
}

func (s *Store) DeleteArtifact(ctx context.Context, wc store.WriteContext, id int64) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM vibe_artifacts WHERE id = ? AND project_id = ?`, id, activeProject)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_artifacts",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}, "")
}

func (s *Store) ListArtifacts(ctx context.Context, f vibeflow.ArtifactListFilters) ([]vibeflow.Artifact, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q := `SELECT id, session_id, vibe_case, spec_id, artifact_url, artifact_type,
	             brand_id, jurisdiction, has_disclosure, validation_status, created_at
	      FROM vibe_artifacts WHERE project_id = ?`
	args := []any{activeProject}
	if f.VibeCase != "" {
		q += ` AND vibe_case = ?`
		args = append(args, f.VibeCase)
	}
	if f.BrandID != "" {
		q += ` AND brand_id = ?`
		args = append(args, f.BrandID)
	}
	if f.Jurisdiction != "" {
		q += ` AND jurisdiction = ?`
		args = append(args, f.Jurisdiction)
	}
	if f.SessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	if f.Status != "" {
		q += ` AND validation_status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []vibeflow.Artifact{}
	for rows.Next() {
		var a vibeflow.Artifact
		var sessionID, artifactURL, brandID, jurisdiction, validationStatus sql.NullString
		var specID, hasDisclosure sql.NullInt64
		if err := rows.Scan(&a.ID, &sessionID, &a.VibeCase, &specID, &artifactURL, &a.ArtifactType,
			&brandID, &jurisdiction, &hasDisclosure, &validationStatus, &a.CreatedAt); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			a.SessionID = sessionID.String
		}
		if specID.Valid {
			a.SpecID = specID.Int64
		}
		if artifactURL.Valid {
			a.ArtifactURL = artifactURL.String
		}
		if brandID.Valid {
			a.BrandID = brandID.String
		}
		if jurisdiction.Valid {
			a.Jurisdiction = jurisdiction.String
		}
		if validationStatus.Valid {
			a.ValidationStatus = validationStatus.String
		}
		if hasDisclosure.Valid && hasDisclosure.Int64 != 0 {
			a.HasDisclosure = true
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetArtifactValidation(ctx context.Context, wc store.WriteContext, id int64, status string) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`UPDATE vibe_artifacts SET validation_status = ? WHERE id = ? AND project_id = ?`,
		status, id, activeProject)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_artifacts",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Notes:           "validation_status=" + status,
	}, "")
}

// scanArtifact scans a single row into an Artifact.
func scanArtifact(row *sql.Row) (*vibeflow.Artifact, error) {
	var a vibeflow.Artifact
	var sessionID, artifactURL, brandID, jurisdiction, validationStatus sql.NullString
	var specID, hasDisclosure sql.NullInt64
	if err := row.Scan(&a.ID, &sessionID, &a.VibeCase, &specID, &artifactURL, &a.ArtifactType,
		&brandID, &jurisdiction, &hasDisclosure, &validationStatus, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if sessionID.Valid {
		a.SessionID = sessionID.String
	}
	if specID.Valid {
		a.SpecID = specID.Int64
	}
	if artifactURL.Valid {
		a.ArtifactURL = artifactURL.String
	}
	if brandID.Valid {
		a.BrandID = brandID.String
	}
	if jurisdiction.Valid {
		a.Jurisdiction = jurisdiction.String
	}
	if validationStatus.Valid {
		a.ValidationStatus = validationStatus.String
	}
	if hasDisclosure.Valid && hasDisclosure.Int64 != 0 {
		a.HasDisclosure = true
	}
	return &a, nil
}

// ----- drift reports -----

func (s *Store) SaveDriftReport(ctx context.Context, wc store.WriteContext, d *vibeflow.DriftReport) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.CreatedAt == "" {
		d.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_drift_reports (artifact_id, spec_id, verdict, spec_diff_json, judge_reasoning, reconciled_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ArtifactID, nullInt(d.SpecID), d.Verdict, nullStr(d.SpecDiff), nullStr(d.JudgeReasoning),
		nullStr(d.ReconciledAt), d.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	d.ID = id
	return id, s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "vibe_drift_reports",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       d.CreatedAt,
	}, "")
}

func (s *Store) LatestDriftForArtifact(ctx context.Context, artifactID int64) (*vibeflow.DriftReport, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, artifact_id, spec_id, verdict, spec_diff_json, judge_reasoning, reconciled_at, created_at
		 FROM vibe_drift_reports WHERE artifact_id = ? ORDER BY id DESC LIMIT 1`, artifactID)
	return scanDriftReport(row)
}

func (s *Store) ListDriftReports(ctx context.Context, artifactID int64, verdict string, limit int) ([]vibeflow.DriftReport, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, artifact_id, spec_id, verdict, spec_diff_json, judge_reasoning, reconciled_at, created_at
	      FROM vibe_drift_reports WHERE 1=1`
	args := []any{}
	if artifactID > 0 {
		q += ` AND artifact_id = ?`
		args = append(args, artifactID)
	}
	if verdict != "" {
		q += ` AND verdict = ?`
		args = append(args, verdict)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []vibeflow.DriftReport{}
	for rows.Next() {
		d, err := scanDriftReportRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func scanDriftReport(row *sql.Row) (*vibeflow.DriftReport, error) {
	var d vibeflow.DriftReport
	var specID, specDiff, reasoning, reconciled sql.NullString
	if err := row.Scan(&d.ID, &d.ArtifactID, &specID, &d.Verdict, &specDiff, &reasoning, &reconciled, &d.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if specID.Valid {
		d.SpecID = parseIntString(specID.String)
	}
	if specDiff.Valid {
		d.SpecDiff = specDiff.String
	}
	if reasoning.Valid {
		d.JudgeReasoning = reasoning.String
	}
	if reconciled.Valid {
		d.ReconciledAt = reconciled.String
	}
	return &d, nil
}

func scanDriftReportRows(rows *sql.Rows) (*vibeflow.DriftReport, error) {
	var d vibeflow.DriftReport
	var specID, specDiff, reasoning, reconciled sql.NullString
	if err := rows.Scan(&d.ID, &d.ArtifactID, &specID, &d.Verdict, &specDiff, &reasoning, &reconciled, &d.CreatedAt); err != nil {
		return nil, err
	}
	if specID.Valid {
		d.SpecID = parseIntString(specID.String)
	}
	if specDiff.Valid {
		d.SpecDiff = specDiff.String
	}
	if reasoning.Valid {
		d.JudgeReasoning = reasoning.String
	}
	if reconciled.Valid {
		d.ReconciledAt = reconciled.String
	}
	return &d, nil
}

// ----- sdd evaluations -----

func (s *Store) SaveSDDEvaluation(ctx context.Context, wc store.WriteContext, e *ssd.SDDEvaluation) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sdd_evaluations
		 (eval_type, target_type, target_id, verdict_json, confidence,
		  prompt_version, model, created_at,
		  constitution_id, constitution_version, active_mods_json,
		  refused_attempts, refusal_pattern)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.EvalType, e.TargetType, e.TargetID, e.VerdictJSON, e.Confidence,
		nullStr(e.PromptVersion), nullStr(e.Model), e.CreatedAt,
		nullStr(e.ConstitutionID), nullStr(e.ConstitutionVersion), nullStr(e.ActiveModsJSON),
		e.RefusedAttempts, nullStr(e.RefusalPattern))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	e.ID = id
	return id, s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "sdd_evaluations",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       e.CreatedAt,
	}, "")
}

func (s *Store) LatestSDDEvaluation(ctx context.Context, evalType, targetType, targetID string) (*ssd.SDDEvaluation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, eval_type, target_type, target_id, verdict_json, confidence,
		        prompt_version, model, created_at,
		        constitution_id, constitution_version, active_mods_json,
		        refused_attempts, refusal_pattern
		 FROM sdd_evaluations
		 WHERE eval_type = ? AND target_type = ? AND target_id = ?
		 ORDER BY id DESC LIMIT 1`, evalType, targetType, targetID)
	return scanSDDEval(row)
}

func (s *Store) ListSDDEvaluations(ctx context.Context, f ssd.ListFilters) ([]ssd.SDDEvaluation, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	q := `SELECT id, eval_type, target_type, target_id, verdict_json, confidence,
	             prompt_version, model, created_at,
	             constitution_id, constitution_version, active_mods_json,
	             refused_attempts, refusal_pattern
	      FROM sdd_evaluations WHERE 1=1`
	args := []any{}
	if f.EvalType != "" {
		q += ` AND eval_type = ?`
		args = append(args, f.EvalType)
	}
	if f.TargetType != "" {
		q += ` AND target_type = ?`
		args = append(args, f.TargetType)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ssd.SDDEvaluation{}
	for rows.Next() {
		e, err := scanSDDEvalRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func scanSDDEval(row *sql.Row) (*ssd.SDDEvaluation, error) {
	var e ssd.SDDEvaluation
	var promptVersion, model, consID, consVer, activeModsJSON, refusalPattern sql.NullString
	if err := row.Scan(&e.ID, &e.EvalType, &e.TargetType, &e.TargetID, &e.VerdictJSON, &e.Confidence,
		&promptVersion, &model, &e.CreatedAt,
		&consID, &consVer, &activeModsJSON, &e.RefusedAttempts, &refusalPattern); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if promptVersion.Valid {
		e.PromptVersion = promptVersion.String
	}
	if model.Valid {
		e.Model = model.String
	}
	if consID.Valid {
		e.ConstitutionID = consID.String
	}
	if consVer.Valid {
		e.ConstitutionVersion = consVer.String
	}
	if activeModsJSON.Valid {
		e.ActiveModsJSON = activeModsJSON.String
	}
	if refusalPattern.Valid {
		e.RefusalPattern = refusalPattern.String
	}
	return &e, nil
}

func scanSDDEvalRows(rows *sql.Rows) (*ssd.SDDEvaluation, error) {
	var e ssd.SDDEvaluation
	var promptVersion, model, consID, consVer, activeModsJSON, refusalPattern sql.NullString
	if err := rows.Scan(&e.ID, &e.EvalType, &e.TargetType, &e.TargetID, &e.VerdictJSON, &e.Confidence,
		&promptVersion, &model, &e.CreatedAt,
		&consID, &consVer, &activeModsJSON, &e.RefusedAttempts, &refusalPattern); err != nil {
		return nil, err
	}
	if promptVersion.Valid {
		e.PromptVersion = promptVersion.String
	}
	if model.Valid {
		e.Model = model.String
	}
	if consID.Valid {
		e.ConstitutionID = consID.String
	}
	if consVer.Valid {
		e.ConstitutionVersion = consVer.String
	}
	if activeModsJSON.Valid {
		e.ActiveModsJSON = activeModsJSON.String
	}
	if refusalPattern.Valid {
		e.RefusalPattern = refusalPattern.String
	}
	return &e, nil
}

// ----- constitution -----

func (s *Store) SaveConstitution(ctx context.Context, wc store.WriteContext, c *constitution.Constitution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.CreatedAt == "" {
		c.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if c.ActivatedAt == "" {
		c.ActivatedAt = c.CreatedAt
	}
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO constitutions (constitution_id, version, label, source, file_path,
		                           parsed_json, sha256, enabled, created_at, activated_at,
		                           last_verified_at, last_verified_sha256)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)
		 ON CONFLICT(constitution_id, version) DO UPDATE SET
		   label = excluded.label,
		   source = excluded.source,
		   file_path = excluded.file_path,
		   parsed_json = excluded.parsed_json,
		   sha256 = excluded.sha256,
		   enabled = excluded.enabled,
		   activated_at = excluded.activated_at`,
		c.ConstitutionID, c.Version, nullStr(c.Label), c.Source, c.FilePath,
		c.ParsedJSON, c.SHA256, enabled, c.CreatedAt, c.ActivatedAt)
	if err != nil {
		return err
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "constitutions",
		RowID:           0,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  c.ConstitutionID,
		ConstitutionVer: c.Version,
		CreatedAt:       c.CreatedAt,
		Notes:           "constitution_id=" + c.ConstitutionID + "@" + c.Version,
	}, "")
}

func (s *Store) GetConstitution(ctx context.Context, constitutionID, version string) (*constitution.Constitution, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, constitution_id, version, label, source, file_path, parsed_json, sha256,
		        enabled, created_at, activated_at
		 FROM constitutions
		 WHERE constitution_id = ? AND version = ?`, constitutionID, version)
	return scanConstitution(row)
}

func (s *Store) ListConstitutions(ctx context.Context, limit int) ([]constitution.Constitution, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, constitution_id, version, label, source, file_path, parsed_json, sha256,
		        enabled, created_at, activated_at
		 FROM constitutions
		 ORDER BY activated_at DESC, version DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []constitution.Constitution{}
	for rows.Next() {
		c, err := scanConstitutionRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func scanConstitution(row *sql.Row) (*constitution.Constitution, error) {
	var c constitution.Constitution
	var label, activatedAt sql.NullString
	var enabled int
	if err := row.Scan(&c.ID, &c.ConstitutionID, &c.Version, &label, &c.Source, &c.FilePath, &c.ParsedJSON,
		&c.SHA256, &enabled, &c.CreatedAt, &activatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if label.Valid {
		c.Label = label.String
	}
	if activatedAt.Valid {
		c.ActivatedAt = activatedAt.String
	}
	c.Enabled = enabled != 0
	return &c, nil
}

func scanConstitutionRows(rows *sql.Rows) (*constitution.Constitution, error) {
	var c constitution.Constitution
	var label, activatedAt sql.NullString
	var enabled int
	if err := rows.Scan(&c.ID, &c.ConstitutionID, &c.Version, &label, &c.Source, &c.FilePath, &c.ParsedJSON,
		&c.SHA256, &enabled, &c.CreatedAt, &activatedAt); err != nil {
		return nil, err
	}
	if label.Valid {
		c.Label = label.String
	}
	if activatedAt.Valid {
		c.ActivatedAt = activatedAt.String
	}
	c.Enabled = enabled != 0
	return &c, nil
}

func (s *Store) VerifyConstitutionHash(ctx context.Context, constitutionID, sha256Hash string) (bool, error) {
	var stored string
	err := s.db.QueryRowContext(ctx,
		`SELECT sha256 FROM constitutions WHERE constitution_id = ? ORDER BY version DESC LIMIT 1`, constitutionID).Scan(&stored)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return stored == sha256Hash, nil
}

// ----- mods -----

func (s *Store) SaveMod(ctx context.Context, wc store.WriteContext, m *mods.Mod) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	requiresTor := 0
	if m.RequiresTor {
		requiresTor = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mods (mod_id, name, version, source, manifest_json, sha256,
		                 risk_class, target_scope, requires_tor, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(mod_id) DO UPDATE SET
		   name = excluded.name,
		   version = excluded.version,
		   source = excluded.source,
		   manifest_json = excluded.manifest_json,
		   sha256 = excluded.sha256,
		   risk_class = excluded.risk_class,
		   target_scope = excluded.target_scope,
		   requires_tor = excluded.requires_tor,
		   updated_at = excluded.updated_at`,
		m.ModID, m.Name, m.Version, m.Source, m.ManifestJSON, m.SHA256,
		nullStr(m.RiskClass), nullStr(m.TargetScope), requiresTor, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return err
	}
	return s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "mods",
		RowID:           0,
		Actor:           wc.Actor,
		SessionID:       wc.SessionID,
		WritePath:       wc.WritePath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       m.UpdatedAt,
		Notes:           "mod_id=" + m.ModID,
	}, "")
}

func (s *Store) GetMod(ctx context.Context, modID string) (*mods.Mod, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, mod_id, name, version, source, manifest_json, sha256, risk_class,
		        target_scope, requires_tor, created_at, updated_at
		 FROM mods WHERE mod_id = ?`, modID)
	return scanMod(row)
}

func (s *Store) ListMods(ctx context.Context, limit int) ([]mods.Mod, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, mod_id, name, version, source, manifest_json, sha256, risk_class,
		        target_scope, requires_tor, created_at, updated_at
		 FROM mods ORDER BY updated_at DESC, mod_id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mods.Mod{}
	for rows.Next() {
		m, err := scanModRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func scanMod(row *sql.Row) (*mods.Mod, error) {
	var m mods.Mod
	var riskClass, targetScope, updatedAt sql.NullString
	var requiresTor int
	if err := row.Scan(&m.ID, &m.ModID, &m.Name, &m.Version, &m.Source, &m.ManifestJSON, &m.SHA256,
		&riskClass, &targetScope, &requiresTor, &m.CreatedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if riskClass.Valid {
		m.RiskClass = riskClass.String
	}
	if targetScope.Valid {
		m.TargetScope = targetScope.String
	}
	if updatedAt.Valid {
		m.UpdatedAt = updatedAt.String
	}
	m.RequiresTor = requiresTor != 0
	return &m, nil
}

func scanModRows(rows *sql.Rows) (*mods.Mod, error) {
	var m mods.Mod
	var riskClass, targetScope, updatedAt sql.NullString
	var requiresTor int
	if err := rows.Scan(&m.ID, &m.ModID, &m.Name, &m.Version, &m.Source, &m.ManifestJSON, &m.SHA256,
		&riskClass, &targetScope, &requiresTor, &m.CreatedAt, &updatedAt); err != nil {
		return nil, err
	}
	if riskClass.Valid {
		m.RiskClass = riskClass.String
	}
	if targetScope.Valid {
		m.TargetScope = targetScope.String
	}
	if updatedAt.Valid {
		m.UpdatedAt = updatedAt.String
	}
	m.RequiresTor = requiresTor != 0
	return &m, nil
}

func (s *Store) RecordModLoad(ctx context.Context, wc store.WriteContext, load *mods.ModLoad) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if load.LoadedAt == "" {
		load.LoadedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO mod_loads (mod_id, session_id, loaded_at, duration_ms,
		                       capabilities_count, error, constitution_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		load.ModID, nullStr(load.SessionID), load.LoadedAt, load.DurationMs,
		load.CapabilitiesCount, nullStr(load.Error), nullStr(load.ConstitutionID))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, s.recordWriteLocked(ctx, audit.WriteEvent{
		TableName:       "mod_loads",
		RowID:           id,
		Actor:           wc.Actor,
		SessionID:       nullStrOr(load.SessionID, wc.SessionID),
		WritePath:       wc.WritePath,
		ConstitutionID:  load.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		CreatedAt:       load.LoadedAt,
		Notes:           "mod_id=" + load.ModID,
	}, "")
}

func (s *Store) ListModLoads(ctx context.Context, modID string, limit int) ([]mods.ModLoad, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, mod_id, session_id, loaded_at, duration_ms, capabilities_count, error, constitution_id
	      FROM mod_loads WHERE 1=1`
	args := []any{}
	if modID != "" {
		q += ` AND mod_id = ?`
		args = append(args, modID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mods.ModLoad{}
	for rows.Next() {
		var l mods.ModLoad
		var sessionID, errMsg, consID sql.NullString
		if err := rows.Scan(&l.ID, &l.ModID, &sessionID, &l.LoadedAt, &l.DurationMs,
			&l.CapabilitiesCount, &errMsg, &consID); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			l.SessionID = sessionID.String
		}
		if errMsg.Valid {
			l.Error = errMsg.String
		}
		if consID.Valid {
			l.ConstitutionID = consID.String
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Vacuum(ctx context.Context, policy store.VacuumPolicy) (store.VacuumStats, error) {
	stats := store.VacuumStats{Duration: time.Now().UTC().Format(time.RFC3339Nano)}
	if !policy.DryRun {
		if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (s *Store) Stats(ctx context.Context) (*store.Stats, error) {
	out := &store.Stats{Driver: s.DriverName(), Open: true}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		out.Tables = append(out.Tables, name)
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&out.SchemaVersion)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_runs`).Scan(&out.RunsTotal)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_items`).Scan(&out.ItemsTotal)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_links`).Scan(&out.LinksTotal)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vibe_specs`).Scan(&out.SpecsTotal)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vibe_artifacts`).Scan(&out.ArtifactsTotal)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vibe_drift_reports`).Scan(&out.DriftReportsTotal)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sdd_evaluations`).Scan(&out.SDDEvaluations)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM write_audit`).Scan(&out.WriteAuditTotal)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE status = 'active'`).Scan(&out.SessionsActive)
	return out, nil
}

// ----- projects (INV-7) -----

func (s *Store) CreateProject(ctx context.Context, p *project.Project) error {
	if p == nil || p.ProjectID == "" || p.DisplayName == "" {
		return fmt.Errorf("%w: project_id and display_name required", store.ErrInvalidArgument)
	}
	if err := validateProjectID(p.ProjectID); err != nil {
		return err
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   display_name = excluded.display_name,
		   description = excluded.description,
		   constitution_id = excluded.constitution_id,
		   constitution_ver = excluded.constitution_ver,
		   parent_project_id = excluded.parent_project_id,
		   archived_at = NULL`,
		p.ProjectID, p.DisplayName, nullStr(p.Description), nullStr(p.ConstitutionID), nullStr(p.ConstitutionVer),
		p.CreatedAt, nullStr(p.ArchivedAt), nullStr(p.ParentProjectID))
	if err != nil {
		return err
	}
	// Seed a 'default' project if this is the first project and 'default' doesn't exist.
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE project_id = 'default'`).Scan(&n)
	if p.ProjectID != "default" && n == 0 {
		_, _ = s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO projects (project_id, display_name, created_at) VALUES ('default', 'Default Project', ?)`,
			time.Now().UTC().Format(time.RFC3339Nano))
	}
	return nil
}

func (s *Store) GetProject(ctx context.Context, projectID string) (*project.Project, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id
		 FROM projects WHERE project_id = ?`, projectID)
	var p project.Project
	var desc, consID, consVer, archived, parent sql.NullString
	if err := row.Scan(&p.ID, &p.ProjectID, &p.DisplayName, &desc, &consID, &consVer, &p.CreatedAt, &archived, &parent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if desc.Valid {
		p.Description = desc.String
	}
	if consID.Valid {
		p.ConstitutionID = consID.String
	}
	if consVer.Valid {
		p.ConstitutionVer = consVer.String
	}
	if archived.Valid {
		p.ArchivedAt = archived.String
	}
	if parent.Valid {
		p.ParentProjectID = parent.String
	}
	return &p, nil
}

func (s *Store) ListProjects(ctx context.Context, limit int) ([]project.Project, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id
		 FROM projects
		 WHERE archived_at IS NULL
		 ORDER BY created_at DESC, project_id ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []project.Project{}
	for rows.Next() {
		var p project.Project
		var desc, consID, consVer, archived, parent sql.NullString
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.DisplayName, &desc, &consID, &consVer, &p.CreatedAt, &archived, &parent); err != nil {
			return nil, err
		}
		if desc.Valid {
			p.Description = desc.String
		}
		if consID.Valid {
			p.ConstitutionID = consID.String
		}
		if consVer.Valid {
			p.ConstitutionVer = consVer.String
		}
		if archived.Valid {
			p.ArchivedAt = archived.String
		}
		if parent.Valid {
			p.ParentProjectID = parent.String
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ArchiveProject(ctx context.Context, projectID string) error {
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET archived_at = COALESCE(archived_at, ?) WHERE project_id = ?`, now, projectID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// validateProjectID enforces the same rules as brand_id: no slashes,
// no NUL, no whitespace. Keeps the project_id safe in path-shaped
// contexts and in the dark_memory_project_get URL handler.
func validateProjectID(s string) error {
	if s == "" {
		return fmt.Errorf("%w: project_id is empty", store.ErrInvalidArgument)
	}
	if strings.ContainsAny(s, "/\\\x00\n\r") {
		return fmt.Errorf("%w: project_id contains invalid characters", store.ErrInvalidArgument)
	}
	return nil
}

// ----- helpers -----

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt returns nil for 0 so the DB stores NULL cleanly.
func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// nullStrOr returns s1 if non-empty, else s2. Used to derive the write_audit
// row's session_id from the resource being audited (vibeflow artifacts carry
// their own session_id; audit rows fall back to wc.SessionID when missing).
func nullStrOr(s1, s2 string) string {
	if s1 != "" {
		return s1
	}
	return s2
}

// parseIntString converts a string-typed integer column to int64.
// Used for nullable BIGINT columns where the driver reports a string.
// Zero on parse error.
func parseIntString(s string) int64 {
	if s == "" {
		return 0
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
