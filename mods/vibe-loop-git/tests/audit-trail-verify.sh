#!/usr/bin/env bash
# vibe-loop-git audit-trail verifier
# Asserts LOCAL-ONLY discipline: no remote tags, no git push traces.

set -e

REPO="C:/Users/Nico/Documents/dark-memory-mcp"
BRANCH=$(git -C "$REPO" rev-parse --abbrev-ref HEAD 2>/dev/null)

echo "=== vibe-loop-git audit-trail verify ==="
echo "repo: $REPO"
echo "branch: $BRANCH"

# 1. Current branch is feat/v4-redesign (per LOCAL-ONLY discipline)
if [ "$BRANCH" != "feat/v4-redesign" ]; then
  echo "FAIL: not on feat/v4-redesign branch (LOCAL-ONLY discipline violated)"
  exit 1
fi
echo "OK: on feat/v4-redesign branch"

# 2. No remote tags matching v4.0.0-alpha.* (LOCAL tags only)
REMOTE_TAGS=$(git -C "$REPO" ls-remote --tags origin 2>/dev/null | grep -E "v4\.0\.0-alpha\." | wc -l)
if [ "$REMOTE_TAGS" -gt 0 ]; then
  echo "FAIL: found $REMOTE_TAGS remote tags matching v4.0.0-alpha.* (LOCAL-ONLY violated)"
  echo "Hint: delete the remote tags or don't push them"
  exit 1
fi
echo "OK: no remote tags matching v4.0.0-alpha.* (LOCAL-ONLY enforced)"

# 3. Local tags include the expected progression
EXPECTED_TAGS=("v4.0.0-alpha.27" "v4.0.0-alpha.28")
for tag in "${EXPECTED_TAGS[@]}"; do
  if ! git -C "$REPO" rev-parse "$tag" >/dev/null 2>&1; then
    echo "WARN: local tag $tag missing (was it deleted?)"
  else
    echo "OK: local tag $tag exists"
  fi
done

# 4. Recent commits include the 6 vibe-loop loops + T-407-c
RECENT_LOGS=$(git -C "$REPO" log --oneline -20)
for needle in "Loop 1" "Loop 2" "Loop 3" "Loop 4" "Loop 5" "Loop 6" "T-407-c"; do
  if echo "$RECENT_LOGS" | grep -q "$needle"; then
    echo "OK: commit log contains '$needle'"
  else
    echo "WARN: '$needle' not in recent 20 commits"
  fi
done

# 5. .gitignore whitelists mods/vibe-loop-git/
GITIGNORE="$REPO/.gitignore"
if grep -q "!mods/vibe-loop-git/" "$GITIGNORE"; then
  echo "OK: .gitignore whitelists mods/vibe-loop-git/"
else
  echo "WARN: .gitignore does not whitelist mods/vibe-loop-git/ (will need -f on git add)"
fi

# 6. No secrets in the mod (skip tests/ entirely — contains the regex itself)
# NOTE: `set -e` at top makes any non-zero exit abort. `|| true` prevents
# `grep` exiting 1 (no matches) from killing the script.
SECRETS_HITS=$(grep -rE "(sk-ant-api03-|x-api-key|sk-[a-zA-Z0-9_-]{20,})" \
  --include="*.md" --include="*.json" --include="*.toml" --include="*.yaml" --include="*.yml" --include="*.txt" \
  --exclude-dir="tests" \
  "C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git/" 2>/dev/null) || true
if [ -n "$SECRETS_HITS" ]; then
  echo "FAIL: possible secret in mod files:"
  echo "$SECRETS_HITS"
  exit 1
fi
echo "OK: no secrets found in mod files (tests/ excluded because it contains the regex)"

# 7. dark-memory-mcp is gitignored (the dark-mem-mcp.exe binary etc.)
# This is informational only — the dark-mem-mcp binary should NOT be in the mod
if ls "C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git/bin/" 2>/dev/null; then
  echo "WARN: mod has a bin/ directory (should not be there)"
fi

echo "=== audit-trail verify PASS ==="
exit 0
