// Package sqlite contains the SQLite-flavored DDL for Dark Memory MCP
// migrations v1..v16. Each migration's Up SQL is idempotent.
//
// The Migration slice here MUST have the same Version+Name as the
// postgres package's Migrations; the Up SQL differs (INTEGER PRIMARY
// KEY AUTOINCREMENT vs SERIAL, etc.).
//
// v11 (5A.iii — pivote active-memory) adds the atom-frame storage:
// `vibe_frames` + `vibe_recall_subscriptions`. v12 (5E.ii) is the
// destructive-but-rebuild sessions lifecycle rewrite for the 5-state
// enum + resurrection chain. See SCHEMA_v11_v12.md §3 for design.
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
	{
		// v9 — vlp_state table (atomic spec 2.3 VLPPersistence)
		// Per-session state machine state. UPSERT pattern: SaveVLPState uses
		// INSERT ... ON CONFLICT(project_id, session_id) DO UPDATE so repeated
		// saves update the existing row instead of inserting duplicates.
		// State column is INT (corresponds to internal/vlp.State enum);
		// LastEvent and LastVerdict are TEXT (canonical string forms) for
		// human-readable audit.
		//
		// INV-7 multi-tenancy: uniqueness is per-project via the composite
		// UNIQUE INDEX (project_id, session_id). A row under project A with
		// session_id="s1" can coexist with project B + session_id="s1".
		// Without this composite, two tenants using overlapping session IDs
		// (e.g. harness-generated UUIDs) would collide on the table-level
		// UNIQUE(session_id) constraint. The Store impl enforces reads via
		// AND project_id = ? on every query; writes use
		// wc.ProjectID || s.ActiveProject().
		Version: 9,
		Name:    "vlp_state_table",
		Up: `
CREATE TABLE IF NOT EXISTS vlp_state (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        TEXT NOT NULL,
    state             INTEGER NOT NULL,
    last_event        TEXT,
    last_verdict      TEXT,
    turn_count        INTEGER NOT NULL DEFAULT 0,
    minset_current    TEXT,
    constitution_id   TEXT,
    constitution_ver  TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    project_id        TEXT NOT NULL DEFAULT 'default',
    open_spec_id      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_vlp_state_session ON vlp_state(session_id);
CREATE INDEX IF NOT EXISTS idx_vlp_state_state   ON vlp_state(state);
CREATE INDEX IF NOT EXISTS idx_vlp_state_project ON vlp_state(project_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vlp_state_project_session
    ON vlp_state(project_id, session_id);
`,
	},
	{
		// v10 — audit project composite index (debt-elimination, F33).
		// Note: the write_audit.project_id column was already added by
		// migration v7 ("project_namespace") when the rest of the
		// tenant-scoped tables got it. This migration only adds the
		// composite (project_id, session_id) index for ListWrites
		// filtering efficiency.
		//
		// The Store impl now (a) populates WriteEvent.ProjectID from
		// wc.ProjectID or s.ActiveProject() at write time, and (b)
		// filters ListWrites by ProjectID at read time (when set).
		Version: 10,
		Name:    "audit_project_index",
		Up: `
CREATE INDEX IF NOT EXISTS idx_write_audit_project_session ON write_audit(project_id, session_id);
`,
	},
	{
		// v11 — atomic frames + recall subscriptions (5A.iii).
		// Additive: creates two new tables with no impact on existing
		// rows. `vibe_frames` stores composed atomic frames keyed by
		// (session_id, scope_level, scope_id, frame_kind) with TTL via
		// `expires_at` and an `INV-5`-ish `content_sha256`. The `last_write_id`
		// column is the cache-invalidation cursor (pointing into write_audit).
		// `vibe_recall_subscriptions` tracks per-scope `last_seen_token` so
		// `dark_memory_recall(scope, since_token)` can compute deltas.
		// Both tables carry an explicit `project_id` (INV-7). See
		// `vibe-flow/main/SCHEMA_v11_v12.md` §2.4 for the full SQL + index
		// strategy.
		Version: 11,
		Name:    "atomic_frames_and_recall_subscriptions",
		Up: `
CREATE TABLE IF NOT EXISTS vibe_frames (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  session_id      TEXT    NOT NULL,
  scope_level     TEXT    NOT NULL CHECK (scope_level IN ('global','project','session','call')),
  scope_id        TEXT    NOT NULL,
  frame_kind      TEXT    NOT NULL CHECK (frame_kind IN ('identity','scope','evidence','capabilities','drift','persona')),
  composed_at     TEXT    NOT NULL,
  expires_at      TEXT    NOT NULL,
  frame_json      TEXT    NOT NULL,
  content_sha256  TEXT    NOT NULL,
  last_write_id   INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_scope_lookup
  ON vibe_frames (session_id, scope_level, scope_id, frame_kind, expires_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_project
  ON vibe_frames (project_id, frame_kind, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_invalidation
  ON vibe_frames (session_id, last_write_id);

CREATE TABLE IF NOT EXISTS vibe_recall_subscriptions (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT    NOT NULL,
  session_id       TEXT    NOT NULL,
  scope_level      TEXT    NOT NULL CHECK (scope_level IN ('global','project','session','call')),
  scope_id         TEXT    NOT NULL,
  last_seen_token  INTEGER NOT NULL DEFAULT 0,
  created_at       TEXT    NOT NULL,
  updated_at       TEXT    NOT NULL,
  UNIQUE (session_id, scope_level, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_recall_subs_lookup
  ON vibe_recall_subscriptions (session_id, scope_level, scope_id);

CREATE INDEX IF NOT EXISTS idx_recall_subs_project
  ON vibe_recall_subscriptions (project_id, scope_level);
`,
	},
	{
		// v12 — session lifecycle overhaul (Wave 5E.ii).
		// Destructive-but-rebuild on the `sessions` table. Backfills any
		// legacy `status='open'` row to `closed_aborted` so harness-accidental
		// deaths become resurrectable (per INV-8). Adds three new columns
		// (last_heartbeat_at for INV-9, parent_session_id + resurrected_from
		// for the resurrection chain). Adds a `session_event` column to
		// `write_audit` so session-related writes carry an audit breadcrumb.
		// Adds three indexes for status/operator lookups and resurrection
		// chain walks. See SCHEMA_v11_v12.md §3 for the canonical design
		// and SCHEMA_v11_v12.md §3.5 (backfill pre-flight) for the abort-on-
		// unexpected-legacy-status guard (the Go migration runner runs that
		// pre-flight; this SQL is the safe-rebuild path).
		Version: 12,
		Name:    "session_lifecycle_overhaul",
		Up: `
PRAGMA foreign_keys = OFF;
ALTER TABLE sessions RENAME TO _sessions_old;

CREATE TABLE sessions (
  id                  INTEGER PRIMARY KEY,
  session_id          TEXT    NOT NULL UNIQUE,
  status              TEXT    NOT NULL DEFAULT 'open'
                      CHECK (status IN ('open','idle','closed_clean','closed_aborted','archived')),
  constitution_id     TEXT,
  constitution_ver    TEXT,
  active_mods         TEXT,
  operator            TEXT    NOT NULL,
  started_at          TEXT    NOT NULL,
  closed_at           TEXT,
  last_heartbeat_at   TEXT,
  parent_session_id   TEXT,
  resurrected_from    TEXT,
  notes               TEXT,
  project_id          TEXT    NOT NULL DEFAULT 'default',
  created_at          TEXT    NOT NULL,
  CHECK (
    (status IN ('closed_clean','archived') AND closed_at IS NOT NULL)
    OR (status = 'open' AND closed_at IS NULL)
    OR (status IN ('idle','closed_aborted'))
  )
);

-- Backfill: legacy 'open' (orphan by accident) -> closed_aborted (resurrectable).
-- Legacy 'closed' (terminal by historical convention) -> closed_clean.
-- Legacy 'active' (the v5 default before v12) -> closed_clean too (treat as
-- pre-pivot baseline: any actually-still-open sessions will surface as orphans
-- via the runtime's startup-recover sweep, which is the canonical resurrection
-- entry point per BRIDGE_AND_COEXISTENCE.md §6.2).
-- created_at: legacy schemas (v5 to v11) did not track created_at separately
-- from started_at, so we use started_at as the proxy value.
-- active_project_id (added in v7) is intentionally dropped — v12 schema folds
-- that state into project_id + resurrected_from, and the project_id column
-- already holds the active value for legacy rows.
INSERT INTO sessions
  (id, session_id, status,
   constitution_id, constitution_ver, active_mods,
   operator, started_at, closed_at,
   last_heartbeat_at, parent_session_id, resurrected_from,
   notes, project_id, created_at)
SELECT
  id, session_id,
  CASE
    WHEN status='open'           THEN 'closed_aborted'
    WHEN status='active'         THEN 'closed_clean'
    WHEN status='closed'         THEN 'closed_clean'
    WHEN status='closed_clean'   THEN 'closed_clean'
    WHEN status='closed_aborted' THEN 'closed_aborted'
    WHEN status='archived'       THEN 'archived'
    ELSE 'closed_clean'
  END,
  constitution_id, constitution_ver, active_mods,
  operator, started_at, closed_at,
  NULL, NULL, NULL,
  notes, project_id, started_at
FROM _sessions_old;

DROP TABLE _sessions_old;

ALTER TABLE write_audit ADD COLUMN session_event TEXT;

CREATE INDEX IF NOT EXISTS idx_sessions_status_operator
  ON sessions (status, operator, closed_at DESC);

CREATE INDEX IF NOT EXISTS idx_sessions_resurrected
  ON sessions (resurrected_from);

CREATE INDEX IF NOT EXISTS idx_write_audit_session_event
  ON write_audit (session_event, created_at);

PRAGMA foreign_keys = ON;
`,
	},
	{
		// v13 — vibe_frames unique natural key (5A.ii.a polish).
		// Adds a UNIQUE INDEX on the composite key (project_id,
		// session_id, scope_level, scope_id, frame_kind) so SaveFrame
		// can use INSERT ... ON CONFLICT (true UPSERT) instead of the
		// SELECT-then-INSERT/UPDATE pattern that was racy under
		// concurrent writes for the same key. The pivot drift judge
		// flagged this at 0.55 — the polish wave ships the fix.
		//
		// Failure mode: pre-v13 DBs that already accumulated duplicate
		// rows from the race hit the UNIQUE INDEX creation with
		// "UNIQUE constraint failed". The migration runner's applyOne
		// propagates that error verbatim and aborts v13 — operator
		// sees `migrate: v13: UNIQUE constraint failed: ...` and can
		// resolve by deleting duplicates manually:
		//
		//   DELETE FROM vibe_frames WHERE id NOT IN (
		//     SELECT MIN(id) FROM vibe_frames
		//     GROUP BY project_id, session_id, scope_level, scope_id, frame_kind
		//   );
		//
		// then re-run. The pivot's INFRA-003 work environment
		// (corporate WDAC + Carbon Black) didn't exercise concurrent
		// SaveFrame calls, so the live dark.db has no duplicates —
		// the v13 migration applies cleanly. New DBs created after
		// this wave have no race window.
		Version: 13,
		Name:    "vibe_frames_unique_natural_key",
		Up: `
CREATE UNIQUE INDEX IF NOT EXISTS uq_vibe_frames_natural_key
  ON vibe_frames (project_id, session_id, scope_level, scope_id, frame_kind);
`,
	},
	{
		// v14 — projects.drift_strictness (5X.3).
		// Adds drift_strictness column to projects table for per-project
		// override of the drift-at-write interceptor (5A.vi M6). Values:
		//   'default' (use DARK_DRIFT_STRICTNESS env)
		//   'off' / 'warn' / 'strict' (per-project override)
		// NOT NULL DEFAULT 'default' so pre-v14 DBs preserve the
		// existing env-driven behavior. Idempotent: ADD COLUMN with
		// DEFAULT is safe to re-run (SQLite records the column once).
		Version: 14,
		Name:    "projects_drift_strictness",
		Up: `
ALTER TABLE projects ADD COLUMN drift_strictness TEXT NOT NULL DEFAULT 'default';
`,
	},
	{
		// v15 — vlp_state.open_spec_id (5X.4).
		// Adds the spec_id column that ScopeFrame needs to point at
		// the active spec for a session. Pre-5X.4 the recall cache
		// (5A.ii.b.2.c) had to use vlp_state.ID as a proxy because
		// no real mapping existed. This column closes that gap.
		//
		// Idempotent: ADD COLUMN with DEFAULT 0 is safe to re-run.
		// Pre-v15 DBs get open_spec_id=0 (no spec open) for all
		// existing rows — same behavior as before.
		Version: 15,
		Name:    "vlp_state_open_spec_id",
		Up: `
ALTER TABLE vlp_state ADD COLUMN open_spec_id INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		// v16 - constitution watchdog audit columns (5E.iv follow-up).
		// Adds last_verified_at + last_verified_sha256 to constitutions
		// so the watchdog can record the timestamp + SHA of the most
		// recent verification. Without these columns, the watchdog's
		// INSERT INTO constitutions (runWatchdog, INV-4 path) fails
		// silently with "no such column" because the INSERT statement
		// references both fields. The Store code's `_, _ = ...Exec`
		// discarded the error, so the watchdog appeared to succeed
		// but no row was written. Surfaced by
		// TestSQLiteConstitutionWatchdogMigration.
		//
		// activated_at already exists (added in the original v6
		// constitution migration). Pre-v16 DBs already had activated_at
		// populated; new columns default to NULL/empty for back-compat.
		Version: 16,
		Name:    "constitution_watchdog_audit",
		Up: `
ALTER TABLE constitutions ADD COLUMN last_verified_at TEXT;
ALTER TABLE constitutions ADD COLUMN last_verified_sha256 TEXT;
`,
	},
	{
		// v17 - projects.active_session_id + active_session_set_at
		// (v2.0.2 gate fix). v2.0.1 introduced GateMiddleware that
		// requires SessionID for every tool call, but its
		// ActiveSessionResolver was an empty StaticSessionResolver
		// — every call returned "" and the gate refused all calls.
		// v2.0.2 introduces real per-project active-session tracking:
		// session_start saves + sets this column; session_close
		// clears via sessionID match (race-safe); session_resume
		// overwrites. The StoreBackedActiveSessionResolver (server/)
		// reads this column on each call (with a short in-process
		// cache) so PreCheck's SessionID resolution works.
		//
		// active_session_set_at is the timestamp for sweeper-style
		// staleness cleanup (a session older than HEARTBEAT_TIMEOUT
		// without a heartbeat should be cleared). The sweeper
		// itself lands in a follow-up; this column is here so the
		// schema is ready when it does. Pre-v17 rows have both
		// columns NULL, which the resolver treats as "no active
		// session" (same behavior as pre-v2.0.1).
		//
		// NOT NULL constraints: deliberately omitted. Pre-v17 rows
		// need to be readable without backfill. The Store impls
		// treat NULL as the same as "".
		//
		// Idempotent: ADD COLUMN with no DEFAULT is safe to re-run on
		// a DB that already has these columns.
		Version: 17,
		Name:    "projects_active_session",
		Up: `
ALTER TABLE projects ADD COLUMN active_session_id TEXT;
ALTER TABLE projects ADD COLUMN active_session_set_at TEXT;
`,
	},
	{
		// v18 - agent_memory table + indexes + FTS5 mirror + sync
		// triggers (v2.1.0).
		//
		// Mem0-aligned agent-memory data plane. See
		// internal/agentmemory/types.go for the canonical kind set
		// and scope semantics; see
		// dark-mem-research/2026-07-27-v2.0.1-v2.0.2-research.md for
		// the research-driven design.
		//
		// Why FTS5 in the same migration: BM25 ranked search is a
		// core retrieval path (cf. Mem0 README "Fuses semantic
		// similarity, BM25 keyword matching, and entity matching in
		// parallel"). v2.1.0 ships BM25; semantic / entity matching
		// follow in v2.1.x once we have an embedding pipeline.
		//
		// Trigger strategy: classic FTS5 contentless-table mirror.
		//   ai  = AFTER INSERT  -> mirror the new row
		//   ad  = AFTER DELETE  -> remove the old FTS row ('delete'
		//                          command is the FTS5 idiom for
		//                          removing from a contentless mirror)
		//   au1 = BEFORE UPDATE -> delete-old (BEFORE so we can still
		//                          read old.* values)
		//   au2 = AFTER UPDATE  -> insert-new (AFTER so we read new.*)
		// Splitting UPDATE into two triggers lets each trigger body
		// be a single statement inside BEGIN..END (which the v2.1.0
		// SQL-aware splitStatements handles cleanly; see
		// internal/migrate/split_statements.go and
		// split_test.go for the table-driven coverage).
		//
		// Idempotent: every CREATE uses IF NOT EXISTS / IF EXISTS.
		// No ALTER statements in this migration.
		Version: 18,
		Name:    "agent_memory_and_fts5",
		Up: `
CREATE TABLE IF NOT EXISTS agent_memory (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL,
    session_id  TEXT,
    operator    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    title       TEXT,
    content     TEXT NOT NULL,
    tags        TEXT,
    pinned      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    archived_at TEXT,
    expires_at  TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_memory_proj ON agent_memory (project_id, archived_at, pinned DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_memory_sess ON agent_memory (session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_memory_op   ON agent_memory (operator, archived_at, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_memory_kind ON agent_memory (project_id, kind, archived_at);
CREATE VIRTUAL TABLE IF NOT EXISTS agent_memory_fts USING fts5(
    content, title, tags,
    content='agent_memory', content_rowid='id',
    tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS agent_memory_ai AFTER INSERT ON agent_memory BEGIN INSERT INTO agent_memory_fts(rowid, content, title, tags) VALUES (new.id, new.content, COALESCE(new.title,''), COALESCE(new.tags,'')); END;
CREATE TRIGGER IF NOT EXISTS agent_memory_ad AFTER DELETE ON agent_memory BEGIN INSERT INTO agent_memory_fts(agent_memory_fts, rowid, content, title, tags) VALUES('delete', old.id, old.content, COALESCE(old.title,''), COALESCE(old.tags,'')); END;
CREATE TRIGGER IF NOT EXISTS agent_memory_au1 BEFORE UPDATE ON agent_memory BEGIN INSERT INTO agent_memory_fts(agent_memory_fts, rowid, content, title, tags) VALUES('delete', old.id, old.content, COALESCE(old.title,''), COALESCE(old.tags,'')); END;
CREATE TRIGGER IF NOT EXISTS agent_memory_au2 AFTER UPDATE ON agent_memory BEGIN INSERT INTO agent_memory_fts(rowid, content, title, tags) VALUES (new.id, new.content, COALESCE(new.title,''), COALESCE(new.tags,'')); END;
`,
	},
	{
		// v19 - agent_memory v2.3.0 columns + indices (save-decouple +
		// INV-10 + agent_memory_recall).
		//
		// Adds two columns without disturbing the FTS5 mirror
		// (memory_type is a filter, not text — FTS5 stays unchanged
		// because it indexes content+title+tags already):
		//
		//   agent_id     TEXT NULL
		//     Mem0 agent_id semantics: the LLM that owns the memory.
		//     Distinct from operator (which is the human/agent
		//     identity per INV-1 audit). Single-agent flows may use
		//     same value for both. Filterable via scope=agent.
		//
		//   memory_type  TEXT NULL
		//     Mem0 three-class taxonomy: episodic | semantic |
		//     procedural. Independent of `kind` (operator's tool
		//     filter, 10 values). NULL = unclassified. Used as
		//     export contract for cross-system port; not currently
		//     a recall scope dimension (v2.4.0+ may add).
		//
		// Why two columns and not one combined: agent_id is a key,
		// memory_type is a tag. They have different nullability
		// constraints downstream and combining them would require
		// JSON parsing at every read. Two columns, two indices.
		//
		// Idempotent via the standard SQLite pattern: ALTER TABLE
		// ADD COLUMN with no DEFAULT is a no-op if the column
		// already exists. We do NOT drop the constraint or reindex
		// existing rows; that would be data-destructive on
		// already-deployed v2.1.0 databases.
		Version: 19,
		Name:    "agent_memory_v230_columns",
		Up: `
ALTER TABLE agent_memory ADD COLUMN agent_id TEXT;
ALTER TABLE agent_memory ADD COLUMN memory_type TEXT;
CREATE INDEX IF NOT EXISTS idx_agent_memory_agent ON agent_memory (project_id, agent_id, archived_at, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_memory_mtype ON agent_memory (project_id, memory_type, archived_at, created_at DESC) WHERE memory_type IS NOT NULL;
`,
	},
	{
		// v20 — projects.default_agent_id (v2.4.1)
		// Closes the agent_id plumbing debt that v2.3.0 introduced:
		// the agent_memory table gained an `agent_id` column (the LLM
		// that owns each memory per Mem0 semantics), and v2.4.0 wired
		// agent_memory into session_start (ContextRecap) and
		// publish_vibe (drift_judge enrichment). v2.4.0 used a
		// project-wide scope, which conflates memories from different
		// agents (LLMs) writing to the same project — exactly the
		// cross-leakage the agent_id column was designed to prevent.
		//
		// v2.4.1 fixes this by:
		//   1. Adding projects.default_agent_id — the project-level
		//      default that session_start / publish_vibe resolve when
		//      the caller doesn't pass an explicit agent_id.
		//   2. Threading agent_id through session_start (optional
		//      input param) and publish_vibe (optional input param).
		//   3. Plumbing agent_id into the VLP integrations: ContextRecap
		//      (listPinnedForVibe, listOpenTodosForVibe) and the
		//      drift_judge enrichment (recallForVibe).
		//
		// Schema:
		//   default_agent_id   TEXT NULL
		//     Mem0 agent_id (LLM identity). Distinct from operator
		//     (human/agent audit identity). Empty = unconfigured =
		//     project-wide scope (backward compatible with v2.4.0).
		//     Set by project_create(default_agent_id=...) at tenant
		//     provisioning; resolved by session_start / publish_vibe.
		//
		// Idempotent: ALTER TABLE ADD COLUMN with no DEFAULT is a
		// no-op if the column already exists (SQLite semantics).
		// No index needed — default_agent_id is read at session_start
		// (low-frequency, single-row lookup by primary key) and never
		// used as a hot query prefix.
		//
		// Postgres parity: see internal/migrate/postgres/ddl.go where
		// the same ALTER TABLE statement is mirrored with the IF NOT
		// EXISTS pattern (Postgres requires it for idempotency).
		Version: 20,
		Name:    "projects_default_agent_id",
		Up: `
ALTER TABLE projects ADD COLUMN default_agent_id TEXT;
`,
	},
	{
		// v21 — active_subagents (v2.8.0-alpha, DARK_MEMORY_V280)
		//
		// Closes the C2 subagent-scope-handoff gap: when a subagent
		// is spawned via mindset_apply(spawn_subagent=true), we need
		// to remember (project_id, operator) -> subagent_id binding
		// so the next agent_memory_save call resolves agent_id to
		// the subagent's uuid instead of the principal's default.
		//
		// Why a separate table (not a column on agent_memory or
		// sessions): the binding is short-lived (TTL default 1h),
		// per-operator (not per-session), and per-subagent (a
		// principal could spawn multiple subagents in parallel). A
		// dedicated table + (project_id, operator, subagent_id)
		// PRIMARY KEY gives us O(1) upsert + targeted clear +
		// sweep-by-TTL semantics. No FTS5 / no triggers.
		//
		// Defense-in-depth against arxiv:2605.08460 inheritance
		// attacks: subagent writes are tagged with subagent_id (an
		// opaque uuid the principal generates, NOT derivable from
		// principal's agent_id). ContextRecap filters by
		// projects.default_agent_id which the subagent does NOT
		// inherit, so poisoned subagent memory can never appear in
		// the principal's pinned recap.
		//
		// Schema:
		//   project_id       TEXT NOT NULL
		//   operator         TEXT NOT NULL
		//   subagent_id      TEXT NOT NULL
		//   parent_agent_id  TEXT NOT NULL
		//   spawned_at       TEXT NOT NULL      (RFC3339Nano)
		//   ttl_seconds      INTEGER NOT NULL DEFAULT 3600
		//
		// id is an AUTOINCREMENT surrogate key so write_audit (INV-1)
		// can reference a single-row id. The composite natural key
		// (project_id, operator, subagent_id) is also UNIQUE so
		// INSERT OR REPLACE refreshes the same row.
		// INDEX (project_id, operator) for GetActiveSubagent lookup.
		//
		// Postgres parity: see internal/migrate/postgres/ddl.go
		// where the same DDL is mirrored with the IF NOT EXISTS
		// pattern (Postgres requires it for idempotency).
		Version: 21,
		Name:    "active_subagents",
		Up: `
CREATE TABLE IF NOT EXISTS active_subagents (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    operator        TEXT NOT NULL,
    subagent_id     TEXT NOT NULL,
    parent_agent_id TEXT NOT NULL,
    spawned_at      TEXT NOT NULL,
    ttl_seconds     INTEGER NOT NULL DEFAULT 3600,
    UNIQUE (project_id, operator, subagent_id)
);
CREATE INDEX IF NOT EXISTS idx_active_subagents_lookup ON active_subagents (project_id, operator);
`,
	},
	{
		// v22 - porter_stemming: rebuild agent_memory_fts with porter
		// unicode61 tokenizer so morphology-equivalent words
		// ("running", "runs", "ran") collapse to the same stem and
		// rank for the same FTS5 query.
		//
		// PR-1 of the v2.9.0 plan (row 160); LOW risk. Backward
		// compat: result SET unchanged; only BM25 ranking differs.
		// Operators on v2.8.0-alpha see different ordering for stem
		// collisions, identical results for unambiguous queries.
		//
		// FTS5 does not support ALTER TABLE on the tokenizer of an
		// existing virtual table; we must DROP + CREATE VIRTUAL
		// TABLE. The source data is preserved (it's in
		// agent_memory which we never touch). After the recreate,
		// INSERT 'rebuild' tells FTS5 to repopulate from the
		// content='agent_memory' external content table.
		//
		// Idempotency: IF EXISTS / IF NOT EXISTS on every DDL.
		// If somehow re-applied (e.g. operator manually unmarks v22
		// from schema_migrations), the migration is a no-op plus
		// a re-index, both safe.
		//
		// Postgres parity: see internal/migrate/postgres/ddl.go
		// for the tsvector + snowball stemmer equivalent (deferred
		// to a follow-up commit; this PR is SQLite-only).
		Version: 22,
		Name:    "porter_stemming",
		Up: `
DROP TABLE IF EXISTS agent_memory_fts;
CREATE VIRTUAL TABLE IF NOT EXISTS agent_memory_fts USING fts5(
    content, title, tags,
    content='agent_memory', content_rowid='id',
    tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS agent_memory_ai AFTER INSERT ON agent_memory BEGIN INSERT INTO agent_memory_fts(rowid, content, title, tags) VALUES (new.id, new.content, COALESCE(new.title,''), COALESCE(new.tags,'')); END;
CREATE TRIGGER IF NOT EXISTS agent_memory_ad AFTER DELETE ON agent_memory BEGIN INSERT INTO agent_memory_fts(agent_memory_fts, rowid, content, title, tags) VALUES('delete', old.id, old.content, COALESCE(old.title,''), COALESCE(old.tags,'')); END;
CREATE TRIGGER IF NOT EXISTS agent_memory_au1 BEFORE UPDATE ON agent_memory BEGIN INSERT INTO agent_memory_fts(agent_memory_fts, rowid, content, title, tags) VALUES('delete', old.id, old.content, COALESCE(old.title,''), COALESCE(old.tags,'')); END;
CREATE TRIGGER IF NOT EXISTS agent_memory_au2 AFTER UPDATE ON agent_memory BEGIN INSERT INTO agent_memory_fts(rowid, content, title, tags) VALUES (new.id, new.content, COALESCE(new.title,''), COALESCE(new.tags,'')); END;
INSERT INTO agent_memory_fts(agent_memory_fts) VALUES('rebuild');
`,
	},
	{
		// v23 - agent_memory_embedding: hybrid retrieval (PR-2 of v2.9.0 plan,
		// agent_memory row 160). Adds an opaque BLOB column to agent_memory
		// that holds the dense vector (little-endian float32, length =
		// embedder.Dim()) for vector / rrf modes.
		//
		// Storage is in-process: text NEVER leaves the operator's machine
		// for the embedding call (only the embedder call's destination
		// receives it; per row 163 "data and cache are local-first" the
		// embedding column itself stays on disk). Per-agent flow:
		//   - Save path: when embedder != None, callers can populate
		//     AgentMemory.Embedding from the embedder.Prerendered
		//     hint (see internal/store/sqlite/store.go:SaveAgentMemory).
		//     Embedding is optional — rows saved with no embedding
		//     are invisible to Mode=vector/rrf but still visible to
		//     Mode=bm25.
		//   - Search path: when Mode=vector/rrf, the Store loads all rows
		//     in scope with embedding IS NOT NULL, computes cosine in
		//     process, and ranks. sqlite-vec / pgvector deferred to
		//     v2.9.1+ per row 160.
		//
		// BLOB (vs TEXT or REAL[]) chosen for portability: SQLite has
		// no native f32 array type, but the engine has been byte
		// stable on BLOB ordering since forever. Decoders use
		// binary.LittleEndian.Uint32 -> math.Float32frombits.
		//
		// Idempotency: ALTER TABLE ADD COLUMN with no DEFAULT is
		// naturally idempotent only across binary-equal columns;
		// applyOne (F37) tolerates the SQLite "duplicate column
		// name" error class, so re-running this migration on a DB
		// that already has the column is safe (treated as warning).
		Version: 23,
		Name:    "agent_memory_embedding",
		Up: `
ALTER TABLE agent_memory ADD COLUMN embedding BLOB;
`,
	},
}
