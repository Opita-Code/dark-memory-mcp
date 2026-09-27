# Agent Memory Schema — v4-alpha.1

> **Audience**: contributors touching `internal/v4alpha/agent_memory/`.
> **Schema version**: `v4alpha/2026-09-27/001`.

## 1. The two tables

```sql
CREATE TABLE IF NOT EXISTS agent_memory (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    operator   TEXT    NOT NULL CHECK (operator <> ''),
    kind       TEXT    NOT NULL CHECK (kind <> ''),
    title      TEXT,
    content    TEXT    NOT NULL,
    tags       TEXT,
    pinned     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT
);

CREATE VIRTUAL TABLE IF NOT EXISTS agent_memory_fts
    USING fts5(content, title, tags,
                content='agent_memory',
                content_rowid='id');
```

### 1.1 Base table — `agent_memory`

| Column | Type | Constraint | Purpose |
|---|---|---|---|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | rowid; FTS5 mirror |
| `operator` | TEXT | NOT NULL, `<> ''` | tenant scope (INV-1 audit identity) |
| `kind` | TEXT | NOT NULL, `<> ''`, allow-listed | canonical kind (see §2) |
| `title` | TEXT | nullable | optional short label |
| `content` | TEXT | NOT NULL | the memory payload (searched by FTS5) |
| `tags` | TEXT | nullable | comma-separated (searched by FTS5) |
| `pinned` | INTEGER | NOT NULL DEFAULT 0 | bool (0/1); pinned rows surface first in `List` |
| `created_at` | TEXT | NOT NULL DEFAULT CURRENT_TIMESTAMP | RFC3339-ish timestamp from SQLite clock |
| `updated_at` | TEXT | nullable | set by `Update` via `CURRENT_TIMESTAMP` |

### 1.2 FTS5 sidecar — `agent_memory_fts`

**Important**: this is **contentless FTS5**. The actual content
lives in `agent_memory`; the FTS5 table stores only the index.
The columns `content, title, tags` named in the FTS5 declaration
are the indexed columns, NOT stored columns.

- `content='agent_memory'` — bind to the base table for content
  lookup (saves ~2× storage vs regular FTS5).
- `content_rowid='id'` — bind the FTS5 rowid to the base table's
  primary key. The base INSERT does NOT auto-populate the FTS5
  index; every `Save` / `Update` / `Archive` must sync manually.

**Do not use the FTS5 `'delete'` or `'delete-all'` commands.**
They raise `SQLITE_CORRUPT (267)` in modernc.org/sqlite v1.53
under SERIALIZABLE. Use plain `DELETE FROM agent_memory_fts WHERE
rowid = ?` instead — verified working against the contentless
schema by `TestArchive_RemovesFromBaseAndFTS`.

### 1.3 Secondary indexes (3)

```sql
CREATE INDEX IF NOT EXISTS agent_memory_operator_idx
    ON agent_memory(operator);

CREATE INDEX IF NOT EXISTS agent_memory_kind_idx
    ON agent_memory(kind);

CREATE INDEX IF NOT EXISTS agent_memory_pinned_idx
    ON agent_memory(pinned);
```

- `operator_idx` — used by every `Get`/`List`/`Recall`/`Archive` to
  scope by tenant.
- `kind_idx` — used by `List` filters like `kind=decision`.
- `pinned_idx` — used by `List`'s `ORDER BY pinned DESC, created_at DESC`.

## 2. Canonical kinds

7 kinds, allow-listed at Save. Empty or unknown kinds are rejected
with `ErrInvalidKind`.

```go
KindNote        = "note"
KindObservation = "observation"
KindDecision    = "decision"
KindFinding     = "finding"
KindTodo        = "todo"
KindLink        = "link"
KindContext     = "context"
```

### 2.1 Semantics

| Kind | Use it for |
|---|---|
| `note` | free-form thought; the catch-all default |
| `observation` | something noticed about the world (operator-facing) |
| `decision` | a choice made; rationale goes in `content` |
| `finding` | a research or audit finding (often agent-discovered) |
| `todo` | an action item |
| `link` | a reference to external state (URL, file path, ticket id) |
| `context` | background context for a future operator |

### 2.2 Mutability

`operator` and `kind` are **immutable** (INV-1 audit identity). To
change them, Archive + Save a new row. `Update` rejects any attempt
to change these fields (it doesn't even expose them as parameters).

## 3. The six store methods

| Method | Args | Returns | Tx? | FTS5 sync |
|---|---|---|---|---|
| `Save` | `op, kind, title, content, tags, pinned` | `(int64, error)` | Yes (`WithTx` SERIALIZABLE) | INSERT after base INSERT |
| `Get` | `id` | `(*Row, error)` | No | read-only |
| `List` | `op, limit` | `([]Row, error)` | No | read-only |
| `Recall` | `op, query, limit` | `([]Row, error)` | No | FTS5 query → JOIN base |
| `Update` | `id, *title, *content, *tags, *pinned` | `error` | Yes (`WithTx` SERIALIZABLE) | see §4 below |
| `Archive` | `id` | `error` | Yes (`WithTx` SERIALIZABLE) | plain DELETE on fts |

### 3.1 `Save` — insert + index

```sql
BEGIN ISOLATION LEVEL SERIALIZABLE;
  INSERT INTO agent_memory (operator, kind, title, content, tags, pinned)
    VALUES (?, ?, ?, ?, ?, ?);
  -- last_insert_rowid() = new id
  INSERT INTO agent_memory_fts (rowid, content, title, tags)
    VALUES (?, ?, ?, ?);
COMMIT;
```

### 3.2 `Update` — the canonical sequence (INV-17)

```sql
BEGIN ISOLATION LEVEL SERIALIZABLE;
  -- (1) existence check (so ErrNotFound is precise)
  SELECT 1 FROM agent_memory WHERE id = ?;

  -- (2) FTS5 DELETE FIRST (the load-bearing property)
  DELETE FROM agent_memory_fts WHERE rowid = ?;

  -- (3) base UPDATE (with updated_at = CURRENT_TIMESTAMP)
  UPDATE agent_memory
    SET title = ?, content = ?, tags = ?, pinned = ?, updated_at = CURRENT_TIMESTAMP
    WHERE id = ?;

  -- (4) re-read (the new values are committed in-tx; SELECT sees them)
  SELECT COALESCE(title,''), content, COALESCE(tags,'')
    FROM agent_memory WHERE id = ?;

  -- (5) FTS5 re-INSERT
  INSERT INTO agent_memory_fts (rowid, title, content, tags)
    VALUES (?, ?, ?, ?);
COMMIT;
```

**Why this exact order?** `modernc.org/sqlite` v1.53 raises
`SQLITE_CORRUPT (267)` when an FTS5 DELETE follows a base-table
UPDATE inside a SERIALIZABLE transaction. Reversing the order
(DELETE → UPDATE → INSERT) avoids the corrupt error. Verified by
`TestUpdate_MutatesFieldsAndReSyncsFTS`.

### 3.3 `Archive` — soft-delete + index cleanup

```sql
BEGIN ISOLATION LEVEL SERIALIZABLE;
  DELETE FROM agent_memory WHERE id = ?;
  DELETE FROM agent_memory_fts WHERE rowid = ?;
COMMIT;
```

Note: base DELETE first, FTS5 DELETE second. This is the inverse
of the Update ordering; both work because in Archive the base row
is the trigger, in Update the FTS5 entry is the trigger (the base
row's indexed values are what changed).

### 3.4 `Recall` — FTS5 query → base join

```sql
SELECT m.id, m.operator, m.kind, m.title, m.content, m.tags, m.pinned, m.created_at
FROM agent_memory_fts f
JOIN agent_memory m ON m.id = f.rowid
WHERE agent_memory_fts MATCH ?
  AND m.operator = ?
ORDER BY rank
LIMIT ?;
```

The query string is tokenised by FTS5 (lower-cased, stemmed,
prefix-matched on tokens ending in `*`). Operators do NOT escape
special FTS5 characters; FTS5 does that internally.

## 4. Errors

```go
var ErrEmptyOperator = errors.New("agent_memory: operator must be non-empty (INV-1)")
var ErrEmptyContent  = errors.New("agent_memory: content must be non-empty")
var ErrInvalidKind   = errors.New("agent_memory: invalid kind")
var ErrNotFound      = errors.New("agent_memory: not found")
```

`Save`:
- `ErrEmptyOperator` if operator is empty
- `ErrEmptyContent` if content is empty
- `ErrInvalidKind` if kind is not in the allow-list

`Get`:
- `ErrNotFound` if no row matches the id

`Archive`:
- `ErrNotFound` if no row was deleted

`Update`:
- `ErrNotFound` if id doesn't exist
- `errors.New("agent_memory Update: content cannot be empty")` if
  `*content == ""` (passed but empty)

## 5. Tests

`internal/v4alpha/agent_memory/agent_memory_test.go` — 18 tests:

| Group | Tests |
|---|---|
| Save | TestSave_InsertsAndReturnsID, TestSave_RejectsEmptyOperator, TestSave_RejectsEmptyContent, TestSave_RejectsInvalidKind |
| Get | TestGet_ReturnsRow, TestGet_NotFoundReturnsError |
| List | TestList_ReturnsByOperatorScope, TestList_OrdersPinnedFirst, TestList_HonorsLimit |
| Recall | TestRecall_FindsByQuery, TestRecall_FilteredByOperator, TestRecall_NoResultsReturnsEmpty |
| Archive | TestArchive_RemovesFromBaseAndFTS, TestArchive_NotFoundReturnsError |
| Update | TestUpdate_MutatesFieldsAndReSyncsFTS, TestUpdate_RejectsEmptyContent, TestUpdate_NotFound, TestUpdate_NoFieldsIsNoOp |

## 6. What's NOT here

- **Audit emission on Save**: `agent_memory.Save` does NOT yet
  emit a `write_audit` row. This is INV-1's only remaining gap
  in v4-alpha.1; closes in BUG-9.
- **Embedding vector / Mem0 three-class taxonomy**: deferred to
  BUG-9 alongside the audit emission.
- **`operator` vs `project_id`**: v4 uses `operator` as the tenant
  primitive (every row carries an operator). The legacy v3
  `project_id` column is NOT used on this table. Multi-tenant
  tooling should filter by `operator`; `dark-cli` and other
  dark-* projects that share a dark.db are partitioned by operator.

## 7. Cross-references

- [INV-10](../INVARIANTS.md#inv-10--agent-memory-rows-are-persistent-across-session-lifecycle-v230)
  — agent memory rows survive session close (no auto-bind).
- [INV-17](../INVARIANTS.md#inv-17--fts5-ordering-under-serializable-featv4-redesign)
  — the canonical Update sequence.
- [INV-16](../INVARIANTS.md#inv-16--dark-db-concurrency-contract-featv4-redesign)
  — every `WithTx` call here uses `LevelSerializable`.
- [v4-status.md §3](./v4-status.md#3-invariants--adoption-status)
  — invariant adoption matrix.
- [agent_memory.go](../../internal/v4alpha/agent_memory/agent_memory.go) — source of truth.
