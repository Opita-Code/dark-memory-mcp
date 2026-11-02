#!/usr/bin/env bash
# governance/check-constitution.sh
#
# Verifica que la constitution YAML (el artefacto de contrato, §7) y la
# copia JSON que el servidor carga (la forma que el inyector de persona
# sabe leer) dicen LO MISMO.
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
# QUE COMPRUEBA
#
#   1. Los dos ficheros existen.
#   2. id y version coinciden entre YAML y JSON.
#   3. La seccion persona del JSON existe y sus tres campos son
#      literales (voice, claims_policy, tone): es lo que el inyector
#      parsea con json.Unmarshal. Sin esto la regla NUNCA llega al
#      agente, aunque el watchdog acepte el fichero.
#   4. El SHA-256 del JSON activo coincide con el sha256 declarado en
#      el YAML: el watchdogetu ya esta vigilando ese hash.
#   5. El JSON es JSON valido (jq lo parsea sin error).
#
# USO
#
#   governance/check-constitution.sh [ruta_json]
#
# CODIGOS: 0 ok · 64 uso · 65 discrepancia · 66 fichero que falta

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

YAML="${YAML_PATH:-$REPO_ROOT/constitutions/dark-agents-workspace-policy.yaml}"
JSON="${1:-$SCRIPT_DIR/workspace-policy.constitution.json}"

fail() { echo "check-constitution: $1" >&2; exit "$2"; }

command -v jq >/dev/null 2>&1 || fail "se necesita jq para verificar" 64

[[ -f "$YAML" ]] || fail "no existe la constitution YAML: $YAML" 66
[[ -f "$JSON" ]] || fail "no existe la constitution JSON activa: $JSON
  creala desde el ejemplo:
    cp governance/workspace-policy.constitution.example.json \\
       governance/workspace-policy.constitution.json" 66

# --- 5. el JSON es JSON valido ------------------------------------------------
jq -e . "$JSON" >/dev/null 2>&1 || fail "el JSON no parsea: $JSON" 65

# --- 2. id y version coinciden -----------------------------------------------
yaml_value() { grep -m1 -E "^$1:[[:space:]]*" "$YAML" | sed -E "s/^$1:[[:space:]]*//; s/^[\"']//; s/[\"']$//"; }

yaml_id="$(yaml_value id)"
yaml_version="$(yaml_value version)"
json_id="$(jq -r '.meta.id // .id // ""' "$JSON")"
json_version="$(jq -r '.meta.version // .version // ""' "$JSON")"

[[ -n "$yaml_id" && -n "$yaml_version" ]] || fail "el YAML no declara id/version" 65
[[ "$yaml_id" == "$json_id" ]] || fail "discrepancia de id:
  YAML: $yaml_id
  JSON: $json_id" 65
[[ "$yaml_version" == "$json_version" ]] || fail "discrepancia de version:
  YAML: $yaml_version
  JSON: $json_version" 65

# --- 3. la persona existe y es literal ---------------------------------------
for field in voice claims_policy tone; do
  json_val="$(jq -r --arg f "$field" '.persona[$f] // ""' "$JSON")"
  [[ -n "$json_val" ]] || fail "el JSON no tiene persona.$field
  sin esa seccion el inyector (parsePersonaFromConstitution) no inyecta
  nada y la politica no llega al agente" 65
  yaml_val="$(grep -m1 -E "^[[:space:]]+$field:[[:space:]]*" "$YAML" \
    | sed -E "s/^[[:space:]]+$field:[[:space:]]*//; s/^[\"']//; s/[\"']$//")"
  [[ -n "$yaml_val" ]] || fail "el YAML no declara persona.$field" 65
  [[ "$yaml_val" == "$json_val" ]] || fail "discrepancia en persona.$field:
  el YAML y el JSON deben decir exactamente lo mismo" 65
done

# --- 4. el SHA que el watchdogetu vigila ------------------------------------
declared_sha="$(yaml_value sha256)"
if [[ -n "$declared_sha" ]]; then
  actual_sha="$(sha256sum "$JSON" | cut -d' ' -f1)"
  [[ "$declared_sha" == "$actual_sha" ]] || fail "el SHA del JSON activo no coincide con el declarado:
  declarado: $declared_sha
  actual:   $actual_sha
  el watchdogetu rechazaria este fichero (mismo id/version, hash distinto)" 65
fi

echo "OK: la constitution YAML y el JSON activo coinciden."
echo "  id      : $yaml_id"
echo "  version : $yaml_version"
echo "  persona : voice + claims_policy + tone presentes y literales"
[[ -n "$declared_sha" ]] && echo "  sha256  : $declared_sha (coincide)"
exit 0
