#!/usr/bin/env bash
# governance/test-cierre-loop-guard.sh — pruebas del guard de cierre de loop.
#
# QUE COMPRUEBA
#   RUTA vibe (vibe_publish, la de siempre):
#   1. Un artefacto sin publicar -> rc 65 (no se cerro nada).
#   2. Un artefacto publicado sin rastro en memoria -> rc 66.
#   3. Un artefacto publicado con rastro pero sin commit -> rc 67.
#   4. Las tres partes en su sitio, sin veredicto -> rc 68 (ausencia de
#      veredicto, que NO es un fallo del cierre).
#   5. Las tres partes con veredicto aligned -> rc 0.
#
#   RUTA sha (el juez directo, dark_memory_judge -> sdd_evaluations):
#   6. Un sha sin juzgar -> rc 65.
#   7. Un sha juzgado sin rastro en memoria -> rc 66.
#   8. Un sha juzgado con rastro pero sin commit -> rc 67.
#   9. Un sha juzgado, con las tres partes y veredicto aligned -> rc 0.
#  10. Un sha con una muestra needs_human -> rc 68.
#  11. Un sha con una muestra drift_detected entre las aligned -> rc 0, pero
#      el veredicto que se declara es drift_detected (el peor manda).
#  12. Un commit que menciona el NUMERO de la evaluacion, sin marca ni ruta,
#      NO cuenta como el commit del artefacto -> rc 67.
#  13. La ruta sha sin --ruta -> rc 64 (uso): el commit se busca por la ruta.
#  14. Sin DS_REPO_ROOT, la raiz del repo se deduce de la ruta del artefacto.
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

# check_out <nombre> <rc esperado> <texto que tiene que aparecer> <salida>
check_out() {
  local name="$1" want="$2" needle="$3" out="$4"
  if [[ "$out" == *"$needle"* ]]; then
    printf 'ok   %s (dice "%s")\n' "$name" "$needle"
  else
    printf 'FAIL %s (no dice "%s")\n' "$name" "$needle"
    printf '     salida: %s\n' "${out:0:400}"
    fails=$((fails + 1))
  fi
}

# ── la base de pruebas ─────────────────────────────────────────────────────
# Los shas de la prueba. Son de mentira, pero tienen la forma y la longitud de
# los de verdad (64 hex), porque el guard exige 16 hex minimos.
SHA_A="a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
SHA_B="b1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
SHA_C="c1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
SHA_D="d1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
SHA_E="e1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
# SHA_F no esta en la base: es el sha que NO se juzgo.
SHA_F="f1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
# SHA_G: el mismo contenido juzgado dos veces, la primera con drift y la
# segunda con aligned. El vigente es el ultimo.
SHA_G="01b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

DB_PATH="$DB" REPO="$REPO" SHA_A="$SHA_A" SHA_B="$SHA_B" SHA_C="$SHA_C" SHA_D="$SHA_D" SHA_E="$SHA_E" SHA_F="$SHA_F" SHA_G="$SHA_G" bun -e '
import { Database } from "bun:sqlite";
const db = new Database(process.env.DB_PATH);
db.exec(`
CREATE TABLE vibe_artifacts (id INTEGER PRIMARY KEY, session_id TEXT, vibe_case TEXT,
  spec_id INTEGER, artifact_url TEXT, artifact_type TEXT, created_at TEXT, project_id TEXT);
CREATE TABLE vibe_drift_reports (id INTEGER PRIMARY KEY, artifact_id INTEGER, spec_id INTEGER,
  verdict TEXT, judge_reasoning TEXT, created_at TEXT, project_id TEXT);
CREATE TABLE agent_memory (id INTEGER PRIMARY KEY, project_id TEXT, operator TEXT, kind TEXT,
  title TEXT, content TEXT, tags TEXT, created_at TEXT);
CREATE TABLE sdd_evaluations (id INTEGER PRIMARY KEY, eval_type TEXT, target_type TEXT,
  target_id TEXT, verdict_json TEXT, confidence REAL, prompt_version TEXT, model TEXT,
  created_at TEXT, constitution_id TEXT, constitution_version TEXT, active_mods_json TEXT,
  refused_attempts INTEGER, refusal_pattern TEXT, project_id TEXT, merkle_root TEXT,
  artifact_source TEXT, artifact_sha256 TEXT, artifact_path TEXT, artifact_size INTEGER,
  chunk_index INTEGER, chunk_total INTEGER, nli_provider_id TEXT);
`);

const now = new Date().toISOString();
const repo = process.env.REPO;

// ── la ruta vibe, como hasta ahora ──
const art = db.query(`INSERT INTO vibe_artifacts
  (id, session_id, vibe_case, spec_id, artifact_url, artifact_type, created_at, project_id)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`);
art.run(900, "s", "C1", 1, "file:///tmp/x.diff", "code", now, "p");
art.run(901, "s", "C1", 1, "file:///tmp/y.diff", "code", now, "p");
art.run(902, "s", "C1", 1, "file:///tmp/z.diff", "code", now, "p");
art.run(903, "s", "C1", 1, "file:///tmp/w.diff", "code", now, "p");
art.run(904, "s", "C1", 1, "file:///tmp/v.diff", "code", now, "p");
// MEDIDO el 2026-11-17: el guard buscaba el commit por la RUTA en el MENSAJE
// del commit, y eso casa con cualquier commit que mencione el fichero. Para
// el artefacto 322 devolvio un commit del 2026-10-23 que tocaba el fichero
// pero no citaba el artefacto, y dio por bueno un cierre con un commit que
// no era el del cierre. Estos tres artefactos reproducen ese fallo.
art.run(905, "s", "C1", 1, "file:///tmp/vecino.md", "code", now, "p");
art.run(906, "s", "C1", 1, "file:///tmp/marcado.md", "code", now, "p");
art.run(907, "s", "C1", 1, "file:///tmp/ambos.md", "code", now, "p");
const drift = db.query(`INSERT INTO vibe_drift_reports
  (id, artifact_id, spec_id, verdict, judge_reasoning, created_at, project_id)
  VALUES (?, ?, ?, ?, ?, ?, ?)`);
drift.run(901, 901, 1, "aligned", "el arreglo cumple la promesa", now, "p");
drift.run(902, 902, 1, "needs_human", "nli: provider rate limited", now, "p");
drift.run(905, 905, 1, "aligned", "el arreglo cumple la promesa", now, "p");
drift.run(906, 906, 1, "aligned", "el arreglo cumple la promesa", now, "p");
drift.run(907, 907, 1, "aligned", "el arreglo cumple la promesa", now, "p");

// ── la ruta sha: el juez directo ──
// El layout es el de opita-code: el artefacto juzgado esta en
// <repo>/.partes-juez/<documento>/<parte>.md
const ev = db.query(`INSERT INTO sdd_evaluations
  (id, eval_type, target_type, target_id, verdict_json, confidence, created_at, project_id,
   artifact_source, artifact_sha256, artifact_path, artifact_size, chunk_index, chunk_total, nli_provider_id)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`);
const j = (v, c, r) => JSON.stringify({ verdict: v, confidence: c, reasoning: r });
const ruta = (doc, parte) => `${repo}/.partes-juez/${doc}/${parte}`;

// SHA_A: tres muestras (consenso 3), todas aligned. CIERRE COMPLETO.
ev.run(950, "drift_judge", "artifact", "artifact", j("aligned", 0.99, "cumple"), 0.99, now, "p", "file", process.env.SHA_A, ruta("docs_canal.md", "canal.parte-1.md"), 900, 0, 0, "judge-opita-gate");
ev.run(951, "drift_judge", "artifact", "artifact", j("aligned", 0.98, "cumple"), 0.98, now, "p", "file", process.env.SHA_A, ruta("docs_canal.md", "canal.parte-1.md"), 900, 0, 0, "judge-opita-gate");
ev.run(952, "drift_judge", "artifact", "artifact", j("aligned", 0.92, "cumple"), 0.92, now, "p", "file", process.env.SHA_A, ruta("docs_canal.md", "canal.parte-1.md"), 900, 0, 0, "judge-opita-gate");
// SHA_B: una muestra drift entre las aligned. EL PEOR MANDA.
ev.run(953, "drift_judge", "artifact", "artifact", j("aligned", 0.99, "cumple"), 0.99, now, "p", "file", process.env.SHA_B, ruta("docs_drift.md", "drift.parte-1.md"), 900, 0, 0, "judge-opita-gate");
ev.run(954, "drift_judge", "artifact", "artifact", j("drift_detected", 0.95, "se pasa"), 0.95, now, "p", "file", process.env.SHA_B, ruta("docs_drift.md", "drift.parte-1.md"), 900, 0, 0, "judge-opita-gate");
// SHA_C: una muestra needs_human. AUSENCIA DE VEREDICTO.
ev.run(955, "drift_judge", "artifact", "artifact", j("needs_human", 0.8, "no se pudo"), 0.8, now, "p", "file", process.env.SHA_C, ruta("docs_para.md", "para.parte-1.md"), 900, 0, 0, "judge-opita-gate");
// SHA_D: juzgado, con memoria, pero SIN commit que lo cite.
ev.run(956, "drift_judge", "artifact", "artifact", j("aligned", 0.97, "cumple"), 0.97, now, "p", "file", process.env.SHA_D, ruta("docs_sincommit.md", "sinc.parte-1.md"), 900, 0, 0, "judge-opita-gate");
// SHA_E: sin memoria que lo cite.
ev.run(957, "drift_judge", "artifact", "artifact", j("aligned", 0.96, "cumple"), 0.96, now, "p", "file", process.env.SHA_E, ruta("docs_sinmemoria.md", "sinm.parte-1.md"), 900, 0, 0, "judge-opita-gate");
// SHA_G: el mismo contenido, juzgado dos veces. La primera dio drift y la
// segunda aligned. MEDIDO 2026-11-16: si se agregaran todas las muestras,
// un veredicto viejo poisoningaria al nuevo para siempre. El vigente es el
// ultimo, y el historico se imprime entero.
ev.run(958, "drift_judge", "artifact", "artifact", j("drift_detected", 0.98, "se pasa"), 0.98, now, "p", "file", process.env.SHA_G, ruta("docs_rejuzgado.md", "rej.parte-1.md"), 900, 0, 0, "judge-opita-gate");
ev.run(959, "drift_judge", "artifact", "artifact", j("aligned", 0.94, "cumple"), 0.94, now, "p", "file", process.env.SHA_G, ruta("docs_rejuzgado.md", "rej.parte-1.md"), 900, 0, 0, "judge-opita-gate");

// ── la memoria: las tres de la ruta vibe, y las de la ruta sha ──
const mem = db.query(`INSERT INTO agent_memory
  (id, project_id, operator, kind, title, content, tags, created_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`);
mem.run(700, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 900", "900", now);
mem.run(701, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 901", "901", now);
mem.run(702, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 902", "902", now);
mem.run(704, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 904", "904", now);
mem.run(705, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 905", "905", now);
mem.run(706, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 906", "906", now);
mem.run(707, "p", "opencode", "finding", "t", "el hallazgo cita el artefacto 907", "907", now);
// La memoria puede citar el sha o el id de la evaluacion con su marca "#".
mem.run(710, "p", "opencode", "finding", "t", `el hallazgo cita el sha ${process.env.SHA_A}`, "", now);
mem.run(711, "p", "opencode", "finding", "t", "el hallazgo cita la evaluacion #953 y #954", "", now);
mem.run(712, "p", "opencode", "finding", "t", "el hallazgo cita la evaluacion #955", "", now);
mem.run(713, "p", "opencode", "finding", "t", `el hallazgo cita el sha ${process.env.SHA_D}`, "", now);
// Y una memoria que solo cita el NUMERO de la evaluacion, sin marca: no vale.
mem.run(714, "p", "opencode", "finding", "t", "contador 957 en el texto", "", now);
mem.run(715, "p", "opencode", "finding", "t", `el hallazgo cita las evaluaciones #958 y #959`, "", now);
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
# Los commits de la ruta sha: citan la RUTA del documento, que es lo durable.
mkdir -p "$REPO/docs"
for d in docs_canal.md docs_drift.md docs_para.md docs_sinmemoria.md docs_rejuzgado.md; do
  echo "doc" > "$REPO/$d"
  git -C "$REPO" add "$d" >/dev/null 2>&1
  git -C "$REPO" commit -q -m "cierra el juicio de $d" >/dev/null 2>&1
done
# MEDIDO 2026-11-15: un commit que menciona el NUMERO del artefacto (904)
# sin citar su ruta no es el commit del artefacto. Antes el guard lo daba
# por bueno (buscaba el numero suelto) y daba por cerrado un loop que no
# estaba. Este commit es el que reproduce ese fallo.
git -C "$REPO" commit -q --allow-empty -m "contador 904 en el mensaje" >/dev/null 2>&1
git -C "$REPO" commit -q --allow-empty -m "contador 900 en el mensaje" >/dev/null 2>&1
# Y el mismo fallo en la ruta sha: un commit que suelta el numero de una
# evaluacion NO es el commit de esa evaluacion.
git -C "$REPO" commit -q --allow-empty -m "contador 957 en el mensaje" >/dev/null 2>&1
git -C "$REPO" commit -q --allow-empty -m "contador 956 en el mensaje" >/dev/null 2>&1
# MEDIDO el 2026-11-17: un commit que MENCIONA la ruta del artefacto en su
# cuerpo, sin tocar el fichero, NO es el commit del artefacto. El cuerpo de
# un commit con diff lista las rutas, asi que este caso es el everyday: sin
# el, el guard daba por bueno un cierre con un commit que no era del cierre.
git -C "$REPO" commit -q --allow-empty -m "menciona /tmp/vecino.md sin tocarlo" >/dev/null 2>&1
# Y el marcador explicito: un commit que dice "artefacto 906" entrega ese
# artefacto, aunque no toque ningun fichero del repo.
git -C "$REPO" commit -q --allow-empty -m "cierra el artefacto 906" >/dev/null 2>&1
# Y el caso con las dos pruebas: el fichero tiene su commit, y ademas hay un
# commit con el marcador. El marcador manda: es el commit del artefacto.
echo "doc" > "$REPO/ambos.md"
git -C "$REPO" add ambos.md >/dev/null 2>&1
git -C "$REPO" commit -q -m "toca ambos.md" >/dev/null 2>&1
git -C "$REPO" commit -q --allow-empty -m "cierra el artefacto 907" >/dev/null 2>&1

run() {
  # run <artefacto> <expected_rc> <nombre>
  local art="$1" want="$2" out rc
  out="$(DARK_DB="$DB" DS_REPO_ROOT="$REPO" "$GUARD" p "$art" 2>&1)"
  rc=$?
  check "$3" "$want" "$rc"
  LAST_OUT="$out"
}

run_sha() {
  # run_sha <sha> <ruta> <expected_rc> <nombre> [--sin-repo-root]
  local sha="$1" ruta="$2" want="$3" out rc
  if [[ "${5:-}" == "--sin-repo-root" ]]; then
    out="$(DARK_DB="$DB" "$GUARD" p "sha:$sha" --ruta "$ruta" 2>&1)"
  else
    out="$(DARK_DB="$DB" DS_REPO_ROOT="$REPO" "$GUARD" p "sha:$sha" --ruta "$ruta" 2>&1)"
  fi
  rc=$?
  check "$4" "$want" "$rc"
  LAST_OUT="$out"
}

# ── la ruta vibe ──
run 999 65 "artefacto sin publicar -> 65"
run 900 67 "publicado con memoria pero sin commit -> 67"
run 904 67 "un commit con el numero suelto NO cuenta como commit del artefacto -> 67"
run 903 66 "publicado y con commit pero SIN memoria -> 66"
run 902 68 "las tres partes sin veredicto -> 68 (ausencia, no fallo)"
run 901 0  "las tres partes con veredicto aligned -> 0"
# MEDIDO el 2026-11-17: el falso positivo del guard.
run 905 67 "un commit que solo MENCIONA la ruta NO cuenta como commit del artefacto -> 67"
run 906 0  "un commit con el MARCADOR 'artefacto 906' si cuenta -> 0"
check_out "el guard dice que lo encontro por el marcador" 0 "por marcador" "$LAST_OUT"
run 907 0  "con las dos pruebas, manda el marcador -> 0"
check_out "el commit citado es el del marcador, no el que toca el fichero" 0 "cierra el artefacto 907" "$LAST_OUT"

# ── la ruta sha ──
run_sha "$SHA_F" "docs_canal.md" 65 "un sha sin juzgar -> 65"
run_sha "$SHA_A" "docs_canal.md" 0 "sha juzgado, con memoria, commit y veredicto aligned -> 0"
run_sha "$SHA_E" "docs_sinmemoria.md" 66 "sha juzgado con commit pero SIN memoria -> 66"
run_sha "$SHA_D" "docs_sincommit.md" 67 "sha juzgado con memoria pero SIN commit -> 67"
run_sha "$SHA_C" "docs_para.md" 68 "sha con needs_human -> 68 (ausencia, no fallo)"
run_sha "$SHA_B" "docs_drift.md" 0 "sha con una muestra drift -> 0 (el loop esta cableado)"
check_out "el veredicto vigente es el ULTIMO" 0 "veredicto   : drift_detected" "$LAST_OUT"
run_sha "$SHA_G" "docs_rejuzgado.md" 0 "un rejuzgado con verdictos distintos -> el ultimo manda -> 0"
check_out "el veredicto vigente tras rejuzgar es aligned" 0 "veredicto   : aligned" "$LAST_OUT"
check_out "el historico NO se oculta" 0 "nota: 1 muestra(s) dio un veredicto distinto" "$LAST_OUT"
run_sha "$SHA_E" "docs_sinmemoria.md" 66 "sha SIN memoria que lo cite -> 66 (un numero suelto en la memoria no cuenta)"
run_sha "$SHA_A" "docs_canal.md" 0 "sin DS_REPO_ROOT la raiz se deduce del artefacto -> 0" --sin-repo-root

# La ruta sha sin --ruta: el commit se busca por la ruta, asi que sin ella no
# se puede comprobar el cierre.
out="$(DARK_DB="$DB" DS_REPO_ROOT="$REPO" "$GUARD" p "sha:$SHA_A" 2>&1)"; rc=$?
check "la ruta sha sin --ruta -> 64 (uso)" 64 "$rc"

if [[ "$fails" -ne 0 ]]; then
  printf 'test-cierre-loop-guard: %d fallo(s)\n' "$fails" >&2
  exit 1
fi

echo "test-cierre-loop-guard: todas pasan"
