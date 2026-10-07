#!/usr/bin/env bash
# e2e test for C2 (text) — checks spec-c2.json has tier-1 source enforcement
set -e
MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"
echo "=== e2e-c2 (text) ==="
python3 -c "
import json
d = json.load(open('$MOD_ROOT/templates/spec-c2.json'))
assert d['vibe_case'] == 'C2' and d['label'] == 'text'
assert d['spec'].get('tier1_sources_required') is True
assert d['spec'].get('min_word_count', 0) >= 200
print('OK: spec-c2.json enforces tier-1 + min 200 words')
"
python3 -c "
import json
d = json.load(open('$MOD_ROOT/mod.json'))
c2 = d['vibeCases']['C2']
assert c2['label'] == 'text' and 'md' in c2['canonicalArtifact']
print('OK: mod.json C2 references text artifacts')
"
echo "=== e2e-c2 PASS ==="
