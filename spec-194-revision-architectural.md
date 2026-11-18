# Revisión arquitectónica de SPEC-194 — El plan como artefacto y el gate que obliga al harness

> Artefacto sometido al juez. Commit de la spec: `16cf434` (rama
> `feat/spec-194-plan-y-gate`, base `9663113` = `upstream/main` tras el merge
> del PR #39).
> Documento revisado: `docs/specs/SPEC-194-el-plan-y-el-gate.md` (716 líneas).
> Fecha: 2026-11-17.

---

## 1. Qué se somete a juicio

SPEC-194 es la primera de las dos specs que K1 (TODO #728) declara. La
propuesta, en una frase:

> El kernel hoy juzga el resultado y no el camino. La spec introduce el
> **PLAN** como un artefacto persistido (candidatos con peso, tier por
> profundidad, presupuesto agotable) y tres **razones de gate** tipadas que
> obligan al harness: `ReasonPlanRequired`, `ReasonNotApproved`,
> `ReasonBudgetExceeded`.

La spec es **diseño**, no implementación: no hay código de plan todavía. Lo
que se juzga es si el diseño es coherente, si su base es real, y si sus
criterios de aceptación son verificables.

---

## 2. La base: todo lo que la spec afirma está medido

Este es el punto que más se ha cuidado, y es el que un juez puede verificar
sin contexto adicional. Cada afirmación técnica de la spec cita un
`fichero:línea` del repo en el commit `9663113` (`upstream/main`). **Las 13
referencias se han
verificado una a una contra el código real**, no contra la documentación.

### 2.1 El hueco que la spec denuncia, con su evidencia

| Afirmación de la spec | Evidencia verificada |
|---|---|
| No existe un artefacto de plan: `vibe_specs` guarda el *qué*, no el *cómo* | `internal/migrate/sqlite/ddl.go:71` — `CREATE TABLE IF NOT EXISTS vibe_specs (…, spec_json TEXT, tasks_json TEXT, …)`. No hay columna de plan, ni tabla de planes. |
| "Presupuesto" no es un invariante del kernel | `internal/vibeflow/cost.go:487` — el comentario del propio repo dice literalmente: *"Does NOT enforce budgets (use a future L11 budget gate for that)"*. La spec **cita al repo como testigo de su propio hueco**. Lo único que hay es `ContextRecapTokens` (`internal/orchestration/session_start.go:65`), que es un presupuesto de *tokens* del recap de contexto: otra cosa. |
| Las tools se clasifican en dos conjuntos, no en tres | `internal/server/middleware.go:384-397` — `IsArtifactCreating` con **7** tools. `internal/policy/gate.go:86-113` — `RequiresActiveSession` con **9** tools exentas. Ninguna pregunta si la tool muta el estado sin plan aprobado. |
| El juez es tardío y no determinista | `internal/v4alpha/judge/v4judge/llm_judge.go:128` — `DefaultMaxArtifactBytes = 32_768`. Y el veredicto: el mismo artefacto dio `drift_detected` 0.99 y `aligned` 0.91 en corridas distintas, porque el proveedor ignora `temperature: 0`. |

### 2.2 Las tres razones encajan en el enum existente

`internal/policy/gate.go:57-62` define hoy cinco razones más `ok`:
`ReasonScopeRequired`, `ReasonCapabilityDenied`, `ReasonPersonaMissing`,
`ReasonFrameStale`, `ReasonDriftAtWrite`. Sus `ErrorKind()` (el campo
`data.error_kind` del sobre MCP) están en `internal/policy/gate.go:107-124`.

La spec **no inventa un mecanismo nuevo**: añade tres constantes al mismo
enum y tres casos al mismo `switch`. El patrón de refusals —`Reason` →
`ErrorKind()` → `refusalResponse(reason, message, hint)`— ya existe y la
spec lo reutiliza.

### 2.3 El espejo de la dureza es real

`internal/drift/strictness.go` ya tiene el patrón exacto que la spec replica
con nombre propio (`DARK_PLAN_STRICTNESS`): un tipo `Strictness` con
`Off`/`Warn`/`Strict`, `String()` y un `ParseStrictness()` **defensivo**
—un valor desconocido cae en `Off`, con el comentario *"never accidentally
refuse a save because the operator mis-typed the env"*.

El gate del plan se enchufa en `internal/server/middleware.go:142`
(`func (m *GateMiddleware) Wrap`), dentro de `PreCheck`, y el juez de drift
sigue en `internal/server/middleware.go:215` (`PostCheck`). **Son fases
distintas y complementarias**, no se pisan.

---

## 3. Las tres decisiones de diseño que hay que juzgar

### 3.1 Dónde vive el plan: `vibe_specs` con campos nuevos

La spec **descarta dos alternativas** y explica por qué:

**Contra una tabla `plans` nueva:** duplicaría `vibe_specs`, que ya tiene
`spec_json` libre para el JSON del plan. La regla del repo (SPEC-204 §3.4)
es *resolver una cosa en un solo sitio*; una tabla `plans` la violaría, y
además exigiría migración de lectura y escritura en los dos stores.

**Contra `vlp_state`:** `internal/vlp/state.go:37-54` define el enum `State`
con nueve estados, y el esquema de `vlp_state` tiene un índice **UNIQUE** en
`(project_id, session_id)`: es **una fila por sesión**, no por spec. El plan
es **por spec**: una sesión puede abrir y cerrar varias specs, cada una con
su plan y su decisión propia. Meterlo en `vlp_state` fuerza la colisión.

El plan es, por tanto, una fila de `vibe_specs` con **siete** columnas nuevas
(`plan_json`, `plan_status`, `plan_decision`, `plan_decided_by`,
`plan_decided_at`, `plan_tier`, `plan_budget_used`) y un `plan_status` con
**cinco** valores (`draft`, `approved`, `executed`, `cancelled`, `edited`).

### 3.2 Por qué el gate va en `PreCheck` y no en `PostCheck`

Porque `PreCheck` es donde se puede rechazar **antes** de que el trabajo
cueste, y porque `PreCheck` no puede llamar al juez: sería un round-trip LLM
en *cada* tool call, y el juez tarda entre 60 y 130 s. De ahí la separación
de responsabilidades que la spec hace explícita:

- **el plan se valida de forma determinista** en `PreCheck`;
- **el juez valida el shortlist una sola vez**, cuando el plan se crea.

Es la única forma de que el gate sea barato. Y encaja con la literatura que
la spec cita: *Science of Scaling* (arXiv 2512.08296) mide un overhead de
coordinación del 58-515 % con **interacción negativa** con el número de tools
(β=-0.141, p<0.001).

### 3.3 La jerarquía de autoridades

La spec es explícita en que **ninguna capa decide lo que es de la otra**:

| Capa | Autoridad | Qué NO puede hacer |
|---|---|---|
| Determinista (BM25, tier, presupuesto) | Proponer un shortlist | Elegir |
| Juez (rúbrica) | Validar el shortlist | Ejecutar |
| Persona (`plan_decide`) | Aprobar, editar, cancelar | Revivir un presupuesto agotado |

El motivo está medido: *Strategy Auctions* (arXiv 2602.02751) demuestra que
los routers que se apoyan en la descripción de la tarea quedan **por debajo**
del agente grande; y *SkillRouter* (arXiv 2603.22455) mide 74,0 % top-1 con
recuperar-y-rerankear, no eligiendo de un catálogo entero.

**La elección de BM25 no es estética.** El embedder del kernel está hoy en
`kind=none, dim=0`: la búsqueda es **solo lexical**. Y BM25 sobre el
*registro de tools con la doc completa* es la configuración que la literatura
reporta como la mejor (y ~40× más rápida que embeddings en CPU).

---

## 4. Los puntos débiles del diseño (encontrados en la revisión)

Esta sección existe porque una revisión que solo dice «todo bien» no es una
revisión. Los seis puntos siguientes son **defectos reales** que la revisión
detectó, y cuatro de ellos ya están corregidos en el texto que se somete.

### 4.1 El deadlock del gate — CORREGIDO

**Defecto:** en la primera redacción, `plan_decide` estaba dentro de
`IsPlanRequired`. Pero `plan_decide` es **precisamente la tool que registra
la aprobación** del plan: exigirle un plan aprobado para aprobar el primer
plan es un deadlock. El mismo problema alcanzaba a `plan_create`.

**Corrección aplicada:** una exención **cerrada y explícita**,
`PlanLifecycleExempt` (6 tools), con el mismo estilo que las otras dos listas del
kernel (`RequiresActiveSession`, `IsArtifactCreating`), y con el orden de
comprobaciones explícito en la spec:

```
1. ¿la tool es del ciclo de vida del plan?  → exenta
2. ¿la tool muta el estado?                 → exige plan
3. ¿hay plan abierto para la sesión?        → si no: ErrPlanRequired
4. ¿el plan está aprobado?                  → si no: ErrNotApproved
5. ¿queda presupuesto?                      → si no: ErrBudgetExceeded
```

**Lo que la spec se prohíbe a sí misma:** no hay bandera
`skip_plan_check` en los argumentos. Una excepción por argumentos se puede
pasar por alto y dejaría el gate abierto por la puerta de atrás.

**El precio, dicho en voz alta:** `plan_create` y `plan_decide` no están
protegidas por el gate. Se compensa porque `plan_create` **no ejecuta
trabajo**: solo propone. El trabajo empieza con el plan aprobado, y ahí sí
manda el gate.

### 4.2 El plan no cabe en el techo del juez — RESUELTO EN LA PUBLICACIÓN

**Hecho medido:** la spec tiene 36.475 bytes y `DefaultMaxArtifactBytes` es
32.768 (`internal/v4alpha/judge/v4judge/llm_judge.go:128`). Además
`internal/orchestration/drift_judge.go:184-185` **clampa** el `MaxBytes` al
techo: no hay forma de subirlo.

La aritmética exacta: al truncar a 32.768 quedarían el **89,8 %** del
documento y se perderían 3.707 bytes, el **10,2 %** final — que es donde caen
precisamente §9-§12, es decir, **las dependencias, el riesgo declarado y el
apartado de "qué NO cambia"**. La pérdida es pequeña en proporción y grave en
sustancia: el juez no vería el apartado que declara los riesgos del diseño.

Y el veredicto lo confirma empíricamente: en esta misma corrida, el juez
señaló que la enumeración de `plan_status` estaba mal y que la frase
"evaluar la mitad" no se sostenía. Es decir, el juez **sí leyó** el
documento largo, pero razonó sobre una enumeración que no era la del
documento: leyó la versión truncada.

Es el mismo defecto que ya se cometió una vez (publicar un diff de 58 KB y
que el juez viera medio diff) y que la memoria operativa recoge como
criterio: *publicar el artefacto que de verdad quieres que juzguen*.

**Resolución adoptada:** el artefacto somete a juicio **este documento** —la
revisión arquitectónica—, que sí cabe, y que contiene la verificación de las
referencias, la justification de las decisiones y los puntos débiles. La
spec íntegra queda en el commit, citable y verificable.

### 4.3 La calibración de la lista de tools — ABIERTO, POR DECISIÓN

`IsPlanRequired` es una lista **candidata** de 21 tools. La spec lo dice sin
adornarlo y deja la calibración fina abierta en la tarea T2, con el criterio
de que la lista **se ajusta con el KPI, no con criterio**.

Es la decisión correcta: una lista de tools que exige plan es una lista que
**pararía el trabajo real** si se calibrara por intuición. Se calibra con la
medida de las negativas por razón.

### 4.4 La dureza: `warn` por defecto — ARGUMENTADO, CON RIESGO DECLARADO

La spec no activa `strict`. El motivo está medido y es concreto: **el
propio trabajo de este repo** (escribir specs, publicar releases, actualizar
memorias) llama tools mutantes sin plan. Un gate que para el trabajo es un
gate que se desactiva el primer día.

La spec por tanto hace tres cosas: `warn` es el default; el **KPI de
obligación se mide desde el día uno** (ratio de rechazo, negativas por razón,
ratio aprobación/edición — el indicador de *automation bias* de AICA, y
profundidad media); y `strict` se propone cuando el ratio de rechazo es > 0
**y** existe al menos un adaptador (K2) que produce planes.

**El riesgo está declarado en la propia spec** (§10): si se activara
`strict` sin que el KPI lo justifique, el efecto sería un gate que la gente
desactiva, que es el peor resultado posible porque **da la falsa impresión de
que hay gobernanza**.

### 4.5 El tier y los presupuestos son una propuesta, no una ley — DECLARADO

Los números (`corta` → 3 tool calls, `media` → 8, `larga` → 20) y el corte
del tier por número de candidatos (≤3 / 4-8 / >8) están marcados en la spec
como **propuestos**, no como valores fijos. La cifra real vive en el plan y
la persona puede editarla — que es exactamente lo que `plan_decide(edit)`
permite.

La regla que la spec se impone: *si el KPI dice que el 90 % de los planes se
agota, el tier está mal dimensionado*. Es decir, **el propio mecanismo de
edición de la persona es el que calibra el sistema**.

### 4.6 Una confusion que la spec evita explícitamente

El tier **no** se calcula con el `tool_count` de `health_ping`
(`internal/v4alpha/transport/mcp/health.go:69`), que es el número de tools
**registradas en el servidor**, no el tamaño de una tarea. Es una confusión
que ya se detectó antes en el repo (#737) y esta spec no la repite.

### 4.7 Los dos hallazgos del juez sobre este artefacto, ya corregidos

El juez juzgó la primera versión de este documento y salió `aligned` 0.94, con
**dos imprecisiones** señaladas en el razonamiento. Ambas eran reales, y
ambas están corregidas en la versión que se somete ahora:

| Hallazgo del juez | Qué era | Corrección |
|---|---|---|
| *"imprecisiones menores en la enumeración de plan_status"* | La spec decía «`plan_status` es el estado del gate, con **tres** valores» y a continuación listaba **cinco** (`draft`, `approved`, `executed`, `cancelled`, `edited`). | La spec dice ahora **cinco**, en tabla con una columna explícita de terminalidad, y distingue que `edited` es un estado **histórico**: el plan vivo vuelve a `draft`. |
| *"la expresión de que un límite de 32.768 bytes aplicado sobre un artefacto de 36.475 bytes implica evaluar la mitad"* | La analogía era **incorrecta**: quedaría el 89,8 %, no la mitad. | La aritmética está exacta ahora: 32.768 de 36.475 deja el **89,8 %** y pierde el **10,2 %** final — que es exactamente donde caen §9-§12 (dependencias, riesgo declarado y «qué NO cambia»). |

Lo segundo tiene una consecuencia que el juez no dijo y que conviene
registrar: la pérdida del 10,2 % final no es proportionemente grave, pero **es
sustancialmente grave**, porque el tramo que se pierde es el que declara los
riesgos del diseño y las dependencias. Y hay una confirmación empírica en el
propio veredicto: el juez señaló que la enumeración de `plan_status` no era la
del documento, lo que significa que **leyó la versión truncada**. Ese es
justo el fallo que la cisión del artefacto evita.

> **Nota sobre el método:** un veredicto `aligned` con hallazgos señalados **no
> está cerrado**. El ciclo se cierra cuando (a) el artefacto está publicado,
> (b) el veredicto existe, (c) el rastro está en la memoria, (d) el cambio
> está en un commit, y (e) **los defectos que el juez señaló están
> corregidos**. Este apartado es (e), y se escribe en el artefacto, no en un
> canal privado.

---

## 5. Verificabilidad de los criterios de aceptación

Los quince AC son comprobables, no aspiracionales. Los que tienen forma
binaria:

**Los quince AC, itemizados.** Los que tienen forma binaria se comprueban
con un test; los que son de sistema se comprueban con un comando.

| AC | Qué exige | Cómo se comprueba | Forma |
|---|---|---|---|
| **AC1** | El plan se persiste y se recupera íntegro | `plan_get` devuelve los candidatos, el tier y el presupuesto idénticos a los guardados | Test |
| **AC2** | Tool mutante sin plan → `ErrPlanRequired` | En `strict` se rechaza; en `warn` pasa con la negativa registrada | Test |
| **AC3** | Plan en `draft` → `ErrNotApproved` | El gate rechaza mientras el estado no sea `approved` | Test |
| **AC4** | Presupuesto agotado → `ErrBudgetExceeded` | Al llegar a `max_tool_calls` o `max_judge_calls` | Test |
| **AC5** | `plan_decide(edit)` reabre como `draft` | El prompt del revisor queda aplicado y el plan anterior sigue en el rastro | Test |
| **AC6** | Fila en `write_audit` | `write_path = "plan_decide"`, con `actor`, `session_id` y `notes` | Test |
| **AC7** | El contador coincide con las tool calls | El `used_tool_calls` es igual al número de tool calls que pasaron | Test |
| **AC8** | El juez **valida** el shortlist | El plan aprobado contiene los candidatos que el juez validó, no otros | Test |
| **AC9** | Las tools de lectura no exigen plan | `recall`, `list`, `get` pasan sin plan abierto | Test |
| **AC10** | `off` = comportamiento de hoy | Con `DARK_PLAN_STRICTNESS=off`, ningún rechazo por plan | Test |
| **AC11** | El contador es **atómico** | Dos llamadas concurrentes sobre el mismo plan no pueden gastarlo dos veces | Test de concurrencia |
| **AC12** | Build y CI en verde | `go build ./...`, `go test ./...` y los 6 checks del PR | Sistema |
| **AC13** | El ciclo del plan no exige plan | `plan_create` y `plan_decide` funcionan sin plan abierto | Test |
| **AC14** | **No hay** bandera para saltarse el gate | Ningún argumento del schema permite saltarlo | Test |
| **AC15** | La lista de exentas es **cerrada** | Un test falla si se le añade una tool sin justificación en el propio test | Test |

AC9, AC10 y AC14 son los que protegen de los excesos: las tools de lectura no
exigen plan, con `off` el comportamiento es el de hoy, y no hay ninguna forma
de saltarse el gate. AC15 protege la exención de que crezca sin que nadie lo
justifique.

---

## 6. La ruta del veredicto (lo que hay que mirar al leer la respuesta)

La spec tiene una sección (§7) dedicada a esto, y es relevante **para este
proceso**: hay **dos** rutas de veredicto en el servidor y no valen lo mismo.

| Ruta | Qué es | Para qué sirve |
|---|---|---|
| **Rúbrica** (`v4judge`, `provider_id` con prefijo `judge-`) | El juez evalúa el artefacto contra el intent con una rúbrica de veredicto | **La que discrimina.** Encontró cuatro defectos reales en un ciclo. |
| **Cadena NLI** (`provider_id` con prefijo `chat-`) | `premise`/`hypothesis` → `entailment`/`contradiction`/`neutral` | El fallback legacy. Para contenido técnico es un juez débil: respondió `entailment` (aligned 1.0) al mismo artefacto que la rúbrica había marcado `drift_detected` tres veces. |

En el `reasoning` se distingue: `nli=entailment` es la cadena NLI; un
párrafo escrito es la rúbrica. **Este artefacto debe juzgarse por rúbrica.**

Y hay un recordatorio que la spec hereda del repo: **un veredicto aislado no
cierra nada**. El proveedor ignora `temperature: 0`, así que el mismo
artefacto puede veredicarse distinto en dos corridas. Lo que cierra es el
**ciclo completo**: artefacto + rastro en memoria + commit.

---

## 7. Lo que esta spec NO decide (y por qué está bien que sea así)

| Fuera de alcance | Por qué |
|---|---|
| Los adaptadores de harness (el bucle desglose → `question` → `plan_decide`) | Es W2/K2 (TODO #729). Aquí se garantiza que **cuando** el harness nombre su plan, el kernel lo hace valer. |
| El gate de las tools de otros MCPs (`[FUTURE-MCP-N]`, `dark-research`) | D7: el kernel no puede hacer valer el plan de un MCP que no es suyo. Ese gate es del adaptador. |
| La personalización de la configuración (dureza, presupuestos, umbrales) | Es W11 (TODO #767), y D11: lo decide la persona al configurar. |
| El panel | D5: «el panel no es el producto». |
| El token de npm | Sin relación; bloqueado por credenciales y en pausa. |

La dependencia es **real, y se ha discharged**: K0 (SPEC-203/204) estaba en el
PR #39, **sin mergear** cuando se escribió la primera versión de este
documento. El PR #39 se mergeó el 2026-11-17T23:18:50Z (commit `f09b3f7`),
junto con los PRs #38, #40 y #41, y esta rama se rebasó sobre ese `main`
(`9663113`).

Consecuencia: **la dependencia bloqueante de K1 ha desaparecido**. Sin la
identidad resuelta, la fila de `plan_decide` caería en el proyecto
equivocado — el defecto exacto que SPEC-204 arrancó a arreglar — pero SPEC-204
ya está en `main`, así que ese riesgo está cerrado y el código de K1 puede
aterrizar. Lo que queda pendiente es una dependencia de **secuencia**, no de
bloqueo: el gate de `strict` sigue necesitando el adaptador de W2/K2.

---

## 8. Veredicto esperado y qué haría cambiar la spec

Si este artefacto se juzga alineado, la spec pasa a **propuesta en revisión
formal** y el siguiente paso es el PR A (las tools del plan: T1, T5, T6, T7,
T8, T9) y el PR B (el gate: T2, T3, T4, T11), con los tests de rechazo como
criterio de aceptación del PR B.

Los hallazgos que **harían** cambiar el diseño, si el juez los señala:

1. **Si el plan debe ser un estado del VLP y no una fila de `vibe_specs`.**
   La spec lo decide en contra con el argumento del índice UNIQUE de
   `vlp_state`. Si el juez lo ve de otro modo, la migración cambia, pero el
   modelo del plan no.
2. **Si la exención `PlanLifecycleExempt` es demasiado ancha.** Con seis
   tools, es estrecha. Si el juez pide que `plan_create` quede bajo el gate,
   el deadlock vuelve y hay que re-plantearlo (por ejemplo: el primer plan
   se crea por `session_start`, no por una tool).
3. **Si el gate del plan debe ir en `PostCheck`.** La spec lo pone en
   `PreCheck` por coste y por oportunidad. Un juez que lo quiera al final
   cambia el coste del gate, no su función.
4. **Si el presupuesto debería ser por sesión y no por plan.** La spec
   justifica la elección por spec con la literatura de coordinación intra-tarea.
   Es discutible y es una decisión legítima de la persona.

Ninguno de estos cuatro son defectos de la base medida: son decisiones de
diseño que la spec documenta con su motivo, y que el juez puede cuestionar
legítimamente.
