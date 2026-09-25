package vibe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Canonical ArtifactType values. The CHECK constraint in
// vibe_artifacts mirrors this enum.
const (
	ArtifactTypeCode  = "code"
	ArtifactTypeText  = "text"
	ArtifactTypeImage = "image"
	ArtifactTypeVideo = "video"
	ArtifactTypeAudio = "audio"
	ArtifactTypeMulti = "multi"
)

// Canonical ArtifactRefKind values. "" means "no ref" — the artifact
// is purely textual.
const (
	RefKindNone     = ""
	RefKindFile     = "file"
	RefKindGitSHA   = "git_sha"
	RefKindURL      = "url"
	RefKindSpecID   = "spec_id"
	RefKindArtifact = "artifact_id"
)

// IsValidArtifactType reports whether t is one of the canonical
// ArtifactType constants.
func IsValidArtifactType(t string) bool {
	switch t {
	case ArtifactTypeCode, ArtifactTypeText, ArtifactTypeImage,
		ArtifactTypeVideo, ArtifactTypeAudio, ArtifactTypeMulti:
		return true
	}
	return false
}

// ArtifactRef is the polymorphic reference for an artifact. Exactly
// one shape is populated per ref (discriminated by Kind).
//
//   Kind="file"       : Path is the filesystem path
//   Kind="git_sha"    : Path is the repo-relative path, GitSHA is pinned
//   Kind="url"        : URL is the remote location
//   Kind="spec_id"    : SpecID is the canonical ref
//   Kind="artifact_id": ArtifactID is the canonical ref
//
// MaxBytes bounds the resolver (default 256 KiB in production).
type ArtifactRef struct {
	Kind       string
	Path       string
	GitSHA     string
	GitRepo    string
	URL        string
	SpecID     int64
	ArtifactID int64
	MaxBytes   int
}

// Artifact is one publishable unit under a spec.
//
// Invariants (enforced by Artifact.Validate):
//   - Type is canonical (code/text/image/video/audio/multi)
//   - SpecID > 0 (INV-3 cross-spec consistency; existence checked
//     by ArtifactStore.Insert)
//   - At least one of URL, Text, or Ref is set (no empty artifacts)
type Artifact struct {
	ID        int64
	SpecID    int64
	Type      string
	URL       string
	Text      string
	Ref       *ArtifactRef
	CreatedAt time.Time
}

// CreateArtifactSchema creates the vibe_artifacts table. Idempotent.
//
// Schema note: no FOREIGN KEY on spec_id. The spec table
// (vibe_specs) is created in cycle 4 when Pipeline.Publish lands.
// Spec existence is enforced by ArtifactStore.Insert via SELECT.
func CreateArtifactSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vibe_artifacts (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			spec_id         INTEGER NOT NULL,
			artifact_type   TEXT    NOT NULL CHECK (artifact_type IN
			                       ('code','text','image','video','audio','multi')),
			artifact_url    TEXT    NOT NULL DEFAULT '',
			text            TEXT    NOT NULL DEFAULT '',
			ref_kind        TEXT,
			ref_path        TEXT,
			ref_git_sha     TEXT,
			ref_git_repo    TEXT,
			ref_url         TEXT,
			ref_spec_id     INTEGER,
			ref_artifact_id INTEGER,
			ref_max_bytes   INTEGER,
			created_at      TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("vibe CreateArtifactSchema: %w", err)
	}
	return nil
}

// Artifact-specific errors. ErrSpecNotFound is the app-level
// invariant for INV-3 cross-spec consistency.
var (
	ErrInvalidArtifactType = errors.New("vibe: invalid artifact_type")
	ErrMissingSpecID       = errors.New("vibe: artifact spec_id is required")
	ErrMissingArtifactRef  = errors.New("vibe: artifact requires URL, text, or ref")
	ErrRefKindMismatch     = errors.New("vibe: artifact_ref fields do not match Kind")
	ErrSpecNotFound        = errors.New("vibe: spec_id does not exist")
)

// Validate enforces the v4-alpha artifact invariants.
//
// Invariant 1: Type must be canonical (code/text/image/video/audio/multi).
// Invariant 2: SpecID must be > 0.
// Invariant 3: At least one of URL, Text, or Ref must be set.
// Invariant 4: If Ref is set, the matching required field is non-zero.
func (a *Artifact) Validate() error {
	if !IsValidArtifactType(a.Type) {
		return fmt.Errorf("%w: got %q", ErrInvalidArtifactType, a.Type)
	}
	if a.SpecID <= 0 {
		return ErrMissingSpecID
	}
	if a.URL == "" && a.Text == "" && a.Ref == nil {
		return ErrMissingArtifactRef
	}
	if a.Ref != nil {
		switch a.Ref.Kind {
		case RefKindFile, RefKindGitSHA:
			if a.Ref.Path == "" {
				return fmt.Errorf("%w: %s requires Path",
					ErrRefKindMismatch, a.Ref.Kind)
			}
		case RefKindURL:
			if a.Ref.URL == "" {
				return fmt.Errorf("%w: url requires URL", ErrRefKindMismatch)
			}
		case RefKindSpecID:
			if a.Ref.SpecID <= 0 {
				return fmt.Errorf("%w: spec_id requires SpecID > 0",
					ErrRefKindMismatch)
			}
		case RefKindArtifact:
			if a.Ref.ArtifactID <= 0 {
				return fmt.Errorf("%w: artifact_id requires ArtifactID > 0",
					ErrRefKindMismatch)
			}
		default:
			return fmt.Errorf("%w: unknown Kind %q",
				ErrRefKindMismatch, a.Ref.Kind)
		}
	}
	return nil
}

// ArtifactStore persists artifacts. Backed by SQLite in v4-alpha.
type ArtifactStore struct {
	db *sql.DB
}

// NewArtifactStore wraps a *sql.DB. The caller is responsible for
// running CreateArtifactSchema at startup.
func NewArtifactStore(db *sql.DB) *ArtifactStore {
	return &ArtifactStore{db: db}
}

// specExists returns true if vibe_specs.id == id exists. Used by
// Insert to enforce INV-3 cross-spec consistency without a SQL FK
// (the spec table is created in cycle 4).
func (s *ArtifactStore) specExists(ctx context.Context, specID int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM vibe_specs WHERE id = ? LIMIT 1", specID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("artifact specExists: %w", err)
	}
	return true, nil
}

// Insert persists a new artifact and returns the assigned ID. Also
// assigns the ID back to the caller's struct.
//
// INV-3 enforcement: looks up the spec row before insert; returns
// ErrSpecNotFound if missing.
func (s *ArtifactStore) Insert(ctx context.Context, a *Artifact) (int64, error) {
	if err := a.Validate(); err != nil {
		return 0, fmt.Errorf("artifact Insert: %w", err)
	}
	exists, err := s.specExists(ctx, a.SpecID)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, fmt.Errorf("%w: id=%d", ErrSpecNotFound, a.SpecID)
	}

	var (
		refKind   sql.NullString
		refPath   sql.NullString
		refSHA    sql.NullString
		refRepo   sql.NullString
		refURL    sql.NullString
		refSpecID sql.NullInt64
		refArtID  sql.NullInt64
		refMax    sql.NullInt64
	)
	if a.Ref != nil {
		refKind = sql.NullString{String: a.Ref.Kind, Valid: true}
		if a.Ref.Path != "" {
			refPath = sql.NullString{String: a.Ref.Path, Valid: true}
		}
		if a.Ref.GitSHA != "" {
			refSHA = sql.NullString{String: a.Ref.GitSHA, Valid: true}
		}
		if a.Ref.GitRepo != "" {
			refRepo = sql.NullString{String: a.Ref.GitRepo, Valid: true}
		}
		if a.Ref.URL != "" {
			refURL = sql.NullString{String: a.Ref.URL, Valid: true}
		}
		if a.Ref.SpecID > 0 {
			refSpecID = sql.NullInt64{Int64: a.Ref.SpecID, Valid: true}
		}
		if a.Ref.ArtifactID > 0 {
			refArtID = sql.NullInt64{Int64: a.Ref.ArtifactID, Valid: true}
		}
		if a.Ref.MaxBytes > 0 {
			refMax = sql.NullInt64{Int64: int64(a.Ref.MaxBytes), Valid: true}
		}
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_artifacts
		 (spec_id, artifact_type, artifact_url, text,
		  ref_kind, ref_path, ref_git_sha, ref_git_repo,
		  ref_url, ref_spec_id, ref_artifact_id, ref_max_bytes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.SpecID, a.Type, a.URL, a.Text,
		refKind, refPath, refSHA, refRepo,
		refURL, refSpecID, refArtID, refMax,
	)
	if err != nil {
		return 0, fmt.Errorf("artifact Insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("artifact Insert LastInsertId: %w", err)
	}
	a.ID = id
	return id, nil
}

// Get returns the artifact by id, or ErrNotFound if not present.
func (s *ArtifactStore) Get(ctx context.Context, id int64) (*Artifact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, spec_id, artifact_type, artifact_url, text,
		        ref_kind, ref_path, ref_git_sha, ref_git_repo,
		        ref_url, ref_spec_id, ref_artifact_id, ref_max_bytes,
		        created_at
		 FROM vibe_artifacts WHERE id = ?`, id)
	var a Artifact
	var (
		refKind, refPath, refSHA, refRepo, refURL sql.NullString
		refSpecID, refArtID, refMax               sql.NullInt64
		createdAt                                string
	)
	err := row.Scan(
		&a.ID, &a.SpecID, &a.Type, &a.URL, &a.Text,
		&refKind, &refPath, &refSHA, &refRepo,
		&refURL, &refSpecID, &refArtID, &refMax, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("artifact Get: %w", err)
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if refKind.Valid {
		a.Ref = &ArtifactRef{
			Kind:     refKind.String,
			Path:     refPath.String,
			GitSHA:   refSHA.String,
			GitRepo:  refRepo.String,
			URL:      refURL.String,
			MaxBytes: int(refMax.Int64),
		}
		if refSpecID.Valid {
			a.Ref.SpecID = refSpecID.Int64
		}
		if refArtID.Valid {
			a.Ref.ArtifactID = refArtID.Int64
		}
	}
	return &a, nil
}
