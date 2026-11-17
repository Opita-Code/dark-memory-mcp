# SPEC-204 — Resolución central de la identidad (sesión → proyecto)

> Corrige el bloqueante **B1** de la revisión de merge del PR #39
> (`fix/project-id-desde-la-sesion`, SPEC-203).
> Base: SPEC-203 (`docs/specs/SPEC-203-project-id-desde-la-sesion.md`).
> Estado: propuesta, en revisión.

---

## 1. El problema

SPEC-203 corrigió el defecto que motivó la spec: la escritura ya no caía en el
proyecto equivocado. Pero aplicó la regla **a medias**:

- la **escritura** va al proyecto de la sesión, y solo en las tools que
  declaran `session_id`;
- la **lectura** y **12 de las ~28 rutas de escritura** siguen siguiendo el
  *slot* global del `Store`.

El resultado es una **asimetría**: escribir y leer ya no son coherentes. Antes
de SPEC-203 eran coherentes (aunque en el proyecto equivocado): todo caía en
el slot. Ahora la escritura va a un sitio y la lectura a otro, y el que escribe
no ve lo que escribió.

### 1.1 Evidencia medida (binario con SPEC-203 en marcha, base real)

| Paso | Resultado |
|---|---|
| `session_start(<proyecto A>)` → SA | slot = proyecto A |
| `agent_memory_save(session_id=SA, …)` | fila en el proyecto A ✅ |
| `session_start(<proyecto B>)` → SB | el slot se mueve |
| `agent_memory_recall(query=…)` **sin** `session_id` | **0 hits** ❌ |
| `agent_memory_recall(query=…, session_id=SA)` | 1 hit ✅ |

La fila está donde debe estar y, aun así, **el agente no la ve**. El bucle del
producto —«escribo, y luego lo recuerdo»— queda roto.

### 1.2 El diagnóstico de fondo

No son dos bugs: es **una sola pregunta resuelta dos veces**.

- El **gate** (el middleware) resuelve la sesión → proyecto para juzgar la
  llamada.
- El **store** vuelve a resolver la sesión → proyecto para aplicarla.

Cuando las dos resoluciones coinciden, todo funciona. Cuando no coinciden —y no
coinciden, porque el slot es estado mutable compartido por el proceso— el
sistema se rompe por el lado más silencioso: **no hay error, hay un vacío**.

> **Un gate solo es tan fuerte como la resolución de contexto que hay detrás.**

---

## 2. El principio

> **La identidad (sesión, proyecto, operador) se resuelve una vez, en un sitio,
> y viaja hasta el punto donde se aplica.**

Tres capas, cada una con una sola responsabilidad:

| Capa | Dónde | Qué hace |
|---|---|---|
| **Resolución** | `internal/server/middleware.go` | Resuelve sesión → proyecto. Ya lo hace para el gate; ahora es **la** resolución. |
| **Transporte** | `context.Context` | La identidad resuelta viaja con la llamada. Sin estado en el proceso. |
| **Aplicación** | `internal/store/*` | Aplica la identidad. Es la única capa que dice «no». |

El store **no** pierde su autoridad: sigue siendo quien decide y quien
rechaza. Lo que cambia es que ahora aplica **la misma** identidad que el gate
juzgó, en vez de resolverla otra vez y poder obtener otra respuesta.

---

## 3. El mecanismo

### 3.1 El transportador (`internal/session/scope.go`, nuevo)

```go
// Scope es la identidad de una llamada: la sesión que la hizo y el
// proyecto al que pertenece esa sesión.
type Scope struct {
    SessionID string
    ProjectID string
}

func WithScope(ctx context.Context, sc Scope) context.Context
func ScopeFromContext(ctx context.Context) (Scope, bool)
```

`internal/session` es el paquete que ya define la sesión como primitiva, y lo
importan **tanto** el servidor como el store: no hay ciclo de imports.

### 3.2 La resolución (`internal/server/middleware.go`)

En `GateMiddleware.Wrap`, después de `buildGateInput` (que ya resolvió sesión y
proyecto) y **antes** de `inner(ctx, args)`:

```go
ctx = session.WithScope(ctx, session.Scope{
    SessionID: gateIn.SessionID,
    ProjectID: gateIn.ProjectID,
})
```

Una línea, en el único sitio por el que pasa toda llamada del servidor. A
partir de ahí, **ninguna tool tiene que declarar `session_id`** para que su
escritura caiga en el proyecto correcto.

### 3.3 La aplicación (`internal/store/`)

**Escritura** — `resolveWriteProject(ctx, wc) (string, error)`:

1. sesión = `wc.SessionID`; si está vacía, la del `ctx`;
2. si hay sesión → proyecto = el de esa sesión (del `ctx` si es la misma
   sesión, si no con `ProjectIDForSession`);
3. si no hay sesión → `wc.ProjectID` (los flujos administrativos);
4. si tampoco → el *slot* activo (el último recurso, y el que esta spec
   marca como **deprecado** como fuente de resolución).

**Lectura** — `resolveReadProject(ctx, projectID, sessionID) (string, error)`:
misma cadena, y con la regla que ya existía: **una lectura nunca amplía**
— si el proyecto explícito contradice al de la sesión, la lectura se rechaza
con `ErrCrossProjectAccess`.

**Auditoría** — `recordWriteTx` / `recordWriteLockedTx`: si el evento no lleva
`session_id`, se toma del `ctx`. Así **toda** escritura deja rastro de la
sesión que la hizo, no solo las que la declaraban.

### 3.4 El store deja de resolver dos veces lo mismo

Cuando el `ctx` ya trae la identidad resuelta, el store **no vuelve a
consultar**: usa el proyecto del `ctx` cuando la sesión coincide. Una sola
consulta por escritura, no dos.

---

## 4. Regla de fallo: **cerrado**, nunca degradado

Si la resolución falla (error de E/S, base bloqueada, contexto cancelado), la
operación **falla**. No cae al slot.

Motivo: el slot es exactamente el estado mutable y compartido que esta spec
existe para eliminar. Degradar hacia él reintroduce el bug original como
camino silencioso: el peor sitio, porque **no deja error**.

Es el hallazgo **A1** de la revisión, y el arreglo es de una línea por sitio.

---

## 5. Tareas

| Id | Tarea | Hallazgo que cierra |
|---|---|---|
| **T1** | `internal/session/scope.go`: el transportador + test unitario | — |
| **T2** | Middleware: inyectar el scope resuelto antes de `inner` + test de wire | B1 (transporte) |
| **T3** | Store sqlite: la cadena de resolución usa el `ctx` y **falla cerrado** | B1, **A1** |
| **T4** | Store sqlite: el `write_audit` lleva `session_id` desde el `ctx` | B1.a (rastro) |
| **T5** | Store sqlite: `vlp_state` se lee por `(session_id, project_id)` | **A2** |
| **T6** | Store sqlite: `recall` y `list` se comportan igual con `scope=session` | **M1** |
| **T7** | Store postgres: espejo de T3–T6 y `GetVLPStateByID` implementado | **M2** |
| **T8** | Store sqlite: resolver el proyecto **antes** de tomar el mutex | **M3** |
| **T9** | Tests que fijan el invariante `sessions.session_id` UNIQUE; corregir el enunciado de la topología en SPEC-203 | **L5**, **D1** |

---

## 6. Criterios de aceptación

- **AC1** — Una escritura hecha en una llamada que nombra la sesión, **sin**
  declarar `session_id` en la tool, aterriza en el proyecto de esa sesión.
- **AC2** — Una lectura hecha en una llamada que **nombra** su sesión
  devuelve la fila que esa sesión escribió, aunque el *slot* diga otra cosa.
  *(La asimetría de SPEC-203 cerrada: escritura y lectura aplican la misma
  identidad.)*
  **Lo que AC2 NO dice**: que una llamada que *no* nombre su sesión encuentre
  su fila. Eso no puede garantizarse en el servidor (§11 y §13): sobre stdio,
  un proceso sirve a varias sesiones lógicas, y adivinar sería escribir en el
  proyecto de otro. La garantía del servidor es la **coherencia** (una
  identidad por llamada, aplicada en todas partes); la del **bucle cerrado** la
  da el kernel, que nombra la sesión.
- **AC3** — Un error de resolución hace **fallar** la operación; no degrada al
  slot. *(A1.)*
- **AC4** — Dos filas de `vlp_state` con el mismo `session_id` en proyectos
  distintos **no** se mezclan. *(A2.)*
- **AC5** — `recall(scope=session)` respeta la sesión nombrada, igual que
  `list`. *(M1.)*
- **AC6** — Postgres implementa `GetVLPStateByID` y el frame source no pierde
  el `ScopeFrame`. *(M2.)*
- **AC7** — El `write_audit` de una escritura sin sesión explícita lleva
  `session_id`. *(B1.a.)*
- **AC8** — Un test falla si `sessions.session_id` deja de ser UNIQUE. *(L5.)*
- **AC9** — `go build ./...` y `go test ./...` en verde; el CI del PR en verde.

---

## 7. Plan de pruebas

| Nivel | Qué prueba |
|---|---|
| Unitario | `internal/session`: el `Scope` viaja por el `ctx` y se recupera. |
| Unitario | `internal/store/sqlite`: escritura con el `ctx` portador → proyecto de la sesión; error de resolución → error. |
| Unitario | `internal/store/sqlite`: `vlp_state` con dos proyectos y el mismo `session_id` → aislamiento. |
| Unitario | `internal/store/sqlite`: `recall` y `list` con `scope=session`. |
| Unitario | `internal/migrate/sqlite`: `sessions.session_id` es UNIQUE (intento de insertar duplicado → error). |
| Wire | `tests/wire/`: una llamada MCP real con `session_id` en los argumentos y **sin** `session_id` en el cuerpo de la tool → la fila cae en el proyecto de la sesión. |
| Regresión | La A/B con dos binarios contra la base real (la que destapó el fallo del gate en SPEC-203): el `recall` sin `session_id` debe **encontrar** la fila. |

---

## 8. Fuera de alcance (y por qué)

| No lo resuelve esta spec | Por qué |
|---|---|
| Que el *harness* nombre su sesión en cada llamada | Es trabajo del **kernel** (adaptadores DMAP, W1/K2). Aquí solo se garantiza que **cuando** se nombre, vale en todas partes (§13). El servidor no puede inventar la identidad de una llamada que no la declara: sobre stdio, un proceso sirve a varias sesiones lógicas (§11). |
| Que el *slot* global deje de existir | Es el último recurso de la cadena. Depender de él es el bug; **usarlo** solo cuando no hay nada mejor ya no es el bug. Se marca **deprecado** como fuente de resolución. |
| La identidad del *operador* | Hoy es un argumento explícito de cada tool. Unificarla con la sesión es otra spec. |
| El cambio de **frontera de confianza** (§10) | Se documenta aquí, pero su instrumentación (grants por sesión) es otra spec. |

---

## 9. Qué NO cambia

- `GetSession` conserva su filtro por proyecto activo: INV‑7 no se relaja.
- Una lectura que nombra un proyecto que contradice al de la sesión se
  rechaza con `ErrCrossProjectAccess`: una lectura nunca amplía.
- `session_start` sigue abriendo una sesión en el proyecto que dice, y sigue
  fijando el slot: es el que abre la sesión, luego es su dueño.
- El gate sigue siendo el que juzga; el store sigue siendo el que dice «no».

---

## 10. Cambio de frontera de confianza (A3 de la revisión)

Al resolver la sesión **por id** y no por el proyecto activo, **nombrar la
sesión de otro proyecto pasa a ser una identidad válida** para las 78 tools.
Antes se rechazaba («identity unavailable for this session»).

Mitigaciones reales:

- los ids de sesión tienen **128 bits** de entropía (`sess-` + 32 hex): no son
  adivinables;
- `ListSessions` está filtrado por proyecto: no se pueden enumerar sesiones
  ajenas;
- `HasProjectAccess` concede el scope `"*"`: hoy la comprobación de proyecto
  del gate solo corre si la sesión tiene una spec abierta.

El riesgo práctico es bajo, pero es un **cambio de modelo de confianza** y
queda escrito como tal. Regla que se hereda: **el id de sesión es una
credencial**: no se registra en logs abiertos ni se acepta de un argumentario
sin el scope del operador.

---

## 11. La topología real (corrige el D1 de la revisión)

> **Corrección de una corrección.** La revisión de merge del PR #39 (D1)
> afirmaba que el enunciado de SPEC-203 sobre el proceso compartido «no está
> medido» y que describía una topología distinta. **Era falso**, y esta spec
> lo corrige: el proceso **sí** se comparte. Lo que faltaba era la medición.

SPEC-203 afirma que «el servidor MCP de stdio comparte ese proceso entre varios
clientes». Ese enunciado es **correcto**. Lo que no existía era la prueba
dura, y ahora la hay: el servidor expone `runtime.pid` y `runtime.booted_at`
en `health_ping`, así que la topología se **mide** en vez de interpretarse.

**Medido (2026-11-17):**

| Observación | Dato |
|---|---|
| Mi proceso | `pid 1209767`, arrancado `14:11:05Z` |
| Mi escritura (proyecto A) | `14:32:18Z` → el slot de **ese** proceso = proyecto A |
| `session_start` de otro cliente (proyecto B) | `14:44:03Z` → movió **el slot de mi proceso** |
| `health_ping` a las `14:45:38Z` | `active_project` = proyecto B, en **ese** proceso |
| `session_start` de un tercer cliente (proyecto C) | `14:45:25Z` → **no** movió ese slot: estaba en otro proceso |

Dos consecuencias, y las dos importan:

1. **El proceso se comparte** entre clientes, y hay varios procesos a la vez.
   El *slot* es estado en memoria **por proceso**.
2. **Sobre un transporte stdio, el servidor no puede saber qué agente lógico
   está llamando** cuando la llamada no nombra su sesión: el id de sesión MCP
   en stdio es la constante `"stdio"` (por proceso), y un mismo proceso sirve a
   varias sesiones lógicas de proyectos distintos (medido arriba).

Por eso esta spec **no** intenta adivinar: fija la identidad que la llamada
declara, la hace viajar, y la aplica en todas partes. Lo que no puede hacer es
inventar la identidad de una llamada que no la declara — y por eso el *slot*
queda **deprecado** como fuente de resolución (§8) y la obligación de nombrar
la sesión pasa al **kernel** (§13).

El texto de SPEC-203 queda actualizado en T9 con esta medición.

---

## 13. Qué cierra SPEC-204 y qué NO (y por qué no puede cerrarlo aquí)

**Cierra:**

- La **asimetría** de SPEC-203: la escritura iba a la sesión y la lectura iba
  al slot. Ahora las dos sides aplican **la misma** identidad, la de la
  llamada. El gate juzga y el store ejecuta **la misma** cosa.
- Los **16 puntos de escritura** y las **lecturas del vibe-loop**: ya no
  depende de que la tool declare `session_id`.
- **A1** (fallo cerrado), **A2** (tenant en `vlp_state`), **M1** (`recall` y
  `list` coinciden), **M2** (postgres), **M3** (mutex), **L5** (invariante
  UNIQUE con test).

**NO cierra, y no puede:**

- **Que una llamada que no nombra su sesión lande en el proyecto de esa
  sesión.** Con el transporte stdio, un proceso sirve a varias sesiones
  lógicas de proyectos distintos (§11), así que el servidor **no puede
  saber** a cuál de ellas pertenece una llamada que no lo dice. Cualquier
  «adivinación» sería una asignación de identidad falsa: el agente escribiría
  en el proyecto de otro.
- Por eso, la regla que queda es: **la llamada nombra su sesión, o el
  servidor usa el último recurso (el slot), que es un comportamiento
  explícito y no una garantía.**

**La obligación es del kernel, no del servidor.** Los adaptadores DMAP
(W1/K2) nombran la sesión en cada llamada: entonces el bucle leer/escribir
queda cerrado por construcción, y no por una heurística del servidor. Este es
el punto donde SPEC-203 se quedó corto: quiso arreglar el store cuando el
problema de fondo era **quién dice qué sesión es**.


## 14. Relación con el harness compuesto

Esta spec es la primera aplicación de una regla que el gate compuesto va a
exigir:

> **La identidad se resuelve una vez, en un sitio, y viaja hasta donde se
> aplica.**

El gate compuesto (W1) tendrá tres capas —intención, plan con pesos,
veredicto— y un gate que decide si algo se entrega. Si cada capa resuelve su
propio contexto, el gate juzgará una cosa y el kernel ejecutará otra. El
segundo veredicto es el que cuenta.

Por eso el cambio no es «un bug del store»: es el **primer caso real** del
principio que sostiene al harness compuesto.
