#!/usr/bin/env bash
# e2e test for vibe_case C1 (code)
# Verifies a sample C1 spec template produces a valid JSON spec
# and that the manifest references it correctly.

set -e

MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"

echo "=== e2e-c1 (code) ==="

# 1. Template is valid JSON
python3 -c "
import json
d = json.load(open('$MOD_ROOT/templates/spec-c1.json'))
assert d['vibe_case'] == 'C1', f'vibe_case mismatch: {d[\"vibe_case\"]}'
assert d['label'] == 'code', f'label mismatch: {d[\"label\"]}'
assert 'tasks' in d and len(d['tasks']) >= 4, 'must have >= 4 tasks'
assert any('OSINT' in t['description'] for t in d['tasks']), 'must have OSINT task'
assert any('go test' in t['description'].lower() or 'test' in t['description'].lower() for t in d['tasks']), 'must have a test task'
print('OK: spec-c1.json valid, has OSINT + test tasks')
" || exit 1

# 2. mod.json references C1
python3 -c "
import json
d = json.load(open('$MOD_ROOT/mod.json'))
c1 = d['vibeCases']['C1']
assert c1['label'] == 'code'
assert 'go' in c1['canonicalArtifact'].lower() or 'internal' in c1['canonicalArtifact']
print('OK: mod.json C1 references code artifacts')
" || exit 1

# 3. SKILL.md mentions C1
grep -q "C1" "$MOD_ROOT/SKILL.md" || {
  echo "FAIL: SKILL.md missing C1 mention"
  exit 1
}
echo "OK: SKILL.md mentions C1"

echo "=== e2e-c1 PASS ==="
exit 0
