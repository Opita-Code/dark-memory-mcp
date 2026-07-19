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
[![MCP tools](https://img.shields.io/badge/MCP-29%20tools-blueviolet)](#los-29-tools)
[![Tests](https://img.shields.io/badge/tests-20%20suites%20passing-brightgreen)](#tests)
[![Backends](https://img.shields.io/badge/backends-sqlite%20%7C%20postgres-blue)](docs/MIGRATION.md)
[![Conformant](https://img.shields.io/badge/MCP%20Inspector-passing-success)](tests/conformance/)

[¿Qué hace?](#qué-hace) · [¿Para quién?](#para-quién) · [Quickstart](#quickstart) · [Arquitectura](#arquitectura) · [Vibe-Flow](#el-vibe-flow-loop)

</div>

---

## ¿Qué hace?

**dark-memory-mcp** es un servidor MCP escrito en Go que entrega a tu agente IA **28 herramientas canónicas** agrupadas en 10 oficios (incluido el namespace L6-VLP para el state machine del Vibe-Loop Protocol, y el namespace PROJECT para bootstrap multi-tenant sin acceso directo a la DB), persistidas en una base SQL dual-driver (SQLite para dev, Postgres para prod) y gobernadas por **8 invariantes operacionales** que se defienden a sí mismos en el boundary del Store.

Una sola API. Tres binarios (`dark-mem-mcp` server MCP, `dark-mem-cli` admin, `dark-mem-inspect` read-only). Un solo `dark.db` compartido con `dark-research-mcp` (tablas distintas, propietarios distintos). **Sin magia: con código que puedes leer y modificar.**

Cuando se opera en modo `armed` (`DARK_REDTEAM=armed`), el servidor emite además **3 herramientas L7-REDTEAM adicionales** (`dark_memory_redteam_list_mods`, `_get_prompts`, `_log_attempt` — research use only, ver [Modo Armed (L7-REDTEAM)](#modo-armed-l7-redteam)). Superficie total: **31**. Sin `armed`, la superficie canónica se mantiene en 28 (test defensivo en `TestE2E_28ToolsRegistered` + `TestWire_RuntimeToolEnumeration`).

**Health probe (v1.3.0):** `dark_memory_health_ping` es un liveness probe de bajo costo (latencia objetivo <50ms en caliente, presupuesto <500ms) apto para K8s liveness/readiness. Devuelve un snapshot inmutable `{server, db, runtime, registry, latency_ms, checked_at}` sin tocar el audit bus ni avanzar el VLP state. Ver `docs/PRODUCTION_CHECKLIST.md` §Health Probe para el wiring.

> 🇨🇴 *Construido en Colombia como parte del ecosistema [Opita Code](https://opitacode.com). Software práctico para investigación real, no para verse bonito en una presentación.*

---

## Para quién

| Si eres… | Te interesa porque… |
|---|---|
| 🤖 **Agent developer** | 28 `dark_memory_*` tools canónicos listos para usar en tu agente MCP — sesiones, research, vibe-flow, judge, policy, observability, admin, project. Wire format estabilizado, orden canónico enforced. |
| 🧠 **Memory engineer** | El Store dual-driver + 7 invariantes operacionales te da persistencia defendible: write-path audit, per-session scoping, canary en writes, constitution watchdog, cache re-hash, mod sanitization, multi-tenancy. |
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
  "schema_version": 10,
  "canary_present": false,
  "active_constitution_id": "dark-agents/dark-memory-mcp",
  "active_constitution_version": "1.0.0",
  "tables": ["projects", "research_runs", "research_items", "vibe_specs", ...]
}
```

El binario `dark-mem-cli` aplica migraciones explícitas cuando las quieras, y `dark-mem-inspect` corre contra producción sin escribir nada.

---

## Los 29 tools

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
| **OBSERVABILITY** | 4 | `dark_memory_health_ping` (v1.3.0), `_memory_state`, `_writes`, `_anomalies` |
| **ADMIN** | 3 | `dark_memory_admin_migrate`, `_schema_status`, `_vacuum` |
| **L6-VLP** (DMAP v1.1) | 1 | `dark_memory_vlp_handle_event` |

Total: **1+4+3+4+4+3+2+3+3+1 = 28** ✓ (RFC §D-9 + DMAP v1.1 spec 193 Layer 6 + F33 v1.2.0 + health_ping v1.3.0)

Cada tool expone un JSON Schema de input. Cada respuesta lleva `data + audit + next` para que el LLM sepa qué hacer después. La posición en esta tabla es el orden wire (`tools/list`); los harnesses pueden confiar en el índice.

### Modo Armed (L7-REDTEAM)

Cuando se arranca con `DARK_REDTEAM=armed`, el servidor registra **3 herramientas adicionales** en el namespace `dark_memory_redteam_*`:

| Namespace | Count | Tools |
|---|---|---|
| **L7-REDTEAM** (armed-only) | 3 | `dark_memory_redteam_list_mods`, `_get_prompts`, `_log_attempt` |

Las herramientas cargan los mods instalados bajo `mods/redteam/` (configurable vía `DARK_REDTEAM_MODS_PATH`). Los mods son files de payloads de security research (prompt-injection-lab, jailbreak-taxonomy, etc.). **Solo para uso de investigación con autorización explícita.** No destinados a infraestructura de ataque en producción.

La superficie armed es 28 + 3 = **31**. La superficie sin armar es 28, garantizada por `TestE2E_28ToolsRegistered` (Go level) y `TestWire_RuntimeToolEnumeration` (wire level).

---

## El vibe-flow loop

El problema #1 sin resolver en 2026 AI-assisted development es el **spec-drift**: el agente genera algo, lo publica, y nunca reconcilia si lo que generó realmente cumple lo que el spec pedía.

**dark-memory-mcp** cierra ese loop con persistencia + LLM-as-judge:

```
                     ┌───────────────────────────────────────────┐
                     │  1. Crear spec (vibe_publish / vibe_spec) │
                     │     Persiste intent + tasks + constitution│
                     │     (vibe_case ∈ {C1..C7} enforced, v1.4.1)│
                     │                                           │
                     │  2. Generar el artifact                   │
                     │     (tu modelo / servicio preferido)      │
                     │                                           │
                     │  3. Loggear artifact                      │
                     │     artifact_log → write_audit row        │
                     │                                           │
                     │  4. LLM-as-judge: drift                   │
                     │     dark_memory_judge(                    │
                     │       eval_type="drift_judge",            │
                     │       target_id=artifact_id,              │
                     │       content=artifact_text)              │
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

Cada `dark_memory_judge(eval_type=drift_judge)` persiste su verdict en `sdd_evaluations` con `prompt_version` + `model`. **Reproducible, auditable, mejorable con el tiempo** (calibration loop). Para high-stakes verdicts usa `dark_memory_consensus(eval_type, content, n=5..7)` — N-shot LLM-as-judge con modal verdict. (El viejo namespace `dark_ssd_*` se deprecó en v1.4.0 y se consolidó en dark-memory-mcp como `dark_memory_judge`.)

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
         │   │  28 MCP tools (+3 armed) │     │
         │   │  ├ PROJECT (1, v1.2.0)   │     │
         │   │  ├ SESSION (4)           │     │
         │   │  ├ RESEARCH (3)          │     │
         │   │  ├ VIBE (4)              │     │
         │   │  ├ CONTEXT (3)           │     │
         │   │  ├ JUDGE (3)             │     │
         │   │  ├ POLICY (2)            │     │
         │   │  ├ OBSERVABILITY (4)     │     │
         │   │  ├ ADMIN (3)             │     │
         │   │  └ L6-VLP (1, v1.1.0)    │     │
         │   │  + L7-REDTEAM (3, armed) │     │
         │   └──────────────────────────┘     │
         │                                    │
         │   ┌──────────────────────────┐     │
         │   │  internal/  (22 packages)│     │
         │   │  ├ store (sqlite/pg)     │◄──── DARK_DB_DRIVER + DARK_DB
         │   │  ├ tools (28 handlers)   │     │
         │   │  ├ orchestration (16)   │     │
         │   │  ├ vlp (state machine,  │     │
         │   │  │  DMAP v1.1 Layer 2)  │     │
         │   │  ├ vibecase (C1-C7,      │     │
         │   │  │  v1.4.1)             │     │
         │   │  ├ vibeflow (5)          │     │
         │   │  ├ context (8)           │     │
         │   │  ├ ssd (judge persist)   │     │
         │   │  ├ llm/cache (INV-5)     │     │
         │   │  ├ constitution (INV-4)  │     │
         │   │  ├ safety (canary)       │     │
         │   │  ├ audit (INV-1)         │     │
         │   │  ├ mods/loader (INV-6)   │     │
         │   │  ├ project (INV-7)       │     │
         │   │  ├ session               │     │
         │   │  ├ server/bootstrap      │     │
         │   │  ├ version (resolver,    │     │
         │   │  │  v1.4.0)             │     │
         │   │  ├ adapter/opencode      │     │
         │   │  ├ federation (cross-MCP)│     │
         │   │  ├ migrate (sqlite+pg)   │     │
         │   │  └ economy (Atlan 5-bkt) │     │
         │   └──────────────────────────┘     │
         └────────────────┬───────────────────┘
                         │
              ┌──────────┴──────────┐
              ▼                     ▼
   ┌───────────────────┐  ┌──────────────────┐
   │ dark-memory.db    │  │ Postgres         │
   │ (SQLite, INV-8    │  │ (jackc/pgx/v5)   │
   │  default)         │  │                  │
   │                   │  │  same schema,    │
   │  projects         │  │  v10 migrations  │
   │  sessions         │  │  + dark-research │
   │  vlp_state        │  │  cross-link      │
   │  write_audit      │  │                  │
   │  research_*       │  │                  │
   │  vibe_*           │  │                  │
   │  sdd_evaluations  │  │                  │
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
| `DARK_CACHE_DIR` | (vacío) | Dónde persiste el LLM cache (INV-5). Vacío = in-process only. |
| `DARK_MOD_WHITELIST` | (vacío) | Lista comma-separated de mod IDs permitidos a cargar (INV-6) |
| `DARK_SERVER_NAME` | `dark-memory-mcp` | `serverInfo.name` en initialize response |
| `DARK_SERVER_VERSION` | `1.4.1-dev` | `serverInfo.version` en initialize response (canonical source: `internal/version` resolver; `Makefile release` lo inyecta via `-ldflags`) |
| `DARK_COEXISTENCE_GROUP` | `dark-agents/memory` | Bridge §2.1 coexistence contract |
| `DARK_HOME` | `~/.config/dark-memory-mcp` | Donde `dark-mem-cli set-driver` escribe `config.toml` |
| `DARK_REDTEAM` | (unset) | Si =`armed`, registra las 3 herramientas L7-REDTEAM. Surface total = 31. Sin la var, surface = 28. |
| `DARK_REDTEAM_MODS_PATH` | `./mods/redteam` | Path al directorio de mods armed. |

---

## Tests

```bash
go test -count=1 ./...
```

**20 suites verdes** con `-count=1` (cold rebuild, ~12 min total). v1.4.0–v1.4.2 añadieron: `internal/vibecase` (15 tests, v1.4.1), `internal/version` (9 tests, v1.4.0), `internal/federation` (v1.4.2), `internal/vlp` (55 tests across 6 files, DMAP v1.1).

```
ok  internal/adapter/opencode   ~46s   (OpenCode harness adapter)
ok  internal/federation         ~49s   (cross-MCP federation lookup)
ok  internal/orchestration       ~3s   (16 orchestrators)
ok  internal/server              ~5s   (bootstrap + version + DSN)
ok  internal/tools              ~73s   (28 tool handlers + registry)
ok  internal/version             ~1s   (resolver: ldflags|buildinfo|dev)
ok  internal/vibecase            ~1s   (15 tests: C1..C7 taxonomy)
ok  internal/vlp                ~103s   (55 tests: state machine + persistence + audit)
ok  tests/cli                   ~123s   (13 tests: 11 + 2 canary_present regression)
ok  tests/conformance            ~99s   (4 bridge.7 tests via mcp-go real client)
ok  tests/context                ~37s
ok  tests/dual_driver             ~8s   (sqlite contract 7/7 sub-tests)
ok  tests/e2e                   ~108s   (1000-mixed-no-deadlock + 28-tool register guard)
ok  tests/economy                 ~1s
ok  tests/invariants              ~6s   (INV-5 + INV-6)
ok  tests/migrate                ~88s
ok  tests/orchestration         ~124s   (73+ tests; F36 dual-form + v1.4.1 vibecase)
ok  tests/project                ~90s   (INV-7 multi-tenancy)
ok  tests/tools                  ~44s   (F33 project tool: 7 sub-tests, schema rejection, idempotency)
ok  tests/wire                   ~26s   (wire conformance: F33/F35/F36/F37/F38/F39/F40 + health_ping)
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

---

## Status

- ✅ **v1.0.0** — 25 tools + dual-driver + bridge conformance 5/5 + 9 test suites + 6 runbooks
- ✅ **v1.0.x** — `dark-mem-inspect` ahora reporta `canary_present` correctamente (review-w4-001); `dark-mem-mcp` tiene panic recovery en boot-path (review-w4-002); mcp-go upgraded to v0.56.0 (review-w4-003); bridge.7 cold-cache timeout bumped 10s→30s (review-w4-004)
- ✅ **v1.1.0** — DMAP v1.1 Layer 6: `dark_memory_vlp_handle_event` (Vibe-Loop Protocol wire tool) + OpenCode adapter demo + L6.1 merge
- ✅ **v1.2.0** (F33 + F35, 2026-07-16) — `dark_memory_project_create` cierra el loop de bootstrap multi-tenant. `vibe_publish` JSON Schema corregido (nested spec+artifact en lugar de flat) + `vibeSpecTaskSchema` strict (additionalProperties:false) + `BindOrchestrator`'s `typeMismatchToolError` devuelve field path + expected/actual type. Tool count: 26 → 27.
- ✅ **v1.2.1** (F36, 2026-07-16) — `dark_memory_vibe_spec.tasks` ahora acepta tanto JSON array como JSON-encoded string (compatibilidad con la gemela `dark_research_spec_create` que persiste el campo como string opaco). 2 tests nuevos. Drop-in replacement; sin migrations; sin cambio de surface. **Restart requerido del binario `dark-mem-mcp.exe`** para tomar el código nuevo.
- ✅ **v1.2.2** (F37 + F38 + F39 + F40, 2026-07-16) — Migration runner self-healing: `applyOne` ahora split-on-`;` y tolera 4 clases de errores DDL idempotentes (`duplicate column name`, `no such module`, `table already exists`). `EnsureCoreTables` recrea tablas core faltantes al boot. Sin esto, dark-memory-mcp NO podía arrancar contra dark.dbs parcialmente migrados. 8 tests nuevos en `tests/migrate/`.
- ✅ **v1.3.0** (production-readiness, 2026-07-16) — `dark_memory_health_ping` (liveness probe enlatado, OBSERVABILITY 3→4), wire-conformance total (10/10 tests PASS contra el binario real), wait-for-boot-marker para eliminar race en startup, `.github/workflows/ci.yml` con receta de CI operator-reproducible, race-detector note en PRODUCTION_CHECKLIST, stale-binary gotcha documentada. **28 tools canónicos, 31 armed.**
- ✅ **v1.4.0** (release-integrity, 2026-07-18) — [`CONSTITUTION.md`](CONSTITUTION.md) `release-integrity@1.0.0` (5 reglas: single source of truth, archive-not-delete, CHANGELOG authoritative, drift detection on boot, session-bound governance). `internal/version` package — resolver canónico (`-ldflags` → `debug.ReadBuildInfo()` → hardcoded `"dev"`). `Makefile` con `build`/`release`/`drift-check`/`version`/`tag` targets. `dark_memory_health_ping` ahora incluye bloque `git` (`tag`, `commit`, `dirty`, `build_time`, `source`, `is_dev`) + `drift` bool. `scripts/inject-version.{sh,ps1}`. 9 tests del resolver cubren los 3 paths.
- ✅ **v1.4.1** (vibecase taxonomy, 2026-07-18) — `internal/vibecase` como **single source of truth** para la taxonomía C1..C7 (vibe_case case labels). `dark_memory_vibe_spec` ahora enforza el enum (antes era free string; **ver "Behavior change" en CHANGELOG [1.4.1]**). `dark_memory_vibe_publish` JSON Schema enum deriva de `vibecase.JSONSchemaEnum()`. Ambos orchestrators validan via `vibecase.Parse` (defense in depth). 4 nuevos tests (InvalidVibeCase x2, AcceptsAllCanonicalCases, AcceptsTrimmedVibeCase). 15 nuevos unit tests en `internal/vibecase`. **Behavior change**: callers que pasaban valores no canónicos ahora reciben `ErrInvalidArgument`.
- ✅ **v1.4.2** (CI follow-ups, 2026-07-18) — 2 PRs separados (#8 + #9) cerrando las fallas de CI pre-existentes que cargó v1.4.1 (merge con `--admin`). PR #8: `redteamModsAbsPath` honra `DARK_REDTEAM_MODS_PATH` primero, fallback `t.Skipf` (no `t.Fatalf`) cuando no hay mods → 6 unit tests pasan/skipped clean. PR #9: drop over-strict `USER` MUST-contain check en `TestWire_HealthPingShape` (redaction sólo aplica a paths bajo `$HOME`). Vendoring guide en `internal/tools/testdata/redteam-vendoring.md`. **Primer release con CI 100% verde desde v1.4.0.**
- ✅ **v2.0.0** (memory-as-policy-gateway pivot, 2026-07-19) — **breaking architectural pivot**. Replaces the pull-based CRUD model with a gate-driven active-memory model. Every `tools/call` now traverses `internal/policy.PostCheck` which composes atomic frames (`internal/atomic`), invokes the orchestrator with the frame as input, and drift-checks the response at the write boundary. 13 waves shipped (5A.ii.b.2.a..c, 5A.vi, 5E.iv, 5E.iv.b, 5E.v, 5X.1, 5X.2, 5X.3, 5X.4, 5A.ii.a polish). New: `internal/atomic`, `internal/drift`, `internal/policy`, `internal/recall` packages. New tool: `dark_memory_recall` (29th canonical; CONTEXT 3→4). Schema migrations v11–v15 (frame UPSERT, audit session_event, drift_strictness, open_spec_id). L6 adapter integration: startup-recover + periodic-heartbeat + exit-clean. Shutdown default close_reason `aborted` → `clean` (BREAKING). Operator env contract rename: `DARK_SCRAPPER_URL` → `DARK_DRIFT_JUDGE_DAEMON_URL` (PR #10). 9 commits + release. Tool count: 28 → 29.
- 🚧 **v2.1.x** — Vector recall via sqlite-vec; constitution mod registry v2; L7-REDTEAM integration formal (actualmente en operator-WIP)

Patches publicados (release artifacts, no en el repo):
- `dark-memory-mcp-v1.2.0.patch` — superficie 27 tools, ~870 LOC adicionales
- `dark-memory-mcp-v1.2.1.patch` — drop-in replacement, F36 fix
- `dark-memory-mcp-v1.2.5.patch` — wire-conformance suite (tests/wire/) + F35 wire propagation + CONTRIBUTING + PRODUCTION_CHECKLIST
- `dark-memory-mcp-v1.3.0.patch` — production-readiness: health_ping + wire-conformance bump + race-free boot + CI workflow
- v1.4.0 / v1.4.1 / v1.4.2 — distribuidos vía GitHub Releases (no como `.patch` files) por la mayor densidad de cambios (constitution + version resolver + vibecase + 2 CI fixes)

Ver [`CHANGELOG.md`](CHANGELOG.md) para el detalle completo de cada release y [`docs/PR-v1.2.0.md`](docs/PR-v1.2.0.md) para el desglose técnico de F33+F35.

---

## Contribuir

Lee [`CONTRIBUTING.md`](CONTRIBUTING.md). PRs bienvenidos:

1. `go test -count=1 ./...` antes de pushear (20 suites, ~12 min)
2. Si añades un tool nuevo: sigue el orden canónico (no renumeres) — el orden es wire contract
3. Si añades un orchestrator: implementa tests + spec_create (C1) + drift_judge antes de merge
4. Si añades una migración: append a `migratesqlite.Migrations` / `migratepostgres.Migrations`, nunca edites una pasada
5. Si añades un invariante: documéntalo en `docs/INVARIANTS.md` + agregas test defensivo

---

## Coexistencia con dark-research-mcp

| MCP | Namespace | Tools (canonical) | Propósito |
|---|---|---|---|
| **dark-memory-mcp** | `dark_memory_*` | 28 (+3 armed) | Memoria persistente + workflow orchestration (este repo) |
| **dark-research-mcp** | `dark_research_*` | ~13 + multi + router | OSINT acquisition |
| (deprecado) | `dark_mem_*` | legacy | dark-research-mcp emite `{deprecated: true, successor: 'dark-memory-mcp'}` en cada response |

Ambos comparten `dark.db` (tablas distintas, propietarios distintos). El `coexistence_group=dark-agents/memory` se declara en el `initialize` response — los harnesses MCP-native detectan automáticamente.

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
