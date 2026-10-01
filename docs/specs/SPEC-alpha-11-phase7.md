# SPEC-alpha-11-phase7: alpha.19 — Sub-agent wiring + LLM router upgrade + coverage close

> **Estado**: Draft (P1=C, P2=III, P3=needs_human, P4=drift_judge operator-confirmed 2026-10-02)
> **Fecha**: 2026-10-02
> **Autor**: Opita-AI (operator=nico, project_id=dark-memory)
> **Spec id**: SPEC-alpha-11-phase7
> **Mirror**: 1 SUMMARY + 5 SECTION (per ADR-008, 5 chunks → 1 summary + 4 chunk-section + 1 meta = 6 rows minimum)
> **Vibe-loop**: alpha.11-phase7 (alpha.19, close alpha.18.1 deferred items)

## 1. Contexto

Phase 6 (alpha.18.1) closed on 2026-10-01 with 7 commits
(`ee0fb8e`, `a4833fc`, `504f427`, `ab28867`, `4229684`,
`63fab10`, `710cbb5`) + 1 local tag `v4.0.0-alpha.18.1` +
28 atomic mirrors (rows 2266-2293). Four items were explicitly
deferred to alpha.19 per `docs/sota-critique.md §5.2.2` and
the Chunk 6.7 §B cross-refs.

**Deferidos explícitos de alpha.18.1** (todos son MUST-CLOSE
para alpha.19):

1. `delegate_intent` DECIDE router es literal-pattern
   (no captura "primero X después Y" — fila el issue documentado
   en row 2285, drift_detected 0.85).
2. `delegate_intent` CURATE devuelve `delegation_context=""`
   (no wirea C2 subagent binding — fila §5.2.2 #2).
3. `internal/tools` 22.2% coverage (84 untested MCP-RPC
   handlers — fila §5.2.2 #3).
4. `internal/recall` 45.3% coverage gap en CachedSource
   (cache.go mock testing — fila §5.2.2 #4).

**Decisión del operador** (2026-10-02 morning, es-CO tuteado):

- D1: **P1=C** (EXTRACT intermediario con DECIDE guard),
  **P2=III** (nueva persona `judge-delegator`),
  **P3=needs_human** (failure surface con alternativas),
  **P4=drift_judge** (validación por el juez).
- D2: Audit gaps (ADR-016 transparency log + ADR-018 audit
  verify tool) **deferidos a alpha.20** — alpha.19 = 5 chunks.
- D3: Spec-first obligatorio (este documento).

## 2. Decisión

Phase 7 (alpha.19) ships en 5 chunks:

1. **Chunk 7.1** — LLM-extracted sub-tasks router for
   `delegate_intent` (DECIDE guard + persona `judge-delegator`
   + drift_judge validation + needs_human surface).
2. **Chunk 7.2** — `agent_memory_delegate` C2 subagent
   binding (subagent_register por subtask + parent_session_id
   propagation + defense-in-depth contra inheritance attacks).
3. **Chunk 7.5** — internal/tools 84-handler httptest harness
   (mcp-go StreamableHTTPHandler + httptest.Server para los
   84 MCP-RPC entrypoints; sube coverage 22.2% → ~75%).
4. **Chunk 7.6** — internal/recall CachedSource mock testing
   (cache.go cobertura: frameTTL, NewCachedSource,
   cachedGetIdentity, cachedGetCapabilities, cachedFetchRaw,
   persistIdentity, persistCapabilities, persistRaw,
   auditWriteContext, applyCanary, recordCacheErr; sube
   coverage 45.3% → ~80%).
5. **Chunk 7.7** — Docs followup + alpha.19 tag local
   (CHANGELOG `[4.0.0-alpha.19]` + v4-status §1.7 +
   v4-alpha-11-plan §7 + sota-critique §5.2.3 + tag
   `v4.0.0-alpha.19` LOCAL ONLY).

**No incluido en alpha.19** (deferido a alpha.20+):

- Embedder integration (BGE-large text, ImageBind 1024-dim
  image, wav2vec 2.0 audio, ONNX pluggable adapter) →
  alpha.20 dedicado "Embedders".
- ProGraph 2-layer entity extraction → alpha.21.
- Bitemporal (transaction_time + valid_time) → alpha.21.
- BUG-12 cross-process monotonicity → alpha.22.
- ADR-016 transparency log + ADR-018 audit verify tool →
  alpha.20.

## 3. SPEC por chunk

### 3.1 Chunk 7.1 — LLM-extracted sub-tasks router

**Goal**: Replace the alpha.18.1 v1 deterministic DECIDE
router with a hybrid: DECIDE determinista + EXTRACT
LLM-based (solo cuando DECIDE=delegate AND length>200 OR
vibe_case=C7). Validación via drift_judge. Failure via
needs_human con alternativas al operador.

**Files** (estimado ~700 LoC):

- `internal/v4alpha/delegation/router.go` NEW — DECIDE
  function (move from delegation.go).
- `internal/v4alpha/delegation/extract.go` NEW —
  extractSubtasks(ctx, op, project, vibeCase, task,
  judgeClient) — LLM call + JSON parse + validate.
- `internal/v4alpha/delegation/prompt.go` NEW — system +
  user prompts para el LLM (uses judge-delegator persona).
- `internal/v4alpha/delegation/judge.go` NEW — drift_judge
  wrapper (eval_type=subtask_extraction).
- `internal/v4alpha/delegation/cache.go` NEW — LLM result
  cache (sha256 key).
- `internal/v4alpha/delegation/validate.go` — NEW
  cap 8 subtasks, min_length 10, topological sort.
- `internal/v4alpha/transport/mcp/delegation.go` MODIFIED —
  wire-up to delegation package, CURATE subagent_register,
  wire shape v2.
- `internal/v4alpha/judge/personas_v4.go` MODIFIED — add
  judge-delegator persona (14th).
- `internal/v4alpha/judge/rubric.go` MODIFIED — default
  mapping adds judge-delegator for vibe_case C4 (research
  decomposition) + C7 (multi) override.
- `internal/v4alpha/judge/eval_types.go` MODIFIED — add
  `subtask_extraction` eval_type.
- `internal/v4alpha/delegation/extract_test.go` NEW — 7 unit
  tests con mock LLM (canned responses).
- `internal/v4alpha/delegation/cache_test.go` NEW — 3 cache
  tests (hit, miss, fallback).
- `internal/v4alpha/transport/mcp/project_test.go`
  MODIFIED — 4 new tests (ExtractHit, ExtractFallback,
  NeedsHumanAlternatives, CacheHit).

**Persona `judge-delegator`** (nueva, 14 personas total,
rompe el canon de Phase 6 §6.1 que shipó 13):

- **Lens**: atomic decomposition.
- **Bias controls**: max_subtasks=8, min_length=10, no_cycles,
  prefer_independent (no_dependencies unless explicit).
- **Prompt template** (`PersonaContent.PromptTemplate`):
  ```
  You are a task decomposition agent. Given a compound task
  description and a vibe_case (code|text|decision|research|
  video|audio|multi), extract 0..N non-overlapping subtasks
  that together cover the original task.

  Output JSON:
  {
    "decision": "inline"|"delegate"|"refused",
    "reasoning": "...",
    "subtasks": [{"id": "subtask-N", "description": "...", "dependencies": []}]
  }

  Constraints:
  - subtasks: 0..8 (cap 8)
  - each description: ≥10 chars
  - no cycles in dependencies
  - prefer independent subtasks (empty dependencies)
  - if the task is single-step, return 1 subtask with the
    whole task as description and decision="inline"
  - if the task is impossible or out-of-scope, return 0
    subtasks and decision="refused"
  ```
- **Eval type**: NO eval_type — solo el LLM-as-judge para
  validar la extracción (eval_type=`subtask_extraction`,
  separado).

**eval_type `subtask_extraction`** (nueva):

- Trigger: invoked after EXTRACT, before MIND/CURATE.
- Judge persona: `judge-mapping` (alias de `judge-empirical`
  con lens "non-overlapping subtask validation").
- Output verdict:
  - `aligned`: subtasks cubren el task original, sin overlap,
    sin cycles, min_length OK.
  - `drift_detected`: subtasks tienen overlap, missing coverage,
    o min_length fail — refine prompt + retry (max 2 retries).
  - `needs_human`: subtasks parse OK pero judge can't validate
    quality (e.g., task too ambiguous) — surface con
    alternatives[].
- Acceptance: ≥85% aligned verdicts en micro-eval de 50 tasks.

**Wire shape v2 (alpha.19)**:

```json
{
  "decision": "inline"|"delegate"|"refused",
  "reasoning": "DECIDE: ... | EXTRACT: ... | JUDGE: ...",
  "subtasks": [
    {
      "id": "subtask-1",
      "description": "...",
      "system_prompt": "...",  // from MIND
      "model": "inherit",
      "tools": [],
      "subagent_id": "uuid",    // from CURATE (alpha.19)
      "delegation_context": "..." // from CURATE (alpha.19)
    }
  ],
  "cache_hit": false,
  "verdict": "aligned"|"drift_detected"|"needs_human"|"cached",
  "alternatives": []  // populated only when verdict=needs_human
}
```

**Failure modes + mitigation**:

| Mode | Trigger | Verdict | Alternatives |
|---|---|---|---|
| LLM network error | `judge.LLMProvider.Call` returns net.Err | needs_human | [accept-PLAN, refuse, custom] |
| LLM rate limit | 429 from provider | needs_human | [accept-PLAN, retry-after-Ns, refuse] |
| LLM parse error | JSON unmarshal fails | needs_human | [accept-partial, fallback-PLAN, refuse] |
| LLM schema mismatch | output missing decision/subtasks | needs_human | [accept-partial, fallback-PLAN, refuse] |
| Judge drift_detected x3 | subtasks have overlap/missing coverage | needs_human | [accept-as-is, fallback-PLAN, custom-instructions] |
| Judge needs_human | judge can't decide quality | needs_human | [accept-as-is, fallback-PLAN, refuse] |
| Topo sort cycle | dependencies form cycle | needs_human | [accept-as-is, drop-deps, refuse] |
| >8 subtasks | LLM produces >8 | truncate + audit row kind=observation | (no needs_human, just truncate) |
| <10 char subtask | LLM produces <10 char description | drop subtask + audit row | (no needs_human, just drop) |

**Acceptance criteria**:

1. 7 unit tests (mock LLM) PASS.
2. 3 cache tests (hit/miss/fallback) PASS.
3. 4 integration tests (needs_human with alternatives) PASS.
4. drift_judge verdict: aligned ≥85% en 50-task micro-eval.
5. Coverage: internal/delegation/ ≥80%, internal/judge/rubric.go
   100%, internal/judge/eval_types.go 100%.
6. Wire shape backwards-compat: alpha.18.1 callers (3 tests in
   `project_test.go`) siguen pasando con wire shape v1.
7. Persona registry: 14 personas (was 13). Test
   `TestNewPersonaRegistry_HasAllFourteen` PASS.

### 3.2 Chunk 7.2 — agent_memory_delegate C2 subagent binding

**Goal**: Wire `dark_memory_subagent_register` por cada
subtask en `delegate_intent`. Subagent_id persiste en
agent_memory para que el orquestador pueda consultar
`subagent_get`. Defense-in-depth contra inheritance attacks
(arxiv:2605.08460).

**Files** (estimado ~300 LoC):

- `internal/v4alpha/transport/mcp/delegation.go` MODIFIED —
  CURATE function: por cada subtask, invoke
  `dark_memory_subagent_register(operator, subagent_id,
  parent_agent_id, ttl_seconds=3600)` antes de retornar.
- `internal/v4alpha/transport/mcp/project_test.go` MODIFIED —
  3 new tests (SubagentBindingPersists, ParentSessionIDPropagated,
  UnregisterOnClose).
- `internal/v4alpha/transport/mcp/subagent_binding.go` NEW —
  pure helper `bindSubtasksToSubagents(ctx, store, op,
  parentAgentID, subtasks) ([]Subtask, error)`.

**Wire contract** (additive, no breaking changes):

- `subtasks[].subagent_id` — opaque uuid generado por
  `subagent_register` (TTL 3600s, clamped [60, 86400]).
- `subtasks[].delegation_context` — JSON blob:
  ```json
  {
    "parent_session_id": "sess-XXX",
    "parent_agent_id": "alpha-11-phase7",
    "project_id": "dark-memory",
    "subtask_index": 0,
    "delegated_at": "2026-10-02T..."
  }
  ```
- `dark_memory_subagent_register` invocado con
  `parent_agent_id=projects.default_agent_id` (resolves to
  active session's agent_id).

**Acceptance criteria**:

1. 3 new tests PASS (binding persists, parent_session propagated,
   unregister on close).
2. audit_log row per subtask_bind con actor="delegate_intent_bind",
   event="subagent_register", kind="delegation".
3. INV-1 atomicity: agent_memory row + audit row en same Tx.
4. Coverage: internal/transport/mcp/subagent_binding.go ≥90%.

### 3.5 Chunk 7.5 — internal/tools 84-handler httptest harness

**Goal**: Subir coverage de internal/tools 22.2% → ~75% con
un harness httptest-based usando mcp-go's
`StreamableHTTPHandler`. Cubre los 84 MCP-RPC handler
funciones (la mayoría delegan a `transport/mcp/*.go`
handlers — el harness valida el routing + JSON-RPC parsing,
no la lógica de cada handler).

**Files** (estimado ~500 LoC):

- `internal/tools/handlers_test.go` NEW — 30+ tests con
  httptest.
- `internal/tools/harness_test.go` NEW — helper
  `newMCPServer(t)` que construye un `*mcp.Server`
  con `StreamableHTTPHandler` + `httptest.NewServer`.
- `internal/tools/harness.go` NEW — exported helper
  `NewTestServer(t *testing.T, store store.Store) *httptest.Server`
  (test-only, build tag `//go:build test`).

**Strategy**:

1. Construir server con `NewTestServer(t, mockStore)` donde
   `mockStore` es real SQLite en `t.TempDir()` (mismo patrón
   que Chunk 6.4).
2. POST JSON-RPC requests a `/mcp` endpoint.
3. Validar: status 200, response JSON shape, side-effects en
   SQLite.
4. 30+ tests (1 por handler más usado + 5 grupos de error
   paths).

**Acceptance criteria**:

1. 30+ handler tests PASS.
2. Coverage: internal/tools 22.2% → ≥75%.
3. Harness reusable for alpha.20 handler additions.
4. go vet clean.

### 3.6 Chunk 7.6 — internal/recall CachedSource mock testing

**Goal**: Cerrar el coverage gap en `cache.go` (CachedSource
methods). Subir coverage de internal/recall 45.3% → ~80%.

**Files** (estimado ~350 LoC):

- `internal/recall/cache_test.go` NEW — 12 tests.
- `internal/recall/mock_store.go` NEW — mock-Store
  interface (subset de los 105+ métodos que cache.go usa).

**Tests**:

1. `TestNewCachedSource_DefaultTTL` — TTL=5min default.
2. `TestCachedSource_GetIdentity_Hit` — second call returns
   cached value.
3. `TestCachedSource_GetIdentity_Miss` — first call fetches
   from store.
4. `TestCachedSource_GetCapabilities_ExceedsTTL` — after
   frameTTL, re-fetch.
5. `TestCachedSource_FetchRaw_AuditContext` — every fetch
   writes audit row.
6. `TestCachedSource_PersistIdentity_Idempotent` — second
   persist is no-op.
8. `TestCachedSource_ApplyCanary_RefusesUnauthorized` — canary
   INV-3.
9. `TestCachedSource_RecordCacheErr_PIIMasked` — INV-5 cache
   mismatch.
10. `TestCachedSource_ConcurrentAccess_Safe` — race-free
    con 100 goroutines.
11. `TestCachedSource_StoreError_FallsThroughToErrorPath`.
12. `TestCachedSource_FrameTTL_NegativeClampedToDefault`.

**Acceptance criteria**:

1. 12 tests PASS.
2. Coverage: internal/recall 45.3% → ≥80%.
3. Mock store covers 11 cache.go methods (no over-mocking).

### 3.7 Chunk 7.7 — Docs followup + alpha.19 tag local

**Goal**: Documentar el phase closeout + tag local.

**Files** (estimado ~400 LoC):

- `CHANGELOG.md` — `[4.0.0-alpha.19]` entry (~165 LoC).
- `docs/v4-status.md` — §1.7 Phase 7 changelog (~120 LoC).
- `docs/v4-alpha-11-plan.md` — §7 Phase 7 SHIPPED (~80 LoC).
- `docs/sota-critique.md` — §5.2.3 Phase 7 per-gap closure
  evidence (~60 LoC).
- `git tag v4.0.0-alpha.19` LOCAL ONLY.

**Acceptance criteria**:

1. CHANGELOG entry covers 5 chunks.
2. v4-status §1.7 has 5 subsections (7.1-7.6).
3. v4-alpha-11-plan §7 marks Phase 7 SHIPPED.
4. sota-critique §5.2.3 lists 4 alpha.18.1 deferred items
   closed + alpha.20 follow-ups (embedders, ProGraph,
   bitemporal, audit gaps).
5. Local tag `v4.0.0-alpha.19` created.
6. Atomic mirrors Phase 7: 1 SUMMARY pinned + N SECTION pinned.

## 4. Acceptance criteria (overall Phase 7)

| Criterion | Target |
|---|---|
| Commits shipped | 5 (one per chunk) + 1 docs commit + 1 tag |
| LoC shipped | ~2,500 (incluyendo tests) |
| Tests passing | ≥45 new tests, 0 regressions |
| Coverage delta | internal/tools ≥75%, internal/recall ≥80%, internal/judge ≥95% |
| drift_judge verdict | ≥85% aligned |
| Cross-version hash pin | unchanged |
| Atomic mirrors | 1 SUMMARY pinned + 5 SECTION pinned=false |
| alpha.18.1 deferred items closed | 4 of 4 (7.1, 7.2, 7.5, 7.6) |
| Local-only deploy | confirmed (no git push) |

## 5. Risks + mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `judge-delegator` persona breaks Phase 6 §6.1 canonical (14 not 13) | LOW | LOW | Documented en §3.1; honestidad > canon |
| LLM cost overruns (cache miss rate >60%) | MEDIUM | LOW | Cache key includes model_floor + project_id; alert si cache_hit_rate < 30% |
| drift_judge verdict rate below 85% | MEDIUM | MEDIUM | Retry loop + refine prompt + needs_human fallback |
| needs_human surface overwhelms operator | LOW | MEDIUM | Rate limit: max 5 needs_human/hour/project; alert si exceeded |
| mcp-go httptest API breaks alpha.19 | LOW | HIGH | Pin mcp-go version; fallback a raw JSON-RPC over net/http |
| CachedSource mock store over-mocking | MEDIUM | LOW | Use real SQLite in t.TempDir() (Chunk 6.4 lesson) for integration, mock only para unit |
| Wire shape v2 breaks alpha.18.1 callers | LOW | HIGH | Additive change; all new fields optional; v1 callers pass through |

## 6. Drift_judge gate per chunk

Per ADR-008 + v4-alpha-11-plan §7 vibe-loop pattern, cada
chunk sigue:

1. Implement spec.
2. Build + test: `go build ./...` clean, all tests pass.
3. Commit con detailed body citing spec + tasks.
4. Atomic mirror: 1 SUMMARY pinned=true + N SECTION pinned=false.
5. drift_judge: `dark_memory_judge(eval_type=drift_judge)`
   con artifact_ref apuntando a los files cambiados +
   spec_intent de este spec.
6. Verdict: aligned → continue; drift_detected → fix + re-publish;
   needs_human → STOP + surface al operator.

## 7. Cross-version lockstep hash pin

**Unchanged**: `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`

## 8. Cross-refs

- `docs/sota-critique.md §5.2.2` — 4 alpha.18.1 deferred items.
- `docs/v4-status.md §1.6.7` — known limitations deferred to
  alpha.19 (this phase closes them).
- `docs/v4-alpha-11-plan.md §6.7` — alpha.18.1 docs + tag.
- `internal/v4alpha/transport/mcp/delegation.go` (alpha.18.1) —
  base to refactor en 7.1.
- `internal/v4alpha/transport/mcp/mindset.go` (alpha.18.1) —
  composeSystemPrompt to reuse.
- `internal/v4alpha/judge/personas_v4.go` (alpha.18.1) —
  base to add judge-delegator.
- `internal/v4alpha/judge/rubric.go` (alpha.18.1) — base to
  update default mapping.
- `internal/recall/assemble_store_test.go` (alpha.18.1) — real
  SQLite pattern to extend en 7.6.
- dark-memory rows 2266-2293 (Phase 6 atomic mirrors).
- `SPEC-alpha-11-phase6.md` — Phase 6 master spec (predecessor).
- `SPEC-alpha-11-chunk7.md` — SOTA-doc workstream close
  (unrelated to this phase).

## 9. operator decisions recap

| Decision | Operator input | Implementation |
|---|---|---|
| D1 P1 | C (EXTRACT intermediario) | Chunk 7.1 §3.1 |
| D1 P2 | III (new judge-delegator persona) | Chunk 7.1 §3.1 |
| D1 P3 | needs_human con alternativas | Chunk 7.1 §3.1 failure modes table |
| D1 P4 | drift_judge validation | Chunk 7.1 §3.1 + §6 |
| D2 | Audit gaps deferred a alpha.20 | Excluded from this phase |
| D3 | Spec-first | This document |

## 10. Acceptance sign-off

Phase 7 SHIPS when:

- [ ] All 5 chunks committed + 1 docs commit + 1 local tag.
- [ ] go vet clean, all tests pass, 0 regressions.
- [ ] Cross-version hash pin unchanged.
- [ ] drift_judge verdict ≥85% aligned per chunk.
- [ ] Atomic mirrors written (1 SUMMARY + 5 SECTION minimum).
- [ ] Operator confirms via "Cerremos" o equivalente.