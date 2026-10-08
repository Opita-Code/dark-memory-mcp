# Constitution v4 — dark-memory-v4

**ID:** `release-integrity-v4`
**Version:** `0.1.0`
**Established:** 2026-09-27 (BUG-6 → BUG-8 on `feat/v4-redesign`)
**Scope:** `dark-memory-v4` repository, the redesign branch
(`feat/v4-redesign` from `v2.20.0`). The legacy `dark-memory-mcp`
production tree is governed by its own `CONSTITUTION.md` v1.0.0
(established 2026-07-18, DARK-MEM-001).

This constitution codifies the release-integrity rules for v4-alpha.x.
Every `vibe_publish` artifact published under the `dark-memory-v4`
project is evaluated by `dark_memory_drift_judge` (via the
`NoOpJudge` stub in v4-alpha.1; the LLM-backed judge lands in BUG-9)
against these rules.

---

## Rule 1 — Single source of truth for version

The version reported by the `dark-memory-v4` binary MUST be derivable
from the build, in this order of priority:

1. **`-ldflags "-X github.com/dark-agents/dark-memory-mcp/internal/v4alpha/cmd.Version=<v>"`**
   injected at build time by the release pipeline. This is the
   canonical path for release builds.
2. **`runtime/debug.ReadBuildInfo().Main.Version`** — used for
   `go install` and ad-hoc dev builds.
3. **Hardcoded fallback** in `cmd/dark-memory-v4/version.go`
   (`const devVersion = "v4alpha.1-dev"`) is reserved for emergency
   debugging only. Any build that resolves here MUST emit a
   `drift_warning` field in the `dark_memory_health_ping` response.

If `dark_memory_health_ping.git.tag` does not match the
`dark_memory_health_ping.server.version` field, the response MUST
include a `drift=true` field and the active policy MUST be marked as
`constitution_drift=true`. The MCP also dispatches a
`vlp_handle_event(event=drift_log, verdict=drift_detected)` (when the
L6-VLP tool lands in BUG-9).

This rule is enforced by `internal/v4alpha/transport/mcp/health_test.go`
once it lands; today the version is asserted in
`cmd/dark-memory-v4/version_test.go`.

## Rule 2 — Archive, do not delete

Deprecation of code in `feat/v4-redesign` MUST follow the archive
pattern:

- Files move to `archive/<context>/` (e.g. `archive/pre-federation/`,
  `docs/archive/v3.0-wave-4/`).
- The original location is replaced with a thin deprecation shim
  (Go) or a `<NAME>.DEPRECATED.md` marker (docs).
- The shim MUST contain a header comment with the deprecation date
  and a pointer to the archive directory.
- A `DEPRECATED.md` file at the archive root explains what was moved,
  why, and where the canonical replacement lives.

Deleting code without an archive step is a drift violation.

## Rule 3 — CHANGELOG is authoritative for releases

Every git tag on `feat/v4-redesign` MUST have a corresponding
`## [<version>]` entry in `CHANGELOG.md`. The entry MAY be added in
the same commit that creates the tag, or in any prior commit. It
MUST NOT be added in a commit descendant of the tag.

CHANGELOG entries MUST follow
[Keep a Changelog](https://keepachangelog.com/) 1.1.0 format and be
ordered newest-first. A `vibe_publish` that adds a tag without a
CHANGELOG entry will be flagged as `drift_detected`.

The v4 design uses `[4.0.0-alpha.1]`, `[4.0.0-alpha.2]`, ... as
pre-release segments until v4.0.0 ships.

## Rule 4 — Drift detection on every boot

Every release of `dark-memory-v4` MUST:

- Expose `git.tag`, `git.head_sha`, `git.dirty`, `git.build_version`
  in the `dark_memory_health_ping` response.
- When `git.tag != server.version` OR `git.dirty == true`, emit a
  `vlp_handle_event(event=drift_log, verdict=drift_detected)` (BUG-9).
- When the build resolved via the dev fallback (Rule 1, priority 3),
  emit a `drift_warning` field in the health response. This is an
  informational drift signal — it does not block boot, but it is
  reported to the operator and persisted to `write_audit` (INV-1).

## Rule 5 — Session-bound governance

All `vibe_publish` and `vibe_spec` calls under the `dark-memory-v4`
project MUST be issued within an active session opened via
`dark_memory_session_start(operator=..., project_id=dark-memory-v4)`.
Calls outside an active session are rejected with `ErrSessionNotActive`.

This rule enforces the per-session audit trail (INV-1, INV-2) and
prevents orphan artifacts that cannot be attributed to a human or
agent operator.

The `project_id=dark-memory-v4` value is the canonical v4 tenant. It
differs from the v3 `dark-mem` to make v4 work discoverable in shared
dark.db spaces and to keep audit trails cleanly partitioned.

## Rule 6 — INV-16 + INV-17 contract binding

Every release of `dark-memory-v4` MUST:

- Open every dark-db connection via `store.OpenSQLite` (INV-16).
- Wrap every read-modify-write in `store.WithTx` `LevelSerializable`
  (INV-16).
- Use the **canonical FTS5 sequence** in `agent_memory.Save`,
  `Update`, `Archive`: FTS5 DELETE → base UPDATE → re-read → FTS5
  INSERT (INV-17).
- Use **regular FTS5** (no `content=` clause) on every FTS5 virtual
  table the release creates (INV-17).

A release that violates INV-16 or INV-17 fails the
`internal/v4alpha/agent_memory` test suite at CI time, regardless of
which layer introduced the violation.

## Rule 7 — Publication policy (RETIRED 2026-10-08)

**This rule no longer binds.** It previously required every release on
`feat/v4-redesign` to be local-only: no `git push`, no `git fetch`, no
`git pull`, no remote tags, branches existing only in the local clone.

It is retired by operator decision. The repository is public at
`github.com/Opita-Code/dark-memory-mcp`, releases are published there,
and PRs are opened against it.

The rule was not wrong when written; it became false, because the
repository acquired a remote and went public while the rule stayed in
place. That gap is the reason this section is rewritten rather than
deleted: a silently missing rule is how a stale policy survives.
- The operator may override this rule explicitly (e.g. for a
  community-mirrored OSS snapshot) by amending this constitution and
  bumping the version.

This rule is structural — the operator's CLAUDE.md / global skill
preserves it. Any commit that attempts a `git push` is reverted on
detection.

---

## How to amend this constitution

1. Bump the `Version:` field at the top of this file.
2. Open a PR with the proposed changes.
3. The PR MUST include a `vibe_publish` with `vibe_case=C1` and a
   `constitution_amendment` task in the spec.
4. After merge, the new `(release-integrity-v4, <new-version>)` pair
   becomes the reference for `dark_memory_active_policy`. Operators
   bind the new version via `dark_memory_project_create` with
   `constitution_id=release-integrity-v4,
   constitution_ver=<new-version>`.
5. The previous version is retained in the `constitutions` table for
   audit (Rule 3 implication: CHANGELOG entry for the amendment is
   mandatory).

## Violations and remediation

A `drift_detected` verdict from `dark_memory_drift_judge` is not
auto-correcting. The operator MUST either:

- **Accept** the drift via `dark_memory_resolve_drift(decision=accept, note=...)`
  when the artifact is correct as-is (e.g. the test scaffolding
  intentionally bypasses Rule 1).
- **Reject** via `dark_memory_resolve_drift(decision=reject, note=...)`
  and amend the artifact. Rejection is logged to `write_audit` and
  the artifact's `validation_status` reverts to `pending`.

An unhandled `drift_detected` blocks promotion to the next stage of
the pipeline state machine (spec → artifact → drift_judge → complete)
until resolution is recorded.

---

## Cross-reference

- **Legacy constitution (v1.0.0)**: `CONSTITUTION.md` — governs
  v1.x and v2.x production trees. Still in force for v2.20.0.
- **Architecture**: `ARCHITECTURE-V4.md §7` — the constitutional
  contract for the workflow runtime (aspirational; the v4-alpha.1
  policy is hardcoded in `internal/v4alpha/transport/mcp/policy.go`
  until BUG-9 introduces the `policy_registry` table).
- **Invariants**: `docs/INVARIANTS.md` — INV-1..INV-10 (inherited
  from v3), INV-16 (BUG-5), INV-17 (BUG-8).
- **Status**: `docs/v4-status.md` — what's actually built vs the
  aspirational §5 of `ARCHITECTURE-V4.md`.
