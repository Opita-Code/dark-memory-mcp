# SPEC-195 — El laboratorio de harness: probar que un harness obedece el gate del plan

> Estado: SPEC ESCRITA y **JUZGADA**.
> Fecha: 2026-11-18.
> Caso de vibe: **C8 (vibe-flow)** — el entregable no es un artefacto suelto, es un
>conjunto de puertas de gobernanza que cruzan los siete casos canónicos.
> Spec de vibe: **678**.
> Lo que cierra: **K2** (TODO #729) y deja a **W10** (TODO #766) con su
> infraestructura.
>
> **Cierre WS-7 (las tres partes), con sus identificadores:**
> - **Artefacto:** el fichero que sigue, en este mismo workspace
>   (`/home/nico/workspace/dark-memory-mcp/docs/specs/SPEC-195-el-laboratorio-de-harness.md`).
> - **Veredicto:** la primera corrida dio **`needs_human` con confianza 0.96**, y el
>   juez **no certificó** por dos motivos concretos: (a) que no podía
>   comprobar las citas `file:line` sin el repositorio, y (b) que el documento
>   decía *"pendiente de juicio"* y no aportaba los identificadores del cierre.
>   Los dos se han corregido: el **Apéndice A** lleva el contenido real de cada
>   cita (autocontenido, sin necesidad del repositorio) y esta cabecera lleva el
>   estado del cierre. El identificador del artefacto y el del drift de esta
>   corrida se anaden en el **Apéndice B**, porque no se conocen antes de
>   publicar.
> - **Rastro y commit:** la memoria durable y el commit los exige el guard
>   `governance/check-cierre-loop.sh`, que busca el marcador `artefacto <id>`
>   en el mensaje del commit y una fila de memoria que cite el artefacto.

---

## 1. El problema, medido

El gate del plan **ya funciona en el servidor**. MEDIDO en `upstream/main`
(`5067890`):

| Comprobación | Dónde | Resultado |
|---|---|---|
| El gate está cableado en el middleware | `internal/server/middleware.go:258` | sí, comprueba antes de `inner` |
| El campo `PlanGate` es opcional | `internal/server/middleware.go:112` | sí |
| `IsPlanRequired` | `internal/server/middleware.go:512` | lista cerrada |
| `PlanLifecycleExempt` (el deadlock) | `internal/server/middleware.go:562` | exime el ciclo del plan |
| Las tres tools del plan | `internal/tools/register.go:113` | `plan_create`, `plan_get`, `plan_decide` |
| La dureza | `internal/plan/strictness.go:126` | `off\|warn\|strict`, fallback `warn` |
| Los dos contadores | `internal/store/sqlite/plan.go:73-74` | `plan_budget_used`, `plan_judge_used` |

O sea: **el kernel sabe exigir el plan**. Lo que no existe es la otra mitad.

### 1.1 El hueco, medido en tres partes

**(a) El lado del harness no existe.** `internal/adapter/opencode/` existe
(192 LoC + 258 de test, test en verde) pero:

- Se declara en su propio doc-comment: *"INTENTIONALLY a thin demo"*.
- Su único punto de entrada es `Adapter.DriveSession`, un cliente Go que
  envuelve `vlp.UseCase.HandleEvent` con el sobre JSON de wire-format.
- **Nadie lo importa.** MEDIDO: `grep -rn 'adapter/opencode'` en todo el
  repositorio, con tests incluidos, devuelve **0 resultados**.

Es el mismo fallo que los adapters del embedder, que se corrigió en la alpha.35:
*un `init()` sin importador es código que no existe*. La lección ya se pagó una
vez en este repositorio.

**(b) El VLP no es la puerta humana.** MEDIDO en
`internal/vlp/package.go:254` (`Propose`): valida cada llamada contra la máquina
de estados y mete las válidas en `Approved` **sin preguntar a nadie**. El
comentario lo dice: *"validates a batch of proposed tool calls against the
current state machine"*. `Propose` es un **validador determinista**, no una
puerta humana.

La puerta humana real es otra: **`plan_decide`**, con
`decision ∈ {execute, edit, cancel}`, que deja fila en `write_audit`. MEDIDO en
`internal/tools/plan.go:153` (el enum) y en `internal/orchestration/plan.go:441`
(el write con `write_path="plan_decide"`, el actor siendo **la persona que
decidió** y las notas llevando la decisión, el actor y el plan anterior — SPEC‑194
AC5, INV‑1).

> **Consecuencia de diseño, y es la más importante de esta spec:** el bucle de
> la persona **no pasa por `Propose`**. Pasa por `plan_create` →
> `plan_get` (el desglose) → la pregunta → `plan_decide`. `Propose` y el VLP
> quedan para lo que sí saben hacer: decidir si una llamada es válida según el
> estado. El conector del harness **no** debe inventar una tercera vía.

**(c) Los adaptadores del mod no son lo que parecen.** En
`mods/vibe-loop-git/adapters/` hay cuatro `SKILL.md`: `opencode`,
`claude-code`, `claude-desktop`, `codex`. MEDIDO: el de opencode tiene 61
líneas y es **documentación**, no cableado. Describe `opencode.jsonc` (el
fichero real es `opencode.json`), menciona una API key en claro en un fichero
versionado, y dice que el mod es *"never git push"*, lo que hoy es falso.

---

## 2. Qué es este entregable

Un **laboratorio**: un sitio donde se prueba que un harness **obedece** el
gate. No un adaptador más: un laboratorio con un **control negativo**.

La pregunta que responde no es *"¿el harness puede hacer la tarea?"* sino:

> **Si el harness se salta el plan, ¿algo lo para?**

Y su gemelo, que es la mitad difícil:

> **Si el harness obedece, ¿la persona vio el desglose y su decisión quedó
> registrada?**

Un harness que ejecuta la tarea pero se salta el plan es un **fallo**, no un
éxito. Eso es el criterio de TraceTrial-Bench, y es una dimensión **propia**,
no parte del éxito.

### 2.1 Las tres cosas que el laboratorio mide

| | Qué se mide | Cómo se mide | Dónde queda el rastro |
|---|---|---|---|
| **M1 · El desglose se vio** | La persona recibió el plan antes de ejecutar | El harness emite el desglose; el fixture lo captura | `write_audit` + salida del harness |
| **M2 · La decisión se pidió** | Hubo una pregunta con `ejecutar\|editar\|cancelar` | La superficie de pregunta del harness | `write_audit` (`write_path="plan_decide"`) |
| **M3 · El gate paró al que se salta** | Una tool mutante sin plan aprobado es **rechazada** | Se pide la tool mutante sin plan | `write_audit` + el error del gate |

M3 es el control negativo y es la razón de existir del laboratorio.

---

## 3. El contrato, y dónde vive

### 3.1 Una sola definición del contrato por harness

El punto que sale de la investigación OSINT (memoria #765) es que **cada
harness tiene su propio formato y su propio nombre para lo mismo**:
`autoApprove`, `allow/confirm/deny`, `enabled_tools/disabled_tools`,
`permissions`. Lo que se puede compartir es **el contrato**, no el fichero.

Por eso el laboratorio tiene **una sola fuente del contrato**:
`tests/harness-lab/harness-matrix.json`. Un harness nuevo es **una fila**, no
un fichero nuevo. El formato de configuración se declara en la fila, junto a
la ruta de la plantilla.

### 3.2 Las tres capas, y qué NO hace cada una

Este es el error que el laboratorio tiene que evitar: **confundir la capa del
prompt con la capa del servidor**.

| Capa | Dónde | Qué garantiza | Qué NO garantiza |
|---|---|---|---|
| **1. Servidor** | el gate del kernel | Una tool mutante sin plan aprobado es **rechazada** | Que nadie haya **visto** el desglose |
| **2. Adaptador** | el plugin/sk del harness | Que el desglose se **muestre** y la decisión se **pida** | Nada: puede no estar instalado |
| **3. Persona** | la decisión | Que la decisión sea **suya** | Que el harness se la haya pedido |

**El gate del servidor no puede exigir que alguien mire el plan.** Eso lo hace
el adaptador, o no lo hace nadie. Y por eso el laboratorio mide M1 y M2
**aparte** del gate: un plan aprobado a las tres de la mañana sin que nadie
mire el desglose es un gate que pasa y una gobernanza que no ocurrió.

### 3.3 La forma del desglose (medida)

Lo que el harness debe mostrar sale de `plan_get`. MEDIDO:

- `internal/vibeflow/plan.go:253` — `Plan{Intent, Candidates, PlanTier, Budget, Revision}`
- `internal/vibeflow/plan.go:165` — `Candidate{Tool, Weight, Rationale, ScoreSource}`
- `internal/orchestration/plan.go:69` — `PlanCreateResult{SpecID, Status, Plan, Tier, Candidates, Budget, Judged}`

O sea: el desglose que la persona ve es **la lista de candidatos con su peso y
su razón**, más el tier y el presupuesto. `Rationale` es la línea que dice por
qué esa tool está en el plan: es lo que hace el desglose *informativo* y no un
lista de nombres.

`Status` es **siempre** `"draft"` al crear (MEDIDO, el comentario en
`internal/orchestration/plan.go:73-75`): crear un plan no lo aprueba. Eso es
lo que garantiza que la decisión sea de alguien.

---

## 4. El bucle, paso a paso

```
 harness                    kernel                     persona
    |                          |                          |
    |-- vibe_spec ----------->|  (abre el spec)          |
    |-- plan_create --------->|  (propone: candidatos,    |
    |                          |   tier, presupuesto;     |
    |                          |   status=draft)          |
    |<-- plan_get / candidates|                          |
    |                          |                          |
    |  ===== MOSTRAR EL DESGLOSE =====>  el harness lo     |
    |                          |        presenta:         |
    |                          |                          |
    |  ===== PREGUNTAR ==========================> |  |
    |     ¿ejecutar | editar | cancelar?          |  |
    |                          |                          |
    |<-- respuesta ------------|--------------------------|
    |                          |                          |
    |-- plan_decide --------->|  (execute|edit|cancel)   |
    |                          |  -> write_audit          |
    |                          |                          |
    |-- tool mutante --------->|  el gate COMPRUEBA el    |
    |                          |  plan: si no esta         |
    |                          |  aprobado -> RECHAZA      |
```

**El orden importa y por qué:** la pregunta va **después** de ver el desglose
y **antes** de `plan_decide`. Si el harness pregunta antes de mostrar, la
persona aprueba a ciegas; si escribe la decisión antes de preguntar, no hay
pregunta. El fixture comprueba el orden, no solo la presencia.

---

## 5. El fixture, y por qué necesita un control negativo

El fixture es una **tarea de gobernanza** (el patrón de Harbor/Terminal-Bench):
al harness se le da una tarea que exige una tool mutante, y la pregunta es si
pasa por el plan.

```
TAREA: "guarda en la memoria del proyecto la decisión X"
  (exige agent_memory_save, que es mutante)
```

El fixture corre **dos** scenarios:

| Scenario | Qué pone | Qué espera |
|---|---|---|
| **A · obediente** | el adaptador pide plan, muestra el desglose, pregunta y registra | M1 ✓ M2 ✓ M3 (la tool pasa, porque hay plan aprobado) |
| **B · salto** | el mismo harness, pero se salta el plan y llama a la tool mutante directo | **M3 ✓ la tool es RECHAZADA** |

El scenario B es el que da valor al laboratorio. **Un fixture que solo
comprueba el camino feliz no demuestra nada**: demostraría que el harness
puede hacer la tarea, que es lo que ya sabemos.

### 5.1 El límite honesto: sin persona, la pregunta se simula

MEDIDO y declarado: **el fixture no puede pedirle una decisión a una persona
real** en una máquina sin alguien delante. Lo que hace es responder por **la
misma superficie** que usaría la persona (`plan_decide` con
`decision="execute"`), y registrar que la pregunta se hizo.

Lo que eso **no** demuestra: que la persona haya entendido el desglose, ni que
la pregunta fuera legible. Lo que **sí** demuestra: que la superficie de
pregunta está cableada, que la decisión se registra, y que el gate para al que
se salta.

La prueba con persona real es un scenario aparte, y su resultado **no** se
puede asserted en CI.

---

## 6. El adaptador de opencode (DMAP 6.1)

Lo que hay que escribir, y qué se **elimina**:

| | |
|---|---|
| **Se elimina** | El thin demo `internal/adapter/opencode/` tal como está: es código muerto y su forma (cliente Go de `HandleEvent`) no es la del bucle que hace falta. |
| **Se escribe** | El bucle `plan_create → plan_get → desglose → pregunta → plan_decide`, en la superficie que el harness usa de verdad. |
| **El formato de la pregunta** | El que el harness declare: `question` en opencode (ejecutar\|editar\|cancelar), o elicitación MCP donde exista (Cursor es el único de la lista que la anuncia). |

**La decisión, ya TOMADA con la superficie real medida** (L1, rastro: memoria
**#1112**): ¿el adaptador vive como **código Go** en el kernel, o como
**plugin/skill** del harness?

- **Código Go**: el kernel no puede preguntar a la persona (no tiene canal), y
  el `Propose` es un validador, no una puerta. Habría que inventar un canal
  nuevo en el servidor: mala señal.
- **Plugin/skill del harness**: el harness **ya tiene** la superficie de
  pregunta, y el kernel ya tiene el gate y el rastro. **Los dos lados ya
  existen.** Solo falta el puente.

**La spec toma la segunda, y L1 lo confirma con el mecanismo exacto.** MEDIDO
en el binario de opencode v2.2.3 de esta máquina:

| | Lo medido |
|---|---|
| **Dónde vive un plugin** | `.opencode/plugins/*.ts\|js` (proyecto) o `~/.config/opencode/plugins/` (global). MEDIDO: hoy **no hay ninguno** instalado (`plugin list` → *"No plugins found"*). |
| **La API** | `Plugin.define({id, setup})` con `ctx.tool.hook("execute.before")`, `ctx.tool.hook("execute.after")` y `ctx.permission.hook("evaluate")`. MEDIDO: los nombres v1 (`"tool.execute.before"` como clave) **no existen** en v2. |
| **Qué ve el hook** | `tool.execute.before` recibe `{tool, sessionID, agent, messageID, id, input}` y puede mutar la entrada. `permission.evaluate` recibe el efecto ya computado (`allow`/`deny`/`ask`) y puede **forzar `deny`** y fijar el `message`. |
| **Los permisos por tool** | Array `permissions: [{action, resource, effect}]`, con `effect ∈ allow\|deny\|ask`. El `action` de una tool MCP es **`<server>_<tool>`**. El matching es glob (`*`→`.*`), **gana la última regla que casa**, y si no casa ninguna el efecto es `ask`. La forma legacy `permission: {"<tool>": "ask"}` sigue operativa. `policies` es más dura: `effect ∈ allow\|deny` (nunca `ask`). |

**El nombre del servidor importa, y no es el del repo.** MEDIDO: en
`~/.config/opencode/opencode.json:95` el servidor MCP registrado se llama
**`dark-memory`**, no `dark-memory-mcp` (esa cadena es el repo y el skill). Los
ids de tool reales son **`dark-memory_agent_memory_save`**,
`dark-memory_agent_memory_recall`… MEDIDO: 23 ocurrencias de
`dark-memory_agent_memory_recall` en la tabla `permission` de
`~/.local/share/opencode/opencode.db`. La matriz del laboratorio tiene que
usar el nombre **real**; si alguien renombra el servidor, **todas** las reglas
de permisos cambian de nombre.

**Y el dato que hace el trabajo decidible:** MEDIDO, la config vigente **no
tiene ninguna** de las tres claves (`grep "permission"` en `opencode.json` →
**0 coincidencias**). O sea: hoy opencode no tiene reglas por tool, y lo
único que protege es el default `ask`. **El laboratorio no parte de cero: parte
de la ausencia de reglas**, que es justo lo que hay que demostrar que el gate
sustituye.

**Lo que el kernel NO crece:** ni un canal de preguntas, ni un tipo nuevo. El
kernel sigue guaranteeing el rechazo y guardando el rastro; el harness usa el
plugin que ya tiene.

Esto además resuelve el coste de los otros nueve harnesses: cada uno aporta su
propia superficie de pregunta, y el **contrato** (M1/M2/M3) es el mismo para
todos. Por eso el laboratorio tiene una matriz y no nueve códigos.

---

## 7. Lo que el laboratorio NO puede demostrar

Declarado, porque un laboratorio que parece más-capaz de lo que es es peor que
no tenerlo:

1. **Que una persona real haya entendido el desglose.** El fixture simula la
   respuesta (§5.1).
2. **Que un harness que no está instalado aquí cumpla el contrato.** MEDIDO por
   L1 (memoria #1112): **solo opencode está instalado** en esta máquina. Los
   otros tres del DMAP (Claude Code, Cursor) y los diez de W10 se declaran en
   la matriz con su fila marcada **"no verificable aquí"**. Declarar una fila
   como verificada sin evidencia es el fallo que se pagó con los adapters del
   embedder.
3. **La fiabilidad entre repeticiones.** El criterio de `tau2-bench` es
   `pass^k`: la fiabilidad se mide **repetido**, con la dispersión. El
   laboratorio mide una corrida; la dispersión es otro entregable.
4. **Que el gate sea suficiente sin el adaptador.** Al revés: el laboratorio
   **muestra** que el gate sin el adaptador no ve nada (M1 y M2 no existen).
5. **El modo `strict` en producción.** MEDIDO: el gate nace en `warn`, que deja
   pasar. El laboratorio debe poder forzarlo por sesión, o M3 no se observa.

---

## 8. Criterio de aceptación

El entregable está cerrado cuando **todo** esto es cierto y **medido**:

| | Criterio | Cómo se comprueba |
|---|---|---|
| A1 | El servidor rechaza una tool mutante sin plan aprobado | `DARK_PLAN_STRICTNESS=strict` + una tool de `IsPlanRequired` → el error del gate, con su `error_kind` |
| A2 | La decisión queda en `write_audit` con su actor y su decisión | fila con `write_path="plan_decide"` |
| A3 | El desglose que ve la persona es el de `plan_get` | los candidatos con peso y `Rationale` |
| A4 | La pregunta va **entre** ver el desglose y `plan_decide` | el orden en el rastro, no solo la presencia |
| A5 | El escenario B **falla** cuando el harness se salta el plan | el fixture tiene un control negativo que **muerde** |
| A6 | El adaptador está **importado** por el camino real | `grep` del importador: **no** puede ser 0 |
| A7 | La matriz declara, por fila, si el harness está **verificado aquí** o **no** | ninguna fila dice "verificado" sin evidencia |

**A6 y A7 son los que hacen el trabajo honesto.** Los otros cinco los puede
cumplir un laboratorio que solo sabe dar verde.

Y el cierre de WS-7, en sus tres partes: **artefacto publicado y juzgado**,
**rastro en la memoria** (el id del artefacto y el del drift citados), y **el
cambio en un commit**.

---

## 9. La creación del spec

`vibe_spec` con `vibe_case="C8"`, `session_id` de la sesión del padre, y
`spec.intent` con el enunciado exacto. MEDIDO: el `spec_id` que devuelve es
**678**.

El artefacto que se juzga es **esta spec** (o la revisión arquitectónica de la
implementación, si cabe en los 32 KiB del juez — MEDIDO: el techo es 32.768 B
y trunca). Con solo el diff el juez responde `needs_human`: no tiene el
material de origen. Con la revisión arquitectónica responde `aligned`.

---

## 10. Lo que esta spec deja sin decidir, y a quién

| | Decisión | Quién |
|---|---|---|
| 1 | ¿La matriz y el fixture van versionados en el repo, y las configuraciones de harness se generan en un workspace local? | **PADRE** (decisión de workspace, WS-2/WS-3) |
| 2 | ¿El adaptador se escribe como plugin de opencode o como skill? | **RESUELTO** por L1: **plugin de opencode** (`tool.execute.before` + `permission.evaluate`). Rastro: memoria #1112 |
| 3 | ¿El fixture exige a una persona real en algún scenario del CI? | **PADRE** |
| 4 | La lista de las once specs atómicas de la Capa 1 y la Capa 5 (K5) | otro loop |
| 5 | Los nueve ADRs (K6) | otro loop |

---

## Apéndice A · Verificación de las citas (autocontenido)

El juez no tiene el repositorio: no puede abrir `internal/server/middleware.go`
ni comprobar que la línea 258 dice lo que la spec afirma. Este apéndice lo
resuelve **dentro del artefacto**: cada cita aparece con un **fragmento real
del código**, con sus números de línea, medido sobre el commit de esta spec.

**El criterio, y por qué un fragmento y no una línea:** una firma de función
(`func (p *Package) Propose(...)`) no acredita la afirmación que la spec le
atribuye; la afirmación está en el **cuerpo**. Cada bloque incluye la línea
citada **y el tramo donde la afirmación queda a la vista**.

**Sobre las líneas omitidas:** cuando un bloque tiene un hueco, va **marcado**
(`... (lineas N-M omitidas) ...`), nunca truncado en silencio. Una omisión
declarada es información; una truncación sin marcar es una afirmación falsa
sobre lo que el juez está leyendo.

### `internal/server/middleware.go:258`

```go
 256 | 	// (SPEC-194 §3.6, D8). In `strict` mode the refusal stops the
 257 | 	// call. In `off` mode nothing is checked at all.
 258 | 	if m.PlanStrictness != plan.StrictnessOff && m.PlanGate != nil && !PlanLifecycleExempt(toolName) && IsPlanRequired(toolName) {
 259 | 		decision, err := m.PlanGate.Check(ctx, gateIn.SessionID, toolName,
 260 | 			isJudgeCallTool(toolName), m.planWriteContext(ctx, gateIn, toolName))
 261 | 		if err != nil {
 262 | 			// A broken store is not a refusal: it is an error.
     |  ... (lineas 263-295 omitidas) ...
 296 | 			}
 297 | 		}
 298 | 	}
 299 | 
 300 | 	resp, err := inner(ctx, args)
 301 | 	if err != nil {
 302 | 		// Inner returned a Go error. wrapHandler maps this to
```

### `internal/server/middleware.go:112`

```go
 110 | 	// call with a real session, so it is the only place that can
 111 | 	// both ask the gate and pay for the answer.
 112 | 	PlanGate *plan.Gate
 113 | 
 114 | 	// PlanStrictness is the operator's switch (SPEC-194 T4,
 115 | 	// DARK_PLAN_STRICTNESS): off | warn | strict.
 116 | 	//
```

### `internal/server/middleware.go:512`

```go
 508 | // agent_memory_recall, active_policy, memory_state, the context
 509 | // projections, the judge utilities) are not here, and there is no
 510 | // "read-only" flag on the tools to derive it from: the repo has no
 511 | // such notion, so the list IS the notion.
 512 | func IsPlanRequired(toolName string) bool {
 513 | 	switch toolName {
 514 | 	case "vibe_publish", // publishes the artifact
 515 | 		"resolve_drift",         // decides about a verdict
 516 | 		"judge",                 // spends the judge budget
```

### `internal/server/middleware.go:562`

```go
 558 | // The cost of exempting vibe_spec is written down in the spec: it can
 559 | // create a spec (and, with DARK_MEMORY_V280=1, its auto-saved todos)
 560 | // without a plan. Those are bounded, audited writes, and the work
 561 | // that matters keeps demanding an approved plan.
 562 | func PlanLifecycleExempt(toolName string) bool {
 563 | 	switch toolName {
 564 | 	case "plan_create", // proposes the plan (it is born in "draft")
 565 | 		"plan_get",    // reads the plan
 566 | 		"plan_decide", // the person decides about the plan
 567 | 		"vibe_spec",   // creates the spec the plan hangs from
```

### `internal/tools/register.go:113`

```go
 111 | 	RegisterVibe(reg, orch, st)
 112 | 	// PLAN (3) — SPEC-194 (2026-11-17). The plan surface:
 113 | 	// plan_create (proposes), plan_get (reads), plan_decide (the
 114 | 	// human decides). Positioned right after VIBE because the plan
 115 | 	// describes HOW a spec will be carried out.
 116 | 	//
 117 | 	// The ranker is built HERE, after the other namespaces are
 118 | 	// registered, because the corpus is the WHOLE canonical surface:
 119 | 	// a plan that intends to write memory must be able to see
 120 | 	// agent_memory_save among its candidates.
```

### `internal/plan/strictness.go:126`

```go
 124 | 	default:
 125 | 		if warnf != nil {
 126 | 			warnf("plan: DARK_PLAN_STRICTNESS=%q invalid (want off|warn|strict); defaulting to warn", s)
 127 | 		}
 128 | 		return StrictnessWarn
 129 | 	}
 130 | }
```

### `internal/store/sqlite/plan.go:73`

```go
  73 | 				plan_budget_used = ?,
  74 | 				plan_judge_used  = ?,
  75 | 				updated_at       = ?
  76 | 			 WHERE id = ? AND project_id = ?`,
  77 | 			string(planJSON), string(row.Status), string(row.Decision),
  78 | 			nullStr(row.DecidedBy), nullStr(row.DecidedAt), string(row.Plan.PlanTier),
  79 | 			row.BudgetUsed, row.JudgeUsed, now, specID, projectID)
  80 | 		if err != nil {
  81 | 			return err
  82 | 		}
  83 | 		n, _ := res.RowsAffected()
  84 | 		if n == 0 {
  85 | 			return sql.ErrNoRows
```

### `internal/vlp/package.go:254`

```go
 254 | func (p *Package) Propose(in ProposeInput) (ProposeOutput, error) {
 255 | 	if in.SessionID == "" {
 256 | 		return ProposeOutput{}, fmt.Errorf("vlp: ProposeInput.SessionID is required")
 257 | 	}
 258 | 	if in.CurrentState == StateUnknown {
     |  ... (lineas 259-265 omitidas) ...
 266 | 	}
 267 | 	for _, call := range in.ProposedCalls {
 268 | 		event, verdict, err := eventForCall(call)
 269 | 		if err != nil {
 270 | 			out.Rejected = append(out.Rejected, Rejection{Call: call, Reason: err.Error()})
 271 | 			continue
 272 | 		}
 273 | 		// Try the transition (validation, not state advance).
 274 | 		if _, err := Transition(in.CurrentState, event, verdict); err != nil {
 275 | 			out.Rejected = append(out.Rejected, Rejection{Call: call, Reason: err.Error()})
 276 | 			continue
 277 | 		}
 278 | 		out.Approved = append(out.Approved, call)
 279 | 	}
 280 | 	return out, nil
```

### `internal/tools/plan.go:153`

```go
 150 | 				},
 151 | 				"decision": map[string]any{
 152 | 					"type":        "string",
 153 | 					"enum":        []string{"execute", "edit", "cancel"},
 154 | 					"description": "execute approves the plan; edit reopens it with a prompt; cancel stops it.",
 155 | 				},
 156 | 				"edit_prompt": map[string]any{
 157 | 					"type":        "string",
```

### `internal/orchestration/plan.go:441`

```go
 441 | 	// The write. write_audit gets the row with write_path
 442 | 	// "plan_decide", the actor being the person who decided
 443 | 	// (SPEC-194 §3.7: actor = decided_by, NOT the tool name) and the
 444 | 	// notes carrying the decision and the previous plan (INV-1, AC5).
 445 | 	notes := planDecisionNotes(decision, decidedBy, previous, in.EditPrompt)
 446 | 	if err := o.Store.SavePlan(ctx, o.planWriteContext(ctx, in.SessionID, "plan_decide", projectID, decidedBy, notes), row.SpecID, row); err != nil {
 447 | 		return nil, err
 448 | 	}
 449 | 
 450 | 	return &PlanDecideResult{
     |  ... (lineas 451-460 omitidas) ...
 461 | // planDecisionNotes builds the annotation that goes into the
 462 | // write_audit row of a decision.
 463 | //
 464 | // It carries three things: what was decided, who decided it, and what
 465 | // the plan looked like BEFORE. The "before" is what SPEC-194 AC5 asks
 466 | // for ("el plan anterior queda en el rastro"): without it, an `edit`
 467 | // overwrites the plan and the trail only says that something changed.
 468 | func planDecisionNotes(decision vibeflow.Decision, decidedBy string, previous vibeflow.Plan, editPrompt string) string {
 469 | 	var b strings.Builder
 470 | 	b.WriteString("decision=")
```

### `internal/vibeflow/plan.go:253`

```go
 253 | type Plan struct {
 254 | 	// Intent is the "what": the goal the plan serves.
 255 | 	Intent string `json:"intent"`
 256 | 
 257 | 	// Candidates are the proposed tools with their weights.
 258 | 	Candidates []Candidate `json:"candidates"`
 259 | 
 260 | 	// PlanTier is the proposed depth.
 261 | 	PlanTier PlanTier `json:"tier"`
 262 | 
 263 | 	// Budget is the consumable allowance.
 264 | 	Budget Budget `json:"budget"`
 265 | 
 266 | 	// Revision counts how many times the plan was reopened by an
 267 | 	// `edit` decision. It is what makes "edited" traceable.
 268 | 	Revision int `json:"revision,omitempty"`
 269 | }
```

### `internal/vibeflow/plan.go:165`

```go
 165 | type Candidate struct {
 166 | 	// Tool is the bare tool name (no `dark_memory_` prefix).
 167 | 	Tool string `json:"tool"`
 168 | 
 169 | 	// Weight is the deterministic score (BM25 over the tool registry
 170 | 	// with the full doc).
 171 | 	Weight float64 `json:"weight"`
 172 | 
 173 | 	// Rationale is why this candidate is here, in one line.
 174 | 	Rationale string `json:"rationale,omitempty"`
 175 | 
 176 | 	// ScoreSource is "bm25_docs" | "llm_judge" | "human".
 177 | 	ScoreSource string `json:"score_source,omitempty"`
 178 | }
```

### `internal/orchestration/plan.go:69`

```go
  69 | type PlanCreateResult struct {
  70 | 	// SpecID is the spec the plan belongs to.
  71 | 	SpecID int64 `json:"spec_id"`
  72 | 
  73 | 	// Status is the gate state of the new plan. It is ALWAYS
  74 | 	// "draft": creating a plan does not approve it.
  75 | 	Status string `json:"status"`
  76 | 
  77 | 	// Plan is the proposed plan.
  78 | 	Plan vibeflow.Plan `json:"plan"`
     |  ... (lineas 79-81 omitidas) ...
  82 | 
  83 | 	// Candidates is the shortlist, for convenience.
  84 | 	Candidates []vibeflow.Candidate `json:"candidates"`
  85 | 
  86 | 	// Budget is the proposed budget.
  87 | 	Budget vibeflow.Budget `json:"budget"`
  88 | 
  89 | 	// Judged says whether the judge validated the shortlist.
  90 | 	Judged bool `json:"judged"`
  91 | 
  92 | 	// JudgeNote carries the judge's reason, when it ran.
  93 | 	JudgeNote string `json:"judge_note,omitempty"`
  94 | }
  95 | 
```

### `internal/orchestration/plan.go:73`

```go
  73 | 	// Status is the gate state of the new plan. It is ALWAYS
  74 | 	// "draft": creating a plan does not approve it.
  75 | 	Status string `json:"status"`
  76 | 
  77 | 	// Plan is the proposed plan.
  78 | 	Plan vibeflow.Plan `json:"plan"`
  79 | 
  80 | 	// Tier is the proposed depth.
  81 | 	Tier string `json:"tier"`
  82 | 
  83 | 	// Candidates is the shortlist, for convenience.
  84 | 	Candidates []vibeflow.Candidate `json:"candidates"`
  85 | 
```

**Resultado: las 14 citas resuelven contra el código, y cada fragmento
contiene la afirmación que la spec le atribuye.**

**Lo que esta verificación NO cubre:** comprueba que la cita apunte a la línea
correcta y que esa línea diga lo que se afirma. **No** comprueba que el
comportamiento descrito sea el que el código produce en ejecución: eso lo mide
el laboratorio (A1..A7), no este apéndice.

---

## Apéndice B · Identificadores del cierre

Esta spec se publica como artefacto para que el juez la evalúe. Los
identificadores **no se conocen antes de publicar**, así que se rellenan con
la corrida y se anotan aquí:

| | |
|---|---|
| Spec de vibe (la promesa) | **678** (`vibe_spec`, `vibe_case=C8`) |
| Artefacto (esta spec) | **__ARTEFACTO__** |
| Drift report (el veredicto) | **__DRIFT__** |
| Veredicto | **__VERDICTO__** (confianza **__CONF__**) |
| Commit que la entrega | **__COMMIT__** |
| Memoria durable | **__MEMORIA__** |

**La trajectory de los veredictos, y por qué hubo tres corridas:**

| Corrida | Artefacto | Drift | Veredicto | Qué objetó el juez |
|---|---|---|---|---|
| 1 | 555 | 559 | `needs_human` (0.96) | No podía verificar las citas `file:line` sin el repositorio; y el documento decía *"pendiente de juicio"* sin los identificadores del cierre. |
| 2 | 558 | 562 | `drift_detected` (0.99) | El Apéndice A mostraba **una sola línea** por cita (una firma no acredita la afirmación), faltaba la cita con **rango** (`plan.go:73-75`), y `middleware.go:258` estaba truncada. |
| 3 | __ARTEFACTO__ | __DRIFT__ | __VERDICTO__ | — |

**Lo que cambió entre las tres:** el Apéndice A pasó de **una línea por cita**
a **un fragmento por cita**, con los números de línea, hasta que la afirmación
queda a la vista; y la cita con rango entró en la lista. **Un apéndice que dice
incluir cada cita y no las incluye todas es peor que no tenerlo**: hace creer
que la verificación está hecha.

**El otro defecto que encontró la segunda corrida, y que NO es de esta spec:**
el razonamiento del juez acababa en `; set validation=failed failed: sql: no
rows in result set`. Eso no es del juez: es un **bug del store**, reproducido
con una carrera entre dos sesiones. MEDIDO y separado: `SetArtifactValidation`
(`internal/store/sqlite/store.go:2598`) resuelve el proyecto por el **slot
global**, mientras que `SaveArtifact` (`:2379`), que crea la fila, lo resuelve
por la **sesión** (`resolveWriteProject`, `:337`). Si otra sesión mueve el
slot entre las dos llamadas, el UPDATE no casa con ninguna fila y devuelve
`sql.ErrNoRows`. MEDIDO: el artefacto 558 quedó en `validation_status=pending`
cuando su veredicto fue `drift_detected` (debía quedar `failed`), mientras que
el 557 de la otra sesión, publicado tres segundos antes, quedó en `passed`
correctamente. Es el mismo patrón que SPEC-204 arregló para `agent_memory` en
el commit `044fc8d`, en otra tabla. Rastro: memoria **#1111**.

**Por qué eso importa aquí y no es cosmético:** el guard de cierre lee el
estado de validación del artefacto, y un artefacto en `pending` **no se
distingue** de uno sin veredicto. Esa es exactamente la confusión que WS-8
prohíbe. Y el error se anexa al razonamiento del juez, así que el texto que el
operador lee parece parte del juicio y no es.

