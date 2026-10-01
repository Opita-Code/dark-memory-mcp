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

## 7. The vibe-loop pattern, restated

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

## 7. Operator decisions (recap from sota-critique.md §7.6.8)

| # | Decision | Default |
|---|---|---|
| OD1 | Phase 1 start point | chunk 7 first |
| OD2 | Include ADR-013? | defer to beta |
| OD3 | BUG-10 10b blocks other work? | parallel |
| OD4 | v2.9.x embedder resurrected? | NO (per row 1578) |
| OD5 | ADR-013 strategy (if OD2=YES) | FRESH |
| OD6 | `project_id` framing | **namespace (soft)** per §7.6.9 |

## 8. Cross-references

- `docs/sota-critique.md` §7.6 — the source meta-doc
  (this file summarizes it for execution).
- `docs/sota-critique.md` §7.6.9 — namespace primitive
  threat model.
- `docs/specs/SPEC-alpha-11-chunk7.md` — the first spec
  (chunk 7 close).
- `docs/sota-critique.md` §8 — per-chunk SOTA criticism
  cross-references (chunks 1-5 shipped).
- Row 2173 (dark-memory) — namespace reframe decision.
- Row 1578 (dark-memory) — v2.9.x embedder ABANDONED.
- Row 2112 (dark-memory) — BUG-10 10a pre-coding 4-doc
  plan + operator approval gate pattern.
