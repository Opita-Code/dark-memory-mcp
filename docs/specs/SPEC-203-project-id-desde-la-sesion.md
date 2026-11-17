# SPEC-203: El project_id se resuelve desde la sesion, no desde un slot global

> **Estado**: Accepted
> **Fecha**: 2026-11-17
> **Autor**: agente dark-memory-v4, con la decision del operador (D1: K0 primero, PR aparte)
> **Spec id**: SPEC-203
> **Mirror**: 1 fila SUMMARY + N filas SECTION en dark-memory (proyecto `dark-memory-mcp`)
> **Base**: `upstream/main` @ b6427c4 (las lineas citadas son las de esa base)

## 1. Contexto

El Store tiene **un solo** `project_id` activo: un string protegido por un mutex
(`internal/store/sqlite/store.go:375`). Lo escribe `SetActiveProject` (:174) y lo
consume todo lo demas: cada escritura lo etiqueta (`projectIDOrActive`, :212) y
cada lectura filtra por el (`ListAgentMemory` :4764, `searchByBM25` :5134).

`session_start` pone ese slot (`internal/orchestration/session_start.go:141`). El
servidor MCP de stdio **comparte ese proceso entre varios clientes**, cada uno
con su proyecto, y sus `session_start` se entrelazan: medido en la maquina del
operador, tres conexiones al mismo proceso servidor, con llamadas separadas por
menos de diez segundos. Cada una mueve el slot, y a partir de ahi:

- las escrituras de `agent_memory` caen en el proyecto del slot, no en el de la
  sesion que las pidio;
- las lecturas devuelven el proyecto del slot;
- `GetSession` no encuentra sesiones de otros proyectos, porque tambien filtra por
  el slot (:705).

**Medido**: 8 filas que debian vivir en otro proyecto aparecieron en el
proyecto del cliente vecino (memoria #726 del proyecto `dark-memory-mcp`, el
mismo que pidio las 8 filas). No es un caso raro: es la situacion normal de
quien abre una sesion por proyecto.

Hay un segundo defecto en el mismo sitio: las escrituras de `agent_memory` no
llevan `project_id` ni `session_id` en su `WriteContext`
(`internal/orchestration/agent_memory.go:115`), de modo que la fila de
`write_audit` sale con `session_id` NULL. **El rastro no dice quien escribio**,
ni en que proyecto; solo dice quien pidio la llamada.

## 2. Decision

El proyecto de una escritura o de una lectura se resuelve de **la sesion que la
peticion nombra**. El slot global deja de ser la fuente y pasa a ser el respaldo
(comportamiento actual, para los llamadores que no nombran sesion).

Cadena de resolucion, una sola y en el store:

1. `WriteContext.ProjectID` explicito → **gana** (lo usan `session_start` y los
   flujos administrativos).
2. `WriteContext.SessionID` → `sessions.project_id` de esa sesion, buscada por
   `session_id` **sin filtrar por el slot**.
3. El slot global → respaldo (comportamiento actual).

Lo mismo para las lecturas: la llamada puede nombrar `session_id` y el proyecto
se deriva de el.

Y una regla que no se negocia: un `project_id` explicito que **no** sea el de tu
sesion es un `ErrCrossProjectAccess`, nunca un "traeme las filas del otro
proyecto". **INV-7 no se relaja: se deja de depender de un estado mutable.**

## 3. La resolucion en el store

Tres piezas, todas en `internal/store`:

1. **`ProjectIDForSession(ctx, sessionID) (string, error)`** — nueva metodo de la
   interfaz `store.Store`: `SELECT project_id FROM sessions WHERE session_id = ?`,
   **sin** el filtro `project_id = activeProject` que usa `GetSession` (:705).
   Devuelve `""` si la sesion no existe. Se implementa en sqlite y en postgres.
2. **`resolveWriteProject(ctx, wc)`** — la cadena de §2 en un solo sitio. Sustituye
   a `projectIDOrActive` en los quince puntos de escritura del store. La variante
   que se llama con el mutex ya tomado lee `s.activeProject` sin volver a tomar
   el lock (el mismo cuidado que hay hoy en el codigo).
3. **La auditoria se corrige sola**: `recordWriteLocked`/`recordWriteLockedTx`
   reciben el `project_id` ya resuelto, y `write_audit.session_id` pasa a llevar la
   sesion cuando la llamada la nombra. Hoy esa columna sale NULL en todas las
   escrituras de `agent_memory`.

**Nota de alcance**: la resolucion se aplica a **todas** las escrituras del store,
no solo a las de `agent_memory`. Un proyecto no puede tener dos reglas: si la
memoria del agente se resuelve por sesion y el resto no, el fallo se esconde en
la parte que nadie prueba. Los testes existentes son el mapa de esa dependencia:
si alguno depende del slot, se rompe, y ahi se pasa el `project_id` explicito.

## 4. El contrato de las tools

Las tools de `agent_memory` no aceptan hoy ni `session_id` ni `project_id` (el
proyecto "lo da la sesion activa"). Se anaden **opcionales**, sin romper nada:

| Tool | Parametro nuevo | Que hace |
|---|---|---|
| `agent_memory_save` | `session_id` | scope del proyecto de la escritura + atribucion en `write_audit` |
| `agent_memory_update` | `session_id` | lo mismo |
| `agent_memory_archive` | `session_id` | lo mismo |
| `agent_memory_list` | `session_id`, `project_id` | la lectura se scopea a la sesion; `project_id` es una **asercion** |
| `agent_memory_recall` | `session_id`, `project_id` | lo mismo |
| `agent_memory_get` | `session_id`, `project_id` | lo mismo |

El nombre `session_id` no es arbitrario: el gate **ya** lo lee de los args
(`internal/server/middleware.go:257`) para validar el ambito. Con el parametro, el
gate valida contra la sesion correcta en vez de contra la del slot.

`bind_session` sigue significando lo que significaba: ligar la **fila** al ciclo de
vida de la sesion. El `session_id` nuevo liga la **escritura** (su proyecto y su
fila de auditoria). Son dos cosas distintas y no se confunden.

## 5. Las lecturas con proyecto explicito

`agentmemory.AgentMemoryListFilters` y `agentmemory.SearchFilters` ganan un campo
`ProjectID`. Cuando viene informado:

- si coincide con el proyecto resuelto de la sesion → la lectura se scopea ahi;
- si **no** coincide → `ErrCrossProjectAccess` (codigo de error existente), no un
  conjunto vacio: el vacio es indistinguible de "no hay memoria", y esa
  ambiguedad ya se corrigio una vez en `GetAgentMemory` por el mismo motivo.

El store sigue exigiendo `requireProject()`: sin proyecto resuelto, la lectura se
niega.

## 6. El test de dos clientes concurrentes

El test vive en `tests/project/` (la suite de aislamiento de INV-7) y es **el**
criterio de aceptacion:

1. Se abre el store y se crean los proyectos A y B.
2. `session_start(A)` → sesion `S_A`; `session_start(B)` → sesion `S_B`. El slot
   queda en B: ese es el estado que rompe todo.
3. Una escritura que nombra `S_A` **cae en A**, no en B.
4. Una escritura que no nombra sesion cae en B (el respaldo, comportamiento
   actual, documentado).
5. `agent_memory_list` con la sesion de A devuelve **solo** filas de A.
6. `agent_memory_list` con `project_id` de B y sesion de A → `ErrCrossProjectAccess`.

Antes del arreglo, el punto 3 falla. Ese es el test rojo que se escribe primero.

## 7. Compatibilidad

- **Sin migracion de esquema**: la tabla `sessions` ya tiene `project_id`. El
  schema sigue en v32.
- **Sin ruptura de API**: los parametros nuevos son opcionales. Un llamador que
  no nombre sesion sigue cayendo en el slot, igual que hoy.
- **`write_audit` mejora**: las filas de `agent_memory` pasan a llevar
  `session_id` cuando la llamada nombra sesion. Es mas informacion, no menos.
- **El slot sigue existiendo**: `dark-mem-inspect` lo sigue enseñando y
  `session_start` lo sigue poniendo. Se degrada a "la sesion activa por defecto",
  no se borra.
- **El gate no cambia de comportamiento**: `DARK_PLAN_STRICTNESS` y el resto de
  las razones del gate son de otra spec (W1). Aqui solo se le da al gate la
  sesion correcta cuando la llamada la nombra.

## 8. Acceptance criteria

- [ ] `go test ./tests/project/...` en verde, con el test de dos clientes
      concurrentes (6 puntos del §6).
- [ ] `go test ./tests/invariants/...` en verde (INV-7 no se relaja).
- [ ] `go build ./...` y `go vet` limpios sobre los paquetes tocados.
- [ ] Una escritura con `session_id` aparece en `write_audit` con ese
      `session_id` y con el `project_id` de la sesion.
- [ ] Veredicto del juez sobre la revision arquitectonica de este PR:
      `aligned` con confianza >= 0.85, y el `prov=` del razonamiento dice que
      hablo la rubrica (no la cadena NLI).
- [ ] El cambio esta en un commit, y el PR lleva el trailer del juez.

## 9. Open questions

1. Cuando las tools de `agent_memory` acepten `session_id`, ¿deberia el resto de
   las 78 tools aceptarlo tambien, o basta con la familia de memoria? La
   respuesta decide el alcance de W1 (el gate del plan necesita un `session_id`
   fiable en todas las tools mutantes).
2. ¿Debe el slot global pasar a ser "la sesion del ultimo `session_start`" en
   lugar de "el ultimo `project_id` pedido"? Es el mismo problema con otro nombre,
   pero afecta a lo que ve `dark-mem-inspect`.
