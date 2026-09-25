package vibe

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SpecStore persists Specs. The minimal spec schema is enough for
// v4-alpha; the full schema (with operator, constitution_id, etc.)
// lands in a later slice.
type SpecStore struct {
	db *sql.DB
}

// NewSpecStore wraps a *sql.DB.
func NewSpecStore(db *sql.DB) *SpecStore {
	return &SpecStore{db: db}
}

// CreateSpecSchema creates the vibe_specs table. Idempotent.
//
// Schema note: tasks_json holds the canonical JSON encoding of the
// Spec.Tasks slice (round-trip stable).
func CreateSpecSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vibe_specs (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			vibe_case  TEXT    NOT NULL,
			intent     TEXT    NOT NULL,
			tasks_json TEXT    NOT NULL DEFAULT '[]',
			created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("vibe CreateSpecSchema: %w", err)
	}
	return nil
}

// Insert persists a Spec and returns the assigned ID. Assigns the
// ID back to the caller's struct.
func (s *SpecStore) Insert(ctx context.Context, spec *Spec) (int64, error) {
	if err := spec.Validate(); err != nil {
		return 0, fmt.Errorf("specStore Insert: %w", err)
	}
	tasksJSON, err := json.Marshal(spec.Tasks)
	if err != nil {
		return 0, fmt.Errorf("specStore Insert tasks: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_specs (vibe_case, intent, tasks_json)
		 VALUES (?, ?, ?)`,
		spec.VibeCase, spec.Intent, string(tasksJSON),
	)
	if err != nil {
		return 0, fmt.Errorf("specStore Insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("specStore Insert LastInsertId: %w", err)
	}
	spec.ID = id
	return id, nil
}

// Get returns a Spec by id, or ErrNotFound (the package-level
// sentinel declared in errors.go, shared by ArtifactStore.Get and
// DriftStore.Get).
func (s *SpecStore) Get(ctx context.Context, id int64) (*Spec, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, vibe_case, intent, tasks_json, created_at
		 FROM vibe_specs WHERE id = ?`, id)
	var (
		spec      Spec
		tasksJSON string
		createdAt string
	)
	err := row.Scan(&spec.ID, &spec.VibeCase, &spec.Intent, &tasksJSON, &createdAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("specStore Get: %w", err)
	}
	if err := json.Unmarshal([]byte(tasksJSON), &spec.Tasks); err != nil {
		return nil, fmt.Errorf("specStore Get tasks: %w", err)
	}
	spec.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &spec, nil
}

// Sentinel errors for SpecStore.
var (
	ErrEmptySpecID = errors.New("vibe: spec id is required")
)
