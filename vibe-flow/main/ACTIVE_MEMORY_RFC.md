# Dark Memory MCP — Active Memory RFC

**Version**: 1.0.0
**Status**: Draft → Active
**Supersedes**: `DARK_MEMORY_MCP_RFC.md` (v1.0.0) for sections §P1-P5, §D-3, §D-7, §D-9, §D-10, §6 (process lifecycle), §10 (deferred items — RES-1..3 and FRAME-1..3 absorbed into active scope).
**Source of truth for**: the pivoted thesis — memory decides, harness invokes.
**Authoring session**: dark-memory MCP session `sess-682d28f970d2d2bd`, project `vibe-flow-pivot`, spec `304`.
**Decision record**: this RFC supersedes none of the persistent spec_ids already in dark.db; instead it changes how future specs and tools interpret the data model. The legacy RFC §P1-P5 remains valid for non-active-memory consumers (e.g., read-only consumers that don't gate tool calls).
**Date**: 2026-07-19

---

## 0. Why this RFC exists

Dark Memory MCP took its name from a thesis: **be the persistent memory of the harness**. What we shipped through v1.4.2 is a CRUD MCP with orchestrators — a passive database. The LLM is the conductor; dark-memory answers questions on demand. This inverted the original thesis: memory became what the harness asks, not what tells the harness.

The original design intent, honored in the name but not in the code, was an **active memory**: a system that decides what the harness knows, what tools it may invoke, what scope it operates under, and what it is forbidden from doing — without the harness having to remember to ask.

This RFC is the pivot. It replaces the pull-based, intent-hinted model with a gate-based, frame-injected model. It does not delete the prior CRUD surface; it gates it behind a new policy interceptor that every `tools/call` must traverse. It also hardens the session lifecycle so a harness crash does not destroy the operator's working state.

Three debts are addressed together because they share a root cause (memory acts as a database instead of a brain):

1. **Active governance**: gate-driven, capability-scoped, persona-bound, drift-at-write.
2. **Scoped recall**: global / project / session context as composable frames the LLM never assembles itself.
3. **Session lifecycle resilience**: closed-due-to-crash sessions are resurrectable; only operator-initiated termination is terminal.

## 1. The pivot — one paragraph

Memory decides; the harness invokes. Every `tools/call` from the LLM passes through a **policy gate** in dark-memory before reaching an underlying orchestrator. The gate (a) composes an **atomic context frame** from session + project + global state, (b) verifies the call's intent is in scope and the LLM has the **capability grant** for it, (c) invokes the orchestrator with the frame as input, and (d) **drift-checks the response** at the write boundary before returning to the LLM. The LLM never sees tools it isn't granted, never sees scope outside its session, never sees ungrounded facts, and never sees a response that drifted the spec. The **persona** is constitution-applied at every generation. **Closed sessions** are resurrectable unless the operator explicitly closed them as clean — making harness crashes non-destructive.

## 2. Design principles — A1 through A7

These replace (and augment) the legacy P1-P5 of `DARK_MEMORY_MCP_RFC.md`. Where they conflict, A1-A7 win for the pivoted unit; legacy P1-P5 win for read-only consumers.

### A1 — Memory decides, harness invokes

Inversion of control. Every `tools/call` from the LLM is intercepted by the dark-memory policy gate before reaching an orchestrator. The harness **never** calls a dark-agents tool without the gate deciding the call is admissible. The gate can refuse, can redirect to a different tool, can require a follow-up call (e.g., reload policy), or can admit and forward.

**What changes from legacy P1**: P1 said "intent before CRUD" but the LLM still authored the intent and the call chain. A1 says the gate decides admissibility independently of the LLM's authoring. The LLM's intent is one input among several (frame, grants, persona, constitution).

**What this looks like in code**: `internal/policy/gate.go` is invoked from every MCP tool wrapper. The gate is the only path to the orchestrator layer. There is no bypass.

### A2 — Atomic context, not chunks of rows

Every context injection is a **frame**: `{intent, scope, bindings, grants, drift-state, evidence-pointers, persona}`. Frames are composed server-side; the LLM never assembles context across multiple tool calls.

**Why "atomic"**: the LLM cannot compose reliable context. It also should not need to. A frame is the irreducible unit of "what the LLM needs to know about THIS call". Frames subsume by scope level — the project frame inherits the global frame; the session frame inherits the project frame; the call frame inherits the session frame. A request for one frame at any level includes references to all higher-scope frames (or embeds them if the harness prefers).

**Concrete shapes** (declared in `internal/atomic/types.go`, implemented per frame file):

| Frame | Source | Stored as |
|---|---|---|
| IdentityFrame | session + operator | `Actor`, `Operator`, `SessionID`, `ConstitutionID+Ver`, `CanaryActive`, `ResolvedAt` |
| ScopeFrame | session.scope_state | `OpenSpec`, `OpenTasks`, `EvidencePointers[]`, `LastDriftVerdict` |
| EvidenceFrame | last `write_audit` rows + linked research items | `RecentWrites[]`, `LinkedResearch[]`, `LastSeenToken` |
| CapabilitiesFrame | constitution + mod grants for this session | `GrantedTools[]`, `GrantedScopes[]`, `GrantedExpiresAt` |
| DriftFrame | last drift report per open spec | `SpecID`, `LastVerdict`, `LastReconciledAt`, `PendingItems[]` |
| PersonaFrame | constitution + brand resolved | `Voice`, `ClaimsPolicy`, `RefusalPattern`, `Tone` |

These six are the v1 enumeration. New frame kinds are added in minor versions.

### A3 — Policy gate at the tool boundary

A three-phase gate sits between the MCP `tools/call` and the orchestrator:

```
   tools/call [intent]
        │
        ▼
   PRE-HOOK (gate)
   ┌──────────────────────────────────────────────────┐
   │  1. resolve IdentityFrame  (must be present)     │
   │  2. resolve CapabilitiesFrame                     │
   │  3. is intent in granted tools?  ─── no ──► Err  │
   │  4. resolve ScopeFrame                             │
   │  5. is intent in scope?  ─────────── no ──► Err  │
   │  6. compose EvidenceFrame + DriftFrame + Persona  │
   │  7. pass orchestrator(in, frame)                  │
   └──────────────────────────────────────────────────┘
        │
        ▼
   orchestrator (now frame-aware)
        │
        ▼
   POST-HOOK (gate)
   ┌──────────────────────────────────────────────────┐
   │  1. did the orchestrator mutate state?           │
   │     yes ──► drift-check the result vs spec       │
   │         drift ────► ErrDriftAtWrite (refuse)      │
   │  2. emit write_audit (INV-1)                      │
   │  3. cache composed frame (vibe_frames table)     │
   │  4. update recall subscription last_seen_token   │
   │  5. return response + frame envelope              │
   └──────────────────────────────────────────────────┘
```

**Refusal typed errors** (added to legacy refusal set, RFC §P5 refusal shape, and constitution v1):

| Error | When | Hint |
|---|---|---|
| `ErrScopeRequired` | intent needs scope on project X, session has none | "Call `dark_memory_recall(scope='project', project_id='X')` first" |
| `ErrCapabilityNotGranted` | tool not in `GrantedTools[]` | "Tool `{name}` is not granted to this session" |
| `ErrPersonaNotResolvable` | no constitution or brand binding for the intent | "Bind a constitution or resolve persona first" |
| `ErrFrameStaleTooFar` | composed frame is older than `MAX_FRAME_AGE` | "Call `dark_memory_recall(scope='session')` to refresh" |
| `ErrSessionNotResurrectable` | `close(reason='clean')` was called; resurrection refused | "Start a new session; this one is terminal" |
| `ErrPolicyGatewayDown` | the gateway itself is unreachable (only in harness glue) | "Dark Memory gateway is unreachable; degraded mode" |

### A4 — Capability grants, not tool enumeration

The LLM never sees a tool it doesn't have scope for. `dark_memory_active_policy` returns the granted-set, not the global catalog. The 28-tool surface of legacy §D-9 is a developer reference; **the LLM only sees whatever the gate grants to its session**.

**Mechanism**: `vibe_grants(grant_id, session_id, scope, capability, resource, granted_at, expires_at, source)` table, populated at session start from constitution + active mods + operator-set `DARK_GRANTS` env. Grants are mutable via `dark_memory_grant_manage` (operational tool, not exposed to the LLM).

**What changes from legacy**: in legacy §P1 we offered "intent-driven" tools but the LLM could still name any of the 28. Under A4, if the LLM hallucinates a tool name (very common anti-pattern), the gate refuses with `ErrCapabilityNotGranted` instead of returning a soft-200 "tool not found" that the LLM might treat as a signal.

### A5 — Persona is constitution-applied

Vocal style, claims policy, refusal-pattern, and tone are not chosen by the LLM at generation time. They are resolved from the active constitution + brand at the session level and applied to every frame that crosses the gate.

**Mechanism**: `internal/persona/` package. `ResolvePersona(constitutionID, brandID) → PersonaFrame`. The persona is applied at the **frame composition** step (pre-hook, step 6) and at the **response shaping** step (post-hook, step 5). The LLM never sees raw orchestrator output — it sees the persona-shaped response.

**Why this matters**: the LLM tends to drift voice under context pressure. Persona-as-applied injects the voice invariant on every turn. The drift_judge at write-time checks that the response conforms to the persona claims policy.

### A6 — Drift at write, not at review

Every `SaveArtifact` runs through the same drift-check that `dark_memory_vibe_publish` uses today — but **synchronously at write time**. Artifacts that drift the active spec are refused with `ErrDriftAtWrite` before transaction commit.

**What changes from legacy §D-7**: legacy `dark_memory_vibe_publish` runs drift_judge AFTER save (so a drifted artifact exists briefly in the DB before being marked `validation_status='failed'`). Under A6, the drift-check happens INSIDE the transaction; on drift the transaction rolls back and no artifact row is created.

**Mechanism**: `internal/drift/write_interceptor.go` — pre-commit hook on `Store.SaveArtifact`. Uses the same judge client as the meta-orchestrator's drift step, but invoked BEFORE the DB commit, in the same transaction. Can flag (don't save, return error) or warn (save with `validation_status='pending'`) depending on configured strictness per project.

### A7 — Scope is first-class state

The session has a **scope**: the currently-open spec, the in-flight tasks, the granted capability set, the linked evidence, the most recent drift verdict. The scope is a `ScopeState` struct persisted to `vibe_frames` and rendered into every response envelope.

**Mechanism**: `internal/scope/` package. `ScopeState{SessionID, OpenSpecID, OpenTasks[], EvidencePointers[], GrantedTools[], LastDriftVerdict, ResolvedAt}`. Updated atomically on every state change (spec_create, task_complete, write_audit, drift_log).

**Out-of-scope action**: a `tools/call` whose intent requires a scope the session does not have (e.g., trying to publish under a spec the session never opened) is refused with `ErrScopeRequired`.

## 3. Mechanisms — M1 through M8

Eight concrete mechanisms implement A1-A7. Each lives in a separate Go package so they can be implemented incrementally across waves.

### M1 — `internal/atomic/` — Frame types and composition

- `types.go`: `FrameKind` enum (`Identity`, `Scope`, `Evidence`, `Capabilities`, `Drift`, `Persona`); `FrameEnvelope` struct with `Kind, ComposedAt, ExpiresAt, SessionID, ScopeLevel, ScopeID, FrameJSON, ContentSHA256, LastWriteID`; `Frame` interface (`Kind()`, `ComposedAt()`, `Validate()`, `Hash()`, `Render()`).
- Per-frame files: `identity_frame.go`, `scope_frame.go`, `evidence_frame.go`, `capabilities_frame.go`, `drift_frame.go`, `persona_frame.go`.
- `assemble.go`: server-side composition. `ComposeFrame(kind, scopeCtx) → Frame`.
- `cache.go`: cache by `(session_id, scope_level, scope_id, frame_kind)`. TTL configurable per kind (default: identity=15min, scope=30s, evidence=10s, capabilities=15min, drift=30s, persona=15min). Cache invalidation on any `write_audit` row in the scope (INV-5 hash re-check).

### M2 — `internal/policy/gate.go` — Tool-boundary interceptor

- Pre-hook: composes frames, checks grants, checks scope, decides admissibility.
- Mid-hook: optional re-check after partial orchestrator work (rare; for long-running tools).
- Post-hook: drift-checks response, emits audit, caches frame, updates subscription.
- Configurable per project: `policy_mode = strict | warn | permissive` (strict = refuse on drift; warn = save with `pending`; permissive = save always, drift only at later `vibe_publish`).
- Phase ordering is non-negotiable: identity → capabilities → scope → evidence → drift → persona → forward.

### M3 — `internal/persona/` — Persona resolution

- `resolver.go`: `ResolvePersona(constitutionID, brandID) → PersonaFrame`. Reads constitution's `[tone]` + brand's `[voice_claims]` + jurisdiction's `[refusal_pattern]`.
- `apply.go`: applies persona to a response before returning to the LLM. Subtle but critical — the LLM receives persona-shaped prose, not raw orchestrator output.
- `claims_policy.json` (per brand): controls what the LLM may claim about the project, the user, and the work product. Drift_judge enforces it.

### M4 — `internal/capabilities/` — Grant storage and resolution

- `vibe_grants(grant_id, session_id, scope, capability, resource, granted_at, expires_at, source)` table.
- `resolver.go`: `ResolveCapabilities(sessionID) → CapabilitiesFrame`. Joins constitution grants + mod grants + operator env grants.
- `manage.go`: operator-side CRUD (NOT exposed to LLM); per project, per session.

### M5 — `internal/scope/` — Scope state machine

- `state.go`: `ScopeState` struct with state transitions (open, extend, refresh, expire, close).
- `tracker.go`: hooks into `write_audit` to update scope state on every relevant event.
- `render.go`: serializes scope into the gate response envelope.

### M6 — `internal/drift/write_interceptor.go` — Drift at write

- `interceptor.go`: `InterceptSaveArtifact(ctx, artifact) → DriftVerdict`. Runs drift_judge synchronously, IN the same transaction as `SaveArtifact`.
- `strictness.go`: per-project policy (`strict` mode rejects; `warn` mode flags).
- Re-uses legacy `dark_ssd_drift_judge`/now `dark_memory_judge(eval_type=drift_judge)` (post-consolidation in v1.4.0).

### M7 — `internal/delegation/` — Delegation router

- `router.go`: decides whether an intent is handled by the LLM itself, by a sub-agent, or refused. Inputs: IdentityFrame, CapabilitiesFrame, ScopeFrame. Outputs: `DelegationDecision{Handler, Reasoning}`.
- `subagents.go`: registry of known sub-agents (compiled in for v1; dynamic registration deferred to v1.1).
- `audit.go`: every delegation is logged to `vibe_delegations` (planned in v1.1).

### M8 — `internal/adapters/{claudecode,cursor,vscode,mcjam}/` — Harness glue

- Each adapter implements the harness's startup-`recover`, periodic-`heartbeat`, and exit-`close_clean` lifecycle.
- Adapters do not contain any policy logic; they only translate harness hooks into MCP calls.
- Adapters register as standard dark-agents MCP peers; they don't extend the protocol.

## 4. Session lifecycle — five states, four new transitions

The legacy model has two states: `open` and `closed`. The pivoted model has five:

| State | Meaning | Source transition |
|---|---|---|
| `open` | Receiving tool calls; `last_heartbeat_at` is recent | `idle` → `open` (resume) |
| `idle` | Was `open`, `last_heartbeat_at` is stale (no write in `IDLE_TIMEOUT`); resurrectable | `open` → `idle` (timeout) |
| `closed_clean` | Operator called `dark_memory_session_close(reason='clean')`; **NOT** resurrectable | `open`/`idle` → `closed_clean` |
| `closed_aborted` | Process exited without explicit close; harness died; resurrection allowed | `open`/`idle` → `closed_aborted` (timeout, SIGKILL, panic) |
| `archived` | Operator explicitly archived (vetting, retention policy hit); NOT resurrectable | any → `archived` |

### 4.1 State machine

```
            ┌──────────────────────────────────────────────┐
            │                                              │
            ▼                                              │
   ┌────────────────┐  idle_timeout  ┌────────────────┐    │
   │     open       │ ─────────────► │     idle       │    │
   │ (heartbeat ok) │ ◄───────────── │ (no writes)    │    │
   └────┬───────────┘   resume/grace  └────────┬───────┘    │
        │                                       │            │
        │ _close(reason=clean)                  │ timeout    │
        │ OR _close(reason=archived)            │ to aborted │
        ▼                                       ▼            │
   ┌────────────────────┐             ┌────────────────────┐ │
   │   closed_clean     │             │  closed_aborted    │◄┘
   │ (terminal, NOT     │             │ (resurrectable)    │
   │  resurrectable)    │             │                    │
   └────────────────────┘             └─────────┬──────────┘
                                                  │
                                                  │ _resurrect()
                                                  ▼
                                          ┌────────────────┐
                                          │     open       │  (new session_id,
                                          │ (new session)  │   parent_session_id
                                          └────────────────┘   set to original)
```

### 4.2 New operations

| Operation | State precondition | Effect |
|---|---|---|
| `dark_memory_session_close(session_id, reason)` | `open`/`idle` | `reason='clean'` → `closed_clean` (terminal); `reason='aborted'` → `closed_aborted` (resurrectable); default `clean` |
| `dark_memory_session_resume(session_id)` | `open`/`idle` | Barato. Re-attach: `last_seen_token = write_audit.max(id)` |
| `dark_memory_session_resurrect(original_session_id, reason)` | `closed_aborted` (or `idle`) | New session row + `parent_session_id` + `resurrected_from`; inherits scope state + evidence pointers; re-derives grants from constitution + mods |
| `dark_memory_session_heartbeat(session_id)` | `open`/`idle` | `UPDATE sessions SET last_heartbeat_at = now()` |
| `dark_memory_session_recover(actor, lookback='24h')` | always | Read-only: finds the most-recent `closed_aborted` for this `actor` within lookback; returns `{recovered_from, requires_consent, frame_preview}`; **does not create a session** — caller decides whether to call `_resurrect` |

### 4.3 Heartbeat protocol

The harness calls `dark_memory_session_heartbeat(session_id)` every ~30s. The server updates `last_heartbeat_at`. If `now - last_heartbeat_at > HEARTBEAT_TIMEOUT` (env-configurable, default 300s = 5min):

- A **sweeper goroutine** in `internal/scope/sweeper.go` runs every 60s and promotes stale `open` sessions to `closed_aborted`.
- On server **boot**, an `boot_reconcile()` step does the same sweep after Store.Open(), so sessions abandoned by a previous server process are recovered at next start.

**Harnesses without heartbeat hooks** (some web clients) live in "no-heartbeat" mode: their session is treated as `idle` after the first heartbeat miss and promoted to `closed_aborted` after `HEARTBEAT_TIMEOUT`. They're still resurrectable.

## 5. New invariables — INV-8 Resilience, INV-9 Heartbeat

The existing INV-1..7 (write audit, per-session scoping, canary, constitution audit, cache integrity, mod sanitize, project namespace) are unchanged. Two new ones:

### INV-8 — Resilience

> A session is terminal-non-resurrectable **only** if `_close` was called with `reason='clean'` or `reason='archived'`. All other close paths (`closed_aborted`, `idle` timeout, missing heartbeat) leave the session resurrectable.

**Enforcement**: `_close` is the only path to `closed_clean`/`archived`; the only path to `closed_aborted` is the sweeper (sweeper cannot write `closed_clean`); the boot reconciliation cannot mark `closed_clean`. The CHECK constraint on `sessions.status` and the sweep logic jointly enforce this.

**Why this matters**: legacy INV-2 + legacy `close` lifecycle together could destroy operator work. INV-8 makes harness crashes non-destructive without operator intervention.

### INV-9 — Heartbeat

> A session with `status='open'` whose `last_heartbeat_at` is more than `HEARTBEAT_TIMEOUT` seconds in the past is promoted to `status='closed_aborted'` by either the sweeper goroutine or the boot reconciliation step. The promotion emits a `write_audit` row with `session_event='close_aborted'`.

**Enforcement**: sweeper runs every 60s; boot reconciliation runs once per `Store.Open()`. The promotion is idempotent (re-running on a `closed_aborted` is a noop). The audit trail always shows which path promoted the session.

**Why this matters**: the system survives harness crashes without data loss AND without ghost sessions (an `open` session that the operator abandoned 6 months ago is still resurrectable but is clearly flagged as `closed_aborted` rather than masked as `open`).

## 6. Wave plan — re-prioritized for the pivot

This RFC supersedes `PLAN.md` v1.4.2's wave ordering. The new ordering carries the same identifiers (5+, 4A-4F) but their priority is re-assigned because the gateway work (5A) is a precondition for everything downstream.

### 6.1 New wave order

| Priority | Wave | Sub-specs | Status |
|---|---|---|---|
| **P0 NOW** | **5A — Atomic Context** | 5A.i frames; 5A.ii scoped recall; 5A.iii schema v11; 5A.iv gate integration | Draft → design now, implementation in 5A.i first |
| P1 | **5B — Persona + Capabilities** | 5B.i persona resolver; 5B.ii capabilities model; 5B.iii gate integration of persona + grants | After 5A.i |
| P1 | **5C — Delegation** | 5C.i delegation state machine; 5C.ii delegate_intent orchestrator; 5C.iii sub-agent registry | After 5A.iv |
| P1 | **5D — Harness adapters** | 5D.i opencode glue updates; 5D.ii Claude Code adapter; 5D.iii Cursor adapter | After 5B (which gates what the adapters can do) |
| P1 | **5E — Session Lifecycle Resilience** | 5E.i schema v12; 5E.ii `_close`/`_resurrect`/`_heartbeat`/`_recover`; 5E.iii sweeper + boot reconciliation; 5E.iv frame-aware resurrection; 5E.v L6 adapter integration | 5E.i-.iii after 5A.iii; 5E.iv after 5A.ii; 5E.v after 5E.ii + 5D |
| P2 | **4B-prime — CLI admin** | Driver switch, schema-status, vacuum | Can run parallel to 5x as operational tool |
| P3 | **4A-prime — MCP server (gated)** | 12-15 gated tools (NOT 28 passive); gate interceptor at every tool wrapper | After 5A.iv + 5B.iii (gate must exist) |
| P3 | **4D-prime — Bridge conformance cx.v3** | `policy_gateway: true` declaration; dark-research-mcp demoted to tool-backing; cx.v3 contract ratified | After 5A.iv + dark-research-mcp update |
| P4 | **4C — Runbooks** | RUNBOOK.md, COEXISTENCE.md, INVARIANTS.md, CONTEXT_OBJECTS.md, PERFORMANCE.md, MIGRATION.md (now reflects active memory + 9 invariants) | Last docs update |
| P5 | **4F — Human gate** | Drift-judge every sub-spec; resolve all drift; tag ready-for-publish | Final |

### 6.2 What is cancelled, what is absorbed

- **`dark-recall plugin v2.3` — CANCELLED.** It was opencode-specific glue for cross-MCP prefill. Under A2, dark-memory itself does prefill natively (via the gate + frames). The harness does not need an opencode-specific plugin to get the same behavior. The plugin's capabilities are absorbed into 5A (frames) and 5D (adapters).
- **`dark_research_spec_create` deprecation — ACCELERATED.** dark-research-mcp's `dark_mem_*` namespace is the legacy shape. Under cx.v3, dark-research-mcp becomes a **tool backing** (a dark-agents peer that lives behind the dark-memory gateway). Its own `dark_research_*` surface is still accessible to harnesses, but `dark_mem_*` is a frozen shim. Migration timeline: existing users keep their shim behavior; new installs are prompted to use `dark_research_*` directly through the gateway.
- **`dark_recall_research` — REPLACED.** Under 5A.ii, contextual recall is `dark_memory_recall(scope, since_token)`. The legacy `dark_research_recall` (BM25/LIKE) is preserved for raw search but is no longer the canonical "what do I know?" tool.

### 6.3 Sub-spec ID assignments (sub-spec 11..17)

| ID | Wave | Sub-spec |
|---|---|---|
| 11 | 5A.i | Atomic frame types |
| 12 | 5A.ii | Scoped recall orchestrators |
| 13 | 5A.iii | Schema v11 (Frames + Subscriptions) |
| 14 | 5A.iv | `policy/gate.go` integration |
| 15 | 5B | Persona + Capabilities (joint sub-spec because the gate needs both to function) |
| 16 | 5C | Delegation |
| 17 | 5D | Adapters |
| (later) | 5E.i-5E.v | Session lifecycle — sub-spec IDs reserved as 18-22 |

## 7. Coexistence evolution — cx.v3 with policy_gateway

The legacy coexistence contract (`BRIDGE_AND_COEXISTENCE.md` v1) defines cx.v1 and cx.v2 as shared-DB + namespace conventions. This RFC introduces **cx.v3 — policy gateway**, where dark-memory becomes the single policy authority over all dark-agents tools.

### 7.1 New `serverInfo` field

```json
{
  "serverInfo": {
    "name": "dark-memory-mcp",
    "version": "1.5.0",          // first version advertising cx.v3
    "vendor": "dark-agents",
    "coexistence_group": "dark-agents/memory",
    "policy_gateway": true       // NEW — declares dark-memory as the policy gateway
  }
}
```

### 7.2 Demotion of dark-research-mcp

Under cx.v3, dark-research-mcp's role changes from "sibling surface" to "tool backing". Concretely:

- It still exposes `dark_research_*` (the 13 OSINT intents + multi + router).
- Its `tools/call` responses pass through the **dark-memory policy gate** if `policy_gateway=true` is active in the harness.
- The harness may call `dark_research_*` directly (fine), but if it does, the responses are not persona-shaped and not drift-checked. The harness sees the raw orchestrator shape.

### 7.3 Harness-side failure mode

When `policy_gateway=true` but dark-memory-mcp is unreachable:

- The harness surfaces a graceful degraded mode: dark-agents tools are NOT callable (gate is required).
- The harness can still call non-dark-agents tools.
- A toast/notice is shown once per session: "Dark Memory gateway is unreachable; advanced memory features disabled".

This is the "no silently-wrong behavior" contract from the constitution. Better to fail loudly than to silently bypass the gate.

## 8. Acceptance criteria per wave

### 5A — Atomic Context
- Given any `tools/call`, the gate composes the 6 frames for the session and attaches them to the response envelope.
- Drift at write returns `ErrDriftAtWrite` (transient — no DB row).
- `vibe_frames` table has rows for every composed frame; cache hit ratio > 80% after warmup in standard use.

### 5B — Persona + Capabilities
- LLM cannot name a tool outside `GrantedTools[]` without `ErrCapabilityNotGranted`.
- Generated responses match `PersonaFrame.tone` + `PersonaFrame.claims_policy`; drift_judge verifies per artifact.

### 5C — Delegation
- Intent A can be delegated to sub-agent X; LLM-B cannot invoke sub-agent X without grant.
- Every delegation emits `vibe_delegations` row (planned table; deferred to v1.1 if needed for speed).

### 5D — Adapters
- opencode adapter: starts → `_recover`; periodic → `_heartbeat`; exit → `_close_clean`.
- Claude Code adapter: same three hooks via MCP `notifications/tools/list_changed` triggers.
- Cursor adapter: same via Cursor Rules.

### 5E — Session Lifecycle Resilience
- After harness SIGKILL (simulated), new harness invocation calls `_recover` and gets `{recovered_from: ..., requires_consent: true, frame_preview: ...}`. Operator consents → `_resurrect` → work continues.
- Sweeper promotes stale `open` to `closed_aborted` within 60s of `HEARTBEAT_TIMEOUT`.
- `_close(reason='clean')` is terminal: subsequent `_resurrect` returns `ErrSessionNotResurrectable` with reasoning.
- Closed_due_to_accident session resurrects the **same project_id** and **same operator** with the same grants (re-derived).

### 4A-prime — MCP server (gated)
- Every `tools/call` MCP tool traverses the gate. Tool registry exposes only the 12-15 gated tools. The full 28-tool legacy surface is preserved at the Go library API (compiled in) but the MCP surface is gated.
- 1000-call mixed e2e stress test passes without deadlock and without a single bypassed audit row.

### 4D-prime — Bridge conformance cx.v3
- `dark-memory-mcp initialize` returns `coexistence_group=dark-agents/memory, policy_gateway=true`.
- `dark-research-mcp initialize` returns `coexistence_group=dark-agents/research` (NEW — was previously omitted; the legacy versions assumed a shared "dark-agents/memory" group which is now correct only for dark-memory).
- MCP Inspector run against dark-memory-mcp shows exactly 12-15 gated tools in `tools/list`.

### 4F — Human gate
- Every sub-spec 11..17 has at least one `aligned` drift verdict.
- All `drift_detected` rows are reconciled (operator resolved or code fix).
- This RFC's sections are stable against drift for ≥ 7 days.

## 9. Supersession map (legacy section → where it lives now)

| Legacy section | Status | New home |
|---|---|---|
| §P1 Intent before CRUD | Refined | A1 (with inversion of control) |
| §P2 Context object, not row dump | Refined | A2 (atomic frames, not chunks-of-rows) |
| §P3 Sequence-aware response | Kept | Returned as the `next` field on every response envelope (post-hook, step 5) |
| §P4 Defense as contract | Kept | INV-1..7 unchanged; INV-8 + INV-9 added |
| §P5 Economy by default | Refined | Economy applies to frame composition (M1 `cache.go`); research recall stays as-is |
| §D-1 sibling Go module | Kept | dark-research-mcp demoted to **tool-backing** under cx.v3 |
| §D-2 dual-driver storage | Kept | Unchanged |
| §D-3 schema migrations | Refined | v11 (additive) + v12 (sessions rewrite) pending |
| §D-4 six invariants | Refined | INV-1..7 unchanged; INV-8 + INV-9 added (§5) |
| §D-5 context objects | Refined | Replaced by M1 atomic frames; legacy `ArtifactContext/SessionContext/PolicyContext` retained as read-only views for backward compat |
| §D-6 orchestrators as functions | Kept | Orchestrators are now frame-aware (M2 pre/post hooks); legacy orchestrators continue to work but emit frames when invoked through the gate |
| §D-7 sequence-aware responses | Kept | Now part of the gate's post-hook response envelope |
| §D-8 economy pipeline | Kept | Now invoked at frame composition (M1), not at retrieval |
| §D-9 ~25 tools | Refined | Gated → 12-15 tools; legacy 28 retained at library API |
| §D-10 dark-recall v2.3 | **Cancelled** | Absorbed into 5A |
| §6 process lifecycle | Refined | Session lifecycle upgraded to five states (§4) |
| §7 security model | Kept | INV-1..9 (§5) |
| §8 coexistence | Refined | cx.v3 with `policy_gateway` (§7); legacy §8 retained as the read-only-consumer view |
| §9 migration/deprecation | Refined | dark-recall v2.3 cancelled (§6.2); remaining deprecations unchanged |
| §10 deferred | Resolved | Vector recall, write audit partitioning, multi-tenant isolation, armed-mode — deferred items remain deferred for v1.1; not addressed here |

## 10. Out of scope (explicit)

- LLM inference (delegated to harness).
- Vector search (deferred to v1.1 per legacy §10).
- Cross-driver replication (legacy §10 deferred).
- Multi-tenant isolation beyond session_id (legacy §10 deferred; the resurrection chain makes session-scoped isolation more reliable without addressing tenant privacy).
- dark-recall plugin v2.3+ (cancelled; superseded by 5A prefill native to the gateway).
- Custom glue between dark-agents MCPs (the cx.v3 contract makes this unnecessary; MCP itself is the bridge).

## 11. Decision record

This RFC supersedes nothing in dark.db. All persisted spec_ids remain valid; the drift in their interpretation is resolved by re-publishing each spec's artifacts under the new frame model. Where legacy specs (e.g., 173, 177, 178) reference §D-9 tool counts or §P1-P5 model, those references are noted in the drift-burst report (sub-spec `t7-drift-burst` of spec 304).

Future specs that follow this RFC will reference `dark-agents/dark-memory-mcp-cerebro@1.0.0` (the constitution paired with this RFC) and adopt the frame model natively.

End of Active Memory RFC.
