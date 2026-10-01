# SPEC-alpha-11-phase8: alpha.20 — v4alpha wiring close + embedder pilot

> **Estado**: Draft (operator-approved 2026-10-01, es-CO tuteado)
> **Fecha**: 2026-10-01
> **Autor**: Opita-AI (operator=nico, project_id=dark-memory)
> **Spec id**: SPEC-alpha-11-phase8
> **Mirror**: 1 SUMMARY + 7 SECTION (per ADR-008, 10 chunks → 1 summary + 1 wiring + 1 embedder + 1 prograph + 1 audit + 1 recall + 1 bitemporal + 1 docs + 1 fake_authority = 8 rows minimum)
> **Vibe-loop**: alpha.11-phase8 (alpha.20, closes Phase 8 e2e critical wiring gaps + alpha.20 follow-ups)
> **Pre-flight**: Phase 7 alpha.19 SHIPPED + tagged local `v4.0.0-alpha.19` on commit `1ed7bc5`. Phase 8 e2e EXHAUSTIVE (17 tests, 12 min, ~$0.30) verified production-grade behavior with **2 CRITICAL wiring gaps** discovered.

## 1. Contexto

Phase 7 (alpha.19) closed on 2026-10-01 with 6 commits
(`7a83be5`, `2bb20a3`, `f3692cf`, `39cc899`, `d4b7347`,
`1ed7bc5`) + 1 local tag `v4.0.0-alpha.19` +
27 atomic mirrors (rows 2294-2320, including spec + 5 chunk
SUMMARY pinned + 19 SECTION pinned=false). All 4 alpha.18.1
deferred items were closed (delegate_intent LLM-router
7.1, C2 subagent binding 7.2, internal/tools httptest
harness 7.5, internal/recall CachedSource tests 7.6).

**Phase 8 e2e EXHAUSTIVE** (17 tests, 12 min, ~$0.30) ran
on 2026-10-01 18:32:51 → 18:42:30 against the live MCP
server. **2 production-grade wiring gaps** were discovered
that BLOCK operator access to Phase 7 improvements:

1. **delegate_intent routes to v2 orchestrator, NOT
   v4alpha EXTRACT pipeline** (e2e T7, row 2327). Chunk
   7.1's LLM-extracted sub-tasks router + judge-delegator
   persona + drift_judge validation + needs_human surface
   are all implemented in `internal/v4alpha/transport/mcp/`
   but **NOT registered as MCP tool**. MCP tool
   `dark_memory_delegate_intent` still routes to
   `internal/orchestration/delegate_intent.go:86`
   (alpha.18.1 deterministic router, 1 subtask "bundle").

2. **judge_list_personas returns 8 v2 personas only** (e2e
   T3, row 2323). The v4alpha registry
   (`internal/v4alpha/judge/personas_v4.go:13-17`) contains
   **14 personas** (8 legacy + 6 v4-new including
   judge-delegator added in alpha.19). Operators cannot
   enumerate v4alpha personas via MCP public API.

**Additional findings** (caveats, not blocking):

- **fake_authority pattern is intentionally narrow** (e2e
  T12, row 2344). 5 attempted phrasings all missed; other
  9 patterns trigger with reasonable phrasing.
  Documentation gap: operators don't know what phrasing
  triggers fake_authority.
- **LLM judge variability is real production behavior**
  (e2e T11, row 2329). consensus N=7 returned 5/7
  needs_human + 2/7 drift_detected outliers (stddev=0.26).
  System handles correctly but operator should expect
  inter-sample disagreement.
- **drift_judge IS NOT fooled by adversarial spec_intent**
  (e2e T13, row 2340). Inverted "fabrication" framing
  produced drift_detected@0.97. INV-5 + override
  validator work as designed.
- **INV-1 atomicity holds under concurrent + cross-process**
  (e2e T14, row 2341 + T17, row 2343). 10x parallel
  agent_memory_save IDs 2330-2339 monotonic. sqlite3 CLI
  cross-process write audit_id 26949 monotonic with
  subsequent MCP writes 26950-26957. BUG-12 fix from
  alpha.14 verified working.
- **Session resurrection chain works end-to-end** (e2e
  T16, row 2342). recover → resurrect → resume → close,
  all 4 steps succeeded against a closed_aborted Phase 7
  planning session.

**Decisión del operador** (2026-10-01 afternoon, es-CO tuteado):

- **D1**: §8.1 + §8.2 CRITICAL — close both e2e wiring gaps
  in alpha.20 chunks 1+2 (they MUST close before alpha.20
  ships).
- **D2**: §8.3-§8.7 — alpha.20 follow-ups per
  `docs/sota-critique.md §5.2.3` (5 items: embedders,
  ProGraph, audit gaps, recall 95%, bitemporal).
- **D3**: §8.8 docs + alpha.20 tag local (CHANGELOG, v4-status §1.8,
  v4-alpha-11-plan §8, sota-critique §5.2.4).
- **D4**: §8.9 — Document fake_authority pattern examples
  in sota-critique.md §5.2.4 (operator awareness).
- **D5**: §8.10 — Meta-decision codified: alpha.21+ MUST
  include exhaustive e2e gate before SHIP. 0 critical
  findings required.
- **D6**: Spec-first mandatory (this document).

## 2. Decisión

Phase 9 (alpha.20) ships en 10 chunks:

1. **Chunk 8.1** — Wire v4alpha Server into MCP public
   registry (CRITICAL — closes e2e T7). `delegate_intent`
   routes to `internal/v4alpha/transport/mcp.Server` not
   `internal/orchestration`.
2. **Chunk 8.2** — Expose v4alpha persona registry in MCP
   public API (CRITICAL — closes e2e T3). `judge_list_personas`
   returns 14 (8 legacy + 6 v4-new).
3. **Chunk 8.3** — Embedder integration (ADR-013). BGE-large
   text, ImageBind image, wav2vec 2.0 audio, ONNX pluggable
   adapter. Replaces alpha.18 stub vector path.
4. **Chunk 8.4** — ProGraph 2-layer entity extraction
   (ADR-015). Multi-hop retrieval for C3/C4. Closes
   alpha.18 stub.
5. **Chunk 8.5** — Audit gaps (ADR-016 transparency log +
   ADR-018 audit verify tool). `dark_memory_audit_export` +
   `dark_memory_audit_verify` exposed publicly. Deferred
   from Phase 6 per SPEC D2.
6. **Chunk 8.6** — internal/recall 82.1% → 95%+ polish
   (Render/Hash error branches hard to trigger).
7. **Chunk 8.7** — Bitemporal (ADR-014) — transaction_time
   + valid_time. Schema migration v30→v31. alpha.18
   DecayScore precursor (ScrubJay-MEM π_i + τ_i).
8. **Chunk 8.8** — Docs followup + alpha.20 tag local
   (CHANGELOG `[4.0.0-alpha.20]` + v4-status §1.8 +
   v4-alpha-11-plan §8 + sota-critique §5.2.4 + tag
   `v4.0.0-alpha.20` LOCAL ONLY).
9. **Chunk 8.9** — Document `fake_authority` pattern
   examples in sota-critique.md §5.2.4 (operator awareness
   for the intentionally narrow pattern).
10. **Chunk 8.10** — Meta-decision enforcement: alpha.21+
    MUST include exhaustive e2e gate before SHIP. Codified
    in `docs/v4-alpha-11-plan.md §10` (Operator decisions).
    Required tests at minimum: (a) session lifecycle chain;
    (b) vibe_publish + drift_judge round-trip; (c) audit
    chain cross-process monotonicity; (d) override pattern
    sweep; (e) persona registry count; (g) concurrent
    write stress ≥10 parallel; (h) cross-session atomic
    mirror survival; (i) needs_human surface for any tool
    with failure modes.

**No incluido en alpha.20** (deferido a alpha.21+):

- ADR-008 governance evolution (atomic mirror schema
  improvements beyond WORK_STANDARD).
- Cross-process restart-testing infrastructure (need
  orchestrator to fork/restart MCP daemon — defer until
  v4.0.0-beta).
- Federated peer dark_memory (peer db files exist but
  cross-peer audit chain not built yet).
- Persona expansion past 14 (judge-opinion currently
  reserved/empty; could activate post-alpha.20).

## 3. SPEC por chunk

### 3.1 Chunk 8.1 — Wire v4alpha Server into MCP public registry 🚨 CRITICAL

**Goal**: `dark_memory_delegate_intent` routes to
`internal/v4alpha/transport/mcp.Server.HandleDelegateIntent`
instead of `internal/orchestration.Orchestrator.DelegateIntent`.
Wire shape v2 unchanged (LLM-extracted sub-tasks,
judge-delegator persona, drift_judge validation, needs_human
surface with alternatives[] all become reachable).

**Files** (estimado ~120 LoC + 8 tests):

- `internal/tools/delegation.go` MODIFIED — replace
  `BindOrchestrator("delegate_intent", ...)` with
  `BindV4AlphaDelegateIntent(...)` (or feature flag
  `DARK_DELEGATION_BACKEND=v2|v4alpha` default v4alpha).
- `internal/v4alpha/transport/mcp/server.go` MODIFIED —
  export `RegisterDelegateIntentTool(srv *mcp.Server)` so
  external callers can wire the v4alpha handler into the
  MCP public registry.
- `internal/tools/registry.go` MODIFIED — add
  v4alpha server init + RegisterDelegateIntentTool call
  during tools.RegisterAll.
- `internal/v4alpha/transport/mcp/delegation_e2e_test.go`
  NEW — 8 tests via mcp-go + httptest covering LLM-extract
  path (mock LLM), drift_judge validation, refine+retry,
  needs_human surface with alternatives[], wire shape v2
  round-trip, subagent binding propagation.

**Strategy**:

1. Replace `BindOrchestrator("delegate_intent", ...)`
   in `internal/tools/delegation.go:28` with a v4alpha
   handler wrapper that calls
   `internal/v4alpha/transport/mcp.Server.HandleDelegateIntent`.
2. Wire `internal/v4alpha/transport/mcp.Server` (which
   already has all the v4alpha logic) into the MCP public
   registry at `internal/tools/registry.go`.
3. Add feature flag `DARK_DELEGATION_BACKEND` (default
   v4alpha) for emergency rollback to v2.
4. 8 e2e tests with mock LLM + real SQLite + mcp-go
   StreamableHTTPServer.
5. Re-run Phase 8 e2e T7 → expect v4alpha surface
   (multi-subtask, EXTRACT pipeline visible).

**Acceptance criteria**:

1. 8 e2e tests PASS.
2. Re-running Phase 8 e2e T7 returns multi-subtask plan
   (not 1 subtask "bundle").
3. `internal/tools/registry.go` wires v4alpha Server.
4. Feature flag `DARK_DELEGATION_BACKEND=v2` rollback
   works (emergency escape hatch).
5. go vet clean, no regressions.

### 3.2 Chunk 8.2 — Expose v4alpha persona registry in MCP public API 🚨 CRITICAL

**Goal**: `judge_list_personas` returns **14 personas**
(8 legacy + 6 v4-new) with `source` discriminator
(`compiled` for v2, `v4alpha` for v4-new).

**Files** (estimado ~60 LoC + 5 tests):

- `internal/tools/registry.go` MODIFIED — `personas`
  handler consults both v2 `internal/orchestration/judge_personas_default.go`
  AND v4alpha `internal/v4alpha/judge/personas_v4.go`;
  merge with `source` discriminator.
- `internal/judge_list_personas_test.go` NEW — 5 tests:
  (1) count=14; (2) v4-new has source=v4alpha; (3) legacy
  has source=compiled; (4) all 6 v4-new ids present
  (judge-cross-modal, judge-pipeline, judge-opinion,
  judge-decision, judge-research, judge-delegator);
  (5) eval_types field populated for v4-new.

**Strategy**:

1. Read v2 personas from
   `internal/orchestration/judge_personas_default.go`
   (8 personas).
2. Read v4-new personas from
   `internal/v4alpha/judge/personas_v4.go` (6 personas).
3. Merge into single list with `source` discriminator.
4. Operator can now see full registry via
   `dark_memory_judge_list_personas`.

**Acceptance criteria**:

1. 5 tests PASS.
2. Re-running Phase 8 e2e T3 returns 14 personas.
3. `source: v4alpha` discriminator visible for 6 v4-new.
4. No regression in v2 persona enumeration.

### 3.3 Chunk 8.3 — Embedder text integration (ADR-013 — RECORT)

> **Operator decision 2026-10-01 (Option A)**: Recort to text-only.
> ImageBind + wav2vec deferred until agent_memory schema gains
> attachment columns (BLOB + storage). Honest reason: encoding
> images/audio without a place to store them = wasted compute.
> BGE-large 1024-dim and BGE-small 384-dim become **operator-vendored
> opt-ins** (vendor model + tokenizer via env vars), not bundled —
> the bundled ONNX adapter keeps shipping
> **Xenova/all-MiniLM-L6-v2 INT8** (384-dim, sha-pinned, ~25MB
> on disk after extraction).

**Goal**: close the alpha.18 stub vector path. Vectors move from
FTS5-absorbed to real embedding space via the existing
`internal/embedder/` factory + `Store.WithEmbedder()` seam +
`SearchAgentMemory` Mode dispatch (bm25/vector/rrf). All the
plumbing ships in v2.9.0-alpha PR-2; Chunk 8.3 wires it at boot +
tests the hybrid path end-to-end.

**Pre-existing assets (NOT new in Chunk 8.3)**:

- `internal/embedder/embedder.go:79` — `Embedder` interface
  (`Kind`/`Dim`/`Embed`/`Close`).
- `internal/embedder/embedder.go:258` — `FactoryAuto()` walks
  manual → harness-detected → bundled ONNX → OPENAI_API_KEY → stub.
- `internal/embedder/embedder.go:127` — `DefaultKind()` reads
  `$DARK_MEMORY_EMBEDDER` (none|onnx|openai|voyage|ollama).
- `internal/embedder/onnx/` — bundled
  `Xenova/all-MiniLM-L6-v2 INT8` (384-dim, sha-pinned, libonnxruntime
  binaries per platform).
- `internal/embedder/ollama/` — HTTP to local Ollama
  (`nomic-embed-text` 768d default, override via
  `DARK_MEMORY_OLLAMA_MODEL`).
- `internal/embedder/openai/`, `internal/embedder/voyage/` —
  cloud backends (env-key gated).
- `internal/store/sqlite/store.go:396` — `Store.WithEmbedder(e)`
  builder.
- `internal/store/sqlite/store.go:4904` — `SearchAgentMemory`
  Mode dispatch (bm25|vector|rrf) — fully implemented in v2.9.0
  PR-2.
- `internal/store/sqlite/vector.go:240` — `searchByVector`
  (brute-force cosine, 1024/384/768/1536-dim aware).
- `internal/store/sqlite/vector.go:306` — `searchByRRF`
  (BM25 + vector arms fused via RRF k=60).
- `internal/migrate/sqlite/ddl.go:983` — `embedding BLOB` column
  already on `agent_memory` (migration v23).

**Files** (Chunk 8.3 NEW work — estim ~150 LoC + 6 tests):

- `cmd/dark-mem-mcp/legacy_main.go` MODIFIED — call
  `embedder.FactoryAuto()` + `bootState.Store.WithEmbedder(...)`
  ONCE at boot, BEFORE the first `SearchAgentMemory` call.
  Currently the Store always boots with `embedder.None()` (BM25-only).
- `internal/embedder/bge.go` NEW — thin wrapper that re-exports
  the existing ONNX adapter with BGE-large INT8 (1024-dim) or
  BGE-small (384-dim) configurations. Operator-vendored: must
  supply `$DARK_MEMORY_BGE_MODEL_PATH` pointing at their
  pre-downloaded model.onnx + tokenizer.json. **NOT bundled** —
  the operator downloads from HuggingFace
  (`Xenova/bge-large-en-v1.5` or `Xenova/bge-small-en-v1.5`) and
  pins the SHA via `DARK_MEMORY_BGE_MODEL_SHA256`.
- `internal/embedder/bge_test.go` NEW — 3 unit tests: load
  synthetic 384-dim ONNX stub, verify Encode returns correct
  shape, verify SHA mismatch returns typed error.
- `internal/store/sqlite/embedder_integration_test.go` NEW —
  3 e2e tests using the mock embedder: (1) `save` populates
  `embedding` BLOB, (2) `Mode=rrf` returns hybrid result that
  lexical alone would miss, (3) `Mode=vector` returns cosine
  ranking when no lexical overlap.
- `docs/embedder-ops.md` NEW — operator-facing runbook
  (~80 LoC): when FactoryAuto picks each one, how to vendor
  BGE-large, how to point at a local Ollama, what the perf
  trade-offs are per backend.

**Acceptance criteria**:

1. `go build ./...` clean (parent + cmd/dark-mem-mcp).
2. 6 new tests PASS (3 unit + 3 e2e integration).
3. `Store.Embedder().Kind() != "none"` after `legacy_main.go`
   boot when ANY backend is reachable (Ollama running OR
   `DARK_MEMORY_EMBEDDER` set).
4. `health_ping` reports `embedder_kind` field with the active
   backend (currently absent — 1 LoC add).
5. Hybrid search smoke: `save` 3 rows with semantically similar
   but textually distinct content, recall via `Mode=rrf` returns
   the most-similar row even with 0 token overlap.
6. `go vet ./...` clean.
7. Cross-version hash pin `4e6196a07c...` unchanged.

**Out of scope (deferred)**:

- ImageBind / wav2vec adapters (until attachment schema lands).
- sqlite-vec vector index (until dataset size > 50k rows).
- Embedder auth via OCAIS RS256 + tenant binding (separate ADR).

### 3.4 Chunk 8.4 — ProGraph 2-layer entity extraction (ADR-015)

**Goal**: multi-hop retrieval stub closes. C1/C2/C5/C6
stay 1-hop (single-frame retrieval). C3/C4 promote to
2-hop entity traversal.

**Files** (estimado ~600 LoC + 15 tests):

- `internal/recall/prograph.go` NEW — ProGraph engine
  with `ExtractEntities(content) []Entity` +
  `MultiHopRetrieve(query, depth int) []Frame`.
- `internal/recall/entity.go` NEW — Entity struct
  (id, type, value, confidence) + EntityStore.
- `internal/recall/prograph_test.go` NEW — 15 tests:
  1-hop vs 2-hop, entity deduplication, relationship
  cycle detection, traversal depth limits.

**Strategy**:

1. `ExtractEntities` parses content via regex + LLM
   hybrid (LLM for ambiguous cases).
2. `EntityStore` indexes entities by type + value.
3. `MultiHopRetrieve` traverses entity graph Layer 1
   (entities) then Layer 2 (relationships).
4. Cycle detection prevents infinite traversal.
5. Depth limit (default 2) prevents exponential blowup.
6. 15 tests covering 1-hop (C1/C2/C5/C6), 2-hop
   (C3/C4), cycle detection, depth limits.

**Acceptance criteria**:

1. 15 tests PASS.
2. C3/C4 multi-hop retrieval works (2 layers).
3. C1/C2/C5/C6 stay 1-hop (no over-retrieval).
4. Cycle detection prevents infinite loops.
5. Entity deduplication works across overlapping sources.

### 3.5 Chunk 8.5 — Audit gaps (ADR-016 transparency log + ADR-018 audit verify tool)

**Goal**: `dark_memory_audit_export` +
`dark_memory_audit_verify` exposed publicly. Operators
can dump and verify the audit chain offline.

**Files** (estimado ~400 LoC + 12 tests):

- `internal/audit/export.go` NEW — JSONL stream emitter
  of all write_audit rows with HMAC chain verification
  (ADR-016).
- `internal/audit/verify.go` NEW — JSONL stream reader
  + HMAC recomputation + monotonicity verification
  (ADR-018).
- `internal/audit/hmac.go` NEW — HMAC computation +
  verification helpers.
- `internal/tools/audit.go` NEW — MCP handlers
  `dark_memory_audit_export` + `dark_memory_audit_verify`.
- `internal/audit/{export,verify,hmac}_test.go` NEW —
  12 tests: chain integrity (forward + backward),
  cross-DB verification, broken-chain detection,
  HMAC key rotation.

**Strategy**:

1. `audit_export` streams write_audit rows as JSONL
   with HMAC chained signatures (each row's HMAC
   includes previous row's HMAC for chain integrity).
2. `audit_verify` reads JSONL stream + recomputes
   HMAC + verifies monotonicity + cross-DB consistency.
3. 12 tests: chain forward + backward verification,
   cross-DB, broken-chain detection (negative tests).

**Acceptance criteria**:

1. 12 tests PASS.
2. `dark_memory_audit_export` streams valid JSONL with
   HMAC chain.
3. `dark_memory_audit_verify` detects broken chain
   (negative test passes).
4. Cross-DB verification works (sqlite3 CLI exported
   JSONL verified against MCP-emitted HMAC).
5. HMAC key rotation supported.

### 3.6 Chunk 8.6 — internal/recall 82.1% → 95%+ polish

**Goal**: finish the chunk 7.6 remaining gap
(Render/Hash error branches hard to trigger from
production paths).

**Files** (estimado ~250 LoC + 8 tests):

- `internal/recall/cache_test.go` MODIFIED — 8 new
  tests targeting remaining uncovered branches:
  - `TestCachedSource_IdentityFrame_RenderFailure`
  - `TestCachedSource_IdentityFrame_HashCollision`
  - `TestCachedSource_PersistRaw_StoreError_Propagates`
  - `TestCachedSource_FrameTTL_NegativeClampedToDefault`
  - `TestCachedSource_FrameTTL_Zero_ReturnsZero`
  - `TestCachedSource_PersistIdentity_ConcurrentSameSession`
  - `TestCachedSource_PersistCapabilities_ConcurrentSameSession`
  - `TestCachedSource_CapabilitiesFrame_Staleness_Boundary`

**Strategy**:

1. Identify remaining uncovered branches from chunk 7.6
   per-function coverage report.
2. Write 8 tests targeting each branch.
3. Use real SQLite (Chunk 6.4 pattern) for integration.
4. No production code changes (test-only).

**Acceptance criteria**:

1. 8 tests PASS.
2. Coverage: internal/recall 82.1% → ≥95%.
3. All Render/Hash error branches covered.
4. No production code regression.

### 3.7 Chunk 8.7 — Bitemporal (ADR-014)

**Goal**: alpha.18 DecayScore precursor
(ScrubJay-MEM π_i + τ_i). Each agent_memory row gains
`transaction_time` (write clock) + `valid_time`
(semantic clock). Schema migration v30→v31.

**Files** (estimado ~500 LoC + 15 tests):

- `internal/store/bitemporal.go` NEW — schema migration
  adds 2 NOT NULL columns to agent_memory
  (transaction_time TEXT NOT NULL DEFAULT current_timestamp,
  valid_time TEXT NOT NULL DEFAULT current_timestamp).
- `internal/store/store.go` MODIFIED — `SaveFrame` /
  `GetFrame` / `ListFrames` accept + return bitemporal
  fields.
- `internal/store/time_travel.go` NEW — `RecallAtTime(t, kind)`
  for time-travel queries (returns frames with
  valid_time ≤ t AND transaction_time ≤ t).
- `internal/store/migration_31.go` NEW — migration from
  v30 → v31 (adds columns, defaults to current_timestamp
  for existing rows).
- `internal/store/{bitemporal,time_travel,migration}_test.go`
  NEW — 15 tests: transaction_time monotonicity,
  valid_time settability, time-travel consistency,
  migration safety.

**Strategy**:

1. Schema migration adds 2 columns with NOT NULL +
   DEFAULT current_timestamp for backward compat.
2. `SaveFrame` populates transaction_time = write clock.
3. `valid_time` settable per write (default = transaction_time).
4. `RecallAtTime(t, kind)` queries frames valid at time `t`.
5. 15 tests: monotonicity, settability, time-travel,
   migration safety.

**Acceptance criteria**:

1. 15 tests PASS.
2. Schema migration v30→v31 applies cleanly to existing
   data.
3. transaction_time monotonic across writes.
4. valid_time settable per write.
5. `RecallAtTime` time-travel queries work.

### 3.8 Chunk 8.8 — Docs followup + alpha.20 tag local

**Goal**: Documentar phase closeout + tag local.

**Files** (estimado ~600 LoC):

- `CHANGELOG.md` — `[4.0.0-alpha.20]` entry covering
  all 10 chunks (~250 LoC).
- `docs/v4-status.md` — §1.8 Phase 8 changelog
  (~150 LoC).
- `docs/v4-alpha-11-plan.md` — §8 already added by
  Chunk 8.0 (committed before chunks); confirm + add
  per-chunk cross-refs to dark-memory atomic mirror
  rows (~50 LoC).
- `docs/sota-critique.md` — §5.2.4 Phase 8 per-gap
  closure evidence table (~100 LoC).
- `git tag v4.0.0-alpha.20` LOCAL ONLY on final commit.

**Acceptance criteria**:

1. CHANGELOG entry covers 10 chunks.
2. v4-status §1.8 has 10 subsections (8.1-8.10).
3. v4-alpha-11-plan §8 already committed (Chunk 8.0);
   add cross-refs to atomic mirror rows.
4. sota-critique §5.2.4 lists §8.1+§8.2 critical gaps
   closed + alpha.20 follow-ups shipped + e2e gate
   enforced.
5. Local tag `v4.0.0-alpha.20` created.
6. Atomic mirrors Phase 9: 1 SUMMARY pinned + 7 SECTION
   pinned=false minimum.

### 3.9 Chunk 8.9 — Document `fake_authority` pattern examples

**Goal**: operator awareness for the intentionally
narrow pattern. Add examples of phrasings that DO and
DO NOT trigger each pattern (10 patterns total), plus
design rationale.

**Files** (estimado ~40 LoC doc-only):

- `docs/sota-critique.md §5.2.4` — appendix
  "Override validator patterns" with table:
  - Pattern name | Description | DO trigger example
    | DOES NOT trigger example | Design rationale
  - Cover all 10 patterns
  - fake_authority design rationale: intentionally
    narrow (privilege escalation requires sophisticated
    phrasing — design correct per security review).

**Acceptance criteria**:

1. Doc-only change (no code).
2. All 10 patterns documented with DO/DOES NOT examples.
3. fake_authority design rationale explained.

### 3.10 Chunk 8.10 — Meta-decision enforcement: e2e gate

**Goal**: codify alpha.21+ requirement for exhaustive
e2e gate before SHIP. Update `docs/v4-alpha-11-plan.md
§10` (Operator decisions) to add the meta-decision
formally.

**Files** (estimado ~30 LoC):

- `docs/v4-alpha-11-plan.md §10` MODIFIED — add
  meta-decision OD7: "Exhaustive e2e gate required
  before alpha.21+ SHIP".
- `docs/v4-alpha-11-plan.md §11` (Cross-references)
  MODIFIED — link to this SPEC §3.10.

**Acceptance criteria**:

1. OD7 added to §10 with required tests enumerated.
2. Cross-refs updated in §11.
3. Future phases cannot SHIP without 0 critical
   findings from exhaustive e2e.

## 4. Acceptance criteria (overall Phase 9 / alpha.20)

| Criterion | Target |
|---|---|
| Commits shipped | 10 (one per chunk) + 1 docs commit + 1 tag |
| LoC shipped | ~3,500 (incluyendo tests) |
| Tests passing | ≥100 new tests, 0 regressions |
| Coverage delta | internal/recall ≥95%, all v4alpha packages ≥85% |
| Phase 8 e2e T7 re-run | multi-subtask plan (NOT 1 subtask "bundle") |
| Phase 8 e2e T3 re-run | 14 personas (NOT 8) |
| Critical e2e findings | 0 |
| drift_judge verdict | ≥85% aligned per chunk |
| Cross-version hash pin | unchanged |
| Atomic mirrors | 1 SUMMARY pinned + 7 SECTION pinned=false |
| Phase 8 e2e critical gaps closed | 2 of 2 (8.1, 8.2) |
| alpha.20 follow-ups shipped | 5 of 5 (8.3-8.7) |
| Meta-decision e2e gate | codified in §10 OD7 |
| Local-only deploy | confirmed (no git push) |

## 5. Risks + mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| §8.1 wiring breaks v2 callers | LOW | HIGH | Feature flag `DARK_DELEGATION_BACKEND=v2` rollback; 8 e2e tests cover both paths |
| §8.2 merge introduces duplicate ids | LOW | MEDIUM | Source discriminator + dedup by id before response |
| §8.3 ONNX runtime heavy dep | MEDIUM | MEDIUM | Pluggable adapter; defer ONNX if too heavy; BGE-large + HTTP fetch can substitute |
| §8.4 ProGraph cycle detection infinite loop | LOW | HIGH | Kahn's algorithm + depth limit (default 2) |
| §8.5 HMAC rotation breaks chain | LOW | MEDIUM | HMAC key tagged per row; verify supports multiple keys |
| §8.7 schema migration v30→v31 corrupts data | LOW | HIGH | Migration test with 100k rows fixture; rollback path |
| §8.10 e2e gate adds cost | MEDIUM | LOW | Budget $5-10/phase; cheaper than post-ship bug fixes |
| LLM cost overruns across 10 chunks | MEDIUM | MEDIUM | Cache hits + reuse session LLMClient + DARK_DELEGATION_CACHE_TTL=1h |

## 6. Drift_judge gate per chunk

Per ADR-008 + v4-alpha-11-plan §9 vibe-loop pattern,
cada chunk sigue:

1. Implement spec.
2. Build + test: `go build ./...` clean, all tests pass.
3. Commit con detailed body citing spec + tasks.
4. Atomic mirror: 1 SUMMARY pinned=true + N SECTION
   pinned=false per chunk.
5. drift_judge: `dark_memory_judge(eval_type=drift_judge)`
   con artifact_ref apuntando a los files cambiados +
   spec_intent de este spec.
6. Verdict: aligned → continue; drift_detected → fix +
   re-publish; needs_human → STOP + surface al operator.

## 7. Cross-version lockstep hash pin

**Unchanged**: `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`

## 8. Cross-refs

- `docs/v4-alpha-11-plan.md §8` — Phase 8 alpha.20 plan
  (committed 4d48805).
- `docs/sota-critique.md §5.2.3` — alpha.20 follow-ups
  (5 items closed in §8.3-§8.7).
- `docs/sota-critique.md §5.2.4` — Phase 8 per-gap
  closure evidence (added by §8.8).
- `docs/specs/SPEC-alpha-11-phase7.md` — predecessor
  spec for alpha.19.
- `internal/v4alpha/transport/mcp/delegation.go`
  (alpha.19) — base for Chunk 8.1 wire-up.
- `internal/v4alpha/judge/personas_v4.go` (alpha.19) —
  base for Chunk 8.2 persona merge.
- `internal/v4alpha/judge/rubric.go` (alpha.19) — judge
  registry to extend in Chunk 8.2.
- `internal/recall/cache.go` (alpha.19) — base for
  Chunk 8.6 coverage polish.
- dark-memory rows 2322-2345 (Phase 8 e2e atomic
  mirrors — 24 rows; T1-T17 + caveats + final report).
- dark-memory row 2346 (Phase 8 §8 PLANNED decision
  pinned).

## 9. operator decisions recap

| Decision | Operator input | Implementation |
|---|---|---|
| D1 | §8.1+§8.2 critical gaps close | Chunks 8.1+8.2 §3.1+§3.2 |
| D2 | §8.3-§8.7 alpha.20 follow-ups | Chunks 8.3-8.7 §3.3-§3.7 |
| D3 | §8.8 docs + alpha.20 tag local | Chunk 8.8 §3.8 |
| D4 | §8.9 fake_authority documentation | Chunk 8.9 §3.9 |
| D5 | §8.10 meta-decision e2e gate | Chunk 8.10 §3.10 |
| D6 | Spec-first mandatory | This document |

## 10. Acceptance sign-off

Phase 9 (alpha.20) SHIPS when:

- [ ] All 10 chunks committed + 1 docs commit + 1 local tag.
- [ ] go vet clean, all tests pass, 0 regressions.
- [ ] Cross-version hash pin unchanged.
- [ ] drift_judge verdict ≥85% aligned per chunk.
- [ ] Atomic mirrors written (1 SUMMARY + 7 SECTION minimum).
- [ ] Phase 8 e2e T3+T7 re-run shows v4alpha exposed (NOT v2).
- [ ] Phase 8 e2e re-run with 0 critical findings.
- [ ] Operator confirms via "Cerremos" o equivalente.