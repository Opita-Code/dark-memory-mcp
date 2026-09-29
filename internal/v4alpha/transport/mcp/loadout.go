// loadout.go (v4alpha) — PRE-1 C3.
//
// The Loadout is the operator's startup context, returned
// inline with session_start. One call gives the harness
// pinned rows, open todos, recent audit writes, constitution
// binding, schema version, and server time. The harness's
// first 60 seconds is dominated by retrieval, not work —
// Loadout collapses the 5-7 follow-up calls (PRE-1 C1+C2+C4
// already documented this) into one inline field.
//
// # Scope (v1)
//
//   - pinned_rows: am.ListFiltered(operator, pinned=true)
//   - open_todos: am.ListFiltered(operator, kind=todo)
//   - recent_writes: audit_log WHERE actor = operator
//   - constitution: ldflags (id + version) + mods table count
//   - schema_version: latest row in schema_migrations
//   - server_now: time.Now().UTC().Format(RFC3339)
//
// # Partial-failure contract
//
// Each query is independent. If a query fails (e.g. db
// transient error), the affected field is set to its zero
// value AND a warning is added to the warnings slice. The
// session_start itself NEVER fails on a loadout problem —
// the operator needs the session to keep working.
//
// # Out of scope (v1, documented for future)
//
//   - Drift reports (vibe_drifts.session_id column doesn't
//     exist; BUG-10 10b's schema migration).
//   - Judge verdicts (sdd_evaluations.session_id column;
//     same migration).
//   - Per-project filtering. The operator wants ALL their
//     rows, not just one project. project_id is the
//     session's project, not a loadout filter.
//   - Pagination (50/50/20 hard caps; "loadout_large" v2).
//   - Caching (no TTL; every session_start re-queries).
//
// # Atomicity contract
//
//   - ONE constructor: NewLoadoutBuilder
//   - ONE entry point: Build
//   - ZERO schema changes (queries read existing tables).
//
// INV-1: Build is read-only; it emits no audit rows. The
// session_start handler emits one (via session.Store.Start).
package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	am "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// ConstitutionInfo is the constitution binding surfaced in
// the loadout. The id and version come from ldflags (set at
// binary build time). ActiveMods is the count of mods
// currently loaded (0 in v4-alpha.12; the mod loader is
// deferred to alpha.3 per docs/v4-status.md §3).
type ConstitutionInfo struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	ActiveMods int    `json:"active_mods"`
}

// Loadout is the operator's startup context. Returned inline
// with the session_start output. All fields are best-effort:
// a nil pointer or empty slice means "this field could not be
// populated"; the LoadoutWarnings slice has the per-field
// error messages.
type Loadout struct {
	PinnedRows    []am.Row         `json:"pinned_rows"`
	OpenTodos     []am.Row         `json:"open_todos"`
	RecentWrites  []AuditLoadRow   `json:"recent_writes"`
	Constitution  *ConstitutionInfo `json:"constitution"`
	SchemaVersion string           `json:"schema_version"`
	ServerNow     string           `json:"server_now"`
}

// AuditLoadRow is one row from audit_log, projected for the
// loadout. Payload is the raw JSON BLOB (the harness parses
// only if it cares about the contents).
type AuditLoadRow struct {
	AuditID   int64  `json:"audit_id"`
	Actor     string `json:"actor"`
	SessionID string `json:"session_id,omitempty"`
	Payload   string `json:"payload"` // string form of the BLOB
	CreatedAt string `json:"created_at"`
}

// LoadoutLimits bundles the per-field limits. Defaults are
// exported as DefaultLoadoutLimits so the harness can
// override them in the future (today they're hardcoded in
// the wire schema).
type LoadoutLimits struct {
	PinnedRowsMax   int
	OpenTodosMax    int
	RecentWritesMax int
}

// DefaultLoadoutLimits returns the v1 defaults: 50 / 50 / 20.
// The recent_writes cap is lower than summarize's 100 because
// session_start is the IMMEDIATE context (last 20 = ~10
// minutes of work); deeper history calls summarize_session.
func DefaultLoadoutLimits() LoadoutLimits {
	return LoadoutLimits{
		PinnedRowsMax:   50,
		OpenTodosMax:    50,
		RecentWritesMax: 20,
	}
}

// LoadoutBuilder builds a Loadout from the v4alpha deps.
// The schema and constitution sources are functions so the
// tests can substitute without wiring a real binary.
type LoadoutBuilder struct {
	amStore   *am.Store
	db        *sql.DB
	limits    LoadoutLimits
	schemaFn  func(ctx context.Context, db *sql.DB) (string, error)
	constitFn func() *ConstitutionInfo
}

// NewLoadoutBuilder wires the builder. SchemaFn and ConstitFn
// are optional — when nil, defaults are used:
//
//   - schemaFn: queries the latest row from schema_migrations.
//   - constitFn: returns the release-integrity-v4 + version from
//     ldflags (default ConstitutionID / ConstitutionVersion).
//
// The defaults are exposed so tests can construct a builder
// without mocking the package globals.
func NewLoadoutBuilder(amStore *am.Store, db *sql.DB) *LoadoutBuilder {
	return &LoadoutBuilder{
		amStore:   amStore,
		db:        db,
		limits:    DefaultLoadoutLimits(),
		schemaFn:  DefaultSchemaVersion,
		constitFn: defaultConstitution,
	}
}

// SetSchemaFn overrides the schema-version source. Used in
// tests; production code never calls this.
func (b *LoadoutBuilder) SetSchemaFn(fn func(ctx context.Context, db *sql.DB) (string, error)) {
	b.schemaFn = fn
}

// SetConstitFn overrides the constitution source. Used in
// tests; production code never calls this.
func (b *LoadoutBuilder) SetConstitFn(fn func() *ConstitutionInfo) {
	b.constitFn = fn
}

// Build assembles the loadout. Returns the populated Loadout
// and a warnings slice (one warning per failed field, in
// field order: pinned → todos → writes → constitution → schema
// → now). Always returns a non-nil Loadout even when every
// field failed — the operator gets the session and a
// partial context.
func (b *LoadoutBuilder) Build(ctx context.Context, operator string) (*Loadout, []string, error) {
	if operator == "" {
		return nil, nil, fmt.Errorf("loadout: operator required")
	}
	if b.amStore == nil || b.db == nil {
		return nil, nil, fmt.Errorf("loadout: nil store/db")
	}

	out := &Loadout{
		PinnedRows:   []am.Row{},     // empty slice, never nil (JSON stability)
		OpenTodos:    []am.Row{},
		RecentWrites: []AuditLoadRow{},
	}
	warnings := []string{}

	// 1. Pinned rows.
	pinnedTrue := true
	pinned, err := b.amStore.ListFiltered(ctx, operator, am.ListFilter{
		Pinned: &pinnedTrue,
	}, b.limits.PinnedRowsMax)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("pinned_rows: %v", err))
	} else {
		out.PinnedRows = pinned
	}

	// 2. Open todos.
	todos, err := b.amStore.ListFiltered(ctx, operator, am.ListFilter{
		Kind: "todo",
	}, b.limits.OpenTodosMax)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("open_todos: %v", err))
	} else {
		out.OpenTodos = todos
	}

	// 3. Recent writes (audit_log where actor = operator).
	writes, err := b.queryRecentWrites(ctx, operator, b.limits.RecentWritesMax)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("recent_writes: %v", err))
	} else {
		out.RecentWrites = writes
	}

	// 4. Constitution.
	if b.constitFn != nil {
		out.Constitution = b.constitFn()
		if out.Constitution == nil {
			warnings = append(warnings, "constitution: not bound")
		}
	} else {
		out.Constitution = nil
	}

	// 5. Schema version.
	if b.schemaFn != nil {
		sv, err := b.schemaFn(ctx, b.db)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("schema_version: %v", err))
		} else {
			out.SchemaVersion = sv
		}
	}

	// 6. Server now (always succeeds unless time itself fails).
	out.ServerNow = time.Now().UTC().Format(time.RFC3339)

	return out, warnings, nil
}

// queryRecentWrites reads up to limit rows from audit_log
// where actor = operator, ordered by audit_id DESC.
func (b *LoadoutBuilder) queryRecentWrites(ctx context.Context, operator string, limit int) ([]AuditLoadRow, error) {
	rows, err := b.db.QueryContext(ctx, `
		SELECT audit_id, actor, session_id, payload, created_at
		FROM audit_log
		WHERE actor = ?
		ORDER BY audit_id DESC
		LIMIT ?
	`, operator, limit)
	if err != nil {
		return nil, fmt.Errorf("loadout audit query: %w", err)
	}
	defer rows.Close()

	out := make([]AuditLoadRow, 0, 16)
	for rows.Next() {
		var ar AuditLoadRow
		var payload []byte
		var sessionID sql.NullString
		if err := rows.Scan(&ar.AuditID, &ar.Actor, &sessionID, &payload, &ar.CreatedAt); err != nil {
			return nil, fmt.Errorf("loadout audit scan: %w", err)
		}
		if sessionID.Valid {
			ar.SessionID = sessionID.String
		}
		ar.Payload = string(payload)
		out = append(out, ar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loadout audit rows: %w", err)
	}
	return out, nil
}

// DefaultSchemaVersion reads the latest row from
// schema_migrations. Returns the version string (e.g.
// "v4alpha/2026-09-27/002"). Returns ("", nil) when the
// table is empty or missing (the harness still gets a
// loadout, just with schema_version="").
//
// Exported (DefaultSchemaVersion) so tests can verify the
// real DB-query path alongside the stub.
func DefaultSchemaVersion(ctx context.Context, db *sql.DB) (string, error) {
	var version string
	err := db.QueryRowContext(ctx, `
		SELECT version FROM schema_migrations
		ORDER BY applied_at DESC, id DESC
		LIMIT 1
	`).Scan(&version)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return version, nil
}

// ConstitutionID + ConstitutionVersion are the v4-alpha
// default constitution binding. The policy tools
// (dark_memory_active_policy / dark_memory_load_constitution)
// use the same defaults; centralized here so Loadout can
// surface them inline. Overridable via ldflags at build
// time; the defaults are stable across alpha releases until
// the policy_registry table lands (BUG-9).
//
// Usage:
//
//	go build -ldflags "-X ...ConstitutionID=dark-cli/v4-alpha.2 \
//	                   -X ...ConstitutionVersion=v4-alpha.2"
//
// The default below matches policy.go's hardcoded values
// (see dark_memory_active_policy + dark_memory_load_constitution).
var (
	ConstitutionID      = "dark-cli/v4-alpha.1"
	ConstitutionVersion = "v4-alpha.1"
)

// defaultConstitution returns the v4 release-integrity
// constitution. ID + version come from the package globals
// above (overridable via ldflags at build time). ActiveMods
// is 0 in v4-alpha.12.
func defaultConstitution() *ConstitutionInfo {
	return &ConstitutionInfo{
		ID:         ConstitutionID,
		Version:    ConstitutionVersion,
		ActiveMods: 0,
	}
}