#!/usr/bin/env bash
# scripts/test-inject-version.sh — regression test for inject-version.{sh,ps1}.
# Verifies that the regex update in scripts/inject-version.sh accepts pre-release
# tag suffixes (-alpha, -beta, -rc.N) and that the legacy X.Y.Z[-N-gSHA][-dirty]
# forms still parse.
#
# Why this test exists: row 195 (2026-08-03) — the regex `^v?(\d+\.\d+\.\d+)…`
# didn't accept `-alpha` and friends, so `git describe --tags` output for a
# v2.9.2-alpha tag fell through to the "dev" sentinel (silently if stderr was
# suppressed by a Makefile $(shell ...)). Fixed in this commit.
#
# Run from anywhere; cd to repo root automatically. Requires bash.
set -euo pipefail

cd "$(dirname "$0")/.."
SCRIPT="./scripts/inject-version.sh"
MOCKBIN="$(mktemp -d)"

# Mock git that prints $MOCK_GIT_DESCRIBE for `git describe …`.
cat > "$MOCKBIN/git" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "describe" ]]; then
    if [[ -n "${MOCK_GIT_DESCRIBE:-}" ]]; then
        printf '%s' "$MOCK_GIT_DESCRIBE"
        exit 0
    fi
fi
exit 127
EOF
chmod +x "$MOCKBIN/git"

pass=0
fail=0

# Helper: run the script with DARK_VERSION=$1, expect --raw output $2.
check_env() {
    local input="$1"
    local want="$2"
    local got
    got=$(DARK_VERSION="$input" "$SCRIPT" --raw 2>/dev/null || true)
    if [[ "$got" == "$want" ]]; then
        printf "  PASS  env %-35s -> %s\n" "$input" "$got"
        pass=$((pass + 1))
    else
        printf "  FAIL  env %-35s want=%q got=%q\n" "$input" "$want" "$got"
        fail=$((fail + 1))
    fi
}

# Helper: mock `git describe` to return $1, expect --raw output $2.
check_git() {
    local describe="$1"
    local want="$2"
    local got
    got=$(MOCK_GIT_DESCRIBE="$describe" PATH="$MOCKBIN:$PATH" "$SCRIPT" --raw 2>/dev/null || true)
    if [[ "$got" == "$want" ]]; then
        printf "  PASS  git %-30s -> %s\n" "$describe" "$got"
        pass=$((pass + 1))
    else
        printf "  FAIL  git %-30s want=%q got=%q\n" "$describe" "$want" "$got"
        fail=$((fail + 1))
    fi
}

echo "== env-var path (DARK_VERSION) — passthrough, v-prefix preserved =="
check_env "v2.9.2"                   "v2.9.2"
check_env "v2.9.2-alpha"             "v2.9.2-alpha"
check_env "v2.9.2-beta"              "v2.9.2-beta"
check_env "v2.9.2-rc.1"              "v2.9.2-rc.1"
check_env "v2.9.2-alpha-3-gabc1234"  "v2.9.2-alpha-3-gabc1234"
check_env "v2.9.2-alpha-dirty"       "v2.9.2-alpha-dirty"
check_env "v2.9.2-3-gabc1234-dirty"  "v2.9.2-3-gabc1234-dirty"

echo
echo "== git-describe path (mocked) — v-prefix stripped =="
check_git "v2.9.2"                   "2.9.2"
check_git "v2.9.2-alpha"             "2.9.2-alpha"
check_git "v2.9.2-beta"              "2.9.2-beta"
check_git "v2.9.2-rc.1"              "2.9.2-rc.1"
check_git "v2.9.2-3-gabc1234"        "2.9.2-3-gabc1234"
check_git "v2.9.2-alpha-3-gabc1234"  "2.9.2-alpha-3-gabc1234"
check_git "v2.9.2-alpha-dirty"       "2.9.2-alpha-dirty"
check_git "v2.9.2-3-gabc1234-dirty"  "2.9.2-3-gabc1234-dirty"
check_git "abc1234"                  "0.0.0-dev-abc1234"
check_git "v999.0.0-rc.42-7-gdeadbeef-dirty" "999.0.0-rc.42-7-gdeadbeef-dirty"

echo
echo "summary: pass=$pass fail=$fail"
rm -rf "$MOCKBIN"

if [[ $fail -gt 0 ]]; then
    exit 1
fi