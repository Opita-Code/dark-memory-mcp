// Package postgres contains the Postgres-flavored DDL for Dark Memory MCP
// migrations v1..v16.
//
// Differences from internal/migrate/sqlite:
//   - INTEGER PRIMARY KEY AUTOINCREMENT → BIGSERIAL (or BIGINT GENERATED ALWAYS AS IDENTITY)
//   - TEXT PRIMARY KEY stays TEXT PRIMARY KEY
//   - ALTER TABLE ADD COLUMN is supported the same way
//   - TIMESTAMP fields use TIMESTAMP WITH TIME ZONE (vs TEXT for SQLite)
//
// The Migration slice here MUST have the same Version+Name as the
// sqlite package's Migrations; the Up SQL differs.
//
// v11 (5A.iii) and v12 (5E.ii) mirror the sqlite migrations. v12 in
// particular rewrites the `sessions` table to the 5-state enum +
// resurrection chain columns + write_audit.session_event.
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
    project_id        TEXT NOT NULL DEFAULT 'default',
    open_spec_id      BIGINT NOT NULL DEFAULT 0
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
		// Mirror of the sqlite migration. The write_audit.project_id
		// column was already added by migration v7; this migration only
		// adds the composite (project_id, session_id) index.
		Version: 10,
		Name:    "audit_project_index",
		Up: `
CREATE INDEX IF NOT EXISTS idx_write_audit_project_session ON write_audit(project_id, session_id);
`,
	},
	{
		// v11 — atomic frames + recall subscriptions (5A.iii).
		// Postgres-flavored variant of the sqlite v11. Same column
		// shape + index strategy; BIGSERIAL for PKs; CHECK constraints
		// identical (Postgres supports them natively). See
		// SCHEMA_v11_v12.md §2.4 for the canonical design.
		Version: 11,
		Name:    "atomic_frames_and_recall_subscriptions",
		Up: `
CREATE TABLE IF NOT EXISTS vibe_frames (
  id              BIGSERIAL PRIMARY KEY,
  project_id      TEXT NOT NULL,
  session_id      TEXT NOT NULL,
  scope_level     TEXT NOT NULL CHECK (scope_level IN ('global','project','session','call')),
  scope_id        TEXT NOT NULL,
  frame_kind      TEXT NOT NULL CHECK (frame_kind IN ('identity','scope','evidence','capabilities','drift','persona')),
  composed_at     TIMESTAMP WITH TIME ZONE NOT NULL,
  expires_at      TIMESTAMP WITH TIME ZONE NOT NULL,
  frame_json      TEXT NOT NULL,
  content_sha256  TEXT NOT NULL,
  last_write_id   BIGINT NOT NULL DEFAULT 0,
  created_at      TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_scope_lookup
  ON vibe_frames (session_id, scope_level, scope_id, frame_kind, expires_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_project
  ON vibe_frames (project_id, frame_kind, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_invalidation
  ON vibe_frames (session_id, last_write_id);

CREATE TABLE IF NOT EXISTS vibe_recall_subscriptions (
  id               BIGSERIAL PRIMARY KEY,
  project_id       TEXT NOT NULL,
  session_id       TEXT NOT NULL,
  scope_level      TEXT NOT NULL CHECK (scope_level IN ('global','project','session','call')),
  scope_id         TEXT NOT NULL,
  last_seen_token  BIGINT NOT NULL DEFAULT 0,
  created_at       TIMESTAMP WITH TIME ZONE NOT NULL,
  updated_at       TIMESTAMP WITH TIME ZONE NOT NULL,
  UNIQUE (session_id, scope_level, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_recall_subs_lookup
  ON vibe_recall_subscriptions (session_id, scope_level, scope_id);

CREATE INDEX IF NOT EXISTS idx_recall_subs_project
  ON vibe_recall_subscriptions (project_id, scope_level);
`,
	},
	{
		// v12 — session lifecycle overhaul (Wave 5E.ii). Postgres-flavored
		// mirror of the sqlite v12. Same column shape, same CHECK constraints
		// (Postgres supports them natively), same index list. BIGSERIAL PKs
		// and TIMESTAMP WITH TIME ZONE for time fields. See SCHEMA_v11_v12.md
		// §3 for the canonical design.
		Version: 12,
		Name:    "session_lifecycle_overhaul",
		Up: `
ALTER TABLE sessions RENAME TO _sessions_old;

CREATE TABLE sessions (
  id                  BIGSERIAL PRIMARY KEY,
  session_id          TEXT    NOT NULL UNIQUE,
  status              TEXT    NOT NULL DEFAULT 'open'
                      CHECK (status IN ('open','idle','closed_clean','closed_aborted','archived')),
  constitution_id     TEXT,
  constitution_ver    TEXT,
  active_mods         TEXT,
  operator            TEXT    NOT NULL,
  started_at          TIMESTAMP WITH TIME ZONE NOT NULL,
  closed_at           TIMESTAMP WITH TIME ZONE,
  last_heartbeat_at   TIMESTAMP WITH TIME ZONE,
  parent_session_id   TEXT,
  resurrected_from    TEXT,
  notes               TEXT,
  project_id          TEXT    NOT NULL DEFAULT 'default',
  created_at          TIMESTAMP WITH TIME ZONE NOT NULL,
  CHECK (
    (status IN ('closed_clean','archived') AND closed_at IS NOT NULL)
    OR (status = 'open' AND closed_at IS NULL)
    OR (status IN ('idle','closed_aborted'))
  )
);

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
`,
	},
	{
		// v13 — vibe_frames unique natural key (5A.ii.a polish).
		// Mirror of sqlite v13. See sqlite/ddl.go for the full
		// rationale (true UPSERT to fix the SELECT-then-INSERT/UPDATE
		// race that the pivot drift judge flagged at 0.55).
		Version: 13,
		Name:    "vibe_frames_unique_natural_key",
		Up: `
CREATE UNIQUE INDEX IF NOT EXISTS uq_vibe_frames_natural_key
  ON vibe_frames (project_id, session_id, scope_level, scope_id, frame_kind);
`,
	},
	{
		// v14 — projects.drift_strictness (5X.3).
		// Mirror of sqlite v14. Adds drift_strictness column to
		// projects table for per-project override of the drift-at-write
		// interceptor (5A.vi M6). See sqlite/ddl.go for full rationale.
		Version: 14,
		Name:    "projects_drift_strictness",
		Up: `
ALTER TABLE projects ADD COLUMN drift_strictness TEXT NOT NULL DEFAULT 'default';
`,
	},
	{
		// v15 — vlp_state.open_spec_id (5X.4). Mirror of sqlite v15.
		// See sqlite/ddl.go for full rationale.
		Version: 15,
		Name:    "vlp_state_open_spec_id",
		Up: `
ALTER TABLE vlp_state ADD COLUMN open_spec_id BIGINT NOT NULL DEFAULT 0;
`,
	},
	{
		// v16 — constitution watchdog audit columns (5E.iv follow-up).
		// Mirror of sqlite v16. See sqlite/ddl.go for full rationale.
		Version: 16,
		Name:    "constitution_watchdog_audit",
		Up: `
ALTER TABLE constitutions ADD COLUMN last_verified_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE constitutions ADD COLUMN last_verified_sha256 TEXT;
`,
	},
	{
		// v17 - projects.active_session_id + active_session_set_at
		// (v2.0.2 gate fix). See sqlite/ddl.go v17 for full rationale.
		// Postgres differs from SQLite only in the timestamp type:
		// TIMESTAMP WITH TIME ZONE here, plain TEXT in sqlite.
		Version: 17,
		Name:    "projects_active_session",
		Up: `
ALTER TABLE projects ADD COLUMN active_session_id TEXT;
ALTER TABLE projects ADD COLUMN active_session_set_at TIMESTAMP WITH TIME ZONE;
`,
	},
	{
		// v18 - agent_memory table (v2.1.0).
		//
		// Mirror of sqlite v18 EXCEPT the FTS5 mirror (Postgres has
		// no native FTS5; an equivalent would use pg_trgm + tsvector
		// or a separate tsvector column + GIN index). For v2.1.0 the
		// Postgres driver is unconfigured on this host (see
		// internal/store/postgres/store.go stubs), so we mirror just
		// the row table here. When the Postgres driver is wired up
		// (post-v2.1.0), v19 should add the FTS-equivalent (likely a
		// tsvector column + GIN index on it, with a trigger to keep
		// it in sync, plus to_tsquery in SearchAgentMemory).
		Version: 18,
		Name:    "agent_memory_and_fts5",
		Up: `
CREATE TABLE IF NOT EXISTS agent_memory (
    id          BIGSERIAL PRIMARY KEY,
    project_id  TEXT NOT NULL,
    session_id  TEXT,
    operator    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    title       TEXT,
    content     TEXT NOT NULL,
    tags        TEXT,
    pinned      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at  TIMESTAMP WITH TIME ZONE NOT NULL,
    archived_at TIMESTAMP WITH TIME ZONE,
    expires_at  TIMESTAMP WITH TIME ZONE
);
CREATE INDEX IF NOT EXISTS idx_agent_memory_proj ON agent_memory (project_id, archived_at, pinned DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_memory_sess ON agent_memory (session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_memory_op   ON agent_memory (operator, archived_at, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_memory_kind ON agent_memory (project_id, kind, archived_at);
`,
	},
	{
		// v20 — projects.default_agent_id (v2.4.1).
		// Mirror of sqlite v20. See internal/migrate/sqlite/ddl.go for
		// the full rationale. Postgres requires IF NOT EXISTS on
		// ADD COLUMN for idempotency; SQLite is idempotent without it
		// (we keep the explicit IF NOT EXISTS for parity).
		Version: 20,
		Name:    "projects_default_agent_id",
		Up: `
ALTER TABLE projects ADD COLUMN IF NOT EXISTS default_agent_id TEXT;
`,
	},
	{
		// v21 — active_subagents (v2.8.0-alpha).
		// Mirror of sqlite v21. Postgres parity with IF NOT EXISTS
		// on all DDL. See internal/migrate/sqlite/ddl.go for the
		// full rationale (C2 subagent-scope-handoff + defense
		// against arxiv:2605.08460 inheritance attacks).
		Version: 21,
		Name:    "active_subagents",
		Up: `
CREATE TABLE IF NOT EXISTS active_subagents (
    project_id       TEXT NOT NULL,
    operator         TEXT NOT NULL,
    subagent_id      TEXT NOT NULL,
    parent_agent_id  TEXT NOT NULL,
    spawned_at       TIMESTAMP WITH TIME ZONE NOT NULL,
    ttl_seconds      INTEGER NOT NULL DEFAULT 3600,
    PRIMARY KEY (project_id, operator, subagent_id)
);
CREATE INDEX IF NOT EXISTS idx_active_subagents_lookup ON active_subagents (project_id, operator);
`,
	},
	{
		// v23 — agent_memory_embedding (PR-2 of v2.9.0 plan).
		// Mirror of sqlite v23; BYTEA on Postgres vs BLOB on SQLite.
		// Encoding: little-endian float32, length = embedder.Dim().
		// See internal/store/sqlite/vector.go for encode/decode (used
		// by both drivers via the shared serialize helper).
		//
		// Postgres porter stemming (v22 in sqlite) was explicitly
		// deferred from PR-12 to a follow-up that wires the
		// tsvector + snowball stemmer equivalent; not addressed here.
		Version: 23,
		Name:    "agent_memory_embedding",
		Up: `
ALTER TABLE agent_memory ADD COLUMN IF NOT EXISTS embedding BYTEA;
`,
	},
	{
		// v24 - agent_memory_entities (PR-3 of v2.9.0 plan).
		// Mirror of sqlite v24. Postgres parity: same column
		// names + types except REAL → DOUBLE PRECISION for the
		// confidence column (Postgres convention; SQLite REAL is
		// already 8-byte IEEE-754 so the values are 1:1).
		//
		// Composite primary key (mem_id, entity) cascades on
		// agent_memory delete (mirrors sqlite). Indexes on entity
		// (search filter) and mem_id (entity-list per row).
		//
		// Idempotent via IF NOT EXISTS on table + indexes.
		Version: 24,
		Name:    "agent_memory_entities",
		Up: `
CREATE TABLE IF NOT EXISTS agent_memory_entities (
    mem_id     BIGINT NOT NULL REFERENCES agent_memory(id) ON DELETE CASCADE,
    entity     TEXT   NOT NULL,
    source     TEXT   NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    model      TEXT,
    created_at TEXT   NOT NULL,
    PRIMARY KEY (mem_id, entity)
);
CREATE INDEX IF NOT EXISTS idx_agent_memory_entities_entity  ON agent_memory_entities (entity);
CREATE INDEX IF NOT EXISTS idx_agent_memory_entities_mem_id ON agent_memory_entities (mem_id);
`,
	},
	{
		// v25 - error_events: Error Observatory (spec 757, Wave 5D).
		// Mirrors the sqlite v25 migration (BIGSERIAL PK, same columns,
		// same indexes). See internal/migrate/sqlite/ddl.go v25 for the
		// full design rationale.
		Version: 25,
		Name:    "error_events",
		Up: `
CREATE TABLE IF NOT EXISTS error_events (
    id               BIGSERIAL PRIMARY KEY,
    project_id       TEXT NOT NULL,
    session_id       TEXT,
    tool_name        TEXT,
    domain           TEXT NOT NULL,
    code             TEXT NOT NULL,
    message          TEXT NOT NULL,
    message_hash     TEXT NOT NULL,
    context_json     TEXT,
    severity         TEXT NOT NULL DEFAULT 'error',
    count            INTEGER NOT NULL DEFAULT 1,
    first_seen_at    TEXT NOT NULL,
    last_seen_at     TEXT NOT NULL,
    resolved         BOOLEAN NOT NULL DEFAULT FALSE,
    resolved_at      TEXT,
    resolution_note  TEXT,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_error_events_last_seen  ON error_events (last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_error_events_domain     ON error_events (domain);
CREATE INDEX IF NOT EXISTS idx_error_events_severity   ON error_events (severity);
CREATE INDEX IF NOT EXISTS idx_error_events_resolved   ON error_events (resolved);
CREATE INDEX IF NOT EXISTS idx_error_events_dedup      ON error_events (domain, code, message_hash, tool_name, session_id, resolved);
`,
	},
	{
// v27 — Merkle chain for vibe_drift_reports (spec 1276, T04).
		// Mirror of sqlite v27. Postgres parity with IF NOT EXISTS
		// so a pre-existing column on legacy DBs doesn't fail.
		Version: 27,
		Name:    "drift_reports_merkle_root",
		Up: `
ALTER TABLE vibe_drift_reports ADD COLUMN IF NOT EXISTS merkle_root TEXT;
`,
	},
	{
		// v28 — projects.nli_config_json (spec 1276, T07).
		// Mirror of sqlite v28. Nullable TEXT column for per-project
		// NLI config (JSON-encoded project.NLIConfig). See sqlite
		// migration v28 for the rationale and reading semantics.
		Version: 28,
		Name:    "projects_nli_config",
		Up: `
ALTER TABLE projects ADD COLUMN IF NOT EXISTS nli_config_json TEXT;
`,
	},
}
