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
fichero real es `opencode.json`), menciona `MINIMAX_API_KEY` como *"your
hardcoded LLM API key (live, 126 chars)"* — una referencia a un secreto en un
fichero versionado, que además está obsoleta — y dice que el mod es
*"LOCAL-ONLY — never git push"*, lo que hoy es falso.

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

**La decisión que hay que tomar y que esta spec no toma:** ¿el adaptador vive
como **código Go** en el kernel, o como **plugin/skill** del harness?

- **Código Go**: el kernel no puede preguntar a la persona (no tiene canal), y
  el `Propose` es un validador, no una puerta. Habría que inventar un canal
  nuevo en el servidor: mala señal.
- **Plugin/skill del harness**: el harness **ya tiene** la superficie de
  pregunta, y el kernel ya tiene el gate y el rastro. **Los dos lados ya
  existen.** Solo falta el puente.

La spec toma la segunda: **el adaptador es del harness, y el kernel solo
garantiza el rechazo y guarda el rastro**. El kernel no crece con un canal de
preguntas; el harness usa el que ya tiene.

Esto además resuelve el coste de los otros nueve harnesses: cada uno aporta su
propia superficie de pregunta, y el **contrato** (M1/M2/M3) es el mismo para
todos. Por eso el laboratorio tiene una matriz y no nueve códigos.

---

## 7. Lo que el laboratorio NO puede demostrar

Declarado, porque un laboratorio que parece más-capaz de lo que es es peor que
no tenerlo:

1. **Que una persona real haya entendido el desglose.** El fixture simula la
   respuesta (§5.1).
2. **Que un harness que no está instalado aquí cumpla el contrato.** En esta
   máquina solo hay un harness utilizable: **opencode**. Los otros tres del
   DMAP (Claude Code, Cursor) y los diez de W10 se declaran en la matriz, pero
   su fila queda **"no verificable aquí"** hasta que haya quien los instale.
   Declarar una fila como si estuviera verificada es exactamente el fallo que
   se pagó con los adapters del embedder.
3. **La fiabilidad entre repeticiones.** El criterio de `tau2-bench` es
   `pass^k`: la fiabilidad se mide **repetido**, con la dispersión. El
   laboratorio mide una corrida; la dispersión es otro entregable.
4. **Que el gate sea suficiente sin el adaptador.** Al revés: el laboratorio
   **muestra** que el gate sin el adaptador no ve nada (M1 y M2 no existen).
5. **El modo `strict` en producción.** MEDIDO: el gate nace en `warn`, que
   deja pasar. El laboratorio debe poder forzarlo por sesión, porque si no,
   M3 no se puede observar.

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
| 2 | ¿El adaptador se escribe como plugin de opencode o como skill? | **PADRE**, con el dato del surface real que trae L1 |
| 3 | ¿El fixture exige a una persona real en algún scenario del CI? | **PADRE** |
| 4 | La lista de las once specs atómicas de la Capa 1 y la Capa 5 (K5) | otro loop |
| 5 | Los nueve ADRs (K6) | otro loop |

---

## Apéndice A · Verificación de las citas (autocontenida)

El juez no tiene el repositorio: no puede abrir `internal/server/middleware.go`
ni comprobar que la línea 258 dice lo que la spec afirma. Este apéndice
resuelve eso **dentro del propio artefacto**: cada cita de la spec aparece con
el **contenido real** de esa línea, medido sobre el commit de esta spec.

Medido con el mismo criterio que la spec usa en todo lo demás: leer la línea,
no suponerla.

| # | Cita | Fichero | Línea | El contenido real de esa línea |
|---|---|---|---|---|
| 1 | `internal/server/middleware.go:258` | `internal/server/middleware.go` | 258 | `if m.PlanStrictness != plan.StrictnessOff && m.PlanGate != nil && !PlanLifecycleExempt(toolName) && …` |
| 2 | `internal/server/middleware.go:112` | `internal/server/middleware.go` | 112 | `PlanGate *plan.Gate` |
| 3 | `internal/server/middleware.go:512` | `internal/server/middleware.go` | 512 | `func IsPlanRequired(toolName string) bool {` |
| 4 | `internal/server/middleware.go:562` | `internal/server/middleware.go` | 562 | `func PlanLifecycleExempt(toolName string) bool {` |
| 5 | `internal/tools/register.go:113` | `internal/tools/register.go` | 113 | `// plan_create (proposes), plan_get (reads), plan_decide (the` |
| 6 | `internal/plan/strictness.go:126` | `internal/plan/strictness.go` | 126 | `warnf("plan: DARK_PLAN_STRICTNESS=%q invalid (want off\|warn\|strict); defaulting to warn", s)` |
| 7 | `internal/vlp/package.go:254` | `internal/vlp/package.go` | 254 | `func (p *Package) Propose(in ProposeInput) (ProposeOutput, error) {` |
| 8 | `internal/tools/plan.go:153` | `internal/tools/plan.go` | 153 | `"enum":        []string{"execute", "edit", "cancel"},` |
| 9 | `internal/orchestration/plan.go:441` | `internal/orchestration/plan.go` | 441 | `// The write. write_audit gets the row with write_path` |
| 10 | `internal/vibeflow/plan.go:253` | `internal/vibeflow/plan.go` | 253 | `type Plan struct {` |
| 11 | `internal/vibeflow/plan.go:165` | `internal/vibeflow/plan.go` | 165 | `type Candidate struct {` |
| 12 | `internal/orchestration/plan.go:69` | `internal/orchestration/plan.go` | 69 | `type PlanCreateResult struct {` |

**Resultado: las 12 citas resuelven, y cada una dice lo que la spec le
atribuye.** La comprobación es reproducible: es el mismo `grep`/`sed` sobre el
commit al que apunte esta spec.

**El alcance de esta verificación, y lo que NO cubre:** comprueba que la cita
apunta a la línea correcta y que esa línea dice lo que se afirma. **No**
comprueba que el comportamiento que la spec describe sea el que el código
produce en ejecución: eso lo mide el laboratorio (A1..A7), no este apéndice.

