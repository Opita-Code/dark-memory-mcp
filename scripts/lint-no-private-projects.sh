#!/usr/bin/env bash
# scripts/lint-no-private-projects.sh
#
# Hard internal rule: dark-memory-mcp MUST NOT reference any private
# project by name in any tracked TEXT file. Private project names
# live in the operator's private index (not in this repo) and naming
# them in public artifacts is a hard leak.
#
# This script iterates every tracked file and grep-matches each
# BLOCKLIST term against the file CONTENT (NOT the file name).
# Wire it into CI and/or run it locally before `git commit` so
# a leak is rejected at the gate, not after the fact.
#
# Add new private project names to BLOCKLIST as the org's
# registry grows. Keep BLOCKLIST sorted for review hygiene.

set -eu

# All private dark-* siblings + the operator's project that lived
# under C:\Users\Nico (operator private index).
BLOCKLIST=(
  dark-harvest
  dark-scrapper
  dark-scraper
  scrappingkeys
  scrapping-keys
  dark-classify
  dark-verify
  dark-vault
  dark-rotate
  dark-copilot
  dark-copilot-v3
)

# Directories that NEVER carry user-authored prose. We skip these
# to avoid false positives (binary blobs, vendored third-party
# code, generated artifacts). Keep this list MINIMAL — only add
# a path here after a confirmed false positive.
SKIP_DIRS=(
  .git
  node_modules
  vendor
  dist
  build
  scripts/lint-no-private-projects.sh
  scripts/lint-no-private-projects.ps1
)

EXIT=0
for term in "${BLOCKLIST[@]}"; do
  # git grep -F: literal-string match (not regex). --name-only: print
  # only filenames. -z: NUL-separated so files with spaces/newlines
  # in their names are still handled.
  matches=$(git grep -z -l -F "$term" 2>/dev/null || true)
  if [ -n "$matches" ]; then
    # Filter out paths under SKIP_DIRS.
    filtered=$(echo "$matches" | tr '\0' '\n' | grep -vE "$(printf '%s|' "${SKIP_DIRS[@]}" | sed 's/|$//')" || true)
    if [ -n "$filtered" ]; then
      echo "LEAK: forbidden project name '$term' found in:"
      echo "$filtered" | sed 's/^/  /'
      EXIT=1
    fi
  fi
done

if [ "$EXIT" -ne 0 ]; then
  echo
  echo "Hard rule: do not mention private projects in public artifacts."
  echo "Replace with a generic placeholder ([FUTURE-MCP-N] / [NEIGHBORING-SCRAPER] / etc.)"
  echo "or move the detail to the operator's private index."
  exit 1
fi

echo "OK: no private project names in tracked files."
exit 0
