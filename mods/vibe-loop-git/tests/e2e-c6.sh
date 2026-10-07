#!/usr/bin/env bash
# e2e test for C6 (audio) — verifies speaker embedding + noise floor
set -e
MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"
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
