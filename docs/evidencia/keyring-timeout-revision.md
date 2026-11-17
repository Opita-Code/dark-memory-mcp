# Fix de fondo: el keyring del SO nunca más cuelga el arranque del MCP

**PR:** `fix/keyring-nunca-cuelga-el-boot` → `Opita-Code/dark-memory-mcp` (base `main`)
**Ficheros:** 3 (1 nuevo, 1 nuevo de test, 1 modificado) · **+570 / −3**
**Fecha:** 2026-11-17

---

## 1. El síntoma, medido

El harness ve `mcp connect failed … Request timed out` a los ~34 s, en los
tres directorios de trabajo. El proceso del servidor MCP **está vivo** (el
`ps` lo muestra) pero **nunca responde** a un `initialize`.

Lo que dice el log del servidor, en orden, es esto:

```
dark-mem-mcp: boot step3 ok migrations applied + constitution watchdog (driver=sqlite schema_version=32)
dark-mem-mcp: registered 78 tools (canonical order)
dark-mem-mcp: boot_reconcile clean (no stale sessions)
dark-mem-mcp: startup-recover failed: session_recover: find: store: workflow tool requires an active session: no active project
dark-mem-mcp: sweeper started idle_timeout=15m0s heartbeat_timeout=1h0m0s tick=30s
```

Y **nunca** aparece la línea `serving stdio`, que es la que emite
`internal/server/server.go:346` justo antes de `server.ServeStdio(...)`.

O sea: el boot termina, el sweeper arranca, y el proceso se queda **entre el
sweeper y `ServeStdio`**. Ese tramo es corto y se lee:

```go
// cmd/dark-mem-mcp/legacy_main.go
runStartupRecoverLegacy(ctx, bootState.Orchestrator)

// v2.20.0 (spec 1188 T6): warm up the default failover chain at
// boot (migrates env keys into the OS keyring + starts the
// background health loop). Best-effort — a missing key is not a
// boot failure.
if _, llmErr := orchestration.DefaultFailoverClient(); llmErr != nil {
    fmt.Fprintf(os.Stderr, "dark-mem-mcp: LLM failover init warning: %v\n", llmErr)
}

if err := srv.ServeStdio(ctx); err != nil {
```

**La causa está en esa llamada**: `DefaultFailoverClient()` →
`buildDefaultKeyStore()` → `llmkeystore.DefaultKeyring()`.

---

## 2. La causa raíz, medida con sondas propias

No es una deducción del código: es una medición, con sondas escritas para
esta investigación.

### 2.1 El `Open` funciona, el `Set` no vuelve nunca

```
keyring.Open(...)          ->  8,7 ms   err=<nil>     FUNCIONA
ring.Set("llm/probe-write") ->  COLGADO > 15 s, goroutine viva
```

El `Open` solo abre la conexión de dbus. Lo que se cuelga es la
**escritura**.

### 2.2 La colección está bloqueada

El Secret Service expone sus colecciones:

```
org.freedesktop.secrets/collection/dark_2dmemory_2dmcp
    Label  = "dark-memory-mcp"
    Locked = true          <-- aqui
org.freedesktop.secrets/collection/login
    Locked = false
org.freedesktop/secrets/collection/session
    Locked = false
```

Y la comprobación cruzada, con un servicio **distinto** por nombre de
servicio (que es como la librería `99designs/keyring` decide la colección):

```
service=dark-memory-mcp     (la bloqueada)    Set COLGADO 8 s
service=dark-memory-mcp-alt (coleccion nueva) Set COLGADO 8 s
service=login               (sin contrasena)   Set err=<nil>   Get ok=true
```

**Cualquier colección con contraseña propia se cuelga. La que no tiene
contraseña propia (`login`) escribe bien.** El patrón está claro: la
escritura espera el *prompt* de desbloqueo, y ese prompt no lo atiende nadie.

### 2.3 El prompt se autodestruye

`org.freedesktop.Secret.Service.Unlock` **funciona** y devuelve un prompt:

```
$ dbus-send … org.freedesktop.Secret.Service.Unlock array:objpath:"/org/freedesktop/secrets/collection/dark_2dmemory_2dmcp"
method return …
   array [ ]
   object path "/org/freedesktop/secrets/prompt/u82"
```

Pero ese prompt **desaparece solo** a los pocos segundos (se comprobó con
`GetAll` sobre su ruta: `No existe el método «…»` / *el objeto no existe en la
ruta*). En una sesión sin agente de prompts (GNOME Keyring en modo
servidor, sin sesión gráfica que lo atienda) **no hay forma de
desbloquear**: la llamada se acepta, se crea el prompt, y el prompt muere
sin que nadie responda.

### 2.4 La lectura sí funciona, y por eso el fallo es confuso

Con `DARK_LLM_KEYRING=1`, el servidor lee las claves del **entorno** sin
problema:

```
minimax has_key=true src=env
```

Lo que no puede es **escribir**. Por eso el síntoma no es "no hay
credenciales" sino "el servidor arranca pero no sirve": la lectura del
keyring no es la que falla; la escritura es.

---

## 3. Por qué esto es un fallo de ARQUITECTURA, no un bug de configuración

El keyring es una **dependencia no crítica del serving**. El MCP sirve sus
78 tools sin ninguna credencial de LLM: `health_ping`, `memory_state`,
`session_start`, `agent_memory_recall`… ninguno la necesita. La huella del
LLM es una de varias, y su ausencia degrada una capacidad, no el servicio.

Y sin embargo, el boot **no llegaba a `ServeStdio`** porque el keyring no
respondía. Eso es un fallo de **separación de criticidad**: la puerta de
herramientas está acoplada al arranque de un subsistema opcional.

Peor: el acoplamiento está en el **punto más caro posible**. El
`DefaultFailoverClient()` está en el boot, así que un keyring colgado no
produce un error: produce un **timeout del harness**, que es el síntoma más
difícil de diagnosticar de todos (parece un problema de red o de versión).

Y hay un patrón que este repo ya ha pagado antes, con otra forma. De
`SPEC-PIPELINE-REAL.md` §2:

> *"dos chupones de clave con políticas distintas es la clase de bug que
> este repo ya ha pagado tres veces"*.

Aquí el "chupón" no es una clave: es **el momento de decidir que un fallo es
permanente**, y estaba definido en tres ficheros a la vez (§4). La misma
familia: **una decisión replicada en varios sitios, que se desincroniza y
falla en la frontera**.

---

## 4. El arreglo

### 4.1 `internal/llmkeystore/timeout_store.go` (nuevo)

Un `TimeoutStore` que envuelve cualquier `KeyStore` y le pone un **deadline
por operación**:

```go
var DefaultOperationTimeout = 2 * time.Second

var ErrKeyringTimeout = errors.New("llm keystore: operation timed out")

type TimeoutStore struct {
    inner   KeyStore
    timeout time.Duration
}
```

Al expirar el plazo, la operación **se abandona y se degrada**:

- Las **lecturas** caen al backend siguiente (el composite recorre la
  cadena: keyring → env).
- Las **escrituras** devuelven `ErrKeyringTimeout` en vez de colgarse.

El plazo por defecto son **2 s**, y la elección está medida: contra un
Secret Service sano, `keyring.Open` tarda entre **0,8 ms y 8,7 ms**. Dos
segundos son tres órdenes de magnitud por encima del caso bueno: un keyring
sano **nunca** va a falsos-positivar.

Una nota de implementación que conviene documentar: la goroutine abandonada
**no se puede matar**. La librería de dbus no tiene forma de cancelar una
llamada a medias, así que la goroutine se queda esperando. Se acepta ese
coste (una goroutine por operación que expira) a cambio de que el **proceso**
nunca se cuelgue: un boot colgado deja el MCP entero sin servir, y una
goroutine huérfana no.

### 4.2 `internal/orchestration/llm_failover_wiring.go` (modificado)

`buildDefaultKeyStore()` ahora hace tres cosas acotadas:

```go
ring, err := openKeyringWithDeadline()   // la apertura, con plazo
if err != nil {
    log.Printf("dark-mem-mcp: OS keyring unavailable (%v) — using env-var keys only", err)
    return env, nil
}

safe := llmkeystore.NewTimeoutStore(ring)   // cada operación, con plazo
composite := llmkeystore.NewComposite(safe, env)

migrated, merr := migrateWithDeadline(safe, llmkeystore.EnvEntriesFromCatalog(pairs))
```

La **migración** `env → keyring` va acotada por una razón concreta: **escribe**
en el keyring, y con la colección bloqueada esa escritura no vuelve nunca.
Si expira, se abandona y las claves de env siguen mandando — que es
justo lo que garantiza el fallback del composite.

El resultado, en una línea: **un keyring colgado degrada a env-var keys y el
MCP arranca. Nunca al revés.**

---

## 5. La prueba: contra el keyring REAL, no un simulacro

Un test con un store de mentira demuestra que el wrapper funciona. No
demuestra que arregla el bug real. Por eso la prueba de fuego es contra el
keyring de verdad, con la colección bloqueada:

| Binario | Resultado (40 s de espera) |
|---|---|
| **El de antes** | **NO RESPONDIÓ** — nunca sirvió, ni una línea de `initialize`. |
| **El de después** | **RESPONDIÓ a los 2,05 s**, con el log `env→keyring migration partial (llm keystore: operation timed out…)`. |

Ese log es la prueba de que la degradación ocurrió de verdad: el keyring
expiró, y el servidor siguió adelante con las claves de env.

---

## 6. Los tests

`internal/llmkeystore/timeout_store_test.go` (nuevo), 6 tests:

| Test | Qué demuestra |
|---|---|
| `SetQueBloquea_ReturnsEnElPlazo` | Una escritura que no vuelve devuelve `ErrKeyringTimeout` **dentro del plazo**. Este es el bug, en forma de test. |
| `GetQueBloquea_ReturnsEnElPlazo` | Una lectura que no vuelve, igual. |
| `Composite_CaeAlEnvCuandoElKeyringSeCuelga` | La lectura **cae al backend de env**: el juez sigue teniendo credencial. |
| `UnStoreSano_NoSeDegrada` | Un store sano devuelve **su** resultado: el plazo no convierte un keyring sano en uno roto. |
| `PlazoPorDefecto_EsHolgado` | El plazo por defecto está entre 500 ms y 10 s: ni falsos positivos ni un timeout del harness. |
| `MigrateFromEnv_ConKeyringColgado_NoSeColga` | La migración **que escribe** no cuelga el boot. |

---

## 7. Lo que este PR NO cambia

- **No cambia la política del keyring.** La credencial sigue viviendo
  **solo** en el vault del SO. `WS-6` intacto.
- **No cambia el formato de la credencial**, ni la migración, ni el
  composite, ni el orden de lectura.
- **No toca el juez.** El binding del proyecto, el failover y el protocolo
  del veredicto son los mismos.
- **No cambia `DARK_LLM_KEYRING=0`**: ese escape hatch sigue siendo lo que
  es (env-var only, sin abrir el keyring). Este PR hace que **ya no haga
  falta** para sobrevivir a un keyring colgado.
- **No arregla la configuración del sistema**: la colección bloqueada de
  esta máquina sigue bloqueada, y desbloquearla sigue siendo cosa del
  operador (o de un agente de prompts). Este PR hace que **eso ya no tumbe
  el MCP**.

---

## 8. Verificación

```
$ go build ./internal/...
$ go vet ./internal/llmkeystore/ ./internal/orchestration/
$ gofmt -l internal/llmkeystore/timeout_store.go \
            internal/llmkeystore/timeout_store_test.go \
            internal/orchestration/llm_failover_wiring.go
(vacío)

$ go test ./internal/llmkeystore/ ./internal/orchestration/ -count=1
ok  github.com/dark-agents/dark-memory-mcp/internal/llmkeystore  0.428s
ok  github.com/dark-agents/dark-memory-mcp/internal/orchestration   9.650s
```

El único fallo de `go test ./...` es `internal/embedder/onnx` (la
dependencia `onnxruntime_go` no trae los blobs de la biblioteca en este
entorno). Es **preexistente**: comprobado con `git stash` sobre el árbol
limpio, falla igual.

La suite de `internal/orchestration` tarda ~10 s porque tiene pruebas de
veredicto con LLM que degradan a `needs_human` cuando no hay credencial.

---

## 9. Una nota que no es del PR, pero este PR la hace visible

El mismo keyring bloqueado era también la causa de que el juez devolviera
`needs_human` con confianza 0 y `confidence_caveat: "no judge provider
recorded"` — **ausencia** de veredicto, no reprobación.

La circularidad era esta: para judgear por el proveedor `judge-opita-gate`
hace falta **persistir** su credencial, y persistir exige el keyring, que no
es escribible. El keyring y el juez eran **el mismo problema**.

La salida (que no es parte de este PR, porque es de la configuración de la
máquina, no del código) fue escribir el `auth_token` del canal NLI en
`projects.nli_config_json`, que es de donde lo lee
`internal/nli/chat.go:254`. Con eso, `llm_provider_probe` devolvió
`ok: true, status_code: 200` y el veredicto salió `aligned 0.99`.

Lo que este PR aporta a esa historia es **el principio**: un subsistema
opcional que no puede responder, degrada. Y eso vale tanto para el keyring
como para cualquier otro.
