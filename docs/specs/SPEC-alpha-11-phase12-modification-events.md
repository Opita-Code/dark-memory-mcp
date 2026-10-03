# SPEC-alpha-11-phase12-modification-events — Phase 12 contract (Camino B)

**Status (2026-10-03)**: 📋 spec approved (Phase 11 T-403), awaiting Phase 12 SHIP.

**Branch**: `feat/v4-redesign` (local-only, no remote push).

**Predecessor**: `docs/specs/SPEC-alpha-11-phase11-camino-e.md` (operator decision
record for Phase 11 = Camino E, agent_memory row 2397).

**Cross-version lockstep hash pin** unchanged:
`4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`.

---

## §0 TL;DR + north star position

Phase 12 closes **primitiva #4 del §8** of the v4.0.0-GA north star:
**modification events**. Per `docs/sota-critique.md §7.4`:

> §8 fully shipped (modification events, LLM-as-judge for modifications,
> rationale-first, modification-replayability)

dark-memory v4-alpha.21 ships 4 of 5 §8 primitivas: capture (write_audit
chain), recall (vibe-case-aware), judge (14 personas + G-Eval), verify
(audit_verify HMAC chain). The **5th is modifications** — the system
auditing its own changes, not just the agent's.

Phase 12 deliverables (after SHIP):
- `modification_events` table (append-only, like write_audit but for
  system-side changes)
- `ModificationWriter` analog of `audit.Writer` (different wire shape)
- `eval_type=modification_audit` + new persona `judge-modifications`
- `dark_memory_modification_replay` MCP tool (reconstruct state from
  event log)
- INV-20 rationale-first invariant
- ~1,500 LoC code + ~30 tests
- Risk MED-HIGH (changes the fundamental data model)
- 4-6 weeks focused

**North star impact**: alpha.21 ~85% → alpha.23 ~95% (closes primitiva
#4 of 5 of §8).

---

## §1 Problem statement

### §1.1 The gap

dark-memory v4-alpha.21 records **agent-side actions** (write_audit).
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

### §1.2 The §8 primitivas from the north star

From `docs/sota-critique.md §7.4`:

> §8 fully shipped (modification events, LLM-as-judge for modifications,
> rationale-first, modification-replayability)

§8 is the **5 primitivas** that dark-memory needs to be a complete
SOTA 2025-26 dark-memory:

1. **Capture** (write_audit) — ✅ shipped alpha.15
2. **Recall** (vibe-case-aware memory) — ✅ shipped alpha.18
3. **Judge** (LLM-as-judge + 14 personas + G-Eval) — ✅ shipped alpha.16 + alpha.19
4. **Modify** (this Phase 12) — ❌ pending
5. **Verify** (audit chain HMAC + verify) — ✅ shipped alpha.20

Phase 12 = primitiva #4 (modify). The 4 sub-deliverables per §7.4:
- modification events (the table + writer)
- LLM-as-judge for modifications (the eval_type + persona)
- rationale-first (INV-20)
- modification-replayability (the replay tool)

### §1.3 Why this matters

**Without modification events**:
- Drift detection catches drift. But it doesn't catch **silent
  modifications** — the system changing state without going through
  the judge pipeline.
- Audit chain tells you WHAT was written. It doesn't tell you WHY
  the system decided to write that.
- Replayability requires event sourcing — without modification events,
  we can't reconstruct state at time T.

**With modification events**:
- The full §8 5-primitive loop closes: capture → recall → judge →
  **modify** → verify.
- dark-memory becomes auditable by LLM-as-judge for its own behavior.
- The system can REPLAY modifications to reconstruct intent, not just
  state.

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

---

## §3 Data model

### §3.1 Schema migration v32 (additive, NULLABLE)

New table `modification_events` + 1 new column on `agent_memory`:

```sql
-- additive, idempotent, NULLABLE
CREATE TABLE IF NOT EXISTS modification_events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    ts              TEXT NOT NULL,                          -- RFC3339
    classification  TEXT NOT NULL,                          -- see §3.3 taxonomy
    source          TEXT NOT NULL,                          -- see §3.4 (which subsystem made the change)
    target_table    TEXT NOT NULL,                          -- which table was modified
    target_row_id   INTEGER NOT NULL,                       -- the row that was changed
    operation       TEXT NOT NULL,                          -- 'insert'|'update'|'delete'|'supersede'
    rationale       TEXT NOT NULL,                          -- INV-20: WHY the change was made
    rationale_kind  TEXT NOT NULL,                          -- see §3.5 (decay|judge|supersede|migration|...)
    payload_before  TEXT,                                   -- JSON snapshot of row before (NULL for insert)
    payload_after   TEXT NOT NULL,                          -- JSON snapshot of row after (NULL for delete)
    confidence      REAL,                                   -- LLM judge confidence (0..1, NULL if not judged)
    judge_verdict  TEXT,                                    -- 'aligned'|'drift_detected'|'needs_human'
    judge_reasoning TEXT,                                   -- LLM judge reasoning text
    judge_run_id    INTEGER,                                -- references sdd_evaluations.id (NULL if not judged)
    project_id      TEXT NOT NULL DEFAULT 'default',        -- INV-19
    actor           TEXT NOT NULL DEFAULT 'system',         -- 'system' | subagent_id | 'operator:<name>'
    chain_prev      TEXT,                                   -- HMAC chain (per §3.6)
    chain_self      TEXT NOT NULL,
    chain_key_id    TEXT NOT NULL
);

CREATE INDEX idx_modification_events_project_ts ON modification_events(project_id, ts);
CREATE INDEX idx_modification_events_target ON modification_events(target_table, target_row_id);
CREATE INDEX idx_modification_events_classification ON modification_events(classification);
CREATE INDEX idx_modification_events_source ON modification_events(source);

-- new column on agent_memory
ALTER TABLE agent_memory ADD COLUMN last_modified_by_event_id INTEGER;
```

### §3.2 Relationships to existing tables

- `modification_events.target_row_id` references rows in any of the
  existing tables (agent_memory, write_audit, sdd_evaluations,
  vibe_specs, vibe_artifacts, etc.). Polymorphic FK; enforced at
  application level (SQLite doesn't enforce cross-table FKs).
- `modification_events.judge_run_id` references `sdd_evaluations.id`
  (for cases where an LLM judge was applied to the modification).
- `agent_memory.last_modified_by_event_id` references
  `modification_events.id` (for query convenience; the canonical
  source is the modification_events table itself).

### §3.3 Classification taxonomy

The `classification` field uses the same enum pattern as
`sdd_evaluations.classification`. Initial values:

- `decay` — auto-decay score change (Phase 5 DecayScore)
- `judge` — LLM judge verdict update (Phase 7 Chunk 7.5)
- `supersede` — auto-superseded decision (Phase 7 Chunk 7.7)
- `migrate` — schema migration
- `embedder` — entity graph refresh (Phase 5 Chunk 5.5)
- `calibration` — judge calibration update (Phase 7 alpha.16 ADR-011)
- `cache` — recall cache invalidate (Phase 7 alpha.17)
- `system` — generic system modification not matching above

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

### §3.5 Rationale kind taxonomy

The `rationale_kind` field distinguishes WHY the change was made:
- `decay_function` — applied per Phase 5 decay formula
- `judge_verdict` — applied per LLM-as-judge verdict
- `bitemporal_transition` — supersession per Phase 7 bitemporal
- `schema_change` — explicit ALTER TABLE
- `entity_graph_update` — ProGraph BFS found new edges
- `cache_ttl_expired` — recall cache invalidation
- `unknown` — generic catch-all

### §3.6 Chain (HMAC)

The `chain_prev` / `chain_self` / `chain_key_id` triple follows the
same pattern as `write_audit` HMAC chain (Phase 9 Chunk 8.5). The
chain includes `DARK_AUDIT_HMAC_KEY` (multi-key rotation per
`docs/audit-hmac-rotation.md`).

The chain is computed at emit time, not stored. Verification happens
via a new MCP tool `dark_memory_modification_verify` (T-105).

---

## §4 Writer

### §4.1 `ModificationWriter` (analog of `audit.Writer`)

New type in `internal/v4alpha/modification/writer.go`:

```go
type Writer struct {
    db     *sql.DB
    keyring *audit.Keyring
    now    func() time.Time  // injectable for tests
}

type Event struct {
    ID              int64
    TS              time.Time
    Classification  string
    Source          string
    TargetTable     string
    TargetRowID     int64
    Operation       string  // 'insert'|'update'|'delete'|'supersede'
    Rationale       string  // INV-20: NEVER empty
    RationaleKind   string
    PayloadBefore   []byte  // nil for insert
    PayloadAfter    []byte  // nil for delete
    ProjectID       string
    Actor           string
}

func (w *Writer) Write(ctx context.Context, ev Event) (int64, error) {
    if ev.Rationale == "" {
        return 0, ErrRationaleRequired  // INV-20 enforcement
    }
    // chain compute + insert in single Tx (INV-1 atomic)
}
```

### §4.2 INV-20 rationale-first invariant

**Every modification must have a rationale**. The `Write` function
returns `ErrRationaleRequired` when `Rationale == ""`. The caller MUST
provide one. This forces the system to articulate WHY before changing
state — a true "rationale-first" primitive per §7.4.

### §4.3 Auto-emission hooks

The 8 system-side modifications listed in §1.1 must emit
modification events automatically. This is done via wrappers around
the existing write paths:

- `internal/recall/decay.go:RefreshOnAccess` → wraps with
  `ModificationWriter.Write({source: "decay.go", classification:
  "decay", ...})`
- `internal/judge/pipeline.go:UpdateConfidence` → wraps with
  `({source: "judge/pipeline.go", classification: "judge", ...})`
- ...etc.

Each wrapper adds ~10-20 LoC. Total auto-emission wiring: ~120 LoC.

---

## §5 LLM-as-judge for modifications

### §5.1 New eval_type: `modification_audit`

Following the `eval_type` enum pattern in `internal/v4alpha/judge/`:

- `eval_type=modification_audit` — evaluates a modification event
  against the original spec/intent that motivated the change.

The judge compares:
- The modification's `rationale` against the original spec intent.
- The `payload_after` against the expected change given the
  rationale.

Returns: `aligned` (modification was justified + correct),
`drift_detected` (rationale doesn't match the change), `needs_human`
(judge uncertain).

### §5.2 New persona: `judge-modifications`

Per `internal/orchestration/judge_personas_types.go`:
- `persona_id="judge-modifications"`
- `source=PersonaSourceV4Alpha`
- `lens="modification alignment"`
- `constraints=["must verify rationale coherence", "must flag silent
  intent drift"]`
- `rubric="modification_audit_v1"`

### §5.3 Statistical self-bias detection (per ADR-011)

The `judge-modifications` persona is registered with the project-scoped
calibration chain (`internal/v4alpha/judge/calibration.go`). When
`n_evaluations >= ShouldRecalibrate threshold`, the calibration
columns update per the alpha.16 bootstrap-CI methodology.

### §5.4 Confidence intervals per arxiv:2511.21140

Following the ICML 2026 paper "How to Correctly Report LLM-as-a-Judge
Evaluations" (Lee et al., arxiv:2511.21140 v4):

- Each modification_audit evaluation reports `confidence_calibrated`
  with `calibration_ci_low` + `calibration_ci_high` (95% CI).
- Bias correction for judge sensitivity/specificity.
- Adaptive sample allocation (more samples for borderline cases).

The calibration columns already exist on `sdd_evaluations` (alpha.16).
`modification_audit` reuses them.

---

## §6 Replay tool

### §6.1 `dark_memory_modification_replay` MCP tool

New MCP tool that reconstructs the state of a target row at time T
by walking the modification_events log:

```
dark_memory_modification_replay(
  target_table: string,
  target_row_id: int,
  as_of: RFC3339,        // optional, default = NOW
  include_rationales: bool // default true
) -> {
  current_state: JSON,
  history: [
    {ts, source, classification, operation, rationale, payload_before, payload_after},
    ...
  ],
  as_of: RFC3339
}
```

### §6.2 Algorithm

For a given target row:
1. Query all modification_events WHERE target_table=X AND
   target_row_id=Y AND ts <= as_of ORDER BY ts ASC.
2. Starting from `payload_after` of the FIRST event (or NULL if
   operation='insert'), apply each subsequent `payload_before` or
   `payload_after` in order to reconstruct state.
3. Return the reconstructed state + the history array (with
   rationales).

This is the classic event-sourcing fold (per the "Event Sourcing 2026
Field Guide" by kihiujohn.github.io).

### §6.3 Snapshot optimization

For rows with >1000 modifications, fold-from-beginning is slow. Per
the SOTA event-sourcing pattern:

- Periodic snapshot of `vibe_artifacts` (already exists for some
  tables; can be extended).
- Replay tool loads the nearest snapshot + folds only modifications
  after the snapshot's `as_of`.

Phase 12 ships the fold-only mode. Snapshot support is Phase 13+.

---

## §7 OD7 e2e gate for Phase 12 SHIP

Before tagging `v4.0.0-alpha.23`:

- (a) schema migration v32 idempotent (run twice, verify zero diff)
- (b) ModificationWriter.Write with empty rationale → ErrRationaleRequired (INV-20)
- (c) ModificationWriter.Write with valid rationale → row + chain data emitted
- (d) modification_audit judge returns aligned/drift_detected/needs_human (all 3 reachable)
- (e) judge-modifications persona registered (source=v4alpha)
- (f) modification_verify with HMAC chain → Status=OK
- (g) modification_replay reconstructs state correctly for a test fixture (insert→update→update→supersede)
- (h) calibration_auto: judge-modifications column + ci_low/ci_high after ≥50 evals
- (i) INV-20 enforcement: zero modifications with empty rationale in write_audit dump
- (j) cross-table auto-emission: each of the 8 system-side paths emits at least one modification event in a 60s soak test

**0 critical findings required.**

---

## §8 Implementation chunks (T-101..T-106)

### T-101 Schema migration v32 (~200 LoC + tests)

- `internal/store/sqlite/migrations/032_modification_events.go` NEW
- `internal/store/sqlite/modification_events.go` NEW (Store-first
  pattern, mirroring `internal/store/sqlite/bitemporal.go`)
- 5 unit tests + 2 migration tests (idempotency + rollback if
  recoverable)

### T-102 ModificationWriter + INV-20 (~400 LoC + tests)

- `internal/v4alpha/modification/writer.go` NEW
- `internal/v4alpha/modification/types.go` NEW (Event struct +
  enums + sentinel errors)
- `internal/v4alpha/modification/writer_test.go` NEW (10 tests:
  rationale required, atomic insert + chain, error paths, multi-key
  rotation)

### T-103 Auto-emission wiring (~200 LoC + tests)

- Wrap 8 system-side write sites:
  - `internal/recall/decay.go:RefreshOnAccess` (Phase 5)
  - `internal/judge/pipeline.go:UpdateConfidence` (alpha.16)
  - `internal/store/sqlite/bitemporal.go:MarkSupersededAgentMemory` (Phase 7)
  - `internal/recall/entity.go:RefreshEntityGraph` (Phase 5)
  - `internal/judge/calibration.go:populateCalibration` (alpha.16)
  - `internal/recall/cache.go:InvalidateCache` (Phase 7)
  - `internal/judge/pipeline.go:UpdateVerdict` (when judge re-runs)
  - `internal/embedder/integration.go:RefreshEmbedding` (alpha.20)
- Each wrap is 10-20 LoC.
- `internal/v4alpha/modification/auto_emit_test.go` NEW (1 test per
  emit site, 8 tests total)

### T-104 judge-modifications persona + eval_type (~300 LoC + tests)

- `internal/orchestration/judge_personas_v4alpha.go` MODIFIED (+1
  entry)
- `internal/v4alpha/judge/pipeline.go` MODIFIED (+new eval_type case)
- `internal/v4alpha/judge/modification_audit.go` NEW (eval logic)
- `internal/v4alpha/judge/modification_audit_test.go` NEW (8 tests:
  aligned/drift/needs_human paths, rationale coherence, payload
  consistency)

### T-105 modification_replay + modification_verify (~200 LoC + tests)

- `internal/v4alpha/modification/replay.go` NEW (fold algorithm)
- `internal/v4alpha/modification/verify.go` NEW (HMAC chain
  verification, mirrors `internal/audit/verify.go`)
- `internal/tools/modification.go` NEW (RegisterModification +
  3 handlers: replay, verify, list)
- 5 tests (fold correctness, chain verify, error paths)

### T-106 LUCIDEZ + docs sweep (~150 LoC)

- `docs/v4-status.md` + `docs/v4-alpha-11-plan.md` update (Phase 12
  changelog)
- `docs/sota-critique.md` §5.2.5/7 addition (modification patterns
  documented)
- `CHANGELOG.md [4.0.0-alpha.23]`
- `docs/modification-events-architecture.md` NEW (canonical architecture
  doc, ~400 LoC)

**Total**: ~1,450 LoC code + ~30 tests + ~800 LoC docs.

---

## §9 Cross-refs

- `docs/sota-critique.md §7.4` — north star (modifications is primitiva #4)
- `docs/specs/SPEC-alpha-11-phase11-camino-e.md` — Phase 11 spec
  (this contract)
- `docs/specs/SPEC-alpha-11-phase5.md` — Phase 5 recall subsystem
  (modifications touch decay.go)
- `docs/specs/SPEC-alpha-11-phase7.md` — Phase 7 subagent wiring
  (modifications touch mark_superseded)
- `internal/audit/writer.go` — INV-1 audit writer (modification
  writer analog)
- `internal/audit/hmac.go` — HMAC chain design (Phase 9 Chunk 8.5)
- `internal/audit/verify.go` — chain verify design (analog for
  modification_verify)
- `internal/judge/pipeline.go` — eval_type enum (modification_audit
  joins)
- `internal/orchestration/judge_personas_v4alpha.go` — persona
  registry (judge-modifications joins)
- `internal/recall/decay.go` — auto-emit hook T-103
- `docs/audit-hmac-rotation.md` — chain key policy (modifications use
  the same DARK_AUDIT_HMAC_KEY)
- `docs/MANIFIESTO.md` — v4 design philosophy (rationale-first is
  consistent)
- atomic mirror row 2397 (Phase 11 = Camino E operator decision)
- Phase 9 alpha.20 SOTA critique §5.2.5 (override patterns — pattern
  #5 also applies to modification_audit)
- "Event Sourcing 2026 — A Field Guide" by kihiujohn.github.io
- arxiv:2511.21140 v4 (Lee et al., ICML 2026, confidence intervals for
  LLM-as-judge)

---

## §10 LUCIDEZ honest disclosure

### §10.1 SPEC-vs-reality drift estimate

Phase 9 alpha.20 historical +165% LoC drift. Phase 12 is ~1,450 LoC
estimate + ~30 tests + ~800 LoC docs. Realistic upper bound: 3,800
LoC code + ~50 tests. Operator-approved via alpha.20 disclosure
table.

### §10.2 Risk MED-HHS justification

Not LOW (production schema change). Not MEDIUM (no production data
migration). MED-HIGH because:
- INV-20 is a STRUCTURAL invariant — once in, can't be weakened without
  breaking all callers.
- The 8 auto-emit sites touch core subsystems (decay, calibration,
  bitemporal). Bugs here cascade.

### §10.3 Open questions BEFORE coding

Per the alpha.17 BUG-10 10b 4-doc plan precedent, Phase 12 SHOULD
NOT start coding before operator approval on:

1. **What is "silent modification"?** Some state changes are
   unambiguously internal (e.g., cache invalidation). Should those
   emit modification events? Or are they noise?
2. **Should modification_audit run sync or async?** Sync blocks the
   modification write; async risks missing the verdict for a write
   that's already committed.
3. **INV-20 violation response?** If a code path forgets to provide
   a rationale and we add INV-20 enforcement after, should we (a)
   reject the modification, (b) auto-generate a rationale, (c) log
   a sentinel "MISSING_RATIONALE" event for triage?
4. **Cross-table consistency?** modification_events are append-only, but
   if the canonical source (e.g., agent_memory row) is updated
   without emitting a modification event, the audit chain breaks.
   Enforcement strategy: trigger? Wrapper only? Both?

These 4 questions are pre-coding. Phase 12 Chunk 12.1 = "spec answers
to these 4 questions + operator approval" (per the alpha.17 BUG-10 10b
precedent).

### §10.4 Honest scope note

This spec is 1,450 LoC code + 30 tests + 800 LoC docs = ~2,300 LoC
total. **Phase 11 Camino E chose NOT to ship Phase 12 code** — only
this spec. Phase 12 SHIP happens in a future alpha cycle.

The spec IS the deliverable. Phase 12 SHIP is a separate workstream.

---

## §11 Atomic mirror

After Phase 12 SHIP:
- 1 SUMMARY pinned (`kind=decision`, `agent_id=alpha-11-phase12`)
- 6 SECTION pinned=false (T-101..T-106)
- 1 row for the open questions in §10.3 (operator decision row)

---

**Spec ends here.** Operator's next move when Phase 12 starts: read
this doc + atomic mirror row 2397, answer the 4 questions in §10.3,
then execute T-101..T-106 per §8.