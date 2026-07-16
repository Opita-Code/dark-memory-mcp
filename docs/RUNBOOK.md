# dark-memory-mcp — RUNBOOK

> **Audience**: Operator (human or automation) running `dark-mem-mcp` in
> production. Pairs with [INVARIANTS.md](./INVARIANTS.md) (the system
> contract) and [MIGRATION.md](./MIGRATION.md) (SQLite → Postgres).
>
> **Source of truth**: the executable contract is in
> `internal/store/` (Store interface) and `internal/server/` (MCP server).
> If this runbook contradicts the code, the code wins — open a PR to
> fix the runbook.

## 0. Five-minute overview

```
┌──────────────────────────────────────────────────────────────────────┐
│  harness (opencode / claude / MCPJam / …)                           │
│      │ MCP over stdio                                                 │
│      ▼                                                               │
│  dark-mem-mcp.exe   (binary; this repo, cmd/dark-mem-mcp/)            │
│      │ reads dark.db                                                  │
│      ▼                                                               │
│  SQLite (modernc.org/sqlite)   OR   Postgres (jackc/pgx/v5)          │
│                                                                      │
│  Sidecar binaries:                                                   │
│    dark-mem-cli      admin (migrate / vacuum / schema-status / …)    │
│    dark-mem-inspect  read-only diagnostic                            │
└──────────────────────────────────────────────────────────────────────┘
```

Both binaries share the same `dark.db` file (when using SQLite) or the
same Postgres database. Migrations live in
`internal/migrate/{sqlite,postgres}/ddl.go` and are applied
automatically when the Store opens.

---

## 1. First install (SQLite — recommended for solo dev / small teams)

```bash
# 1. Build the three binaries
cd dark-memory-mcp
go build -o ./bin/dark-mem-mcp   ./cmd/dark-mem-mcp
go build -o ./bin/dark-mem-cli    ./cmd/dark-mem-cli
go build -o ./bin/dark-mem-inspect ./cmd/dark-mem-inspect

# 2. Pick a DB path (default: ./dark.db in CWD; override with DARK_DB)
mkdir -p ~/.local/share/dark-memory-mcp
export DARK_DB=~/.local/share/dark-memory-mcp/dark.db

# 3. Optional: write driver config (CLI handles this)
./bin/dark-mem-cli set-driver --driver=sqlite --dsn=$DARK_DB

# 4. Migrations are applied on first open. Verify:
./bin/dark-mem-cli schema-status
# driver: sqlite
# schema_version: 8
# tables (16): ...

# 5. (optional) Diagnostic snapshot:
./bin/dark-mem-inspect
# dark-mem-inspect report
# =======================
# generated_at:       2026-07-15T...
# driver:             sqlite
# canary_present:     false
# ...

# 6. Run the MCP server (stdio; bind from your harness)
./bin/dark-mem-mcp
```

That's it. The harness (opencode, claude, MCPJam, anything MCP-native)
talks to `dark-mem-mcp` over stdio using the MCP protocol.

---

## 2. First install (Postgres — multi-agent / high concurrency)

```bash
# 1. Postgres up (any way you like — apt, docker, k8s, RDS, …)
docker run -d --name dark-pg -p 5432:5432 \
  -e POSTGRES_DB=dark_memory \
  -e POSTGRES_USER=dark \
  -e POSTGRES_PASSWORD=$(cat ~/.dark-pg-pass) \
  postgres:16

# 2. Build + export
cd dark-memory-mcp
go build -o ./bin/dark-mem-mcp ./cmd/dark-mem-mcp
export DARK_DB_DRIVER=postgres
export DARK_DB="postgres://dark:$(cat ~/.dark-pg-pass)@127.0.0.1:5432/dark_memory?sslmode=disable"

# 3. Verify schema
./bin/dark-mem-cli schema-status
# driver: postgres
# schema_version: 8
# tables (16): ...

# 4. Run
./bin/dark-mem-mcp
```

`dark-mem-cli` and `dark-mem-inspect` work identically against Postgres;
only the `DARK_DB_DRIVER=postgres` + libpq `DARK_DB` DSN differ.

---

## 3. Driver switch (SQLite → Postgres)

> ⚠️ **Two-phase migration**. Don't change `DARK_DB_DRIVER` until the
> new database has the schema applied. Migrations don't auto-copy
> rows — that's a separate one-shot copy (see [MIGRATION.md](./MIGRATION.md)).

```bash
# Phase 1 — bring up the new Postgres, apply schema (DARK_DB points at it)
export DARK_DB_DRIVER=postgres
export DARK_DB="postgres://...new-db..."
./bin/dark-mem-cli schema-status      # confirm schema_version: 8

# Phase 2 — stop the MCP server, then point at the new DB
# (operator-supplied row copy is your responsibility; see MIGRATION.md)
kill -TERM $(pgrep dark-mem-mcp)      # graceful shutdown

# Phase 3 — restart the MCP server with the new env
./bin/dark-mem-mcp &
```

**Rollback**: just flip `DARK_DB_DRIVER` and `DARK_DB` back. The
Postgres schema stays in place (no destructive changes during the
switch).

---

## 4. Vacuum policy (retention)

`dark-mem-cli vacuum` runs `Store.Vacuum(ctx, VacuumPolicy)`. SQLite
reclaims space (VACUUM); Postgres relies on autovacuum (the call is
a no-op there but still emits the per-table stats).

```bash
# Preview (no delete) — count rows older than 90 days
./bin/dark-mem-cli vacuum --days-old=90 --dry-run --json

# Actually delete + reclaim
./bin/dark-mem-cli vacuum --days-old=90

# Restrict to specific tables (e.g. write_audit only)
./bin/dark-mem-cli vacuum --tables=write_audit,sdd_evaluations --days-old=30
```

Default policy: `--days-old=0` (no time-based GC; only reclaims space).
For production, schedule a weekly cron:

```cron
# /etc/cron.d/dark-memory-vacuum
0 4 * * 0  /opt/dark-memory-mcp/bin/dark-mem-cli vacuum --days-old=180 --json | \
                  logger -t dark-vacuum
```

---

## 5. Migrations

```bash
# What's pending?
./bin/dark-mem-cli schema-status | grep applied

# Apply (idempotent — safe to re-run)
./bin/dark-mem-cli migrate

# JSON for ops dashboards
./bin/dark-mem-cli migrate --json
```

Schema migrations are versioned (`v1` … `v8` currently) and stored in
`internal/migrate/{sqlite,postgres}/ddl.go`. The `schema_migrations`
table tracks what's been applied. **Migrations never delete data.**

---

## 6. Read-only diagnostic

```bash
# Human-readable report (driver + version + tables + migrations +
# recent writes + canary + active constitution)
./bin/dark-mem-inspect

# JSON for alerting
./bin/dark-mem-inspect --json | jq .recent_writes[0:5]

# Deeper write history
./bin/dark-mem-inspect --depth=50
```

---

## 7. Operator audit queries (direct DB)

```sql
-- Last 10 writes
SELECT id, actor, session_id, write_path, created_at
FROM write_audit ORDER BY id DESC LIMIT 10;

-- Active sessions
SELECT session_id, operator, status, started_at
FROM sessions WHERE status = 'active';

-- Drift reports pending resolution
SELECT id, artifact_id, verdict, created_at
FROM vibe_drift_reports
WHERE reconciled_at IS NULL;

-- Per-table counts (mirror of memory_state output)
SELECT 'specs', COUNT(*) FROM vibe_specs
UNION ALL SELECT 'artifacts', COUNT(*) FROM vibe_artifacts
UNION ALL SELECT 'drifts', COUNT(*) FROM vibe_drift_reports
UNION ALL SELECT 'sdd_evals', COUNT(*) FROM sdd_evaluations;
```

---

## 8. Troubleshooting

### Server won't start: "ErrConstitutionDrift"

INV-4: constitution SHA in DB ≠ file on disk. To recover:

```bash
# Inspect: see what constitution is active
./bin/dark-mem-inspect | grep -i constitution

# Fix: reload the constitution (regenerates SHA + updates DB)
# (operator command — to be added in Wave 4+; today use the Library API)
```

### Slow writes

INV-1 is enabled by default — every write emits a `write_audit` row in
the same transaction. The audit insert is fast (single-row INSERT
on an indexed table), but on a heavily-loaded Postgres check:

```sql
SELECT * FROM pg_stat_user_tables
WHERE relname IN ('write_audit', 'vibe_drift_reports')
ORDER BY n_tup_ins DESC;
```

### "canary_present: true" in inspect

A user payload contained the canary token (INV-3 defensive
tripwire). The transaction was rolled back. The audit row records
`canary_present=true`. Inspect with:

```sql
SELECT id, table_name, actor, notes, created_at
FROM write_audit WHERE canary_present = 1
ORDER BY id DESC LIMIT 20;
```

---

## 9. Environment variables

| Var | Default | Purpose |
|---|---|---|
| `DARK_DB_DRIVER` | `sqlite` | `sqlite` or `postgres` |
| `DARK_DB` | `./dark.db` | DSN (file path or libpq URL) |
| `DARK_CACHE_DIR` | (same dir as DB) | LLM cache (INV-5) |
| `DARK_MOD_WHITELIST` | empty | CSV of allowed mod IDs (INV-6) |
| `DARK_SERVER_NAME` | `dark-memory-mcp` | serverInfo.name |
| `DARK_SERVER_VERSION` | `0.1.0` | serverInfo.version |
| `DARK_COEXISTENCE_GROUP` | `dark-agents/memory` | bridge.2 wire evidence |
| `DARK_HOME` | `~/.config/dark-memory-mcp` | base dir for `config.toml` |
| `DARK_TEST_POSTGRES_DSN` | (unset) | if set, dual-driver test runs Postgres too |

---

## 10. Exit codes (CLI)

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime error (DB unavailable, migration failed, IO) |
| 2 | usage error (unknown subcommand, invalid flag, bad driver) |

Scripts should check `$?` and treat 2 as "fix your invocation".

---

*See also: [INVARIANTS.md](./INVARIANTS.md) · [PERFORMANCE.md](./PERFORMANCE.md) · [MIGRATION.md](./MIGRATION.md) · [COEXISTENCE.md](./COEXISTENCE.md)*