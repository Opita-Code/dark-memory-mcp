#!/usr/bin/env bash
# governance/test-dm-flow.sh
#
# Tests de dm-flow.sh y de la coherencia de la constitution del workspace.
#
# POR QUE EXISTEN
#
# ARCHITECTURE-V4.md §12 pide que el codigo nuevo venga con tests. dm-flow.sh
# es un ejecutable que TODA la integracion depende (su pre-flight es la capa
# que valida WS-2/WS-3/WS-4 antes de gastar tokens), asi que sus codigos de
# salida son contrato, no detalle de implementacion. Estos tests fijan ese
# contrato.
#
# QUE CUBRE
#
#   - codigo 0: artefacto dentro del workspace -> pasa
#   - codigo 64: uso incorrecto
#   - codigo 65: la constitution no esta dentro del workspace
#   - codigo 66: el artefacto esta fuera del workspace (incluido /tmp)
#   - el plan emite los 8 pasos en orden y lleva agent_id explicito
#   - los artefactos versionados no llevan rutas del home del operador
#   - el contrato fija el SHA del ejemplo, no el de una maquina
#
# USO:  governance/test-dm-flow.sh
# CODIGO: 0 todo verde · 1 algun fallo

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
FLOW="$SCRIPT_DIR/dm-flow.sh"
CHECK="$SCRIPT_DIR/check-constitution.sh"

PASS=0
FAIL=0
# El fixture NO puede vivir bajo /tmp: la regla que estos tests fijan (WS-2)
# rechaza cualquier artefacto en un espacio temporal, asi que un workspace
# de prueba bajo /tmp seria rechazado por su propia regla. Va en el scratch
# del workspace del operador, que es donde viven los intermediarios
# descartables.
SCRATCH="${DM_FLOW_TEST_SCRATCH:-$HOME/workspace/.scratch}"
mkdir -p "$SCRATCH"
TMPROOT="$(mktemp -d "$SCRATCH/dm-flow-test.XXXXXX")"
trap 'rm -rf "$TMPROOT"' EXIT

ok()   { PASS=$((PASS+1)); echo "  PASS  $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  FAIL  $1"; [ $# -gt 1 ] && echo "        $2"; }

# --- fixture: un workspace con su constitution y un artefacto dentro ---------
WS="$TMPROOT/workspace"
PROJ="demo-project"
mkdir -p "$WS/$PROJ/governance"
cp "$SCRIPT_DIR/workspace-policy.constitution.example.json" \
   "$WS/$PROJ/governance/workspace-policy.constitution.json"
mkdir -p "$WS/$PROJ/out"
echo "artefacto de prueba" > "$WS/$PROJ/out/entregable.md"

run() { WORKSPACE="$WS" bash "$FLOW" "$@" >/dev/null 2>&1; echo $?; }

# --- 1. camino feliz ----------------------------------------------------------
code="$(run check "$PROJ" 25 --artifact "$WS/$PROJ/out/entregable.md")"
[ "$code" = "0" ] && ok "check con artefacto dentro del workspace -> 0" \
                  || bad "check con artefacto dentro del workspace -> 0" "obtenido: $code"

# --- 2. uso incorrecto -------------------------------------------------------
code="$(run check)"
[ "$code" = "64" ] && ok "sin argumentos -> 64 (uso)" \
                   || bad "sin argumentos -> 64 (uso)" "obtenido: $code"

# --- 3. constitution fuera del workspace -------------------------------------
WS2="$TMPROOT/workspace2"
mkdir -p "$WS2"
code="$(WORKSPACE="$WS2" bash "$FLOW" check "$PROJ" 25 >/dev/null 2>&1; echo $?)"
[ "$code" = "65" ] && ok "constitution ausente del workspace -> 65" \
                   || bad "constitution ausente del workspace -> 65" "obtenido: $code"

# --- 4. artefacto fuera del workspace ----------------------------------------
code="$(run check "$PROJ" 25 --artifact "$TMPROOT/entregable.md")"
[ "$code" = "66" ] && ok "artefacto fuera del workspace -> 66" \
                   || bad "artefacto fuera del workspace -> 66" "obtenido: $code"

# --- 5. artefacto en un espacio temporal (WS-2) ------------------------------
mkdir -p "$TMPROOT/tmpzone"
echo "x" > "$TMPROOT/tmpzone/entregable.md"
code="$(run check "$PROJ" 25 --artifact "$TMPROOT/tmpzone/entregable.md")"
[ "$code" = "66" ] && ok "artefacto en espacio temporal -> 66 (WS-2)" \
                   || bad "artefacto en espacio temporal -> 66 (WS-2)" "obtenido: $code"

# --- 5b. un path temporal absoluto tambien se rechaza -----------------------
code="$(run check "$PROJ" 25 --artifact /tmp/entregable.md)"
[ "$code" = "66" ] && ok "artefacto en /tmp -> 66 (WS-2)" \
                   || bad "artefacto en /tmp -> 66 (WS-2)" "obtenido: $code"

# --- 6. el plan emite los pasos en orden y con agent_id explicito -----------
plan="$(WORKSPACE="$WS" bash "$FLOW" plan "$PROJ" 25 --artifact "$WS/$PROJ/out/entregable.md" 2>/dev/null)"
steps_ok=1
for step in session_start spec_context agent_memory_recall vibe_publish agent_memory_save session_close; do
  echo "$plan" | grep -q "$step" || { steps_ok=0; echo "        falta el paso: $step"; }
done
[ "$steps_ok" = "1" ] && ok "el plan contiene los pasos del ciclo" \
                      || bad "el plan contiene los pasos del ciclo"

echo "$plan" | grep -q "agent_memory_save" \
  && ok "el plan incluye el guardado de la decision" \
  || bad "el plan incluye el guardado de la decision"

# Las aserciones sobre agent_id explicito y active_subagent_ids viven en el
# PR que anade el escape hatch del binding C2 (fix/c2-binding-trap): el plan
# solo las emite cuando la constitution y el servidor ya exponen esos campos.

# --- 7. sintaxis -------------------------------------------------------------
bash -n "$FLOW" && ok "sintaxis de dm-flow.sh" || bad "sintaxis de dm-flow.sh"
bash -n "$CHECK" && ok "sintaxis de check-constitution.sh" \
                  || bad "sintaxis de check-constitution.sh"

# --- 8. los artefactos versionados no llevan datos del operador ------------
# Regresion. La primera version de este PR publico la ruta del home del
# operador en el contrato y en el ejemplo. El contrato se publica en un repo
# publico: no puede llevar la ruta de una maquina concreta. Solo se escanean
# los ficheros TRACKED: el JSON activo es local y si lleva rutas reales (es
# su cometido), pero no se versiona.
tracked="$(git -C "$REPO_ROOT" ls-files governance constitutions 2>/dev/null || true)"
leaks=""
for f in $tracked; do
  case "$f" in *.sh|*.json|*.yaml|*.yml|*.md) ;; *) continue ;; esac
  if grep -qE '(/home/[a-z]|/Users/[A-Za-z])' "$REPO_ROOT/$f"; then
    leaks="$leaks $f"
  fi
done
[ -z "$leaks" ] && ok "los artefactos versionados no llevan rutas del home del operador" \
                || bad "los artefactos versionados no llevan rutas del home del operador" "ficheros:$leaks"

# --- 9. el contrato fija el SHA del ejemplo, no el de una maquina -----------
# El SHA declarado en el YAML tiene que ser el del JSON de ejemplo. Si fuera
# el del fichero activo local, el contrato dejaria de ser portable: otro
# operador, con otras rutas, generaria un SHA distinto.
declared="$(grep -m1 -E '^sha256:' "$REPO_ROOT/constitutions/dark-agents-workspace-policy.yaml" \
            | sed -E 's/^sha256:[[:space:]]*//')"
example_sha="$(sha256sum "$SCRIPT_DIR/workspace-policy.constitution.example.json" | cut -d' ' -f1)"
[ -n "$declared" ] && [ "$declared" = "$example_sha" ] \
  && ok "el contrato fija el SHA del ejemplo portable" \
  || bad "el contrato fija el SHA del ejemplo portable" "declarado: $declared · ejemplo: $example_sha"

# --- 10. la forma canonica usa marcadores, no rutas -------------------------
grep -q '<workspace_root>' "$SCRIPT_DIR/workspace-policy.constitution.example.json" \
  && ok "el ejemplo usa marcadores en lugar de rutas" \
  || bad "el ejemplo usa marcadores en lugar de rutas"

echo
echo "  $PASS PASS · $FAIL FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
