# SPEC-alpha-11-phase12-modification-events — Phase 12 contract (Camino B + progress events)

**Status (2026-10-04)**: 📋 spec REVISED to include progress events + async judge +
subagent audit. Q1-Q4 operator decisions baked in. Awaiting T-101 code.

**Branch**: `feat/v4-redesign` (local-only, no remote push).

**Predecessor**: `docs/specs/SPEC-alpha-11-phase11-camino-e.md` (operator
decision record for Phase 11 = Camino E, agent_memory row 2397). Phase 12
= Camino B per row 2397.

**Cross-version lockstep hash pin** unchanged:
`4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`.

**SOTA grounding (2026-10-04)**:
- [OpenTelemetry GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai) — `gen_ai.*` attributes, `gen_ai.client.inference.operation.details` event
- [LangFuse data model](https://langfuse.com/docs/observability/data-model) — single observations table with discriminator, Sessions → Traces → Observations nesting, OTel-based background batching
- [AWS Step Functions](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-statemachines.html) — Standard (sync, exactly-once, history) vs Express (async, at-least-once, CloudWatch); `.sync`/`.waitForTaskToken` patterns
- `docs/sota-critique.md §7.4` — north star (modifications is primitiva #4)

---

## §0 TL;DR + north star position

Phase 12 closes **primitiva #4 del §8** of the v4.0.0-GA north star:
**modifications AND process progress** (combined into a single
event-sourced log). Per `docs/sota-critique.md §7.4`:

> §8 fully shipped (modification events, LLM-as-judge for modifications,
> rationale-first, modification-replayability)

dark-memory v4-alpha.22 ships 4 of 5 §8 primitivas: capture (write_audit
chain), recall (vibe-case-aware), judge (14 personas + G-Eval), verify
(audit_verify HMAC chain). The **5th is modify + process audit** — the
system auditing its own state changes AND its own async process
progress, not just the agent's.

### Phase 12 deliverables (after SHIP):

**Modifications:**
- `events` table with `kind='modification'` discriminator (state changes)
- `EventWriter` analog of `audit.Writer` with INV-20 enforcement
- `eval_type=modification_audit` + new persona `judge-modifications`
- `dark_memory_event_log` (query) + `dark_memory_event_replay` (reconstruct state)

**Progress events (NEW in this revision):**
- Same `events` table with `kind='progress'` discriminator (async work)
- Auto-emission from `pipeline_status` async drift_judge loop (T-103b)
- Auto-emission from `delegate_intent` parent + child subagent events (T-103c)
- OpenTelemetry GenAI-style attributes in `payload_json` field

**Cross-cutting:**
- INV-20 rationale-first invariant with combo (a)+(c) belt-and-suspenders
- Single HMAC chain across both kinds (Phase 11 T-401 already enabled)
- ~1,800 LoC code + ~35 tests + ~900 LoC docs
- Risk MED-HIGH (changes the fundamental data model)
- 4-6 weeks focused

**North star impact**: alpha.22 ~85% → alpha.23 ~95% (closes primitiva
#4 of 5 of §8).

---

## §1 Problem statement

### §1.1 The gap: silent modifications

dark-memory v4-alpha.22 records **agent-side actions** (write_audit).
It does NOT record **system-side modifications**. Concretely:

| Event | Currently tracked? |
|---|---|
| Agent saves `agent_memory` row | ✅ write_audit |
| Agent runs `vibe_publish` | ✅ write_audit |
| Agent calls `vibe_spec` | ✅ write_audit |
| LLM judge updates `sdd_evaluations` confidence calibration | ❌ NOT TRACKED |
| Dark-memory auto-decays an agent_memory score (Phase 5 DecayScore) | ❌ NOT TRACKED |
| Dark-memory auto-supersedes a decision row (Phase 7 Chunk 7.7 mark_superseded) | ❌ NOT TRACKED |
| Dark-memory auto-migrates schema (any migration) | ❌ NOT TRACKED |
| Embedder refreshes an entity graph (Phase 5 Chunk 5.5 RefreshOnAccess) | ❌ NOT TRACKED |
| Drift_judge updates calibration_method (Phase 7 alpha.16 ADR-011) | ❌ NOT TRACKED |

This is the **primitiva #4 of §8 gap**: the system modifies state
without telling anyone. It's the gap between "the agent can be
audited" and "the system can be audited."

### §1.2 The new gap: blind async processes

Phase 12 (this revision) also addresses a problem observed during Phase 11
testing: **async processes are black boxes to the orchestrator**.

| Process | Currently observable? |
|---|---|
| LLM judge running in background (async_drift_check=true) | ❌ verdict only at end |
| Subagent spawned by `delegate_intent` | ❌ result only at end |
| Embedder refreshing entity graph (kicks off goroutine) | ❌ no progress |
| Calibration re-runs after N evaluations | ❌ no heartbeat |
| Schema migration on a large table | ❌ silent until done |

The orchestrator (top-level agent loop) can't see what these processes
are doing. If they hang, crash, or produce surprising results, the
orchestrator finds out too late. This is what LangSmith / LangFuse /
AWS Step Functions / OpenTelemetry GenAI solve in their domains.

### §1.3 Why this matters (combined)

**Without modification events**:
- Drift detection catches drift. But it doesn't catch **silent
  modifications** — the system changing state without going through
  the judge pipeline.
- Audit chain tells you WHAT was written. It doesn't tell you WHY
  the system decided to write that.
- Replayability requires event sourcing — without modification events,
  we can't reconstruct state at time T.

**Without progress events**:
- The orchestrator sees async work as fire-and-forget.
- A hung LLM judge wastes compute for minutes before failing.
- A subagent that crashes silently leaves the orchestrator waiting.
- No way to tune async behavior (latency budgets, retry policies)
  without instrumentation.

**With modification + progress events**:
- The full §8 5-primitive loop closes: capture → recall → judge →
  **modify+progress** → verify.
- dark-memory becomes auditable by LLM-as-judge for its own behavior.
- The system can REPLAY modifications to reconstruct intent, not just
  state.
- The orchestrator can OBSERVE async work and react to hung processes,
  surprising verdicts, or cascading subagent failures.

### §1.4 The §8 primitivas from the north star

From `docs/sota-critique.md §7.4`:

> §8 fully shipped (modification events, LLM-as-judge for modifications,
> rationale-first, modification-replayability)

§8 is the **5 primitivas** that dark-memory needs to be a complete
SOTA 2025-26 dark-memory:

1. **Capture** (write_audit) — ✅ shipped alpha.15
2. **Recall** (vibe-case-aware memory) — ✅ shipped alpha.18
3. **Judge** (LLM-as-judge + 14 personas + G-Eval) — ✅ shipped alpha.16 + alpha.19
4. **Modify** (this Phase 12: modifications + progress) — ❌ pending
5. **Verify** (audit chain HMAC + verify) — ✅ shipped alpha.20

Phase 12 = primitiva #4 (modify). The 4 sub-deliverables per §7.4:
- modification events (the table + writer) + progress events (shared table)
- LLM-as-judge for modifications (the eval_type + persona)
- rationale-first (INV-20) — applies to modifications, progress events
  have `payload_json` for rationale-equivalent context
- modification-replayability (the replay tool)

---

## §2 Non-goals (scope boundary)

Explicitly OUT of Phase 12:

- **Real-time modification stream** (Kafka-style). Phase 12 ships
  write_at_init style — modifications recorded synchronously at the
  point of decision.
- **Cross-project modification federation**. Per-project only.
  Federation is Phase 13+ (Camino C — Streamable HTTP enables it).
- **Modification rollback as a primitive**. Phase 12 ships replay
  (read-only history reconstruction). True rollback (apply inverse
  events) is Phase 13+ (rationale-first INV-20 is a prerequisite,
  but the apply-inverse engine is separate).
- **Modification LLM judge deliberation in real time**. Phase 12's
  `judge-modifications` runs async (best-effort, never blocks the
  modification write).
- **Modification events for write_audit itself**. Phase 12 modifies
  write_audit the canonical way (i.e., write_audit emits modification
  events when other parts of the system mutate write_audit).
  Recursion is avoided.
- **Separate `progress_events` table**. Phase 12 uses ONE
  polymorphic `events` table with `kind` discriminator (SOTA per
  LangFuse observations table). New tables break chain continuity
  and require duplicate writers + HMAC chains.
- **Progress events for sync operations**. Sync writes don't need
  progress — they're already observable by call/return. Progress is
  ONLY for async (goroutines, subagents, polling loops).
- **Streaming progress to external systems**. Phase 12 ships
  write-to-DB progress. Webhook / OTLP export is Phase 13+.

---

## §3 Data model

### §3.1 Schema migration v32 (additive, NULLABLE)

ONE polymorphic `events` table with `kind` discriminator. SOTA-grounded by
LangFuse (single observations table) + AWS Step Functions (execution
history) + OpenTelemetry GenAI (event types).

```sql
CREATE TABLE IF NOT EXISTS events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,                -- INV-19
    kind            TEXT NOT NULL CHECK(kind IN ('modification', 'progress')),
    ts              TEXT NOT NULL,                -- RFC3339
    
    -- Common (both kinds)
    actor           TEXT NOT NULL,                -- 'system' | 'judge' | subagent_id | 'operator:<name>'
    session_id      TEXT,                         -- which session originated this (NULL for system-side)
    parent_event_id INTEGER REFERENCES events(id),-- nesting tree (LangFuse-style)
    root_event_id   INTEGER REFERENCES events(id),-- top-level execution (Step Functions ARN-like)
    
    -- Modification-only (NULL for progress)
    target_table    TEXT,                         -- which canonical table was modified
    target_row_id   INTEGER,                      -- the row that was changed
    operation       TEXT,                         -- 'insert'|'update'|'delete'|'supersede'
    classification  TEXT,                         -- see §3.3 taxonomy
    source          TEXT,                         -- see §3.4 (which subsystem made the change)
    rationale       TEXT,                         -- INV-20: WHY the change was migrated
    rationale_kind  TEXT,                         -- see §3.5
    payload_before  TEXT,                          -- JSON snapshot of row before (NULL for insert)
    payload_after   TEXT,                         -- JSON snapshot of row after (NULL for delete)
    confidence      REAL,                         -- LLM judge confidence (0..1, NULL if not judged)
    judge_verdict   TEXT,                         -- 'aligned'|'drift_detected'|'needs_human'
    judge_reasoning TEXT,                         -- LLM judge reasoning text
    judge_run_id    INTEGER,                      -- references sdd_evaluations.id (NULL if not judged)
    
    -- Progress-only (NULL for modification)
    process_id      TEXT,                         -- unique per async work unit (UUID)
    phase           TEXT,                         -- 'spawned'|'started'|'running'|'heartbeat'|'completed'|'failed'|'wait_callback'
    progress_pct    REAL,                         -- 0.0..1.0 (NULL if not applicable)
    message         TEXT,                         -- human-readable status
    duration_ms     INTEGER,                      -- for completed/failed events
    error_msg       TEXT,                         -- for failed events
    
    -- OTel GenAI-style payload (both kinds can use)
    payload_json    TEXT,                         -- JSON: {gen_ai.operation.name, gen_ai.request.model, gen_ai.usage.input_tokens, gen_ai.input.messages, ...}
    
    -- HMAC chain (Phase 11 T-401 enabled; same key as write_audit)
    chain_prev      TEXT,
    chain_self      TEXT NOT NULL,
    chain_key_id    TEXT NOT NULL
);

-- Indexes for fast lookup (LangFuse-style query patterns)
CREATE INDEX idx_events_project_ts ON events(project_id, ts);
CREATE INDEX idx_events_kind ON events(kind);
CREATE INDEX idx_events_kind_classification ON events(kind, classification) WHERE kind = 'modification';
CREATE INDEX idx_events_kind_phase ON events(kind, phase) WHERE kind = 'progress';
CREATE INDEX idx_events_process_id ON events(process_id) WHERE process_id IS NOT NULL;
CREATE INDEX idx_events_root_event_id ON events(root_event_id) WHERE root_event_id IS NOT NULL;
CREATE INDEX idx_events_parent_event_id ON events(parent_event_id) WHERE parent_event_id IS NOT NULL;
CREATE INDEX idx_events_target ON events(target_table, target_row_id) WHERE target_table IS NOT NULL;

-- new column on agent_memory
ALTER TABLE agent_memory ADD COLUMN last_modified_by_event_id INTEGER;
```

### §3.2 Relationships to existing tables

- `events.target_row_id` references rows in any of the existing tables
  (agent_memory, write_audit, sdd_evaluations, vibe_specs,
  vibe_artifacts, etc.). Polymorphic FK; enforced at application level
  (SQLite doesn't enforce cross-table FKs).
- `events.judge_run_id` references `sdd_evaluations.id` (for cases
  where an LLM judge was applied to the modification).
- `events.parent_event_id` references `events.id` (self-FK for
  nesting — subagent tree, delegate tree, async chain).
- `events.root_event_id` references the top-level `events.id` of an
  execution (Step Functions execution ARN analog).
- `agent_memory.last_modified_by_event_id` references `events.id` (for
  query convenience; the canonical source is the events table itself).

### §3.3 Modification classification taxonomy

The `classification` field uses the same enum pattern as
`sdd_evaluations.classification`. Initial values:

- `decay` — auto-decay score change (Phase 5 DecayScore)
- `judge` — LLM judge verdict update (Phase 7 Chunk 7.5)
- `supersede` — auto-superseded decision (Phase 7 Chunk 7.7)
- `migrate` — schema migration
- `embedder` — entity graph refresh (Phase 5 Chunk 5.5)
- `calibration` — judge calibration update (Phase 7 alpha.16 ADR-011)
- `cache` — semantic-affecting recall cache invalidate (NOT pure eviction)
- `system` — generic system modification not matching above

Q1 selectivo (operator decision 2026-10-04): pure cache evictions
(LRU, TTL expiry) do NOT emit. Only semantic-affecting cache changes
emit.

Future classifications can be added without schema change.

### §3.4 Source taxonomy

Which subsystem made the change:
- `decay.go` (Phase 5)
- `judge/pipeline.go` (alpha.16)
- `agent_memory/mark_superseded.go` (Phase 7 Chunk 7.7)
- `admin/migrate.go` (any migration)
- `recall/entity.go` (Phase 5 Chunk 5.5)
- `recall/cache.go` (Phase 7 alpha.17)
- `system` — generic

For progress events, `source` can be:
- `pipeline_status.go` (async drift_judge loop)
- `delegate_intent.go` (subagent spawn)
- `subagent/<id>.go` (child subagent)
- `embedder/refresh.go` (entity graph refresh)

### §3.5 Rationale kind taxonomy

The `rationale_kind` field distinguishes WHY the change was made:
- `decay_function` — applied per Phase 5 decay formula
- `judge_verdict` — applied per LLM-as-judge verdict
- `bitemporal_transition` — supersession per Phase 7 bitemporal
- `schema_change` — explicit ALTER TABLE
- `entity_graph_update` — ProGraph BFS found new edges
- `cache_ttl_expired` — recall cache invalidation
- `progress_started` — async process started (progress kind)
- `progress_completed` — async process completed (progress kind)
- `progress_failed` — async process failed (progress kind)
- `unknown` — generic catch-all

### §3.6 Progress phase taxonomy

The `phase` field (progress kind only) follows LangFuse / Step
Functions conventions:

- `spawned` — orchestrator emitted the event, process hasn't started yet
- `started` — process began execution
- `running` — process is making progress (heartbeat-style)
- `heartbeat` — periodic update from long-running process
- `wait_callback` — process is waiting for external callback (Step
  Functions `.waitForTaskToken` pattern)
- `completed` — process finished successfully
- `failed` — process terminated with error

### §3.7 Chain (HMAC)

The `chain_prev` / `chain_self` / `chain_key_id` triple follows the
same pattern as `write_audit` HMAC chain (Phase 9 Chunk 8.5 +
Phase 11 T-401 enabled `DARK_AUDIT_HMAC_KEY`). The chain is
SINGLE — modification and progress events interleave in the same
chain, enabling end-to-end audit.

The chain includes `DARK_AUDIT_HMAC_KEY` (multi-key rotation per
`docs/audit-hmac-rotation.md`).

The chain is computed at emit time, not stored. Verification happens
via the existing `dark_memory_audit_export` + `dark_memory_audit_verify`
tools (events inherit the same chain).

---

## §4 Writer

### §4.1 `EventWriter` (analog of `audit.Writer`)

New type in `internal/v4alpha/event/writer.go`:

```go
type Writer struct {
    db     *sql.DB
    keyring *audit.Keyring
    now    func() time.Time  // injectable for tests
}

type Event struct {
    ID              int64
    ProjectID       string
    Kind            string  // 'modification' | 'progress'
    TS              time.Time
    
    // Common
    Actor           string
    SessionID       string
    ParentEventID   *int64
    RootEventID     *int64
    
    // Modification-only
    TargetTable     string
    TargetRowID     *int64
    Operation       string
    Classification  string
    Source          string
    Rationale       string
    RationaleKind   string
    PayloadBefore   []byte
    PayloadAfter    []byte
    Confidence      *float64
    JudgeVerdict    string
    JudgeReasoning  string
    JudgeRunID      *int64
    
    // Progress-only
    ProcessID       string
    Phase           string
    ProgressPct     *float64
    Message         string
    DurationMs      *int64
    ErrorMsg        string
    
    // Both kinds
    PayloadJSON     []byte  // OTel GenAI-style attributes
}

// Emit is the canonical entry. For modifications with empty rationale,
// it returns ErrRationaleRequired AFTER auto-emitting a sentinel.
func (w *Writer) Emit(ctx context.Context, ev Event) (int64, error)

// EmitAsync is for non-blocking progress events. Caller MUST handle
// the error in a separate goroutine (does not block the hot path).
func (w *Writer) EmitAsync(ctx context.Context, ev Event) {
    go func() {
        if _, err := w.Emit(ctx, ev); err != nil {
            // log to error_events with severity=warn (LangFuse pattern)
        }
    }()
}
```

### §4.2 INV-20 rationale-first invariant (combo (a)+(c))

Q3 combo (operator decision 2026-10-04): belt-and-suspenders.

```go
func (w *Writer) Emit(ctx context.Context, ev Event) (int64, error) {
    if ev.Kind == "modification" && ev.Rationale == "" {
        // (c) sentinel — emit BEFORE the reject so we have forensic record
        sentinelEv := Event{
            Kind: "modification",
            Classification: "inv20_violation",
            Source: ev.Source,
            TargetTable: ev.TargetTable,
            TargetRowID: ev.TargetRowID,
            Operation: ev.Operation,
            Rationale: "MISSING_RATIONALE — auto-emitted pre-reject for forensic record",
            RationaleKind: "inv20_violation",
            Actor: "system",
            // bypass INV-20 check for the sentinel itself (chicken-and-egg avoidance)
        }
        _, _ = w.emitSentinel(ctx, sentinelEv)  // best-effort, never fails
        
        // (a) reject — return error to caller
        return 0, ErrRationaleRequired
    }
    // ... normal emit logic ...
}
```

Why combo (a)+(c):
- (a) reject alone: any forgotten rationale = silent production bug;
  no record of what was attempted
- (c) sentinel alone: operator has no UI to surface sentinels (Q3
  operator decision: "no tengo ningun dashboard")
- Combo: loud failure + forensic record in DB for post-incident
  analysis

Q3 operator decision rationale (2026-10-04): "no dashboard → reject
because sentinel is invisible without UI". Combo gives both: visible
failure + queryable forensic trail.

### §4.3 Auto-emission hooks (T-103a, T-103b, T-103c)

**T-103a: Modification auto-emission (8 sites)**

The 8 system-side modifications listed in §1.1 must emit
modification events automatically. This is done via wrappers around
the existing write paths:

- `internal/recall/decay.go:RefreshOnAccess` → wraps with
  `EventWriter.Emit({kind: "modification", source: "decay.go",
  classification: "decay", rationale: ..., ...})`
- `internal/judge/pipeline.go:UpdateConfidence` → wraps with
  `({kind: "modification", source: "judge/pipeline.go",
  classification: "judge", ...})`
- `internal/store/sqlite/bitemporal.go:MarkSupersededAgentMemory` →
  wraps with `({kind: "modification", source:
  "agent_memory/mark_superseded.go", classification: "supersede",
  ...})`
- `internal/recall/entity.go:RefreshEntityGraph` → wraps with
  `({kind: "modification", source: "recall/entity.go",
  classification: "embedder", ...})`
- `internal/judge/calibration.go:populateCalibration` → wraps with
  `({kind: "modification", source: "judge/calibration.go",
  classification: "calibration", ...})`
- `internal/recall/cache.go:InvalidateCache` (semantic-affecting only)
  → wraps with `({kind: "modification", source: "recall/cache.go",
  classification: "cache", ...})`. **Q1 selectivo: pure LRU eviction
  does NOT emit.**
- `internal/judge/pipeline.go:UpdateVerdict` (when judge re-runs)
- `internal/embedder/integration.go:RefreshEmbedding` (alpha.20)

Each wrapper adds ~10-20 LoC. Total auto-emission wiring: ~120 LoC.

**T-103b: Progress from async drift_judge (pipeline_status loop)**

In `internal/judge/pipeline_status.go`, when an async drift check is
running, emit progress events:

```go
// Inside the background loop
processID := fmt.Sprintf("drift-%d", artifactID)
parentEvID, _ := w.Emit(ctx, Event{
    Kind: "progress", ProcessID: processID, Phase: "started",
    PayloadJSON: jsonMarshal({
        "gen_ai.operation.name": "drift_judge",
        "gen_ai.provider.name": "judge",
        "eval_type": "drift_judge",
        "artifact_id": artifactID,
    }),
})
// periodic heartbeat
w.EmitAsync(ctx, Event{
    Kind: "progress", ProcessID: processID, Phase: "heartbeat",
    ProgressPct: 0.5,
    Message: "evaluating hypothesis against premise",
    ParentEventID: parentEvID,
})
// completion
w.Emit(ctx, Event{
    Kind: "progress", ProcessID: processID, Phase: "completed",
    DurationMs: 4200,
    PayloadJSON: jsonMarshal({
        "verdict": "aligned",
        "gen_ai.usage.input_tokens": 450,
        "gen_ai.usage.output_tokens": 120,
        "gen_ai.response.finish_reasons": ["stop"],
    }),
    ParentEventID: parentEvID,
})
```

**T-103c: Progress from delegate_intent (parent + child)**

In `internal/v4alpha/delegation/intent.go`, parent emits spawn event;
child subagent emits its own events with `parent_event_id` linking:

```go
// In delegate_intent.go (parent)
parentEvID, _ := w.Emit(ctx, Event{
    Kind: "progress", ProcessID: subagentID, Phase: "spawned",
    Message: fmt.Sprintf("delegate_intent: %s for subagent %s", case, subagentID),
    PayloadJSON: jsonMarshal({
        "vibe_case": case,
        "task_description": taskDescription,
    }),
})

// In subagent's own execution (via SubagentRegister binding)
rootEvID := parentEvID
w.Emit(ctx, Event{
    Kind: "progress", ProcessID: subagentID, Phase: "started",
    ParentEventID: parentEvID, RootEventID: rootEvID,
    Actor: subagentID, SessionID: subagentSessionID,
})
// ... subagent works, emitting more progress events with same
// parent_event_id and root_event_id ...
w.Emit(ctx, Event{
    Kind: "progress", ProcessID: subagentID, Phase: "completed",
    ParentEventID: parentEvID, RootEventID: rootEvID,
    PayloadJSON: jsonMarshal({
        "artifact_ids": [...],
        "memory_ids": [...],
    }),
})
```

Total auto-emission wiring (T-103a/b/c combined): ~250 LoC.

---

## §5 LLM-as-judge for events

### §5.1 New eval_types: `modification_audit` + `progress_audit`

Following the `eval_type` enum pattern in `internal/v4alpha/judge/`:

- `eval_type=modification_audit` — evaluates a modification event
  against the original spec/intent that motivated the change.

- `eval_type=progress_audit` (NEW in this revision) — evaluates a
  completed progress event's lifecycle (started → heartbeat → completed)
  for sanity (e.g., `progress_pct` monotonic, `duration_ms` plausible).

The judge compares:
- For modification_audit: rationale coherence + payload_after matches
  rationale intent.
- For progress_audit: phase sequence valid, progress_pct monotonic
  increasing, duration matches typical budget, payload fields consistent.

Returns: `aligned` (event was justified + correct),
`drift_detected` (rationale doesn't match the change / progress looks
anomalous), `needs_human` (judge uncertain).

### §5.2 New personas: `judge-modifications` + `judge-progress`

Per `internal/orchestration/judge_personas_types.go`:
- `persona_id="judge-modifications"` — `source=PersonaSourceV4Alpha`,
  `lens="modification alignment"`, `constraints=["must verify
  rationale coherence", "must flag silent intent drift"]`,
  `rubric="modification_audit_v1"`
- `persona_id="judge-progress"` — `source=PersonaSourceV4Alpha`,
  `lens="progress lifecycle sanity"`, `constraints=["must verify
  phase sequence", "must flag stalled or stuck processes"]`,
  `rubric="progress_audit_v1"`

### §5.3 Statistical self-bias detection (per ADR-011)

Both `judge-modifications` and `judge-progress` personas are registered
with the project-scoped calibration chain
(`internal/v4alpha/judge/calibration.go`). When `n_evaluations >=
ShouldRecalibrate threshold`, the calibration columns update per the
alpha.16 bootstrap-CI methodology.

### §5.4 Confidence intervals per arxiv:2511.21140

Following the ICML 2026 paper "How to Correctly Report LLM-as-a-Judge
Evaluations" (Lee et al., arxiv:2511.21140 v4):

- Each `*_audit` evaluation reports `confidence_calibrated` with
  `calibration_ci_low` + `calibration_ci_high` (95% CI).
- Bias correction for judge sensitivity/specificity.
- Adaptive sample allocation (more samples for borderline cases).

The calibration columns already exist on `sdd_evaluations` (alpha.16).
`*_audit` reuses them.

---

## §6 Replay tool

### §6.1 `dark_memory_event_log` + `dark_memory_event_replay`

Two new MCP tools (Phase 12 adds 3 tools, see §8 T-105):

```
dark_memory_event_log(
  kind: 'modification'|'progress'|'',
  project_id: string,                    // default = active
  process_id: string,                    // optional (progress kind)
  root_event_id: int,                    // optional (delegation tree)
  parent_event_id: int,                  // optional
  classification: string,                // optional (modification kind)
  phase: string,                         // optional (progress kind)
  since_id: int,                         // incremental fetch (LangFuse pattern)
  limit: int                             // default 100
) -> []Event
```

```
event_replay(
  root_event_id: int,                    // OR
  process_id: string,                    // OR
  target_table: string,                  // for modification replay
  target_row_id: int,
  as_of: RFC3339,                        // optional
  include_rationales: bool               // default true
) -> {
  current_state: JSON,
  history: [{ts, source, classification, operation, rationale, payload_before, payload_after, phase, progress_pct, message}],
  as_of: RFC3339
}
```

### §6.2 Algorithm

For modification replay (a given target row):
1. Query all events WHERE kind='modification' AND target_table=X AND
   target_row_id=Y AND ts <= as_of ORDER BY ts ASC.
2. Starting from `payload_after` of the FIRST event (or NULL if
   operation='insert'), apply each subsequent `payload_before` or
   `payload_after` in order to reconstruct state.
3. Return the reconstructed state + the history array (with
   rationales).

For progress replay (a given process_id or root_event_id):
1. Query all events WHERE kind='progress' AND (process_id=X OR
   root_event_id=Y) ORDER BY ts ASC.
2. Return the timeline array (no fold needed — events ARE the state).
3. Optionally include child events via `parent_event_id` traversal
   (LangFuse-style nesting).

This is the classic event-sourcing fold (per the "Event Sourcing 2026
Field Guide" by kihiujohn.github.io) + the Step Functions execution
history view.

### §6.3 Snapshot optimization

For rows with >1000 modifications, fold-from-beginning is slow. Per
the SOTA event-sourcing pattern:

- Periodic snapshot of `vibe_artifacts` (already exists for some
  tables; can be extended).
- Replay tool loads the nearest snapshot + folds only modifications
  after the snapshot's `as_of`.

Phase 12 ships the fold-only mode. Snapshot support is Phase 13+.

---

## §7 OD7 e2e gate for Phase 12 SHIP (13 NEW tests, 23 total)

Before tagging `v4.0.0-alpha.23`:

**Carried from Phase 11 OD7 (12 tests, all currently passing):**
- (a) session lifecycle chain
- (b) vibe_publish + drift_judge round-trip
- (c) audit chain HMAC
- (d) override pattern sweep
- (e) persona registry = 14
- (f) 10 concurrent writes monotonic
- (g) cross-session atomic mirror survival
- (h) needs_human surface
- (i) override sweep re-run via v3

**NEW Phase 12 OD7 (11 new tests):**
- (j) **MISSING_RATIONALE sentinel count == 1 after rejected write** — combo (a)+(c) verification
- (k) **caller gets `ErrRationaleRequired`** — INV-20 enforcement
- (l) **zero modifications with empty rationale in audit dump after triage** — INV-20 invariant
- (m) **single events table polymorphic schema works** — kind discriminator
- (n) **HMAC chain continuous across both kinds** — single chain verification (audit_export covers)
- (o) **async drift_judge emits ≥3 progress events** (started, heartbeat, completed) — T-103b
- (p) **subagent delegation tree parent + child events** — T-103c
- (q) **`event_log` query by process_id returns full timeline** — LangFuse-style timeline
- (r) **`event_replay` reconstructs state from event log** — both kinds
- (s) **cross-table invariant: each modification event has matching canonical row** — Q4 wrapper+invariant test
- (t) **progress events don't break modification audit chain** — kind isolation

**0 critical findings required.**

---

## §8 Implementation chunks (T-101..T-106 with T-103 split)

### T-101 Schema migration v32 + EventStore (~250 LoC + tests)

- `internal/store/sqlite/migrations/032_events_polymorphic.go` NEW
- `internal/store/sqlite/events.go` NEW (Store-first pattern,
  mirroring `internal/store/sqlite/bitemporal.go`)
- 5 unit tests + 2 migration tests (idempotency + rollback if
  recoverable)

### T-102 EventWriter + INV-20 combo (a)+(c) (~400 LoC + tests)

- `internal/v4alpha/event/writer.go` NEW
- `internal/v4alpha/event/types.go` NEW (Event struct + enums +
  sentinel errors)
- `internal/v4alpha/event/writer_test.go` NEW (12 tests: rationale
  required + sentinel combo, atomic insert + chain, error paths,
  multi-key rotation, async emit non-blocking, kind discriminator
  validation)

### T-103a Auto-emission modifications (8 sites, ~120 LoC + tests)

- Wrap 8 system-side write sites (see §4.3 T-103a list)
- Each wrap is 10-20 LoC
- `internal/v4alpha/event/auto_emit_modification_test.go` NEW (1 test
  per emit site, 8 tests total)

### T-103b Progress from async drift_judge (~80 LoC + tests)

- `internal/judge/pipeline_status.go` MODIFIED (emit progress events
  in background loop)
- `internal/v4alpha/event/auto_emit_progress_drift_test.go` NEW
  (4 tests: started emits, heartbeat emits, completed emits, parent_event_id
  consistency)

### T-103c Progress from delegate_intent (~80 LoC + tests)

- `internal/v4alpha/delegation/intent.go` MODIFIED (parent spawn
  event)
- `internal/v4alpha/delegation/subagent.go` MODIFIED (child events
  with parent_event_id + root_event_id)
- `internal/v4alpha/event/auto_emit_progress_delegation_test.go`
  NEW (4 tests: parent spawn, child start, child complete, tree
  integrity)

### T-104 judge-modifications + judge-progress personas + eval_types (~350 LoC + tests)

- `internal/orchestration/judge_personas_v4alpha.go` MODIFIED (+2
  entries: judge-modifications + judge-progress)
- `internal/v4alpha/judge/pipeline.go` MODIFIED (+2 new eval_type
  cases: modification_audit + progress_audit)
- `internal/v4alpha/judge/modification_audit.go` NEW (eval logic for
  modifications)
- `internal/v4alpha/judge/progress_audit.go` NEW (eval logic for
  progress)
- `internal/v4alpha/judge/audit_test.go` NEW (10 tests total:
  aligned/drift/needs_human paths for both kinds, phase sequence
  validation, progress_pct monotonicity)

### T-105 event_log + event_replay tools (~250 LoC + tests)

- `internal/v4alpha/event/log.go` NEW (query implementation)
- `internal/v4alpha/event/replay.go` NEW (fold algorithm for
  modifications, timeline assembly for progress)
- `internal/tools/event.go` NEW (RegisterEvent + 3 handlers:
  event_log, event_replay, event_emit_progress)
- 8 tests (timeline query, fold correctness, delegation tree traversal,
  since_id incremental)

### T-106 event_emit_progress helper + LUCIDEZ + docs (~150 LoC)

- `internal/v4alpha/event/emit_progress.go` NEW (helper for callers
  that don't need full Event struct)
- `docs/v4-status.md` + `docs/v4-alpha-11-plan.md` update (Phase 12
  changelog)
- `docs/sota-critique.md §5.2.5/7 addition (modification + progress
  patterns documented, OTel GenAI + Step Functions + LangFuse cited)
- `CHANGELOG.md [4.0.0-alpha.23]`
- `docs/events-architecture.md` NEW (canonical architecture doc,
  ~450 LoC)

**Total**: ~1,800 LoC code + ~35 tests + ~900 LoC docs.

---

## §9 Cross-refs

- `docs/sota-critique.md §7.4` — north star (modifications is primitiva #4)
- `docs/specs/SPEC-alpha-11-phase11-camino-e.md` — Phase 11 spec
  (this contract's predecessor)
- `docs/specs/SPEC-alpha-11-phase5.md` — Phase 5 recall subsystem
  (modifications touch decay.go)
- `docs/specs/SPEC-alpha-11-phase7.md` — Phase 7 subagent wiring
  (modifications touch mark_superseded; progress touches delegate_intent)
- `internal/audit/writer.go` — INV-1 audit writer (EventWriter analog)
- `internal/audit/hmac.go` — HMAC chain design (Phase 9 Chunk 8.5)
- `internal/audit/verify.go` — chain verify design (events inherit)
- `internal/audit/export.go` — chain export (events inherit via
  audit_export)
- `internal/judge/pipeline.go` — eval_type enum (modification_audit +
  progress_audit join)
- `internal/orchestration/judge_personas_v4alpha.go` — persona
  registry (judge-modifications + judge-progress join)
- `internal/recall/decay.go` — auto-emit hook T-103a
- `internal/judge/pipeline_status.go` — auto-emit hook T-103b
- `internal/v4alpha/delegation/intent.go` — auto-emit hook T-103c
- `docs/audit-hmac-rotation.md` — chain key policy (events use the
  same DARK_AUDIT_HMAC_KEY)
- `docs/MANIFIESTO.md` — v4 design philosophy (rationale-first is
  consistent; event sourcing is core model)
- atomic mirror row 2397 (Phase 11 = Camino E operator decision)
- atomic mirror row 2414 (Phase 11 SHIPPED summary)
- Phase 9 alpha.20 SOTA critique §5.2.5 (override patterns — pattern
  #5 also applies to modification_audit)
- "Event Sourcing 2026 — A Field Guide" by kihiujohn.github.io
- arxiv:2511.21140 v4 (Lee et al., ICML 2026, confidence intervals for
  LLM-as-judge)
- OpenTelemetry GenAI semantic conventions
  (https://github.com/open-telemetry/semantic-conventions-genai)
- LangFuse data model
  (https://langfuse.com/docs/observability/data-model)
- AWS Step Functions execution history
  (https://docs.aws.amazon.com/step-functions/latest/dg/concepts-statemachines.html)

---

## §10 LUCIDEZ honest disclosure

### §10.1 SPEC-vs-reality drift estimate

Phase 9 alpha.20 historical +165% LoC drift. Phase 12 is ~1,800 LoC
estimate + ~35 tests + ~900 LoC docs. Realistic upper bound: 4,750
LoC code + ~55 tests. Operator-approved via alpha.20 disclosure
table.

### §10.2 Risk MED-HIGH justification

Not LOW (production schema change). Not MEDIUM (no production data
migration). MED-HIGH because:
- INV-20 is a STRUCTURAL invariant — once in, can't be weakened without
  breaking all callers.
- The 8 auto-emit sites touch core subsystems (decay, calibration,
  mark_superseded, embedder).
- The progress events touch async paths (pipeline_status background
  loop, delegate_intent spawn, subagent execution) — these are
  observability gaps that have caused real issues during Phase 11 testing.
- Single HMAC chain across both kinds means a regression in either
  breaks both.

### §10.3 Operator decisions (Q1-Q4) baked into this spec

Per the 2026-10-04 Phase 12 planning conversation (operator: Nico,
project: dark-memory), the 4 open questions from this spec's prior
version are now DECIDED:

1. **Q1 selectivo**: semantic-affecting modifications emit; pure cache
   invalidations (LRU eviction, TTL expiry) do NOT emit. → §3.3, §4.3
   T-103a (cache.go emit conditional on semantic effect).

2. **Q2 híbrido**: structural modifications are sync (block write,
   require verdict before commit); cosmetic modifications (confidence
   refresh, calibration_method flip, embedding refresh) are async
   (best-effort, no blocking). Progress events are always async
   (non-blocking). → §4.1 (`Emit` vs `EmitAsync`), §4.3 T-103b/c
   (progress always async).

3. **Q3 combo (a)+(c)**: rejected writes auto-emit a `MISSING_RATIONALE`
   sentinel BEFORE returning `ErrRationaleRequired`. Combo gives loud
   failure (no dashboard operator needs to triage silently) + forensic
   record (queryable in DB for post-incident analysis). → §4.2.

4. **Q4 ambas**: EventWriter wrapper (clean Go API) +
   DB-level invariant test in OD7 (s) (1:1 ratio verification of
   modification events vs canonical rows). DB-level triggers are
   deferred to Phase 13+ (SQLite trigger portability issues).
   → §4.1, §7 (s), §8 T-103a.

### §10.4 Honest scope note

This spec is 1,800 LoC code + 35 tests + 900 LoC docs = ~2,700 LoC
total. Phase 11 Camino E chose NOT to ship Phase 12 code — only the
original spec. Phase 12 SHIP happens in this alpha cycle (alpha.23),
with operator approval confirmed 2026-10-04 ("Apruebo").

The spec IS the contract for Phase 12 SHIP. Code execution begins with
T-101 (schema migration v32) after operator reads this revised spec.

---

## §11 Atomic mirror

After Phase 12 SHIP:
- 1 SUMMARY pinned (`kind=decision`, `agent_id=alpha-11-phase12`)
- 6+ SECTION pinned=false (T-101..T-106, with T-103 split into 3a/3b/3c)
- 1 row for the operator decisions (Q1-Q4) baked in (§10.3)
- 1 row for the SOTA grounding (OTel + LangFuse + Step Functions)

---

**Spec ends here.** Operator's next move: review this revised spec +
atomic mirror row 2415 (Phase 12 decisions), then authorize T-101
start. Phase 12 SHIP = alpha.23 tag.