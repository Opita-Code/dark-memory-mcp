# SPEC-alpha-11-phase2 — Phase 2 lite: hash chain + audit_verify (Option B)

> **Status**: DRAFT (2026-09-29)
> **Phase**: 2 of alpha.11+ plan (`docs/v4-alpha-11-plan.md`)
> **Vibe-loop**: alpha-11-phase-2
> **ADR-008 compliance**: monolithic spec + atomic mirror
> **Spec-level**: 2 (audit_log schema + Writer + verify tool — cross-cutting)
> **Operator decision**: Option B (2026-09-29). Ed25519 (ADR-017) deferred.

---

## 0. TL;DR

`audit_log` is **structurally unchained**: each row stands alone. There is no
detection for tampering, no way to prove a row was emitted by `dark-memory`,
and no operator-facing tool to walk the chain end-to-end. The SOTA criticism
(chunk 3 §D, row 2153) flagged 3 specific gaps:

1. **No cryptographic chaining of rows** (gap #1, ADR-017 / 4)
2. **No payload signature** (gap #5, ADR-017)
3. **No audit chain verification tool** (gap #6, ADR-018)

**Phase 2 lite** fixes the structural gap with a **hash chain** (SHA-256)
and adds a **`dark_memory_audit_verify`** MCP tool. Ed25519 signatures
(ADR-017) are **deferred** because there is no external verifier today
(see §2 for the rationale).

What lands in this phase:
- 2 new columns on `audit_log`: `prev_hash BLOB`, `row_hash BLOB`
- `audit.Writer` gains `lastHash []byte` mirror + canonical SHA-256 chain
- 1 new MCP tool: `dark_memory_audit_verify(start_id?, end_id?)`
- Schema migration: `ALTER TABLE × 2` (additive, NULL-tolerant for legacy rows)
- ~400-500 LoC, 6+ tests, 41 → 42 tools

What **does NOT** land:
- Ed25519 signatures (ADR-017) — deferred to ADR-017-deferred
- Payload split into structured columns (ADR-019) — orthogonal, separate phase
- External transparency log (Rekor-style) — ADR-016, alpha.3 deferred
- Merkle tree / inclusion proofs — alpha.3 deferred

---

## 1. Context (Phase 1 closed)

Per `docs/v4-alpha-11-plan.md` §2, Phase 2 was originally 3 ADRs:

| # | Item | LoC | Risk |
|---|---|---|---|
| 5 | ADR-019 (split payload BLOB) | ~250 | MEDIUM (schema) |
| 6 | ADR-017 (Ed25519 signature) | ~120 | LOW |
| 7 | ADR-018 (audit_verify tool) | ~100 | LOW |

Phase 1 closed 2026-09-29 with 4 vibe-loops shipped (chunk 7, PRE-1 C3,
PRE-1 C4, BUG-12). v4 is at **alpha.14**.

Operator chose **Option B (Phase 2 lite)** on 2026-09-29:
- Hash chain (SHA-256) — no crypto key
- `dark_memory_audit_verify` tool
- Ed25519 deferred to a future phase when there's a use case

**Why this is the right cut**: see §2 below.

---

## 2. Option B rationale (why no Ed25519)

The original plan calls for Ed25519 signatures per audit row (ADR-017).
This spec defers that work. The reasoning:

### 2.1 Threat model (who is the adversary?)

| Adversary | What stops them? |
|---|---|
| Operator self-deception / accidental modification | Hash chain — breaks sequence |
| Buggy process writing bad rows | Hash chain — broken hash = bad row |
| Attacker with file access to `dark.db` | Hash chain — modification breaks chain |
| External auditor (offline) verifying integrity | Ed25519 — only with private key |
| Collusion of processes against the chain | Neither (would need full external log) |

The **first three adversaries** are realistic; the **fourth** has no
current use case; the **fifth** is out of v4 scope entirely.

### 2.2 The Ed25519 cost

| Cost | Comes | Without Ed25519 |
|---|---|---|
| 1 Ed25519 sign per Write | ~30-50μs | none |
| 1 Ed25519 verify per audit_verify row | ~50-100μs | none |
| 64 bytes signature column | +64 B/row | none |
| Key management: who holds the private key? | The operator (single-tenant local) | none |
| External verification use case | **None today** | n/a |

The key management question is the killer: **who holds the private key?** If
the operator, then Ed25519 is theater — the operator can rewrite history.
If the dark-memory process, then the process can rewrite history. The only
real value of Ed25519 is **external verification by someone who doesn't
trust the operator**, and we don't have that today.

### 2.3 Forward-compat (Ed25519 can come later)

The hash chain is **forward-compatible** with Ed25519:

- The canonical encoding (§3.2) is extensible — adding `signature BLOB` to
  the hash input means **Ed25519 signatures cover the chain too**, not just
  the row payload.
- Adding a `signature` column later is a forward-only ALTER TABLE.
- `audit_verify` can be extended with a `verify_signature=true` flag.

So Option B is **a strict subset of Option A**. We pay ~20% of the cost for
~80% of the value. **Ed25519 deferred is not Ed25519 abandoned** — it's
Ed25519 waiting for a real verifier.

### 2.4 What Option B does NOT give us

Be honest:

- **No payload integrity**: a row can be inserted claiming `actor=alice`
  when alice didn't actually emit it. The hash chain only proves the row
  came from SOME process that wrote to this DB in sequence.
- **No non-repudiation**: the same process can rewrite history (delete +
  rewrite). Hash chain detects it after the fact.
- **No external trust anchor**: the hash chain is local. If the DB is
  exfiltrated, the attacker has the chain AND can recreate the hashes.
  An external log (Rekor) solves this — deferred to alpha.3.

These are limitations, not blockers. They match the threat model: a
local, single-operator, tamper-detection-after-the-fact use case.

---

## 3. Hash chain design

### 3.1 Two new columns

```sql
ALTER TABLE audit_log ADD COLUMN prev_hash BLOB;  -- 32 bytes (SHA-256)
ALTER TABLE audit_log ADD COLUMN row_hash  BLOB;  -- 32 bytes (SHA-256)
```

Both nullable. Legacy rows have `NULL` for both. **Verify** treats NULL
hashes as "trust anchors" (see §5.4).

### 3.2 Canonical encoding for `row_hash`

`row_hash` is SHA-256 over a deterministic concatenation. Order and
separators matter — the verify tool uses the **same** encoding, and any
change to either breaks verification (which is the point).

```
row_hash = SHA256(
    prev_hash                       // 32 bytes (zero for row 1)
    || audit_id                     // int64, big-endian (8 bytes)
    || actor                        // UTF-8 bytes (non-empty per INV-1)
    || 0x00                         // separator
    || session_id                   // UTF-8 bytes (empty string if NULL)
    || 0x00                         // separator
    || payload                      // raw bytes (may be empty)
    || 0x00                         // separator
    || created_at                   // RFC3339 string (24 bytes typically)
    || 0x00                         // trailing separator (no following field)
)
```

Why this encoding:
- **Fixed-width where possible** (`audit_id` 8 bytes BE): cheap to encode.
- **Length-prefixed implicitly** via separators: avoids length-prefix
  ambiguity (e.g., actor "foo" + session_id "" vs actor "fo" + session_id "o").
- **Trailing `0x00`**: prevents "field N and field N+1 concatenated into a
  new field" attacks (e.g., payload ending in something that looks like a
  valid RFC3339 string).
- **`created_at` generated in Go**: `time.Now().UTC().Format(time.RFC3339Nano)`.
  The SAME value is stored in the row AND fed to the hash, so Write and
  Verify agree on the bytes. Uniform format across writers (no SQLite
  `CURRENT_TIMESTAMP`, which is second-granularity and would need an
  extra SELECT per Write). Judge fix F1 (2026-09-30): spec originally
  said DB-generated; implementation ships Go-generated.

### 3.3 Why SHA-256

- Constant-time, well-studied, no key requirement.
- ~1μs per hash on modern hardware — well under our 5μs budget.
- 32 bytes per row is acceptable (10k rows = 320 KB extra).
- No surprises: deterministic across platforms (the encoding above
  excludes process-local state; `created_at` is generated once per Write
  and stored, so Verify reads back the identical bytes).

### 3.4 The chain

Row N's `prev_hash` = row N-1's `row_hash`. For row 1, `prev_hash` is
32 zero bytes. Each row's `row_hash` is a function of `prev_hash` and
the row contents, so:

- Modifying row K's contents → row_hash_K changes → row K+1's prev_hash
  no longer matches → row_hash_K+1 is wrong → cascade.
- Deleting row K → row K+1's prev_hash refers to a row that no longer
  exists → broken_at = K+1.
- Inserting a fake row between K and K+1 → impossible: auto-increment
  is monotonic, the new row gets id K+2 (not K+1), but K+1's prev_hash
  is wrong (it would need to match the fake's row_hash).

**The chain detects: modification, deletion, and forgery.** It does NOT
detect the "rewrite the whole file from scratch" attack — that requires
an external trust anchor (out of scope).

---

## 4. Writer changes (audit.Writer + audit.Writer.WriteExec)

### 4.1 New struct field

```go
type Writer struct {
    db       *sql.DB
    mu       sync.Mutex
    lastID   int64
    lastHash []byte  // NEW: mirror of most recent row_hash (32 bytes)
}
```

`lastHash` is an in-memory mirror, **per-process** (like `lastID`). On
first Write, it's `nil`; Writer bootstraps from DB (§4.3). On subsequent
Writes, it's used as `prev_hash` for the INSERT.

### 4.2 Write flow

```go
func (w *Writer) Write(ctx, actor, sessionID, payload) (int64, error) {
    if actor == "" { return 0, fmt.Errorf("...INV-1...") }
    
    w.mu.Lock()
    defer w.mu.Unlock()
    
    // 1. Resolve prev_hash
    prevHash, err := w.resolvePrevHash(ctx)
    if err != nil { return 0, fmt.Errorf("audit Write prev_hash: %w", err) }
    
    // 2. INSERT (omit row_hash; assign via prev_hash)
    var sessionIDArg interface{}
    if sessionID == "" { sessionIDArg = nil } else { sessionIDArg = sessionID }
    res, err := w.db.ExecContext(ctx,
        "INSERT INTO audit_log (actor, session_id, payload, prev_hash) VALUES (?, ?, ?, ?)",
        actor, sessionIDArg, payload, prevHash)
    if err != nil { return 0, fmt.Errorf("audit Write insert: %w", err) }
    id, err := res.LastInsertId()
    if err != nil { return 0, fmt.Errorf("audit Write LastInsertId: %w", err) }
    
    // 3. Compute row_hash (canonical encoding §3.2)
    createdAt, err := w.fetchCreatedAt(ctx, id)
    if err != nil { return id, fmt.Errorf("audit Write created_at: %w", err) }
    rowHash := computeRowHash(prevHash, id, actor, sessionID, payload, createdAt)
    
    // 4. UPDATE row_hash
    _, err = w.db.ExecContext(ctx,
        "UPDATE audit_log SET row_hash = ? WHERE audit_id = ?",
        rowHash, id)
    if err != nil { return id, fmt.Errorf("audit Write update row_hash: %w", err) }
    
    // 5. Update mirrors
    w.lastID = id
    w.lastHash = rowHash
    return id, nil
}
```

### 4.3 Bootstrap (resolve prev_hash)

```go
func (w *Writer) resolvePrevHash(ctx) ([]byte, error) {
    if w.lastHash != nil {
        return w.lastHash, nil  // hot path
    }
    // Cold start: read most recent row from DB
    var prevHash []byte
    err := w.db.QueryRowContext(ctx,
        "SELECT row_hash FROM audit_log ORDER BY audit_id DESC LIMIT 1").Scan(&prevHash)
    if err == sql.ErrNoRows {
        return zeroHash, nil  // empty table
    }
    if err != nil { return nil, err }
    if prevHash == nil {
        return zeroHash, nil  // legacy rows (post-migration, pre-Phase-2)
    }
    return prevHash, nil
}
```

`zeroHash` is a package-level `var zeroHash = make([]byte, 32)`. Reusing
the same slice avoids allocation on the hot path.

### 4.4 Why INSERT-then-UPDATE (not single INSERT)

We can't put `row_hash` in the INSERT because we need `audit_id` (from
`LastInsertId`) to compute it. Two options:

- **A**: single INSERT with placeholder, compute row_hash via SQL
  extension (modernc.org/sqlite does NOT bundle a SHA-256 function).
- **B**: INSERT without row_hash, then UPDATE.

Option B is what we use. Cost: 1 extra UPDATE per Write (~1ms total,
negligible). Benefit: pure-Go hash, no SQL extension, no migration risk.

**Edge case**: if the UPDATE fails (very unlikely — could happen if disk
fills mid-write), the row exists but `row_hash` is NULL. Verify flags
this row as "hash missing" — operator can investigate.

### 4.5 WriteExec (tx variant)

`WriteExec` follows the same pattern as `Write`:

1. Resolve prev_hash **inside** the tx (read from executor = tx)
2. INSERT with prev_hash
3. Read back audit_id via LastInsertId
4. UPDATE row_hash (still inside the tx)

**Critical**: `prev_hash` is resolved inside the tx so we see a
consistent snapshot. If WriteExec is called inside an `agent_memory.WithTx`
block, the prev_hash reflects the DB state at tx BEGIN, not the live
state. This is correct semantics — the new audit row is part of the
same atomic unit.

### 4.6 Cross-process correctness

The BUG-12 fix (cross-process audit_id monotonicity) makes the chain
safe across processes. Two processes writing concurrently:

- Process A: prev_hash = N's hash → INSERT id=K+1 → row_hash computed → UPDATE
- Process B: prev_hash = (also N's hash, since A's row hasn't committed yet) → INSERT id=K+2 → row_hash computed → UPDATE

**Both processes computed the same prev_hash** but got different
audit_ids. Process B's prev_hash is **wrong** (it should reference A's
row_hash_K+1, not N's hash). Process B's chain is now forked at row K+2.

**Detection**: the verify tool walks the chain and detects the fork at
K+2 (because prev_hash doesn't match the previous row's row_hash).

**Mitigation**: callers that care about strict chain integrity should
use `WriteExec` inside a tx with `BEGIN IMMEDIATE` (write lock). The
audit Writer itself can't enforce this — it's a caller discipline.

For the audit_log write pattern today (synchronous, single-process
dominant), this is a non-issue. Concurrent writes from multiple
processes are the exception, not the rule. Document the limitation.

---

## 5. Verify tool — `dark_memory_audit_verify`

### 5.1 MCP tool signature

```go
type auditVerifyInput struct {
    StartID *int64 `json:"start_id,omitempty"` // default: smallest non-NULL prev_hash row
    EndID   *int64 `json:"end_id,omitempty"`   // default: MAX(audit_id)
}

type auditVerifyOutput struct {
    Verified   bool   `json:"verified"`
    BrokenAt   int64  `json:"broken_at"`   // 0 if verified, else first broken audit_id
    StartID    int64  `json:"start_id"`
    EndID      int64  `json:"end_id"`
    Count      int    `json:"count"`        // rows walked
    ElapsedMS  int64  `json:"elapsed_ms"`
}
```

Tool count: 41 → 42 (one new tool, additive).

### 5.2 Behavior

Walk `audit_log` in `[start_id, end_id]` order. For each row:

1. **If `row_hash` is NULL**: skip (legacy row). Remember the position
   as a "trust anchor" — verify restarts from the next non-NULL row.
2. **If `row_hash` is non-NULL**:
   - Recompute `row_hash_expected = SHA256(prev_hash || ...)` per §3.2.
   - If `row_hash != row_hash_expected`: `broken_at = audit_id`,
     `verified = false`, return early.
   - **For row 1** (or first non-NULL row): `prev_hash` must be
     `zeroHash` (32 zero bytes). Otherwise `broken_at = audit_id`.
   - **For row N > 1**: `prev_hash` must equal `row_hash` of row N-1.
     Otherwise `broken_at = audit_id`.

If the loop completes without breaks: `verified = true`, `broken_at = 0`.

### 5.3 Default range

- **start_id**: smallest audit_id with non-NULL `row_hash` (or 1 if all
  NULL). Computed via `SELECT MIN(audit_id) FROM audit_log WHERE
  row_hash IS NOT NULL`.
- **end_id**: `SELECT MAX(audit_id) FROM audit_log`. (Pre-BUG-12 fix,
  this was ambiguous; post-BUG-12, AUTOINCREMENT makes it well-defined.)

### 5.4 Legacy rows (NULL hashes)

Rows emitted before Phase 2 deployment have `prev_hash = NULL` and
`row_hash = NULL`. The verify tool:

- Skips them (treats as "before the chain started").
- Uses the first non-NULL row as the new row 1.
- The first non-NULL row's `prev_hash` MUST be `zeroHash` (not NULL,
  not anything else).

This is **forward-compatible**: the chain picks up from wherever
Phase 2 first writes.

### 5.5 Concurrency

Verify is **read-only**. No locks taken. The verify snapshot is
point-in-time — a row committed DURING the verify may or may not be
seen, depending on SQLite's snapshot isolation. **Acceptable** — the
goal is "did anyone tamper with the past", not "is the live DB
consistent right now".

### 5.6 Tool handler

```go
// internal/v4alpha/transport/mcp/audit_verify.go
package mcp

func (s *Server) handleAuditVerify(ctx context.Context, in auditVerifyInput) (auditVerifyOutput, error) {
    start, end, err := s.audit.Verify(ctx, in.StartID, in.EndID)
    // ... build output
}
```

The actual walk is in `internal/v4alpha/audit/verify.go` — pure DB code,
testable without MCP machinery.

---

## 6. Schema migration

### 6.1 Strategy

`ALTER TABLE × 2` to add the columns. Both nullable. No data backfill.

```sql
-- migration v4alpha/2026-09-30/001
ALTER TABLE audit_log ADD COLUMN prev_hash BLOB;
ALTER TABLE audit_log ADD COLUMN row_hash  BLOB;
```

### 6.2 Pre-Phase-2 rows

Existing rows have NULL for both new columns. Verify treats them as
"trust anchors" (§5.4). No data migration needed.

### 6.3 First post-migration Write

`prev_hash` resolves to:
- `zeroHash` if the table is empty.
- The most recent non-NULL `row_hash` if there are post-Phase-2 rows.
- `zeroHash` if all existing rows have NULL `row_hash` (legacy-only).

In the legacy-only case, the new row's prev_hash is `zeroHash` (NOT
the legacy rows' hash, because there isn't one). This is correct: the
chain starts NOW. Legacy rows are orphans.

### 6.4 Schema version

`schema_migrations` gets:
- `v4alpha/2026-09-30/001` — `audit_log_chain` (ALTER TABLE × 2)

No row bumps, no column drops. Idempotent.

---

## 7. Edge cases + failure modes

| Case | Behavior |
|---|---|
| Empty audit_log | First Write: prev_hash = zeroHash. Chain starts. |
| First Write post-migration | All existing rows NULL → first Write gets prev_hash = zeroHash. Chain starts fresh. |
| Mid-migration crash (column added but UPDATE not committed) | Column exists, row has NULL row_hash. Verify skips. Next Write succeeds. |
| UPDATE row_hash fails (rare) | Row exists with NULL row_hash. Verify flags as "hash missing" — actually verify SKIPS (NULL row_hash = trust anchor). Operator must investigate. |
| Two processes race for prev_hash | Both get same prev_hash, get different audit_ids, second's row_hash is wrong. Verify detects at row K+2. |
| Process restart mid-Write | Idempotent on retry (insert new row; sqlite_sequence advances either way). Verify may flag the row as orphan. |
| Corrupted row_hash field (e.g., truncated) | SHA-256 recompute ≠ stored value. Verify: broken_at = this audit_id. |
| NULL payload | payload || 0x00 || created_at || 0x00 in the canonical encoding. SHA-256 over (zero-length bytes, then separators) — well-defined. |
| created_at modified by DB trigger | Triggers that modify created_at would break the chain. Out of scope — no triggers defined today. |
| Time zone | `CURRENT_TIMESTAMP` is UTC. RFC3339 string. Deterministic. |

---

## 8. Test plan

### 8.1 Unit tests (L1 — rapid 20 iter, no DB)

- `TestCanonicalEncoding_Deterministic`: same inputs → same hash. 1000 random inputs.
- `TestCanonicalEncoding_Separators`: actor "fo" + session_id "o" vs actor "foo" + session_id "" must produce different hashes.
- `TestComputeRowHash_NoSeparatorAmbiguity`: payload ending in ASCII that looks like RFC3339 doesn't fuse with created_at.
- `TestZeroHash_Stable`: zeroHash is always 32 zero bytes.

### 8.2 Integration tests (L2 — stdlib boundaries, file-backed DB)

- `TestWriter_HashChain_LinearSequence`: 100 Writes. Verify all chain.
- `TestWriter_HashChain_PostMigration`: write 50 legacy rows (NULL hashes), then 50 chained rows. Verify chains from row 51.
- `TestWriter_HashChain_CrossProcess`: 2 *sql.DB handles, 100 Writes interleaved. Verify catches the fork (expected broken_at if no IMMEDIATE lock).
- `TestWriter_HashChain_RestartMidSequence`: Writer 1 writes 10 rows; Writer 2 starts (new process), bootstraps prev_hash from DB, writes 10 more. Verify chains.
- `TestWriter_HashChain_RowCorruption`: manually UPDATE row K to corrupt row_hash. Verify broken_at = K.
- `TestWriter_HashChain_Deletion`: DELETE row K. Verify broken_at = K+1.
- `TestWriter_HashChain_Rewrite`: UPDATE row K's payload. Verify broken_at = K.

### 8.3 Property tests (L1 — go-mutesting compatible)

- `TestProperty_ChainLength_MatchesRowCount`: for any N writes, chain length = N (no extra rows, no missing).
- `TestProperty_Verify_AcceptsCleanChain`: 100 random sequences → verify always returns verified=true.
- `TestProperty_Verify_DetectsModification`: 100 random modifications → verify always returns broken_at = modified row.

### 8.4 Tool tests (L4 — drift_judge via spec compliance)

- `TestAuditVerify_MCP`: call `dark_memory_audit_verify()` over 100 rows. Output matches expected.
- `TestAuditVerify_RangeLimit`: call with start_id=10, end_id=20. Output count = 11.
- `TestAuditVerify_EmptyRange`: end_id < start_id → error, no rows walked.

### 8.6 Coverage goal ≥80% on new code (audit.Verify + Writer additions).

---

## 9. Acceptance criteria

- [ ] `audit_log` has `prev_hash BLOB` and `row_hash BLOB` columns (nullable).
- [ ] `audit.Writer.Write` and `WriteExec` write `prev_hash` and `row_hash` on every new row.
- [ ] First row has `prev_hash = 0x00 * 32` (zeroHash).
- [ ] Subsequent rows have `prev_hash = previous row's row_hash`.
- [ ] `dark_memory_audit_verify` tool returns 200 with `{verified, broken_at, start_id, end_id, count, elapsed_ms}`.
- [ ] Verify detects: modification (rewrite), deletion, payload swap.
- [ ] Verify passes on: clean chain, post-migration chain (legacy NULLs).
- [ ] Migration `v4alpha/2026-09-30/001` applied on schema startup; idempotent.
- [ ] All 15 existing audit tests still pass (no regressions).
- [ ] All 11 v4alpha packages pass (no regressions).
- [ ] Tool count: 42.
- [ ] Schema version bumped in `schema_migrations`.
- [ ] `CHANGELOG.md [4.0.0-alpha.15]` entry.
- [ ] `docs/v4-status.md`: alpha.14 → alpha.15.
- [ ] `docs/INVARIANTS.md §18` updated: chain integrity gap CLOSED for hash chain; Ed25519 still deferred.
- [ ] Atomic mirror: 1 SUMMARY pinned + 4 SECTION (option B rationale, hash design, verify semantics, ADR-017 deferred).

---

## 10. ADR-017 deferred (out of scope)

### 10.1 What ADR-017 would add

```sql
ALTER TABLE audit_log ADD COLUMN signature BLOB;  -- 64 bytes (Ed25519)
ALTER TABLE audit_log ADD COLUMN pubkey_id TEXT;  -- which key signed this row
```

Each Write:
1. Compute `row_hash` (as today, post-Phase 2).
2. Sign `row_hash` with the operator's Ed25519 private key.
3. INSERT with `signature = sign(row_hash)`.
4. `pubkey_id` is operator-controlled (key rotation support).

Verify:
1. Recompute `row_hash` (chain integrity).
2. Verify `signature = sign(pubkey, row_hash)` (payload integrity).
3. Both checks must pass.

### 10.2 Why deferred

- **No external verifier today** (§2.2).
- **Key management is unsolved**: who holds the private key?
- **Cost is real**: 30-50μs per Write, 50-100μs per verify, +64 bytes/row.
- **Forward-compat preserved**: when ADR-017 lands, the canonical encoding
  can include `signature` in the hash input (so signatures cover the chain
  too, not just the row).

### 10.3 When to revisit

Trigger conditions for ADR-017:
- An external auditor use case appears (compliance review, supply chain).
- The operator wants cryptographic non-repudiation for legal reasons.
- A multi-tenant case where another party must verify WITHOUT trusting
  the dark-memory process.

Until any of those triggers, ADR-017 stays deferred. The hash chain
covers the realistic threat model.

---

## 11. Implementation plan

### 11.1 Files modified / created

| File | Action | LoC |
|---|---|---|
| `internal/v4alpha/audit/writer.go` | MODIFY (add `lastHash`, chain in Write) | +60 |
| `internal/v4alpha/audit/writer_tx.go` | MODIFY (same chain in WriteExec) | +40 |
| `internal/v4alpha/audit/canonical.go` | NEW (canonical encoding + SHA-256) | +60 |
| `internal/v4alpha/audit/canonical_test.go` | NEW (L1 unit tests) | +100 |
| `internal/v4alpha/audit/verify.go` | NEW (verify walker) | +120 |
| `internal/v4alpha/audit/verify_test.go` | NEW (L2 integration tests) | +200 |
| `internal/v4alpha/audit/writer_chain_test.go` | NEW (L2 integration tests for chain) | +150 |
| `internal/v4alpha/store/migrate.go` | MODIFY (add migration v4alpha/2026-09-30/001) | +10 |
| `internal/v4alpha/transport/mcp/audit_verify.go` | NEW (MCP tool handler) | +60 |
| `internal/v4alpha/transport/mcp/server.go` | MODIFY (register tool, count 41→42) | +5 |
| `internal/v4alpha/transport/mcp/server_test.go` | MODIFY (assert count=42) | +2 |
| `docs/specs/SPEC-alpha-11-phase2.md` | NEW (this file) | +400 |
| `docs/v4-status.md` | MODIFY (alpha.14 → alpha.15) | +5 |
| `CHANGELOG.md` | MODIFY ([4.0.0-alpha.15] entry) | +50 |
| `docs/INVARIANTS.md §18` | MODIFY (chain integrity gap CLOSED; Ed25519 deferred) | +10 |
| **Total** | | **~+1272** |

### 11.2 Sequence

1. Write canonical.go + canonical_test.go (pure Go, no DB).
2. Modify writer.go + writer_tx.go.
3. Write writer_chain_test.go.
4. Write verify.go + verify_test.go.
5. Modify store/migrate.go (add migration).
6. Write transport/mcp/audit_verify.go.
7. Modify transport/mcp/server.go + server_test.go.
8. Update v4-status.md + CHANGELOG.md + INVARIANTS.md.
9. Commit + atomic mirror.
10. Update opencode.jsonc if needed (no — verify tool is additive).
11. Rebuild `cmd/dark-memory-v4/dark-memory-v4.exe`.

### 11.3 Risk

| Risk | Mitigation |
|---|---|
| Cross-process race causes permanent chain fork | Document limitation; recommend IMMEDIATE for multi-process callers. Verify detects it. |
| UPDATE row_hash fails | Row inserted with NULL hash; verify skips. Operator investigates. |
| Performance regression (extra SELECT + UPDATE per Write) | Benchmark before/after; expected +1-2ms per Write. Acceptable. |
| Migration on huge existing audit_log | Migration is `ALTER TABLE ADD COLUMN` — O(1) in SQLite. No backfill. |
| Existing audit tests break | New tests are ADDITIVE; existing tests don't touch hash columns. |

---

## 12. Cross-references

- `docs/v4-alpha-11-plan.md` §2 — Phase 2 origin
- `docs/sota-critique.md` §5.3 — 8 audit gaps (this phase closes #1 + #6, defers #5)
- `docs/INVARIANTS.md §18` — SOTA criticism of audit chain (to be updated)
- `docs/specs/SPEC-alpha-11-bug12.md` — Phase 1D (cross-process monotonicity, prerequisite)
- `docs/specs/SPEC-alpha-11-pre1c3.md` — Loadout (queries audit_log)
- `docs/specs/SPEC-alpha-11-pre1c4.md` — summarize_session (queries audit_log by session_id)
- row 2153 (chunk 3 §D) — 8 gaps, this phase closes 2
- row 2172 (impl-org §C) — OD1-OD5 decisions
- row 2182 (PRE-1 C3 SUMMARY) — Loadout uses audit_log
- row 2186 (BUG-12 SUMMARY) — prerequisite fix
- `internal/v4alpha/audit/writer.go` — current state
- `internal/v4alpha/audit/writer_tx.go` — current state
- SQLite docs: <https://www.sqlite.org/lang_createtable.html#rowid>
- immudb VerifiableGet: <https://docs.immudb.io/>
- Rekor log overview: <https://docs.sigstore.dev/rekor/overview/>

---

## 13. Open questions for operator

1. **Tool naming**: `dark_memory_audit_verify` (verbose) vs `dark_memory_verify_chain` (clearer) vs `dark_memory_chain_check` (terse). Default: `dark_memory_audit_verify` (matches ADR-018 naming).
2. **Verify-by-actor**: should verify filter by actor? E.g., `dark_memory_audit_verify(actor="nico")` walks only nico's writes. Default: NO (whole audit log; sub-filtering is v2).
3. **Batch verification**: should `audit_verify` support checking across `audit_log` AND `agent_memory` (cross-table consistency)? Default: NO — single-table scope. agent_memory has its own audit trail already.

If operator has no objections, defaults apply.

---

## 14. Sign-off

- **Spec author**: Opita-AI (MiniMax-M3), 2026-09-29
- **Operator decision**: Option B (2026-09-29, message "b")
- **Pre-flight review**: chunk 3 §D gap analysis (row 2153), impl-org §C OD1-OD5 (row 2172), Phase 1 close (BUG-12 commit 7ebfa26)
- **Next step**: await operator go-ahead to write code (canonical.go first)