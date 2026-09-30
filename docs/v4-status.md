# v4 Status — current state of the redesign

> **Audience**: anyone touching the `feat/v4-redesign` branch.
> **TL;DR**: v4-alpha.17 ships **46 of 57 canonical tools** (81% of
> the surface) plus the full judge pipeline (ADR-007, 4 commits
> shipped) plus the judge_util + research namespaces (BUG-10 10a)
> plus the SOTA-doc workstream (7 of 7 chunks, +2,380/-7 lines,
> 12 file operations) plus PRE-1 C4 (summarize_session +
> skill_loaded tracking) plus PRE-1 C3 (session_start gains a
> Loadout of operator startup context) plus BUG-12 (cross-
> process audit_id monotonicity) plus **Phase 2 (audit hash chain
> + dark_memory_audit_verify MCP tool, Option B)** plus
> **Phase 3 (ADR-009: provider allow-list 4 → 9; ADR-011:
> bootstrap-CI statistical self-bias detection per Play Favorites)**
> plus **Phase 4 (BUG-10 10b namespace primitive — foundation +
> 4 MCP tools + hard isolation enforcement, 3 commits, INV-19)**.
> The package layout is **NOT** what `ARCHITECTURE-V4.md
> §5 (original)` promised — see "actual layout" below. The operator-
> facing surface is real and tested.
> Remaining 11 tools land in BUG-10 10b-e; the judge pipeline was
> added in 4 commits (commits 1-4 of ADR-007). The next concrete
> work is **Phase 5 (ADR-013/014/015 memory subsystem, gated on
> OD2=YES)** — see `docs/v4-alpha-11-plan.md`.

| Field | Value |
|---|---|
| Branch | `feat/v4-redesign` (from `v2.20.0`, NOT from `v3.0-void`) |
| Last reviewed | 2026-09-30 |
| Status | **alpha.17** — pre-release, local-only, contributors only |
| Version constant | `v4alpha.17-dev` (resolved via `ldflags` → `debug.ReadBuildInfo` → `"dev"`) |
| Schema version | `v4alpha/2026-09-30/004` (stamped in `schema_migrations`); audit_log gains `prev_hash`, `row_hash` (alpha.15); sdd_evaluations gains `confidence_calibrated`, `calibration_ci_low`, `calibration_ci_high`, `calibration_method` (alpha.16); 5 tables gain `project_id` (alpha.17) — agent_memory, audit_log, sdd_evaluations, vibe_specs, vibe_artifacts |
| Binary | `dark-memory-v4` (19.66 MB Windows) |
| Local-only policy | YES — no `git push`/`fetch`/`pull`, no remote tags/releases |

---
## 1. Tools inventory (46 of 57)

The canonical surface is 57 tools (see `ARCHITECTURE-V4.md §6.3
tool-count target`). v4-alpha.17 registers **46 of those**.

### 1.1 session_start Loadout (PRE-1 C3, alpha.13)

The `dark_memory_session_start` response gains two new fields
(`loadout`, `loadout_warnings`). The loadout collapses 5-7
follow-up calls into one inline response:

- `pinned_rows` (up to 50)
- `open_todos` (up to 50)
- `recent_writes` (up to 20, audit_log for the operator)
- `constitution` (id + version + active_mods)
- `schema_version` (latest row in schema_migrations)
- `server_now` (RFC3339)

`loadout_warnings` is a per-field failure signal (empty when
clean). The session itself NEVER fails on a loadout problem —
degraded loadout is better than a dead session.

No new tool registered. Tool count remains 41 (after PRE-1 C3).

### 1.2 Phase 2 — audit hash chain (alpha.15) ⭐ NEW

Per `docs/specs/SPEC-alpha-11-phase2.md` (Option B, 2026-09-29):

- **2 new columns on `audit_log`**:
  - `prev_hash BLOB` (32 bytes, SHA-256 of the previous row)
  - `row_hash BLOB` (32 bytes, SHA-256 of the current row)
- **Canonical encoding** (per `internal/v4alpha/audit/canonical.go`):
  ```
  row_hash = SHA256(prev_hash || audit_id_BE || actor || 0x00
                 || session_id || 0x00 || payload || 0x00
                 || created_at || 0x00)
  ```
- **1 new MCP tool**: `dark_memory_audit_verify(start_id?, end_id?)`
  → `{verified, broken_at, count, start_id, end_id, elapsed_ms}`.
  Read-only; walks the chain; detects modification, deletion, forgery.
- **1 new migration helper**: `audit.ApplyChainColumns(ctx, db)` —
  idempotent `ALTER TABLE ADD COLUMN × 2` for legacy DBs.
- **Tool count**: 41 → 42.

Ed25519 (ADR-017) is **deferred** — see §10 of the spec for
rationale (no external verifier use case today).

### 1.3 Phase 3 — judge improvements (alpha.16) ⭐ NEW

Per `docs/specs/SPEC-alpha-11-phase3.md` (2026-09-30):

**ADR-009 — Provider allow-list 4 → 9**:
- `internal/v4alpha/judge/llm.go` `supportedProviderIDs` extended
  with `openai`, `google`, `zhipu`, `moonshot`, `qwen` (was
  `anthropic`, `minimax`, `minimax-cn`, `deepseek`).
- Auto-detect chain `autoDetectOrder` iterates all 9.
- Pin-error enumerates full 9-provider list.
- Mistral NOT included — requires new `ProviderSpec` entry in
  `internal/llm/catalog.go` (separate decision; catalog work,
  not judge-client work).
- 5 new L1 tests (`TestNewRealLLMClient_*`).
- **No new MCP tools** (surface stays at 42).

**ADR-011 — Bootstrap-CI statistical self-bias**:
- **4 new columns on `sdd_evaluations`**:
  - `confidence_calibrated REAL` — point estimate (mean of historical)
  - `calibration_ci_low REAL` — 2.5th percentile (95% CI default)
  - `calibration_ci_high REAL` — 97.5th percentile
  - `calibration_method TEXT` — `'play_favorites_v1'` (placeholder
    for future methods)
- **NEW helper** `internal/v4alpha/judge/calibration.go`:
  `BootstrapCI(samples, confidence, nResamples) → (point, low, high)`.
  Efron 1979 percentile method, pure Go, `math/rand/v2`,
  deterministic seed=42, `defaultResamples=1000`.
  `ShouldRecalibrate(n) → n >= 50` per Play Favorites §4.3.
- **NEW migration helper**: `judge.ApplyCalibrationColumns(ctx, db)`
  idempotent `ALTER TABLE ADD COLUMN × 4` (same pragma_table_info
  pattern as `audit.ApplyChainColumns` from Phase 2). Also creates
  `idx_sdd_eval_provider_target`.
- **EC-007 split** (`internal/v4alpha/judge/edge_cases.go`):
  - **EC-007a** (renamed from `EC007SelfReference`): binary
    substring check. Preserved as legacy fallback for uncalibrated
    rows (`pc.CalibrationCI == nil`).
  - **EC-007b** (NEW `EC007bSelfBiasStatistical`): fires when
    `llm_confidence > ci_high`. Severity: `warn` by default,
    upgrades to `error` when excess > 0.20.
  - `NewDefaultEdgeCaseRunner` now registers 16 ECs (was 15).
- **NEW hook**: `Server.populateCalibration()` in
  `internal/v4alpha/transport/mcp/judge.go` runs after each
  `SaveEvaluation`. Pulls historical confidences via
  `Store.ConfidencesByProviderTarget()`; when `ShouldRecalibrate(N)`,
  stamps the row via `Store.SetCalibration()` with
  `method='play_favorites_v1'`. Best-effort (failure logged to
  stderr, NOT fatal).
- **NEW tests**: 8 L1 (calibration_test.go NEW) + 4 L1 (llm_test.go)
  + 4 L2 (edge_cases_test.go) + 1 L2 (store_deliberate_breaks_test.go)
  = 17 new tests + 1 updated pre-existing test.

**Tool count**: 42 → 42 (no new MCP tools; surface unchanged).

### 1.4 Phase 4 — BUG-10 10b namespace primitive (alpha.17) ⭐ NEW

Per `docs/specs/SPEC-alpha-11-phase4.md` (2026-09-30):

Per the threat model in `docs/sota-critique.md §7.6.9`, v4's
`project_id` is a SOFT workstream namespace (filter column), NOT
a multi-tenant primitive. HARD isolation is `coexistence_group`
(per-MCP `dark.db`). Phase 4 ships the namespace primitive in
3 commits (foundation + surface + hard isolation):

#### 1.4.1 Chunk 4.1 — foundation (commit `1d39659`)

- **NEW `internal/v4alpha/project/` package** (~1,023 LoC):
  - `types.go` (156 LoC) — `Project` struct (7 fields vs v3's 13),
    reserved-id map (`'default'`, `'dark'`), kebab-case regex
    `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`, sentinel errors
    (`ErrProjectNotFound`, `ErrReservedProjectID`,
    `ErrInvalidProjectID`, `ErrInvalidProject`,
    `ErrProjectAlreadyGone`).
  - `schema.go` (175 LoC) — `CreateSchema` (idempotent, seeds
    `'default'` via `INSERT OR IGNORE`) +
    `ApplyProjectIDColumns` (idempotent ALTER ADD COLUMN × 5
    tables + 5 indexes via `pragma_table_info`).
  - `store.go` (283 LoC) — `Store.Create` (idempotent,
    rejects reserved/invalid), `Lookup`, `Archive`, `List`.
    Every Create emits one INV-1 audit row via
    `audit.Writer.WriteWithProject`.
- **`audit.Writer.WriteWithProject` (NEW sibling of Write)**:
  same canonical hash (project_id is metadata, NOT part of the
  chain — Phase 2 §3.2 invariant preserved). 16 existing Write
  callers stay unchanged; their rows get `project_id='default'`
  via column DEFAULT.
- **`project_id` column added to 5 tables**:
  `agent_memory`, `audit_log`, `sdd_evaluations`, `vibe_specs`,
  `vibe_artifacts` + 5 indexes. Idempotent migration.
- **8 unit tests** + shared test setup.

#### 1.4.2 Chunk 4.2 — surface (commit `7d3cdee`)

- **4 NEW MCP tools** (42 → 46):
  - `dark_memory_project_create` — idempotent on `project_id`,
    rejects reserved ids ('default', 'dark'), rejects invalid
    kebab-case. INV-1 audit row emitted.
  - `dark_memory_project_lookup` — returns `{found: bool, project: null}`
    on not-found (matches `agent_memory_get` wire shape).
  - `dark_memory_mindset_apply` — **STUB**. Canned system_prompt
    that names vibe_case + task_description. `stub_notice` field
    makes MVP nature observable. Full implementation: alpha.18
    (when v4 LLMClient is wired into a v4 MCP tool).
  - `dark_memory_delegate_intent` — **STUB**. Always returns
    `decision='inline'` + `subtasks=[]`. Full DECIDE→PLAN→MIND→
    CURATE pipeline: alpha.18.

#### 1.4.3 Chunk 4.3 — hard isolation (commit `badb1a2`)

INV-19 namespace primitive enforcement. Phase 2 §3.2 hash chain
invariant preserved (project_id is metadata, NOT part of the
hash). The 3 isolation surfaces:

1. **audit / agent_memory / session** (Chunk 4.3 §A):
   - `audit.Writer.WriteExecWithProject` (NEW sibling of
     WriteExec, threads project_id inside the tx).
   - `agent_memory.writeAuditWithProject` + `judge.writeAuditExecWithProject`
     (local helpers; centralise the policy in one place).
   - `session.Store.projects` field (`ProjectValidator` interface,
     nil-safe). Start now calls `projects.Lookup` BEFORE INSERT;
     missing project → `ErrUnknownProject` (NEW sentinel error).
   - Session audit emission uses `WriteWithProject`.
   - **`session_start` defaultProjectID**: `'dark-memory-v4'`
     (legacy) → `'default'` (the seeded catch-all).

2. **vibe / judge** (Chunk 4.3 §B):
   - `vibe.Artifact.ProjectID` + `ArtifactStore.Insert/Get`.
   - `vibe.Pipeline.Publish` uses `writeAuditWithProject` helper.
   - `judge.Evaluation.ProjectID` + `SaveEvaluation` /
     `SaveConsensusSamples` INSERTs include `project_id`.
   - `judge.Store.ConfidencesByProjectProviderTarget` (NEW): the
     project-scoped sibling of `ConfidencesByProviderTarget`.
     Empty projectID rejected (project-scoped queries must carry
     scope).

3. **transport / calibration** (Chunk 4.3 §C):
   - `populateCalibration` project-scoped first, falls back to
     global when project has `< 50` samples (cold start).
   - `transport/mcp/server.go` wires `sessionStore.SetProjectsForTest`
     via `projectStoreValidator` adapter (avoids session→project
     import cycle).
   - `project.ApplyProjectIDColumns` now skips tables that don't
     exist (subset-boot tolerance; production unaffected).

#### 1.4.4 Verified

- `go vet ./...` clean.
- **12 v4alpha packages PASS** (audit, agent_memory, docs_index,
  judge, manifest, project, research, security, session, store,
  transport/mcp, vibe).
- **14 new isolation tests** pass + all pre-Phase-4 tests still pass.
- v4 binary rebuilt: **19.66 MB** (was 19.5 MB).
- Cross-version lockstep hash pin unchanged (audit chain
  backward-compatible — pre-Phase-4 audit rows still verify).
- Tool count: **46** (was 42).
- Atomic mirror: 1 SUMMARY pinned (row 2225) + 4 SECTION
  (rows 2226-2229).

#### 1.4.5 Backwards-compat note

`session_start` default `project_id` switched from
`'dark-memory-v4'` to `'default'` (literal). Callers that
relied on the legacy literal must pass it explicitly AND
register it via `project_create`, or accept `'default'`. Wire
shape unchanged.

#### 1.4.6 Phase 5 preview (ADR-013/014/015 memory subsystem)

Gated on OD2=YES (operator approves vector retrieval). If
approved: hybrid FTS5 + vector retrieval (ADR-013, Cormack 2009
RRF k=60), temporal re-ranking (ADR-014), multi-hop graph
(ADR-015). ~1,280 LoC, 3-4 weeks.

### ✅ Registered (46)

| Namespace | Tools | Count | When |
|---|---|---|---|
| **Health** | `health_ping` | 1 | BUG-7 |
| **Session** | `start`, `close`, `status`, `resume`, `heartbeat` | 5 | BUG-7 + BUG-8 |
| **Agent memory** | `save`, `recall`, `list`, `get`, `update`, `archive` | 6 | BUG-7 + BUG-8 |
| **Observability** | `memory_state`, `writes`, `anomalies` | 3 | BUG-8 |
| **Error obs** | `summary`, `list`, `get`, `resolve` | 4 | BUG-8 |
| **Policy** | `active_policy`, `load_constitution` | 2 | BUG-8 |
| **Vibe** | `spec`, `publish`, `pipeline_status`, `resolve_drift` | 4 | BUG-8 |
| **Judge** | `judge`, `consensus`, `judgment_history`, `judge_list_personas` | 4 | ADR-007 C2 |
| **Judge util** | `normalize`, `validate_overrides`, `pattern_descriptions`, `verify`, `verify_hash`, `trace`, `validate_trace` | 7 | BUG-10 10a |
| **Research** | `topic`, `recall`, `resume_thread` | 3 | BUG-10 10a |
| **Summarize** | `summarize_session`, `skill_loaded` | 2 | PRE-1 C4 |
| **Audit verify** | `audit_verify` | 1 | Phase 2 (alpha.15) |
| **Project** ⭐ NEW | `project_create`, `project_lookup` | 2 | **Phase 4 Chunk 4.2 (alpha.17)** |
| **Mindset** ⭐ NEW (STUB) | `mindset_apply` | 1 | **Phase 4 Chunk 4.2 (alpha.17)** |
| **Delegation** ⭐ NEW (STUB) | `delegate_intent` | 1 | **Phase 4 Chunk 4.2 (alpha.17)** |

### ⏳ Deferred to BUG-10+ (11)

| Namespace | Tools | Count | Defer reason |
|---|---|---|---|
| **Context** | `artifact_context`, `spec_context`, `session_context`, `recall` | 4 | Reads from vibe pipeline tables (already exist) |
| **Agent bootstrap** | `bootstrap`, `recommend_companions`, `detect_environment` | 3 | Needs embedded `dark-memory://docs/*` resources |
| **L6-VLP** | `vlp_handle_event` | 1 | Needs state machine FSM |
| **Red-team** | `list_mods`, `get_prompts`, `log_attempt` | 3 | Env-gated (`DARK_REDTEAM=armed`); needs mod loader |
| **Admin** | `vacuum` (migrate + schema_status are in cmd/) | 1 | SQL `VACUUM` + retention policy |
| **Agent memory c2** | `delegate`, `entities`, `subagent_register`, `subagent_unregister` | 4 | Needs C2 subagent binding + Mem0 entity extraction |
| **Session c2** | `recover`, `resurrect` | 2 | INV-8 contract + closed_aborted discovery |

---

## 2. Actual package layout (`internal/v4alpha/`)

`ARCHITECTURE-V4.md §5 (original)` describes the aspirational layout:
`core/`, `vibe/`, `security/`, `adapters/`, `governance/`,
`constitutions/`, `transport/`, `tools/`, `installer/`,
`extensions/`.

**The actual layout** (2026-09-27):

```
internal/v4alpha/
├── agent_memory/    # Save, Get, List, Recall, Archive, Update + FTS5
├── audit/           # write_audit schema + Writer + WriteExec + ListFilters
├── judge/           # Verdict + Pipeline + LLMJudge + Consensus + EdgeCases + PersonaContent + sdd_evaluations Store
                    # (modernc.org/sqlite v1.53.0 — pure-Go, no cgo; see https://pkg.go.dev/modernc.org/sqlite)
├── manifest/        # cap_store + cap_token + manifest (RBAC layer)
├── research/        # (placeholder — no tools yet, BUG-9)
├── session/         # Store (open/read/heartbeat/close) + property tests
├── store/           # OpenSQLite, WithTx, store_test (INV-16)
├── transport/
│   └── mcp/         # mcp-go wrapper: server.go + 8 tool files
└── vibe/            # Spec/Artifact/Drift stores + Pipeline
```

### What is NOT yet built (deferred)

| Aspirational | Status | When |
|---|---|---|
| `core/` (pure types) | Inline in `vibe/` and `agent_memory/` for now | alpha.2 — extract when 3rd package needs the same type |
| `security/` (redact, ssrf, pii, injection, capability) | NOT STARTED | alpha.3 — INV-11..INV-15 land as `internal/v4alpha/security/` |
| `adapters/` (LLM, credentials, embedding, update) | NOT STARTED | alpha.3 — when first LLM client lands |
| `governance/` | Inline in `vibe/` + `judge/` | alpha.2 |
| `constitutions/` | Hardcoded in `transport/mcp/policy.go` | BUG-9 — `policy_registry` table |
| `tools/` (manifest-based auto-discovery) | `transport/mcp/*` files | M3 aspirational; deferred past alpha.2 |
| `installer/` | Lives in `npm/wrapper/` (legacy v2) | Carry over; not v4-specific |
| `extensions/` | NOT STARTED | alpha.3+ — community pack loader |

---

## 3. Invariants — adoption status

The original `docs/INVARIANTS.md` defines INV-1..INV-10 (v3, still
in force). v4 adds INV-16 (dark-db concurrency) and INV-17 (FTS5
ordering).

| Invariant | Adopted in v4? | Where | When |
|---|---|---|---|
| INV-1 (write-path audit) | **YES (fully closed)** | `agent_memory.Save/Update/Archive` emit `audit_log` row in same Tx (C3); `error_resolve` audit on; `judge.Pipeline.Evaluate` emits `sdd_evaluations` row + audit | **ADR-007 C3 (this release)** |
| INV-2 (per-session scoping) | YES | `agent_memory.Recall` filters by `operator`; cross-session by default | inherited |
| INV-3 (canary check) | DEFERRED | not in `internal/v4alpha/` | alpha.2 — when first research adapter lands |
| INV-4 (constitution SHA) | YES (light) | `transport/mcp/policy.go::loadConstitution` reads + returns hash | full watchdog (refuse-migrate-on-drift) deferred |
| INV-5 (cache re-hash on Get) | DEFERRED | not in v4-alpha.1 | alpha.2 — when LLM cache adapter lands |
| INV-6 (mod sanitization) | DEFERRED | mod loader is in `internal/tools/` (v3), not yet ported | alpha.3 |
| INV-7 (per-project scoping) | YES | every `Save`/`List`/`Get`/`Archive` filters by `operator`; the legacy `projects` table is replaced by `operator` as the tenant primitive in v4 | see ARCHITECTURE-V4.md §6.4 (planned) |
| INV-8 (per-MCP DB isolation) | YES | `cmd/dark-memory-v4` defaults to operator-configurable DSN; `defaultDSN` defaults to `dark-memory.db` (not `dark.db`) | inherited |
| INV-9 (reserved) | YES (reserved) | — | — |
| INV-10 (agent_memory lifecycle) | YES | rows survive session close; no auto-bind | inherited |
| **INV-11** (capability token) | NOT STARTED | alpha.3 | alpha.3 — when transport auth lands |
| **INV-12** (audit chain) | **YES (hash chain only)** | `internal/v4alpha/audit/canonical.go` (SHA-256 chain); `audit.Verify()`; `dark_memory_audit_verify` MCP tool | **Phase 2 / alpha.15** — Ed25519 (ADR-017) deferred; Merkle tree deferred to alpha.3 |
| **INV-18** (judge calibration) | **YES** (bootstrap-CI) | `internal/v4alpha/judge/calibration.go` (`BootstrapCI`, `ShouldRecalibrate`); `Server.populateCalibration()` hook; `judge.ApplyCalibrationColumns` migration | **Phase 3 / alpha.16 (this release)** — Play Favorites (arxiv:2508.06709) §4.3 percentile method; deterministic seed=42; n=1000 default |
| **INV-13** (redact-before-log) | NOT STARTED | alpha.3 | alpha.3 — when first OSINT adapter lands |
| **INV-14** (SSRF guard) | NOT STARTED | alpha.3 | alpha.3 — when first URL-fetching tool lands |
| **INV-15** (prompt injection scan) | NOT STARTED | alpha.3 | alpha.3 — when first URL-fetching tool lands |
| **INV-16** (dark-db concurrency) | **YES** | `store.OpenSQLite` + `store.WithTx` (`WAL + busy_timeout=5000ms + MaxOpenConns=8`; upstream docs: <https://sqlite.org/wal.html>) | BUG-5 (this release) |
| **INV-17** (FTS5 ordering) | **YES** | `agent_memory.Update` ordering (DELETE fts → UPDATE base → re-read → INSERT fts) under SERIALIZABLE; schema stays contentless | BUG-8 (this release) |
| **INV-19** (namespace primitive) | **YES (soft, NOT multi-tenant)** | `internal/v4alpha/project/` package (`projects` registry, `ApplyProjectIDColumns` migration); `audit.Writer.WriteWithProject` + `WriteExecWithProject` (project_id is metadata, NOT part of the chain); `session.Store.Start` validates project_id existence (INV-7 hard isolation at the session boundary); `vibe.Artifact.ProjectID` + `judge.Evaluation.ProjectID` thread project_id into INSERT + audit; `judge.Store.ConfidencesByProjectProviderTarget` (project-scoped calibration). Per `sota-critique.md §7.6.9`: HARD isolation is `coexistence_group` (per-MCP dark.db), NOT `project_id`. | **Phase 4 (alpha.17, this release)** |

---

## 4. Pipeline vs workflow runtime

`ARCHITECTURE-V4.md §8` describes a `Workflow` struct with mutable
States/Events/Transitions driven by an orchestrator via
`modify_workflow`. **This is aspirational.** What v4-alpha.1 ships is
a fixed FSM:

```
spec_create → artifact_log → drift_judge → drift_log → (aligned | drift_detected | needs_human)
```

Implemented in `internal/v4alpha/vibe/pipeline.go`:

- `SpecStore`, `ArtifactStore`, `DriftStore` (3 separate stores, one
  per state type)
- `Pipeline.Publish(ctx, spec, artifact) → Verdict`
- `Pipeline.Status(artifact_id) → DriftReport`
- `Pipeline.ResolveDrift(drift_id, decision, note) → void`
- `Judge` interface (`Evaluate(ctx, artifact_ref, spec_intent) →
  Verdict`) — LLM-backed `LLMJudge` shipped in **ADR-007 commit 2**.
  Provider allow-list extended in **Phase 3 (alpha.16)** from 4
  (anthropic, minimax, minimax-cn, deepseek) to 9 (added openai,
  google, zhipu, moonshot, qwen). Mistral deferred (not in
  `internal/llm/catalog.go` yet).
  `NoOpJudge` remains as the fallback when no provider key is set.

### 4.1 Judge pipeline v4 (ADR-007, 4 commits shipped)

| Commit | Title | Status |
|---|---|---|
| C1 (commit `8e1af1e`) | Judge pipeline interfaces + 30 EC fixtures | ✅ shipped |
| C2 (commit `4f860ed`) | LLM-backed Judge + Consensus + 4 new MCP tools | ✅ shipped |
| C3 (commit `b01caea`) | Audit emission + INV-1 closure + `sdd_evaluations` persistence (18 cols + 4 indexes) | ✅ shipped |
| C4 (commit pending) | Operator-facing docs (`judge-pipeline-v4.md`, `edge-case-catalog.md`, `persona-registry-v4.md`, status update, changelog) | ✅ shipped (docs-only) |

Docs:
- `docs/judge-pipeline-v4.md` — operator's guide
- `docs/edge-case-catalog.md` — 15 ECs (ADR-007) + 1 added in alpha.16 (EC-007b) = **16 total** + how to extend
- `docs/persona-registry-v4.md` — 11 personas + override mechanism

The mutable workflow runtime (Workflow struct + modify_workflow
event + drift_judge of modifications) lands in v4.0.0-beta at the
earliest.

---

## 5. Live verification

The `cmd/dark-memory-v4 serve` binary accepts JSON-RPC over stdio.
Verified end-to-end on Windows (2026-09-27):

```bash
$ dark-memory-v4 migrate --dsn=/tmp/v4-live.db
  ok  audit
  ok  session
  ok  manifest/cap
  ok  manifest/meta
  ok  vibe/spec
  ok  vibe/artifact
  ok  vibe/drift
  dark-memory-v4: migrate ok at version v4alpha/2026-09-27/001

$ printf "%s\n" \
    '{"jsonrpc":"2.0","id":1,"method":"initialize",...}' \
    '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
    '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | dark-memory-v4 serve --dsn=/tmp/v4-live.db
→ 25 tools registered (full list in §1 above)
```

---

## 6. What you can rely on

| Reliability | What's stable |
|---|---|
| ✅ Stable (won't change) | Tool wire names, agent_memory schema, FTS5 ordering (INV-17), worker pool size=1, store/WithTx contract (INV-16), `sdd_evaluations` schema (18 → 22 cols; +4 for calibration in alpha.16), 4 judge MCP tool wire shapes, persona registry ids, `BootstrapCI` deterministic seed=42 |
| ⚠️ Likely to evolve | Package names (still aspirational vs actual drift), Pipeline API (LLM judge swap), Constitution (still hardcoded), persona override mechanism (spec 1155 v14 inheritance) |
| ❌ Not implemented | security/* (INV-11..15), mutable Workflow, red-team mods, 6 `judge_util_*` tools, research/mindset/delegation/project/context/bootstrap/L6-VLP/admin/agent_memory-c2/session-c2 |

## 6.5. SOTA-doc workstream (2026-09-28, 6 of 7 chunks shipped)

The SOTA-doc workstream is the **criticism** axis of the
v4-alpha mandate ("retomar el trabajo de v4, respecto a
la documentación y crítica SOTA"). It shipped 6 of 7
chunks as a doc-only release:

- `b080e91` — chunk 1: judge pipeline SOTA (Prometheus 2,
  Play Favorites, G-Eval, MT-Bench).
- `e176f2f` — chunk 2: agent memory SOTA (MemGPT, Mem0,
  Letta, A-MEM).
- `5e35e30` — chunk 3: audit chain INV-1 SOTA (Rekor, immudb,
  in-toto, Trillian, CT).
- `8c8a7de` — chunk 4: workflow runtime M1 SOTA
  (LangGraph, LlamaIndex, DSPy, AutoGen, CrewAI, Temporal).
- `0b11a67` — chunk 5: MCP + spec-driven SOTA
  (Anthropic MCP, Pydantic, Datomic, CUE, Effect).
- `fbec6e2` — chunk 6: meta-doc `docs/sota-critique.md`
  (13 ahead / 28 on-par / 39 behind, 17 ADRs + 1 BUG).
- `6b4daae` + this commit — chunk 7: workstream close
  + alpha.11 plan + namespace reframe.

**Aggregate**: +2,380/-7 lines across 12 file operations.
20 honest "couldn't verify" findings (4 per chunk × 5
chunks, none papered over). 17 ADRs + 1 BUG proposed as
remediation. 5 ADRs (020-024) explicitly marked
out-of-v4-alpha-scope (architectural decisions deferred
to beta/GA).

**Per-chunk atomic mirrors** (per ADR-008):
- chunk 1: rows 2137 (SUMMARY) + 2138-2142
- chunk 2: rows 2143 (SUMMARY) + 2144-2148
- chunk 3: rows 2149 (SUMMARY) + 2150-2154
- chunk 4: rows 2155 (SUMMARY) + 2157-2160
- chunk 5: rows 2161 (SUMMARY) + 2162-2165
- chunk 6: rows 2166 (SUMMARY) + 2167-2169
- chunk 7: row 2174 (SUMMARY) + 2175-2179 (this commit)

**Next**: alpha.11 plan — 5 vibe-loops, ~3,320 LoC,
8-11 weeks. See `docs/v4-alpha-11-plan.md` and
`docs/sota-critique.md` §7.6 + §7.6.9.

## 6.6. Namespace primitive threat model (per row 2173, ENFORCED in alpha.17)

> v4 assumes the harness session is the only concurrent
> consumer. Project IDs scope workstreams within one
> operator. For HARD isolation between concurrent users,
> use separate `coexistence_group`s or separate MCP
> instances. The `project_id` column is a soft namespace,
> not a security boundary.

- **Hard isolation primitive**: `coexistence_group`
  (per-MCP `dark.db`). Production-grade.
- **Soft separation primitive**: `project_id` column
  (BUG-10 10b's namespace registry — **ENFORCED in
  alpha.17 / Phase 4** per `INV-19`).
- SOTA pattern is consistent: Notion workspace=hard,
  page=soft; GitHub org=hard, repo=soft; Snowflake
  account=hard, schema=soft.

This statement is the canonical threat model for v4. BUG-10
10b's ADR-025 (or similar) is the "Namespace Primitive"
ADR, NOT a "Multi-Tenant Primitive" ADR.

**Phase 4 enforcement** (alpha.17, commit `badb1a2`):
- `session.Store.Start` rejects unknown `project_id` with
  `ErrUnknownProject` (INV-7 hard isolation at the session
  boundary).
- Every audit-emitting surface (agent_memory, vibe, judge)
  threads `project_id` via `audit.Writer.WriteWithProject`
  / `WriteExecWithProject`. Phase 2 §3.2 hash chain
  invariant preserved — `project_id` is metadata, NOT
  part of the hash.
- `judge.Store.ConfidencesByProjectProviderTarget` filters
  the calibration pool by `project_id`. Cold-start fallback
  to global pool preserves the `ShouldRecalibrate(N)`
  invariant.

---

## 7. Tier-1 sources (verified 2026-09-27)

Every external claim in this status doc has a verifiable primary
source. The operator-facing docs (`docs/judge-pipeline-v4.md` §9,
`docs/edge-case-catalog.md` §7, `docs/persona-registry-v4.md` §8)
carry the full reference list with URLs, dates, and license info.
The summary below points to the upstream sources cited in this doc.

| Claim | Source | URL |
|---|---|---|
| INV-16 dark-db concurrency uses `journal_mode=WAL` + `busy_timeout=5000ms` + `MaxOpenConns=8` (WAL readers-don't-block-writers, 1000-page auto-checkpoint, 3.51.3 fixes WAL-reset bug) | SQLite WAL docs | <https://sqlite.org/wal.html> |
| `internal/v4alpha/store/` is backed by the pure-Go driver (no cgo) | modernc.org/sqlite v1.53.0 (pinned in `go.mod`, verified via `grep`) | <https://pkg.go.dev/modernc.org/sqlite> |
| Judge pipeline (29 tools shipped, 4 commits, 16 ECs [15 original + EC-007b added alpha.16], 11 personas) is grounded in SOTA LLM-as-judge literature | `docs/judge-pipeline-v4.md` §9 (4 papers + Anthropic structured outputs + SQLite WAL + pkg.go.dev) | — |

---

## 8. Where to read next

- `ARCHITECTURE-V4.md §5 (revised)` — current package layout
- `ARCHITECTURE-V4.md §6 (revised)` — invariant adoption
- `docs/INVARIANTS.md` — INV-1..INV-17 definitions (INV-16 + INV-17 added 2026-09-27)
- `docs/AGENT_MEMORY_SCHEMA.md` — table shape, FTS5, indexes
- `docs/judge-pipeline-v4.md` — operator's guide to the judge (ADR-007)
- `docs/edge-case-catalog.md` — 16 ECs (alpha.16 split EC-007 into 007a + 007b) + how to extend (ADR-007 §4)
- `docs/persona-registry-v4.md` — 11 personas + override mechanism (ADR-007 §2.2)
- `CONSTITUTION-V4.md` — release-integrity constitution (v4 fork)
- `docs/sota-critique.md` — meta-doc SOTA criticism (5 chunks
  aggregated: 13 ahead / 28 on-par / 39 behind, 17 ADRs + 1 BUG,
  20 honest couldn't-verify). §7.6 has the alpha.11+ roadmap;
  §7.6.9 has the namespace primitive threat model.
- `docs/v4-alpha-11-plan.md` — 5 vibe-loops for the next 8-11
  weeks (Phase 1-5: close + cheap wins / audit chain / judge
  improvements / BUG-10 10b namespace / memory subsystem).
  Phase 1-4 shipped (Phase 4 = BUG-10 10b namespace primitive,
  3 commits: 1d39659, 7d3cdee, badb1a2); Phase 5 (memory
  subsystem, ADR-013/014/015) to follow — gated on OD2=YES.
- `docs/specs/SPEC-alpha-11-*.md` — the per-phase vibe-loop
  specs (chunk 7 + pre1c3 + pre1c4 + Phase 2 + Phase 3 ship).
- `CHANGELOG.md` (top of file) — entry `[4.0.0-alpha.16]`
  (Phase 3: judge improvements; provider allow-list + bootstrap-CI).
- `docs/decisions/ADR-007-judge-pipeline-v4.md` — design ADR
- `docs/decisions/ADR-008-work-standard.md` — atomic mirror discipline
- `docs/archive/v3.0-wave-4/` — v3 context (legacy, kept for reference)
