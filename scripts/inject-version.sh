#!/usr/bin/env bash
#
# inject-version.sh — resolve the canonical version string and emit a
# `-ldflags` expression that injects it into the version.buildVersion
# variable (the single point of injection for CONSTITUTION.md Rule 1).
#
# Usage (from Makefile):
#   VERSION_LDFLAGS := $(shell ./scripts/inject-version.sh)
#   go build -ldflags "$(VERSION_LDFLAGS)" ./cmd/dark-mem-mcp
#
# Usage (standalone):
#   ./scripts/inject-version.sh           # prints: -X ...version.buildVersion=1.3.2
#   ./scripts/inject-version.sh --raw     # prints: 1.3.2
#   ./scripts/inject-version.sh --json    # prints: {"version":"1.3.2","commit":"abc1234","dirty":false}
#
# Resolution rules (in priority order):
#   1. $DARK_VERSION env var (operator override for canary builds).
#   2. `git describe --tags --always --dirty` output.
#        - "v1.3.2" → "1.3.2"
#        - "v1.3.2-3-gabc1234" → "1.3.2-3-gabc1234" (N commits past tag)
#        - "v1.3.2-dirty" → "1.3.2-dirty" (working tree had changes)
#        - "abc1234" (no tag) → "0.0.0-dev-abc1234"
#   3. If git is unavailable: print "dev" with a warning to stderr.
#
# On any error, the script falls back to the "dev" sentinel and emits
# a non-zero exit ONLY if --strict was passed. The non-strict default
# is intentional: a missing git on a CI worker should not block a
# debug build.

set -euo pipefail

raw=0
json=0
strict=0
for arg in "$@"; do
    case "$arg" in
        --raw) raw=1 ;;
        --json) json=1 ;;
        --strict) strict=1 ;;
        -h|--help)
            sed -n '2,28p' "$0"
            exit 0
            ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

PKG="github.com/dark-agents/dark-memory-mcp/internal/version"
VAR="buildVersion"

# 1. Operator override.
if [[ -n "${DARK_VERSION:-}" ]]; then
    version="${DARK_VERSION}"
    source="env"
else
    # 2. git describe.
    if command -v git >/dev/null 2>&1; then
        if git describe --tags --always --dirty >/dev/null 2>&1; then
            describe="$(git describe --tags --always --dirty)"
            if [[ "$describe" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)(-([0-9]+)-g([0-9a-f]+))?(-dirty)?$ ]]; then
                tag="${BASH_REMATCH[1]}"
                commits="${BASH_REMATCH[3]:-}"
                sha="${BASH_REMATCH[4]:-}"
                dirty="${BASH_REMATCH[5]:-}"
                if [[ -n "$commits" && -n "$sha" ]]; then
                    version="${tag}-${commits}-g${sha}${dirty}"
                else
                    version="${tag}${dirty}"
                fi
                source="git"
            elif [[ "$describe" =~ ^[0-9a-f]+$ ]]; then
                version="0.0.0-dev-${describe}"
                source="git-sha-only"
            else
                version="dev"
                source="git-unparseable"
                echo "inject-version: WARNING: unparseable git describe: $describe" >&2
            fi
        else
            version="dev"
            source="git-failed"
            echo "inject-version: WARNING: git describe failed" >&2
        fi
    else
        version="dev"
        source="no-git"
        echo "inject-version: WARNING: git not available; falling back to 'dev'" >&2
    fi
fi

# Strict mode: error out on any fallback to 'dev'.
if [[ "$strict" == "1" && "$version" == "dev" ]]; then
    echo "inject-version: --strict set and resolution fell back to 'dev'; aborting" >&2
    exit 1
fi

# Output.
if [[ "$raw" == "1" ]]; then
    printf '%s' "$version"
elif [[ "$json" == "1" ]]; then
    # commit + dirty from git (best-effort; non-fatal).
    commit=""
    dirty=false
    if command -v git >/dev/null 2>&1; then
        commit="$(git rev-parse --short HEAD 2>/dev/null || echo "")"
        [[ "$(git status --porcelain 2>/dev/null | head -n1)" != "" ]] && dirty=true
    fi
    printf '{"version":"%s","commit":"%s","dirty":%t,"source":"%s"}' \
        "$version" "$commit" "$dirty" "$source"
else
    printf -- '-X %s.%s=%s' "$PKG" "$VAR" "$version"
fi
