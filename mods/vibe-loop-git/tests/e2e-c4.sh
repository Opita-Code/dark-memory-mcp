#!/usr/bin/env bash
# e2e test for C4 (research) — verifies tier-1 + corrections discipline
set -e
MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"
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
