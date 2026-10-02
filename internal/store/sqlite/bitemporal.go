// Package sqlite — bitemporal.go: MarkSupersededAgentMemory + RecallAtTime
// implementations (alpha.20 Chunk 8.7, ADR-014 lite).
//
// These two operations ride on top of the v30/v31 schema additions
// (see internal/migrate/sqlite/ddl.go):
//
//	v30 phase5_port_to_production adds:
//	  agent_memory.decision_state    TEXT NOT NULL DEFAULT 'active'
//	  agent_memory.supersedes_id     INTEGER
//	  agent_memory.valid_from        TEXT
//	  agent_memory.valid_to          TEXT
//	  decision_transitions           table (TokenMizer-style history)
//	v31 bitemporal_lite adds:
//	  agent_memory.transaction_time  TEXT
//	  agent_memory.valid_time        TEXT
//
// The v4alpha MarkSuperseded (internal/v4alpha/recall/decay.go) was
// the prototype; this file is the production-grade version reachable
// via MCP dark_memory_mark_superseded.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// MarkSupersededAgentMemory implements store.Store.MarkSupersededAgentMemory.
//
// Algorithm:
//  1. Read both rows; verify they exist, are kind=decision, and share
//     the active project. Cross-check first so we don't half-write.
//  2. Default trigger='operator_action' when empty; validTime=NOW (UTC
//     RFC3339Nano) when empty.
//  3. tx:
//     UPDATE agent_memory SET decision_state='superseded',
//                              supersedes_id=newMemID,
//                              valid_to=validTime
//     WHERE id=oldMemID AND project_id=active.
//     INSERT INTO decision_transitions.
//     recordWriteLocked (WritePath=MarkSupersededAgentMemory).
//
// Design note (ADR-014 lite scope): we deliberately do NOT update
// newMemID's valid_from column. The NEW row's valid_from is set at
// SaveAgentMemory time (= created_at) and represents when the new
// fact first became true. Setting it to validTime here would conflate
// "fact creation time" with "supersession event time" — those are
// different concepts and a caller that needs both can JOIN
// decision_transitions to recover the supersession event time
// (decision_transitions.created_at). The strict "set new.valid_from
// = validTime" semantic is alpha.21+ territory; see docs/sota-critique
// §5.2.3.
//
// Returns:
//   - store.ErrInvalidSupersession (wrapped) for same-id, missing,
//     non-decision-kind, or cross-project rows.
//   - any underlying tx error wrapped.
//
// Locking: read s.activeProject BEFORE taking s.mu.Lock() (don't call
// s.ActiveProject() inside the tx closure — that re-acquires the same
// mutex and deadlocks). Pattern is the same as SaveAgentMemory:4857.
func (s *Store) MarkSupersededAgentMemory(
	ctx context.Context, wc store.WriteContext,
	oldMemID, newMemID int64,
	trigger, reason, evidence, validTime string,
) error {
	if oldMemID == newMemID {
		return fmt.Errorf("%w: cannot supersede self (id=%d)", store.ErrInvalidSupersession, oldMemID)
	}
	if oldMemID <= 0 || newMemID <= 0 {
		return fmt.Errorf("%w: invalid row id (old=%d new=%d)", store.ErrInvalidSupersession, oldMemID, newMemID)
	}
	if reason == "" {
		return fmt.Errorf("%w: reason is required", store.ErrInvalidSupersession)
	}
	if err := s.requireProject(); err != nil {
		return err
	}
	// Read activeProject BEFORE acquiring s.mu.Lock() — calling
	// s.ActiveProject() inside the lock would deadlock (ActiveProject
	// re-acquires s.mu).
	activeProject := s.activeProject
	if trigger == "" {
		trigger = "operator_action"
	}
	if validTime == "" {
		validTime = time.Now().UTC().Format(time.RFC3339Nano)
	}

	// Pre-flight: validate before taking the lock.
	var oldKind, newKind, oldProject, newProject string
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, project_id FROM agent_memory WHERE id = ?`, oldMemID,
	).Scan(&oldKind, &oldProject)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: old row not found (id=%d)", store.ErrInvalidSupersession, oldMemID)
		}
		return fmt.Errorf("agent_memory: read old: %w", err)
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT kind, project_id FROM agent_memory WHERE id = ?`, newMemID,
	).Scan(&newKind, &newProject)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: new row not found (id=%d)", store.ErrInvalidSupersession, newMemID)
		}
		return fmt.Errorf("agent_memory: read new: %w", err)
	}
	if oldKind != agentmemory.KindDecision || newKind != agentmemory.KindDecision {
		return fmt.Errorf("%w: both rows must be kind=decision (old=%s new=%s)",
			store.ErrInvalidSupersession, oldKind, newKind)
	}
	if oldProject != activeProject || newProject != activeProject {
		return fmt.Errorf("%w: cross-project supersession forbidden (old=%s new=%s active=%s)",
			store.ErrInvalidSupersession, oldProject, newProject, activeProject)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runInTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		// Step 1: mark old as superseded.
		res, err := tx.ExecContext(ctx, `
			UPDATE agent_memory
			   SET decision_state = 'superseded',
			       supersedes_id  = ?,
			       valid_to       = ?
			 WHERE id = ?
			   AND project_id = ?`,
			newMemID, validTime, oldMemID, activeProject)
		if err != nil {
			return fmt.Errorf("agent_memory: supersede: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// race: row deleted between pre-flight and tx.
			return fmt.Errorf("%w: old row vanished during supersession (id=%d)",
				store.ErrInvalidSupersession, oldMemID)
		}
		// Step 2: record transition.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO decision_transitions
			    (decision_id, superseded_id, trigger, reason, evidence, project_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			newMemID, oldMemID, trigger, reason, nullIfEmptyString(evidence),
			activeProject, now); err != nil {
			return fmt.Errorf("decision_transitions: insert: %w", err)
		}
		// Step 3: INV-1 audit row.
		if wc.WritePath == "" {
			wc.WritePath = "MarkSupersededAgentMemory"
		}
		if err := s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName:       "agent_memory",
			RowID:           oldMemID,
			ProjectID:       activeProject,
			Actor:           wc.Actor,
			SessionID:       wc.SessionID,
			WritePath:       wc.WritePath,
			ConstitutionID:  wc.ConstitutionID,
			ConstitutionVer: wc.ConstitutionVer,
			CreatedAt:       now,
		}, ""); err != nil {
			return fmt.Errorf("agent_memory: audit: %w", err)
		}
		return nil
	})
}

// RecallAtTime implements store.Store.RecallAtTime. Bitemporal "as-of"
// query: returns rows in the active project whose valid_time <= t.
//
// Algorithm (SIMPLE form per ADR-014 lite):
//   - Filter: project_id == active AND archived_at IS NULL
//     AND COALESCE(valid_time, created_at) <= t.
//   - Optional kind filter (no kind = any kind).
//   - Sort: valid_time DESC (newest first), then id ASC (deterministic
//     tie-break).
//
// What's NOT in this lite form (per ADR-014 strict supersession-
// exclusion is alpha.21+ territory):
//   - decision_state='superseded' rows ARE still returned (the
//     historical fact was known at time t, even if it's been
//     superseded since). To exclude them, callers can filter on the
//     result client-side OR use a future strict-form variant.
//   - No JOIN to decision_transitions to check successor valid_from.
//     That semantic (as-of strict: "what was the current decision at
//     time t") is alpha.21+; see docs/sota-critique §5.2.3.
//
// Limit <= 0 means no limit. Empty kind means any kind.
//
// Locking (mirror of MarkSupersededAgentMemory): read s.activeProject
// into a local variable BEFORE the query — calling s.ActiveProject()
// inside the read path acquires s.mu, which is a write-side lock; for
// a pure SELECT we don't need to take it. Pattern is the same as
// SaveAgentMemory:4349 / MarkSupersededAgentMemory:75.
func (s *Store) RecallAtTime(
	ctx context.Context, t time.Time, kind string, limit int,
) ([]*agentmemory.AgentMemory, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if t.IsZero() {
		return nil, fmt.Errorf("%w: t must not be zero", store.ErrInvalidArgument)
	}
	activeProject := s.activeProject // local copy; avoid re-locking on read path
	tStr := t.UTC().Format(time.RFC3339Nano)

	q := `
		SELECT id, project_id, COALESCE(session_id, ''), operator,
		       COALESCE(agent_id, ''), kind, COALESCE(memory_type, ''),
		       COALESCE(title, ''), content, COALESCE(tags, ''),
		       pinned, created_at, updated_at, COALESCE(archived_at, ''),
		       COALESCE(expires_at, ''),
		       COALESCE(transaction_time, ''), COALESCE(valid_time, '')
		  FROM agent_memory
		 WHERE project_id = ?
		   AND archived_at IS NULL
		   AND COALESCE(valid_time, created_at) <= ?`
	args := []interface{}{activeProject, tStr}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY COALESCE(valid_time, created_at) DESC, id ASC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("agent_memory: recall_at_time query: %w", err)
	}
	defer rows.Close()

	out := make([]*agentmemory.AgentMemory, 0)
	for rows.Next() {
		var m agentmemory.AgentMemory
		var pinnedInt int
		if err := rows.Scan(
			&m.ID, &m.ProjectID, &m.SessionID, &m.Operator, &m.AgentID,
			&m.Kind, &m.MemoryType, &m.Title, &m.Content, &m.Tags,
			&pinnedInt, &m.CreatedAt, &m.UpdatedAt, &m.ArchivedAt, &m.ExpiresAt,
			&m.TransactionTime, &m.ValidTime,
		); err != nil {
			return nil, fmt.Errorf("agent_memory: recall_at_time scan: %w", err)
		}
		m.Pinned = pinnedInt != 0
		out = append(out, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent_memory: recall_at_time rows: %w", err)
	}
	return out, nil
}

// nullIfEmptyString returns nil when s is empty so SQL stores NULL
// instead of "" — keeps the evidence column NULLABLE semantics clean.
func nullIfEmptyString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}