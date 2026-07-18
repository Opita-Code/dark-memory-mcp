# Dark Memory MCP — Master Plan v1

> **Snapshot**: 2026-07-15 16:32 UTC (initial), refreshed through v1.4.2 (2026-07-18) · **dark.db** spec_id `178` (canónico)
>
> Este documento es el **roadmap vivo** del proyecto. Cualquier wave nueva debe referenciarlo vía `dark_research_spec_update(spec_id=178, ...)`. Wave 4 (MCP server) shipped in v1.0.0; the remaining Wave 4 work (CLI, runbooks, bridge conformance, dark-recall) shipped across v1.0.0–v1.4.x. See **Status** section below for the current state.

---

## 0. TL;DR

| Estado | Qué | Siguiente |
|---|---|---|
| ✅ **Waves 1–4 shipped** through v1.4.2 (2026-07-18) | All sections complete except L1 (Atomic Context), L3 (Delegation), L4 (Persona), L5 (Mod System), L6.2/6.3 (Claude Code / Cursor adapters) | Wave 5: L1 + L3 + L4 + L5 + L6.2/6.3 |
| ✅ **v1.4.2 released** as the canonical state | release-integrity@1.0.0 constitution, version resolver, drift detection in health_ping, vibecase C1..C7 taxonomy | Maintain + monitoring |
| 🚧 **Wave 5+** (atomic context, persona, mods) | 5+ atomic specs pending | DMAP v1.1 |

---

## 1. Waves entregadas

### Wave 1 — Storage foundation (spec 154)
- `internal/store/Store` interface + `factory.go` + sqlite + postgres impls
- `internal/migrate/sqlite/ddl.go` + `internal/migrate/postgres/ddl.go` (v1..v8)
- 6 invariants baseline: INV-1 write_audit, INV-3 canary, INV-4 constitution watchdog, INV-5 cache re-hash, INV-6 mod sanitize, + INV-7 project namespace (added Wave 3p1)
- **Commit raíz**: `679a17e w3p1: project namespace (INV-7) + migration v7`

### Wave 2 — Context economy (spec 156)
- `internal/context/` (ArtifactContext, SessionContext, PolicyContext)
- `internal/economy/` (Atlan 5-bucket pipeline: dedup → filter → truncate → compress → cap)
- `tests/context/`, `tests/economy/`

### Wave 3 part 1 — Project namespace + INV-7 (specs 157 / 679a17e)
- Migration v7 + project_id tagging en todas las tablas
- Re-abierto por review: 5 HIGH findings (W3-001..W3-005) → spec 171 remediation
- **Sub-tasks cerradas**:
  - **T1** W3-001 — GetRun + ListItems cross-project isolation (drift 149)
  - **T2** W3-005 — SetActiveProject validation (drift 150)
  - **T3** W3-004 — default project auto-seed (drift 151)
  - **T4a-g** W3-002 — read-method project filtering (sessions/specs/brands+compliance/artifacts/drift+ssd/constitutions+mods/docs) (drifts 152, 153, 155-159)
  - **T5** W3-003 — Postgres option (b): drop RLS, mirror SQLite explicit WHERE (drift 162)
- **Cierre**: spec 157 drift_resolved, Wave 3 part 1 ships

### Wave 3 part 2 — Orchestrators O1-O5 (spec 173)
- `internal/orchestration/{session_start,session_close,research_topic,recall_context,judge}.go`
- `internal/orchestration/{orchestrator,research_backend,llm_client,llm_selector,recommended_models}.go`
- 33 tests verdes (54/54 → 75/75 progresivo)
- **Cierre**: 5/5 orchestrators shipped, drift_resolved (drift 170)
- **Bookkeeping cleanup** (Wave 3 part 3 Task 0): 18 artifacts de spec 173 sincronizados a `validation_status=passed` (artifact_ids 299-322)

### Wave 3 part 3 — Orchestrators O7-O12 (spec 177) — SHIPPED
6 orchestrators nuevos shipped + 41 tests verdes (suite completa 74/74 + everything since):

| # | Orchestrator | Archivo | Tests | Función |
|---|---|---|---|---|
| O7 | `PublishVibe` | `publish_vibe.go` | 9 | Meta-orchestrator: spec_create + artifact_log + brand_match + compliance_check + drift_judge + drift_log |
| O8 | `JudgeConsensus` | `judge_consensus.go` | 8 | N-shot Judge (1..7) con modal + confidence interval |
| O9 | `ActivePolicy` | `active_policy.go` | 5 | Snapshot constitution + mods + canary (INV-4 drift detection) |
| O10 | `MemoryState` | `memory_state.go` | 4 | Runtime counts + driver + schema_version |
| O11 | `ResolveDrift` | `resolve_drift.go` | 7 | Human gate: accept/reject + doble-resolve → `ErrInvalidState` (nuevo) |
| O12 | `VibeSpec` | `vibe_spec.go` | 8 | spec_create con tasks validation (unique ids + no cycle + depends_on) |

Cambios colaterales:
- `internal/store/store.go`: añadido `ErrInvalidState` (necesario para O11)
- `tests/orchestration/orchestrator_test.go`: +1522 lines (41 tests nuevos)

**Spec 157 (cascade root)**: 9/9 orchestrators shipped (3 originales: ResearchTopic, RecallContext, Judge + 6 nuevos: PublishVibe, JudgeConsensus, ActivePolicy, MemoryState, ResolveDrift, VibeSpec). Drift_judge pendiente.

**Total orchestrators live**: 11 (9 spec 157 + 2 session lifecycle O1/O2)

### Side wave — opencode clean install (spec 175)
- `~/.config/opencode/opencode.jsonc` con default_agent=dark-research + MCP wiring
- `~/.config/opencode/agents/{dark-research.md,scour.md}`
- `~/.config/opencode/prompts/dark-research-build.txt`
- All 4 artifacts aligned (drifts 171-174)

---

## 2. Waves pendientes (Wave 4+)

### Wave 4 part A — MCP server (sub-spec 158) — **PRIORIDAD 1**
Exponer los 11 orchestrators como **25 MCP tools** en `dark_memory_*` namespace.

**Scope**:
```
cmd/dark-memory-mcp/main.go          (boot)
internal/server/server.go            (registry)
internal/tools/registry.go           (Tool type)
internal/tools/wiring.go             (orchestrator → MCP adapter)
internal/tools/errors.go             (typed error → MCP response)
internal/tools/next.go               (NextAction serialisation)
cmd/dark-memory-mcp/go.mod           (separado, requires library + mcp-go)
internal/tools/{session,research,vibeflow,spec,brand,compliance,
                artifact,drift,ssd,constitution,mods,economy,
                observability,admin}.go  (11 files)
tests/e2e/server_test.go             (1000 mixed calls no deadlock)
```
- **Estimación**: 30 files
- **Depende**: spec 177 cerrado (drift_judge verde)
- **Habilita**: dark-recall v2.3 (sub-spec 160), bridge conformance (164)

### Wave 4 part B — CLI admin (sub-spec 159) — **PRIORIDAD 2**
Operador-side: migrate, vacuum, schema-status, set-driver, inspect.

**Scope**:
```
cmd/dark-memory-cli/main.go          (dispatch)
cmd/dark-memory-cli/{migrate,vacuum,schema_status,set_driver}.go
cmd/dark-memory-inspect/main.go      (read-only diagnostic)
cmd/dark-memory-cli/go.mod           (separado)
tests/cli/cli_test.go
```
- **Depende**: Wave 4 part A (CLI llama al library)
- **Sin dependencia de**: bridge conformance (puede ir en paralelo)

### Wave 4 part C — Runbooks (sub-spec 162) — **PRIORIDAD 3**
6 docs para que un operador externo pueda correr el sistema.

**Scope**:
```
docs/RUNBOOK.md          (Postgres install driver switch vacuum retention)
docs/COEXISTENCE.md      (dark-research-mcp + dark-memory-mcp story)
docs/INVARIANTS.md       (6 invariants para operadores)
docs/CONTEXT_OBJECTS.md  (shape + intent of each context type)
docs/PERFORMANCE.md      (P50 P99 targets)
docs/MIGRATION.md        (SQLite-only → Postgres paso-a-paso)
```
- **Depende**: Wave 4 part A (RUNBOOK.md referencia MCP tools)

### Wave 4 part D — Bridge conformance (spec 164) — **PRIORIDAD 4**
Hace que dark-research-mcp + dark-memory-mcp coexistan vía MCP nativo.

**Tareas pendientes** (6/7):
| ID | Tarea | Deps |
|---|---|---|
| bridge.1 | `BRIDGE_AND_COEXISTENCE.md` publicado | ✅ done |
| bridge.2 | dark-memory-mcp initialize `coexistence_group` | Wave 4A |
| bridge.3 | dark-research-mcp initialize `coexistence_group` | (edita dark-research-mcp) |
| bridge.4 | tools/list 25 orchestrators canonical order | Wave 4A |
| bridge.5 | dark-recall v2.3 prefiere `dark_memory_*` | sub-spec 160 |
| bridge.6 | Failure isolation tests | Wave 4A |
| bridge.7 | MCP Inspector conformance test | Wave 4A |

### Wave 4 part E — dark-recall v2.3 (sub-spec 160) — **FUERA DEL REPO**
Vive en el plugin dark-recall de opencode. Detecta dark-memory-mcp, llama orchestrators, falla a `dark_mem_*` con toast.

**Tareas**: 7 sub-spec descritas en spec 160.

### Wave 4 part F — Human gate (sub-spec 163) — **FINAL**
Una vez Wave 4A-E cerradas:
- `dark_ssd_drift_judge` per sub-spec 1-9
- resolve `drift_detected` verdicts
- final report RFC vs delivered
- human gate review (operator)
- tag sub-specs ready-for-publish

---

## 3. Estado actual (snapshot)

```
$ git log --oneline | wc -l
20 commits en review/w3p1 (1 ahead de main)

$ go test ./...
ok tests/context
ok tests/dual_driver          (SQLite + Postgres contract)
ok tests/economy
ok tests/invariants
ok tests/orchestration        (74 tests — 41 nuevos W3p3)
ok tests/project

$ ls internal/orchestration/
orchestrator.go         session_start.go      session_close.go
research_backend.go     research_topic.go     recall_context.go
judge.go                judge_consensus.go    ← W3p3
llm_client.go           llm_selector.go       recommended_models.go
publish_vibe.go                              ← W3p3
active_policy.go                            ← W3p3
memory_state.go                             ← W3p3
resolve_drift.go                            ← W3p3
vibe_spec.go                                 ← W3p3
```

### Artefactos pendientes de drift_judge (8)

| artifact_id | file | spec |
|---|---|---|
| 327 | publish_vibe.go | 177 |
| 328 | orchestrator_test.go | 177 |
| 329 | judge_consensus.go | 177 |
| 330 | active_policy.go | 177 |
| 331 | memory_state.go | 177 |
| 332 | resolve_drift.go | 177 |
| 333 | vibe_spec.go | 177 |
| 334 | store/store.go | 177 |

### Specs activos

| ID | Tema | Estado |
|---|---|---|
| 175 | opencode clean install | closed (4/4 aligned) |
| 176 | dark-research sin daemon | **BLOQUEADO** — bloquea drift_judge batch |
| 177 | Wave 3 part 3 orchestrators | código done, drift_judge pending |
| 178 | Master plan v1 | **NEW (este doc)** |

### Specs futuros (Wave 4+)

| ID | Tema | Prioridad |
|---|---|---|
| TBD | Wave 4A MCP server (sub-spec 158) | P1 |
| TBD | Wave 4B CLI admin (sub-spec 159) | P2 |
| TBD | Wave 4C Runbooks (sub-spec 162) | P3 |
| TBD | Wave 4D Bridge conformance (spec 164) | P4 |
| TBD | Wave 4E dark-recall v2.3 (sub-spec 160) | P5 |
| TBD | Wave 4F Human gate (sub-spec 163) | FINAL |

---

## 4. Orden de ejecución recomendado

```
NOW ─┬─→ operator: opencode-with-vault.ps1          [cierra spec 176]
     ├─→ run drift_judge batch (8 artifacts)        [cierra spec 177]
     ├─→ drift_log spec 157 re-abierto + reconciled  [cascade root completo]
     ├─→ git commit Wave 3 part 3 (review/w3p1)
     │
     ├─→ Wave 4A: MCP server (sub-spec 158)         [25 tools]
     │      ↓ habilita ↓
     ├─→ Wave 4D: Bridge conformance (spec 164)     [parcial]
     │      ↓ habilita ↓
     ├─→ Wave 4E: dark-recall v2.3 (sub-spec 160)   [fuera del repo]
     │
     ├─→ Wave 4B: CLI admin (sub-spec 159)          [paralelo a 4A]
     ├─→ Wave 4C: Runbooks (sub-spec 162)           [depende 4A]
     │
     └─→ Wave 4F: Human gate (sub-spec 163)         [FINAL — cierra RFC 153]
```

---

## 5. Reglas duras (de la constitución + este plan)

1. **Cada wave abre un spec** (`dark_research_spec_create`) ANTES de codear.
2. **Cada artefacto se loguea** (`dark_research_artifact_log`) ANTES de publicar.
3. **drift_judge ANTES de close** (`dark_memory_judge(eval_type=drift_judge)` + `dark_research_drift_log`). The historical `dark_ssd_*` namespace was deprecated in v1.4.0 and consolidated into dark-memory-mcp.
4. **bookkeeping propagation**: validation_status debe reflejar el drift_log verdict.
5. **commits por wave** (no mega-commits): cada wave = N commits con mensaje descriptivo.
6. **anti-prototipo**: las herramientas residuales (`dark_recall_*`, `dark_mem_publish_*`, `extract_api_keys`, `wrapper-mcp.ps1`, etc.) NO existen en este proyecto. Si aparecen, rechazar.
7. **LLM routing**: dark-ssd judges requieren `SDD_LLM_BASE_URL=https://api.minimax.io/anthropic` O vault cargado vía `opencode-with-vault.ps1`. Sin esto, drift_judge retorna 401.

---

## 6. Referencias

- **Constitución**: `vibe-flow/constitution/dark-memory-mcp.constitution.toml`
- **RFC maestro**: `vibe-flow/main/DARK_MEMORY_MCP_RFC.md` (32K — sub-specs 1-10 del cascade)
- **Bridge architecture**: `vibe-flow/main/BRIDGE_AND_COEXISTENCE.md` (15K — coexistence_group, versionado)
- **Testing framework**: spec 174 (4-layer pyramid, severity tiers, patterns)
- **Master plan (canónico)**: spec 178 en dark.db
- **Branch**: `main` (v1.4.2 = `bfb15e2`; v1.4.1 = `e4e5b68`; v1.4.0 = `a12b2d9`)

---

*Mantenido por `dark-research-build`. Cualquier cambio a este plan se hace vía `dark_research_spec_update(spec_id=178)`.*