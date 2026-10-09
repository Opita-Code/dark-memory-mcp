<div align="center">

```
╔════════════════════════════════════════════════════════════════════════════════════╗
║                                                                                    ║
║         ██████╗  ██████╗██████╗ ███╗   ███╗      ███╗   ███╗ ██████╗██████╗        ║
║        ██╔═══██╗██╔════╝██╔══██╗████╗ ████║      ████╗ ████║██╔════╝██╔══██╗       ║
║        ██║   ██║██║     ██║  ██║██╔████╔██║      ██╔████╔██║██║     ██████╔╝       ║
║        ██║   ██║██║     ██║  ██║██║╚██╔╝██║      ██║╚██╔╝██║██║     ██╔═══╝        ║
║        ▚██████╔╝╚██████╗██████╔╝██║ ╚═╝ ██║      ██║ ╚═╝ ██║╚██████╗██║            ║
║         ╚═════╝  ╚═════╝╚═════╝ ╚═╝     ╚═╝      ╚═╝     ╚═╝ ╚═════╝╚═╝            ║
║                                                                                    ║
║                     MEMORIA PERSISTENTE PARA AGENTES DE IA                         ║
║                                                                                    ║
╚════════════════════════════════════════════════════════════════════════════════════╝
```

**Dark Memory convierte a tu agente de un amnésico en un colega que recuerda.**

Es un cuaderno que vive en tu disco. Tu agente escribe en él y lo consulta entre
sesiones, con verificación automática de que lo que entregó cumple lo que le
pediste. Sin servidor remoto, sin telemetría, sin costo.

[![MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![MCP tools](https://img.shields.io/badge/MCP-73%20canonical%20tools-blueviolet)](docs/tools.md)
[![Schema](https://img.shields.io/badge/schema-v32-blueviolet)](docs/v4-status.md)
[![Install](https://img.shields.io/badge/install-npx%20%40opita--code%2Fdark--memory--mcp-cc3534)](docs/npm-install.md)

[Qué es](#el-problema-que-resuelve) · [Quickstart](#quickstart) · [Las 73 herramientas](docs/tools.md) · [Vibe-cases](docs/tools.md#vibe-cases) · [Ayuda](docs/npm-install.md) · [Estado real](docs/v4-status.md) · [Contribuir](CONTRIBUTING.md)

> **STATUS: v4-alpha.31** — pre-release. 73 herramientas canónicas, schema 32,
> MIT. Lo que funciona y lo que no está medido en
> **[`docs/v4-status.md`](docs/v4-status.md)** — léelo antes de apostar
> producción por esto.

</div>

---

## El problema que resuelve

```
Lunes:   "Vamos a refactorizar el módulo de autenticación."
         El agente trabaja. Tomas 5 decisiones de arquitectura.
         Cierras la sesión.

Martes:  "Continuemos con el refactor."
         El agente: "¿Qué refactor? ¿Qué módulo? ¿Quién eres?"
         Tienes que explicarle TODO otra vez. 20 minutos perdidos.
```

Con Dark Memory:

```
Lunes:   "Vamos a refactorizar el módulo de autenticación."
         El agente anota: proyecto, decisión #1, decisión #2...
         Todo queda en tu disco.

Martes:  Abres sesión. El agente recibe:
           - Proyecto: dark-memory-mcp
           - Decisiones del lunes: 5 (la #3: "RS256, no HS256")
           - Pendientes: 2
         "OK, ayer elegimos RS256, terminemos los tests."
         0 minutos de re-explicación.
```

Tú no interactúas con Dark Memory. Lo usa tu agente (Claude, opencode, Cursor)
por MCP, como quien consulta un cuaderno de notas.

---

## El vibe-loop

Cierra el círculo entre **lo que pediste** y **lo que entregaron**.

```
Sin supervisión:
  Tú:    "Crea una API que devuelva productos filtrados por categoría."
  Agente: [200 líneas]
  Tú:    "También devuelve los borrados..."
  Agente: "Cierto." [150 líneas más]
  Tú:    "No tiene paginación."
  Agente: "..."  ← iteración sin fin
```

```
Con vibe-loop:
  1. CREAS LA PROMESA (spec)      → dark_memory_vibe_spec
  2. EL AGENTE TRABAJA
  3. ENTREGA EL ARTEFACTO         → dark_memory_vibe_publish
  4. EL JUEZ COMPARA entrega vs promesa:
        "aligned"        → cumple, adelante        ✅
        "drift_detected" → se desvió, reintenta    🔄
        "needs_human"    → no está seguro, te pregunta 🤔
  5. CIERRE — queda registrado qué pediste, qué entregaron, qué dijo el juez
```

No eres tú revisando cada línea. Es el juez evaluando cada entrega contra la
promesa original, y el agente corrige solo.

---

## Quickstart

Necesitas **Node.js 18+** (wrapper npm) o **Go 1.25+** (desde source).

```jsonc
// Agrega esto a la config MCP de tu agente
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"]
    }
  }
}
```

| Agente | Dónde pegarlo |
|---|---|
| **opencode** | `opencode.jsonc` → `mcp.dark-memory` |
| **Claude Code** | `.claude.json` |
| **Claude Desktop** | `claude_desktop_config.json` |
| **Cursor** | `.cursor/mcp.json` |

`npx` descarga el binario de tu sistema la primera vez. **No compilas nada.**

### Verificar que funciona

```
> "Llama a dark_memory_health_ping y dime qué responde."
```

```json
{
  "server": { "version": "4.0.0-alpha.31", "name": "dark-memory-mcp" },
  "db": { "live": true, "schema_version": 32 },
  "registry": { "canonical_tools": 73 }
}
```

`server.version` refleja el stamp de build: si compilaste sin `-ldflags` verás
`dev` en vez de una versión. Eso no es un error.

### Primeros pasos

```
1. "Inicia sesión en dark-memory. Operador: [tu nombre]. Proyecto: mi-proyecto."
2. "Guarda: 'Estamos usando Postgres 16 con uuidv7 como PKs.'"
3. "¿Qué tenemos anotado sobre la base de datos?"
4. "Cierra la sesión."
```

Desde source: `go build -o bin/dark-mem-mcp ./cmd/dark-mem-mcp`
Más detalle por host: **[`docs/npm-install.md`](docs/npm-install.md)**.

---

## Elige tu nivel

**Nivel 1 — el cuaderno.** El agente recuerda entre sesiones. Para el primer día.
`session_start`, `agent_memory_save`, `agent_memory_recall`, `session_close`.

**Nivel 2 — los vibe-loops.** Verifica que lo entregado cumple lo pedido.
`vibe_spec` para prometer, `vibe_publish` para entregar, `pipeline_status` para
el veredicto, `resolve_drift` para aceptar o rechazar.

**Nivel 3 — delegar.** Para trabajos grandes: `delegate_intent` decide si se hace
directo o entre sub-agentes, `mindset_apply` compone sus prompts.

**Nivel 4 — extender.** Agregar herramientas, afinar rúbricas, mejorar búsqueda.
Empieza por [`CONTRIBUTING.md`](CONTRIBUTING.md) → *good first issues*.

---

## Conceptos que verás en el código

| Término | Qué es |
|---|---|
| **MCP** | El protocolo estándar con el que tu agente usa herramientas externas. |
| **Sesión** | Un tramo de trabajo. `session_start` … `session_close`. |
| **Spec** | La promesa: "voy a hacer X con las tareas T1, T2, T3". |
| **Artifact** | Lo entregado: código, texto, imagen, lo que sea. |
| **Drift** | Cuando lo entregado no cumple lo prometido. El juez lo detecta. |
| **Vibe-loop** | El ciclo: spec → trabajo → artifact → juez → reintentar o aceptar. |
| **Agent memory** | El cuaderno persistente del agente. Sobrevive a las sesiones. |
| **Vibe-case** | C1–C8. El tipo de artefacto; decide la rúbrica del juez. |

Catálogo completo: **[`docs/tools.md`](docs/tools.md)**.

---

## Si algo falla

| Síntoma | Causa probable |
|---|---|
| *"Mi agente no ve las herramientas"* | Falta reiniciar el agente tras cambiar la config. En Windows el binario puede quedar bloqueado: cierra todas las terminales. |
| *"Dice 'session required'"* | Casi todas las herramientas necesitan sesión activa. Inicia una primero. |
| *"Guardé cosas y no las encuentro"* | Las notas tienen alcance: por sesión, proyecto u operador. Cambiaste de proyecto sin darte cuenta. |
| *"Error de schema_version"* | Vienes de una versión vieja; la DB necesita migrar. `./bin/dark-mem-cli migrate --apply` |

---

## Lo que todavía no hace

Honesto, porque un README que sólo vende no sirve para decidir:

- **Pre-release.** 73 de las 97 herramientas que `ARCHITECTURE-V4.md` prometió
  — **75%**, no 100%.
- **Phase 21 en 2 de 9 primitivas.** El motor de vibe-loop es una biblioteca
  verificada con el cableado empezado, no un producto terminado. Las 7
  primitivas restantes cambian comportamiento y necesitan
  instrument → measure → decide → flip.
- **Postgres sin probar en anger.** 58 métodos devuelven `notImpl` (0 en SQLite).
  El path de producción es SQLite.
- **Sin consumidores externos todavía.** Lo usa el autor a diario desde 2026-10-08.

Todo esto medido, con fecha y origen, en **[`docs/v4-status.md`](docs/v4-status.md)**.
`internal/tools/v4_status_consistency_test.go` falla el build si ese documento
se contradice con el código que corre, así que no puede pudrirse en silencio.

---

## Cómo funciona por dentro

```
Tu agente (Claude / opencode / Cursor)
    │  MCP (JSON-RPC sobre stdin/stdout)
    ▼
dark-mem-mcp.exe  ←── proceso local, 73+3 herramientas
    │  SQL
    ▼
SQLite (archivo .db en tu disco)   ←── o Postgres con DARK_DRIVER=postgres
```

- **Schema DB:** 32, entero en `schema_migrations.version`. 66 tablas en la
  instancia viva, contando sombras de FTS5.
- **Dependencias externas:** ninguna en runtime. Go stdlib + SQLite embebido.
- **Tests:** el número autoritativo es el de CI, no el de este README. Un
  número copiado en un README es exactamente lo que este proyecto dejó de hacer.

---

## Contribuir

MIT. Aceptamos contribuciones de todo nivel.

1. [`CONTRIBUTING.md`](CONTRIBUTING.md) — las reglas de casa
2. [§10 Keeping published claims honest](CONTRIBUTING.md#10-keeping-published-claims-honest)
   — la regla que este proyecto aprendió por las malas
3. Toda contribución pasa por vibe-loop: spec → artifact → drift_judge → merge

**Principios de diseño:**
- **Append-only**: las migraciones nunca se editan, solo se agregan
- **Orden canónico**: las herramientas tienen un orden fijo que no se renumera
- **Best-effort**: VLP, sweepers y telemetría nunca bloquean la operación principal
- **Sin hardcoding**: una sola fuente de verdad para cada número, nombre y versión

---

<div align="center">

Construido con ❤️ desde Neiva, Huila, Colombia por [Opita Code](https://opitacode.com).

*"No construimos software para que se vea bonito en una presentación. Lo construimos para que trabaje contigo todos los días."*

[opitacode.com](https://opitacode.com) · [github.com/Opita-Code](https://github.com/Opita-Code) · [dark-research-mcp](https://github.com/Opita-Code/dark-research-mcp)

</div>
