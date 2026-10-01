package recall

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DecayScore computes the decay multiplier for one AnnotatedRow
// at time `now`.
//
// Per SPEC-alpha-11-phase5.md §9:
//
//   - Forever rows return 1.0 (no decay).
//   - Other rows: e^(-age / tau) where tau = DecayTauDays.
//   - Access-count boost (ScrubJay-MEM): 1 + 0.3 × log10(count+1),
//     capped at 2.0×, applied ONLY when refresh_on_access=true and
//     last_refreshed_at is set (otherwise the boost is meaningless).
//
// Returns 1.0 for rows without a decay_class (legacy rows; alpha.18
// auto-derives on read but pre-Phase-5 rows may be NULL).
func DecayScore(row AnnotatedRow, now time.Time) float64 {
	if row.DecayClass == DecayClassForever || row.DecayClass == "" {
		return 1.0
	}
	if row.DecayTauDays <= 0 {
		return 1.0
	}
	// Reference time: last_refreshed_at if refresh_on_access is true
	// and the timestamp is set; otherwise created_at (most conservative).
	ref := row.CreatedAt
	if row.RefreshOnAccess && row.LastRefreshedAt != nil && !row.LastRefreshedAt.IsZero() {
		ref = *row.LastRefreshedAt
	}
	if ref.IsZero() {
		return 1.0
	}
	ageDays := now.Sub(ref).Hours() / 24.0
	if ageDays < 0 {
		ageDays = 0
	}
	base := mathExp(-ageDays / float64(row.DecayTauDays))

	// Access-count boost (ScrubJay π_i + τ_i).
	if row.RefreshOnAccess && row.AccessCount > 0 {
		boost := 1.0 + 0.3*log10Fn(float64(row.AccessCount+1))
		if boost > 2.0 {
			boost = 2.0
		}
		base *= boost
	}
	return base
}

// RefreshOnAccess bumps access_count + last_refreshed_at for one row.
// Returns the new access_count.
//
// Per R-C §4 I-4: refresh-on-access is the canonical Mnemosyne pattern.
// It runs in the same transaction as the recall that triggered the
// refresh (operator-flagged, default ON for forever rows; OFF for
// perishable rows to avoid inflating their half-life).
func RefreshOnAccess(ctx context.Context, db *sql.DB, rowID int64, now time.Time) (int int64, err error) {
	if db == nil {
		return 0, fmt.Errorf("recall RefreshOnAccess: db is nil")
	}
	if rowID <= 0 {
		return 0, fmt.Errorf("recall RefreshOnAccess: invalid row id %d", rowID)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		UPDATE agent_memory
		SET access_count = COALESCE(access_count, 0) + 1,
		    last_refreshed_at = ?
		WHERE id = ?
	`, now.UTC().Format(time.RFC3339Nano), rowID)
	if err != nil {
		return 0, fmt.Errorf("recall RefreshOnAccess: %w", err)
	}
	// Read back the new count.
	var n sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT access_count FROM agent_memory WHERE id = ?`, rowID,
	).Scan(&n); err != nil {
		return 1, nil // best-effort: return at least 1
	}
	if !n.Valid {
		return 1, nil
	}
	return n.Int64, nil
}

// MarkSuperseded marks one decision row as superseded by another.
// Per R-F §4 I-4 (TokenMizer-style transition record):
//
//   - The OLD row gets decision_state='superseded', supersedes_id=NEW.
//   - A row is inserted into decision_transitions with trigger+reason.
//   - valid_to is set on the old row (bitemporal: simple form alpha.18).
//
// Both writes are best-effort in alpha.18 — alpha.19 wraps them in
// a transaction with the audit emission.
//
// Returns ErrInvalidSupersession if the new row is not in the same
// project or is not a decision-kind row.
func MarkSuperseded(ctx context.Context, db *sql.DB, oldID, newID int64, trigger, reason, evidence string) error {
	if db == nil {
		return fmt.Errorf("recall MarkSuperseded: db is nil")
	}
	if oldID == newID {
		return fmt.Errorf("recall MarkSuperseded: cannot supersede self (id=%d)", oldID)
	}
	if trigger == "" {
		trigger = "operator_action"
	}
	if reason == "" {
		return fmt.Errorf("recall MarkSuperseded: reason is required")
	}

	// Validate: oldID and newID are both decision-kind rows in the
	// same project. If not, refuse — bad data is worse than no data.
	var oldKind, newKind, oldProject, newProject string
	err := db.QueryRowContext(ctx, `SELECT kind, project_id FROM agent_memory WHERE id = ?`, oldID).Scan(&oldKind, &oldProject)
	if err != nil {
		return fmt.Errorf("recall MarkSuperseded read old: %w", err)
	}
	err = db.QueryRowContext(ctx, `SELECT kind, project_id FROM agent_memory WHERE id = ?`, newID).Scan(&newKind, &newProject)
	if err != nil {
		return fmt.Errorf("recall MarkSuperseded read new: %w", err)
	}
	if oldKind != "decision" || newKind != "decision" {
		return fmt.Errorf("%w: both rows must be kind=decision (old=%s new=%s)",
			ErrInvalidSupersession, oldKind, newKind)
	}
	if oldProject != newProject {
		return fmt.Errorf("%w: project_id mismatch (old=%s new=%s)",
			ErrInvalidSupersession, oldProject, newProject)
	}

	// Best-effort write (alpha.18). Alpha.19 wraps this in a tx.
	if _, err := db.ExecContext(ctx, `
		UPDATE agent_memory
		SET decision_state = 'superseded',
		    supersedes_id = ?,
		    valid_to = ?
		WHERE id = ?
	`, newID, time.Now().UTC().Format(time.RFC3339Nano), oldID); err != nil {
		return fmt.Errorf("recall MarkSuperseded update: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO decision_transitions
		    (decision_id, superseded_id, trigger, reason, evidence, project_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`, newID, oldID, trigger, reason, nullIfEmptyString(evidence), oldProject); err != nil {
		return fmt.Errorf("recall MarkSuperseded transitions: %w", err)
	}
	return nil
}

// ErrInvalidSupersession is returned when MarkSuperseded is called with
// non-decision rows or cross-project supersession.
var ErrInvalidSupersession = fmt.Errorf("recall: invalid supersession")

// nullIfEmptyString converts "" to nil so SQL stores NULL instead of "".
// Used by MarkSuperseded for the optional `evidence` column.
func nullIfEmptyString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// PerVibeCaseMultiplier returns the per-vibe-case decay multiplier
// per SPEC §9 (per-vibe-case table). Returns 1.0 if vibe_case is
// unknown or empty (no adjustment).
//
//   - C1 code: × 1.0
//   - C2 text: × 0.5  (text decays slower than medium)
//   - C3 decision: × ∞ (forever; multiplier is meaningless)
//   - C4 research: × 1.0 (× 3.0 math, × 0.3 AI/ML — alpha.19)
//   - C5 video: × 0.25 (high decay)
//   - C6 audio: × 1.0 (medium)
//   - C7 multi: subtask-aware (alpha.19)
func PerVibeCaseMultiplier(vibeCase string) float64 {
	switch vibeCase {
	case VibeCaseCode, VibeCaseResearch, VibeCaseAudio:
		return 1.0
	case VibeCaseText:
		return 0.5
	case VibeCaseVideo:
		return 0.25
	case VibeCaseDecision:
		// Forever — caller should check DecayClass first.
		return 1.0
	default:
		return 1.0
	}
}
