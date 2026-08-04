# Delegación Inteligente — Estado del Arte y Síntesis

**Fecha:** 2026-08-04
**Autor:** dark-agent (sesión `sess-ffaa09495ad20596`)
**Objetivos del operador:**
1. Reducir el desperdicio de contexto del orquestador → mantener su % de contexto lo más bajo posible → minimizar alucinaciones.
2. Mejorar la calidad del trabajo con un mindset correcto (persona/rol especializado).
3. Cerrar el gap de delegación del vibe-loop (Wave 5C del PLAN.md, nunca implementada).

---

## 1. El problema: context rot

Fuente primaria: [Codex Knowledge Base — Context Window Management: Avoiding Compaction with Sub-Agent Delegation](https://codex.danielvaughan.com/2026/03/27/context-window-management-subagent-delegation/) (2026-03-27, actualizado 2026-07-16).

- **Context rot**: en sesiones largas de agente, la fiabilidad se erosiona no porque el modelo "olvide" las instrucciones, sino porque el contexto se llena de ruido de tool output, stack traces intermedios, callejones sin salida e hipótesis descartadas.
- **La compactación es una cura con costo**: cada compactación es una operación *lossy* (resumen generado, historia cruda descartada). Múltiples compactaciones en una sesión **componen la pérdida**. OpenAI documenta: "Long conversations and multiple compactions can cause the model to be less accurate."
- **Diagnóstico de la industria (feb 2026)**: "Agents can't endlessly compact/recycle in the same context window — we need either smarter harnesses or something which provides more delegation." El cuello de botella #1 de los sistemas multi-agente es la **coordinación del contexto**.

**Conclusión directa para dark-memory:** la delegación NO es un feature más — es la estrategia de prevención primaria de degradación. El orquestador debe mantenerse limpio por diseño, no por remediación.

---

## 2. Principios SOTA extraídos (8)

### P1 — El hilo principal queda limpio; los hijos devuelven resúmenes condensados

```
User prompt ──► Orquestador (brief, requisitos, decisiones)
                  │ delegate
                  ├─► Explorer subagent  (read-only scan) ──► resumen condensado
                  ├─► Worker subagent    (implementación) ──► lista de archivos + diff
                  └─► Test runner        (pytest + logs)  ──► pass/fail + resumen
                  ▼
              Respuesta final al usuario
```

El orquestador ve **3 resúmenes limpios**, no 3 ventanas de contexto crudas. Su uso de tokens crece lento y conserva fidelidad sobre la tarea de alto nivel.

**Regla de oro:** *"Return summaries, not raw output."* Si un explorer devuelve miles de tokens crudos, empuja al orquestador hacia la compactación. Formato de retorno: bullet list de archivos (solo paths, sin contenido), una frase por hallazgo, contadores. **20 líneas, no 2.000.**

### P2 — Handoff estructurado > resumen libre > historia completa

Fuente: [Stochastic Sandbox — AI Agent Orchestration Patterns](https://stochasticsandbox.com/posts/ai-agent-orchestration-patterns-2026-04-21/) (2026-04-21).

Tres formas de transferir contexto entre agentes:

| Enfoque | Costo | Fiabilidad | Uso |
|---|---|---|---|
| Historia completa | Explota tokens (el 3er agente hereda todo) | Alta | Nunca en cadenas largas |
| Resumen libre | Bajo | Media (pierde detalles críticos) | Dominios de datos distintos |
| **Handoff estructurado** | Bajo | **Alta — el contrato es explícito y testeable** | **El estándar de producción** |

Schema típico de handoff estructurado:

```json
{
  "findings": "lista de puntos clave descubiertos",
  "original_query": "la petición original del usuario (SIEMPRE incluida)",
  "remaining_tasks": "lo que queda por hacer",
  "constraints": "restricciones identificadas"
}
```

**Regla crítica:** incluir la *original_query* en TODO handoff — la mitigación #1 contra "el delegador resumió demasiado agresivo y el receptor decidió con info incompleta".

### P3 — Supervisor: descomposición centralizada + dispatch paralelo + síntesis explícita

El patrón supervisor resuelve los 3 fallos de la delegación lateral (peer-to-peer):
1. **Descomposición centralizada** (un agente con visión global), no ad-hoc por cada agente.
2. **Síntesis explícita** (el supervisor revisa los resultados parciales y produce output coherente).
3. **Error handling con dueño único** (retry, cambiar worker, o degradar con gracia).

Dispatch paralelo: tareas independientes en batch (topological batching por `depends_on`).

**Regla anti-sobre-descomposición** (del prompt supervisor de referencia):
> "If the task is simple enough for one agent, use one subtask. Use at most 5 subtasks. Prefer fewer."

Over-decomposition = más latencia + más probabilidad de error de síntesis.

### P4 — Selección de modelo por rol, no por tamaño

Fuente: Stochastic Sandbox + Codex.

| Rol | Modelo | Racional |
|---|---|---|
| Supervisor | **No el más capaz** — bueno en structured planning | Las decisiones de routing no requieren dominio; los workers tienen el dominio |
| Worker de código | El mejor disponible | Generación de código compleja |
| Explorer / resumen | Mini/cheap | Throughput > razonamiento profundo |
| Síntesis | Igual que supervisor (consistencia) | — |

*"Usar un modelo más barato para el supervisor que para los workers especializados es contraintuitivo pero a menudo correcto."*

### P5 — Presupuesto de contexto explícito

La ventana no es el límite real. **El rendimiento se degrada antes de llenarla** — los modelos atienden peor la información en el medio de contextos largos. Regla práctica de producción: **mantener el contexto por agente < 50K tokens aunque la ventana sea 200K**.

Tabla de costos típicos por tipo de estado:

| Tipo de estado | Costo típico | Compresión |
|---|---|---|
| System prompt | 500–2.000 | Fijo; mantener tight |
| Mensajes de usuario | 100–500/turno | Raramente compresible |
| Tool call + resultado | 200–5.000 | **Resumir resultados grandes** |
| Historia acumulada | ~1K/turno | Sliding window o resumen |

**Mitigación directa:** resumir tool results > 3K tokens con un modelo barato antes de append (reduce consumo 80–90% en tool data-heavy).

### P6 — Cuándo NO delegar (el otro lado del router)

Fuente: Codex KB. La delegación no es siempre correcta:

| Caso | Razón |
|---|---|
| **Tareas cortas** | El overhead de spawn + espera + consolidación cuesta MÁS tokens que hacerlo inline |
| **Workflows write-heavy paralelos** | Escrituras concurrentes sin coordinación → merge conflicts. **Paralelizar reads libremente; serializar writes** |
| **Tareas profundamente dependientes** | Si B necesita el output de A en detalle (no resumen), un solo hilo preserva fidelidad |
| **Delegación recursiva** | `max_depth > 1` rara vez necesario; fan-out exponencial de tokens. Default: depth 1 |

### P7 — Control flow híbrido (determinista + LLM acotado)

Fuente: Stochastic Sandbox. El espectro:

```
Pipeline determinista ──► Hybrid routing ──► Totalmente autónomo
```

- **Determinista**: predecible, testeable, pero inflexible.
- **Híbrido**: el LLM elige de un **menú fijo** de agentes/acciones (`{"agents": [...], "parallel": true}`). Flexibilidad + predictibilidad. **El sweet spot de producción.**
- **Totalmente autónomo**: el menos fiable. El fallo escala con los puntos de decisión: 90% por paso → 59% end-to-end en 5 pasos (0.9^5); 95% → 77%.

**Para dark-memory:** el DelegationRouter debe ser **híbrido** — reglas deterministas por vibe_case + selección LLM acotada a un menú fijo de estrategias.

### P8 — Personas/roles especializados: calidad por dimensión, no universal

Fuentes: [arXiv 2605.29420 — When Does Persona Prompting Actually Help?](https://arxiv.org/html/2605.29420v1); [Sagar Mandal — Why Specialization Beats Generalization](https://www.sagarmandal.com/2026/03/15/agentic-engineering-part-3-role-based-agent-personas-why-specialization-beats-generalization/); [PromptEdit — Role Prompting](https://www.promptedit.app/prompt-framework/role-prompting).

- El role/persona prompting **sí** mejora la calidad, pero **a lo largo de dimensiones específicas** (vocabulario de dominio, patrones de razonamiento, estilo) — no es un boost universal. Hay que elegir la persona según la dimensión que la tarea necesita.
- La especialización por rol **desacopla** tareas cognitivas, operacionales y evaluativas → escalabilidad y robustez.
- dark-memory ya tiene esto: `mindset_apply` (v2.7.0) compone system prompts con over-qualification, 5 criterios de calidad, y validación LLM-as-judge. **Es el engine de P8 — solo falta conectarlo al router.**

---

## 3. Mapa por vibe-case: delegación por tarea

Los vibe-cases C1-C7 son la **clasificación de tipo de artefacto**. La síntesis SOTA permite ahora mapear cada uno a una estrategia de delegación concreta. La columna "¿Delegar?" es la regla determinista del router (P7).

### C1 — Code

- **Naturaleza:** funciones, módulos, servicios.
- **¿Delegar?** Condicional — delegar cuando: (a) el task_description implique exploración de código existente (recon), (b) sea un refactor grande, (c) incluya testing/verificación. NO delegar tareas cortas (una función aislada).
- **Estrategia:** orquestador = arquitecto (modelo fuerte). Hijos: `explorer` (read-only, modelo cheap) + `worker` (implementación, modelo fuerte) + `test-runner` (verificación).
- **Handoff:** `{archivos_tocados, dependencias, resumen_por_modulo, contadores, original_query}`. El worker devuelve `{archivos_cambiados, diff_resumen}`.
- **Mindset (P8):** senior engineer con dominio específico (security review → OWASP/CVE; refactor → patterns del código base). Herramientas mínimas: Read, Grep, Glob, Bash.

### C2 — Text

- **Naturaleza:** prosa, documentación, contenido narrativo.
- **¿Delegar?** Condicional — delegar cuando: (a) audiencia/especialización clara (marketing, legal, docs técnicas), (b) se requiera revisión adversarial. NO delegar prosa corta con contexto ya en el orquestador.
- **Estrategia:** un subagente redactor especializado + opcionalmente un `reviewer` adversarial (skeptical reviewer es un patrón de persona probado).
- **Handoff:** `{brief, audiencia, tono, jurisdiccion, original_query}` → devuelve `{borrador, decisiones_de_tono, claims_hechos}`.
- **Mindset:** copywriter/docs specialist — el role prompting activa vocabulario de dominio. Para compliance: persona legal con claims policy.

### C3 — Image

- **Naturaleza:** imágenes estáticas, ilustraciones, arte generado.
- **¿Delegar?** Solo si el stack tiene generadores de imagen disponibles como subagentes. En dark-memory puro, el orquestador no puede generar imágenes — la decisión es **refuse/redirect** (A1: "¿hace, delega, o se rehúsa?"). Si existe un provider externo → delegar a subagente de generación.
- **Mindset:** art director / prompt artist (estructura de prompt de imagen: subject, style, lighting, composition).

### C4 — Video

- **Naturaleza:** video, animación, video sintético. **EU AI Act 2026-08-02 disclosure REQUERIDA.**
- **¿Delegar?** SIEMPRE delegable por pipeline: script → storyboard → render. Además **SIEMPRE** requiere un subagente de compliance (EU AI Act disclosure).
- **Estrategia:** supervisor con pipeline secuencial (script → storyboard → render) + compliance check paralelo.
- **Handoff:** `{script, storyboard_spec, render_params, disclosure_requirements}`.
- **Mindset:** video director + compliance officer (el disclosure es no-negociable).

### C5 — Audio

- **Naturaleza:** voz, música, SFX, audio sintético. **EU AI Act disclosure REQUERIDA.**
- **¿Delegar?** Igual que C4 — pipeline + compliance obligatorio.
- **Mindset:** audio engineer / voice director + compliance.

### C6 — Multi-modal

- **Naturaleza:** artefacto compuesto ≥2 modos {code, text, image, video, audio} en UN output.
- **¿Delegar?** SIEMPRE — es la naturaleza del supervisor: descomponer en workers por modalidad, dispatch en batches por dependencias, síntesis final.
- **Estrategia:** supervisor con 2-5 subtasks (regla anti-sobre-descomposición), workers paralelos, síntesis explícita.
- **Handoff:** schema estructurado por modalidad + original_query.

### C7 — Mixed

- **Naturaleza:** bundle coordinado de artefactos independientes (ej. campaña = imagen + texto + landing page code).
- **¿Delegar?** SIEMPRE — por definición son artefactos independientes → parallel dispatch máximo.
- **Estrategia:** supervisor, tareas sin dependencias (batch 0 todo), síntesis del bundle.
- **Mindset del supervisor:** campaign director / producer.

---

## 4. Aplicación a dark-memory — arquitectura propuesta

### 4.1 El DelegationRouter (Wave 5C, hybrid — P7)

```
vibe_spec(C1..C7, tasks)  ──►  spec_active
                                    │
                            DelegationRouter.decide()
                            ┌────────────────────────────┐
                            │ inputs: vibe_case + tasks   │
                            │         + IdentityFrame     │
                            │         + CapabilitiesFrame │
                            │         + ScopeFrame        │
                            ├────────────────────────────┤
                            │ reglas deterministas (mapa  │
                            │  del §3) + LLM acotado a    │
                            │  menú fijo de estrategias   │
                            └──────────┬─────────────────┘
                                       │
                   ┌───────────────────┼────────────────────┐
                   ▼                   ▼                    ▼
              HANDLE (directo)   DELEGATE (N subagentes)   REFUSE
              orquestador hace    router genera plan       no hay capability
              todo, sin spawn     + mindsets por subtask
```

Salida del router: `DelegationDecision{handler: HANDLE|DELEGATE|REFUSE, plan: [{subtask_id, vibe_case, task, mindset_id, model_floor, depends_on}], reasoning}`.

### 4.2 Integración con lo que ya existe (nada se tira)

| Primitiva existente | Rol en el nuevo diseño |
|---|---|
| `mindset_apply(vibe_case, task)` | **El engine de P8** — el router lo llama por subtask para obtener el system_prompt correcto (role + goal + constraints + tools) |
| `agent_memory_delegate(subagent_id, task)` | **El handoff estructurado (P2)** — ya genera el bloque markdown (metadatos + pinned + todos). Alinear su output con el schema: findings/original_query/remaining_tasks/constraints |
| `subagent_register/unregister` | C2 isolation — el router los usa al hacer spawn; ya existente |
| `internal/vibecase/taxonomy.go` | La fuente del mapa del §3 — las reglas deterministas se derivan de `Case` |
| `internal/vlp/` | Nuevo estado `delegating` + evento `delegate` (entre `spec_active` y `drift_judging`) |

### 4.3 Contratos de retorno (P1 + P2) — la clave del objetivo #1

Los subagentes **deben** devolver resúmenes condensados con schema estructurado. En la práctica opencode/Codex: el subagente devuelve su mensaje final — el DelegationRouter instruye (vía mindset constraints) el formato de retorno exacto:

- Explorer: `{archivos: [paths], hallazgos: [una frase c/u], totales: N}` — 20 líneas, no 2.000.
- Worker: `{archivos_cambiados, resumen_del_diff, tests: pass/fail}`.
- Siempre incluir `original_query` en el contexto de entrada del subagente.

### 4.4 Presupuesto de contexto (P5) — metas concretas

- **Meta del orquestador:** contexto total por turno < 50K tokens (~25% de una ventana 200K), idealmente < 30K.
- Todo tool result > 3K tokens → resumir antes de append (mitigación directa de context rot).
- Los `context_recap` de session_start deben ser **curated** (pinned, no todo) — ya es el caso.
- El system prompt del orquestador debe mantenerse tight (P5: 500-2K tokens).

### 4.5 Anti-patrones a evitar (P6 + P7)

- NO delegar tareas cortas (overhead > beneficio).
- NO `max_depth > 1` (fan-out exponencial).
- NO write-heavy paralelo sin coordinación (serializar writes).
- NO over-decomposition (≤ 5 subtasks, preferir menos).
- NO routing totalmente autónomo (compounding error 0.9^5 = 59%).

---

## 5. Métricas de éxito

| Métrica | Baseline actual | Meta |
|---|---|---|
| Tokens de contexto del orquestador por task | ~1x (todo en un hilo) | < 30-50K / < 25% de ventana |
| # de compactaciones por sesión | 1+ en sesiones largas | 0 (delegación previene) |
| Alucinaciones observadas por sesión | — | ↓ medible vía drift_judge `needs_human` rate |
| Overhead de delegación en tareas cortas | — | 0 (el router decide HANDLE) |
| rate de drift_detected en drift_judge | baseline | ↓ (mindset correcto = output más alineado) |

---

## 6. Referencias

1. [Codex KB — Context Window Management: Avoiding Compaction with Sub-Agent Delegation](https://codex.danielvaughan.com/2026/03/27/context-window-management-subagent-delegation/) — context rot, delegación como prevención, cuándo NO delegar, model selection.
2. [Stochastic Sandbox — AI Agent Orchestration Patterns](https://stochasticsandbox.com/posts/ai-agent-orchestration-patterns-2026-04-21/) — 3 patrones, handoff estructurado, supervisor, presupuestos de contexto, error recovery, control flow híbrido.
3. [MDPI — LLM-Based Multi-Agent Orchestration: A Survey (2023–early 2026)](https://www.mdpi.com/1999-5903/18/6/326) — survey académico de orchestration frameworks.
4. [Sub-Agents in LLM Systems: Architecture, Execution Model, Design](https://martinuke0.github.io/posts/subagents/) — características del subagente: narrow scope, restricted tools, structured I/O, ephemeral.
5. [arXiv 2605.29420 — When Does Persona Prompting Actually Help?](https://arxiv.org/html/2605.29420v1) — persona prompting mejora por dimensiones específicas.
6. [Sagar Mandal — Role-Based Agent Personas: Why Specialization Beats Generalization](https://www.sagarmandal.com/2026/03/15/agentic-engineering-part-3-role-based-agent-personas-why-specialization-beats-generalization/).
7. [PromptEdit — Role Prompting / Expert Persona](https://www.promptedit.app/prompt-framework/role-prompting).
8. [O'Reilly Radar — The AI Agents Stack (2026)](https://www.oreilly.com/radar/the-ai-agents-stack-2026-edition/) — "what to remember, what gets dropped, how you stop old context from polluting new answers".
9. [Azure Architecture Center — AI Agent Orchestration Patterns](https://learn.microsoft.com/en-us/azure/architecture/ai-ml/guide/ai-agent-design-patterns) — sequential, concurrent, group chat, handoff, magentic.
10. [LanceDB — Reduce Hallucinations Using Long-Term Memory](https://www.lancedb.com/blog/how-to-reduce-hallucinations-from-llm-powered-agents-using-long-term-memory-72f262c3cc1f) — memoria de largo plazo como ancla anti-alucinación.
11. [byAIteam — Context Window Management for LLMs: Reduce Hallucinations](https://byaiteam.com/blog/2025/11/14/context-window-management-for-llms-reduce-hallucinations/) — segmentación, overlap, compresión progresiva, retrieval a demanda.
