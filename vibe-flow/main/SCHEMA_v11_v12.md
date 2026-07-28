# Schema Design — v11 + v12 Migrations (Wave 5A.iii + 5E.ii)

**Version**: 1.0.0 (design only — not yet applied)
**Status**: Draft → pending implementation in Waves 5A.iii and 5E.ii
**Supersedes**: the legacy sessions schema (v1-v10 schema_migrations) for the lifecycle and frames dimensions
**Source specs**: ACTIVE_MEMORY_RFC.md §5 (INV-8/9) + §6.2 (Wave 5A.iii + 5E.ii)
**Source constitution**: dark-agents/dark-memory-mcp-cerebro@1.0.0
**Date**: 2026-07-19

---

## 1. Overview

Two migrations, kept separate so they rollback independently:

- **v11** (Wave 5A.iii) — **additive** — adds `vibe_frames` + `vibe_recall_subscriptions`. No data migration; existing reads/writes continue unchanged.
- **v12** (Wave 5E.ii) — **destructive-but-rebuild** — rewrites the `sessions` table to support 5 lifecycle states + adds columns for parent_session_id / resurrected_from / last_heartbeat_at. Backfills any legacy `status='open'` rows to `status='closed_aborted'`.

**Sequence**: a clean dark.db at v10 upgrades to v11 first (additive, safe), then to v12 (destructive on sessions). Failure to apply v12 cleanly leaves the DB at v11 + partially-migrated sessions; per-store rollback to v11 is supported via the DOWN migration of v12.

Both migrations are **dual-driver**: same UP/DOWN shapes for SQLite (`internal/migrate/sqlite/ddl.go`) and Postgres (`internal/migrate/postgres/ddl.go`); driver-specific dialect differences are minimal (boolean handling, default timestamps).

---

## 2. Migration v11 — Wave 5A.iii (additive)

### 2.1 New tables

```sql
-- v11/vibe_frames.sql
CREATE TABLE IF NOT EXISTS vibe_frames (
  id             INTEGER PRIMARY KEY,                 -- SQLite; SERIAL for Postgres
  project_id     TEXT    NOT NULL,                    -- INV-7: explicit project_id; tag from active session
  session_id     TEXT    NOT NULL,
  scope_level    TEXT    NOT NULL CHECK (scope_level IN
                   ('global','project','session','call')),
  scope_id       TEXT    NOT NULL,                    -- project_id for project-level, etc.
  frame_kind     TEXT    NOT NULL CHECK (frame_kind IN
                   ('identity','scope','evidence','capabilities','drift','persona')),
  composed_at    TEXT    NOT NULL,                    -- RFC3339Nano
  expires_at     TEXT    NOT NULL,                    -- RFC3339Nano; cache TTL
  frame_json     TEXT    NOT NULL,                    -- canonical JSON of the frame
  content_sha256 TEXT    NOT NULL,                    -- INV-5 cache integrity
  last_write_id  INTEGER NOT NULL DEFAULT 0,          -- invalidation cursor
                   -- pointer into write_audit max(id) at compose time
  created_at     TEXT    NOT NULL                     -- RFC3339Nano
);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_scope_lookup
  ON vibe_frames (session_id, scope_level, scope_id, frame_kind, expires_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_project
  ON vibe_frames (project_id, frame_kind, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_invalidation
  ON vibe_frames (session_id, last_write_id)
  WHERE scope_level != 'global';
  -- Postgres variant: CREATE INDEX ... WHERE ...;
```

### 2.2 Second new table (recall subscriptions)

```sql
-- v11/vibe_recall_subscriptions.sql
CREATE TABLE IF NOT EXISTS vibe_recall_subscriptions (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,                   -- INV-7: explicit project_id
  session_id      TEXT    NOT NULL,
  scope_level     TEXT    NOT NULL CHECK (scope_level IN
                    ('global','project','session','call')),
  scope_id        TEXT    NOT NULL,
  last_seen_token INTEGER NOT NULL DEFAULT 0,         -- write_audit.max(id) at last recall
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL,
  UNIQUE (session_id, scope_level, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_recall_subs_lookup
  ON vibe_recall_subscriptions (session_id, scope_level, scope_id);

CREATE INDEX IF NOT EXISTS idx_recall_subs_project
  ON vibe_recall_subscriptions (project_id, scope_level);
```

### 2.3 Reused invariances

- **INV-1 (write audit)**: every SaveFrame and UpdateRecallSubscription emits a `write_audit` row with `actor=atomic_save_frame` or `actor=recall_subscription_update`.
- **INV-5 (cache integrity)**: `vibe_frames.content_sha256` is verified on every Get; mismatch treated as cache miss + `anomaly` event emitted. The frame is re-composed on miss.
- **INV-7 (project namespace)**: every SaveFrame carries `project_id` (denormalized from session); reads filter by project_id unless explicitly cross-project.

### 2.4 UP migration (SQLite)

```sql
-- v11 up
CREATE TABLE IF NOT EXISTS vibe_frames (
  id             INTEGER PRIMARY KEY,
  project_id     TEXT    NOT NULL,                    -- INV-7
  session_id     TEXT    NOT NULL,
  scope_level    TEXT    NOT NULL CHECK (scope_level IN
                   ('global','project','session','call')),
  scope_id       TEXT    NOT NULL,
  frame_kind     TEXT    NOT NULL CHECK (frame_kind IN
                   ('identity','scope','evidence','capabilities','drift','persona')),
  composed_at    TEXT    NOT NULL,
  expires_at     TEXT    NOT NULL,
  frame_json     TEXT    NOT NULL,
  content_sha256 TEXT    NOT NULL,
  last_write_id  INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_scope_lookup
  ON vibe_frames (session_id, scope_level, scope_id, frame_kind, expires_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_project
  ON vibe_frames (project_id, frame_kind, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_vibe_frames_invalidation
  ON vibe_frames (session_id, last_write_id);

CREATE TABLE IF NOT EXISTS vibe_recall_subscriptions (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,                   -- INV-7
  session_id      TEXT    NOT NULL,
  scope_level     TEXT    NOT NULL CHECK (scope_level IN
                    ('global','project','session','call')),
  scope_id        TEXT    NOT NULL,
  last_seen_token INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL,
  UNIQUE (session_id, scope_level, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_recall_subs_lookup
  ON vibe_recall_subscriptions (session_id, scope_level, scope_id);

CREATE INDEX IF NOT EXISTS idx_recall_subs_project
  ON vibe_recall_subscriptions (project_id, scope_level);

PRAGMA user_version = 11;
```

The new `project_id` columns are populated at insert time by the application layer from the session's active project_id (resolved via `sessions.project_id` lookup or carried in the WriteContext). Reads filter by `project_id` directly without the join, satisfying INV-7 explicitly rather than via implicit session-based resolution.

### 2.5 DOWN migration (SQLite)

```sql
-- v11 down
DROP TABLE IF EXISTS vibe_recall_subscriptions;
DROP INDEX IF EXISTS idx_vibe_frames_scope_lookup;
DROP INDEX IF EXISTS idx_vibe_frames_invalidation;
DROP TABLE IF EXISTS vibe_frames;

PRAGMA user_version = 10;
```

### 2.6 Backwards compatibility

Pre-v11 code reading or writing the existing tables sees no schema change. The new tables are entirely additive; the DB at v10 is forward-compatible to v11 without code changes other than the migration tool.

For dual-driver parity, the Postgres variant uses `SERIAL` instead of `INTEGER PRIMARY KEY`, `TIMESTAMP WITH TIME ZONE` instead of `TEXT` for the timestamp fields (with formatters on each side to render as RFC3339Nano), and `BIGINT` instead of `INTEGER`. The application layer uses parameterized types so the schema dialect is invisible.

---

## 3. Migration v12 — Wave 5E.ii (destructive-but-rebuild)

### 3.1 What changes

The `sessions` table is rewritten with:

1. New `status` CHECK enum (5 states instead of 2)
2. Three new columns: `last_heartbeat_at`, `parent_session_id`, `resurrected_from`
3. New CHECK constraint coupling `status IN ('closed_clean','archived')` to `closed_at IS NOT NULL`
4. Backfill of any existing `status='open'` rows to `status='closed_aborted'` (operator-consented migration of "harness accidentally killed" sessions into resurrectable state)
5. New column on `write_audit`: `session_event TEXT` for events like `'open'`, `'heartbeat'`, `'close_clean'`, `'close_aborted'`, `'resurrect'`
6. New indexes for status/operator and resurrection-chain lookups

### 3.2 Status CHECK (5 states)

| State | Meaning | Resurrectable? |
|---|---|---|
| `open` | Active | yes (no-op via `_resume`) |
| `idle` | Was open, no recent heartbeat | yes (via `_resume` or wait for timeout) |
| `closed_clean` | Operator closed cleanly with `reason='clean'` | **NO** (terminal) |
| `closed_aborted` | Sweeper/boot reconciliation promoted | yes (via `_resurrect`) |
| `archived` | Operator archived | **NO** (terminal) |

### 3.3 New columns

```sql
last_heartbeat_at  TEXT,        -- RFC3339Nano; updated by _heartbeat; NULL until first heartbeat
parent_session_id  TEXT,        -- session_id of the session that created THIS one via _resurrect
                                  -- nullable; non-null only for resurrected sessions
resurrected_from   TEXT,        -- session_id of the FIRST ancestor session; nullable
                                  -- non-null only for sessions in a resurrection chain
```

### 3.4 New CHECK constraint

```sql
CHECK (
  (status IN ('closed_clean','archived') AND closed_at IS NOT NULL)
  OR (status = 'open' AND closed_at IS NULL)
  OR (status IN ('idle','closed_aborted'))     -- closed_at may be NULL (was open) or set (sweeper promoted)
)
```

Couples `closed_clean`/`archived` to having a `closed_at`, couples `open` to NOT having `closed_at`, and lets `idle`/`closed_aborted` carry a `closed_at` only when the sweeper explicitly set one (preserves forensic information without forcing it). Protects against partial state from buggy code paths. The CHECK is permissive on `idle`/`closed_aborted` because the `closed_at` field there has dual semantics: NULL means "session went stale before reaching terminal state"; non-NULL means "sweeper/boot reconciliation promoted it and stamped the time".

Note: the backfill in §3.5 sets `closed_at = now()` for every orphan promoted from legacy `status='open'`, so the audit trail is consistent.

### 3.5 Backfill of legacy `status='open'` rows

```sql
-- Backfill: any row with status='open' at migration time is treated as an
-- accidental non-clean-close (the user is denouncing these). Promote to
-- closed_aborted so they become resurrectable. closed_at = now().

-- Step 1: Pre-flight check. Halt migration if unexpected legacy statuses
-- are present. Legacy v10 status enum is {open, closed}. Anything else
-- (e.g. a partial state from a buggy v9 install) is a migration hazard.

-- The Go migration runner runs:
--   SELECT DISTINCT status FROM _sessions_old WHERE
--     status NOT IN ('open','closed');
-- If the result is non-empty, the migration ABORTS with operator-visible
-- error: "Unexpected legacy status(es): [<list>]. Manual intervention
-- required before v12 can apply. Inspect the sessions table."

UPDATE sessions
SET status = 'closed_aborted',
    closed_at = COALESCE(closed_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
WHERE status = 'open';

UPDATE sessions
SET status = 'closed_clean'             -- legacy 'closed' is terminal-clean by historical convention
WHERE status = 'closed';

-- Notes about what we DON'T backfill:
-- - Any status outside {open, closed} is rejected (halt).
-- - legacy 'closed' is treated as 'closed_clean' (terminal, not resurrectable).
--   If the operator knows specific legacy 'closed' sessions were actually
--   'closed_aborted' (kill-by-harness), they can manually UPDATE after
--   migration; this is intentionally NOT auto-reclassified to avoid
--   silently resurrecting sessions the operator intended as terminal.
-- - resurrection chain info (parent_session_id, resurrected_from) is
--   populated by FUTURE _resurrect calls; backfill does not infer chains.
```

This is the explicit "yes, we know you have orphan sessions; this migration claims them as resurrectable" line.

### 3.6 `write_audit` extension

```sql
ALTER TABLE write_audit ADD COLUMN session_event TEXT;
-- session_event ∈ {NULL, 'open', 'heartbeat', 'idle_timeout', 'close_clean',
--                  'close_aborted', 'resurrect', 'recover'}
-- NULL for non-session-related writes (drift_log, artifact_log, etc.)
--
-- 'idle_timeout' is emitted by the sweeper (per INV-9 / Wave 5E.iii) when it
-- promotes a stale open session to idle. Subsequent promotion from idle to
-- closed_aborted (after a second HEARTBEAT_TIMEOUT) is emitted as
-- 'close_aborted'. The two-event sequence makes the audit trail complete:
-- operators can reconstruct "session went stale at T1, was finally promoted
-- at T2" without joining multiple tables.
--
-- 'heartbeat' is emitted by the dark_memory_session_heartbeat tool when
-- the harness periodic-call arrives. High-volume heartbeats (every ~30s
-- per session) are audit-logged but condensed in observability views.
-- Operators can filter session_event='heartbeat' out of the daily audit
-- report if desired.
```

### 3.7 New indexes

```sql
CREATE INDEX IF NOT EXISTS idx_sessions_status_operator
  ON sessions (status, operator, closed_at DESC);

CREATE INDEX IF NOT EXISTS idx_sessions_resurrected
  ON sessions (resurrected_from)
  WHERE resurrected_from IS NOT NULL;
  -- Postgres: CREATE INDEX ... WHERE ...;

CREATE INDEX IF NOT EXISTS idx_write_audit_session_event
  ON write_audit (session_event, created_at)
  WHERE session_event IS NOT NULL;
```

### 3.8 UP migration (SQLite)

```sql
-- v12 up
PRAGMA foreign_keys = OFF;
ALTER TABLE sessions RENAME TO _sessions_old;

CREATE TABLE sessions (
  id                  INTEGER PRIMARY KEY,
  session_id          TEXT    NOT NULL UNIQUE,
  status              TEXT    NOT NULL DEFAULT 'open'
                      CHECK (status IN ('open','idle','closed_clean','closed_aborted','archived')),
  constitution_id     TEXT,
  constitution_ver    TEXT,
  active_mods         TEXT,
  operator            TEXT    NOT NULL,
  started_at          TEXT    NOT NULL,
  closed_at           TEXT,
  last_heartbeat_at   TEXT,
  parent_session_id   TEXT,
  resurrected_from    TEXT,
  notes               TEXT,
  created_at          TEXT    NOT NULL,
  CHECK (
    (status IN ('closed_clean','archived') AND closed_at IS NOT NULL)
    OR (status NOT IN ('closed_clean','archived'))
  )
);

INSERT INTO sessions
  (id, session_id, status,
   constitution_id, constitution_ver, active_mods,
   operator, started_at, closed_at,
   last_heartbeat_at, parent_session_id, resurrected_from,
   notes, created_at)
SELECT
  id, session_id,
  CASE WHEN status='open' THEN 'closed_aborted' ELSE 'closed_clean' END,
  constitution_id, constitution_ver, active_mods,
  operator, started_at, closed_at,
  NULL, NULL, NULL,
  notes, created_at
FROM _sessions_old;

DROP TABLE _sessions_old;

ALTER TABLE write_audit ADD COLUMN session_event TEXT;

CREATE INDEX IF NOT EXISTS idx_sessions_status_operator
  ON sessions (status, operator, closed_at DESC);

CREATE INDEX IF NOT EXISTS idx_sessions_resurrected
  ON sessions (resurrected_from);

CREATE INDEX IF NOT EXISTS idx_write_audit_session_event
  ON write_audit (session_event, created_at);

PRAGMA foreign_keys = ON;

PRAGMA user_version = 12;
```

### 3.9 DOWN migration (SQLite)

```sql
-- v12 down (lossy; does not preserve closed_aborted back to open)
PRAGMA foreign_keys = OFF;
ALTER TABLE sessions RENAME TO _sessions_old;

CREATE TABLE sessions (
  id                  INTEGER PRIMARY KEY,
  session_id          TEXT    NOT NULL UNIQUE,
  status              TEXT    NOT NULL DEFAULT 'open'
                      CHECK (status IN ('open','closed')),
  constitution_id     TEXT,
  constitution_ver    TEXT,
  active_mods         TEXT,
  operator            TEXT    NOT NULL,
  started_at          TEXT    NOT NULL,
  closed_at           TEXT,
  notes               TEXT,
  created_at          TEXT    NOT NULL
);

-- Map back: closed_clean -> closed (terminal);
-- closed_aborted, idle, archived -> closed (lossy; resurrection info lost)
INSERT INTO sessions
  (id, session_id, status,
   constitution_id, constitution_ver, active_mods,
   operator, started_at, closed_at,
   notes, created_at)
SELECT
  id, session_id, 'closed',
  constitution_id, constitution_ver, active_mods,
  operator, started_at, closed_at,
  notes, created_at
FROM _sessions_old;

DROP TABLE _sessions_old;

-- Cannot drop write_audit.session_event without data loss; leave the column
-- but mark as deprecated. Future v13 or manual cleanup can drop it.

DROP INDEX IF EXISTS idx_sessions_status_operator;
DROP INDEX IF EXISTS idx_sessions_resurrected;
DROP INDEX IF EXISTS idx_write_audit_session_event;

PRAGMA foreign_keys = ON;

PRAGMA user_version = 11;
```

The DOWN migration is **lossy for session lifecycle** — closed_aborted / idle / archived all collapse to closed (legacy v10 shape). This is acceptable because the DOWN is an emergency-only path (operator accidentally upgraded and must roll back), not a normal operation. Resurrection chain info is lost in the rollback.

### 3.10 Backwards compatibility

This IS a breaking change for any in-flight `'open'` sessions. They get promoted to `'closed_aborted'`, becoming resurrectable. Operators are notified at migration time with a count:
- "This migration will promote N orphan open sessions to closed_aborted. They remain accessible via _resurrect(reason='rescue-from-orphan-v12'). Do you wish to proceed?"

For harnesses / clients holding a `session_id` from a v11 (or earlier) install, after migration:
- If that session was `closed_clean` before → still `closed_clean` (no change).
- If that session was `open` → now `closed_aborted`; the harness must call `_resurrect` (or `_recover`) to get the `parent_session_id` chain.
- If that session was already `closed` (legacy v10 status enum) → now `closed_clean` (post-migration backfill maps `closed` → `closed_clean`).

### 3.11 Driver parity

Postgres variant uses:
- `ALTER TABLE sessions RENAME TO _sessions_old` works the same in Postgres
- `CREATE TABLE` with `SERIAL` for PK, `TIMESTAMP WITH TIME ZONE` for times
- The CHECK constraints work identically
- `DROP INDEX IF EXISTS` works
- Down migration: same as SQLite with `SERIAL` and `TIMESTAMP WITH TIME ZONE`

Note: in Postgres, `ALTER TABLE ... ADD COLUMN` is non-blocking; in SQLite it requires a table copy. The Postgres path is faster but more visible (each ADD COLUMN is a separate ALTER). The Go migration runner applies both in the same transaction per migration; partial failures roll back cleanly.

---

## 4. Combined impact

### 4.1 Sequence

Reference key for the wave numbering (for readers cross-referencing RFC / PLAN):

| This doc says | Means | Lives in (RFC §X / wave) |
|---|---|---|
| **5A.i** | atomic frame types (the 6 kinds) | RFC §3 M1 |
| **5A.ii** | scoped recall (single MCP tool with discriminator) | RFC §3 M1 + §6.1; PLAn §2.1 |
| **5A.iii** | this schema v11 | RFC §6.1 |
| **5A.iv** | policy/gate.go interceptor | RFC §3 M2 |
| **5B** | persona + capabilities | RFC §3 M3 + M4 |
| **5C** | delegation | RFC §3 M7 |
| **5D** | harness adapters | RFC §3 M8 |
| **5E.i** | this schema v12 | RFC §4 + §6.5 |
| **5E.ii** | close/resurrect/heartbeat/recover tools | RFC §4.2 |
| **5E.iii** | scope/sweeper + boot reconciliation | RFC §4.2 + INFRA-002 patch |
| **5E.iv** | frame-aware resurrection | RFC §3 M1 + §4.2 |
| **5E.v** | L6 adapter integration | RFC §3 M8 |

```
dark.db v10 (legacy)
   │
   ├─→ v11 (Wave 5A.iii):  additive, installs vibe_frames + vibe_recall_subscriptions
   │      │
   │      ▼  (5A.ii = scoped recall writes to new tables; 5A.iv = gate reads them)
   │
   ▼
dark.db v11 (with frames + subs, but legacy sessions schema)
   │
   ├─→ v12 (Wave 5E.ii):  destructive on sessions, adds session_event
   │      │
   │      ▼  (5E.iii sweeper + boot reconciliation starts emitting idle_timeout / close_aborted)
   │
   ▼
dark.db v12 (final pivote state)
```

Note: the §4.1 line "(5E.iii sweeper + boot reconciliation starts emitting idle_timeout / close_aborted)" is the explicit tie-in to §3.6's session_event enum. The constitution's INV-9 wording describes the END state (open → closed_aborted via HEARTBEAT_TIMEOUT); the schema doc here surfaces the INTERMEDIATE state (open → idle via IDLE_TIMEOUT, then idle → closed_aborted via HEARTBEAT_TIMEOUT-since-idle). INFRA-002 is filed to patch the constitution's INV-9 wording to match this dual-timeout detail.

### 4.2 Tests required

Per `PLAN.md` §6 / RFC §8 acceptance criteria:

- `tests/migrate/v11_test.go`: dual-driver contract (SQLite always; Postgres if `DARK_TEST_POSTGRES_DSN` set).
- `tests/migrate/v12_test.go`: same dual-driver; tests BOTH the orphan-backfill path (sessions with `status='open'` at v10 get promoted to `closed_aborted`) AND the new 5-state enum (all 5 states are insertable + queryable).
- `tests/store/sessions_test.go`: 5-state transitions; resurrection chain (parent_session_id + resurrected_from); boot_reconcile idempotency.
- `tests/store/frames_test.go`: frame cache invalidation via last_write_id; INV-5 cache-miss + anomaly event on hash mismatch.
- `tests/orchestration/5E_session_lifecycle_test.go`: full lifecycle harness — operator `_close(reason='clean')` is terminal; `_close(reason='aborted')` is resurrectable; harness kill promotes via sweeper.
- `tests/e2e/pivot_smoke.go`: end-to-end — operator kill simulation → harness restart → `_recover` → consent → `_resurrect` → session continues with same scope state.

### 4.3 Performance impact

- `vibe_frames` cache TTL: identity=15min, scope=30s, evidence=10s, capabilities=15min, drift=30s, persona=15min. With 80%+ hit ratio in standard use (per ACTIVE_MEMORY_RFC.md §8 acceptance), per-call cost is ~5ms (cache lookup) vs ~50ms cold.
- `sessions` rewrite with indexes: status/operator lookups <5ms; resurrection-chain lookups ~10ms (small index).
- `write_audit.session_event` adds 4 bytes per row; no perf impact.
- Sweeper goroutine: 60s interval, scans `sessions WHERE status='open' AND last_heartbeat_at < now - HEARTBEAT_TIMEOUT`. Index on (status, last_heartbeat_at) keeps this O(active_open_count).

---

## 5. Open questions / decisions deferred

- **Postgres partitioning of write_audit**: deferred per legacy §10. Not addressed here.
- **Cross-driver replication**: not addressed (out of scope).
- **Vector indexes on vibe_frames.frame_json**: deferred; substring search acceptable for v2.0.
- **Frame encryption at rest**: deferred; the constitution does not specify encryption of frames, and session closure does NOT encrypt the frame cache (operators may want to inspect frames for debugging; if this is a concern, add a per-project field-level encryption toggle in a future wave).
- **Resurrection chain depth limit**: no max enforced in v2.0. If chains grow unbounded (theoretically possible via resurrect → close_aborted → resurrect loops), a future v2.1 wave may add a depth cap.

---

## 6. References

- **ACTIVE_MEMORY_RFC.md** §5 (INV-8, INV-9) — the invariables these migrations enforce.
- **ACTIVE_MEMORY_RFC.md** §4 (session lifecycle states) — what the new `status` enum encodes.
- **ACTIVE_MEMORY_RFC.md** §6.2 (Wave 5A.iii + 5E.ii) — the wave placement.
- **dark-memory-mcp-cerebro.constitution.toml v1.0.0** — INV-5 (cache integrity), INV-8 (Resilience), INV-9 (Heartbeat) are the contract these migrations uphold.
- **BRIDGE_AND_COEXISTENCE.md v2** §3.4 — the table ownership rules these migrations preserve.
- **v10 schema** (the legacy target) — additive v11 + destructive-on-sessions v12 FROM v10.

---

## 7. Implementation notes (for the implementing wave)

When 5A.iii + 5E.ii code lands, the work splits cleanly into:

1. `internal/migrate/sqlite/ddl.go` — add `migrateV11Up`, `migrateV11Down`, `migrateV12Up`, `migrateV12Down`
2. `internal/migrate/postgres/ddl.go` — same; Postgres dialect
3. `internal/migrations.go` — add to the `migrations` slice, names `"v11-frames-recall-subs"`, `"v12-sessions-lifecycle"`, ordered
4. `tests/migrate/*_test.go` — new tests per §4.2
5. `internal/store/sessions.go` — extend SaveSession / GetSession for the new columns + new status enum
6. `internal/store/frames.go` — new file: SaveFrame / GetFrame / ListFrames with last_write_id-based invalidation
7. `internal/store/recall_subscriptions.go` — new file: SaveSubscription / UpdateLastSeenToken / GetSubscription

The design above specifies everything the implementing code needs to fill in. The implementing PR should reference this spec by name in its description.

---

End of Schema Design v1.0.0.
