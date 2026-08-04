# Delegación en dark-memory — Síntesis Arquitectónica

**Fecha:** 2026-08-04  
**Autor:** dark-agent (sesión `sess-ffaa09495ad20596`)  
**SOTA referencial:** `vibe-flow/main/DELEGATION_SOTA.md` (hermano, 11 fuentes)

---

## 0. Por qué la síntesis superficial es insuficiente

El SOTA de la industria (Codex, LangGraph, CrewAI, OpenAI Agents SDK) trata la delegación como
**prevención de context rot**: "el orquestador se llena de ruido → compacta (lossy) → degrada → mejor
delegar para que los hijos carguen con el ruido."

dark-memory NO tiene ese problema de base. dark-memory ya resolvió la persistencia del contexto
con una arquitectura de tres capas: `agent_memory` (proyecto), `vibe_frames` (sesión),
`research_items` (conocimiento externo). Hacer una síntesis que ignore esto y diga "apliquemos
los patrones de la industria" es ignorar el 80% del valor arquitectónico que dark-memory ya
entrega.

**Esta síntesis parte de la tesis inversa:** ¿cómo cambia el problema de la delegación cuando
la memoria es persistente, recuperable, y sobrevive a la compactación y al cierre de sesión?

---

## 1. Lo que dark-memory ya resuelve (y por qué importa para delegar)

### 1.1 El modelo mental equivocado

```
SOTA:   Orquestador ──[pasa contexto EN EL PROMPT]──► Subagente
                          ↑
                    pérdida por compactación
                    pérdida al cerrar sesión
                    pérdida entre delegaciones
```

```
dark-memory:  Orquestador ──[cura qué memorias heredar]──► Subagente
                      │                                        │
                      ▼                                        ▼
              agent_memory_save()                     agent_memory_recall()
              agent_memory_list()                     agent_memory_save()
                      │                                        │
                      └──────── DB compartida (project scope) ──┘
```

La diferencia es fundacional: **el sustrato del handoff no es el prompt — es la base de datos.**

### 1.2 Capacidades existentes que la síntesis SOTA ignora

| Problema SOTA | Solución SOTA | Qué dark-memory YA tiene | Ventaja arquitectónica |
|---|---|---|---|
| Contexto perdido tras compactación | `encrypted_content` (Codex), sliding window | `agent_memory_save` + `agent_memory_recall` (BM25) — el registro persiste en DB, no en el chat | Compactar no destruye nada. Recuperas lo que necesitas bajo demanda, en <100ms. |
| Subagente arranca ciego | Prompt injection del historial del padre | `agent_memory_delegate` — curated view de pinned + todos, max 8K tokens budgeted. `agent_memory_recall` independiente. | El subagente no hereda historial crudo — hereda una *vista curada* del proyecto + capacidad de recall autónomo. |
| Conocimiento entre sesiones | Nada — sesión muerta = contexto muerto | `agent_memory_list(scope=project)` + `session_resurrect` + scope inheritance | Proyectos acumulan conocimiento. Las sesiones son *ventanas transitorias* sobre una memoria persistente. |
| Handoff entre agentes | JSON estructurado en el prompt (500-2000 tokens) | El subagente guarda findings en `agent_memory` (row persistente, no mensaje efímero). El orquestador *recupera* (no recibe). | El handoff sobrevive al cierre del subagente. El orquestador puede consultarlo en cualquier momento, no solo al recibir. |
| Tool hallucination | Capability grants en el prompt | `policy/gate.go` + `CapabilitiesFrame` — el LLM nunca ve tools no concedidas | Defense-in-depth: el gate rechaza antes de que el LLM pueda alucinar |
| Verificación de output | Drift_judge al final | `drift_judge` sincrónico + `vibe_publish` + `vlp_handle_event` | Cada artefacto es evaluado contra constitución + spec. La delegación añade *otro* punto de verificación. |

### 1.3 La tesis arquitectónica

> **En dark-memory, delegar no es "pasar contexto en el prompt." Delegar es:**
> 1. **CURAR** qué subconjunto de `agent_memory` hereda el subagente.
> 2. **ARMAR** el mindset correcto para la tarea (rol, goal, constraints, tools).
> 3. **AISLAR** al subagente (C2) para que no contamine el `ContextRecap` del principal.
> 4. **RECUPERAR** los hallazgos del subagente vía recall, no vía mensaje de retorno.
> 5. **SINTETIZAR** desde la memoria persistente del subagente, no desde el chat history.

---

## 2. El DelegationRouter — arquitectura en 5 etapas

### 2.1 Etapa 1: DECIDE (A1 puro — Memory decides)

```
Inputs:  vibe_case (C1..C7)
         task_description
         IdentityFrame  (sesión, operador, constitución)
         ScopeFrame     (spec abierto, tareas, evidencia vinculada)
         CapabilitiesFrame (tools concedidas)

Proceso: 1. Reglas deterministas por vibe_case (ver §3)
         2. Si el caso es CONDICIONAL → LLM acotado a menú fijo
         3. Si el caso es SIEMPRE → decisión sin LLM

Output:  DelegationDecision{
           handler: HANDLE | DELEGATE | REFUSE,
           reason:  string,
           plan:    nil | []SubTask
         }
```

Las reglas deterministas por caso (ver §3) cubren ~70% de las decisiones sin llamada LLM.
El LLM acotado se reserva para casos condicionales (C1 code, C2 text) donde la complejidad
de la tarea determina si delegar o no.

### 2.2 Etapa 2: PLAN (si DELEGATE — supervisor pattern P3)

```
Inputs:  task_description + ScopeFrame

Proceso: 1. Descomponer en subtasks
         2. Asignar vibe_case a cada subtask (puede diferir del padre)
         3. Topological batching por depends_on
         4. ≤5 subtasks (regla anti-sobre-descomposición)

Output:  Plan{
           subtasks: [{
             id, task, vibe_case,
             depends_on: [],
             model_floor,  // resuelto por rol
           }]
         }
```

**Ejemplo:** C7 mixed (campaña = imagen + texto + landing page)
```
Plan:
  batch 0 (parallel):
    t1: generar imagen (C3) — depende de: nada
    t2: redactar copy (C2) — depende de: nada
    t3: código de landing (C1) — depende de: copy (t2) para datos
  → t1 y t2 en batch 0, t3 en batch 1
```

### 2.3 Etapa 3: MIND (por subtask — engine de personas P8)

```
Inputs:  subtask.vibe_case + subtask.task

Proceso: mindset_apply(vibe_case, task_description)
         → composición procedural + judge validation (5 criterios)
         → cache en agent_memory (kind=context, tags=mindset-cache, TTL 1h)

Output:  Mindset{
           system_prompt,     // "You are a senior {role}..."
           tools_recommended, // ["Read", "Grep", "Glob", "Bash"]
           model_recommended, // "sonnet" | "haiku" | "opus"
           category_used     // "c1/security-review" | "c2/marketing-copy"
         }
```

Esta etapa YA está implementada (v2.7.0, 778 LOC). El router la consume, no la reimplementa.

### 2.4 Etapa 4: CURATE (por subtask — handoff vía memoria, no vía prompt)

```
Inputs:  subtask.id + subtask.task

Proceso: agent_memory_delegate(subagent_id, task_description, max_tokens=2000)
         → registra binding C2
         → selecciona pinned memories + open todos relevantes
         → produce bloque markdown: metadatos de sesión + curated memories + todos

Output:  DelegationContext{
           markdown_block,  // para inyectar en el system prompt del subagente
           subagent_id,     // binding C2 registrado
         }
```

**Por qué esto es diferente del SOTA:** el subagente no recibe un volcado de la historia del
orquestador. Recibe una vista CURADA del `agent_memory` del proyecto — solo lo relevante.
Y además tiene capacidad autónoma de recall: si necesita más contexto, llama
`agent_memory_recall("lo que sea")` y lo obtiene.

### 2.5 Etapa 5: AUDIT + SYNTHESIZE (post-delegación — el ciclo de recuperación)

```
Proceso: 1. Por cada batch (topological order):
            - spawn subagentes en paralelo (batch)
            - esperar completion
            - AUDIT: agent_memory_list(tag="subagent-{id}", kind="finding")
                     → recuperar findings del subagente desde la DB
         2. SYNTHESIZE:
            - orquestador compone output final desde findings persistidos
            - NO necesita mantener el chat history de los subagentes
            - drift_judge sobre el artefacto sintetizado
```

**El ciclo de auditoría es el superpoder:** el orquestador puede compactar su propia sesión,
cerrarla, o incluso crashear — y al volver, `agent_memory_list(tag="subagent-{id}")` le
devuelve exactamente lo que el subagente descubrió. La delegación deja una **traza persistente
y recuperable**, no un mensaje efímero en el chat.

---

## 3. Mapa de delegación por vibe-case (revisitado con el lente de memoria)

Cada caso tiene 4 dimensiones de decisión: **qué**, **quién**, **con qué**, **cómo auditar**.

### C1 — Code

| Dimensión | Decisión |
|---|---|
| **¿Delegar?** | CONDICIONAL. HANDLE si ≤2 archivos o tarea aislada. DELEGATE si requiere exploración (recon) o ≥3 archivos de cambio. |
| **¿Qué subtasks?** | `explorer` (read-only scan, model cheap) + `worker(s)` (implementación, model fuerte) + `test-runner` (verificación). Explorer siempre primero (dependencia). |
| **¿Qué mindset?** | Explorer: "codebase cartographer" (over-qualified en patrones del lenguaje). Worker: "senior {lang} engineer" especializado según task (security→OWASP, refactor→patterns, feature→domain). |
| **¿Qué memorias hereda?** | Explorer: decisiones de arquitectura pinned + spec actual. Worker: findings del explorer + decisiones de arquitectura pinned. |
| **¿Qué guarda?** | Explorer: `kind=finding, tag=subagent-{id}` — paths, dependencias, hallazgos. Worker: `kind=decision, tag=subagent-{id}` — qué cambió y por qué. Test-runner: `kind=observation, tag=subagent-{id}` — pass/fail/diff. |
| **¿Cómo audita el orquestador?** | `agent_memory_list(tag="subagent-{explorer_id}", kind="finding")` → paths explorados. `agent_memory_list(tag="subagent-{worker_id}", kind="decision")` → cambios. `agent_memory_list(tag="subagent-{test_id}", kind="observation")` → resultados. |

### C2 — Text

| Dimensión | Decisión |
|---|---|
| **¿Delegar?** | CONDICIONAL. HANDLE si prosa corta con contexto en el orquestador. DELEGATE si requiere especialización de audiencia/tono o revisión adversarial. |
| **¿Qué subtasks?** | `copywriter` (producción) + opcional `skeptical-reviewer` (adversarial check). |
| **¿Qué mindset?** | Copywriter: role prompting con dominio + audiencia + tono (arXiv 2605.29420: mejora por dimensión). Reviewer: "skeptical editor" (rol adversarial probado en prompt engineering). |
| **¿Qué guarda?** | Copywriter: `kind=artifact, tag=subagent-{id}` — el borrador. Reviewer: `kind=observation, tag=subagent-{id}` — feedback adversarial. |
| **¿Cómo audita?** | Orquestador recupera ambos y decide qué incorporar. |

### C3 — Image

| Dimensión | Decisión |
|---|---|
| **¿Delegar?** | DETERMINISTA. REFUSE si no hay provider externo de generación de imágenes en `CapabilitiesFrame`. DELEGATE a un image-generator si existe. |
| **¿Por qué REFUSE es válido?** | A1 (Memory decides) en su forma operacional: "¿hace, delega, o **se rehúsa**?" Si dark-memory no tiene la capability → REFUSE con `ErrCapabilityNotGranted`. Esto es una decisión arquitectónica, no un fallo. |
| **¿Qué mindset?** | "Art director / prompt artist" — composición visual (subject, style, lighting, composition). |

### C4 — Video

| Dimensión | Decisión |
|---|---|
| **¿Delegar?** | DETERMINISTA. SIEMPRE DELEGATE. Pipeline fijo (script → storyboard → render) + compliance paralelo. |
| **¿Qué subtasks?** | 4 subtasks: `script-writer` → `storyboard-artist` (depende de script) → `render-spec` (depende de storyboard) — secuenciales. `compliance-checker` (EU AI Act disclosure) — paralelo, sin dependencias. |
| **¿Qué mindset?** | Script-writer: video narrative designer. Compliance: "EU AI Act 2026-08-02 disclosure officer" — las constraints de compliance son NO-NEGOCIABLES (criterion 3 de mindset_apply: ≥3 "do not" statements). |
| **¿Qué memorias hereda?** | Compliance recibe el disclosure requirement como memoria de contexto. NO recibe el contenido creativo (no lo necesita). |
| **¿Cómo audita?** | Compliance guarda `kind=observation` con veredicto de disclosure. Si `verdict=fail` → el orquestador bloquea el pipeline completo. |

### C5 — Audio

| Dimensión | Decisión |
|---|---|
| **¿Delegar?** | DETERMINISTA. SIEMPRE DELEGATE. Pipeline + compliance. Misma estructura que C4. |
| **¿Qué subtasks?** | `audio-script` → `voice/sfx-spec` (depende) → `render-spec`. `compliance-checker` paralelo. |
| **¿Qué mindset?** | Audio engineer / voice director. Compliance: igual C4. |

### C6 — Multi-modal

| Dimensión | Decisión |
|---|---|
| **¿Delegar?** | DETERMINISTA. SIEMPRE DELEGATE. Supervisor pattern (un agente coordina múltiples modalidades). |
| **¿Qué subtasks?** | LLM-bounded: el supervisor elige del menú fijo de modalidades disponibles (text, code, image, audio, video). ≤5 subtasks. Topological batching. |
| **¿Qué mindset?** | Supervisor: "multi-modal producer" — structured planning, decomposition, synthesis. Workers: mindsets por modalidad (C1/C2/C3/C4/C5 respectivamente). |
| **¿Qué memorias hereda?** | Supervisor: el spec completo + todas las memorias pinned relevantes. Workers: subset específico de su modalidad. |
| **¿Cómo audita?** | Supervisor recupera findings de todos los workers por tag. Sintetiza → `artifact_log` → `drift_judge`. |

### C7 — Mixed

| Dimensión | Decisión |
|---|---|
| **¿Delegar?** | DETERMINISTA. SIEMPRE DELEGATE. Parallel dispatch máximo (artefactos independientes por definición). |
| **¿Qué subtasks?** | Todos sin dependencias (batch 0 completo). Cada subtask produce un artefacto independiente con su propio vibe_case. |
| **¿Qué mindset?** | Supervisor: "campaign director / producer" — coordina, no crea. Workers: mindsets por tipo de artefacto. |
| **¿Cómo audita?** | Bundle synthesis: orquestador compone el output desde los N artefactos independientes. Cada uno pasa su propio drift_judge. |

---

## 4. Economía de contexto — el modelo dark-memory (no el modelo SOTA)

### 4.1 La diferencia fundamental

| | SOTA | dark-memory |
|---|---|---|
| **Estrategia** | "Mantener el contexto bajo 50K tokens comprimiendo/resumiendo" | "No cargar contexto — **recuperarlo bajo demanda** desde memoria persistente" |
| **Presupuesto** | Token budget en la ventana del LLM | Token budget en la ventana + **recall latency budget** (BM25 <100ms) |
| **Pérdida** | Compaction = lossy. Resumen = lossy. | Recall = lossless sobre lo que se decidió persistir. Solo se pierde lo que no se guardó. |
| **Handoff** | Prompt injection: el padre vuelca contexto en el prompt del hijo | Memory curation: el padre selecciona qué rows de agent_memory hereda el hijo. El hijo recuerda autónomamente el resto. |
| **Audit trail** | Chat history (efímero, no consultable estructuralmente) | `write_audit` + `agent_memory` rows + `sdd_evaluations` — trazable por `session_id`, `project_id`, `subagent_id`, `eval_type` |

### 4.2 El orquestador compacta sin perder nada

```
ANTES de compactar:
  orquestador: agent_memory_save(kind="note", content="estado actual: plan en batch 2/3, workers t1+t2 done")
  orquestador: agent_memory_save(kind="observation", content="worker t1 encontró X en auth.ts")

DESPUÉS de compactar:
  orquestador: agent_memory_recall("estado actual plan batch workers") → recupera el note
  orquestador: agent_memory_list(tag="subagent-{t1_id}", kind="finding") → recupera findings de t1
```

El orquestador pierde el chat history, pero **no pierde el conocimiento** — está en agent_memory.
La compactación pasa de ser una pérdida a ser una **operación de limpieza de ruido** sin costo
de conocimiento.

### 4.3 Métricas de contexto que importan (redefinidas)

| Métrica SOTA (irrelevante para dark-memory) | Métrica dark-memory (la que importa) |
|---|---|
| "Tokens en ventana del orquestador" | **"Recall hit rate del orquestador post-compactación"** — ¿qué % de lo que necesita recupera en <3 recalls? |
| "Tamaño del system prompt" | **"Precisión de curation en agent_memory_delegate"** — ¿el subagente recibió las rows correctas? |
| "Latencia de compactación" | **"Latencia de recall"** — BM25 <100ms + network roundtrip |
| "# de compactaciones por sesión" | **"Frecuencia de agent_memory_save del orquestador"** — ¿está persistiendo los checkpoints correctos? |

### 4.4 La meta real

> **No es "orquestador <50K tokens." Es "orquestador recupera el 100% de lo que necesita en ≤3 calls de recall, con <300ms de overhead total, después de cualquier compactación o cierre de sesión."**

---

## 5. Qué hay que construir (y qué ya existe)

### 5.1 Primitivas existentes — reutilizables sin modificar

| Primitiva | Rol en la arquitectura de delegación | Estado |
|---|---|---|
| `mindset_apply(vibe_case, task)` | **Etapa 3 (MIND)** — engine de composición de personas. Ya validado con 5 criterios + judge. Cache 1h. | ✅ v2.7.0 |
| `agent_memory_delegate(subagent_id, task)` | **Etapa 4 (CURATE)** — genera el bloque markdown de handoff (metadatos + pinned + todos). Registra binding C2. | ✅ v2.9.3 |
| `subagent_register / unregister` | **C2 isolation** — el router los invoca al spawnear. El subagente queda aislado del ContextRecap del principal. | ✅ v2.8.0 |
| `agent_memory_save / recall / list` | **Sustrato de handoff + auditoría** — el subagente persiste findings; el orquestador los recupera. | ✅ v2.1.0+ |
| `internal/vibecase/taxonomy.go` | **Fuente del mapa C1-C7** — las reglas deterministas del router se derivan de `Case`. | ✅ v1.4.1 |
| `internal/vlp/` (state machine) | **Orquestación del ciclo de vida** — necesita nuevo estado + evento (ver §5.2). | ✅ base, falta extender |
| `internal/atomic/` (frames) | **Contexto del router** — IdentityFrame, ScopeFrame, CapabilitiesFrame como inputs. | ✅ |
| `dark_memory_judge / consensus` | **Verificación post-delegación** — drift_judge sobre el artefacto sintetizado. | ✅ |

### 5.2 Lo que falta construir

| Componente | Qué hace | Dónde vive |
|---|---|---|
| **DelegationRouter** | Orquesta las 5 etapas (DECIDE → PLAN → MIND → CURATE → AUDIT+SYNTHESIZE). Reglas deterministas por vibe_case + LLM acotado a menú fijo. | `internal/delegation/router.go` |
| **DelegationPlan** | Tipos: `DelegationDecision`, `Plan`, `SubTask`, `DelegationContext`. Serialización/deserialización. | `internal/delegation/types.go` |
| **DelegationAudit** | `agent_memory_list` por `subagent_id` tag + consolidación de findings. Trazabilidad completa: qué subagente, qué encontró, cuándo. | `internal/delegation/audit.go` |
| **VLP: estado `delegating`** | Nuevo estado en `internal/vlp/state.go`. Transiciones: `spec_active → delegating` (evento `delegate`), `delegating → artifact_log` (síntesis completada), `delegating → needs_human` (fallo irrecuperable). | `internal/vlp/state.go` |
| **VLP: evento `delegate`** | Nuevo evento en el enum. El harness lo dispara cuando el router decide DELEGATE. | `internal/vlp/state.go` |
| **Orchestrator: `DelegateIntent`** | Entry point MCP tool `dark_memory_delegate_intent`. Recibe `vibe_case` + `task_description`, devuelve `DelegationDecision`. | `internal/orchestration/delegate_intent.go` |
| **Tool surface** | Wire de `delegate_intent` en el registry. Tool gated (requiere session + capability grant). | `internal/tools/delegation.go` |

### 5.3 Orden de implementación (lo pragmático)

1. **`internal/delegation/types.go`** — los tipos primero (DelegationDecision, Plan, SubTask). Sin tipos no hay contrato.
2. **`internal/delegation/router.go`** — las reglas deterministas por vibe_case (§3) + el switch LLM-bounded para casos condicionales. Lo mínimo viable: DECIDE + PLAN para C7 mixed (el caso más simple: siempre delega, sin dependencias).
3. **`internal/vlp/state.go`** — nuevo estado `delegating` + evento `delegate`. 3 transiciones nuevas (~30 líneas).
4. **`internal/orchestration/delegate_intent.go`** — el orchestrator que consume router + mindset_apply + agent_memory_delegate.
5. **`internal/delegation/audit.go`** — recuperación de findings por subagent_id tag.
6. **`internal/tools/delegation.go`** — wire tool surface.
7. **Iterar sobre C1-C6** — cada vibe_case añade sus reglas deterministas al router.

**MVP:** con C7 mixed funcionando (siempre delega, parallel dispatch, sin dependencias), el loop completo (DECIDE → PLAN → MIND → CURATE → AUDIT → SYNTHESIZE) queda validado end-to-end con el caso más simple. Luego se añaden C4/C5/C6 (deterministas también), y finalmente C1/C2 (condicionales con LLM).

---

## 6. Referencias

- `vibe-flow/main/DELEGATION_SOTA.md` — 11 fuentes del estado del arte (Codex, Stochastic Sandbox, MDPI, Azure, O'Reilly, arXiv).
- `vibe-flow/main/ACTIVE_MEMORY_RFC.md` §M7 — especificación original M7 Delegation Router.
- `vibe-flow/PLAN.md` §2.3 — Wave 5C plan original (P1, sub-spec 16).
- `docs/mindsets.md` — documentación de `mindset_apply` (v2.7.0).
- `internal/orchestration/mindset_apply.go` — implementación del engine de personas.
- `internal/orchestration/subagent.go` — C2 isolation bindings.
- `internal/tools/agent_memory.go:185-209` — `agent_memory_delegate`.
- `internal/vibecase/taxonomy.go` — taxonomía C1-C7.
- `internal/vlp/state.go` — VLP state machine actual.
