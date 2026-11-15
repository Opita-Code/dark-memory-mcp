#!/usr/bin/env bash
# governance/test-cierre-loop-guard.sh — pruebas del guard de cierre de loop.
#
# QUE COMPRUEBA
#   1. Un artefacto sin publicar -> rc 65 (no se cerró nada).
#   2. Un artefacto publicado sin rastro en memoria -> rc 66.
#   3. Un artefacto publicado con rastro pero sin commit -> rc 67.
#   4. Las tres partes en su sitio, sin veredicto -> rc 68 (ausencia de
#      veredicto, que NO es un fallo del cierre).
#   5. Las tres partes con veredicto aligned -> rc 0.
#
# La prueba usa una base y un repo TEMPORALES: no toca la base real ni el
# repo real, y no depende de red.
#
# CODIGOS: 0 todas pasan · 1 alguna falla

set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$DIR/check-cierre-loop.sh"

[[ -x "$GUARD" ]] || { echo "test-cierre-loop-guard: falta $GUARD" >&2; exit 1; }

command -v bun >/dev/null 2>&1 || { echo "test-cierre-loop-guard: se necesita bun" >&2; exit 1; }

TMP="$(mktemp -d -t cierre-loop-test-XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

DB="$TMP/memoria.db"
REPO="$TMP/repo"
mkdir -p "$REPO"

fails=0
check() {
  local name="$1" want="$2" got="$3"
  if [[ "$got" == "$want" ]]; then
    printf 'ok   %s (exit %s)\n' "$name" "$got"
  else
    printf 'FAIL %s (exit %s, esperado %s)\n' "$name" "$got" "$want"
    fails=$((fails + 1))
  fi
}

# ── la base de pruebas ─────────────────────────────────────────────────────
DB_PATH="$DB" bun -e '
import { Database } from "bun:sqlite";
const db = new Database(process.env.DB_PATH);
db.exec(`
CREATE TABLE vibe_artifacts (id INTEGER PRIMARY KEY, session_id TEXT, vibe_case TEXT,
  spec_id INTEGER, artifact_url TEXT, artifact_type TEXT, created_at TEXT, project_id TEXT);
CREATE TABLE vibe_drift_reports (id INTEGER PRIMARY KEY, artifact_id INTEGER, spec_id INTEGER,
  verdict TEXT, judge_reasoning TEXT, created_at TEXT, project_id TEXT);
CREATE TABLE agent_memory (id INTEGER PRIMARY KEY, project_id TEXT, operator TEXT, kind TEXT,
  title TEXT, content TEXT, tags TEXT, created_at TEXT);
`);
const now = new Date().toISOString();
const art = db.query(`INSERT INTO vibe_artifacts
  (id, session_id, vibe_case, spec_id, artifact_url, artifact_type, created_at, project_id)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`);
art.run(900, "s", "C1", 1, "file:///tmp/x.diff", "code", now, "p");
art.run(901, "s", "C1", 1, "file:///tmp/y.diff", "code", now, "p");
art.run(902, "s", "C1", 1, "file:///tmp/z.diff", "code", now, "p");
art.run(903, "s", "C1", 1, "file:///tmp/w.diff", "code", now, "p");
const drift = db.query(`INSERT INTO vibe_drift_reports
  (id, artifact_id, spec_id, verdict, judge_reasoning, created_at, project_id)
  VALUES (?, ?, ?, ?, ?, ?, ?)`);
drift.run(901, 901, 1, "aligned", "el arreglo cumple la promesa", now, "p");
drift.run(902, 902, 1, "needs_human", "nli: provider rate limited", now, "p");
db.query(`INSERT INTO agent_memory
  (id, project_id, operator, kind, title, content, tags, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
  .run(700, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 900", "900", now);
db.query(`INSERT INTO agent_memory
  (id, project_id, operator, kind, title, content, tags, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
  .run(701, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 901", "901", now);
db.query(`INSERT INTO agent_memory
  (id, project_id, operator, kind, title, content, tags, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
  .run(702, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 902", "902", now);
' || { echo "test-cierre-loop-guard: no se pudo crear la base de pruebas" >&2; exit 1; }

# ── un repo con commits que citan las rutas ───────────────────────────────
git -C "$REPO" init -q 2>/dev/null
git -C "$REPO" config user.email "test@local"
git -C "$REPO" config user.name "test"
for f in y.diff z.diff w.diff; do
  echo "diff" > "$REPO/$f"
  git -C "$REPO" add "$f" >/dev/null 2>&1
  git -C "$REPO" commit -q -m "cambio que cita /tmp/$f" >/dev/null 2>&1
done

run() {
  # run <artifact_id> <expected_rc>
  local art="$1" want="$2" out rc
  out="$(DARK_DB="$DB" DS_REPO_ROOT="$REPO" "$GUARD" p "$art" 2>&1)"
  rc=$?
  check "$3" "$want" "$rc"
}

run 999 65 "artefacto sin publicar -> 65"
run 900 67 "publicado con memoria pero sin commit -> 67"
run 903 66 "publicado y con commit pero SIN memoria -> 66"
run 902 68 "las tres partes sin veredicto -> 68 (ausencia, no fallo)"
run 901 0  "las tres partes con veredicto aligned -> 0"

if [[ "$fails" -ne 0 ]]; then
  printf 'test-cierre-loop-guard: %d fallo(s)\n' "$fails" >&2
  exit 1
fi

echo "test-cierre-loop-guard: 5/5"
