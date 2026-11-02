# Workspace de dark-memory-mcp

> **TL;DR**: este directorio tiene dos cosas: la **herramienta** que valida
> las reglas del workspace antes de gastar tokens (`dm-flow.sh`) y la
> **documentación** de cómo se integra dark-memory en un flujo de desarrollo
> (`FLOW.md`). La política activa del operador (`workspace-policy.constitution.json`)
> es un artefacto por proyecto: vive en tu workspace y **no** se versiona
> aquí; en su lugar hay un `.example.json` reproducible.

Este directorio es el **workspace canónico** del proyecto: un checkout git real
de `https://github.com/Opita-Code/dark-memory-mcp` (rama `main`).

## Reglas (WS-1 … WS-6)

Están codificadas en la constitution del workspace
(`dark-agents/workspace-policy` v1.0.0), cuyo contenido de ejemplo está en
[`workspace-policy.constitution.example.json`](./workspace-policy.constitution.example.json):

| Regla | Qué exige |
|---|---|
| WS-1 | Un workspace por proyecto: `/home/nico/workspace/<project_id>`. El home es espacio personal, no de proyecto. |
| WS-2 | Nada de workspaces temporales: `/tmp` y `/var/tmp` no guardan artefactos de proyecto. |
| WS-3 | Los entregables se citan por su ruta dentro del workspace. |
| WS-4 | La constitution que gobierna al operador reside dentro del workspace (no en un `os.TempDir()`). |
| WS-5 | Cada write registra `constitution_id@version`. |
| WS-6 | Fail closed: cambiar la constitution sin bumpear la versión hace que el servidor no abra el store. |

Los directorios temporales solo se usan como intermediarios descartables y se
copian al workspace antes de terminar.

## Por qué la constitution activa no se versiona aquí

Una constitution es la **política del operador para un proyecto concreto**, no
un default del producto. El producto shippea sus propias constitutions (ver
`constitutions/` en [ARCHITECTURE-V4.md](../ARCHITECTURE-V4.md)); versionar
la política de un workspace mezcla dos cosas distintas y expone la
configuración de un operador en un repositorio público.

Por eso:

| Fichero | Qué es | ¿Versionado? |
|---|---|---|
| `workspace-policy.constitution.example.json` | El contenido de la política, reproducible. | Sí. |
| `workspace-policy.constitution.json` | La copia activa que carga el servidor. | No (está en `.gitignore`). |

El archivo activo se crea a partir del ejemplo, y su SHA es el que el watchdog
vigila:

```bash
cp governance/workspace-policy.constitution.example.json \
   governance/workspace-policy.constitution.json
```

## Cómo se aplica

```bash
# el binario MCP lee la constitution de este workspace
export DARK_CONSTITUTION_FILE="$PWD/governance/workspace-policy.constitution.json"
export DARK_CONSTITUTION_ID="dark-agents/workspace-policy"
export DARK_CONSTITUTION_VER="1.0.0"
```

En `~/.config/opencode/opencode.json` ya está cableado en el servidor `dark-memory`.

### Formato: JSON, no TOML — a propósito

El watchdog guarda los **bytes crudos** del archivo en `constitutions.parsed_json`
y el inyector de persona los parsea con `json.Unmarshal`
(`internal/recall/assemble.go:parsePersonaFromConstitution`). Una constitution en
TOML queda guardada pero su sección `[persona]` **nunca llega al agente**.

Por eso este archivo es JSON: la sección `persona` sí se inyecta en cada
`dark_memory_recall`, que es lo que hace que la regla se cumpla.

### Evolución de la política

1. Edita `governance/workspace-policy.constitution.json`.
2. Sube `meta.version` **y** la variable `DARK_CONSTITUTION_VER`.
3. Arranca el servidor con `DARK_CONSTITUTION_ACCEPT_BUMPS=1`.

Sin el bump de versión, el watchdog rechaza el archivo (mismo versión, SHA
distinto) y el servidor no abre el store. Es el comportamiento buscado.

## Verificación

```bash
# la constitution activa y su SHA
dark_memory_active_policy   # constitution_id, version, sha256, drift

# el persona frame: debe llegar la regla de workspace
dark_memory_recall          # frames.persona.voice / .claims_policy

# el pre-flight de las reglas del workspace
governance/dm-flow.sh check  <project_id> <spec_id> --artifact "$RUTA"
```

## Componentes

| Ruta | Qué es |
|---|---|
| `governance/dm-flow.sh` | Pre-flight de las reglas + plan de llamadas MCP. No habla MCP por sí mismo: valida y emite el plan; el agente ejecuta con sus propias tools. |
| `governance/FLOW.md` | Cómo se integra dark-memory en un flujo de desarrollo, qué garantiza cada capa y qué huecos hay. |
| `governance/workspace-policy.constitution.example.json` | El contenido de la política del workspace (ejemplo reproducible). |
| `governance/workspace-policy.constitution.json` | La copia activa que carga el servidor (no versionada). |
| `docs/` | Documentación canónica: `tools.md`, `v4-status.md`, `npm-install.md`, `agent-bootstrap.md`. |
| `CONSTITUTION.md` | Constitution de release-integrity del repo (reglas de release, no de workspace). |
| `vibe-flow/constitution/` | Constitutions del unit `dark-mem` (TOML, embebidas en el binario). |
