# GATE.md — dark-memory-mcp ↔ dark-cli audit gate (consumer side)

> Slice 5 cycle 6 · 2026-09-26

This document is the **dark-memory-mcp consumer-side companion** to
[`/dark-cli/docs/AUDIT.md`](../../../dark-cli/docs/AUDIT.md), which
is the canonical cross-package protocol. Read that document FIRST
for the JSON shape, trust-root model, freshness window, and
producer flow. This file focuses on what lives in dark-memory-mcp:

- the consumer-side package (`internal/auditgate/`)
- the pre-write gate hook inside `PublishVibe`
- the env-var wiring at boot
- the cross-package wire-format pin

## 1. Why a gate, why in dark-memory-mcp

dark-memory's `vibe_publish` is the canonical "publish a generated
artifact" entry point. Without a gate, any artifact that satisfies
the schema + drift_judge path lands in dark-memory's store. That is
useful in isolation but dangerous when dark-memory is used as the
provenance index for an operator's installed bundles: an attacker
who controls an LLM-judge-able prompt could push artifacts that
claim to be "opita-market" without ever having installed anything.

dark-cli is the operator-trusted producer of `AuditRecord` files
(docs/AUDIT.md §6). The gate in dark-memory-mcp turns that producer
log into an access control: every published artifact MUST have a
corresponding signed audit record from a publisher the operator
trusts, or the publish is rejected.

## 2. What lives in dark-memory-mcp

### 2.1 `internal/auditgate/`

Three files, no production write paths (the gate is a consumer):

| File | Purpose |
|---|---|
| `record.go` | `AuditRecord`, `AuditSig`, `CanonicalBytes`, `Sign`, `Verify`, `MarshalJSONOnDisk`, `UnmarshalJSONOnDisk`, all sentinel errors, `SchemaVersion`, `FreshnessWindow`, `HashFileBytes` |
| `store.go` | `Store`, `Open`, `DefaultDir`, `Lookup`, `List`, `PutTestOnly` (test-only) |
| `gate.go` | `Gate` interface, `ReferenceGate`, `NewReferenceGate`, `Check`, `Provenance`, `ToProvenance`, `bytesEqualConstTime` |

Re-implements `dark-cli/internal/audit/` against docs/AUDIT.md.
Cross-version divergence is caught by the lockstep hash pin in
`record_test.go` + `dark-cli/internal/audit/record_test.go`
(§5 below).

### 2.2 `Orchestrator.AuditGate` + `WithAuditGate`

The orchestrator carries an optional `*auditgate.ReferenceGate`.
Wired by `WithAuditGate(gate, trustRootsDir)`. When `AuditGate` is
non-nil, every `PublishVibe` call MUST pass `gate.Check(sha256, now)`
before the artifact is admitted to dark-memory's store.

### 2.3 `PublishVibe` hook

In `internal/orchestration/publish_vibe.go`, between
`SaveArtifact` and the drift-judge pipeline:

```go
if o.AuditGate != nil {
    gateResult, gateErr := o.checkAuditGate(ctx, in)
    if gateErr != nil {
        // Persist drift_log verdict=drift_detected (mirror LLM-unavailable)
        // Return PublishResult{AuditProvenance: gateResult, ...}
    }
    o.pendingAuditProvenance = gateResult
}
```

The gate runs BEFORE `runJudgePipeline` so an untrusted artifact
never costs a judge call.

`checkAuditGate` (in `internal/orchestration/check_audit_gate.go`)
computes the SHA-256 via:

1. `in.Artifact.ArtifactRef` → resolve via `artifact.Resolver` →
   SHA the bytes (same Resolver the LLM-judge pipeline uses)
2. `in.Artifact.Text` → SHA the raw text (Phase-1 backward-compat)
3. neither → **bypass** (URL-only artifacts can't be SHAed at the
   gate layer; logged at info)

### 2.4 Boot wiring

In `internal/server/lifecycle.go`, `Boot` reads two env vars:

| Env var | Meaning | Default |
|---|---|---|
| `DARK_AUDIT_DIR` | Directory where dark-cli writes `<sha256>.json` records | unset → gate bypassed |
| `DARK_TRUST_ROOTS_DIR` | Directory of `*.pubkey` files (raw 32-byte ed25519 pubkeys) | unset → empty trust set (fail closed) |

When `DARK_AUDIT_DIR` is unset, `AuditGate` stays nil and
`PublishVibe` bypasses the check entirely. This is the default for
vanilla dark-memory-mcp installs that don't run alongside dark-cli.

When `DARK_TRUST_ROOTS_DIR` is set but doesn't exist (or is empty),
the gate is configured with an empty trust set → rejects every
artifact (fail closed). Operators MUST drop a `*.pubkey` file in
the directory for the gate to admit anything.

## 3. Gate contract (dark-memory-mcp side)

### 3.1 Decision tree

```
PublishVibe(artifact):
  1. if o.AuditGate == nil: → bypass (default)
  2. body = resolve(in.Artifact.ArtifactRef) || in.Artifact.Text
  3. if body == nil: → bypass (URL-only)
  4. sha = sha256(body)
  5. rec, err := o.AuditGate.Check(sha, o.now())
  6. if err == nil:
       → admit; attach result.AuditProvenance = rec.ToProvenance(true)
  7. if err != nil and rec == nil:
       → reject; persist drift_log verdict=drift_detected
         reasoning = "audit gate rejected: <err>"
         result.AuditProvenance = nil
  8. if err != nil and rec != nil:
       → reject (with evidence); persist drift_log verdict=drift_detected
         reasoning = "audit gate rejected: <err>"
         result.AuditProvenance = rec.ToProvenance(false)
```

### 3.2 Sentinel errors

`internal/auditgate/record.go` defines 9 record-level sentinels
matching `dark-cli/internal/audit` byte-for-byte. `gate.go` adds 3
gate-level sentinels:

| Sentinel | When |
|---|---|
| `ErrRecordNil` | record is nil |
| `ErrRecordSchema` | unknown `schema_version` |
| `ErrRecordKind` | unknown `kind` |
| `ErrRecordNoArtifact` | bad `artifact_sha256` (not 64 hex chars) |
| `ErrRecordNoPubkey` | missing `publisher_pubkey` (wrong length) |
| `ErrRecordUnsigned` | missing `signature` block |
| `ErrRecordBadAlgorithm` | non-ed25519 / wrong sig size |
| `ErrRecordSignature` | ed25519 verify failed |
| `ErrRecordStale` | `signed_at_unix` outside ±300s of `now` |
| `ErrRecordNotFound` | `Store.Lookup` no such file (renamed to `ErrGateNoRecord` at gate layer) |
| `ErrGateNoRecord` | gate couldn't find an audit record for this sha |
| `ErrGateUntrusted` | record found, but publisher pubkey not in trust roots |
| `ErrGateDisabled` | gate not configured (returned by nil gate receivers) |

All sentinels are wrapped with `%w` when surfaced from gate
methods, so callers can use `errors.Is(err, auditgate.ErrGateNoRecord)`
to branch.

### 3.3 Constant-time trust check

`ReferenceGate.isTrusted` uses `bytesEqualConstTime` (XOR-fold) per
trust root. The set is small (operator-set, single-digit) and the
loop is bounded; the FIRST match returns early. The constant-time
guarantee per root is the security invariant — an attacker probing
the gate cannot measure WHICH root matched by timing.

```go
func (g *ReferenceGate) isTrusted(pub ed25519.PublicKey) bool {
    for _, root := range g.roots {
        if bytesEqualConstTime(pub, root) {
            return true
        }
    }
    return false
}
```

Length mismatch returns false immediately (so length is not a
timing oracle either).

## 4. Tests

| Test | Layer | Purpose |
|---|---|---|
| `internal/auditgate/record_test.go` (16 tests) | unit | parallel to dark-cli `internal/audit/record_test.go` |
| `internal/auditgate/store_test.go` (9 tests) | unit | file naming, parse errors, env override |
| `internal/auditgate/gate_test.go` (9 tests) | unit | accept, reject-missing, reject-untrusted, reject-stale, multi-root |
| `internal/orchestration/publish_vibe_audit_gate_test.go` (4 tests) | E2E | PublishVibe path with SQLite-backed store |
| `internal/auditgate/record_test.go::TestExample_SignUnderlyingPackageStillDeterministic` | cross-package | canonical-bytes hash pin |
| `dark-cli/internal/audit/record_test.go::TestExample_CanonicalBytesHash_MatchesDarkMemoryMcp` | cross-package | mirror pin |

Cross-package pin: `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`.
**Both packages MUST produce this hash for the same fixture
input**. If either side changes canonicalization, both tests
fail in lockstep — divergence is impossible to miss.

## 5. Cross-package wire-format pin (the protocol integrity check)

Both packages share a single lockstep test pair:

**dark-memory-mcp** (`internal/auditgate/record_test.go`):
```go
func TestExample_SignUnderlyingPackageStillDeterministic(t *testing.T) {
    rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "/cfg/opencode",
        "0.1.0-alpha.8", "dbd30b9", "/cfg/opencode/dark.lock.json", "",
        fixedSHA, []string{"skills/a.md"}, "installed", fixedNow, fixedPub)
    _ = rec.Sign(fixedSeed, fixedNow)
    canonical, _ := rec.CanonicalBytes()
    h := sha256.Sum256(canonical)
    const want = "4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb"
    if hex.EncodeToString(h[:]) != want {
        t.Fatalf("drift")
    }
}
```

**dark-cli** (`internal/audit/record_test.go`):
```go
func TestExample_CanonicalBytesHash_MatchesDarkMemoryMcp(t *testing.T) {
    rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "/cfg/opencode",
        "0.1.0-alpha.8", "dbd30b9", "/cfg/opencode/dark.lock.json",
        fixedSHA(), []string{"skills/a.md"}, "installed", fixedNow)
    _ = rec.Sign(fixedSeed(), fixedNow)
    canonical, _ := rec.CanonicalBytes()
    h := sha256.Sum256(canonical)
    const want = "4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb"
    if hex.EncodeToString(h[:]) != want {
        t.Fatalf("drift")
    }
}
```

### Procedure on drift

If a future change makes the hashes diverge:

1. **Update BOTH packages' canonicalization together.** The
   divergence means one side is wrong, not both.
2. Re-run both tests. The new `got` hash will be identical on
   both sides (because the inputs are identical).
3. Update `want` in BOTH files in the same commit.
4. Update docs/AUDIT.md §3 (canonical-bytes rules) if the
   change is semantic, not just a refactor.

### What this catches

The pin catches any change to:

- JSON field order
- Field name (e.g. renaming `publisher_pubkey` to `publisher_key`)
- Field type (e.g. `[]byte` vs `string`)
- Indent style
- `omitempty` tags
- FilesWritten sort order
- RecordedAt representation (int64 seconds vs RFC3339 string)
- Anything else that changes the on-disk JSON shape

It does NOT catch changes that affect only the verifier logic
(those are caught by the 9 error-sentinel tests in
`record_test.go`).

## 6. Operational notes

### 6.1 Same-host setup (default)

```
$ export DARK_AUDIT_DIR=$HOME/.config/dark-cli/audit
$ export DARK_TRUST_ROOTS_DIR=$HOME/.config/dark-cli/trust
$ dark-mem-mcp
```

`dark lock apply` writes `<sha256>.json` files to
`$DARK_AUDIT_DIR`; `dark-mem-mcp` reads them on every
`vibe_publish`. Trust roots live at
`$DARK_TRUST_ROOTS_DIR/*.pubkey` (raw 32-byte ed25519 pubkeys,
same format as dark-cli's `templates/trust/*.pubkey`).

### 6.2 Cross-host setup (the §10 gap from docs/AUDIT.md)

The `DARK_AUDIT_DIR` + `DARK_TRUST_ROOTS_DIR` env vars point at
arbitrary filesystem paths, so dark-memory-mcp can read dark-cli
audit records from a shared mount (NFS, SMB, etc.) while running
on a different host. Tests cover the local case only.

### 6.3 Bypass semantics

- `DARK_AUDIT_DIR` unset → gate bypassed (vanilla install works)
- `DARK_AUDIT_DIR` set + `DARK_TRUST_ROOTS_DIR` empty/missing →
  gate rejects ALL (fail closed; operator MUST drop a `*.pubkey`
  to admit anything)
- Gate configured + no record for sha → reject
- Gate configured + record present but untrusted publisher → reject
- Gate configured + record present, trusted, but stale (>300s old)
  → reject
- Gate configured + record present, trusted, fresh → admit

There is NO `--force` knob. The gate is the security control.

## 7. What this gate does NOT cover

Same list as docs/AUDIT.md §10 (replicated for self-containment):

| Gap | Plan |
|---|---|
| Hardware-key (YubiKey) signing | v0.2 |
| Trust-root revocation propagation | C7 (`dark trust add` + sync) |
| Replay where attacker re-presents a still-fresh record for a now-revoked artifact | trust-root revocation + freshness window cover together |
| Operator forgets to set `DARK_TRUST_ROOTS_DIR` | gate fails closed (silent rejections; logged via Error Observatory) |

## 8. Cross-repo implementation map

| Concern | Lives in |
|---|---|
| Producer (write audit records) | `dark-cli/internal/audit/` |
| Consumer (gate + Check) | `dark-memory-mcp/internal/auditgate/` (this file) |
| Reference producer record shape | `dark-cli/internal/audit/record.go::AuditRecord` |
| Reference consumer record shape | `dark-memory-mcp/internal/auditgate/record.go::AuditRecord` (mirror) |
| Reference gate implementation | `dark-memory-mcp/internal/auditgate/gate.go::ReferenceGate` |
| Cross-package protocol (canonical) | `dark-cli/docs/AUDIT.md` |
| This consumer-side companion | `dark-memory-mcp/docs/GATE.md` (this file) |
| Trust roots (initial set) | `dark-cli/templates/trust/*.pubkey` |
| Trust-root loader in dark-memory-mcp | `internal/orchestration/orchestrator.go::loadPubkeysFromDir` |
| Env-var wiring | `internal/server/lifecycle.go::Boot` |
| PublishVibe hook | `internal/orchestration/publish_vibe.go` (gate check between SaveArtifact and runJudgePipeline) |
| `checkAuditGate` helper | `internal/orchestration/check_audit_gate.go` |
