# SPDX-License-Identifier: CC-BY-NC-4.0
# Copyright (c) 2026 Opita Code / Juan Nicolas Urrutia Salcedo
# Licensed under CC-BY-NC-4.0. See ./LICENSE for full terms.
#!/usr/bin/env bash
# e2e test for C5 (video) — verifies S2V-01 + AI-slop detection
set -e
# El repo se deriva de la ubicacion de este script, no de una ruta
# fija: asi el script funciona en cualquier maquina, y el repo publico no
# lleva el nombre de nadie.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MOD_ROOT="$(cd "$HERE/.." && pwd)"
echo "=== e2e-c5 (video) ==="
python3 -c "
import json
d = json.load(open('$MOD_ROOT/templates/spec-c5.json'))
assert d['vibe_case'] == 'C5' and d['label'] == 'video'
assert d['spec'].get('subject_reference_required') is True
# S2V-01 mention in tasks
assert any('S2V-01' in t['description'] for t in d['tasks']), 'must reference S2V-01'
# AI-slop mention
assert any('AI-slop' in t['description'] or 'slop' in t['description'].lower() for t in d['tasks'])
print('OK: spec-c5.json enforces S2V-01 + AI-slop check')
"
echo "=== e2e-c5 PASS ==="
