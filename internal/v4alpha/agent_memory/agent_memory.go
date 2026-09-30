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
//     identifiable)
//     2. FTS5 index is kept in sync with the base table inside
//     one transaction (write-then-search is consistent)
//     3. kind is one of the canonical set (note, observation,
//     decision, finding, todo, link, context) — empty kind is
//     rejected to keep Recall results queryable
//
// INV-1 audit integration (ADR-007 C3): Save/Update/Archive emit
// exactly one audit_log row each, INSIDE the same store.WithTx as
// the agent_memory row insert/update/delete. Both rows are
// atomic — they succeed together or roll back together. The
// audit emission uses audit.Writer.WriteExec so the audit row
// participates in the transaction (tx-aware Executor path).
//
// Backwards compat (ADR-007 §6): the transport layer (transport/mcp)
// is the only production caller and always threads a non-nil *Audit
// (actor = operator id). Future surface (CLI, dark-copilot) follows
// the same convention.
package agent_memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// Kind enumerates the canonical operator-memory kinds. Mirrors
// v3.0 agent_memory kinds (spec 1130) — the v4 rename + Mem0
// taxonomy will land in BUG-9.
const (
	KindNote        = "note"
	KindObservation = "observation"
	KindDecision    = "decision"
	KindFinding     = "finding"
	KindTodo        = "todo"
	KindLink        = "link"
	KindContext     = "context"
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

// Audit is the INV-1 audit metadata threaded through every
// state-mutating call (Save/Update/Archive). Actor is required
// (INV-1 enforcement lives in the audit package). SessionID and
// ProjectID are optional; empty SessionID is stored as SQL NULL
// in audit_log to keep JOIN queries clean.
type Audit struct {
	Actor     string // required, INV-1 audit owner
	SessionID string // optional, empty → NULL
	ProjectID string // optional, reserved for INV-7 future work
}

// Store is the agent_memory repository. Bound to a *sql.DB and an
// audit.Writer. The schema must already be initialised via
// CreateSchema (call once at boot, idempotent).
type Store struct {
	db    *sql.DB
	audit *audit.Writer
}

// NewStore returns a Store wired to db and w. Both must be
// initialized (CreateSchema on db; NewWriter for w). The audit
// Writer is mandatory — there is no "no-audit" mode. INV-1 is a
// non-negotiable invariant; see ADR-007 §5.3.
func NewStore(db *sql.DB, w *audit.Writer) *Store {
	return &Store{db: db, audit: w}
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
			created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT
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

// Save inserts one row + updates the FTS5 index + emits one
// audit_log row inside a single SERIALIZABLE transaction (INV-16
// + INV-1 enforcement). Returns the new id.
//
// If pinned is true the row is surfaced first by Recall (until
// it's archived or unpinned).
//
// audit is the INV-1 metadata: Actor must be non-empty. When
// auditMeta.ProjectID is non-empty, the audit row is stamped with
// that project_id via audit.Writer.WriteExecWithProject (Phase 4
// Chunk 4.3 hard isolation). Empty project_id falls back to the
// legacy WriteExec (audit row gets project_id='default' via column
// DEFAULT). The audit row, the agent_memory row, and the FTS5 sync
// all commit atomically; a failure at any step rolls back all
// three.
func (s *Store) Save(ctx context.Context, auditMeta *Audit, op, kind, title, content, tags string, pinned bool) (int64, error) {
	if op == "" {
		return 0, ErrEmptyOperator
	}
	if content == "" {
		return 0, ErrEmptyContent
	}
	if _, ok := validKinds[kind]; !ok {
		return 0, fmt.Errorf("%w: %q (allowed: %s)", ErrInvalidKind, kind, strings.Join(allKinds(), ","))
	}
	if auditMeta == nil || auditMeta.Actor == "" {
		return 0, ErrEmptyOperator // audit.Actor == "" is INV-1 violation
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

		// Emit the INV-1 audit row inside the same transaction.
		// If anything below this line fails (or the tx is rolled
		// back), the audit_log row is undone together with the
		// agent_memory insert — no phantom audit rows.
		payload := []byte(fmt.Sprintf(
			`{"event":"agent_memory.save","id":%d,"operator":%q,"kind":%q}`,
			id, op, kind,
		))
		if err := writeAuditWithProject(ctx, s.audit, tx, auditMeta, payload); err != nil {
			return fmt.Errorf("agent_memory Save audit: %w", err)
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
	return s.ListFiltered(ctx, op, ListFilter{}, limit)
}

// Recall runs FTS5 against (content, title, tags) for the given
// query. Results are scored by FTS5 bm25 (lower = better). The
// query string is tokenised by FTS5 — no need to escape.
//
// limit=0 means 10. The query MUST be non-empty; an empty query
// returns an empty slice and no error (caller decides whether
// to treat that as a usage error).
func (s *Store) Recall(ctx context.Context, op, query string, limit int) ([]Row, error) {
	return s.RecallFiltered(ctx, op, query, RecallFilter{}, limit)
}

// RecallFilter is the optional set of filters for RecallFiltered.
// All fields are zero-value = no filter applied.
//
// PRE-1 C1: this struct unlocks the loadout protocol. The
// transport layer (memory.go) exposes the equivalent JSON
// fields to MCP callers. See package doc for the design.
type RecallFilter struct {
	TagPrefix string    // filter to rows whose tags CSV contains a tag starting with this. Empty = no filter. Example: "doc-index:" returns all rows tagged doc-index:v1, doc-index:v2, etc.
	Kind      string    // filter to one canonical kind. Empty = all kinds. Validated against validKinds; empty is the only "no filter" value.
	Since     time.Time // filter to rows with created_at >= this time. Zero = no filter. Stored as RFC3339Nano for cross-version compat; compared lexicographically against the SQLite CURRENT_TIMESTAMP "YYYY-MM-DD HH:MM:SS" format (works because both sort correctly).
}

// ListFilter is the optional set of filters for ListFiltered.
// All fields are zero-value = no filter applied.
//
// PRE-1 C1: ListFilter is the read-mostly path counterpart to
// RecallFilter. It does NOT run FTS5 (use Recall for that);
// it just filters the base table by the given criteria.
type ListFilter struct {
	Kind   string    // filter to one canonical kind. Empty = all kinds.
	Tag    string    // filter to rows whose tags CSV contains this exact tag. Empty = no filter.
	Pinned *bool     // filter to pinned (true) or unpinned (false). nil = all.
	Since  time.Time // filter to rows with created_at >= this time. Zero = no filter.
}

// ListFiltered returns up to limit rows for the given operator
// that match the filter, pinned first then newest. limit=0
// means 50.
//
// PRE-1 C1: new method. The old List() is a thin wrapper.
// The transport layer (memory.go) builds a ListFilter from
// the input JSON and calls this directly so the MCP tool
// surface can offer the new filters without breaking the
// old "operator + limit" contract.
func (s *Store) ListFiltered(ctx context.Context, op string, filter ListFilter, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = 50
	}

	// Build the WHERE clause dynamically. Empty filter fields
	// contribute no constraint. We always filter by operator
	// (INV-2: operator scope is the primary isolation axis).
	where := []string{"operator = ?"}
	args := []interface{}{op}

	if filter.Kind != "" {
		if _, ok := validKinds[filter.Kind]; !ok {
			return nil, fmt.Errorf("%w: kind=%q", ErrInvalidKind, filter.Kind)
		}
		where = append(where, "kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.Tag != "" {
		// Tag exact match inside the CSV. Same trick as
		// TagPrefix: surround with synthetic commas, single
		// LIKE pattern.
		where = append(where, "(',' || tags || ',') LIKE ? ESCAPE '\\'")
		args = append(args, "%,"+escapeLike(filter.Tag)+",%")
	}
	if filter.Pinned != nil {
		where = append(where, "pinned = ?")
		args = append(args, boolToInt(*filter.Pinned))
	}
	if !filter.Since.IsZero() {
		where = append(where, "created_at >= ?")
		// SQLite CURRENT_TIMESTAMP returns "YYYY-MM-DD HH:MM:SS"
		// UTC. We compare as TEXT (lexicographic) which works
		// because both formats sort the same.
		args = append(args, filter.Since.UTC().Format("2006-01-02 15:04:05"))
	}

	q := `SELECT id, operator, kind, COALESCE(title,''), content, COALESCE(tags,''), pinned, created_at
	      FROM agent_memory WHERE ` + joinAnd(where) + `
	      ORDER BY pinned DESC, created_at DESC
	      LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("agent_memory ListFiltered: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// RecallFiltered runs FTS5 against (content, title, tags) for the
// given query, then applies the optional filters. Results are
// scored by FTS5 bm25 (lower = better).
//
// limit=0 means 10. The query MUST be non-empty; an empty query
// returns an empty slice and no error.
//
// PRE-1 C1: this is the new method the MCP transport calls.
// The old Recall() is a thin wrapper for backwards compat.
func (s *Store) RecallFiltered(ctx context.Context, op, query string, filter RecallFilter, limit int) ([]Row, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	where := []string{"agent_memory_fts MATCH ?", "m.operator = ?"}
	args := []interface{}{query, op}

	if filter.TagPrefix != "" {
		// Tag prefix match: any tag in the CSV that starts with
		// the prefix. The CSV is "tag1,tag2,tag3"; we surround
		// it with synthetic commas and then LIKE for "%,<prefix>%"
		// which matches at any comma boundary. This unifies all
		// 4 cases (only tag / first / middle / last) into one
		// pattern.
		where = append(where, "(',' || m.tags || ',') LIKE ? ESCAPE '\\'")
		args = append(args, "%,"+escapeLike(filter.TagPrefix)+"%")
	}
	if filter.Kind != "" {
		if _, ok := validKinds[filter.Kind]; !ok {
			return nil, fmt.Errorf("%w: kind=%q", ErrInvalidKind, filter.Kind)
		}
		where = append(where, "m.kind = ?")
		args = append(args, filter.Kind)
	}
	if !filter.Since.IsZero() {
		where = append(where, "m.created_at >= ?")
		args = append(args, filter.Since.UTC().Format("2006-01-02 15:04:05"))
	}

	// We join FTS5 to the base table so we can filter by operator
	// (FTS5 doesn't know about operator scope on its own).
	q := `SELECT m.id, m.operator, m.kind, COALESCE(m.title,''), m.content,
	             COALESCE(m.tags,''), m.pinned, m.created_at
	      FROM agent_memory_fts f
	      JOIN agent_memory m ON m.id = f.rowid
	      WHERE ` + joinAnd(where) + `
	      ORDER BY rank
	      LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("agent_memory RecallFiltered: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// joinAnd joins WHERE clauses with " AND ".
func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

// escapeLike escapes SQLite LIKE wildcards (% and _) and the
// escape character (\) in s so the value is treated as literal.
// The caller MUST use `ESCAPE '\'` in the LIKE clause.
//
// Example:
//   escapeLike("doc-index:") → "doc-index:"
//   escapeLike("a%b_c")      → "a\\%b\\_c"
//   escapeLike("a\\b")       → "a\\\\b"
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
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

// Archive soft-deletes one row. The FTS5 sidecar is updated + one
// audit_log row is emitted inside the same transaction so a
// follow-up Recall doesn't see the row AND a follow-up audit
// query shows the archive event. All three operations are atomic.
//
// audit is the INV-1 metadata; Actor must be non-empty. When
// auditMeta.ProjectID is non-empty, the audit row is stamped via
// WriteExecWithProject (Phase 4 Chunk 4.3); empty falls back to
// WriteExec.
//
// FTS5 quirk (resolved): earlier revisions used a contentless
// FTS5 table (`content='agent_memory'`) which required the FTS5
// 'delete' command for row removal. modernc.org/sqlite v1.53
// returned SQLITE_CORRUPT (267) on those commands in some
// journal modes. The schema now uses a regular FTS5 table that
// stores its own copy — plain DELETE works.
func (s *Store) Archive(ctx context.Context, auditMeta *Audit, id int64) error {
	if auditMeta == nil || auditMeta.Actor == "" {
		return ErrEmptyOperator // INV-1
	}
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
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_memory_fts WHERE rowid = ?`, id); err != nil {
			return fmt.Errorf("agent_memory Archive fts sync: %w", err)
		}
		// INV-1 audit emission inside the same tx.
		payload := []byte(fmt.Sprintf(
			`{"event":"agent_memory.archive","id":%d}`, id))
		if err := writeAuditWithProject(ctx, s.audit, tx, auditMeta, payload); err != nil {
			return fmt.Errorf("agent_memory Archive audit: %w", err)
		}
		return nil
	})
}

// Update mutates mutable fields of one agent_memory row. Fields
// with the empty string or zero value are NOT overwritten (NULL
// semantics): pass *string for the optionals, *bool for pinned.
// Operator and kind are immutable (INV-1 audit identity); to
// change them, archive + save a new row.
//
// FTS5 sync: content + title + tags are re-indexed inside the
// same transaction. updated_at is set to CURRENT_TIMESTAMP so
// downstream List can sort by recency. One audit_log row is
// emitted inside the same transaction for INV-1.
//
// audit is the INV-1 metadata; Actor must be non-empty.
//
// Returns ErrNotFound when the id does not exist.
func (s *Store) Update(ctx context.Context, auditMeta *Audit, id int64, title, content, tags *string, pinned *bool) error {
	if auditMeta == nil || auditMeta.Actor == "" {
		return ErrEmptyOperator // INV-1
	}
	if content != nil && *content == "" {
		return fmt.Errorf("agent_memory Update: content cannot be empty")
	}
	return store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		// Verify the row exists first so we can return ErrNotFound
		// before constructing the UPDATE statement.
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM agent_memory WHERE id = ?`, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("agent_memory Update exists check: %w", err)
		}
		// Build UPDATE dynamically. title + tags can be NULL (skip).
		setClauses := []string{}
		args := []interface{}{}
		if title != nil {
			setClauses = append(setClauses, "title = ?")
			args = append(args, nullIfEmpty(*title))
		}
		if content != nil {
			setClauses = append(setClauses, "content = ?")
			args = append(args, *content)
		}
		if tags != nil {
			setClauses = append(setClauses, "tags = ?")
			args = append(args, nullIfEmpty(*tags))
		}
		if pinned != nil {
			setClauses = append(setClauses, "pinned = ?")
			args = append(args, boolToInt(*pinned))
		}
		if len(setClauses) == 0 {
			// Nothing to update — return success without writing.
			// Still emit the audit row so INV-1 covers "no-op update"
			// attempts (operator wanted to update, but nothing
			// changed — record the attempt for the audit trail).
			payload := []byte(fmt.Sprintf(
				`{"event":"agent_memory.update.noop","id":%d}`, id))
			if err := writeAuditWithProject(ctx, s.audit, tx, auditMeta, payload); err != nil {
				return fmt.Errorf("agent_memory Update audit: %w", err)
			}
			return nil
		}
		// FTS5 sync FIRST (before the base UPDATE). modernc.org/sqlite
		// v1.53 has a quirk where an FTS5 DELETE immediately following
		// a base-table UPDATE inside the same SERIALIZABLE Tx raises
		// SQLITE_CORRUPT (267) — "database disk image is malformed".
		// Doing the FTS5 delete first sidesteps the issue: the base
		// row still exists, so the index entry is the only stale thing.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_memory_fts WHERE rowid = ?`, id); err != nil {
			return fmt.Errorf("agent_memory Update fts delete: %w", err)
		}
		// Always bump updated_at so the row reflects the change.
		setClauses = append(setClauses, "updated_at = CURRENT_TIMESTAMP")
		args = append(args, id)
		q := "UPDATE agent_memory SET "
		for i, c := range setClauses {
			if i > 0 {
				q += ", "
			}
			q += c
		}
		q += " WHERE id = ?"
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("agent_memory Update: %w", err)
		}
		// Re-index the FTS5 with the new (now-current) content.
		var newTitle, newContent, newTags string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(title,''), content, COALESCE(tags,'') FROM agent_memory WHERE id = ?`,
			id).Scan(&newTitle, &newContent, &newTags); err != nil {
			return fmt.Errorf("agent_memory Update fts re-read: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_memory_fts(rowid, title, content, tags) VALUES (?, ?, ?, ?)`,
			id, newTitle, newContent, newTags); err != nil {
			return fmt.Errorf("agent_memory Update fts insert: %w", err)
		}
		// INV-1 audit emission inside the same tx.
		fields := []string{}
		if title != nil {
			fields = append(fields, "title")
		}
		if content != nil {
			fields = append(fields, "content")
		}
		if tags != nil {
			fields = append(fields, "tags")
		}
		if pinned != nil {
			fields = append(fields, "pinned")
		}
		payload := []byte(fmt.Sprintf(
			`{"event":"agent_memory.update","id":%d,"fields":%q}`,
			id, strings.Join(fields, ",")))
		if err := writeAuditWithProject(ctx, s.audit, tx, auditMeta, payload); err != nil {
			return fmt.Errorf("agent_memory Update audit: %w", err)
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

// writeAuditWithProject (Phase 4 Chunk 4.3) emits one audit_log
// row inside the caller's transaction. When auditMeta.ProjectID is
// non-empty, the row is stamped with that project_id via
// audit.Writer.WriteExecWithProject. Empty project_id falls back
// to audit.Writer.WriteExec (the row gets project_id='default' via
// the column DEFAULT clause).
//
// The audit package's `sqlExec` interface is unexported, so we
// can't re-declare it here. Instead we accept *sql.Tx directly:
// *sql.Tx is the only concrete type passed by Save/Update/Archive
// (all three are inside store.WithTx). It satisfies audit.sqlExec
// implicitly.
//
// Centralised so Save/Update/Archive share the same branching
// logic — keeps the audit emission policy in one place.
func writeAuditWithProject(ctx context.Context, w *audit.Writer, tx *sql.Tx, meta *Audit, payload []byte) error {
	if meta.ProjectID != "" {
		_, err := w.WriteExecWithProject(ctx, tx, meta.Actor, meta.SessionID, meta.ProjectID, payload)
		return err
	}
	_, err := w.WriteExec(ctx, tx, meta.Actor, meta.SessionID, payload)
	return err
}
