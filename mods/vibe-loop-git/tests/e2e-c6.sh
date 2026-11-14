# SPDX-License-Identifier: CC-BY-NC-4.0
# Copyright (c) 2026 Opita Code / Juan Nicolas Urrutia Salcedo
# Licensed under CC-BY-NC-4.0. See ./LICENSE for full terms.
#!/usr/bin/env bash
# e2e test for C6 (audio) — verifies speaker embedding + noise floor
set -e
# El repo se deriva de la ubicacion de este script, no de una ruta
# fija: asi el script funciona en cualquier maquina, y el repo publico no
# lleva el nombre de nadie.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MOD_ROOT="$(cd "$HERE/.." && pwd)"
echo "=== e2e-c6 (audio) ==="
python3 -c "
import json
d = json.load(open('$MOD_ROOT/templates/spec-c6.json'))
assert d['vibe_case'] == 'C6' and d['label'] == 'audio'
assert d['spec'].get('speaker_embedding_required') is True
assert any('speaker' in t['description'].lower() for t in d['tasks'])
assert any('noise' in t['description'].lower() for t in d['tasks'])
print('OK: spec-c6.json enforces speaker embedding + noise floor check')
"
echo "=== e2e-c6 PASS ==="
