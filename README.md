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
[![MCP tools](https://img.shields.io/badge/MCP-26%20tools-blueviolet)](#los-26-tools)
[![Tests](https://img.shields.io/badge/tests-9%20suites%20passing-brightgreen)](#tests)
[![Backends](https://img.shields.io/badge/backends-sqlite%20%7C%20postgres-blue)](docs/MIGRATION.md)
[![Conformant](https://img.shields.io/badge/MCP%20Inspector-passing-success)](tests/conformance/)

[¿Qué hace?](#qué-hace) · [¿Para quién?](#para-quién) · [Quickstart](#quickstart) · [Arquitectura](#arquitectura) · [Vibe-Flow](#el-vibe-flow-loop)

</div>

---

## ¿Qué hace?

**dark-memory-mcp** es un servidor MCP escrito en Go que entrega a tu agente IA **26 herramientas especializadas** agrupadas en 9 oficios (incluido el namespace L6-VLP para el state machine del Vibe-Loop Protocol), persistidas en una base SQL dual-driver (SQLite para dev, Postgres para prod) y gobernadas por **7 invariantes operacionales** que se defienden a sí mismos en el boundary del Store.

Una sola API. Tres binarios (`dark-mem-mcp` server MCP, `dark-mem-cli` admin, `dark-mem-inspect` read-only). Un solo `dark.db` compartido con `dark-research-mcp` (tablas distintas, propietarios distintos). **Sin magia: con código que puedes leer y modificar.**

> 🇨🇴 *Construido en Colombia como parte del ecosistema [Opita Code](https://opitacode.com). Software práctico para investigación real, no para verse bonito en una presentación.*

---

## Para quién

| Si eres… | Te interesa porque… |
|---|---|
| 🤖 **Agent developer** | 25 `dark_memory_*` tools listos para usar en tu agente MCP — sesiones, research, vibe-flow, judge, policy, observability, admin. Wire format estabilizado, orden canónico enforced. |
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
  "schema_version": 7,
  "canary_present": false,
  "active_constitution_id": "dark-agents/dark-memory-mcp",
  "active_constitution_version": "1.0.0",
  "tables": ["projects", "research_runs", "research_items", "vibe_specs", ...]
}
```

El binario `dark-mem-cli` aplica migraciones explícitas cuando las quieras, y `dark-mem-inspect` corre contra producción sin escribir nada.

---

## Los 26 tools

Ocho namespaces. El prefijo wire es `dark_memory_` (mandatory por BRIDGE_AND_COEXISTENCE §2.2). El orden canónico es **parte del contrato wire** — harnesses pueden indexar por posición.

| Namespace | Count | Tools |
|---|---|---|
| **SESSION** | 4 | `dark_memory_session_start`, `_resume`, `_status`, `_close` |
| **RESEARCH** | 3 | `dark_memory_research_topic`, `_recall`, `_resume_thread` |
| **VIBE** | 4 | `dark_memory_vibe_publish`, `_spec`, `_pipeline_status`, `_resolve_drift` |
| **CONTEXT** | 3 | `dark_memory_artifact_context`, `_spec_context`, `_session_context` |
| **JUDGE** | 3 | `dark_memory_judge`, `_consensus`, `_judgment_history` |
| **POLICY** | 2 | `dark_memory_active_policy`, `_load_constitution` |
| **OBSERVABILITY** | 3 | `dark_memory_memory_state`, `_writes`, `_anomalies` |
| **ADMIN** | 3 | `dark_memory_admin_migrate`, `_schema_status`, `_vacuum` |
| **L6-VLP** (DMAP v1.1) | 1 | `dark_memory_vlp_handle_event` |

Total: **4+3+4+3+3+2+3+3+1 = 26** ✓ (RFC §D-9 + DMAP v1.1 spec 193 Layer 6)

Cada tool expone un JSON Schema de input. Cada respuesta lleva `data + audit + next` para que el LLM sepa qué hacer después.

---

## El vibe-flow loop

El problema #1 sin resolver en 2026 AI-assisted development es el **spec-drift**: el agente genera algo, lo publica, y nunca reconcilia si lo que generó realmente cumple lo que el spec pedía.

**dark-memory-mcp** cierra ese loop con persistencia + LLM-as-judge:

```
                    ┌──────────────────────────────────────────┐
                    │  1. Crear spec (vibe_publish / vibe_spec) │
                    │     Persiste intent + tasks + constitution│
                    │                                          │
                    │  2. Generar el artifact                  │
                    │     (tu modelo / servicio preferido)     │
                    │                                          │
                    │  3. Loggear artifact                     │
                    │     artifact_log → write_audit row       │
                    │                                          │
                    │  4. LLM-as-judge: drift                  │
                    │     drift_judge(artifact_id)             │
                    │     verdict ∈ {aligned, drift_detected,  │
                    │                  needs_human}            │
                    │                                          │
                    │  5. Loggear verdict                     │
                    │     drift_log(verdict, judge_reasoning)  │
                    │                                          │
                    │  6. Human gate si algo falló             │
                    │     resolve_drift(accept | reject)       │
                    └──────────────────────────────────────────┘
```

Cada `dark_ssd_drift_judge` (sub-spec 180) persiste su verdict en `sdd_evaluations` con `prompt_version` + `model`. **Reproducible, auditable, mejorable con el tiempo** (calibration loop).

---

## Arquitectura

```
┌─────────────────────────────────────────────────────────────────┐
│  Tu agente (opencode, Claude Code, Cursor, lo que sea)         │
│                                                                 │
│  stdio MCP                                                      │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
        ┌────────────────────────────────────┐
        │   dark-mem-mcp.exe                 │
        │                                    │
        │   ┌──────────────────────────┐     │
        │   │  26 MCP tools            │     │
        │   │  ├ SESSION (4)           │     │
        │   │  ├ RESEARCH (3)          │     │
        │   │  ├ VIBE (4)              │     │
        │   │  ├ CONTEXT (3)           │     │
        │   │  ├ JUDGE (3)             │     │
        │   │  ├ POLICY (2)            │     │
│   │  ├ OBSERVABILITY (3)     │     │
│   │  ├ ADMIN (3)             │     │
│   │  └ L6-VLP (1)            │     │
        │   └──────────────────────────┘     │
        │                                    │
        │   ┌──────────────────────────┐     │
        │   │  internal/               │     │
        │   │  ├ store (sqlite/pg)     │◄──── DARK_DB_DRIVER + DARK_DB
        │   │  ├ orchestration (9)     │     │
        │   │  ├ context (8)           │     │
        │   │  ├ vibeflow (5)          │     │
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
   ┌──────────────────┐  ┌──────────────────┐
   │  dark.db (SQLite) │  │ Postgres         │
   │                   │  │ (jackc/pgx/v5)   │
   │  projects         │  │                  │
   │  sessions         │  │  same schema,    │
   │  write_audit      │  │  v7 migrations   │
   │  research_*       │  │  + dark-research │
   │  vibe_*           │  │  cross-link      │
   │  sdd_evaluations  │  │                  │
   │  constitutions    │  │                  │
   │  mods             │  │                  │
   └──────────────────┘  └──────────────────┘
```

Detalles en [`docs/`](docs/) y [`vibe-flow/main/DARK_MEMORY_MCP_RFC.md`](vibe-flow/main/DARK_MEMORY_MCP_RFC.md).

---

## Los 7 invariantes operacionales

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

Tabla completa con cada método Store → su set de invariantes en [`docs/INVARIANTS.md`](docs/INVARIANTS.md).

---

## Configuración

| Variable | Default | Propósito |
|---|---|---|
| `DARK_DB_DRIVER` | `sqlite` | `sqlite` \| `postgres` |
| `DARK_DB` | `./dark.db` (cwd) | Path al SQLite o URL Postgres |
| `DARK_CACHE_DIR` | (vacío) | Dónde persiste el LLM cache (INV-5). Vacío = in-process only. |
| `DARK_MOD_WHITELIST` | (vacío) | Lista comma-separated de mod IDs permitidos a cargar (INV-6) |
| `DARK_SERVER_NAME` | `dark-memory-mcp` | `serverInfo.name` en initialize response |
| `DARK_SERVER_VERSION` | `0.1.0` | `serverInfo.version` en initialize response |
| `DARK_COEXISTENCE_GROUP` | `dark-agents/memory` | Bridge §2.1 coexistence contract |
| `DARK_HOME` | `~/.config/dark-memory-mcp` | Donde `dark-mem-cli set-driver` escribe `config.toml` |

---

## Tests

```bash
go test -count=1 ./...
```

9 suites verdes con `-count=1` (cold rebuild, ~11 min total):

```
ok  tests/cli              122.606s   (13 tests: 11 + 2 canary_present regression)
ok  tests/conformance       73.779s   (4 bridge.7 tests via mcp-go real client)
ok  tests/context           25.381s
ok  tests/dual_driver        6.906s   (sqlite contract 7/7 sub-tests)
ok  tests/e2e              103.409s   (6 tests including 1000-mixed-no-deadlock)
ok  tests/economy            6.736s
ok  tests/invariants         5.870s   (INV-5 + INV-6)
ok  tests/orchestration     218.509s   (73+ tests across 9 orchestrators)
ok  tests/project          109.620s   (INV-7 multi-tenancy)
```

Highlights:
- `TestE2E_1000MixedCallsNoDeadlock` — RFC §12 #4 (1000 mixed tool calls)
- `TestBridge7_ListToolsCanonical` — wire-format regression for canonical order
- `TestSQLiteStoreContract/*` — dual-driver contract (sqlite branch)
- `TestInspect_CanaryPresent_StoreMethod` — review-w4 regression guard

---

## Status

- ✅ **v1.0.0** — 25 tools + dual-driver + bridge conformance 5/5 + 9 test suites + 6 runbooks
- 🆕 **unreleased** — `dark-mem-inspect` now correctly reports `canary_present` (review-w4-001); `dark-mem-mcp` has boot-path panic recovery (review-w4-002); mcp-go upgraded to v0.56.0 (review-w4-003); bridge.7 cold-cache timeout bumped 10s→30s (review-w4-004)
- 🚧 **v1.0.1** — `dark_ssd_drift_judge` real LLM verdicts (start dark-scrapper daemon, sub-spec 180)
- 🚧 **v1.1** — `dark-recall` v2.3 plugin migration (sibling opencode plugin); modelcontextprotocol/go-sdk migration evaluation
- 🚧 **v1.2** — Vector recall via pgvector; multi-tenancy beyond session_id filter

Ver [`HUMAN_GATE_REPORT.md`](HUMAN_GATE_REPORT.md) para el review completo y [`DECISION_MATRIX.md`](DECISION_MATRIX.md) para la trazabilidad de merge.

---

## Contribuir

Lee [`CONTRIBUTING.md`](CONTRIBUTING.md). PRs bienvenidos:

1. `go test -count=1 ./...` antes de pushear (9 suites, ~11 min)
2. Si añades un tool nuevo: sigue el orden canónico (no renumeres) — el orden es wire contract
3. Si añades un orchestrator: implementa tests + spec_create (C1) + drift_judge antes de merge
4. Si añades una migración: append a `migratesqlite.Migrations` / `migratepostgres.Migrations`, nunca edites una pasada
5. Si añades un invariante: documéntalo en `docs/INVARIANTS.md` + agregas test defensivo

---

## Coexistencia con dark-research-mcp

| MCP | Namespace | Tools | Propósito |
|---|---|---|---|
| **dark-memory-mcp** | `dark_memory_*` | 25 | Memoria persistente + workflow orchestration (este repo) |
| **dark-research-mcp** | `dark_research_*` | ~13 + multi + router | OSINT acquisition |
| (deprecado) | `dark_mem_*` | legacy | dark-research-mcp emite `{deprecated: true, successor: 'dark-memory-mcp'}` en cada response |

Ambos comparten `dark.db` (tablas distintas, propietarios distintos). El `coexistence_group=dark-agents/memory` se declara en el `initialize` response — los harnesses MCP-native detectan automáticamente.

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
