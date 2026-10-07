#!/usr/bin/env bash
# measure-cold-start.sh — measure full boot time of dark-mem-mcp.
#
# Phase 19 / Loop 9 (T-411). Tier-1 sources: Brendan Gregg "USE Method",
# Go official diagnostics (go.dev/doc/diagnostics), Wilson Mar "Go
# Benchmarking" (median of N=5).
#
# What we measure: process exec → first MCP response for initialize +
# tools/list (the operator-visible "time-to-first-byte" for the harness).
#
# Run from the dark-memory-mcp root:
#
#   bash perf/scripts/measure-cold-start.sh [RUNS=5]
#
# Output: perf/cold-start-measurement-<UTC>.txt
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BIN="$REPO_ROOT/bin/dark-mem-mcp.exe"
OUTPUT_DIR="$REPO_ROOT/perf"
RUNS="${1:-${RUNS:-5}}"
mkdir -p "$OUTPUT_DIR"

if [ ! -x "$BIN" ]; then
    echo "FAIL: $BIN not found. Build with: make build" >&2
    exit 1
fi

# Compose an MCP initialize + tools/list call pair (JSON-RPC stdio).
INIT_JSON='{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}},"id":1}
{"jsonrpc":"2.0","method":"tools/list","id":2}
'

HMAC_KEY="v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
OUTPUT_FILE="$OUTPUT_DIR/cold-start-measurement-$(date -u +%Y%m%dT%H%M%SZ).txt"
: > "$OUTPUT_FILE"

echo "=== Cold-start measurement (T-411 / Loop 9) ==="
echo "binary:        $BIN"
echo "runs:          $RUNS"
echo "output:        $OUTPUT_FILE"
echo

# Helper: time a single boot via Python (subprocess + perf_counter).
# Python's perf_counter is monotonic + nanosecond resolution on
# Windows (matches QueryPerformanceCounter).
python_timed() {
        local dbpath="$1"
        python3 - "$dbpath" <<'PYEOF'
import os, sys, time, subprocess, tempfile

dbpath = sys.argv[1]
hmac_key = "v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
init_json = '{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}},"id":1}\n{"jsonrpc":"2.0","method":"tools/list","id":2}\n'

env = os.environ.copy()
env["DARK_DB_DRIVER"] = "sqlite"
env["DARK_DB_DSN"] = dbpath
env["DARK_AUDIT_HMAC_KEY"] = hmac_key
env["DARK_MEMORY_EMBEDDER"] = "none"
env["DARK_AUDIT_DIR"] = ""
env["DARK_TRUST_ROOTS_DIR"] = ""
env["DARK_FEDERATION_PEER_DSN"] = ""

# Resolve binary path (env var override first, then script-relative)
bin_path = os.environ.get("BENCH_DARK_MEM_MCP_BIN", "")
if not bin_path:
    # We can't use __file__ reliably from a heredoc; resolve from CWD.
    # The shell wrapper ensures CWD = repo root before invoking us.
    bin_path = os.path.abspath("bin/dark-mem-mcp.exe")
if not os.path.isfile(bin_path):
    print(f"FAIL: binary not found: {bin_path}", file=sys.stderr)
    sys.exit(1)

t0 = time.perf_counter()
proc = subprocess.Popen(
    [bin_path],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL,
    env=env,
)
try:
    stdout, _ = proc.communicate(input=init_json.encode("utf-8"), timeout=30)
except subprocess.TimeoutExpired:
    proc.kill()
    proc.wait()
    print(f"FAIL: timeout (30s)", file=sys.stderr)
    sys.exit(1)
t1 = time.perf_counter()

elapsed_ms = (t1 - t0) * 1000
print(f"{elapsed_ms:.1f}", end="")  # bare number, parsed below
PYEOF
}

declare -a MEASUREMENTS
echo "[boot-to-full-tools-list response]" | tee -a "$OUTPUT_FILE"
for i in $(seq 1 "$RUNS"); do
    DBPATH="/tmp/bench_cs_${$}_${RANDOM}_${i}.db"
    rm -f "${DBPATH}"* 2>/dev/null || true
    T_MS=$(python_timed "$DBPATH")
    rm -f "${DBPATH}"* 2>/dev/null || true
    MEASUREMENTS+=("$T_MS")
    printf "  run %d: %s ms\n" "$i" "$T_MS" | tee -a "$OUTPUT_FILE"
done

# Compute median
MEDIAN=$(printf '%s\n' "${MEASUREMENTS[@]}" | sort -n | awk -v n="${#MEASUREMENTS[@]}" 'NR==int((n+1)/2)')
MIN=$(printf '%s\n' "${MEASUREMENTS[@]}" | sort -n | head -1)
MAX=$(printf '%s\n' "${MEASUREMENTS[@]}" | sort -n | tail -1)
echo
echo "=== Summary (N=$RUNS) ==="
echo "  median: ${MEDIAN} ms"
echo "  min:    ${MIN} ms"
echo "  max:    ${MAX} ms"
echo
echo "Operator-visible boot budget = MEDIAN of full MCP initialize+tools/list round-trip"
echo "Reference: Loop 5 latency-budget.json drift_judge 9-step pipeline budget = 25s realistic p50"
echo "Comparison: this is the FRESH-DEPLOY scenario, NOT drift_judge pipeline" | tee -a "$OUTPUT_FILE"
echo
echo "Raw numbers in: $OUTPUT_FILE"