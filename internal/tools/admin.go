// Package tools — admin.go: the ADMIN namespace (3 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_admin_migrate
//	dark_memory_admin_schema_status
//	dark_memory_admin_vacuum
//
// All three are operator-side maintenance tools. They are the only
// tools in the surface that can mutate schema state (per RFC §5
// naming convention: any tool that mutates schema lives under
// dark_memory_admin_*).
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterAdmin wires the 3 ADMIN tools into the registry.
func RegisterAdmin(reg *Registry, _ /* orch */ interface{}, st store.Store) {
	// admin_migrate — runs pending schema migrations.
	reg.Add(BindStore("admin_migrate",
		"Run pending schema migrations on the active Store. Returns the migration status before + after.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		st,
		func(ctx context.Context, s store.Store, in struct{}) (*AdminMigrateResult, error) {
			// Single MigrationStatus call before + after (perf: saves
			// one full status walk on large DBs).
			before, err := s.MigrationStatus(ctx)
			if err != nil {
				return nil, err
			}
			if err := s.Migrate(ctx); err != nil {
				return nil, err
			}
			after := cloneMigrationStatus(before) // copy by-value snapshot
			// Re-query only if Migrate reported a version bump — for
			// now we always re-query to keep semantics obvious.
			after, err = s.MigrationStatus(ctx)
			if err != nil {
				return nil, err
			}
			applied := diffMigrationStatus(before, after)
			return &AdminMigrateResult{
				BeforeVersion: versionFromStatus(before),
				AfterVersion:  versionFromStatus(after),
				Applied:       applied,
			}, nil
		}))

	// admin_schema_status — read-only: current schema version + tables.
	reg.Add(BindStore("admin_schema_status",
		"Return the current schema version, applied migrations, and the list of tables in the active Store. Read-only.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		st,
		func(ctx context.Context, s store.Store, in struct{}) (*AdminSchemaStatusResult, error) {
			stats, err := s.Stats(ctx)
			if err != nil {
				return nil, err
			}
			migrations, err := s.MigrationStatus(ctx)
			if err != nil {
				return nil, err
			}
			return &AdminSchemaStatusResult{
				Driver:        s.DriverName(),
				SchemaVersion: stats.SchemaVersion,
				Tables:        stats.Tables,
				Migrations:    migrations,
			}, nil
		}))

	// admin_vacuum — runs SQLite VACUUM (no-op on Postgres where the
	// store implementation handles it via autovacuum).
	reg.Add(BindStore("admin_vacuum",
		"Run a vacuum on the active Store. SQLite: reclaims space; Postgres: no-op (autovacuum handles GC).",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"days_old": map[string]any{"type": "integer", "description": "GC rows older than N days. 0 = no time-based GC."},
				"tables":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Restrict to these table names. Empty = all retention-policy tables."},
				"dry_run":  map[string]any{"type": "boolean", "description": "Report counts only, no delete."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in AdminVacuumInput) (*AdminVacuumResult, error) {
			stats, err := s.Vacuum(ctx, store.VacuumPolicy{
				DaysOld: in.DaysOld,
				Tables:  in.Tables,
				DryRun:  in.DryRun,
			})
			if err != nil {
				return nil, err
			}
			return &AdminVacuumResult{
				TablesVacuumed: stats.TablesVacuumed,
				RowsDeleted:    stats.RowsDeleted,
				BytesReclaimed: stats.BytesReclaimed,
				Duration:       stats.Duration,
			}, nil
		}))
}

// AdminMigrateResult is the output for admin_migrate.
type AdminMigrateResult struct {
	BeforeVersion int      `json:"before_version"`
	AfterVersion  int      `json:"after_version"`
	Applied       []string `json:"applied,omitempty"`
}

// AdminSchemaStatusResult is the output for admin_schema_status.
type AdminSchemaStatusResult struct {
	Driver        string                 `json:"driver"`
	SchemaVersion int                    `json:"schema_version"`
	Tables        []string               `json:"tables"`
	Migrations    []store.MigrationStatus `json:"migrations"`
}

// AdminVacuumInput is the input for admin_vacuum.
type AdminVacuumInput struct {
	DaysOld int      `json:"days_old,omitempty"`
	Tables  []string `json:"tables,omitempty"`
	DryRun  bool     `json:"dry_run,omitempty"`
}

// AdminVacuumResult is the output for admin_vacuum.
type AdminVacuumResult struct {
	TablesVacuumed []string `json:"tables_vacuumed"`
	RowsDeleted    int64    `json:"rows_deleted"`
	BytesReclaimed int64    `json:"bytes_reclaimed,omitempty"`
	Duration       string   `json:"duration"`
}

// versionFromStatus returns the highest applied version from a
// migration status slice.
func versionFromStatus(statuses []store.MigrationStatus) int {
	max := 0
	for _, s := range statuses {
		if s.Version > max {
			max = s.Version
		}
	}
	return max
}

// diffMigrationStatus returns the names of migrations that were
// pending before but applied after. Used by admin_migrate to
// surface what changed.
func diffMigrationStatus(before, after []store.MigrationStatus) []string {
	beforeSet := make(map[string]bool, len(before))
	for _, s := range before {
		if s.Applied {
			beforeSet[s.Name] = true
		}
	}
	applied := make([]string, 0)
	for _, s := range after {
		if s.Applied && !beforeSet[s.Name] {
			applied = append(applied, s.Name)
		}
	}
	return applied
}

// cloneMigrationStatus returns a deep copy of the status slice. Used
// as a defensive fallback when an after-query fails.
func cloneMigrationStatus(in []store.MigrationStatus) []store.MigrationStatus {
	out := make([]store.MigrationStatus, len(in))
	copy(out, in)
	return out
}