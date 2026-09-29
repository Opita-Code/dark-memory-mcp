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
| 5a | Hash chain (prev_hash + row_hash columns) | x | +~400 | LOW | **SHIPPED alpha.15 (commit pending)** |
| 5b | `dark_memory_audit_verify` MCP tool | S | ~120 | LOW | **SHIPPED alpha.15 (commit pending)** |
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
| # | Item | Complexity | LoC | Risk | Spec file |
|---|---|---|---|---|---|
| 8 | ADR-009 (provider allow-list 4 → 10+) | S | ~150 | LOW | `SPEC-alpha-11-adr009.md` |
| 9 | ADR-011 (judge calibration bootstrap-CI) | M | ~250 | MEDIUM (statistical) | `SPEC-alpha-11-adr011.md` |

**Dependency graph**: ADR-009 and ADR-011 are independent.

**Acceptance criteria**:
- ADR-009: 10+ providers in the allow-list
  (anthropic, minimax, minimax-cn, deepseek, openai,
  google, qwen, kimi, zhipu, moonshot, mistral). Each
  with native structured outputs (or documented gap).
- ADR-011: every `sdd_evaluations` row has a
  `confidence_calibrated` field computed via bootstrap-CI
  (per Play Favorites arxiv:2508.06709). Calibration is
  self-bias-corrected.
- EC-007 (self-bias) now uses the statistical framework,
  not the binary signal.

## 4. Phase 4 — BUG-10 10b namespace primitive (2-3 weeks)

**Vibe-loop**: `alpha-11-phase-4`

**Items**:
| # | Item | Complexity | LoC | Risk | Spec file |
|---|---|---|---|---|---|
| 10 | BUG-10 10b (mindset + delegation + project + namespace) | L | ~500 | MEDIUM (schema) | `SPEC-alpha-11-bug10-10b.md` |

**Pre-coding 4-doc plan** (per 10a pattern, row 2112):
1. `docs/bug10-10b-spec.md` — what 4 tools + namespace.
2. `docs/bug10-10b-design.md` — schema, package layout,
   transport wiring.
3. `docs/bug10-10b-tests.md` — test plan (10-12 tests).
4. `docs/bug10-10b-risk.md` — risk register, mitigations.

**Operator approval gate** between docs and code.

**Acceptance criteria**:
- 4 new tools registered: `mindset_apply`, `delegate_intent`,
  `project_create`, `project_lookup` (38 → 42 tools).
- `projects` table exists (namespace registry, NOT tenant
  registry per §7.6.9).
- `project_id` column on 5 existing tables
  (agent_memory, audit_log, sdd_evaluations, sessions,
  writes — or whatever the 5 are in BUG-10 10b's spec).
- Default `project_id = "default"` for backfill.
- Threat model statement in `docs/v4-status.md`:
  "v4 assumes the harness session is the only concurrent
  consumer. Project IDs scope workstreams within one
  operator. For hard isolation between concurrent users,
  use separate `coexistence_group`s or separate MCP
  instances."

## 5. Phase 5 — Memory subsystem (3-4 weeks)

**Vibe-loop**: `alpha-11-phase-5`

**Items**:
| # | Item | Complexity | LoC | Risk | Spec file |
|---|---|---|---|---|---|
| 11 | ADR-013 (vector retrieval + RRF) | XL | ~700 | HIGH (biggest scope) | `SPEC-alpha-11-adr013.md` |
| 12 | ADR-014 (temporal re-ranking) | M | ~180 | MEDIUM | `SPEC-alpha-11-adr014.md` |
| 13 | ADR-015 (multi-hop / graph) | L | ~400 | MED-HIGH | `SPEC-alpha-11-adr015.md` |

**Only if OD2 = YES** (operator approves vector retrieval
in this cycle). If OD2 = NO, Phase 5 deferred to v4.0.0-beta.

**Dependency graph**:
- ADR-013 → no deps.
- ADR-014 → depends on ADR-013.
- ADR-015 → depends on ADR-013.

**D1 decision baked in**: ADR-013 is FRESH
implementation, no v2.9.x inheritance (per row 1578
abandonment). Cormack 2009 RRF (k=60) as reference, not
code to import.

**Acceptance criteria**:
- ADR-013: hybrid FTS5 + vector retrieval in `Recall()`.
  Pluggable embedding adapter (HTTP: OpenAI, Voyage,
  Cohere; local: ONNX optional). RRF re-ranker (k=60).
- ADR-014: `Recall()` re-ranks by recency × relevance.
- ADR-015: graph links between rows (parent/child,
  related, references). `Recall()` traverses 1-2 hops.

## 6. The vibe-loop pattern, restated

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
