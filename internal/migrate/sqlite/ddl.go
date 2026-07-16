// Package sqlite contains the SQLite-flavored DDL for Dark Memory MCP
// migrations v1..v6. Each migration's Up SQL is idempotent.
//
// The Migration slice here MUST have the same Version+Name as the
// postgres package's Migrations; the Up SQL differs (INTEGER PRIMARY
// KEY AUTOINCREMENT vs SERIAL, etc.).
package sqlite

import "github.com/dark-agents/dark-memory-mcp/internal/migrate"

// Migrations is the registry of SQLite migrations. Order matters —
// they apply in Version ascending.
var Migrations = []migrate.Migration{
	{
		Version: 1,
		Name:    "initial_schema",
		Up: `
CREATE TABLE IF NOT EXISTS research_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT,
    query           TEXT NOT NULL,
    intent          TEXT NOT NULL,
    backend_used    TEXT,
    backends_tried  TEXT,
    took_ms         INTEGER NOT NULL DEFAULT 0,
    confidence_avg  REAL NOT NULL DEFAULT 0,
    items_count     INTEGER NOT NULL DEFAULT 0,
    errors          TEXT,
    created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_research_runs_intent  ON research_runs(intent);
CREATE INDEX IF NOT EXISTS idx_research_runs_session ON research_runs(session_id);
CREATE INDEX IF NOT EXISTS idx_research_runs_created ON research_runs(created_at);

CREATE TABLE IF NOT EXISTS research_items (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       INTEGER NOT NULL REFERENCES research_runs(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    url          TEXT,
    snippet      TEXT,
    source       TEXT NOT NULL,
    confidence   REAL NOT NULL DEFAULT 0,
    freshness_at TEXT,
    lang         TEXT,
    raw          TEXT,
    actor        TEXT,
    write_path   TEXT,
    content_sha256 TEXT,
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_research_items_run    ON research_items(run_id);
CREATE INDEX IF NOT EXISTS idx_research_items_source ON research_items(source);

CREATE TABLE IF NOT EXISTS research_links (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    research_item_id INTEGER NOT NULL REFERENCES research_items(id) ON DELETE CASCADE,
    target_type      TEXT NOT NULL,
    target_id        TEXT NOT NULL,
    note             TEXT,
    source           TEXT,
    confidence       REAL NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_research_links_target ON research_links(target_type, target_id);

CREATE TABLE IF NOT EXISTS vibe_specs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    vibe_case         TEXT NOT NULL,
    session_id        TEXT,
    constitution_json TEXT,
    spec_json         TEXT,
    tasks_json        TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT
);
CREATE INDEX IF NOT EXISTS idx_vibe_specs_case    ON vibe_specs(vibe_case);
CREATE INDEX IF NOT EXISTS idx_vibe_specs_session ON vibe_specs(session_id);

CREATE TABLE IF NOT EXISTS vibe_brands (
    brand_id        TEXT PRIMARY KEY,
    voice_json      TEXT,
    visual_json     TEXT,
    narrative_json  TEXT,
    compliance_json TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT
);

CREATE TABLE IF NOT EXISTS vibe_compliance (
    jurisdiction   TEXT PRIMARY KEY,
    rules_json     TEXT NOT NULL,
    effective_at   TEXT,
    source_url     TEXT,
    created_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vibe_artifacts (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        TEXT,
    vibe_case         TEXT NOT NULL,
    spec_id           INTEGER REFERENCES vibe_specs(id) ON DELETE SET NULL,
    artifact_url      TEXT,
    artifact_type     TEXT NOT NULL,
    brand_id          TEXT,
    jurisdiction     TEXT,
    has_disclosure    INTEGER NOT NULL DEFAULT 0,
    validation_status TEXT NOT NULL DEFAULT 'pending',
    created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_case    ON vibe_artifacts(vibe_case);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_brand   ON vibe_artifacts(brand_id);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_session ON vibe_artifacts(session_id);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_status  ON vibe_artifacts(validation_status);

CREATE TABLE IF NOT EXISTS vibe_drift_reports (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    artifact_id      INTEGER NOT NULL REFERENCES vibe_artifacts(id) ON DELETE CASCADE,
    spec_id          INTEGER REFERENCES vibe_specs(id) ON DELETE SET NULL,
    verdict          TEXT NOT NULL,
    spec_diff_json   TEXT,
    judge_reasoning  TEXT,
    reconciled_at    TEXT,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vibe_drift_artifact ON vibe_drift_reports(artifact_id);
CREATE INDEX IF NOT EXISTS idx_vibe_drift_spec     ON vibe_drift_reports(spec_id);

CREATE TABLE IF NOT EXISTS sdd_evaluations (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    eval_type        TEXT NOT NULL,
    target_type      TEXT NOT NULL,
    target_id        TEXT NOT NULL,
    verdict_json     TEXT NOT NULL,
    confidence       REAL NOT NULL DEFAULT 0,
    prompt_version   TEXT,
    model            TEXT,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sdd_eval_type     ON sdd_evaluations(eval_type);
CREATE INDEX IF NOT EXISTS idx_sdd_eval_target   ON sdd_evaluations(target_type, target_id);
CREATE INDEX IF NOT EXISTS idx_sdd_eval_created  ON sdd_evaluations(created_at);
`,
	},
	{
		Version: 2,
		Name:    "constitutions_and_mods",
		Up: `
CREATE TABLE IF NOT EXISTS constitutions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    constitution_id TEXT NOT NULL,
    version         TEXT NOT NULL,
    label           TEXT,
    source          TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    parsed_json     TEXT NOT NULL,
    sha256          TEXT NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    activated_at    TEXT,
    UNIQUE(constitution_id, version)
);
CREATE INDEX IF NOT EXISTS idx_constitutions_id     ON constitutions(constitution_id);
CREATE INDEX IF NOT EXISTS idx_constitutions_active ON constitutions(enabled);

CREATE TABLE IF NOT EXISTS mods (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    mod_id        TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    version       TEXT NOT NULL,
    source        TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    risk_class    TEXT,
    target_scope  TEXT,
    requires_tor  INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_mods_id     ON mods(mod_id);
CREATE INDEX IF NOT EXISTS idx_mods_risk   ON mods(risk_class);

CREATE TABLE IF NOT EXISTS mod_loads (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    mod_id              TEXT NOT NULL,
    session_id          TEXT,
    loaded_at           TEXT NOT NULL,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    capabilities_count  INTEGER NOT NULL DEFAULT 0,
    error               TEXT,
    constitution_id     TEXT
);
CREATE INDEX IF NOT EXISTS idx_mod_loads_mod     ON mod_loads(mod_id);
CREATE INDEX IF NOT EXISTS idx_mod_loads_session ON mod_loads(session_id);
`,
	},
	{
		Version: 3,
		Name:    "sdd_evaluations_constitution_audit",
		Up: `
ALTER TABLE sdd_evaluations ADD COLUMN constitution_id     TEXT;
ALTER TABLE sdd_evaluations ADD COLUMN constitution_version TEXT;
ALTER TABLE sdd_evaluations ADD COLUMN active_mods_json    TEXT;
ALTER TABLE sdd_evaluations ADD COLUMN refused_attempts    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sdd_evaluations ADD COLUMN refusal_pattern     TEXT;
`,
	},
	{
		Version: 4,
		Name:    "write_audit_table",
		Up: `
CREATE TABLE IF NOT EXISTS write_audit (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    table_name        TEXT NOT NULL,
    row_id            INTEGER NOT NULL,
    actor             TEXT NOT NULL,
    session_id        TEXT NOT NULL,
    write_path        TEXT NOT NULL,
    content_sha256    TEXT,
    canary_present    INTEGER NOT NULL DEFAULT 0,
    constitution_id   TEXT,
    constitution_ver  TEXT,
    notes             TEXT,
    created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_write_audit_session   ON write_audit(session_id);
CREATE INDEX IF NOT EXISTS idx_write_audit_actor     ON write_audit(actor);
CREATE INDEX IF NOT EXISTS idx_write_audit_table_row ON write_audit(table_name, row_id);
CREATE INDEX IF NOT EXISTS idx_write_audit_created   ON write_audit(created_at);
CREATE INDEX IF NOT EXISTS idx_write_audit_constitution ON write_audit(constitution_id);
`,
	},
	{
		Version: 5,
		Name:    "sessions_table",
		Up: `
CREATE TABLE IF NOT EXISTS sessions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT NOT NULL UNIQUE,
    status              TEXT NOT NULL DEFAULT 'active',
    constitution_id     TEXT,
    constitution_ver    TEXT,
    active_mods         TEXT,
    started_at          TEXT NOT NULL,
    closed_at           TEXT,
    notes               TEXT,
    parent_session_id   TEXT,
    operator            TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_status  ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at);
CREATE INDEX IF NOT EXISTS idx_sessions_parent  ON sessions(parent_session_id);
`,
	},
	{
		Version: 6,
		Name:    "constitutions_watchdog_columns",
		Up: `
ALTER TABLE constitutions ADD COLUMN last_verified_at TEXT;
ALTER TABLE constitutions ADD COLUMN last_verified_sha256 TEXT;
CREATE INDEX IF NOT EXISTS idx_constitutions_verified ON constitutions(last_verified_at);
`,
	},
	{
		// v7 — project namespace (multi-tenancy)
		// Adds `projects` table + `project_id` column on every tenant-scoped table
		// with default 'default' so existing 164 specs persist unchanged.
		// Phase 2 of this migration (separately applied) adds RLS policies for
		// Postgres. SQLite has no RLS, so isolation is enforced at the Store
		// layer via automatic project_id filters on every read.
		Version: 7,
		Name:    "project_namespace",
		Up: `
CREATE TABLE IF NOT EXISTS projects (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        TEXT NOT NULL UNIQUE,
    display_name      TEXT NOT NULL,
    description       TEXT,
    constitution_id   TEXT,
    constitution_ver  TEXT,
    created_at        TEXT NOT NULL,
    archived_at       TEXT,
    parent_project_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_projects_active ON projects(archived_at);
CREATE INDEX IF NOT EXISTS idx_projects_parent ON projects(parent_project_id);

-- project_id column on every tenant-scoped table. Default 'default'
-- keeps existing 164 specs working. New rows can specify any project_id.
ALTER TABLE research_runs     ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE research_items    ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE research_links    ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_specs        ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_artifacts    ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_drift_reports ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sdd_evaluations   ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE write_audit       ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE mod_loads        ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sessions         ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sessions         ADD COLUMN active_project_id TEXT;

-- Composite indexes (project_id is the new hot prefix)
CREATE INDEX IF NOT EXISTS idx_research_runs_project    ON research_runs(project_id, id);
CREATE INDEX IF NOT EXISTS idx_research_items_project   ON research_items(project_id, id);
CREATE INDEX IF NOT EXISTS idx_research_links_project   ON research_links(project_id, id);
CREATE INDEX IF NOT EXISTS idx_vibe_specs_project       ON vibe_specs(project_id, id);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_project   ON vibe_artifacts(project_id, id);
CREATE INDEX IF NOT EXISTS idx_vibe_drift_project      ON vibe_drift_reports(project_id, id);
CREATE INDEX IF NOT EXISTS idx_sdd_eval_project         ON sdd_evaluations(project_id, id);
CREATE INDEX IF NOT EXISTS idx_write_audit_project     ON write_audit(project_id, id);
CREATE INDEX IF NOT EXISTS idx_mod_loads_project       ON mod_loads(project_id, id);
CREATE INDEX IF NOT EXISTS idx_sessions_project        ON sessions(project_id, id);

-- Backfill: existing rows already have project_id='default' via the DEFAULT
-- clause above, no UPDATE needed. INSERT: the sessions table now has both
-- project_id (audit) and active_project_id (runtime override). The Store
-- impl seeds a 'default' project row on first Open.
`,
	},
	{
		// v8 — multi-tenant brand_id: change vibe_brands.brand_id from
		// PRIMARY KEY (globally unique) to UNIQUE(project_id, brand_id)
		// (per-project unique). Required for the multi-tenant pattern
		// where the same brand_id (e.g. "acme-base") lives in many
		// sub-projects with different voice/visual.
		//
		// Also fixes an oversight in v7: vibe_brands was not in the
		// ALTER TABLE list that added project_id to tenant-scoped
		// tables. This migration adds the column first (existing rows
		// backfill to 'default' via DEFAULT), then performs the
		// rename-recreate pattern required because SQLite cannot DROP
		// a PRIMARY KEY in place. Existing rows are preserved verbatim.
		//
		// vibe_compliance is intentionally left untouched: jurisdiction
		// is a property of law (GDPR is GDPR), not of project. Global by
		// design — see spec 171 T4c finding W3-002 / decision rationale.
		Version: 8,
		Name:    "vibe_brands_composite_unique",
		Up: `
-- Step 1: ensure project_id exists on vibe_brands (oversight fix).
ALTER TABLE vibe_brands ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default';

-- Step 2: rename-recreate to swap the PRIMARY KEY for a composite UNIQUE.
ALTER TABLE vibe_brands RENAME TO vibe_brands_old;

CREATE TABLE vibe_brands (
    brand_id        TEXT NOT NULL,
    voice_json      TEXT,
    visual_json     TEXT,
    narrative_json  TEXT,
    compliance_json TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT,
    project_id      TEXT NOT NULL DEFAULT 'default',
    UNIQUE(project_id, brand_id)
);

INSERT INTO vibe_brands
    (brand_id, voice_json, visual_json, narrative_json, compliance_json, created_at, updated_at, project_id)
SELECT brand_id, voice_json, visual_json, narrative_json, compliance_json, created_at, updated_at, project_id
    FROM vibe_brands_old;

DROP TABLE vibe_brands_old;

CREATE INDEX IF NOT EXISTS idx_vibe_brands_project ON vibe_brands(project_id, brand_id);
`,
	},
}
