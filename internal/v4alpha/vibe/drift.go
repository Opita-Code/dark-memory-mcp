package vibe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Canonical Verdict values. The CHECK constraint in vibe_drifts
// mirrors this enum.
const (
	VerdictAligned       = "aligned"
	VerdictDriftDetected = "drift_detected"
	VerdictNeedsHuman    = "needs_human"
)

// Canonical Decision values for operator-driven resolve.
const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
)

// IsValidVerdict reports whether v is one of the canonical verdicts.
func IsValidVerdict(v string) bool {
	switch v {
	case VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman:
		return true
	}
	return false
}

// IsValidDecision reports whether d is accept or reject.
func IsValidDecision(d string) bool {
	switch d {
	case DecisionAccept, DecisionReject:
		return true
	}
	return false
}

// DriftReport is one verdict against one artifact. The full lifecycle
// is: insert (verdict + reasoning) → optionally Resolve (operator
// gates accept/reject).
type DriftReport struct {
	ID          int64
	ArtifactID  int64
	EvalType    string  // "drift_judge" by default
	Verdict     string  // aligned/drift_detected/needs_human
	Confidence  float64 // [0.0, 1.0]
	Reasoning   string
	EvaluatedAt time.Time
	Resolved    bool
	Resolution  string // accept/reject/empty until resolved
	OperatorID  string // empty until resolved
	Note        string // empty until resolved
}

// CreateDriftSchema creates the vibe_drifts table. Idempotent.
//
// Schema notes:
//   - confidence is REAL with a CHECK bounded to [0.0, 1.0].
//   - resolved is INTEGER (0/1) — SQLite has no native bool.
//   - resolution is nullable; CHECK allows NULL or accept/reject.
func CreateDriftSchema(db *sql.DB) error {
	_, err := sdbExec(db, `
		CREATE TABLE IF NOT EXISTS vibe_drifts (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			artifact_id  INTEGER NOT NULL,
			eval_type    TEXT    NOT NULL DEFAULT 'drift_judge',
			verdict      TEXT    NOT NULL CHECK (verdict IN
			                  ('aligned','drift_detected','needs_human')),
			confidence   REAL    NOT NULL CHECK (confidence >= 0.0 AND confidence <= 1.0),
			reasoning    TEXT    NOT NULL,
			evaluated_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
			resolved     INTEGER NOT NULL DEFAULT 0 CHECK (resolved IN (0, 1)),
			resolution   TEXT    CHECK (resolution IS NULL
			                             OR resolution IN ('accept','reject')),
			operator_id  TEXT,
			note         TEXT
		)
	`)
	if err != nil {
		return fmt.Errorf("vibe CreateDriftSchema: %w", err)
	}
	return nil
}

// sdbExec is a tiny indirection to keep the schema SQL close to the
// table definition without leaking err wrapping everywhere.
func sdbExec(db *sql.DB, q string) (sql.Result, error) {
	return db.Exec(q)
}

// Drift-specific errors.
var (
	ErrInvalidVerdict    = errors.New("vibe: invalid verdict")
	ErrInvalidConfidence = errors.New("vibe: confidence must be in [0.0, 1.0]")
	ErrArtifactNotFound  = errors.New("vibe: artifact_id does not exist")
	ErrInvalidDecision   = errors.New("vibe: invalid decision")
	ErrEmptyOperator     = errors.New("vibe: operator id required for resolve")
	ErrDriftNotFound     = errors.New("vibe: drift not found")
)

// Validate enforces drift-level invariants:
//
// Invariant 1: ArtifactID must be > 0.
// Invariant 2: Verdict must be canonical.
// Invariant 3: Confidence must be in [0.0, 1.0] inclusive.
func (d *DriftReport) Validate() error {
	if d.ArtifactID <= 0 {
		return errors.New("vibe: artifact_id is required")
	}
	if !IsValidVerdict(d.Verdict) {
		return fmt.Errorf("%w: got %q", ErrInvalidVerdict, d.Verdict)
	}
	if d.Confidence < 0.0 || d.Confidence > 1.0 {
		return fmt.Errorf("%w: got %f", ErrInvalidConfidence, d.Confidence)
	}
	return nil
}

// DriftStore persists drift reports and exposes Status queries.
type DriftStore struct {
	db *sql.DB
}

// NewDriftStore wraps a *sql.DB. Caller runs CreateDriftSchema at startup.
func NewDriftStore(db *sql.DB) *DriftStore {
	return &DriftStore{db: db}
}

// artifactExists is the app-level INV-3 cross-table check (drift
// must reference an existing artifact). No FK in this minimal schema;
// enforced at Insert time.
func (s *DriftStore) artifactExists(ctx context.Context, id int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM vibe_artifacts WHERE id = ? LIMIT 1", id,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("drift artifactExists: %w", err)
	}
	return true, nil
}

// Insert persists a new drift report and assigns the ID back.
func (s *DriftStore) Insert(ctx context.Context, d *DriftReport) (int64, error) {
	if err := d.Validate(); err != nil {
		return 0, fmt.Errorf("drift Insert: %w", err)
	}
	exists, err := s.artifactExists(ctx, d.ArtifactID)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, fmt.Errorf("%w: id=%d", ErrArtifactNotFound, d.ArtifactID)
	}

	if d.EvalType == "" {
		d.EvalType = "drift_judge"
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO vibe_drifts
		 (artifact_id, eval_type, verdict, confidence, reasoning)
		 VALUES (?, ?, ?, ?, ?)`,
		d.ArtifactID, d.EvalType, d.Verdict, d.Confidence, d.Reasoning,
	)
	if err != nil {
		return 0, fmt.Errorf("drift Insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("drift Insert LastInsertId: %w", err)
	}
	d.ID = id
	return id, nil
}

// Get returns a drift report by id, or ErrNotFound.
func (s *DriftStore) Get(ctx context.Context, id int64) (*DriftReport, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, artifact_id, eval_type, verdict, confidence,
		        reasoning, evaluated_at, resolved, resolution,
		        operator_id, note
		 FROM vibe_drifts WHERE id = ?`, id)
	return scanDrift(row)
}

// Resolve applies an operator decision to a drift. The UPDATE is
// unconditional on the WHERE id = ? — caller's responsibility is
// to first call Get and verify Resolved=false (idempotent semantics
// are enforced at the Pipeline layer).
//
// Returns ErrDriftNotFound if the drift does not exist.
func (s *DriftStore) Resolve(ctx context.Context, driftID int64, decision, note, operator string) error {
	// First check existence so we can return ErrDriftNotFound.
	var exists int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM vibe_drifts WHERE id = ? LIMIT 1", driftID,
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return ErrDriftNotFound
	}
	if err != nil {
		return fmt.Errorf("drift Resolve lookup: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE vibe_drifts
		 SET resolved = 1, resolution = ?, operator_id = ?, note = ?
		 WHERE id = ?`,
		decision, operator, note, driftID,
	)
	if err != nil {
		return fmt.Errorf("drift Resolve update: %w", err)
	}
	return nil
}

// Status returns the latest drift report for an artifact, ordered
// by evaluated_at DESC then id DESC. Returns ErrNotFound if no drift
// has been recorded for that artifact.
func (s *DriftStore) Status(ctx context.Context, artifactID int64) (*DriftReport, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, artifact_id, eval_type, verdict, confidence,
		        reasoning, evaluated_at, resolved, resolution,
		        operator_id, note
		 FROM vibe_drifts WHERE artifact_id = ?
		 ORDER BY evaluated_at DESC, id DESC LIMIT 1`, artifactID)
	return scanDrift(row)
}

func scanDrift(row *sql.Row) (*DriftReport, error) {
	var d DriftReport
	var resolved int
	var resolution, operatorID, note sql.NullString
	var evaluatedAt string
	err := row.Scan(
		&d.ID, &d.ArtifactID, &d.EvalType, &d.Verdict, &d.Confidence,
		&d.Reasoning, &evaluatedAt, &resolved, &resolution,
		&operatorID, &note,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("drift scan: %w", err)
	}
	d.Resolved = resolved == 1
	if resolution.Valid {
		d.Resolution = resolution.String
	}
	if operatorID.Valid {
		d.OperatorID = operatorID.String
	}
	if note.Valid {
		d.Note = note.String
	}
	d.EvaluatedAt, _ = time.Parse(time.RFC3339Nano, evaluatedAt)
	return &d, nil
}
