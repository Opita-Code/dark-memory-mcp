# Archive — v3.0 Wave 4 (July 2026)

> **Audience**: historical reference only. These documents describe
> the Wave 4 merge of `dark-memory-mcp` v1.0.0 (mcp-go v0.56.0, dual-
> driver SQLite + Postgres, 25 canonical tools). They do NOT describe
> the current v4 redesign.

## Contents

| File | Why archived |
|---|---|
| `HUMAN_GATE_REPORT.md` | Gate report for Wave 4 merge of `review/w3p1` → `main` on 2026-07-15. Predates v4 redesign (branch `feat/v4-redesign`). Verified all RFC §12 acceptance + BRIDGE contracts + INV-1..7 invariants at the time. The 1 HIGH finding (bridge.7 cold-cache flake) was fixed during the gate. |
| `DECISION_MATRIX.md` | Operator decision matrix (5 decisions: merge review/w3p1, README timing, sub-spec 180 daemon, backlog handling, v1.0.0 tag) presented 2026-07-15. The matrix is closed (operator answered "OK all 5"); the merge happened. |

## Why a separate redesign branch exists

Per `ARCHITECTURE-V4.md §1.1`:

> v3.0 is a deliberate void. The operator decided 2026-09-24 to branch
> `feat/v4-redesign` from v2.20.0 (not v3.0-void). Three reasons:
> 1. The v3.0 changes are large but cohesive — applying them as
>    incremental patches over v2.20.0 would touch 39 internal packages.
> 2. The 30 R-recos demand structural changes — not feature patches.
> 3. The community will inherit v4 — designed for OSS maintenance.

The work in this archive predates the void declaration. It is
preserved verbatim because it documents the foundation v4 builds on
(the INV-1..INV-10 invariants, the audit chain, the dual-driver
contract, the BRIDGE coexistence contract, the canonical 25-tool
surface in `internal/tools/registry.go`).

## What v4 actually shipped (current truth)

See `docs/v4-status.md` (one-stop) or `ARCHITECTURE-V4.md §5
(revised 2026-09-27)` for the package layout, tool inventory, and
invariant adoption status of the current `feat/v4-redesign` branch.
