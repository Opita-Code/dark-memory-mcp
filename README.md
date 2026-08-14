<div align="center">

```
╔════════════════════════════════════════════════════════════════════════════════════╗
║                                                                                    ║
║         ██████╗  ██████╗██████╗ ███╗   ███╗      ███╗   ███╗ ██████╗██████╗        ║
║        ██╔═══██╗██╔════╝██╔══██╗████╗ ████║      ████╗ ████║██╔════╝██╔══██╗       ║
║        ██║   ██║██║     ██║  ██║██╔████╔██║      ██╔████╔██║██║     ██████╔╝       ║
║        ██║   ██║██║     ██║  ██║██║╚██╔╝██║      ██║╚██╔╝██║██║     ██╔═══╝        ║
║        ╚██████╔╝╚██████╗██████╔╝██║ ╚═╝ ██║      ██║ ╚═╝ ██║╚██████╗██║            ║
║         ╚═════╝  ╚═════╝╚═════╝ ╚═╝     ╚═╝      ╚═╝     ╚═╝ ╚═════╝╚═╝            ║
║                                                                                    ║
║                     MEMORIA PERSISTENTE PARA AGENTES DE IA                         ║
║                                                                                    ║
║     El cuaderno que tu agente no olvida. Sesión tras sesión, proyecto tras         ║
║     proyecto. Con verificación automática de que lo entregado cumple lo pedido.    ║
║                                                                                    ║
╚════════════════════════════════════════════════════════════════════════════════════╝
```

**Memoria persistente · Vibe-Loop Engine · Gobernanza de agentes · MCP**

[![MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![MCP tools](https://img.shields.io/badge/MCP-53%20canonical%20tools-blueviolet)](#las-53-herramientas)
[![Schema](https://img.shields.io/badge/schema-v26-success)](#arquitectura)
[![Install](https://img.shields.io/badge/install-npx%20%40opita--code%2Fdark--memory--mcp-cc3534)](docs/npm-install.md)
[![Backends](https://img.shields.io/badge/backends-sqlite%20%7C%20postgres-blue)](#arquitectura)

[¿Qué es esto?](#qué-es-esto) · [El problema que resuelve](#el-problema-que-resuelve) · [El vibe-loop](#el-vibe-loop-un-paradigma-nuevo) · [Conceptos clave](#conceptos-clave) · [Quickstart](#quickstart) · [Las 53 herramientas](#las-53-herramientas) · [Camino de aprendizaje](#camino-de-aprendizaje) · [Resolver problemas](#resolver-problemas) · [Contribuir](#contribuir)

</div>

---

## ¿Qué es esto?

Imagina que trabajas con un asistente de IA. Le pides tareas, te entrega resultados. Es brillante, pero tiene un defecto: **cuando cierras la conversación, lo olvida todo**. Al día siguiente tienes que volver a explicarle quién eres, qué proyecto llevas, qué decisiones tomaste ayer, qué intentaste y no funcionó.

**Dark Memory** es el cuaderno de ese asistente. Un archivo que vive en tu computadora y al que tu agente puede escribir y consultar cuando quiera, desde cualquier sesión.

### ¿Cómo se conecta?

Tu agente (Claude, opencode, Cursor, etc.) se comunica con Dark Memory a través del protocolo **MCP** (_Model Context Protocol_) — un estándar abierto para que las IAs usen herramientas externas. Dark Memory aparece como un conjunto de herramientas que el agente puede invocar:

```
Tú le hablas al agente  →  El agente usa dark_memory_*  →  Dark Memory guarda/recupera
```

Tú no interactúas directamente con Dark Memory. Es tu agente quien lo usa, como quien consulta un cuaderno de notas.

### ¿Para quién es?

Para cualquiera que use agentes de IA para trabajo serio: desarrollo de software, investigación, diseño, escritura, análisis de datos. Si tu agente hace más que conversación casual, Dark Memory le da memoria.

---

## El problema que resuelve

### Sin Dark Memory

```
Lunes:   "Vamos a refactorizar el módulo de autenticación."
         El agente trabaja. Tomas 5 decisiones de arquitectura.
         Cierras la sesión.

Martes:  "Continuemos con el refactor."
         El agente: "¿Qué refactor? ¿Qué módulo? ¿Quién eres?"
         Tienes que explicarle TODO otra vez. 20 minutos perdidos.
```

### Con Dark Memory

```
Lunes:   "Vamos a refactorizar el módulo de autenticación."
         El agente anota en Dark Memory: proyecto, decisión #1, decisión #2, ...
         Todo queda guardado en tu disco duro.

Martes:  Abres sesión. Dark Memory le devuelve al agente:
           - Proyecto: dark-memory-mcp
           - Decisiones del lunes: 5 (la #3 dice "usar RS256 en vez de HS256")
           - Tareas pendientes: 2 ("escribir tests de integración", "actualizar docs")
         El agente: "OK, ayer decidimos RS256, terminemos los tests."
         0 minutos de re-explicación.
```

**Dark Memory convierte a tu agente de un amnésico en un colega que recuerda.**

---

## El vibe-loop: un paradigma nuevo

El vibe-loop es una forma de trabajar con agentes que cierra el círculo entre **lo que pediste** y **lo que entregaron**. Es la práctica central que Dark Memory habilita.

### El problema del agente sin supervisión

```
Tú:    "Crea una API que devuelva productos filtrados por categoría."
Agente: [escribe 200 líneas de código]
Tú:    "Esto devuelve productos borrados también..."
Agente: "Cierto, lo arreglo." [otras 150 líneas]
Tú:    "Ahora no tiene paginación..."
Agente: "..." [iteración sin fin]
```

El agente nunca sabe por sí solo si se desvió de lo pedido. Tú haces de control de calidad manual.

### Cómo lo resuelve el vibe-loop

```
1. CREAS LA PROMESA (spec)
   "Voy a construir X, con estas tareas: T1, T2, T3."
   → dark_memory_vibe_spec

2. EL AGENTE TRABAJA
   Escribe código, investiga, diseña. Guarda hallazgos en el cuaderno.

3. ENTREGA EL ARTEFACTO (artifact)
   "Aquí está lo que hice, bajo la promesa #42."
   → dark_memory_vibe_publish

4. EL JUEZ REVISA AUTOMÁTICAMENTE (drift_judge)
   Compara lo entregado contra lo prometido:
   - "aligned"     → Cumple. Adelante. ✅
   - "drift_detected" → Se desvió. El agente ve la crítica y reintenta. 🔄
   - "needs_human" → El juez no está seguro. Te consulta a ti. 🤔

5. CIERRE
   Todo queda registrado: qué pediste, qué entregaron, qué dijo el juez.
```

El juez es automático. No eres tú revisando cada línea; es Dark Memory evaluando cada entrega contra la promesa original. **El agente recibe retroalimentación inmediata** y corrige solo, sin que tú intervengas en cada iteración.

Es como tener un revisor de código automático que nunca se cansa y nunca olvida lo que se pidió.

---

## Conceptos clave

Estos términos aparecen en el código, las herramientas y la documentación. Tenerlos claros desde el principio ahorra fricción.

| Término | Qué significa |
|---|---|
| **MCP** (_Model Context Protocol_) | El protocolo estándar que permite a los agentes de IA usar herramientas externas. Dark Memory se conecta a tu agente vía MCP. |
| **Agente** | El asistente de IA con el que trabajas (Claude, opencode, Cursor, etc.). |
| **Sesión** | Un período de trabajo. Empiezas con `session_start`, terminas con `session_close`. |
| **Spec** | La promesa: "voy a hacer X con las tareas T1, T2, T3". |
| **Artifact** (artefacto) | Lo que el agente entrega: código, texto, imagen, lo que sea. |
| **Drift** (desviación) | Cuando lo entregado no cumple lo prometido. El juez lo detecta. |
| **Vibe-loop** | El ciclo completo: spec → trabajo → artifact → juez → (reintentar o aceptar). |
| **Agent memory** | El cuaderno personal del agente: notas, decisiones, observaciones, tareas pendientes. Persiste entre sesiones. |
| **VLP** (_Vibe-Loop Protocol_) | La máquina de estados que gobierna el ciclo del vibe-loop: idle → drafting_spec → spec_active → drift_judging → complete. |
| **Constitución** | Las reglas que el agente debe respetar. Define qué se considera "alineado" y qué no. |

---

## Quickstart

Necesitas **Node.js 18+** (para el wrapper npm) o **Go 1.25+** (para compilar desde source).

### Opción A — npm wrapper (recomendado)

Agrega esto a la configuración MCP de tu agente:

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

`npx` descarga el binario correcto para tu sistema operativo la primera vez. Las siguientes usan caché. **No necesitas compilar nada.**

- **opencode**: pega el bloque en `opencode.jsonc` → `mcp.dark-memory`
- **Claude Code**: pega en `.claude.json` o `/Users/tu/.claude.json`
- **Claude Desktop**: pega en `claude_desktop_config.json`
- **Cursor**: pega en `.cursor/mcp.json`

Detalles por host en [`docs/npm-install.md`](docs/npm-install.md).

### Opción B — descargar el binario

Ve a [Releases](https://github.com/Opita-Code/dark-memory-mcp/releases), descarga el binario para tu sistema y apunta tu MCP host a la ruta absoluta.

### Opción C — compilar desde source

```bash
git clone https://github.com/Opita-Code/dark-memory-mcp.git
cd dark-memory-mcp
go build -o bin/dark-mem-mcp ./cmd/dark-mem-mcp
```

### Verificar que funciona

Una vez configurado, dile a tu agente:

> "Llama a dark_memory_health_ping y dime qué responde."

Debe mostrar algo como:

```json
{
  "server": { "version": "2.13.0", "name": "dark-memory-mcp" },
  "db": { "live": true, "schema_version": 25 },
  "registry": { "canonical_tools": 52 }
}
```

### Primeros pasos

```text
1. "Inicia una sesión de dark-memory. Operador: [tu nombre]. Proyecto: mi-proyecto."
   → El agente llama a dark_memory_session_start

2. "Guarda esto en el cuaderno: 'Estamos usando Postgres 16 con uuidv7 como PKs.'"
   → El agente llama a dark_memory_agent_memory_save

3. "¿Qué tenemos anotado sobre la base de datos?"
   → El agente llama a dark_memory_agent_memory_recall

4. "Cierra la sesión."
   → El agente llama a dark_memory_session_close
```

---

## Las 53 herramientas

Dark Memory expone **52 herramientas** (más 3 extras en modo investigación). El agente las invoca con el prefijo `dark_memory_`. Están agrupadas por 16 oficios:

### Sesión (7 tools)
`session_start` · `session_resume` · `session_heartbeat` · `session_status` · `session_close` · `session_recover` · `session_resurrect`

Abrir, mantener viva, recuperar y cerrar sesiones de trabajo.

### Investigación (3 tools)
`research_topic` · `research_recall` · `research_resume_thread`

Buscar en la web y recordar investigaciones previas.

### Self-bootstrap (3 tools)
`agent_bootstrap` · `agent_recommend_companions` · `agent_detect_environment`

El servidor se documenta a sí mismo. El agente puede leer el manual completo sin docs externos.

### Vibe-loop (4 tools)
`vibe_spec` · `vibe_publish` · `pipeline_status` · `resolve_drift`

Crear promesas, entregar artefactos, verificar drift, aceptar o rechazar.

> **Nuevo en v2.13.0:** `vibe_publish` y `vibe_spec` ahora avanzan automáticamente la máquina de estados del VLP. El agente ya no necesita llamar `vlp_handle_event` manualmente después de cada entrega.

### Contexto (4 tools)
`artifact_context` · `spec_context` · `session_context` · `recall`

Ver el estado de artefactos, specs, sesiones y cambios recientes.

### Cuaderno del agente (10 tools)
`agent_memory_save` · `agent_memory_list` · `agent_memory_recall` · `agent_memory_get` · `agent_memory_update` · `agent_memory_archive` · `agent_memory_delegate` · `agent_memory_entities` · `subagent_register` · `subagent_unregister`

El cuaderno persistente. Guardar notas, decisiones, hallazgos, tareas. Buscar por texto (BM25). Delegar contexto a sub-agentes.

### Juez (3 tools)
`judge` · `consensus` · `judgment_history`

Evaluar artefactos contra specs. Con rubricas G-Eval por tipo de trabajo (código, texto, imagen...). `consensus(n=5)` para decisiones de alto riesgo.

### Mente y delegación (2 tools)
`mindset_apply` · `delegate_intent`

Componer system prompts para sub-agentes. Decidir si un trabajo se hace directo, se delega, o se rechaza.

### Políticas (2 tools)
`active_policy` · `load_constitution`

Consultar las reglas activas y la constitución del proyecto.

### Observabilidad (4 tools)
`health_ping` · `memory_state` · `writes` · `anomalies`

Monitorear salud del servidor, estado de la base de datos, auditoría de escrituras.

### Error Observatory (4 tools)
`error_summary` · `error_list` · `error_get` · `error_resolve`

Backlog de errores clasificados por dominio y severidad. Triage operador.

### Admin (3 tools)
`admin_migrate` · `admin_schema_status` · `admin_vacuum`

Migraciones de schema, estado, limpieza de disco.

### VLP (1 tool)
`vlp_handle_event`

Manejar manualmente la máquina de estados del vibe-loop (el agente normalmente no necesita llamarlo; las herramientas de vibe-loop auto-avanzan el estado desde v2.13.0).

### Embedder (1 tool)
`embedder_setup_prompt`

Consentimiento único para búsqueda híbrida vectorial.

### Red team (3 tools, modo armado)

Herramientas de investigación de seguridad. Solo disponibles con `DARK_REDTEAM=armed`.

---

## Camino de aprendizaje

Dark Memory se aprende por capas. No necesitas entender todo para empezar.

### Nivel 1 — Usar el cuaderno

El agente recuerda entre sesiones. Ideal para tu primer día.

```text
"Guarda esto en dark-memory: 'El endpoint /api/users necesita rate limiting.'"
"¿Qué decisiones tomamos ayer sobre la base de datos?"
"Anota como tarea pendiente: escribir tests para el módulo de auth."
```

Herramientas que necesitas: `session_start`, `agent_memory_save`, `agent_memory_recall`, `session_close`.

### Nivel 2 — Hacer vibe-loops

Verificar que lo que el agente entrega cumple lo que pediste.

```text
"Voy a pedirte que crees una función de validación de emails.
 Antes de empezar, crea un vibe_spec con las reglas: debe aceptar
 emails con +, debe rechazar dominios sin TLD, debe ser O(1)."
 
[el agente trabaja]

"Entrega el código bajo la spec #X y pídele al juez que lo revise."
```

Herramientas que necesitas: las del nivel 1 + `vibe_spec`, `vibe_publish`, `judge`.

### Nivel 3 — Delegar a sub-agentes

Para trabajos grandes, el agente principal divide y coordina.

```text
"Este refactor involucra 4 módulos. Usa delegate_intent para decidir
 si lo haces tú solo o divides el trabajo entre sub-agentes."
 
[el agente evalúa, decide delegar, compone mindsets, registra bindings]
 
"Cada sub-agente entrega su parte. Consolida y haz vibe_publish final."
```

Herramientas que necesitas: las del nivel 2 + `delegate_intent`, `mindset_apply`, `agent_memory_delegate`.

### Nivel 4 — Contribuir a Dark Memory

Cuando entiendes el paradigma, puedes extenderlo. Agregar una herramienta, afinar las rúbricas de evaluación, mejorar el motor de búsqueda.

**Buen primer PR:** [`CONTRIBUTING.md`](CONTRIBUTING.md) tiene issues etiquetados `good-first-issue`. Tres sugerencias de entrada:
- Traducir errores y mensajes (i18n/l10n)
- Agregar un backend de búsqueda (Weaviate, Qdrant, pgvector)
- Escribir tests para un paquete con cobertura baja (`internal/recall/`, `internal/entity/`)

---

## Resolver problemas

### "Mi agente no ve las herramientas de dark-memory"

1. ¿El binario existe en la ruta que pusiste en la config? Verifícalo con `ls` o `dir`.
2. ¿Reiniciaste el agente después de cambiar la configuración MCP? Sin restart, no se recarga.
3. En Windows, a veces el binario queda bloqueado. Cierra TODAS las terminales y vuelve a abrir.

### "Dice 'session required' y no me deja hacer nada"

La mayoría de las herramientas necesitan una sesión activa. Pídele al agente:

```text
"Inicia una sesión de dark-memory primero: operador [tu nombre], proyecto default."
```

### "Guardé cosas pero ahora no las encuentro"

Las notas tienen alcance: por sesión, por proyecto, o por operador. Si cambiaste de proyecto sin darte cuenta, las notas están en el otro. Usa:

```text
"Búscame en dark-memory todo lo que tengo anotado, sin filtrar por proyecto."
```

### "Me sale un error de schema_version"

Si vienes de una versión vieja, la base de datos necesita migrar. Normalmente es automático al arrancar. Si falla:

```bash
./bin/dark-mem-cli migrate --status   # ver qué falta
./bin/dark-mem-cli migrate --apply    # aplicar migraciones
```

---

## Arquitectura

Dark Memory corre como un proceso local. No hay servidor remoto, no hay telemetría, no hay costo.

```
Agente (Claude / opencode / Cursor)
    │
    │  MCP (JSON-RPC sobre stdin/stdout)
    │
    ▼
dark-mem-mcp.exe  ←──  proceso local, 52+3 herramientas
    │
    │  SQL (database/sql)
    │
    ▼
SQLite (archivo .db en tu disco)  ←── o Postgres si configuras DARK_DRIVER=postgres
```

- **Versión actual:** v2.13.0
- **Schema DB:** v25 (error_events, vibe-loop state, agent_memory con BM25)
- **Dependencias externas:** ninguna en runtime. Solo Go stdlib + SQLite embebido.
- **Tests:** 29 suites de integración + 27 paquetes con test.

---

## Contribuir

Dark Memory es open-source (MIT). Aceptamos contribuciones de todo nivel.

1. Lee [`CONTRIBUTING.md`](CONTRIBUTING.md) — las reglas de casa
2. Busca issues con label `good-first-issue`
3. Toda contribución pasa por vibe-loop: spec → artifact → drift_judge → merge

**Principios de diseño:**
- **Append-only**: las migraciones de schema nunca se editan, solo se agregan
- **Orden canónico**: las herramientas tienen un orden fijo que no se renumera
- **Best-effort**: los componentes auxiliares (VLP, sweepers, telemetría) nunca bloquean la operación principal
- **Sin hardcoding**: una sola fuente de verdad para cada número, nombre y versión

---

<div align="center">

Construido con ❤️ desde Neiva, Huila, Colombia por [Opita Code](https://opitacode.com).

*"No construimos software para que se vea bonito en una presentación. Lo construimos para que trabaje contigo todos los días."*

[opitacode.com](https://opitacode.com) · [github.com/Opita-Code](https://github.com/Opita-Code) · [dark-research-mcp](https://github.com/Opita-Code/dark-research-mcp)

</div>
