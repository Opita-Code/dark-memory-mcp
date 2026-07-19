# Dark Memory MCP — Master Plan v2 (Pivoted)

> **Snapshot**: 2026-07-19 (v2.0.0-RFC pivot) · Spec `304` is the canonical pivot spec · RFC source of truth: `vibe-flow/main/ACTIVE_MEMORY_RFC.md`
>
> This document is the **roadmap vivo** under the pivoted thesis. Wave 5A is now P0 NOW; the legacy PLAN v1.4.2 ordering (4A→4D→5x) is reversed. Pre-pivot spec_ids 154, 156, 157, 171, 173, 177, 178 remain valid for historical continuity; new specs 11-17 cover the pivot work.

---

## 0. TL;DR

| Estado | Qué | Siguiente |
|---|---|---|
| ✅ **Waves 1-4 shipped** at v1.4.2 | Storage foundation, context economy, project namespace (INV-7), orchestrators O1-O12, MCP server boot (28 pasivos ready), plugin wiring | Under pivote: 28-tool pasivos se reemplazan por 12-15 gated en 4A-prime |
| 🚧 **Wave 5A — P0 NOW** | Atomic Context + Scoped Recall + Gate | Diseño en spec 304/305; primer código en t6-frame-proto |
| 🚧 **Waves 5B/5C/5D/5E — P1** | Persona + Capabilities / Delegation / Adapters / Session Lifecycle Resilience | Sub-specs 15, 16, 17 + post-RFC para 5E.i-v |
| 🚧 **Waves 4B'/4A'/4D'/4C/4F — P2-P5** | CLI, MCP server gated, Bridge conformance cx.v3, Runbooks, Human gate | Sequence governed by pivot dependencies, no por número heredado |
| 🚫 **dark-recall plugin v2.3** — CANCELLED | Absorbido por 5A nativo | No se escribe |

---

## 1. Waves entregadas (waves 1-4, v1.4.2 — frozen for historical continuity)

### Wave 1 — Storage foundation (spec 154)
- `internal/store/Store` interface + `factory.go` + sqlite + postgres impls
- `internal/migrate/sqlite/ddl.go` + `internal/migrate/postgres/ddl.go` (v1..v10)
- 7 invariants baseline: INV-1 (write_audit), INV-2 (session scoping), INV-3 (canary), INV-4 (constitution watchdog), INV-5 (cache integrity), INV-6 (mod sanitize), + INV-7 project namespace (W3p1)
- **Commit raíz**: `679a17e w3p1: project namespace (INV-7) + migration v7`

### Wave 2 — Context economy (spec 156)
- `internal/context/` (ArtifactContext, SessionContext, PolicyContext)
- `internal/economy/` (Atlan 5-bucket pipeline)
- Tests: `tests/context/`, `tests/economy/`

### Wave 3 part 1 — Project namespace + INV-7 (spec 157 / 679a17e)
- Migration v7 + project_id tagging en todas las tablas
- Remediation spec 171 (drifts 149-162)

### Wave 3 part 2 — Orchestrators O1-O5 (spec 173)
- `internal/orchestration/{session_start,session_close,research_topic,recall_context,judge}.go`
- 33 tests verdes

### Wave 3 part 3 — Orchestrators O7-O12 (spec 177)
- 6 nuevos: `publish_vibe`, `judge_consensus`, `active_policy`, `memory_state`, `resolve_drift`, `vibe_spec`
- 41 tests nuevos

### Side wave — opencode clean install (spec 175)
- `~/.config/opencode/opencode.jsonc` con default_agent=dark-research + MCP wiring
- **Nota pivote**: este side wave se reconfigura en 5D (heartbeat + recover wiring)

---

## 2. Waves pendientes — pivot-order (re-prioritización)

> Esto REEMPLAZA la priorización de v1.4.2. La justificación completa está en `ACTIVE_MEMORY_RFC.md` §6. Resumen: el cerebro activo (5A) es precondición para todo lo demás.

### 2.1 P0 NOW — Wave 5A: Atomic Context + Scoped Recall + Gate

Sub-specs (sub-spec IDs nuevos en dark.db):
- **5A.i** = sub-spec 11: Atomic frame types. `internal/atomic/{types.go, identity_frame.go, scope_frame.go, evidence_frame.go, capabilities_frame.go, drift_frame.go, persona_frame.go}` + tests.
- **5A.ii** = sub-spec 12: Scoped recall. `internal/recall/{assemble.go, cache.go, delta.go, economy.go}` + un MCP tool único `dark_memory_recall(scope={global|project|session}, since_token=nil)`.
- **5A.iii** = sub-spec 13: Schema v11 (additive). `vibe_frames` + `vibe_recall_subscriptions` + indexes. Reusa INV-1 + INV-5.
- **5A.iv** = sub-spec 14: `internal/policy/gate.go` — pre/mid/post hooks. Integration con orchestrators como wrapper.

### 2.2 P1 — Wave 5B: Persona + Capabilities (joint sub-spec 15)

- `internal/persona/{resolver.go, apply.go}`
- `internal/capabilities/{vibe_grants.go, resolver.go, manage.go}`
- Gate integration: persona shaping + capability gating
- Aplica A4 (Capability grants) + A5 (Persona)

### 2.3 P1 — Wave 5C: Delegation (sub-spec 16)

- `internal/delegation/{router.go, subagents.go, audit.go}`
- `dark_memory_delegate_intent` orchestrator
- Aplica A1 (Memory decides) en su forma más operacional: ¿el LLM hace, delega, o se rehúsa?

### 2.4 P1 — Wave 5D: Harness Adapters (sub-spec 17)

- `internal/adapters/{claudecode,cursor,vscode,mcjam}/` + opencode adapter updates
- Cada adapter: startup-`recover`, periodic-`heartbeat`, exit-`close_clean`
- Aplica A1 en harnesses no-opencode; cumple INV-9

### 2.5 P1 — Wave 5E: Session Lifecycle Resilience (sub-specs 18-22)

5 sub-specs, los más acoplados al pivote:
- **5E.i** (sub-spec 18): Schema v12 (sessions rewrite — destructive-but-rebuild). Backfill `status=open` huérfanos → `closed_aborted`.
- **5E.ii** (sub-spec 19): `_close(reason=...)` modificada + `_resurrect` + `_heartbeat` + `_recover` (3 nuevos tools).
- **5E.iii** (sub-spec 20): `internal/scope/sweeper.go` + boot reconciliation. Promueve stale `open` → `closed_aborted` cada 60s.
- **5E.iv** (sub-spec 21): Frame-aware resurrection. `_resurrect` re-deriva grants desde constitución + mods, hereda scope state del original.
- **5E.v** (sub-spec 22): L6 adapter integration — los adapters implementan las 3 hooks.

### 2.6 P2 — Wave 4B': CLI admin

- `cmd/dark-memory-cli/{migrate,vacuum,schema_status,set_driver,inspect,grants,sweeper}.go`
- Operacional; corre paralelo a 5x

### 2.7 P3 — Wave 4A': MCP server GATED (12-15 tools, no 28 pasivos)

- Cada `tools/call` envuelto en pre-hook (gate)
- Tool registry declara solo tools gated; los 28 pasivos quedan en library API
- **Cambio crítico** del v1.4.2: el `tools/list` para harnesses es 12-15 names; legacy 28 solo accesibles via dark-memory CLI

### 2.8 P3 — Wave 4D': Bridge conformance cx.v3

- `serverInfo.policy_gateway = true`
- Demote dark-research-mcp a tool backing
- Cancela dark-recall v2.3 formalmente
- cx.v3 en coexistence version table
- Opencode adapter con `coexistence_group=dark-agents/memory` (igual) + new capability advertisement
- **Bridge doc se actualiza**: `vibe-flow/main/BRIDGE_AND_COEXISTENCE.md` v2

### 2.9 P4 — Wave 4C: Runbooks (post-gateway)

- `docs/RUNBOOK.md` (driver switch, vacuum, retention, sweeper tuning, recovery procedures)
- `docs/COEXISTENCE.md` (cx.v3 + dark-research-mcp as backing + dark-recall cancelled)
- `docs/INVARIANTS.md` (9 invariantes, INV-8 + INV-9 nuevos)
- `docs/CONTEXT_OBJECTS.md` (los 6 frames — supersedes legacy single-context-object doc)
- `docs/PERFORMANCE.md` (P50/P99 per frame type + scoped recall)
- `docs/MIGRATION.md` (v1.4.2 → v2.x schema transitions for v11 + v12)

### 2.10 P5 — Wave 4F: Human gate (final)

- Drift-judge cada sub-spec 11..22
- Resolve `drift_detected`
- Final report RFC vs delivered
- Operator tag ready-for-publish

---

## 3. Estado actual (snapshot pivote, 2026-07-19)

```
$ cd dark-memory-mcp
$ git log --oneline | wc -l
20 commits en main (v1.4.2 = bfb15e2; pre-pivot)
[post-pivot] commits nuevos por wave 5A-E

$ ls internal/
atomic/                  (vacío — primera wave 5A.i)
policy/                  (vacío — primera wave 5A.iv)
recall/                  (vacío — primera wave 5A.ii)
persona/                 (vacío — primera wave 5B.i)
capabilities/            (vacío — primera wave 5B.ii)
scope/                   (vacío — primera wave 5E.iii)
drift/                   (vacío — primera wave 5A.vi/M6)
delegation/              (vacío — primera wave 5C)
adapters/{claudecode,cursor,vscode,mcjam}/  (vacío — primera wave 5D)
vibeflow/  vibecase/  vlp/  orchestration/  store/  safety/  ...  (pre-pivot, frozen at v1.4.2)
```

### Specs persistidos (pivote + recientes)

| spec_id | tema | status |
|---|---|---|
| 304 | vibe-flow-pivot C2 spec | root spec, abierto |
| 305 | ACTIVE_MEMORY_RFC.md (RFC artifact) | drift_log 373→374 (accepted), spec pasa |
| (futuros) | 5A.i, 5A.ii, 5A.iii, 5A.iv, 5B, 5C, 5D, 5E.i-v | a crear con cada wave |
| (legacy frozen) | 154, 156, 157, 171, 173, 175, 177, 178 | cerrados en histórico; algunos artefactos (spec 177 los 8) re-drift-judge con criterios pivote |

### Drifts pendientes de resolver a nivel pivote

| drift_id | tema | status |
|---|---|---|
| 373 | RFC v1 (art 490) | resolved (374=accept) |
| 372 | root spec artifact | skipped (creation only) |
| (futuros) | cada wave tendrá su drift_log; algunos serán drift_detected por el bug parseDriftVerdict y serán resueltos con drift_resolve(accept) cuando el LLM subyacente esté aligned |

### Infra tech-debt (no del pivote, surface en t7-drift-burst)

- `internal/orchestration/publish_vibe.go:parseDriftVerdict` solo reconoce `{"aligned":bool}` legacy. Drift-judge actual devuelve `{"verdict":"aligned"|"drift_detected"}`. Resultado: toda `vibe_publish(auto_drift_check=true)` retorna drift_detected aunque el judge subyacente diga aligned. Filed as INFRA-001. Patch de ~5 líneas; rebuild + restart MCP.
- **Workaround actual**: `vibe_publish(auto_drift_check=false)` + `dark_memory_judge` directo + `dark_memory_resolve_drift(decision=accept)` cuando corresponda. Cada resolución cita el INFRA-001 en el note.

### INFRA-003 — RESOLVED via INV-10 (Wave INFRA-003 shipped 2026-07-19)

Corporate Windows hosts with WDAC + Carbon Black quarantine freshly-built
Go test binaries. The workaround (build+vet+drift-judge, CI for runtime)
is now constitutionalized as **INV-10** in `[operator-private]@1.1.0`.
See:
- the operator-private constitution (`vibe-flow/constitution/[operator-private].constitution.toml`) — operational_rules.10
- `docs/INFRA-003.md` — operator-facing explanation
- `tests/README.md` — developer-facing test workflow

When the policy changes (e.g., WDAC moves to audit-only, Carbon Black
replaced), INV-10 can be retired and the workflow reverts to local
`go test ./...` plus CI.

---

## 4. Orden de ejecución — pivote

```
NOW ─┬─→ 5A.i: atomic frame types + identity_frame proto    [P0, primero]
     │       ↓ tests verdes
     ├─→ 5A.iii: schema v11 (additive)                       [P0, depende 5A.i shape]
     │       ↓ migrations applied
     ├─→ 5A.ii: scoped recall orchestrators + cache + delta  [P0, depende 5A.i + 5A.iii]
     │       ↓ assemble/cache/delta integration
     ├─→ 5A.iv: policy/gate.go interceptor                   [P0, depende 5A.i + 5A.ii]
     │       ↓ gate wraps orchestrators in pre/post hooks
     │
     ├─→ 5B: persona + capabilities                          [P1, depende 5A.iv]
     │       ↓ grants model + persona resolution
     ├─→ 5C: delegation                                     [P1, depende 5A.iv + 5B]
     │       ↓ decide-handle-refuse routing
     ├─→ 5D: adapters                                       [P1, depende 5B + 5E.ii]
     │       ↓ opencode glue + claudecode + cursor
     │
     ├─→ 5E.i: schema v12 sessions rewrite                   [P1, depende 5A.iii knowledge]
     │       ↓ destructive migrate + backfill
     ├─→ 5E.ii: _close/_resurrect/_heartbeat/_recover        [P1, depende 5E.i]
     │       ↓ 3 nuevos tools, 1 modificado
     ├─→ 5E.iii: scope/sweeper + boot reconcile              [P1, depende 5E.ii]
     │       ↓ sweeper goroutine + boot step
     ├─→ 5E.iv: frame-aware resurrection                     [P1, depende 5A.ii + 5E.ii]
     │       ↓ grant re-derivation + scope inheritance
     ├─→ 5E.v: adapter integration                           [P1, depende 5D + 5E.ii]
     │       ↓ startup-`recover`, periodic-`heartbeat`, exit-`close_clean`
     │
     ├─→ 4B': CLI admin (parallel a 5x)                     [P2]
     ├─→ 4A': MCP server GATED (12-15 tools)                [P3, depende 5A.iv + 5B]
     ├─→ 4D': Bridge conformance cx.v3                      [P3, depende 5D ready]
     │
     ├─→ 4C: Runbooks (post-gateway)                        [P4]
     │
     └─→ 4F: Human gate final                                [P5, cierra RFC]
```

Note the inversion: previously Wave 4A was P1 and Wave 5x was P5+. Now 5A is P0 NOW and 4A becomes P3 (gated variant).

---

## 5. Reglas duras (de la constitución + este plan + el pivote)

1. **Cada wave abre un spec** en dark.db ANTES de codear.
2. **Cada artefacto se loguea** via `vibe_publish` o `dark_memory_research_topic` (research intents) ANTES de considerar publicable.
3. **drift-judge ANTES de close** — el verdict del LLM judge debe estar aligned, confidence > 0.5 (UMBRAL) antes de marcar spec como cerrado.
4. **bookkeeping propagation**: validation_status debe reflejar el drift_log verdict (con el workaround INFRA-001 actual: si el wrapper retorna drift_detected, el operador debe resolver con accept citando la evidence).
5. **commits por wave** (no mega-commits): cada wave = N commits con mensaje descriptivo.
6. **anti-prototipo**: las herramientas residuales (`dark_recall_*`, `dark_mem_*` legacy shim, `extract_api_keys`, `wrapper-mcp.ps1`, etc.) NO existen en el código pivote. Si aparecen, rechazar.
7. **LLM routing**: `dark_memory_judge` requiere `SDD_LLM_BASE_URL=https://api.minimax.io/anthropic` + `ANTHROPIC_API_KEY` propagada al MCP subprocess. Sin esto, drift_judge retorna 401.
8. **pivot invariables**: INV-8 (Resilience) y INV-9 (Heartbeat) son mandatorios en wave 5E; la constitución pivote los codifica.
9. **dark-recall v2.3 cancelled**: cualquier referencia a `dark-recall` plugin v2.3+ es rechazada. Si reaparece, etiquetar como drift a resolver.
10. **cx.v3 ≠ cx.v2**: a partir de 4D', la coexistencia requiere `policy_gateway=true` en dark-memory. Cargas de harness que reporten `policy_gateway=false` son legacy — `dark-research-mcp` debe ser actualizado.

---

## 6. Referencias

- **Constitución pivote**: the operator-private constitution (`vibe-flow/constitution/[operator-private].constitution.toml`, created by t4-constitution of spec 304)
- **RFC pivote**: `vibe-flow/main/ACTIVE_MEMORY_RFC.md` (creado por t1-rfc) — A1-A7, M1-M8, 5 estados, INV-8/9, wave plan
- **Bridge conformance v2**: `vibe-flow/main/BRIDGE_AND_COEXISTENCE.md` v2 (cx.v3, policy_gateway, dark-research-mcp demoted, dark-recall cancelled)
- **Schema design**: `vibe-flow/main/SCHEMA_v11_v12.md` (a crear por t5-schema)
- **PLAN legacy archived**: `vibe-flow/PLAN.md` v1.4.2 archived at this pivot; v2 es el nuevo source of truth
- **RFC legacy archived**: `vibe-flow/main/DARK_MEMORY_MCP_RFC.md` v1.0.0 archived (P1-P5 retenidos como read-only-consumer view; superseded en active-memory por A1-A7)
- **Branch**: `main` (v1.4.2 frozen; v2.0.0-RFC en branch `pivot/v2`)

---

*Mantenido por dark-agent. Cambios a este plan via `dark_memory_research_topic` (intento arquitectural) o nueva spec en dark.db.*
