# Las 73 herramientas

> **Este archivo no se lee para decidir si instalas.** Se lee para
> confirmar, *después* de instalar, que lo que tenías delante existe.
> Para la lista completa con namespaces, tabla compacta y qué herramienta
> usar en cada momento, sigue aquí.

Dark Memory expone **73 herramientas canónicas** (más 3 extras armados por
variable de entorno, 76 en total) agrupadas en **20 namespaces**. El agente
las invoca con el prefijo `dark_memory_`.

Estos 73 son la superficie **canónica medida** en el código. El objetivo
original de `ARCHITECTURE-V4.md` eran 97+, así que la cifra honesta es
**73/97 ≈ 75% de la superficie originalmente prometida**, no 100%.
Estructura agrupada por 20 oficios:

Dark Memory expone **73 herramientas canónicas** (más 3 extras arms por variable de entorno, 76 en total) agrupadas en **20 namespaces**. El agente las invoca con el prefijo `dark_memory_`.

Estos 73 son la superficie **canónica medida** en el código. El objetivo original de `ARCHITECTURE-V4.md` eran 97+, así que la cifra honesta es **73/97 ≈ 75% de la superficie originalmente prometida**, no 100%. Estructura agrupada por 20 oficios:

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

### Cuaderno del agente (13 tools)
`agent_memory_save` · `agent_memory_list` · `agent_memory_recall` · `agent_memory_get` · `agent_memory_update` · `agent_memory_archive` · `agent_memory_delegate` · `agent_memory_entities` · `subagent_register` · `subagent_unregister` · `prograph_query` · `mark_superseded` · `recall_bitemporal`

El cuaderno persistente. Guardar notas, decisiones, hallazgos, tareas. Buscar por texto (BM25). Delegar contexto a sub-agentes.

### Juez (4 tools)
`judge` · `consensus` · `judgment_history` · `judge_list_personas`

Evaluar artefactos contra specs. Con rubricas G-Eval por tipo de trabajo (código, texto, imagen...). `consensus(n=5)` para decisiones de alto riesgo.

### Mente (1 tool)
`mindset_apply`

Compone el system prompt de un sub-agente y lo valida con el juez antes de devolverlo.

### Delegación (1 tool)
`delegate_intent`

Decide si un trabajo se hace directo, se delega a sub-agentes, o se rechaza. Pasa por DECIDE → EXTRACT → MIND → CURATE.

> Antes agrupadas bajo "Mente y delegación (2 tools)". Partidas porque el
> registro canónico las tiene en namespaces separados y una cuenta
> combinada no se puede verificar — el guard de drift sólo sabe
> comprobar la correspondencia uno a uno.

### Políticas (2 tools)
`active_policy` · `load_constitution`

Consultar las reglas activas y la constitución del proyecto.

### Observabilidad (6 tools)
`health_ping` · `memory_state` · `writes` · `anomalies` · `audit_export` · `audit_verify`

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

---

## Vibe-cases · C1 a C8

Cada entrega va bajo un caso, y el caso decide la rúbrica con la que el juez la
evalúa. Transcrito de `internal/vibecase/taxonomy.go:64`, no recordado.

| Caso | Tipo | Cubre |
|---|---|---|
| **C1** | `code` | Artefactos de código fuente: funciones, módulos, servicios |
| **C2** | `text` | Prosa, documentación, contenido narrativo |
| **C3** | `image` | Imágenes fijas, ilustración, arte generado |
| **C4** | `video` | Imagen en movimiento, animación, video sintético — *divulgación EU AI Act* |
| **C5** | `audio` | Voz, música, efectos de sonido, audio sintético — *divulgación EU AI Act* |
| **C6** | `multi-modal` | Artefacto compuesto de una sola salida, ≥2 modalidades |
| **C7** | `mixed` | Paquete coordinado de artefactos independientes |
| **C8** | `vibe-flow` | Gating de flujo ambiental (vibe-loop-git Loop 7) |

`C1`–`C7` son los que acepta el juez; `C8` es gating de flujo, no evaluación
de artefacto. Nota: el enum del schema del juez todavía no incluye `C8`.

<details>
<summary><b>English — vibe-cases</b> (literal form, for agents and parsers)</summary>

| Case | Type | Covers |
|---|---|---|
| **C1** | `code` | Source-code artifacts: functions, modules, services |
| **C2** | `text` | Prose, documentation, narrative content |
| **C3** | `image` | Still images, illustration, generated art |
| **C4** | `video` | Motion pictures, animation, synthetic video — *EU AI Act disclosure* |
| **C5** | `audio` | Voice, music, sound effects, synthetic audio — *EU AI Act disclosure* |
| **C6** | `multi-modal` | Composite single-output artifact spanning ≥2 modalities |
| **C7** | `mixed` | Coordinated bundle of independent artifacts |
| **C8** | `vibe-flow` | Ambient workflow gating (vibe-loop-git Loop 7) |

C1-C7 are the cases the judge accepts. C8 is for workflow gating, not artifact
evaluation. The judge's schema enum does not yet include C8.

</details>
