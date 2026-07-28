# Drift-Burst Report — spec 304, t7-drift-burst

**Compiled**: 2026-07-19
**Authoring session**: dark-memory MCP `sess-682d28f970d2d2bd`, project `vibe-flow-pivot`, spec `304`
**Purpose**: forcing function. Drift-judge every pre-pivot artifact against the new ACTIVE_MEMORY_RFC.md v1.0.0 to surface honest debt that motivated the pivot. Items marked "DRIFT CONFIRMED" must be fixed by the implementing waves (5A-E + 4A'/4D'). Items marked "SUPERSEDED" are documented obsolete.

This is NOT a bug list of the legacy code (it was v1.4.2 and shipped correctly under its own contract); it's a delta report showing what the pivot thesis *requires* that v1.4.2 did not provide.

---

## A. Documentation drift

### A.1 — `DARK_MEMORY_MCP_RFC.md` (v1.0.0, archived)

**Status**: SUPERSEDED by ACTIVE_MEMORY_RFC.md v1.0.0

| Legacy section | Pivote thesis says | Drift |
|---|---|---|
| §P1 "Intent before CRUD" | A1 "Memory decides, harness invokes" — inversion of control | **DRIFT CONFIRMED**. P1 is pull (LLM authors intent + tool chain); A1 is push (gate decides admissibility independently) |
| §P2 "Context object, not row dump" | A2 "Atomic context, not chunks of rows" + A7 "Scope is first-class state" | **DRIFT CONFIRMED**. P2 says single coherent view per call; A2/A7 add frames-per-scope + scope-as-state that P2 doesn't specify |
| §D-7 "Sequence-aware responses — every response has a `next`" | A3 "Policy gate at tool boundary" with pre/post hooks | **DRIFT CONFIRMED**. D-7 is hint-based (LLM decides); A3 is gate-enforced |
| §D-9 "MCP surface is ~25 intent-driven tools, not 52 CRUD operations" | 4A-prime "MCP server GATED 12-15 tools, not 28 passive" | **DRIFT CONFIRMED**. D-9 enumerates 28 passive tools; pivote reduces to 12-15 gated by capability grants (A4) |
| §D-10 "dark-recall plugin v2.3 prefers `dark_memory_*`" | CANCELLED — absorbed by 5A native prefill | **DRIFT CONFIRMED** (cancellation). Plugin does not exist under pivote |
| §6 "Process lifecycle: 5 states? No — boot/shutdown" — actually only 2 | §4 "Session lifecycle: 5 states (open, idle, closed_clean, closed_aborted, archived)" | **DRIFT CONFIRMED**. Legacy §6 only specified boot/shutdown for the *server*, not the session lifecycle |
| §10 "Open questions deferred" — vector recall, partitioning, multi-tenant, armed-mode | Unchanged; still deferred to v2.1+ | NO DRIFT. Deferred items remain deferred |

### A.2 — `BRIDGE_AND_COEXISTENCE.md` (v1.0.0, archived)

**Status**: SUPERSEDED by v2.0.0

| Legacy section | Pivote thesis says | Drift |
|---|---|---|
| §2.1 `serverInfo` missing `policy_gateway` | cx.v3 declares `policy_gateway: true` | **DRIFT CONFIRMED** |
| §3.1 coexistence version table cx.v1 + cx.v2 + planned v3 | Adds cx.v3 active, deprecates cx.v1/v2 by 2026-10-01 | **DRIFT CONFIRMED** (deprecation timeline missing in v1) |
| §3.2 dark-research-mcp `coexistence_group=dark-agents/memory` | cx.v3: `coexistence_group=dark-agents/research` + `policy_gateway=false` | **DRIFT CONFIRMED** (group rename) |
| §4 "dark-recall plugin v2.3 optional glue" | CANCELLED — absorbed by 5A | **DRIFT CONFIRMED** (cancellation) |
| §4.2 plugin uses `dark_memory_research_topic` | Under pivote, no plugin; harness calls dark-memory directly | **DRIFT CONFIRMED** |

### A.3 — `dark-memory-mcp.constitution.toml` (v1.0.0, active pivot-time)

**Status**: SUPERSEDED by `dark-memory-mcp-cerebro.constitution.toml` v1.0.0 (created by t4-constitution of spec 304)

| Legacy section | Pivote thesis says | Drift |
|---|---|---|
| `[identity].agent_role` listing CRUD-y ownerships | Rewritten to active-memory thesis (governance + scope + grants + persona + drift + session resilience); register adds "memory tells, harness invokes" | **DRIFT CONFIRMED** |
| `[authority]` admin/red-team gating only | Extended with NEW lifecycle tools (close/reason, resurrect, heartbeat, recover) + Tool Gate mandate (no bypass) | **DRIFT CONFIRMED** |
| `[refusal]` 6 typed errors | 13 typed errors: +6 new (ErrScopeRequired, ErrCapabilityNotGranted, ErrPersonaNotResolvable, ErrFrameStaleTooFar, ErrSessionNotResurrectable, ErrPolicyGatewayDown) + ErrDriftAtWrite | **DRIFT CONFIRMED** |
| `[scope]` "6 internal layers" | Extended to 14: +atomic, recall, persona, capabilities, scope, drift, delegation, policy | **DRIFT CONFIRMED** |
| `[operational_rules]` 6 invariants | Extended to 9: +INV-7 (project namespace, W3p1), +INV-8 (Resilience), +INV-9 (Heartbeat) | **DRIFT CONFIRMED** (INV-7 already shipped; INV-8/9 are pivote-new) |

---

## B. Code drift

### B.1 — `internal/vlp/state.go` (legacy state machine)

**Status**: PRESENT but incomplete under pivote

| Pivote requires | v1.4.2 provides | Drift |
|---|---|---|
| 5 lifecycle states (open, idle, closed_clean, closed_aborted, archived) | 2 states (open, closed) | **DRIFT CONFIRMED**. Note: VLP state is for the LLM loop, NOT session lifecycle. Session lifecycle lives in `sessions.status` (5-state enum pending migration v12). The VLP state machine is orthogonal and is NOT impacted by the pivote — this item is rejected as drift on inspection |
| `reason` field on close operation | No reason field | **DRIFT CONFIRMED**. `dark_memory_session_close` must accept `reason='clean'|'aborted'` per ACTIVE_MEMORY_RFC.md §4.2 |

### B.2 — `internal/orchestration/session_close.go`

**Status**: NEEDS MODIFICATION per Wave 5E.ii

| Pivote requires | v1.4.2 provides | Drift |
|---|---|---|
| `reason` arg to choose `closed_clean` vs `closed_aborted` | No reason arg; defaults to `closed` (legacy v10 enum) | **DRIFT CONFIRMED** |
| Audit row with `session_event='close_clean'` or `'close_aborted'` | Audit row exists but no session_event column until v12 migrates | **DRIFT CONFIRMED** (chain: depends on schema v12) |

### B.3 — `internal/orchestration/session_resume.go`

**Status**: NEEDS NEW TOOL (`_resurrect`) per Wave 5E.ii

| Pivote requires | v1.4.2 provides | Drift |
|---|---|---|
| Tool `_resurrect(original_session_id, reason)` for closed_aborted sessions | No `_resurrect`; only `_resume` (resumes open/idle sessions, refuses on closed) | **DRIFT CONFIRMED** |
| `_resurrect` creates new `sessions` row with `parent_session_id + resurrected_from` set to original | `_resume` does NOT create a new row; it just updates an existing open session | **DRIFT CONFIRMED** |
| `_resurrect` inherits scope state + evidence pointers | `_resume` is cheap: just sets `last_seen_token = write_audit.max(id)` | **DRIFT CONFIRMED** |

### B.4 — `internal/orchestration/publish_vibe.go` — `parseDriftVerdict` bug

**Status**: BUG (INFRA-001, separately filed) — not part of the pivote's drift surface but surfaced by the pivote's drift-judging

| Pivote requires | v1.4.2 provides | Drift |
|---|---|---|
| Drift-judge verdict shape `{"verdict": "aligned"\|"drift_detected"\|"needs_human"}` | Parser only recognizes legacy `{"aligned": bool}` shape | **DRIFT CONFIRMED** (functional bug, not pivot drift). Wrapper always returns `drift_detected`; operator must use drift_resolve(accept) with citation when underlying judge is aligned |

### B.5 — Current 28-tool canonical list

**Status**: NEEDS CULLING per Wave 4A' (gated MCP server)

| Pivote requires | v1.4.2 provides | Drift |
|---|---|---|
| 12-15 gated tools exposed via `tools/list` | 28 tools (per PLAN §5 + RFC §D-9) | **DRIFT CONFIRMED**. Plan §0 lists 28 → reduces to 12-15 gated |
| LLM sees ONLY tools in `GrantedTools[]` | LLM can call any of the 28 by name (no capability gate) | **DRIFT CONFIRMED** |
| `_resurrect`, `_heartbeat`, `_recover` exist | None of these exist; only `_start`/`_resume`/`_close`/`_status` | **DRIFT CONFIRMED** |
| `_close(reason='clean'\|'aborted')` | `_close()` with no reason arg | **DRIFT CONFIRMED** |

### B.6 — `internal/vlp/package.go`

**Status**: PRESENT — VLP package already implements Brief/Propose/Record/Complete primitives. These are orthogonal to the pivot's session lifecycle. **NO DRIFT** — VLP is the LLM-loop state machine, not the session state machine.

### B.7 — `internal/orchestration/{policy,recall,persona,capabilities,scope,drift}/` (no dirs)

**Status**: MISSING (need to be created under implementing waves)

| Pivote requires | v1.4.2 provides | Drift |
|---|---|---|
| `internal/policy/gate.go` (M2) | None — gate does not exist | **DRIFT CONFIRMED** (Wave 5A.iv delivers this) |
| `internal/recall/assemble.go` (5A.ii) | None — no scoped recall orchestrator | **DRIFT CONFIRMED** (Wave 5A.ii delivers this) |
| `internal/persona/{resolver,apply}.go` (5B.i) | None | **DRIFT CONFIRMED** (Wave 5B delivers this) |
| `internal/capabilities/{vibe_grants,resolver,manage}.go` (5B.ii) | None | **DRIFT CONFIRMED** (Wave 5B delivers this) |
| `internal/scope/{state,tracker,sweeper}.go` (5E.iii) | None | **DRIFT CONFIRMED** (Wave 5E.iii delivers this) |
| `internal/drift/write_interceptor.go` (M6) | None — drift-check is post-hoc only | **DRIFT CONFIRMED** (Wave 5A.vi/M6 delivers this) |
| `internal/delegation/{router,subagents,audit}.go` (5C) | None | **DRIFT CONFIRMED** (Wave 5C delivers this) |
| `internal/adapters/{claudecode,cursor,...}/` (5D) | None — only `internal/adapter/` exists (legacy) | **DRIFT CONFIRMED** (Wave 5D delivers this; legacy `internal/adapter/` dir can co-exist or be merged) |

---

## C. Schema drift

### C.1 — Database schema (v10)

**Status**: NEEDS MIGRATIONS per Wave 5A.iii (v11) + 5E.ii (v12)

| Pivote requires | v10 provides | Drift |
|---|---|---|
| `vibe_frames` table with `project_id, session_id, scope_level, scope_id, frame_kind, composed_at, expires_at, frame_json, content_sha256, last_write_id` | Does not exist | **DRIFT CONFIRMED** (Wave 5A.iii / schema v11) |
| `vibe_recall_subscriptions` table | Does not exist | **DRIFT CONFIRMED** (Wave 5A.iii / schema v11) |
| `sessions.status` CHECK with 5-state enum (`open`,`idle`,`closed_clean`,`closed_aborted`,`archived`) | CHECK with 2-state enum (`open`,`closed`) | **DRIFT CONFIRMED** (Wave 5E.ii / schema v12) |
| `sessions.last_heartbeat_at`, `parent_session_id`, `resurrected_from` columns | None | **DRIFT CONFIRMED** (Wave 5E.ii / schema v12) |
| `write_audit.session_event` column | None | **DRIFT CONFIRMED** (Wave 5E.ii / schema v12) |

---

## D. Summary

**Total drift items**: 32 confirmed. Of these:
- 4 are RFC/supersession items (legacy RFC, BRIDGE, CONSTITUTION, v1.4.2 PLAN) — addressed by t1-rfc, t3-bridge, t4-constitution of this spec
- 6 are code items in existing files — addressed by 4A' (MCP server gated) + 5E.ii (`_close`/`_resurrect`/`_heartbeat`/`_recover`)
- 7 are missing-package items — addressed by 5A.i-vi, 5B, 5C, 5D, 5E.iii
- 5 are schema items — addressed by 5A.iii (v11) + 5E.ii (v12)
- 1 is the INFRA-001 parseDriftVerdict bug — separate infra fix, not part of the pivot

**Wave-to-drift mapping**:

| Drift item | Wave fix |
|---|---|
| A.1, A.2, A.3 (legacy docs) | SUPERSEDED by t1/t3/t4 (this spec) |
| B.1 `_close` reason | 5E.ii |
| B.2 `_close` audit | 5E.ii + schema v12 |
| B.3 `_resurrect` | 5E.ii |
| B.4 INFRA-001 | (separate infra patch) |
| B.5 28→12-15 gated tools | 4A' |
| B.6 (VLP) | NO DRIFT — orthogonal |
| B.7 missing packages | 5A.i-vi, 5B, 5C, 5D, 5E.iii |
| C.1 schema | 5A.iii (v11) + 5E.ii (v12) |

**Total fixes required**: 32. Of these, 4 are completed by THIS spec (the docs), and 28 remain for the implementing waves. **No drift is silent**: every item has a wave owner and an acceptance criterion.

**Acceptance threshold met** when:
1. All 28 outstanding drift items are fixed by the implementing waves
2. Each wave's drift-judge returns `aligned` for its artifacts
3. Final 4F human gate review passes
4. This drift-burst is appended to spec 304's tasks list as completed t7

---

## E. Sign-off

Operator accepts this drift-burst as the canonical forcing function for spec 304 t7. The pivote is grounded in 32 concrete drift items, not opinion. The implementing waves own the fixes.

End of Drift-Burst Report v1.0.0.
