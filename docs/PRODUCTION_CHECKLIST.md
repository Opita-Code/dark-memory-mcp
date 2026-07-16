# Production Checklist — dark-memory-mcp v1.2.5+

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
dark-mem-mcp: registered 27 tools (canonical order)
dark-mem-mcp: serving stdio (server=dark-memory-mcp vX.Y.Z coexistence_group=dark-agents/memory)
```

| Step | What it signals | If it fails |
|---|---|---|
| step1 | Config parsed, DSN resolved | `defaultDSN` returned empty or unreadable — set `DARK_DB` env var |
| step2 | SQLite Store opened | Either file missing+locked, or v1.2.0+ DB schema corruption |
| step3 | Migrations applied + constitution watchdog OK | See "Migration recovery" below |
| step4 | Canary installed | Drifted constitution file (INV-4) — see INV-4 in docs/INVARIANTS.md |
| `registered 27 tools` | Tool registry sanity | If < 27, a new tool wasn't added to `CanonicalOrder()` in `internal/tools/registry.go` |
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

## Performance baseline (v1.2.5+ on this hardware)

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
