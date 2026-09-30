# SPEC — alpha.11 Phase 4 — BUG-10 10b Namespace Primitive (HARD)

| Field | Value |
|---|---|
| **Vibe-loop** | `alpha-11-phase-4` |
| **Vibe-case** | C1 (code) |
| **Author** | Opita-AI (MiniMax-M3), 2026-09-30 |
| **Operator approval** | received 2026-09-30 ("ATAQUEMOS ENOTNCES Phase 4") |
| **Target alpha** | v4.0.0-alpha.17 |
| **Branch** | `feat/v4-redesign` |
| **Plan ref** | `docs/v4-alpha-11-plan.md:186-218` |
| **ADR refs** | ADR-025 (Namespace Primitive threat model, NEW) |
| **SOTA source** | `docs/sota-critique.md:714-799` (§7.6.9 namespace primitive threat model) |

**Plan acceptance target vs actual**: The plan §4 calls for "4 new tools registered: mindset_apply, delegate_intent, project_create, project_lookup (38 → 42 tools)". The current v4 alpha.16 MVP surface has 6 tools (health + session*3 + memory*2). After Phase 4 ships, surface grows **6 → 10 tools** (project_create + project_lookup + mindset_apply + delegate_intent). v3.0.0-docfix canonical has all 4 in its 57-tool surface already (PROJECT/2 + MINDSET/1 + DELEGATION/1 = 4) but the **v4 binary's transport layer does NOT implement them yet** — this is the gap Phase 4 closes.

---

## 1. TL;DR

Three independent items in one vibe-loop workstream (split into 3 chunks for execution):

- **§3 Schema migration** — new `projects` table (namespace registry, NOT multi-tenant) + `project_id TEXT` column on 5 existing tables (`agent_memory`, `audit_log`, `sdd_evaluations`, `vibe_specs`, `vibe_artifacts`) + 5 indexes. S complexity, ~150 LoC. **[CHUNK 4.1 — THIS SESSION]**
- **§4 4 new MCP tools** — `project_create` + `project_lookup` (PROJECT namespace, mirrors v3.0.0-docfix); `mindset_apply` (MINDSET namespace, compose + judge-validate subagent prompt); `delegate_intent` (DELEGATION namespace, DECIDE→PLAN→MIND→CURATE). L complexity, ~300 LoC. **[CHUNK 4.2 — NEXT SESSION]**
- **§5 Hard isolation enforcement** — write_audit surfaces `project_id`; drift_judge scopes by project_id; session_start validates project_id exists in `projects` table. S complexity, ~80 LoC. **[CHUNK 4.3 — LATER SESSION]**

Total: ~530 LoC, ~12-18 days calendar across 3 sub-chunks. Each sub-chunk ships as a separate atomic mirror + commit.

**This session ships Chunk 4.1 only** (schema migration + projects table). The vibe_publish below covers all of Phase 4 but commits only Chunk 4.1 code.

---

## 2. Why Phase 4 now

### 2.1 The namespace primitive threat model (the reframe)

Per `docs/sota-critique.md:714-799` (§7.6.9), the operator's question "qué interpretas por multitentant para un MCP" surfaced a critical confusion between **HARD multi-tenant isolation** and **SOFT workstream namespacing**:

- **HARD isolation primitive** = `coexistence_group` (per-MCP `dark.db`). This IS the security boundary. dark-memory's `dark.db` is physically separate from dark-research's `dark-research.db`. Rows in one do not appear in the other.
- **SOFT separation primitive** = `project_id` column. This is a FILTER COLUMN, not a security boundary. Useful for the operator to scope their workstreams (opita-market, dark-memory-mcp, Pasiones) within one MCP instance.

The threat model statement (canonical, for `docs/v4-status.md`):

> v4 assumes the harness session is the only concurrent consumer. Project IDs scope workstreams within one operator. For HARD isolation between concurrent users, use separate `coexistence_group`s or separate MCP instances. The `project_id` column is a soft namespace, not a security boundary.

Without this reframe, future contributors may treat `project_id` as a security boundary, write code that assumes isolation, and ship a vulnerability. Naming the primitive "namespace" instead of "multi-tenant" makes the soft nature obvious in the type name.

### 2.2 The current state of the v4 alpha.16

The v4 alpha.16 binary (`cmd/dark-memory-v4/dark-memory-v4.exe`) has:

- **6 MVP tools**: `health` + `session_start`/`session_resume`/`session_close` + `agent_memory_save`/`agent_memory_recall`. See `cmd/dark-memory-v4/serve.go:81`.
- **9 schema tables**: `audit_log`, `sessions`, `agent_memory`, `capabilities`, `manifest`, `vibe_specs`, `vibe_artifacts`, `vibe_drifts`, `sdd_evaluations`.
- **1 table already has `project_id`**: `sessions` (added during Phase 1 PRE-1 C3, `internal/v4alpha/session/session.go:98`).
- **6 tables need `project_id`**: `agent_memory`, `audit_log`, `sdd_evaluations`, `vibe_specs`, `vibe_artifacts`, `vibe_drifts`.
- **0 PROJECT/MINDSET/DELEGATION tools in transport layer**: the v4 binary does NOT wire `project_create`, `project_lookup`, `mindset_apply`, or `delegate_intent`.

This means:
- All `agent_memory_recall` calls today return rows from ALL projects (no filter). Cross-workstream bleed is the default.
- All `vibe_publish` calls today cannot be scoped to one project (the spec/artifact row has no `project_id`).
- All `audit_log` rows today have `actor` (operator) but no `project_id`. INV-1 audit is operator-scoped but not project-scoped.

### 2.3 What the operator said

> "ATAQUEMOS ENOTNCES Phase 4" (2026-09-30)

Phase 4 of the alpha.11+ plan is next. This is the BUG-10 10b surface expansion that the original chunk-7 spec (`docs/specs/SPEC-alpha-11-chunk7.md`) marked as "L, ~500 LoC, MEDIUM risk".

---

## 3. §A — Schema migration (projects table + project_id column on 5 tables)

### 3.1 The `projects` table (namespace registry, NOT multi-tenant)

```sql
CREATE TABLE IF NOT EXISTS projects (
    project_id       TEXT    PRIMARY KEY CHECK (project_id <> ''
                                                  AND project_id = lower(project_id)
                                                  AND length(project_id) >= 3
                                                  AND length(project_id) <= 64
                                                  AND project_id NOT IN ('default', 'dark')),
    display_name     TEXT    NOT NULL CHECK (display_name <> ''),
    description      TEXT    NOT NULL DEFAULT '',
    default_agent_id TEXT,                  -- Mem0 agent_id (LLM identity)
    created_at       TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at      TEXT,                  -- soft delete (NULL = active)
    CHECK (archived_at IS NULL OR archived_at >= created_at)
);

-- Reserved ids that operators cannot create (enforced in app layer too):
--   'default' — seeded on Open, the catch-all workstream
--   'dark'    — reserved for internal use (e.g., system writes)
```

**Differences from v3.0.0-docfix `internal/project/types.go:20-100`**: v3 has 13 fields (NLIConfig JSON, DriftStrictness, ActiveSessionID, etc.). v4 ships 7 fields (project_id, display_name, description, default_agent_id, created_at, archived_at, plus reserved-id check). NLIConfig and DriftStrictness are deferred to Phase 5 (memory subsystem) or alpha.18.

**Why reserved ids `default` and `dark`**:
- `default` is the seeded catch-all (per v3 plan; existing 164 specs live in `default`).
- `dark` is reserved for internal-use writes (e.g., the bootstrap migration that backfills `project_id='default'` on existing rows). Cannot be operator-created.

### 3.2 The `project_id` column on 5 tables

For each of these 5 tables, add `project_id TEXT NOT NULL DEFAULT 'default' CHECK (project_id <> '')`:

| Table | Current columns | New column | New index |
|---|---|---|---|
| `agent_memory` | id, operator, kind, title, content, tags, pinned, created_at, updated_at | `project_id TEXT NOT NULL DEFAULT 'default' CHECK (project_id <> '')` | `agent_memory_project_idx ON agent_memory(project_id)` |
| `audit_log` | audit_id, actor, session_id, payload, created_at, prev_hash, row_hash | `project_id TEXT NOT NULL DEFAULT 'default' CHECK (project_id <> '')` | `audit_log_project_idx ON audit_log(project_id)` |
| `sdd_evaluations` | eval_id, eval_type, target_type, target_id, verdict, confidence, reasoning, evaluated_at, [+4 calibration cols from Phase 3] | `project_id TEXT NOT NULL DEFAULT 'default' CHECK (project_id <> '')` | `sdd_eval_project_target_idx ON sdd_evaluations(project_id, target_type, target_id)` |
| `vibe_specs` | id, vibe_case, intent, tasks_json, created_at | `project_id TEXT NOT NULL DEFAULT 'default' CHECK (project_id <> '')` | `vibe_specs_project_idx ON vibe_specs(project_id)` |
| `vibe_artifacts` | id, spec_id, artifact_type, artifact_url, text, ref_*, created_at | `project_id TEXT NOT NULL DEFAULT 'default' CHECK (project_id <> '')` | `vibe_artifact_project_idx ON vibe_artifacts(project_id)` |

**Why NOT `vibe_drifts`**: drifts are inherently tied to their artifact_id (`internal/v4alpha/vibe/drift.go:68-82`), which carries `project_id` via JOIN to `vibe_artifacts`. No new column needed; queries that filter by project use the artifact JOIN. The plan's "5 tables" matches this (sessions is excluded because it already has `project_id` from Phase 1).

### 3.3 The migration helper `ApplyProjectIDColumns`

Mirror the Phase 2 pattern from `internal/v4alpha/audit/writer.go:118-150` (ApplyChainColumns):

```go
// ApplyProjectIDColumns idempotently adds project_id to the 5 tables.
// Each ALTER TABLE ADD COLUMN is O(1) in SQLite (only schema B-tree
// is modified, not data). DEFAULT 'default' backfills existing rows.
// Idempotent: uses pragma_table_info to skip if column already exists.
func ApplyProjectIDColumns(ctx context.Context, db *sql.DB) error {
    tables := []string{
        "agent_memory", "audit_log", "sdd_evaluations",
        "vibe_specs", "vibe_artifacts",
    }
    for _, t := range tables {
        // pragma_table_info check (skip if exists)
        // ALTER TABLE ADD COLUMN project_id TEXT NOT NULL DEFAULT 'default'
        // CREATE INDEX IF NOT EXISTS <t>_project_idx ...
    }
    return nil
}
```

### 3.4 Package layout

NEW: `internal/v4alpha/project/project.go` (~150 LoC):

```go
package project

type Project struct {
    ProjectID       string
    DisplayName     string
    Description     string
    DefaultAgentID  string
    CreatedAt       time.Time
    ArchivedAt      *time.Time  // nil = active
}

type Store struct {
    db    *sql.DB
    audit *audit.Writer
}

func CreateSchema(db *sql.DB) error
func NewStore(db *sql.DB, w *audit.Writer) *Store
func (s *Store) Create(ctx context.Context, p *Project) error  // idempotent on project_id
func (s *Store) Lookup(ctx context.Context, projectID string) (*Project, error)
func (s *Store) Archive(ctx context.Context, projectID string) error  // soft delete
func (s *Store) List(ctx context.Context, includeArchived bool) ([]*Project, error)

var ErrProjectNotFound = errors.New("project: not found")
var ErrReservedProjectID = errors.New("project: project_id is reserved ('default', 'dark')")
var ErrInvalidProjectID = errors.New("project: project_id must be kebab-case, 3-64 chars, lowercase alnum + hyphen")
```

**Backfill semantics**: `CreateSchema` for `projects` seeds one row `(project_id='default', display_name='Default')` via `INSERT OR IGNORE`. All 5 tables backfill `project_id='default'` via the `DEFAULT 'default'` clause in `ApplyProjectIDColumns`. Existing rows are preserved.

### 3.5 Tests (5-7 unit tests)

- `TestCreateSchema_Idempotent` — call CreateSchema twice; second call is no-op.
- `TestStore_Create_DefaultSeed` — `Lookup("default")` succeeds without prior `Create` call.
- `TestStore_Create_ReservedID_Rejected` — `Create(project_id="dark")` returns `ErrReservedProjectID`.
- `TestStore_Create_Idempotent` — `Create(project_id="opita-market")` then replay returns the same row (no error, no overwrite of mutable fields).
- `TestStore_Create_InvalidProjectID` — `Create(project_id="OPITA-MARKET")` returns `ErrInvalidProjectID` (uppercase).
- `TestStore_Archive_SetsArchivedAt` — `Archive("opita-market")` then `Lookup` returns `ArchivedAt != nil`.
- `TestApplyProjectIDColumns_Idempotent` — call twice; second call no-op (pragma_table_info check works).

---

## 4. §B — 4 new MCP tools (Chunk 4.2, NEXT SESSION)

### 4.1 PROJECT namespace (mirrors v3.0.0-docfix)

#### 4.1.1 `dark_memory_project_create`

Mirrors `internal/tools/project.go` (v3). Per v3 spec: idempotent on `project_id`; sets `default_agent_id`; seeds `display_name` + `description`. Emits an INV-1 audit row.

```go
// dark_memory_project_create(
//   project_id: string,             // kebab-case, 3-64 chars
//   display_name: string,           // 1-128 chars
//   description: string = "",       // ≤512 chars
//   default_agent_id: string = "",  // Mem0 agent_id (≤128 chars)
//   constitution_id: string = "",   // optional, INV-7 binding
//   constitution_ver: string = "",  // paired with constitution_id
//   nli_config: NLIConfig = null,   // optional, JSON blob
// ) -> Project
```

Reserved ids `default` and `dark` are rejected (the `default` row is seeded on Open, never re-created; `dark` is reserved). Errors: `ErrAlreadyExists` (NOT — idempotent replay returns the existing row); `ErrReservedProjectID`; `ErrInvalidProjectID`.

#### 4.1.2 `dark_memory_project_lookup`

```go
// dark_memory_project_lookup(project_id: string) -> Project
// Returns ErrProjectNotFound if not found.
```

### 4.2 MINDSET namespace

#### 4.2.1 `dark_memory_mindset_apply`

Mirrors v3 MINDSET/1 (`dark_memory_mindset_apply`). Per system prompt: "Procedurally compose a subagent system_prompt for the given (vibe_case, task_description) and validate it via LLM-as-judge before returning. Cached in agent_memory (TTL via DARK_MINDSET_CACHE_TTL, default 1h). Composition iterates up to DARK_MINDSET_MAX_ITERATIONS (default 3) if the judge rejects the first attempt."

```go
// dark_memory_mindset_apply(
//   vibe_case: string,           // C1..C7
//   task_description: string,    // ≥10 chars
//   operator: string = "orchestrator_mindset",  // INV-1 audit
//   model_floor: string = "",    // sonnet|opus|haiku|inherit
// ) -> {
//   system_prompt: string,
//   tools_recommended: [string],
//   model_recommended: string,
//   cache_hit: bool,
//   iterations: int,
// }
```

Implementation: v4 has no live LLM call yet (judge package's `RealLLMClient` exists but isn't wired into a v4 MCP tool). For v4 MVP, `mindset_apply` returns a STUB system_prompt + the cached result if any, WITHOUT calling LLM. The judge-validate iteration loop is deferred to alpha.18 (when v4 LLM client is wired).

### 4.3 DELEGATION namespace

#### 4.3.1 `dark_memory_delegate_intent`

Mirrors v3 DELEGATION/1 (`dark_memory_delegate_intent`). Per system prompt: "Wave 5C: Decide whether an intent is handled inline, delegated to sub-agents, or refused (A1: Memory decides). Runs the DelegationRouter pipeline: DECIDE (deterministic rules per vibe_case + bounded LLM choice) → PLAN (subtask graph with dependency batches) → MIND (mindset_apply per subtask: system_prompt + tools + model) → CURATE (agent_memory_delegate per subtask: curated agent_memory context + C2 subagent binding)."

```go
// dark_memory_delegate_intent(
//   task_description: string,    // ≥10 chars
//   vibe_case: string,           // C1..C7
//   operator: string = "<active-session-operator>",
// ) -> {
//   decision: "inline"|"delegate"|"refused",
//   subtasks: [{
//     id: string,
//     system_prompt: string,
//     tools: [string],
//     model: string,
//     delegation_context: string,
//   }],
// }
```

Implementation: v4 MVP returns a STUB decision (`{"decision": "inline", "subtasks": []}`) for all intents. The DECIDE→PLAN→MIND→CURATE pipeline is deferred to alpha.18 (same gating as mindset_apply).

### 4.4 Transport wiring

NEW: `internal/v4alpha/transport/mcp/project.go` (~80 LoC)
NEW: `internal/v4alpha/transport/mcp/mindset.go` (~60 LoC)
NEW: `internal/v4alpha/transport/mcp/delegation.go` (~40 LoC)

Wire into `internal/v4alpha/transport/mcp/server.go` (existing dispatcher). Update `cmd/dark-memory-v4/serve.go:81` note to "MVP tool set: ... + project*2 + mindset*1 + delegation*1 (10 tools)".

### 4.5 Tests (5-7 unit tests)

- `TestProjectCreate_Idempotent_DefaultSeeded` — `project_create("default", ...)` returns ErrReservedProjectID (cannot override).
- `TestProjectLookup_NotFound` — `project_lookup("nonexistent")` returns ErrProjectNotFound.
- `TestMindsetApply_StubReturns` — `mindset_apply("C1", "test task", "operator")` returns a stub system_prompt (≥100 chars) without LLM call.
- `TestDelegateIntent_StubReturnsInline` — `delegate_intent("test task", "C1")` returns `{"decision": "inline"}`.
- `TestTransportWiring_4NewToolsRegistered` — call `mcpSrv.ListTools()` (or equivalent), assert `project_create`, `project_lookup`, `mindset_apply`, `delegate_intent` present.

---

## 5. §C — Hard isolation enforcement (Chunk 4.3, LATER SESSION)

### 5.1 write_audit surfaces `project_id`

Update `internal/v4alpha/audit/writer.go` `Write` to accept `projectID` and stamp the new `audit_log.project_id` column. INV-1 audit gains project scope: "every Save emits exactly one row tagged with the actor AND project".

```go
func (w *Writer) Write(ctx context.Context, actor, sessionID, projectID string, payload []byte) (int64, error)
```

Existing callers (`session.Create`, `agent_memory.Save`, etc.) pass `projectID` from the session context.

### 5.2 drift_judge scopes by `project_id`

Update `internal/v4alpha/transport/mcp/judge.go` `populateCalibration` to filter historical confidences by `project_id` (not just `(provider, target_type, eval_type)`). This prevents cross-project leakage of calibration baselines.

```go
// Before:
historical := s.store.ConfidencesByProviderTarget(ctx, provider, targetType, evalType)
// After:
historical := s.store.ConfidencesByProjectProviderTarget(ctx, projectID, provider, targetType, evalType)
```

### 5.3 session_start validates `project_id` exists

Update `internal/v4alpha/session/session.go` `Start` to lookup the `project_id` in `projects` table before INSERT into `sessions`. Returns `ErrProjectNotFound` if not found (closes the gap where Phase 1 PRE-1 C3 added `project_id` column but did NOT enforce existence at session start).

```go
// Before:
// INSERT INTO sessions (id, operator, project_id, ...) VALUES (?, ?, ?, ...)
// After:
// projectExists := projects.Lookup(ctx, projectID)  // returns ErrProjectNotFound if missing
// INSERT INTO sessions ...
```

### 5.4 Tests (3-5 unit tests)

- `TestAuditWrite_ProjectIDSurfaced` — call `Write(actor, sessionID, "opita-market", payload)`; query `audit_log`; assert `project_id="opita-market"`.
- `TestDriftJudge_CalibrationFilteredByProject` — populate 100 rows in project A (avg confidence 0.7), 100 rows in project B (avg confidence 0.5); call `populateCalibration(project="A", ...)`; assert the CI reflects project A's data only.
- `TestSessionStart_UnknownProject_Rejected` — `session_start(operator, projectID="nonexistent")` returns `ErrProjectNotFound`.

---

## 6. Compatibility

### 6.1 What this phase DOES break

- **`vibe_specs` table gains `project_id`**: all `SpecStore.Get/Insert` calls must handle the new column. `vibe/spec_store.go:46-94` needs a 1-line change: `INSERT INTO vibe_specs (vibe_case, intent, tasks_json, project_id) VALUES (?, ?, ?, ?)`. **Backward compatible**: existing rows have `project_id='default'` via the DEFAULT clause.
- **`vibe_artifacts` table gains `project_id`**: `Artifact.Insert` (`vibe/artifact.go`) accepts a `project_id` from the spec (or operator override). **Backward compatible**: same DEFAULT clause.
- **`sdd_evaluations` table gains `project_id`**: `judge.SaveEvaluation` (`judge/store.go:151`) accepts a `project_id` from the calling context. **Backward compatible**.
- **`audit_log` table gains `project_id`**: every `audit.Writer.Write` call now requires `projectID`. Callers in `session.Create`, `agent_memory.Save`, `project.Store.Create` (from §4.1.1) must thread projectID through. **Backward compatible at the data layer** (DEFAULT clause), but **code-level breaking change** for any caller that doesn't pass it (compile error).
- **`agent_memory` table gains `project_id`**: `Recall` queries default to `project_id='default'` unless caller passes `scope='project'` and a project_id. **Behavioral change**: today's `Recall` returns rows from ALL projects; after Phase 4, it defaults to `'default'` only.

### 6.2 What this phase DOES NOT break

- **The 6 existing MVP tools** (health + session*3 + memory*2) keep their surface. `agent_memory_recall` gains an optional `scope` parameter (default `'default'`).
- **The v3.0.0-docfix binary** (separate from v4). It already has PROJECT/MINDSET/DELEGATION; not affected.
- **The judge pipeline** (Phase 3 calibration, calibration.go, etc.). The 4 calibration columns stay; `project_id` is ADDED (5th new column, total 22+1=23 cols).
- **The audit hash chain** (Phase 2). `prev_hash` and `row_hash` semantics unchanged; `project_id` is just another column in the row.
- **The `projects` table seed**: `default` is created on Open; `dark` is reserved but not seeded.

### 6.3 Migration risk

- **ALTER TABLE ADD COLUMN with DEFAULT**: O(1) in SQLite (only schema B-tree modified, not data). All 5 tables backfill to `'default'` instantly.
- **Backfill for existing rows**: zero manual work — the DEFAULT clause handles it.
- **For pre-Phase-4 databases**: `ApplyProjectIDColumns` is idempotent and detects pre-existing columns via `pragma_table_info`. Safe to run on an already-initialized DB.

---

## 7. Acceptance criteria

### 7.1 Chunk 4.1 (THIS SESSION)

- [x] SPEC-alpha-11-phase4.md exists at `docs/specs/SPEC-alpha-11-phase4.md` (~400 LoC).
- [ ] NEW `internal/v4alpha/project/` package: `project.go` + `project_store.go` + `project_store_test.go`.
- [ ] `CreateSchema` creates `projects` table (idempotent, seeds `'default'` row).
- [ ] `ApplyProjectIDColumns(ctx, db)` adds `project_id` column to 5 tables + 5 indexes (idempotent).
- [ ] `Store.Create`, `Lookup`, `Archive`, `List` operations on projects.
- [ ] Reserved ids (`default`, `dark`) rejected by `Create` (returns `ErrReservedProjectID`).
- [ ] Invalid project_ids (uppercase, special chars, length out of bounds) rejected (returns `ErrInvalidProjectID`).
- [ ] Wire `ApplyProjectIDColumns` into `serve.go:applyAllSchemas`.
- [ ] All 7 project tests pass (`TestCreateSchema_Idempotent` through `TestApplyProjectIDColumns_Idempotent`).
- [ ] All 11 v4alpha packages still pass (no existing test breaks).
- [ ] `go vet ./...` clean.
- [ ] Commit + atomic mirror (1 SUMMARY + 5 SECTION pinned=false, agent_id=`alpha-11-phase4`).
- [ ] Drift check ALIGNED ≥0.85 on the spec.

### 7.2 Chunk 4.2 (NEXT SESSION)

- [ ] NEW `internal/v4alpha/transport/mcp/project.go` — `project_create`, `project_lookup`.
- [ ] NEW `internal/v4alpha/transport/mcp/mindset.go` — `mindset_apply` (stub).
- [ ] NEW `internal/v4alpha/transport/mcp/delegation.go` — `delegate_intent` (stub).
- [ ] Wire into `serve.go` (note "10 tools").
- [ ] 5 new transport tests pass.
- [ ] All 11 v4alpha packages pass.
- [ ] Commit + atomic mirror.

### 7.3 Chunk 4.3 (LATER)

- [ ] `audit.Writer.Write` accepts `projectID`; threads through callers.
- [ ] `judge.populateCalibration` filters by `project_id` (not just provider/target).
- [ ] `session.Store.Start` validates `project_id` exists (returns `ErrProjectNotFound`).
- [ ] 3-5 new tests pass.
- [ ] Commit + atomic mirror.

### 7.4 Full phase-4 acceptance (after all 3 chunks)

- [ ] 6 → 10 MCP tools wired.
- [ ] `projects` table exists + seeded with `'default'`.
- [ ] `project_id` column on 5 tables + 5 indexes.
- [ ] `audit_log` rows have `project_id` (INV-1 + INV-7 audit strengthened).
- [ ] Threat model statement in `docs/v4-status.md` §1.4.
- [ ] CHANGELOG.md `[4.0.0-alpha.17]` entry.
- [ ] v4-alpha-11-plan.md §4 marked shipped.

---

## 8. Open questions

1. **Should `dark` reserved id be seeded at boot, or only blocked at create-time?** Recommendation: blocked only. The v4 binary doesn't NEED a `dark` project to function; the reserved id exists to prevent operator typo. (v3.0.0-docfix doesn't have `dark` reserved — it's a v4 design choice.)
2. **Should `agent_memory_recall` default to `scope='project'` (current project only) or `scope='all'` (cross-project)?** Recommendation: `scope='project'` (safer default; matches the namespace primitive threat model). Operator can pass `scope='all'` for explicit cross-project queries. **NEEDS OPERATOR CONFIRMATION.**
3. **Should `mindset_apply` and `delegate_intent` be stubbed or omitted entirely until v4 LLM client is wired?** Recommendation: stubbed (returns canned response + surfaces the API surface). The alternative (omit until alpha.18) delays the operator's `dark_memory_*` workflow for 2+ weeks. **NEEDS OPERATOR CONFIRMATION.**
4. **Should the 4 new MCP tools be exposed via the v4 binary's MCP server (stdio JSON-RPC), or ONLY via the v3.0.0-docfix binary?** Recommendation: both. The v4 binary is the new binary; not exposing the tools would make v4 less capable than v3. The alternative (only v3) defeats the purpose of v4.
5. **Chunk 4.2's `mindset_apply` stub: what should the canned system_prompt say?** Recommendation: a minimal "you are a sub-agent for ${vibe_case} task: ${task_description}" prompt with no tools + no model. Operator can override in alpha.18.

---

## 9. Cross-references

- `docs/v4-alpha-11-plan.md:186-218` — Phase 4 acceptance criteria.
- `docs/sota-critique.md:714-799` — §7.6.9 namespace primitive threat model.
- `docs/sota-critique.md:654-674` — D5 `project_id` framing decision (recommendation A: namespace primitive, not multi-tenant).
- `docs/decisions/ADR-007-judge-pipeline-v4.md` — historical ADR for judge pipeline (15 ECs).
- `internal/project/types.go:20-100` — v3.0.0-docfix Project struct (13 fields); v4 ships 7.
- `internal/tools/project.go` — v3.0.0-docfix PROJECT tools (project_create + project_lookup).
- `internal/v4alpha/audit/writer.go:118-150` — ApplyChainColumns pattern (mirrored by ApplyProjectIDColumns).
- `internal/v4alpha/judge/store.go:151` — sdd_evaluations CREATE TABLE (current 22 cols).
- `internal/v4alpha/session/session.go:95-109` — sessions CREATE TABLE (already has project_id).

---

## 10. Sign-off

- **Spec author**: Opita-AI (MiniMax-M3), 2026-09-30.
- **Operator authorization**: 2026-09-30 ("ATAQUEMOS ENOTNCES Phase 4" → green light to proceed).
- **Pre-flight review**: rows 2190 (Phase 2 SUMMARY), 2201 (Phase 3 SUMMARY), 2207 (Phase 3 docs followup SUMMARY).
- **Drift check target**: ALIGNED ≥0.85.
- **Atomic mirror target**: 1 SUMMARY pinned + 9 SECTION pinned=false (agent_id=`alpha-11-phase4`).
- **Companion docs (deferred to Chunk 4.4)**: `docs/v4-status.md` §1.4, `docs/INVARIANTS.md` INV-19 (namespace primitive), `CHANGELOG.md` `[4.0.0-alpha.17]`, `docs/v4-alpha-11-plan.md` §4 marked shipped.