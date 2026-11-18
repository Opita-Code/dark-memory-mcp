package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// SPEC-194: the persistence of the plan.
//
// The plan lives on vibe_specs (eight new columns, migration v33). It
// is NOT a table of its own: a `plans` table would duplicate
// vibe_specs and break the SPEC-204 rule ("resolve one thing in one
// place"), and vlp_state cannot hold it because its index is UNIQUE
// per session while the plan is per spec.
//
// The four methods here are the whole surface: SavePlan, GetPlan,
// GetOpenPlan and ChargePlanCall.

// SavePlan writes the plan document and its gate state onto an
// existing spec row.
//
// It refuses with store.ErrInvalidArgument when the spec does not
// exist: a plan describes HOW a spec will be carried out, so it cannot
// exist without one.
func (s *Store) SavePlan(ctx context.Context, wc store.WriteContext, specID int64, row *vibeflow.PlanRow) error {
	if err := s.requireProject(); err != nil {
		return err
	}
	if row == nil {
		return store.NewFieldError(store.ErrInvalidArgument, "plan: nil row")
	}
	projectID, projErr := s.resolveWriteProject(ctx, wc)
	if projErr != nil {
		return projErr
	}
	planJSON, err := json.Marshal(row.Plan)
	if err != nil {
		return store.NewFieldError(store.ErrInvalidArgument, "plan: cannot marshal: "+err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		// The spec must exist. A plan without a spec is a plan
		// nobody can execute, so it is refused rather than silently
		// creating an orphan.
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM vibe_specs WHERE id = ? AND project_id = ?`,
			specID, projectID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return store.NewFieldError(store.ErrInvalidArgument,
				fmt.Sprintf("plan: spec %d does not exist in this project", specID))
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE vibe_specs SET
				plan_json        = ?,
				plan_status      = ?,
				plan_decision    = ?,
				plan_decided_by  = ?,
				plan_decided_at  = ?,
				plan_tier        = ?,
				plan_budget_used = ?,
				plan_judge_used  = ?,
				updated_at       = ?
			 WHERE id = ? AND project_id = ?`,
			string(planJSON), string(row.Status), string(row.Decision),
			nullStr(row.DecidedBy), nullStr(row.DecidedAt), string(row.Plan.PlanTier),
			row.BudgetUsed, row.JudgeUsed, now, specID, projectID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_specs",
			RowID:           specID,
			Actor:           wc.Actor,
			SessionID:       nullStrOr(row.SessionID, wc.SessionID),
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			// The notes carry what the decision was and what the
			// plan looked like BEFORE it changed (SPEC-194 AC5: the
			// previous plan stays in the trail). Without them the row
			// says only "something changed", which is not a trail.
			Notes:     wc.Notes,
			CreatedAt: now,
		}, "")
	})
}

// GetPlan reads the plan of a spec. It returns (nil, nil) when the
// spec exists but carries no plan: "no plan" is a normal state, not an
// error, and the gate has to be able to ask the question without
// treating the absence as a failure.
func (s *Store) GetPlan(ctx context.Context, specID int64) (*vibeflow.PlanRow, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	activeProject, err := s.resolveReadProject(ctx, "", "")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getPlanLocked(ctx, specID, activeProject)
}

func (s *Store) getPlanLocked(ctx context.Context, specID int64, projectID string) (*vibeflow.PlanRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, plan_json, plan_status, plan_decision,
		        plan_decided_by, plan_decided_at, plan_tier, plan_budget_used,
		        plan_judge_used, created_at, updated_at
		 FROM vibe_specs WHERE id = ? AND project_id = ?`, specID, projectID)
	pr, err := scanPlanRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return pr, nil
}

// GetOpenPlan reads the plan of the OPEN spec of a session.
//
// "Open" means the spec the session is currently working on: the
// vlp_state row with open_spec_id set. This is how the gate finds the
// plan that applies to the call being made, without the harness having
// to name a spec in every call.
func (s *Store) GetOpenPlan(ctx context.Context, sessionID string) (*vibeflow.PlanRow, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, nil
	}
	activeProject, err := s.resolveReadProject(ctx, "", "")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var openSpecID int64
	err = s.db.QueryRowContext(ctx,
		`SELECT open_spec_id FROM vlp_state WHERE session_id = ? AND project_id = ?`,
		sessionID, activeProject).Scan(&openSpecID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && openSpecID > 0 {
		return s.getPlanLocked(ctx, openSpecID, activeProject)
	}

	// MEASURED el 2026-11-18: vlp_state.open_spec_id no lo escribia
	// NADA en produccion (los dos constructores de VLPStateRow lo
	// dejaron en 0), asi que el camino de arriba nunca encontraba un
	// plan. El fix de raiz es que vibe_spec registre el spec abierto
	// (Store.SetOpenSpecID), pero las filas YA escritas siguen con 0,
	// y una sesion nueva que ya tiene specs y planes no encontraria
	// su plan.
	//
	// Por eso hay un segundo camino: el spec MAS RECIENTE de la
	// sesion que tenga un plan. No es una suposicion: la sesion
	// trabajo sobre el spec que creo, y el plan vive en ese spec.
	//
	// El ORDER BY id DESC importa: el spec mas reciente es el que
	// la sesion esta trabajando, no el primero que abrio.
	var fallbackID int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM vibe_specs
		 WHERE session_id = ? AND project_id = ? AND plan_json IS NOT NULL
		 ORDER BY id DESC LIMIT 1`,
		sessionID, activeProject).Scan(&fallbackID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return s.getPlanLocked(ctx, fallbackID, activeProject)
}

// ChargePlanCall atomically charges ONE call to the plan's budget and
// returns the row AFTER the charge.
//
// ATOMICITY (AC11): the check and the increment happen in ONE
// transaction, and the UPDATE is CONDITIONAL on the budget not being
// exhausted yet. Two concurrent calls therefore cannot both spend the
// last unit: the second UPDATE matches zero rows and gets
// ErrBudgetExceeded. A read-then-write pair would be a race, and the
// budget is the one thing in the plan that MUST not be raced.
//
// The judge-call budget is charged on the same counter path but
// against its own maximum, because the two resources are distinct and
// differently scarce.
func (s *Store) ChargePlanCall(ctx context.Context, wc store.WriteContext, specID int64, judgeCall bool) (*vibeflow.PlanRow, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	projectID, projErr := s.resolveWriteProject(ctx, wc)
	if projErr != nil {
		return nil, projErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var out *vibeflow.PlanRow
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		// Read the current plan INSIDE the transaction.
		row := tx.QueryRowContext(ctx,
			`SELECT id, session_id, plan_json, plan_status, plan_decision,
			        plan_decided_by, plan_decided_at, plan_tier, plan_budget_used,
			        plan_judge_used, created_at, updated_at
			 FROM vibe_specs WHERE id = ? AND project_id = ?`, specID, projectID)
		pr, err := scanPlanRow(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.NewFieldError(store.ErrInvalidArgument,
					fmt.Sprintf("plan: spec %d does not exist in this project", specID))
			}
			return err
		}
		if pr == nil {
			return store.NewFieldError(store.ErrInvalidArgument,
				fmt.Sprintf("plan: spec %d has no plan", specID))
		}

		budget := pr.EffectiveBudget()
		if judgeCall {
			if budget.UsedJudgeCalls >= budget.MaxJudgeCalls {
				return store.NewFieldError(store.ErrBudgetExceeded,
					fmt.Sprintf("plan: judge-call budget exhausted (%d/%d)",
						budget.UsedJudgeCalls, budget.MaxJudgeCalls))
			}
		} else if budget.UsedToolCalls >= budget.MaxToolCalls {
			return store.NewFieldError(store.ErrBudgetExceeded,
				fmt.Sprintf("plan: tool-call budget exhausted (%d/%d)",
					budget.UsedToolCalls, budget.MaxToolCalls))
		}

		// CONDITIONAL update. The WHERE clause re-asserts BOTH the
		// approval AND the ceiling of the resource being spent:
		//
		//   AND plan_budget_used < <max_tool_calls>
		//
		// That condition is what makes the charge atomic AT THE
		// DATABASE LEVEL: if a concurrent transaction spent the last
		// unit between our SELECT and this UPDATE, the UPDATE matches
		// zero rows and the caller gets ErrBudgetExceeded.
		//
		// The earlier version of this comment claimed the condition
		// existed when it did not (the WHERE only re-asserted
		// plan_status='approved'), so the real guarantee was the
		// process mutex and not the SQL. The independent review of
		// PR #43 caught it. Now the SQL says what the comment says.
		var res sql.Result
		if judgeCall {
			res, err = tx.ExecContext(ctx,
				`UPDATE vibe_specs SET plan_judge_used = plan_judge_used + 1,
				 updated_at = ?
				 WHERE id = ? AND project_id = ? AND plan_status = 'approved'
				   AND plan_judge_used < ?`,
				now, specID, projectID, budget.MaxJudgeCalls)
		} else {
			res, err = tx.ExecContext(ctx,
				`UPDATE vibe_specs SET plan_budget_used = plan_budget_used + 1,
				 updated_at = ?
				 WHERE id = ? AND project_id = ? AND plan_status = 'approved'
				   AND plan_budget_used < ?`,
				now, specID, projectID, budget.MaxToolCalls)
		}
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.NewFieldError(store.ErrBudgetExceeded,
				"plan: the plan is no longer approved or no longer has budget")
		}

		// Read back the row as it is AFTER the charge.
		row2 := tx.QueryRowContext(ctx,
			`SELECT id, session_id, plan_json, plan_status, plan_decision,
			        plan_decided_by, plan_decided_at, plan_tier, plan_budget_used,
			        plan_judge_used, created_at, updated_at
			 FROM vibe_specs WHERE id = ? AND project_id = ?`, specID, projectID)
		after, err := scanPlanRow(row2)
		if err != nil {
			return err
		}
		out = after

		return s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "vibe_specs",
			RowID:           specID,
			Actor:           wc.Actor,
			SessionID:       nullStrOr(after.SessionID, wc.SessionID),
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       now,
		}, "")
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanPlanRow reads one plan row. A NULL plan_json means "this spec
// has no plan": the row comes back as (nil, nil), which is the
// contract GetPlan and GetOpenPlan promise.
func scanPlanRow(row *sql.Row) (*vibeflow.PlanRow, error) {
	var pr vibeflow.PlanRow
	var sessionID, planJSON, status, decision, decidedBy, decidedAt, tier, updatedAt sql.NullString
	if err := row.Scan(&pr.SpecID, &sessionID, &planJSON, &status, &decision,
		&decidedBy, &decidedAt, &tier, &pr.BudgetUsed, &pr.JudgeUsed,
		&pr.CreatedAt, &updatedAt); err != nil {
		return nil, err
	}
	if !planJSON.Valid || planJSON.String == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(planJSON.String), &pr.Plan); err != nil {
		return nil, store.NewFieldError(store.ErrInvalidArgument,
			"plan: stored plan is not valid JSON: "+err.Error())
	}
	pr.SessionID = sessionID.String
	pr.Status = vibeflow.ParsePlanStatus(status.String)
	pr.Decision = vibeflow.Decision(decision.String)
	pr.DecidedBy = decidedBy.String
	pr.DecidedAt = decidedAt.String
	if tier.Valid {
		pr.Plan.PlanTier = vibeflow.PlanTier(tier.String)
	}
	if updatedAt.Valid {
		pr.UpdatedAt = updatedAt.String
	}
	return &pr, nil
}
