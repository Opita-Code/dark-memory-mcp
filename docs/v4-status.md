# v4 Status — current state of the redesign

> **Audience**: anyone touching the `feat/v4-redesign` branch.
> **TL;DR**: v4-alpha.1 ships **25 of 57 canonical tools** (44% of
> the surface). The package layout is **NOT** what `ARCHITECTURE-V4.md
> §5 (original)` promised — see "actual layout" below. The operator-
> facing surface is real and tested (37 tests PASS, 0 FAIL); the
> remaining 32 tools land in BUG-9+.

| Field | Value |
|---|---|
| Branch | `feat/v4-redesign` (from `v2.20.0`, NOT from `v3.0-void`) |
| Last reviewed | 2026-09-27 |
| Status | **alpha.1** — pre-release, local-only, contributors only |
| Version constant | `v4alpha.1-dev` (resolved via `ldflags` → `debug.ReadBuildInfo` → `"dev"`) |
| Schema version | `v4alpha/2026-09-27/001` (stamped in `schema_migrations`) |
| Binary | `dark-memory-v4` (10.3 MB Windows) |
| Local-only policy | YES — no `git push`/`fetch`/`pull`, no remote tags/releases |

---

## 1. Tools inventory (25 of 57)

The canonical surface is 57 tools (see `ARCHITECTURE-V4.md §6.3
tool-count target`). v4-alpha.1 registers **25 of those**.

### ✅ Registered (25)

| Namespace | Tools | Count | BUG |
|---|---|---|---|
| **Health** | `health_ping` | 1 | 7 |
| **Session** | `start`, `close`, `status`, `resume`, `heartbeat` | 5 | 7 + 8 |
| **Agent memory** | `save`, `recall`, `list`, `get`, `update`, `archive` | 6 | 7 + 8 |
| **Observability** | `memory_state`, `writes`, `anomalies` | 3 | 8 |
| **Error obs** | `summary`, `list`, `get`, `resolve` | 4 | 8 |
| **Policy** | `active_policy`, `load_constitution` | 2 | 8 |
| **Vibe** | `spec`, `publish`, `pipeline_status`, `resolve_drift` | 4 | 8 |

### ⏳ Deferred to BUG-9+ (32)

| Namespace | Tools | Count | Defer reason |
|---|---|---|---|
| **Research** | `topic`, `recall`, `resume_thread` | 3 | Needs backend stub (will be a no-op that returns "no backends registered") |
| **Judge** | `judge`, `consensus`, `judgment_history` + 6 `judge_util_*` | 9 | Needs LLM provider key + HTTP client to `DARK_JUDGE_PROVIDER` |
| **Mindset** | `mindset_apply` | 1 | Needs procedural composition + judge-validation cache |
| **Delegation** | `delegate_intent` | 1 | Needs DECIDE→PLAN→MIND→CURATE pipeline |
| **Project** | `create`, `lookup` | 2 | Needs `projects` table + idempotent-on-project_id |
| **Context** | `artifact_context`, `spec_context`, `session_context`, `recall` | 4 | Reads from vibe pipeline tables (already exist) |
| **Agent bootstrap** | `bootstrap`, `recommend_companions`, `detect_environment` | 3 | Needs embedded `dark-memory://docs/*` resources |
| **L6-VLP** | `vlp_handle_event` | 1 | Needs state machine FSM |
| **Red-team** | `list_mods`, `get_prompts`, `log_attempt` | 3 | Env-gated (`DARK_REDTEAM=armed`); needs mod loader |
| **Admin** | `vacuum` (migrate + schema_status are in cmd/) | 1 | SQL `VACUUM` + retention policy |
| **Agent memory c2** | `delegate`, `entities`, `subagent_register`, `subagent_unregister` | 4 | Needs C2 subagent binding + Mem0 entity extraction |
| **Session c2** | `recover`, `resurrect` | 2 | INV-8 contract + closed_aborted discovery |

---

## 2. Actual package layout (`internal/v4alpha/`)

`ARCHITECTURE-V4.md §5 (original)` describes the aspirational layout:
`core/`, `vibe/`, `security/`, `adapters/`, `governance/`,
`constitutions/`, `transport/`, `tools/`, `installer/`,
`extensions/`.

**The actual layout** (2026-09-27):

```
internal/v4alpha/
├── agent_memory/    # Save, Get, List, Recall, Archive, Update + FTS5
├── audit/           # write_audit schema + Writer + ListFilters
├── judge/           # Judge interface + NoOpJudge (LLM judge in BUG-9)
├── manifest/        # cap_store + cap_token + manifest (RBAC layer)
├── research/        # (placeholder — no tools yet, BUG-9)
├── session/         # Store (open/read/heartbeat/close) + property tests
├── store/           # OpenSQLite, WithTx, store_test (INV-16)
├── transport/
│   └── mcp/         # mcp-go wrapper: server.go + 8 tool files
└── vibe/            # Spec/Artifact/Drift stores + Pipeline
```

### What is NOT yet built (deferred)

| Aspirational | Status | When |
|---|---|---|
| `core/` (pure types) | Inline in `vibe/` and `agent_memory/` for now | alpha.2 — extract when 3rd package needs the same type |
| `security/` (redact, ssrf, pii, injection, capability) | NOT STARTED | alpha.3 — INV-11..INV-15 land as `internal/v4alpha/security/` |
| `adapters/` (LLM, credentials, embedding, update) | NOT STARTED | alpha.3 — when first LLM client lands |
| `governance/` | Inline in `vibe/` + `judge/` | alpha.2 |
| `constitutions/` | Hardcoded in `transport/mcp/policy.go` | BUG-9 — `policy_registry` table |
| `tools/` (manifest-based auto-discovery) | `transport/mcp/*` files | M3 aspirational; deferred past alpha.2 |
| `installer/` | Lives in `npm/wrapper/` (legacy v2) | Carry over; not v4-specific |
| `extensions/` | NOT STARTED | alpha.3+ — community pack loader |

---

## 3. Invariants — adoption status

The original `docs/INVARIANTS.md` defines INV-1..INV-10 (v3, still
in force). v4 adds INV-16 (dark-db concurrency) and INV-17 (FTS5
ordering).

| Invariant | Adopted in v4? | Where | When |
|---|---|---|---|
| INV-1 (write-path audit) | YES (partially) | `agent_memory.Save` audit pending; `error_resolve` audit on | BUG-9 closes the Save audit gap |
| INV-2 (per-session scoping) | YES | `agent_memory.Recall` filters by `operator`; cross-session by default | inherited |
| INV-3 (canary check) | DEFERRED | not in `internal/v4alpha/` | alpha.2 — when first research adapter lands |
| INV-4 (constitution SHA) | YES (light) | `transport/mcp/policy.go::loadConstitution` reads + returns hash | full watchdog (refuse-migrate-on-drift) deferred |
| INV-5 (cache re-hash on Get) | DEFERRED | not in v4-alpha.1 | alpha.2 — when LLM cache adapter lands |
| INV-6 (mod sanitization) | DEFERRED | mod loader is in `internal/tools/` (v3), not yet ported | alpha.3 |
| INV-7 (per-project scoping) | YES | every `Save`/`List`/`Get`/`Archive` filters by `operator`; the legacy `projects` table is replaced by `operator` as the tenant primitive in v4 | see ARCHITECTURE-V4.md §6.4 (planned) |
| INV-8 (per-MCP DB isolation) | YES | `cmd/dark-memory-v4` defaults to operator-configurable DSN; `defaultDSN` defaults to `dark-memory.db` (not `dark.db`) | inherited |
| INV-9 (reserved) | YES (reserved) | — | — |
| INV-10 (agent_memory lifecycle) | YES | rows survive session close; no auto-bind | inherited |
| **INV-11** (capability token) | NOT STARTED | alpha.3 | alpha.3 — when transport auth lands |
| **INV-12** (audit chain) | NOT STARTED | alpha.3 | alpha.3 — when `security/audit/` lands |
| **INV-13** (redact-before-log) | NOT STARTED | alpha.3 | alpha.3 — when first OSINT adapter lands |
| **INV-14** (SSRF guard) | NOT STARTED | alpha.3 | alpha.3 — when first URL-fetching tool lands |
| **INV-15** (prompt injection scan) | NOT STARTED | alpha.3 | alpha.3 — when first URL-fetching tool lands |
| **INV-16** (dark-db concurrency) | **YES** | `store.OpenSQLite` + `store.WithTx` | BUG-5 (this release) |
| **INV-17** (FTS5 ordering) | **YES** | `agent_memory.Update` ordering (DELETE fts → UPDATE base → re-read → INSERT fts) under SERIALIZABLE; schema stays contentless | BUG-8 (this release) |

---

## 4. Pipeline vs workflow runtime

`ARCHITECTURE-V4.md §8` describes a `Workflow` struct with mutable
States/Events/Transitions driven by an orchestrator via
`modify_workflow`. **This is aspirational.** What v4-alpha.1 ships is
a fixed FSM:

```
spec_create → artifact_log → drift_judge → drift_log → (aligned | drift_detected | needs_human)
```

Implemented in `internal/v4alpha/vibe/pipeline.go`:

- `SpecStore`, `ArtifactStore`, `DriftStore` (3 separate stores, one
  per state type)
- `Pipeline.Publish(ctx, spec, artifact) → Verdict`
- `Pipeline.Status(artifact_id) → DriftReport`
- `Pipeline.ResolveDrift(drift_id, decision, note) → void`
- `Judge` interface (`Evaluate(ctx, artifact_ref, spec_intent) →
  Verdict`) — `NoOpJudge` always returns `VerdictAligned + Confidence
  1.0`. The LLM-backed `Judge` lands in BUG-9.

The mutable workflow runtime (Workflow struct + modify_workflow
event + drift_judge of modifications) lands in v4.0.0-beta at the
earliest.

---

## 5. Live verification

The `cmd/dark-memory-v4 serve` binary accepts JSON-RPC over stdio.
Verified end-to-end on Windows (2026-09-27):

```bash
$ dark-memory-v4 migrate --dsn=/tmp/v4-live.db
  ok  audit
  ok  session
  ok  manifest/cap
  ok  manifest/meta
  ok  vibe/spec
  ok  vibe/artifact
  ok  vibe/drift
  dark-memory-v4: migrate ok at version v4alpha/2026-09-27/001

$ printf "%s\n" \
    '{"jsonrpc":"2.0","id":1,"method":"initialize",...}' \
    '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
    '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | dark-memory-v4 serve --dsn=/tmp/v4-live.db
→ 25 tools registered (full list in §1 above)
```

---

## 6. What you can rely on

| Reliability | What's stable |
|---|---|
| ✅ Stable (won't change) | Tool wire names, agent_memory schema, FTS5 ordering (INV-17), worker pool size=1, store/WithTx contract (INV-16) |
| ⚠️ Likely to evolve | Package names (still aspirational vs actual drift), Pipeline API (LLM judge swap), Constitution (still hardcoded) |
| ❌ Not implemented | security/* (INV-11..15), mutable Workflow, red-team mods, real LLM judge |

---

## 7. Where to read next

- `ARCHITECTURE-V4.md §5 (revised)` — current package layout
- `ARCHITECTURE-V4.md §6 (revised)` — invariant adoption
- `docs/INVARIANTS.md` — INV-1..INV-17 definitions (INV-16 + INV-17 added 2026-09-27)
- `docs/AGENT_MEMORY_SCHEMA.md` — table shape, FTS5, indexes
- `CONSTITUTION-V4.md` — release-integrity constitution (v4 fork)
- `CHANGELOG.md` (top of file) — entry `[4.0.0-alpha.1]`
- `docs/archive/v3.0-wave-4/` — v3 context (legacy, kept for reference)
