#!/usr/bin/env bash
# measure-throughput.sh — measure per-tool throughput with N workers.
#
# Phase 19 / Loop 9 (T-411). Tier-1 source: Brendan Gregg "USE Method",
# Wilson Mar "Go Benchmarking".
#
# What we measure: how long it takes to issue M tool calls across W
# concurrent workers. Per-tool latency = total / M. Throughput = M / total.
#
# We use the read-only `dark_memory_health_ping` tool (cheap, no DB
# write, no LLM call) so we isolate the Go-MCP dispatch + handler
# overhead from any other moving parts.
#
# Run from the dark-memory-mcp root:
#
#   bash perf/scripts/measure-throughput.sh [WORKERS=1,5,10]
#
# Output: perf/throughput-measurement-<UTC>.txt
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BIN="$REPO_ROOT/bin/dark-mem-mcp.exe"
OUTPUT_DIR="$REPO_ROOT/perf"
WORKERS_LIST="${1:-${WORKERS:-1 5 10}}"
CALLS_PER_WORKER="${CALLS_PER_WORKER:-50}"
mkdir -p "$OUTPUT_DIR"

if [ ! -x "$BIN" ]; then
    echo "FAIL: $BIN not found." >&2
    exit 1
fi

HMAC_KEY="v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
OUTPUT_FILE="$OUTPUT_DIR/throughput-measurement-$(date -u +%Y%m%dT%H%M%SZ).txt"
: > "$OUTPUT_FILE"

# Compose the MCP call sequence: 1 initialize + 1 health_ping + N
# additional health_ping calls (per worker).
# Worker call: health_ping takes no arguments.
HEALTH_PING_REQ='{"jsonrpc":"2.0","method":"tools/call","params":{"name":"dark_memory_health_ping","arguments":{}},"id":2}
'

python_throughput() {
    local dbpath="$1"
    local workers="$2"
    local calls="$3"
    python3 - "$dbpath" "$workers" "$calls" <<'PYEOF'
import os, sys, time, subprocess, threading, queue

dbpath, workers_str, calls_str = sys.argv[1], sys.argv[2], sys.argv[3]
workers = int(workers_str)
calls_per_worker = int(calls_str)

hmac_key = "v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
init_req = '{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}},"id":1}\n'
call_req = '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"dark_memory_health_ping","arguments":{}},"id":2}\n'

env = os.environ.copy()
env["DARK_DB_DRIVER"] = "sqlite"
env["DARK_DB_DSN"] = dbpath
env["DARK_AUDIT_HMAC_KEY"] = hmac_key
env["DARK_MEMORY_EMBEDDER"] = "none"
env["DARK_AUDIT_DIR"] = ""
env["DARK_TRUST_ROOTS_DIR"] = ""
env["DARK_FEDERATION_PEER_DSN"] = ""

bin_path = os.environ.get("BENCH_DARK_MEM_MCP_BIN", "") or os.path.abspath("bin/dark-mem-mcp.exe")
if not os.path.isfile(bin_path):
    print(f"FAIL: {bin_path} not found", file=sys.stderr)
    sys.exit(1)

# Each worker spawns its OWN process to actually get concurrency on Windows
# (stdin/stdout pipes are per-process). This is what a real MCP client
# looks like — N clients, one server each.
def worker_thread(worker_id: int, init_req: str, call_req: str, calls: int) -> float:
    proc = subprocess.Popen(
        [bin_path],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        env=env,
    )
    try:
        # Build all the requests at once: init + N calls
        all_input = init_req + (call_req * calls)
        t0 = time.perf_counter()
        stdout, _ = proc.communicate(input=all_input.encode("utf-8"), timeout=120)
        t1 = time.perf_counter()
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
        return float("inf")
    # Count the number of successful JSON-RPC responses (each starts with "{")
    resp_count = stdout.count(b'"jsonrpc":"2.0"')
    elapsed_s = t1 - t0
    return elapsed_s, resp_count

# Launch N workers in parallel
results = []
threads = []
results_lock = threading.Lock()

def run_worker(wid):
    res = worker_thread(wid, init_req, call_req, calls_per_worker)
    with results_lock:
        results.append(res)

t0 = time.perf_counter()
threads = [threading.Thread(target=run_worker, args=(i,)) for i in range(workers)]
for t in threads:
    t.start()
for t in threads:
    t.join()
t_total = time.perf_counter() - t0

# Aggregate per-tool latency: total wall-time of slowest worker
sum_lat = sum(r[0] for r in results if isinstance(r, tuple))
total_responses = sum(r[1] for r in results if isinstance(r, tuple))
total_calls = total_responses - workers  # subtract 1 init per worker

if total_calls <= 0:
    print("FAIL: no calls recorded", file=sys.stderr)
    sys.exit(1)

# Each worker does calls_per_worker calls. Total = workers * calls_per_worker
expected_calls = workers * calls_per_worker
per_call_ms = (sum_lat / expected_calls) * 1000
throughput_rps = expected_calls / sum_lat

# Print: workers|calls|total_seconds|per_call_ms|throughput_rps
print(f"{workers}|{expected_calls}|{sum_lat:.3f}|{per_call_ms:.1f}|{throughput_rps:.1f}")
PYEOF
}

echo "=== Throughput measurement (T-411 / Loop 9) ==="
echo "binary:           $BIN"
echo "workers:          $WORKERS_LIST"
echo "calls/worker:     $CALLS_PER_WORKER"
echo "tool under test:  dark_memory_health_ping (read-only, no LLM)"
echo "output:           $OUTPUT_FILE"
echo
echo "[results: workers | calls | total_s | per_call_ms | rps]"
echo "workers | calls | total_s | per_call_ms | rps" | tee -a "$OUTPUT_FILE"
for w in $WORKERS_LIST; do
    DBPATH="/tmp/bench_th_${$}_${RANDOM}_w${w}.db"
    rm -f "${DBPATH}"* 2>/dev/null || true
    RESULT=$(python_throughput "$DBPATH" "$w" "$CALLS_PER_WORKER")
    rm -f "${DBPATH}"* 2>/dev/null || true
    printf "  %s\n" "$RESULT" | tee -a "$OUTPUT_FILE"
done
echo
echo "Reference: Phase 14 row 2472 (atomic-mirror) measured"
echo "  sequential p50 = 2576ms, p95 = 4742ms, p99 = 18522ms (1000 calls)"
echo "  concurrent 5-worker p50 = 2879ms, p95 = 9840ms, p99 = 15504ms"
echo
echo "Our measurement isolates the tool dispatch path (no LLM, no write)"
echo "Raw numbers in: $OUTPUT_FILE"