// O10: MemoryState — runtime snapshot for observability. Returns
// counts (specs, brands, compliance, artifacts, drifts, sdd_evals,
// sessions, runs, items, links), driver name, schema version, and
// retention-relevant metrics (write_audit total, sessions active).
//
// This is a read-only orchestrator. It is the operator's view of
// "what's in dark.db right now". The MCP server exposes this via
// dark_memory_memory_state for LLM-driven introspection (a model
// can ask "what do I know?" and the answer is this snapshot).
//
// GLOBAL by design (matches Stats()). Per-project stats are a
// future sister method (StatsForProject) — out of scope for v1.
package orchestration

import (
	"context"
)

// MemoryStateResult is the runtime snapshot. Counts are aggregate
// across the entire dark.db (not filtered by active project).
type MemoryStateResult struct {
	Driver            string   `json:"driver"`
	SchemaVersion     int      `json:"schema_version"`
	Tables            []string `json:"tables"`
	Counts            MemoryCounts `json:"counts"`
	ActiveProject     string   `json:"active_project,omitempty"`
	CanaryPresent     bool     `json:"canary_present"`
	ConstitutionID    string   `json:"constitution_id,omitempty"`
	ConstitutionVer   string   `json:"constitution_ver,omitempty"`
	ConstitutionDrift bool     `json:"constitution_drift"`
	SnapshotVersion   string   `json:"snapshot_version"` // schema version of this snapshot
}

// MemoryCounts groups the per-table row counts. Mirrors store.Stats
// but adds project-filtered fields where they make sense for the
// operator.
type MemoryCounts struct {
	Specs            int `json:"specs"`
	BrandGuides      int `json:"brand_guides"`
	ComplianceRules  int `json:"compliance_rules"`
	Artifacts        int `json:"artifacts"`
	DriftReports     int `json:"drift_reports"`
	SDDEvaluations   int `json:"sdd_evaluations"`
	SessionsActive   int `json:"sessions_active"`
	SessionsTotal    int `json:"sessions_total"`
	RunsTotal        int `json:"runs_total"`
	ItemsTotal       int `json:"items_total"`
	LinksTotal       int `json:"links_total"`
	WriteAuditTotal  int `json:"write_audit_total"`
	ModsTotal        int `json:"mods_total"`
	ConstitutionsTotal int `json:"constitutions_total"`
	ProjectsTotal    int `json:"projects_total"`
}

// MemoryState returns the runtime snapshot. Read-only.
func (o *Orchestrator) MemoryState(ctx context.Context) (*MemoryStateResult, error) {
	// Aggregate Stats() first — driver name, schema version, table list.
	stats, err := o.Store.Stats(ctx)
	if err != nil {
		return nil, err
	}

	// Counts that aren't already in Stats. Stats is GLOBAL, so we
	// can query the additional counts globally too (they're cheap
	// COUNTs over indexed columns).
	brandGuides, _ := o.Store.ListBrandGuides(ctx, 1000)
	complianceRules, _ := o.Store.ListComplianceRules(ctx, 1000)
	sessions, _ := o.Store.ListSessions(ctx, 1000)
	sessionsActive := 0
	for _, s := range sessions {
		if s.Status == "active" {
			sessionsActive++
		}
	}
	mods, _ := o.Store.ListMods(ctx, 1000)
	constitutions, _ := o.Store.ListConstitutions(ctx, 1000)
	projects, _ := o.Store.ListProjects(ctx, 1000)

	id, ver, _ := o.Store.ActiveConstitution(ctx)

	return &MemoryStateResult{
		Driver:          stats.Driver,
		SchemaVersion:   stats.SchemaVersion,
		Tables:          stats.Tables,
		ActiveProject:   o.Store.ActiveProject(),
		CanaryPresent:   !o.Safety.Active().IsZero(),
		ConstitutionID:  id,
		ConstitutionVer: ver,
		// ConstitutionDrift is computed by ActivePolicy; for this
		// snapshot we leave it false and let callers call ActivePolicy
		// for the drift verdict. Doing the SHA recompute here would
		// duplicate work.
		SnapshotVersion: "1.0.0",
		Counts: MemoryCounts{
			Specs:              stats.SpecsTotal,
			BrandGuides:        len(brandGuides),
			ComplianceRules:    len(complianceRules),
			Artifacts:          stats.ArtifactsTotal,
			DriftReports:       stats.DriftReportsTotal,
			SDDEvaluations:     stats.SDDEvaluations,
			SessionsActive:     sessionsActive,
			SessionsTotal:      len(sessions),
			RunsTotal:          stats.RunsTotal,
			ItemsTotal:         stats.ItemsTotal,
			LinksTotal:         stats.LinksTotal,
			WriteAuditTotal:    stats.WriteAuditTotal,
			ModsTotal:          len(mods),
			ConstitutionsTotal: len(constitutions),
			ProjectsTotal:      len(projects),
		},
	}, nil
}