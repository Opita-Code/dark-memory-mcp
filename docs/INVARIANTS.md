# Eight Operational Invariants

> **Audience**: anyone touching the dark-memory-mcp code. The
> invariants are **type-system guarantees** — they are exercised by
> `tests/invariants/`. A future contributor adding a new `Save*` method
> cannot accidentally bypass them; the interface signature requires
> the right `WriteContext` and the test suite fails if the audit insert
> is missing.

Each invariant has a **defensive test** in `tests/invariants/` and is
enforced at the `Store` interface boundary (not as documentation, as
mechanical guarantees).

---

## INV-1 — write-path audit

**Statement**: Every `Save*` method must insert a `write_audit` row in
the **same transaction** as the data write. If the audit insert fails,
the data write rolls back.

**Why**: INV-1 is the foundation of audit + forensic. Without it,
post-mortem analysis is impossible ("who wrote row 17, when, from
which session, with which constitution in force?").

**Enforced at**: `Store.Save*` methods take a `WriteContext{Actor,
SessionID, WritePath, ConstitutionID, ConstitutionVersion, ProjectID}`
parameter. The implementation must record the audit row in the same
transaction.

**Defensive test**: `tests/invariants/inv5_6_test.go::TestInv1_WriteAuditAtomic`
(rolls back on audit failure, asserts no row in data table).

**Operator signal**: `write_audit` row count should grow ≈ data row
count. Discrepancy > 1% = bug.

---

## INV-2 — per-session scoping

**Statement**: `Recall(query, opts)` filters by `SessionScope`. The
default is cross-session (the existing behavior pre-orchestration),
but workflow orchestrators always pass `SessionScope=self +
session_id`.

**Why**: Without scoping, one operator can read another's research
notes. INV-2 is the lightweight multi-tenancy primitive.

**Enforced at**: `Store.Recall` accepts `research.RecallOptions{
SessionScope, SessionID }`. The implementation filters
`research_items` by `session_id` when `SessionScope=self`.

**Defensive test**: `TestInv2_RecallFiltersBySession` — writes from
session A, calls `Recall` from session B with `SessionScope=self`,
asserts empty.

**Operator signal**: `SELECT COUNT(*) FROM research_items WHERE
session_id = '...'` should equal what that session's orchestrator
sees in its RecallContext.

---

## INV-3 — canary check on payload writes

**Statement**: `Store.SaveRun` (research items insert path) runs
`safety.ValidatePayload(payload)` against the active canary before
inserting. Canary hit returns `ErrCanaryInPayload`; the transaction
rolls back.

**Scope (v1.3.0 precision)**: INV-3 explicitly applies to the
research_items ingest path. Other `Save*` methods (Spec, Artifact,
BrandGuide, ComplianceRule, Constitution, etc.) write content that
originates from the LLM in the same session as the operator, not
from external untrusted sources; the canary tripwire is calibrated
for the OSINT research path where prompt injection is the threat
model. Spec / artifact content goes through other defensive layers
(project isolation INV-7, write_audit INV-1, constitution validation
INV-4).

**Why**: A user prompt that includes the canary token (placed there
defensively by upstream callers like the system prompt composer) is
a signal that something is replaying untrusted content. The canary
is a defensive tripwire, not user data.

**Enforced at**: `Store.SaveRun` invokes the `safety.Holder` before
inserting. The canary is a 128-bit random token minted at server boot
(installed via `safety.NewCanary()`).

**Defensive test**: `TestInv3_CanaryRejected` — payload contains
canary → Save returns `ErrCanaryInPayload`; data row absent.

**Operator signal**: `inspect --json` reports
`canary_present: true/false`. If the canary was minted, the
`canary_present: true` flag on a write_audit row means the payload
tripped the wire.

---

## INV-4 — constitution watchdog (SHA verify)

**Statement**: `Store.Open` reads the active constitution's SHA256
from the DB, hashes the file at the configured `file_path`, compares.
Mismatch raises `ErrConstitutionDrift`. **Migrations refuse under drift.**

**Why**: A constitution is the rules in force when a write happened.
If the file changes after writes happened, audit history becomes
inconsistent ("this row was written under constitution v2, but
`constitution_id=v3` says otherwise"). The watchdog refuses to start
the Store until the operator aligns the file with the stored SHA.

**Enforced at**: `Store.Open` runs the watchdog before applying
migrations. Migrations are also gated: pending migrations + drift =
refuse.

**Defensive test**: `TestInv4_ConstitutionDriftRefusesOpen` —
mutate the constitution file → Store.Open returns
`ErrConstitutionDrift`.

**Operator signal**: `dark-mem-mcp` panic log on boot
("ErrConstitutionDrift: stored=X actual=Y"). Recovery: regenerate
the constitution OR reset `ActiveConstitution` to a known-good state
(see `Store.SaveConstitution` / `SetActiveProject`).

---

## INV-5 — cache re-hash on Get

**Statement**: `llm/cache.go Cache.Get` re-hashes stored text with
SHA-256, compares to `entry.SHA256`. Mismatch → treat as miss + emit
anomaly event.

**Why**: If the cache backend is corrupted (disk bit rot, partial
write, malicious modification), the LLM could be served stale or
forged data. Re-hashing on every Get is cheap (microseconds for
SHA-256) and turns silent corruption into a loud miss.

**Enforced at**: `internal/llm/cache.go::Get`.

**Defensive test**: `TestInv5_CacheRehashDetectsTamper`.

**Operator signal**: `dark-mem-inspect` `anomalies` tool
(NOT YET IMPLEMENTED in v1 — Wave 4+ work; today, anomalies surface
in `write_audit.notes` and the session log).

---

## INV-6 — mod content sanitization

**Statement**: The mod loader runs `directive/knowledge` bodies through
`injectionMarkers` regex set. Refused unless `risk_class ∈
{exploit-development, active-probing}` AND user-file is whitelisted
via `DARK_MOD_WHITELIST`.

**Why**: Mods are loaded from disk and injected into the LLM context.
A mod that contains `IGNORE PREVIOUS INSTRUCTIONS` or similar
injection markers is a backdoor vector. INV-6 is the gate.

**Enforced at**: `internal/mods/loader.go` (the loader runs
`injectionMarkers` regex on every loaded file).

**Defensive test**: `TestInv6_ModInjectionRejected`.

**Operator signal**: `dark-mem-mcp` boot log emits
"`mod X rejected: injection marker at line N`". Refused mods don't
load. Whitelist via `DARK_MOD_WHITELIST=research-only-mod,approved-mod`.

---

## INV-7 — per-project scoping (multi-tenancy)

**Statement**: Every tenant-scoped table carries `project_id` (added in
migration v7). `Store.Save*` and reads filter by `ActiveProject()` —
unknown projects are rejected.

**Why**: Enables multi-tenant dark-research-mcp deployments (one
process serving many isolated projects). Default project = `default`
(legacy compatibility).

**Enforced at**: `Store.SetActiveProject(ctx, projectID)` validates
against the `projects` table. All reads use `Store.requireProject()`
to refuse operations when no project is set.

**Defensive test**: `tests/project/project_test.go` (cross-project
isolation suite).

**Operator signal**: `dark-mem-inspect` `active_project` field. Set
via `dark_memory_session_start` or `Store.SetActiveProject`.

## INV-8 — per-MCP database isolation

**Statement**: Each MCP server in the dark-agents family owns its
**own SQLite database file**. The default file path for
`dark-memory-mcp` is `dark-memory.db` (NOT shared with
`dark-research-mcp`'s `dark.db`). Operators MAY override via
`DARK_DB=` env var, but the override MUST point to a file that no
other dark-* server is concurrently writing to.

**Why**: Sharing `dark.db` between `dark-research-mcp` and
`dark-memory-mcp` produced v1.2.2 boot crashes because both
projects registered migration rows in the same `schema_migrations`
table under overlapping version numbers (v1=`initial_schema`
in both). When `dark-research-mcp`'s v1-v3 had been applied and
`dark-memory-mcp`'s v4-v10 hadn't, the latter's migration runner
saw a phantom "all already-applied" state and never materialised
the tables its schema expected — yielding "no such table: sessions"
on every boot. The principle generalises: any cross-MCP DB
sharing produces migration bookkeeping collisions because
version-number-NAMES overlap without versioning the project that
owns each row.

**Enforced at**: `defaultDSN()` in `internal/server/bootstrap.go`
returns `dark-memory.db`. A defensive test in
`tests/e2e/server_test.go::TestServer_DefaultDSN_DoesNotCollideWithDarkResearch`
asserts the returned filename differs from the historical
`dark.db` default that dark-research-mcp uses. Operators who want
the legacy shared-DB behaviour can explicitly set
`DARK_DB=dark.db`; the constitution requires them to opt in via
the env var, not via the default.

**Defensive test**: `TestServer_DefaultDSN_DoesNotCollideWithDarkResearch`
(regression guard against reintroducing the shared default).

**Operator signal**: `dark-mem-inspect --json` reports
`resolved_db_path`. If two dark-* MCPs share a `resolved_db_path`,
the migration runner on the second-starting one will refuse
migrations under `ErrInvalidArgument` and the operator will
see the recovery paths in CHANGELOG v1.2.2.

**Applies to every dark-* future server**: `[FUTURE-MCP-1]` MUST
default to `harvest.db` (or a project-specific filename), NOT
`dark.db`. The CI lint rule `check_no_shared_db_default` greps
every `defaultDSN()`-like function in the org and ensures
uniqueness; passing this rule is a precondition for merging any
new MCP server into `dark-agents/`. Documented in
`CONTRIBUTING.md` `Add a new MCP server` section.

---

## INV-9 — *(reserved)*

INV-9 was deliberately skipped. The numbering preserves INV-1
through INV-8 in their original order; subsequent invariants
follow (INV-10, INV-11, ...). v2.3.0 introduces INV-10. Operators
that see gaps in numbering should treat them as deliberate (the
v2.x.0 invariant contract is documented in CHANGELOG).

---

## INV-10 — agent memory rows are persistent across session lifecycle (v2.3.0)

**Statement**: `agent_memory` rows are independently queryable by
`(project_id, agent_id, kind)`. Closing the active session does
NOT invalidate rows. Recall (the BM5-ranked search via
`agent_memory_recall`) is permitted against rows whose
`session_id IS NULL` (i.e. un-bound) within the active project.

**Why**: Pre-v2.3.0, `SaveAgentMemory` auto-bound `session_id` from
the active session. After a session closed (sweeper abort,
`session_close`, or restarts), rows were invisible via
`scope=session` because the `session_id` no longer matched any
active session. They were also invisible via `scope=operator`
because the v2.1.x `ListAgentMemory` implementation resolved
`scope=operator` via `SELECT actor FROM write_audit ORDER BY id
DESC LIMIT 1`, which returned `session_sweeper:open_to_idle` (the
last actor to touch the audit log) — NOT the operator who
actually saved the row. Operators experienced "I saved things,
then they disappeared."

v2.3.0 closes this gap by:
1. Removing auto-bind (caller must explicitly set `bind_session=true`
   in `AgentMemorySaveInput` to tag a row with the active session).
2. Resolving `scope=operator` from the caller-provided `Operator`
   filter field — NOT from `write_audit.actor`.
3. Adding `scope=agent` (Mem0 `agent_id` semantics) that filters
   on the new `agent_id` column.
4. Adding `dark_memory_agent_memory_recall` as the canonical
   retrieval path so other tools can consume the data plane.

This invariant is the type-system-level guarantee. The wire
contract is additive only: pre-v2.3.0 callers that omitted
`bind_session` (implicit behaviour) get rows that survive session
close; callers that passed `bind_session=true` get rows with the
session tag, just as before.

**Enforced at**:
- `internal/store/sqlite/store.go::SaveAgentMemory` — no longer
  populates `session_id` from the active session. (Line numbers:
  pre-v2.3.0 auto-bind removed.)
- `internal/store/sqlite/store.go::ListAgentMemory` — scope
  resolution uses `f.Operator` (caller-provided) NOT
  `write_audit.actor`. `f.AgentID` is required for `scope=agent`.
- `internal/agentmemory/types.go::ScopeAgent` — new scope value.
- `internal/tools/agent_memory.go::RegisterAgentMemory` — all six
  tools' wire schemas.

**Defensive test** (added in v2.3.0):
- `tests/dual_driver/agent_memory_scope_test.go::TestListAgentMemory_ScopeOperator_UsesCallerOperatorNotAuditActor` — saves rows
  from operator A, lets sweeper write one audit row, then asserts
  `scope=operator` with `operator="A"` returns A's rows (was
  broken pre-v2.3.0; sweeper would shadow).
- `tests/dual_driver/agent_memory_lifecycle_test.go::TestSaveAgentMemory_NoAutoBind_PersistsAcrossSessionClose` — save without
  `bind_session` while session S is active; close S; reopen a new
  session; assert the row is visible via `scope=project`.

**Operator signal**: `dark_memory_agent_memory_list(scope="project")`
after a session restart should return the same rows as
`scope="project"` immediately after saving — modulo any
session-tagged rows (those are filtered correctly only via
`scope=session`, which now requires an active session).

**Migration**: `internal/migrate/sqlite/ddl.go` v19
(`agent_memory_v230_columns`) adds `agent_id` and `memory_type`
columns. Pre-v2.3.0 rows have `agent_id = NULL` and
`memory_type = NULL`; that is fine for the new `scope=agent` and
`memory_type` filters (NULL = unfiltered, which is what existing
callers expect).

---

# v4-only invariants (added on `feat/v4-redesign`)

The invariants below are introduced on the v4 redesign branch. They
do NOT exist in v1.x / v2.x production. Each carries a `feat/v4-
redesign` tag in its header so v3 readers don't accidentally treat
them as inherited.

---

## INV-16 — dark-db concurrency contract [`feat/v4-redesign`]

**Statement**: Every `*sql.DB` opened against a dark-db path (the
SQLite file at `<UserConfigDir>/dark-agents/dark-memory.db` and per-
project siblings) MUST go through `internal/v4alpha/store.OpenSQLite`
so the DSN carries the BUG-5 pragma set:

```
?_pragma=busy_timeout(5000)
&_pragma=journal_mode(WAL)
&_pragma=synchronous(NORMAL)
&_pragma=foreign_keys(1)
&_pragma=wal_autocheckpoint(1000)
&_pragma=cache_size(-2000)
&_pragma=temp_store(MEMORY)
```

…AND the connection pool is bounded: `MaxOpenConns(8)`,
`MaxIdleConns(4)`, `ConnMaxIdleTime(5m)`.

Every read-modify-write sequence over a dark-db path MUST go through
`store.WithTx(ctx, db, fn)` which opens a `sql.LevelSerializable`
transaction.

**Why**: dark-db is shared across multiple agents, multiple operator
sessions, and the BUG-5 stress test (2026-09-27) confirmed that the
default `sql.Open("sqlite", dsn+"?_pragma=foreign_keys(1)")` pattern
deadlocks under concurrent writers on Windows within ~30 seconds
(16 goroutines opening the same file; 2/16 failed with `SQLITE_BUSY`
from the `PingContext` that drives the `journal_mode` conversion).
Production agents writing concurrently would silently corrupt audit
trails or hang the MCP transport.

The chosen pragma set is grounded in four tier-1 sources:

1. `sqlite.org/wal.html §2.2`: "WAL provides more concurrency as
   readers do not block writers and a writer does not block readers.
   ... since there is only one WAL file, there can only be one writer
   at a time."
2. `sqlite.org/pragma.html#synchronous`: `synchronous=NORMAL` + WAL
   means writers never fsync — only the checkpoint does, which runs
   in the background.
3. `pkg.go.dev/modernc.org/sqlite` Performance §: maintainer verbatim
   "Bound the pool with `sql.DB.SetMaxOpenConns` and do not issue a
   periodic query before the previous one has returned."
4. `sqlite.org/wal.html §11` (WAL-reset bug): fixed in SQLite 3.51.3.
   We embed SQLite 3.53.2 via modernc.org/sqlite v1.53.0 (`CLAUDE.md`
   of the modernc repo), so we are patched.

**Enforced at**: `internal/v4alpha/store/open.go::OpenSQLite` and
`internal/v4alpha/store/tx.go::WithTx`. Direct `sql.Open("sqlite",
...)` against a dark-db path is a violation; the code review rule is
"search for `sql.Open` and verify the path is `:memory:` or a non-
dark-db temporary file".

**Defensive tests**: `internal/v4alpha/store/store_test.go` (10 tests)
and `internal/v4alpha/manifest/cap_store_*test.go` (5 concurrent
tests). Specifically:

- `TestOpenSQLite_DSNContainsAllPragmas` — pins the pragma list.
- `TestOpenSQLite_JournalModeIsWAL` — engine-level WAL check.
- `TestOpenSQLite_BusyTimeoutApplied` — 5000 ms landed.
- `TestOpenSQLite_PoolBounds` — `MaxOpenConnections == 8`.
- `TestOpenSQLite_ConcurrentOpens` — 16 goroutines race (T7).
- `TestConcurrent_Grant_SameID` — T2 (32 goroutines, same id).
- `TestConcurrent_GrantThenRevoke_SameID` — T3 (16 revokers).
- `TestStress_10k_Writes` — T4 (no deadlock under 10k inserts).
- `TestPoolExhaustion_200Goroutines_Pool8` — T5 (200 goroutines).
- `TestMultiDBIsolation` — T8 (cross-DB independence).

**Operator signal**: `dark_memory_memory_state` tool reports
`db_pool` (`max_open`, `max_idle`, `in_use`, `idle`). A failure of
any of the 10 tests above is the alert.

**Migration cost**: zero — `OpenSQLite` is the canonical opener; no
v3 code is required to migrate (v4 branches from v2.20.0).

---

## INV-17 — FTS5 ordering under SERIALIZABLE [`feat/v4-redesign`]

> **Corrected 2026-09-27**: an earlier draft of this invariant (and
> agent_memory row 2040) claimed we switched the schema from
> contentless to regular FTS5. **This was incorrect.** The schema
> in v4-alpha.1 is contentless (`content='agent_memory',
> content_rowid='id'`); plain `DELETE FROM fts WHERE rowid=?` works
> against the contentless table on `modernc.org/sqlite` v1.53 (the
> `TestArchive_RemovesFromBaseAndFTS` test passes). What we DID
> discover is a real ordering bug inside `SERIALIZABLE` transactions.
> See "Why" below for the correct story.

**Statement**: Every multi-step write path that mutates both a
base table and its companion FTS5 index MUST execute the FTS5
DELETE **before** the base-table UPDATE within the SERIALIZABLE
transaction, regardless of whether the FTS5 table is contentless
or regular. The FTS5 re-INSERT happens after the base-table UPDATE
completes.

The canonical sequence in `agent_memory.Update` is:

```
1. (verify row exists) SELECT 1 FROM agent_memory WHERE id=?
2. DELETE FROM agent_memory_fts WHERE rowid = ?      ← FIRST
3. UPDATE agent_memory SET ... WHERE id = ?
4. SELECT title, content, tags FROM agent_memory WHERE id = ?
5. INSERT INTO agent_memory_fts (rowid, title, content, tags)
   VALUES (?, ?, ?, ?)
```

The canonical sequence in `agent_memory.Save` is:

```
1. INSERT INTO agent_memory (operator, kind, title, content, tags, pinned)
   VALUES (?, ?, ?, ?, ?, ?)
2. INSERT INTO agent_memory_fts (rowid, content, title, tags)
   VALUES (last_insert_rowid(), ?, ?, ?)
```

The canonical sequence in `agent_memory.Archive` is:

```
1. DELETE FROM agent_memory WHERE id = ?
2. DELETE FROM agent_memory_fts WHERE rowid = ?
```

(Note: in `Archive` the order is base-first, then FTS5, because
the base DELETE is the trigger and the FTS5 DELETE is the
sidecar — `TestArchive_RemovesFromBaseAndFTS` passes with this
order against the contentless schema.)

**Why**: empirical discoveries on `modernc.org/sqlite` v1.53
(2026-09-27, BUG-8):

- **Ordering bug**: `UPDATE base → DELETE fts → SELECT → INSERT fts`
  inside one `SERIALIZABLE` transaction raises `SQLITE_CORRUPT
  (267) — "database disk image is malformed"`. The workaround is
  `DELETE fts → UPDATE base → SELECT → INSERT fts` — the FTS5
  DELETE goes first while the base row is still in its original
  shape. Verified by `TestUpdate_MutatesFieldsAndReSyncsFTS` (passes
  with the fix; was failing with the reversed order).
- **Schema type is contentless, not regular**: the v4-alpha.1 schema
  uses `content='agent_memory', content_rowid='id'`. Earlier draft
  docs and agent_memory row 2040 claimed we switched to regular
  FTS5 to dodge SQLITE_CORRUPT on plain `DELETE FROM fts WHERE
  rowid=?`. The test shows plain DELETE works on contentless too;
  the real fix is the **ordering** above, not the schema type.
  Leaving the schema contentless saves the ~2× storage cost.
- **FTS5 `'delete-all'` command** (the canonical way to delete from
  a contentless FTS5) is also known to fail on modernc v1.53 under
  SERIALIZABLE; we never use it. Plain `DELETE FROM fts WHERE
  rowid=?` works and is what all three write methods use.

These are not bugs we can patch upstream. They are quirks in
modernc v1.53's FTS5 module under SERIALIZABLE isolation. The
only durable defense is the **canonical sequence** documented
above.

**Enforced at**: `internal/v4alpha/agent_memory/agent_memory.go` —
`Save`, `Update`, `Archive` all use the canonical sequence. Code
review rule: any new method that mutates both a base table and its
FTS5 sidecar inside one transaction MUST do the FTS5 DELETE before
the base UPDATE.

**Defensive tests**: `internal/v4alpha/agent_memory/agent_memory_test.go`:

- `TestUpdate_MutatesFieldsAndReSyncsFTS` — exercises the Update
  path; asserts FTS5 sees the new content after Update.
- `TestUpdate_NoFieldsIsNoOp` — boundary: empty mutation should not
  corrupt FTS5.
- `TestArchive_RemovesFromBaseAndFTS` — asserts FTS5 row is gone
  after Archive. PASSES against the contentless schema.
- `TestList_OrdersPinnedFirst` + `TestRecall_FindsRecentlySaved` —
  guard against future regressions that re-introduce the
  ordering bug.

**Operator signal**: `dark_memory_memory_state` reports
`agent_memory.fts_mode = "contentless"`. If a future schema
migration flips this to `"regular"`, the storage cost doubles
(unnecessary in v4-alpha.1) but no functionality changes. If a
future revision flips it to `external content` (FTS5 column names
without a rowid binding), the entire agent_memory surface breaks;
the test suite catches it.

**Why "fts-delete-first" is the canonical name**: the ordering is
the load-bearing property. Future FTS5 work in v4 (research items,
workflow journals, etc.) should start from this rule.

**Source**: discovered during BUG-8 implementation, agent_memory row
2040 in `dark-memory-v4` project (operator `nico`, 2026-09-27). The
"regular vs contentless" detail was corrected on 2026-09-27 after
the test pass + code inspection showed the schema is contentless.

---

## Quick reference: which `Save*` enforces which invariant

| Store method | INV-1 | INV-2 | INV-3 | INV-4 | INV-6 | INV-7 | INV-8 | INV-10 | INV-16 | INV-17 |
|---|---|---|---|---|---|---|---|---|---|---|
| `SaveSpec` | ✓ | — | — | (read) | — | ✓ | ✓ | — | ✓ | — |
| `SaveArtifact` | ✓ | — | ✓ | (read) | — | ✓ | ✓ | — | ✓ | — |
| `SaveDriftReport` | ✓ | — | — | (read) | — | ✓ | ✓ | — | ✓ | — |
| `SaveSDDEvaluation` | ✓ | — | — | (read) | — | ✓ | ✓ | — | ✓ | — |
| `SaveRun` | ✓ | — | ✓ | (read) | — | ✓ | ✓ | — | ✓ | — |
| `SaveSession` | ✓ | ✓ | — | (read) | — | ✓ | ✓ | — | ✓ | — |
| `SaveMod` / `RecordModLoad` | ✓ | — | ✓ | (read) | ✓ | ✓ | ✓ | — | ✓ | — |
| `SaveConstitution` | ✓ | — | — | (write) | — | ✓ | ✓ | — | ✓ | — |
| `SaveAgentMemory` | ✓ | — | — | (read) | — | ✓ | ✓ | ✓ | ✓ | ✓ |
| `ListAgentMemory` | — | — | — | (read) | — | ✓ | ✓ | ✓ | ✓ | — |
| `SearchAgentMemory` | — | — | — | (read) | — | ✓ | ✓ | ✓ | ✓ | (FTS5 read) |
| `UpdateAgentMemory` | ✓ | — | — | (read) | — | ✓ | ✓ | ✓ | ✓ | ✓ |
| `ArchiveAgentMemory` | ✓ | — | — | (read) | — | ✓ | ✓ | — | ✓ | ✓ |
| `Recall` | — | ✓ | (read) | (read) | — | ✓ | ✓ | — | ✓ | (FTS5 read) |
| `Vacuum` | — | — | — | — | — | (filters by project) | ✓ | — | ✓ | — |
| `Migrate` | — | — | — | refused under drift | — | — | ✓ | — | ✓ | — |

*Legend: ✓ = enforces this invariant; — = not relevant; (read) =
reads constitution for WriteContext, doesn't enforce a write-side
invariant; `(FTS5 read)` = goes through FTS5 index, INV-17 read-side
contract applies (no DELETE/INSERT within the read Tx).*

---

*See also: [RUNBOOK.md](./RUNBOOK.md) · [COEXISTENCE.md](./COEXISTENCE.md) · [CONTEXT_OBJECTS.md](./CONTEXT_OBJECTS.md) · [v4-status.md](./v4-status.md) · [AGENT_MEMORY_SCHEMA.md](./AGENT_MEMORY_SCHEMA.md)*