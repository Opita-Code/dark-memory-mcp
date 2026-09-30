// Package session (v4alpha) — session lifecycle primitive.
//
// A session is one operational context with audit-logged lifecycle:
// Start → Heartbeat (×N) → Close. Sessions are scoped to one
// (operator, project_id) pair. INV-7 (tenant primitive) is enforced
// at Read time: a session started in project P cannot be read by an
// operator working in project P'.
//
// # Atomicity contract (per internal/atomic convention)
//   - ONE constructor: NewStore
//   - ONE schema function: CreateSchema
//   - THREE lifecycle methods: Start, Heartbeat, Close
//   - TWO read methods: Read (per project), Get (operator-only)
//   - FOUR invariants enforced:
//     1. Start emits exactly one INV-1 audit row
//     2. Heartbeat emits one audit row + advances last_heartbeat_at
//     3. Close emits one audit row + terminalizes status
//     4. INV-7: Read(sessionID, projectID) requires match
//
// INV-1 audit integration: every lifecycle mutation calls audit.Write.
// The Writer's audit_id is the canonical ordering axis for replay.
package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // test/dev driver; production may use pgx

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// Status constants. Mirroring the v3 INV-8 lifecycle but simplified
// for v4alpha (closed_clean / closed_aborted; resurrect is post-alpha).
const (
	StatusOpen          = "open"
	StatusClosedClean   = "closed_clean"
	StatusClosedAborted = "closed_aborted"
)

// ErrProjectMismatch is returned by Read when the requested project_id
// does not match the session's project_id. INV-7 enforcement.
var ErrProjectMismatch = errors.New("session: project_id mismatch (INV-7)")

// ErrSessionClosed is returned when a lifecycle method (Heartbeat,
// Close) is called on a closed session.
var ErrSessionClosed = errors.New("session: closed (no further lifecycle)")

// ErrEmptyOperator is returned by Start when operator is empty.
var ErrEmptyOperator = errors.New("session: operator must be non-empty")

// ErrUnknownProject is returned by Start when the requested
// project_id does not exist in the `projects` registry (Phase 4
// Chunk 4.3 — hard isolation enforcement). Sessions may only be
// opened against a known project; unknown projects are rejected at
// the boundary so operator queries against sessions stay scoped.
var ErrUnknownProject = errors.New("session: project_id is not registered (INV-7)")

// ErrNotFound is returned by Read/Get when no session matches.
var ErrNotFound = errors.New("session: not found")

// Session is the public view of one row in the sessions table.
type Session struct {
	ID              string
	Operator        string
	ProjectID       string
	Status          string
	StartedAt       time.Time
	LastHeartbeatAt time.Time
	ClosedAt        *time.Time
}

// Summary is returned by Close. Captures the counts of writes/runs/items
// attributed to the session between Start and Close.
type Summary struct {
	WriteCount     int64
	RunCount       int64
	ItemCount      int64
	AuditIDAtClose int64 // last audit_id emitted by this session's lifecycle
}

// Store is the session repository. Bound to a DB and an audit Writer.
// The optional `projects` field (set via SetProjectsForTest or
// SetProjects in production wiring) enforces INV-7 hard isolation:
// Start rejects project_ids that are not registered in the projects
// table. Nil `projects` skips validation (test-only — keeps the
// pre-Phase-4 test suite green without spinning up the project
// package).
type Store struct {
	db       *sql.DB
	audit    *audit.Writer
	projects ProjectValidator // optional, nil-safe
}

// ProjectValidator is the minimum interface session.Store needs from
// the project registry to enforce INV-7 hard isolation on Start.
// The interface lives here (not in the project package) so session
// does not import project — keeps the dependency graph acyclic and
// tests trivial.
type ProjectValidator interface {
	Lookup(ctx context.Context, projectID string) (any, error)
}

// NewStore returns a Store wired to db and w. Both must be initialized
// (CreateSchema called on db; NewWriter called on w).
func NewStore(db *sql.DB, w *audit.Writer) *Store {
	return &Store{db: db, audit: w}
}

// SetProjectsForTest wires a ProjectValidator into the Store.
// Called from production wiring (cmd/dark-memory-v4/serve.go) AFTER
// both the session Store and the project Store are constructed.
// Tests that don't need INV-7 validation can skip this; nil is safe.
func (s *Store) SetProjectsForTest(p ProjectValidator) {
	s.projects = p
}

// CreateSchema creates the sessions table. Idempotent.
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			operator TEXT NOT NULL CHECK (operator <> ''),
			project_id TEXT NOT NULL CHECK (project_id <> ''),
			status TEXT NOT NULL CHECK (status IN ('open','closed_clean','closed_aborted')),
			started_at TEXT NOT NULL,
			last_heartbeat_at TEXT NOT NULL,
			closed_at TEXT
		)
	`)
	if err != nil {
		return fmt.Errorf("session CreateSchema: %w", err)
	}
	return nil
}

// Start creates a new session, emits one INV-1 audit row, and returns
// the Session view.
//
// INV-7 hard isolation (Phase 4 Chunk 4.3): when projects is wired
// (SetProjectsForTest), Start validates that projectID exists in the
// projects registry. Unknown project_id returns ErrUnknownProject
// WITHOUT inserting a session row. When projects is nil (legacy /
// test-only path), validation is skipped and the pre-Phase-4
// behavior is preserved.
//
// The audit emission uses WriteWithProject (project_id='<projectID>')
// so the lifecycle row in audit_log is scoped to the same project
// as the session itself. Pre-Phase-4 audit rows get project_id=
// 'default' via the column DEFAULT.
func (s *Store) Start(ctx context.Context, operator, projectID string) (*Session, error) {
	if operator == "" {
		return nil, ErrEmptyOperator
	}
	if projectID == "" {
		return nil, fmt.Errorf("session: project_id must be non-empty")
	}
	if s.projects != nil {
		// INV-7 hard isolation: unknown project_id is rejected
		// at the session boundary. We swallow the Lookup error
		// surface to ErrUnknownProject so callers can
		// errors.Is(err, ErrUnknownProject) reliably.
		_, err := s.projects.Lookup(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrUnknownProject, projectID)
		}
	}

	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("session: generate id: %w", err)
	}

	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, operator, project_id, status, started_at, last_heartbeat_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, operator, projectID, StatusOpen, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("session Start insert: %w", err)
	}

	if _, err := s.audit.WriteWithProject(ctx, operator, id, projectID, []byte(`{"event":"session.start"}`)); err != nil {
		return nil, fmt.Errorf("session Start audit: %w", err)
	}

	return &Session{
		ID:              id,
		Operator:        operator,
		ProjectID:       projectID,
		Status:          StatusOpen,
		StartedAt:       now,
		LastHeartbeatAt: now,
		ClosedAt:        nil,
	}, nil
}

// Heartbeat refreshes last_heartbeat_at for the given session. Emits
// one INV-1 audit row. Returns ErrSessionClosed if status != open.
func (s *Store) Heartbeat(ctx context.Context, sessionID string) error {
	sess, err := s.fetchOpen(ctx, sessionID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		"UPDATE sessions SET last_heartbeat_at = ? WHERE id = ?",
		now.Format(time.RFC3339Nano), sessionID,
	)
	if err != nil {
		return fmt.Errorf("session Heartbeat update: %w", err)
	}

	if _, err := s.audit.Write(ctx, sess.Operator, sessionID, []byte(`{"event":"session.heartbeat"}`)); err != nil {
		return fmt.Errorf("session Heartbeat audit: %w", err)
	}
	return nil
}

// Close terminalizes the session. Returns the Summary with audit_id
// at close. If clean is true, status = closed_clean; otherwise
// closed_aborted. Emits one INV-1 audit row.
func (s *Store) Close(ctx context.Context, sessionID string, clean bool) (*Summary, error) {
	sess, err := s.fetchOpen(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	status := StatusClosedClean
	if !clean {
		status = StatusClosedAborted
	}

	_, err = s.db.ExecContext(ctx,
		"UPDATE sessions SET status = ?, closed_at = ? WHERE id = ?",
		status, now.Format(time.RFC3339Nano), sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("session Close update: %w", err)
	}

	if _, err := s.audit.Write(ctx, sess.Operator, sessionID, []byte(`{"event":"session.close","clean":`+fmt.Sprint(clean)+`}`)); err != nil {
		return nil, fmt.Errorf("session Close audit: %w", err)
	}

	// Compute summary by JOIN on session_id column (proper audit integration).
	// The 1 base count = the Close audit row itself; +N heartbeats; +1 Start.
	var writeCount int64
	row := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM audit_log WHERE session_id = ?",
		sessionID,
	)
	if err := row.Scan(&writeCount); err != nil {
		return nil, fmt.Errorf("session Close summary: %w", err)
	}

	return &Summary{
		WriteCount:     writeCount,
		RunCount:       0, // post-alpha: wired to vibe_flow engine
		ItemCount:      0, // post-alpha: wired to artifact_log
		AuditIDAtClose: s.audit.LastID(),
	}, nil
}

// Read returns the Session view for (sessionID, projectID). Returns
// ErrProjectMismatch if the session's project_id does not equal the
// requested projectID (INV-7 enforcement).
func (s *Store) Read(ctx context.Context, sessionID, projectID string) (*Session, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, operator, project_id, status, started_at, last_heartbeat_at, closed_at FROM sessions WHERE id = ?",
		sessionID,
	)
	sess, err := scanSession(row)
	if err != nil {
		return nil, err
	}
	if sess.ProjectID != projectID {
		return nil, ErrProjectMismatch
	}
	return sess, nil
}

// Get returns the Session view without project_id check (operator-only
// access). Used internally by Heartbeat/Close.
func (s *Store) Get(ctx context.Context, sessionID string) (*Session, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, operator, project_id, status, started_at, last_heartbeat_at, closed_at FROM sessions WHERE id = ?",
		sessionID,
	)
	return scanSession(row)
}

// fetchOpen returns the Session if status=open, ErrSessionClosed
// otherwise. Used by Heartbeat/Close.
func (s *Store) fetchOpen(ctx context.Context, sessionID string) (*Session, error) {
	sess, err := s.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status != StatusOpen {
		return nil, ErrSessionClosed
	}
	return sess, nil
}

func scanSession(row *sql.Row) (*Session, error) {
	var (
		sess        Session
		startedAt   string
		lastHBAt    string
		closedAtRaw sql.NullString
	)
	err := row.Scan(&sess.ID, &sess.Operator, &sess.ProjectID, &sess.Status,
		&startedAt, &lastHBAt, &closedAtRaw)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session scan: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
		sess.StartedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, lastHBAt); err == nil {
		sess.LastHeartbeatAt = t
	}
	if closedAtRaw.Valid {
		if t, err := time.Parse(time.RFC3339Nano, closedAtRaw.String); err == nil {
			sess.ClosedAt = &t
		}
	}
	return &sess, nil
}

// newSessionID returns a 128-bit hex ID with a stable prefix. Format:
// "sess-" + 32 hex chars (128 bits of entropy).
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "sess-" + hex.EncodeToString(b[:]), nil
}
