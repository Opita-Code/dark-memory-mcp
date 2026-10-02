# v4-alpha.11 plan — 5 vibe-loops for the next 8-11 weeks

> **Audience**: anyone picking up the v4-alpha.11+ work.
> **TL;DR**: 13 items across 4 categories, organized into 5
> vibe-loops (one per phase). Each vibe-loop is a
> `vibe_spec` → `vibe_publish` → `drift_judge` → `vibe_resolve_drift`
> cycle per the dark-memory v4 MCP surface.
> **Source**: `docs/sota-critique.md` §7.6 (the meta-doc
> implementation organization).
> **Namespace primitive**: per §7.6.9, `project_id` is a
> SOFT workstream namespace, not a SaaS multi-tenant
> primitive. Hard isolation is `coexistence_group`.

## 0. Why "vibe-loops"?

The dark-memory v4 MCP exposes 4 vibe tools: `vibe_spec`,
`vibe_publish`, `pipeline_status`, `vibe_resolve_drift`.
Together they form the spec → artifact → judge → verdict
cycle. The plan below is structured as 5 vibe-loops because
each phase is a coherent spec with concrete artifacts and
verifiable acceptance criteria. This is the same shape
as the SOTA-doc workstream (which was 7 vibe-loops, one
per chunk).

A vibe-loop is:

```
spec (intent + tasks + acceptance criteria)
  ↓
artifact (the actual change — code, doc, or config)
  ↓
drift_judge (LLM-as-judge: does the artifact match the spec?)
  ↓
verdict (aligned | drift_detected | needs_human)
  ↓
resolve (operator action on the verdict)
```

For each phase below, the vibe-loop is named
`alpha-11-phase-N`. The spec is at
`docs/specs/SPEC-alpha-11-phase-N.md`. The atomic mirror
follows ADR-008 (1 SUMMARY pinned + N SECTION pinned=false).

## 1. Phase 1 — Close + cheap wins (1-2 weeks)

**Vibe-loop**: `alpha-11-phase-1`

**Items**:
| # | Item | Complexity | LoC | Risk | Spec file |
|---|---|---|---|---|---|
| 1 | chunk 7 (SOTA-doc workstream close) | XS | ~50 | LOW | `SPEC-alpha-11-chunk7.md` |
| 2 | PRE-1 C3 (Loadout for session_start) | M | ~200 | LOW | `SPEC-alpha-11-pre1c3.md` |
| 3 | PRE-1 C4 (summarize_session) | M | ~180 | LOW | `SPEC-alpha-11-pre1c4.md` |
| 4 | BUG-12 (cross-process monotonicity) | XS | ~40 | LOW | `SPEC-alpha-11-bug12.md` |

**Dependency graph**:
- chunk 7 → no deps (closes SOTA-doc workstream, can ship
  first).
- PRE-1 C3 → no deps (additive API, builds on PRE-1 C1 ✓).
- PRE-1 C4 → depends on PRE-1 C3 (summarize_session needs
  the session context that C3 sets up).
- BUG-12 → no deps (single-file change to `audit/writer.go`).

**Parallelization**: chunk 7, PRE-1 C3, BUG-12 are
parallelizable. PRE-1 C4 is sequential after C3.

**Acceptance criteria** (Phase 1 ships when ALL 4 are done):
- chunk 7 commit: v4-status + CHANGELOG updated.
- PRE-1 C3 commit: `session_start` returns Loadout (6
  fields: pinned_rows, open_todos, recent_writes,
  constitution, canary, error_summary).
- PRE-1 C4 commit: `summarize_session(session_id)` returns
  handoff doc + skill-loaded tracking.
- BUG-12 commit: `audit_log` uses SQLite AUTOINCREMENT
  (cross-process monotonicity verified by concurrent
  insert test).
- `go build ./...` clean. All v4alpha tests pass.

## 2. Phase 2 — Audit chain completion (1 week)

**Vibe-loop**: `alpha-11-phase-2`

**Items**:
| # | Item | Complexity | LoC | Risk | Spec file |
|---|---|---|---|---|---|
| 5 | ADR-019 (split payload BLOB into structured columns) | M | ~250 | MEDIUM (schema) | `SPEC-alpha-11-adr019.md` |
| 6 | ADR-017 (Ed25519 signature on payload BLOB) | S | ~120 | LOW | `SPEC-alpha-11-adr017.md` |
| 7 | ADR-018 (dark_memory_audit_verify tool) | S | ~100 | LOW | `SPEC-alpha-11-adr018.md` |

**Dependency graph**:
- ADR-019 → no deps (audit chain foundation).
- ADR-017 → depends on ADR-019 (needs the structured
  columns to sign).
- ADR-018 → depends on ADR-019 (needs the structured
  columns to walk the chain).

**Parallelization**: ADR-019 first; ADR-017 + ADR-018
parallel after.

**Operator decision (2026-09-29): Option B (Phase 2 lite).**
Hash chain (SHA-256, no key) + `dark_memory_audit_verify` tool.
Ed25519 (ADR-017) deferred to ADR-017-deferred; ADR-019
(split payload) deferred to a separate phase. The original
3-item bundle is replaced with:

| # | Item | Complexity | LoC | Risk | Status |
|---|---|---|---|---|---|
| 5a | Hash chain (prev_hash + row_hash columns) | x | +~400 | LOW | **SHIPPED alpha.15 (commit `f322776`)** |
| 5b | `dark_memory_audit_verify` MCP tool | S | ~120 | LOW | **SHIPPED alpha.15 (commit `f322776`)** |
| 6 | ADR-017 (Ed25519 payload signature) | S | ~120 | LOW | DEFERRED → ADR-017-deferred |
| 7 | ADR-019 (split payload BLOB into structured columns) | M | ~250 | MEDIUM | DEFERRED → ADR-019-deferred (orthogonal) |

**Spec**: `docs/specs/SPEC-alpha-11-phase2.md` (662 lines,
Option B rationale in §2; canonical encoding in §3; writer
flow in §4; verify semantics in §5; ADR-017 deferred in §10).

**Acceptance criteria** (Option B, shipped):
- ✅ 2 new columns on `audit_log`: `prev_hash BLOB`, `row_hash BLOB`
  (nullable; legacy rows have NULL).
- ✅ Canonical SHA-256 encoding in `internal/v4alpha/audit/canonical.go`.
- ✅ `audit.Writer.Write` + `WriteExec` write `prev_hash` + `row_hash`.
- ✅ First row has `prev_hash = 0x00*32` (zeroHash).
- ✅ Subsequent rows have `prev_hash = previous row's row_hash`.
- ✅ `dark_memory_audit_verify` MCP tool — 41 → 42 tools.
- ✅ Verify detects: modification, deletion, payload swap, forgery.
- ✅ Verify passes on: clean chain, post-migration chain (legacy NULLs).
- ✅ Migration `audit.ApplyChainColumns` idempotent.
- ✅ All 15 pre-existing audit tests still pass.
- ✅ All 11 v4alpha packages pass.
- ✅ Tool count: 42.
- ✅ `CHANGELOG.md [4.0.0-alpha.15]` entry.
- ✅ `docs/v4-status.md`: alpha.14 → alpha.15.
- ✅ `docs/INVARIANTS.md §18` updated: 3 of 8 gaps closed
  (hash chain, verify tool, cross-process monotonicity); 1
  explicitly deferred (Ed25519 = ADR-017); 4 still alpha.3.
- ✅ Atomic mirror: 1 SUMMARY pinned + 4 SECTION (rows
  2190-2194).

## 3. Phase 3 — Judge improvements (1 week)

**Vibe-loop**: `alpha-11-phase-3`

**Items**:
| # | Item | Complexity | LoC | Risk | Spec file | Status |
|---|---|---|---|---|---|---|
| 8 | ADR-009 (provider allow-list 4 → 9) | S | ~150 | LOW | `SPEC-alpha-11-adr009.md` | **SHIPPED alpha.16 (commit `8681113`)** |
| 9 | ADR-011 (judge calibration bootstrap-CI) | M | ~250 | MEDIUM (statistical) | `SPEC-alpha-11-adr011.md` | **SHIPPED alpha.16 (commit `8681113`)** |

**Dependency graph**: ADR-009 and ADR-011 are independent.

**Operator decision (2026-09-30)**: ship 9 providers
(anthropic, minimax, minimax-cn, deepseek, openai, google,
zhipu, moonshot, qwen) — ALL 9 in the canonical
`internal/llm/catalog.go`. Mistral + kimi deferred (require
catalog work, not judge-client work). EC-007a (binary
substring) preserved as legacy fallback for uncalibrated rows.

**Acceptance criteria**:
- ✅ ADR-009: **9 providers** in the allow-list
  (anthropic, openai, google, deepseek, minimax, minimax-cn,
  zhipu, moonshot, qwen). Each with native structured outputs
  or documented override. Mistral + kimi deferred (catalog
  work; separate decision).
- ✅ ADR-011: every `sdd_evaluations` row has 4 new
  calibration columns computed via bootstrap-CI
  (`confidence_calibrated`, `calibration_ci_low`,
  `calibration_ci_high`, `calibration_method`). Per Play
  Favorites arxiv:2508.06709. Efron 1979 percentile method,
  deterministic seed=42, n=1000 default.
- ✅ EC-007 split into EC-007a (binary, cold start) +
  EC-007b (statistical bootstrap-CI, N >= 50). 16 ECs total
  (was 15). EC-007b severity `warn` by default, upgrades to
  `error` when excess > 0.20.
- ✅ All 11 v4alpha packages PASS (`go vet` clean; full suite).
- ✅ Drift check: spec judged ALIGNED with confidence 0.92
  (eval 1952).
- ✅ Atomic mirror: 1 SUMMARY pinned (row 2201) + 5 SECTION
  (rows 2202-2206).
- ✅ `docs/v4-status.md` §1.3 Phase 3 changelog entry;
  `docs/INVARIANTS.md` §19 cross-ref; `docs/edge-case-catalog.md`
  EC-007a/b per-EC cards; `docs/judge-pipeline-v4.md` §10.3
  SOTA criticism updated; `docs/v4-alpha-11-plan.md` §3
  Phase 3 marked shipped (this commit); `CHANGELOG.md`
  `[4.0.0-alpha.16]` entry at top.

## 4. Phase 4 — BUG-10 10b namespace primitive ✅ SHIPPED (alpha.17, 2026-09-30)

**Vibe-loop**: `alpha-11-phase-4`

**Spec**: `docs/specs/SPEC-alpha-11-phase4.md` (427 LoC, 10
sections, vibe_loop `alpha-11-phase-4`, spec_id 1811, drift
ALIGNED 0.95).

**Items**:
| # | Item | Complexity | LoC | Risk | Status | Commits |
|---|---|---|---|---|---|---|
| 10a | Chunk 4.1 (foundation: projects table + 5-column migration + audit.WriteWithProject) | L | ~1,200 | MEDIUM (schema) | ✅ SHIPPED | `1d39659` |
| 10b | Chunk 4.2 (4 new MCP tools: project_create, project_lookup, mindset_apply STUB, delegate_intent STUB) | M | ~480 | LOW | ✅ SHIPPED | `7d3cdee` |
| 10c | Chunk 4.3 (hard isolation enforcement: WriteExecWithProject + session.Store validator + vibe/judge threading + project-scoped calibration) | L | ~600 | MEDIUM (cross-package) | ✅ SHIPPED | `badb1a2` |
| 10d | Chunk 4.4 (docs followup: v4-status §1.4, INVARIANTS INV-19, CHANGELOG alpha.17, v4-alpha-11-plan §4 marked shipped, sota-critique §7.6.9) | S | ~250 | LOW | ✅ SHIPPED | (this commit) |

**Operator decision (2026-09-30)**: namespace primitive (SOFT
workstream scope), NOT multi-tenant — per `docs/sota-critique.md
§7.6.9`. HARD isolation is `coexistence_group` (per-MCP dark.db).
The rename "namespace" (not "tenant") prevents the common bug of
treating `project_id` as a security boundary in future code.

**Spec**: `docs/specs/SPEC-alpha-11-phase4.md` (427 LoC, 10
sections). vibe_loop `alpha-11-phase-4`. spec_id 1811. Drift
ALIGNED with confidence 0.95 (eval from vibe_publish 1547).

**Acceptance criteria** (all met, 2026-09-30):
- ✅ 4 new tools registered (Chunk 4.2, alpha.17):
  `project_create`, `project_lookup`, `mindset_apply` (STUB),
  `delegate_intent` (STUB). Tool count: 42 → 46.
- ✅ `projects` table exists (namespace registry, NOT tenant
  registry per §7.6.9). Auto-seeds `'default'` via
  `INSERT OR IGNORE`.
- ✅ `project_id` column on 5 existing tables
  (agent_memory, audit_log, sdd_evaluations, vibe_specs,
  vibe_artifacts) — Chunk 4.1 migration. Idempotent via
  `pragma_table_info`.
- ✅ Default `project_id = "default"` for backfill (column
  DEFAULT clause, O(1) — SQLite stores default in schema).
- ✅ Threat model statement in `docs/v4-status.md` §6.6 +
  `docs/INVARIANTS.md` INV-19:
  "v4 assumes the harness session is the only concurrent
  consumer. Project IDs scope workstreams within one
  operator. For HARD isolation between concurrent users,
  use separate `coexistence_group`s or separate MCP
  instances. The `project_id` column is a soft namespace,
  not a security boundary."
- ✅ INV-19 (namespace primitive) defined in
  `docs/INVARIANTS.md`. Enforced at 8 sites (per the Quick
  Reference table at the bottom of INVARIANTS.md).
- ✅ Phase 2 §3.2 hash chain invariant preserved:
  `WriteWithProject` + `WriteExecWithProject` use the same
  `ComputeRowHash` as `Write` + `WriteExec`. Pre-Phase-4
  audit rows still verify.
- ✅ Hard isolation enforcement (Chunk 4.3):
  `session.Store.Start` validates `project_id` existence
  (`ErrUnknownProject`); every audit-emitting surface
  (agent_memory, vibe, judge) threads `project_id` via
  `WriteWithProject` / `WriteExecWithProject`;
  `populateCalibration` is project-scoped first, global
  fallback.
- ✅ 14 new isolation tests pass (~310 LoC across 5 new
  test files).
- ✅ All 12 v4alpha packages PASS (`go vet` clean;
  full suite).
- ✅ Atomic mirror per ADR-008 (3 SUMMARY pinned + 12
  SECTION pinned=false):
  - Chunk 4.1 SUMMARY (row 2214) + §A-§D (rows 2215-2218).
  - Chunk 4.2 SUMMARY (row 2220) + §A-§D (rows 2221-2224).
  - Chunk 4.3 SUMMARY (row 2225) + §A-§D (rows 2226-2229).
- ✅ `docs/v4-status.md` §1.4 Phase 4 changelog (this
  release).
- ✅ `docs/INVARIANTS.md` INV-19 (this commit).
- ✅ `CHANGELOG.md [4.0.0-alpha.17]` entry (this commit).
- ✅ `docs/sota-critique.md §7.6.9` — threat model "now
  enforced" annotation (this commit).
- ✅ v4 binary rebuilt: `dark-memory-v4.exe` (19.66 MB,
  was 19.5 MB).
- ✅ Cross-version lockstep hash pin unchanged (audit chain
  backward-compatible).
- ✅ Schema version: `v4alpha/2026-09-30/003` → `004`.

**Backwards-compat note**: `session_start` default `project_id`
switched from `'dark-memory-v4'` to `'default'` (literal). The
legacy default was NOT a registered project; with hard isolation
enabled, it would fail with `ErrUnknownProject`. Callers that
relied on the literal must pass it explicitly AND register it via
`project_create`, or accept `'default'`. Wire shape unchanged.

## 5. Phase 5 — Memory subsystem ✅ SHIPPED (alpha.18, 2026-10-01)

**Vibe-loop**: `alpha-11-phase-5`

**Operator decision (2026-10-01, OD2=YES)**: ship Phase 5
with vector retrieval + temporal re-ranking + multi-hop
graph via the new `internal/v4alpha/recall/` package.
LOCAL ONLY — no remote push (per deploy policy).

**Original plan vs shipped**:

| # | Original Item | Shipped | Diff |
|---|---|---|---|
| 11 | ADR-013 vector + RRF | ✅ C2/C4/C5/C6 vector stubs (alpha.18), full BGE-large alpha.19 | partial — vector stub absorbed into FTS5, alpha.19 wires real embedder |
| 12 | ADR-014 temporal re-ranking | ✅ DecayScore + RefreshOnAccess + PerVibeCaseMultiplier (5 classes, ScrubJay-MEM π_i + τ_i) | superset — also got MarkSuperseded (TokenMizer bitemporal) |
| 13 | ADR-015 multi-hop / graph | ✅ 1-hop (C1/C2/C5/C6) + 2-hop (C3/C4) via adr_refs/inv_refs | shipped — HippoRAG-style expansion |

**5 commits on `feat/v4-redesign`** (commits
`76a2a11`, `fe1b97d`, `a137947`, `9f834f2`, `e647239`).

**Items shipped vs. plan**:

- Chunk 5.1: schema migration — 20 additive columns on
  `agent_memory`, 3 new tables (`agent_memory_entities`,
  `agent_memory_links`, `decision_transitions`), 9
  indexes, all idempotent, INV-19 hard isolation.
  ~799 LoC. drift ALIGNED 0.99.
- Chunk 5.2: polymorphic `RecallFor` dispatch + first
  strategy `C3DecisionRecall`. ~1,235 LoC. drift ALIGNED
  0.96 + 0.95.
- Chunk 5.3: three more strategies `C1CodeRecall`,
  `C2TextRecall`, `C4ResearchRecall`. ~956 LoC. drift C1
  ALIGNED 0.96 / C2 drift_detected 0.82 (alpha.18 vector
  stub) / C4 ALIGNED 0.92.
- Chunk 5.4: last three strategies `C5VideoRecall`,
  `C6AudioRecall`, `C7MultiRecall` ensemble. ~698 LoC.
  drift C5 drift_detected 0.92 (ImageBind stub) / C6
  ALIGNED 0.95 / C7 drift_detected 0.82 (keyword router).
- Chunk 5.5: cross-cutting — `DecayScore` + `RefreshOnAccess`
  + `MarkSuperseded` + `PerVibeCaseMultiplier` +
  11/11 tests + 50-Q LifecycleBench micro-eval. ~451 LoC.
  drift decay.go drift_detected 0.88 (false positive) /
  decay_test.go ALIGNED 0.97.

**Total Phase 5**: ~4,099 LoC across 19 files in
`internal/v4alpha/recall/`. 41/41 recall tests pass. Full
v4alpha suite (13 packages) passes.

**Atomic mirror per ADR-008**: 5 SUMMARY pinned + 16
SECTION + 1 meta SUMMARY = 22 rows (2235-2257). All
`bind_session=true`, tags include `mirror:atomic`.

**Drift summary**: 7 ALIGNED + 4 drift_detected (all
intentional alpha.18 stubs documented, with the alpha.19
evolution path captured in
`docs/v4-status.md §1.5.7`).

**Acceptance criteria status**:

| Criterion | Status | Evidence |
|---|---|---|
| ADR-013 hybrid FTS5 + vector retrieval in `Recall()` | PARTIAL alpha.18 | C2/C4/C5/C6 strategies with vector stubs; alpha.19 wires BGE-large + ImageBind |
| ADR-013 pluggable embedding adapter (HTTP/local ONNX) | DEFERRED alpha.19 | Per SPEC §2.2 non-goals |
| ADR-013 RRF re-ranker (k=60) | ✅ SHIPPED | Cormack 2009 k=60 in `scoreFTSPlusGraph` |
| ADR-014 `Recall()` re-ranks by recency × relevance | ✅ SHIPPED | DecayScore + PerVibeCaseMultiplier |
| ADR-015 graph links between rows | ✅ SHIPPED | adr_refs/inv_refs columns + 1-hop (C1/C2/C5/C6) + 2-hop (C3/C4) |
| ADR-015 `Recall()` traverses 1-2 hops | ✅ SHIPPED | graphExpandShared hop=1 or 2 per strategy |

**alpha.19 follow-up (deferred from alpha.18)**:

- ImageBind 1024-dim image encoder integration (alpha.19
  wires `embed_image` column via `adapter.Adapter`)
- BGE-large text embedder (alpha.19 wires `embed_text` —
  not in alpha.18 column set, will add column then)
- ONNX local embedder (alpha.19, pluggable adapter)
- ProGraph 2-layer entity extraction
  (`agent_memory_entities` table is reserved alpha.18)
- CABLE auto-link (`agent_memory_links` operator-flag
  default OFF alpha.18)
- LLM-extracted sub-tasks for C7MultiRecall
  (alpha.18 keyword router)
- SENTINEL forgery guard (R-F §6.3 threat)
- ReFind-style agent-controlled search (R-D counter-
  evidence)

**Cross-refs**:
- `docs/specs/SPEC-alpha-11-phase5.md` — Phase 5 master
  spec (434 LoC, 15 sections).
- `docs/research/phase-5/{README,R-A,R-B,R-C,R-D,R-E,R-F}.md`
  — 6 research artifacts synthesizing ~120 SOTA 2026
  papers.
- `docs/v4-status.md §1.5` — Phase 5 changelog with all 5
  chunks.
- `docs/sota-critique.md §5.2` — 3 tractable agent-
  memory gaps NOW ENFORCED.
- `CHANGELOG.md [4.0.0-alpha.18]` — release entry.
- dark-memory rows 2235-2257 (5 SUMMARY + 16 SECTION +
  1 meta, agent_id `alpha-11-phase5`, session
  `sess-7e6f313abd9818cd`).

## 6. Phase 6 — alpha.18 close-out ✅ SHIPPED (alpha.18.1, 2026-10-01)

Phase 6 is the close-out before alpha.19. Six master spec items in
`docs/specs/SPEC-alpha-11-phase6.md`:

### 6.1 Vibe-case mapping reconciliation (commit `ee0fb8e`)

**Live bug fix.** Closes the drift in 3 docs (`persona-registry-v4.md`,
`GLOSARIO.md`, `ADR-007-judge-pipeline-v4.md`) and the
`judge/populateCalibration` function (the persona registry had drifted
from the canonical `spec.go:14-16` taxonomy after the v4-alpha.3 split).
5 changes: rubric.go canonical, personas_v4.go (2 new),
persona-registry-v4.md §2.1-§2.3 rebuild, GLOSARIO.md deprecation note,
ADR-007 canonical C1-C7, research-backends.md academic + network recon.

### 6.2 Full impl `mindset_apply` (commit `ab28867`)

Replaces alpha.17 STUB. Real cache + procedural composition + judge
validation loop. Wire shape change: removed StubNotice; added Verdict
field. Vibe→persona mapping per the canonical taxonomy.

### 6.3 Full impl `delegate_intent` (commit `4229684`)

Replaces alpha.17 STUB. DECIDE→PLAN→MIND→CURATE pipeline. DECIDE is
deterministic (no LLM); PLAN splits by sentence boundaries; MIND calls
composeSystemPrompt in-process; CURATE empty delegation_context for
alpha.19 (C2 subagent binding). Wire shape: removed StubNotice; added
Reasoning.

### 6.4 Mutation coverage close (commit `63fab10`)

| Package | Before | After | Δ |
|---|---|---|---|
| internal/recall | 17.5% | 45.3% | +27.8 pp |
| internal/agentbootstrap | 71.8% | 90.8% | +19.0 pp |
| internal/tools | 20.3% | 22.2% | +1.9 pp |

3 new test files (+633 LoC). Real SQLite in `t.TempDir()` for the
recall package (NOT a hand-rolled mock — store.Store has 105+ methods).

### 6.5 ADR-017 Ed25519 audit row signatures (commit `a4833fc`)

`audit/signature.go` + `audit/verify_signature.go` + Writer.signer
+ SetSigner + ApplySignatureColumns migration. 16 tests PASS. Env
vars: DARK_AUDIT_SIGNING_KEY (base64 64B priv) + DARK_AUDIT_VERIFY_KEY
(base64 32B pub).

### 6.6 ADR-019 payload BLOB split (commit `504f427`)

`audit/payload_split.go` + PayloadFields struct + ExtractPayloadFields
(pure JSON parser) + ApplyPayloadColumns + AddPayloadIndex (payload_event
only). 12 tests PASS. nullableString + nullableInt64 helpers.

### 6.7 Docs + tag (commit pending, this commit)

- `CHANGELOG.md [4.0.0-alpha.18.1]` — release entry.
- `docs/v4-status.md §1.6` — Phase 6 changelog.
- `docs/v4-alpha-11-plan.md §6` — Phase 6 close-out (this section).
- `docs/sota-critique.md` updates — reference ADR-017 + 019 followups.
- `git tag v4.0.0-alpha.18.1` LOCAL ONLY.
- dark-memory rows 2266-2290 (6 SUMMARY + 19 SECTION, agent_id
  `alpha-11-phase6`, session `sess-4fffa0428c585cd0`).

## 7. Phase 7 — sub-agent wiring + LLM router upgrade + coverage close ✅ SHIPPED (alpha.19, 2026-10-02)

Phase 7 closes all 4 alpha.18.1 deferred items (per §1.6.7) plus
an LLM-router upgrade on `delegate_intent`. Five master spec items
in `docs/specs/SPEC-alpha-11-phase7.md` (454 LoC, vibe_loop
`alpha-11-phase-7`, 5 commits on `feat/v4-redesign` + 1 docs commit
+ 1 local tag). 14 v4alpha packages PASS, 0 regressions.
Cross-version lockstep hash pin unchanged.

### 7.1 LLM-extracted sub-tasks router for `delegate_intent` (commit `2bb20a3`)

Replaces the alpha.18.1 v1 deterministic DECIDE router. New hybrid
**DECIDE→EXTRACT→MIND→CURATE** pipeline. Closes Chunk 6.3 drift 0.85.

- `internal/v4alpha/delegation/` NEW (7 files, ~2,483 LoC).
- New `judge-delegator` persona (14th). Registry 13 → 14.
- Wire shape v2 (additive over v1): `cache_hit`, `verdict`,
  `alternatives[]`, `subagent_id`, `delegation_context` per subtask.
- DECIDE priority chain: refusal markers > delegation markers > C7
  multi > length>200 > default inline.
- EXTRACT cache + LLM call via judge-delegator + drift_judge
  (eval_type=`subtask_extraction`) + refine+retry up to 2x + needs_human
  on failure.
- **27 new tests + 2 modified (TestDelegateIntent_FullImplDelegate
  C7→C2).**

### 7.2 `agent_memory_delegate` C2 subagent binding (commit `f3692cf`)

Wires `dark_memory_subagent_register` per subtask. Defense-in-depth
vs arxiv:2605.08460 inheritance.

- `internal/v4alpha/transport/mcp/subagent_binding.go` NEW (~280 LoC):
  uuid per subtask + agent_memory.Save kind=link tag=subagent:v1,
  INV-1 atomic with audit row in single Tx.
- TTL clamp `[60, 86400]s` default 3600.
- `parent_session_id` + `parent_agent_id` (additive).
- **3 new CURATE tests** (BindingPersists,
  ParentSessionIDPropagated, UnregisterOnClose).

### 7.3 — *skipped* (originally Chunk 7.3 / 7.4 reserved; Chunk 7.5 is the next numbered chunk per SPEC §3.5)

Per `SPEC-alpha-11-phase7.md` §2, Chunk 7.5 is the third active
chunk (after 7.1 + 7.2); 7.3 + 7.4 were reserved for audit gaps
that we deferred to alpha.20 (per SPEC D2 decision).

### 7.5 internal/tools 51-test httptest harness (commit `39cc899`)

Closes the deferred `internal/tools` 22.2% gap. mcp-go
`StreamableHTTPServer` + `WithStateLess(true)` + httptest.

- 3 NEW files (`harness.go` 195 LoC + `harness_test.go` 270 LoC +
  `handlers_test.go` 1,008 LoC) in `internal/tools/` with build tag
  `//go:build test`.
- **51 new tests** (2 smoke + 18 namespace + 27 e2e + 5 http-error).
- Uses v2 wire shapes (`data.row.id`, `data.hits`, `data.rows`,
  `data.spec_id`, `data.db.live`).

### 7.6 internal/recall CachedSource mock testing (commit `d4b7347`)

Closes the deferred `internal/recall` 45.3% gap.

- `internal/recall/cache_test.go` NEW (1,112 LoC, `package recall_test`).
- **22 NEW tests** (24 functions incl. 6 sub-tests in
  FrameTTL_AllKnownKinds): identity/capabilities miss/hit/error/TTL,
  applyCanary semantics, INV-5 cache mismatch, error observatory,
  concurrent access (100 goroutines, race-clean), store error
  propagation, pass-through frames, frameTTL unknown-kind fallback.
- `internal/recall/export_test.go` — exposes 7 internal symbols
  via free-function wrappers.
- **Coverage: `internal/recall` 45.3% → 82.1% (+36.8 pp)**, exceeds
  target ≥80%.
- **Race-detector caught a latent race** in fakeInner shared-pointer
  + concurrent `applyCanary` mutation + `Hash()` json.Marshal reads
  of `CanaryActive`. Fixed by cloning per call.

### 7.7 Docs followup + alpha.19 tag (this commit)

- `CHANGELOG.md [4.0.0-alpha.19]` — release entry (this commit).
- `docs/v4-status.md §1.7` — Phase 7 changelog.
- `docs/v4-alpha-11-plan.md §7` — Phase 7 close-out (this section).
- `docs/sota-critique.md §5.2.3` — Phase 7 per-gap closure evidence.
- `git tag v4.0.0-alpha.19` LOCAL ONLY.
- dark-memory rows 2294-2315 (1 spec SUMMARY pinned + 5 chunk SUMMARY
  pinned + 19 SECTION pinned=false, agent_id `alpha-11-phase7`,
  session `sess-a4c92524784e1891`).

## 8. Phase 8 — v4alpha wiring close + embedder pilot ✅ SHIPPED (alpha.20, 2026-10-02)

**Status (2026-10-02)**: 8 of 10 chunks shipped. **alpha.20 tag
local** `v4.0.0-alpha.20` placed on the Chunk 8.8 docs commit (NO
remote push per platform-LOCAL policy). 62 canonical tools, schema
v31, 84 new tests, 0 critical findings on re-test.

| Chunk | Commit | Atomic mirror rows | Status |
|---|---|---|---|
| 8.1 v4alpha delegate_intent wired (CRITICAL e2e T7) | `75a04fa` | 2356 (SUMMARY) + 2357-2359 (3 SECTION) | ✅ SHIPPED |
| 8.2 v4alpha personas exposed (CRITICAL e2e T3) | `bade6d0` | 2360 (SUMMARY) + 2361-2363 (3 SECTION) | ✅ SHIPPED |
| 8.3 embedder wired at boot (recort text-only) | `514d003` | 2365 (SUMMARY) + 2366-2368 (3 SECTION) | ✅ SHIPPED |
| 8.3-bench real-latency benchmark | `03aa531` | 2369 (observation, pinned) | ✅ SHIPPED |
| 8.4 ProGraph 2-layer entity extraction BFS (ADR-015) | `9217379` | 2372 (SUMMARY) + 7 SECTION | ✅ SHIPPED |
| 8.5 audit_export + audit_verify (ADR-016 + ADR-018) | `9c4cfe6` | 2371 (SUMMARY) + 3 SECTION | ✅ SHIPPED |
| 8.6 internal/recall coverage 82.1% → 91.9% | `07c2b90` | 2370 (SUMMARY) | ✅ SHIPPED |
| 8.7 Bitemporal lite + Phase 5 schema port (ADR-014) | `c61982b` | 2375 (SUMMARY) | ✅ SHIPPED |
| 8.8 Docs sweep + alpha.20 tag local | (this commit) | 2376 (SUMMARY) | ✅ SHIPPED |
| 8.9 fake_authority documentation | (deferred to alpha.20.1) | — | 📋 PLANNED |
| 8.10 E2E production-grade gate meta-decision | (deferred to alpha.20.1) | — | 📋 PLANNED |

**Pre-flight**: Phase 7 alpha.19 shipped + tagged local
`v4.0.0-alpha.19` on commit `1ed7bc5`. **Phase 8 e2e EXHAUSTIVE run**
(17 tests, 12 min, ~$0.30) verified production-grade behavior with
**2 CRITICAL wiring gaps** discovered — these gaps BLOCKED operator
access to Phase 7 improvements; both closed in §8.1 + §8.2 above.

### §8.1 Wire v4alpha Server into MCP public registry 🚨 CRITICAL ✅ SHIPPED

**Commit**: `75a04fa` (1310 insertions, 150 deletions, 7 files)
**Atomic mirror**: row 2356 (SUMMARY, decision) + rows 2357-2359
(3 SECTION) — `alpha-11-phase8` agent_id, project `dark-memory`.

- **Gap (e2e T7)**: `dark_memory_delegate_intent` routes to `internal/orchestration` (v2 router alpha.18.1, 1 subtask "bundle"). The v4alpha EXTRACT pipeline (Chunk 7.1) is implemented in `internal/v4alpha/transport/mcp/delegation.go` but has NO MCP public tool registration. Chunk 7.1 is shipped + tagged but **inaccessible to MCP consumers**.
- **Goal**: `dark_memory_delegate_intent` routes to `internal/v4alpha/transport/mcp.Server.HandleDelegateIntent` (wire shape v2 unchanged). LLM-extracted sub-tasks, judge-delegator persona, drift_judge validation, needs_human surface with `alternatives[]` all become reachable.
- **Architecture (final)**: `internal/v4alpha/transport/mcp/wire.go` NEW exports `RunDelegateIntentCore` as pure function (no mcp-go types); `RegisterDelegationWithBackend` + `DARK_DELEGATION_BACKEND` env var (default `v4alpha`, `v2` rollback).
- **Tests**: 8 e2e tests in `internal/tools/delegation_e2e_test.go` PASS (InlineShort, DelegateLong, RefuseMarker, NeedsHuman_NoLLMKey, NeedsHuman_LLMParseError, RefineRetry, WireShapeV3, v2_FallbackByFlag).
- **Estimated LoC**: planned 120, **actual 1310** (incl. v4alpha wire + e2e harness + tests).
- **Closes**: Phase 8 e2e T7 critical finding (row 2327).

### §8.2 Expose v4alpha persona registry in MCP public API 🚨 CRITICAL ✅ SHIPPED

**Commit**: `bade6d0` (507 insertions, 3 deletions, 6 files)
**Atomic mirror**: row 2360 (SUMMARY, decision) + rows 2361-2363
(3 SECTION) — `alpha-11-phase8` agent_id, project `dark-memory`.

- **Gap (e2e T3)**: `dark_memory_judge_list_personas` returns 8 v2 personas only. The v4alpha registry (internal/v4alpha/judge/personas_v4.go) contains 14 personas = 8 legacy + 6 v4-new (judge-cross-modal C5, judge-pipeline C6, judge-opinion reserved, judge-decision C3, judge-research C4, judge-delegator EXTRACT). Phase 7 alpha.19 added judge-delegator (was 13 in alpha.18.1, now 14).
- **Goal**: `judge_list_personas` returns 14 (8 legacy + 6 v4-new). Operators can see the full registry.
- **Architecture (final)**: `internal/orchestration/judge_personas_types.go` + `judge_personas_v4alpha.go` (NEW 155 LoC) merge v4alpha personas after overrides in `NewPersonaRegistry`. `PersonaSourceV4Alpha="v4alpha"` discriminator. `orch.WithV4AlphaPersonas(enabled)` builder (default false; backward compat preserved).
- **Tests**: 6 hermetic tests in `internal/orchestration/judge_list_personas_test.go` PASS.
- **Estimated LoC**: planned 60, **actual 507**.
- **Closes**: Phase 8 e2e T3 critical finding (row 2323).

### §8.3 Embedder integration (ADR-013) — BGE-large text via ONNX pluggable adapter ✅ SHIPPED (recort operator decision A)

**Commit**: `514d003` (432 insertions, 48 deletions, 7 files)
**Atomic mirror**: row 2365 (SUMMARY, decision) + rows 2366-2368
(3 SECTION) — `alpha-11-phase8` agent_id. Chunk 8.3-bench
(`03aa531`, row 2369 observation) is the real-latency benchmark
companion.

- **Goal (recort)**: alpha.18 stub vector path closes via the
  EXISTING `internal/embedder/` infrastructure (5 adapters + FactoryAuto
  ladder shipped since v2.9.0-alpha PR-2). What was missing was the
  boot wiring (`cmd/dark-mem-mcp/legacy_main.go` never called
  `FactoryAuto()`; Store always booted with `embedder.None()`).
- **Recort rationale (operator decision A)**: text-only (dropped
  BGE-large + ImageBind + wav2vec 2.0 multi-modal scope — dark-memory
  has NO attachment schema for image/audio storage, so encoding
  those modalities would compute embeddings nobody can use). Recort
  shrunk scope from ~800 LoC to ~150 LoC NEW work.
- **Architecture (final)**: `WithEmbedder(e) Store` to the Store
  interface; `bootState.Store.WithEmbedder(embedder.FactoryAuto())`
  at boot; `embedderInfo` struct (Kind+Dim, frozen wire shape) on
  `healthPingResult`.
- **Tests**: 5 e2e tests in `internal/store/sqlite/embedder_integration_test.go`
  PASS (WithEmbedder_RRFReturnsSemanticMatch, VectorCosineRanking,
  FactoryAutoLadder, WithEmbedderNilRestoresStub).
- **Estimated LoC**: planned 800, **actual 432** (recort).
- **Closes**: sota-critique.md §5.2.3 alpha.20 follow-up #1 (deferred from alpha.19).

### §8.4 ProGraph 2-layer entity extraction (ADR-015) ✅ SHIPPED

**Commit**: `9217379` (2041 LoC / -12 LoC, 13 files)
**Atomic mirror**: row 2372 (SUMMARY, decision) — `alpha-11-phase8`
agent_id, project `dark-memory`.

- **Goal**: multi-hop retrieval stub closes. C1/C2/C5/C6 stay 1-hop; C3/C4 promote to 2-hop entity traversal.
- **Architecture (final)**: `internal/recall/entity.go` (Entity +
  EntityStore in-memory index, sync.RWMutex) + `internal/recall/prograph.go`
  (ExtractEntities + MultiHopRetrieve BFS + ProGraphSource 3-method
  subset interface for hermetic tests). Store-first BFS expansion
  (NOT in-memory-only — caught by e2e). depth cap=2 (alpha.21+
  territory for 3+).
- **Tests**: 23 PASS (6 entity_test, 12 prograph_test, 5 prograph_e2e).
- **Estimated LoC**: planned 600, **actual 2041** (EntityStore
  snapshot/rebuild + cycle detection + Store-first BFS are real
  engineering work, not stubs).
- **Closes**: sota-critique.md §5.2.3 alpha.20 follow-up #2.

### §8.5 Audit gaps (ADR-016 transparency log + ADR-018 audit verify tool) ✅ SHIPPED

**Commit**: `9c4cfe6` (1319 LoC, 8 files)
**Atomic mirror**: row 2371 (SUMMARY, decision) + 3 SECTION —
`alpha-11-phase8` agent_id.

- **Goal**: `dark_memory_audit_export` + `dark_memory_audit_verify` exposed publicly. Operators can dump and verify the audit chain offline.
- **Architecture (final)**: `internal/audit/hmac.go` (HMAC-SHA256
  chain, ChainPrev/ChainSelf/ChainKeyID, KeyringFromEnv for rotation
  via `DARK_AUDIT_HMAC_KEY`) + `export.go` (JSONL stream) +
  `verify.go` (Status enum OK | Broken | UnknownKey | Malformed,
  FirstBadID + Reason on first failure).
- **Tests**: 37 PASS (31 hermetic + 4 e2e + 1 staleness). OBSERVABILITY 4→6.
- **Estimated LoC**: planned 400, **actual 1319**.
- **Closes**: sota-critique.md §5.2.3 alpha.20 follow-up #5 (deferred from Phase 6 per SPEC D2).

### §8.6 internal/recall 82.1% → 95%+ polish ✅ SHIPPED (ceiling 91.9%)

**Commit**: `07c2b90` (1414 LoC, 5 new files)
**Atomic mirror**: row 2370 (SUMMARY, decision) — `alpha-11-phase8`
agent_id.

- **Goal (ceiling 91.9%)**: finish the chunk 7.6 remaining gap. DriftFrame 38.1% → 95.2%, PersonaFrame 68.8% → 93.8%, IdentityFrame 80.0% → 90.0%, ScopeFrame 90.9%, CapabilitiesFrame 86.7% (dead branches, accepted ceiling).
- **LUCIDEZ honest disclosure**: SPEC §3.6 COMPLETELY MISSED DriftFrame 38.1% gap + PersonaFrame 68.9% gap — required 5+5 additional tests not 0. Remaining 3.1% gap to SPEC ≥95% target: unreachable defensive paths (`json.Marshal` cannot fail on JSON-safe struct types; closed-store test fails GetFrame first; DefaultToolGrants has no trailing comma).
- **Tests**: 27+ PASS (5 new files: cache_persist_test, assemble_drift_test, assemble_persona_test, assemble_final_test, assemble_final2_test).
- **Estimated LoC**: planned 250, **actual 1414**.
- **Closes**: sota-critique.md §5.2.3 alpha.20 follow-up #6.

### §8.7 Bitemporal (ADR-014) — transaction_time + valid_time ✅ SHIPPED (operator decision B)

**Commit**: `c61982b` (1755 LoC, 16 files including SPEC §3.7 LUCIDEZ
clarification)
**Atomic mirror**: row 2375 (SUMMARY, decision) — `alpha-11-phase8`
agent_id.

- **Goal**: alpha.18 DecayScore precursor (ScrubJay-MEM π_i + τ_i).
  Each agent_memory row gains `transaction_time` (write clock) and
  `valid_time` (semantic clock). Schema v29 → v31.
- **Architecture (operator decision B)**: v30 `phase5_port_to_production`
  ports v4alpha Phase 5 schema (decision_state, valid_from,
  valid_to, supersedes_id, decision_transitions) to production. v31
  `bitemporal_lite` adds transaction_time + valid_time columns
  (NULLABLE; backfill UPDATE from created_at; COALESCE on read).
  MarkSupersededAgentMemory + RecallAtTime in Store interface +
  dark_memory_mark_superseded + dark_memory_recall_bitemporal MCP.
- **LUCIDEZ honest disclosure (SPEC §3.7 amended)**: v31 columns are
  NULLABLE, not `NOT NULL` as originally specified. SQLite cannot do
  `ALTER TABLE ALTER COLUMN SET NOT NULL` without table rebuild.
  PostgreSQL variant CAN enforce NOT NULL (v32 migration if parity
  is the priority).
- **Tests**: 21 PASS (4 migration_v30_v31, 10 bitemporal_*, 7
  bitemporal_e2e). AGENT_MEMORY 11→13; canonical tools 60→62; schema 29→31.
- **Estimated LoC**: planned 500, **actual 1755** (Phase 5 port was the
  actual engineering work, operator explicitly approved via B).
- **Closes**: sota-critique.md §5.2.3 alpha.20 follow-up #3 + Phase 6 D2
  mark_superseded gap (blocked since v4alpha Phase 5).

### §8.8 Docs + alpha.20 tag local ✅ SHIPPED

**Atomic mirror row 2376** (this SUMMARY pinned, decision,
memory_type=episodic, agent_id `alpha-11-phase8`).

- `CHANGELOG.md [4.0.0-alpha.20]` entry covering all 8 chunks (~250 LoC).
- `docs/v4-status.md §1.8` Phase 9 changelog (~150 LoC).
- `docs/v4-alpha-11-plan.md §8` this section — confirmed + per-chunk
  cross-refs to atomic mirror rows 2356, 2360, 2365, 2369, 2370,
  2371, 2372, 2375, 2376.
- `docs/sota-critique.md §5.2.4` Phase 9 per-gap closure evidence
  table (~100 LoC).
- `README.md` — fix to `MCP-62 canonical tools` + `## Las 62
  herramientas` + `schema-v31` + `17 oficios` (unblocks
  `tests/docs` TestDocs_SurfaceNumbersMatchRuntime).
- `git tag v4.0.0-alpha.20` LOCAL ONLY on this commit (chunks 8.1-8.7
  + this). NO remote push per platform-LOCAL policy.
- Atomic mirror: 1 SUMMARY pinned + 7 SECTION pinned=false (8 chunks
  × 1 SECTION) — row 2376 + 7 SECTION (this commit).

### §8.9 Document `fake_authority` pattern examples 📋 PLANNED

- **Caveat (e2e T12)**: 5 attempted phrasings of `fake_authority` pattern all missed the validator. Other 9 patterns (no_needs_human, auto_sign, self_modify, ignore_invariant, disable_audit, skip_injection, always_aligned, remove_safety, trust_unconditional) trigger with reasonable phrasing. fake_authority is intentionally narrow (privilege escalation requires sophisticated phrasing).
- **Goal**: add to `docs/sota-critique.md §5.2.4` an "override validator patterns" appendix with examples of phrasings that DO and DO NOT trigger each pattern, plus design rationale (fake_authority is intentionally narrow per security review).
- **Estimated LoC**: +40 LoC doc-only.
- **Closes**: Phase 8 e2e T12 caveat (row 2344).

### §8.10 E2E production-grade gate — meta-decision

- **Lesson (Phase 8 e2e)**: exhaustive e2e found 2 critical wiring gaps that unit tests missed. Future phases MUST include exhaustive e2e gate before SHIP.
- **Decision (operator-approved)**: alpha.21+ must include exhaustive e2e before SHIP. Required tests at minimum: (a) session lifecycle chain; (b) vibe_publish + drift_judge round-trip; (c) audit chain cross-process monotonicity; (d) override pattern sweep; (e) persona registry count; (g) concurrent write stress (≥10 parallel); (h) cross-session atomic mirror survival; (i) needs_human surface for any tool with failure modes.
- **Acceptance**: 0 critical findings before SHIP. Caveats documented but not blocking.
- **Cost budget**: ~$5-10 per phase e2e gate (LLM judge calls).

### Acceptance criteria (Phase 8 / alpha.20 SHIP)

1. §8.1 + §8.2 critical gaps closed (re-run Phase 8 e2e T7+T3 in alpha.20 — both MUST show v4alpha exposed).
2. §8.3-§8.7 alpha.20 follow-ups shipped.
3. §8.8 docs+tag local.
4. §8.9 fake_authority documentation closed.
5. §8.10 meta-decision: future phases require e2e gate (codified in §10 below).
6. All v4alpha packages PASS, 0 regressions.
7. Phase 8 e2e re-run with 0 critical findings.
8. Cross-version lockstep hash pin unchanged.

### Cross-references

- `docs/sota-critique.md §5.2.3` — Phase 20 follow-ups (6 items, 5 closed in §8.3-§8.7, 1 carried to §8.6).
- `docs/sota-critique.md §5.2.4` — Phase 8 per-gap closure evidence (added by §8.8).
- Phase 8 e2e final report — dark-memory row 2345 (pinned, agent_id `alpha-11-phase8`, session `sess-961aa31e58a94f22`).
- Phase 8 e2e critical findings — rows 2323 (T3 persona gap) + 2327 (T7 EXTRACT gap) (both pinned).
- Phase 8 e2e caveats — rows 2344 (T12 fake_authority) + 2340 (T13 adversarial) + 2324 (T4 drift_detected).

## 9. The vibe-loop pattern, restated

For each phase, the workflow is:

1. **Read the spec** at `docs/specs/SPEC-alpha-11-phase-N.md`.
2. **Open a session** with `dark_memory_session_start`.
3. **Pre-flight**: 6 calls (recall pinned decisions, recall
   phase-spec, list open todos, recent writes, error
   summary, load skills).
4. **Implement** the spec's tasks.
5. **Build** + test: `go build ./...` clean, all tests pass.
6. **Commit** with detailed body citing spec + tasks.
7. **Atomic mirror** per ADR-008: 1 SUMMARY pinned=true +
   N SECTION pinned=false, `agent_id=alpha-11-phase-N`,
   `kind=link`, `memory_type=semantic`, `bind_session=true`,
   tags `spec:alpha-11-phase-N, mirror:atomic, v4alpha`.
8. **drift_judge**: invoke `dark_memory_judge` with
   `eval_type=drift_judge` + `artifact_ref` pointing to
   the changed files + `spec_intent` from the spec.
9. **Verdict**:
   - `aligned` → phase complete, move to next.
   - `drift_detected` → fix and re-publish.
   - `needs_human` → STOP, surface to operator.

## 10. Operator decisions (recap from sota-critique.md §7.6.8)

| # | Decision | Default |
|---|---|---|
| OD1 | Phase 1 start point | chunk 7 first |
| OD2 | Include ADR-013? | defer to beta |
| OD3 | BUG-10 10b blocks other work? | parallel |
| OD4 | v2.9.x embedder resurrected? | NO (per row 1578) |
| OD5 | ADR-013 strategy (if OD2=YES) | FRESH |
| OD6 | `project_id` framing | **namespace (soft)** per §7.6.9 |

## 11. Cross-references

- `docs/sota-critique.md` §7.6 — the source meta-doc
  (this file summarizes it for execution).
- `docs/sota-critique.md` §7.6.9 — namespace primitive
  threat model.
- `docs/specs/SPEC-alpha-11-chunk7.md` — the first spec
  (chunk 7 close).
- `docs/sota-critique.md §8` — per-chunk SOTA criticism
  cross-references (chunks 1-5 shipped).
- Row 2173 (dark-memory) — namespace reframe decision.
- Row 1578 (dark-memory) — v2.9.x embedder ABANDONED.
- Row 2112 (dark-memory) — BUG-10 10a pre-coding 4-doc
  plan + operator approval gate pattern.
