// Package sqlite — error_events.go: the Error Observatory storage
// primitive (spec 757, Wave 5D, migration v25).
//
// Contract notes (mirrored in the Store interface docs):
//
//   - SaveErrorEvent is BEST-EFFORT: it never returns an error from
//     the recording path (it logs and returns nil) because error
//     recording must never amplify the failure it captures. The only
//     exceptions are nil-row / empty-project programming errors,
//     which return an error so tests catch misuse.
//   - Dedup: an UNRESOLVED row with the same (domain, code,
//     message_hash, tool_name, session_id) within a 24h window gets
//     count+1 + last_seen_at updated. Resolved clusters stay resolved;
//     a new occurrence after resolution creates a fresh cluster.
//   - INV-1: the INSERT path (new cluster) emits a write_audit row
//     atomically in the same tx (TableName="error_events",
//     RowID=cluster id, Actor="error_observatory"). The dedup UPDATE
//     path does NOT emit a second audit row — incrementing a counter
//     on an existing cluster is not a new data write. If the tx fails,
//     both the cluster and its audit row roll back together, and the
//     caller sees nil (best-effort) — never an orphan audit row.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// dedupWindow is the time window within which an identical
// (domain, code, message_hash, tool_name, session_id) cluster is
// incremented instead of inserted fresh.
const dedupWindow = 24 * time.Hour

// init wires the store sentinels into the errorobs classifier. The
// classifier never imports internal/store (cycle-free); this init
// is the one-time registration point. The table maps each of the 17
// store sentinels (internal/store/store.go:113-143) to its domain.
func init() {
	errorobs.RegisterSentinels([]errorobs.SentinelRegistration{
		{Err: store.ErrDriverMismatch, Domain: errorobs.DomainStore, Code: "ErrDriverMismatch"},
		{Err: store.ErrVersionMismatch, Domain: errorobs.DomainStore, Code: "ErrVersionMismatch"},
		{Err: store.ErrNotConfigured, Domain: errorobs.DomainStore, Code: "ErrNotConfigured"},
		{Err: store.ErrConstitutionDrift, Domain: errorobs.DomainStore, Code: "ErrConstitutionDrift"},
		{Err: store.ErrCanaryInPayload, Domain: errorobs.DomainGate, Code: "ErrCanaryInPayload"},
		{Err: store.ErrModContentRefused, Domain: errorobs.DomainValidation, Code: "ErrModContentRefused"},
		{Err: store.ErrSessionRequired, Domain: errorobs.DomainGate, Code: "ErrSessionRequired"},
		{Err: store.ErrArmedRequired, Domain: errorobs.DomainGate, Code: "ErrArmedRequired"},
		{Err: store.ErrAlreadyExists, Domain: errorobs.DomainStore, Code: "ErrAlreadyExists"},
		{Err: store.ErrNotFound, Domain: errorobs.DomainStore, Code: "ErrNotFound"},
		{Err: store.ErrProjectNotFound, Domain: errorobs.DomainStore, Code: "ErrProjectNotFound"},
		{Err: store.ErrInvalidArgument, Domain: errorobs.DomainValidation, Code: "ErrInvalidArgument"},
		{Err: store.ErrCrossProjectAccess, Domain: errorobs.DomainStore, Code: "ErrCrossProjectAccess"},
		{Err: store.ErrInvalidState, Domain: errorobs.DomainStore, Code: "ErrInvalidState"},
	})
}

// SaveErrorEvent records or dedup-increments one error cluster.
// Best-effort by contract: recording failures are logged and
// swallowed — the caller's request must never fail because error
// telemetry failed.
func (s *Store) SaveErrorEvent(ctx context.Context, e *errorobs.ErrorEvent) error {
	if e == nil {
		return fmt.Errorf("sqlite: SaveErrorEvent: nil event")
	}
	if !errorobs.ValidDomain(e.Domain) {
		return fmt.Errorf("sqlite: SaveErrorEvent: invalid domain %q", e.Domain)
	}
	if !errorobs.ValidSeverity(e.Severity) {
		return fmt.Errorf("sqlite: SaveErrorEvent: invalid severity %q", e.Severity)
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Errorf("sqlite: SaveErrorEvent: message required")
	}
	projectID := projectIDOrActive(e.ProjectID, s.activeProject)
	if projectID == "" {
		return fmt.Errorf("sqlite: SaveErrorEvent: no active project")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	msg := errorobs.Sanitize(e.Message)
	hash := errorobs.MessageHash(msg)

	// Normalize the event fields (caller may have passed raw values).
	e.ProjectID = projectID
	e.Message = msg
	if e.Count <= 0 {
		e.Count = 1
	}
	if e.FirstSeenAt == "" {
		e.FirstSeenAt = now
	}
	if e.LastSeenAt == "" {
		e.LastSeenAt = now
	}
	if e.CreatedAt == "" {
		e.CreatedAt = now
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Dedup: find an UNRESOLVED cluster with the same fingerprint
	// within the window. Note the window is evaluated on the row's
	// first_seen_at (the cluster anchor), not on now — a cluster
	// that started 23h ago and keeps firing stays one cluster.
	cutoff := time.Now().UTC().Add(-dedupWindow).Format(time.RFC3339Nano)
	var existingID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM error_events
		WHERE project_id = ? AND domain = ? AND code = ? AND message_hash = ?
		  AND COALESCE(tool_name, '') = COALESCE(?, '')
		  AND COALESCE(session_id, '') = COALESCE(?, '')
		  AND resolved = 0
		  AND first_seen_at >= ?
		ORDER BY last_seen_at DESC LIMIT 1`,
		projectID, string(e.Domain), e.Code, hash,
		e.ToolName, e.SessionID, cutoff,
	).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logErrorf("sqlite: SaveErrorEvent dedup query: %v", err)
		// Degraded recording: fall through to INSERT (a new cluster
		// is better than losing the error entirely).
		existingID = 0
	}

	if existingID > 0 {
		// Dedup hit: increment count + refresh last_seen_at.
		if _, err := s.db.ExecContext(ctx, `
			UPDATE error_events
			SET count = count + 1, last_seen_at = ?
			WHERE id = ?`,
			now, existingID); err != nil {
			logErrorf("sqlite: SaveErrorEvent dedup update: %v", err)
		}
		e.ID = existingID
		e.Count++
		e.LastSeenAt = now
		return nil
	}

	// Fresh cluster. The INSERT + INV-1 write_audit row commit in the
	// SAME transaction (the write_audit documents the creation of a
	// new error cluster — TableName="error_events", RowID=cluster id).
	// The dedup UPDATE path above deliberately does NOT emit a second
	// write_audit: incrementing a counter on an existing cluster is
	// not a new data write.
	//
	// Best-effort contract is preserved: if the tx fails (DB wedged),
	// the error is logged and nil is returned — the caller's request
	// must never fail because telemetry failed. The write_audit row
	// rolls back with the tx, so a failed cluster insert leaves NO
	// orphan audit row.
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO error_events (
				project_id, session_id, tool_name, domain, code, message,
				message_hash, context_json, severity, count, first_seen_at,
				last_seen_at, resolved, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			projectID, nullString(e.SessionID), nullString(e.ToolName),
			string(e.Domain), e.Code, msg, hash, nullString(e.ContextJSON),
			string(e.Severity), e.Count, e.FirstSeenAt, e.LastSeenAt, e.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("error_events: insert: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("error_events: last insert id: %w", err)
		}
		// INV-1: audit the new cluster atomically with the insert.
		if err := s.recordWriteLockedTx(ctx, tx, audit.WriteEvent{
			TableName: "error_events",
			RowID:     id,
			ProjectID: projectID,
			Actor:     "error_observatory",
			SessionID: e.SessionID,
			WritePath: "SaveErrorEvent",
			CreatedAt: now,
		}, ""); err != nil {
			return fmt.Errorf("error_events: audit: %w", err)
		}
		e.ID = id
		return nil
	})
	if err != nil {
		// Best-effort: never fail the caller.
		logErrorf("sqlite: SaveErrorEvent insert (rolled back with audit): %v", err)
		return nil
	}
	return nil
}

// ListErrorEvents returns error_events rows matching the filters,
// newest-first (last_seen_at DESC). INV-7: scoped to active project.
func (s *Store) ListErrorEvents(ctx context.Context, f errorobs.ErrorListFilters) ([]errorobs.ErrorEvent, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	projectID := s.activeProject

	var clauses []string
	var args []any
	clauses = append(clauses, "project_id = ?")
	args = append(args, projectID)

	if f.Domain != "" {
		clauses = append(clauses, "domain = ?")
		args = append(args, string(f.Domain))
	}
	if f.Severity != "" {
		clauses = append(clauses, "severity = ?")
		args = append(args, string(f.Severity))
	}
	if f.Resolved != nil {
		clauses = append(clauses, "resolved = ?")
		args = append(args, boolToInt(*f.Resolved))
	}
	if f.SessionID != "" {
		clauses = append(clauses, "COALESCE(session_id, '') = ?")
		args = append(args, f.SessionID)
	}
	if f.ToolName != "" {
		clauses = append(clauses, "COALESCE(tool_name, '') = ?")
		args = append(args, f.ToolName)
	}
	if f.Since != "" {
		clauses = append(clauses, "last_seen_at >= ?")
		args = append(args, f.Since)
	}

	query := "SELECT id, project_id, COALESCE(session_id, ''), COALESCE(tool_name, ''), domain, code, message, " +
		"COALESCE(context_json, ''), severity, count, first_seen_at, last_seen_at, " +
		"resolved, COALESCE(resolved_at, ''), COALESCE(resolution_note, ''), created_at " +
		"FROM error_events WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY last_seen_at DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ListErrorEvents: %w", err)
	}
	defer rows.Close()

	out := make([]errorobs.ErrorEvent, 0)
	for rows.Next() {
		var e errorobs.ErrorEvent
		var resolved int
		if err := rows.Scan(
			&e.ID, &e.ProjectID, &e.SessionID, &e.ToolName, &e.Domain, &e.Code,
			&e.Message, &e.ContextJSON, &e.Severity, &e.Count,
			&e.FirstSeenAt, &e.LastSeenAt, &resolved, &e.ResolvedAt,
			&e.ResolutionNote, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("sqlite: ListErrorEvents scan: %w", err)
		}
		e.Resolved = resolved != 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: ListErrorEvents rows: %w", err)
	}
	return out, nil
}

// GetErrorEvent returns one row by id. Cross-project reads return
// (nil, nil) — INV-7 existence-leak parity.
func (s *Store) GetErrorEvent(ctx context.Context, id int64) (*errorobs.ErrorEvent, error) {
	if id <= 0 {
		return nil, nil
	}
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	projectID := s.activeProject

	var e errorobs.ErrorEvent
	var resolved int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, COALESCE(session_id, ''), COALESCE(tool_name, ''), domain, code, message,
		       COALESCE(context_json, ''), severity, count, first_seen_at,
		       last_seen_at, resolved, COALESCE(resolved_at, ''),
		       COALESCE(resolution_note, ''), created_at
		FROM error_events
		WHERE id = ? AND project_id = ?`,
		id, projectID,
	).Scan(
		&e.ID, &e.ProjectID, &e.SessionID, &e.ToolName, &e.Domain, &e.Code,
		&e.Message, &e.ContextJSON, &e.Severity, &e.Count,
		&e.FirstSeenAt, &e.LastSeenAt, &resolved, &e.ResolvedAt,
		&e.ResolutionNote, &e.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: GetErrorEvent: %w", err)
	}
	e.Resolved = resolved != 0
	return &e, nil
}

// ResolveErrorEvent marks a cluster resolved with an operator note.
// Idempotent on already-resolved rows (note updated). ErrNotFound
// when the row does not exist in the active project.
func (s *Store) ResolveErrorEvent(ctx context.Context, wc store.WriteContext, id int64, note string) error {
	if id <= 0 {
		return store.ErrNotFound
	}
	if err := s.requireProject(); err != nil {
		return err
	}
	projectID := projectIDOrActive(wc.ProjectID, s.activeProject)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `
		UPDATE error_events
		SET resolved = 1, resolved_at = COALESCE(resolved_at, ?), resolution_note = ?
		WHERE id = ? AND project_id = ?`,
		now, nullString(note), id, projectID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: ResolveErrorEvent: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: ResolveErrorEvent rows affected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ErrorSummary returns aggregate error metrics. GLOBAL scope (like
// Stats) so operators see cross-project health.
func (s *Store) ErrorSummary(ctx context.Context, hours int) (*errorobs.ErrorSummary, error) {
	if hours <= 0 {
		hours = 1
	}
	recent := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)

	out := &errorobs.ErrorSummary{
		ByDomain:   map[errorobs.Domain]int{},
		BySeverity: map[errorobs.Severity]int{},
		ReportedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}

	// Total + unresolved + last-hour in one pass.
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN resolved = 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN last_seen_at >= ? THEN 1 ELSE 0 END), 0)
		FROM error_events`,
		recent,
	).Scan(&out.TotalErrors, &out.Unresolved, &out.ErrorsLastHour); err != nil {
		return nil, fmt.Errorf("sqlite: ErrorSummary totals: %w", err)
	}

	// By domain.
	drows, err := s.db.QueryContext(ctx, `
		SELECT domain, COUNT(*) FROM error_events GROUP BY domain ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ErrorSummary by domain: %w", err)
	}
	defer drows.Close()
	for drows.Next() {
		var d string
		var c int
		if err := drows.Scan(&d, &c); err != nil {
			return nil, fmt.Errorf("sqlite: ErrorSummary domain scan: %w", err)
		}
		out.ByDomain[errorobs.Domain(d)] = c
	}

	// By severity.
	srows, err := s.db.QueryContext(ctx, `
		SELECT severity, COUNT(*) FROM error_events GROUP BY severity ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ErrorSummary by severity: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var sev string
		var c int
		if err := srows.Scan(&sev, &c); err != nil {
			return nil, fmt.Errorf("sqlite: ErrorSummary severity scan: %w", err)
		}
		out.BySeverity[errorobs.Severity(sev)] = c
	}

	// Top-5 unresolved clusters by count.
	top, err := s.ListErrorEvents(ctx, errorobs.ErrorListFilters{
		Resolved: boolPtr(false),
		Limit:    5,
	})
	if err == nil {
		out.TopRecurring = top
	}

	return out, nil
}

// boolPtr returns a pointer to b (helper for filter construction).
func boolPtr(b bool) *bool { return &b }

// logErrorf is a tiny local logger for the best-effort recording
// path. Uses the package log so recording failures are at least
// visible on stderr when the DB is truly wedged.
func logErrorf(format string, args ...any) {
	log.Printf(format, args...)
}
