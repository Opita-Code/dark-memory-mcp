#!/usr/bin/env bash
# tests/gate-coverage-check.sh — verifies that every tool name in the gate-trigger-matrix.json
# actually exists in the dark-memory-mcp registry at the current version.
#
# Reads the live tool list via `dark_memory_list_canonical_tools` semantics: parses the
# dark-memory-mcp Go source tree's `internal/tools/register.go` for `mcp.NewTool(<name>`.
#
# Exit 0 if all references resolve. Exit 1 with details on the first miss.

set -uo pipefail

MOD_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DARK_REPO="$(cd "$MOD_DIR/../.." && pwd)"
TOOLS_FILE="$DARK_REPO/internal/tools/register.go"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

ok() {
  echo "  ok: $1"
}

# Extract all canonical tool names from the canonical registry.
# The single source of truth is internal/tools/registry.go:canonicalNamespaces,
# which lists tools as bare names (e.g. "session_start"). The server prepends
# "dark_memory_" at runtime.
TOOL_NAMES=$(cd "$DARK_REPO" && python3 -c "
import re
text = open('internal/tools/registry.go','r',encoding='utf-8').read()
# Extract the canonicalNamespaces block.
start = text.find('var canonicalNamespaces')
end = text.find('\n}\n', start) + 3
block = text[start:end]
# Extract every quoted string. Namespaces are uppercase; tools are lowercase.
tools = set()
namespace_labels = set()
for m in re.finditer(r'\"([a-zA-Z_]+)\"', block):
    n = m.group(1)
    if n != n.lower():
        namespace_labels.add(n)
    else:
        tools.add('dark_memory_' + n)
for n in sorted(tools):
    print(n)
")

[[ -n "$TOOL_NAMES" ]] || fail "no tool names found in $TOOLS_FILE"
echo "vibe-loop-git v0.2.0 gate-coverage-check — registry has $(echo "$TOOL_NAMES" | wc -l) canonical tools"

# Extract every tool name referenced in gate-trigger-matrix.json
MATRIX_TOOLS=$(cd "$MOD_DIR" && python3 -c "
import json, re
d = json.load(open('core/gate-trigger-matrix.json','r',encoding='utf-8'))
names = set()
for row in d['matrix']:
    for t in row.get('tools', []):
        # Tool names like 'session_start' -> dark_memory_session_start
        n = t['tool']
        names.add('dark_memory_' + n)
for n in sorted(names):
    print(n)
")

[[ -n "$MATRIX_TOOLS" ]] || fail "no tools referenced in matrix"

# Diff
missing=$(comm -23 <(echo "$MATRIX_TOOLS") <(echo "$TOOL_NAMES"))
if [[ -n "$missing" ]]; then
  echo
  echo "MISSING tools (referenced in matrix but not in registry):"
  echo "$missing"
  fail "tool coverage gap"
fi

ok "all $(echo "$MATRIX_TOOLS" | wc -l) tool references resolve in registry"
echo
echo "gate-coverage-check: passed"
exit 0