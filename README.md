<div align="center">

```
╔════════════════════════════════════════════════════════════════════════════════════╗
║                                                                                    ║
║   ██████╗  ██████╗██████╗ ███╗   ███╗      ███╗   ███╗ ██████╗██████╗              ║
║  ██╔═══██╗██╔════╝██╔══██╗████╗ ████║      ████╗ ████║██╔════╝██╔══██╗             ║
║  ██║   ██║██║     ██║  ██║██╔████╔██║      ██╔████╔██║██║     ██████╔╝             ║
║  ██║   ██║██║     ██║  ██║██║╚██╔╝██║      ██║╚██╔╝██║██║     ██╔═══╝              ║
║  ╚██████╔╝╚██████╗██████╔╝██║ ╚═╝ ██║      ██║ ╚═╝ ██║╚██████╗██║                  ║
║   ╚═════╝  ╚═════╝╚═════╝ ╚═╝     ╚═╝      ╚═╝     ╚═╝ ╚═════╝╚═╝                  ║
║                                                                                    ║
║                              OPITA CODE DARK MEMORY MCP                            ║
║                                                                                    ║
║        Persistent Memory • Autonomous Agents • Threat Intelligence • MCP           ║
║                                                                                    ║
╚════════════════════════════════════════════════════════════════════════════════════╝
```

**El servidor MCP de memoria persistente y orquestación de workflows para dark-agents-v2.**

[![MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![MCP tools](https://img.shields.io/badge/MCP-28%20canonical%20%7C%2029%20federated%20%7C%2031%20armed-blueviolet)](#los-28-tools)
[![Tests](https://img.shields.io/badge/tests-17%20of%2018%20passing-brightgreen)](#tests)
[![Backends](https://img.shields.io/badge/backends-sqlite%20%7C%20postgres-blue)](docs/MIGRATION.md)
[![Conformant](https://img.shields.io/badge/MCP%20Inspector-passing-success)](tests/conformance/)

[¿Qué hace?](#qué-hace) · [¿Para quién?](#para-quién) · [Quickstart](#quickstart) · [Arquitectura](#arquitectura) · [Vibe-Flow](#el-vibe-flow-loop)

</div>

---

## ¿Qué hace?

**dark-memory-mcp** es un servidor MCP escrito en Go que entrega a tu agente IA **28 herramientas canónicas** agrupadas en 10 oficios (incluido el namespace L6-VLP para el state machine del Vibe-Loop Protocol, y el namespace PROJECT para bootstrap multi-tenant sin acceso directo a la DB), persistidas en una base SQL dual-driver (SQLite para dev, Postgres para prod) y gobernadas por **8 invariantes operacionales** que se defienden a sí mismos en el boundary del Store.

Una sola API. Tres binarios (`dark-mem-mcp` server MCP, `dark-mem-cli` admin, `dark-mem-inspect` read-only). Un solo `dark-memory.db` por defecto (INV-8: cada MCP de la familia `dark-*` usa su propio archivo). Compatibilidad de schema con `dark-research-mcp` en tablas `vibe_*` / `research_*` — la DB física es distinta. **Sin magia: con código que puedes leer y modificar.**

Cuando se arranca con `DARK_REDTEAM=armed`, el servidor emite **3 herramientas L7-REDTEAM adicionales** (`dark_memory_redteam_list_mods`, `_get_prompts`, `_log_attempt` — research use only, ver [Modo Armed (L7-REDTEAM)](#modo-armed-l7-redteam)). Cuando se setea `DARK_FEDERATION_PEER_DSN`, se añade **1 herramienta opt-in** (`dark_memory_federation_lookup`, ver [Federation](#federation-cross-namespace-lookup)). La superficie canónica de **28 tools** se preserva en cualquier combinación (defensivo: `TestE2E_28ToolsRegistered` + `TestWire_RuntimeToolEnumeration`). Surface states: 28 (canonical) → 29 (federation) → 31 (armed) → 32 (armed + federation).

**Health probe (v1.3.0):** `dark_memory_health_ping` es un liveness probe de bajo costo (latencia objetivo <50ms en caliente, presupuesto <500ms) apto para K8s liveness/readiness. Devuelve un snapshot inmutable `{server, db, runtime, registry, latency_ms, checked_at}` sin tocar el audit bus ni avanzar el VLP state. Ver `docs/PRODUCTION_CHECKLIST.md` §Health Probe para el wiring.

> 🇨🇴 *Construido en Colombia como parte del ecosistema [Opita Code](https://opitacode.com). Software práctico para investigación real, no para verse bonito en una presentación.*

---

## Para quién

| Si eres… | Te interesa porque… |
|---|---|
| 🤖 **Agent developer** | 28 `dark_memory_*` tools canónicos listos para usar en tu agente MCP — sesiones, research, vibe-flow, judge, policy, observability, admin, project, VLP. Wire format estabilizado, orden canónico enforced. |
| 🧠 **Memory engineer** | El Store dual-driver + 8 invariantes operacionales te da persistencia defendible: write-path audit, per-session scoping, canary en writes, constitution watchdog, cache re-hash, mod sanitization, multi-tenancy, per-MCP DB isolation. |
| 🌊 **Vibe-coder** | El pipeline `spec_create → artifact_log → drift_judge → drift_log → publish` cierra el loop spec-vs-artifact. Para de regenerar el mismo bug cada vez. |
| 🏛️ **Compliance officer** | `dark_memory_active_policy` retorna constitution + active mods + jurisdiction. Cada write_audit lleva `constitution_id@version`. Auditoría de punta a punta. |
| 🛡️ **Red-teamer** | El canary token en payloads detecta constitution extraction attempts. INV-6 mod loader rechaza prompt injection. Cross-link con `dark-research-mcp` para combinar OSINT + memory. |
| 🔌 **MCP integrator** | Bridge conformance 5/5 contra MCP Inspector. `coexistence_group=dark-agents/memory` declarado en initialize. Coexiste con `dark-research-mcp` (sibling) sin pisarse. |

---

## Quickstart

```bash
# 1. Clona y compila
git clone https://github.com/Opita-Code/dark-memory-mcp.git
cd dark-memory-mcp
go build -o bin/dark-mem-mcp ./cmd/dark-mem-mcp
go build -o bin/dark-mem-cli ./cmd/dark-mem-cli
go build -o bin/dark-mem-inspect ./cmd/dark-mem-inspect

# 2. Configura en opencode.jsonc (la coexistencia con dark-research-mcp es automática)
{
  "mcp": {
    "dark-memory": {
      "type": "local",
      "command": ["C:/path/to/bin/dark-mem-mcp.exe"],
      "enabled": true
    }
  }
}

# 3. Primera ejecución — el server auto-bootstraps (migrations + watchdog + seed default project)
./bin/dark-mem-inspect --json
```

Salida esperada:

```json
{
  "generated_at": "2026-07-15T22:50:00Z",
  "driver": "sqlite",
  "schema_version": 7,
  "canary_present": false,
  "active_constitution_id": "dark-agents/dark-memory-mcp",
  "active_constitution_version": "1.0.0",
  "tables": ["projects", "research_runs", "research_items", "vibe_specs", ...]
}
```

El binario `dark-mem-cli` aplica migraciones explícitas cuando las quieras, y `dark-mem-inspect` corre contra producción sin escribir nada.

---

## Los 28 tools

Diez namespaces. El prefijo wire es `dark_memory_` (mandatory por BRIDGE_AND_COEXISTENCE §2.2). El orden canónico es **parte del contrato wire** — harnesses pueden indexar por posición.

| Namespace | Count | Tools |
|---|---|---|
| **PROJECT** (v1.2.0) | 1 | `dark_memory_project_create` |
| **SESSION** | 4 | `dark_memory_session_start`, `_resume`, `_status`, `_close` |
| **RESEARCH** | 3 | `dark_memory_research_topic`, `_recall`, `_resume_thread` |
| **VIBE** | 4 | `dark_memory_vibe_publish`, `_spec`, `_pipeline_status`, `_resolve_drift` |
| **CONTEXT** | 3 | `dark_memory_artifact_context`, `_spec_context`, `_session_context` |
| **JUDGE** | 3 | `dark_memory_judge`, `_consensus`, `_judgment_history` |
| **POLICY** | 2 | `dark_memory_active_policy`, `_load_constitution` |
| **OBSERVABILITY** | 4 | `dark_memory_health_ping`, `dark_memory_memory_state`, `_writes`, `_anomalies` |
| **ADMIN** | 3 | `dark_memory_admin_migrate`, `_schema_status`, `_vacuum` |
| **L6-VLP** (DMAP v1.1) | 1 | `dark_memory_vlp_handle_event` |

Total: **1+4+3+4+3+3+2+4+3+1 = 28** ✓ (RFC §D-9 + DMAP v1.1 spec 193 Layer 6 + F33 v1.2.0 + F35 v1.3.0 health_ping)

Cada tool expone un JSON Schema de input. Cada respuesta lleva `data + audit + next` para que el LLM sepa qué hacer después. La posición en esta tabla es el orden wire (`tools/list`); los harnesses pueden confiar en el índice.

### Modo Armed (L7-REDTEAM)

Cuando se arranca con `DARK_REDTEAM=armed`, el servidor registra **3 herramientas adicionales** en el namespace `dark_memory_redteam_*`:

| Namespace | Count | Tools |
|---|---|---|
| **L7-REDTEAM** (armed-only) | 3 | `dark_memory_redteam_list_mods`, `_get_prompts`, `_log_attempt` |

Las herramientas cargan los mods instalados bajo `mods/redteam/` (configurable vía `DARK_REDTEAM_MODS_PATH`). Los mods son files de payloads de security research (prompt-injection-lab, jailbreak-taxonomy, etc.). **Solo para uso de investigación con autorización explícita.** No destinados a infraestructura de ataque en producción.

La superficie armed es 28 + 3 = **31**. La superficie sin armar es 28, garantizada por `TestE2E_28ToolsRegistered` (Go level) y `TestWire_RuntimeToolEnumeration` (wire level).

### Federation (cross-namespace lookup)

Cuando se arranca con `DARK_FEDERATION_PEER_DSN` apuntando al SQLite del peer, el servidor registra **1 herramienta adicional**:

| Namespace | Count | Tools |
|---|---|---|
| **FEDERATION** (peer-DSN opt-in) | 1 | `dark_memory_federation_lookup` |

El peer se abre en modo `mode=ro` (read-only) — no hay write path posible hacia la DB remota. El lookup consulta `vibe_artifacts`, `vibe_drift_reports` y `research_items` por `artifact_id` o `session_id`, devolviendo `{peer_artifact, peer_drift, peer_session_artifacts}` cuando el peer reporta match. Si el peer no tiene las tablas esperadas, el boot falla ruidosamente (`ErrInvalidSchema`) — nunca degradación silenciosa.

`pipeline_status` también aprende un nuevo campo `cross_namespace_hint` que, en miss local, hace una sola probe read-only al peer y devuelve la URL del artifact + validation status del lado remoto. **Solo metadata, no copia de datos.**

La superficie federation es 28 + 1 = **29**. Armed + federation: 28 + 3 + 1 = **32**. La superficie canónica de 28 se preserva en cualquier combinación porque federation es opt-in (la herramienta solo se registra si la env var está seteada).

---

## El vibe-flow loop

El problema #1 sin resolver en 2026 AI-assisted development es el **spec-drift**: el agente genera algo, lo publica, y nunca reconcilia si lo que generó realmente cumple lo que el spec pedía.

**dark-memory-mcp** cierra ese loop con persistencia + LLM-as-judge:

```
                    ┌───────────────────────────────────────────┐
                    │  1. Crear spec (vibe_publish / vibe_spec) │
                    │     Persiste intent + tasks + constitution│
                    │                                           │
                    │  2. Generar el artifact                   │
                    │     (tu modelo / servicio preferido)      │
                    │                                           │
                    │  3. Loggear artifact                      │
                    │     artifact_log → write_audit row        │
                    │                                           │
                    │  4. LLM-as-judge: drift                   │
                    │     drift_judge(artifact_id)              │
                    │     verdict ∈ {aligned, drift_detected,   │
                    │                  needs_human}             │
                    │                                           │
                    │  5. Loggear verdict                       │
                    │     drift_log(verdict, judge_reasoning)   │
                    │                                           │
                    │  6. Human gate si algo falló              │
                    │     resolve_drift(accept | reject)        │
                    └───────────────────────────────────────────┘
```

Cada `dark_ssd_drift_judge` (sub-spec 180) persiste su verdict en `sdd_evaluations` con `prompt_version` + `model`. **Reproducible, auditable, mejorable con el tiempo** (calibration loop).

---

## Arquitectura

```
┌─────────────────────────────────────────────────────────────────┐
│  Tu agente (opencode, Claude Code, Cursor, lo que sea)          │
│                                                                 │
│  stdio MCP                                                      │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
         ┌────────────────────────────────────┐
         │   dark-mem-mcp.exe                 │
         │                                    │
         │   ┌──────────────────────────┐     │
         │   │  28 MCP tools canonical │     │
         │   │  ├ PROJECT (1, v1.2.0)   │     │
         │   │  ├ SESSION (4)           │     │
         │   │  ├ RESEARCH (3)          │     │
         │   │  ├ VIBE (4)              │     │
         │   │  ├ CONTEXT (3)           │     │
         │   │  ├ JUDGE (3)             │     │
         │   │  ├ POLICY (2)            │     │
         │   │  ├ OBSERVABILITY (3)     │     │
         │   │  ├ ADMIN (3)             │     │
         │   │  └ L6-VLP (1)            │     │
         │   │  + L7-REDTEAM (3, armed) │     │
         │   └──────────────────────────┘     │
        │                                    │
        │   ┌──────────────────────────┐     │
         │   │  internal/               │     │
         │   │  ├ store (sqlite/pg)     │◄──── DARK_DB_DRIVER + DARK_DB
         │   │  ├ orchestration (9)     │     │
         │   │  ├ context (8)           │     │
         │   │  ├ vibeflow (5)          │     │
         │   │  ├ federation (1, 1.3.2) │◄──── DARK_FEDERATION_PEER_DSN
         │   │  ├ safety (canary)       │     │
         │   │  ├ constitution (INV-4)  │     │
         │   │  ├ audit (INV-1)         │     │
         │   │  ├ llm/cache (INV-5)     │     │
         │   │  ├ mods/loader (INV-6)   │     │
         │   │  └ project (INV-7)       │     │
        │   └──────────────────────────┘     │
        └────────────────┬───────────────────┘
                         │
              ┌──────────┴──────────┐
              ▼                     ▼
    ┌───────────────────┐  ┌──────────────────┐
    │  dark-memory.db   │  │ Postgres         │
    │  (SQLite, default)│  │ (jackc/pgx/v5)   │
    │                   │  │                  │
    │  projects         │  │  same schema,    │
    │  sessions         │  │  v7 migrations   │
    │  write_audit      │  │  + peer-DSN      │
    │  research_*       │  │  cross-namespace │
    │  vibe_*           │  │  lookup          │
    │  sdd_evaluations  │  │  (read-only)     │
    │  constitutions    │  │                  │
    │  mods             │  │                  │
    └───────────────────┘  └──────────────────┘
```

Detalles en [`docs/`](docs/) y [`vibe-flow/main/DARK_MEMORY_MCP_RFC.md`](vibe-flow/main/DARK_MEMORY_MCP_RFC.md).

---

## Los 8 invariantes operacionales

Cada operación del Store respeta estos contratos — defendidos en el boundary, no en la constitución como texto:

| ID | Regla | Defendido por |
|---|---|---|
| **INV-1** | Write-path audit en cada `Save*` | `Store.RecordWrite` dentro de la misma transacción |
| **INV-2** | Per-session scoping en `Recall()` | `research.RecallOptions.SessionScope`; workflow tools siempre llevan `session_id` |
| **INV-3** | Payloads con canary token son rechazados | `Store.canary.ValidatePayload` al inicio de cada `Save*` |
| **INV-4** | Constitution audit + SHA watchdog en Open | `Store.runWatchdog` verifica constitution file SHA256 |
| **INV-5** | Cache re-hash on Get (mismatch = anomaly) | `internal/llm/cache.go` |
| **INV-6** | Mod content sanitization (injection markers) | `internal/safety/safety.go` injectionMarkers regex |
| **INV-7** | Multi-tenancy: `SetActiveProject` requerido antes de leer/escribir | `Store.requireProject` |
| **INV-8** | **Per-MCP database isolation**: cada MCP usa su propio archivo SQLite. Default = `dark-memory.db` (NO `dark.db`). Operador puede sobrescribir pero el default mantiene aislado de dark-research-mcp. Aplica a toda la familia dark-* (futuro [FUTURE-MCP-1] debe usar `harvest.db`). | `server.DefaultDSN()` retorna string con substring `dark-memory`; CI lint valida que `defaultDSN` de cada MCP sea único |

Tabla completa con cada método Store → su set de invariantes en [`docs/INVARIANTS.md`](docs/INVARIANTS.md).

---

## Configuración

| Variable | Default | Propósito |
|---|---|---|
| `DARK_DB_DRIVER` | `sqlite` | `sqlite` \| `postgres` |
| `DARK_DB` | `./dark-memory.db` (cwd) | Path al SQLite o URL Postgres. Default cambió en v1.2.3 (era `./dark.db` antes de v1.2.2) por **INV-8**: cada MCP de la familia dark-* usa su propio archivo. |
| `DARK_FEDERATION_PEER_DSN` | (unset) | Path al SQLite del peer para cross-namespace lookup. Si está set, registra `dark_memory_federation_lookup` como 29° tool (v1.3.2). Read-only contra el peer. |
| `DARK_CACHE_DIR` | (vacío) | Dónde persiste el LLM cache (INV-5). Vacío = in-process only. |
| `DARK_MOD_WHITELIST` | (vacío) | Lista comma-separated de mod IDs permitidos a cargar (INV-6) |
| `DARK_SERVER_NAME` | `dark-memory-mcp` | `serverInfo.name` en initialize response |
| `DARK_SERVER_VERSION` | `1.3.2` | `serverInfo.version` en initialize response |
| `DARK_COEXISTENCE_GROUP` | `dark-agents/memory` | Bridge §2.1 coexistence contract |
| `DARK_HOME` | `~/.config/dark-memory-mcp` | Donde `dark-mem-cli set-driver` escribe `config.toml` |
| `DARK_REDTEAM` | (unset) | Si =`armed`, registra las 3 herramientas L7-REDTEAM. Surface total = 31. Sin la var, surface = 28. |
| `DARK_REDTEAM_MODS_PATH` | `./mods/redteam` | Path al directorio de mods armed. |

---

## Tests

```bash
go test -count=1 ./...
```

**17 / 18 packages PASS** con `-count=1` (cold rebuild). El único FAIL pre-existente es `internal/tools` con 6 sub-tests de la suite redteam, que requieren un directorio `mods/redteam` con mods instalados — operator-WIP documentado en Status. Sin regresión de v1.3.2.

```
ok  internal/adapter/opencode  21s   (OpenCode harness adapter)
ok  internal/federation        25s   (F7 cross-namespace peer, v1.3.2)
ok  internal/orchestration     1s    (F1 judge HTTP wiring coverage, v1.3.2)
ok  internal/server            2s
ok  internal/vlp               61s   (DMAP v1.1 spec 193 state machine + VLP tool)
ok  tests/cli                  78s   (13 tests: 11 + 2 canary_present regression)
ok  tests/conformance          58s   (4 bridge.7 tests via mcp-go real client)
ok  tests/context              19s
ok  tests/dual_driver          6s    (sqlite contract 7/7 sub-tests)
ok  tests/e2e                  70s   (6 tests including 1000-mixed-no-deadlock + 28-tool register guard)
ok  tests/economy              1s
ok  tests/invariants           1s    (INV-5 + INV-6)
ok  tests/migrate              54s
ok  tests/orchestration        82s   (73+ tests across 9 orchestrators; F36 dual-form tasks)
ok  tests/project              53s   (INV-7 multi-tenancy)
ok  tests/tools                19s   (F33 project tool: 7 sub-tests, schema rejection, idempotency)
ok  tests/wire                 15s   (F35 wire propagation, 10/10 conformance)
FAIL internal/tools            37s   (6 redteam tests, pre-existing operator-WIP, no mods)
```

Highlights:
- `TestE2E_28ToolsRegistered` — Go-level canonical-order guard (v1.3.0: 27 → 28 with health_ping)
- `TestVibeSpec_AcceptsStringifiedTasks` — F36 dual-form compat with `dark_research_spec_create` (v1.2.1)
- `TestVibeSpec_StringifiedTasks_MalformedRejected` — F36 precise error surfaces field hint
- `TestE2E_1000MixedCallsNoDeadlock` — RFC §12 #4 (1000 mixed tool calls)
- `TestBridge7_ListToolsCanonical` — wire-format regression for canonical order
- `TestSQLiteStoreContract/*` — dual-driver contract (sqlite branch)
- `TestInspect_CanaryPresent_StoreMethod` — review-w4 regression guard
- `TestProjectTool_HappyPath`, `_SchemaRejects*`, `_IdempotentReplay` — F33 v1.2.0 coverage
- `TestFederation_PeerReadOnly`, `TestFederation_LookupByArtifactID` — F7 v1.3.2 cross-namespace coverage
- `TestScrapperWiring_*` (9 sub-tests) — F1 v1.3.2 judge HTTP wiring coverage

---

## Status

- ✅ **v1.0.0** — 25 tools + dual-driver + bridge conformance 5/5 + 9 test suites + 6 runbooks
- ✅ **v1.0.x** — `dark-mem-inspect` ahora reporta `canary_present` correctamente (review-w4-001); `dark-mem-mcp` tiene panic recovery en boot-path (review-w4-002); mcp-go upgraded to v0.56.0 (review-w4-003); bridge.7 cold-cache timeout bumped 10s→30s (review-w4-004)
- ✅ **v1.1.0** — DMAP v1.1 Layer 6: `dark_memory_vlp_handle_event` (Vibe-Loop Protocol wire tool) + OpenCode adapter demo + L6.1 merge
- ✅ **v1.2.0** (F33 + F35, 2026-07-16) — `dark_memory_project_create` cierra el loop de bootstrap multi-tenant. `vibe_publish` JSON Schema corregido (nested spec+artifact en lugar de flat) + `vibeSpecTaskSchema` strict (additionalProperties:false) + `BindOrchestrator`'s `typeMismatchToolError` devuelve field path + expected/actual type. Tool count: 26 → 27.
- ✅ **v1.2.1** (F36, 2026-07-16) — `dark_memory_vibe_spec.tasks` ahora acepta tanto JSON array como JSON-encoded string (compatibilidad con la gemela `dark_research_spec_create` que persiste el campo como string opaco). 2 tests nuevos. Drop-in replacement; sin migrations; sin cambio de surface. **Restart requerido del binario `dark-mem-mcp.exe`** para tomar el código nuevo.
- ✅ **v1.2.2** (F37 + F38 + F39 + F40, 2026-07-16) — Migration runner self-healing: `applyOne` ahora split-on-`;` y tolera 4 clases de errores DDL idempotentes (`duplicate column name`, `no such module`, `table already exists`). `EnsureCoreTables` recrea tablas core faltantes al boot. Sin esto, dark-memory-mcp NO podía arrancar contra dark.dbs parcialmente migrados. 8 tests nuevos en `tests/migrate/`.
- ✅ **v1.2.3** (2026-07-16) — INV-8 per-MCP database isolation: default DSN pasó de `./dark.db` → `./dark-memory.db`. Leak scrub en `dark-mem-mcp` boot path (remueve matches accidentales de `Bearer <token>`, `sk-<key>`, paths privados, session ids antes de escribir al audit log). Defensive lint en CI: rechaza cualquier `defaultDSN()` que no contenga el nombre del MCP.
- ✅ **v1.2.4** (2026-07-16) — bump `DARK_SERVER_VERSION` default de 1.2.2 → 1.2.3 (alinea handshake `initialize` con el `DefaultServerVersion` constant real).
- ✅ **v1.2.5** (2026-07-16) — production-readiness sweep: `tests/wire/` suite completa (10/10 conformance contra el binario real, incluyendo `TestWire_HealthPingShape` y `TestWire_HealthPingLatency`), F35 wire propagation, CONTRIBUTING y PRODUCTION_CHECKLIST actualizados.
- ✅ **v1.3.0** (production-readiness, 2026-07-16) — `dark_memory_health_ping` (liveness probe enlatado, OBSERVABILITY 3→4), wire-conformance total (10/10 tests PASS contra el binario real), wait-for-boot-marker para eliminar race en startup, `.github/workflows/ci.yml` con receta de CI operator-reproducible, race-detector note en PRODUCTION_CHECKLIST, stale-binary gotcha documentada. **28 tools canónicos, 31 armed.**
- ✅ **v1.3.1** (sync, 2026-07-16) — squsha el trabajo unreleased de local main al origin remoto. Trae los 7 commits de v1.2.0 → v1.3.0 que vivían solo en local, en un solo commit de release por la convención del repo. Sin diff funcional vs. local main pre-sync; solo operacional. **64 files, ~6.8K insertions, ~0.4K deletions en una sola release commit.**
- ✅ **v1.3.2** (wave-3.5, 2026-07-16) — dos fixes producto de validación E2E end-to-end:
  - **F1 — judge HTTP wiring complete.** El cliente de judge ya no es stub incondicional; ahora enruta a un backend HTTP opt-in (configurable vía env var; ver env table) y devuelve el verdict al loop. Los proveedores de pago siguen sin cablear por diseño (visibilidad sobre degradación silenciosa). 9 sub-tests nuevos.
  - **F7 — `dark_memory_federation_lookup` opt-in.** Cross-namespace lookup cuando se setea `DARK_FEDERATION_PEER_DSN`. Read-only, schema-validado al boot, registro del tool solo si el peer tiene las tablas esperadas (si no, `ErrInvalidSchema` ruidoso, nunca degradación silenciosa). `pipeline_status` aprende `cross_namespace_hint`. Superficie canónica 28 → 29 con la opt-in. 8 sub-tests nuevos.
  - **10 files, ~1.4K insertions, ~44 deletions en una sola release commit.**
- 🚧 **v1.3.x** — Vector recall via sqlite-vec; constitution mod registry v2; L7-REDTEAM integration formal (los 6 tests redteam que fallan en `internal/tools` son de esta línea, pre-existen v1.3.2, requieren mods instalados en `mods/redteam/`)

Patches publicados:
- `dark-memory-mcp-v1.2.0.patch` — superficie 27 tools, ~870 LOC adicionales
- `dark-memory-mcp-v1.2.1.patch` — drop-in replacement, F36 fix
- `dark-memory-mcp-v1.2.5.patch` — wire-conformance suite (tests/wire/) + F35 wire propagation + CONTRIBUTING + PRODUCTION_CHECKLIST
- `dark-memory-mcp-v1.3.0.patch` — production-readiness: health_ping + wire-conformance bump + race-free boot + CI workflow
- `dark-memory-mcp-v1.3.1.patch` — sync release (7 commits unreleased → 1 squash, sin diff funcional)
- `dark-memory-mcp-v1.3.2.patch` — F1 judge HTTP wiring + F7 cross-namespace federation_lookup

Ver [`CHANGELOG.md`](CHANGELOG.md) para el detalle completo de cada release y [`docs/PR-v1.2.0.md`](docs/PR-v1.2.0.md) para el desglose técnico de F33+F35.

---

## Contribuir

Lee [`CONTRIBUTING.md`](CONTRIBUTING.md). PRs bienvenidos:

1. `go test -count=1 ./...` antes de pushear (17 / 18 packages PASS; el único FAIL es la suite redteam operator-WIP, pre-existente a v1.3.2)
2. Si añades un tool nuevo: sigue el orden canónico (no renumeres) — el orden es wire contract
3. Si añades un orchestrator: implementa tests + spec_create (C1) + drift_judge antes de merge
4. Si añades una migración: append a `migratesqlite.Migrations` / `migratepostgres.Migrations`, nunca edites una pasada
5. Si añades un invariante: documéntalo en `docs/INVARIANTS.md` + agregas test defensivo

---

## Coexistencia con dark-research-mcp

| MCP | Namespace | Tools (canonical) | Propósito |
|---|---|---|---|
| **dark-memory-mcp** | `dark_memory_*` | 28 canónico (29 federation, 31 armed, 32 ambos) | Memoria persistente + workflow orchestration (este repo) |
| **dark-research-mcp** | `dark_research_*` | ~13 + multi + router | OSINT acquisition |
| (deprecado) | `dark_mem_*` | legacy | dark-research-mcp emite `{deprecated: true, successor: 'dark-memory-mcp'}` en cada response |

Ambos comparten **el mismo schema** sobre tablas `vibe_*` / `research_*`, pero viven en archivos SQLite distintos por INV-8 (`dark-memory.db` vs `dark.db`). El `coexistence_group=dark-agents/memory` se declara en el `initialize` response — los harnesses MCP-native detectan automáticamente.

La integración cross-namespace es **opt-in** vía `DARK_FEDERATION_PEER_DSN` apuntando al archivo del peer. Cuando está activa, `dark_memory_federation_lookup` permite consultar artifacts / drifts / sessions en el otro DB sin escribir nada (peer abierto en `mode=ro`).

> **F36 note:** `dark_research_spec_create` persiste el campo `tasks` como string opaco. `dark_memory_vibe_spec` antes rechazaba inputs que vinieran stringificados de harnesses que adoptan ese patrón; **F36 (v1.2.1) acepta ambos shapes** (JSON array o JSON-encoded string). Migración operativa: si tu harness ya maneja el shape string de `dark_research_spec_create`, ahora funciona tal cual contra `dark_memory_vibe_spec`.

Arquitectura completa: [`vibe-flow/main/BRIDGE_AND_COEXISTENCE.md`](vibe-flow/main/BRIDGE_AND_COEXISTENCE.md) y [`docs/COEXISTENCE.md`](docs/COEXISTENCE.md).

---

## Licencia

[MIT](LICENSE). Úsalo, modifícalo, distribúyelo. Si construyes algo bueno cuéntanos.

---

<div align="center">

Construido con 🇨🇴 desde Neiva, Huila, Colombia por [Opita Code](https://opitacode.com).

*"No construimos software para que se vea bonito en una presentación. Lo construimos para que trabaje contigo todos los días."*

[opitacode.com](https://opitacode.com) · [github.com/Opita-Code](https://github.com/Opita-Code) · [dark-research-mcp](https://github.com/Opita-Code/dark-research-mcp)

</div>
