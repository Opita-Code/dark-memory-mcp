# Audit HMAC key rotation policy (dark-memory v4)

**Status (2026-10-03)**: ✅ Phase 11 T-401 active.

**Scope**: operator-facing policy for the `DARK_AUDIT_HMAC_KEY` env var that
powers the `dark_memory_audit_export` + `dark_memory_audit_verify` HMAC chain
(ADR-016 + ADR-018).

## Why this document exists

The `DARK_AUDIT_HMAC_KEY` env var enables the write_audit chain to be
HMAC-signed and verified. The chain is computed at **export time**, not
stored in `write_audit` — the `chain_prev` / `chain_self` /
`chain_key_id` fields appear only in the JSONL stream emitted by
`audit_export` (see `internal/audit/export.go:104-126`).

When the env var is unset, the `audit_export` + `audit_verify` MCP
tools are **silently not registered** (`internal/tools/register.go:180-185`).
This is by design — legacy operators don't care about audit chain
verification. Operators who want it set the env var per this policy.

## Format

```
DARK_AUDIT_HMAC_KEY="v1:<hex32bytes>[,v2:<hex32bytes>[,...]]"
```

- `v1` is the **key id**, free-form, used for rotation
- `<hex32bytes>` is **32 bytes (256 bits) hex-encoded** (64 hex chars)
- Multiple keys comma-separated — supports rolling rotation
- Total format: `id:hexsecret,id:hexsecret,...`

Per `internal/audit/hmac.go:77-87`:
```go
func KeyringFromEnv(env string) (*Keyring, error) {
    raw := strings.TrimSpace(env)
    if raw == "" {
        return &Keyring{keys: map[string][]byte{}}, nil
    }
    return NewKeyring(strings.Split(raw, ",")...)
}
```

## Generation

Generate with Go's `crypto/rand` (32 bytes → hex-encode):

```go
b := make([]byte, 32)
if _, err := rand.Read(b); err != nil { panic(err) }
fmt.Printf("v1:%s\n", hex.EncodeToString(b))
```

Or in PowerShell (Windows):
```powershell
$b = New-Object byte[] 32
(New-Object System.Security.Cryptography.RNGCryptoServiceProvider).GetBytes($b)
"v1:" + [BitConverter]::ToString($b).Replace('-', '').ToLower()
```

Or in bash:
```bash
v1=$(openssl rand -hex 32)
echo "v1:$v1"
```

## Storage

The key is currently stored **plaintext** in
`~/.config/opencode/opencode.jsonc` env block. This matches the
existing pattern for `MINIMAX_API_KEY` (hardcoded literal since
2026-09-03, override 1443). The operator accepts the plaintext risk
in exchange for restart-free deploys.

**Operator backup (recommended, NOT enforced)**:
- Save a copy of the key offline (password manager, encrypted USB, etc.)
- Without the key, **old exports become unverifiable** (chain can't be
  recomputed). The DB rows themselves remain valid; only the JSONL
  chain breaks.

## Rotation policy (event-driven, NOT periodic)

Aligned with **NIST SP 800-57 Part 1 Rev. 5** and **OWASP Key
Management Cheat Sheet**:

1. **Trigger**: key compromise OR operator decision to rotate (every
   ~6 months recommended, not enforced).
2. **Procedure**:
   - Generate `v2:<hex>` (new key).
   - Append to the env var: `DARK_AUDIT_HMAC_KEY="v1:<old>,v2:<new>"`.
   - The verifier can still verify old rows stamped with `v1` (look
     up the v1 key in the keyring).
   - New exports should stamp `v2` (the new "primary"). To force
     this, pass `KeyID="v2"` to `audit_export` (default = primary
     iterated first).
   - After **all operators have re-exported with v2**, retire v1 by
     removing it from the env var.
3. **Compromise**: if v1 leaks, remove v1 IMMEDIATELY. Old exports
   stamped with v1 become unverifiable. Audit logs of v1-stamped
   exports are valid historical record but no longer verifiable.

## Operational constraint: opencode restart required for env changes

The supervisor (`cmd/dark-mem-mcp-supervisor/main.go:109`) inherits
env from its parent (the harness). The harness sets env only at
opencode startup. Therefore:

- Updating `DARK_AUDIT_HMAC_KEY` in `opencode.jsonc` requires a
  **full opencode restart** to take effect (NOT a deploy procedure
  touch+reload — that only cycles the binary, not the env).
- The deploy procedure (touch `bin/dark-mem-mcp.exe.reload`) is for
  binary code changes only.

## Verification procedure (post-rotation)

After updating `DARK_AUDIT_HMAC_KEY`:

1. Restart opencode.
2. Confirm `audit_export` + `audit_verify` MCP tools appear in
   `tools/list` (they're conditionally registered based on
   `len(auditKr.IDs()) > 0`).
3. Call `audit_export` → save JSONL output.
4. Call `audit_verify` against the JSONL → expect `Status=OK`.

If `Status=Broken` or `Status=UnknownKey`: see §Troubleshooting below.

## Troubleshooting

| Status | Cause | Fix |
|---|---|---|
| `OK` | All rows verified. | None. |
| `Broken` | chain_prev of row N+1 ≠ chain_self of row N. | Either (a) a row was tampered with, (b) export + verify used different keys, (c) a row was deleted. Re-export + re-verify with the SAME key to rule out (b). |
| `UnknownKey` | chain_key_id of a row not in the verifier's keyring. | Update the env var to include the missing key id. |
| `Malformed` | a row is missing chain_prev / chain_self / chain_key_id fields. | Re-export to regenerate the chain data. |

## Cross-refs

- `internal/audit/hmac.go:77-87` — `KeyringFromEnv`
- `internal/audit/hmac.go:62-75` — `NewKeyring` (the canonical pair parser)
- `internal/audit/export.go:104-126` — chain computation at export time
- `internal/audit/verify.go:31-37` — Status enum (OK/Broken/UnknownKey/Malformed)
- `internal/tools/register.go:180-185` — conditional registration based on keyring
- `internal/tools/audit.go:154-158` — `ErrAuditNoKeyring` sentinel
- `docs/deploy-mcp-binary.md §X` — deployment procedure
- `docs/v4-alpha-20-1-decision.md §10 OD7` — gate enforcement

## Atomic mirror

This doc is referenced from atomic mirror row 2398 (T-401 SUMMARY
pinned) when Phase 11 ships to alpha.22.