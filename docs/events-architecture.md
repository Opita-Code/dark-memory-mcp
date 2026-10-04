# Events Architecture (alpha.23, schema v32)

> **Status**: Phase 12 SHIPPED (2026-10-04, local tag
> `v4.0.0-alpha.23`). This doc is the **canonical architecture
> reference** for the events subsystem. Cross-refs:
> `docs/v4-status.md §1.11`, `CHANGELOG.md [4.0.0-alpha.23]`,
> `docs/specs/SPEC-alpha-11-phase12-modification-events.md`.

## 1. Why a single polymorphic events table

The events subsystem records **every state change** in
dark-memory-mcp with one invariant: **one row in the events table,
one tamper-evident HMAC chain link**. Two families of events share
this invariant:

| Family | Examples | Why they share the table |
|---|---|---|
| **Modifications** | Decision supersessions, decay refreshes, schema migrations, calibration updates, judge verdict updates, persona updates, embedder refreshes | Each is a **durable state change** to a row in another table (agent_memory, sdd_evaluations, etc.). |
| **Progress** | Async drift_judge lifecycle (started → in_progress → completed), async `delegate_intent` tree (DECIDE → EXTRACT → MIND → CURATE → COMPLETED) | Each is a **durable checkpoint** of a multi-step pipeline. |

A single polymorphic table (kind discriminator) is preferred over
two parallel tables because:

1. **Single HMAC chain** — the chain is continuous across both
   families, so a verifier detects tampering on EITHER family by
   checking the chain's monotonic nonce sequence.
2. **Unified observability surface** — one tool (`event_log`) +
   one tree-expansion tool (`event_replay`) cover all events.
3. **Cross-family joins are trivial** — e.g., "show me all events
   for artifact X across the modification AND progress lifecycles".

**SOTA-grounded**:
- **LangFuse Data Model v2** — one observation table with kind
  discriminator (SPAN, EVENT, GENERATION, ...).
- **AWS Step Functions execution history** — one execution_history
  table with eventType discriminator + parentId/rootId chain.
- **OpenTelemetry GenAI semantic conventions** — `gen_ai.*` span
  semantics for `classification`, `target_table`, `rationale_kind`.

## 2. The 27-column event row

`internal/store/events.go` (canonical location) +
`internal/store/sqlite/events.go` (type aliases). Postgres parity is
provided by 6 `notImpl` stubs in `internal/store/postgres/store.go`
— the schema is ready; runtime lands in Phase 13+.

```go
type Event struct {
    ID             int64     // primary key
    ProjectID      string    // INV-7 scoping (active project)
    Kind           string    // "modification" | "progress" | "sentinel"
    TS             string    // RFC3339Nano, server-side stamped
    Actor          string    // "system:package/path" or user-supplied
    SessionID      string    // INV-8 cross-process correlation
    ParentEventID  int64     // 0 = root; >0 = child of parent_event_id
    RootEventID    int64     // 0 = this IS root; >0 = root of this tree
    TargetTable    string    // "agent_memory" | "sdd_evaluations" | ...
    TargetRowID    int64     // id of the modified/progressed row
    Operation      string    // "insert" | "update" | "delete" | "emit"
    Classification string    // "judge" | "schema" | "embedder" | ...
    Source         string    // canonical source path (e.g., "judge/store.go:645")
    Rationale      string    // human-readable reason for this event
    RationaleKind  string    // "user" | "auto" | "sentinel" | "system"
    PayloadBefore  []byte    // JSON of the row BEFORE the change
    PayloadAfter   []byte    // JSON of the row AFTER the change
    Confidence     float64   // judge verdict confidence (modifications only)
    JudgeVerdict   string    // "aligned" | "drift_detected" | "needs_human"
    JudgeReasoning string    // LLM-as-judge reasoning text
    JudgeRunID     int64     // sdd_evaluations row id of the verdict
    ProcessID      string    // "drift-<artifactID>" | "delegate-<sessionID>" | ""
    Phase          string    // "started" | "in_progress" | "completed" | "failed"
    ProgressPct    float64   // 0.0..100.0
    Message        string    // human-readable status
    DurationMs     int64     // elapsed milliseconds (for completed/failed)
    ErrorMsg       string    // error message (failed only)
    PayloadJSON    []byte    // catch-all JSON for event-specific fields
}
```

### 2.1 The 3 indexes

| Index | Columns | Covers |
|---|---|---|
| `idx_events_target` | (target_table, target_row_id) | "All events for row X" |
| `idx_events_process_id` | (process_id) | "All events for pipeline P" |
| `idx_events_root_event_id` | (root_event_id) | "All events in tree T" |

The (target_table, target_row_id) pair is the most-queried axis
(modifications always have a target row; progress events for
non-row pipeline steps leave it blank).

## 3. The 8 AutoEmitter helpers

`internal/v4alpha/event/auto_emit.go` (~265 LoC). All helpers are
**fire-and-forget** by design: failures are logged internally,
never propagated to the caller. The primary action (data write,
cache eviction, etc.) is the caller's responsibility; the event
is for forensic traceability.

The 8 helpers are organized by the table they target:

| Helper | Target table | Q-INDIAN |
|---|---|---|
| `EmitSupersede` | agent_memory | sync |
| `EmitDecayRefresh` | agent_memory | async |
| `EmitSchemaMigration` | schema_migrations | sync |
| `EmitEmbedderRefresh` | (future) embedder_entities | sync |
| `EmitCalibrationUpdate` | sdd_evaluations | sync |
| `EmitCacheInvalidation` | (semantic cache table) | async |
| `EmitJudgeVerdictUpdate` | sdd_evaluations | sync |
| `EmitPersonaUpdate` | personas_v4 | sync |

### Q-INDIAN policy (operator decision, 2026-10-04)

**Sync vs async** is decided by **semantic impact**:

- **Sync (Emit)** — modifications that change durable state visible
  to operators querying immediately (e.g., a judge verdict that
  feeds a downstream decision).
- **Async (EmitAsync)** — progress events + modifications to
  observation-only state (e.g., `EmitDecayRefresh` only updates the
  access count for FTS5 ranking — a query that runs before the
  async dispatch lands gets the stale count, which is fine because
  the count is already decayed).

**Q1 selectivo**: pure cache invalidations (LRU, TTL) do NOT emit
at all. The `EmitCacheInvalidation` helper takes a `semantic bool`
flag — callers must explicitly opt in. This prevents event-table
spam from routine cache evictions.

### 3.1 The `internal/eventholder` leaf package

AutoEmitter needs to be callable from packages that **cannot
import `internal/v4alpha/event`** (cycle risk: store/sqlite ↔
v4alpha/event). The solution is a leaf package with NO imports of
v4alpha packages:

```go
// internal/eventholder/holder.go (29 LoC)
package eventholder

import (
    "context"
    "sync/atomic"
)

type AutoEmitter interface {
    EmitSupersede(ctx context.Context, oldMemID, newMemID int64, trigger, reason string)
    EmitDecayRefresh(ctx context.Context, rowID, newAccessCount int64)
    EmitSchemaMigration(ctx context.Context, fromVersion, toVersion int, rationale string)
    EmitEmbedderRefresh(ctx context.Context, rowID int64, newEntityCount int)
    EmitCalibrationUpdate(ctx context.Context, evalID int64, newPointEstimate float64, method string)
    EmitCacheInvalidation(ctx context.Context, cacheTable string, rowID int64, semantic bool, reason string)
    EmitJudgeVerdictUpdate(ctx context.Context, evalID int64, verdict string, confidence float64)
    EmitPersonaUpdate(ctx context.Context, personaID, rationale string)
}

var holder atomic.Pointer[AutoEmitter]

func Set(ae AutoEmitter) { holder.Store(&ae) }
func Get() AutoEmitter    { p := holder.Load(); if p == nil { return nil }; return *p }
```

Every caller does `if ae := eventholder.Get(); ae != nil { ae.EmitXxx(...) }`. The
eventholder is set ONCE at boot (`cmd/dark-memory-v4/main.go:233`
sets it to `v4alpha/event.NewAutoEmitter(...)`). Pre-boot
(eventholder.Get() == nil) silently no-ops.

## 4. The EventWriter (INV-20 combo (a)+(c))

`internal/v4alpha/event/writer.go` (307 LoC). The single write-path
for the events table. Implements INV-20 combo (a)+(c):

### 4.1 INV-20 combo (a)+(c) rationale

INV-20 says **"no semantic write without rationale"**. The naïve
implementation is to return `ErrRationaleRequired` and refuse the
write. The problem: a refused write means **no event row at all**,
so the operator can't grep for "all the times someone forgot to
provide a rationale".

Combo (a)+(c) preserves the API contract AND the forensic trail:

- **(a) sentinel emission** — when a caller submits a write with
  `rationale = ""`, the writer emits ONE sentinel event BEFORE
  returning `ErrRationaleRequired`. The sentinel has
  `kind = "sentinel"`, `rationale_kind = "sentinel"`,
  `target_table = caller_function_name` (resolved via
  `runtime.CallerForPC`). The payload_after contains the partial
  event the caller tried to write.
- **(c) caller surface** — the writer returns
  `ErrRationaleRequired` to the caller. The API contract is
  preserved (caller's code STILL fails loudly). The sentinel is
  NOT a silent workaround — it's a forensic breadcrumb.

### 4.2 The HMAC chain integration (ADR-016 + ADR-018)

`internal/audit/writer.go` already chains `write_audit` rows with
HMAC-SHA256 over `(chain_prev || canonical_row_bytes)`. The
EventWriter computes the SAME HMAC over `(chain_prev ||
canonical_event_bytes)` and embeds the chain_self_hmac column into
the events table. **One chain across both tables** means a
verifier can detect tampering on EITHER table by checking the
chain's monotonic nonce sequence.

`dark_memory_audit_export` reads both tables and produces a
single HMAC-chained JSONL stream. `dark_memory_audit_verify`
verifies the chain end-to-end.

## 5. The 2 observer tools (EVENTS namespace)

`internal/tools/events.go` (~225 LoC). 2 MCP tools:

### 5.1 `dark_memory_event_log`

Filterable list. Inputs:

| Filter | Type | Notes |
|---|---|---|
| `kind` | string | `"modification" \| "progress" \| "sentinel"` |
| `target_table` | string | exact match |
| `target_row_id` | int64 | exact match |
| `process_id` | string | exact match (drift-judge process_id, etc.) |
| `session_id` | string | exact match (INV-8 cross-process correlation) |
| `actor` | string | exact match (e.g., "system:migrate/migrate.go") |
| `since_id` | int64 | cursor: rows with id > since_id |
| `limit` | int | clamped to [1, 10000] |

Returns rows in `id ASC` order (LangFuse timeline pattern). Honors
INV-7 (active project scoping — the caller's session determines
the project_id filter).

### 5.2 `dark_memory_event_replay`

Tree expansion. Inputs:

| Field | Type | Notes |
|---|---|---|
| `event_id` | int64 | the root event id |
| `include_children` | bool | default true |

Returns `*EventReplayResult { Root, Children, ChildCount }`. Reads
via `ListEventsByRootEventID` (the canonical tree-expansion path).
NotFound returns an empty result (`Root=nil`) so callers can detect
missing event_id without an error.

## 6. The 2 progress emitters

### 6.1 DriftJudgeProgressEmitter (T-103b)

`internal/v4alpha/event/progress_drifter.go` (~155 LoC). 4 emit
points:

| Method | Phase | progress_pct |
|---|---|---|
| `EmitStarted` | "started" | 0 |
| `EmitInProgress` | "in_progress" | 25 |
| `EmitCompleted` | "completed" | 100 |
| `EmitFailed` | "failed" | 0 (error_msg set) |

Wired into `internal/orchestration/publish_vibe.go:runAsyncJudgePipeline`.
Each event has `process_id = "drift-<artifactID>"` so an operator
can grep all events for a single artifact with one query.

### 6.2 DelegationProgressEmitter (T-103c)

`internal/v4alpha/event/progress_delegation.go` (~175 LoC). 5 emit
points + failure:

| Method | Phase | parent_event_id |
|---|---|---|
| `EmitDecide` | "started" | 0 (IS root) |
| `EmitExtract` | "in_progress" | rootEventID |
| `EmitMind` | "in_progress" | rootEventID |
| `EmitCurate` | "in_progress" | rootEventID |
| `EmitCompleted` | "completed" | rootEventID |
| `EmitFailed` | "failed" | rootEventID |

Wired into `internal/v4alpha/transport/mcp/wire.go:RunDelegateIntentCore`.
The DECIDE event captures `rootEventID` synchronously; child events
use `parent_event_id = rootEventID` so an operator can trace the
entire DECIDE→EXTRACT→MIND→CURATE→COMPLETED tree with one
`event_replay` call.

## 7. The 2 personas + 2 eval_types

`internal/v4alpha/judge/personas_v4.go` + `internal/ssd/types.go`.

### 7.1 judge-modifications

Evaluates modification events:

- **Rationale coverage** — does every event have a non-empty
  rationale?
- **Classification fit** — does the classification match the
  operation+target_table?
- **Source attribution** — is the source path canonical?
- **Payload completeness** — for UPDATE operations, are
  payload_before and payload_after both set?

### 7.2 judge-progress

Evaluates progress events:

- **Process ID consistency** — same process_id across all phases?
- **Phase progression monotonicity** — started → in_progress →
  completed, never backwards.
- **Parent_event_id correctness** — child events reference an
  existing root_event_id with the same process_id.

### 7.3 The 2 eval_types

`EvalModificationAudit`, `EvalProgressAudit`. 9-provider
per-eval-type recommendations in
`internal/orchestration/recommended_models.go`.

## 8. The 6 of 8 wired helpers

`internal/eventholder/wires_e2e_test.go` (10 tests, all PASS) covers
the 6 wires shipped:

| Helper | Call site | Sync/Async |
|---|---|---|
| `EmitSupersede` | `internal/store/sqlite/bitemporal.go:MarkSupersededAgentMemory` (after tx commit) | sync |
| `EmitDecayRefresh` | `internal/v4alpha/recall/decay.go:RefreshOnAccess` (after UPDATE) | async |
| `EmitPersonaUpdate` | `internal/v4alpha/judge/personas_v4.go:RegisterPersonaContent` (after registry update) | sync |
| `EmitSchemaMigration` | `internal/migrate/migrate.go:Migrate` (after each `applyOne` commit) | sync |
| `EmitCalibrationUpdate` | `internal/v4alpha/judge/store.go:SetCalibration` (after UPDATE) | sync |
| `EmitJudgeVerdictUpdate` | `internal/v4alpha/judge/store.go:SaveEvaluation` (after tx commit) | sync |

### 8.1 Why 6 of 8 and not 8 of 8

The remaining 2 are deferred for principled reasons:

- **`EmitEmbedderRefresh`** — no embedder code exists yet. The
  helper is exported but no caller invokes it. Phase 13+ when the
  embedder integration ships.
- **`EmitCacheInvalidation`** — the only existing site
  (`ExtractCache.Invalidate` in
  `internal/v4alpha/delegation/cache.go:112`) is pure LRU; per
  Q-INDIAN selectivo it emits NOTHING. If a semantic cache
  invalidation trigger arises (e.g., FTS index rebuild on entity
  extraction change), it's re-evaluated.

The 2-of-8 gap is **documented in the OD7 (s) invariant** (see
`docs/v4-status.md §1.11.11`). OD7 (s) is SATISFIED with the gap
because the gap is **no-ops by design**, not implementation drift.

## 9. End-to-end flow

```
Operator calls drift_judge on artifact art-7
  │
  ▼ runAsyncJudgePipeline (publish_vibe.go)
  │ DriftJudgeProgressEmitter.EmitStarted
  │   process_id = "drift-art-7", parent_event_id = 0
  │
  ▼ LLM call begins
  │ DriftJudgeProgressEmitter.EmitInProgress
  │   process_id = "drift-art-7", parent_event_id = rootEventID, progress_pct = 25
  │
  ▼ LLM call succeeds
  │ judge.Store.SaveEvaluation
  │   [sdd_evaluations row + audit_log row commit]
  │ eventholder.Get().EmitJudgeVerdictUpdate(ctx, id, "aligned", 0.92)
  │   target_table = "sdd_evaluations", target_row_id = id, kind = "modification"
  │
  ▼ DriftJudgeProgressEmitter.EmitCompleted
  │   process_id = "drift-art-7", parent_event_id = rootEventID, progress_pct = 100
  ▼
HMAC chain link added (chain_prev → chain_self)
Audit verifier can replay all 4 events via:
  dark_memory_event_log(target_table = "sdd_evaluations", target_row_id = id)
  dark_memory_event_replay(event_id = rootEventID)
```

## 10. Where to read next

- `docs/specs/SPEC-alpha-11-phase12-modification-events.md` — full
  Phase 12 spec (700+ LoC).
- `docs/v4-status.md §1.11` — Phase 12 changelog with all 9 commits
  + OD7 gate + tier-1 SOTA grounding.
- `CHANGELOG.md [4.0.0-alpha.23]` — release entry.
- `internal/v4alpha/event/auto_emit.go` — 8 helpers + docs.
- `internal/v4alpha/event/writer.go` — EventWriter + INV-20 combo.
- `internal/eventholder/holder.go` — leaf package.
- `internal/tools/events.go` — 2 observer tools.
- `internal/store/events.go` — canonical Event struct.
- `internal/eventholder/wires_e2e_test.go` — 10 e2e wire tests.