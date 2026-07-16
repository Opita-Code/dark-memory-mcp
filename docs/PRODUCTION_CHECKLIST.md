# Production Checklist — dark-memory-mcp v1.3.0+

This is the operator's runbook for what to check, what to do, and
when to panic. Every entry came from a real production incident.

---

## Boot-time signal matrix

When `dark-mem-mcp.exe` boots, the daemon prints a structured log
sequence on **stderr** (NOT stdout — stdout is for JSON-RPC only).
A healthy boot looks like:

```
dark-mem-mcp: boot step1 ok driver=sqlite dsn=<path>
dark-mem-mcp: boot step2 ok driver=sqlite
dark-mem-mcp: boot step3 ok migrations + constitution watchdog (driver=sqlite)
dark-mem-mcp: boot step4 ok canary installed (present=true)
dark-mem-mcp: registered 28 tools (canonical order)
dark-mem-mcp: serving stdio (server=dark-memory-mcp vX.Y.Z coexistence_group=dark-agents/memory)
```

| Step | What it signals | If it fails |
|---|---|---|
| step1 | Config parsed, DSN resolved | `defaultDSN` returned empty or unreadable — set `DARK_DB` env var |
| step2 | SQLite Store opened | Either file missing+locked, or v1.2.0+ DB schema corruption |
| step3 | Migrations applied + constitution watchdog OK | See "Migration recovery" below |
| step4 | Canary installed | Drifted constitution file (INV-4) — see INV-4 in docs/INVARIANTS.md |
| `registered 28 tools` | Tool registry sanity | If < 28, a new tool wasn't added to `CanonicalOrder()` in `internal/tools/registry.go`. v1.3.0 grew OBSERVABILITY 3→4 with health_ping. |
| `serving stdio (...)` | Ready for JSON-RPC | If absent, boot completed but stdio MCP transport didn't bind |

## Health probe (operator script)

```bash
# Healthy daemons respond 200 to a tools/list call on stdio. The
# harness's mcp-go sends the call; you can replay it with this one-liner.
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | dark-mem-mcp.exe
```

If the daemon responds with `{"id":2, "result":{"tools":[...]}}`,
it's healthy. If it responds with `error` or no response at all, jump to "Recovery".

## Health probe (`dark_memory_health_ping`) — v1.3.0

The canonical surface grew 27 → 28 with the addition of
`dark_memory_health_ping`. This is a **strict liveness probe**,
distinct from `dark_memory_memory_state`:

| Property | `health_ping` | `memory_state` |
|---|---|---|
| Latency budget | <500ms (target <50ms warm) | unbounded (does COUNT(*) on all tables) |
| Side effects | none | reads the audit bus indirectly |
| Audit-bus touch | NO | YES (memory state queries count writes) |
| Output shape | frozen `{server,db,runtime,registry,latency_ms,checked_at}` | per-table counters, debug-friendly |
| K8s liveness probe? | YES | NO |

Wire conformance is enforced by `tests/wire/health_ping_test.go`
(`TestWire_HealthPingShape` + `TestWire_HealthPingLatency`).

### Wiring into K8s liveness probe

```yaml
livenessProbe:
  exec:
    command:
      - /bin/sh
      - -c
      - >-
        printf '%s\n%s\n%s\n' \
          '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"k8s","version":"1"}}}' \
          '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
          '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dark_memory_health_ping","arguments":{}}}' \
          | /usr/local/bin/dark-mem-mcp
        # Check the response contains "live":true
  initialDelaySeconds: 5
  periodSeconds: 10
  timeoutSeconds: 2
  failureThreshold: 3
```

### Wiring into Prometheus blackbox exporter

```yaml
modules:
  mcp_health:
    prober: tcp
    timeout: 5s
    tcp:
      # The mcp exporter wraps stdio in HTTP; see the operator's
      # mcp-sidecar repo for the sidecar. health_ping is invoked
      # every scrape, latency_ms becomes a `mcp_health_latency_seconds`
      # metric, `db.live` becomes `up`.
```

### Failure modes

| Symptom | Likely cause | Recovery |
|---|---|---|
| `db.live=false` AND `ToolError.code=ErrDBUnreachable` | DB file missing/locked/corrupt | See R-2 below |
| `runtime.uptime_seconds < 0` or `0.000` | boot marker never printed (binary hanging at startup) | Check stderr for the boot signal matrix; binary hasn't reached `serving stdio` yet |
| `registry.canonical_tools != 28` | binary is a v1.2.x build (26) or v1.3.0 with a tool removed | Rebuild from main; check `$DARK_MEM_MCP_BIN` points at the fresh binary |
| `latency_ms > 500` | blocking call added to the hot path | file a bug with the wire trace; `tests/wire/TestWire_HealthPingLatency` should fire on regression |

## Race detector availability

The Go race detector (`go test -race`) requires a C compiler. On
this dev host **no gcc is installed**, so `-race` is not runnable
here. The race detector's coverage is therefore substituted by:

1. **`tests/wire/TestWire_HealthPingLatency`** — fires 5 sequential
   calls back-to-back and asserts the worst-of-5 roundtrip stays
   under 500ms. Detects accidental perf regressions / blocking I/O
   that would slow the hot path.
2. **`tests/e2e/server_test.go`** — fires 1000 concurrent
   calls under the SQLite WAL regime. Detects "database is locked"
   storms and deadlocks under load.
3. **`tests/orchestration/`** — high-volume parallel orchestrator
   calls exercising the WriteContext + audit path.

If your environment HAS gcc (Linux, macOS, MinGW on Windows), run
the full race-detector sweep before publishing:

```bash
CGO_ENABLED=1 CC=gcc go test -race ./... -count=1 -timeout 240s
```

The `-race` results are CI's responsibility on environments with a
C compiler. The never-push policy means CI is local-only — see
`.github/workflows/ci.yml` for the operator-reproducible recipe.

## Stale-binary gotcha (wire-test resolution)

The wire tests (`tests/wire/`) need a binary. The harness's
`resolveWireBin` resolution order:

1. `$env:DARK_MEM_MCP_BIN` (always preferred; set this in CI)
2. `../cmd/dark-mem-mcp/dark-mem-mcp.exe` (the canonical build)
3. `dark-mem-mcp.exe` on PATH or in cwd (fallback)

**Gotcha:** a leftover stale binary at the repo root
(`dark-mem-mcp.exe` from a previous build) will be picked up by
the cwd-fallback path. Symptom: `TestWire_HealthPingShape` reports
empty fields because the stale binary doesn't have `SetRuntimeContext`.

Mitigation:

- **Always rebuild into `cmd/dark-mem-mcp/`** — never the repo root.
- **Always set `DARK_MEM_MCP_BIN`** when running wire tests:
  ```powershell
  $env:DARK_MEM_MCP_BIN = "$PWD/cmd/dark-mem-mcp/dark-mem-mcp.exe"
  ```
- If a stale binary exists at the repo root, **delete it before
  running wire tests**. The `go build` command never writes here
  by default; the file is operator-side residue from a previous
  layout (pre-INV-8 default was the repo root).

## Recovery playbooks

### R-1. v8 / vec0 triggers block the boot

**Symptom:** stderr contains:
```
dark-mem-mcp: server.New failed: server.Boot step2 (runtime.Open): migrate: v8 up: stmt[N]: SQL logic error: no such module: vec0
```

**Cause:** orphan triggers from a previous sqlite-vec install still
reference the unloaded `vec0` module. The boot crashes before
reaching step4.

**Fix:** drop the orphan triggers + the vec0 virtual tables.
```sql
-- via sqlite3 (paths below assume the operator's canonical layout;
-- substitute $DARK_DB if the operator overrode it)
DELETE FROM sqlite_master
WHERE type='trigger' AND name LIKE '%_vec_%';
DROP TABLE IF EXISTS research_items_vec;
DROP TABLE IF EXISTS vibe_specs_vec;
DROP TABLE IF EXISTS vibe_artifacts_vec;
```
Then restart the daemon; v8 will re-create the tables via the
vibe_brands migration. F37+F39 will tolerate the rest.

After the fix, run the unit tests for `tests/migrate` to confirm:
```
go test ./tests/migrate/... -count=1
```

### R-2. dark-memory.db schema version behind

**Symptom:** `dark_memory_admin_schema_status` returns schema_version
< 10 even after a fresh binary boot. OR `dark_memory_admin_schema_status` reports
applied=true for v10 but the underlying table is missing.

**Cause:** schema_migrations bookkeeping got out of sync with the
actual DB state. Most common causes:
- `dark-memory.db` was created by a an older dark-research build build
  (which shares `schema_migrations` rows but with different SQL).
- operator manually deleted a migration row while the table still
  existed (or vice versa).

**Fix:** re-bootstrap. The destructive path:
```powershell
# Stop all dark-mem-mcp instances first.
Get-Process dark-mem-mcp | Stop-Process -Force

# Archive the corrupted DB.
Move-Item "C:\Users\Nico\AppData\Local\dark-agents\dark-memory.db" \
          "C:\Users\Nico\AppData\Local\dark-agents\dark-memory.db.bak-$(Get-Date -Format yyyyMMdd)"

# Restart the daemon. It creates a fresh DB at the configured DSN.
dark-mem-mcp.exe
```

The recent darks are in `dark-research.db` (dark-research.db) — that
one is preserved in the same directory. Don't touch it.

### R-3. tools/call returns ErrInvalidArgument with Field=tasks

**Symptom:** the harness emits tool calls with `tasks` and the server
returns:
```
{"error":{"code":"ErrInvalidArgument",
          "message":"invalid argument at field=tasks"}}
```

**Cause:** the LLM running this session is emitting `tasks` in a shape
the orchestrator can't parse (not a JSON array, not a JSON string).
This happens when the LLM follows an older prompt template that
nests tasks differently (e.g. inside a wrapper object).

**Fix:** post-F36 we accept BOTH:
- `tasks: [{...}, {...}]` (canonical JSON array)
- `tasks: "[{...},{...}]"` (JSON-encoded string)

If you're seeing Field=tasks ERRORS in production after v1.2.1:
1. Verify the daemon IS v1.2.1+ (`dark-mem-mcp: serving stdio (...)`
   shows the version in the banner).
2. If running an older binary, restart after upgrading.
3. If running v1.2.1+ and still seeing this error, file a bug with
   the actual JSON payload that fails (it must be a shape we don't
   cover).

### R-4. LLM emits the wrong tasks shape 100% of the time

**Symptom:** all vibe_spec calls fail with Field=tasks despite being
v1.2.1+.

**Cause:** the LLM's prompt template (AGENTS.md or system prompt)
is hard-coding one shape. The harness's tool-use layer doesn't
adapt.

**Fix:** update the system prompt to instruct the LLM:
- "Use the dark_memory_vibe_spec tool. The `tasks` argument accepts a
  JSON array OR a JSON-encoded string of an array. Pass the array
  form whenever possible."

If the operator can't reach the LLM's prompt, an interim workaround
is to define a wrapper tool in `AGENTS.md` that converts array→string
before calling the underlying tool.

## Migration recovery (deep dive)

The dark-memory.db migration runner (F37-F40) tolerates four error
classes. The errors it SURFACES are the truly bad ones:

| Error class | Runner treats | Operator action |
|---|---|---|
| `duplicate column name: X` | tolerated (F37) | none |
| `no such module: X` | tolerated (F39) | investigate if X is `vec0` (R-1) |
| `table X already exists` | tolerated (F40) | none |
| `no such table: Y` | FATAL | seed has missing Y; see R-1 if Y=vibe_brands_old |

If the daemon crashes at `migrate: vN up: stmt[M]: no such table: Y`:
1. Stop the daemon.
2. Open the DB in sqlite3.
3. Compare actual tables to the expected schema (see
   `docs/OPERATIONS.md` for the canonical schema).
4. Re-create missing tables via `CREATE TABLE IF NOT EXISTS ...` (the
   statements from `migrations/0001_initial.sql` etc. are your
   friend).
5. Restart the daemon; F38 will materialise any tables that the
   migrations expected to create.

## Dark-research vs dark-memory isolation (INV-8)

Each MCP owns its own DB. If dark-memory-mcp somehow starts writing
to `dark.db` (the sibling server's file), you have a shared-write
hazard — that's what INV-8 prevents.

Verification:
```powershell
# 1. What DB is dark-memory-mcp writing to?
$proc = Get-Process dark-mem-mcp | Select-Object -First 1
$env = (Get-CimInstance Win32_Process -Filter "ProcessId=$($proc.Id)").CommandLine
# Look for DARK_DB=<path>; if missing, the daemon is on its default.
# Default must be dark-memory.db, NOT dark.db (INV-8 verification).
```

The `tests/wire/TestWire_INV8_DefaultDSNRespectsIsolation` test
guards against the default ever falling back to `dark.db`.

## Logging best practices

The daemon writes **all** operational logs to stderr; stdout is
strictly JSON-RPC. When reading logs:

```
# Filter to daemon bootstrap + lifecycle only:
dark-mem-mcp 2>&1 1>/dev/null | grep -E 'dark-mem-mcp:'

# Filter to errors only:
dark-mem-mcp 2>&1 1>/dev/null | grep -iE 'failed|warn|error'
```

The daemon uses structured log lines (`key=value` pairs) where
possible. A future release will switch fully to slog JSON.

## Performance baseline (v1.3.0+ on this hardware)

If the operator notices degradation, compare against these:

* **Boot time:** 1.5s (cold cache, fresh DB), 0.4s (warm cache)
* **tools/list response:** < 50ms
* **dark_memory_vibe_spec with 100 tasks:** < 80ms (orchestrator only), < 200ms (with persist + audit)
* **Session table growth:** ~30 bytes/session on disk (with audit)

Numbers above are targets. If your actual measurements are >5x off,
file a bug.

## Operator's one-page cheat sheet

```
# Is the daemon alive?
Get-Process dark-mem-mcp

# What version?
dark-mem-mcp: serving stdio (... vX.Y.Z ...)

# What DB?
dark-mem-mcp: boot step1 ok driver=sqlite dsn=<path>

# Did migrations finish?
dark-mem-mcp: boot step3 ok migrations + constitution watchdog

# Bounce the daemon (operator restart, no data loss):
Get-Process dark-mem-mcp | Stop-Process -Force
opencode   # or whatever launches the MCP

# Run wire tests against a CI-built binary:
$DARK_MEM_MCP_BIN=.\dark-mem-mcp.exe go test ./tests/wire/... -v

# Verify INV-8 (operator-mandated default):
select * from schema_migrations where version >= 7; -- should show at least v7
# If dark.db instead of dark-memory.db, run R-2.
```
