# SPEC-194 — El plan como artefacto y el gate que obliga al harness

> K1 del bucle (TODO #728). Es la spec que convierte el harness compuesto en
> algo exigible, no en algo opcional.
> Base: `9663113` = `upstream/main` tras el merge del PR #39 (SPEC-203 +
> SPEC-204 + parche C2 + keyring) y de los PRs #38, #40 y #41. K0 está
> **merged**, así que esta spec ya no tiene la dependencia bloqueante.
> Estado: propuesta, en revisión.
> Decisiones del operador que la gobiernan: D7 (alcance del gate), D8 (dureza),
> D9 (fuente única del plan), D11 (quién decide la configuración).

---

## 1. El problema

El kernel hoy **juzga el resultado** y no **el camino**: el gate de
drift-at-write (`internal/drift/strictness.go`) comprueba el artefacto contra
el *intent* después de que el trabajo está hecho, y el gate de política
(`internal/policy/gate.go`) comprueba identidad, scope y capacidades de la
llamada. Entre la intención y el artefacto hay un hueco: **nadie ha decidido
qué tools se van a llamar, en qué orden, con qué profundidad y con qué
presupuesto**.

Medido en esta máquina, el hueco tiene tres síntomas concretos:

| # | Síntoma | Evidencia |
|---|---|---|
| **S1** | No hay un artefacto que diga "esto es lo que voy a hacer". El `spec_json` de `vibe_specs` guarda el *qué* (la intención y las tareas), no el *cómo* (los candidatos con peso, el tier, el presupuesto). | `internal/migrate/sqlite/ddl.go:71` — `vibe_specs(spec_json, tasks_json)` |
| **S2** | La palabra "presupuesto" no existe como invariante del kernel. Lo único que hay es un presupuesto de *tokens* para el recap de contexto (`internal/orchestration/session_start.go:65`), que es otra cosa. | `grep budget internal/` → solo `ContextRecapTokens` y `internal/vibeflow/cost.go:487`, que dice literalmente *"Does NOT enforce budgets (use a future L11 budget gate for that)"* |
| **S3** | Las tools se clasifican en dos conjuntos, no en tres. `IsArtifactCreating` (`internal/server/middleware.go:384`) decide si una tool es "creadora de artefacto" y por tanto sujeta al juez; y `RequiresActiveSession` (`internal/policy/gate.go:86`) decide si necesita sesión. **Ninguna de las dos pregunta si la tool muta el estado sin plan aprobado.** | `internal/server/middleware.go:384-397` (7 tools) y `internal/policy/gate.go:86-113` (9 tools exentas) |

### 1.1 La consecuencia

Sin plan, la única defensa es el juez sobre el resultado. Y el juez, medido en
esta máquina, **es tardío y no es determinista**:

- trunca el artefacto a 32 KiB (`v4judge.DefaultMaxArtifactBytes`), así que un
  diff grande se evalúa a medias;
- su veredicto **no es determinista**: el mismo artefacto dio `drift_detected`
  0.99 y `aligned` 0.91 en corridas distintas, porque el proveedor ignora
  `temperature: 0`.

Un gate que solo puede actuar al final y no siempre igual no es un gate: es un
avisador. **El plan mueve la decisión al principio**, donde es barata,
determinista y reversible.

### 1.2 Lo que dice la evidencia externa (medido, con fecha)

El plan no es una invención: es lo que la literatura reciente converge en
recomendar, y lo que este repo ya tiene anotado.

| Fuente | Qué dice | Qué obliga aquí |
|---|---|---|
| **BATS**, arXiv 2511.17006 (2025-11-21) | Dar más presupuesto de tool calls **sin conciencia de presupuesto** no mejora nada: hace falta un *budget tracker* con señal continua y un coste unificado de tokens + tool calls. | El plan lleva presupuesto **medido y agotable**, no un máximo decorativo. |
| **SkillRouter / retrieve-and-rerank**, arXiv 2603.22455 (2026-04-01) | 74,0 % top-1 con recuperar-y-rerankear; no eligiendo de un catálogo entero. | El plan **no elige**: propone un shortlist y el juez lo valida. |
| **Strategy Auctions**, arXiv 2602.02751 (2026-08-24) | Los routers que se apoyan en la descripción de la tarea quedan **por debajo** del agente grande. | La parte determinista **solo propone**. La decisión la toma el juez; la aprobación, la persona. |
| **AICA**, 2025-10-13 | Tiers por **reversibilidad × impacto**; gate en el punto de la consecuencia externa, no en cada handoff; *automation bias*: medir el ratio aprobación/override. | El tier del plan es por profundidad **y** por riesgo; el gate va en el punto de consecuencia. |
| **Zapier HITL**, 2025-11-12 | El revisor puede **editar** la propuesta antes de aprobar; rechazar puede parar o redirigir. | `plan_decide` tiene `execute \| edit \| cancel`, y `edit` es un prompt nuevo, no un rechazo. |
| **Science of Scaling**, arXiv 2512.08296 | Overhead de coordinación 58-515 %; **interacción negativa** con el número de tools (β=-0.141, p<0.001); saturación ~45 %. | El plan **reduce** el número de tools candidatas; sin ese recorte, el harness se degrada. |
| **Task complexity**, arXiv 2510.04311 | La **profundidad** es decisiva; la anchura satura. | El **tier** va por profundidad (corta/media/larga), no por número de tools. |

> **Nota de idioma:** las citas son de fuentes externas y se dejan en su
> formulación original; el resto del documento va en español tuteado, que es la
> convención del repo.

---

## 2. El principio

> **El plan es un artefacto que se persiste, se juzga y se puede agotar.
> Sin plan, una tool que muta el estado no se ejecuta.**

Tres propiedades, y las tres importan:

1. **Persiste.** El plan es una fila, no un estado en memoria. Si el proceso
   muere, el plan sigue ahí y la decisión humana sigue siendo válida. Es la
   diferencia entre "el gate de drift" (que juzga un artefacto ya hecho) y
   "el gate de plan" (que juzga una intención antes de que cueste).
2. **Se juzga.** El plan pasa por el juez como cualquier otro artefacto: el
   juez **valida el shortlist**, no lo elige. Esto es exactamente lo que
   separa la ruta fuerte de la débil (§7).
3. **Se agota.** El presupuesto no es un número que se mira: es un contador
   que, al llegar a cero, **corta**. Un presupuesto que no corta no es un
   presupuesto.

### 2.1 Dónde vive el plan (D9: fuente única)

> **El plan vive en el kernel. `vibe-tools` lo consume por MCP. Nunca al revés.**

Motivo: si el estado del plan existe en dos sitios, se desincronizan y el gate
acaba validando contra el plan equivocado. El kernel lo guarda, lo expone y
lo hace valer; el runtime lo refleja.

Consecuencia concreta: el kernel **no** conoce los planes de otros MCPs
(`[FUTURE-MCP-N]`, `dark-research`) y no puede hacerlos valer. Eso es D7: el
gate de las tools de dark-memory es del kernel; el de las demás es del
adaptador (W2/K2), y en ese caso el kernel expone el plan y el adaptador
obedece.

---

## 3. El mecanismo

### 3.1 El plan: `vibe_specs` con campos nuevos, no un subsistema

El plan **no** es una tabla nueva. Es el `spec_json` de una fila de
`vibe_specs`, con campos nuevos, y su estado de gate vive en columnas nuevas
de la misma tabla.

```go
// internal/vibeflow/plan.go (nuevo)

// Plan es el artefacto: la intención, los candidatos con su peso, el tier
// por profundidad y el presupuesto. Es la fila vibe_specs, no otra tabla:
// el plan de una sesión es su spec abierta.
type Plan struct {
    // Intent es el "qué" (lo que hoy viaja en spec_json).
    Intent string `json:"intent"`

    // Candidates son los candidatos con su peso determinista. Los pesos
    // PROPONEN: no eligen (arXiv 2602.02751).
    Candidates []Candidate `json:"candidates"`

    // Tier es la profundidad estimada: corta | media | larga.
    // La profundidad es decisiva; la anchura satura (arXiv 2510.04311).
    Tier Tier `json:"tier"`

    // Budget es el presupuesto agotable.
    Budget Budget `json:"budget"`
}

type Candidate struct {
    Tool        string  `json:"tool"`
    Weight      float64 `json:"weight"`      // BM25 sobre el registro de tools
    Rationale   string  `json:"rationale"`   // por qué este candidato
    ScoreSource string  `json:"score_source"` // "bm25_docs" | "llm_judge" | "human"
}

type Tier string
const (
    TierShort Tier = "corta"
    TierMedium Tier = "media"
    TierLong  Tier = "larga"
)

type Budget struct {
    MaxToolCalls int `json:"max_tool_calls"`
    UsedToolCalls int `json:"used_tool_calls"`
    MaxJudgeCalls int `json:"max_judge_calls"`
    UsedJudgeCalls int `json:"used_judge_calls"`
}
```

**Por qué BM25 sobre la doc completa.** Medido en la literatura: BM25 sobre el
registro de tools es ~40× más rápido que embeddings en CPU, y **la doc
completa de la tool** fue la mejor configuración. El embedder del kernel está
hoy en `kind=none, dim=0` (búsqueda solo lexical), así que BM25 es además la
única opción disponible sin tocar el embedder.

**Por qué los pesos no eligen.** La parte determinista propone un shortlist
ordenado; el juez lo **valida** (¿es este el conjunto correcto de tools?);
la persona lo **aprueba**. Tres capas, cada una con su autoridad. Esto es el
"gate compuesto" que el operador-patrón (#737)_medió como inexistente.

### 3.2 La tabla: campos nuevos en `vibe_specs`

```sql
-- migración v33 (nueva)
ALTER TABLE vibe_specs ADD COLUMN plan_json     TEXT;      -- el Plan
ALTER TABLE vibe_specs ADD COLUMN plan_status   TEXT NOT NULL DEFAULT 'draft';
ALTER TABLE vibe_specs ADD COLUMN plan_decision TEXT;      -- execute|edit|cancel
ALTER TABLE vibe_specs ADD COLUMN plan_decided_by TEXT;    -- actor
ALTER TABLE vibe_specs ADD COLUMN plan_decided_at TEXT;
ALTER TABLE vibe_specs ADD COLUMN plan_tier     TEXT;      -- corta|media|larga
ALTER TABLE vibe_specs ADD COLUMN plan_budget_used INTEGER NOT NULL DEFAULT 0;
```

`plan_status` es el estado del gate, con **cinco** valores:

| Estado | Significado | Terminal |
|---|---|---|
| `draft` | El plan existe, nadie lo ha aprobado. Una tool mutante **se rechaza**. | No |
| `approved` | La decisión humana es `execute`. Las tools del plan pueden ejecutarse. | No |
| `executed` | El plan se ejecutó hasta agotar su presupuesto o cumplir su objetivo. | Sí |
| `edited` | El revisor cambió el plan con un prompt; se reabrió como `draft`. Es un estado **histórico**: el plan vivo vuelve a `draft`. | Sí (como estado recorrido) |
| `cancelled` | La persona lo canceló. Las tools mutantes se rechazan con `ErrNotApproved` hasta que haya otro plan. | Sí |

**Por qué en `vibe_specs` y no en `vlp_state`.** `vlp_state` es el estado de la
máquina del VLP (9 estados, `internal/vlp/state.go:37-54`) y su esquema tiene
índice UNIQUE en `(project_id, session_id)`: es **una fila por sesión**, no
por spec. El plan es **por spec**: una sesión puede abrir y cerrar varias
specs, y cada una tiene su propio plan y su propia decisión. Meterlo en
`vlp_state` forces la colisión. `vibe_specs` ya es la tabla de las specs y ya
tiene `spec_json` libre para el JSON del plan.

**Por qué no una tabla `plans` nueva.** Una tabla nueva exige migración de
lectura y escritura en los dos stores (sqlite y postgres), y `vibe_specs` ya
tiene lo necesario. La regla del repo (SPEC-204 §3.4) es resolver una sola
cosa en un solo sitio; una tabla `plans` que duplica `vibe_specs` la violaría.

### 3.3 Las TRES razones de gate

Espejo exacto de `policy.Reason`, que hoy tiene 5 razones + `ok`
(`internal/policy/gate.go:57-62`):

```go
const (
    ReasonOK               Reason = "ok"
    ReasonScopeRequired    Reason = "scope_required"
    ReasonCapabilityDenied Reason = "capability_not_granted"
    ReasonPersonaMissing   Reason = "persona_not_resolvable"
    ReasonFrameStale       Reason = "frame_stale_too_far"
    ReasonDriftAtWrite     Reason = "drift_at_write"
    // --- SPEC-194 ---
    ReasonPlanRequired     Reason = "plan_required"
    ReasonNotApproved      Reason = "not_approved"
    ReasonBudgetExceeded   Reason = "budget_exceeded"
)
```

Y sus `ErrorKind()`, que es el campo `data.error_kind` del sobre de error MCP
(`internal/policy/gate.go:107-124`):

| Reason | ErrorKind | Qué significa | Quién lo puede levantar |
|---|---|---|---|
| `ReasonPlanRequired` | `ErrPlanRequired` | Una tool que muta el estado se llamó sin plan abierto. | El gate, en `PreCheck`. |
| `ReasonNotApproved` | `ErrNotApproved` | Hay plan, pero su decisión no es `execute` (está `draft`, `edited` o `cancelled`). | El gate, en `PreCheck`. |
| `ReasonBudgetExceeded` | `ErrBudgetExceeded` | El presupuesto del plan está agotado. | El gate, en `PreCheck`. |

**Por qué `PreCheck` y no `PostCheck`.** `PostCheck` es donde vive el juez de
drift (`internal/server/middleware.go:215`), y su trabajo es juzgar el
**resultado**. El gate del plan juzga la **intención de mutar**, y eso se sabe
antes de ejecutar: en `PreCheck`. Además, el orden importa: `PreCheck` no
puede llamar al juez (sería un round-trip LLM en cada tool call, y el juez
tarda 60-130 s), así que **el plan se valida en `PreCheck` de forma
determinista**, y el juez valida el shortlist **una vez**, cuando el plan se
crea.

### 3.4 La tercerización de las tools: `IsPlanRequired`

Hoy hay dos conjuntos. La spec añade el tercero, en el mismo sitio y con el
mismo estilo:

```go
// internal/server/middleware.go, junto a IsArtifactCreating

// IsPlanRequired lista las tools que MUTAN el estado del kernel y,
// por tanto, exigen un plan aprobado. Es el tercer conjunto: ni el de
// las tools que crea artefactos (IsArtifactCreating, que van al juez
// sobre el resultado) ni el de las que no necesitan sesión.
//
// La lista es hardcoded a propósito, por la misma razón que
// IsArtifactCreating (internal/server/middleware.go:379-383): es parte
// del contrato de cableado. Añadir una tool aquí es un cambio de
// contrato para los harnesses que observen ErrPlanRequired.
func IsPlanRequired(toolName string) bool {
    switch toolName {
    case "vibe_publish",        // publica el artefacto
        "vibe_spec",            // crea el spec
        "resolve_drift",         // decide sobre un veredicto
        "judge",                 // gasta presupuesto de juez
        "consensus",             // gasta presupuesto de juez
        "admin_migrate",         // migra el esquema
        "admin_vacuum",          // compacta la base
        "vlp_handle_event":      // mueve la máquina del VLP
        "agent_memory_save",     // escribe memoria
        "agent_memory_update",   // escribe memoria
        "agent_memory_archive":  // escribe memoria
        "agent_memory_delegate":  // crea un binding de subagente
        "subagent_register",
        "subagent_unregister",
        "llm_key_add",           // toca el keyring del SO
        "llm_key_remove",
        "llm_provider_bind",     // cambia el juez del proyecto
        "oauth_connect",
        "oauth_disconnect",
        "project_create",
        "session_close":
        return true
    }
    return false
}
```

### 3.4-bis Las tools del ciclo de vida del plan están exentas

Hay un **deadlock** que la lista anterior tenía y esta versión corrige: si
`plan_create` y `plan_decide` exigieran un plan aprobado, el primer plan no
podría crearse ni aprobarse. El plan tiene que poder **nacer** y **decidirse**
sin plan previo.

La solución es un conjunto de exentas, **del mismo estilo** que el
`RequiresActiveSession` que ya existe (`internal/policy/gate.go:86`), y por
la misma razón: son tools que el harness necesita **antes** de que haya un
plan, y bloquearlas sería bloquear el flujo entero.

```go
// PlanLifecycleExempt son las tools que participan en el ciclo de vida del
// plan y por tanto NO exigen plan aprobado. Sin esta exención el gate
// tiene un deadlock: el primer plan no se puede crear ni aprobar.
//
// Esta lista es el ÚNICO punto por el que se puede saltar el gate de
// plan, y está acotada a las tools del propio ciclo del plan.
func PlanLifecycleExempt(toolName string) bool {
    switch toolName {
    case "plan_create",   // propone el plan (nace en 'draft')
        "plan_get",      // consulta el plan
        "plan_decide",   // la persona decide sobre el plan
        "health_ping",
        "memory_state",
        "active_policy":
        return true
    }
    return false
}
```

**Y el orden de las comprobaciones es explícito**, porque importa:

```
1. ¿la tool es del ciclo de vida del plan?  → exenta, se comprueba después
2. ¿la tool muta el estado?                 → exige plan
3. ¿hay plan abierto para la sesión?        → si no: ErrPlanRequired
4. ¿el plan está aprobado?                  → si no: ErrNotApproved
5. ¿queda presupuesto?                      → si no: ErrBudgetExceeded
```

**Lo que NO se hace.** No hay una exención genérica de «tools de
administración» ni una bandera `skip_plan_check` en los argumentos: una
excepción por argumentos se puede pasar por alto y dejaría el gate abierto
por la puerta de atrás. La exención es **una lista cerrada y explícita**,
como las otras dos del kernel (`RequiresActiveSession` e
`IsArtifactCreating`).

**El precio de esta exención, dicho en voz alta.** `plan_create` y
`plan_decide` pueden escribirse sin plan. Es lo que hace falta para que el
ciclo arranque, y es coherente con que la persona es quien aprueba: la
herramienta que registra la aprobación no puede exigir la aprobación que
registra. Pero significa que **el gate no protege esas dos tools**, y que
`plan_create` puede crear un plan sin que nadie lo haya pedido. Eso se
compensa porque `plan_create` **no ejecuta trabajo**: solo propone. El
trabajo empieza cuando el plan está aprobado, y ahí sí manda el gate.

**Nota sobre la lista.** Es una lista de *candidatas*: el criterio es «muta
el estado del kernel o gasta presupuesto scarce». No incluye las tools de
lectura (`recall`, `list`, `get`, `health_ping`, `memory_state`, `active_policy`,
`judge_util_*`, `research_*`), porque leer no necesita plan. **Queda abierta
en T2 la calibración fina de la lista**, medible con el KPI de obligación
(§6.2): la lista se ajusta con datos, no con criterio.

### 3.5 El gate: dónde se enchufa

El `GateMiddleware.Wrap` (`internal/server/middleware.go:142`) ya tiene tres
fases. La de plan entra en `PreCheck`, **después** de la comprobación de
identidad y **antes** de `inner`:

```go
// en PreCheck, tras el chequeo de identidad y antes de inner():
if IsPlanRequired(toolName) {
    if planGate != nil {
        verdict := planGate.Check(ctx, toolName, gateIn.SessionID)
        if !verdict.Allowed {
            m.recordRefusal(ctx, toolName, gateIn.SessionID,
                verdict.Reason.ErrorKind(), verdict.Message)
            return refusalResponse(verdict.Reason, verdict.Message, verdict.Hint), nil
        }
    }
}
```

El `PlanGate` es un campo nuevo del middleware, **opcional** (nil = el gate
no está cableado = comportamiento legacy, igual que `DriftChecker` hoy). Esto
mantiene la compatibilidad: un servidor sin plan gate se comporta igual que
hoy.

### 3.6 La dureza: `DARK_PLAN_STRICTNESS` (espejo de `DARK_DRIFT_STRICTNESS`)

`internal/drift/strictness.go` ya tiene el patrón exacto: `Strictness` con
`Off`/`Warn`/`Strict`, `String()`, `ParseStrictness()` defensivo (valor
desconocido → `Off`, "never accidentally refuse a save because the operator
mis-typed the env"). La spec lo replica **con nombre propio**:

| Modo | Efecto |
|---|---|
| `off` | No se comprueba el plan. Default. |
| `warn` | Se registra la negativa en `write_audit` y **se deja pasar**. |
| `strict` | Se **rechaza** con `ErrPlanRequired` / `ErrNotApproved` / `ErrBudgetExceeded`. |

**Por qué `warn` es el default en esta spec, y no `strict`** (D8). En `strict`,
cualquier tool mutante llamada sin plan aprobado se para, y **hoy el trabajo
real no cumple el flujo**: el propio trabajo de este repo (escribir specs,
publicar releases, actualizar memorias) llama tools mutantes sin plan. Un gate
que para el trabajo es un gate que se desactiva el primer día.

Por eso la especificación de la dureza es:

- `warn` es el default, y **registra** cada negativa (con su razón) en
  `write_audit` y en el Error Observatory;
- el **KPI de obligación** se mide desde el día uno (§6.2);
- `strict` se activa cuando el adaptador (W2/K2) esté en pie, y se puede
  activar antes **solo** para las tools que declaren efecto irreversible
  (AICA: reversibilidad × impacto) — que es la vía que D8 deja admitida.

Es decir: el gate **nace midiendo**, no nace bloqueando.

### 3.7 La decisión humana: `plan_decide`

```go
// tool: dark_memory_plan_decide
// entrada:  { spec_id, decision: "execute"|"edit"|"cancel", edit_prompt?: string }
// salida:   { spec_id, plan_status, decided_by, decided_at, revision }
```

Tres decisiones, y las tres tienen precedente medido:

| Decisión | Qué hace | Precedente |
|---|---|---|
| `execute` | Aprueba el plan tal cual. `plan_status = approved`. | Zapier HITL: aprobar. |
| `edit` | El revisor **reescribe** el plan con un prompt. `plan_status = draft` (reabierto) y el plan se vuelve a proponer. **No es un rechazo**: es una redirección. | Zapier HITL: *el revisor puede editar la propuesta antes de aprobar*; Jira: rejecting puede parar **o redirigir**. |
| `cancel` | Termina el plan. `plan_status = cancelled`. Las tools mutantes se rechazan con `ErrNotApproved` hasta que haya otro plan. | Jira: rejecting para. |

**La fila en `write_audit` (INV-1).** La decisión es una escritura y deja
rastro: `table_name = "vibe_specs"`, `row_id = spec_id`, `actor = decided_by`,
`session_id` de la sesión que decidió, `write_path = "plan_decide"`, y el
`notes` con la decisión y, en `edit`, el prompt (con el canario si procede).

**Por qué `plan_decide` es una tool y no una llamada al gate.** Porque la
decisión humana llega por el harness (K2 lo hace con el bucle desglose →
`question` → `plan_decide`), y el kernel tiene que poder **persistirla y
auditarla** igual que cualquier otra escritura. Si el gate aceptara la
decisión por un canal fuera del store, no habría fila, y el rastro (WS-7)
se rompería en el punto exacto donde más importa.

### 3.8 El presupuesto: cómo se gasta y cómo corta

El presupuesto es **por plan** y tiene dos contadores, porque los dos recursos
son distintos y scarcity-distinto (BATS: coste unificado de tokens + tool
calls; aquí tokens ya los cuenta el harness):

| Contador | Lo gasta | Cuándo corta |
|---|---|---|
| `used_tool_calls` | Las tool calls del plan (las mutantes que el plan declara). | Al llegar a `max_tool_calls`: `ErrBudgetExceeded`. |
| `used_judge_calls` | Las llamadas al juez (`judge`, `consensus`). | Al llegar a `max_judge_calls`: `ErrBudgetExceeded`. |

**Quién incrementa el contador.** El gate, después de dejar pasar una tool
mutante del plan: es el único sitio por el que pasa toda llamada, así que es
el único que puede contar sin mentir.

**El presupuesto es por plan, no por sesión.** Una sesión que abre 3 specs
tiene 3 presupuestos. Razón: el presupuesto mide el *trabajo de una
intención*, y la literatura dice que el problema es la coordinación dentro de
una tarea (Science of Scaling), no el volumen total de la sesión.

**El presupuesto lo propone el plan y lo ratifica la persona.** `max_tool_calls`
sale del tier:

| Tier | Profundidad | `max_tool_calls` (propuesta) | `max_judge_calls` (propuesta) |
|---|---|---|---|
| `corta` | Una tool, una comprobación. | 3 | 1 |
| `media` | Tool + verificación + un reintento. | 8 | 2 |
| `larga` | Trabajo con hitos y más de una verificación. | 20 | 4 |

**Estos números son una propuesta, no una ley.** Se calibran con el KPI de
§6.2: si el KPI dice que el 90 % de los planes se agota, el tier está mal
dimensionado. La cifra real vive en el plan y la persona puede editarla (que
es exactamente lo que `edit` permite).

### 3.9 El tier: cómo se calcula

El tier se propone **determinísticamente** a partir del número de candidatos
del shortlist, y lo ratifica la persona:

| Candidatos en el shortlist | Tier propuesto |
|---|---|
| ≤ 3 | `corta` |
| 4-8 | `media` |
| > 8 | `larga` |

Por qué por **número de candidatos** y no por tokens: la profundidad es lo que
decide (arXiv 2510.04311) y en el harness la unidad de profundidad es la tool
call. Y por qué el shortlist y no el catálogo: elegir de un catálogo entero
es exactamente lo que el SkillRouter descarta (74,0 % top-1 con
recuperar-y-rerankear).

**El tier NO va por el `tool_count` de `health_ping`.** Ese campo
(`internal/v4alpha/transport/mcp/health.go:69`) es el número de tools
**registradas en el servidor**, no el tamaño de una tarea. Es una confusión
que ya se detectó (#737) y esta spec no la repite.

---

## 4. El bucle completo

```
   intención del agente
          │
          ▼
   ┌─────────────────────┐
   │ dark_memory_plan_*  │  propone: candidatos + peso (BM25, doc completa)
   │  (determinista)      │           + tier + presupuesto
   └──────────┬──────────┘
              │
              ▼
   ┌─────────────────────┐
   │ dark_memory_judge   │  VALIDA el shortlist (no lo elige)
   │  (rúbrica, 1 vez)   │  → verdict + confianza
   └──────────┬──────────┘
              │
              ▼
   ┌─────────────────────┐
   │ dark_memory_plan_   │  la persona decide:
   │   decide            │  execute | edit | cancel
   └──────────┬──────────┘
              │  (plan_status = approved)
              ▼
   ┌─────────────────────┐
   │ GATE (PreCheck)     │  tool mutante:
   │  · sin plan   → ErrPlanRequired
   │  · no aprobado → ErrNotApproved
   │  · sin budget → ErrBudgetExceeded
   └──────────┬──────────┘
              │  (pasa; el gate incrementa el contador)
              ▼
        la tool se ejecuta
              │
              ▼
   ┌─────────────────────┐
   │ GATE (PostCheck)    │  el juez sobre el RESULTADO (lo que ya existe)
   └─────────────────────┘
```

**Los dos gates son complementarios, no sustitutos.** El de plan decide
*antes* (barato, determinista, reversible); el de drift juzga *después* (caro,
heurístico, con LLM). El primero evita el trabajo mal(LOG) invertido; el
segundo detecta que el trabajo bien(LOG) hecho se desvía.

---

## 5. Tareas

| Id | Tarea | Cierra |
|---|---|---|
| **T1** | `internal/vibeflow/plan.go`: los tipos `Plan`, `Candidate`, `Tier`, `Budget` + `PlanGate.Check` puro (sin store) + test unitario | — |
| **T2** | `internal/server/middleware.go`: `IsPlanRequired` + `PlanLifecycleExempt` (la exención que evita el deadlock) + el campo `PlanGate` opcional en `GateMiddleware` + cableado en `PreCheck`, en el orden explícito de §3.4-bis | S3 |
| **T3** | `internal/policy/gate.go`: las 3 razones + sus `ErrorKind()` + test de los 3 rechazos | S3 |
| **T4** | `internal/plan/strictness.go`: `DARK_PLAN_STRICTNESS` (espejo de `DARK_DRIFT_STRICTNESS`) con `off`/`warn`/`strict` y parseo defensivo | D8 |
| **T5** | Migración v33: los 6 campos nuevos en `vibe_specs` (sqlite + postgres) + test de migración | §3.2 |
| **T6** | `internal/store/`: `SavePlan`, `GetPlan`, `CountPlanCall` (atómico: leer + incrementar) + espejo postgres | §3.8 |
| **T7** | Orchestrator: `dark_memory_plan_create` (propone: candidatos BM25 + tier + presupuesto), `dark_memory_plan_get`, `dark_memory_plan_decide` | §3.7 |
| **T8** | `plan_decide` escribe su fila en `write_audit` (INV-1) con `write_path = "plan_decide"` | §3.7 |
| **T9** | La generación de candidatos: BM25 sobre el registro de tools **con la doc completa**, reordenado; el juez valida el shortlist en `plan_create` | §3.1 |
| **T10** | El KPI de obligación: un contador de negativas por razón, consultable (`dark_memory_anomalies` o una tool propia) | §6.2 |
| **T11** | Tests de rechazo: tool mutante sin plan → `ErrPlanRequired`; plan `draft` → `ErrNotApproved`; presupuesto agotado → `ErrBudgetExceeded`; **y en `strict`, y en `warn` (que deja pasar pero registra)**; y en `off` (que no comprueba nada) | §6 |
| **T12** | `docs/DMAP_V1_1.md`: el plan como Capability 1.4 (`TierCoordinator`) encaja aquí; actualizar el índice de specs | — |
| **T12-bis** | Test del deadlock: `plan_create` sin plan → OK; `plan_decide` sin plan → OK; una tool mutante sin plan → `ErrPlanRequired`. Fija la exención de §3.4-bis | §3.4-bis |

**Nota sobre T9 y el embedder.** El embedder del kernel está en `kind=none,
dim=0`: la búsqueda es **solo lexical**. Por eso el shortlist se construye con
BM25, no con embeddings. Si algún día se activa el embedder, T9 cambia; la
spec no depende de eso.

---

## 6. Criterios de aceptación

### 6.1 Funcionales

- **AC1** — Un plan aprobado se persiste en `vibe_specs` con sus candidatos, su
  tier y su presupuesto, y se recupera íntegro con `plan_get`.
- **AC2** — Una tool de `IsPlanRequired` llamada **sin plan abierto** se
  rechaza con `ErrPlanRequired` (en `strict`) o se deja pasar con su negativa
  registrada (en `warn`).
- **AC3** — Una tool de `IsPlanRequired` llamada con un plan en `draft` se
  rechaza con `ErrNotApproved`.
- **AC4** — Una tool de `IsPlanRequired` llamada con el presupuesto agotado se
  rechaza con `ErrBudgetExceeded`.
- **AC5** — `plan_decide(edit)` **reabre** el plan como `draft` con el prompt
  del revisor aplicado; el plan anterior queda en el rastro.
- **AC6** — `plan_decide` deja fila en `write_audit` con `write_path =
  "plan_decide"`, `actor`, `session_id` y `notes` con la decisión.
- **AC7** — El presupuesto se incrementa **una vez por tool call que pasa**,
  y el contador coincide con el número de tool calls del plan.
- **AC8** — El juez **valida** el shortlist del plan; el plan aprobado contiene
  los candidatos que el juez validó, no otros.
- **AC9** — Las tools de lectura (`recall`, `list`, `get`) **no** exigen plan.
- **AC10** — Con `DARK_PLAN_STRICTNESS=off`, el comportamiento es el de hoy:
  ningún rechazo por plan.
- **AC11** — El contador del presupuesto es **atómico**: dos llamadas
  concurrentes sobre el mismo plan no pueden gastarlo dos veces.
- **AC12** — `go build ./...` y `go test ./...` en verde; el CI del PR en
  verde.
- **AC13** — `plan_create` y `plan_decide` **no** exigen plan aprobado (la
  exención de §3.4-bis): el primer plan se puede crear y aprobar.
- **AC14** — Ninguna tool fuera de la lista de exentas puede saltarse el
  gate: **no existe** ninguna bandera en los argumentos que lo evite.
- **AC15** — La lista de exentas es cerrada: un test falla si se le añade una
  tool sin justificación en el propio test.

### 6.2 El KPI de obligación (la métrica que decide cuándo subir a `strict`)

D8 exige medir desde el día uno. La métrica:

| KPI | Definición | Para qué |
|---|---|---|
| **Ratio de rechazo** | Negotiaciones por plan / total de tool calls mutantes | Si es ~0, el gate no hace nada y el plan no se está usando. |
| **Negativas por razón** | Conteo de `ErrPlanRequired` / `ErrNotApproved` / `ErrBudgetExceeded` | Distingue «no hay plan» de «el plan no se aprobó» de «se agotó». |
| **Ratio aprobación/edición** | Zapier/AICA:Many aprobaciones sin una edición → *automation bias*, señal a investigar | Si nadie edita nunca, la puerta humana es decorativa. |
| **Profundidad media** | Calls por plan abierto | Si la media es 1, el tier `corta` está bien calibrado. |

**La regla que sale de aquí:** `strict` se propone cuando el ratio de rechazo
es > 0 **y** hay al menos un adaptador (K2) que produce planes. Antes de eso,
`strict` es un gate que solo bloquearía el trabajo real.

---

## 7. La ruta del veredicto (por qué esto no es la cadena NLI)

La spec tiene que distinguir **dos rutas de veredicto**, porque no valen lo
same:

| Ruta | Qué es | Para qué sirve |
|---|---|---|
| **Rúbrica** (`v4judge`, `provider_id` con prefijo `judge-`) | El juez evalúa el artefacto contra el intent con una rúbrica de veredicto | **La que discrimina.** Encontró cuatro defectos reales en un ciclo. |
| **Cadena NLI** (`provider_id` con prefijo `chat-`) | `premise`/`hypothesis` → `entailment`/`contradiction`/`neutral` | El fallback **legacy**. Para código es un juez débil: respondió `entailment` (aligned 1.0) al mismo artefacto que la rúbrica había marcado `drift_detected` tres veces. |

En el `reasoning` se distingue: `nli=entailment` es la cadena NLI; un párrafo
escrito es la rúbrica. **Para el plan, hay que publicar y comprobar que habló
la rúbrica.**

**El plan se juzga con la rúbrica**, y el artefacto que se publica es **el
plan**, no el diff. Motivo: el techo del artefacto es 32 KiB
(`v4judge.DefaultMaxArtifactBytes`) y el techo del *hypothesis* (el intent) es
4096 bytes. Un plan cabe; un diff grande no.

**El veredicto del plan no es determinista** (el proveedor ignora
`temperature: 0`): un veredicto aislado no cierra nada. Lo que cierra es el
**ciclo completo**: artefacto + rastro + commit (WS-7).

---

## 8. Fuera de alcance (y por qué)

| No lo resuelve esta spec | Por qué |
|---|---|
| **Los adaptadores de harness** (el bucle desglose → `question` → `plan_decide`) | Es W2/K2 (TODO #729). Aquí se garantiza que **cuando** el harness nombre su plan, el kernel lo hace valer. |
| **El gate de las tools de otros MCPs** (`[FUTURE-MCP-N]`, `dark-research`) | D7: el kernel no puede hacer valer el plan de un MCP que no es suyo. Ese gate es del adaptador. |
| **La personalización de la configuración** (dureza, presupuestos, umbrales del tier) | Es W11 (TODO #767), y D11: lo decide la persona al configurar. Aquí el valor por defecto es `warn` y el resto son constantes. |
| **El panel** | D5: «el panel no es el producto». |
| **El token de npm** | Sin relación. Bloqueado por credenciales, en pausa. |

---

## 9. Qué NO cambia

- El gate de drift-at-write sigue juzgando **el resultado**. No se toca
  `internal/drift/strictness.go`.
- `IsArtifactCreating` no cambia: sigue decidiendo qué tools van al juez sobre
  el artefacto.
- `RequiresActiveSession` no cambia: la lista de tools exentas de sesión es la
  misma.
- El *slot* global sigue siendo el último recurso de resolución (SPEC-204 §8).
- La constitution sigue mandando: todo write del plan lleva
  `constitution_id@version` (WS-5).

---

## 10. El riesgo que esta spec asume

En `strict`, el gate **para** el trabajo real de este repo. Se asume que:

1. `warn` es el default, y por tanto **nadie se queda bloqueado** mientras
   K2 no exista;
2. el KPI se mide desde el primer día, así que la decisión de subir a
   `strict` es **dato**, no opinión;
3. la lista `IsPlanRequired` es una **candidata** que se calibra con el KPI
   (T2 la deja abierta).

Si se activara `strict` sin que el KPI lo justifique, el efecto sería un gate
que la gente desactiva — el peor resultado posible, porque da la falsa
impresión de que hay gobernanza.

---

## 11. Relación con el harness compuesto

Esta spec es el **primer caso real** del principio que sostiene al harness
compuesto (#737):

> **La identidad se resuelve una vez, en un sitio, y viaja hasta donde se
> aplica** (SPEC-204).

Y añade el segundo principio:

> **El plan se decide una vez, en un sitio, y se puede agotar.**

Tres capas con tres autoridades, y ninguna decide lo que es de la otra:

| Capa | Autoridad | Qué NO puede hacer |
|---|---|---|
| **Determinista** (BM25, tier, presupuesto) | Proponer un shortlist | Elegir. Los routers por descripción quedan por debajo del agente grande (arXiv 2602.02751). |
| **Juez** (rúbrica) | Validar el shortlist | Ejecutar. El juez juzga, no manda. |
| **Persona** (`plan_decide`) | Aprobar, editar, cancelar | No puede saltarse el presupuesto: si se agotó, no hay decisión que lo reviva (salvo `edit`, que propone otro plan). |

Y el **gate** es el que hace que las tres valgan: sin él, las tres son
documentos.

---

## 12. Dependencias

| Dependencia | Estado | Nota |
|---|---|---|
| K0 (SPEC-203/204) | **Merged** el 2026-11-17 (`f09b3f7`), junto con los PRs #38, #40 y #41 | **Dependencia discharged.** Era bloqueante mientras estuvo sin mergear: sin la identidad resuelta, la fila de `plan_decide` caería en el proyecto equivocado — el defecto exacto que SPEC-204 arregla. Con SPEC-204 en `main`, el código de K1 puede aterrizar. Queda una dependencia de **secuencia**, no de bloqueo: `strict` sigue necesitando el adaptador de W2/K2. |
| `internal/policy` (gate) | Existe | Se le añaden 3 razones. |
| `internal/drift` (strictness) | Existe | Se le copia el patrón. |
| `internal/vlp` (states) | Existe | El plan **no** es un estado del VLP (§3.2). |
| Embedder | `kind=none, dim=0` | Por eso BM25 y no embeddings. |
| W2/K2 (adaptador) | No existe | Sin él, `strict` no se activa (§6.2). |
| W11 (personalización) | No existe | Los valores por defecto de esta spec son constantes, hasta que W11 los vuelva configurables. |
