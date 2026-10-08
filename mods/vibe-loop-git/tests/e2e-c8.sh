#!/usr/bin/env bash
# tests/e2e-c8.sh — E2E smoke for vibe-loop-git Loop 7 (vibe-flow mode, C8).
#
# Verifies the mod's on-disk assets parse and reference the right namespaces.
# Does NOT actually run the dark-memory server (that's left to live verification).
#
# Exit 0 on success, 1 on any check failure.

set -uo pipefail

# Resolve paths via cygpath so Windows paths work under Git Bash
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MOD_DIR_WIN="$(cd "$(dirname "$0")/.." && pwd)"
MOD_DIR="$SCRIPT_DIR/.."
CORE_DIR="$MOD_DIR/core"
TEMPLATES_DIR="$MOD_DIR/templates"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

ok() {
  echo "  ok: $1"
}

echo "vibe-loop-git v0.2.0 e2e — C8 vibe-flow mode"

# 1. Required core artifacts exist
for f in loop-7-vibe-flow.md gate-trigger-matrix.json gate-protocol.md; do
  [[ -f "$CORE_DIR/$f" ]] || fail "missing $f"
  ok "core/$f exists"
done

# 2. mod.json version + loops shipped (cd into dir to avoid path-conversion issues)
mod_version=$(cd "$MOD_DIR" && python3 -c "import json; print(json.load(open('mod.json','r',encoding='utf-8'))['version'])")
[[ "$mod_version" == "0.2.0" ]] || fail "mod.json version = $mod_version, want 0.2.0"
ok "mod.json version = $mod_version"

loops=$(cd "$MOD_DIR" && python3 -c "import json; print(json.load(open('mod.json','r',encoding='utf-8'))['audit']['loopsShipped'])")
[[ "$loops" == "7" ]] || fail "loopsShipped = $loops, want 7"
ok "mod.json loopsShipped = $loops"

# 3. vibeCases includes C8
c8=$(cd "$MOD_DIR" && python3 -c "import json; print('C8' in json.load(open('mod.json','r',encoding='utf-8'))['vibeCases'])")
[[ "$c8" == "True" ]] || fail "vibeCases.C8 missing"
ok "mod.json vibeCases.C8 present"

# 4. gate-trigger-matrix.json has 23 events
events=$(cd "$MOD_DIR" && python3 -c "import json; d=json.load(open('core/gate-trigger-matrix.json','r',encoding='utf-8')); print(len(d['matrix']))")
[[ "$events" == "23" ]] || fail "matrix rows = $events, want 23"
ok "gate-trigger-matrix.json has 23 rows"

# 5. matrix covers 18 namespaces
wired=$(cd "$MOD_DIR" && python3 -c "import json; d=json.load(open('core/gate-trigger-matrix.json','r',encoding='utf-8')); print(len(d['summary']['namespaces_wired']))")
[[ "$wired" == "18" ]] || fail "namespaces_wired = $wired, want 18"
ok "matrix covers 18 namespaces"

# 6. gate-protocol.md has the 23-event table
events_in_protocol=$(grep -cE "^\| E[0-9]{2} \|" "$CORE_DIR/gate-protocol.md" || true)
[[ "$events_in_protocol" -ge 20 ]] || fail "gate-protocol.md events in table = $events_in_protocol, want >= 20"
ok "gate-protocol.md event table has $events_in_protocol rows"

# 7. spec-c8.json template exists and is valid JSON
[[ -f "$TEMPLATES_DIR/spec-c8.json" ]] || fail "missing templates/spec-c8.json"
spec_c8_valid=$(cd "$MOD_DIR" && python3 -c "import json; d=json.load(open('templates/spec-c8.json','r',encoding='utf-8')); print(d.get('vibe_case')=='C8')")
[[ "$spec_c8_valid" == "True" ]] || fail "templates/spec-c8.json vibe_case != C8"
ok "templates/spec-c8.json vibe_case = C8"

echo
echo "e2e-c8: all checks passed"
exit 0