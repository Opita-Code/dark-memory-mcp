# R-F — Decision-Context Retrieval Patterns (SOTA 2026)

**Loop**: L1 (research) → L1.6
**Author**: Opita-AI (MiniMax-M3), 2026-09-30
**Status**: ✅ Complete — 9 primary sources (MOOSEDev, MemLACE, TokenMizer, DPM, MemClaw, Engram, FARMA, StateAuditor, Why Git)
**Implications for**: C3 (decision) strategy; decision_transitions schema; supersession semantics; forgery threat

---

## 1. Research Question

**¿Cómo se recuperan decisiones en sistemas de memoria 2026? ¿Qué patrones de schema, supersession, y provenance son SOTA? ¿Cuáles son los attack surfaces?**

Sub-questions:
1. ¿Cuál es la estructura óptima para decision memory?
2. ¿Cómo se modela supersession (reemplazo de decisión)?
3. ¿Cuál es el rol del graph vs flat rows para decision retrieval?
4. ¿Hay amenazas específicas (forgery attacks)?
5. ¿Qué patrones de multi-agent/multi-tenant aplican?

---

## 2. Primary Sources (9 papers)

### Decision KG with supersession

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2608.13662** | **MOOSEDev** (NeSy 2026) | 2026-08 | **Ontology-grounded KG with lifecycle + provenance + supersession links** | **0.98-1.00 on supersession questions vs 6-27% baseline.** Code: github.com/Trivyn/moosedev. |
| **2609.03201** | **MemLACE** | 2026-09 | Sparse merge + supersession + contradiction relations | Atomic memories + provenance. 66.6% runtime reduction vs Hindsight. |

### Bitemporal + decision-transition records

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2606.06337** | **TokenMizer** | 2026-06 | 14 node types + 7 edge types + 8-state lifecycle + **bitemporal validity** + **decision-transition records** (trigger, reason, evidence) | 85% decision recall vs 70% baseline. Code: github.com/Shweta-Mishra-ai/tokenmizer. |
| **2606.09900** | **Engram** | 2026-06 | Bi-temporal data model + invalidating never deleting + **as-of filter** | **83.6% vs 73.2% full-context (+10.4pp), 8x token reduction.** |

### Regulated / enterprise

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2604.20158** | **DPM** (Stateless Decision Memory) | 2026-04 | Append-only event log + task-conditioned projection | For underwriting, claims, tax. 7-15x faster at binding budgets. 2 LLM calls vs 83-97. |

### Stale memory + audit

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2608.01619** | StateAuditor / STALE | 2026-08 | **Audit-from-state-to-draft pattern** | VTA 0.736 vs 0.686 (+5.0pp, CI [+2.9, +7.2]). Implicit policy adaptation gap. |

### Multi-agent / multi-tenant

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2606.24535** | **MemClaw** | 2026-06 | 4 primitives: scoped retrieval, temporal supersession, provenance, policy-governed propagation | 100% provenance reconstruction at depth-4. **Pipeline ordering conflict** warning. |
| **2607.05029** | FARMA / SENTINEL | 2026-07 | Forged reasoning attacks + structural defense | **100% ASR baseline; 0% with SENTINEL, no false positives.** |

### Why-rationale

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2607.14390** | Why Git Is the Memory Solution | 2026-07 | Git-bound memory + decision synthesis + why-arcs | 0.83 answer sufficiency on 50k-LOC prod. 382-980 tokens/question. |

---

## 3. Findings

### F1 — Decision retrieval is a recognized subfield in 2026 (9 papers in one search)

The pattern is now mature:
- KG + lifecycle + provenance + supersession.
- Decision-transition records (why + reason + evidence).
- Bitemporal validity for time-travel.
- Forgery detection as a threat.

### F2 — MOOSEDev: KG >> vector for decision retrieval (KEY FINDING)

- 0.98-1.00 on supersession, set-completeness, negation questions.
- Vector-memory baseline: 6-27% top-k retrieval.
- Same baseline relevance + token cost.
- **For dark-memory**: validates C3 graph weight (0.70 in R-A I-1). KG is THE differentiator for decision retrieval.

### F3 — MemLACE explicit supersession + contradiction

- Sparse merge, supersession, contradiction relations.
- Atomic memories + provenance.
- **66.6% runtime reduction** vs Hindsight (strongest reflective-memory baseline).
- BEAM + StructMemEval: highest overall in same-backbone comparisons.
- **For dark-memory**: schema must support supersession + contradiction. Phase 5 alpha.18 simplified version.

### F4 — TokenMizer bitemporal + decision-transition records (GOLD STANDARD)

- 14 node types + 7 edge types + 8-state lifecycle.
- **First-class decision-transition records** preserve why each decision replaced its predecessor:
  - trigger (what caused the transition).
  - reason (why).
  - evidence (links to supporting findings).
- **Bitemporal validity intervals** for time-travel queries.
- 85% decision recall vs 70% baseline.
- **For dark-memory**: alpha.18 ships simplified version (single temporal axis); alpha.19 full bitemporal.

### F5 — DPM for regulated domains (enterprise template)

- Deterministic Projection Memory: append-only event log + task-conditioned projection.
- 4 systems properties required:
  1. Deterministic replay.
  2. Auditable rationale.
  3. Multi-tenant isolation.
  4. Statelessness for horizontal scale.
- 7-15x faster at binding budgets.
- **For dark-memory**: alpha.19 candidate for enterprise mode (when operator needs regulated compliance).

### F6 — STALE: memory updates ≠ behavior updates (warning)

- Implicit Policy Adaptation (IPA) gap: agent knows state is outdated but plans around old value.
- StateAuditor's audit-from-state-to-draft pattern: +5.0pp (CI [+2.9, +7.2]).
- 95% CI confirms gain.
- **For dark-memory**: when a decision is superseded, downstream context must be audited. Decision supersession is not enough; the calling code/agent must re-evaluate.

### F7 — MemClaw: 4 failure modes + pipeline ordering conflict (KEY WARNING)

- Failure modes:
  1. Unauthorized leakage.
  2. Stale propagation.
  3. Contradiction persistence.
  4. Provenance collapse.
- 4 primitives: scoped retrieval, temporal supersession, provenance tracking, policy-governed propagation.
- **Pipeline ordering conflict**: synchronous near-duplicate gate can prematurely reject contradictory writes BEFORE the asynchronous contradiction detector evaluates them.
- **For dark-memory**: Phase 5 design must avoid this ordering bug. Write first (async contradiction detection), don't sync-gate contradictions.

### F8 — Engram bi-temporal + as-of filter

- Dual-process: fast write (no LLM on critical path) + async extract.
- Atomic (subject, predicate, object) facts + bi-temporal KG.
- Resolves contradictions WITHOUT LLM call per fact — **invalidating, never deleting**.
- Hybrid read: dense + lexical + graph + recency/salience + **as-of filter** (point-in-time).
- **83.6% vs 73.2% full-context (+10.4pp), 8x token reduction, 0/500 errored.**
- **For dark-memory**: validates bi-temporal model + as-of queries. Phase 5 alpha.18 should ship `as_of` query parameter.

### F9 — FARMA: reasoning history is an attack surface

- Forged Amplifying Rationale Memory Attack (FARMA): 100% ASR baseline.
- SENTINEL: 0% ASR with no false positives.
- 5 weighted signals for forgery detection.
- **For dark-memory**: Phase 5 spec must document the threat. Alpha.18 ships no built-in forgery detection (operator-managed). Alpha.19 integrates SENTINEL pattern.

### F10 — Why-arcs are the differentiator (Why Git principle)

- Decision synthesis reconstructs why-arcs no single session contains.
- 0.83 answer sufficiency on real production system (50k-LOC).
- 382-980 tokens per question (3 orders of magnitude below recorded history).
- **For dark-memory**: validates rationale preservation. C3 must surface decision WHY, not just decision WHAT. Schema: `rationale TEXT` column.

---

## 4. Implications for dark-memory Phase 5

### I-1 — C3 decision strategy: graph-only with rationale + supersession

```go
func (s *C3DecisionRecall) Recall(ctx, query, projectID, sessionID, topK) ([]Row, error) {
    // Stage 1: FTS5 with decision-aware query expansion
    candidates := s.ftsIndex.Search(query, topK*3)
    
    // Stage 2: Graph expansion (decision-kind graph, 2 hops for ADR/INV chains)
    candidates = s.graphExpand(candidates, 2, "decision")
    
    // Stage 3: RRF with weights (0.30 FTS5, 0.70 graph)
    scored := s.applyRRF(candidates, s.weights, topK*2)
    
    // Stage 4: Decision-transition filter (return current state, not superseded)
    return s.applyDecisionFilter(scored, topK), nil
}
```

### I-2 — Decision schema (MemLACE + TokenMizer inspired, simplified)

```sql
-- Decision rows: agent_memory.kind='decision' (already exists, alpha.17)
-- New columns:
ALTER TABLE agent_memory ADD COLUMN rationale TEXT;             -- optional, why this decision
ALTER TABLE agent_memory ADD COLUMN supersedes_id INTEGER;       -- optional, prior decision replaced
ALTER TABLE agent_memory ADD COLUMN decision_state TEXT DEFAULT 'active';  -- active | superseded | contested
ALTER TABLE agent_memory ADD COLUMN valid_from TEXT;            -- bitemporal (alpha.18: simple)
ALTER TABLE agent_memory ADD COLUMN valid_to TEXT;              -- bitemporal (alpha.18: simple)

-- New table: decision_transitions (TokenMizer pattern, simplified)
CREATE TABLE decision_transitions (
    transition_id INTEGER PRIMARY KEY AUTOINCREMENT,
    decision_id INTEGER NOT NULL,           -- the active decision
    superseded_id INTEGER,                  -- the prior decision being replaced
    trigger TEXT NOT NULL,                  -- what caused the transition
    reason TEXT NOT NULL,                   -- why
    evidence TEXT,                          -- JSON: links to supporting findings
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    project_id TEXT NOT NULL DEFAULT 'default',
    FOREIGN KEY (decision_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE,
    FOREIGN KEY (superseded_id) REFERENCES agent_memory(row_id) ON DELETE SET NULL
);
CREATE INDEX idx_dt_decision ON decision_transitions(decision_id);
CREATE INDEX idx_dt_superseded ON decision_transitions(superseded_id);
```

### I-3 — Supersession semantics (MoM + MemLACE)

- A `decision` row has state: `active | superseded | contested`.
- When `agent_memory.save()` with kind=decision, operator can optionally set `supersedes_id`.
- Phase 5 alpha.18: supersession is manual (operator-flag); auto-detect via audit-on-save is alpha.19.
- `superseded` decisions remain queryable for auditability but excluded from default `RecallFor(C3)`.

### I-4 — Decision-transition filter in Recall

`RecallFor(vibe_case=C3)` returns only `decision_state='active'` rows.
Transition history preserved in `decision_transitions` table.
Operator can query with `include_superseded=true` flag for audit views.

### I-5 — Pipeline ordering (MemClaw warning)

- DON'T use sync gate that rejects contradictory writes.
- Phase 5 design: write first (async contradiction detection). Detection runs every N minutes.
- Documents `decisions` are immutable once written; corrections happen via new decision rows + transitions.

### I-6 — Bitemporal validity (alpha.18: simple, alpha.19: full)

- Alpha.18: `valid_from` + `valid_to` columns (simple temporal).
- Alpha.19: full bitemporal (valid_time + transaction_time, per Engram).
- Query param: `as_of=<RFC3339>` returns rows valid at that time.

### I-7 — Forgery threat documented, deferred to alpha.19

- Phase 5 spec mentions FARMA + SENTINEL.
- Alpha.18 ships no built-in forgery detection (operator-managed).
- Alpha.19 integrates SENTINEL pattern (5 weighted signals).

### I-8 — Why-arcs surface in recall response

- `RecallFor(C3)` returns `rationale` field.
- Decision kind rows: `rationale` is required (NOT NULL after operator migration).
- Audit trail via `decision_transitions` table.

### I-9 — Per-strategy weights (validated by MOOSEDev)

C3 weights from R-A I-1 (validated by F2):
- FTS5: 0.30
- Graph: 0.70 (decision-kind graph)
- Vector: 0.00 (no text embedding of decisions)

This is the OPPOSITE of C2 text (0.40 FTS5, 0.50 vector, 0.10 graph). Decisions are graph-driven; text is graph-augmented.

### I-10 — C3 specific safeguards (MemClaw + STALE)

- **Scope**: project_id (already wired, alpha.17).
- **Audit-on-supersede**: when a decision is superseded, emit audit event with transition info.
- **Audit-from-state-to-draft**: alpha.19 hook for downstream context re-evaluation.
- **Pipeline ordering**: write first, async contradiction detection.

---

## 5. Open Questions

1. **Auto-detect supersession**: when an operator saves a NEW decision that contradicts an existing one, should auto-supersede fire? Decision: alpha.19; alpha.18 is manual.
2. **Decision-state transitions**: what triggers `contested`? Decision: alpha.18 = manual; alpha.19 = LLM-detected from contradictions.
3. **FARMA forgery detection at scale**: 5 signals may be too few. Decision: alpha.19 extends to 8-10 signals if SOTA evidence warrants.
4. **Why-arcs across sessions**: when operator saves a decision referencing a finding from another session, the rationale should link. Schema supports this via evidence JSON.
5. **DPM enterprise mode**: when is operator demand strong enough for alpha.19? Decision: track via operator feedback; not blocking.

---

## 6. Recommended Defaults for SPEC-alpha-11-phase5.md

| Item | Default | Justification |
|---|---|---|
| C3 strategy | Graph-only with rationale + supersession | F2: MOOSEDev 0.98-1.00 vs 6-27% |
| Schema | rationale + supersedes_id + decision_state + valid_from/to | I-2 |
| Decision transitions table | New table | F4: TokenMizer |
| Filter | decision_state='active' for default recall | I-4 |
| Pipeline | Write-first, async contradiction detection | F7: MemClaw warning |
| Bitemporal | Alpha.18 simple; alpha.19 full | I-6, F8: Engram |
| Forgery detection | Documented, alpha.19 | F9: FARMA |
| Rationale | Required for decision kind | F10, I-8 |
| Per-strategy weights | FTS5 0.30, Graph 0.70, Vector 0.00 | I-9 |

---

## 7. Cross-references

- **R-A §4 I-1**: C3 weights (0.30 FTS5, 0.70 graph) — validated here.
- **R-A §4 I-3**: C3 zero decay (decisions are forever) — validated by F4 TokenMizer (bitemporal validity preserves history).
- **R-C §4 I-1**: kind=decision → forever decay_class — validated here.
- **R-D §4 I-3**: graph role as candidate expander (Quanta) — applied to C3.
- **R-E §6**: ADR/INV refs indexed — directly used in C3 graph expansion.

---

## 8. Sources NOT fetched

- Mem0's decision memory specifics (only secondary mentions).
- Letta's decision rationale implementation (no arxiv paper).
- Engram GitHub code (referenced, not deep-read).

These are minor; 9 primary sources are sufficient.

---

## 9. L1 Status

**L1 (research) ✅ COMPLETE — 6/6 areas done.**

All 6 research artifacts produced:
- R-A: RRF + hybrid retrieval fusion.
- R-B: Cross-modal embeddings.
- R-C: Temporal decay.
- R-D: Multi-hop graph.
- R-E: Code retrieval.
- R-F: Decision retrieval.

**L1 → L2 transition**: ready to write `docs/specs/SPEC-alpha-11-phase5.md` synthesizing all 6 artifacts.

**Operator decision**: confirm L1 → L2 transition or redirect.
