#!/usr/bin/env bash
# mut-test-short.sh — dark-memory mutation testing exec wrapper.
#
# go-mutesting's built-in exec runs `go test -timeout Ns <pkg>` with the
# FULL suite, no -short. For packages with heavy tests (e2e, dual_driver,
# embedder/onnx, tests that spawn real services) that means every mutant
# pays the full suite cost and some mutants hang `go test` on Windows.
#
# This wrapper follows the dark-testing skill v1.0.0:
#   - reads $MUTATE_PACKAGE from the go-mutesting env (NOT an argv arg)
#   - runs `go test -short -count=1 "$MUTATE_PACKAGE"` so mutation passes
#     only pay the unit-test layer (Layer 0 per the taxonomy)
#   - go-mutesting detects test failure via the exit code: non-zero
#     (tests failed) = mutant KILLED; zero (tests passed) = mutant ESCAPED
#
# Usage in a mutation command (slash-style flags, per the skill):
#   go-mutesting --config .go-mutesting.yml --blacklist .go-mutesting.blacklist \
#     /exec:"bash scripts/mut-test-short.sh" /exec-timeout:60 ./internal/atomic/...
set -euo pipefail

pkg="${MUTATE_PACKAGE:-}"
if [ -z "$pkg" ]; then
  echo "mut-test-short: MUTATE_PACKAGE is empty" >&2
  exit 1
fi

go test -short -count=1 -timeout 40s "$pkg"
