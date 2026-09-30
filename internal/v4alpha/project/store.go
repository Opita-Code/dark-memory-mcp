// Package project — store. Implements the project lifecycle:
// Create (idempotent), Lookup, Archive (soft delete), List.
//
// Wire-up:
//
//	store, err := project.NewStore(db, auditWriter)
//	if err != nil { return err }
//	if err := project.CreateSchema(db); err != nil { return err }
//	if err := project.ApplyProjectIDColumns(ctx, db); err != nil { return err }
//
// The audit writer is required (not optional). Every successful
// Create emits ONE INV-1 audit row tagged with actor + project_id.
// This is the INV-1 + INV-7 enforcement for project lifecycle.
package project

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// Store is the v4 project lifecycle store. It wraps a *sql.DB and
// an *audit.Writer. All operations are safe to call from multiple
// goroutines (SQLite is serialized at the connection level, and
// modernc.org/sqlite uses a per-conn mutex — see INV-16).
type Store struct {
	db    *sql.DB
	audit *audit.Writer
}

// NewStore returns a Store wired to db and w. db must be initialized
// (CreateSchema + ApplyProjectIDColumns called) before any Store
// operation. w must be a non-nil audit.Writer — every Create emits
// one INV-1 audit row.
func NewStore(db *sql.DB, w *audit.Writer) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("project NewStore: db is nil")
	}
	if w == nil {
		return nil, fmt.Errorf("project NewStore: audit.Writer is nil (INV-1 requires audit on Create)")
	}
	return &Store{db: db, audit: w}, nil
}

// DB returns the underlying *sql.DB. Read-only access for tests
// and helpers that need to run schema introspection queries
// (e.g., pragma_table_info). Production callers should prefer
// the Store methods (Create / Lookup / Archive / List).
func (s *Store) DB() *sql.DB {
	return s.db
}

// Create inserts a new project row. Idempotent on project_id:
//
//   - First call: INSERTs the row, sets CreatedAt, emits audit row.
//   - Replay (same project_id): returns the existing row without
//     modification. Mutable fields (DisplayName, Description,
//     DefaultAgentID) are NOT updated on replay — the operator
//     must Archive + re-Create to rename.
//
// Returns:
//
//   - ErrReservedProjectID if project_id is 'default' or 'dark'.
//   - ErrInvalidProjectID if the kebab-case regex fails.
//   - ErrInvalidProject for other validation failures (display_name,
//     description, default_agent_id length).
//   - nil on success (whether new or replay).
//
// The CreatedAt timestamp is server-side (time.Now().UTC() in the
// Store, not the caller). Caller-supplied CreatedAt is IGNORED —
// the Store overwrites it. Same posture as audit.Writer.Write.
func (s *Store) Create(ctx context.Context, p *Project) error {
	if s == nil {
		return ErrInvalidProject
	}
	if err := p.Validate(); err != nil {
		return err
	}

	// Normalize display_name before INSERT so the canonical form
	// is stable (whitespace collapsed). Description is left as-is
	// (free-form prose, whitespace may matter).
	p.DisplayName = NormalizeDisplayName(p.DisplayName)

	// INSERT OR IGNORE — first writer wins on race.
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO projects (project_id, display_name, description, default_agent_id)
		 VALUES (?, ?, ?, ?)`,
		p.ProjectID, p.DisplayName, p.Description, p.DefaultAgentID,
	)
	if err != nil {
		return fmt.Errorf("project Store.Create insert: %w", err)
	}

	// Always fetch the actual row (whether we just inserted it or
	// a concurrent caller beat us). This gives the caller the
	// canonical CreatedAt and surfaces idempotent replay semantics.
	row, err := s.lookupByID(ctx, p.ProjectID)
	if err != nil {
		return err
	}

	// Copy server-side fields back to caller so they see the
	// canonical row (especially CreatedAt, which they didn't set).
	*p = *row

	// Emit one INV-1 audit row only if this call actually inserted.
	// Replay should NOT double-audit.
	affected, _ := res.RowsAffected()
	if affected > 0 {
		_, auditErr := s.audit.WriteWithProject(ctx, "project_create", "", p.ProjectID,
			[]byte(fmt.Sprintf(`{"project_id":%q,"display_name":%q}`, p.ProjectID, p.DisplayName)))
		if auditErr != nil {
			// Audit failure is non-fatal — the project row exists.
			// We log via the error return so the operator sees it.
			return fmt.Errorf("project Store.Create: row inserted but audit failed: %w", auditErr)
		}
	}
	return nil
}

// Lookup returns the Project for projectID, or ErrProjectNotFound.
//
// Includes archived projects (callers can check IsArchived()). For
// an active-only variant, use List(ctx, false) and scan.
func (s *Store) Lookup(ctx context.Context, projectID string) (*Project, error) {
	if s == nil {
		return nil, ErrInvalidProject
	}
	if projectID == "" {
		return nil, fmt.Errorf("%w: project_id is required", ErrInvalidProject)
	}
	return s.lookupByID(ctx, projectID)
}

// lookupByID is the unexported helper used by Create (to fetch the
// canonical row) and Lookup (the public API).
func (s *Store) lookupByID(ctx context.Context, projectID string) (*Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT project_id, display_name, description, default_agent_id, created_at, archived_at
		 FROM projects WHERE project_id = ?`,
		projectID,
	)
	var (
		p          Project
		createdAt  string
		archivedAt sql.NullString
	)
	err := row.Scan(&p.ProjectID, &p.DisplayName, &p.Description,
		&p.DefaultAgentID, &createdAt, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %q", ErrProjectNotFound, projectID)
	}
	if err != nil {
		return nil, fmt.Errorf("project Store.lookupByID scan: %w", err)
	}

	if t, perr := time.Parse(time.RFC3339Nano, createdAt); perr == nil {
		p.CreatedAt = t.UTC()
	}
	if archivedAt.Valid && archivedAt.String != "" {
		if t, perr := time.Parse(time.RFC3339Nano, archivedAt.String); perr == nil {
			utc := t.UTC()
			p.ArchivedAt = &utc
		}
	}
	return &p, nil
}

// Archive soft-deletes the project by setting archived_at to the
// current UTC time. Idempotent: re-archiving an already-archived
// project returns ErrProjectAlreadyGone (NOT a new error —
// preserves caller intent: "you thought you had an active project,
// but it was already gone").
//
// Returns:
//
//   - ErrProjectNotFound if the project_id does not exist at all.
//   - ErrProjectAlreadyGone if the project is already archived.
//   - nil on success.
//
// Phase 4 Chunk 4.3's session_start MAY want to reject calls whose
// project_id is archived. That's a caller-side check using
// IsArchived(); Archive does not enforce it (allows un-archive by
// UPDATE — out of scope for alpha.17).
func (s *Store) Archive(ctx context.Context, projectID string) error {
	if s == nil {
		return ErrInvalidProject
	}
	if projectID == "" {
		return fmt.Errorf("%w: project_id is required", ErrInvalidProject)
	}

	// 1. Verify project exists and is NOT already archived.
	existing, err := s.lookupByID(ctx, projectID)
	if err != nil {
		return err
	}
	if existing.IsArchived() {
		return fmt.Errorf("%w: %q was archived at %s",
			ErrProjectAlreadyGone, projectID, existing.ArchivedAt.Format(time.RFC3339))
	}

	// 2. Soft-delete via UPDATE.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET archived_at = ? WHERE project_id = ? AND archived_at IS NULL`,
		now, projectID,
	)
	if err != nil {
		return fmt.Errorf("project Store.Archive update: %w", err)
	}

	// 3. Audit (only if we actually changed the row).
	affected, _ := res.RowsAffected()
	if affected > 0 {
		_, auditErr := s.audit.WriteWithProject(ctx, "project_archive", "", projectID,
			[]byte(fmt.Sprintf(`{"project_id":%q,"archived_at":%q}`, projectID, now)))
		if auditErr != nil {
			return fmt.Errorf("project Store.Archive: row updated but audit failed: %w", auditErr)
		}
	}
	return nil
}

// List returns all projects ordered by created_at ASC. If
// includeArchived is false, archived projects are filtered out.
//
// Returns ErrInvalidProject only on nil receiver (defensive).
func (s *Store) List(ctx context.Context, includeArchived bool) ([]*Project, error) {
	if s == nil {
		return nil, ErrInvalidProject
	}

	q := `SELECT project_id, display_name, description, default_agent_id, created_at, archived_at
	      FROM projects`
	if !includeArchived {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("project Store.List query: %w", err)
	}
	defer rows.Close()

	var out []*Project
	for rows.Next() {
		var (
			p          Project
			createdAt  string
			archivedAt sql.NullString
		)
		if err := rows.Scan(&p.ProjectID, &p.DisplayName, &p.Description,
			&p.DefaultAgentID, &createdAt, &archivedAt); err != nil {
			return nil, fmt.Errorf("project Store.List scan: %w", err)
		}
		if t, perr := time.Parse(time.RFC3339Nano, createdAt); perr == nil {
			p.CreatedAt = t.UTC()
		}
		if archivedAt.Valid && archivedAt.String != "" {
			if t, perr := time.Parse(time.RFC3339Nano, archivedAt.String); perr == nil {
				utc := t.UTC()
				p.ArchivedAt = &utc
			}
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project Store.List rows.Err: %w", err)
	}
	return out, nil
}

// Notes on the sentinel error discrimination:
//
// Callers use errors.Is(err, project.ErrReservedProjectID) etc.
// The fmt.Errorf wraps in Create/Lookup/Archive preserve the
// sentinel via %w so errors.Is works transitively. The
// `var _ = ...` line was removed in favor of this comment block.