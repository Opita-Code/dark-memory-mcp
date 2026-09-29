# SPEC-alpha-11-bug12 — BUG-12: cross-process audit_id monotonicity

> **Status**: DRAFT (2026-09-29)
> **Phase**: 1D of alpha.11+ plan (`docs/v4-alpha-11-plan.md`)
> **Vibe-loop**: alpha-11-phase-1d
> **ADR-008 compliance**: monolithic spec + atomic mirror
> **Spec-level**: 1 (single surface — refactor of audit.Writer)

---

## 0. TL;DR

`audit.Writer.Write` and `WriteExec` currently use an in-memory
counter (`w.seq++`) that they pass explicitly as `audit_id` in
the INSERT. This is **process-local monotonic** but **cross-process
unsafe**: two processes writing to the same `audit_log` table can
both compute `seq=5` and the second INSERT fails with a PRIMARY
KEY conflict.

**Fix**: drop the in-memory counter. Rely on SQLite's
`AUTOINCREMENT` (backed by the persistent `sqlite_sequence` table)
for cross-process monotonicity. Read the new id back via
`Result.LastInsertId()`.

`Writer.lastID` is kept as an in-memory mirror for the fast
`LastID()` diagnostic (no DB roundtrip per call), but it is
**never** used to drive the INSERT.

---

## 1. Problem

Today:

```go
type Writer struct {
    db  *sql.DB
    mu  sync.Mutex
    seq int64  // process-local
}

func (w *Writer) Write(ctx, actor, sessionID, payload) (int64, error) {
    ...
    w.mu.Lock()
    defer w.mu.Unlock()
    w.seq++
    _, err := w.db.ExecContext(ctx,
        "INSERT INTO audit_log (audit_id, actor, session_id, payload) VALUES (?, ?, ?, ?)",
        w.seq, actor, sessionIDArg, payload,
    )
    if err != nil {
        w.seq-- // rollback the seq claim
        return 0, err
    }
    return w.seq, nil
}
```

The schema:

```sql
CREATE TABLE audit_log (
    audit_id INTEGER PRIMARY KEY AUTOINCREMENT,
    ...
)
```

**Cross-process failure mode**:

1. Process A: Write → `w.seq=5` → INSERT (audit_id=5) ✓
2. Process B: Write → `w.seq=5` (its own counter) → INSERT (audit_id=5) → **PRIMARY KEY constraint failed**
3. Process B returns error. The audit row is LOST.

**Process-local correctness**:

- Mutex serializes in-process writes ✓
- Counter rollback on INSERT failure prevents in-process gaps ✓

**Cross-process gap**:

- Two processes have INDEPENDENT `w.seq` counters ✗
- The DB's AUTOINCREMENT is BYPASSED because we pass audit_id explicitly ✗
- SQLite can't help us because we override its sequence ✗

---

## 2. Solution

Drop the in-memory counter. Let SQLite AUTOINCREMENT do the work:

```go
type Writer struct {
    db     *sql.DB
    mu     sync.Mutex
    lastID int64 // mirror of LastInsertId, for fast LastID() reads
}

func (w *Writer) Write(ctx, actor, sessionID, payload) (int64, error) {
    if actor == "" {
        return 0, fmt.Errorf("audit Write: actor must be non-empty (INV-1)")
    }
    var sessionIDArg interface{}
    if sessionID == "" {
        sessionIDArg = nil
    } else {
        sessionIDArg = sessionID
    }
    res, err := w.db.ExecContext(ctx,
        "INSERT INTO audit_log (actor, session_id, payload) VALUES (?, ?, ?)",
        actor, sessionIDArg, payload,
    )
    if err != nil {
        return 0, fmt.Errorf("audit Write insert: %w", err)
    }
    id, err := res.LastInsertId()
    if err != nil {
        return 0, fmt.Errorf("audit Write LastInsertId: %w", err)
    }
    w.mu.Lock()
    w.lastID = id
    w.mu.Unlock()
    return id, nil
}
```

**Why this works cross-process**:

- SQLite AUTOINCREMENT is backed by the persistent `sqlite_sequence`
  table, which is a DB-level sequence. Two processes writing to the
  same DB file coordinate via this table.
- `Result.LastInsertId()` returns the SQLite-assigned id, which is
  guaranteed to be strictly greater than any prior id in this DB
  (including ids assigned by other processes).
- The mutex is no longer needed for ordering, but is kept to
  serialize in-process `lastID` updates (race-free reads).

---

## 3. Design decisions

### 3.1 Keep `lastID` as in-memory mirror

Why:
- `LastID()` is called from `session.Store.Close` (per close, every
  close emits 1 audit row + 1 LastID read). A DB roundtrip per call
  is wasteful.
- The mirror is updated only after a successful `LastInsertId()`.
  It is a strict under-approximation of MAX(audit_id) in the DB
  (other processes may have written more).

Trade-off:
- Per-process `LastID()` is correct (== the last id THIS process
  assigned). It may be smaller than MAX(audit_id) if other
  processes wrote concurrently. This is acceptable for the
  diagnostic use case (`session.Summary.AuditIDAtClose`).
- `MAX(audit_id)` is the truth. Code that needs the global truth
  should query it directly: `SELECT MAX(audit_id) FROM audit_log`.

### 3.2 Drop the counter rollback dance

With the new design, there is no counter to roll back. The flow is:

1. Validate (empty actor → error)
2. ExecContext (DB error → error, no state change)
3. LastInsertId (rare error → error, the row WAS inserted)

If ExecContext fails, the row was not inserted; sqlite_sequence
was not advanced. The next Write gets a fresh id from the DB.

If ExecContext succeeds but LastInsertId fails, the row WAS
inserted (and sqlite_sequence WAS advanced) but we can't tell
the caller what id it got. This is a rare edge case (Result is
nil or LastInsertId is not supported by the driver). We surface
the error to the caller; the row is still in the DB. The next
Write gets an id one greater than the lost one.

This is cleaner than the old design (no counter rollback needed)
and matches SQLite's own semantics.

### 3.3 Mutex is still needed (for lastID)

Even though the mutex no longer drives the INSERT, it protects
`lastID` updates from races:

- Goroutine 1: Write → lastInsertId=5 → wants to set lastID=5
- Goroutine 2: Write → lastInsertId=6 → wants to set lastID=6

Without the mutex, both could read lastID=4 and write their own
value; the final state is non-deterministic. With the mutex, the
last writer wins (which is fine; lastID is best-effort mirror).

### 3.4 No schema change

`audit_log` schema is unchanged. AUTOINCREMENT was already declared
(`CREATE TABLE IF NOT EXISTS audit_log (audit_id INTEGER PRIMARY
KEY AUTOINCREMENT, ...)`). The fix is purely in the Writer logic.

### 3.5 Existing tests should pass unchanged

- `TestExample_Write_FirstAuditIDIsOne` — SQLite AUTOINCREMENT
  starts at 1. First Write returns id=1. ✓
- `TestExample_Write_MonotonicAcrossActors` — sequential Writes
  always produce strictly increasing ids (SQLite guarantees this).
  ✓
- `TestProperty_Write_AuditIDMonotonic` — property holds. ✓
- `TestWriteExecRollback` — tx rollback leaves audit_log empty;
  next Write is id=2 (gap at 1). SQLite AUTOINCREMENT updates
  `sqlite_sequence` BEFORE the row is written (crash safety), so
  the gap is real. ✓
- `TestWriteExecInsertFailureCounterRollsBack` — ExecContext on
  closed tx fails → sqlite_sequence not updated → next Write is
  id=1. ✓
- `TestExample_Write_EmptyActorRejected` — empty actor rejected
  before ExecContext. ✓
- `TestWriteExecEmptyActor` — same. ✓
- `TestWriteExecEmptySessionIDStoredAsNULL` — same. ✓

---

## 4. Atomicity contract

### 4.1 Before (old)

```
mutex.Lock
seq++
INSERT (audit_id=seq, ...)
if error: seq-- ; return error
return seq
mutex.Unlock
```

### 4.2 After (new)

```
(actor validation, before mutex)
INSERT (audit_id=OMITTED, ...)
if error: return error  (no mutex touched)
LastInsertId()
if error: return error (rare; row was inserted)
mutex.Lock
lastID = id
mutex.Unlock
return id
```

### 4.3 invariants (preserved)

1. INV-1: empty actor rejected before DB I/O ✓
2. Monotonicity: per-process AND cross-process ✓
3. Atomicity: exactly one row inserted per Write ✓

---

## 5. Out of scope (v1)

- **Multi-DB support**: v4 ships one DB per process via the binary
  DSN. Multiple DBs sharing audit_log is a v2 architectural concern.
- **Concurrent writers from different processes via different
  *sql.DB handles**: this IS what BUG-12 fixes. ✓
- **Adopting writer-level retry on TransientError**: out of scope.
  Callers (session.Store, agent_memory.Store) handle their own
  retry. The audit Writer is the last-mile primitive.
- **audit_log_seq table** (alternative to AUTOINCREMENT): not needed.
  SQLite's AUTOINCREMENT + sqlite_sequence is the right primitive.

---

## 6. Acceptance criteria

| # | Criterion | Verified by |
|---|---|---|
| 1 | audit.Writer.Write drops the in-memory seq field | compile + tests |
| 2 | Write uses `INSERT INTO audit_log (actor, session_id, payload)` (no audit_id) | source review + tests |
| 3 | Write returns the SQLite-assigned id via `Result.LastInsertId()` | TestExample_Write_FirstAuditIDIsOne |
| 4 | lastID field is set after each Write (fast LastID() reads) | TestWriteExecEmptyActor (LastID()==0 after rejection) |
| 5 | Cross-process monotonicity: two Writers on the same file-backed DB produce strictly increasing ids | TestCrossProcess_Monotonic |
| 6 | All existing audit tests pass (no regressions) | `go test ./internal/v4alpha/audit/...` |
| 7 | All v4alpha packages pass (no regressions) | `go test ./internal/v4alpha/...` |
| 8 | Tool count unchanged (no new tool) | server_test.go |
| 9 | Atomic mirror saved (1 SUMMARY pinned + 3 SECTION, per ADR-008) | this turn |

---

## 7. Files

### MODIFIED (3 files)

- `internal/v4alpha/audit/writer.go` — drop seq, use AUTOINCREMENT
  + LastInsertId. Keep lastID mirror for fast LastID().
- `internal/v4alpha/audit/writer_tx.go` — same pattern in
  WriteExec.
- `CHANGELOG.md` — `[4.0.0-alpha.14]` entry.

### NEW (1 file)

- `internal/v4alpha/audit/writer_cross_process_test.go` — the
  cross-process monotonicity test.

### UNCHANGED

- `internal/v4alpha/audit/writer_test.go` — 6 existing tests
  should pass unchanged.
- `internal/v4alpha/audit/writer_property_test.go` — 3 property
  tests should pass unchanged.
- `internal/v4alpha/audit/writer_tx_test.go` — 6 existing tests
  should pass unchanged.
- `internal/v4alpha/audit/helpers_test.go` — unchanged.
- `internal/v4alpha/session/session.go` — uses audit.Writer but
  doesn't depend on seq semantics.
- `internal/v4alpha/agent_memory/` — uses WriteExec but doesn't
  depend on seq semantics.
- `internal/v4alpha/judge/` — uses WriteExec but doesn't depend
  on seq semantics.

---

## 8. Test plan

### 8.1 New test: cross-process monotonicity

`TestCrossProcess_Monotonic` (in writer_cross_process_test.go):

1. Open a file-backed SQLite DB in a temp dir (not :memory:).
2. Create schema.
3. Spin up 2 audit.Writer instances pointing at the same DB.
4. Write 5 audit rows from Writer 1, then 5 from Writer 2.
5. SELECT audit_id, actor FROM audit_log ORDER BY audit_id ASC.
6. Assert: audit_ids are strictly increasing (no gaps from PK
   conflicts, no duplicates).

### 8.2 New test: cross-process interleaved

`TestCrossProcess_Interleaved` (in writer_cross_process_test.go):

1. Same setup.
2. Interleave writes from both Writers (A, B, A, B, A, ...).
3. Assert: audit_ids strictly increasing; each Writer's last
   id matches the row it inserted.

### 8.3 Existing tests (must pass unchanged)

9 tests total in the audit package. None should fail.

---

## 9. Cross-references

- `docs/sota-critique.md` §7.6 — Phase 1D is item 4 of 4
  in Phase 1.
- `docs/v4-alpha-11-plan.md` — the 5-vibe-loop plan.
- `docs/specs/SPEC-alpha-11-pre1c3.md` — previous chunk (PRE-1 C3).
- `docs/specs/SPEC-alpha-11-pre1c4.md` — PRE-1 C4 (sister).
- `docs/specs/SPEC-alpha-11-chunk7.md` — workstream close.
- Row 2180 (PRE-1 C4) — summarize_session depends on
  audit_log.session_id.
- Row 2182 (PRE-1 C3) — session_start Loadout reads audit_log
  via queryRecentWrites.
- SQLite AUTOINCREMENT docs: <https://www.sqlite.org/autoinc.html>
- modernc.org/sqlite (pure-Go driver used in v4alpha): no cgo,
  drives the AUTOINCREMENT semantics identically to the C
  reference driver.

---

## 10. Sign-off

Self-approved (operator mandate 2026-09-29, "vamos por la
siguiente fase"). Implementation proceeds in this turn.