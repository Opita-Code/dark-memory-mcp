package manifest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for Entry.Validate and ManifestStore. Wrapped with
// %w so callers use errors.Is to discriminate.
var (
	// ErrEmptyManifestID is returned by Entry.Validate when ID is empty
	// (the row was never persisted; ManifestStore.Insert assigns an ID).
	ErrEmptyManifestID = errors.New("manifest: entry id is empty")

	// ErrBadArtifactID is returned when ArtifactID is not strictly positive.
	ErrBadArtifactID = errors.New("manifest: artifact_id must be positive")

	// ErrEmptyKind is returned when Kind is empty or not one of the
	// canonical ArtifactType values.
	ErrEmptyKind = errors.New("manifest: kind is empty or not canonical")

	// ErrBadSHA256 is returned when the SHA256 field is not exactly 64
	// lowercase hex characters.
	ErrBadSHA256 = errors.New("manifest: sha256 must be 64 lowercase hex chars")

	// ErrEmptyManifestSignedBy is returned when SignedBy is empty.
	ErrEmptyManifestSignedBy = errors.New("manifest: signed_by is empty")

	// ErrEmptyManifestSignature is returned when Signature bytes are empty.
	ErrEmptyManifestSignature = errors.New("manifest: signature is empty")

	// ErrManifestNotFound is returned by GetByID and ListByArtifact when
	// no matching rows exist.
	ErrManifestNotFound = errors.New("manifest: entry not found")
)

// Canonical manifest entry kinds. These mirror the v4alpha/vibe
// ArtifactType values but are duplicated here to keep the manifest
// package independent of vibe (manifest is a Tier-1 primitive; vibe
// depends on manifest, not the other way around).
var CanonicalKinds = []string{
	"code", "text", "image", "video", "audio", "multi",
}

// IsValidKind reports whether kind is one of the canonical manifest kinds.
func IsValidKind(kind string) bool {
	for _, k := range CanonicalKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Entry is a single artifact attestation in the manifest registry. Every
// published artifact gets at least one Entry (the initial sign by the
// publishing operator). Updates to the artifact may produce additional
// Entries; ListByArtifact returns the chronological chain.
//
// INV-14: every Entry carries an Ed25519 Signature produced by SignedBy.
// Verify is a separate call (see sign.go).
//
// INV-15: SHA256 is the hex SHA-256 of the canonical artifact content.
// Callers compute this BEFORE constructing the Entry and never recompute
// inside the manifest package — the manifest stores hashes, not blobs.
type Entry struct {
	// ID is the manifest row id. Empty until Insert assigns one.
	ID int64

	// ArtifactID is the foreign reference to a vibe.Artifact.ID
	// (or any artifact identifier in another system). Strictly positive.
	ArtifactID int64

	// Kind is the canonical artifact kind (code/text/image/etc).
	Kind string

	// SHA256 is the hex SHA-256 of the canonical artifact content.
	// 64 lowercase hex characters; computed by the caller.
	SHA256 string

	// SignedBy is the operator id whose private key produced Signature.
	SignedBy string

	// SignedAt is the moment the signature was produced.
	SignedAt time.Time

	// Signature is the Ed25519 signature over CanonicalBytes.
	Signature []byte
}

// Validate enforces the structural invariants of an Entry. ID is allowed
// to be 0 (zero value indicates "not yet persisted"); Insert assigns one.
// The other fields are validated regardless of persistence state.
func (e *Entry) Validate() error {
	if e == nil {
		return fmt.Errorf("manifest: %w", ErrBadArtifactID)
	}
	if e.ArtifactID <= 0 {
		return fmt.Errorf("manifest: %w: got %d", ErrBadArtifactID, e.ArtifactID)
	}
	if !IsValidKind(e.Kind) {
		return fmt.Errorf("manifest: %w: got %q", ErrEmptyKind, e.Kind)
	}
	if !isValidSHA256Hex(e.SHA256) {
		return fmt.Errorf("manifest: %w", ErrBadSHA256)
	}
	if e.SignedBy == "" {
		return fmt.Errorf("manifest: %w", ErrEmptyManifestSignedBy)
	}
	if len(e.Signature) == 0 {
		return fmt.Errorf("manifest: %w", ErrEmptyManifestSignature)
	}
	return nil
}

// isValidSHA256Hex returns true if s is exactly 64 lowercase hex chars.
func isValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// HashBytes is a convenience helper: returns the hex SHA-256 of b.
// Use this when constructing an Entry from raw artifact bytes.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ManifestStore persists Entry rows in a SQLite table. Same concurrency
// guarantees as CapStore (safe when *sql.DB is shared).
//
// Schema:
//
//	CREATE TABLE manifest (
//	    id          INTEGER PRIMARY KEY AUTOINCREMENT,
//	    artifact_id INTEGER NOT NULL,
//	    kind        TEXT NOT NULL,
//	    sha256      TEXT NOT NULL,
//	    signed_by   TEXT NOT NULL,
//	    signed_at   INTEGER NOT NULL,
//	    signature   BLOB NOT NULL,
//	    CHECK (artifact_id > 0),
//	    CHECK (length(kind) > 0),
//	    CHECK (length(sha256) = 64),
//	    CHECK (length(signed_by) > 0)
//	);
type ManifestStore struct {
	db *sql.DB
}

// NewManifestStore returns a ManifestStore bound to db. The caller must
// have invoked CreateManifestSchema on db.
func NewManifestStore(db *sql.DB) *ManifestStore {
	return &ManifestStore{db: db}
}

// CreateManifestSchema installs the manifest table and its supporting
// index. Idempotent; safe on every startup.
func CreateManifestSchema(ctx context.Context, db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS manifest (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    artifact_id INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    signed_by   TEXT NOT NULL,
    signed_at   INTEGER NOT NULL,
    signature   BLOB NOT NULL,
    CHECK (artifact_id > 0),
    CHECK (length(kind) > 0),
    CHECK (length(sha256) = 64),
    CHECK (length(signed_by) > 0)
);

CREATE INDEX IF NOT EXISTS idx_manifest_artifact
    ON manifest(artifact_id, signed_at);
`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("manifest: create manifest schema: %w", err)
	}
	return nil
}

// Insert persists e and returns the assigned ID. e.ID is ignored (set
// to 0 before INSERT so AUTOINCREMENT governs). e.Validate must pass.
func (s *ManifestStore) Insert(ctx context.Context, e *Entry) (int64, error) {
	if e == nil {
		return 0, fmt.Errorf("manifest: %w", ErrBadArtifactID)
	}
	if err := e.Validate(); err != nil {
		return 0, err
	}
	const q = `
INSERT INTO manifest (artifact_id, kind, sha256, signed_by, signed_at, signature)
VALUES (?, ?, ?, ?, ?, ?)
`
	res, err := s.db.ExecContext(ctx, q,
		e.ArtifactID, e.Kind, e.SHA256, e.SignedBy,
		e.SignedAt.Unix(), e.Signature,
	)
	if err != nil {
		return 0, fmt.Errorf("manifest: insert entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("manifest: last insert id: %w", err)
	}
	e.ID = id
	return id, nil
}

// GetByID returns the entry with the given id, or ErrManifestNotFound.
func (s *ManifestStore) GetByID(ctx context.Context, id int64) (*Entry, error) {
	if id <= 0 {
		return nil, fmt.Errorf("manifest: %w", ErrManifestNotFound)
	}
	const q = `
SELECT id, artifact_id, kind, sha256, signed_by, signed_at, signature
FROM manifest
WHERE id = ?
`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanEntry(row.Scan)
}

// ErrManifestNotFound is declared with the other sentinel errors at
// the top of this file.

// ListByArtifact returns every Entry for the given artifact_id, ordered
// by signed_at ASC (oldest first). Returns an empty slice (not nil) when
// no rows match.
func (s *ManifestStore) ListByArtifact(ctx context.Context, artifactID int64) ([]*Entry, error) {
	if artifactID <= 0 {
		return []*Entry{}, nil
	}
	const q = `
SELECT id, artifact_id, kind, sha256, signed_by, signed_at, signature
FROM manifest
WHERE artifact_id = ?
ORDER BY signed_at ASC, id ASC
`
	rows, err := s.db.QueryContext(ctx, q, artifactID)
	if err != nil {
		return nil, fmt.Errorf("manifest: list by artifact: %w", err)
	}
	defer rows.Close()
	out := []*Entry{}
	for rows.Next() {
		e, err := scanEntry(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("manifest: list iterate: %w", err)
	}
	return out, nil
}

// scanFn abstracts *sql.Row.Scan and *sql.Rows.Scan so scanEntry can
// work for both GetByID and ListByArtifact.
type scanFn func(dest ...any) error

func scanEntry(scan scanFn) (*Entry, error) {
	var (
		id        int64
		artID     int64
		kind      string
		sha       string
		signedBy  string
		signedAt  int64
		signature []byte
	)
	if err := scan(&id, &artID, &kind, &sha, &signedBy, &signedAt, &signature); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("manifest: %w", ErrManifestNotFound)
		}
		return nil, fmt.Errorf("manifest: scan entry: %w", err)
	}
	return &Entry{
		ID:         id,
		ArtifactID: artID,
		Kind:       kind,
		SHA256:     sha,
		SignedBy:   signedBy,
		SignedAt:   time.Unix(signedAt, 0).UTC(),
		Signature:  signature,
	}, nil
}
