#!/usr/bin/env bash
# scripts/lint-no-private-projects.sh
#
# Hard internal rule: dark-memory-mcp MUST NOT reference any private
# project by name in any tracked TEXT file. Private project names
# live in the operator's private index (not in this repo) and naming
# them in public artifacts is a hard leak.
#
# This script iterates every tracked file and grep-matches each
# BLOCKLIST term against the file CONTENT (NOT the file name).
# Wire it into CI and/or run it locally before `git commit` so
# a leak is rejected at the gate, not after the fact.
#
# Add new private project names to BLOCKLIST as the org's
# registry grows. Keep BLOCKLIST sorted for review hygiene.
#
# ---------------------------------------------------------------------------
# LA SEGUNDA REGLA: LAS RUTAS DEL OPERADOR (2026-11-14).
#
# El bloque de arriba filtraba los NOMBRES de los proyectos privados. Las
# RUTAS del home del operador no las cubria nadie, y en un repo publico son
# una fuga igual: dicen como se llama el operador en su maquina y donde
# tiene sus cosas.
#
# MEDIDO el 2026-11-14: 41 ocurrencias en 25 ficheros. La mayoria eran
# ejemplos en la documentacion, pero cuatro eran CODIGO: scripts que
# tienen hardcodeada la ruta del operador y que, por eso, no funcionan en
# ninguna otra maquina. Por eso la regla no es solo "no lo escribas": es
# "si lo necesitas, derivalo de donde estas".
#
# QUE SE CONSIDERA UN PLACEHOLDER. Un segmento de usuario con angulares
# (`<name>`) es el placeholder del repo, y lo usan tanto la documentacion
# como los fixtures de test. Los nombres genericos (you, user, name,
# operator, example) tambien valen. Cualquier otra cosa es un nombre real
# y se rechaza.
#
# LO QUE ESTE SCRIPT NO HACE. No reescribe: encuentra y dice. Y no mira
# binarios: un blob vendorizado puede traer una ruta de la maquina donde
# se compilo, y eso no es del contenido de este repo.

set -eu

# All private dark-* siblings + the operator's private product repos.
#
# MEDIDO el 2026-11-14: la lista estaba incompleta. Faltaban los repos
# privados del operador que NO son hermanos `dark-*` (que es donde estaban
# los nombres que se colaron), y faltaba el nombre NUEVO del companion de
# navegador: el repo se renombro de `dark-copilot` a `dark-copilot-private`
# precisamente para que el viejo no se filtrara, pero la lista se quedo con
# el viejo, y las 17 menciones del nombre viejo se quedaron en el repo
# publico. El lint no las vio porque el filtro de SKIP_DIRS usaba `.git`
# como REGEX (la `.` encaja con cualquier caracter), y una coincidencia de
# mas vacio la lista de filtradas. Un guard que no filtra no vigila.
#
# `dark-research-mcp` NO esta aqui, y es a proposito: su nombre es publico
# (lo publica el propio paquete npm del servidor, y aparece en 229 sitios
# de este repo). Lo que no es publico es su CODIGO.
BLOCKLIST=(
  dark-harvest
  dark-scrapper
  dark-scraper
  scrappingkeys
  scrapping-keys
  dark-classify
  dark-verify
  dark-vault
  dark-rotate
  dark-copilot
  dark-copilot-v3
  dark-copilot-private
  opita-growth
  opita-os-engine
  opita-barber
  opita-vapes
  opita-vapes-web
  client-giovanni
  dark-dev
  project-creator-tool
  maxim-operis-platform
  v2-cvtools
  wifi-redteam-lab
  python-arboles
)

# Directories that NEVER carry user-authored prose. We skip these
# to avoid false positives (binary blobs, vendored third-party
# code, generated artifacts). Keep this list MINIMAL — only add
# a path here after a confirmed false positive.
SKIP_DIRS=(
  .git
  node_modules
  vendor
  dist
  build
  scripts/lint-no-private-projects.sh
  scripts/lint-no-private-projects.ps1
)

EXIT=0
for term in "${BLOCKLIST[@]}"; do
  # git grep -F: literal-string match (not regex). --name-only: print
  # only filenames.
  matches=$(git grep -l -F "$term" 2>/dev/null || true)
  if [ -n "$matches" ]; then
    # Filter out paths under SKIP_DIRS.
    #
    # LOS SKIP_DIRS SON RUTAS, NO REGEXES. Antes se usaban como regex, y
    # `.git` como regex encaja con cualquier caracter seguido de "git":
    # una coincidencia de mas vaciaba la lista de filtradas y el lint
    # pasaba en verde con las fugas dentro. MEDIDO el 2026-11-14: con el
    # filtro roto, el lint daba OK con 11 ficheros que nombraban un
    # proyecto privado.
    filtrados=""
    while IFS= read -r ruta; do
      [ -z "$ruta" ] && continue
      saltar=0
      for skip in "${SKIP_DIRS[@]}"; do
        case "$ruta" in
          "$skip"|"$skip"/*) saltar=1; break ;;
        esac
      done
      [ "$saltar" -eq 0 ] && filtrados="${filtrados}${ruta}
"
    done <<EOF
$matches
EOF
    if [ -n "$filtrados" ]; then
      echo "LEAK: forbidden project name '$term' found in:"
      printf '%s' "$filtrados" | sed 's/^/  /'
      EXIT=1
    fi
  fi
done

# ---------------------------------------------------------------------------
# LA SEGUNDA REGLA: las rutas del home.
#
# Se busca cualquier home con un usuario que NO sea un placeholder. El
# placeholder del repo es `<name>`, y los nombres genericos tambien valen.
#
# Se aplica sobre los ficheros de TEXTO que git rastrea. Los binarios se
# dejan: un blob vendorizado puede traer la ruta de la maquina donde se
# compilo, y eso no es contenido de este repo.
PLACEHOLDERS='^(<[^>]*>|you|user|name|operator|example|someone|default|USER|ROOT|root)$'

# El segmento de usuario no puede empezar por `[`: eso es una clase de
# caracteres de una REGEX, no un nombre. El propio gate de governance
# busca rutas con `/home/[a-z]`, y ese patron no es una fuga.
rutas=$(git grep -I -n -E \
  '(/home/[^/[:space:]]+|/Users/[^/[:space:]]+|[A-Za-z]:[\\/]Users[\\/][^\\/[:space:]]+)' \
  -- '*.go' '*.md' '*.sh' '*.json' '*.yaml' '*.yml' '*.toml' '*.txt' '*.mjs' '*.ts' '*.js' \
  2>/dev/null || true)

if [ -n "$rutas" ]; then
  filtradas=""
  while IFS= read -r linea; do
    [ -z "$linea" ] && continue
    fichero="${linea%%:*}"
    # los ficheros que el propio script necesita tocar se saltan
    case "$fichero" in
      scripts/lint-no-private-projects.sh|scripts/lint-no-private-projects.ps1) continue ;;
    esac
    usuario=$(printf '%s' "$linea" \
      | grep -oE '(/home/|/Users/|[A-Za-z]:[\\/]Users[\\/])[^/\\[:space:]\[]+' \
      | head -1 \
      | sed -E 's#^(/home/|/Users/|[A-Za-z]:[\\/]Users[\\/])##; s#[^A-Za-z0-9_<>.-]+$##')
    [ -z "$usuario" ] && continue
    if ! printf '%s' "$usuario" | grep -qiE "$PLACEHOLDERS"; then
      filtradas="${filtradas}${linea}
"
    fi
  done <<EOF
$rutas
EOF
  if [ -n "$filtradas" ]; then
    echo "LEAK: una ruta del home del operador en un fichero publico:"
    printf '%s' "$filtradas" | sed 's/^/  /'
    EXIT=1
  fi
fi

if [ "$EXIT" -ne 0 ]; then
  echo
  echo "Hard rule: do not mention private projects in public artifacts."
  echo "Replace with a generic placeholder ([FUTURE-MCP-N] / [NEIGHBORING-SCRAPER] / etc.)"
  echo "or move the detail to the operator's private index."
  echo
  echo "Y las rutas del home: usa el placeholder <name>, y si el fichero es"
  echo "un script, deriva la ruta de la ubicacion del propio script:"
  echo '  HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"'
  exit 1
fi

echo "OK: no private project names and no operator home paths in tracked files."
exit 0
