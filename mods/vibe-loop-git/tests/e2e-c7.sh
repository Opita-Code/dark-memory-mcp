#!/usr/bin/env bash
# e2e test for C7 (multi) — verifies sub-router + coherence + parallel
set -e
MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"
echo "=== e2e-c7 (multi) ==="
python3 -c "
import json
d = json.load(open('$MOD_ROOT/templates/spec-c7.json'))
assert d['vibe_case'] == 'C7' and d['label'] == 'multi'
assert d['spec'].get('min_sub_artifacts', 0) >= 2
assert d['spec'].get('parallel_execution') is True
assert d['spec'].get('coherence_check_required') is True
assert any('parallel' in t['description'].lower() for t in d['tasks'])
assert any('coherence' in t['description'].lower() for t in d['tasks'])
# c7-subrouter.json artifact exists
import os
assert os.path.exists('$MOD_ROOT/core/c7-subrouter.json'), 'c7-subrouter.json missing'
print('OK: spec-c7.json enforces parallel + coherence + c7-subrouter.json exists')
"
echo "=== e2e-c7 PASS ==="
