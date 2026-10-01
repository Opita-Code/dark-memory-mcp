// Package audit — payload BLOB split (ADR-019, Phase 6 alpha.18.1).
//
// ADR-019 was deferred from Phase 2 (alpha.15) because Phase 2 was
// already a large commit (hash chain + verify tool + 9 new tests).
// Phase 6 closes ADR-019: the audit_log payload BLOB is parsed
// on Write and its known structured fields land in dedicated columns.
//
// # What gets extracted
//
// The known payload formats (from agent_memory + session + project +
// judge callers) are JSON objects with these common keys:
//
//   - "event"   (string) — e.g., "agent_memory.save", "session.start"
//   - "id"      (int64)  — the affected resource id (or 0)
//   - "kind"    (string) — for agent_memory: "decision" / "observation" etc.
//
// Other keys are ignored. Non-JSON payloads (rare; legacy or external
// callers) leave the structured columns NULL.
//
// # Why this matters
//
// Pre-Phase-6, querying audit_log by event type required parsing
// every row's BLOB. Phase 6 makes this a SQL query:
//
//   SELECT audit_id, actor, payload_event, payload_id
//   FROM audit_log
//   WHERE payload_event = 'agent_memory.save'
//     AND payload_kind = 'decision'
//     AND project_id = 'dark-memory';
//
// No BLOB parsing at query time. Index-friendly.
//
// # Wire contract
//
// Backward-compatible: the payload BLOB is still the canonical
// record. The structured columns are derived metadata. Legacy rows
// (Phase 2/4/5 era) have NULL structured columns; ExtractPayloadFields
// on Write fills the columns for new rows. The hash chain is unchanged.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// PayloadFields is the structured extraction result. Zero value
// represents "could not parse / not JSON / legacy row".
type PayloadFields struct {
	// Event is the action type (e.g., "agent_memory.save").
	Event string
	// ID is the affected resource id (0 when absent).
	ID int64
	// Kind is the agent_memory kind (decision/observation/etc.),
	// or the analogous kind field for other payloads. Empty when
	// absent.
	Kind string
}

// ExtractPayloadFields parses a JSON payload BLOB and returns its
// structured fields. Returns the zero PayloadFields when the payload
// is empty, not valid JSON, or doesn't have any known keys (silent
// — the BLOB is preserved regardless).
//
// Pure function — no DB, no time.
func ExtractPayloadFields(payload []byte) PayloadFields {
	if len(payload) == 0 {
		return PayloadFields{}
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return PayloadFields{}
	}
	out := PayloadFields{}
	if v, ok := raw["event"].(string); ok {
		out.Event = v
	}
	if v, ok := raw["kind"].(string); ok {
		out.Kind = v
	}
	// id can be float64 (JSON default for numbers) or int (when
	// marshaled via Go's int type). Handle both.
	switch v := raw["id"].(type) {
	case float64:
		out.ID = int64(v)
	case int64:
		out.ID = v
	case int:
		out.ID = int64(v)
	}
	return out
}

// ApplyPayloadColumns adds the payload_event, payload_id, payload_kind
// columns to a pre-Phase-6 audit_log table (Phase 2/4/5 era).
// Idempotent via pragma_table_info. Legacy rows have NULL values
// for all three columns; new rows are filled by ExtractPayloadFields
// on Write.
//
// (Phase 6 alpha.18.1 ADR-019)
func ApplyPayloadColumns(ctx context.Context, db *sql.DB) error {
	cols := []struct {
		name string
		typ  string
	}{
		{"payload_event", "TEXT"},
		{"payload_id", "INTEGER"},
		{"payload_kind", "TEXT"},
	}
	for _, col := range cols {
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM pragma_table_info('audit_log') WHERE name = ?",
			col.name,
		).Scan(&n); err != nil {
			return fmt.Errorf("audit ApplyPayloadColumns (pragma %s): %w", col.name, err)
		}
		if n > 0 {
			continue // already present
		}
		if _, err := db.ExecContext(ctx,
			"ALTER TABLE audit_log ADD COLUMN "+col.name+" "+col.typ,
		); err != nil {
			return fmt.Errorf("audit ApplyPayloadColumns (add %s): %w", col.name, err)
		}
	}
	return nil
}

// AddPayloadIndex adds an index on payload_event for the audit_query
// tool's most common WHERE clause. Idempotent via sqlite_master check.
// Indexes ONLY payload_event (not project_id) because the index
// must work even on legacy DBs without project_id column. The
// audit_query tool uses this index for `WHERE payload_event = ?`
// queries; project_id filtering happens in a second pass.
//
// (Phase 6 alpha.18.1 ADR-019)
func AddPayloadIndex(ctx context.Context, db *sql.DB) error {
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_audit_payload_event'",
	).Scan(&n); err != nil {
		return fmt.Errorf("audit AddPayloadIndex (pragma): %w", err)
	}
	if n > 0 {
		return nil // already present
	}
	if _, err := db.ExecContext(ctx,
		"CREATE INDEX idx_audit_payload_event ON audit_log(payload_event)",
	); err != nil {
		return fmt.Errorf("audit AddPayloadIndex (create): %w", err)
	}
	return nil
}
