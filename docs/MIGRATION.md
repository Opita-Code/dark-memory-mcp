# Migration — SQLite-only → Postgres

> **Audience**: operator moving from a single-host SQLite deployment
> to a multi-host Postgres deployment. **Two-phase**: (a) bring up the
> new DB + apply schema, (b) one-shot copy of rows. The dark-memory-mcp
> binary doesn't auto-migrate rows — that's your responsibility.

---

## Phase 0 — Pre-flight

Before touching either DB, capture a baseline:

```bash
# 1. Snapshot the current schema version + per-table counts
./dark-mem-cli schema-status --json > /tmp/source-schema.json
./dark-mem-inspect --json > /tmp/source-inspect.json

# 2. (recommended) Take a SQLite backup (file copy works — safe while
#    no writes are happening; for live, use sqlite3 .backup)
sqlite3 ~/.local/share/dark-memory-mcp/dark.db ".backup /tmp/dark.db.bak"

# 3. Stop the MCP server so no writes happen during the copy
kill -TERM $(pgrep dark-mem-mcp)
```

---

## Phase 1 — Bring up Postgres

### Option A: local docker (dev / staging)

```bash
docker run -d --name dark-pg \
  -e POSTGRES_DB=dark_memory \
  -e POSTGRES_USER=dark \
  -e POSTGRES_PASSWORD=$(openssl rand -base64 32 | tr -d '\n') \
  -p 5432:5432 \
  postgres:16

# Save the password somewhere safe (vault, sops, …)
echo "$POSTGRES_PASSWORD" > ~/.dark-pg-pass
chmod 600 ~/.dark-pg-pass
```

### Option B: managed Postgres (production)

Use your provider's standard procedure (RDS / Cloud SQL / Neon /
Supabase). Get the libpq URL:

```
postgres://dark:<password>@<host>:5432/dark_memory?sslmode=require
```

### Apply schema

```bash
export DARK_DB_DRIVER=postgres
export DARK_DB="postgres://dark:$(cat ~/.dark-pg-pass)@127.0.0.1:5432/dark_memory?sslmode=disable"

# Migrations are applied on first open. Verify:
./dark-mem-cli schema-status
# driver: postgres
# schema_version: 10
# tables (21): ...
```

If `schema_status` returns the same schema version as the SQLite
source (and the same tables), you're ready to copy rows.

---

## Phase 2 — Copy rows (one-shot)

`dark-memory-mcp` does not ship a row-copy tool (deliberate — row
migration is operator-policy territory). Pick the path that matches
your scale.

### Option A: small DB (< 100MB), pg_dump restore from SQLite export

```bash
# 1. Export the SQLite DB to a portable SQL dump
sqlite3 ~/.local/share/dark-memory-mcp/dark.db .dump > /tmp/dark.sql

# 2. Convert SQLite dialect to Postgres dialect (sqlite -> postgres
#    migration is mostly mechanical, with edge cases):
#   - INTEGER PRIMARY KEY AUTOINCREMENT -> SERIAL
#   - TEXT (no length) -> TEXT
#   - DATETIME -> TIMESTAMPTZ
#   - REAL -> DOUBLE PRECISION
#   - BLOB -> BYTEA
# Use `pgloader` for automated conversion (recommended):
pgloader /tmp/dark.sqlite postgres://dark:...@host/dark_memory

# 3. Verify counts match the source
./dark-mem-inspect --json | jq .counts
```

### Option B: medium DB (100MB - 10GB), per-table COPY

```bash
# 1. Per-table CSV export from SQLite
for table in research_runs research_items vibe_specs vibe_artifacts vibe_drift_reports sdd_evaluations write_audit sessions constitutions mods; do
  sqlite3 -header -csv ~/.local/share/dark-memory-mcp/dark.db \
    "SELECT * FROM $table;" > /tmp/$table.csv
done

# 2. Per-table COPY into Postgres
for table in research_runs research_items vibe_specs vibe_artifacts vibe_drift_reports sdd_evaluations write_audit sessions constitutions mods; do
  psql "$DARK_DB" -c "\COPY $table FROM '/tmp/$table.csv' WITH (FORMAT csv, HEADER true)"
done
```

### Option C: large DB (> 10GB), streaming replication

For deployments with hundreds of GB or more, set up streaming logical
replication from a Postgres source. Out of scope for this runbook —
contact the operator team.

---

## Phase 3 — Switch the server

```bash
# 1. Stop the SQLite-backed MCP server (already done in Phase 0)
# 2. Point the env at Postgres
export DARK_DB_DRIVER=postgres
export DARK_DB="postgres://dark:$(cat ~/.dark-pg-pass)@127.0.0.1:5432/dark_memory?sslmode=require"

# 3. Restart
./dark-mem-mcp &
```

### Verify

```bash
# Compare post-migration counts vs pre-migration baseline
./dark-mem-inspect --json > /tmp/post-inspect.json
diff <(jq -S .counts /tmp/source-inspect.json) <(jq -S .counts /tmp/post-inspect.json)
# (empty diff = counts match; small drift acceptable if writes happened
# during the copy)
```

### Rollback

If something goes wrong:

```bash
# Flip env back to SQLite
unset DARK_DB_DRIVER
export DARK_DB=~/.local/share/dark-memory-mcp/dark.db
./dark-mem-mcp &
```

The Postgres database stays in place (no destructive changes during
the switch — migrations don't delete data).

---

## Phase 4 — Hardening (recommended for production)

After a clean switch:

```sql
-- 1. Vacuum + analyze (Postgres side)
VACUUM ANALYZE;

-- 2. Indexes already exist (migration v7+). Verify:
SELECT schemaname, tablename, indexname FROM pg_indexes
WHERE schemaname = 'public' ORDER BY tablename, indexname;
-- Expect: 21 tables × ~3 indexes each (~60 indexes total)

-- 3. Connection pooling: use pgBouncer in front of Postgres for
--    >10 concurrent MCP servers
-- (deployment-specific; not part of dark-mem-mcp)

-- 4. Backups: pg_basebackup nightly + WAL archiving for PITR
-- (deployment-specific)
```

---

## Schema drift: what to do if source != destination

`dark-mem-cli schema-status` on both sides. If they differ:

```bash
# Show pending migrations on Postgres (it should say "nothing to apply"
# if schema_version matches source)
./dark-mem-cli schema-status

# If Postgres is BEHIND source: run migrate on Postgres
# (migrations are idempotent — safe to re-run)
export DARK_DB_DRIVER=postgres
./dark-mem-cli migrate

# If Postgres is AHEAD of source: rare; would require downgrading
# Postgres. The right fix is to keep source up-to-date. Document and
# open an issue.
```

---

## Common pitfalls

| Pitfall | Symptom | Fix |
|---|---|---|
| Foreign-key ordering wrong | COPY errors | Disable FK checks during COPY: `SET session_replication_role = replica;` then re-enable |
| `created_at` strings not parsed | TIMESTAMPTZ NULL on import | `SET datestyle = 'ISO, YMD';` before COPY |
| `project_id` defaults to 'default' | All rows share one project | Verify migration v7 ran: `\d+ vibe_specs` shows the column |
| Concurrent writes during COPY | Inconsistent counts | Phase 0 stops the MCP server before Phase 2 starts |
| Postgres auth | "password authentication failed" | Check `~/.pgpass` or DSN; verify `pg_hba.conf` |

---

*See also: [RUNBOOK.md](./RUNBOOK.md) · [PERFORMANCE.md](./PERFORMANCE.md) · [INVARIANTS.md](./INVARIANTS.md)*