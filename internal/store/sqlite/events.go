// Package sqlite — events.go: polymorphic events table operations
// (Phase 12 / alpha.23 T-0001).
//
// The `events` table is a polymorphic append-only log with a `kind`
// discriminator (`modification` | `progress`). See
// docs/specs/SPEC-alpha-11-phase12-modification-events.md for the
// full design (SOTA-grounded by LangFuse + AWS Step Functions +
// OpenTelemetry GenAI semantic conventions).
//
// Architecture summary:
//
//   - Insert is the only write. Events are append-only — no Update or
//     Delete. The HMAC chain (audit_export → audit_verify) catches
//     tampering post-hoc. SPEC §16.2 INV-1 atomic write.
//   - Read operations are query helpers for the 5 patterns surfaced
//     by T-105's event_log + event_replay tools:
//     * GetByID                       — single row
//     * ListByKind                    — kind filter
//     * ListByProcessID               — LangFuse process timeline
//     * ListByRootEventID             — Step Functions exec history
//     * ListByParentEventID           — LangFuse tree-nesting (children)
//   - HMAC chain fields (chain_prev/chain_self/chain_key_id) are NOT
//     stored in events; the chain is computed by audit_export at
//     stream time. This matches the existing write_audit pattern.
//   - INV-20 (rationale-first for modifications) is enforced by the
//     EventWriter in T-0002 (internal/v4alpha/event/writer.go), NOT
//     here. This file is the data layer; the writer is the policy
//     layer.
//
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Phase 12 T-105 (alpha.23): Event and ListEventsFilter are type
// aliases of the canonical types in internal/store. The struct
// definitions live there so the store.Store interface can reference
// them WITHOUT importing this package (which would create a cycle).
// Existing callers that use sqlite.Event continue to work — the
// alias is transparent.
type (
	Event            = store.Event
	ListEventsFilter = store.ListEventsFilter
)

// EventKind values for the polymorphic discriminator (re-declared
// as constants — Go doesn't allow const-aliasing, so we re-export
// the same string values). These MUST match store.EventKind* to
// keep wire compatibility across the type alias.
const (
	EventKindModification = "modification"
	EventKindProgress     = "progress"
)

// Event is one row in the events table. The struct maps 1:1 to the
// schema in internal/migrate/sqlite/ddl.go v32 (and the Postgres
// mirror).
//
// All fields are JSON-marshalable so callers can emit them through
// the OTel GenAI-style payload_json field. The polymorphic fields
// (modification-only vs progress-only) are pointers/strings that are
// NULL when unused — the schema uses NULLABLE TEXT columns so SQLite
// stores NULL cleanly. Scan target fields use sql.NullString /
	// sql.NullFloat64 / sql.NullInt64 to read NULL safely.
	//
	// For zero-value events (caller fills fields one by one), the convention
	// is: set Kind = "modification" or "progress" before calling Insert.
	//
	// Performance: Insert is one statement in runInTx; reads are single
	// QueryContext calls. Indexes (set in v32) cover the 5 read surfaces.
	//
	// (Type definitions for Event + ListEventsFilter live in
	// internal/store/events.go so the store.Store interface can
	// reference them. Type aliases at the top of this file preserve
	// backward compatibility for callers using sqlite.Event directly.)

// InsertEvent appends one event row to the events table. Returns
// the new row's id.
//
// ProjectID is filled from s.activeProject if the caller passes "".
//
// Caller-supplied event fields:
//   - Kind       (required): "modification" or "progress"
//   - Actor      (required): "system" | agent_id | "operator:<name>"
//   - TS         (optional, default = NOW UTC RFC3339Nano)
//   - SessionID  (optional, empty = uncategorized)
//   - ParentEventID / RootEventID (optional, for nested/progress)
//   - Modification fields: any subset, NULL by default
//   - Progress fields:     any subset, NULL by default
//   - PayloadJSON (optional, OTel GenAI-style attributes)
//
// INV-20 rationale enforcement happens in T-0002's EventWriter.Emit,
// NOT here. This Insert is the data-layer primitive; policy lives
// one level up.
//
// Errors:
//   - store.ErrInvalidArgument — missing Kind, invalid Kind value, missing Actor
//   - any underlying tx error wrapped
//
// Locking: read s.activeProject BEFORE s.mu.Lock() (don't call
// s.ActiveProject() inside the tx — that re-acquires the same mutex
// and deadlocks). Pattern mirrors MarkSupersededAgentMemory.
func (s *Store) InsertEvent(
	ctx context.Context,
	ev *Event,
) (int64, error) {
	if err := s.requireProject(); err != nil {
		return 0, err
	}
	if ev.Kind != EventKindModification && ev.Kind != EventKindProgress {
		return 0, fmt.Errorf("%w: events: kind must be %q or %q (got %q)",
			store.ErrInvalidArgument, EventKindModification, EventKindProgress, ev.Kind)
	}
	if ev.Actor == "" {
		return 0, fmt.Errorf("%w: events: actor is required", store.ErrInvalidArgument)
	}

	activeProject := s.activeProject // local copy; avoid re-locking inside tx
	if ev.ProjectID == "" {
		ev.ProjectID = activeProject
	}
	if ev.TS == "" {
		ev.TS = nowRFC3339Nano()
	}
	// SessionID empty string maps to NULL (matches schema NULLABLE
	// semantics for session_id).
	if ev.SessionID.String == "" && !ev.SessionID.Valid {
		ev.SessionID = sql.NullString{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var id int64
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO events (
				project_id, kind, ts,
				actor, session_id, parent_event_id, root_event_id,
				target_table, target_row_id, operation, classification, source,
				rationale, rationale_kind, payload_before, payload_after,
				confidence, judge_verdict, judge_reasoning, judge_run_id,
				process_id, phase, progress_pct, message, duration_ms, error_msg,
				payload_json
			) VALUES (
				?, ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				?
			)`,
			ev.ProjectID, ev.Kind, ev.TS,
			ev.Actor, ev.SessionID, ev.ParentEventID, ev.RootEventID,
			ev.TargetTable, ev.TargetRowID, ev.Operation, ev.Classification, ev.Source,
			ev.Rationale, ev.RationaleKind, ev.PayloadBefore, ev.PayloadAfter,
			ev.Confidence, ev.JudgeVerdict, ev.JudgeReasoning, ev.JudgeRunID,
			ev.ProcessID, ev.Phase, ev.ProgressPct, ev.Message, ev.DurationMs, ev.ErrorMsg,
			ev.PayloadJSON,
		)
		if err != nil {
			return fmt.Errorf("events: insert: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("events: last insert id: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetEventByID returns the event with the given id, or
// store.ErrNotFound if no such row exists.
//
// Cross-project reads are allowed (admin / replay tool); tenant
// isolation at read time is the caller's responsibility.
//
// Locking: read-only path. Does NOT take s.mu (consistent with
// RecallAtTime pattern in bitemporal.go:213).
func (s *Store) GetEventByID(ctx context.Context, id int64) (*Event, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: events: id must be positive (got %d)", store.ErrInvalidArgument, id)
	}
	row := s.db.QueryRowContext(ctx, eventSelectAllFromWhereID, id)
	return scanEvent(row)
}

// ListEventsFilter holds optional filters for the List* family.
// (Type alias at top of this file — see internal/store/events.go
// for the canonical definition.)
//
// SinceID > 0 = id > SinceID (delta cursor, LangFuse flush pattern).
//
// Limit <= 0 = no limit.
//
// ProjectID empty = active project enforced (INV-7). New filters
// added in T-105: TargetTable, TargetRowID, ProcessID, SessionID,
// Actor — see internal/store/events.go for the full struct shape.

// ListEvents returns events matching the filter, ordered by id ASC
// (canonical insertion order). Limit is clamped to [1, 10000].
//
// Locking: read-only path. Does NOT take s.mu.
func (s *Store) ListEvents(ctx context.Context, f ListEventsFilter) ([]*Event, error) {
	if f.Kind != "" && f.Kind != EventKindModification && f.Kind != EventKindProgress {
		return nil, fmt.Errorf("%w: events: kind filter must be empty, %q, or %q",
			store.ErrInvalidArgument, EventKindModification, EventKindProgress)
	}

	q := eventSelectAllFrom + ` WHERE 1=1`
	args := []interface{}{}
	if f.ProjectID != "" {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	// Phase 12 T-105: extended filters (TargetTable, TargetRowID,
	// ProcessID, SessionID, Actor). All optional; matching index
	// for performance (idx_events_target / idx_events_process_id).
	if f.TargetTable != "" {
		q += ` AND target_table = ?`
		args = append(args, f.TargetTable)
	}
	if f.TargetRowID > 0 {
		q += ` AND target_row_id = ?`
		args = append(args, f.TargetRowID)
	}
	if f.ProcessID != "" {
		q += ` AND process_id = ?`
		args = append(args, f.ProcessID)
	}
	if f.SessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	if f.Actor != "" {
		q += ` AND actor = ?`
		args = append(args, f.Actor)
	}
	if f.SinceID > 0 {
		q += ` AND id > ?`
		args = append(args, f.SinceID)
	}
	q += ` ORDER BY id ASC`
	if f.Limit > 0 {
		if f.Limit > 10000 {
			f.Limit = 10000
		}
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events: list: %w", err)
	}
	defer rows.Close()

	out := make([]*Event, 0)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("events: list scan: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list rows: %w", err)
	}
	return out, nil
}

// ListEventsByProcessID returns all progress events for one
// process_id, ordered by id ASC. process_id is a unique-per-async-work
// identifier (UUID-ish, set by the caller — usually drift-<artifactID>
// for async drift_judge or subagent-<id> for delegate_intent).
//
// Empty processID is rejected.
//
// Locking: read-only path. Does NOT take s.mu.
func (s *Store) ListEventsByProcessID(ctx context.Context, processID string) ([]*Event, error) {
	if processID == "" {
		return nil, fmt.Errorf("%w: events: processID is required", store.ErrInvalidArgument)
	}
	rows, err := s.db.QueryContext(ctx,
		eventSelectAllFromWhere+` AND process_id = ? ORDER BY id ASC`,
		processID)
	if err != nil {
		return nil, fmt.Errorf("events: list_by_process: %w", err)
	}
	defer rows.Close()

	out := make([]*Event, 0)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("events: list_by_process scan: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list_by_process rows: %w", err)
	}
	return out, nil
}

// ListEventsByRootEventID returns all events (any kind) sharing the
// given root_event_id, ordered by id ASC. Used by event_replay to
// reconstruct the full timeline of an execution tree (LangFuse trace
// style — root_event_id is the top-level trace id).
//
// rootEventID <= 0 is rejected.
//
// Locking: read-only path. Does NOT take s.mu.
func (s *Store) ListEventsByRootEventID(ctx context.Context, rootEventID int64) ([]*Event, error) {
	if rootEventID <= 0 {
		return nil, fmt.Errorf("%w: events: rootEventID must be positive (got %d)",
			store.ErrInvalidArgument, rootEventID)
	}
	rows, err := s.db.QueryContext(ctx,
		eventSelectAllFromWhere+` AND root_event_id = ? ORDER BY id ASC`,
		rootEventID)
	if err != nil {
		return nil, fmt.Errorf("events: list_by_root: %w", err)
	}
	defer rows.Close()

	out := make([]*Event, 0)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("events: list_by_root scan: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list_by_root rows: %w", err)
	}
	return out, nil
}

// ListEventsByParentEventID returns all events whose parent_event_id
// matches the given id, ordered by id ASC. Used to walk a subagent
// delegation tree (LangFuse-style children of a parent observation).
//
// parentEventID <= 0 is rejected.
//
// Locking: read-only path. Does NOT take s.mu.
func (s *Store) ListEventsByParentEventID(ctx context.Context, parentEventID int64) ([]*Event, error) {
	if parentEventID <= 0 {
		return nil, fmt.Errorf("%w: events: parentEventID must be positive (got %d)",
			store.ErrInvalidArgument, parentEventID)
	}
	rows, err := s.db.QueryContext(ctx,
		eventSelectAllFromWhere+` AND parent_event_id = ? ORDER BY id ASC`,
		parentEventID)
	if err != nil {
		return nil, fmt.Errorf("events: list_by_parent: %w", err)
	}
	defer rows.Close()

	out := make([]*Event, 0)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("events: list_by_parent scan: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list_by_parent rows: %w", err)
	}
	return out, nil
}

// eventSelectAllFromWhere is the SELECT prefix shared by all read
// operations. Add the specific WHERE filter and ORDER BY after this
// fragment.
//
// s is intentionally without a project_id predicate; callers that want
// tenant isolation should filter via ListEventsFilter.ProjectID.
const eventSelectAllFromWhere = eventSelectAllFrom + ` WHERE 1=1`

// eventSelectAllFrom is the SELECT prefix without WHERE.
//
// Column order matches the INSERT order in InsertEvent; same order used
// by scanEvent.
const eventSelectAllFrom = `
SELECT id, project_id, kind, ts,
       actor, session_id, parent_event_id, root_event_id,
       target_table, target_row_id, operation, classification, source,
       rationale, rationale_kind, payload_before, payload_after,
       confidence, judge_verdict, judge_reasoning, judge_run_id,
       process_id, phase, progress_pct, message, duration_ms, error_msg,
       payload_json
  FROM events`

// eventSelectAllFromWhereID is the prefix + WHERE id = ? for
// GetEventByID. The id is bound at the call site.
const eventSelectAllFromWhereID = eventSelectAllFrom + ` WHERE id = ?`

// scanEvent scans one row from a QueryRow or QueryRows into an
// Event. Shared by all read operations.
func scanEvent(s scanner) (*Event, error) {
	var ev Event
	err := s.Scan(
		&ev.ID, &ev.ProjectID, &ev.Kind, &ev.TS,
		&ev.Actor, &ev.SessionID, &ev.ParentEventID, &ev.RootEventID,
		&ev.TargetTable, &ev.TargetRowID, &ev.Operation, &ev.Classification, &ev.Source,
		&ev.Rationale, &ev.RationaleKind, &ev.PayloadBefore, &ev.PayloadAfter,
		&ev.Confidence, &ev.JudgeVerdict, &ev.JudgeReasoning, &ev.JudgeRunID,
		&ev.ProcessID, &ev.Phase, &ev.ProgressPct, &ev.Message, &ev.DurationMs, &ev.ErrorMsg,
		&ev.PayloadJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: events: not found", store.ErrNotFound)
		}
		return nil, fmt.Errorf("events: scan: %w", err)
	}
	return &ev, nil
}

// scanner abstracts over *sql.Row and *sql.Rows so scanEvent can
// serve both GetEventByID (single row) and the List* family
// (multiple rows).
type scanner interface {
	Scan(dest ...interface{}) error
}

// nowRFC3339Nano returns the current UTC time as RFC3339Nano — the
// default events.ts gets when the caller leaves it empty. Mirrors
// bitemporal.go:93 pattern.
func nowRFC3339Nano() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}