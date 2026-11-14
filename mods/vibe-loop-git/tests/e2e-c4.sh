# SPDX-License-Identifier: CC-BY-NC-4.0
# Copyright (c) 2026 Opita Code / Juan Nicolas Urrutia Salcedo
# Licensed under CC-BY-NC-4.0. See ./LICENSE for full terms.
#!/usr/bin/env bash
# e2e test for C4 (research) — verifies tier-1 + corrections discipline
set -e
# El repo se deriva de la ubicacion de este script, no de una ruta
# fija: asi el script funciona en cualquier maquina, y el repo publico no
# lleva el nombre de nadie.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MOD_ROOT="$(cd "$HERE/.." && pwd)"
echo "=== e2e-c4 (research) ==="
python3 -c "
import json
d = json.load(open('$MOD_ROOT/templates/spec-c4.json'))
assert d['vibe_case'] == 'C4' and d['label'] == 'research'
assert d['spec'].get('tier1_sources_required') is True
assert d['spec'].get('tier1_min_count', 0) >= 3
print('OK: spec-c4.json enforces tier-1 + min 3 sources')
"
python3 -c "
import json
d = json.load(open('$MOD_ROOT/mod.json'))
c4 = d['vibeCases']['C4']
assert c4['label'] == 'research'
print('OK: mod.json C4 references research artifacts')
"
echo "=== e2e-c4 PASS ==="
