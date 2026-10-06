# SPEC-alpha-11-phase13-house-keeping — Phase 13 contract: close Phase 12 deferred + pre-existing debt

**Status (2026-10-06)**: 📋 spec PROPOSED awaiting operator approval (T-201 implementation
blocked on this gate).

**Branch**: `feat/v4-redesign` (local-only, no remote push).

**Predecessor**: `docs/specs/SPEC-alpha-11-phase12-modification-events.md` (Phase 12 = Camino B,
agent_memory row 2425 SHIPPED at `v4.0.0-alpha.23` 2026-10-04).

**Cross-version lockstep hash pin** unchanged:
`4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`.

**Operator decision (2026-10-06)**: "A primero" — Opción A (house-keeping) chosen over
Opción B (M4 auto-update) and Opción C (security foundation). Phase 13 closes Phase 12
deferred holes + pre-existing debt; does NOT advance v4 north star.

**SOTA grounding (2026-10-06)**:
- [LangFuse v3 — semantic search](https://langfuse.com/docs/observability/data-model) —
  references Phase 12's single polymorphic events table as the data model
- [SQLite WAL best practices](https://sqlite.org/wal.html) — referenced in T-204 for the
  test-hang fix
- [Voyage AI `voyage-3` embeddings](https://docs.voyageai.com/docs/embeddings) —
  embedder integration target (1024d) for T-202
- [ONNX Runtime Go bindings yalue/onnxruntime_go](https://pkg.go.dev/github.com/yalue/onnxruntime_go)
  — bundled ONNX path for T-202 (cross-platform embedding)
- [PostgreSQL FTS + partial indexes](https://www.postgresql.org/docs/current/textsearch-indexes.html)
  — for T-203 postgres events parity (GIN index on target_table+target_row_id)
- `docs/sota-critique.md §7.6.9` — INV-19 namespace primitive reference

---

## §0 TL;DR + north star position

Phase 13 is **house-keeping**, not feature work. It closes 6 specific holes
left open when `v4.0.0-alpha.23` (Phase 12) shipped and the pre-existing debt
documented in agent_memory row 1370 (Phase H5 partial, 2026-08-28) and row
995 (C7 LLM-env-sensitive tests). Per the operator decision 2026-10-06
("A primero"), Phase 13 deliberately does NOT advance the v4 north star —
no M1 (mutable workflow runtime), no M4 (auto-update), no INV-11..INV-15
security. Those remain for Phase 14+.

### Phase 13 deliverables (after SHIP):

| # | Task | LoC | Pre-tag | Critical? |
|---|---|---|---|---|
| T-201 | NLI `EnsureNLIRouter` bug fix | ~300 | `alpha.24-pre-1` | 🔴 BLOCKS drift_judge |
| T-202 | Embedder integration in v4alpha/recall + `EmitEmbedderRefresh` wire | ~700 | `alpha.24-pre-2` | 🟢 depends on T-201 |
| T-203 | Postgres events parity (6+ notImpl stubs) | ~500 | `alpha.24-pre-3` | 🟢 standalone |
| T-204 | `TestBitemporal_E2E_MarkSuperseded` hang fix | ~100 | `alpha.24-pre-4` | 🟢 standalone |
| T-205 | `TestDelegateIntent_C7_*` LLM-env sensitivity fix | ~50 | `alpha.24-pre-4` | 🟢 standalone |
| T-206 | CHANGELOG [4.0.0-alpha.24] + v4-status §1.12 + INVARIANTS refresh | ~300 | `alpha.24-pre-5` | 🟢 depends on T-201..T-205 |
| **Σ** | | **~1,950 LoC** | 5 pre-tags | |

**North star impact**: alpha.23 ~95% → alpha.24 ~95% (no movement; this phase
holds the line + closes deferred). Post-Phase 13, the v4 architecture is
**production-clean** for the alpha.23 surface area.

**Why this matters**: without T-201, every `vibe_publish` returns `needs_human@0`
because the drift_judge NLI provider returns `provider unavailable`. The vibe-loop
engine is therefore physically incapable of returning `aligned` verdicts —
which means Phase 12's drift_judge infra is operating as a noise generator, not
a judge. Fixing T-201 is the difference between `drift_judge` being cosmetic
and being load-bearing.

---

## §1 Problem statement

### §1.1 The 3 holes from Phase 12 SHIPPED (agent_memory row 2425)

Phase 12 SHIPPED with **3 explicit deferred items** documented as known unknowns:

> Known unknowns (Phase 13+): Postgres parity for events (6 notImpl stubs),
> EmitEmbedderRefresh wire (no embedder code), EmitCacheInvalidation wire
> (only LRU exists; per Q-INDIAN selectivo emits nothing).

**Hole 1: Postgres events parity.** Phase 12 added 6 new `Event*` methods to
`store.Store` interface (`InsertEvent`, `GetEventByID`, `ListEvents`,
`ListEventsByProcessID`, `ListEventsByRootEventID`, `ListEventsByParentEventID`).
The sqlite implementation handles them. The postgres implementation returns
`notImpl` for all 6 (see `internal/store/postgres/store.go:476-515`). Operators
running dark-memory against postgres get `err = "notImpl"`. This is a **wire
contract** violation: the tool surface (event_log + event_replay, Phase 12 T-105)
is supposed to work uniformly across both backends.

**Hole 2: EmitEmbedderRefresh wire.** Phase 12 deferred 2 of 8 AutoEmitter
helpers (EmitEmbedderRefresh + EmitCacheInvalidation) by design — there were
no embedder cache callsites in the v4alpha/recall paths. However, the embedder
code IS present (`internal/embedder/{detect,embedder,mock,ollama,onnx,openai,voyage}`,
7 sub-packages, ~40 LoC). The integration was deferred from `alpha.19` (per
`c2_text.go:25` comment: "the alpha.19 embedder integration will replace this
slot"). Now that alpha.23 has shipped, the integration is still pending. With
it wired:
- 6/8 AutoEmitter helpers → 7/8 wired
- Real cosine-similarity contributes to the 0.50 vector-weight slot
- Operators can use the `MINIMAX_API_KEY` / `OPENAI_API_KEY` / `VOYAGE_API_KEY`
  to power real semantic recall instead of the FTS5-only fallback

**Hole 3 (deferred by design, NOT in Phase 13 scope)**: EmitCacheInvalidation.
Only LRU.Invalidate exists; per Q-INDIAN operator decision 2026-10-04, pure
cache invalidations (LRU, TTL) do NOT emit. **Phase 13 does NOT close this
hole — it remains a design choice, not a hole.**

### §1.2 The 3 holes from pre-Phase-12 debt

**Hole 4: NLI `EnsureNLIRouter` returns (nil, nil) despite DB has config.**

Documented in agent_memory row 1370 (Phase H5 partial, 2026-08-28). The
operator's dark.db confirms `nli_config_json` is 379 bytes, `enabled=true`,
but `drift_judge` continues to return `verdict=needs_human, reasoning="nli
score failed: nli: provider unavailable"`. Root cause hypothesis from row 175:
"stale in-memory cache that didn't invalidate across restarts" OR "different
store reference between MCP tool handler and orchestrator" OR "WAL/snapshot
lag". T-201 must:
- Trace `o.Store.GetProjectNLIConfig("default")` end-to-end (orchestrator
  caller → store impl → DB read)
- Identify why it returns nil despite DB has it
- Add a regression test that fails if the bug recurs

This is BLOCKING because every `vibe_publish` call after Phase 12 will hit this
infra issue and return `needs_human@0`. The drift_judge pipeline is physically
broken until T-201 lands.

**Hole 5: `TestBitemporal_E2E_MarkSuperseded_RecallAfterSupersession` hang.**

Reproduces on clean HEAD `1d925ce` (pre-Phase 12). Under `go test -short
./... >20s` it hangs. The test was always slow (heavy Bitemporal reasoning
across many rows), but with Phase 12's polymorphic events table the test
got slower because the events table is also part of the bitemporal reasoning.
Per operator decision document 0709 (zero data loss on destructive migrations),
the legacy 11-column `events` table is preserved as `events_legacy_v2_pre_alpha23`.
The new test must:
- Either skip under `-short` flag with a clear message
- Or factor the expensive path into a separate test file (`*_long_test.go`)
- Or be parallelized

T-204 picks option (b): factor into `_long_test.go` so `go test -short ./...`
returns in <30s, and the long test runs only under `-long` or `-run TestBitemporalLongB`.

**Hole 6: `TestDelegateIntent_C7_*` LLM-env-sensitive (2 tests).**

Documented in agent_memory row 995 (2026-08-18). Two tests assert
`sub.SystemPrompt != ""` presupposing a working LLM. They fail-fast (1-2s)
when the operator's shell env has an invalid API key (HTTP 401). The async
delegation pipeline needs a real LLM to compose the subagent's mindset. Fix
option (c) from row 995: refactor mindset_apply to surface HTTP status in
MindsetErr + tests skip if 401/429/5xx. T-205 picks option (c) which is the
most general (not just a test env workaround but a real runtime improvement).

### §1.3 What this is NOT

Phase 13 is **NOT**:
- A north-star advancement (no M1, no M4, no INV-11..INV-15)
- A new capability introduction
- A breaking change (cross-version lockstep hash pin unchanged)
- A new namespace addition (no canonical tools added/removed)

Phase 13 IS:
- Closing 6 specific known-bounded issues
- Improving test ergonomics (`go test -short` clean)
- Unblocking drift_judge (T-201)
- Wiring 1 more AutoEmitter helper (7/8 wired post-Phase 13)
- Postgres events parity (postgres-resident operators get event observability)

---

## §2 Goals + Non-Goals + Out of Scope

### §2.1 Goals (in priority order)

1. **🔴 G1 (T-201)**: drift_judge returns `aligned` or `drift_detected` (NOT
   `needs_human@0`) for a clean artifact. After T-201, the operator can run
   `vibe_publish` on a simple test artifact and see a real verdict.

2. **🟢 G2 (T-202)**: Embedder integration in v4alpha/recall C1/C2/C4 paths
   + `EmitEmbedderRefresh` AutoEmitter helper wired. After T-202:
   - 7/8 AutoEmitter helpers wired (was 6/8 post-Phase 12)
   - Real cosine-similarity contributes to recall scoring (was FTS5-only)
   - Operators can configure `MINIMAX_API_KEY` / `OPENAI_API_KEY` / `VOYAGE_API_KEY`
     per `internal/embedder/detect/detect.go:97-108` and see semantic recall work

3. **🟢 G3 (T-203)**: Postgres events parity. After T-203, all 6 events
   methods return real results (not `notImpl`) on postgres. Operators with
   `DARK_STORE_DRIVER=postgres` can use `dark_memory_event_log` and
   `dark_memory_event_replay` and see the same surface as sqlite.

4. **🟢 G4 (T-204)**: `go test -short ./...` returns in <30s. After T-204,
   the heavy Bitemporal test path is factored into `_long_test.go` and the
   short suite is fast.

5. **🟢 G5 (T-205)**: `go test ./internal/orchestration/...` passes without
   pre-existing LLM env vars. After T-205, the C7 LLM-dependent tests skip
   gracefully on 401/429/5xx.

6. **🟢 G6 (T-206)**: Documentation reflects the SHIPPED state. CHANGELOG
   [4.0.0-alpha.24] entry + v4-status §1.12 + INVARIANTS.md refresh.

### §2.2 Non-Goals

- **No new canonical namespace**: the 71 canonical tools / 19 namespaces
  remain frozen (per `canonicalNamespaces` carve-out, `internal/tools/registry.go:276`).
- **No schema bump**: schema stays at v32. T-203 (postgres events) does NOT
  require a sqlite schema change; it requires a postgres-side table creation
  in the bootstrap migration.
- **No breaking change**: cross-version lockstep hash pin
  `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb` unchanged.
- **No NLI provider configuration**: T-201 fixes the wiring bug; it does NOT
  add a new NLI provider. The existing `chat-deepseek` config in the
  operator's DB (row 1370) is what T-201 enables.
- **No embedder code addition**: the embedder code is already there
  (`internal/embedder/`). T-202 wires it into the recall path.

### §2.3 Out of Scope (deferred)

- **M1 Mutable workflow runtime** (ARCHITECTURE-V4 §2.1): deferred to Phase 14+.
- **M4 Auto-update protocol** (ARCHITECTURE-V4 §2.4): deferred to Phase 14+.
- **INV-11..INV-15 security v4 movements** (ARCHITECTURE-V4 §3.4): deferred to
  Phase 14+. Some (INV-13 redact, INV-14 SSRF, INV-15 injection scan) may
  already be partially ported from v3 — T-206 includes a brief audit.
- **EmitCacheInvalidation wire**: deferred by design (Q-INDIAN operator
  decision 2026-10-04, only LRU exists; per selectivo emits nothing).
- **Postgres `agent_memory` parity** (4 notImpl stubs at
  `internal/store/postgres/store.go:422-468`: `ListAgentMemoryByAnyEntity`,
  `MarkSupersededAgentMemory`, `RecallAtTime`): out of scope. Phase 13
  closes events parity only.
- **Pre-existing `TestBitemporal_E2E_MarkSuperseded` data-race fix**:
  T-204 factors into `_long_test.go` (avoidance), it does NOT fix the
  underlying data-race (which is pre-existing).
- **NLI provider config UI / CLI**: out of scope. Operators configure via
  `dark_memory_project_create` with `nli_config` parameter (already shipped
  in Phase 11/12 INV-19 wiring).

---

## §3 Tasks (T-201..T-206 with depends_on graph)

### §3.2 Task graph

```
T-201 (NLI fix) ─── BLOCKER for everything except T-204 + T-205
   │
   ├─→ T-202 (embedder integration)
   │
   ├─→ T-203 (postgres events parity)
   │
   └─→ T-206 (docs sweep)
                       ↑
T-204 + T-205 (test fixes) ── can run in parallel with T-202/T-203
                                   ↑
                                   └─→ T-206
```

Concrete depends_on:

| Task | depends_on | Parallelizable? |
|---|---|---|
| T-201 | — | no (blocker) |
| T-202 | T-201 | yes with T-203 + T-204 + T-205 |
| T-203 | T-201 | yes with T-202 + T-204 + T-205 |
| T-204 | — | yes with T-205 |
| T-205 | — | yes with T-204 |
| T-206 | T-201, T-202, T-203, T-204, T-205 | no (final sweep) |

### §3.3 Pre-tag sequence

```
commit: T-201 (alpha.24-pre-1)
commit: T-202 (alpha.24-pre-2)
commit: T-203 (alpha.24-pre-3)
commit: T-204 + T-205 (alpha.24-pre-4)
commit: T-206 (alpha.24-pre-5)
tag: v4.0.0-alpha.24 (final)
```

T-204 + T-205 are bundled into pre-4 because both are test-only and small
(~150 LoC total).

---

## §4 Per-task specification

### §4.1 T-201 — NLI `EnsureNLIRouter` bug fix (~300 LoC)

**File:line targets**:
- `internal/orchestration/orchestrator.go:256-281` — `EnsureNLIRouter` function
- `internal/orchestration/orchestrator.go:378-398` — `defaultDriftJudgeProvider`
- `internal/v4alpha/project/store.go` — `GetProjectNLIConfig` impl
- `internal/v4alpha/project/store_impl_postgres.go` — postgres equivalent
- `internal/v4alpha/judge/store.go` — NLI consumption

**Intent**: when the operator's project has `nli_config_json` stored + the
config has `enabled=true`, the orchestrator must successfully build an
NLI provider and the drift_judge pipeline must use it. Today, the pipeline
returns `needs_human@0` despite the config being valid.

**Approach** (3 steps):

1. **Trace**: log every step in `EnsureNLIRouter` with debug-level detail
   (gated by `DARK_DEBUG=1` env var to avoid noise in production). Capture:
   - `o.Store.ActiveProject()` return value
   - `o.Store.GetProjectNLIConfig("default")` return value (Config + nil)
   - `nliCfg != nil && nliCfg.Enabled` evaluation
   - `buildProvider(nliCfg)` final return

2. **Identify**: based on trace output, determine which of the 3 hypotheses
   from agent_memory row 1370 is the root cause:
   - (a) stale in-memory cache → invalidation on `SetProjectNLIConfig` +
     restart-triggered re-read
   - (b) different store reference → ensure orchestrator + MCP tool handler
     bind to the SAME `store.Store` instance (singleton or DI)
   - (c) WAL/snapshot lag → add `PRAGMA wal_checkpoint(TRUNCATE)` before
     reading `nli_config_json` + run a small validation query to force a read

3. **Fix + regression test**: apply the fix identified in (2) + add a test
   that:
   - Stores a valid `nli_config_json` via `project_create`
   - Calls `EnsureNLIRouter`
   - Asserts `(provider, nil)` returns with non-nil provider
   - Asserts `provider.ID() == "chat-deepseek"` (or whatever the config says)

**Tests required**: 4-6 tests covering each hypothesis + the happy path.

**Acceptance criteria**:
- `drift_judge` returns `aligned` for a clean artifact (operator can
  manually verify by publishing a known-clean test artifact)
- Regression test fails if the bug recurs
- No regression in any other drift_judge path

### §4.2 T-202 — Embedder integration in v4alpha/recall + EmitEmbedderRefresh wire (~700 LoC)

**File:line targets**:
- `internal/v4alpha/recall/c1_code.go` — embedder integration in C1
- `internal/v4alpha/recall/c2_text.go:25` — replace 0.50 vector-weight slot
  with real `EmbedBatch` cosine similarity (per `c2_text.go:21-25` comment)
- `internal/v4alpha/recall/c4_research.go:294` — replace 0.50 vector-weight
  slot in `scoreFTSPlusGraph` (per `c4_research.go:294` comment)
- `internal/v4alpha/event/auto_emit.go` — `EmitEmbedderRefresh` wire point
- `internal/v4alpha/recall/recall.go` — embedder provider injection (via
  `orchestration.Orchestrator.WithEmbedder`)
- `internal/embedder/embedder.go` — interface contract (already exists)

**Intent**: the embedder code (`internal/embedder/`, 7 sub-packages, ~40 LoC)
is already present. Phase 13 wires it into the recall paths so:
- C1/C2/C4 recall strategies use real cosine similarity instead of FTS5-only
- `EmitEmbedderRefresh` AutoEmitter helper emits when the embedder refreshes
  the entity graph (the wire point was deferred from Phase 12 because no
  caller existed — now we add the caller)

**Approach** (5 steps):

1. **Define embedder contract**: `internal/embedder/embedder.go` already has
   the `Embedder` interface. Verify it covers `EmbedBatch(ctx, texts) ([][]float32, error)`.
   If not, extend.

3. **Wire C1**: in `c1_code.go:50-100`, add `s.Embedder.EvaluateScore(ctx,
   [premise, hypothesis])` to the `scoreFTSPlusGraph` blend. The 0.50 vector
   weight now contributes real cosine-similarity.

3. **Wire C2**: in `c2_text.go:25-60`, replace "the vector signal is currently
   ABSORBED into FTS5 with synonym expansion" with a real `EmbedBatch` call
   + cosine similarity. Add `Weights.Vector = 0.50` to the literal.

4. **Wire C4**: in `c4_research.go:294`, same as C2 but for research rows.

6. **Wire EmitEmbedderRefresh**: in the embedder cache refresh goroutine
   (the call site where `internal/embedder/{mock,onnx,voyage}` invalidate
   the cache), call `eventholder.Get().EmitEmbedderRefresh(ctx, rowID,
   newEntityCount)`. Update `auto_emit.go` helper signature to match.

7. **Tests**: per wire point, add a test that:
   - Sets up a mock embedder
   - Runs the recall path
   - Asserts the score includes cosine similarity > 0
   - Asserts `EmitEmbedderRefresh` was called when the cache invalidates

**Tests required**: 6-8 tests (one per wire + one for EmitEmbedderRefresh).

**Acceptance criteria**:
- 7/8 AutoEmitter helpers wired (was 6/8 post-Phase 12)
- C1/C2/C4 recall paths use embedder (configurable via `DARK_MEMORY_EMBEDDER` env)
- Regression tests pass; pre-existing T-204 hang fix NOT regressed
- Cross-version lockstep hash pin unchanged

### §4.3 T-203 — Postgres events parity (~500 LoC)

**File:line targets**:
- `internal/store/postgres/store.go:476-515` — the 6 notImpl stubs
- `internal/store/postgres/ddl.go` — postgres events table bootstrap
- `internal/store/postgres/events.go` — NEW file with the 6 method impls

**Intent**: the 6 events methods (Phase 12 T-105 additions to `store.Store`
interface) return `notImpl` on postgres. After T-203, postgres operators
get the same surface as sqlite operators: `dark_memory_event_log` +
`dark_memory_event_replay` work uniformly.

**Approach** (4 steps):

1. **Bootstrap migration**: in `internal/store/postgres/ddl.go`, add the
   `events` table CREATE statement (matching schema v32 from sqlite's
   `internal/store/sqlite/ddl.go:1508`). Columns: same 28 + indexes (GIN on
   target_table+target_row_id, B-tree on process_id, B-tree on root_event_id).

2. **InsertEvent**: insert a row + return `id`. Encode `payload_json` as JSONB
   (not TEXT) for indexability.

4. **ListEvents**: support the 6 ListEventsFilter filters (target_table,
   target_row_id, process_id, parent_event_id, session_id, kind). Use
   partial GIN indexes on (target_table, target_row_id) for fast lookups.

4. **GetEventByID + ListEventsByProcessID + ListEventsByRootEventID +
   ListEventsByParentEventID**: trivial implementations over the base table.

6. **Tests**: per method, add a test that:
   - Creates a test event
   - Inserts via `InsertEvent`
   - Queries via the target method
   - Asserts roundtrip equality

**Tests required**: 6 tests (one per method) + 1 integration test that runs
all 6 in sequence.

**Acceptance criteria**:
- All 6 events methods return real results on postgres
- `dark_memory_event_log` + `dark_memory_event_replay` work on both backends
- HMAC chain continuous across `write_audit` + `events` on postgres
  (per ADR-016 + ADR-018)

### §4.4 T-204 — `TestBitemporal_E2E_MarkSuperseded` hang fix (~100 LoC)

**File:line targets**:
- `internal/store/sqlite/bitemporal_test.go` — current test (the hang)
- `internal/store/sqlite/bitemporal_long_test.go` — NEW file with the heavy path

**Intent**: the test hangs under `go test -short ./... >20s`. Per agent_memory
row 2425 + 995, this is a pre-existing issue exacerbated by Phase 12's
polymorphic events table (which is also part of the bitemporal reasoning).

**Approach** (2 steps):

1. **Rename**: move the heavy path of
   `TestBitemporal_E2E_MarkSuperseded_RecallAfterSupersession` into a new
   `internal/store/sqlite/bitemporal_long_test.go` file with the `Long`
   build tag.

3. **Add a short version**: create
   `TestBitemporal_E2E_MarkSuperseded_RecallAfterSupersession_Short` with
   a 3-row bitemporal dataset (instead of the original 50-row). Runs in <2s.

4. **Makefile/build tag**: add `-tags=long` to the CI long-test command.

**Tests required**: 2 tests (short and long).

**Acceptance criteria**:
- `go test -short ./...` returns in <30s (was hanging)
- `go test -tags=long ./...` runs the heavy path (still comprehensive)

### §4.5 T-205 — `TestDelegateIntent_C7_*` LLM-env sensitivity fix (~50 LoC)

**File:line targets**:
- `internal/orchestration/delegate_intent_test.go:127-129` — `llmAvailable` false positive
- `internal/v4alpha/mindset/mindset_apply.go:367-414` — `composeSystemPrompt`
  error handling

**Intent**: per agent_memory row 995, the C7 LLM-dependent tests fail-fast
on HTTP 401 (key invalid). Option (c) chosen: refactor `mindset_apply` to
surface HTTP status in `MindsetErr` and tests skip if 401/429/5xx.

**Approach** (3 steps):

1. **MindsetErr enhancement**: extend `MindsetErr` to carry the underlying
   HTTP status code + retry-after hint. Update `composeSystemPrompt`
   error path to populate the new fields.

3. **Test runner**: in `delegate_intent_test.go:127-129`, add a helper
   `skipIfLLMUnavailable(t)` that:
   - Calls `llmAvailable(orch)`
   - If `true`, attempts a tiny LLM probe (e.g., 1-token completion)
   - If probe returns 401/429/5xx, `t.Skip("LLM unavailable:", statusCode)`

4. **Tests pass**: existing tests skip gracefully on bad env; pass on good env.

**Tests required**: 2 tests (the 2 C7 tests get the helper).

**Acceptance criteria**:
- `go test ./internal/orchestration/...` passes without pre-existing LLM env vars
- `go test ./internal/orchestration/...` with valid key still runs the actual
  test (not skip silently)
- No regression in any other test

### §4.6 T-206 — Docs sweep (~300 LoC)

**File:line targets**:
- `CHANGELOG.md` — `[4.0.0-alpha.24]` entry (mirror Phase 12 entry structure)
- `docs/v4-status.md` — new §1.12 with T-201..T-205 results
- `docs/INVARIANTS.md` — refresh INV-19 status (T-203 closes INV-19 for postgres)
- `docs/events-architecture.md` — small addendum about postgres events parity
- `docs/specs/SPEC-alpha-11-phase13-house-keeping.md` — UPDATE with T-201..T-205 results

**Intent**: Phase 12 SHIPPED with comprehensive docs (CHANGELOG + v4-status +
NEW events-architecture.md, +1,119 LoC). Phase 13 continues the pattern.

**Approach**: follow the T-106 template (Phase 12 docs sweep) + add an
"LUCIDEZ 10/10 GREEN" section + an "OD7 gate" section.

**Acceptance criteria**:
- CHANGELOG entry exists, references all 5 pre-tags + final
- v4-status §1.12 has T-201..T-205 results with file:line refs
- INVARIANTS.md INV-19 status updated to reflect postgres parity

---

## §5 OD7 gate (Phase 13 specific)

The OD7 gate from Phase 12 (9 invariants) carries over unchanged. Phase 13
adds NO new invariants. The 9 invariants from Phase 12:

| # | Invariant | Phase 13 impact |
|---|---|---|
| (m) | single polymorphic events table [T-101] | unchanged |
| (j) | sentinel count == 1 [T-102] | unchanged |
| (k) | caller gets ErrRationaleRequired [T-102] | unchanged |
| (n) | HMAC chain continuous across both kinds [T-102] | unchanged |
| (o) | async drift_judge progress [T-103b] | T-201 enables; pre-T-201 was physics-only |
| (p) | subagent delegation tree [T-103c] | unchanged |
| (q) | audit-quality eval_types [T-104] | unchanged |
| (n) | events table observability [T-105] | T-203 enables for postgres |
| (s) | cross-table 1:1 ratio [T-107] | unchanged |

**Net OD7 status after Phase 13 SHIP**: 9/9 GREEN, with (o) and (n) having
**observability** enabled end-to-end (pre-Phase 13: physics-only).

---

## §6 LUCIDEZ 10/10 R-rules

- **R1 file:line** — every T-201..T-205 commit cites file:line refs in its
  commit message body. Cross-version lockstep hash pin unchanged.
- **R2 no shallow** — T-201 trace + identify is a 2-step minimum. T-202 wires
  3 recall paths + 1 AutoEmitter helper. T-203 implements 6 methods.
- **R3 root cause over symptom** — T-201 picks the root cause from the 3
  hypotheses (row 1370), not a workaround. T-202 wires the real embedder,
  not a stub.
- **R4 honest cost** — 1,950 LoC + 5 pre-tags + 4-6 weeks focused (per
  Phase 12 scaling).
- **R5 3 real options** — operator picked A over B+C (presented 2026-10-06).
- **R6 declare unknowns** — open questions §9.
- **R7 honest cost** — no new capability; just closes deferred.
- **R8 audit trail** — atomic mirror per SPEC-TEMPLATE.
- **R9 no lecture on protocol** — concise.
- **R10 pause-and-summarize** — end-of-chunk summary in commit message.

---

## §7 SOTA grounding

| Source | Used for |
|---|---|
| [LangFuse v3 — data model](https://langfuse.com/docs/observability/data-model) | T-203 postgres events parity (mirror sqlite schema) |
| [SQLite WAL best practices](https://sqlite.org/wal.html) | T-201 (c) hypothesis (WAL/snapshot lag) |
| [Voyage AI `voyage-3` docs](https://docs.voyageai.com/docs/embeddings) | T-202 embedder target (1024d) |
| [ONNX Runtime Go bindings](https://pkg.go.dev/github.com/yalue/onnxruntime_go) | T-202 bundled ONNX path |
| [PostgreSQL FTS + partial indexes](https://www.postgresql.org/docs/current/textsearch-indexes.html) | T-203 GIN index on (target_table, target_row_id) |
| `docs/sota-critique.md §7.6.9` | INV-19 namespace primitive reference |

---

## §8 Acceptance criteria (Phase 13 SHIP gate)

### §8.1 Build + test verification

- [ ] `go build ./...` clean
- [ ] `go vet ./...` clean
- [ ] `go test -short ./...` passes in <30s (T-204 enables)
- [ ] `go test -tags=long ./...` passes (T-204 enables)
- [ ] `go test ./internal/orchestration/...` passes without LLM env vars (T-205 enables)
- [ ] `go test ./internal/eventholder/...` passes (Phase 12 carry-over)
- [ ] `TestCanonicalOrder_Frozen_57_17_28` PASS (no new canonical tools)

### §8.2 Drift verification

- [ ] `drift_judge` returns `aligned` for a clean artifact (T-201 enables)
- [ ] Cross-version lockstep hash pin unchanged
- [ ] Embedder recall scoring includes cosine similarity > 0 (T-202 enables)
- [ ] Postgres events parity: 6/6 methods return real results (T-203 enables)

### §8.3 Documentation

- [ ] `CHANGELOG.md [4.0.0-alpha.24]` entry exists
- [ ] `docs/v4-status.md §1.12` has T-201..T-205 results
- [ ] `docs/INVARIANTS.md` INV-19 status updated
- [ ] `docs/specs/SPEC-alpha-11-phase13-house-keeping.md` updated with T-201..T-205 results

### §8.4 Atomic mirror

- [ ] 1 SUMMARY pinned row + 6 SECTION rows (1 per task) in agent_memory
- [ ] All rows tagged `spec:SPEC-alpha-11-phase13-house-keeping, mirror:atomic`
- [ ] All rows have `session_id` (bind_session=true)

### §8.5 Deployment

- [ ] `bin/dark-mem-mcp.exe` deployed at `v4.0.0-alpha.24`
- [ ] Old binary preserved at `bin/dark-mem-mcp.exe.bak-pre-v4.0.0-alpha.24-<ts>`
- [ ] Reload flag triggered
- [ ] Live smoke test: boot ok + migrations applied + 6/8 helpers wired (was 6/8) + 7/8 wired post-Phase 13 + drift_judge returns `aligned` for clean artifact

---

## §9 Open questions

1. **Q1**: T-201 root cause is one of (a/b/c) from agent_memory row 1370.
   Should we **plan for all 3** in the trace + apply a defensive fix that
   covers all 3 (cache invalidation + store singleton + WAL checkpoint), OR
   **trace first, then fix** (cheaper but requires operator to wait for trace
   output)?
   - **Recommended**: trace first, then fix. The 3 hypotheses are mutually
     exclusive at the bug level (only 1 is the root cause); a defensive fix
     adds 50-100 LoC of unused code paths.

2. **Q2**: T-202 wire order — should C1, C2, C4 be wired in parallel (3
   commits pre-2..pre-4) or sequentially (1 commit pre-2 covering all 3)?
   - **Recommended**: 1 commit covering all 3 (pre-2). They're conceptually
     the same change ("wire embedder into recall"); splitting adds noise.

3. **Q3**: T-203 postgres GIN index — partial index on
   `(target_table, target_row_id) WHERE project_id = ?` OR non-partial?
   - **Recommended**: partial. Most operators query by project_id; partial
     index is 5-10x smaller.

4. **Q4**: T-204 Makefile / build tag — should `make test` run with `-tags=long`
   by default OR only with `make test-long`?
   - **Recommended**: `make test` runs short; `make test-long` runs long.
     Avoids CI hangs.

5. **Q5**: T-205 surface HTTP status in MindsetErr — should this be a `breaking`
   change to the MindsetErr contract (consumers must handle new field) OR
   additive (new field, ignore if not present)?
   - **Recommended**: additive. `MindsetErr.HTTPCode` is a new field; if
   - `MindsetErr` already implements `error`, the field is optional via
   - `errors.As` + type assertion.

---

## §10 Risks + honest cost

### §10.1 Risks

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| T-201 root cause is something other than (a/b/c) | medium | medium | trace first; if new root cause, update spec + add to backoff |
| T-202 embedder recall regresses existing FTS5 scores | low | medium | A/B test on existing CI fixtures; roll back if regression > 5% |
| T-203 postgres events parity breaks sqlite parity | low | high | run all Phase 12 events tests against both backends |
| T-204 long-test factoring introduces new test bugs | low | low | preserve the original test body verbatim in `_long_test.go` |
| T-205 MindsetErr change breaks downstream callers | low | medium | additive field; type-assertion pattern documented |

### §10.2 Honest cost

- **Time**: 4-6 weeks focused (per Phase 12 scaling; ~1,950 LoC is roughly
  half of Phase 12's ~8,250 LoC but T-201 trace-and-fix may add 1-2 weeks
  if root cause is non-obvious).
- **LoC**: 1,950 source + ~500 test fixtures (50% of source per TDD standard)
  + ~300 docs (per Phase 12 scaling) = ~2,750 total.
- **Pre-tags**: 5 (`alpha.24-pre-1`..`pre-5`) + final `v4.0.0-alpha.24`.
- **Binary impact**: small (~+200 KB for embedder wiring + ~+50 KB for postgres
  events parity).

### §10.3 What Phase 13 does NOT solve

- **No M1 / M4 / INV-11..INV-15**: deferred to Phase 14+.
- **No schema v33**: schema stays at v32.
- **No new canonical tool**: 71/19 stays frozen.
- **No NLI provider addition**: just fixes the wiring bug for the existing
  `chat-deepseek` config.

---

## §11 References

- Phase 12 SHIP: agent_memory row 2425
- Phase 12 deferred: agent_memory rows 2425, 2427
- NLI infra bug: agent_memory row 1370 (Phase H5 partial, 2026-08-28)
- TestBitemporal hang: agent_memory row 2425 + clean HEAD `1d925ce`
- TestDelegateIntent C7: agent_memory row 995 (2026-08-18)
- Embedder integration comment: `internal/v4alpha/recall/c2_text.go:21-25`,
  `internal/v4alpha/recall/c4_research.go:294`
- Postgres notImpl stubs: `internal/store/postgres/store.go:476-515`
- 8 AutoEmitter helpers: `internal/v4alpha/event/auto_emit.go` (6/8 wired post-Phase 12)
- HMAC chain: ADR-016 + ADR-018, `DARK_AUDIT_HMAC_KEY` env var
- Cross-version lockstep hash pin:
  `4e6196a07c7903dc712fd4a96cbc4df49317stldamachb11b`

---

**Spec END** (~700 LoC, atomic mirror pending)