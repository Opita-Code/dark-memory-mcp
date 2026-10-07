#!/usr/bin/env bash
# vibe-loop-git bootstrap smoke test
# Verifies the mod's manifest + structure are intact. Idempotent.
# Run from anywhere — uses absolute paths to the mod.

set -e

MOD_ROOT="C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git"

echo "=== vibe-loop-git bootstrap test ==="
echo "mod root: $MOD_ROOT"

# 1. mod.json exists and is valid JSON
if [ ! -f "$MOD_ROOT/mod.json" ]; then
  echo "FAIL: mod.json missing"
  exit 1
fi
python3 -c "import json; json.load(open('$MOD_ROOT/mod.json'))" || {
  echo "FAIL: mod.json is not valid JSON"
  exit 1
}
echo "OK: mod.json valid JSON"

# 2. SKILL.md exists and has frontmatter
if [ ! -f "$MOD_ROOT/SKILL.md" ]; then
  echo "FAIL: SKILL.md missing"
  exit 1
fi
head -1 "$MOD_ROOT/SKILL.md" | grep -q "^---" || {
  echo "FAIL: SKILL.md missing frontmatter"
  exit 1
}
echo "OK: SKILL.md present with frontmatter"

# 3. All 6 loop artifacts present in core/
EXPECTED_CORE_LOOPS=(
  "core/loop-1-context-strategies.md"
  "core/judge-mapping.json"
  "core/c7-subrouter.json"
  "core/coldstart-rules.md"
  "core/latency-budget.json"
  "core/self-eval.json"
)
for f in "${EXPECTED_CORE_LOOPS[@]}"; do
  if [ ! -f "$MOD_ROOT/$f" ]; then
    echo "FAIL: $f missing"
    exit 1
  fi
done
echo "OK: 6 core/ loop artifacts present (self-contained in v0.1.0)"
# Loop 1 moved from docs/specs/ to core/loop-1-context-strategies.md in Phase 18
if [ -f "C:/Users/Nico/Documents/dark-memory-mcp/docs/specs/SPEC-vibe-loop-git-v0.2-loop-1.md" ]; then
  echo "WARN: Loop 1 spec doc still at docs/specs/ (should have been moved to core/ in Phase 18)"
fi

# 4. T-407-c design doc present
if [ ! -f "$MOD_ROOT/core/t-407-c-design.md" ]; then
  echo "FAIL: t-407-c-design.md missing"
  exit 1
fi
echo "OK: t-407-c-design.md present"

# 5. All 4 adapters present
for adapter in opencode claude-code claude-desktop codex; do
  if [ ! -f "$MOD_ROOT/adapters/$adapter/SKILL.md" ]; then
    echo "FAIL: adapters/$adapter/SKILL.md missing"
    exit 1
  fi
done
echo "OK: 4 adapters present (opencode, claude-code, claude-desktop, codex)"

# 6. All 7 templates present
for c in 1 2 3 4 5 6 7; do
  if [ ! -f "$MOD_ROOT/templates/spec-c$c.json" ]; then
    echo "FAIL: templates/spec-c$c.json missing"
    exit 1
  fi
  python3 -c "import json; json.load(open('$MOD_ROOT/templates/spec-c$c.json'))" || {
    echo "FAIL: templates/spec-c$c.json is not valid JSON"
    exit 1
  }
done
echo "OK: 7 templates present and valid JSON"

# 7. mod.json counts match reality
ACTUAL_LOOPS=$(ls "$MOD_ROOT/core/"*.md "$MOD_ROOT/core/"*.json 2>/dev/null | grep -v t-407-c | wc -l)
MODJSON_LOOPS=$(python3 -c "import json; d=json.load(open('$MOD_ROOT/mod.json')); print(len(d['core']['artifacts']))")
if [ "$ACTUAL_LOOPS" -ne "$MODJSON_LOOPS" ]; then
  echo "FAIL: actual loop files ($ACTUAL_LOOPS) != mod.json count ($MODJSON_LOOPS)"
  exit 1
else
  echo "OK: loop file count matches mod.json ($ACTUAL_LOOPS)"
fi

echo "=== bootstrap test PASS ==="
exit 0
