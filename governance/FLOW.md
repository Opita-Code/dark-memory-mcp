# Integrar dark-memory en un flujo de desarrollo

> **TL;DR**: dark-memory da a un flujo de desarrollo tres cosas que una
> sesión de chat no tiene: una promesa escrita **antes** del trabajo, un juez
> que lee los bytes entregados y un rastro que sobrevive a la sesión. Este
> documento describe cómo encajan esas tres piezas, qué garantiza cada capa y
> qué huecos debe conocer quien llama.
>
> Todo lo de aquí está medido contra `dark-memory-mcp v4.0.0-alpha.33`
> (commit `ba1c29e`). Los números son observaciones, no recuerdos.

La política que gobierna el flujo es la constitution del workspace
(`workspace-policy.constitution.json`, que vive en el workspace del proyecto y
aquí se versiona en su forma canónica
[`workspace-policy.constitution.example.json`](./workspace-policy.constitution.example.json)).

---

## 1. El flujo

```
  ┌─ SESIÓN (una unidad de trabajo) ────────────────────────────────────┐
  │                                                                      │
  │  1. session_start ──► liga proyecto + constitution_id@version         │
  │  2. recall ────────► lo que ya sabemos (memoria antes de búsqueda)    │
  │  3. vibe_spec ─────► la PROMESA: intent + tareas + constitution       │
  │  4. [trabajo] ─────► el agente escribe el artefacto                   │
  │  5. vibe_publish ─► ENTREGA al juez (artifact_ref obligatorio)        │
  │  6. veredicto ─────► aligned | drift_detected | needs_human           │
  │  7. memory_save ───► la decisión durable (kind=decision, pinned)      │
  │  8. session_close ─► queda el rastro                                 │
  │                                                                      │
  └──────────────────────────────────────────────────────────────────────┘
              │                                    │
              │ constitution (SHA-pineada)         │ frame de persona
              │                                   │ (inyectado en
              ▼                                   ▼ cada recall)
        integridad + procedencia          la regla llega al agente
```

Cada capa hace un trabajo distinto y **ninguna sustituye a otra**.

---

## 2. Qué garantiza cada capa (medido, no supuesto)

| Capa | Mecanismo | Qué garantiza | Cómo se verificó |
|---|---|---|---|
| **Integridad de la política** | Watchdog de SHA (`internal/store/sqlite/store.go`, `runWatchdog`) | Editar la constitution sin subir la versión ⇒ el store **no abre**. Fail closed. | `active_policy` informa `constitution_drift: false`; el SHA-256 del fichero coincide con el pineado. |
| **La regla llega al agente** | `PersonaFrame` en `dark_memory_recall` (`internal/recall/assemble.go`, `parsePersonaFromConstitution`) | El `voice` y el `claims_policy` de la constitution se inyectan en **cada** recall. | El recall devolvió el texto WS-1…WS-6 en `frames.persona`. |
| **La promesa precede al trabajo** | `vibe_spec` | Existe una intención **antes** de que exista el artefacto. | Los specs 20 y 25 llevaban intent + tareas + constitution. |
| **El juez lee los bytes reales** | `artifact_ref` + `drift_judge` | El juez puntúa los **bytes del fichero**, no la descripción que da el agente. | `aligned` conf 1.00 para un artefacto conforme; `drift_detected` conf 1.0 para un texto que apuntaba a `/tmp/build` — el juez citar la ruta. |
| **Procedencia** | `write_audit` + `constitution_id@version` | Cada write queda atribuido a la política en vigor. | `session_close` informa de un `writes_total` para la sesión. |
| **Continuidad** | `agent_memory` + `ContextRecap` | La sesión siguiente arranca sabiendo lo que se decidió. | `session_start` devolvió un recap con el todo abierto. |
| **Nada falla en silencio** | Error Observatory (41 puntos de llamada a `RecordError` en `internal/orchestration`) | Cada error queda registrado y es consultable. | `error_summary` mostró los recuentos por dominio y severidad. |

---

## 3. Dónde está el presupuesto

El juez es la parte cara: una llamada al LLM por publicación. Todo lo demás
es una lectura local de SQLite. Reglas prácticas:

- **Publica evidencia, no el binario.** El juez lee bytes; un diff o un
  informe de verificación cuesta menos tokens que el artefacto entero.
- **Un spec por entregable.** Reutilizar una intención vieja contra un
  artefacto nuevo produce `drift_detected` espurios.
- **No reintentes un `needs_human`.** Significa que el juez no pudo decidir;
  decide el operador. Reintentar gasta tokens para obtener el mismo no.
- **El veredicto viene de un solo juez.** La respuesta lo dice ella misma
  (`confidence_caveat: "single-judge measurement … correlated, not
  independent review"`). Para decisiones de riesgo, usa `consensus` con n≤7 en
  vez de confiar en la suerte.

---

## 4. Dónde vive el flujo

| Capa | Dónde | Por qué |
|---|---|---|
| Reglas | `governance/workspace-policy.constitution.json` (workspace del operador; aquí versionada como `.example.json`) | Política del operador, por proyecto: SHA-pineada, viviendo en el workspace del proyecto (WS-4). No es un default del producto — mira `constitutions/` en [ARCHITECTURE-V4.md](../ARCHITECTURE-V4.md) para lo que sí shippea el producto. |
| Orquestación | `governance/dm-flow.sh` | Valida las reglas antes de gastar ningún token y luego emite el plan de llamadas. |
| Decisiones | la base SQLite de dark-memory | Cruza sesiones y proyectos. |
| Rastro | `write_audit` | Un write, una fila. |

El script **no habla MCP por sí mismo**. El primer prototipo levantaba su
propio servidor MCP y le hablaba por FIFOs: se colgaba cuando la respuesta no
llegaba, y un script colgado tumba con él la sesión del harness. Un gate de
desarrollo no puede ser el componente que cuelga el flujo.

Así que `dm-flow.sh` hace dos cosas, y solo dos:

1. **Pre-flight** (barato, antes del trabajo caro): ¿está la constitution en
   el workspace? ¿está el artefacto dentro del workspace? ¿existe?
2. **Plan**: las llamadas MCP exactas, con los argumentos ya resueltos, para
   que las ejecute el agente con sus propias tools — donde el gate del
   servidor las valida igualmente.

```bash
# ¿esto respeta las reglas? (exit 0 = sí, 65/66 = no, con el motivo)
governance/dm-flow.sh check  dark-memory-mcp 25 --artifact "$RUTA"

# el plan de llamadas, con los argumentos resueltos
governance/dm-flow.sh plan   dark-memory-mcp 25 --artifact "$RUTA"
```

---

## 5. Huecos que la integración debe conocer

Verificados en esta máquina, no inferidos.

### 5.1 `vibe_spec` y `vibe_publish` no comparten `spec_id`

`vibe_publish` **crea su propia fila** en `vibe_specs`
(`internal/orchestration/publish_vibe.go:265` → `SaveSpec`). Publicar un
artefacto con un `spec.spec` reenviado produce un spec **nuevo** (observado:
el spec 20 abierto produjo los specs 21, 22 y 23).

Consecuencia: los `todos` que crea `vibe_spec` **nunca** los archiva el
publish, porque el publish no apunta al spec original.

**Mitigación aplicada**: `dm-flow.sh` no depende del auto-cierre. El paso 7
del plan es un `agent_memory_save` explícito. El auto-cierre sí existe
(`auto_save_decision: true` funciona: creó una fila de decisión con
`prov=judge-minimax`), pero **no** cierra el spec abierto.

### 5.2 `auto_save_todos` no está expuesto por MCP

El campo existe en el orquestador (`vibe_spec.go:80`) pero **no** en el
schema de la tool `dark_memory_vibe_spec`. Confirmado buscando el parámetro
en el namespace y con un spec de una tarea que no creó ningún todo. El
auto-archivado de todos es inalcanzable desde MCP.

### 5.3 Los mods no se cargan al arrancar

`mods_total: 0`, un `mod_loads` vacío, y `SaveMod` no se llama nunca durante
el arranque. El mod `lucidez-operator-risk` del repo **no** se carga. Un mod
escrito hoy es decorativo: los dos vectores de enforcement vivos son el
watchdog de SHA y el frame de persona.

### 5.4 Una constitution en TOML nunca llega al agente

El watchdog guarda los bytes crudos del fichero en `constitutions.parsed_json`
y el inyector los parsea con `json.Unmarshal`. Las constitutions del repo son
TOML: se guardan bien, pero su sección `[persona]` **nunca** se inyecta. Por
eso la política del workspace es JSON.

### 5.5 El gate protege la memoria, no el disco

`ErrFrameStaleTooFar` significa "no hay sesión activa". El gate **no**
valida dónde se escribieron los ficheros. Lo que protege el disco son
WS-2/WS-3, y por eso el pre-flight de `dm-flow.sh` los valida explícitamente:
es la capa que el gate no cubre.

---

## 6. Qué NO automatizar

- **`needs_human`.** El juez no sabe; decide el operador.
- **Los cambios de propósito.** Si el artefacto cambió *qué* hace y no *cómo*
  lo hace, escribe un spec nuevo. Forzar el artefacto contra una intención
  vieja produce un veredicto de drift correcto y tiempo perdido.
- **El cierre de sesión.** Es una línea de código y es lo que hace que la
  sesión siguiente arranque sabiendo. Sin él el trabajo se pierde — que es
  exactamente el problema que dark-memory existe para resolver.
