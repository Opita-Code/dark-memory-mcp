# Constitution — dark-memory-mcp

**ID:** `release-integrity`
**Version:** `1.0.0`
**Established:** 2026-07-18 (DARK-MEM-001)
**Scope:** `dark-memory-mcp` repository. The sibling `dark-research-mcp`
follows its own `dark-research-constitution` (see its `ARCHITECTURE.md`).

This constitution codifies the release-integrity rules that govern
`dark-memory-mcp` development. Every `vibe_publish` artifact published
under this project is evaluated by `dark_memory_drift_judge` against
these rules. Violations surface as `drift_detected` verdicts in the
active policy.

---

## Rule 1 — Single source of truth for version

The version reported by any binary in this repository MUST be derivable
from the git tag at the commit the binary was built from, in this order
of priority:

1. **`-ldflags "-X github.com/dark-agents/dark-memory-mcp/internal/version.Version=<v>"`**
   injected at build time by the `make release` target. This is the
   canonical path and is required for every release build.
2. **`runtime/debug.ReadBuildInfo().Main.Version`** — used for
   `go install` and ad-hoc dev builds. The Makefile target `make dev`
   relies on this path.
3. **Hardcoded fallback** in `internal/version/version.go` (`const devVersion = "dev"`)
   is reserved for emergency debugging only. Any build that resolves
   here MUST emit a `drift_warning` field in the `dark_memory_health_ping`
   response (see Rule 4).

If `dark_memory_health_ping.git.tag` does not match the
`dark_memory_health_ping.server.version` field, the response MUST
include a `drift=true` field and the active policy MUST be marked as
`constitution_drift=true`. The MCP also dispatches a
`vlp_handle_event(event=drift_log, verdict=drift_detected)`.

This rule is enforced by `tests/wire/health_ping_test.go`.

## Rule 2 — Archive, do not delete

Deprecation of code in this repository MUST follow the archive pattern:

- Files move to `archive/<context>/` (e.g. `archive/pre-federation/`).
- The original location is replaced with a thin deprecation shim (Go) or
  a `<NAME>.DEPRECATED.md` marker (docs).
- The shim MUST contain a header comment with the deprecation date and
  a pointer to the archive directory.
- A `DEPRECATED.md` file at the archive root explains what was moved,
  why, and where the canonical replacement lives.

Deleting code without an archive step is a drift violation. The
`dark-research-mcp` cleanup (DARK-MEM-004) is the canonical reference
implementation of this pattern.

## Rule 3 — CHANGELOG is authoritative for releases

Every git tag MUST have a corresponding `## [<version>]` entry in
`CHANGELOG.md`. The entry MAY be added in the same commit that creates
the tag, or in any prior commit. It MUST NOT be added in a commit
descendant of the tag.

CHANGELOG entries MUST follow
[Keep a Changelog](https://keepachangelog.com/) 1.1.0 format and be
ordered newest-first. A `vibe_publish` that adds a tag without a
CHANGELOG entry will be flagged as `drift_detected`.

## Rule 4 — Drift detection on every boot

Every release of `dark-memory-mcp` MUST:

- Expose `git.tag`, `git.head_sha`, `git.dirty`, `git.build_version`
  in the `dark_memory_health_ping` response.
- When `git.tag != server.version` OR `git.dirty == true`, emit a
  `vlp_handle_event(event=drift_log, verdict=drift_detected)` with the
  `git` payload as evidence.
- When the build resolved via the dev fallback (Rule 1, priority 3),
  emit a `drift_warning` field in the health response. This is an
  informational drift signal — it does not block boot, but it is
  reported to the operator and persisted to `write_audit` (INV-1).

## Rule 5 — Session-bound governance

All `vibe_publish` and `vibe_spec` calls under this project MUST be
issued within an active session opened via
`dark_memory_session_start(operator=..., project_id=dark-mem)`. Calls
outside an active session are rejected with `ErrSessionNotActive`.

This rule enforces the per-session audit trail (INV-1, INV-2) and
prevents orphan artifacts that cannot be attributed to a human or
agent operator.

---

## How to amend this constitution

1. Bump the `Version:` field at the top of this file.
2. Open a PR with the proposed changes.
3. The PR MUST include a `vibe_publish` with `vibe_case=C1` and a
   `constitution_amendment` task in the spec.
4. After merge, the new `(release-integrity, <new-version>)` pair
   becomes the reference for `dark_memory_active_policy`. Operators
   bind the new version via `dark_memory_project_create` with
   `constitution_id=release-integrity, constitution_ver=<new-version>`.
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
  and amend the artifact. Rejection is logged to `write_audit` and the
  artifact's `validation_status` reverts to `pending`.

An unhandled `drift_detected` blocks promotion to the next stage of
the VLP state machine until resolution is recorded.
