#!/usr/bin/env bash
# governance/check-cierre-loop.sh — WS-7: el hallazgo se cablea al juez y a opencode.
#
# POR QUE EXISTE
#
# MEDIDO 2026-11-15: se implemento y se verifico con tests duros el arreglo del
# bug de cuota por region de dark-scrapper-ts, y NO se cableo al juez ni a
# opencode. Se cerro el trabajo de codigo y se dejo el hallazgo sin veredicto
# ni rastro durable. El trabajo propio con sus propios tests NO es la
# verificacion: el juez es quien valida el artefacto, y opencode es quien se
# queda con el estado.
#
# Un hallazgo medido no esta entregado hasta que las TRES cosas estan hechas:
#
#   (a) el artefacto, publicado para que el juez lo evalue (vibe_publish),
#   (b) el rastro durable en la memoria de dark-memory, con el id,
#   (c) el commit local que lo deja en el historial.
#
# Este guard comprueba (a) y (b) contra el estado real de la base, y (c) contra
# el arbol de git. No es una frase: es un check. Si la regla no tiene un
# check, se anade el check o se declara que no se aplica.
#
# USO
#   governance/check-cierre-loop.sh <project_id> <artifact_id> [drift_id]
#
# CODIGOS: 0 cerrado · 64 uso · 65 sin artefacto publicado · 66 sin rastro en
# memoria · 67 sin commit · 68 veredicto ausente (no es fallo: se declara)

set -uo pipefail

DB="${DARK_DB:-$HOME/.local/share/dark-memory/memoria.db}"
PROJECT="${1:-}"
ARTIFACT="${2:-}"
DRIFT="${3:-}"

usage() {
  echo "uso: check-cierre-loop.sh <project_id> <artifact_id> [drift_id]" >&2
  exit 64
}

[[ -n "$PROJECT" && -n "$ARTIFACT" ]] || usage
[[ -f "$DB" ]] || { echo "check-cierre-loop: no existe la base: $DB" >&2; exit 64; }

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

# ── (a) el artefacto existe y esta publicado ─────────────────────────────
ART_ROW="$(q "SELECT id, artifact_type, artifact_url FROM vibe_artifacts WHERE id = '$ARTIFACT'")"
[[ -n "$ART_ROW" ]] || fail "el artefacto $ARTIFACT no esta en vibe_artifacts: no se publico" 65

ART_TYPE="$(echo "$ART_ROW" | cut -d'|' -f2)"
ART_URL="$(echo "$ART_ROW" | cut -d'|' -f3)"
echo "artefacto   : $ARTIFACT ($ART_TYPE)"
echo "             $ART_URL"

# ── (b) el veredicto del juez ─────────────────────────────────────────────
DRIFT_ID="${DRIFT:-$(q "SELECT id FROM vibe_drift_reports WHERE artifact_id = '$ARTIFACT' ORDER BY id DESC LIMIT 1")}"
VERDICT="(sin veredicto)"
if [[ -n "$DRIFT_ID" ]]; then
  # El esquema real: vibe_drift_reports tiene verdict y judge_reasoning.
  # No hay columna confidence: la confianza vive en el veredicto JSON.
  VERDICT="$(q "SELECT verdict FROM vibe_drift_reports WHERE id = '$DRIFT_ID'")"
  REASON="$(q "SELECT judge_reasoning FROM vibe_drift_reports WHERE id = '$DRIFT_ID'")"
  echo "drift       : $DRIFT_ID  verdict=$VERDICT"
  echo "             ${REASON:0:200}"
fi

# ── (c) el rastro durable en la memoria ───────────────────────────────────
MEM="$(q "SELECT id FROM agent_memory WHERE project_id = '$PROJECT' AND (content LIKE '%$ARTIFACT%' OR content LIKE '%artifact#$ARTIFACT%' OR tags LIKE '%$ARTIFACT%') ORDER BY id DESC LIMIT 1")"
[[ -n "$MEM" ]] || fail "no hay memoria en el proyecto $PROJECT que cite el artefacto $ARTIFACT: el hallazgo no tiene rastro durable" 66
echo "memoria     : #${MEM%%|*} (cita el artefacto $ARTIFACT)"

# ── (d) el commit local ───────────────────────────────────────────────────
# El commit se busca por la RUTA del artefacto (lo durable) o por su id.
REPO_ROOT="${DS_REPO_ROOT:-$HOME/workspace/dark-scrapper-ts}"
ART_PATH="${ART_URL#file://}"
if [[ -d "$REPO_ROOT/.git" ]]; then
  HIT="$(git -C "$REPO_ROOT" log --oneline -1 --grep="$ART_PATH" 2>/dev/null)"
  [[ -z "$HIT" ]] && HIT="$(git -C "$REPO_ROOT" log --oneline -1 --grep="$ARTIFACT" 2>/dev/null)"
  [[ -z "$HIT" ]] && HIT="$(git -C "$REPO_ROOT" log --oneline -1 --grep="artefacto $ARTIFACT" 2>/dev/null)"
  if [[ -n "$HIT" ]]; then
    echo "commit      : $HIT"
  else
    echo "commit      : (ninguno cita $ART_PATH ni el artefacto $ARTIFACT)" >&2
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
    echo "       reintentar vibe_publish cuando el juez tenga proveedor util."
    exit 68
    ;;
  *)
    echo "CIERRE: completo (artefacto publicado, memoria, commit, veredicto $VERDICT)"
    ;;
esac

exit 0
