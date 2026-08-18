# Glosario — dark-memory-mcp

> **TL;DR**: Definiciones en español de la jerga técnica que aparece
> en `dark-memory-mcp`. Cada entrada tiene: término en español,
> equivalente en inglés (entre paréntesis), definición operativa,
> ejemplo, y referencia cruzada. Úsalo como diccionario inverso
> cuando leas código o documentación.

---

## 0. Cómo leer este glosario

Cada entrada tiene cinco campos:

- **Término** — la palabra en español.
- **Inglés** — el original en inglés (la mayoría de la jerga técnica
  viene del inglés).
- **Definición** — qué significa **en este proyecto**. No es la
  definición de diccionario, es la operativa.
- **Ejemplo** — un caso concreto del proyecto.
- **Véase** — enlaces cruzados a docs relacionados.

Los términos están en orden alfabético en español.

---

## A

### Agente (agent)

**Inglés**: agent.

**Definición**: Programa interactúa con el modelo de lenguaje (LLM) y
con herramientas externas. En este proyecto, el "agente" es el
cliente MCP (Claude Desktop, opencode, Cursor) que llama a las 57
herramientas de `dark-memory-mcp`.

**Ejemplo**: "El agente quiere guardar una observación. Llama a
`agent_memory_save` con `kind=observation`".

**Véase**: [MANIFIESTO.md §2.2](./MANIFIESTO.md#22-vocabulario-técnico),
[README.md](../README.md).

### Agente de IA (AI agent)

**Inglés**: AI agent.

**Definición**: Software que toma decisiones en representación de un
usuario, típicamente componiendo un LLM con herramientas externas. La
diferencia con un programa tradicional es que el LLM decide qué
herramientas llamar y en qué orden.

**Ejemplo**: "Un agente de IA que reserva vuelos: el LLM decide
cuándo llamar a la API de búsqueda, cuándo leer el resultado, cuándo
llamar a la de reserva".

**Véase**: [MANIFIESTO.md §1](./MANIFIESTO.md#1-tres-audiencias-un-documento).

### Anti-patrón (antipattern)

**Inglés**: anti-pattern.

**Definición**: Patrón de diseño que parece una solución pero produce
más problemas de los que resuelve. El proyecto documenta 14
anti-patrones de documentación (de dark-testing) y 9 antipatrones de
LLM-engineering (de dark-memory).

**Ejemplo**: "El **deliberate-break test** es la cura para el
anti-patrón de 'test que pasa pero no prueba nada'".

**Véase**: [MANIFIESTO.md §3](./MANIFIESTO.md#3-anti-patrones-de-documentación-de-dark-testing),
archivo row 859 en agent_memory (pinned).

### API (Application Programming Interface)

**Inglés**: API.

**Definición**: Contrato de programación entre dos sistemas. En este
proyecto, la API principal es la superficie MCP (57 herramientas
expuestas como JSON-RPC sobre stdio).

**Ejemplo**: "`dark_memory_save_agent_memory` es una API de escritura
a `agent_memory` con auditoría obligatoria".

**Véase**: [README.md](../README.md#las-57-herramientas).

### Arbiter (ver `drift_judge`)

### ARN (no aplica)

No usamos ARN en este proyecto. Si ves ARN, es de otra cosa.

### Atomicidad (atomicity)

**Inglés**: atomicity.

**Definición**: Propiedad de una operación: o se ejecuta completa, o
no se ejecuta. En el contexto de base de datos, una transacción
atómica no deja estado intermedio.

**Ejemplo**: "Si guardas un agente con auditoría, la fila en
`agent_memory` y la fila en `write_audit` se insertan en la misma
transacción. Si una falla, las dos se deshacen".

**Véase**: [INVARIANTS.md §INV-1](./INVARIANTS.md#inv-1--auditoría-en-el-camino-de-escritura).

### Auditoría (audit)

**Inglés**: audit.

**Definición**: Registro permanente de quién hizo qué, cuándo, y bajo
qué reglas. En este proyecto, la auditoría es la base de la
reconstrucción forense.

**Ejemplo**: "La tabla `write_audit` tiene una fila por cada
`Save*` exitoso, con campos para actor, sesión, proyecto, y
constitución en vigor".

**Véase**: [INVARIANTS.md](./INVARIANTS.md), [RUNBOOK.md](./RUNBOOK.md).

---

## B

### Baseline (línea base)

**Inglés**: baseline.

**Definición**: Estado de referencia contra el cual se compara el
estado actual. En testing, es el conjunto de tests que se ejecutan
sin cambios para detectar regresiones.

**Ejemplo**: "El baseline de tests pasa en 42s. Si el nuevo commit
los hace pasar en 60s, hay regresión de performance".

### Bridge (puente)

**Inglés**: bridge.

**Definición**: En este proyecto, es el binario
`dark-mem-mcp-bridge` (3,9 MB) que se lanza por cada sesión de
opencode. Su único trabajo es recibir JSON-RPC por stdio y
redirigirlo al daemon por socket.

**Ejemplo**: "El bridge pesa 3,9 MB porque solo tiene el código de
forwarding. El daemon tiene los 30 MB".

**Véase**: [ARCHITECTURE.md](./ARCHITECTURE.md).

### Build (compilación)

**Inglés**: build.

**Definición**: Proceso de convertir código fuente en un binario
ejecutable. En este proyecto, `go build` produce los binarios
`dark-mem-mcp`, `dark-mem-mcp-bridge`, y `dark-mem-mcp-daemon`.

**Ejemplo**: "`go build -o dark-mem-mcp.exe ./cmd/dark-mem-mcp/`
construye el dispatcher".

---

## C

### Caso de uso (use case)

**Inglés**: use case.

**Definición**: Escenario concreto de uso del sistema. Un buen
documento de casos de uso tiene nombre, actor, precondición, y
resultado observable.

**Ejemplo**: "Caso de uso: estudiante guarda una observación sobre
un bug. Resultado: la observación aparece en `agent_memory` y puede
ser recuperada con `recall`".

### Cliente MCP (MCP client)

**Inglés**: MCP client.

**Definición**: Programa que invoca herramientas expuestas por un
servidor MCP. En este proyecto, los clientes son Claude Desktop,
opencode, Cursor, MCPJam, etc.

**Ejemplo**: "Claude Desktop es un cliente MCP que se conecta al
servidor `dark-mem-mcp` por stdio".

### Coexistencia (coexistence)

**Inglés**: coexistence.

**Definición**: Propiedad de varios sistemas que comparten un mismo
recurso (DB, socket, archivo) sin destruirse. En este proyecto,
`dark-memory-mcp` y `dark-research-mcp` coexisten en el mismo
archivo SQLite.

**Ejemplo**: "Dark-memory escribe la tabla `agent_memory`;
dark-research escribe la tabla `research_items`. No chocan".

**Véase**: [COEXISTENCE.md](./COEXISTENCE.md).

### Constitución (constitution)

**Inglés**: constitution.

**Definición**: Conjunto inmutable de reglas que gobierna el
comportamiento del sistema. En este proyecto, la constitución
define invariantes, validadores, y mods activos.

**Ejemplo**: "La constitución del proyecto tiene 8 invariantes
operativos. Romper cualquiera es un cambio de major version".

**Véase**: [INVARIANTS.md](./INVARIANTS.md), [CONSTITUTION.md](../CONSTITUTION.md).

### Commit (confirmación)

**Inglés**: commit.

**Definición**: Unidad de cambio en un repositorio git. Cada commit
tiene un SHA, autor, fecha, mensaje, y un diff.

**Ejemplo**: "El commit `c3456d5` añadió el tool `judge_list_personas`
al registry canónico".

### Constitución

Ver **Constitución**.

---

## D

### Daemon (demonio)

**Inglés**: daemon.

**Definición**: Proceso de larga duración que se ejecuta en segundo
plano. Convención POSIX: nombre terminado en `d`. En este proyecto,
`dark-mem-mcp-daemon` es el daemon principal.

**Ejemplo**: "El daemon sobrevive a los reinicios de opencode porque
no está atado a stdin de la sesión".

**Véase**: [ARCHITECTURE.md](./ARCHITECTURE.md).

### Dispatcher (despachador)

**Inglés**: dispatcher.

**Definición**: Componente que decide a qué handler enviar una
petición. En este proyecto, `dark-mem-mcp` es el dispatcher: decide
si arranca el bridge o el modo legacy.

**Ejemplo**: "El dispatcher mira `DARK_MEM_BRIDGE=0` y arranca el
modo legacy".

**Véase**: [ARCHITECTURE.md](./ARCHITECTURE.md).

### Drift (deriva)

**Inglés**: drift.

**Definición**: En este proyecto, diverge semántica entre lo que el
código hace y lo que el spec/artifacts dice que hace. El
`drift_judge` la detecta.

**Ejemplo**: "Spec dice que la herramienta retorna 3 campos; el
código retorna 4. Hay drift: el código está adelante".

**Véase**: [README.md](../README.md#el-vibe-loop-un-paradigma-nuevo).

### Drift judge (juez de drift)

**Inglés**: drift judge.

**Definición**: LLM-as-judge que evalúa si un artifact cumple lo que
el spec promete. Devuelve `aligned`, `drift_detected`, o
`needs_human`.

**Ejemplo**: "`drift_judge` evalúa el artifact y devuelve `aligned`
con confianza 0,97".

**Véase**: [judge-personas.md](./judge-personas.md).

---

## E

### Endpoint (punto de acceso)

**Inglés**: endpoint.

**Definición**: URL o método expuesto por un servicio. En este
proyecto, los "endpoints" son las 57 herramientas MCP.

**Ejemplo**: "El endpoint `agent_memory_save` está documentado en
[README.md §Las 57 herramientas](../README.md#las-57-herramientas)".

### Estado (state)

**Inglés**: state.

**Definición**: Conjunto de valores que un sistema recuerda entre
operaciones. En este proyecto, el estado se divide en:

- **Estado de sesión**: vive en DB, sobrevive a procesos.
- **Estado de proceso**: vive en memoria, muere con el proceso.
- **Estado de constitución**: vive en DB, cambia solo entre
  versiones.

**Ejemplo**: "Las sesiones activas están en la tabla `sessions`; el
caché de constitución está en memoria del daemon".

---

## F

### FSM (Finite State Machine — máquina de estados finitos)

**Inglés**: FSM.

**Definición**: Modelo computacional con un número finito de estados
y transiciones entre ellos. En este proyecto, el daemon tiene una
FSM con 4 estados: `not_running`, `starting`, `ready`,
`shutting_down`.

**Ejemplo**: "El daemon pasa de `starting` a `ready` cuando termina
de aceptar conexiones".

**Véase**: [RUNBOOK.md §Daemon lifecycle](./RUNBOOK.md).

---

## G

### Goroutine (hilo ligero)

**Inglés**: goroutine.

**Definición**: Unidad de concurrencia en Go. Más liviana que un
thread del sistema operativo. En este proyecto, el daemon
arranca una goroutine por conexión aceptada.

**Ejemplo**: "El accept loop del daemon despacha cada conexión a su
propia goroutine".

### Guard (verificador)

**Inglés**: guard.

**Definición**: Función que valida una condición antes de proceder.
En este proyecto, los guards verifican invariantes antes de
escribir.

**Ejemplo**: "`guardProjectAccess` rechaza escrituras que no
pertenecen al proyecto activo".

---

## H

### Handler (manejador)

**Inglés**: handler.

**Definición**: Función que procesa una petición específica. En este
proyecto, cada una de las 57 herramientas tiene un handler.

**Ejemplo**: "El handler de `agent_memory_save` invalida la caché
de recall antes de retornar".

### Handshake (apretón de manos)

**Inglés**: handshake.

**Definición**: Protocolo de inicio entre dos partes. En este
proyecto, el handshake MCP incluye `initialize` y
`notifications/initialized`.

**Ejemplo**: "El cliente MCP envía `initialize` con su versión y
capacidades; el servidor responde con la suya".

---

## I

### Invariante (invariant)

**Inglés**: invariant.

**Definición**: Propiedad que el sistema garantiza en todo momento.
En este proyecto, los 8 invariantes operativos son enforced at the
`Store` interface boundary, no como documentación.

**Ejemplo**: "INV-1: cada `Save*` inserta una fila en `write_audit`
en la misma transacción".

**Véase**: [INVARIANTS.md](./INVARIANTS.md).

### Integración (integration)

**Inglés**: integration.

**Definición**: Tipo de test que verifica la cooperación entre
componentes. En este proyecto, los tests de integración viven en
`tests/integration/`.

**Ejemplo**: "El test de integración `agent_memory_save_and_recall`
verifica que una fila guardada es recuperable".

---

## J

### JSON-RPC (JSON Remote Procedure Call)

**Inglés**: JSON-RPC.

**Definición**: Protocolo de RPC codificado en JSON. Es el protocolo
que usa MCP. En este proyecto, todo el wire protocol es JSON-RPC
2.0 sobre stdio.

**Ejemplo**: "`{\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"id\":1}`".

**Véase**: [MANIFIESTO.md §2.3](./MANIFIESTO.md#23-estructura-markdown).

### Judge (juez)

**Inglés**: judge.

**Definición**: LLM externo (no el agente principal) que evalúa
afirmaciones. En este proyecto, el judge corre la eval_type
`drift_judge` para evaluar artifacts.

**Ejemplo**: "El judge devolvió `aligned` con confianza 0,97".

---

## L

### LLM (Large Language Model)

**Inglés**: LLM.

**Definición**: Modelo de lenguaje de gran tamaño. En este proyecto,
el LLM principal es el que ejecuta el agente. El judge es un
segundo LLM, distinto, para evitar sesgo.

**Ejemplo**: "El agente principal es MiniMax-M3; el judge es
DeepSeek-v4".

### Layer (capa)

**Inglés**: layer.

**Definición**: Nivel de abstracción en la arquitectura. En este
proyecto, las 4 capas son opencode, bridge, daemon, DB.

**Ejemplo**: "El bridge es la capa 2; traduce entre stdio y el
socket del daemon".

---

## M

### MCP (Model Context Protocol)

**Inglés**: MCP.

**Definición**: Protocolo abierto que estandariza cómo los modelos
de IA invocan herramientas externas. Es el estándar de la
industria para dar a un LLM acceso a tools.

**Ejemplo**: "Claude Desktop es un cliente MCP; `dark-mem-mcp` es
un servidor MCP".

**Véase**: [README.md](../README.md).

### Mindset (mentalidad)

**Inglés**: mindset.

**Definición**: System prompt compuesto para un sub-agente específico.
En este proyecto, "mindset" es el término para el rol+goal+backstory
que se inyecta a un sub-agente.

**Ejemplo**: "El mindset `growth-coordinator` se compone con
`mindset_apply(vibe_case=C7, task_description=...)`".

**Véase**: [mindsets.md](./mindsets.md).

### Mock (simulacro)

**Inglés**: mock.

**Definición**: En testing, objeto que simula el comportamiento de
otro. En este proyecto, los mocks se usan en tests unitarios para
aislar la unidad bajo test.

**Ejemplo**: "`mockSessionResolver` devuelve una sesión fija
independiente del tiempo".

### Mutation (mutación)

**Inglés**: mutation.

**Definición**: En testing, modificación deliberada del código para
verificar que el test suite detecta el cambio. En este proyecto,
los mutation tests se corren en CI.

**Ejemplo**: "Cambiar `>=` por `>` en `isAdult` es una mutación
común; el test debe detectarla".

---

## N

### N-shot (N intentos)

**Inglés**: N-shot.

**Definición**: En LLM-as-judge, ejecutar el judge N veces y tomar
la moda de las respuestas. En este proyecto, `consensus(n=3)` es
una decisión de 3-shot.

**Ejemplo**: "`consensus(n=5)` para claims de alta stake".

---

## O

### OSINT (Open Source Intelligence)

**Inglés**: OSINT.

**Definición**: Inteligencia derivada de fuentes abiertas (públicas).
En este proyecto, dark-research es el módulo que OSINT.

**Ejemplo**: "CVE-2024-1234 verificación tier-1 via
`dark_research_cve`".

---

## P

### Property test (test de propiedad)

**Inglés**: property-based test.

**Definición**: Test que verifica una propiedad universal sobre un
espacio de inputs, no un ejemplo específico. En este proyecto,
los property tests viven en `internal/daemon/protocol_test.go`.

**Ejemplo**: "Para todo input válido, `MarshalFrame` debe poder
recuperarse vía `UnmarshalFrame`".

**Véase**: [dark-testing](../.config/opencode/skills/dark-testing/SKILL.md).

### Prompt injection (inyección de prompt)

**Inglés**: prompt injection.

**Definición**: Ataque donde un usuario malicioso embebe instrucciones
en contenido que el LLM luego lee. En este proyecto, la capa
`snapshot_markdown` tiene un eval_type `prompt_injection_scan`
para detectar esto.

**Ejemplo**: "El usuario publica un post con 'ignora las
instrucciones anteriores' embebido. `prompt_injection_scan` lo
detecta".

---

## R

### Race (condición de carrera)

**Inglés**: race condition.

**Definición**: Bug donde el resultado depende del orden no
determinista de operaciones concurrentes. En este proyecto, los
race conditions se evitan con `sync.Mutex` y channels.

**Ejemplo**: "Si dos goroutines escriben a la misma fila
simultáneamente sin lock, una puede perder sus cambios".

### Ready file (archivo de listo)

**Inglés**: ready file.

**Definición**: Archivo temporal que un proceso crea cuando está
listo para aceptar conexiones. Patrón dark-copilot G14. En este
proyecto, el daemon crea `~/.dark-agents/daemon.ready` cuando
termina de inicializar.

**Ejemplo**: "El bridge testa el ready file antes de conectar".

### Recursive (recursivo)

**Inglés**: recursive.

**Definición**: Función que se llama a sí misma. En este proyecto,
los parsers de Mermaid son recursivos.

**Ejemplo**: "La función `parseNode` es recursiva para nodos
anidados".

### Registry (registro)

**Inglés**: registry.

**Definición**: Tabla o estructura que mantiene un mapeo
ordenado. En este proyecto, `CanonicalOrder()` define el orden
de las 57 herramientas en el wire.

**Ejemplo**: "`dark_mem_canonical_order()` retorna la lista de 57
herramientas en el orden canónico".

**Véase**: [ARCHITECTURE.md §Registry canónico](./ARCHITECTURE.md).

---

## S

### Scope (alcance)

**Inglés**: scope.

**Definición**: En este proyecto, partición del espacio de memoria.
Hay tres scopes: `agent` (subagente aislado), `project` (dentro
del proyecto activo), `operator` (todos los proyectos).

**Ejemplo**: "`agent_memory_list(scope=project)` retorna solo las
filas del proyecto activo".

**Véase**: [README.md](../README.md#conceptos-clave).

### Schema (esquema)

**Inglés**: schema.

**Definición**: Definición de la estructura de datos. En este
proyecto, el schema v26 define 18 tablas en SQLite.

**Ejemplo**: "El schema de `agent_memory` incluye id, kind,
content, tags, pinned, created_at".

### Session (sesión)

**Inglés**: session.

**Definición**: Unidad de trabajo que agrupa recuerdos,
especificaciones, y artefactos. En este proyecto, una sesión
nace con `session_start` y muere con `session_close`.

**Ejemplo**: "La sesión `sess-97960312a948803c` está trabajando
en la documentación en español".

### Spec (especificación)

**Inglés**: specification.

**Definición**: Documento que define qué se va a hacer. En este
proyecto, un spec nace con `vibe_spec` y tiene un vibe_case (C1-C7)
y un conjunto de tasks.

**Ejemplo**: "El spec 1176 es la especificación del wrapper
daemon-bridge".

### Stdio (entrada/salida estándar)

**Inglés**: stdio.

**Definición**: Standard input/output. En este proyecto, el
transport MCP es stdio: el cliente y el servidor se comunican
por los file descriptors 0, 1, y 2.

**Ejemplo**: "El bridge lee frames de su stdin y los escribe al
socket del daemon".

**Véase**: [RUNBOOK.md §Transporte](./RUNBOOK.md#transporte).

### Steering (orientación)

**Inglés**: steering.

**Definición**: Modificar el comportamiento del LLM en tiempo real.
En este proyecto, no hay steering explícito; los mindsets son
"static steering" via system prompt.

**Ejemplo**: "Steering el mindset de un sub-agente cambia su
system prompt".

---

## T

### Tier (nivel de fuente)

**Inglés**: tier.

**Definición**: En investigación, nivel de confianza de una fuente.
Tier 1: vendor blog, NVD, OSV. Tier 3: news, blogs generales.

**Ejemplo**: "Atribución de CVE requiere fuente tier-1".

**Véase**: [dark-research skill](../.config/opencode/skills/dark-research/SKILL.md).

### Todo (pendiente)

**Inglés**: todo.

**Definición**: Acción concreta a realizar. En este proyecto, los
todos tienen un `content`, un `status`, y un `priority`.

**Ejemplo**: "`todowrite` mantiene la lista de pendientes en la
sesión actual".

### Tool (herramienta)

**Inglés**: tool.

**Definición**: Función invocable por el LLM. En este proyecto, las
tools son las 57 funciones MCP.

**Ejemplo**: "`agent_memory_save` es una tool".

### Tracing (rastreo)

**Inglés**: tracing.

**Definición**: Observabilidad de una petición a través de varios
componentes. En este proyecto, el trace ID se mantiene en el
`WriteContext`.

**Ejemplo**: "El trace ID `tr-abc123` aparece en los logs de
múltiples servicios".

---

## U

### Unit test (test unitario)

**Inglés**: unit test.

**Definición**: Test de una sola unidad de código. En este proyecto,
viven junto al código (`package_test.go`).

**Ejemplo**: "`TestSaveAgentMemory` es un unit test del handler
correspondiente".

---

## V

### Vibecase (caso de vibra)

**Inglés**: vibe_case.

**Definición**: Categoría de trabajo en el vibe-loop. C1=código,
C2=texto, C3=imagen, C4=video, C5=audio, C6=multimodal, C7=mixto.

**Ejemplo**: "La documentación es vibe_case C2 (texto)".

**Véase**: [README.md](../README.md#el-vibe-loop-un-paradigma-nuevo).

### VLP (Vibe-Loop Engine)

**Inglés**: VLP.

**Definición**: Máquina de estados que governa el ciclo de trabajo.
Los eventos: `session_start`, `vibe_publish`, `artifact_log`,
`drift_log`, `complete`.

**Ejemplo**: "El VLP pasa de `spec_active` a `delegating` cuando
se delega".

**Véase**: [README.md](../README.md#el-vibe-loop-un-paradigma-nuevo).

---

## W

### Wire (alambre)

**Inglés**: wire.

**Definición**: En este proyecto, el protocolo JSON-RPC que viaja
sobre stdio. "Wire conformance" significa que los tests verifican
el comportamiento observable en la red.

**Ejemplo**: "Los test wire conformance lanzan el binario contra
su wire y miden respuestas".

### Wrapper (envoltura)

**Inglés**: wrapper.

**Definición**: Componente que envuelve a otro para añadirle
funcionalidad. En este proyecto, el bridge es el wrapper del
daemon.

**Ejemplo**: "El `dark-mem-mcp` es un wrapper del binario original,
añadiendo la lógica de dispatcher".

**Véase**: [ARCHITECTURE.md](./ARCHITECTURE.md).

---

## Notas finales

Si encuentras un término que debería estar aquí, agrégalo via PR.
El formato de cada entrada puede copiarse de la primera que
encuentres.

Para la versión en inglés de esta misma documentación, ver
`docs/GLOSSARY.en.md` (futuro).
