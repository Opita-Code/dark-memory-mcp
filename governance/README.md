# Workspace de dark-memory-mcp

> **TL;DR**: este directorio tiene dos cosas: la **herramienta** que valida
> las reglas del workspace antes de gastar tokens (`dm-flow.sh`) y la
> **documentación** de cómo se integra dark-memory en un flujo de desarrollo
> (`FLOW.md`). La política del operador (`workspace-policy.constitution.json`)
> es un artefacto por proyecto: vive en tu workspace y **no** se versiona
> aquí; en su lugar hay una forma canónica (`.example.json`) y el YAML de
> contrato.

Este directorio es el **workspace canónico** del proyecto: un checkout git real
de `https://github.com/Opita-Code/dark-memory-mcp` (rama `main`).

## Reglas (WS-1 … WS-6)

Están codificadas en la constitution del workspace
(`dark-agents/workspace-policy` v1.1.0), cuyo contenido canónico está en
[`workspace-policy.constitution.example.json`](./workspace-policy.constitution.example.json):

| Regla | Qué exige |
|---|---|
| WS-1 | Un workspace por proyecto: `<workspace_root>/<project_id>`. El home es espacio personal, no de proyecto. |
| WS-2 | Nada de workspaces temporales: `/tmp` y `/var/tmp` no guardan artefactos de proyecto. |
| WS-3 | Los entregables se citan por su ruta dentro del workspace. |
| WS-4 | La constitution que gobierna al operador reside dentro del workspace (no en un `os.TempDir()`). |
| WS-5 | Cada write registra `constitution_id@version`. |
| WS-6 | Fail closed: cambiar la constitution sin bumpear la versión hace que el servidor no abra el store. |

Los directorios temporales solo se usan como intermediarios descartables y se
copian al workspace antes de terminar.

## Las tres formas, y por qué

| Fichero | Qué es | ¿Versionado? |
|---|---|---|
| `constitutions/dark-agents-workspace-policy.yaml` | El artefacto de contrato (§7): la política en YAML, versionada y SHA-pineada. | Sí. |
| `governance/workspace-policy.constitution.example.json` | La forma **canónica**: los marcadores `<workspace_root>` y `<home_root>` sin sustituir. Sin datos personales. | Sí. |
| `governance/workspace-policy.constitution.json` | La instancia **activa**: el ejemplo con los marcadores sustituidos por tus rutas reales. | No (está en `.gitignore`). |

El motivo de que la activa no se versione: una constitution es la **política
del operador para un proyecto concreto**, no un default del producto. El
producto shippea sus propias constitutions (ver `constitutions/` en
[ARCHITECTURE-V4.md](../ARCHITECTURE-V4.md)); versionar la política de un
workspace mezcla dos cosas distintas y expone la configuración de un operador
en un repositorio público.

### Marcadores: el contrato es portable

La forma canónica lleva dos marcadores en lugar de rutas:

| Marcador | Qué es | Valor por defecto |
|---|---|---|
| `<workspace_root>` | La raíz de workspaces | `$HOME/workspace` |
| `<home_root>` | El home del operador | `$HOME` |

El contrato versionado **no lleva la ruta de ninguna máquina concreta**: quien
lo lee puede instanciarlo con sus propias rutas. Es lo que hace que el
artefacto sea reutilizable en otro repo y en otro host.

### Los SHA: el contrato fija el ejemplo, el watchdog vigila el activo

| Hash | Qué fija | Quién lo comprueba |
|---|---|---|
| `sha256:` del YAML | El **ejemplo** (contenido estable, sin datos personales) | `governance/check-constitution.sh` |
| SHA del **activo** | El fichero que el servidor carga | El watchdog, en cada arranque |
| `<activo>.sha256` | El activo, contra un pin local | `check-constitution.sh`, si el pin existe |

Un SHA local en el contrato lo ataría a una máquina: otro operador, con otras
rutas, generaría un hash distinto y el contrato quedaría inservible. Por eso
el YAML fija el ejemplo y el fichero activo se pinea en local.

## Instalar la forma activa

```bash
sed -e "s|<workspace_root>|$HOME/workspace|g" \
    -e "s|<home_root>|$HOME|g" \
    governance/workspace-policy.constitution.example.json \
    > governance/workspace-policy.constitution.json
```

Y hay que **subir la versión**: el watchdog rechaza un cambio que no venga
acompañado de un bump, porque eso es exactamente lo que evita que alguien
cambie la política por debajo (WS-6).

## Por qué la constitution activa no se versiona aquí

La constitution del contrato es YAML (§7). La que el servidor consume es
JSON, y no por gusto: el watchdog guarda los **bytes crudos** del fichero en
`constitutions.parsed_json` y el inyector de persona los parsea con
`json.Unmarshal`
(`internal/recall/assemble.go:parsePersonaFromConstitution`). Una
constitution en YAML o TOML queda guardada pero su sección `persona` **nunca
llega al agente** — le pasa también a la constitution builtin del repo
(`vibe-flow/constitution/dark-mem.constitution.toml`, cuya sección `[tone]`
tiene el mismo destino).

`governance/check-constitution.sh` es el paso de verificación que §7 pide y
que `dark-mem constitution verify` no implementa en esta build: comprueba que
las tres formas dicen lo mismo, que el SHA del contrato es el del ejemplo y
que el activo es el ejemplo instanciado.

## Cómo se aplica

```bash
# el binario MCP lee la constitution de este workspace
export DARK_CONSTITUTION_FILE="$PWD/governance/workspace-policy.constitution.json"
export DARK_CONSTITUTION_ID="dark-agents/workspace-policy"
export DARK_CONSTITUTION_VER="1.1.0"
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

Sin el bump de versión, el watchdog rechaza el archivo (misma versión, SHA
distinto) y el servidor no abre el store. Es el comportamiento buscado.

## Verificación

```bash
# la constitution activa y su SHA
dark_memory_active_policy   # constitution_id, version, sha256, drift

# el persona frame: debe llegar la regla de workspace
dark_memory_recall          # frames.persona.voice / .claims_policy

# el pre-flight de las reglas del workspace
governance/dm-flow.sh check  <project_id> <spec_id> --artifact "$RUTA"

# las tres formas de la constitution dicen lo mismo
governance/check-constitution.sh

# los codigos de salida del pre-flight (contrato, no detalle)
governance/test-dm-flow.sh
```

## Idioma

Toda la documentación de este repositorio va en **español tuteado**: los
comentarios de los scripts, las notas de la constitution, este README y
`FLOW.md`. Es la convención del repo (ver el PR que cambia §10.2 de
`CONTRIBUTING.md` para que lo diga de forma explícita).

## Componentes

| Ruta | Qué es |
|---|---|
| `governance/dm-flow.sh` | Pre-flight de las reglas + plan de llamadas MCP. No habla MCP por sí mismo: valida y emite el plan; el agente ejecuta con sus propias tools. |
| `governance/test-dm-flow.sh` | Tests de los códigos de salida de `dm-flow.sh` (0 / 64 / 65 / 66), del contenido del plan y de que los artefactos versionados no lleven datos del operador. |
| `governance/check-constitution.sh` | Verifica que el YAML de contrato, el ejemplo y la copia activa coinciden (id, version, persona, SHA del ejemplo, instanciación). |
| `governance/FLOW.md` | Cómo se integra dark-memory en un flujo de desarrollo, qué garantiza cada capa y qué huecos hay. |
| `constitutions/dark-agents-workspace-policy.yaml` | La política del workspace como artefacto de contrato (§7): YAML, versionado, SHA-pineado sobre el ejemplo. |
| `governance/workspace-policy.constitution.example.json` | La forma canónica que el servidor carga (JSON), con marcadores y sin datos personales. |
| `governance/workspace-policy.constitution.json` | La copia activa que carga el servidor (no versionada). |
| `docs/` | Documentación canónica: `tools.md`, `v4-status.md`, `npm-install.md`, `agent-bootstrap.md`. |
| `CONSTITUTION.md` | Constitution de release-integrity del repo (reglas de release, no de workspace). |
| `vibe-flow/constitution/` | Constitutions del unit `dark-mem` (TOML, embebidas en el binario). |
