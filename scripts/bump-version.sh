#!/usr/bin/env bash
# Bump versions in 8 files: 1 wrapper + 6 platforms + server.json (2 fields).
# Usage: bash scripts/bump-version.sh <new-version>
# Example: bash scripts/bump-version.sh 2.7.1
#
# What this script does:
#   1. Updates the "version" field in 8 files (1 wrapper + 6 platforms + server.json).
#   2. Updates the optionalDependencies references in npm/wrapper/package.json
#      (which pin the wrapper to exact versions of the platform packages).
#
# Idempotency: the script REPLACES the old version with the new one. To find
# the old version, it reads npm/wrapper/package.json and uses its current
# "version" value. So running the script twice with the same $1 is a no-op.
#
# Pre-flight: after running, the operator should manually verify with:
#   for f in npm/wrapper/package.json npm/platform-*/package.json server.json; do
#     node -p "require('./$f').version"
#   done
#
# Defense in depth: precheck-version.yml (CI) will catch any drift at
# tag-push time and fail fast with a list of mismatched files.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <new-version>" >&2
  echo "Example: $0 2.7.1" >&2
  exit 1
fi

NEW="$1"

# Validate semver-ish (allow prerelease suffixes: 2.7.1, 2.7.0-alpha, 2.7.0-beta.1).
if ! [[ "$NEW" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
  echo "::error::Invalid version: $NEW (expected: X.Y.Z or X.Y.Z-prerelease)" >&2
  exit 1
fi

# Read old version from the canonical source (wrapper/package.json).
OLD=$(node -p "require('./npm/wrapper/package.json').version")
if [ "$OLD" = "$NEW" ]; then
  echo "Already at version $NEW; nothing to do."
  exit 0
fi

echo "Bumping $OLD → $NEW"

FILES=(
  "npm/wrapper/package.json"
  "npm/platform-darwin-arm64/package.json"
  "npm/platform-darwin-x64/package.json"
  "npm/platform-linux-arm64/package.json"
  "npm/platform-linux-x64/package.json"
  "npm/platform-win32-arm64/package.json"
  "npm/platform-win32-x64/package.json"
  "server.json"
)

PLATFORMS=(
  "darwin-x64"
  "darwin-arm64"
  "linux-x64"
  "linux-arm64"
  "win32-x64"
  "win32-arm64"
)

for f in "${FILES[@]}"; do
  # Top-level "version" field.
  sed -i "s/\"version\": \"$OLD\"/\"version\": \"$NEW\"/g" "$f"
done

# optionalDependencies in npm/wrapper/package.json only.
for p in "${PLATFORMS[@]}"; do
  sed -i "s/\"@opitacode\/dark-memory-mcp-$p\": \"$OLD\"/\"@opitacode\/dark-memory-mcp-$p\": \"$NEW\"/g" "npm/wrapper/package.json"
done

# Verify: every file should now declare the new version. If sed missed any
# (e.g., the file already had a different version), this loop catches it
# before commit so the operator gets a clear error.
echo ""
echo "Verifying all files declare $NEW:"
FAILED=0
for f in "${FILES[@]}"; do
  V=$(node -p "require('./$f').version" 2>&1) || {
    echo "  ::error::$f: failed to parse JSON: $V"
    FAILED=1
    continue
  }
  if [ "$V" = "$NEW" ]; then
    echo "  OK $f → $V"
  else
    echo "  ::error::$f → $V (expected $NEW)"
    FAILED=1
  fi
done

if [ "$FAILED" -ne 0 ]; then
  echo ""
  echo "Bump failed; please inspect manually. Did the file's current version differ from $OLD?" >&2
  exit 1
fi

echo ""
echo "Done. Next steps:"
echo "  git diff --stat"
echo "  git add -A && git commit -m 'chore: bump versions to $NEW'"
echo "  git push origin main"
echo "  git tag v$NEW && git push origin v$NEW"