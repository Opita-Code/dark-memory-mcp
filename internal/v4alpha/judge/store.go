// Package judge — Store + sdd_evaluations schema (ADR-007 C3).
//
// C3 introduces the persistence layer for the v4-alpha judge surface.
// Every call to dark_memory_judge and dark_memory_consensus emits
// one sdd_evaluations row per Evaluate call (per-sample for consensus,
// plus one modal row at eval_type="consensus"). The atomicity contract
// matches agent_memory: one sdd_evaluations row + one audit_log row
// inside a single SERIALIZABLE transaction.
//
// # Atomicity contract
//
//   - ONE constructor: NewStore
//   - ONE schema function: CreateSchema
//   - THREE write methods:
//     SaveEvaluation          — single sdd_evaluations row + audit_log
//     SaveConsensusSamples    — N per-sample rows + 1 modal row + N+1 audit_log rows
//     (SaveEvaluation is the building block for SaveConsensusSamples)
//   - THREE read methods:
//     GetEvaluation           — by id
//     ListEvaluations         — by eval_type + target_type + target_id filters
//     LatestEvaluation        — most recent matching row
//   - FOUR invariants enforced:
//     1. eval_type is non-empty (drift_judge | brand_match | ... | consensus)
//     2. target_type + target_id are non-empty (audit-traceable)
//     3. Audit.Actor is non-empty (INV-1)
//     4. The verdict JSON round-trips: VerdictFromEvaluation(
//     EvaluationFromVerdict(v)) == v
//
// # Schema note (legacy compat)
//
// The legacy v3 sdd_evaluations table lives in internal/migrate/sqlite/
// ddl.go and is used by the orchestration layer. The v4-alpha table
// introduced here is INDEPENDENT (different DBs by default — v4alpha
// boots with its own dark.db). Cross-version federation (Dark-Federation)
// is post-alpha; for now the two tables coexist as separate consumers
// of the same conceptual artifact.
package judge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// EvalConsensus is the sentinel eval_type used for the modal verdict
// row of a consensus call. Per-sample rows carry their normal eval_type
// (drift_judge, brand_match, etc.); the modal row uses EvalConsensus
// so queries can find "the aggregated result" without scanning N rows.
const EvalConsensus = "consensus"

// Audit is the INV-1 audit metadata threaded through every
// state-mutating call. Mirrors agent_memory.Audit (kept as a
// separate struct to avoid the import cycle agent_memory <-> judge;
// the two packages will eventually share via internal/v4alpha/inv1).
type Audit struct {
	Actor     string
	SessionID string
	ProjectID string
}

// Evaluation is the canonical row stored in sdd_evaluations.
//
// On Write, callers populate EvalType + TargetType + TargetID + Verdict;
// the TemperatureNote (Provider, Model, Temperature, Seed, MaxTokens,
// TimeoutMs, TopP, PersonaID, RubricVersion, SchemaVersion) is
// flattened into sdd_evaluations columns for queryability. VerdictJSON
// is computed from Verdict (and the v4 extensions — Criteria, Evidence,
// EdgeCaseHits, BiasAudit) so the full Verdict can be reconstructed
// by VerdictFromEvaluation.
//
// On Read, all fields are populated from the row, including the
// reconstructed Verdict via VerdictFromEvaluation.
//
// Note: NonDeterministic is NOT a Verdict field — it lives on
// LLMResponse. C3 stores 0 by default. A future iteration can
// extend TemperatureNote with a NonDeterministic flag if needed.
//
// ADR-011 (Phase 3): 4 calibration columns (ConfidenceCalibrated,
// CalibrationCILow, CalibrationCIHigh, CalibrationMethod) are populated
// by the pipeline's populateCalibration hook. NULL for rows that
// haven't reached ShouldRecalibrate(N) yet.
type Evaluation struct {
	ID          int64     `json:"id"`
	EvalType    string    `json:"eval_type"`
	TargetType  string    `json:"target_type"`
	TargetID    string    `json:"target_id"`
	ProjectID   string    `json:"project_id,omitempty"` // Phase 4 Chunk 4.3: namespace primitive
	VerdictJSON string    `json:"verdict_json"`
	Confidence  float64   `json:"confidence"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	PersonaID   string    `json:"persona_id,omitempty"`
	RubricVer   string    `json:"rubric_version,omitempty"`
	SchemaVer   string    `json:"schema_version,omitempty"`
	Seed        int64     `json:"seed,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	TimeoutMs   int       `json:"timeout_ms,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	TopP        float64   `json:"top_p,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`

	// ADR-011 calibration columns. Populated by the pipeline when
	// ShouldRecalibrate(N) holds (>=50 historical samples for the
	// same (provider, target_type, eval_type) tuple).
	ConfidenceCalibrated float64 `json:"confidence_calibrated,omitempty"`
	CalibrationCILow     float64 `json:"calibration_ci_low,omitempty"`
	CalibrationCIHigh    float64 `json:"calibration_ci_high,omitempty"`
	CalibrationMethod    string  `json:"calibration_method,omitempty"`

	// Verdict is the reconstructed v4 Verdict. Populated by
	// VerdictFromEvaluation on Read; nil when constructing an
	// Evaluation from a fresh Verdict via EvaluationFromVerdict
	// (the Verdict field is what callers have at write time; we
	// JSON-encode it into VerdictJSON before persisting).
	Verdict *Verdict `json:"verdict,omitempty"`
}

// ListFilter constrains ListEvaluations. Zero values mean "any".
type ListFilter struct {
	EvalType   string // "" = all eval_types
	TargetType string // "" = all target_types
	TargetID   string // "" = all target_ids
	Limit      int    // 0 = default 50
}

// Store is the sdd_evaluations repository. Bound to a *sql.DB and
// an audit.Writer. The schema must be initialised via CreateSchema
// before any Save/List call.
type Store struct {
	db    *sql.DB
	audit *audit.Writer
}

// NewStore returns a Store wired to db and w. Both must be initialized
// (CreateSchema on db; NewWriter for w). INV-1 closure: every Save
// emits exactly one audit_log row inside the same transaction.
func NewStore(db *sql.DB, w *audit.Writer) *Store {
	return &Store{db: db, audit: w}
}

// CreateSchema creates the sdd_evaluations table + 4 indexes. Idempotent.
func CreateSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sdd_evaluations (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			eval_type         TEXT    NOT NULL CHECK (eval_type <> ''),
			target_type       TEXT    NOT NULL CHECK (target_type <> ''),
			target_id         TEXT    NOT NULL CHECK (target_id <> ''),
			verdict_json      TEXT    NOT NULL,
			confidence        REAL    NOT NULL DEFAULT 0,
			provider          TEXT,
			model             TEXT,
			persona_id        TEXT,
			rubric_version    TEXT,
			schema_version    TEXT,
			seed              INTEGER,
			max_tokens        INTEGER,
			timeout_ms        INTEGER,
			temperature       REAL,
			top_p             REAL,
			non_deterministic INTEGER NOT NULL DEFAULT 0,
			session_id        TEXT,
			created_at        TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
			confidence_calibrated REAL,
			calibration_ci_low    REAL,
			calibration_ci_high   REAL,
			calibration_method    TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sdd_eval_eval_type
			ON sdd_evaluations(eval_type)`,
		`CREATE INDEX IF NOT EXISTS idx_sdd_eval_target
			ON sdd_evaluations(target_type, target_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sdd_eval_created
			ON sdd_evaluations(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sdd_eval_session
			ON sdd_evaluations(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sdd_eval_provider_target
			ON sdd_evaluations(provider, target_type, eval_type)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("judge CreateSchema (%s): %w", firstLine(s), err)
		}
	}
	return nil
}

// ApplyCalibrationColumns adds the 4 ADR-011 calibration columns
// to a pre-Phase-3 sdd_evaluations table (alpha.15 and earlier).
// Idempotent: uses pragma_table_info to detect presence and skip
// if the column already exists (same pattern as
// audit.ApplyChainColumns from Phase 2).
//
// New DBs should use CreateSchema (which includes the columns
// directly). ApplyCalibrationColumns is the migration path for
// existing DBs that already contain sdd_evaluations rows.
func ApplyCalibrationColumns(ctx context.Context, db *sql.DB) error {
	for _, col := range []string{
		"confidence_calibrated",
		"calibration_ci_low",
		"calibration_ci_high",
		"calibration_method",
	} {
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM pragma_table_info('sdd_evaluations') WHERE name = ?",
			col,
		).Scan(&n); err != nil {
			return fmt.Errorf("judge ApplyCalibrationColumns (pragma %s): %w", col, err)
		}
		if n > 0 {
			continue // already present
		}
		var ddl string
		switch col {
		case "calibration_method":
			ddl = "ALTER TABLE sdd_evaluations ADD COLUMN calibration_method TEXT"
		default:
			ddl = "ALTER TABLE sdd_evaluations ADD COLUMN " + col + " REAL"
		}
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("judge ApplyCalibrationColumns (add %s): %w", col, err)
		}
	}
	// Add the provider_target index if missing.
	var idxN int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_sdd_eval_provider_target'",
	).Scan(&idxN); err != nil {
		return fmt.Errorf("judge ApplyCalibrationColumns (pragma index): %w", err)
	}
	if idxN == 0 {
		if _, err := db.ExecContext(ctx,
			`CREATE INDEX IF NOT EXISTS idx_sdd_eval_provider_target
				ON sdd_evaluations(provider, target_type, eval_type)`,
		); err != nil {
			return fmt.Errorf("judge ApplyCalibrationColumns (create index): %w", err)
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

// ErrEmptyEvalType is returned when EvalType is empty.
var ErrEmptyEvalType = errors.New("judge store: eval_type must be non-empty")

// ErrEmptyTarget is returned when TargetType or TargetID is empty.
var ErrEmptyTarget = errors.New("judge store: target_type + target_id must be non-empty")

// ErrEmptyAuditActor is returned when Audit.Actor is empty.
var ErrEmptyAuditActor = errors.New("judge store: audit actor must be non-empty (INV-1)")

// ErrNotFound is returned by Get/List when no row matches.
var ErrNotFound = errors.New("judge store: not found")

// errEmptySamples is the sentinel returned by SaveConsensusSamples
// when samples is an empty slice.
var errEmptySamples = errors.New("judge store: no samples provided")

// EvaluationFromVerdict builds an Evaluation from a v4 Verdict,
// flattening TemperatureNote into sdd_evaluations columns. Returns
// ErrEmptyTarget if v or v.TemperatureNote are nil.
func EvaluationFromVerdict(evalType, targetType, targetID string, v *Verdict) (*Evaluation, error) {
	if evalType == "" {
		return nil, ErrEmptyEvalType
	}
	if targetType == "" || targetID == "" {
		return nil, ErrEmptyTarget
	}
	if v == nil {
		return nil, fmt.Errorf("judge store: verdict is nil")
	}
	verdictJSON, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("judge store: marshal verdict: %w", err)
	}
	tn := v.TemperatureNote
	return &Evaluation{
		EvalType:    evalType,
		TargetType:  targetType,
		TargetID:    targetID,
		VerdictJSON: string(verdictJSON),
		Confidence:  v.Confidence,
		Provider:    tn.Provider,
		Model:       tn.Model,
		PersonaID:   tn.PersonaID,
		RubricVer:   tn.RubricVersion,
		SchemaVer:   tn.SchemaVersion,
		Seed:        tn.Seed,
		MaxTokens:   tn.MaxTokens,
		TimeoutMs:   tn.TimeoutMs,
		Temperature: tn.Temperature,
		TopP:        tn.TopP,
		// SessionID is populated by the caller (transport layer)
		// after EvaluationFromVerdict returns.
	}, nil
}

// VerdictFromEvaluation reconstructs the v4 Verdict from an Evaluation.
// Returns an error if VerdictJSON is malformed.
func VerdictFromEvaluation(e *Evaluation) (*Verdict, error) {
	if e == nil {
		return nil, fmt.Errorf("judge store: evaluation is nil")
	}
	if e.Verdict == nil {
		var v Verdict
		if err := json.Unmarshal([]byte(e.VerdictJSON), &v); err != nil {
			return nil, fmt.Errorf("judge store: unmarshal verdict: %w", err)
		}
		e.Verdict = &v
	}
	return e.Verdict, nil
}

// SaveEvaluation inserts one sdd_evaluations row + one audit_log row
// inside a single SERIALIZABLE transaction (INV-1 closure). Returns
// the new evaluation id.
//
// audit.Actor must be non-empty.
func (s *Store) SaveEvaluation(ctx context.Context, auditMeta *Audit, e *Evaluation) (int64, error) {
	if auditMeta == nil || auditMeta.Actor == "" {
		return 0, ErrEmptyAuditActor
	}
	if e == nil {
		return 0, fmt.Errorf("judge store: evaluation is nil")
	}
	if e.EvalType == "" {
		return 0, ErrEmptyEvalType
	}
	if e.TargetType == "" || e.TargetID == "" {
		return 0, ErrEmptyTarget
	}

	var id int64
	err := store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		// project_id resolves to 'default' literal when empty
		// (auditMeta.ProjectID == ""). The sdd_evaluations table
		// declares project_id NOT NULL with DEFAULT 'default',
		// but the DEFAULT only fires when the column is OMITTED
		// from the INSERT — passing NULL explicitly triggers the
		// NOT NULL constraint. Always pass a non-null value.
		projectID := auditMeta.ProjectID
		if projectID == "" {
			projectID = "default"
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO sdd_evaluations (
				eval_type, target_type, target_id, project_id, verdict_json, confidence,
				provider, model, persona_id, rubric_version, schema_version,
				seed, max_tokens, timeout_ms, temperature, top_p,
				non_deterministic, session_id,
				confidence_calibrated, calibration_ci_low,
				calibration_ci_high, calibration_method
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)
		`,
			e.EvalType, e.TargetType, e.TargetID, projectID,
			e.VerdictJSON, e.Confidence,
			nullIfEmpty(e.Provider), nullIfEmpty(e.Model),
			nullIfEmpty(e.PersonaID), nullIfEmpty(e.RubricVer),
			nullIfEmpty(e.SchemaVer),
			nullableInt64(e.Seed), nullableInt(e.MaxTokens), nullableInt(e.TimeoutMs),
			nullableFloat(e.Temperature), nullableFloat(e.TopP),
			nullIfEmpty(auditMeta.SessionID),
			nullableFloat(e.ConfidenceCalibrated),
			nullableFloat(e.CalibrationCILow),
			nullableFloat(e.CalibrationCIHigh),
			nullIfEmpty(e.CalibrationMethod),
		)
		if err != nil {
			return fmt.Errorf("judge store SaveEvaluation insert: %w", err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("judge store SaveEvaluation last insert id: %w", err)
		}
		id = n

		// INV-1 audit emission inside the same tx. When
		// auditMeta.ProjectID is non-empty, stamp the audit row
		// with the same project_id (Phase 4 Chunk 4.3 hard
		// isolation). Empty falls back to legacy WriteExec.
		payload := []byte(fmt.Sprintf(
			`{"event":"judge.save","id":%d,"eval_type":%q,"target_type":%q,"target_id":%q}`,
			id, e.EvalType, e.TargetType, e.TargetID,
		))
		if err := writeAuditExecWithProject(ctx, s.audit, tx, auditMeta, payload); err != nil {
			return fmt.Errorf("judge store SaveEvaluation audit: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SaveConsensusSamples inserts N per-sample sdd_evaluations rows
// (evalType carries the original eval_type) plus ONE modal row
// (eval_type="consensus"). All inserts + audit_log rows happen in
// the same transaction. If any insert fails, every row is rolled
// back together — atomic.
//
// The modal *Evaluation is typically built from a synthetic
// Verdict that aggregates the N samples (build by the caller;
// see transport/mcp/judge.go: registerJudgeConsensus).
//
// Returns the modal row id (per-sample ids are not surfaced).
func (s *Store) SaveConsensusSamples(ctx context.Context, auditMeta *Audit, samples []*Evaluation, modal *Evaluation) (int64, error) {
	if auditMeta == nil || auditMeta.Actor == "" {
		return 0, ErrEmptyAuditActor
	}
	if len(samples) == 0 {
		return 0, errEmptySamples
	}
	if modal == nil {
		return 0, fmt.Errorf("judge store: modal evaluation is nil")
	}
	// Validate samples up front (before opening tx).
	for i, e := range samples {
		if e == nil {
			return 0, fmt.Errorf("judge store: sample[%d] is nil", i)
		}
		if e.EvalType == "" {
			return 0, fmt.Errorf("judge store: sample[%d].eval_type empty", i)
		}
		if e.EvalType == EvalConsensus {
			return 0, fmt.Errorf("judge store: sample[%d].eval_type must not be %q (reserved for modal)", i, EvalConsensus)
		}
		if e.TargetType == "" || e.TargetID == "" {
			return 0, fmt.Errorf("judge store: sample[%d].target empty", i)
		}
	}
	// Modal row validates as an Evaluation with eval_type=consensus.
	modalCopy := *modal
	modalCopy.EvalType = EvalConsensus

	var modalID int64
	err := store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		// project_id resolves to 'default' when empty (NOT NULL
		// column; SQLite DEFAULT only fires when the column is
		// OMITTED). Same fix as SaveEvaluation above.
		projectID := auditMeta.ProjectID
		if projectID == "" {
			projectID = "default"
		}
		// N per-sample inserts.
		for i, e := range samples {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO sdd_evaluations (
					eval_type, target_type, target_id, project_id, verdict_json, confidence,
					provider, model, persona_id, rubric_version, schema_version,
					seed, max_tokens, timeout_ms, temperature, top_p,
					non_deterministic, session_id
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
			`,
				e.EvalType, e.TargetType, e.TargetID, projectID,
				e.VerdictJSON, e.Confidence,
				nullIfEmpty(e.Provider), nullIfEmpty(e.Model),
				nullIfEmpty(e.PersonaID), nullIfEmpty(e.RubricVer),
				nullIfEmpty(e.SchemaVer),
				nullableInt64(e.Seed), nullableInt(e.MaxTokens), nullableInt(e.TimeoutMs),
				nullableFloat(e.Temperature), nullableFloat(e.TopP),
				nullIfEmpty(auditMeta.SessionID),
			)
			if err != nil {
				return fmt.Errorf("judge store SaveConsensusSamples insert sample[%d]: %w", i, err)
			}
			// One audit_log row per sample (N audit rows total).
			sampleID, _ := res.LastInsertId()
			payload := []byte(fmt.Sprintf(
				`{"event":"judge.save.consensus.sample","id":%d,"sample_index":%d,"eval_type":%q}`,
				sampleID, i, e.EvalType,
			))
			if err := writeAuditExecWithProject(ctx, s.audit, tx, auditMeta, payload); err != nil {
				return fmt.Errorf("judge store SaveConsensusSamples audit sample[%d]: %w", i, err)
			}
		}

		// 1 modal insert.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO sdd_evaluations (
				eval_type, target_type, target_id, project_id, verdict_json, confidence,
				provider, model, persona_id, rubric_version, schema_version,
				seed, max_tokens, timeout_ms, temperature, top_p,
				non_deterministic, session_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		`,
			modalCopy.EvalType, modalCopy.TargetType, modalCopy.TargetID,
			projectID,
			modalCopy.VerdictJSON, modalCopy.Confidence,
			nullIfEmpty(modalCopy.Provider), nullIfEmpty(modalCopy.Model),
			nullIfEmpty(modalCopy.PersonaID), nullIfEmpty(modalCopy.RubricVer),
			nullIfEmpty(modalCopy.SchemaVer),
			nullableInt64(modalCopy.Seed), nullableInt(modalCopy.MaxTokens),
			nullableInt(modalCopy.TimeoutMs),
			nullableFloat(modalCopy.Temperature), nullableFloat(modalCopy.TopP),
			nullIfEmpty(auditMeta.SessionID),
		)
		if err != nil {
			return fmt.Errorf("judge store SaveConsensusSamples insert modal: %w", err)
		}
		modalID, _ = res.LastInsertId()

		// 1 audit row for the modal.
		modalPayload := []byte(fmt.Sprintf(
			`{"event":"judge.save.consensus.modal","id":%d,"sample_count":%d}`,
			modalID, len(samples),
		))
		if err := writeAuditExecWithProject(ctx, s.audit, tx, auditMeta, modalPayload); err != nil {
			return fmt.Errorf("judge store SaveConsensusSamples audit modal: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return modalID, nil
}

// GetEvaluation returns one row by id.
func (s *Store) GetEvaluation(ctx context.Context, id int64) (*Evaluation, error) {
	row := s.db.QueryRowContext(ctx, selectEvaluationSQL+` WHERE id = ?`, id)
	return scanEvaluation(row)
}

// ConfidencesByProviderTarget returns the historical confidence
// values for the same (provider, target_type, eval_type) tuple,
// up to limit rows, newest first. Used by the pipeline's
// populateCalibration hook (ADR-011) to feed BootstrapCI.
//
// Empty provider / target_type / eval_type means "no filter on
// that column" (the operator's choice). limit <= 0 defaults to
// 1000 (covers the bootstrap N=1000 budget comfortably).
//
// This is the GLOBAL variant — it does NOT scope by project_id.
// Phase 4 Chunk 4.3 added ConfidencesByProjectProviderTarget for
// project-scoped calibration. The populateCalibration hook uses
// the project-scoped variant first and falls back to this global
// variant when the project has fewer than ShouldRecalibrate(N)
// samples (cold start).
func (s *Store) ConfidencesByProviderTarget(ctx context.Context, provider, targetType, evalType string, limit int) ([]float64, error) {
	if limit <= 0 {
		limit = 1000
	}
	q := `SELECT confidence FROM sdd_evaluations WHERE 1=1`
	args := []interface{}{}
	if provider != "" {
		q += ` AND provider = ?`
		args = append(args, provider)
	}
	if targetType != "" {
		q += ` AND target_type = ?`
		args = append(args, targetType)
	}
	if evalType != "" {
		q += ` AND eval_type = ?`
		args = append(args, evalType)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("judge store ConfidencesByProviderTarget: %w", err)
	}
	defer rows.Close()
	out := make([]float64, 0, limit)
	for rows.Next() {
		var c float64
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("judge store ConfidencesByProviderTarget scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConfidencesByProjectProviderTarget is the project-scoped variant
// of ConfidencesByProviderTarget (Phase 4 Chunk 4.3 — hard
// isolation enforcement). Adds a project_id filter so per-project
// calibration never bleeds across workstreams.
//
// projectID is REQUIRED (non-empty) — callers that don't know the
// project should use ConfidencesByProviderTarget (the global
// variant) directly. This keeps the contract explicit: project-
// scoped queries must always carry the scope.
//
// Same signature shape as ConfidencesByProviderTarget. limit <= 0
// defaults to 1000 (bootstrap-CI budget).
func (s *Store) ConfidencesByProjectProviderTarget(ctx context.Context, projectID, provider, targetType, evalType string, limit int) ([]float64, error) {
	if projectID == "" {
		return nil, fmt.Errorf("judge store ConfidencesByProjectProviderTarget: projectID required")
	}
	if limit <= 0 {
		limit = 1000
	}
	q := `SELECT confidence FROM sdd_evaluations WHERE project_id = ?`
	args := []interface{}{projectID}
	if provider != "" {
		q += ` AND provider = ?`
		args = append(args, provider)
	}
	if targetType != "" {
		q += ` AND target_type = ?`
		args = append(args, targetType)
	}
	if evalType != "" {
		q += ` AND eval_type = ?`
		args = append(args, evalType)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("judge store ConfidencesByProjectProviderTarget: %w", err)
	}
	defer rows.Close()
	out := make([]float64, 0, limit)
	for rows.Next() {
		var c float64
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("judge store ConfidencesByProjectProviderTarget scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCalibration updates the 4 calibration columns for one row.
// Called by Pipeline.populateCalibration after a BootstrapCI run.
// Idempotent — overwrites prior values.
func (s *Store) SetCalibration(ctx context.Context, id int64, ci CalibrationCI, method string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sdd_evaluations
		SET confidence_calibrated = ?,
		    calibration_ci_low    = ?,
		    calibration_ci_high   = ?,
		    calibration_method    = ?
		WHERE id = ?`,
		ci.PointEstimate, ci.CILow, ci.CIHigh, method, id,
	)
	if err != nil {
		return fmt.Errorf("judge store SetCalibration: %w", err)
	}
	return nil
}

// LatestEvaluation returns the most recent row matching (evalType,
// targetType, targetID). Empty evalType / targetType / targetID
// are treated as wildcards.
func (s *Store) LatestEvaluation(ctx context.Context, evalType, targetType, targetID string) (*Evaluation, error) {
	q := selectEvaluationSQL + ` WHERE 1=1`
	args := []interface{}{}
	if evalType != "" {
		q += ` AND eval_type = ?`
		args = append(args, evalType)
	}
	if targetType != "" {
		q += ` AND target_type = ?`
		args = append(args, targetType)
	}
	if targetID != "" {
		q += ` AND target_id = ?`
		args = append(args, targetID)
	}
	q += ` ORDER BY id DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, args...)
	return scanEvaluation(row)
}

// ListEvaluations returns up to Limit rows matching the filter,
// newest first. Zero values in ListFilter are wildcards.
func (s *Store) ListEvaluations(ctx context.Context, f ListFilter) ([]Evaluation, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := selectEvaluationSQL + ` WHERE 1=1`
	args := []interface{}{}
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
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("judge store ListEvaluations: %w", err)
	}
	defer rows.Close()
	var out []Evaluation
	for rows.Next() {
		e, err := scanEvaluation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

const selectEvaluationSQL = `
SELECT id, eval_type, target_type, target_id, project_id, verdict_json, confidence,
       COALESCE(provider, ''), COALESCE(model, ''), COALESCE(persona_id, ''),
       COALESCE(rubric_version, ''), COALESCE(schema_version, ''),
       COALESCE(seed, 0), COALESCE(max_tokens, 0), COALESCE(timeout_ms, 0),
       COALESCE(temperature, 0), COALESCE(top_p, 0),
       COALESCE(session_id, ''), created_at,
       COALESCE(confidence_calibrated, 0),
       COALESCE(calibration_ci_low, 0),
       COALESCE(calibration_ci_high, 0),
       COALESCE(calibration_method, '')
FROM sdd_evaluations`

// scanRow is the interface satisfied by both *sql.Row and *sql.Rows.
type scanRow interface {
	Scan(dest ...interface{}) error
}

func scanEvaluation(row scanRow) (*Evaluation, error) {
	var (
		e         Evaluation
		projectID string
		createdAt string
	)
	if err := row.Scan(
		&e.ID, &e.EvalType, &e.TargetType, &e.TargetID, &projectID,
		&e.VerdictJSON,
		&e.Confidence,
		&e.Provider, &e.Model, &e.PersonaID,
		&e.RubricVer, &e.SchemaVer,
		&e.Seed, &e.MaxTokens, &e.TimeoutMs,
		&e.Temperature, &e.TopP,
		&e.SessionID, &createdAt,
		&e.ConfidenceCalibrated, &e.CalibrationCILow,
		&e.CalibrationCIHigh, &e.CalibrationMethod,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("judge store scan: %w", err)
	}
	e.ProjectID = projectID
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		e.CreatedAt = t
	} else if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
		e.CreatedAt = t
	}
	return &e, nil
}

// --- value helpers (mirror agent_memory) ---

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

func nullableInt64(n int64) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

func nullableFloat(f float64) interface{} {
	if f == 0 {
		return nil
	}
	return f
}

// writeAuditExecWithProject (Phase 4 Chunk 4.3) emits one audit_log
// row inside the caller's transaction. When auditMeta.ProjectID is
// non-empty, the row is stamped with that project_id via
// audit.Writer.WriteExecWithProject. Empty project_id falls back
// to audit.Writer.WriteExec (the row gets project_id='default' via
// the column DEFAULT clause).
//
// Same pattern as agent_memory.writeAuditWithProject, kept separate
// because the judge package doesn't import agent_memory (would
// create a cycle). The audit package's `sqlExec` interface is
// unexported, so we accept *sql.Tx directly — the only concrete
// type passed by SaveEvaluation / SaveConsensusSamples (both are
// inside store.WithTx).
func writeAuditExecWithProject(ctx context.Context, w *audit.Writer, tx *sql.Tx, meta *Audit, payload []byte) error {
	if meta.ProjectID != "" {
		_, err := w.WriteExecWithProject(ctx, tx, meta.Actor, meta.SessionID, meta.ProjectID, payload)
		return err
	}
	_, err := w.WriteExec(ctx, tx, meta.Actor, meta.SessionID, payload)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
