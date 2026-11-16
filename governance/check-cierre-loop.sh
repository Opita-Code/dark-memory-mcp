#!/usr/bin/env bash
# governance/check-cierre-loop.sh — WS-7: el hallazgo se cablea al juez y a opencode.
#
# POR QUE EXISTE
#
# MEDIDO 2026-11-15: se implemento y se verifico con tests duros el arreglo del
# bug de cuota por region de [NEIGHBORING-SCRAPER], y NO se cableo al juez ni a
# opencode. Se cerro el trabajo de codigo y se dejo el hallazgo sin veredicto
# ni rastro durable. El trabajo propio con sus propios tests NO es la
# verificacion: el juez es quien valida el artefacto, y opencode es quien se
# queda con el estado.
#
# Un hallazgo medido no esta entregado hasta que las TRES cosas estan hechas:
#
#   (a) el artefacto, publicado para que el juez lo evalue,
#   (b) el rastro durable en la memoria de dark-memory, con el id,
#   (c) el commit local que lo deja en el historial.
#
# Este guard comprueba (a) y (b) contra el estado real de la base, y (c) contra
# el arbol de git. No es una frase: es un check. Si la regla no tiene un
# check, se anade el check o se declara que no se aplica.
#
# DOS RUTAS, Y POR QUE HAY DOS
#
# MEDIDO 2026-11-16: hay DOS formas de pedir un veredicto al juez, y dejan el
# rastro en tablas DISTINTAS:
#
#   vibe_publish       -> vibe_artifacts + vibe_drift_reports
#   dark_memory_judge  -> sdd_evaluations
#
# Antes este guard solo conocia la primera. MEDIDO el 2026-11-16: en el repo
# opita-code la ruta de juicio es `dark_memory_judge` (una afirmacion por
# parte, que es lo que hace falta para juzgar un artefacto partido por
# conceptos), asi que un juicio HECHO y con veredicto daba 65 ("no se
# publico") al pasarle este guard. Un guard que miente es peor que no tener
# guard: por eso la segunda ruta se anade en vez de dejar que cada repo
# mantenimiento su propio cierre a mano.
#
# EL ANCLA DE LA SEGUNDA RUTA ES EL SHA de los bytes juzgados, no el id de la
# evaluacion. El sha es lo que hace VALIDO un veredicto: si el fichero
# cambia, el sha cambia y el veredicto caduca. El id de la evaluacion
# identifica UNA llamada del juez, no el artefacto: con consenso 3 hay tres
# ids para la misma parte, y el veredicto de la parte es el agregado de las
# tres.
#
# USO
#   governance/check-cierre-loop.sh <project_id> <artefacto> [drift_id] [--ruta <ruta>]
#
#   <artefacto> = <id>        la ruta de vibe_publish (igual que hasta ahora)
#               = vibe:<id>   lo mismo, dicho de forma explicita
#               = sha:<hex>   la ruta del juez directo (sdd_evaluations)
#
#   --ruta <ruta>  la ruta del artefacto dentro del repo. En la ruta sha es
#                   OBLIGATORIA: es lo que se busca en los commits. En la ruta
#                   vibe sustituye a la que trae el artefacto.
#
# CODIGOS: 0 cerrado · 64 uso · 65 sin artefacto publicado · 66 sin rastro en
# memoria · 67 sin commit · 68 veredicto ausente (no es fallo: se declara)

set -uo pipefail

DB="${DARK_DB:-$HOME/.local/share/dark-memory/memoria.db}"

usage() {
  echo "uso: check-cierre-loop.sh <project_id> <artefacto> [drift_id] [--ruta <ruta>]" >&2
  echo "  <artefacto> = <id> | vibe:<id> | sha:<hex>" >&2
  exit 64
}

# ── argumentos: posicionales y la opcion --ruta ────────────────────────────
POS=()
RUTA=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ruta)
      [[ $# -ge 2 ]] || usage
      RUTA="$2"; shift 2 ;;
    --ruta=*)
      RUTA="${1#--ruta=}"; shift ;;
    *)
      POS+=("$1"); shift ;;
  esac
done

PROJECT="${POS[0]:-}"
ARTIFACT="${POS[1]:-}"
DRIFT="${POS[2]:-}"

[[ -n "$PROJECT" && -n "$ARTIFACT" ]] || usage
# El proyecto y el hash van a una consulta: solo se admiten los caracteres
# que un identificador puede llevar. No es paranoia: es no construir SQL con
# lo que viene de la linea de ordenes.
[[ "$PROJECT" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "check-cierre-loop: proyecto con caracteres no admitidos" >&2; exit 64; }
[[ -f "$DB" ]] || { echo "check-cierre-loop: no existe la base: $DB" >&2; exit 64; }

# ── la ruta: vibe_publish o el juez directo ─────────────────────────────────
ROUTE="vibe"
SHA=""
case "$ARTIFACT" in
  vibe:*) ARTIFACT="${ARTIFACT#vibe:}" ;;
  sha:*)
    ROUTE="sha"
    SHA="${ARTIFACT#sha:}"
    # 16 hex es el minimo con que un prefijo no se confunde con otra cosa.
    [[ "$SHA" =~ ^[0-9a-fA-F]{16,64}$ ]] || { echo "check-cierre-loop: sha invalido (16 a 64 hex)" >&2; exit 64; }
    ;;
esac

# El acceso a la base es de solo lectura: este guard no escribe.
have_sqlite() {
  command -v sqlite3 >/dev/null 2>&1 && return 0
  command -v bun >/dev/null 2>&1 && return 0
  return 1
}
have_sqlite || { echo "check-cierre-loop: se necesita sqlite3 o bun" >&2; exit 64; }

# El lector sin sqlite3: un script bun temporal, que recibe la base y la
# consulta como argumentos (bun -e no los propaga de forma fiable).
BUN_SQL=""
if ! command -v sqlite3 >/dev/null 2>&1; then
  BUN_SQL="$(mktemp -t cierre-loop-XXXXXX.mjs)"
  cat > "$BUN_SQL" <<'BUNEOF'
import { Database } from "bun:sqlite";
const db = new Database(process.argv[2], { readonly: true });
const rows = db.query(process.argv[3]).all();
for (const r of rows) console.log(Object.values(r).join("|"));
BUNEOF
  trap 'rm -f "$BUN_SQL"' EXIT
fi

q() {
  # q <sql>
  if [[ -n "$BUN_SQL" ]]; then
    bun "$BUN_SQL" "$DB" "$1"
  else
    sqlite3 -readonly "$DB" "$1"
  fi
}

fail() { echo "check-cierre-loop: $1" >&2; exit "$2"; }

# ── el veredicto, en las dos rutas ─────────────────────────────────────────
# En la ruta vibe el veredicto vive en vibe_drift_reports.verdict.
# En la ruta sha vive dentro de verdict_json (la columna es un JSON), asi que
# se extrae con json_extract. Si el SQLite no tiene JSON1, se cae al texto
# crudo: el veredicto se busca con sed dentro del json.
VERDICT="(sin veredicto)"
CONFIANZA=""
RAZON=""
MUESTRAS=0
VERDICT_ANTES=""
OTROS=0

# ── (a) el artefacto existe y esta publicado ─────────────────────────────
ART_ROW=""
ART_TYPE=""
ART_URL=""
EVAL_IDS=()

if [[ "$ROUTE" == "vibe" ]]; then
  ART_ROW="$(q "SELECT id, artifact_type, artifact_url FROM vibe_artifacts WHERE id = '$ARTIFACT'")"
  [[ -n "$ART_ROW" ]] || fail "el artefacto $ARTIFACT no esta en vibe_artifacts: no se publico" 65
  ART_TYPE="$(echo "$ART_ROW" | cut -d'|' -f2)"
  ART_URL="$(echo "$ART_ROW" | cut -d'|' -f3)"
  echo "artefacto   : $ARTIFACT ($ART_TYPE)"
  echo "             $ART_URL"
else
  # Las muestras del mismo sha: una por llamada del juez. Con consenso 3 son
  # tres filas con el mismo sha.
  EVAL_ROWS="$(q "SELECT id, json_extract(verdict_json, '\$.verdict'), confidence, artifact_path FROM sdd_evaluations WHERE project_id = '$PROJECT' AND artifact_sha256 LIKE '%$SHA%' ORDER BY id")"
  if [[ -z "$EVAL_ROWS" ]]; then
    # Puede que el SQLite no tenga JSON1. Se prueba con el texto crudo, del
    # que se saca el veredicto con sed. El id es el primer campo y el json el
    # resto, para que un pipe dentro del texto no parta la fila.
    RAW="$(q "SELECT id || '|' || verdict_json || '|' || COALESCE(artifact_path, '') FROM sdd_evaluations WHERE project_id = '$PROJECT' AND artifact_sha256 LIKE '%$SHA%' ORDER BY id")"
    [[ -n "$RAW" ]] || fail "no hay ninguna evaluacion con el sha $SHA en el proyecto $PROJECT: no se juzgo" 65
    EVAL_ROWS=""
    while IFS= read -r fila; do
      [[ -z "$fila" ]] && continue
      eid="$(echo "$fila" | cut -d'|' -f1)"
      json="$(echo "$fila" | cut -d'|' -f2-)"
      v="$(printf '%s' "$json" | sed -n 's/.*"verdict"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
      [[ -n "$v" ]] || v="(sin veredicto)"
      EVAL_ROWS+="$eid|$v||"$'\n'
    done <<< "$RAW"
  fi

  [[ -n "$EVAL_ROWS" ]] || fail "no hay ninguna evaluacion con el sha $SHA en el proyecto $PROJECT: no se juzgo" 65

  echo "artefacto   : sha:$SHA"
  OTROS=0
  while IFS='|' read -r eid v conf apath; do
    [[ -z "$eid" ]] && continue
    EVAL_IDS+=("$eid")
    MUESTRAS=$((MUESTRAS + 1))
    [[ -n "$apath" && -z "${ART_URL:-}" ]] && ART_URL="$apath"
    echo "             evaluacion $eid  $v  conf=$conf"
    # EL VEREDICTO VIGENTE ES EL ULTIMO, y el guard imprime el historico
    # entero. MEDIDO 2026-11-16: un mismo sha puede tener muchas
    # evaluaciones a lo largo del dia (el mismo contenido se rejuzga), y si
    # se agregaran todas, un veredicto viejo poisoningaria al nuevo para
    # siempre: un documento rejuzgado nunca podria cambiar de veredicto. La
    # ruta vibe hace lo mismo (el ultimo drift report del artefacto).
    if [[ -n "$VERDICT_ANTES" && "$v" != "$VERDICT_ANTES" ]]; then OTROS=$((OTROS + 1)); fi
    VERDICT_ANTES="$v"
    VERDICT="$v"
    CONFIANZA="$conf"
  done <<< "$EVAL_ROWS"

  if [[ -z "$RUTA" ]]; then
    fail "la ruta sha necesita --ruta <ruta>: el commit se busca por la ruta del artefacto en el repo" 64
  fi
  echo "veredicto   : $VERDICT (${MUESTRAS} muestra(s), confianza ${CONFIANZA:-s/d})"
  if [[ "$OTROS" -gt 0 ]]; then
    # No se oculta nada: si antes hubo otro veredicto para estos mismos
    # bytes, se dice. El vigente es el ultimo; el historico, arriba.
    echo "             nota: $OTROS muestra(s) dio un veredicto distinto (historico, arriba)"
  fi
fi

# ── (b) el veredicto del juez (ruta vibe) ──────────────────────────────────
if [[ "$ROUTE" == "vibe" ]]; then
  DRIFT_ID="${DRIFT:-$(q "SELECT id FROM vibe_drift_reports WHERE artifact_id = '$ARTIFACT' ORDER BY id DESC LIMIT 1")}"
  if [[ -n "$DRIFT_ID" ]]; then
    VERDICT="$(q "SELECT verdict FROM vibe_drift_reports WHERE id = '$DRIFT_ID'")"
    REASON="$(q "SELECT judge_reasoning FROM vibe_drift_reports WHERE id = '$DRIFT_ID'")"
    echo "drift       : $DRIFT_ID  verdict=$VERDICT"
    echo "             ${REASON:0:200}"
  fi
fi

# ── (c) el rastro durable en la memoria ───────────────────────────────────
if [[ "$ROUTE" == "vibe" ]]; then
  MEM="$(q "SELECT id FROM agent_memory WHERE project_id = '$PROJECT' AND (content LIKE '%$ARTIFACT%' OR content LIKE '%artifact#$ARTIFACT%' OR tags LIKE '%$ARTIFACT%') ORDER BY id DESC LIMIT 1")"
  [[ -n "$MEM" ]] || fail "no hay memoria en el proyecto $PROJECT que cite el artefacto $ARTIFACT: el hallazgo no tiene rastro durable" 66
  echo "memoria     : #${MEM%%|*} (cita el artefacto $ARTIFACT)"
else
  # La memoria puede citar el sha (la identidad de los bytes) o el id de
  # alguna de las evaluaciones, con su marca "#". Un numero suelto NO cuenta:
  # MEDIDO 2026-11-15, buscar el numero sin marca daba por bueno un cierre
  # que no estaba.
  COND="content LIKE '%$SHA%' OR tags LIKE '%$SHA%'"
  for eid in "${EVAL_IDS[@]:-}"; do
    [[ -z "$eid" ]] && continue
    COND="$COND OR content LIKE '%#$eid%' OR tags LIKE '%#$eid%'"
  done
  MEM="$(q "SELECT id FROM agent_memory WHERE project_id = '$PROJECT' AND ($COND) ORDER BY id DESC LIMIT 1")"
  [[ -n "$MEM" ]] || fail "no hay memoria en el proyecto $PROJECT que cite el sha $SHA (ni el id de sus evaluaciones): el hallazgo no tiene rastro durable" 66
  echo "memoria     : #${MEM%%|*} (cita el sha o la evaluacion)"
fi

# ── (d) el commit local ───────────────────────────────────────────────────
# El commit se busca por la RUTA del artefacto (lo durable) o por su id.
#
# MEDIDO 2026-11-15: la busqueda por el numero del artefacto SUELTO ("179")
# casa con cualquier commit que contenga ese numero en su mensaje (un
# contador, una fecha, un id). Para el artefacto 179 devolvio un commit que
# no era el suyo, y el guard daba por bueno un cierre que no estaba.
# Por eso: `--fixed-strings` (sin regex) y marcadores con PALABRA.
REPO_ROOT="${DS_REPO_ROOT:-}"
ART_PATH="${RUTA:-${ART_URL#file://}}"

# Si la raiz del repo no se dice, se deduce del propio artefacto: la ruta del
# juicio apunta al interior del repo, y la raiz es lo que va antes del
# directorio de trabajo del juez. MEDIDO 2026-11-16 en [NEIGHBORING-SCRAPER]:
# el artefacto juzgado esta en <repo>/.partes-juez/<documento>/, y el
# commit se busca en <repo>. Sin esto, el guard tendria que adivinar el repo,
# y un guard que adivina donde buscar es un guard que puede dar por bueno el
# commit de otro repo.
if [[ -z "$REPO_ROOT" && -n "$ART_URL" && "$ART_URL" == *"/.partes-juez/"* ]]; then
  REPO_ROOT="${ART_URL%%/.partes-juez/*}"
fi
# Y si tampoco se deduce, el repo donde se esta: el guard se usa dentro del
# proyecto, asi que la raiz de git de aqui es la correcta. MEDIDO 2026-11-16:
# antes estaba hardcodeada la ruta de un proyecto privado, que es una fuga en
# un repo publico y ademas hace que el guard no sirva en ningun otro sitio.
if [[ -z "$REPO_ROOT" ]]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
fi
[[ -z "$REPO_ROOT" ]] && REPO_ROOT="$PWD"

if [[ -d "$REPO_ROOT/.git" ]]; then
  HIT=""
  # 1) la ruta del artefacto: absoluta, relativa al repo, o solo el nombre.
  for needle in "$ART_PATH" "${ART_PATH#"$REPO_ROOT"/}" "$(basename "$ART_PATH")"; do
    [[ -z "$needle" ]] && continue
    HIT="$(git -C "$REPO_ROOT" log --oneline -1 --fixed-strings --grep="$needle" 2>/dev/null)"
    [[ -n "$HIT" ]] && break
  done
  # 2) el marcador explicito: "artefacto <id>" o "evaluacion <id>". Nunca el
  #    numero suelto.
  if [[ -z "$HIT" ]]; then
    if [[ "$ROUTE" == "vibe" ]]; then
      HIT="$(git -C "$REPO_ROOT" log --oneline -1 --fixed-strings --grep="artefacto $ARTIFACT" 2>/dev/null)"
    else
      for eid in "${EVAL_IDS[@]:-}"; do
        [[ -z "$eid" ]] && continue
        HIT="$(git -C "$REPO_ROOT" log --oneline -1 --fixed-strings --grep="evaluacion $eid" 2>/dev/null)"
        [[ -n "$HIT" ]] && break
      done
    fi
  fi
  if [[ -n "$HIT" ]]; then
    echo "commit      : $HIT"
  else
    echo "commit      : (ninguno cita $ART_PATH${ARTIFACT:+ ni 'artefacto $ARTIFACT'})" >&2
    fail "el artefacto esta publicado y con rastro en memoria, pero ningun commit lo cita: el cierre del loop exige el commit" 67
  fi
else
  echo "commit      : (repo no encontrado en $REPO_ROOT: se omite esta comprobacion)"
fi

# ── veredicto ─────────────────────────────────────────────────────────────
case "$VERDICT" in
  aligned)
    echo "CIERRE: completo (artefacto publicado, memoria, commit, veredicto aligned)"
    ;;
  needs_human|unverified|"")
    # No es un fallo del loop: el loop esta cableado, pero el juez no pudo
    # evaluar. Se DECLARA, no se disimula (WS-8).
    echo "CIERRE: cableado pero SIN VEREDICTO DEL JUEZ ($VERDICT)."
    echo "       El artefacto, el rastro y el commit estan. El veredicto queda pendiente:"
    echo "       reintentar la publicacion cuando el juez tenga proveedor util."
    exit 68
    ;;
  *)
    echo "CIERRE: completo (artefacto publicado, memoria, commit, veredicto $VERDICT)"
    ;;
esac

exit 0
