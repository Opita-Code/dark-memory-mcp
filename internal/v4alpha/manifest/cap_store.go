package manifest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors for CapStore operations. Wrapped with %w so callers
// can use errors.Is for discrimination.
var (
	// ErrCapExists is returned by Grant when a token with the same ID
	// is already persisted. CapToken IDs are immutable; re-granting the
	// same id is rejected so audit history stays linear.
	ErrCapExists = errors.New("manifest: cap token already exists")

	// ErrCapNotFound is returned by Get and Revoke when the requested
	// id has no matching row. Check never returns it: an operator with
	// no active tokens gets an empty (non-nil) slice instead.
	ErrCapNotFound = errors.New("manifest: cap token not found")

	// ErrCapAlreadyRevoked is returned by Revoke when the token's
	// revoked_at is already non-zero. Revocation is idempotent at the
	// API level (Revoke returns nil on already-revoked) but the strict
	// variant ErrCapAlreadyRevoked is exposed for callers that want to
	// distinguish "I revoked it" from "someone else already did".
	ErrCapAlreadyRevoked = errors.New("manifest: cap token already revoked")
)

// CapStore persists CapTokens in a SQLite table. It is safe to share
// across goroutines as long as the underlying *sql.DB is shared (which
// it always is in modernc.org/sqlite, mattn/go-sqlite3, and
// ncruces/go-strbase).
//
// INV-12: every Grant emits an Ed25519 signature (the caller is
// responsible for populating Signature before calling Grant).
//
// INV-13: Revoke never deletes rows. The revoked_at column is updated
// in place; the original GrantedAt and Signature are preserved for
// audit replay.
//
// Schema:
//
//	CREATE TABLE capabilities (
//	    id          TEXT PRIMARY KEY,
//	    operator    TEXT NOT NULL,
//	    scopes_json TEXT NOT NULL,
//	    granted_at  INTEGER NOT NULL,  -- unix seconds (UTC)
//	    expires_at  INTEGER NOT NULL,  -- unix seconds (UTC)
//	    revoked_at  INTEGER NOT NULL DEFAULT 0,  -- 0 = not revoked
//	    signature   BLOB NOT NULL,
//	    signed_by   TEXT NOT NULL
//	);
type CapStore struct {
	db *sql.DB
}

// NewCapStore returns a CapStore bound to db. The caller must have
// already invoked CreateCapSchema on db (or a database with a compatible
// capabilities table).
func NewCapStore(db *sql.DB) *CapStore {
	return &CapStore{db: db}
}

// CreateCapSchema installs the capabilities table and its supporting
// index. Idempotent: safe to call on every startup. The CHECK
// constraints mirror the runtime Validate invariants so the schema is
// self-defending even if a buggy caller bypasses Validate.
func CreateCapSchema(ctx context.Context, db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS capabilities (
    id          TEXT PRIMARY KEY,
    operator    TEXT NOT NULL,
    scopes_json TEXT NOT NULL,
    granted_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    revoked_at  INTEGER NOT NULL DEFAULT 0,
    signature   BLOB NOT NULL,
    signed_by   TEXT NOT NULL,
    CHECK (length(id) > 0),
    CHECK (length(operator) > 0),
    CHECK (length(scopes_json) > 2),
    CHECK (expires_at > granted_at),
    CHECK (length(signed_by) > 0)
);

CREATE INDEX IF NOT EXISTS idx_cap_operator_active
    ON capabilities(operator, revoked_at, expires_at);
`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("manifest: create cap schema: %w", err)
	}
	return nil
}

// Grant inserts c into the capabilities table. c.Validate() must pass;
// otherwise Grant returns the validation error wrapped.
//
// On duplicate id, returns ErrCapExists.
func (s *CapStore) Grant(ctx context.Context, c *CapToken) error {
	if c == nil {
		return fmt.Errorf("manifest: %w", ErrEmptyCapID)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	scopesJSON, err := json.Marshal(c.Scopes)
	if err != nil {
		return fmt.Errorf("manifest: marshal scopes: %w", err)
	}
	const q = `
INSERT INTO capabilities (id, operator, scopes_json, granted_at, expires_at, revoked_at, signature, signed_by)
VALUES (?, ?, ?, ?, ?, 0, ?, ?)
`
	_, err = s.db.ExecContext(ctx, q,
		c.ID, c.Operator, string(scopesJSON),
		c.GrantedAt.Unix(), c.ExpiresAt.Unix(),
		c.Signature, c.SignedBy,
	)
	if err != nil {
		if isDuplicateKey(err) {
			return fmt.Errorf("manifest: %w: id=%s", ErrCapExists, c.ID)
		}
		return fmt.Errorf("manifest: grant cap: %w", err)
	}
	return nil
}

// Get returns the token with the given id, or ErrCapNotFound if absent.
// Revoked tokens are still returned — use IsActive to filter for current
// validity.
func (s *CapStore) Get(ctx context.Context, id string) (*CapToken, error) {
	if id == "" {
		return nil, fmt.Errorf("manifest: %w", ErrCapNotFound)
	}
	const q = `
SELECT id, operator, scopes_json, granted_at, expires_at, revoked_at, signature, signed_by
FROM capabilities
WHERE id = ?
`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanCap(row)
}

// Revoke marks the token as revoked at the given moment. Returns nil on
// success. If the token is already revoked, returns ErrCapAlreadyRevoked.
// To revoke "even if already revoked, idempotently", use RevokeIdempotent.
func (s *CapStore) Revoke(ctx context.Context, id string, revokedAt time.Time) error {
	return s.revokeWhere(ctx, id, revokedAt, false)
}

// RevokeIdempotent is like Revoke but returns nil when the token is
// already revoked (no error). Use this from operator UIs where a
// double-click should not surface an error to the operator.
func (s *CapStore) RevokeIdempotent(ctx context.Context, id string, revokedAt time.Time) error {
	return s.revokeWhere(ctx, id, revokedAt, true)
}

func (s *CapStore) revokeWhere(ctx context.Context, id string, revokedAt time.Time, idempotent bool) error {
	if id == "" {
		return fmt.Errorf("manifest: %w", ErrCapNotFound)
	}
	if revokedAt.IsZero() {
		return fmt.Errorf("manifest: %w: revokedAt is zero", ErrCapAlreadyRevoked)
	}
	const qStrict = `
UPDATE capabilities
SET revoked_at = ?
WHERE id = ? AND revoked_at = 0
`
	const qAny = `
UPDATE capabilities
SET revoked_at = ?
WHERE id = ?
`
	var (
		res sql.Result
		err error
	)
	if idempotent {
		res, err = s.db.ExecContext(ctx, qAny, revokedAt.Unix(), id)
	} else {
		res, err = s.db.ExecContext(ctx, qStrict, revokedAt.Unix(), id)
	}
	if err != nil {
		return fmt.Errorf("manifest: revoke cap: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("manifest: revoke cap rows: %w", err)
	}
	if n == 0 {
		// Disambiguate: does the row not exist, or is it already revoked?
		var existing int64
		err := s.db.QueryRowContext(ctx, `SELECT revoked_at FROM capabilities WHERE id = ?`, id).Scan(&existing)
		if err == sql.ErrNoRows {
			return fmt.Errorf("manifest: %w: id=%s", ErrCapNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("manifest: revoke cap probe: %w", err)
		}
		if existing > 0 {
			if idempotent {
				return nil
			}
			return fmt.Errorf("manifest: %w: id=%s", ErrCapAlreadyRevoked, id)
		}
		// Should not reach here, but defensively return not-found.
		return fmt.Errorf("manifest: %w: id=%s", ErrCapNotFound, id)
	}
	return nil
}

// Check returns the active tokens for the given operator at the given
// moment. "Active" means: not revoked AND expires_at > at. The returned
// slice is ordered by granted_at ASC (oldest first) so callers can
// reason about precedence deterministically.
//
// Returns an empty slice (not nil) if no active tokens match.
func (s *CapStore) Check(ctx context.Context, operator string, at time.Time) ([]*CapToken, error) {
	if operator == "" {
		return []*CapToken{}, nil
	}
	const q = `
SELECT id, operator, scopes_json, granted_at, expires_at, revoked_at, signature, signed_by
FROM capabilities
WHERE operator = ?
  AND revoked_at = 0
  AND expires_at > ?
ORDER BY granted_at ASC
`
	rows, err := s.db.QueryContext(ctx, q, operator, at.Unix())
	if err != nil {
		return nil, fmt.Errorf("manifest: check caps: %w", err)
	}
	defer rows.Close()
	out := []*CapToken{}
	for rows.Next() {
		c, err := scanCapRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("manifest: check caps iterate: %w", err)
	}
	return out, nil
}

// scanCap reads a single row from QueryRow. Uses scanCapRows internally.
func scanCap(row *sql.Row) (*CapToken, error) {
	// *sql.Row lacks Next/Scanner methods, so we bridge via a tiny adapter.
	rr := &rowAdapter{row: row}
	return scanCapRows(rr)
}

// rowAdapter lets scanCapRows accept both *sql.Row (single-row) and
// *sql.Rows (iterator) via the rowScanner interface.
type rowAdapter struct {
	row *sql.Row
}

func (r *rowAdapter) Scan(dest ...any) error {
	return r.row.Scan(dest...)
}

// rowScanner is the minimal interface shared by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanCapRows(r rowScanner) (*CapToken, error) {
	var (
		id         string
		operator   string
		scopesJSON string
		grantedAt  int64
		expiresAt  int64
		revokedAt  int64
		signature  []byte
		signedBy   string
	)
	if err := r.Scan(&id, &operator, &scopesJSON, &grantedAt, &expiresAt, &revokedAt, &signature, &signedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("manifest: %w", ErrCapNotFound)
		}
		return nil, fmt.Errorf("manifest: scan cap: %w", err)
	}
	var scopes []string
	if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
		return nil, fmt.Errorf("manifest: unmarshal scopes: %w", err)
	}
	c := &CapToken{
		ID:        id,
		Operator:  operator,
		Scopes:    scopes,
		GrantedAt: time.Unix(grantedAt, 0).UTC(),
		ExpiresAt: time.Unix(expiresAt, 0).UTC(),
		Signature: signature,
		SignedBy:  signedBy,
	}
	if revokedAt > 0 {
		c.RevokedAt = time.Unix(revokedAt, 0).UTC()
	}
	return c, nil
}

// isDuplicateKey returns true when err looks like a SQLite UNIQUE
// constraint violation. Uses string match because modernc.org/sqlite
// (the driver we depend on) returns the error message directly without
// a typed error. This is portable across the drivers we use.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") ||
		strings.Contains(s, "constraint failed: UNIQUE")
}
