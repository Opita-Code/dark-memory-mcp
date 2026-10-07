#!/usr/bin/env bash
# e2e test for C5 (video) — verifies S2V-01 + AI-slop detection
set -e
MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"
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
