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
	"log"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
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
	// F38 (v1.2.2): pre-create the core tables v5+ expect. CREATE
	// TABLE IF NOT EXISTS is a no-op when the table already exists;
	// it materialises a missing table when dark-memory-mcp boots
	// against a dark.db that was last touched by dark-research-mcp
	// (which uses a separate schema_migrations ledger and may have
	// left dark-memory-mcp's v5+ rows marked as applied without the
	// matching tables actually existing).
	if err := migrate.EnsureCoreTables(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ensure core tables: %w", err)
	}
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

// CanaryPresent reports whether a canary token is currently installed
// (INV-3). Cheap, lock-free-ish (RLock). Review-w4-001: previously
// dark-mem-inspect printed false even when the Store had a canary.
func (s *Store) CanaryPresent() bool {
	return !s.canary.Active().IsZero()
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

// ActiveConstitution is GLOBAL by design (see spec 171 T4g).
//
// Returns the active constitution's id, version, sha256. Used by
// runWatchdog (INV-4) to verify the constitution file has not drifted.
// The active constitution is a SYSTEM-level property: it defines
// the agent's posture, applies to every operation, and is shared
// across all projects. See spec 171 T4f — the `constitutions`
// table has no project_id column.
//
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
//
// Wave INFRA-003 v2: upgrade scenario. When the file's version (cfg)
// differs from the stored version AND the file's SHA differs from the
// stored SHA AND DARK_CONSTITUTION_ACCEPT_BUMPS=1, write a NEW row
// for the new version and disable the old one. Without the env var,
// return ErrConstitutionDrift with an enhanced message that names the
// env var (operator-facing migration step).
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
	// Look up both the stored SHA AND the stored version for the
	// upgrade detection below.
	var stored, storedVer string
	err = s.db.QueryRowContext(ctx,
		`SELECT sha256, version FROM constitutions WHERE constitution_id = ? AND enabled = 1 ORDER BY version DESC LIMIT 1`,
		s.cfg.ConstitutionID).Scan(&stored, &storedVer)
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
		// SHA mismatch. Two scenarios:
		//   (a) Version upgrade (storedVer != cfg.ConstitutionVer): operator
		//       is moving to a new constitution release. Acceptable with
		//       explicit opt-in (DARK_CONSTITUTION_ACCEPT_BUMPS=1).
		//   (b) File tampering (same version, different SHA): refuse
		//       unconditionally — this is the security path.
		isVersionBump := storedVer != "" && s.cfg.ConstitutionVer != "" && storedVer != s.cfg.ConstitutionVer
		if isVersionBump && os.Getenv("DARK_CONSTITUTION_ACCEPT_BUMPS") == "1" {
			// Migration step (Wave INFRA-003 v2): disable old row,
			// insert new row with the new SHA + version + activation
			// timestamp. Emits a write_audit row via the orchestrator
			// caller (the operator-visible event).
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_, _ = s.db.ExecContext(ctx,
				`UPDATE constitutions SET enabled = 0, last_verified_at = ? WHERE constitution_id = ? AND version = ?`,
				now, s.cfg.ConstitutionID, storedVer)
			_, err := s.db.ExecContext(ctx,
				`INSERT INTO constitutions
				 (constitution_id, version, label, source, file_path, parsed_json, sha256, enabled, created_at, activated_at, last_verified_at, last_verified_sha256)
				 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
				s.cfg.ConstitutionID,
				s.cfg.ConstitutionVer,
				"watchdog-upgrade",
				"watchdog",
				s.cfg.ConstitutionFile,
				"{}",
				computed,
				now, now, now, computed)
			if err != nil {
				return fmt.Errorf("watchdog: write upgrade row: %w", err)
			}
			log.Printf("dark-mem-mcp: constitution upgrade %s -> %s accepted (file_sha=%s env=DARK_CONSTITUTION_ACCEPT_BUMPS=1)",
				storedVer, s.cfg.ConstitutionVer, computed)
			return nil
		}
		// No opt-in (or same-version tamper): refuse with enhanced
		// message that names the env var when it's an upgrade scenario.
		if isVersionBump {
			return fmt.Errorf("%w: stored version=%s sha=%s, file version=%s sha=%s. To accept this upgrade, set DARK_CONSTITUTION_ACCEPT_BUMPS=1 in the server's environment and restart.",
				store.ErrConstitutionDrift, storedVer, stored, s.cfg.ConstitutionVer, computed)
		}
		return fmt.Errorf("%w: file=%s computed=%s stored=%s (version=%s; same-version tamper is refused unconditionally)",
			store.ErrConstitutionDrift, s.cfg.ConstitutionFile, computed, stored, storedVer)
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

// recordWriteLockedTx writes a write_audit row using the given *sql.Tx.
// Used by every Save* method to ensure the audit row commits atomically
// with the data row (INV-1 hardening, debt-elimination commit). Same
// semantics as recordWriteLocked but participates in the caller's tx.
//
// INV-7: project_id is auto-filled from s.activeProject (read without
// locking — caller already holds s.mu). Empty string is only allowed
// for the 3 global tables (vibe_compliance, constitutions, mods) per
// spec 171 T4c/T4f.
func (s *Store) recordWriteLockedTx(ctx context.Context, tx *sql.Tx, ev audit.WriteEvent, contentHash string) error {
	if ev.CreatedAt == "" {
		ev.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if contentHash != "" && ev.ContentSHA256 == "" {
		ev.ContentSHA256 = contentHash
	}
	if ev.ProjectID == "" {
		// Read s.activeProject WITHOUT locking — caller holds s.mu already
		// and ActiveProject() would deadlock on the re-entrant Lock().
		ev.ProjectID = s.activeProject
	}
	canary := 0
	if ev.CanaryPresent {
		canary = 1
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO write_audit (table_name, row_id, project_id, actor, session_id, write_path,
		                           content_sha256, canary_present, constitution_id, constitution_ver, notes, session_event, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.TableName, ev.RowID, ev.ProjectID, ev.Actor, ev.SessionID, ev.WritePath,
		ev.ContentSHA256, canary, ev.ConstitutionID, ev.ConstitutionVer, ev.Notes, ev.SessionEvent, ev.CreatedAt)
	return err
}

// runInTx acquires a *sql.Tx and runs fn inside it. On error from fn
// the tx is rolled back; on success it is committed. Caller MUST use
// the *sql.Tx for all SQL operations and MUST call recordWriteLockedTx
// for audit row emission (not recordWriteLocked). Used by every Save*
// method for INV-1 atomicity: data row + audit row land together or
// neither lands.
//
// Pattern (caller):
//
//	var id int64
//	err := s.runInTx(ctx, func(tx *sql.Tx) error {
//	    res, err := tx.ExecContext(ctx, `INSERT INTO x ...`)
//	    if err != nil { return err }
//	    id, _ = res.LastInsertId()
//	    return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{...}, "")
//	})
//	return id, err
func (s *Store) runInTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op if Commit succeeded
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit tx: %w", err)
	}
	return nil
}

func (s *Store) recordWriteLocked(ctx context.Context, ev audit.WriteEvent, contentHash string) error {
	if ev.CreatedAt == "" {
		ev.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if contentHash != "" && ev.ContentSHA256 == "" {
		ev.ContentSHA256 = contentHash
	}
	if ev.ProjectID == "" {
		// Read s.activeProject WITHOUT locking — caller already holds s.mu.
		ev.ProjectID = s.activeProject
	}
	canary := 0
	if ev.CanaryPresent {
		canary = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO write_audit (table_name, row_id, project_id, actor, session_id, write_path,
		                           content_sha256, canary_present, constitution_id, constitution_ver, notes, session_event, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.TableName, ev.RowID, ev.ProjectID, ev.Actor, ev.SessionID, ev.WritePath,
		ev.ContentSHA256, canary, ev.ConstitutionID, ev.ConstitutionVer, ev.Notes, ev.SessionEvent, ev.CreatedAt)
	return err
}

func (s *Store) ListWrites(ctx context.Context, f audit.ListFilters) ([]audit.WriteEvent, error) {
	q := `SELECT id, table_name, row_id, COALESCE(project_id, ''), actor, session_id, write_path,
	             COALESCE(content_sha256, ''), canary_present, COALESCE(constitution_id, ''), COALESCE(constitution_ver, ''), COALESCE(notes, ''), COALESCE(session_event, ''), created_at
	      FROM write_audit WHERE 1=1`
	args := []any{}
	if f.ProjectID != "" {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
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
	if f.SinceID > 0 {
		q += ` AND id > ?`
		args = append(args, f.SinceID)
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
		if err := rows.Scan(&ev.ID, &ev.TableName, &ev.RowID, &ev.ProjectID, &ev.Actor, &ev.SessionID, &ev.WritePath,
			&ev.ContentSHA256, &canary, &ev.ConstitutionID, &ev.ConstitutionVer, &ev.Notes, &ev.SessionEvent, &ev.CreatedAt); err != nil {
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
		sess.Status = string(session.StatusOpen)
	}
	if sess.LastHeartbeatAt == "" {
		// Newly-started sessions have last_heartbeat_at set to start
		// time. The next heartbeat call refreshes it; sweeps compare
		// against HEARTBEAT_TIMEOUT.
		sess.LastHeartbeatAt = sess.StartedAt
	}
	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		// closed_at must be NULL (not empty string) for open sessions
		// to satisfy the v12 CHECK constraint
		// `(status = 'open' AND closed_at IS NULL)`.
		var closedAt sql.NullString
		if sess.ClosedAt != "" {
			closedAt = sql.NullString{String: sess.ClosedAt, Valid: true}
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (session_id, status, constitution_id, constitution_ver, active_mods,
			                     started_at, closed_at, last_heartbeat_at, parent_session_id,
			                     resurrected_from, notes, operator, project_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sess.SessionID, sess.Status, sess.ConstitutionID, sess.ConstitutionVer, sess.ActiveMods,
			sess.StartedAt, closedAt, sess.LastHeartbeatAt, sess.ParentSessionID,
			sess.ResurrectedFrom, sess.Notes, sess.Operator, projectID,
			sess.StartedAt /* created_at proxy = started_at for new sessions */)
		if err != nil {
			return err
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		id = newID
		sess.ID = newID
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "sessions",
			RowID:           newID,
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
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, status, constitution_id, constitution_ver, active_mods,
		        operator, started_at, closed_at, last_heartbeat_at,
		        parent_session_id, resurrected_from, notes
		 FROM sessions WHERE session_id = ? AND project_id = ?`, sessionID, activeProject)
	var sess session.Session
	var notes, closedAt, heartbeat, parent, rescFrom sql.NullString
	if err := row.Scan(&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
		&sess.Operator, &sess.StartedAt, &closedAt, &heartbeat, &parent, &rescFrom, &notes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if closedAt.Valid {
		sess.ClosedAt = closedAt.String
	}
	if heartbeat.Valid {
		sess.LastHeartbeatAt = heartbeat.String
	}
	if parent.Valid {
		sess.ParentSessionID = parent.String
	}
	if rescFrom.Valid {
		sess.ResurrectedFrom = rescFrom.String
	}
	if notes.Valid {
		sess.Notes = notes.String
	}
	return &sess, nil
}

func (s *Store) CloseSession(ctx context.Context, wc store.WriteContext, sessionID, reason string) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	// Validate reason
	reasonClose, err := session.ParseCloseReason(reason)
	if err != nil {
		return err
	}
	dest := reasonClose.DestinationStatus()
	sessionEvent := session.SessionEventForReason(reasonClose)
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE sessions SET status = ?, closed_at = ?
			 WHERE session_id = ? AND project_id = ? AND status IN ('open','idle')`,
			string(dest), now, sessionID, activeProject)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "sessions",
			Actor:           wc.Actor,
			SessionID:       sessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			SessionEvent:    sessionEvent,
			CreatedAt:       now,
		}, "")
	})
}

// SaveHeartbeat refreshes sessions.last_heartbeat_at on an open
// or idle session. Used by the harness adapters (5D) and the periodic
// harness-side heartbeater (BRIDGE v2 §5.3). Returns ErrNotFound if
// the session doesn't exist OR isn't open/idle (i.e. closed or
// archived — refusing a heartbeat on a closed session is correct).
//
// The sweep (5E.iii) reads sessions.last_heartbeat_at; sessions whose
// last_heartbeat_at is older than HEARTBEAT_TIMEOUT are promoted to
// closed_aborted by either the sweeper or boot_reconcile.
//
// INV-9 heartbeat invariant.
func (s *Store) SaveHeartbeat(ctx context.Context, wc store.WriteContext, sessionID string) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE sessions SET last_heartbeat_at = ?
			 WHERE session_id = ? AND project_id = ? AND status IN ('open','idle')`,
			now, sessionID, activeProject)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "sessions",
			Actor:           wc.Actor,
			SessionID:       sessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			SessionEvent:    "heartbeat",
			CreatedAt:       now,
		}, "")
	})
}

// FindClosedAbortedForActor returns the most-recent closed_aborted
// session for the given operator within the lookback window
// (RFC3339, e.g. "24h"). Read-only — the caller (Recover orchestrator)
// decides whether to call SaveResurrect on the result. Returns
// (nil, nil) when no candidate exists. (Wave 5E.ii.)
//
// The query walks sessions.operator + project_id + status=closed_aborted
// + closed_at >= lookback, ordered most-recent first, limit 1.
func (s *Store) FindClosedAbortedForActor(ctx context.Context, actor, operator, projectID, lookback string) (*session.Session, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	// Defensive: pin project_id to the active project when none was
	// supplied. Operators shouldn't cross-project read others' sessions
	// (INV-7).
	if projectID == "" {
		projectID = s.ActiveProject()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, status, constitution_id, constitution_ver, active_mods,
		        operator, started_at, closed_at, last_heartbeat_at,
		        parent_session_id, resurrected_from, notes
		 FROM sessions
		 WHERE operator = ? AND project_id = ? AND status = 'closed_aborted'
		   AND closed_at >= ?
		 ORDER BY closed_at DESC LIMIT 1`,
		operator, projectID, lookback)
	var sess session.Session
	var notes, closedAt, heartbeat, parent, rescFrom sql.NullString
	if err := row.Scan(
		&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
		&sess.Operator, &sess.StartedAt, &closedAt, &heartbeat, &parent, &rescFrom, &notes,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if closedAt.Valid {
		sess.ClosedAt = closedAt.String
	}
	if heartbeat.Valid {
		sess.LastHeartbeatAt = heartbeat.String
	}
	if parent.Valid {
		sess.ParentSessionID = parent.String
	}
	if rescFrom.Valid {
		sess.ResurrectedFrom = rescFrom.String
	}
	if notes.Valid {
		sess.Notes = notes.String
	}
	return &sess, nil
}

// SaveResurrect creates a new sessions row representing the
// resurrection of a closed_aborted session. parent_session_id and
// resurrected_from are set; status is 'open'; started_at and
// last_heartbeat_at are now(). The original (closed) session is NOT
// touched — it stays closed_aborted in the audit history.
//
// (Wave 5E.ii + 5E.iv.b.) Returns the newly-created session row
// (with both ID and SessionID populated) so the caller can resume
// without a follow-up read. The INSERT + LastInsertId + write_audit
// are wrapped in a single transaction (atomic).
func (s *Store) SaveResurrect(ctx context.Context, wc store.WriteContext, original *session.Session) (*session.Session, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if original == nil {
		return nil, fmt.Errorf("store: SaveResurrect: original is nil")
	}
	if !session.Status(original.Status).IsResurrectable() && session.Status(original.Status) != session.StatusIdle {
		return nil, fmt.Errorf("store: SaveResurrect: original status %q is not resurrectable", original.Status)
	}
	activeProject := s.ActiveProject()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	newSess := &session.Session{
		SessionID:       session.NewSessionID(),
		Status:          string(session.StatusOpen),
		ConstitutionID:  original.ConstitutionID,
		ConstitutionVer: original.ConstitutionVer,
		ActiveMods:      original.ActiveMods,
		Operator:        original.Operator,
		StartedAt:       now,
		LastHeartbeatAt: now,
		ParentSessionID: original.SessionID,
		ResurrectedFrom: original.ResurrectedFrom, // chain to original ancestor (may equal ParentSessionID if no chain)
		Notes:           fmt.Sprintf("resurrect of %s", original.SessionID),
	}
	if newSess.ResurrectedFrom == "" {
		newSess.ResurrectedFrom = original.SessionID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		// closed_at must be NULL (not empty string) for open/idle sessions
		// to satisfy the v12 CHECK constraint.
		var closedAt sql.NullString
		if newSess.ClosedAt != "" {
			closedAt = sql.NullString{String: newSess.ClosedAt, Valid: true}
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (session_id, status, constitution_id, constitution_ver, active_mods,
			                     started_at, last_heartbeat_at, closed_at, parent_session_id,
			                     resurrected_from, notes, operator, project_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newSess.SessionID, newSess.Status, newSess.ConstitutionID, newSess.ConstitutionVer,
			newSess.ActiveMods, newSess.StartedAt, newSess.LastHeartbeatAt, closedAt,
			newSess.ParentSessionID, newSess.ResurrectedFrom, newSess.Notes,
			newSess.Operator, activeProject,
			newSess.StartedAt /* created_at proxy = started_at for resurrected sessions */)
		if err != nil {
			return err
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		newSess.ID = newID
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "sessions",
			Actor:           wc.Actor,
			SessionID:       newSess.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			SessionEvent:    "resurrect",
			CreatedAt:       now,
		}, "")
	})
	if err != nil {
		return nil, err
	}
	return newSess, nil
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
		        operator, started_at, closed_at, last_heartbeat_at,
		        parent_session_id, resurrected_from, notes
		 FROM sessions WHERE project_id = ? ORDER BY id DESC LIMIT ?`, activeProject, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []session.Session{}
	for rows.Next() {
		var sess session.Session
		var notes, closedAt, heartbeat, parent, rescFrom sql.NullString
		if err := rows.Scan(&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
			&sess.Operator, &sess.StartedAt, &closedAt, &heartbeat, &parent, &rescFrom, &notes); err != nil {
			return nil, err
		}
		if closedAt.Valid {
			sess.ClosedAt = closedAt.String
		}
		if heartbeat.Valid {
			sess.LastHeartbeatAt = heartbeat.String
		}
		if parent.Valid {
			sess.ParentSessionID = parent.String
		}
		if rescFrom.Valid {
			sess.ResurrectedFrom = rescFrom.String
		}
		if notes.Valid {
			sess.Notes = notes.String
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ListStaleSessions is the sweeper query (Wave 5E.iii, INV-9). Returns
// sessions whose last_heartbeat_at is strictly older than cutoff AND
// whose status is in the given set. Read-only; the caller (sweeper or
// boot_reconcile) decides whether to transition.
//
// Implementation note: SQLite stores last_heartbeat_at as RFC3339Nano
// TEXT. String comparison on ISO8601 timestamps is lexicographically
// correct only when the timezone offset is consistent — RFC3339Nano
// always produces UTC ("Z") so the comparison is safe.
func (s *Store) ListStaleSessions(ctx context.Context, statuses []string, cutoff time.Time, limit int) ([]session.Session, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, nil
	}
	activeProject := s.ActiveProject()
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 200
	}
	// Build placeholders for the IN clause.
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses)+3)
	args = append(args, activeProject)
	for i, st := range statuses {
		placeholders[i] = "?"
		args = append(args, st)
	}
	args = append(args, cutoff.UTC().Format(time.RFC3339Nano))
	args = append(args, limit)

	q := `SELECT id, session_id, status, constitution_id, constitution_ver, active_mods,
	             operator, started_at, closed_at, last_heartbeat_at,
	             parent_session_id, resurrected_from, notes
	      FROM sessions
	      WHERE project_id = ?
	        AND status IN (` + strings.Join(placeholders, ",") + `)
	        AND last_heartbeat_at IS NOT NULL
	        AND last_heartbeat_at < ?
	      ORDER BY last_heartbeat_at ASC
	      LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []session.Session{}
	for rows.Next() {
		var sess session.Session
		var notes, closedAt, heartbeat, parent, rescFrom sql.NullString
		if err := rows.Scan(&sess.ID, &sess.SessionID, &sess.Status, &sess.ConstitutionID, &sess.ConstitutionVer, &sess.ActiveMods,
			&sess.Operator, &sess.StartedAt, &closedAt, &heartbeat, &parent, &rescFrom, &notes); err != nil {
			return nil, err
		}
		if closedAt.Valid {
			sess.ClosedAt = closedAt.String
		}
		if heartbeat.Valid {
			sess.LastHeartbeatAt = heartbeat.String
		}
		if parent.Valid {
			sess.ParentSessionID = parent.String
		}
		if rescFrom.Valid {
			sess.ResurrectedFrom = rescFrom.String
		}
		if notes.Valid {
			sess.Notes = notes.String
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// PromoteSessionStatus transitions a session to newStatus (Wave 5E.iii,
// INV-9). Valid transitions: open → idle (sweeper demotes a stale live
// session), idle → open (re-activate after a stray idle). Anything else
// returns ErrInvalidState. Emits write_audit with session_event='promote'.
func (s *Store) PromoteSessionStatus(ctx context.Context, wc store.WriteContext, sessionID, newStatus string) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Wave 5E.iv bug-hunt: read-then-write MUST be in the same
	// transaction so a concurrent writer (another dark-mem-mcp
	// process, a CLI operator, the sweeper itself in a race) can't
	// mutate status between our SELECT and UPDATE. Previously the
	// SELECT happened outside runInTx (only mutex-serialized — not
	// process-safe); the Postgres mirror already had this right.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		var currentStatus string
		if err := tx.QueryRowContext(ctx,
			`SELECT status FROM sessions WHERE session_id = ? AND project_id = ?`,
			sessionID, activeProject).Scan(&currentStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			return err
		}
		// Validate transition.
		if !isValidPromote(currentStatus, newStatus) {
			return fmt.Errorf("%w: cannot promote %s -> %s", store.ErrInvalidState, currentStatus, newStatus)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE sessions SET status = ? WHERE session_id = ? AND project_id = ?`,
			newStatus, sessionID, activeProject)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return store.ErrNotFound
		}
		// Look up the row id for the audit row.
		var id int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM sessions WHERE session_id = ? AND project_id = ?`,
			sessionID, activeProject).Scan(&id); err != nil {
			return err
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "sessions",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       sessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			SessionEvent:    "promote",
			CreatedAt:       now,
		}, "")
	})
	return err
}

// isValidPromote returns true iff (current, new) is a valid status
// promotion transition per INV-9 / Wave 5E.iii. Closed/archived states
// are NOT valid targets — use CloseSession with the right reason instead.
//
// The same logic exists in internal/store/postgres/store.go (the
// drivers are separate packages so they can't share helpers).
func isValidPromote(current, next string) bool {
	switch current {
	case string(session.StatusOpen):
		return next == string(session.StatusIdle)
	case string(session.StatusIdle):
		return next == string(session.StatusOpen)
	}
	return false
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
			                           content_sha256, canary_present, constitution_id, constitution_ver, session_event, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"research_items", itemID, wc.Actor, run.SessionID, wc.WritePath, hash, canary, wc.ConstitutionID, wc.ConstitutionVer, wc.SessionEvent, item.CreatedAt)
		if err != nil {
			return 0, err
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO write_audit (table_name, row_id, actor, session_id, write_path,
		                           constitution_id, constitution_ver, session_event, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"research_runs", runID, wc.Actor, run.SessionID, wc.WritePath, wc.ConstitutionID, wc.ConstitutionVer, wc.SessionEvent, run.CreatedAt)
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

// CountItemsForProject — Wave 5E.v. Replaces the SessionClose N+1
// query pattern (ListRuns + N×ListItems) with a single indexed
// COUNT(*) on research_items. O(log n) via idx_research_items_project.
// Read-only; no audit row emitted.
//
// Empty projectID falls back to the active project; this matches the
// defensive pattern used by FindClosedAbortedForActor in 5E.ii.
func (s *Store) CountItemsForProject(ctx context.Context, projectID string) (int, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	activeProject := s.ActiveProject() // capture before locking
	if projectID == "" {
		projectID = activeProject
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_items WHERE project_id = ?`,
		projectID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CountRunsForProject — Wave 5E.v. Counterpart of CountItemsForProject
// for research_runs. Same indexed-count pattern, replaces
// "load all runs just to get the count" with a constant-time COUNT.
func (s *Store) CountRunsForProject(ctx context.Context, projectID string) (int, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	activeProject := s.ActiveProject() // capture before locking
	if projectID == "" {
		projectID = activeProject
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_runs WHERE project_id = ?`,
		projectID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) LinkResearch(ctx context.Context, wc store.WriteContext, link *research.Link) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if link.CreatedAt == "" {
		link.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
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
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
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
	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO vibe_specs (vibe_case, session_id, constitution_json, spec_json, tasks_json, created_at, updated_at, project_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sp.VibeCase, nullStr(sp.SessionID), nullStr(sp.Constitution), nullStr(sp.Spec), nullStr(sp.Tasks),
			sp.CreatedAt, sp.UpdatedAt, projectID)
		if err != nil {
			return err
		}
		newID, _ := res.LastInsertId()
		id = newID
		sp.ID = newID
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_specs",
			RowID:           newID,
			Actor:           wc.Actor,
			SessionID:       nullStrOr(sp.SessionID, wc.SessionID),
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       sp.CreatedAt,
		}, "")
	})
	return id, err
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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
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
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_specs",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       now,
		}, "")
	})
}

func (s *Store) DeleteSpec(ctx context.Context, wc store.WriteContext, id int64) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM vibe_specs WHERE id = ? AND project_id = ?`, id, activeProject)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_specs",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		}, "")
	})
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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
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
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
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
	})
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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM vibe_brands WHERE brand_id = ? AND project_id = ?`, brandID, activeProject)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
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
	})
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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
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
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
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
	})
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
	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO vibe_artifacts (session_id, vibe_case, spec_id, artifact_url, artifact_type,
			                           brand_id, jurisdiction, has_disclosure, validation_status, created_at, project_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nullStr(a.SessionID), a.VibeCase, nullInt(a.SpecID), nullStr(a.ArtifactURL), a.ArtifactType,
			nullStr(a.BrandID), nullStr(a.Jurisdiction), hasDisclosure, a.ValidationStatus, a.CreatedAt, projectID)
		if err != nil {
			return err
		}
		newID, _ := res.LastInsertId()
		id = newID
		a.ID = newID
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_artifacts",
			RowID:           newID,
			Actor:           wc.Actor,
			SessionID:       nullStrOr(a.SessionID, wc.SessionID),
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       a.CreatedAt,
		}, "")
	})
	return id, err
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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
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
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_artifacts",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		}, "")
	})
}

func (s *Store) DeleteArtifact(ctx context.Context, wc store.WriteContext, id int64) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM vibe_artifacts WHERE id = ? AND project_id = ?`, id, activeProject)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_artifacts",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		}, "")
	})
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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE vibe_artifacts SET validation_status = ? WHERE id = ? AND project_id = ?`,
			status, id, activeProject)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
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
	})
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
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject()) // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.CreatedAt == "" {
		d.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO vibe_drift_reports (artifact_id, spec_id, verdict, spec_diff_json, judge_reasoning, reconciled_at, created_at, project_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ArtifactID, nullInt(d.SpecID), d.Verdict, nullStr(d.SpecDiff), nullStr(d.JudgeReasoning),
			nullStr(d.ReconciledAt), d.CreatedAt, projectID)
		if err != nil {
			return err
		}
		newID, _ := res.LastInsertId()
		id = newID
		d.ID = newID
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_drift_reports",
			RowID:           newID,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       d.CreatedAt,
		}, "")
	})
	return id, err
}

func (s *Store) LatestDriftForArtifact(ctx context.Context, artifactID int64) (*vibeflow.DriftReport, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, artifact_id, spec_id, verdict, spec_diff_json, judge_reasoning, reconciled_at, created_at
		 FROM vibe_drift_reports
		 WHERE artifact_id = ? AND project_id = ?
		 ORDER BY id DESC LIMIT 1`, artifactID, activeProject)
	return scanDriftReport(row)
}

func (s *Store) ListDriftReports(ctx context.Context, artifactID int64, verdict string, limit int) ([]vibeflow.DriftReport, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, artifact_id, spec_id, verdict, spec_diff_json, judge_reasoning, reconciled_at, created_at
	      FROM vibe_drift_reports WHERE project_id = ?`
	args := []any{activeProject}
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
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject()) // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO sdd_evaluations
			 (eval_type, target_type, target_id, verdict_json, confidence,
			  prompt_version, model, created_at,
			  constitution_id, constitution_version, active_mods_json,
			  refused_attempts, refusal_pattern, project_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.EvalType, e.TargetType, e.TargetID, e.VerdictJSON, e.Confidence,
			nullStr(e.PromptVersion), nullStr(e.Model), e.CreatedAt,
			nullStr(e.ConstitutionID), nullStr(e.ConstitutionVersion), nullStr(e.ActiveModsJSON),
			e.RefusedAttempts, nullStr(e.RefusalPattern), projectID)
		if err != nil {
			return err
		}
		newID, _ := res.LastInsertId()
		id = newID
		e.ID = newID
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "sdd_evaluations",
			RowID:           newID,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       e.CreatedAt,
		}, "")
	})
	return id, err
}

func (s *Store) LatestSDDEvaluation(ctx context.Context, evalType, targetType, targetID string) (*ssd.SDDEvaluation, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, eval_type, target_type, target_id, verdict_json, confidence,
		        prompt_version, model, created_at,
		        constitution_id, constitution_version, active_mods_json,
		        refused_attempts, refusal_pattern
		 FROM sdd_evaluations
		 WHERE eval_type = ? AND target_type = ? AND target_id = ? AND project_id = ?
		 ORDER BY id DESC LIMIT 1`, evalType, targetType, targetID, activeProject)
	return scanSDDEval(row)
}

func (s *Store) ListSDDEvaluations(ctx context.Context, f ssd.ListFilters) ([]ssd.SDDEvaluation, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Limit <= 0 {
		f.Limit = 20
	}
	q := `SELECT id, eval_type, target_type, target_id, verdict_json, confidence,
	             prompt_version, model, created_at,
	             constitution_id, constitution_version, active_mods_json,
	             refused_attempts, refusal_pattern
	      FROM sdd_evaluations WHERE project_id = ?`
	args := []any{activeProject}
	if f.EvalType != "" {
		q += ` AND eval_type = ?`
		args = append(args, f.EvalType)
	}
	if f.TargetType != "" {
		q += ` AND target_type = ?`
		args = append(args, f.TargetType)
	}
	if f.TargetID != "" {
		q += ` AND target_id = ?`
		args = append(args, f.TargetID)
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
// IMPORTANT (spec 171 T4f): constitutions are GLOBAL by design.
// Constitutions define the agent's posture at system level (INV-4
// watchdog verifies the active constitution). They are NOT
// project-scoped: every project sees the same constitution catalog.
// The `constitutions` table has no `project_id` column — there is
// nothing to filter by. Same rationale as `vibe_compliance`
// (jurisdiction = property of law) — see spec 171 T4c decision.
//
// To move constitutions to project scope, migration v9 must add
// `project_id` to the table and refactor `runWatchdog` (INV-4) to
// pick one per active project. That is intentionally out of scope
// today.

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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
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
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "constitutions",
			RowID:           0,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  c.ConstitutionID,
			ConstitutionVer: c.Version,
			CreatedAt:       c.CreatedAt,
			Notes:           "constitution_id=" + c.ConstitutionID + "@" + c.Version + " (global)",
		}, "")
	})
}

func (s *Store) GetConstitution(ctx context.Context, constitutionID, version string) (*constitution.Constitution, error) {
	// Global by design — see T4f decision.
	row := s.db.QueryRowContext(ctx,
		`SELECT id, constitution_id, version, label, source, file_path, parsed_json, sha256,
		        enabled, created_at, activated_at
		 FROM constitutions
		 WHERE constitution_id = ? AND version = ?`, constitutionID, version)
	return scanConstitution(row)
}

func (s *Store) ListConstitutions(ctx context.Context, limit int) ([]constitution.Constitution, error) {
	// Global by design — see T4f decision.
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
// IMPORTANT (spec 171 T4f): mods CATALOG is GLOBAL by design.
// The `mods` table has UNIQUE(mod_id) — mod_id identifies a mod once
// across all projects. Each project can use any mod, but the catalog
// itself is a single shared registry. Per-project isolation is
// recorded in `mod_loads` (the audit trail of who loaded what) —
// that table has `project_id` and IS project-scoped. See
// RecordModLoad / ListModLoads below.
//
// Splitting the catalog per project would require migration v9
// (composite UNIQUE(project_id, mod_id) on mods) and disabling the
// cross-project mod-share pattern. That is intentionally out of
// scope today.

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
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
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
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "mods",
			RowID:           0,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       m.UpdatedAt,
			Notes:           "mod_id=" + m.ModID + " (global catalog)",
		}, "")
	})
}

func (s *Store) GetMod(ctx context.Context, modID string) (*mods.Mod, error) {
	// Global catalog — see T4f decision.
	row := s.db.QueryRowContext(ctx,
		`SELECT id, mod_id, name, version, source, manifest_json, sha256, risk_class,
		        target_scope, requires_tor, created_at, updated_at
		 FROM mods WHERE mod_id = ?`, modID)
	return scanMod(row)
}

func (s *Store) ListMods(ctx context.Context, limit int) ([]mods.Mod, error) {
	// Global catalog — see T4f decision.
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
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.ActiveProject()) // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if load.LoadedAt == "" {
		load.LoadedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO mod_loads (mod_id, session_id, loaded_at, duration_ms,
			                       capabilities_count, error, constitution_id, project_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			load.ModID, nullStr(load.SessionID), load.LoadedAt, load.DurationMs,
			load.CapabilitiesCount, nullStr(load.Error), nullStr(load.ConstitutionID), projectID)
		if err != nil {
			return err
		}
		newID, _ := res.LastInsertId()
		id = newID
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "mod_loads",
			RowID:           newID,
			Actor:           wc.Actor,
			SessionID:       nullStrOr(load.SessionID, wc.SessionID),
			WritePath:       wc.WritePath,
			ConstitutionID:  load.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       load.LoadedAt,
			Notes:           "mod_id=" + load.ModID + " project_id=" + projectID,
		}, "")
	})
	return id, err
}

func (s *Store) ListModLoads(ctx context.Context, modID string, limit int) ([]mods.ModLoad, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject() // capture before locking
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, mod_id, session_id, loaded_at, duration_ms, capabilities_count, error, constitution_id
	      FROM mod_loads WHERE project_id = ?`
	args := []any{activeProject}
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

// Stats is GLOBAL by design (see spec 171 T4g).
//
// It returns aggregate health counters across the entire dark.db: schema
// version, table list, total rows per table, active sessions. It is the
// operational observability entry point — used by health checks, dashboards,
// and operator tooling to see "what's in this DB" without filtering.
//
// Stats is intentionally NOT scoped to the active project: an operator
// inspecting the system needs to see totals, not just their project's
// slice. The numbers are aggregate counts and risk only information
// disclosure about database size, not about the contents of any
// specific tenant's data.
//
// If multi-tenant observability is ever needed (per-project counters
// without leaking totals), add a sister method `StatsForProject(ctx,
// projectID)` that adds WHERE project_id = ? to each count.
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
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE status IN ('open','idle')`).Scan(&out.SessionsActive)
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
		`INSERT INTO projects (project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id, drift_strictness)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   display_name = excluded.display_name,
		   description = excluded.description,
		   constitution_id = excluded.constitution_id,
		   constitution_ver = excluded.constitution_ver,
		   parent_project_id = excluded.parent_project_id,
		   drift_strictness = excluded.drift_strictness,
		   archived_at = NULL`,
		p.ProjectID, p.DisplayName, nullStr(p.Description), nullStr(p.ConstitutionID), nullStr(p.ConstitutionVer),
		p.CreatedAt, nullStr(p.ArchivedAt), nullStr(p.ParentProjectID),
		nullStr(driftStrictnessOrDefault(p.DriftStrictness)))
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
		`SELECT id, project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id, drift_strictness
		 FROM projects WHERE project_id = ?`, projectID)
	var p project.Project
	var desc, consID, consVer, archived, parent, drift sql.NullString
	if err := row.Scan(&p.ID, &p.ProjectID, &p.DisplayName, &desc, &consID, &consVer, &p.CreatedAt, &archived, &parent, &drift); err != nil {
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
	if drift.Valid {
		p.DriftStrictness = drift.String
	}
	return &p, nil
}

func (s *Store) ListProjects(ctx context.Context, limit int) ([]project.Project, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, display_name, description, constitution_id, constitution_ver, created_at, archived_at, parent_project_id, drift_strictness
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
		var desc, consID, consVer, archived, parent, drift sql.NullString
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.DisplayName, &desc, &consID, &consVer, &p.CreatedAt, &archived, &parent, &drift); err != nil {
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
		if drift.Valid {
			p.DriftStrictness = drift.String
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

// driftStrictnessOrDefault (Wave 5X.3) normalizes the operator's
// DriftStrictness field for INSERT. Empty or "default" → "default"
// (the sentinel that means "use env"). Otherwise pass through.
func driftStrictnessOrDefault(s string) string {
	if s == "" {
		return "default"
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

// ----- Atomic frames (Wave 5A.ii.a; vibe_frames table from v11) -----

// SaveFrame upserts a frame envelope by
// (project_id, session_id, scope_level, scope_id, frame_kind). The
// upsert target is the same tuple as the lookup key in GetFrame, so
// repeated saves overwrite the existing row in place. Writes a
// write_audit row in the SAME tx (INV-1: atomic audit).
//
// Schema note: v11 ships with `vibe_frames` (no UNIQUE constraint).
// The cache layer (5A.ii.b) is expected to add a UNIQUE INDEX on
// (session_id, scope_level, scope_id, frame_kind) as part of v11b.
// Until that migration lands, SaveFrame degrades to "INSERT or
// UPDATE-most-recent" via the (session_id, scope_level, scope_id,
// frame_kind, MAX(created_at)) selector — implemented below by
// matching the same composite key + composed_at.
func (s *Store) SaveFrame(ctx context.Context, wc store.WriteContext, env *atomic.FrameEnvelope) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	if env == nil {
		return 0, fmt.Errorf("sqlite: SaveFrame: env is nil")
	}
	if env.SessionID == "" || env.Kind == "" || env.ScopeLevel == "" || env.ScopeID == "" {
		return 0, fmt.Errorf("sqlite: SaveFrame: missing key fields (session_id=%q scope_level=%q scope_id=%q kind=%q)",
			env.SessionID, env.ScopeLevel, env.ScopeID, env.Kind)
	}
	if len(env.FrameJSON) == 0 {
		return 0, fmt.Errorf("sqlite: SaveFrame: frame_json is empty")
	}
	activeProject := s.ActiveProject()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if env.ComposedAt.IsZero() {
		env.ComposedAt = time.Now().UTC()
	}
	if env.CreatedAt.IsZero() {
		env.CreatedAt = time.Now().UTC()
	}
	shaHex := fmt.Sprintf("%x", env.ContentSHA256)
	s.mu.Lock()
	defer s.mu.Unlock()
	var id int64
	// v13 polish: true UPSERT via INSERT ... ON CONFLICT. Requires the
	// uq_vibe_frames_natural_key UNIQUE INDEX added in migration v13.
	// Replaces the previous SELECT-then-INSERT/UPDATE pattern which
	// was racy under concurrent writes for the same composite key.
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		// Use RETURNING id instead of LastInsertId() — the latter is
		// unreliable for INSERT ... ON CONFLICT DO UPDATE in some
		// SQLite versions (can return the rowid of the inserted row
		// even when an UPDATE branch fired, leading to phantom new
		// ids for idempotent upserts).
		var newID int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO vibe_frames (project_id, session_id, scope_level, scope_id,
			                          frame_kind, composed_at, expires_at, frame_json,
			                          content_sha256, last_write_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(project_id, session_id, scope_level, scope_id, frame_kind)
			 DO UPDATE SET
			   frame_json     = excluded.frame_json,
			   content_sha256 = excluded.content_sha256,
			   expires_at     = excluded.expires_at,
			   last_write_id  = excluded.last_write_id,
			   composed_at    = excluded.composed_at,
			   created_at     = excluded.created_at
			 RETURNING id`,
			activeProject, env.SessionID, env.ScopeLevel, env.ScopeID, env.Kind,
			env.ComposedAt.UTC().Format(time.RFC3339Nano),
			env.ExpiresAt.UTC().Format(time.RFC3339Nano),
			string(env.FrameJSON), shaHex, env.LastWriteID,
			env.CreatedAt.UTC().Format(time.RFC3339Nano)).Scan(&newID)
		if err != nil {
			return err
		}
		id = newID
		env.ID = id
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_frames",
			Actor:           wc.Actor,
			SessionID:       env.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       now,
		}, "")
	})
	return id, err
}

// GetFrame returns the freshest non-expired frame matching the
// composite key within the active project. Returns (nil, nil) on
// cache miss (no row exists, or the row is past expires_at).
//
// NOTE (Wave 5A.ii.a): this is pure persistence — no INV-5 integrity
// check happens here. The cache layer (Wave 5A.ii.b) is responsible
// for verifying content_sha256 on read and emitting the
// cache_mismatch audit breadcrumb. This wave exposes the bytes as
// persisted; the cache layer is the trust boundary.
func (s *Store) GetFrame(ctx context.Context, sessionID string, scope atomic.ScopeLevel, scopeID string, kind atomic.FrameKind) (*atomic.FrameEnvelope, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if sessionID == "" || scope == "" || scopeID == "" || kind == "" {
		return nil, fmt.Errorf("sqlite: GetFrame: missing key (session_id=%q scope=%q scope_id=%q kind=%q)",
			sessionID, scope, scopeID, kind)
	}
	activeProject := s.ActiveProject()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, session_id, scope_level, scope_id, frame_kind,
		        composed_at, expires_at, frame_json, content_sha256, last_write_id, created_at
		 FROM vibe_frames
		 WHERE project_id = ? AND session_id = ? AND scope_level = ?
		   AND scope_id = ? AND frame_kind = ?
		   AND expires_at > ?
		 ORDER BY composed_at DESC LIMIT 1`,
		activeProject, sessionID, scope, scopeID, kind, now)
	var env atomic.FrameEnvelope
	var frameJSON string
	var shaHex string
	var composedAtStr, expiresAtStr, createdAtStr string
	if err := row.Scan(
		&env.ID, &env.ProjectID, &env.SessionID, &env.ScopeLevel, &env.ScopeID, &env.Kind,
		&composedAtStr, &expiresAtStr, &frameJSON, &shaHex, &env.LastWriteID, &createdAtStr,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	env.ComposedAt = parseTimeRFC3339Nano(composedAtStr)
	env.ExpiresAt = parseTimeRFC3339Nano(expiresAtStr)
	env.CreatedAt = parseTimeRFC3339Nano(createdAtStr)
	env.FrameJSON = []byte(frameJSON)
	if err := parseSHA(shaHex, &env.ContentSHA256); err != nil {
		return nil, fmt.Errorf("sqlite: GetFrame: parse content_sha256: %w", err)
	}
	return &env, nil
}

// ListFrames returns frames filtered by FrameListFilters. Expired rows
// are always excluded at the SQL layer (hygiene, not cache policy).
// Newest-first. Limit <= 0 means no limit.
func (s *Store) ListFrames(ctx context.Context, filter store.FrameListFilters) ([]atomic.FrameEnvelope, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if filter.ProjectID == "" {
		filter.ProjectID = s.ActiveProject()
	}
	q := `SELECT id, project_id, session_id, scope_level, scope_id, frame_kind,
	             composed_at, expires_at, frame_json, content_sha256, last_write_id, created_at
	      FROM vibe_frames WHERE project_id = ? AND expires_at > ?`
	args := []any{filter.ProjectID, time.Now().UTC().Format(time.RFC3339Nano)}
	if filter.SessionID != "" {
		q += " AND session_id = ?"
		args = append(args, filter.SessionID)
	}
	if filter.ScopeLevel != "" {
		q += " AND scope_level = ?"
		args = append(args, string(filter.ScopeLevel))
	}
	if filter.Kind != "" {
		q += " AND frame_kind = ?"
		args = append(args, string(filter.Kind))
	}
	q += " ORDER BY composed_at DESC"
	if filter.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []atomic.FrameEnvelope{}
	for rows.Next() {
		var env atomic.FrameEnvelope
		var frameJSON, shaHex string
		var composedAtStr, expiresAtStr, createdAtStr string
		if err := rows.Scan(
			&env.ID, &env.ProjectID, &env.SessionID, &env.ScopeLevel, &env.ScopeID, &env.Kind,
			&composedAtStr, &expiresAtStr, &frameJSON, &shaHex, &env.LastWriteID, &createdAtStr,
		); err != nil {
			return nil, err
		}
		env.ComposedAt = parseTimeRFC3339Nano(composedAtStr)
		env.ExpiresAt = parseTimeRFC3339Nano(expiresAtStr)
		env.CreatedAt = parseTimeRFC3339Nano(createdAtStr)
		env.FrameJSON = []byte(frameJSON)
		if err := parseSHA(shaHex, &env.ContentSHA256); err != nil {
			return nil, fmt.Errorf("sqlite: ListFrames: parse content_sha256 row id=%d: %w", env.ID, err)
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

// DeleteFrame removes a frame by id. Emits a write_audit row in the
// SAME tx (INV-1) using the SessionID from WriteContext. Returns
// store.ErrNotFound if the id doesn't exist. Note: this does NOT
// fetch the row's session_id before deleting — the caller is
// responsible for passing the right WriteContext.
func (s *Store) DeleteFrame(ctx context.Context, wc store.WriteContext, id int64) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	if id == 0 {
		return fmt.Errorf("sqlite: DeleteFrame: id is 0")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM vibe_frames WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_frames",
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       "DeleteFrame",
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			SessionEvent:    "frame_delete",
			CreatedAt:       now,
		}, "")
	})
}

// ----- Recall subscriptions (Wave 5A.ii.b.1; vibe_recall_subscriptions) -----

// SaveRecallSubscription inserts-or-updates a subscription row keyed by
// (session_id, scope_level, scope_id). v11 has a UNIQUE constraint on
// this triple so we can use INSERT ... ON CONFLICT for a true atomic
// upsert (no SELECT-then-UPDATE pattern needed). Returns the row id.
// Pure persistence: the recall orchestrator (5A.ii.b.2) owns the
// delta-cursor logic.
func (s *Store) SaveRecallSubscription(ctx context.Context, wc store.WriteContext, sub *store.RecallSubscription) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	if sub == nil {
		return 0, fmt.Errorf("sqlite: SaveRecallSubscription: sub is nil")
	}
	if sub.SessionID == "" || sub.ScopeLevel == "" || sub.ScopeID == "" {
		return 0, fmt.Errorf("sqlite: SaveRecallSubscription: missing key fields (session_id=%q scope_level=%q scope_id=%q)",
			sub.SessionID, sub.ScopeLevel, sub.ScopeID)
	}
	activeProject := s.ActiveProject()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sub.CreatedAt = now
	sub.UpdatedAt = now
	s.mu.Lock()
	defer s.mu.Unlock()
	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		// ON CONFLICT (session_id, scope_level, scope_id) DO UPDATE
		// refreshes last_seen_token, scope_id (no-op since key), and
		// updated_at. Returns the row id via RETURNING.
		err := tx.QueryRowContext(ctx,
			`INSERT INTO vibe_recall_subscriptions (project_id, session_id, scope_level, scope_id,
			                                        last_seen_token, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (session_id, scope_level, scope_id) DO UPDATE
			 SET last_seen_token = excluded.last_seen_token,
			     updated_at = excluded.updated_at
			 RETURNING id`,
			activeProject, sub.SessionID, sub.ScopeLevel, sub.ScopeID,
			sub.LastSeenToken, sub.CreatedAt, sub.UpdatedAt,
		).Scan(&id)
		if err != nil {
			return err
		}
		sub.ID = id
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_recall_subscriptions",
			Actor:           wc.Actor,
			SessionID:       sub.SessionID,
			WritePath:       "SaveRecallSubscription",
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			SessionEvent:    "subscription_upsert",
			CreatedAt:       now,
		}, "")
	})
	return id, err
}

// GetRecallSubscription returns the row matching the natural key, or
// (nil, nil) if not found. INV-7: filtered by active project.
func (s *Store) GetRecallSubscription(ctx context.Context, sessionID string, scope atomic.ScopeLevel, scopeID string) (*store.RecallSubscription, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if sessionID == "" || scope == "" || scopeID == "" {
		return nil, fmt.Errorf("sqlite: GetRecallSubscription: missing key (session_id=%q scope=%q scope_id=%q)",
			sessionID, scope, scopeID)
	}
	activeProject := s.ActiveProject()
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, session_id, scope_level, scope_id, last_seen_token, created_at, updated_at
		 FROM vibe_recall_subscriptions
		 WHERE project_id = ? AND session_id = ? AND scope_level = ? AND scope_id = ?`,
		activeProject, sessionID, scope, scopeID)
	var sub store.RecallSubscription
	if err := row.Scan(&sub.ID, &sub.ProjectID, &sub.SessionID, &sub.ScopeLevel, &sub.ScopeID,
		&sub.LastSeenToken, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sub, nil
}

// UpdateRecallSubscriptionLastSeenToken advances the cursor. Returns
// store.ErrNotFound if no subscription exists for the key.
func (s *Store) UpdateRecallSubscriptionLastSeenToken(ctx context.Context, wc store.WriteContext, sessionID string, scope atomic.ScopeLevel, scopeID string, newToken int64) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	if sessionID == "" || scope == "" || scopeID == "" {
		return fmt.Errorf("sqlite: UpdateRecallSubscriptionLastSeenToken: missing key (session_id=%q scope=%q scope_id=%q)",
			sessionID, scope, scopeID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE vibe_recall_subscriptions
			 SET last_seen_token = ?, updated_at = ?
			 WHERE project_id = ? AND session_id = ? AND scope_level = ? AND scope_id = ?`,
			newToken, now, s.activeProject, sessionID, scope, scopeID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_recall_subscriptions",
			Actor:           wc.Actor,
			SessionID:       sessionID,
			WritePath:       "UpdateRecallSubscriptionLastSeenToken",
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			SessionEvent:    "subscription_cursor_advance",
			CreatedAt:       now,
		}, "")
	})
}

// parseSHA parses a hex-encoded SHA-256 string into a [32]byte array.
func parseSHA(s string, out *[32]byte) error {
	if len(s) != 64 {
		return fmt.Errorf("sha256: expected 64 hex chars, got %d", len(s))
	}
	for i := 0; i < 32; i++ {
		hi, err := unhex(s[2*i])
		if err != nil {
			return fmt.Errorf("sha256: byte %d high: %w", i, err)
		}
		lo, err := unhex(s[2*i+1])
		if err != nil {
			return fmt.Errorf("sha256: byte %d low: %w", i, err)
		}
		out[i] = (hi << 4) | lo
	}
	return nil
}

// parseTimeRFC3339Nano parses a TEXT column formatted via
// time.RFC3339Nano into time.Time. Used by vibe_frames + sessions
// where columns are TEXT (not TIMESTAMP) for portability across
// sqlite/postgres. Returns zero time on empty input.
func parseTimeRFC3339Nano(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	// Fallback for legacy values without nanosecond precision.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// unhex converts a single hex char to its 4-bit value.
func unhex(c byte) (byte, error) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', nil
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, nil
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, nil
	default:
		return 0, fmt.Errorf("not hex: %q", c)
	}
}

// ----- VLP state (atomic spec 2.3 VLPPersistence) -----

// SaveVLPState upserts a per-session VLP state row. INSERT ... ON CONFLICT
// (project_id, session_id) DO UPDATE so repeated saves update the existing
// row instead of inserting duplicates. The composite ON CONFLICT target
// matches the UNIQUE INDEX idx_vlp_state_project_session (INV-7: per-project
// uniqueness — see migration v9 in ddl.go).
//
// Uses RETURNING id + QueryRowContext so the row id is reliable on both
// INSERT and UPDATE branches (database/sql's LastInsertId() is undefined
// for UPSERT-UPDATE in modernc/sqlite).
//
// Wraps UPSERT + write_audit in a single transaction so INV-1 is enforced
// atomically: either both rows land or neither does. Tx commit failure
// surfaces as an error to the caller; tx rollback on error ensures no
// orphan vlp_state rows.
func (s *Store) SaveVLPState(ctx context.Context, wc store.WriteContext, row *store.VLPStateRow) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveVLPStateTx(ctx, wc, row, "")
}

// SaveVLPStateWithTransition is the atomic combo: UPSERT vlp_state +
// row-level audit + transition-level audit, all in one tx. Closes the
// 2.5 atomicity gap (Save + Audit in separate calls). transitionNotes
// is the JSON payload for the transition-level audit row (typically a
// TransitionRecord serialized to JSON). Empty string means no
// transition-level audit row is emitted (pure Save semantics).
func (s *Store) SaveVLPStateWithTransition(ctx context.Context, wc store.WriteContext, row *store.VLPStateRow, transitionNotes string) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveVLPStateTx(ctx, wc, row, transitionNotes)
}

// saveVLPStateTx is the shared implementation for SaveVLPState and
// SaveVLPStateWithTransition. Caller MUST hold s.mu. transitionNotes
// empty = skip transition-level audit.
func (s *Store) saveVLPStateTx(ctx context.Context, wc store.WriteContext, row *store.VLPStateRow, transitionNotes string) (int64, error) {
	projectID := projectIDOrActive(wc.ProjectID, s.activeProject)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if row.CreatedAt == "" {
		row.CreatedAt = now
	}
	row.UpdatedAt = now

	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO vlp_state (session_id, state, last_event, last_verdict, turn_count,
			                      minset_current, constitution_id, constitution_ver,
			                      created_at, updated_at, project_id, open_spec_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, session_id) DO UPDATE SET
				state             = excluded.state,
				last_event        = excluded.last_event,
				last_verdict      = excluded.last_verdict,
				turn_count        = excluded.turn_count,
				minset_current    = excluded.minset_current,
				constitution_id   = excluded.constitution_id,
				constitution_ver  = excluded.constitution_ver,
				updated_at        = excluded.updated_at,
				open_spec_id      = excluded.open_spec_id
			RETURNING id`,
			row.SessionID, row.State, row.LastEvent, row.LastVerdict, row.TurnCount,
			row.MinsetCurrent, row.ConstitutionID, row.ConstitutionVer,
			row.CreatedAt, row.UpdatedAt, projectID, row.OpenSpecID,
		).Scan(&id); err != nil {
			return fmt.Errorf("vlp_state: upsert: %w", err)
		}

		// Row-level audit row in the same tx (INV-1).
		if wc.WritePath == "" {
			wc.WritePath = "SaveVLPState"
		}
		if err := s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vlp_state",
			RowID:           id,
			Actor:           wc.Actor,
			SessionID:       row.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       now,
		}, ""); err != nil {
			return fmt.Errorf("vlp_state: audit (row-level): %w", err)
		}

		// Transition-level audit (spec 2.4). Distinct write_path so
		// Auditor.ListTransitionsForSession can filter by it. JSON
		// payload in notes (typically a TransitionRecord serialized).
		if transitionNotes != "" {
			if err := s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
				TableName:       "vlp_state",
				RowID:           id,
				Actor:           wc.Actor,
				SessionID:       row.SessionID,
				WritePath:       "vlp.transition",
				ConstitutionID:  wc.ConstitutionID,
				ConstitutionVer: wc.ConstitutionVer,
				Notes:           transitionNotes,
				CreatedAt:       now,
			}, ""); err != nil {
				return fmt.Errorf("vlp_state: audit (transition-level): %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	row.ID = id
	row.ProjectID = projectID
	return id, nil
}

// GetVLPState returns the row for sessionID, or nil if not found.
// Filtered by active project (INV-7): cross-project reads are denied.
func (s *Store) GetVLPState(ctx context.Context, sessionID string) (*store.VLPStateRow, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject := s.ActiveProject()
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, state, last_event, last_verdict, turn_count,
		       minset_current, constitution_id, constitution_ver,
		       created_at, updated_at, project_id, open_spec_id
		FROM vlp_state WHERE session_id = ? AND project_id = ?`, sessionID, activeProject)
	var r store.VLPStateRow
	var le, lv, mc, ci, cv, pid sql.NullString
	if err := row.Scan(&r.ID, &r.SessionID, &r.State, &le, &lv, &r.TurnCount,
		&mc, &ci, &cv, &r.CreatedAt, &r.UpdatedAt, &pid, &r.OpenSpecID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	r.LastEvent = le.String
	r.LastVerdict = lv.String
	r.MinsetCurrent = mc.String
	r.ConstitutionID = ci.String
	r.ConstitutionVer = cv.String
	r.ProjectID = pid.String
	return &r, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()

	// Effective limit for SQL push-down. 0 → no LIMIT clause (caller owns
	// result-set size; same as the public contract).
	effectiveLimit := 0
	if limit > 0 {
		effectiveLimit = limit
	}

	var (
		rows *sql.Rows
		err  error
	)
	if stateFilter == "" {
		if effectiveLimit > 0 {
			rows, err = s.db.QueryContext(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = ?
				ORDER BY updated_at DESC LIMIT ?`, activeProject, effectiveLimit)
		} else {
			rows, err = s.db.QueryContext(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = ?
				ORDER BY updated_at DESC`, activeProject)
		}
	} else {
		if effectiveLimit > 0 {
			rows, err = s.db.QueryContext(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = ? AND state = ?
				ORDER BY updated_at DESC LIMIT ?`, activeProject, stateFilter, effectiveLimit)
		} else {
			rows, err = s.db.QueryContext(ctx, `
				SELECT id, session_id, state, last_event, last_verdict, turn_count,
				       minset_current, constitution_id, constitution_ver,
				       created_at, updated_at, project_id
				FROM vlp_state WHERE project_id = ? AND state = ?
				ORDER BY updated_at DESC`, activeProject, stateFilter)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]store.VLPStateRow, 0, effectiveLimit)
	for rows.Next() {
		var r store.VLPStateRow
		var le, lv, mc, ci, cv, pid sql.NullString
		if err := rows.Scan(&r.ID, &r.SessionID, &r.State, &le, &lv, &r.TurnCount,
			&mc, &ci, &cv, &r.CreatedAt, &r.UpdatedAt, &pid); err != nil {
			return nil, err
		}
		r.LastEvent = le.String
		r.LastVerdict = lv.String
		r.MinsetCurrent = mc.String
		r.ConstitutionID = ci.String
		r.ConstitutionVer = cv.String
		r.ProjectID = pid.String
		out = append(out, r)
	}
	return out, rows.Err()
}
