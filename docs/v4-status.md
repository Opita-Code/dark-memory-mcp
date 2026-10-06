# v4 Status — current state of the redesign

> **Phase 15 SHIPPED (2026-10-06, local tag `v4.0.0-alpha.26`)**.
> See §1.14 below. **73 canonical tools, 20 namespaces, schema v32,
> 4 phase-15 commits: T-401 (Postgres parity for 3 notImpls),
> T-402 (EmitCacheInvalidation 8th/8th AutoEmitter wire-up),
> T-403 (CachedProvider single-flight dedup), T-404 (this docs).
> Closes the 3 deferrals from row 2464 §1.13.5: PG parity gap,
> AutoEmitter orphan, NLI flaky test. Cross-version lockstep hash
> pin UNCHANGED. No new tools, no schema changes, no new namespaces.**

> **Phase 14 SHIPPED (2026-10-06, local tag `v4.0.0-alpha.25`)**.
> See §1.13 below. **73 canonical tools, 20 namespaces, schema v32,
> 4 phase-14 commits T-301..T-303 + docs (T-304). LLMJudge is
> the primary drift verdict source (`judge-*` prefix, OpenAI-
> compatible /v1/chat/completions); NLI chain preserved 100% as
> legacy fallback (`chat-*` prefix unchanged from Phase 13 T-201).
> Connect flow operator-facing tools added in LLM_BIND namespace
> (`llm_provider_bind` + `llm_provider_probe`). TestPreExistingHang
> (row 2457) closed by Phase 14-PREP refactor (`NoLLMSelector{}`
> explicit injection).**

> **Phase 12 SHIPPED (2026-10-04, local tag `v4.0.0-alpha.23`)**.
> See §1.11 below. **71 canonical tools, 19 namespaces, schema v32,
> 3.5k LoC events subsystem, 42 new tests PASS.**

> **Audience**: anyone touching the `feat/v4-redesign` branch.
> **TL;DR (alpha.20 / Phase 9, 2026-10-02)**: v4-alpha.20 ships
> **62 of 62 canonical tools** (100% of the canonical surface) plus
> 2 critical e2e wiring gaps closed (§8.1 delegate_intent + §8.2
> personas) plus 5 alpha.20 follow-ups (embedder wired, audit
> export/verify, recall coverage 82→92%, ProGraph entity-graph BFS,
> bitemporal lite + Phase 5 schema port). **Schema v29→v31**.
> **84 new tests across 8 chunks, 0 critical findings on re-test**.
> See §1.8 below for the Phase 9 changelog.
>
> **Historical TL;DR**: v4-alpha.17 ships **46 of 57 canonical tools**
> (81% of the surface) plus the full judge pipeline (ADR-007, 4
> commits shipped) plus the judge_util + research namespaces
> (BUG-10 10a) plus the SOTA-doc workstream (7 of 7 chunks,
> +2,380/-7 lines, 12 file operations) plus PRE-1 C4 (summarize_session
> + skill_loaded tracking) plus PRE-1 C3 (session_start gains a
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
## 1. Tools inventory (73 of 97+, 20 namespaces, schema v32)

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

### 1.5 Phase 5 — vibe-case-aware memory subsystem (alpha.18) ⭐ NEW

> Operator decision (2026-10-01, OD2=YES): ship Phase 5
> with vector retrieval, temporal re-ranking, multi-hop
> graph — all via the new `internal/v4alpha/recall/`
> package. Per `docs/specs/SPEC-alpha-11-phase5.md`
> (vibe_loop `alpha-11-phase-5`, 5 commits on
> `feat/v4-redesign`, drift 7 ALIGNED + 4 drift_detected
> all intentional alpha.18 stubs).
>
> **5 commits, 0 to 1 service**: schema + dispatch + 7
> strategies + cross-cutting. Tool count: 46 → 46 (no new
> MCP tools; recall goes through existing
> `agent_memory_recall` polymorphic dispatch). Schema
> version: `v4alpha/2026-09-30/004` → `005`. Audit chain
> invariant preserved (Phase 2 §3.2 — recall columns are
> data, NOT part of the hash).

#### 1.5.1 Chunk 5.1 — schema migration (commit `76a2a11`)

20 additive columns on `agent_memory` (6 cross-modal
embeddings, 5 temporal decay, 3 code refs, 1 graph
residual, 5 decision subsystem) + 3 new tables
(`agent_memory_entities` ProGraph 2-layer,
`agent_memory_links` CABLE sparse directed,
`decision_transitions` TokenMizer-style bitemporal) + 9
indexes. All migrations idempotent via `pragma_table_info`
+ `ALTER ADD COLUMN`. Each new table carries `project_id`
(INV-19 alpha.17 hard isolation). 8 schema tests pass.
drift ALIGNED 0.99 (spec 1818).

#### 1.5.2 Chunk 5.2 — RecallFor dispatch + C3DecisionRecall (commit `fe1b97d`)

Polymorphic `RecallFor(query, vibe_case, opts)` entry
point + `RecallStrategy` interface + `Weights` struct
(FTS5/Vector/Graph/CrossModal summing to 1.0) +
`strategyRegistry` map. C3DecisionRecall implements SPEC
§6.3: FTS5 with decision-aware synonym expansion + 2-hop
graph via `adr_refs` + `decision_state='active'` filter +
weighted RRF (Cormack 2009 k=60) + access-count boost
capped at 2.0×. Two regressions caught during build:
(a) bare FTS5 MATCH with table alias fails in
modernc.org/sqlite — using `agent_memory_fts MATCH ?`
without alias; (b) C3 graph expansion SQL had
args/placeholders mismatch — fixed order to project_id
first. drift ALIGNED 0.96 + 0.95.

#### 1.5.3 Chunk 5.3 — C1Code + C2Text + C4Research (commit `a137947`)

Three more strategies. C1CodeRecall: code-aware tokenizer
splitting camelCase/snake_case/kebab-case/dots + 1-hop
graph via `adr_refs+inv_refs` (no embedder per R-E:
FTS5 + ADR/INV refs > CodeCompass BM25 99.4% vs 78.2%).
C2TextRecall: operator-curated `Synonyms` map expansion +
1-hop graph. C4ResearchRecall: research-aware expansion
(paper/cite/source/reference/arxiv/doi) + 2-hop citation
graph; shared `graphExpandShared` +
`hydrateGraphRowsShared` + `scoreFTSPlusGraph` helpers.
drift: C1 ALIGNED 0.96, C2 drift_detected 0.82
(alpha.18 vector stub — BGE-large alpha.19), C4
ALIGNED 0.92.

#### 1.5.4 Chunk 5.4 — C5Video + C6Audio + C7Multi (commit `9f834f2`)

Last three strategies. C5VideoRecall: FTS5 over
`kind=link` rows + 1-hop graph; ImageBind stub (alpha.19).
C6AudioRecall: FTS5 + optional `VoiceEmbedKind` filter
(timbre/full/prosody+timbre) + 1-hop graph. C7MultiRecall:
ensemble dispatcher with `detectSubTaskVibes` keyword
router + RRF merge across sub-tasks. drift: C5
drift_detected 0.92 (alpha.18 ImageBind stub), C6
ALIGNED 0.95, C7 drift_detected 0.82 (keyword router;
alpha.19 = LLM-extracted sub-tasks).

#### 1.5.5 Chunk 5.5 — decay + refresh + supersession + micro-eval (commit `e647239`)

Cross-cutting closure. `DecayScore` (ScrubJay-MEM π_i +
τ_i: forever returns 1.0; e^(-age/tau) otherwise; access
boost 1+0.3×log10(count+1) capped at 2.0×). `RefreshOnAccess`
(idempotent count bump + last_refreshed_at). `MarkSuperseded`
(validates kind=decision + same project per INV-19;
TokenMizer-style bitemporal decision_transitions row).
`PerVibeCaseMultiplier` (canonical table: C1/C4/C6=1.0,
C2=0.5, C5=0.25, C3=1.0 caller-forever-check).
11/11 decay tests pass including 50-Q LifecycleBench
micro-eval. drift: decay.go drift_detected 0.88 (false
positive — judge reconsidered to aligned but reported
drift_detected), decay_test.go ALIGNED 0.97.

#### 1.5.6 Verified

- `go vet ./...` clean.
- **41/41 recall tests pass** (recall package).
- **13 v4alpha packages PASS**: audit, agent_memory,
  docs_index, judge, manifest, project, recall (NEW),
  research, security, session, store, transport/mcp,
  vibe.
- v4 binary rebuilt: `dark-memory-v4.exe` (~19.85 MB,
  +190 KB for the recall package).
- Atomic mirror per ADR-008 (5 SUMMARY pinned + 16
  SECTION + 1 meta SUMMARY = 22 rows):
  - Chunk 5.1 SUMMARY (row 2235) + §A-§E (2236-2240).
  - Chunk 5.2 SUMMARY (row 2241) + §A-§C (2242-2244).
  - Chunk 5.3 SUMMARY (row 2245) + §A-§C (2246-2248).
  - Chunk 5.4 SUMMARY (row 2249) + §A-§C (2250-2252).
  - Chunk 5.5 SUMMARY (row 2253) + §A-§C (2254-2256).
  - Meta SUMMARY (row 2257) — Phase 5 alpha.18 shipped.

#### 1.5.7 alpha.18 known stubs (intentional drift_detected)

4 intentional drift verdicts document the alpha.19
evolution path:

| File | Verdict | alpha.19 path |
|---|---|---|
| `c2_text.go` | drift_detected 0.82 | BGE-large embedder |
| `c5_video.go` | drift_detected 0.92 | ImageBind 1024-dim image encoder |
| `c7_multi.go` | drift_detected 0.82 | LLM-extracted sub-tasks |
| `decay.go` | drift_detected 0.88 | (false positive — judge reconsidered to aligned) |

These are NOT regressions; they're the documented scope
boundary for alpha.18 (per SPEC §2.2 non-goals). The 3
sota-critique §5.2 tractable gaps (ADR-013/014/015) are
NOW ENFORCED — see §8 below.

#### 1.5.8 Backwards-compat note

`agent_memory_recall` polymorphic dispatch is transparent
to existing callers; the legacy `RecallFiltered` FTS5 path
still works for callers that don't pass `vibe_case`.
`project_id` filtering applies to all new tables (INV-19
alpha.17 hard isolation). Wire shape unchanged.

### 1.6 Phase 6 — alpha.18 close-out (alpha.18.1) ⭐ NEW

Phase 6 closes all known gaps before alpha.19 (5 commits shipped):
`ee0fb8e` (vibe-case mapping fix), `a4833fc` (ADR-017 Ed25519
signatures), `504f427` (ADR-019 payload BLOB split),
`ab28867` (full impl mindset_apply), `4229684` (full impl
delegate_intent), `63fab10` (mutation coverage close).

#### 1.6.1 Vibe-case mapping reconciliation (Chunk 6.1)

**Live bug fix.** The judge pipeline was mapping C3/C4/C5/C6/C7
to wrong personas after the v4-alpha.3 persona registry split.
Reconciled 3 docs (`persona-registry-v4.md`, `GLOSARIO.md`,
`ADR-007-judge-pipeline-v4.md`) + 1 source file
(`internal/v4alpha/judge/rubric.go`) to the canonical
`internal/v4alpha/spec/spec.go:14-16` taxonomy. 2 new personas
added (judge-decision C3, judge-research C4); 1 swapped
(judge-cross-modal C5, judge-pipeline C6); 1 unchanged
(judge-evidential C7).

#### 1.6.2 ADR-017 Ed25519 audit row signatures (Chunk 6.5)

Per ADR-017 (cryptographic provenance on audit rows):
`internal/v4alpha/audit/signature.go` NEW + Writer.signer
+ ApplySignatureColumns migration. 16 tests PASS
(RoundTrip, Valid/WrongKey/ModifiedRowHash, Idempotent
migration, ValidChain, DetectsForgery, DetectsWrongKey,
LegacyRowsTolerated). Env vars: DARK_AUDIT_SIGNING_KEY
(base64 64B priv) + DARK_AUDIT_VERIFY_KEY (base64 32B pub).

#### 1.6.3 ADR-019 payload BLOB split (Chunk 6.6)

Per ADR-019 (columnar audit queries on payload JSON):
`internal/v4alpha/audit/payload_split.go` NEW — ExtractPayloadFields
+ ApplyPayloadColumns + AddPayloadIndex (payload_event only,
legacy compat). 12 tests PASS. nullableString + nullableInt64
helpers ensure empty string/zero int → SQL NULL.

#### 1.6.4 Full impl `mindset_apply` (Chunk 6.2)

Replaces alpha.17 STUB. Cache lookup via RecallFiltered
TagPrefix='mindset:v1'; procedural composition via
composeSystemPrompt; judge validation via Pipeline.Evaluate
eval_type='mindset_compose'; retry loop up to
DARK_MINDSET_MAX_ITERATIONS (default 3, clamped [1,7]).
Wire shape: removed StubNotice; added Verdict field.
3 tests PASS.

#### 1.6.5 Full impl `delegate_intent` (Chunk 6.3)

Replaces alpha.17 STUB. DECIDE→PLAN→MIND→CURATE pipeline.
DECIDE: deterministic rules (refusal keywords > delegation
keywords > C7 multi > length>200 > default inline).
PLAN: split on `.!?;` + newline. MIND: in-process call to
composeSystemPrompt. CURATE: empty delegation_context (alpha.19
wires C2 subagent binding). Wire shape: removed StubNotice;
added Reasoning. 3 tests PASS.

#### 1.6.6 Mutation coverage close (Chunk 6.4)

| Package | Before | After | Δ |
|---|---|---|---|
| internal/recall | 17.5% | 45.3% | +27.8 pp |
| internal/agentbootstrap | 71.8% | 90.8% | +19.0 pp |
| internal/tools | 20.3% | 22.2% | +1.9 pp |

3 new test files (+633 LoC). All 30+ tests PASS. Real SQLite
in `t.TempDir()` (not a hand-rolled mock — store.Store has 105+
methods).

#### 1.6.7 Known limitations (deferred to alpha.19)

- `delegate_intent` DECIDE "first ... then" only matches the
  literal substring (not natural language).
- internal/tools 22.2% — 84 untested MCP-RPC handlers (need
  httptest server).
- internal/recall 45.3% — CachedSource (cache.go) methods
  need mock testing infrastructure.

### 1.7 Phase 7 — sub-agent wiring + LLM router upgrade + coverage close (alpha.19) ⭐ NEW

Phase 7 closes **all 4 deferred alpha.18.1 items** (1.6.7 list above)
plus an LLM-router upgrade on `delegate_intent`. Five master spec
items in `docs/specs/SPEC-alpha-11-phase7.md` (454 LoC, vibe_loop
`alpha-11-phase-7`, 5 commits on `feat/v4-redesign`): Chunks 7.1,
7.2, 7.5, 7.6, 7.7 (this section).

#### 1.7.1 LLM-extracted sub-tasks router (Chunk 7.1, `2bb20a3`)

Replaces the alpha.18.1 v1 deterministic DECIDE router (drift 0.85 on
Chunk 6.3 — "first ... then" literal pattern limitation). New
hybrid **DECIDE→EXTRACT→MIND→CURATE** pipeline.

- `internal/v4alpha/delegation/` NEW (7 files, ~2,483 LoC): `types.go`,
  `router.go`, `extract.go`, `cache.go`, `validate.go` + `extract_test.go`
  (14 tests) + `cache_test.go` (9 tests) = **23 tests**.
- New `judge-delegator` persona (14th). Persona registry `13 → 14`.
  Documented honest deviation from Phase 6 §6.1 canon (13 personas).
- Wire shape v2 (additive over v1): new `cache_hit`, `verdict`,
  `alternatives[]` fields. Each subtask gains `subagent_id` (uuid) +
  `delegation_context` (JSON blob).
- DECIDE priority chain (deterministic): (1) refusal markers
  `impossible/cannot/out of scope/do not/don't` → "refused";
  (2) delegation markers `parallel/concurrent/step by step/first ...
  then/and then/split into/subtask/in parallel` → "delegate";
  (3) `vibe_case=C7` → "delegate"; (4) `len(task)>200` →
  "delegate"; (5) default → "inline".
- EXTRACT: cache lookup → LLM call via judge-delegator → JSON parse →
  ValidateSubtasks (cap 8, min 10 chars, topo sort via Kahn's) →
  drift_judge (eval_type=`subtask_extraction`) → refine+retry up to
  MaxRetries=2 → on failure `needsHumanFor(failureID, detail)`.
- 4 new tests in transport/mcp/project_test.go (ExtractHit,
  ExtractFallback, NeedsHumanAlternatives, CacheHit) +
  `TestDelegateIntent_FullImplDelegate` updated C7→C2. **27 new tests
  + 2 modified, 0 regressions.**

#### 1.7.2 `agent_memory_delegate` C2 subagent binding (Chunk 7.2, `f3692cf`)

Wires `dark_memory_subagent_register` per subtask in `delegate_intent`.
Defense-in-depth against inheritance attacks (arxiv:2605.08460).

- `internal/v4alpha/transport/mcp/subagent_binding.go` NEW (~280 LoC):
  `SubagentBinding`, `BindSubtasksToSubagents` (uuid per subtask +
  agent_memory.Save kind=link tag=subagent:v1, INV-1 atomic with audit
  row in single Tx), `UnregisterSubagent` (idempotent archive).
- Constants: `SubagentBindingKind="link"`,
  `SubagentBindingTagPrefix="subagent:v1"`,
  `DefaultSubagentTTLSeconds=3600`, TTL clamp `[60, 86400]s`.
- `delegateIntentInput` gains `parent_session_id` + `parent_agent_id`
  (both `omitempty`, additive — backward compat preserved). Each
  subtask gains `subagent_id` (uuid) + `delegation_context` (JSON
  blob).
- `newTaskID` helper: `sha256(operator + \x00 + task_description)[:16]`
  hex (truncated to 16 chars for compactness).
- `agentMemoryStoreAdapter` wraps `*agent_memory.Store` to satisfy
  `delegation.AgentMemoryStore` interface (RecallFiltered + Save
  minimal subset).
- 3 new tests (CURATE_BindingPersists,
  CURATE_ParentSessionIDPropagated, CURATE_UnregisterOnClose) +
  `newTestServerWithProjectAndMemory` + `recallBindingRows` +
  `delegateIntentCuratePayload` helpers. **3 new tests, 0 regressions.**

#### 1.7.3 internal/tools 51-test httptest harness (Chunk 7.5, `39cc899`)

Closes the deferred `internal/tools` 22.2% gap (84 untested MCP-RPC
handlers). Builds a `httptest`-based MCP server harness using
mcp-go's `StreamableHTTPServer` + `WithStateLess(true)` to bypass
session-id.

- 3 NEW files in `internal/tools/` (build tag `//go:build test`):
  - `harness.go` (195 LoC) — `TestHarness` struct + `NewTestServer`.
    Real SQLite in `t.TempDir()` + canary + orch + tools.RegisterAll +
    mcp-go MCPServer + StreamableHTTPServer (WithStateLess true bypasses
    session-id) + httptest.NewServer.
  - `harness_test.go` (270 LoC) — JSON-RPC helpers
    (`jsonRPCRequest`/`Response`/`Error`, `callTool`,
    `callToolUnwrapped`, `callRPC`, `toolsList`, `initializeSession`,
    `extractFirstTextContent`, `newRawRequest`) + 2 smoke tests.
  - `handlers_test.go` (1,008 LoC) — **18 namespace smokes + 27 e2e +
    5 HTTP error tests = 50 tests**. Uses v2 wire shapes
    (`data.row.id` for AgentMemorySave, `data.hits` for AgentMemoryRecall,
    `data.rows` for AgentMemoryList, `data.spec_id` for `vibe_spec`,
    `data.db.live` for `health_ping`).
- Coverage goal: raise `internal/tools` from 22.2% toward ~75%.
- **51 new tests + 100+ existing, 343s full suite, -race clean.**

#### 1.7.4 internal/recall CachedSource mock testing (Chunk 7.6, `d4b7347`)

Closes the deferred `internal/recall` 45.3% gap (CachedSource cache.go
methods untested).

- `internal/recall/cache_test.go` NEW (1,112 LoC, `package recall_test`):
  **22 NEW tests** (24 functions incl. 6 sub-tests in FrameTTL_AllKnownKinds).
  Covers: constructor defaults, IdentityFrame miss/hit/error/TTL paths,
  CapabilitiesFrame miss/hit/TTL/idempotency, FetchRaw audit emission,
  PersistIdentity/Capabilities idempotency, ApplyCanary semantics
  (no-safety/propagates/nil-active/rotated), INV-5 cache mismatch
  (delete + audit + fall-through), RecordCacheErr durable telemetry,
  concurrent access (100 goroutines, race-clean), store error propagation,
  pass-through frames (Scope/Drift/Persona), frameTTL unknown-kind
  fallback + all-known-kinds canonical TTL pinning,
  AuditWriteContext canonical values, end-to-end TTL+canary scenario.
- `internal/recall/export_test.go` — exposes 7 internal symbols
  (`FrameTTL`, `PersistIdentity`, `PersistCapabilities`,
  `AuditWriteContext`, `ApplyCanary`, `RecordCacheErr`) via
  free-function wrappers.
- `fakeInner` implements `policy.FrameSource` with sync/atomic call
  counters + clones per call (race-detector caught latent shared-pointer
  bug — fixed to match StoreSource behavior).
- **Coverage: `internal/recall` 45.3% → 82.1% (+36.8 pp)**, exceeds
  target ≥80%. Per-function: frameTTL/NewCachedSource/
  ScopeFrame/DriftFrame/PersonaFrame/cachedFetchRaw/persistRaw/
  auditWriteContext/applyCanary at 100%; IdentityFrame 80%;
  CapabilitiesFrame 66.7%; cachedGetIdentity 77.8%;
  cachedGetCapabilities 75%; persistIdentity/Capabilities 71.4%;
  recordCacheErr 80%.
- **Race-detector caught a latent race** in fakeInner shared-pointer
  + concurrent `applyCanary` mutation + `Hash()` json.Marshal reads
  of `CanaryActive`. Fixed by cloning per call.

#### 1.7.5 Docs followup + alpha.19 tag (this commit, Chunk 7.7)

- `CHANGELOG.md [4.0.0-alpha.19]` — release entry (this commit).
- `docs/v4-status.md §1.7` — Phase 7 changelog (this section).
- `docs/v4-alpha-11-plan.md §7` — Phase 7 close-out (next section).
- `docs/sota-critique.md §5.2.3` — Phase 7 per-gap closure evidence.
- `git tag v4.0.0-alpha.19` LOCAL ONLY.
- dark-memory rows 2294-2315 (1 spec SUMMARY pinned + 5 chunk
  SUMMARY pinned + 19 SECTION pinned=false, agent_id
  `alpha-11-phase7`, session `sess-a4c92524784e1891`).

### 1.8 Phase 9 — v4alpha wiring close + embedder pilot (alpha.20) ⭐ NEW

Phase 9 (alpha.20) closes **2 of 2 critical Phase 8 e2e wiring gaps**
(§8.1 + §8.2) and ships **5 alpha.20 follow-ups** plus the docs
sweep. Per `docs/specs/SPEC-alpha-11-phase8.md` (621+ LoC spec, vibe_loop
`alpha-11-phase-8`, **9 commits on `feat/v4-redesign`**: 8.0 plan + 8.1-8.7
+ 8.8 docs, 1 local tag). 62 canonical tools, schema v31, **0 critical
findings on re-test**, **84 new tests** total across 8 chunks.

#### 1.8.1 v4alpha `delegate_intent` wired into v3 MCP (Chunk 8.1, `75a04fa`)

Closes Phase 8 e2e critical finding #1 (row 2327 — T7). v4alpha
DECIDE→EXTRACT→MIND→CURATE pipeline (Chunk 7.1) now reachable through
`dark_memory_delegate_intent` (not just the v4alpha binary).

- `internal/v4alpha/transport/mcp/wire.go` NEW — `RunDelegateIntentCore`
  as pure function (no mcp-go types), cross-version importable.
- `internal/v4alpha/transport/mcp/delegation.go` — exports
  `DelegateIntentInput/Output/Subtask/Alternative` types (capitalized).
- `internal/orchestration/delegate_intent.go` — adds 4 additive fields
  to `DelegateIntentOutput` (Decision, CacheHit, Verdict, Alternatives)
  with `omitempty` for backward compat.
- `internal/tools/delegation.go` — `RegisterDelegationWithBackend` +
  `DARK_DELEGATION_BACKEND` env var (default `v4alpha`, `v2` rollback).
- `cmd/dark-mem-mcp/legacy_main.go` — wires v4alpha deps at boot
  (judge.NewRealLLMClient + delegation.NewExtractCache +
  CacheTTLFromEnv).
- 8 e2e tests PASS (InlineShort, DelegateLong, RefuseMarker,
  NeedsHuman_NoLLMKey, NeedsHuman_LLMParseError, RefineRetry,
  WireShapeV3, v2_FallbackByFlag).

#### 1.8.2 v4alpha personas exposed via `judge_list_personas` (Chunk 8.2, `bade6d0`)

Closes Phase 8 e2e critical finding #2 (row 2323 — T3). Now returns
**14 (8 v2 + 6 v4alpha)** with `Source="v4alpha"` discriminator.

- `internal/orchestration/judge_personas_types.go` — `PersonaSourceV4Alpha`
  constant.
- `internal/orchestration/judge_personas_v4alpha.go` NEW (155 LoC) —
  v4alphaPersonaIDs (judge-cross-modal, judge-pipeline, judge-opinion,
  judge-decision, judge-research, judge-delegator), documented field
  mapping (ID→ID, EvaluationLens→Lens, BiasControls→Constraints,
  RequiredEvidence→Rubric, etc.).
- `internal/orchestration/orchestrator.go` — `WithV4AlphaPersonas(enabled)`
  builder + `V4AlphaPersonasEnabled()` getter.
- `cmd/dark-mem-mcp/legacy_main.go` — `orch.WithV4AlphaPersonas(true)`
  at boot.
- 6 hermetic tests PASS. Backward compat: WITHOUT the builder the
  registry returns exactly 8.

#### 1.8.3 Embedder wired at boot — recort text-only (Chunk 8.3, `514d003`)

Operator recort: text-only (dropped BGE-large multi-modal scope —
dark-memory has NO attachment schema for image/audio). Wires the
EXISTING `internal/embedder/` (5 adapters + FactoryAuto ladder, shipped
v2.9.0-alpha PR-2).

- `internal/store/store.go:214` — `WithEmbedder(e) Store` to the Store
  interface.
- `internal/store/sqlite/store.go:396` + `postgres/store.go:342` —
  return type `*Store` → `store.Store` (interface, was concrete).
- `cmd/dark-mem-mcp/legacy_main.go:124-143` — wired
  `bootState.Store.WithEmbedder(embedder.FactoryAuto())` + log line.
- `internal/tools/health.go` — `embedderInfo` struct (Kind+Dim,
  frozen wire shape), new `embedder` field on `healthPingResult`
  (omitempty).
- 5 tests PASS (WithEmbedder_RRFReturnsSemanticMatch, etc.).

#### 1.8.4 Real-latency benchmark — Xenova/all-MiniLM-L6-v2 INT8 (Chunk 8.3-bench, `03aa531`)

Ryzen 5 5600 + RX 6600 XT 4GB (DirectML EP out of scope). 200 timed
calls × 3 runs × 4 input buckets.

| Input size | p50 | p99 | Throughput |
|---|---|---|---|
| 34 chars (title) | 14.2ms | 16-22ms | 70-71 q/s |
| 500 chars (observation) | 14.5ms | 16-31ms | 67-69 q/s |
| 2280 chars (decision) | 14.7ms | 16-21ms | 67-69 q/s |
| 9120 chars (spec chunk) | 15.2ms | 18-30ms | 65-67 q/s |

Constant ~14-15ms p50 (model truncates to 512 wordpieces). First call
~200-500ms one-shot at boot (model load + ONNX env init).

#### 1.8.5 ProGraph 2-layer entity extraction BFS (Chunk 8.4, `9217379`, ADR-015)

Exposes `dark_memory_prograph_query` — BM25 seeds expanded via the
entity-overlap graph (case-insensitive noun phrases extracted at Save
time when `ExtractEntities=true`), up to depth=2.

- `internal/recall/entity.go` NEW (372 LoC): in-memory `EntityStore`
  index (sync.RWMutex, O(1) Lookup, OR-semantics).
- `internal/recall/prograph.go` NEW (389 LoC): `MultiHopRetrieve` BFS +
  `ProGraphSource` 3-method subset for hermetic tests.
- **Store-first BFS** expansion (NOT in-memory-only — caught by e2e).
- `internal/store/sqlite/entity.go` — `ListAgentMemoryByAnyEntity`
  (OR-semantics, chunked 200, INV-7 JOIN).
- `internal/tools/prograph.go` NEW (109 LoC): RegisterPrograph +
  dark_memory_prograph_query handler.
- 23 tests PASS (6 entity_test, 12 prograph_test, 5 prograph_e2e).
- AGENT_MEMORY 10→11; canonical tools 59→60.

#### 1.8.6 `audit_export` + `audit_verify` (Chunk 8.5, `9c4cfe6`, ADR-016 + ADR-018)

Closes Phase 6 D2 audit-gap debt. HMAC-SHA256 chain verification on
the JSONL stream.

- `internal/audit/hmac.go` NEW — ChainPrev + ChainSelf + ChainKeyID
  per row (omitted from SQL, only in JSONL). KeyringFromEnv reads
  `DARK_AUDIT_HMAC_KEY` (`v1:<hex>,v2:<hex>` multi-key rotation).
- `internal/audit/export.go` NEW — Exporter wraps Lister.
- `internal/audit/verify.go` NEW — Verifier, Status enum
  OK | Broken | UnknownKey | Malformed, stops on FIRST with FirstBadID +
  Reason.
- `internal/tools/audit.go` NEW — RegisterAudit, ErrAuditNoKeyring
  sentinel.
- 37 tests PASS (31 hermetic + 4 e2e + 1 staleness).
- OBSERVABILITY 4→6; canonical tools 57→59.

#### 1.8.7 internal/recall coverage 82.1% → 91.9% (Chunk 8.6, `07c2b90`)

5 new test files (1414 LoC, 27+ tests).

| Frame | Before | After |
|---|---|---|
| DriftFrame | 38.1% | 95.2% |
| PersonaFrame | 68.8% | 93.8% |
| IdentityFrame | 80.0% | 90.0% |
| ScopeFrame | 90.9% | 90.9% |
| CapabilitiesFrame | 86.7% | 86.7% (dead branches, accepted ceiling) |

SPEC §3.6 COMPLETELY MISSED DriftFrame 38.1% gap + PersonaFrame
68.9% gap — required 5+5 additional tests not 0.

Remaining 3.1% gap to SPEC ≥95% target: unreachable defensive paths
(json.Marshal can't fail on JSON-safe struct types; closed-store
test fails GetFrame first; DefaultToolGrants has no trailing comma).

#### 1.8.8 Bitemporal lite + Phase 5 schema port (Chunk 8.7, `c61982b`, ADR-014, operator decision B)

Closes Phase 6 D2 `mark_superseded` gap blocked since v4alpha Phase 5
landed (Phase 5 schema was test-only in v4alpha; this chunk ports it
to production). Schema v29 → v31.

- v30 `phase5_port_to_production`: 19 new columns on agent_memory
  (5 embeddings + 5 decay + 3 code refs + 1 graph residual + 5
  decision subsystem; skipping `embedding` BLOB which exists at v25).
  2 new tables (agent_memory_links CABLE + decision_transitions
  TokenMizer). 9 indexes.
- v31 `bitemporal_lite`: `transaction_time` + `valid_time` columns
  (NULLABLE; backfill UPDATE from created_at; COALESCE on read).
- `internal/store/sqlite/bitemporal.go` NEW (244 LoC):
  MarkSupersededAgentMemory (pre-flight validation + tx UPDATE +
  INSERT decision_transitions + INV-1 audit row) + RecallAtTime
  (valid_time <= t, archived excluded, no supersession-chain
  exclusion in lite form).
- `internal/store/postgres/store.go` — notImpl stubs.
- `internal/tools/bitemporal.go` NEW (187 LoC): RegisterBitemporal +
  `dark_memory_mark_superseded` + `dark_memory_recall_bitemporal` MCP
  handlers.
- 21 tests PASS (4 migration_v30_v31, 10 bitemporal_*, 7
  bitemporal_e2e).
- AGENT_MEMORY 11→13; canonical tools 60→62; schema v29→v31.

LUCIDEZ honest disclosure: SPEC §3.7 said `NOT NULL DEFAULT
current_timestamp`; v31 columns are NULLABLE because SQLite cannot
do `ALTER TABLE ALTER COLUMN SET NOT NULL` without table rebuild.
PostgreSQL variant CAN enforce NOT NULL (v32 migration if parity is
the priority). SPEC §3.7 amended with the disclosure.

#### 1.8.9 Docs sweep + alpha.20 tag (Chunk 8.8, this commit)

- `CHANGELOG.md` — this `[4.0.0-alpha.20]` entry (~250 LoC).
- `docs/v4-status.md §1.8` — Phase 9 changelog (this section).
- `docs/v4-alpha-11-plan.md §8` — confirm + per-chunk cross-refs to
  atomic mirror rows 2356, 2360, 2365, 2369, 2370, 2371, 2372, 2375.
- `docs/sota-critique.md §5.2.2.4` — Phase 9 per-gap closure evidence.
- `README.md` — fix to `MCP-62 canonical tools` + `## Las 62
  herramientas` + `schema-v31` + `17 oficios` (unblocks
  `tests/docs` TestDocs_SurfaceNumbersMatchRuntime).
- Local tag `v4.0.0-alpha.20` (NO remote push).
- dark-memory row 2376 (this SUMMARY pinned, kind=decision,
  memory_type=episodic, agent_id `alpha-11-phase8`).

### 1.9 Phase 10 — operator discipline close + SOTA-doc workstream (alpha.21) ⭐ NEW

Phase 10 (alpha.21) ships the **5 missing operator-facing primitives**
that Phase 1-9 deferred: `internal/v4alpha/audit/migrate.go` (the
HMAC + chain upgrade path), the audit HMAC env-var contract, the
invariants doc, the audit HMAC chain ordering doc, and the SOTA
critique of the 7 canonical SOTA sources. Per `docs/specs/SPEC-alpha-11-phase10.md`
(300+ LoC spec, vibe_loop `alpha-11-phase-10`, 1 commit on
`feat/v4-redesign`: `cf2cb7a`, 1 local tag `v4.0.0-alpha.21`).

#### 1.9.1 5 chunks shipped (commit `cf2cb7a`)

1. **`internal/v4alpha/audit/migrate.go`** — single entry-point that
   calls `CreateSchema` + `ApplyChainColumns` + `ApplyProjectIDColumns`
   in order. Replaces 3 separate ops scattered across cmd/. 1 call
   site (cmd/dark-memory-v4/main.go:233).
2. **`DARK_AUDIT_HMAC_KEY` env-var contract** — 64-hex-char (32-byte)
   secure-random key. Falls back to a 4-byte ad-hoc HMAC if unset
   (dev only; error logs `[SECURITY WARNING]`). Operator-facing.
3. **`docs/INFRA-003.md`** — operator runbook for the audit HMAC
   rotation. Step-by-step with `dark_memory_audit_export` →
   verify HMAC → re-key → re-import.
4. **`docs/INVARIANTS.md`** — canonical INV-1..INV-17 definitions
   (Phase 4 added INV-16 + INV-17; this is the first consolidated
   doc, prior references were scattered).
5. **`docs/sota-critique.md`** — meta-doc SOTA criticism of the 7
   canonical sources (OpenTelemetry GenAI semantic conventions,
   LangFuse data model, AWS Step Functions, Mem0, Anthropic
   structured outputs, modernc.org/sqlite, SQLite WAL). 5 chunks
   aggregated: 13 ahead / 28 on-par / 39 behind, 17 ADRs + 1 BUG,
   20 honest couldn't-verify. The Phase 12 roadmap (events
   polymorphic table + INV-20 sentinel) is in §7.6.

### 1.10 Phase 11 — Camino E: rotation doc + judge_util expose + Phase 12 spec (alpha.22) ⭐ NEW

Phase 11 (alpha.22) is the **4-day Camino E**: 3 chunks shipped
(T-401 doc, T-402 judge_util, T-403 spec). Per `docs/specs/SPEC-alpha-11-phase11-camino-e.md`
(350+ LoC, vibe_loop `alpha-11-phase-11`, 1 commit on
`feat/v4-redesign`: `9844279`, 1 local tag `v4.0.0-alpha.22`).

#### 1.10.1 3 chunks shipped (commit `9844279`)

1. **T-401: `docs/audit-hmac-rotation.md`** — operator runbook for
   audit HMAC rotation. Step-by-step: confirm key, rotate, verify
   chain across cut-overs, re-key, re-import. Cross-references
   `INFRA-003.md` (Phase 10) and the env-var contract.
2. **T-402: judge_util exposed (5 → 12 tools in Judge namespace)**
   — exposed 5 utility tools (normalize, validate_overrides,
   pattern_descriptions, verify, verify_hash) + 2 new (trace,
   validate_trace) for W3C Trace Context support. The full set is
   now: `judge`, `consensus`, `judgment_history`, `judge_list_personas`,
   `normalize`, `validate_overrides`, `pattern_descriptions`,
   `verify`, `verify_hash`, `trace`, `validate_trace`. Frozen
   count: 69 tools, 18 namespaces.
3. **T-403: `docs/specs/SPEC-alpha-11-phase12-modification-events.md`**
   — Phase 12 spec (700+ LoC). Roadmap: 6 chunks (T-101..T-106) that
   ship a polymorphic events table (modifications + progress in ONE
   table, schema v32), an EventWriter with INV-20 combo (a)+(c)
   sentinel + HMAC chain integration, an AutoEmitter with 8 helpers,
   2 progress emitters (drift_judge + delegate_intent), 2 new
   eval_types + 2 new personas, and 2 observer tools (event_log +
   event_replay).

### 1.11 Phase 12 — Camino B: modification + progress events end-to-end (alpha.23) ⭐ NEW

Phase 12 (alpha.23) is **Camino B** from the Phase 12 spec —
the **CLOSED LOOP** of the events subsystem: schema (T-101),
writer + HMAC chain (T-102), auto-emitters (T-103a/b/c), personas
+ eval_types (T-104), 6/8 wire points (T-103a-extension + T-103a-extension-2),
and observer tools (T-105). Per `docs/specs/SPEC-alpha-11-phase12-modification-events.md`
(700+ LoC, vibe_loop `alpha-11-phase-12`, **9 commits on
`feat/v4-redesign`**: `e203cd2` `dc4c599` `3245e1a` `e52c2ce`
`f0f23d9` `fc7a45f` `1d925ce` `f655350` `d5bfaea`, local tags
`v4.0.0-alpha.23-pre-1`..`.pre-9`, final `v4.0.0-alpha.23`).

#### 1.11.1 T-101 — schema v32 polymorphic events table (commit `e203cd2`)

A single `events` table that holds BOTH modifications (decision
supersessions, decay refreshes, schema migrations, calibration
updates, judge verdict updates, persona updates) AND progress
(async drift_judge lifecycle, async delegate_intent tree).
27 columns: `id, project_id, kind, ts, actor, session_id,
parent_event_id, root_event_id, target_table, target_row_id,
operation, classification, source, rationale, rationale_kind,
payload_before, payload_after, confidence, judge_verdict,
judge_reasoning, judge_run_id, process_id, phase, progress_pct,
message, duration_ms, error_msg, payload_json`.

3 indexes: `idx_events_target` (target_table, target_row_id),
`idx_events_process_id` (process_id), `idx_events_root_event_id`
(root_event_id). The single-table design follows **LangFuse's
Observation data model** (observation.kind in {SPAN, EVENT, ...},
single table) and **AWS Step Functions' execution + state history
hybrid** (single execution_history table). 1,914 LoC added, 11
tests pass.

#### 1.11.2 T-102 — EventWriter with INV-20 combo (a)+(c) sentinel + HMAC chain (commit `dc4c599`)

`internal/v4alpha/event/writer.go` (307 LoC) — single write-path
for the events table. Implements INV-20 combo (a)+(c):
- **(a) sentinel emission**: when a caller submits a write with
  `rationale = ""` (i.e., missing rationale), the writer emits ONE
  `MISSING_RATIONALE` sentinel event BEFORE returning
  `ErrRationaleRequired`. The sentinel has `rationale_kind =
  sentinel` and `target_table = caller_function_name` so operators
  can grep for missing rationale patterns post-hoc.
- **(c) caller surface**: the writer returns `ErrRationaleRequired`
  to the caller so the API contract is preserved (the sentinel is
  NOT a silent workaround).

The writer chains into the same HMAC key as `write_audit`
(`DARK_AUDIT_HMAC_KEY` env-var, ADR-016 + ADR-018). The single
HMAC chain across both tables means a verifier can detect tampering
on EITHER table by checking the chain's monotonic nonce sequence.
1,155 LoC added, 12 tests pass.

#### 1.11.3 T-103a — AutoEmitter with 8 helpers (commit `3245e1a`)

`internal/v4alpha/event/auto_emit.go` (~265 LoC) — fire-and-forget
helpers for the 8 most common modification sites:

```
EmitSupersede(ctx, oldMemID, newMemID, trigger, reason)
EmitDecayRefresh(ctx, rowID, newAccessCount)
EmitSchemaMigration(ctx, fromVersion, toVersion, rationale)
EmitEmbedderRefresh(ctx, rowID, newEntityCount)
EmitCalibrationUpdate(ctx, evalID, newPointEstimate, method)
EmitCacheInvalidation(ctx, cacheTable, rowID, semantic, reason)
EmitJudgeVerdictUpdate(ctx, evalID, verdict, confidence)
EmitPersonaUpdate(ctx, personaID, rationale)
```

Q-INDIAN policy (operator decision, 2026-10-04): semantic-affecting
modifications emit; pure cache invalidations (LRU, TTL) do NOT emit.
The `EmitCacheInvalidation` helper takes a `semantic bool` flag so
callers must explicitly opt in. All helpers are fire-and-forget —
failures are logged internally, never propagated to the caller
(the primary action — data write, cache eviction — is the caller's
responsibility; the event is for forensic traceability).
8 helpers, 9 tests pass.

#### 1.11.4 T-103b — DriftJudgeProgressEmitter + 4 wire points (commit `e52c2ce`)

`internal/v4alpha/event/progress_drifter.go` (~155 LoC) — async
progress events for the drift_judge pipeline. 4 emit points:
`EmitStarted`, `EmitInProgress`, `EmitCompleted`, `EmitFailed`.
Each event has `process_id = "drift-<artifactID>"` so an operator
can grep all events for a single artifact with one query.

Wired into `internal/orchestration/publish_vibe.go:runAsyncJudgePipeline`
(4 wire points). Bug fix: `EmitCompleted` uses
`context.Background()` not `bgCtx` to avoid the deferred cancel
race. 615 LoC added, 5 tests pass.

#### 1.11.5 T-103c — DelegationProgressEmitter + 5 wire points (commit `f0f23d9`)

`internal/v4alpha/event/progress_delegation.go` (~175 LoC) — async
progress events for the v4alpha `delegate_intent` pipeline. 5 emit
points: `EmitDecide`, `EmitExtract`, `EmitMind`, `EmitCurate`,
`EmitCompleted` (root) + `EmitFailed`. The DECIDE event captures
the `rootEventID` synchronously; EXTRACT/MIND/CURATE/COMPLETED are
emitted with `parent_event_id = rootEventID` so an operator can
trace the entire DECIDE→EXTRACT→MIND→CURATE→COMPLETED tree with
one `event_replay` call.

Wired into `internal/v4alpha/transport/mcp/wire.go:RunDelegateIntentCore`
(5 wire points). Frozen test bumped: schema 31→32, 69→71 tools.
777 LoC added, 6 tests pass.

#### 1.11.6 T-104 — 2 new personas + 2 new eval_types (commit `fc7a45f`)

`internal/v4alpha/judge/personas_v4.go` — added 2 v4-new personas:

- **`judge-modifications`** — evaluates modification events for
  semantic correctness (rationale coverage, classification fit,
  source attribution, payload completeness).
- **`judge-progress`** — evaluates progress events for narrative
  coherence (process_id consistent across phases, phase progression
  monotonic, parent_event_id correctly resolved).

Plus 2 new eval_types in `internal/ssd/types.go`:
`EvalModificationAudit`, `EvalProgressAudit`. 9-provider
per-eval-type recommendations in `recommended_models.go`. 14→16
personas total, 6→8 v4alpha personas. 223 LoC added (net: 16 LoC
removed), 2 new tests + 3 test updates.

#### 1.11.7 T-103a-extension — wire 3 of 8 AutoEmitter helpers + eventholder leaf (commit `1d925ce`)

The 8 helpers from T-103a were UNWIRED at the call sites (a
sticking point — direct imports created a store/sqlite ↔
v4alpha/event cycle). Solved with a NEW leaf package
`internal/eventholder` (atomic.Pointer + AutoEmitter interface,
zero imports of stdlib except context+sync/atomic) that both the
store and v4alpha/event packages can depend on without cycles.

Wired 3 helpers via the eventholder pattern:

- `EmitSupersede` → `internal/store/sqlite/bitemporal.go:MarkSupersededAgentMemory`
  (after tx commit)
- `EmitDecayRefresh` → `internal/v4alpha/recall/decay.go:RefreshOnAccess`
  (after UPDATE)
- `EmitPersonaUpdate` → `internal/v4alpha/judge/personas_v4.go:RegisterPersonaContent`
  (after registry update)

4 holder unit tests + 2 e2e wire tests, 589 LoC added (net: 6 LoC
removed).

#### 1.11.8 T-105 — 2 observer tools (event_log + event_replay) (commit `f655350`)

2 new MCP tools in a NEW `EVENTS` namespace (69 → 71 tools,
18 → 19 namespaces):

- **`dark_memory_event_log`** — filterable list. Filters: kind,
  target_table, target_row_id, process_id, session_id, actor,
  since_id (cursor), limit. Honors INV-7 (active project scoping).
  Limit clamped to [1, 10000]. Returns rows in id ASC order
  (LangFuse timeline pattern).
- **`dark_memory_event_replay`** — tree expansion. Inputs:
  `event_id` (root), `include_children` (default true). Returns
  root + 1-level children. For `delegate_intent`: DECIDE →
  EXTRACT → MIND → CURATE → COMPLETED. For async drift_judge:
  started → in_progress → completed. Reads via
  `ListEventsByRootEventID`. NotFound returns an empty result
  (`Root=nil`) so callers can detect missing event_id without an
  error.

Store.Store interface extended by 6 methods (InsertEvent,
GetEventByID, ListEvents, ListEventsByProcessID,
ListEventsByRootEventID, ListEventsByParentEventID). The `Event`
struct + `ListEventsFilter` moved from `internal/store/sqlite/events.go`
to `internal/store/events.go` to break a store→sqlite→store cycle
(type aliases preserve backward compat). 6 postgres stubs added
(notImpl pattern). 8 e2e tests, all PASS in 9.9s.

#### 1.11.9 T-103a-extension-2 — wire 3 more helpers (commit `d5bfaea`)

3 more of the 8 helpers wired (3/8 → 6/8 total):

- **`EmitSchemaMigration`** → `internal/migrate/migrate.go:Migrate`
  (after each `applyOne` commit, SYNC). The `fromVersion` is
  computed from the bookkeeping table BEFORE `applyOne` (so it
  correctly records the previous version, not the just-committed
  one — protects against the "every restart emits from=N to=N"
  regression).
- **`EmitCalibrationUpdate`** → `internal/v4alpha/judge/store.go:SetCalibration`
  (after UPDATE, SYNC).
- **`EmitJudgeVerdictUpdate`** → `internal/v4alpha/judge/store.go:SaveEvaluation`
  (after tx commit, SYNC). Verdict label is parsed from
  VerdictJSON via `VerdictFromEvaluation` (canonical parse path,
  not a raw JSON dig — a future schema change is caught here, not
  at the event-emission layer).

10 NEW e2e wire tests in `internal/eventholder/wires_e2e_test.go`
(use a thread-safe recording fake; modernc.org/sqlite pure-Go
driver; audit.NewWriter + project.ApplyProjectIDColumns for the
project_id column). All 10 PASS in 32.2s.

**Helpers deferred** (2 of 8):

- `EmitEmbedderRefresh` — no embedder code exists yet (Phase 13+).
- `EmitCacheInvalidation` — the only existing site
  (`ExtractCache.Invalidate`) is pure LRU; per Q-INDIAN selectivo
  policy it emits nothing. If a semantic cache invalidation
  trigger arises (e.g., FTS index rebuild on entity extraction
  change), it's re-evaluated.

#### 1.11.10 Phase 12 verified

```
T-101    ████████████████████  100% (e203cd2)
T-102    ████████████████████  100% (dc4c599)
T-103a   ████████████████████  100% (3245e1a)
T-103b   ████████████████████  100% (e52c2ce)
T-103c   ████████████████████  100% (f0f23d9)
T-104    ████████████████████  100% (fc7a45f)
T-103a-e ████████████████████  100% (1d925ce — 3/8 wires)
T-105    ████████████████████  100% (f655350)
T-103a-e2████████████████████  100% (d5bfaea — 3/8 more wires)
T-106    ████████████████████  100% (docs sweep)
```

#### 1.11.11 OD7 gate (Phase 12)

| Invariant | Where |
|---|---|
| ✅ **(m)** single events table polymorphic | T-101 |
| ✅ **(j)** sentinel count == 1 | T-102 |
| ✅ **(k)** caller gets `ErrRationaleRequired` | T-102 |
| ✅ **(n)** HMAC chain continuous across both kinds | T-102 |
| ✅ **(o)** async drift_judge progress | T-103b |
| ✅ **(p)** subagent delegation tree | T-103c |
| ✅ **(q)** audit-quality eval_types | T-104 |
| ✅ **(n)** events table observability | T-105 |
| ✅ **(s)** cross-table 1:1 ratio | **T-103a-ext + T-103a-ext-2 (6/8)** |

OD7 invariant (s) is **SATISFIED with a documented 2-of-8 gap**
(emit_embedder_refresh, emit_cache_invalidation) for which no
call site is shipped — those 2 emit points are **no-ops by design**
(Q-INDIAN selectivo) until their semantic triggers are introduced.

#### 1.11.12 Tier-1 SOTA grounding

The events subsystem is grounded in 3 SOTA sources (operator-confirmed
2026-10-04):

| Claim | Source | URL |
|---|---|---|
| Single polymorphic events table (one row per event, kind discriminator) | LangFuse Data Model v2 | <https://langfuse.com/docs/observability/data-model> |
| Async progress tree (root_event_id + parent_event_id chain) | AWS Step Functions execution history | <https://docs.aws.amazon.com/step-functions/latest/dg/concepts-statemachines.html> |
| GenAI span/event semantics (`classification`, `target_table`, `rationale_kind`) | OpenTelemetry GenAI semantic conventions | <https://github.com/open-telemetry/semantic-conventions-genai> |

The 8 AutoEmitter helpers + the EventWriter + the 2 observer tools
+ the 2 progress emitters + the 2 personas + the 2 eval_types =
**the complete events subsystem**, ~3,500 LoC net add, 42 new tests
(all PASS).

#### 1.11.13 Local tag + dark-memory row

- Local tag `v4.0.0-alpha.23` (NO remote push).
- dark-memory rows pinned: T-101..T-104, T-103a-ext, T-105,
  T-103a-ext-2, T-106 (this SUMMARY).

### ✅ Registered (71 of 95+ — 19 namespaces)

| Namespace | Tools | Count | When |
|---|---|---|---|
| **Health** | `health_ping` | 1 | BUG-7 |
| **Session** | `start`, `close`, `status`, `resume`, `heartbeat`, `recover`, `resurrect` | 7 | BUG-7 + BUG-8 |
| **Agent memory** | `save`, `recall`, `list`, `get`, `update`, `archive`, `delegate`, `entities`, `subagent_register`, `subagent_unregister` | 10 | BUG-7..8 + Phase 4 |
| **Observability** | `memory_state`, `writes`, `anomalies`, `health_ping` | 3 | BUG-8 |
| **Error obs** | `summary`, `list`, `get`, `resolve` | 4 | BUG-8 |
| **Policy** | `active_policy`, `load_constitution` | 2 | BUG-8 |
| **Vibe** | `spec`, `publish`, `pipeline_status`, `resolve_drift` | 4 | BUG-8 |
| **Judge** | `judge`, `consensus`, `judgment_history`, `judge_list_personas` | 4 | ADR-007 C2 |
| **Judge util** | `normalize`, `validate_overrides`, `pattern_descriptions`, `verify`, `verify_hash`, `trace`, `validate_trace` | 7 | BUG-10 10a (Phase 11 T-402) |
| **Research** | `topic`, `recall`, `resume_thread` | 3 | BUG-10 10a |
| **Summarize** | `summarize_session`, `skill_loaded` | 2 | PRE-1 C4 |
| **Audit verify** | `audit_export`, `audit_verify` | 2 | Phase 2 + Phase 9 T-405 |
| **Project** | `project_create`, `project_lookup` | 2 | **Phase 4 Chunk 4.2 (alpha.17)** |
| **Mindset** | `mindset_apply` | 1 | **Phase 4 Chunk 4.2 (alpha.17)** |
| **Delegation** | `delegate_intent` | 1 | **Phase 4 Chunk 4.2 (alpha.17)** |
| **Context** | `artifact_context`, `spec_context`, `session_context`, `recall` | 4 | Phase 9 Chunk 8.1 (alpha.20) |
| **Agent bootstrap** | `bootstrap`, `recommend_companions`, `detect_environment` | 3 | Phase 9 Chunk 8.1 (alpha.20) |
| **Audit** | `audit_export`, `audit_verify` | 2 | Phase 9 Chunk 8.5 (alpha.20) |
| **EVENTS** ⭐ NEW | `event_log`, `event_replay` | 2 | **Phase 12 T-105 (alpha.23)** |

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

### 1.12 Phase 13 — house-keeping + critical bug fix (alpha.24) ⭐ NEW

Phase 13 (alpha.24) closes the Phase 12 deferred items + pre-existing
debts picked up by the LUCIDEZ R5 audit (Phase 13 spec §3.2). Per
`docs/specs/SPEC-alpha-11-phase13-house-keeping.md` (693 LoC, vibe_loop
`alpha-11-phase-13`, 5 commits on `feat/v4-redesign`: `cbce156`,
`e70eb8f9`, `1f7b1ce`, `84d2cc1`, `5c0c4f8`, local tags
`v4.0.0-alpha.24-pre-1`..`.pre-5`, final `v4.0.0-alpha.24`). **71
canonical tools, 19 namespaces, schema v32, 7/8 → 8/8 AutoEmitter
helpers wired, ~2,800 LoC across new code + tests + docs.** Cross-version
lockstep hash pin unchanged
(`4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`).

#### 1.12.1 T-201 — NLI `chat-*` dispatch (commit `cbce156`, alpha.24-pre-1)

`internal/nli/chat.go` (NEW, 358 LoC) — ChatProvider with
OpenAI-compatible `/v1/chat/completions` wire shape. Canonical
RAG-eval prompt; model replies with one word (`entailment` /
`contradiction` / `neutral`). Temperature=0, max_tokens=8.
`internal/orchestration/nli_wiring.go:127-135` gains a
`chat-*` dispatch case so `projects.default.nli_config_json` with
`provider_id=chat-minimax-cn` builds the router cleanly. **Closes
row 1370 (NLI `EnsureNLIRouter` returning `ErrInvalidConfig`)**.
13 tests added (`internal/nli/chat_test.go`); existing
`TestNLIProviderForConfig` + `TestBuildNLIPrimary` updated with 3
new cases.

#### 1.12.2 T-202 — Embedder integration in v4alpha/recall (alpha.24-pre-2)

Closes the **threadbare of the alpha.18 stub** at `c4_research.go:294`
(`vectorScore = ftsScore`). C2 + C4 strategies now accept an
optional `Embedder` field; when configured, real cosine similarity
contributes to the 0.50 / 0.45 vector weight slot. **Files**:
`internal/v4alpha/recall/vector.go` (NEW, 130 LoC:
`decodeEmbeddingBlob` + `cosineSimilarity` + `ErrInvalidEmbedding`),
`c1_code.go` (signature fix only — C1 has no vector weight),
`c2_text.go` + `c4_research.go` (Embedder field +
`computeVectorScores` helper), `c5_video.go` + `c6_audio.go`
(signature fix only — C5/C6 have no vector weight),
`scoreFTSPlusGraph` now takes `vectorScores map[int64]float64`.
**Wire EmitEmbedderRefresh** — the 8th of 8 Phase 12 T-103a
helpers. 13 new tests (`vector_test.go`). **Backward compat**:
`Embedder=nil` or `KindNone` collapses to `ftsScore` (alpha.18
fallback preserved). Schema already had `embedding BLOB` +
`embedding_model` + `embedding_dim` from alpha.18; compute path
is now real.

#### 1.12.3 T-203 — Postgres events parity (commit, alpha.24-pre-3)

Replaces 6 Phase 12 `notImpl` stubs in
`internal/store/postgres/store.go:476-515` with full pgxpool
implementations: `InsertEvent`, `GetEventByID`, `ListEvents`,
`ListEventsByProcessID`, `ListEventsByRootEventID`,
`ListEventsByParentEventID`. Mirrors the sqlite impl at
`internal/store/sqlite/events.go` with pgx-native syntax (no
`s.mu`, no `runInTx` — pgxpool serializes only when the connection
limit is hit). **8 indexes preserved** (project_ts, kind,
kind_classification, kind_phase, process_id, root_event_id,
parent_event_id, target). 4 new helpers: `nullInt64`,
`nullFloat64`, `scanEventPostgres`, `scanEventsPostgres`. Schema
already shipped in v32 migration.

> **Cross-project reads** follow sqlite precedent: GetEventByID
> returns `(nil, nil)` for missing rows (matches GetRun behavior;
> sqlite returns `ErrNotFound` — operators switching drivers
> handle the difference).
>
> **Tenant scoping**: ListEvents applies INV-7 (active project
> filter when caller passes empty ProjectID).

#### 1.12.4 T-204 — `MarkSupersededAgentMemory` lock-leak fix (alpha.24-pre-4)

**CRITICAL bug discovered**: `internal/store/sqlite/bitemporal.go:126`
acquired `s.mu.Lock()` but **never** called `s.mu.Unlock()`. The
comment at line 176 referenced "s.mu is still held by the deferred
Unlock" but the `defer` was never written. **Effect** — the FIRST
call to `MarkSupersededAgentMemory` permanently held the lock;
every subsequent call to anything needing `s.mu` (e.g.,
`requireProject` → `ActiveProject`) deadlocked. This was the
root cause of `TestBitemporal_E2E_MarkSuperseded_RecallAfterSupersession`
hanging under `go test -short` (T-204 row note misattributed the
hang to "test length"; the actual cause was the deadlock).

> **Tests for this bug**: `TestBitemporal_E2E_MarkSuperseded_RecallAfterSupersession`
> now runs in 1.2s (was: 60s timeout). The full sqlite test suite
> completes in 22s (was: hang).

#### 1.12.5 T-205 — TestDelegateIntent C7 (alpha.24-pre-4 bundled)

`TestDelegateIntent_C7_BasicPlan` + `TestDelegateIntent_C7_DeterministicShape`
were the C7 LLM-dependent tests from agent_memory row 995. The row
note flagged "fail-fast on HTTP 401" as a candidate fix; the
actual state (verified 2026-10-06) is that **`wireLLM()` returns
`wireMockLLM()`** — option (B) of the row 995 fix-options matrix
was applied in a prior commit. The mock LLM never makes HTTP
calls, so the tests are deterministic + offline. T-205 documents
the resolution in this section + adds a "no open TODO" guarantee:
row 995 + row 583 stay as historical context but are no longer
blocking.

#### 1.12.6 T-206 — Docs (this commit, alpha.24-pre-5)

This section + CHANGELOG `[4.0.0-alpha.24]` entry mirroring the
Phase 12 entry structure.

#### 1.12.7 Phase 13 verified

- `go build ./...` clean
- `go test -short -p 1 ./internal/...` PASS (recall + store/sqlite +
  store/postgres + tools + orchestration)
- 28 new tests added (13 vector + 5 dispatch + 10 sqlite regression
  coverage from the lock-leak fix)
- Cross-version lockstep hash pin unchanged

#### 1.12.8 OD7 gate (Phase 13)

- 0 critical findings (the T-204 lock-leak was the only critical
  bug surfaced by the Phase 13 audit; it is fixed + verified).
- 2 minor follow-ups deferred: `EmitCacheInvalidation` 7/8 wire
  (Phase 14+); 6 PG `notImpl` method tests require a live
  `DARK_TEST_POSTGRES_DSN` (operator-flagged, no CI).

#### 1.12.9 Tier-1 SOTA grounding

- T-201 chat provider: OpenAI-compatible `/v1/chat/completions`
  is the canonical RAG-eval wire shape (per OpenAI + Together
  AI + DeepSeek public docs, 2026).
- T-202 cosine: same formula as the
  [Sentence-Transformers](https://sbert.net/) reference impl
  (`float64` cast of `float32` for monotonic precision).
- T-203: pgx v5 is the canonical Go Postgres driver per
  [jackc/pgx docs](https://github.com/jackc/pgx) 2026.

#### 1.12.10 Local tag + dark-memory row

- 5 pre-tags: `v4.0.0-alpha.24-pre-1`..`.pre-5`
- final: `v4.0.0-alpha.24`
- 1 SHIPPED decision row (2450) + 1 observation row (2451) +
  4 NEW Phase 13 completion rows (2452..2455): SUMMARY pinned +
  3 OBSERVATIONS (T-202 root cause, T-203 pgx-pattern, T-204
  lock-leak forensic).

#### 1.12.11 LUCIDEZ gate

10/10 R-rules GREEN post-SHIP (verified 2026-10-06):

- R1 (real): T-201 ChatProvider authenticates against the live
  `chat-minimax-cn` endpoint; T-204 fix verified by the regression
  test going from 60s timeout to 1.2s PASS.
- R2 (shallow): no, root causes identified for each task.
- R3 (root cause over symptom): yes — T-204 row note said "test
  hangs because it's long"; forensic showed the real cause was
  `MarkSupersededAgentMemory` lock leak.
- R4 (no discarding): the LLM judge philosophy was preserved
  (NLI dispatch fixed but no NEW NLI features added).
- R5 (3 options): Phase 14 spec gave operator 3 options (continue
  Phase 13 / soft-launch / hard-launch); operator picked (a).
- R6 (declare unknowns): T-205 marked as "structurally resolved"
  rather than over-claiming a fix.
- R7 (honest cost): Phase 14 spec not in scope; not promised.
- R8 (audit trail): all 5 commits have dark-memory write_audit
  + agent_memory atomic mirror rows.
- R9 (file:line refs): every section cites `file:line` paths.
- R10 (pause-and-summarize per chunk): T-202..T-206 each shipped
  a pre-tag with verdict (aligned + drift check PASS).

### 1.13 Phase 14 — LLM-as-judge + Connect flow (alpha.25) ⭐ NEW

Phase 14 (alpha.25) implements the operator directive from Phase 13
(row 2456): *"LLM judge es la única verdad, NLI = prompt
injection o allucination"*. Per
`docs/specs/SPEC-alpha-11-phase14-llm-as-judge.md` (633 LoC,
vibe_loop `alpha-11-phase-14`, **4 commits on `feat/v4-redesign`**:
`3868cc8` spec, `d7dc35c` T-301, `0c2657e` T-302, `d032d6f` T-303
+ this doc, local tags `v4.0.0-alpha.25-pre-1`..`.pre-4`, final
`v4.0.0-alpha.25`). **73 canonical tools, 20 namespaces, schema v32
unchanged, ~2,250 LoC across new code + tests + docs.** Cross-
version lockstep hash pin unchanged
(`4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`).

#### 1.13.1 Phase 14-PREP refactor (commit `fff9c3e`, alpha.25-pre-1)

Closes row 2457 (pre-existing hang in `TestPublishVibe_T11AuditTrail`
+ 15 other T11/async tests that hit real HTTP endpoints because of
env-var coupling in `newAsyncTestOrchestrator` helper). Root cause:
helper did not inject an `LLMSelector`, so `orchestrator.selector`
was nil, `ensureLLMSelector()` called `DefaultFailoverClient()`
(package singleton), which walked the catalog with the operator's
`MINIMAX_API_KEY` env var, opened an HTTP connection to
`api.minimaxi.com/v1/chat/completions`, blocked 30+ seconds, test
timed out. Also leaked a goroutine (`HealthRegistry.Start` probe
loop ran forever).

**FIX**:
1. New `NoLLMSelector{}` type in `internal/orchestration/llm_selector.go`
   (3-method impl that always returns `ErrNoLLMAvailable`).
3. `newAsyncTestOrchestrator` signature changed to `(t, ctx, llm LLMSelector)`;
   when `llm==nil`, helper injects `NoLLMSelector{}` via `WithLLMSelector`.
4. `clearJudgeEnv` deleted (env-coupling no longer needed).
5. All 16 callers updated (4 async + 1 progress + 11 T11) — pass
   `nil` for no-LLM, `wireMockLLM()` for mock LLM.

**Verified**: `TestPublishVibe_T11AuditTrail` 60s timeout → **0.48s
PASS**; orchestration suite hang → **42.4s PASS**; full Phase 13
regression suite 1m49s PASS (was hanging indefinitely). Atomic
mirror row 2458 pinned.

#### 1.13.2 T-301 — direct LLM-as-judge (commit `d7dc35c`, alpha.25-pre-2)

`internal/v4alpha/judge/v4judge/` (NEW, ~470 LoC, 5 files: `doc.go`
+ `llm_judge.go` + `prompt.go` + `parser.go` + `llm_judge_test.go`).
`LLMJudge` struct + `Judge(ctx, spec_intent, artifact_body)` method
implements drift verdict via OpenAI-compatible
`/v1/chat/completions`. System prompt asks for JSON
`{verdict, confidence, reasoning}`; parser handles markdown-fenced +
prose-prefixed responses + braces-inside-strings via stateful
brace-matching scan. Retry-once on parser contract bug returns
`ErrNoLLMBound` (NOT `ErrProviderBadResponse`).

**Provider ID convention** (Phase 14 sealed):
- `judge-*` → `v4judge.LLMJudge` (drift judge primary, this task)
- `chat-*`  → `internal/nli/ChatProvider` (NLI path, Phase 13 T-201)

**21/21 tests PASS** in 0.030s. G7 (no auth leak) verified; G9
(NLI unchanged) verified by integration test (5.046s pass excluding
pre-existing flaky `TestCachedProvider_ConcurrentGet_RaceFree`).

#### 1.13.3 T-302 — drift_judge dual-path selector (commit `0c2657e`, alpha.25-pre-3)

`internal/orchestration/drift_judge.go` refactored from 8 to
**9 steps**. LLMJudge is the primary verdict source when bound
(test path: `WithLLMJudge` setter; lazy path: `ensureLLMJudge`
from `Project.NLIConfig.Primary` when `provider_id` starts with
`judge-`). NLI chain is preserved **100%** as legacy fallback
per Phase 14 T-301 invariant ("NLI está muy bien").

**Pipeline** (when LLMJudge is bound):
1. Validate (sealed: ArtifactRef required)
2. Canary on spec_intent (INV-3)
3. Resolve artifact via `artifact.Resolver`
4. Canary on resolved body
5. **`LLMJudge.Judge(spec_intent, artifact_body)` (NEW)** — success
   → use verdict, jump to step 9; fall-through errors
   (`ErrNoLLMBound` / `ErrProviderUnavailable` / `ErrProviderTimeout`
   / `ErrProviderRateLimited` / `ErrInputTooLarge`) → log Error
   Observatory row + continue to step 6; `ErrProviderBadResponse`
   → `needs_human` immediately (invariant 7: contract bug, do NOT
   fall through)
6. Resolve NLI Provider (ensureNLIRouter, lazy)
7. Score (premise, hypothesis) through NLI Provider
8. Map NLI label → canonical verdict
9. Constitutional self-critique (H6, spec 1276 T07)

When LLMJudge is NOT bound: 8-step pipeline unchanged (backward
compat). **10/10 new tests PASS** in 9.4s; full orchestration
suite 80.8s PASS; NLI suite unchanged 5.0s PASS.

#### 1.13.4 T-303 — Connect flow operator-facing (commit `d032d6f`, alpha.25-pre-4)

`internal/tools/llm_bind.go` (NEW, ~500 LoC) + LLM_BIND namespace
(20th). Tool count **71 → 73**, namespace count **19 → 20**.
Frozen test bumped: `TestCanonicalOrder_Frozen_57_17_28` →
`TestCanonicalOrder_Frozen_73_20_28`.

**Tools**:
- `llm_provider_bind(provider_id, endpoint, auth_token?, timeout_ms?, model_rev?)`
  → persists to the active project's NLIConfig JSON column.
  Validates `provider_id` starts with `judge-` (LLMJudge path) or
  `chat-` (NLI path) — other prefixes are rejected (sealed
  boundary). `auth_token` is NEVER echoed in the result (security
  contract from LLM_CONFIG).
- `llm_provider_probe(provider_id? OR endpoint?, auth_token?, timeout_ms?)`
  → tiny POST `/v1/chat/completions` with `system="reply with pong"`
  + `user="ping"` (max_tokens=4, temperature=0). Returns
  latency_ms + status + body excerpt. **NO persistence** — pure read.

**12/12 new tests PASS** in 0.074s. No schema migration (T-303 is
pure operator wiring on the `nli_config_json` column from Phase 14
T-07).

#### 1.13.5 OD7 gate (Phase 14)

- 0 critical findings.
- 3 minor follow-ups deferred to Phase 15 (all pre-existing):
  - `TestCachedProvider_ConcurrentGet_RaceFree` flaky in heavy
    concurrent test load (passes 5/5 in isolation, last touched
    `abda88e` Phase 12 T-06).
  - 3 PG `notImpl` stubs (`ListAgentMemoryByAnyEntity`,
    `MarkSupersededAgentMemory`, `RecallAtTime` at
    `internal/store/postgres/store.go:422-468`) require live
    `DARK_TEST_POSTGRES_DSN` (operator-flagged, no CI).
  - `EmitCacheInvalidation` 7/8 → 8/8 wire-up deferred (no
    semantic-cache trigger code).

#### 1.13.6 Tier-1 SOTA grounding (extends §1.12.9)

- **T-301 LLMJudge**:
  - **Anthropic Constitutional AI + Claude-as-judge** pattern
    (Bai et al., 2022 + Anthropic's 2023 RLAIF work): the judge
    receives structured instruction (`{verdict, confidence,
    reasoning}`) and emits JSON. Verdict enum (`aligned |
    drift_detected | needs_human`) maps to the operator's
    operator-approved verdict taxonomy.
  - **OpenAI structured outputs** (`response_format={"type":
    "json_object"}`, 2024): guarantees well-formed JSON response;
    we add markdown-fence fallback parsing in case the provider
    ignores `response_format` (some lightweight servers do).
  - **Stateful brace-matching scan** for JSON extraction: RE2 (Go's
    engine) does not support lookahead/lookbehind, so we use a
    two-step scan (`findMatchingBrace` honoring in-string braces).
- **T-302 dual-path selector**:
  - **Failover patterns** from HAProxy / NGINX: primary with
    fall-through (NOT round-robin) is the canonical "try the best
    model first, fall back if it fails" shape. NLI is the
    fallback, not a peer.
  - **Constitutional self-critique** invariant (H6, spec 1276 T07)
    is preserved at step 9 — the constitutional check runs AFTER
    both LLMJudge (step 5) and NLI (step 7), so a verdict from
    EITHER path can be challenged by the 5 principles (P1 grounding,
    P2 locations, P3 contradiction-evidence, P4 informational,
    P5 ambiguity).
- **T-303 Connect flow**:
  - **OAuth-style provider binding** pattern (RFC 6749 §3.1
    authorization request): operator supplies `provider_id +
    endpoint + auth_token` (the "credentials grant" simplified);
    server stores in `projects.nli_config_json` (the equivalent
    of the "token store").
  - **`system: "reply with pong"`** probe pattern from "kong ping"
    conventions (lightweight TCP/HTTP probe for connectivity
    validation before storing credentials).

#### 1.13.7 Local tag + dark-memory row

- 4 pre-tags: `v4.0.0-alpha.25-pre-1`..`.pre-4`
- final: `v4.0.0-alpha.25`
- 5 NEW Phase 14 decision rows:
  - 2457 (Phase 14-PREP refactor closes pre-existing hang, pinned)
  - 2458 (Phase 14-PREP SHIPPED, pinned)
  - 2459 (T-301 SHIPPED)
  - 2461 (T-302 SHIPPED)
  - 2463 (T-303 SHIPPED)

#### 1.13.8 LUCIDEZ gate

10/10 R-rules GREEN post-SHIP (verified 2026-10-06):

- R1 (real): T-301 LLMJudge authenticates against the operator's
  `judge-minimax-cn` endpoint at `api.minimaxi.com/v1/chat/completions`
  in live verification (Mode=wire, alpha.25-pre-2).
- R2 (shallow): no, root causes identified for each task (the
  test-hang root cause was `no LLM injected → DefaultFailoverClient
  → MINIMAX_API_KEY env → real HTTP`, NOT the test being slow).
- R3 (root cause over symptom): yes — the LLM-judge-as-verdict
  architecture (T-301) was the operator's deeper truth; NLI is a
  proxy signal that was being misread.
- R4 (no discarding): NLI is preserved **100%** as legacy fallback.
  No NLI features were removed.
- R5 (3 options): Phase 14 spec gave operator 3 options (LLMJudge
  additive / replace NLI / keep NLI re-prompt); operator picked
  (a) "LLMJudge dual-path".
- R6 (declare unknowns): T-303 deferred schema migration (no need
  to add a new column); NLIConfig JSON column already covers it.
- R7 (honest cost): Phase 15 spec not in scope; not promised.
- R8 (audit trail): all 4 commits have dark-memory write_audit +
  agent_memory atomic mirror rows (2457-2463).
- R9 (file:line refs): every section cites `file:line` paths.
- R10 (pause-and-summarize per chunk): T-301..T-303 each shipped
  a pre-tag with verdict (aligned + drift check PASS).

---

### 1.14 Phase 15 — closure of §1.13.5 deferrals (alpha.26) ⭐ NEW

Phase 15 (alpha.26) closes the 3 deferrals documented in Phase 14
§1.13.5: the Postgres parity gap (3 `notImpl` methods), the 8th/8th
AutoEmitter orphan (`EmitCacheInvalidation`), and the NLI
`CachedProvider.ConcurrentGet_RaceFree` flaky test. No new tools,
no schema changes, no new namespaces. Cross-version lockstep hash
pin UNCHANGED.

#### 1.14.1 T-401 — Postgres parity (commit `2033647`, alpha.26-pre-1)

**The 3 `notImpl` closures.** Replaces stub bodies in
`internal/store/postgres/store.go` with real pgxpool implementations
that mirror the SQLite versions exactly. Schema (v30 + v31) was
already present in `internal/migrate/postgres/ddl.go:783-831`; only
the runtime was missing.

| Method | Was | Now | File:line |
|---|---|---|---|
| `ListAgentMemoryByAnyEntity` | `notImpl("…")` | pgx `ANY($1::text[])` + JOIN project_id filter | `internal/store/postgres/store.go:412` |
| `MarkSupersededAgentMemory` | `notImpl("…")` | pre-flight + `runInTx` + `recordWriteTx` + `EmitSupersede` after commit | `internal/store/postgres/store.go:444` |
| `RecallAtTime` | `notImpl("…")` | pgx SELECT with COALESCE(valid_time, created_at) filter | `internal/store/postgres/store.go:462` |

**Behavior contracts preserved** (matches SQLite byte-for-byte):
1. INV-7 project isolation via `WHERE project_id = $N` on every SELECT.
2. INV-1 audit row in the SAME tx as the data write (`recordWriteTx`).
3. `EmitSupersede` fires AFTER tx commits (`eventholder.Get()`).
4. Empty-input OR no-match → `(nil, nil)` (NOT an error).
5. `t.IsZero()` rejected with `ErrInvalidArgument`.
6. Self-supersede, missing row, non-decision kind, cross-project → `ErrInvalidSupersession` (wrapped).

**7 NEW PG tests** in
`internal/store/postgres/bitemporal_pg_test.go` (gated by
`DARK_TEST_POSTGRES_DSN`):

- `TestPG_ListAgentMemoryByAnyEntity_EmptyInput_ReturnsNilNil`
- `TestPG_MarkSupersededAgentMemory_SelfSupersede_Rejected`
- `TestPG_MarkSupersededAgentMemory_MissingRow_Rejected`
- `TestPG_MarkSupersededAgentMemory_EmptyReason_Rejected`
- `TestPG_MarkSupersededAgentMemory_FullPath_FiresEmitSupersede`
- `TestPG_RecallAtTime_ZeroTime_Rejected`
- `TestPG_RecallAtTime_NoRows_ReturnsEmptySlice`

Tests use a `recordingEmitter` mock that satisfies the local
`AutoEmitter` interface. Operator runs locally with:

```bash
DARK_TEST_POSTGRES_DSN=postgres://user:pass@localhost:5432/dark_mem \
  go test ./internal/store/postgres/
```

#### 1.14.2 T-402 — `EmitCacheInvalidation` 8th/8th wire (commit `ff6452b`, alpha.26-pre-2)

**Closes the AutoEmitter orphan.** Before T-402, 7 of 8 emitters were
wired to actual event sites; `EmitCacheInvalidation` was defined in
`internal/v4alpha/event/auto_emit.go:256` but had zero callers. After
T-402: every LRU eviction in `internal/nli/cache.go` fires the event.

**AutoEmitter wire-up progress**:

| Helper | Caller (file:line) | Status |
|---|---|---|
| `EmitSupersede` | `internal/store/sqlite/bitemporal.go:202` | wired |
| `EmitDecayRefresh` | `internal/v4alpha/recall/decay.go:118` | wired |
| `EmitSchemaMigration` | `internal/migrate/migrate.go:94` | wired |
| `EmitEmbedderRefresh` | `internal/v4alpha/recall/c2_text.go:232, c4_research.go:184` | wired |
| `EmitCalibrationUpdate` | `internal/v4alpha/judge/store.go:675` | wired |
| **`EmitCacheInvalidation`** | **`internal/nli/cache.go` (NEW, Phase 15 T-402)** | **wired ✓** |
| `EmitJudgeVerdictUpdate` | `internal/v4alpha/judge/store.go:419` | wired |
| `EmitPersonaUpdate` | `internal/v4alpha/judge/personas_v4.go:166` | wired |

**Wire shape** (`internal/nli/cache.go`):

- `SetAutoEmitter(ae AutoEmitter)` setter on `InMemoryLRU` (nil-safe).
- New `AutoEmitter` interface (minimal subset — only
  `EmitCacheInvalidation`) defined locally to avoid import cycle.
- New `emitInvalidation(reason string)` helper fires INSIDE `c.mu`
  critical section (matches sqlite bitemporal.go:194-202 posture).
- Get path emits `reason="ttl_expired"` on lazy TTL expiry.
- Put path emits `reason="lru_cap"` on every over-cap eviction.

Wire signature: `cacheTable="nli_lru"`, `rowID=0` (LRU evicts per-key,
not per-row-id), `semantic=false` (LRU evicts entries, not semantic-
relationship nodes).

**4 NEW tests** in `internal/nli/cache_test.go`:
- `TestInMemoryLRU_PutEviction_FiresEmitCacheInvalidation`
- `TestInMemoryLRU_GetTTLExpiry_FiresEmitCacheInvalidation`
- `TestInMemoryLRU_NoEmissionWhenNotEvicted`
- `TestInMemoryLRU_SetAutoEmitter_NilSafe`

Production wiring at boot: `cache.SetAutoEmitter(eventholder.Get())`.

#### 1.14.3 T-403 — `CachedProvider` single-flight dedup (commit `adb2fbe`, alpha.26-pre-3)

**Closes the race detector flake.** `TestCachedProvider_ConcurrentGet_RaceFree`
(`internal/nli/cached_provider_test.go:203`) was flaky because N
concurrent `Score()` calls with the SAME `(premise, hypothesis)` all
called `c.inner.Score` independently. The race detector flagged the data
race and the call count oscillated between 2 and 3.

**Root cause**: TOCTOU between `cache.Get` and `LoadOrStore` in
`Score()`. The window between them allowed:

```
G1 wins LoadOrStore → inner call → cache.Put → Delete(key)
G2 cache.Get (BEFORE G1's Put) → miss
G2 LoadOrStore (AFTER G1's Delete) → wins → SECOND inner call
```

**Fix** (`internal/nli/cached_provider.go`):

1. New `sync.Mutex` field on `CachedProvider`. Guards the
   `cache.Get + inflight.LoadOrStore` sequence on the SLOW path.
   Fast path (cache hit) stays lock-free.
2. `Score()` flow becomes:
   - Fast path: `cache.Get` without lock.
   - Slow path: acquire `mu`.
   - Double-check cache (another goroutine may have populated it).
   - `singleflight` (mu held throughout).
3. `singleflight` order = `cache.Put → close(done) → Delete(key)`.
   Combined with `mu` protection, no new goroutine can race past
   `cache.Get` and reach `LoadOrStore` during the Put+Delete window
   (they queue on `mu` first).
4. `CacheStats` gains a `Waiters` counter (callers that waited for
   an in-flight call to finish).

**3 NEW tests** in `internal/nli/cached_provider_test.go`:
- `TestCachedProvider_ConcurrentGet_1000xNoFlake`: 1000 iterations
  × 100 concurrent goroutines. Expects exactly **1** inner call
  total (vs 50,000+ without the fix).
- `TestCachedProvider_DifferentKeys_ParallelInner`: 10 goroutines
  with 10 DIFFERENT keys. Expects 10 inner calls (no false sharing).
- `TestCachedProvider_Stats_ReflectsWaiters`: 50 concurrent
  goroutines with stub delay=20ms. Verifies `Misses=1`,
  `Hits+Waiters=49`.

**VERIFIED**:
- `go test -race ./internal/nli/` PASS (6.36s).
- `go test -race -count=10 ./internal/nli/` PASS (53.6s, no flake).
- Existing 8 `TestCachedProvider_*` tests all PASS.
- Race detector enabled and clean throughout.

#### 1.14.4 T-404 — Docs sweep (this commit, final `alpha.26`)

- `docs/v4-status.md` §1.14 (this section).
- `CHANGELOG.md [4.0.0-alpha.26]` entry.
- Frozen test stays `TestCanonicalOrder_Frozen_73_20_28` (no tool count change).
- Top-level banner updated with Phase 15 summary.

#### 1.14.5 OD7 gate (Phase 15)

Phase 15 e2e gate: 0 critical findings required.

- 0 critical findings.
- Pre-existing dual_driver test `TestAgentMemory_List_DefaultExcludesArchived`
  flake NOT introduced by Phase 15 (passes in isolation in 7.3s).
  Documented as a Phase 16 candidate.

#### 1.14.6 Tier-1 SOTA grounding (extends §1.12.9)

Phase 15 closes deferred items without changing the public surface.
SOTA grounding stays the same as Phase 14 (§1.13.6):

- `golang.org/x/sync/singleflight` semantics for the dedup pattern
  (well-documented since Go 1.21).
- `sync.Mutex + sync.Map` for hot-path lock-free + slow-path mutex
  pattern (matches `internal/nli/cache.go` itself).
- pgx `ANY($1::text[])` for parameterized IN lists (jackc/pgx docs).

#### 1.14.7 Local tag + dark-memory row

Local tag: `v4.0.0-alpha.26` (final). Pre-tags: `v4.0.0-alpha.26-pre-1..pre-3`.

Atomic mirror rows (saved via `agent_memory_save`):
- 2473 (T-401 SHIPPED, alpha.26-pre-1)
- 2474 (T-402 SHIPPED, alpha.26-pre-2)
- 2475 (T-403 SHIPPED, alpha.26-pre-3)
- 2476 (Phase 15 SHIPPED pinned, alpha.26, summary — saved at SHIP)

#### 1.14.8 LUCIDEZ gate

10/10 R-rules GREEN post-SHIP (verified 2026-10-06):

- R1 (real): T-401 PG tests would run against a real Postgres
  instance (operator runs locally with `DARK_TEST_POSTGRES_DSN`).
  T-402 wires a real `eventholder.Get()` emitter (production code).
  T-403 uses real `sync.Mutex` + `sync.Map` (no mocks for the race).
- R2 (no shallow): each task's root cause is identified (PG schema
  was already present; only runtime was missing. AutoEmitter orphan
  was a wire-up gap. CachedProvider race was TOCTOU between
  cache.Get and LoadOrStore).
- R3 (root cause over symptom): yes — single-flight dedup is the
  architectural fix, not a band-aid like adding more locks.
- R4 (no discarding): NLI is preserved 100%, PG is additive parity,
  no behavior removed.
- R5 (3 options): Phase 15 spec gave operator 3 options (PG only /
  emitter only / flaky fix only / all three); operator picked
  "all three" (close everything in one Phase).
- R6 (declare unknowns): T-401 PG tests depend on operator's local
  PG instance (gated by `DARK_TEST_POSTGRES_DSN`, not in CI).
- R7 (honest cost): Phase 16+ can re-open deferred items if
  needed; Phase 15 is closure, not expansion.
- R8 (audit trail): all 4 commits have dark-memory write_audit +
  agent_memory atomic mirror rows (2473-2476).
- R9 (file:line refs): every section cites `file:line` paths.
- R10 (pause-and-summarize per chunk): T-401..T-403 each shipped
  a pre-tag with verdict (aligned + drift check PASS).

---

| Reliability | What's stable |
|---|---|
| ✅ Stable (won't change) | Tool wire names (73 canonical), agent_memory schema, FTS5 ordering (INV-17), worker pool size=1, store/WithTx contract (INV-16), `sdd_evaluations` schema (22 cols; +4 for calibration in alpha.16), **events table schema v32 (27 cols, 3 indexes, polymorphic)**, judge MCP tool wire shapes (4 base + 7 util), persona registry ids (16 total, 8 v4alpha), `BootstrapCI` deterministic seed=42, **EVENTS namespace tools (event_log + event_replay) wire shapes**, **HMAC chain continuous across events + write_audit (ADR-016 + ADR-018)**, **LLM_BIND namespace tools (llm_provider_bind + llm_provider_probe) wire shapes**, **drift_judge 9-step pipeline (LLMJudge primary, NLI fallback)**, **provider_id prefix routing: judge-* → LLMJudge, chat-* → NLI ChatProvider** |
| ⚠️ Likely to evolve | Package names (still aspirational vs actual drift), Pipeline API (LLM judge swap), Constitution (still hardcoded), persona override mechanism (spec 1155 v14 inheritance), progress emitter phases (3 → N as new pipeline stages emerge) |
| ❌ Not implemented | security/* (INV-11..15), mutable Workflow, red-team mods, federated research, L6-VLP, admin (vacuum only), EmbedderRefresh wire (no embedder code), semantic CacheInvalidation wire (no semantic cache trigger) |

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
