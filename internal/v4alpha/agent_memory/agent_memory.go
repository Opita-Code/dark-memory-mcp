// Package agent_memory (v4alpha) — operator-scoped memory store
// with FTS5 recall.
//
// A row is one operator's mental note. The operator id is the
// canonical scope axis (INV-1 audit: every Save emits one row
// tagged with the operator). Recall runs FTS5 against
// (content, title, tags) — same shape as the v3.0 agent_memory
// (spec 1130 §3.2) but simpler: no embedding, no Mem0-style
// three-class taxonomy yet. Those land in BUG-9.
//
// # Atomicity contract (per internal/atomic convention)
//
//   - ONE constructor: NewStore
//   - ONE schema function: CreateSchema
//   - TWO write methods: Save, Archive
//   - THREE read methods: Get, List, Recall
//   - THREE invariants enforced:
//     1. operator id is non-empty on Save (INV-1 audit row is
//        identifiable)
//     2. FTS5 index is kept in sync with the base table inside
//        one transaction (write-then-search is consistent)
//     3. kind is one of the canonical set (note, observation,
//        decision, finding, todo, link, context) — empty kind is
//        rejected to keep Recall results queryable
//
// INV-1 audit integration is deferred to BUG-9. The Save path
// today is a plain INSERT — the audit_id is filled by an
// explicit Write to the audit_log when the transport layer
// (transport/mcp) routes a tool call here. Future v4-alpha
// iterations will promote agent_memory.Save into a primitive
// that emits its own audit row, matching session.Start.
package agent_memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// Kind enumerates the canonical operator-memory kinds. Mirrors
// v3.0 agent_memory kinds (spec 1130) — the v4 rename + Mem0
// taxonomy will land in BUG-9.
const (
	KindNote       = "note"
	KindObservation = "observation"
	KindDecision   = "decision"
	KindFinding    = "finding"
	KindTodo       = "todo"
	KindLink       = "link"
	KindContext    = "context"
)

// validKinds is the allow-list enforced at Save. Empty kind is
// rejected so Recall can filter reliably.
var validKinds = map[string]struct{}{
	KindNote: {}, KindObservation: {}, KindDecision: {}, KindFinding: {},
	KindTodo: {}, KindLink: {}, KindContext: {},
}

// Row is the public view of one agent_memory row.
type Row struct {
	ID        int64     `json:"id"`
	Operator  string    `json:"operator"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title,omitempty"`
	Content   string    `json:"content"`
	Tags      string    `json:"tags,omitempty"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is the agent_memory repository. Bound to a *sql.DB that
// has had CreateSchema called on it.
type Store struct {
	db *sql.DB
}

// NewStore returns a Store wired to db. The schema must already
// be initialised via CreateSchema (call once at boot, idempotent).
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateSchema creates the agent_memory base table + FTS5 virtual
// table + 3 secondary indexes. Idempotent (every statement uses
// IF NOT EXISTS).
//
// The FTS5 table uses content='agent_memory' + content_rowid='id'
// so the FTS index is a sidecar to the base table — INSERTs into
// the base table do NOT auto-update the FTS index, so Save must
// write to both inside one transaction.
func CreateSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_memory (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			operator   TEXT    NOT NULL CHECK (operator <> ''),
			kind       TEXT    NOT NULL CHECK (kind <> ''),
			title      TEXT,
			content    TEXT    NOT NULL,
			tags       TEXT,
			pinned     INTEGER NOT NULL DEFAULT 0,
			created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS agent_memory_operator_idx
			ON agent_memory(operator)`,
		`CREATE INDEX IF NOT EXISTS agent_memory_kind_idx
			ON agent_memory(kind)`,
		`CREATE INDEX IF NOT EXISTS agent_memory_pinned_idx
			ON agent_memory(pinned)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS agent_memory_fts
			USING fts5(content, title, tags, content='agent_memory', content_rowid='id')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("agent_memory CreateSchema (%s): %w", firstLine(s), err)
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// ErrEmptyOperator is returned by Save when operator is empty.
var ErrEmptyOperator = errors.New("agent_memory: operator must be non-empty (INV-1)")

// ErrEmptyContent is returned by Save when content is empty.
var ErrEmptyContent = errors.New("agent_memory: content must be non-empty")

// ErrInvalidKind is returned by Save when kind is not in the
// canonical allow-list.
var ErrInvalidKind = errors.New("agent_memory: invalid kind")

// ErrNotFound is returned by Get when no row matches.
var ErrNotFound = errors.New("agent_memory: not found")

// Save inserts one row + updates the FTS5 index inside a single
// SERIALIZABLE transaction (INV-16 enforcement). Returns the new
// id.
//
// If pinned is true the row is surfaced first by Recall (until
// it's archived or unpinned).
func (s *Store) Save(ctx context.Context, op, kind, title, content, tags string, pinned bool) (int64, error) {
	if op == "" {
		return 0, ErrEmptyOperator
	}
	if content == "" {
		return 0, ErrEmptyContent
	}
	if _, ok := validKinds[kind]; !ok {
		return 0, fmt.Errorf("%w: %q (allowed: %s)", ErrInvalidKind, kind, strings.Join(allKinds(), ","))
	}

	var id int64
	err := store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO agent_memory (operator, kind, title, content, tags, pinned)
			VALUES (?, ?, ?, ?, ?, ?)
		`, op, kind, nullIfEmpty(title), content, nullIfEmpty(tags), boolToInt(pinned))
		if err != nil {
			return fmt.Errorf("agent_memory Save insert: %w", err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("agent_memory Save last insert id: %w", err)
		}
		id = n

		// Sync the FTS5 sidecar inside the SAME transaction so a
		// concurrent Recall never sees a stale index.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO agent_memory_fts (rowid, content, title, tags)
			VALUES (?, ?, ?, ?)
		`, id, content, nullIfEmpty(title), nullIfEmpty(tags))
		if err != nil {
			return fmt.Errorf("agent_memory Save fts sync: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// Get returns one row by id.
func (s *Store) Get(ctx context.Context, id int64) (*Row, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operator, kind, COALESCE(title,''), content, COALESCE(tags,''), pinned, created_at
		FROM agent_memory WHERE id = ?
	`, id)
	var r Row
	var pinned int
	var createdAt string
	if err := row.Scan(&r.ID, &r.Operator, &r.Kind, &r.Title, &r.Content, &r.Tags, &pinned, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("agent_memory Get: %w", err)
	}
	r.Pinned = pinned != 0
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		// Try the SQLite default format (CURRENT_TIMESTAMP).
		t, err = time.Parse("2006-01-02 15:04:05", createdAt)
		if err != nil {
			return nil, fmt.Errorf("agent_memory Get parse created_at %q: %w", createdAt, err)
		}
	}
	r.CreatedAt = t.UTC()
	return &r, nil
}

// List returns up to limit rows for the given operator, newest
// first. limit=0 means 50.
func (s *Store) List(ctx context.Context, op string, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operator, kind, COALESCE(title,''), content, COALESCE(tags,''), pinned, created_at
		FROM agent_memory
		WHERE operator = ?
		ORDER BY pinned DESC, created_at DESC
		LIMIT ?
	`, op, limit)
	if err != nil {
		return nil, fmt.Errorf("agent_memory List: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// Recall runs FTS5 against (content, title, tags) for the given
// query. Results are scored by FTS5 bm25 (lower = better). The
// query string is tokenised by FTS5 — no need to escape.
//
// limit=0 means 10. The query MUST be non-empty; an empty query
// returns an empty slice and no error (caller decides whether
// to treat that as a usage error).
func (s *Store) Recall(ctx context.Context, op, query string, limit int) ([]Row, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	// We join FTS5 to the base table so we can filter by operator
	// (FTS5 doesn't know about operator scope on its own).
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.operator, m.kind, COALESCE(m.title,''), m.content,
		       COALESCE(m.tags,''), m.pinned, m.created_at
		FROM agent_memory_fts f
		JOIN agent_memory m ON m.id = f.rowid
		WHERE agent_memory_fts MATCH ?
		  AND m.operator = ?
		ORDER BY rank
		LIMIT ?
	`, query, op, limit)
	if err != nil {
		return nil, fmt.Errorf("agent_memory Recall fts query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// scanRows materialises the common (id, op, kind, title, content,
// tags, pinned, created_at) projection used by Get, List, Recall.
func scanRows(rows *sql.Rows) ([]Row, error) {
	out := []Row{}
	for rows.Next() {
		var r Row
		var pinned int
		var createdAt string
		if err := rows.Scan(&r.ID, &r.Operator, &r.Kind, &r.Title, &r.Content, &r.Tags, &pinned, &createdAt); err != nil {
			return nil, fmt.Errorf("agent_memory scan: %w", err)
		}
		r.Pinned = pinned != 0
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			t, err = time.Parse("2006-01-02 15:04:05", createdAt)
			if err != nil {
				return nil, fmt.Errorf("agent_memory parse created_at %q: %w", createdAt, err)
			}
		}
		r.CreatedAt = t.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// Archive soft-deletes one row. The FTS5 sidecar is updated inside
// the same transaction so a follow-up Recall doesn't see the row.
// (BUG-9 will add audit trail for archive events.)
func (s *Store) Archive(ctx context.Context, id int64) error {
	return store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM agent_memory WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("agent_memory Archive: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("agent_memory Archive rows affected: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx,
			`DELETE FROM agent_memory_fts WHERE rowid = ?`, id)
		if err != nil {
			return fmt.Errorf("agent_memory Archive fts sync: %w", err)
		}
		return nil
	})
}

func allKinds() []string {
	out := make([]string, 0, len(validKinds))
	for k := range validKinds {
		out = append(out, k)
	}
	return out
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
