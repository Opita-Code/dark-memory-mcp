#!/usr/bin/env bash
# tests/decision-criteria/check.sh — TDD checker for spec 1268
# Verifies that the 3 dark-agent skills contain the decision-criteria
# derived from the gap-analysis (spec 1268, 4 angle sub-agents).
#
# RED phase: most tests should FAIL today (current skills lack criteria).
# GREEN phase (after T5): all tests should PASS.
#
# Usage: bash tests/decision-criteria/check.sh
# Exit code: 0 = all pass, 1 = at least one fail.

set -uo pipefail

# --- Configuration ---
DARK_MEMORY_SKILL="$HOME/.config/opencode/skills/dark-memory/SKILL.md"
DARK_COPILOT_SKILL="$HOME/.config/opencode/skills/dark-copilot/SKILL.md"
DARK_RESEARCH_SKILL="$HOME/.config/opencode/skills/dark-research/SKILL.md"

PASS=0
FAIL=0
FAILED_TESTS=()

# --- Helpers ---
assert_grep() {
    local file="$1"
    local pattern="$2"
    local description="$3"
    if grep -qE "$pattern" "$file"; then
        printf "  PASS: %s\n" "$description"
        PASS=$((PASS + 1))
    else
        printf "  FAIL: %s (pattern not found: %s)\n" "$description" "$pattern"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$description")
    fi
}

assert_not_grep() {
    local file="$1"
    local pattern="$2"
    local description="$3"
    if ! grep -qE "$pattern" "$file"; then
        printf "  PASS: %s\n" "$description"
        PASS=$((PASS + 1))
    else
        printf "  FAIL: %s (anti-pattern found: %s)\n" "$description" "$pattern"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$description")
    fi
}

section() {
    printf "\n=== %s ===\n" "$1"
}

# --- Test 1: tool-selection (dark-research) ---
section "Test 1: tool-selection — dark-research routes CVE-YYYY-NNNN to dark_research_cve, NOT dark_research_web"
assert_grep "$DARK_RESEARCH_SKILL" "dark_research_cve" \
    "dark-research skill mentions dark_research_cve"
assert_grep "$DARK_RESEARCH_SKILL" "CVE lookup" \
    "dark-research skill has CVE lookup decision tree row"
assert_grep "$DARK_RESEARCH_SKILL" "OSV.dev" \
    "dark-research skill lists OSV.dev as tier-1 source"

# --- Test 2: anti-deliracion (dark-research) ---
section "Test 2: anti-deliracion — dark-research documents at least 5 of 6 triggers with tier-1 sources"
assert_grep "$DARK_RESEARCH_SKILL" "version" \
    "dark-research skill mentions version verification trigger"
assert_grep "$DARK_RESEARCH_SKILL" "CVE" \
    "dark-research skill mentions CVE attribution trigger"
assert_grep "$DARK_RESEARCH_SKILL" "NVD" \
    "dark-research skill lists NVD as tier-1 source for CVE"
assert_grep "$DARK_RESEARCH_SKILL" "vendor" \
    "dark-research skill mentions vendor advisory as tier-1 source"

# --- Test 3: drift-handling (dark-memory) ---
section "Test 3: drift-handling — dark-memory documents aligned/drift_detected/needs_human + P0/P1/P2 priority"
assert_grep "$DARK_MEMORY_SKILL" "drift_detected" \
    "dark-memory mentions drift_detected verdict"
assert_grep "$DARK_MEMORY_SKILL" "needs_human" \
    "dark-memory mentions needs_human verdict"
assert_grep "$DARK_MEMORY_SKILL" "aligned" \
    "dark-memory mentions aligned verdict"
assert_grep "$DARK_MEMORY_SKILL" "P0" \
    "dark-memory mentions P0 priority routing"
assert_grep "$DARK_MEMORY_SKILL" "P1" \
    "dark-memory mentions P1 priority routing"
assert_grep "$DARK_MEMORY_SKILL" "P2" \
    "dark-memory mentions P2 priority routing"
assert_grep "$DARK_MEMORY_SKILL" "Error Observatory" \
    "dark-memory cross-references Error Observatory for judge-fail chain"

# --- Test 4: memory-curation (dark-memory) ---
section "Test 4: memory-curation — dark-memory documents 7 kinds + scope=operator vs scope=agent"
assert_grep "$DARK_MEMORY_SKILL" "decision" \
    "dark-memory mentions decision kind"
assert_grep "$DARK_MEMORY_SKILL" "finding" \
    "dark-memory mentions finding kind"
assert_grep "$DARK_MEMORY_SKILL" "observation" \
    "dark-memory mentions observation kind"
assert_grep "$DARK_MEMORY_SKILL" "todo" \
    "dark-memory mentions todo kind"
assert_grep "$DARK_MEMORY_SKILL" "link" \
    "dark-memory mentions link kind"
assert_grep "$DARK_MEMORY_SKILL" "context" \
    "dark-memory mentions context kind"
assert_grep "$DARK_MEMORY_SKILL" "scope.*operator" \
    "dark-memory mentions scope=operator visibility"
assert_grep "$DARK_MEMORY_SKILL" "memory_type" \
    "dark-memory mentions memory_type (episodic/semantic/procedural)"

# --- Test 5: when-NOT-to-use (dark-memory) ---
section "Test 5: when NOT to use dark-memory — dark-memory has a section for trivial-task bypass"
assert_grep "$DARK_MEMORY_SKILL" "When NOT to use|when not to use|skip dark-memory" \
    "dark-memory has a section for when NOT to use the MCP"

# --- Test 6: dark-copilot tier ladder ---
section "Test 6: dark-copilot tier ladder — documents T0-T5 escalation triggers"
assert_grep "$DARK_COPILOT_SKILL" "T0.*Lifecycle|Lifecycle.*T0" \
    "dark-copilot defines T0 lifecycle tier"
assert_grep "$DARK_COPILOT_SKILL" "T5.*Escape|Escape.*T5" \
    "dark-copilot defines T5 escape-hatch tier"
assert_grep "$DARK_COPILOT_SKILL" "analyze_screenshot" \
    "dark-copilot mentions analyze_screenshot (vision)"

# --- Test 7: version awareness ---
section "Test 7: version awareness — dark-memory header says target_version=2.15.5"
assert_grep "$DARK_MEMORY_SKILL" "target_version.*2.15.5" \
    "dark-memory skill header pin target_version=2.15.5"

# --- Test 8: tool count ---
section "Test 8: tool/namespace count — dark-memory skill says 57 tools + 17 namespaces (not 52/16)"
assert_grep "$DARK_MEMORY_SKILL" "57.*tools|57 canonical" \
    "dark-memory skill mentions 57 tools"
assert_grep "$DARK_MEMORY_SKILL" "17.*namespaces" \
    "dark-memory skill mentions 17 namespaces"

# --- Test 9: dark-copilot cross-ref (dark-memory) ---
section "Test 9: dark-memory cross-ref dark-copilot (39 tools, not 35)"
assert_grep "$DARK_MEMORY_SKILL" "39 tools" \
    "dark-memory mentions dark-copilot has 39 tools (not 35)"

# --- Test 10: dark-research tier-1 source rule ---
section "Test 10: dark-research enforces tier-1 sourcing (no news-as-authority for CVE/version)"
assert_grep "$DARK_RESEARCH_SKILL" "tier-1|tier 1" \
    "dark-research skill enforces tier-1 sourcing"
assert_grep "$DARK_RESEARCH_SKILL" "trail color" \
    "dark-research skill calls news 'trail color only'"

# --- Summary ---
printf "\n=== SUMMARY ===\n"
printf "PASS: %d\n" "$PASS"
printf "FAIL: %d\n" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
    printf "FAILED TESTS:\n"
    for t in "${FAILED_TESTS[@]}"; do
        printf "  - %s\n" "$t"
    done
    exit 1
fi
printf "ALL GREEN — spec 1268 TDD complete.\n"
exit 0
