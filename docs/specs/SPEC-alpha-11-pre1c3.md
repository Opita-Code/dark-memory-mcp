# SPEC-alpha-11-pre1c3 — PRE-1 C3: Loadout for session_start

> **Status**: DRAFT (2026-09-29)
> **Phase**: 1B of alpha.11+ plan (`docs/v4-alpha-11-plan.md`)
> **Vibe-loop**: alpha-11-phase-1b
> **ADR-008 compliance**: monolithic spec + atomic mirror
> **Spec-level**: 1 (single surface — additive API change)

---

## 0. TL;DR

`dark_memory_session_start` gains a new `loadout` field in its response.
The loadout is the operator's startup context — pinned rows, open todos,
recent audit writes, constitution binding, schema version, server time —
so a single call gives the harness everything it needs to start working.
No new tools. Additive. Partial-failure tolerant.

**Why now**: Phase 1A (chunk 7) shipped. Phase 1C (PRE-1 C4) shipped.
Phase 1B (this spec) closes the trilogy preflight foundation.

---

## 1. Problem

Today `session_start` returns 5 fields:

```json
{
  "session_id": "sess-...",
  "operator": "nico",
  "project_id": "dark-memory-v4",
  "status": "open",
  "started_at": "2026-09-29T..."
}
```

A new harness session then makes **5-7 separate calls** to assemble
its startup context:

1. `agent_memory_list(pinned_only=true)` — what's pinned
2. `agent_memory_list(kind=todo)` — what's open
3. `error_summary(hours=1)` — what just broke
4. `observability_writes(limit=10)` — recent audit
5. `policy_active_policy()` — constitution
6. `observability_memory_state()` — schema version + counts
7. ...

**The friction**: each call is an RPC roundtrip + a separate parse.
The harness's first 60 seconds is dominated by retrieval, not work.
The "loadout protocol" (chunk 7 + PRE-1 C1+C2+C4) is what the LLM
*should* be able to do in one call.

---

## 2. Solution (additive)

Add a `loadout` field to the session_start output. The harness still
calls session_start exactly once; the response now carries the
operator's startup context inline:

```json
{
  "session_id": "sess-...",
  "operator": "nico",
  "project_id": "dark-memory-v4",
  "status": "open",
  "started_at": "2026-09-29T...",

  "loadout": {
    "pinned_rows":   [...up to 50, kind=link pinned=true],
    "open_todos":    [...up to 50, kind=todo],
    "recent_writes": [...up to 20, audit_log actor=operator],
    "constitution":  {"id": "...", "version": "...", "active_mods": 0},
    "schema_version":"v4alpha/2026-09-27/002",
    "server_now":    "2026-09-29T..."
  },

  "loadout_warnings": [] // partial-failure signals; empty when clean
}
```

### 2.1 Six fields

| Field | Type | Source | Limit |
|---|---|---|---|
| `pinned_rows` | `[]am.Row` | `agent_memory.ListFiltered(operator, pinned=true)` | 50 |
| `open_todos` | `[]am.Row` | `agent_memory.ListFiltered(operator, kind=todo)` | 50 |
| `recent_writes` | `[]audit.Row` | `audit_log` WHERE actor = operator, ORDER BY audit_id DESC | 20 |
| `constitution` | `*Constitution` | ldflags + active mods table | n/a |
| `schema_version` | `string` | `schema_migrations` latest version | n/a |
| `server_now` | `string` (RFC3339) | `time.Now().UTC()` | n/a |

### 2.2 Partial-failure contract

If a loadout query fails (e.g. db transient error), the affected field
is set to its zero value (empty slice / null) AND a string is added
to `loadout_warnings`:

```json
"loadout_warnings": [
  "pinned_rows: db: connection refused",
  "recent_writes: audit_log: table missing"
]
```

The session itself is NOT failed — `session_start` succeeds, returns
the session_id, and the loadout is best-effort. Rationale: the operator
needs the session to keep working; degraded loadout is better than
a dead session.

### 2.3 What does NOT change

- Existing 5 fields (session_id, operator, project_id, status, started_at): unchanged.
- `session_start` audit emission: unchanged (still emits 1 audit row via session.Store.Start).
- `session_close` / `session_status` / `session_resume` / `session_heartbeat`: not touched in this chunk.

---

## 3. Design decisions

### 3.1 Where the Loadout struct lives

`internal/v4alpha/transport/mcp/loadout.go` (new file).
The builder takes (ctx, operator, amStore, db, schemaVersionFn, constitutionFn)
as deps. This makes the builder testable without the full MCP Server.

### 3.2 Why omitempty is NOT used

`loadout` is always present (never omitted). `constitution` is `*Constitution`
(nil-able) because future versions may not have a constitution bound.
`loadout_warnings` is `[]string` (empty array, never omitted).

### 3.3 Why partial-failure tolerant

The same pattern as docs_index.Index (PRE-1 C2). Per-field errors
logged to stderr + surfaced in `loadout_warnings`. Session itself
succeeds.

### 3.4 Why recent_writes limit = 20

Smaller than the summarize limit (100). session_start is the
*immediate context* — recent 20 covers the last ~10 minutes of work.
A deeper dive calls summarize_session afterwards.

### 3.5 Why schema_version as a string, not a struct

It's one field today. Future versions can add a sibling `schema_migrations`
array if multi-version support is needed. YAGNI.

### 3.6 Why server_now as a string (RFC3339), not a time.Time

JSON-RPC + mcp-go convention. Same as started_at, last_heartbeat_at, etc.

### 3.7 Constitution shape

```json
{
  "id": "release-integrity-v4",
  "version": "0.1.0",
  "active_mods": 0
}
```

The `id` and `version` come from ldflags (set at binary build time).
`active_mods` is the count of mods currently loaded (0 in v4-alpha.12;
the mod loader is deferred to alpha.3 per `docs/v4-status.md §3`).

---

## 4. Atomicity contract

- **NEW file**: `internal/v4alpha/transport/mcp/loadout.go`
- **MODIFIED**: `internal/v4alpha/transport/mcp/session.go` (extend
  sessionStartOutput + registerSessionStart closure)
- **NEW tests**: `internal/v4alpha/transport/mcp/loadout_test.go` (8 tests)
- **0 schema changes**: queries read existing tables (agent_memory,
  audit_log, schema_migrations)
- **1 audit emission**: session.Store.Start emits one (unchanged from
  existing behavior)

### 4.1 Contract per ADR-007 §10 (atomicity)

- ONE constructor: `NewLoadoutBuilder(deps) *LoadoutBuilder`
- ONE entry point: `Build(ctx, operator) (*Loadout, []string, error)`
- Field order in output: pinned → todos → writes → constitution → schema → now
- Warning order = build order (one warning per failed field, in the
  same order as the fields above)

---

## 5. Out of scope (v1)

Documented explicitly so the operator knows what the loadout does
NOT do:

- **Drift reports**: vibe_drifts.session_id column doesn't exist yet
  (BUG-10 10b migration). Same constraint as PRE-1 C4 summarize.
- **Judge verdicts**: sdd_evaluations.session_id column doesn't exist
  yet (same migration).
- **Per-project filtering**: pinned_rows / open_todos / recent_writes
  are filtered by **operator** (cross-project). project_id is the
  session's project, not a loadout filter. Rationale: the operator
  wants to see ALL their rows, not just one project.
- **Pagination**: 50/50/20 limits are hard caps. A "loadout_large"
  variant with `?limit=N` is v2.
- **JSON-lines / streaming**: loadout is a single response object.
  No streaming.
- **Caching**: no TTL cache. Every session_start re-queries. Cheap
  enough; an L1 cache is a v2 enhancement.

---

## 6. Acceptance criteria

| # | Criterion | Verified by |
|---|---|---|
| 1 | sessionStartOutput has 7 fields (5 original + loadout + loadout_warnings) | compile + tests |
| 2 | loadout.pinned_rows surfaces up to 50 pinned rows for the operator | TestBuildLoadout_PinnedRows |
| 3 | loadout.open_todos surfaces up to 50 todos for the operator | TestBuildLoadout_OpenTodos |
| 4 | loadout.recent_writes surfaces up to 20 audit rows for the operator | TestBuildLoadout_RecentWrites |
| 5 | loadout.constitution returns release-integrity-v4 + version from ldflags | TestBuildLoadout_Constitution |
| 6 | loadout.schema_version reads latest from schema_migrations | TestBuildLoadout_SchemaVersion |
| 7 | loadout.server_now is within 1s of time.Now() | TestBuildLoadout_ServerNow |
| 8 | loadout_warnings is non-empty when a field's query fails | TestBuildLoadout_PartialFailure |
| 9 | session_start still emits exactly 1 audit row | existing test |
| 10 | All 11 v4alpha packages pass (no regressions) | `go test ./internal/v4alpha/...` |
| 11 | Tool count 41 → 42 (still no new tools; the existing session_start gains the field) | server_test.go |
| 12 | Atomic mirror saved (1 SUMMARY pinned + 4 SECTION, per ADR-008) | this turn |

---

## 7. Files

### NEW

- `docs/specs/SPEC-alpha-11-pre1c3.md` (this file, ~250 lines)
- `internal/v4alpha/transport/mcp/loadout.go` (~180 LoC)
- `internal/v4alpha/transport/mcp/loadout_test.go` (~300 LoC, 8 tests)

### MODIFIED

- `internal/v4alpha/transport/mcp/session.go` (+~30 LoC: extend
  sessionStartOutput with Loadout + Warnings fields; extend
  registerSessionStart closure to call LoadoutBuilder)
- `docs/v4-status.md` (alpha.11 → alpha.12; tool inventory notes
  "session_start gains loadout field"; §8 cross-refs)
- `CHANGELOG.md` (`[4.0.0-alpha.13]` entry)

### UNCHANGED

- `internal/v4alpha/session/` (no changes; Loadout is transport-level)
- `internal/v4alpha/agent_memory/` (no changes; uses existing
  ListFiltered)
- `internal/v4alpha/audit/` (no changes; reads audit_log directly)
- `cmd/dark-memory-v4/serve.go` (no changes; Loadout is transport-
  level only)

---

## 8. Test plan

### 8.1 Unit tests (loadout_test.go)

1. **TestBuildLoadout_EmptyOperator** — fresh operator, no rows,
   no errors. All fields present, all slices empty.
2. **TestBuildLoadout_PinnedRows** — 5 pinned rows + 2 unpinned
   rows. Loadout returns the 5 pinned rows.
3. **TestBuildLoadout_OpenTodos** — 3 todos + 2 notes + 1 decision.
   Loadout returns the 3 todos.
4. **TestBuildLoadout_RecentWrites** — 25 audit_log rows total.
   Loadout returns the latest 20.
5. **TestBuildLoadout_PartialFailure_PinnedRows** — agent_memory
   Save fails. loadout.pinned_rows = []; loadout_warnings = 1 item.
6. **TestBuildLoadout_PartialFailure_RecentWrites** — audit_log
   query fails. loadout.recent_writes = []; loadout_warnings = 1.
7. **TestBuildLoadout_Constitution** — verify the constitution
   struct fields are populated from the builder's source.
8. **TestBuildLoadout_SchemaVersion** — verify the schema_version
   reads from the supplied source function.

### 8.2 Integration test (server_test.go)

9. **TestSessionStart_IncludesLoadout** — call session_start via
   the in-process server. Verify the response has a loadout field.

### 8.3 Cross-package

- All existing v4alpha tests pass (no regressions).
- server_test.go expects 41 tools (unchanged; no new tool).

---

## 9. Sequence diagram (operator start)

```
operator          harness               session_start             LoadoutBuilder
  |                  |                       |                          |
  |--  cmd  -------->|                       |                          |
  |                  |-- session_start  --->|                          |
  |                  |                       |--- Start (db+audit) --->|
  |                  |                       |   { 1 audit row emitted }|
  |                  |                       |                          |
  |                  |                       |--- BuildLoadout -------->|
  |                  |                       |                          |
  |                  |                       |                          |  (queries am, audit, schema)
  |                  |                       |                          |
  |                  |                       |<-- {Loadout, warnings} --|
  |                  |                       |                          |
  |                  |<-- {sess + Loadout} --|                          |
  |<--  cmd result --|                       |                          |
```

Single RPC. Two internal calls (Start + BuildLoadout). One audit row.

---

## 10. Open questions

None. This is an additive API change with a small, well-bounded
surface. If we discover issues post-ship, they're in 3 places:

- The Loadout struct (additive: add new field)
- The Build function (additive: add new query)
- The wire JSON (additive: existing 5 fields unchanged)

No breaking change risk.

---

## 11. Cross-refs

- **docs/sota-critique.md** §7.6 — Phase 1B is item 2 of 5.
- **docs/v4-alpha-11-plan.md** — Phase 1 vibe-loops.
- **docs/specs/SPEC-alpha-11-chunk7.md** — the workstream close.
- **docs/specs/SPEC-alpha-11-pre1c4.md** — the previous chunk (PRE-1 C4).
- **Row 2127 (PRE-1 C1)** — RecallFiltered + tag_prefix (the mechanism
  Loadout uses for pinned/todos).
- **Row 2114 (PRE-1 C2)** — docs_index (the same defensive pattern:
  per-field failures are non-fatal).
- **Row 2180 (PRE-1 C4)** — summarize_session (the sister tool:
  Loadout at session_start, Summarize at session_close).
- **Row 2173 (multi-tenant reframe)** — Loadout is operator-scoped,
  not project-scoped. project_id is the session's project, not a
  loadout filter.

---

## 12. Sign-off

This spec is self-approved (operator mandate 2026-09-28). Implementation
proceeds in the same turn per the alpha.11 plan ("arranca con phase 1C"
→ phase 1C shipped → "dale al next" → phase 1B = this spec).