# ADR-020 — Which audit chain is authoritative, and what audit_verify actually proves

- **Status:** Accepted (measurement). Remediation proposed, not yet scheduled.
- **Date:** 2026-10-09
- **Loop:** 22 — establish the real boundary of the shipped binary (spec 1906)
- **Supersedes:** nothing. **Amends:** the audit claims in `docs/v4-status.md` §1.2,
  §1.3 and the `dark_memory_audit_verify` tool description.

## Context

The architecture review set out to answer one narrow question: with two audit
packages in the shipped binary, which chain is the record of truth under INV-1?

The question turned out to be badly posed, and finding out why changed the
conclusion. The two packages are not competing chains. One of them has never
written anything.

## Measurement

Every claim below was checked against the running system, not against the code
comments.

### The two packages

| | `internal/audit/` | `internal/v4alpha/audit/` |
|---|---|---|
| LoC | 1,542 | 4,199 |
| Reachable from `cmd/dark-mem-mcp` | yes | yes |
| Table | `write_audit` | `audit_log` |
| Writes | no (reads/exports) | yes |
| Chain | HMAC-SHA256, derived at export | SHA-256 row hash, stored columns |
| Rows in the live database | **28,775** | **table does not exist** |

### `audit_log` does not exist anywhere

Searched every non-federation database on the machine:

```
dark.db          104,837,120 bytes   write_audit
dark-memory.db      618,496 bytes   write_audit
```

Neither has an `audit_log` table. `v4alpha/audit.CreateSchema` issues
`CREATE TABLE IF NOT EXISTS audit_log`; if any v4alpha writer had ever run
against either database, the table would be there. It is not. The shipped boot
path in `cmd/dark-mem-mcp/legacy_main.go` does not call it.

The SHA-256 row-hash chain (ADR-002-era), the Ed25519 row signatures (ADR-017)
and the payload columnar split (ADR-019) therefore protect nothing on this
system. They are fully implemented and fully tested, which is precisely why
they read as shipped features in the status document.

**This is a lesson about measurement, not a criticism of the code.** The
Loop 22 reachability guard reports `v4alpha/audit` as reachable — it is linked
into the binary. Reachability is not execution. A package can be compiled,
tested, documented as delivered, and never called, and every static signal will
report it as alive. The database was the only instrument that said otherwise.

### `write_audit` stores no chain

Measured on the live table, 14 columns:

```
id, table_name, row_id, actor, session_id, write_path, content_sha256,
canary_present, constitution_id, constitution_ver, notes, created_at,
project_id, session_event
```

No `chain_prev`, no `chain_self`, no `row_hash`, no signature column. The
`WriteEvent` type doc says so explicitly: *"They are NOT persisted in the
write_audit table (the JSONL stream is a derived view; the canonical truth is
the SQL rows)."*

## Decision

**`write_audit` is the authoritative audit trail under INV-1.** It is the table
INV-1 names, it holds every real write, and the store writes it inside the same
transaction as the data write.

**`internal/v4alpha/audit/` is not an audit trail. It is an unwired
implementation.** It is not registered in the canonical tool registry
(`RegisterAudit` in `internal/tools/audit.go:49` is the only registrar, and it
uses `internal/audit`), its table does not exist, and no production write path
reaches it.

## What audit_verify actually proves

This is the part that matters operationally, and it is narrower than documented.

`ExportJSONL` mints the chain at export time by walking whatever rows
`ListWrites` currently returns (`export.go:104-126`). `VerifyJSONL` re-derives it
from that same stream (`verify.go:86`). Both operate on a snapshot. **Nothing
binds the snapshot to any earlier state of the database, because no earlier
state is stored anywhere.**

Demonstrated executably in `internal/audit/gap_test.go`:

| Tampering | Detected? | Result |
|---|---|---|
| Row deleted from `write_audit` **before** export | **No** | `status=ok`, 9/9 rows verified |
| Row content edited **before** export | **No** | `status=ok`, 6/6 rows verified |
| Row edited in the exported JSONL **after** export | Yes | `status=broken`, `first_bad_id=6`, "HMAC mismatch" |

Deletion is invisible because the chain re-links across the gap: the row after
the missing one has its `chain_prev` computed from the last surviving row, so
continuity holds and the verifier has nothing to flag. Pre-export edits are
invisible because the exporter mints `chain_self` over the edited bytes — the
edit signs itself.

`verify.go:16-18` states the opposite: *"a missing row mid-chain breaks the
chain but is NOT a verifier bug."* For the shipped path that is false.

### The property that holds

`audit_verify` proves: **the stream in hand was minted by a holder of key `v1`
and has not been modified since export.** That is a real property — it is what
protects an exported archive from being edited in transit or in storage. It is
much smaller than the documented promise, and it says nothing about the
database that produced the stream.

## Consequences

1. `docs/v4-status.md` §1.2 claims `dark_memory_audit_verify` "detects
   modification, deletion, forgery". Corrected in this commit. The tool
   detects modification of the *stream*.
2. INV-1 is satisfied. The store writes `write_audit` in the same transaction
   as the data write, which is the invariant's actual requirement.
3. The audit trail has **no tamper-evidence against a database attacker**. An
   attacker with write access to `dark.db` can edit or delete audit rows freely;
   a later export verifies clean. This is the honest statement of the residual
   risk and it is not currently written down anywhere.

## Remediation options, none scheduled

- **Anchor the chain in the database.** Store `chain_self` per row at write
  time, so the chain is a property of the data rather than of a later snapshot.
  This is what makes deletion detectable, because a gap in the stored chain
  cannot be re-minted away. Cost: schema migration, and the write path gains a
  dependency on the previous row.
- **Sign and periodically publish a root hash.** Export a signed digest of the
  chain head to somewhere outside the database. Cheap, and converts silent
  wholesale deletion into a detectable divergence, but does nothing about
  edits.
- **Port the v4alpha cryptography onto `write_audit`.** The Ed25519 signing
  (ADR-017) is real, tested work that would be a genuine improvement here, and
  it is currently protecting a table that does not exist. The least wasteful
  resolution, but it is a migration, not a copy.

The third is the one the evidence points at. Choosing between them is an
operator decision, not an implementation detail.

## What this ADR deliberately does not do

It does not delete `internal/v4alpha/audit/`. 4,199 LoC of tested cryptography
is not removed on the strength of one machine's database — other deployments
may have an `audit_log`, and a future path may intend to adopt it. It records
that on the system measured, it protects nothing.
