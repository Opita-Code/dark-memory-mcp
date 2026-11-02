#!/usr/bin/env bash
# dm-flow — el ciclo de desarrollo ordenado, con dark-memory como nucleo.
#
# POR QUE NO HABLA MCP DIRECTAMENTE
#
# El primer prototipo de este script levantaba su propio servidor MCP y le
# hablaba por FIFOs. Se colgaba: leer de una FIFO bloquea si la respuesta no
# llega, y un hang del script tumba la sesion del harness. Un gate de
# desarrollo no puede ser el componente que cuelga el flujo.
#
# Asi que este script NO invoca el MCP. Su trabajo es:
#
#   1. VALIDAR las reglas del workspace (WS-2, WS-3, WS-4) ANTES de que el
#      agente gaste un solo token. El fallo barato se detecta antes del
#      trabajo caro.
#   2. EMITIR el plan de llamadas MCP exactas, con los argumentos ya
#      resueltos (session_id, constitution_id, artifact_ref con la ruta
#      canonica). El agente las ejecuta con sus propias tools, donde el
#      gate del servidor ya las valida.
#
# Es el mismo contrato que vibe_compile genera, pero con un shim HONESTO:
# no simula que la llamada funciono, dice que falta y por que.
#
# Uso:
#   dm-flow check  <project_id> <spec_id> [--artifact <path>]
#   dm-flow plan   <project_id> <spec_id> [--artifact <path>]
#   dm-flow gate   <project_id> <spec_id> [--artifact <path>]
#
# Codigos de salida: 0 ok · 64 uso · 65 constitution fuera del workspace
#                     66 artefacto fuera del workspace · 67 falta el spec

set -euo pipefail

# La raiz de workspaces por defecto es $HOME/workspace: el valor se puede
# sobreescribir con WORKSPACE, de modo que el script no lleva la ruta de
# ninguna maquina concreta dentro.
WORKSPACE="${WORKSPACE:-${HOME}/workspace}"
CONSTITUTION_ID="${DARK_CONSTITUTION_ID:-dark-agents/workspace-policy}"
CONSTITUTION_VER="${DARK_CONSTITUTION_VER:-1.1.0}"

CMD="${1:-}"
PROJECT_ID="${2:-}"
SPEC_ID="${3:-}"
ARTIFACT=""

shift $(( ${#CMD} > 0 ? 1 : 0 ))
shift $(( ${#PROJECT_ID} > 0 ? 1 : 0 ))
shift $(( ${#SPEC_ID} > 0 ? 1 : 0 ))
while [[ $# -gt 0 ]]; do
  case "$1" in
    --artifact) ARTIFACT="${2:-}"; shift 2 || shift ;;
    *) shift ;;
  esac
done

if [[ -z "$CMD" || -z "$PROJECT_ID" || -z "$SPEC_ID" ]]; then
  echo "uso: dm-flow {check|plan|gate} <project_id> <spec_id> [--artifact <path>]" >&2
  exit 64
fi

WORKSPACE_DIR="$WORKSPACE/$PROJECT_ID"
CONSTITUTION_FILE="$WORKSPACE_DIR/governance/workspace-policy.constitution.json"

# ---------------------------------------------------------------------------
# PRE-FLIGHT: cheapest checks first. Un gate caro despues de un fallo barato
# es un gate mal disenado.
# ---------------------------------------------------------------------------
fail() { echo "dm-flow: $1" >&2; exit "$2"; }

# WS-4: la constitution que gobierna el ciclo debe residir en el workspace.
[[ -f "$CONSTITUTION_FILE" ]] || fail \
  "no existe la constitution en el workspace: $CONSTITUTION_FILE
  la constitution debe residir en el workspace (WS-4), no en un temporal." 65
[[ "$CONSTITUTION_FILE" == "$WORKSPACE_DIR"/* ]] || fail \
  "la constitution esta fuera del workspace: $CONSTITUTION_FILE" 65
[[ -d "$WORKSPACE_DIR" ]] || fail \
  "el workspace del proyecto no existe: $WORKSPACE_DIR (WS-1)" 65

# WS-2 / WS-3: el artefacto, si lo hay, dentro del workspace.
if [[ -n "$ARTIFACT" ]]; then
  case "$ARTIFACT" in
    /tmp/*|/var/tmp/*)
      fail "el artefacto esta en un espacio temporal: $ARTIFACT
  regla WS-2: los artefactos de proyecto no viven en /tmp ni /var/tmp." 66
      ;;
  esac
  [[ "$ARTIFACT" == "$WORKSPACE_DIR"/* ]] || fail \
    "el artefacto esta fuera del workspace del proyecto: $ARTIFACT
  regla WS-3: los entregables viven dentro de $WORKSPACE_DIR." 66
  [[ -f "$ARTIFACT" ]] || fail "el artefacto no existe: $ARTIFACT" 66
fi

echo "pre-flight OK"
echo "  proyecto    : ${PROJECT_ID}"
echo "  workspace   : ${WORKSPACE_DIR}"
echo "  spec_id     : ${SPEC_ID}"
echo "  constitution: ${CONSTITUTION_ID}@${CONSTITUTION_VER}"
[[ -n "$ARTIFACT" ]] && echo "  artefacto   : ${ARTIFACT}"
echo

case "$CMD" in
  check)
    echo "check: las reglas del workspace se respetan. Nada que hacer."
    exit 0
    ;;
  plan|gate) ;;
  *) fail "comando desconocido: ${CMD} (usa check|plan|gate)" 64 ;;
esac

# ---------------------------------------------------------------------------
# PLAN: las llamadas MCP exactas, con los argumentos resueltos.
# ---------------------------------------------------------------------------
cat <<EOF
=== PLAN DE LLAMADAS MCP (ejecutalas con tus tools, en este orden) ===

[1] dark_memory_session_start
    operator        : opencode
    project_id      : ${PROJECT_ID}
    constitution_id : ${CONSTITUTION_ID}
    constitution_ver: ${CONSTITUTION_VER}
    notes           : dm-flow spec ${SPEC_ID}
    -> el gate exige sesion activa; sin esto, todo lo demas devuelve
       ErrFrameStaleTooFar.

[2] dark_memory_spec_context
    spec_id : ${SPEC_ID}
    -> lee la promesa. Si el intent no esta, no publiques: primero
       dark_memory_vibe_spec.

[3] dark_memory_agent_memory_recall
    operator : opencode
    query    : "<lo que sabes de este dominio>"
    scope    : project
    -> memoria primero: lo que ya se decidio no se re-debe.

[4] [TRABAJO] produ el artefacto en ${WORKSPACE_DIR}
    El agente escribe codigo normal. El gate no participa: el gate protege
    la MEMORIA, no el disco. Lo que protege el disco es WS-3, que ya
    valido el pre-flight de este script.

[5] dark_memory_vibe_publish
    session_id : <el de [1]>
    spec.vibe_case : C1
    spec.spec  : JSON.stringify({ intent: "<intent EXACTO del spec [2]>" })
    artifact.artifact_type : code
    artifact.artifact_url  : file://${ARTIFACT:-<ruta en el workspace>}
    artifact.artifact_ref : { kind: "file", path: "${ARTIFACT:-<ruta>}" }
    artifact.auto_save_decision : true
    -> el artifact_ref es OBLIGATORIO: sin el, el juez no puede leer el
       artefacto y devuelve needs_human por campo faltante.
    -> spec.spec DEBE llevar el intent dentro, con la clave "intent".

[6] INTERPRETAR EL VEREDICTO
    aligned         -> continua a [7]
    drift_detected  -> corrige el artefacto y re-publica (max 2 intentos)
    needs_human     -> PARA. No inventes un veredicto.

[7] dark_memory_agent_memory_save
    operator : opencode
    kind     : decision
    pinned   : true
    title    : "Spec ${SPEC_ID} en ${PROJECT_ID}"
    content  : "<que se entrego, el veredicto y la ruta del artefacto>"
    tags     : "vibe-loop,spec:${SPEC_ID},${PROJECT_ID}"

[8] dark_memory_session_close
    session_id : <el de [1]>
    -> cierra limpio y deja el rastro.

EOF

echo "=== GATES HUMANOS (no automatizables) ==="
cat <<EOF
  - El veredicto needs_human requiere al operador: el juez no sabe.
  - Si el artefacto cambio de proposito (no de forma), rehaz el spec,
    no lo fuerces contra el intent viejo.
  - El cierre de sesion es lo que hace que la proxima sesion empiece
    sabiendo. Sin el, el trabajo se pierde.
EOF
