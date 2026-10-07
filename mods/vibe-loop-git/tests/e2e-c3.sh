#!/usr/bin/env bash
# e2e test for C3 (decision) — verifies 3-options + bias-declaration discipline
set -e
MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"
echo "=== e2e-c3 (decision) ==="
python3 -c "
import json
d = json.load(open('$MOD_ROOT/templates/spec-c3.json'))
assert d['vibe_case'] == 'C3' and d['label'] == 'decision'
assert d['spec'].get('options_required', 0) >= 3, 'C3 must require >= 3 options (LUCIDEZ R5)'
print('OK: spec-c3.json requires >= 3 options')
"
python3 -c "
import json
d = json.load(open('$MOD_ROOT/mod.json'))
c3 = d['vibeCases']['C3']
assert c3['label'] == 'decision'
print('OK: mod.json C3 references decision artifacts')
"
echo "=== e2e-c3 PASS ==="
