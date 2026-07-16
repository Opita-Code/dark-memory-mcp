// Package postgres contains the Postgres-flavored DDL for Dark Memory MCP
// migrations v1..v6.
//
// Differences from internal/migrate/sqlite:
//   - INTEGER PRIMARY KEY AUTOINCREMENT → BIGSERIAL (or BIGINT GENERATED ALWAYS AS IDENTITY)
//   - TEXT PRIMARY KEY stays TEXT PRIMARY KEY
//   - ALTER TABLE ADD COLUMN is supported the same way
//
// The Migration slice here MUST have the same Version+Name as the
// sqlite package's Migrations; the Up SQL differs.
package postgres

import "github.com/dark-agents/dark-memory-mcp/internal/migrate"

// Migrations is the registry of Postgres migrations.
var Migrations = []migrate.Migration{
	{
		Version: 1,
		Name:    "initial_schema",
		Up: `
CREATE TABLE IF NOT EXISTS research_runs (
    id              BIGSERIAL PRIMARY KEY,
    session_id      TEXT,
    query           TEXT NOT NULL,
    intent          TEXT NOT NULL,
    backend_used    TEXT,
    backends_tried  TEXT,
    took_ms         BIGINT NOT NULL DEFAULT 0,
    confidence_avg  DOUBLE PRECISION NOT NULL DEFAULT 0,
    items_count     INTEGER NOT NULL DEFAULT 0,
    errors          TEXT,
    created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_research_runs_intent  ON research_runs(intent);
CREATE INDEX IF NOT EXISTS idx_research_runs_session ON research_runs(session_id);
CREATE INDEX IF NOT EXISTS idx_research_runs_created ON research_runs(created_at);

CREATE TABLE IF NOT EXISTS research_items (
    id           BIGSERIAL PRIMARY KEY,
    run_id       BIGINT NOT NULL REFERENCES research_runs(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    url          TEXT,
    snippet      TEXT,
    source       TEXT NOT NULL,
    confidence   DOUBLE PRECISION NOT NULL DEFAULT 0,
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
    id               BIGSERIAL PRIMARY KEY,
    research_item_id BIGINT NOT NULL REFERENCES research_items(id) ON DELETE CASCADE,
    target_type      TEXT NOT NULL,
    target_id        TEXT NOT NULL,
    note             TEXT,
    source           TEXT,
    confidence       DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_research_links_target ON research_links(target_type, target_id);

CREATE TABLE IF NOT EXISTS vibe_specs (
    id                BIGSERIAL PRIMARY KEY,
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
    id                BIGSERIAL PRIMARY KEY,
    session_id        TEXT,
    vibe_case         TEXT NOT NULL,
    spec_id           BIGINT REFERENCES vibe_specs(id) ON DELETE SET NULL,
    artifact_url      TEXT,
    artifact_type     TEXT NOT NULL,
    brand_id          TEXT,
    jurisdiction     TEXT,
    has_disclosure    BOOLEAN NOT NULL DEFAULT FALSE,
    validation_status TEXT NOT NULL DEFAULT 'pending',
    created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_case    ON vibe_artifacts(vibe_case);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_brand   ON vibe_artifacts(brand_id);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_session ON vibe_artifacts(session_id);
CREATE INDEX IF NOT EXISTS idx_vibe_artifacts_status  ON vibe_artifacts(validation_status);

CREATE TABLE IF NOT EXISTS vibe_drift_reports (
    id               BIGSERIAL PRIMARY KEY,
    artifact_id      BIGINT NOT NULL REFERENCES vibe_artifacts(id) ON DELETE CASCADE,
    spec_id          BIGINT REFERENCES vibe_specs(id) ON DELETE SET NULL,
    verdict          TEXT NOT NULL,
    spec_diff_json   TEXT,
    judge_reasoning  TEXT,
    reconciled_at    TEXT,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vibe_drift_artifact ON vibe_drift_reports(artifact_id);
CREATE INDEX IF NOT EXISTS idx_vibe_drift_spec     ON vibe_drift_reports(spec_id);

CREATE TABLE IF NOT EXISTS sdd_evaluations (
    id               BIGSERIAL PRIMARY KEY,
    eval_type        TEXT NOT NULL,
    target_type      TEXT NOT NULL,
    target_id        TEXT NOT NULL,
    verdict_json     TEXT NOT NULL,
    confidence       DOUBLE PRECISION NOT NULL DEFAULT 0,
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
    id              BIGSERIAL PRIMARY KEY,
    constitution_id TEXT NOT NULL,
    version         TEXT NOT NULL,
    label           TEXT,
    source          TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    parsed_json     TEXT NOT NULL,
    sha256          TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TEXT NOT NULL,
    activated_at    TEXT,
    UNIQUE(constitution_id, version)
);
CREATE INDEX IF NOT EXISTS idx_constitutions_id     ON constitutions(constitution_id);
CREATE INDEX IF NOT EXISTS idx_constitutions_active ON constitutions(enabled);

CREATE TABLE IF NOT EXISTS mods (
    id            BIGSERIAL PRIMARY KEY,
    mod_id        TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    version       TEXT NOT NULL,
    source        TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    risk_class    TEXT,
    target_scope  TEXT,
    requires_tor  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TEXT NOT NULL,
    updated_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_mods_id     ON mods(mod_id);
CREATE INDEX IF NOT EXISTS idx_mods_risk   ON mods(risk_class);

CREATE TABLE IF NOT EXISTS mod_loads (
    id                  BIGSERIAL PRIMARY KEY,
    mod_id              TEXT NOT NULL,
    session_id          TEXT,
    loaded_at           TEXT NOT NULL,
    duration_ms         BIGINT NOT NULL DEFAULT 0,
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
    id                BIGSERIAL PRIMARY KEY,
    table_name        TEXT NOT NULL,
    row_id            BIGINT NOT NULL,
    actor             TEXT NOT NULL,
    session_id        TEXT NOT NULL,
    write_path        TEXT NOT NULL,
    content_sha256    TEXT,
    canary_present    BOOLEAN NOT NULL DEFAULT FALSE,
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
    id                  BIGSERIAL PRIMARY KEY,
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
		// v7 — project namespace (multi-tenancy) on Postgres.
		//
		// Spec 171 T5 (option b): RLS removed. The earlier version of this
		// migration created ENABLE + FORCE ROW LEVEL SECURITY + policy
		// dark_mem_project_isolation on every tenant-scoped table, but
		// the Store never wrapped transactions in `withProjectTx` to set
		// the dark_mem.project_id GUC — every read returned 0 rows
		// (RLS evaluated `project_id = NULL` = FALSE). The Store now
		// mirrors SQLite's pattern: explicit `WHERE project_id = $1` on
		// every read and tag on every write. No RLS needed.
		//
		// If you want RLS back, see option (a) in spec 171 T5: wire
		// `withProjectTx` around every read and write transaction, then
		// re-introduce this migration's RLS block in a follow-up
		// version. Until that is done AND tested with a live Postgres
		// test (DARK_TEST_POSTGRES_DSN), keep RLS off.
		Version: 7,
		Name:    "project_namespace",
		Up: `
CREATE TABLE IF NOT EXISTS projects (
    id                BIGSERIAL PRIMARY KEY,
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

-- project_id column on every tenant-scoped table.
ALTER TABLE research_runs     ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE research_items    ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE research_links    ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_specs        ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_artifacts    ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vibe_drift_reports ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sdd_evaluations   ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE write_audit       ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE mod_loads        ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sessions         ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE sessions         ADD COLUMN IF NOT EXISTS active_project_id TEXT;

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
`,
	},
	{
		// v9 — vlp_state table (atomic spec 2.3 VLPPersistence)
		// Per-session state machine state. UPSERT pattern: SaveVLPState uses
		// INSERT ... ON CONFLICT (project_id, session_id) DO UPDATE so
		// repeated saves update the existing row instead of inserting
		// duplicates. State column is BIGINT (corresponds to internal/vlp
		// .State enum); LastEvent and LastVerdict are TEXT (canonical string
		// forms) for human-readable audit.
		//
		// INV-7 multi-tenancy: uniqueness is per-project via the composite
		// UNIQUE INDEX (project_id, session_id). A row under project A with
		// session_id="s1" can coexist with project B + session_id="s1".
		// Without this composite, two tenants using overlapping session IDs
		// would collide on the table-level UNIQUE(session_id) constraint.
		Version: 9,
		Name:    "vlp_state_table",
		Up: `
CREATE TABLE IF NOT EXISTS vlp_state (
    id                BIGSERIAL PRIMARY KEY,
    session_id        TEXT NOT NULL,
    state             BIGINT NOT NULL,
    last_event        TEXT,
    last_verdict      TEXT,
    turn_count        BIGINT NOT NULL DEFAULT 0,
    minset_current    TEXT,
    constitution_id   TEXT,
    constitution_ver  TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    project_id        TEXT NOT NULL DEFAULT 'default'
);
CREATE INDEX IF NOT EXISTS idx_vlp_state_session ON vlp_state(session_id);
CREATE INDEX IF NOT EXISTS idx_vlp_state_state   ON vlp_state(state);
CREATE INDEX IF NOT EXISTS idx_vlp_state_project ON vlp_state(project_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vlp_state_project_session
    ON vlp_state(project_id, session_id);
`,
	},
}
