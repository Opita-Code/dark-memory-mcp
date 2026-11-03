#!/usr/bin/env bash
# governance/check-constitution.sh
#
# Verifica la constitution del workspace en sus tres formas.
#
# POR QUE EXISTE
#
# CONTRIBUTING.md §6 y ARCHITECTURE-V4.md §7 describen un comando
# `dark-mem constitution verify` que verifica una constitution antes de
# mergearla. Ese comando NO existe en esta build (v4.0.0-alpha.33): el
# CLI no lo expone y no hay ninguna tool que lo haga. Este script es el
# equivalente honesto: comprueba lo que un "verify" debe comprobar antes
# de que una constitution llegue a un servidor.
#
# LAS TRES FORMAS
#
#   1. EL CONTRATO   constitutions/dark-agents-workspace-policy.yaml
#                    El artefacto que exige §7: YAML, versionado, SHA-pineado.
#                    El SHA que fija es el del EJEMPLO, no el de una maquina
#                    concreta: asi el contrato es portable y reutilizable.
#
#   2. EL EJEMPLO    governance/workspace-policy.constitution.example.json
#                    La forma canonica, sin datos personales, con los
#                    marcadores <workspace_root> y <home_root>.
#
#   3. EL ACTIVO     governance/workspace-policy.constitution.json
#                    La instancia local: el ejemplo con los marcadores
#                    sustituidos por las rutas reales del operador. No se
#                    versiona (esta en .gitignore). El watchdog vigila su SHA.
#
# QUE COMPRUEBA
#
#   1. Los tres ficheros existen.
#   2. El JSON del ejemplo y el activo son JSON valido (jq los parsea).
#   3. id y version coinciden entre el YAML y el ejemplo.
#   4. La seccion persona existe y sus tres campos son literales
#      (voice, claims_policy, tone): es lo que el inyector parsea con
#      json.Unmarshal. Sin esto la regla NUNCA llega al agente, aunque el
#      watchdog acepte el fichero.
#   5. El SHA-256 del EJEMPLO coincide con el sha256 declarado en el YAML.
#      El contrato fija contenido estable, no la configuracion de un host.
#   6. El ACTIVO, con las rutas reales sustituidas por los marcadores, es
#      byte a byte el EJEMPLO: el activo es el ejemplo instanciado y no
#      puede desviarse sin que se note.
#   7. Si existe un pin local (<activo>.sha256), el SHA del activo coincide
#      con el: deteccion de manipulacion local del fichero que el watchdog
#      vigila.
#
# USO
#
#   governance/check-constitution.sh [ruta_json_activa]
#
# CODIGOS: 0 ok · 64 uso · 65 discrepancia · 66 fichero que falta

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

YAML="${YAML_PATH:-$REPO_ROOT/constitutions/dark-agents-workspace-policy.yaml}"
EXAMPLE="${EXAMPLE_PATH:-$SCRIPT_DIR/workspace-policy.constitution.example.json}"
JSON="${1:-$SCRIPT_DIR/workspace-policy.constitution.json}"

# Las raices que el operador sustituye en el ejemplo. Por defecto derivan
# del home, que es donde vive el workspace por convencion; se pueden
# sobrescribir por entorno si el operador usa otra disposicion.
WORKSPACE_ROOT="${WORKSPACE_ROOT:-$HOME/workspace}"
HOME_ROOT="${HOME_ROOT:-$HOME}"

fail() { echo "check-constitution: $1" >&2; exit "$2"; }

command -v jq >/dev/null 2>&1 || fail "se necesita jq para verificar" 64

[[ -f "$YAML" ]]    || fail "no existe la constitution YAML de contrato: $YAML" 66
[[ -f "$EXAMPLE" ]] || fail "no existe el JSON de ejemplo: $EXAMPLE" 66
[[ -f "$JSON" ]]    || fail "no existe la constitution JSON activa: $JSON
  creala desde el ejemplo sustituyendo los marcadores:
    sed -e \"s|<workspace_root>|$WORKSPACE_ROOT|g\" \\
        -e \"s|<home_root>|$HOME_ROOT|g\" \\
        governance/workspace-policy.constitution.example.json \\
        > governance/workspace-policy.constitution.json
  y sube meta.version y DARK_CONSTITUTION_VER: el watchdog rechaza el
  cambio si la version no sube." 66

# --- 2. los JSON son JSON valido ---------------------------------------------
jq -e . "$EXAMPLE" >/dev/null 2>&1 || fail "el ejemplo no parsea: $EXAMPLE" 65
jq -e . "$JSON"     >/dev/null 2>&1 || fail "el JSON activo no parsea: $JSON" 65

# --- 3. id y version coinciden entre el YAML y el ejemplo --------------------
yaml_value() { grep -m1 -E "^$1:[[:space:]]*" "$YAML" | sed -E "s/^$1:[[:space:]]*//; s/^[\"']//; s/[\"']$//"; }

yaml_id="$(yaml_value id)"
yaml_version="$(yaml_value version)"
example_id="$(jq -r '.meta.id // .id // ""' "$EXAMPLE")"
example_version="$(jq -r '.meta.version // .version // ""' "$EXAMPLE")"

[[ -n "$yaml_id" && -n "$yaml_version" ]] || fail "el YAML no declara id/version" 65
[[ "$yaml_id" == "$example_id" ]] || fail "discrepancia de id:
  YAML:    $yaml_id
  ejemplo: $example_id" 65
[[ "$yaml_version" == "$example_version" ]] || fail "discrepancia de version:
  YAML:    $yaml_version
  ejemplo: $example_version" 65

# --- 4. la persona existe y es literal ---------------------------------------
# En el YAML y en el ejemplo: son la forma canonica y su contrato.
for field in voice claims_policy tone; do
  example_val="$(jq -r --arg f "$field" '.persona[$f] // ""' "$EXAMPLE")"
  [[ -n "$example_val" ]] || fail "el ejemplo no tiene persona.$field
  sin esa seccion el inyector (parsePersonaFromConstitution) no inyecta
  nada y la politica no llega al agente" 65
  yaml_val="$(grep -m1 -E "^[[:space:]]+$field:[[:space:]]*" "$YAML" \
    | sed -E "s/^[[:space:]]+$field:[[:space:]]*//; s/^[\"']//; s/[\"']$//")"
  [[ -n "$yaml_val" ]] || fail "el YAML no declara persona.$field" 65
  [[ "$yaml_val" == "$example_val" ]] || fail "discrepancia en persona.$field:
  el YAML y el ejemplo deben decir exactamente lo mismo" 65
done

# --- 7. el SHA del ACTIVO, el que el watchdog vigila -------------------------
active_sha="$(sha256sum "$JSON" | cut -d' ' -f1)"
PIN="${JSON}.sha256"
if [[ -f "$PIN" ]]; then
  pinned_sha="$(cut -d' ' -f1 < "$PIN" | head -1)"
  [[ "$pinned_sha" == "$active_sha" ]] || fail "el SHA del JSON activo no coincide con el pin local:
  pin:    $pinned_sha
  actual: $active_sha
  el watchdog rechazaria este fichero (mismo id/version, hash distinto)" 65
  pin_state="coincide con el pin local"
else
  pin_state="sin pin local (pinalo con: sha256sum $JSON > $PIN)"
fi

# --- 5. el contrato fija el SHA del EJEMPLO ---------------------------------
declared_sha="$(yaml_value sha256)"
if [[ -n "$declared_sha" ]]; then
  example_sha="$(sha256sum "$EXAMPLE" | cut -d' ' -f1)"
  [[ "$declared_sha" == "$example_sha" ]] || fail "el SHA del ejemplo no coincide con el declarado en el contrato:
  declarado: $declared_sha
  ejemplo:  $example_sha
  el contrato fija el ejemplo, no la configuracion de una maquina" 65
fi

# --- 6. el ACTIVO es el EJEMPLO con los marcadores sustituidos ---------------
# Se sustituye primero la raiz de workspaces (mas larga) y despues el home:
# el home es prefijo de la raiz de workspaces.
normalized="$(mktemp)"
trap 'rm -f "$normalized"' EXIT
sed -e "s|$WORKSPACE_ROOT|<workspace_root>|g" \
    -e "s|$HOME_ROOT|<home_root>|g" \
    "$JSON" > "$normalized"

cmp -s "$normalized" "$EXAMPLE" || {
  diff -u "$EXAMPLE" "$normalized" | head -40 >&2
  fail "el JSON activo NO es el ejemplo instanciado.
  Se espera el ejemplo con <workspace_root> -> $WORKSPACE_ROOT
  y <home_root> -> $HOME_ROOT sustituidos." 65
}

echo "OK: el contrato, el ejemplo y el JSON activo coinciden."
echo "  id      : $yaml_id"
echo "  version : $yaml_version"
echo "  persona : voice + claims_policy + tone presentes y literales"
[[ -n "$declared_sha" ]] && echo "  contrato: sha256 $declared_sha (es el del ejemplo)"
echo "  activo  : sha256 $active_sha ($pin_state)"
exit 0
