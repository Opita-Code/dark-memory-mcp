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
║        Persistent Memory • Vibe-Loop Engine • Agent Governance • MCP               ║
║                                                                                    ║
╚════════════════════════════════════════════════════════════════════════════════════╝
```

**El cuaderno persistente del agente: para que nunca pierdas el hilo de lo que estás haciendo.**

[![MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![MCP tools](https://img.shields.io/badge/MCP-38%20canonical%20tools-blueviolet)](#las-38-herramientas)
[![Schema](https://img.shields.io/badge/schema-v20-success)](#la-base-de-datos)
[![Tests](https://img.shields.io/badge/tests-27%20suites%20verdes-brightgreen)](#tests)
[![Install](https://img.shields.io/badge/install-npx%20%40opita--code%2Fdark--memory--mcp-cc3534)](docs/npm-install.md)
[![MCPB](https://img.shields.io/badge/MCPB%20bundle%20for%20Claude%20Desktop-cc3534)](docs/mcpb-install.md)
[![Backends](https://img.shields.io/badge/backends-sqlite%20%7C%20postgres-blue)](#la-base-de-datos)

[¿Qué es dark-memory?](#que-es-dark-memory) · [¿Qué es vibe-loop?](#que-es-vibe-loop) · [Cómo se conectan](#como-se-conectan) · [Quickstart en 5 minutos](#quickstart-en-5-minutos) · [Hacer un vibe-loop paso a paso](#hacer-un-vibe-loop-paso-a-paso) · [Cuando algo no funciona](#cuando-algo-no-funciona) · [Para los curiosos técnicos](#para-los-curiosos-tecnicos)

</div>

---

## ¿Qué es dark-memory?

Imagina que tu agente IA es un asistente brillante que trabaja contigo todos los días.
Cada día le pides algo nuevo: refactorizar una función, escribir un test, diseñar
un workflow, recordar por qué tomaste una decisión la semana pasada.

**El problema es que el agente olvida.** Cuando cierras la sesión, todo se va.
Al día siguiente le tienes que volver a explicar quién eres, qué proyecto estás
haciendo, qué decisiones tomaste, qué cosas ya intentaste.

**dark-memory es el cuaderno donde el agente anota todo lo importante.**

Cuando el agente aprende algo nuevo, lo escribe. Cuando toma una decisión, registra
por qué. Cuando descubre que algo no funcionó, lo apunta para no repetirlo.
Cuando le preguntas "¿qué decidimos la semana pasada?", lo busca.

Es un **cuaderno persistente** (no se borra al cerrar) que vive en tu propia
computadora y al que tu agente puede acceder cuando quiera, desde cualquier sesión
de trabajo.

### Tres cosas concretas que hace por ti

1. **Recuerda entre sesiones.** Anota el contexto de tu proyecto una vez;
   recuérdalo siempre. Cuando vuelvas mañana, el agente sabe quién eres,
   qué proyecto tienes, en qué punto vas, qué decisiones tomaste.

2. **Te ayuda a hacer vibe-loop.** Cuando le pides algo al agente, dark-memory
   guarda la promesa ("voy a hacer X") y luego verifica que lo que entregó
   realmente cumpla esa promesa. Si no cumple, te avisa. (Más sobre esto abajo.)

3. **Lleva un registro de auditoría.** Cada cambio que el agente hace en su
   cuaderno queda firmado con quién lo hizo, cuándo, y por qué. Si algo
   sale mal, puedes revisar qué pasó.

### ¿Qué NO es dark-memory?

- **No es una base de datos para tu aplicación.** Es un cuaderno del agente,
  no un almacén de datos de negocio. Para eso usa Postgres normal.
- **No es un modelo de IA.** No piensa, no escribe código, no toma decisiones.
  Solo guarda y recupera lo que el agente ya pensó.
- **No es un servicio en la nube.** Todo vive en tu computadora. No hay
  servidor remoto, no hay telemetría, no hay costos recurrentes.

---

## ¿Qué es vibe-loop?

Vibe-loop es una forma de trabajar con tu agente que cierra el círculo entre
**lo que le pediste** y **lo que entregó**.

El flujo normal con un agente es así:

> Tú: "Hazme una función que valide emails."
> Agente: "Aquí está." [te da código]
> Tú: [lo pruebas] "Funciona, gracias."

Pero ¿qué pasa cuando el código entregado **no cumple lo que pediste**?

> Tú: "Hazme una función que valide emails."
> Agente: "Aquí está." [te da una función que solo valida emails de gmail]
> Tú: "Pero esto no valida emails de yahoo..."
> Agente: "Tienes razón, lo arreglo." [te da otra versión]
> Tú: "Ahora tampoco maneja emails con +..."
> Agente: "..." [iteración sin fin]

El problema: **el agente nunca se da cuenta solo de que se desvió**. Tú tienes
que estar revisando cada entrega. Se pierde mucho tiempo.

**Vibe-loop cierra ese círculo:**

```
   Le dices al agente QUÉ quieres (la promesa/spec)
            ↓
   El agente entrega algo (el artefacto)
            ↓
   Un juez automático revisa: ¿lo entregado cumple lo prometido?
            ↓
   Si cumple → "OK, seguimos"
   Si NO cumple → el agente re-intenta con la crítica del juez
            ↓
   Si después de varios intentos NO cumple → te pregunta a ti
```

La promesa (spec) y la crítica del juez (drift) se quedan guardadas en
dark-memory. Mañana puedes revisar: "¿qué le pedí, qué entregó, qué dijo
el juez?"

Es como tener un manager de calidad revisando cada entrega del agente,
pero automático y persistente.

---

## ¿Cómo se conectan?

dark-memory es el **cuaderno donde el vibe-loop escribe**.

| Pieza del vibe-loop | Qué hace | Cómo lo guarda dark-memory |
|---|---|---|
| La promesa | "Voy a hacer X, Y, Z" | Lo guarda como **spec** |
| El artefacto | El código/texto/imagen que entregó el agente | Lo guarda como **artifact** |
| El juicio | "¿Cumple lo prometido? Sí/No/Parcial" | Lo guarda como **drift_log** |
| Tu decisión final | "Acepto / Rechazo" | Lo guarda como **resolve_drift** |
| El cuaderno | Tus notas, observaciones, decisiones, links | Lo guarda como **agent_memory** |

Cuando el agente está trabajando y necesita recordar algo, mira su cuaderno
(consulta `dark_memory_recall`). Cuando termina y entrega algo, escribe
en el cuaderno qué hizo y por qué. Es un loop cerrado.

---

## Quickstart en 5 minutos

### 1. Verifica que tienes lo necesario

```
- Node.js 18+ (https://nodejs.org/) — solo si vas a usar el wrapper npm
- Go 1.25+ (https://go.dev/dl/) — solo si vas a compilar desde source
```

### 2. Conéctalo a tu agente (opencode, Claude Code, Cursor, etc.)

**Opción A — vía npm wrapper (recomendado para vibe-coders, desde v2.5.0):**

Pega esto en tu config de MCP host:

```jsonc
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"]
    }
  }
}
```

Eso es todo. `npx` descarga el wrapper + el binario para tu OS la primera vez; las
siguientes veces usa caché. No hay build, no hay SHA-256 manual, no hay
`go install`. Funciona idéntico en macOS, Linux y Windows.

Detalles por host (Claude Code, Claude Desktop, opencode, Cursor) en
[`docs/npm-install.md`](docs/npm-install.md).

**Opción B — descarga directa del binario (legacy, aún soportado):**

Ve a [Releases](https://github.com/Opita-Code/dark-memory-mcp/releases),
descarga el `.exe` / ELF / Mach-O de tu OS, verifica el SHA-256, y apunta
tu MCP host al path absoluto:

```jsonc
{
  "mcp": {
    "dark-memory": {
      "type": "local",
      "command": ["C:/ruta/a/dark-mem-mcp.exe"],
      "enabled": true
    }
  }
}
```

(en Mac/Linux sería sin `.exe`).

### 3. Verifica que arrancó bien

Si usaste el wrapper npm, abre tu agente y pídele que llame a
`dark_memory_health_ping`. Deberías ver un JSON con
`schema_version: 20` (o superior) y `driver: sqlite`.

Si compilaste desde source:

```bash
./bin/dark-mem-inspect --json
```

### 4. Pídele a tu agente que use el cuaderno

> *"Inicia una sesión de dark-memory para mí, soy Nico y estoy trabajando
> en el proyecto darkmem."*

El agente debería llamar a `dark_memory_session_start`. Si no, recuérdale
que tiene una herramienta MCP disponible.

### ¿Quieres compilarlo tú mismo?

```bash
git clone https://github.com/Opita-Code/dark-memory-mcp.git
cd dark-memory-mcp

# Esto crea los tres binarios que necesitas
go build -o bin/dark-mem-mcp   ./cmd/dark-mem-mcp
go build -o bin/dark-mem-cli   ./cmd/dark-mem-cli
go build -o bin/dark-mem-inspect ./cmd/dark-mem-inspect
```

Útil si quieres contribuir, hacer un fork, o auditar el binario antes
de correrlo.

---

## Las 39 herramientas

dark-memory expone 39 acciones que tu agente puede invocar. Todas empiezan
con el prefijo `dark_memory_`. Están agrupadas en 13 oficios:

### 🧭 Empezar y cerrar sesión (PROJECT + SESSION — 5 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_project_create` | Una vez: para crear un proyecto nuevo (como "crear un workspace") |
| `dark_memory_session_start` | Al comenzar a trabajar: abre tu sesión del día |
| `dark_memory_session_resume` | Si cerraste mal y quieres retomar |
| `dark_memory_session_status` | "¿En qué punto vamos?" |
| `dark_memory_session_close` | Al terminar: cierra la sesión limpio |

### 🔍 Investigar (RESEARCH — 3 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_research_topic` | "Investiga X y dame un resumen" |
| `dark_memory_research_recall` | "¿Qué investigué antes sobre X?" |
| `dark_memory_research_resume_thread` | "Continúa esa investigación de la semana pasada" |

### 🪜 Self-Bootstrapping (AGENT_BOOTSTRAP — 3 tools, v2.6.0)

> **Nuevo en v2.6.0.** El servidor se enseña a sí mismo cómo usarse. Publica
> el manual canónico (8.9 KB), la matriz de compatibilidad por harness, 6
> guías de instalación y 2 docs de MCPs companions como **resources MCP**.
> Cualquier harness (Claude Desktop/Code, opencode, Cline, Cursor, Continue)
> puede descubrirlas sin docs externos.

El `instructions` field que muchos harnesses descartan (opencode [#32856](https://github.com/opencode-ai/opencode/issues/32856))
es **best-effort**; los **resources son el camino canónico** porque todos
los harnesses spec-compliant los soportan. Las 3 tools de abajo le dan al
LLM acceso programático a ese contenido.

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_agent_bootstrap` | "Carga el manual canónico" — `surface`: `system_prompt` \| `compatibility_matrix` \| `install_guide` \| `companion` \| `all`. Para `install_guide` y `companion` pasá `target` (ej. `target=opencode`). |
| `dark_memory_agent_recommend_companions` | "¿Qué MCPs companions me faltan?" — siempre recomienda `dark-research` y `[FUTURE-MCP-N]` con snippet de install. |
| `dark_memory_agent_detect_environment` | "¿Qué spec estás negociando? ¿Quién eres tú como harness?" — devuelve `SpecVersionDetected` (2025-06-18 / 2026-07-28), harness info y capacidades negociadas. |

Si querés customizar el contenido sin esperar un release, exportá
`DARK_AGENT_BOOTSTRAP_DIR=/ruta/a/tu/dir` con los 10 archivos esperados
(`SYSTEM_PROMPT.md`, `COMPATIBILITY_MATRIX.md`, 6 guides en `install/`,
2 docs en `companions/`). El servidor valida al arrancar y hace fallback
al contenido embebido si falta algo.

**Detalles técnicos**:

- **Dual-spec clientInfo**: legacy `initialize.clientInfo` (2025-06-18) +
  nuevo `_meta.clientInfo` per-request (2026-07-28) convergen en una sola
  store `ClientInfoRecord`. Las tools leen de ahí.
- **Audience `assistant` priority `0.9`**: el bootstrap content va al
  LLM, no al usuario. Harness puede demotarlo si tiene context budget
  apretado.
- **URI scheme `dark-memory://`**: no-routable, marca claro de "owned
  by this MCP" (no es `https://`, no es `file://`).

Para la arquitectura completa (5 capas, decision tree, override env var),
ver [`docs/agent-bootstrap.md`](docs/agent-bootstrap.md).

### 🌊 Hacer vibe-loop (VIBE — 4 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_vibe_spec` | Crear la promesa ("voy a hacer X") |
| `dark_memory_vibe_publish` | Entregar el artefacto bajo una promesa |
| `dark_memory_pipeline_status` | "¿Cómo va mi pipeline de vibe-loop?" |
| `dark_memory_resolve_drift` | Cuando el juez dice que se desvió: aceptar o rechazar |

### 📋 Ver el contexto (CONTEXT — 4 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_artifact_context` | "Muéstrame qué entregué en este artefacto" |
| `dark_memory_spec_context` | "Muéstrame la promesa original" |
| `dark_memory_session_context` | "Muéstrame todo lo que pasó en esta sesión" |
| `dark_memory_recall` | "Dame un resumen de los últimos cambios" |

### 🧠 El cuaderno del agente (AGENT_MEMORY — 6 tools, v2.1.0 + v2.3.0)

Esta es la pieza nueva. Es el **cuaderno personal** del agente, donde anota
notas, observaciones, decisiones, links, hallazgos — cosas que quiere
recordar entre sesiones. **v2.3.0** corrigió dos bugs: las notas ya no
desaparecen al cerrar la sesión (INV-10) y se alinean con la taxonomía
Mem0 de 3 clases (`episodic`/`semantic`/`procedural`). Además
`agent_memory_recall` es la primera herramienta que **usa** el cuaderno
internamente — antes no había consumidor.

> ⚠️ **v2.3.0 cambia dos defaults**:
> - `agent_memory_save` ya no ata la sesión automáticamente. Pasá `bind_session: true` si querés el comportamiento pre-v2.3.0.
> - `agent_memory_list(scope="current")` ahora devuelve el proyecto entero (no solo la sesión). Pasá `scope="session"` explícito para mantener la query acotada.

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_agent_memory_save` | "Apunta esto: usamos Postgres 16". Acepta `agent_id`, `memory_type` y `bind_session` (default `false` desde v2.3.0). |
| `dark_memory_agent_memory_list` | "Dame mis notas sobre este proyecto". Scopes: `current` (default `project` desde v2.3.0), `session`, `project`, `operator`, **`agent`** (nuevo en v2.3.0), `all`. |
| `dark_memory_agent_memory_recall` | **Nuevo en v2.3.0**. Búsqueda BM25 sobre contenido + título + tags, con filtro opcional por `agent_id` y/o `memory_type`. |
| `dark_memory_agent_memory_get` | "Muéstrame la nota #42" |
| `dark_memory_agent_memory_update` | "Edita esa nota, ya no aplica". Ahora acepta `memory_type` también. |
| `dark_memory_agent_memory_archive` | "Borra esa nota (soft delete)" |

El agente puede guardar cosas de **tres tipos de alcance**:

- **Sesión** — solo aplica a la sesión actual (cosas tácticas)
- **Proyecto** — aplica a todo el proyecto (decisiones de arquitectura)
- **Operador** — persiste por siempre, asociado a ti (preferencias personales)

Y puede buscar por **tipo de nota** (`note`, `observation`, `decision`,
`finding`, `todo`, `link`, `context`) o por texto libre (búsqueda BM25).

### ⚖️ Juzgar (JUDGE — 3 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_judge` | "Revisa si esto cumple la promesa" |
| `dark_memory_consensus` | "Pregúntale a 5 jueces y dame la mayoría" |
| `dark_memory_judgment_history` | "¿Qué ha dicho el juez antes?" |

### 📜 Políticas (POLICY — 2 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_active_policy` | "¿Cuáles son las reglas que me aplican?" |
| `dark_memory_load_constitution` | "Muéstrame la constitución completa" |

### 👀 Monitorear (OBSERVABILITY — 4 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_health_ping` | "¿Está vivo el servidor?" (latencia <50ms) |
| `dark_memory_memory_state` | "¿Cómo va la base de datos?" |
| `dark_memory_writes` | "Muéstrame los últimos cambios" |
| `dark_memory_anomalies` | "¿Pasó algo raro?" |

### 🛠️ Admin (ADMIN — 3 tools)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_admin_migrate` | "Aplica las migraciones pendientes" |
| `dark_memory_admin_schema_status` | "¿Qué versión de schema tengo?" |
| `dark_memory_admin_vacuum` | "Limpia espacio en disco" |

### 🌀 Estado interno (L6-VLP — 1 tool)

| Herramienta | Cuándo se usa |
|---|---|
| `dark_memory_vlp_handle_event` | "Avanza el ciclo del vibe-loop protocol" |

### 🛡️ Red team (L7-REDTEAM — 3 tools, modo armado)

Si arrancas el servidor con `DARK_REDTEAM=armed`, se activan estas 3
herramientas adicionales para investigación de seguridad:

- `dark_memory_redteam_list_mods`
- `dark_memory_redteam_get_prompts`
- `dark_memory_redteam_log_attempt`

**Solo para investigación de seguridad con autorización.** No las uses en
infraestructura de producción.

---

## Hacer un vibe-loop paso a paso

Este es el flujo más común cuando le pides al agente que haga algo
sustancial (no solo "arregla este typo").

### Paso 1: Abrir sesión

> *"Inicia una sesión para mí. Operador: nico. Proyecto: darkmem."*

El agente llama `dark_memory_session_start` y recibe un `session_id`.

### Paso 2: Crear la promesa

> *"Voy a pedirte que hagas X. Antes de empezar, escribe la promesa."*

El agente llama `dark_memory_vibe_spec` con:
- qué va a hacer (`intent`)
- en qué casos aplica (`vibe_case` — un código C1..C7 que clasifica el trabajo)
- las tareas concretas que va a realizar

La promesa queda guardada. **El juez la va a usar para evaluar la entrega.**

### Paso 3: El agente trabaja

El agente hace lo que tenga que hacer (escribir código, investigar,
diseñar, etc.) usando su propio modelo. Tú no intervienes aquí.

(Opcionalmente, el agente puede ir guardando cosas en su cuaderno con
`dark_memory_agent_memory_save` — hallazgos intermedios, decisiones, etc.)

### Paso 4: Entregar

> *"Ya está. Entrégalo bajo la promesa #N."*

El agente llama `dark_memory_vibe_publish` con el id de la promesa
y la URL del artefacto (código, texto, imagen, lo que sea).

### Paso 5: Juicio automático

> *"Revisa si lo que entregaste cumple la promesa."*

El agente llama `dark_memory_judge(eval_type="drift_judge", ...)` con
el contenido del artefacto. El juez responde uno de tres veredictos:

- **`aligned`** — cumple. Adelante.
- **`drift_detected`** — se desvió. El agente tiene que ver la crítica
  e intentar de nuevo (vuelve al paso 3).
- **`needs_human`** — el juez no está seguro. Te pregunta a ti.

Si quieres más confianza, usa `dark_memory_consensus(n=5)` para que
5 jueces voten y te quedes con la mayoría.

### Paso 6: Tu decisión final (si hubo drift)

Si el juez dijo `drift_detected` y el agente intentó 2-3 veces sin
lograrlo, te pregunta. Tú decides:

- **Aceptar** (`resolve_drift(decision="accept")`) — "OK, sirve aunque
  no sea perfecto, sigamos"
- **Rechazar** (`resolve_drift(decision="reject")`) — "No, esto no
  sirve, intentemos otra cosa"

### Paso 7: Cerrar sesión

> *"Cierra la sesión, todo limpio."*

El agente llama `dark_memory_session_close`. La sesión queda registrada
en el cuaderno.

---

## Cuando algo no funciona

### "El agente dice que las herramientas MCP no están disponibles"

Verifica:
1. Que `bin/dark-mem-mcp.exe` existe y es ejecutable
2. Que la ruta en `opencode.jsonc` es correcta
3. Que reiniciaste el agente después de cambiar la config
4. Que el archivo no quedó bloqueado por otra instancia (en Windows,
  a veces pasa — cierra todas las terminales y vuelve a abrir)

### "El agente no me deja llamar una herramienta porque dice 'session required'"

Casi todas las herramientas necesitan una sesión activa. Pídele al
agente que primero llame `dark_memory_session_start`.

### "El cuaderno está vacío / no encuentro lo que guardé"

Las notas son por **proyecto + sesión**. Si cambiaste de proyecto o
cerraste la sesión sin querer, las notas pueden estar en otro lugar.
Pídele al agente `dark_memory_agent_memory_list(scope="all")` para ver
todo.

### "Hay un schema_version raro en la base de datos"

Si vienes de una versión vieja (< v2.0.0), la base necesita migrar.
Las migraciones se aplican automáticamente al arrancar el servidor,
pero si algo se rompe:

```bash
./bin/dark-mem-cli migrate --status   # ver qué falta
./bin/dark-mem-cli migrate --apply    # aplicar pendientes
```

### "Los tests están fallando en mi máquina pero en CI pasan"

Estás en una máquina corporativa con WDAC o similar (mira
`tests/README.md` para el detalle). El workaround es:
- Confía en el CI como señal autoritativa
- Localmente: `go build ./...` + `go vet ./...` + drift-judge sobre
  el artefacto de la wave

### "Quiero agregar una herramienta nueva"

Lee `CONTRIBUTING.md`. Reglas de oro:
- Respeta el orden canónico (no renumeres los existentes)
- Si agregas una migración: append-only, nunca edites una ya pasada
- Si agregas un invariante: documéntalo en `docs/INVARIANTS.md`
- Si agregas un orchestrator: spec_create + drift_judge antes de merge

---

## Para los curiosos técnicos

Si quieres entender cómo funciona por dentro (MCP, drivers de DB,
invariantes operacionales, formalización del vibe-loop como protocolo
de estado):

- [`docs/`](docs/) — manuales de operación, runbooks, INVARIANTS
- [`vibe-flow/main/DARK_MEMORY_MCP_RFC.md`](vibe-flow/main/DARK_MEMORY_MCP_RFC.md) — el RFC original
- [`vibe-flow/main/BRIDGE_AND_COEXISTENCE.md`](vibe-flow/main/BRIDGE_AND_COEXISTENCE.md) — cómo coexiste con `dark-research-mcp`
- [`CHANGELOG.md`](CHANGELOG.md) — qué cambió en cada versión
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — cómo contribuir

### Estado actual (al cierre de esta versión)

- **Versión**: v2.5.2 (Sprint 3 roadmap: MCPB bundles + carry-forward tests)
- **Schema DB**: v20 (zero migrations across v2.4.x and v2.5.x)
- **Tools canónicos**: 38 (+ 3 en modo armed)
- **Backends**: SQLite (default) + Postgres (research only en este host)
- **Paquetes internos**: 27
- **Suites de test**: 29 distribution tests + 27 total packages
- **Canales de distribución**: GitHub Releases + npm wrapper (`@opitacode/dark-memory-mcp*`) + Official MCP Registry (`io.github.Opita-Code/dark-memory-mcp`) + MCPB bundles for Claude Desktop (`.mcpb`)

---

## Licencia

[MIT](LICENSE). Úsalo, modifícalo, distribúyelo. Si construyes algo bueno
con él, cuéntanos.

---

<div align="center">

Construido con ❤️ desde Neiva, Huila, Colombia por [Opita Code](https://opitacode.com).

*"No construimos software para que se vea bonito en una presentación. Lo construimos para que trabaje contigo todos los días."*

[opitacode.com](https://opitacode.com) · [github.com/Opita-Code](https://github.com/Opita-Code) · [dark-research-mcp](https://github.com/Opita-Code/dark-research-mcp)

</div>