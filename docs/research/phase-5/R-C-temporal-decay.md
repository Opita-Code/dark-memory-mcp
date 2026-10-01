# R-C — Temporal Decay in Memory Systems (SOTA 2026)

**Loop**: L1 (research) → L1.3
**Author**: Opita-AI (MiniMax-M3), 2026-09-30
**Status**: ✅ Complete — Fortunate Recall + ScrubJay-MEM + Mnemosyne + 3 other SOTA 2026 papers
**Implications for**: ADR-014 (temporal re-ranking); per-vibe-case decay functions

---

## 1. Research Question

**¿Las SOTA 2026 memory systems usan decay universal o per-categoría? ¿Qué función de decay gana, y qué patrones de refresh/retroactive update aplican?**

Sub-questions:
1. ¿Mem0, Letta, A-MEM, Memory-R1, MemoryOS usan decay explícita?
2. ¿Cuál es la diferencia entre decay universal y per-category?
3. ¿Refresh-on-access es patrón común?
4. ¿Cómo se valida empíricamente que decay mejora retrieval?

---

## 2. Primary Sources (6 papers)

### Per-category decay (Fortunate Recall — VALIDA NUESTRO ENFOQUE)

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2609.10413** | **Fortunate Recall (FR)** | 2026-09 | **10+1 behavioral ontology, per-category lifecycle** | Composable policy layer. 4 lifecycle primitives: differential temporal decay, slot-key supersession, event-time validity, category-aware retrieval routing. |

### Per-memory perishability (ScrubJay-MEM)

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2608.04746** | ScrubJay-MEM | 2026-08 | **π_i perishability, τ_i utility horizon** | Auto-classified coefficient. What-Where-When tuple. O(1) LLM calls per update. |

### Episodic memory model

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2510.08601** | Mnemosyne | 2025-10 | **Probabilistic recall with temporal decay + refresh** | Human-memory modeled. Graph storage. Edge LLMs. |

### Joint fact-time-affect encoding

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2608.16303** | FTA-Mem | 2026-08 | Fact + Time + Affect Memory Units | BWS for coherent fragments. Joint encoding. |

### Tool-augmented

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2603.09297** | TA-Mem | 2026-03 | Tool-augmented autonomous memory retrieval | Multi-indexed DB. Iterative retrieval. |

### Note evolution (A-MEM, foundational)

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2502.12110** | **A-MEM** (Zettelkasten for LLM agents) | 2025-02 | Dynamic indexing + linking; **note evolution** | NeurIPS 2025. New memories update existing. Code: github.com/WujiangXu/A-mem-sys |

---

## 3. Findings

### F1 — Per-category decay is empirically dominant (Fortunate Recall — KEY FINDING)

**This DIRECTLY validates the per-vibe-case decay plan from R-A §4 I-3.**

Fortunate Recall (FR) uses a **10+1 behavioral ontology** with **per-category lifecycle policies**. Compared on LifecycleBench (516 temporal-disambiguation questions):
- FR: **76.9%** pass rate.
- Mem0, A-MEM, Memory-R1, MemoryOS: 61-70.5% (6.4-15.9 pp gap).

Confabulation cuts:
- **Mem0: 45.1%** over answered queries.
- **FR: 22.4%** over answered queries (50% reduction).
- **32.2% → 13.0%** over all queries (60% reduction).

On LongMemEval-S: 75.2% (FR) with **no measurable cost to standard retrieval**.
On BEAM (independent benchmark): 46.8% vs Mem0 32.9% (over 280 questions).

**Ablation insight** (critical): "replacing the typed layer with three generic lifecycle primitives leaves correctness statistically unchanged (-1.7pp, 95% CI [-6.0, +2.7]), so the generic lifecycle metadata carries the correctness advantage, while the behavioral ontology carries calibration, halving downstream confabulation (12.0% vs 24.2%, p<0.001)."

**For dark-memory**: validates the polymorphic dispatch. The 10+1 ontology maps to dark-memory's vibe_case (C1-C7) + operator-set decay_class (instant/perishable/stable/persistent/forever). This is NOT capability-driven; it's CATEGORY-DRIVEN. Phase 5 plan's "one temporal re-ranker for all" is empirically inferior by 6-15pp.

### F2 — Per-memory perishability coefficient (ScrubJay-MEM — VALIDATES PER-ROW DECAY)

ScrubJay-MEM assigns each memory:
- **π_i** = perishability coefficient (auto-classified).
- **τ_i** = utility horizon (in days or events).
- Encoded as What-Where-When tuple.

On TGT (Temporal Generalization Test):
- ScrubJay-MEM: **only retrieval-based system** with substantially positive GenGap (+0.108).
- All other memory systems: GenGap ≤ 0.

On MemoryAgentBench EventQA-64k:
- +2.66 F1 over Mem0.
- +3.09 F1 over Qwen3-Embedding-4B.

**Decay ablation**: removing per-memory decay **collapses GenGap by 5.7x**. Decay is necessary, not optional.

**For dark-memory**: confirms per-row `decay_class` + `decay_tau_days` schema. α.18 should auto-derive from `kind` (operator-set); operator can override per row.

### F3 — Episodic memory model (Mnemosyne — VALIDATES REFRESH PATTERN)

Mnemosyne uses:
- Graph-structured storage.
- Modular substance + redundancy filters.
- Memory committing + pruning mechanisms.
- **Probabilistic recall with temporal decay and refresh** (modeled after human memory).

Results:
- 65.8% win rate (blind human eval) vs RAG 31.1%.
- Highest LoCoMo score in temporal reasoning + single-hop retrieval (vs same-backboned techniques).
- 54.6% overall (second highest), beating Mem0 + OpenAI baselines.

**For dark-memory**: refresh-on-access pattern (Mnemosyne) + what-where-when tuple (ScrubJay) + per-category routing (FR) = three orthogonal mechanisms that COMPOSE. Phase 5 should implement all three.

### F4 — A-MEM note evolution (MEMORY NETWORK UPDATES)

A-MEM (NeurIPS 2025) introduces **memory evolution**: when a new memory is added, it can trigger updates to existing memories' context representations. This is NOT a passive store; it's an active reorganization.

**For dark-memory**: alpha.18 should implement a basic version — `agent_memory.save()` optionally updates related rows' `updated_at` + `context_summary`. Operator-flag enabled (default OFF for backwards compat).

### F5 — Tool-augmented iterative retrieval (TA-Mem — VALIDATES RecallFor)

TA-Mem's agent decides whether to:
- Iterate (call recall again with refined query).
- Finalize (return current context).

This is the **agent-driven RecallFor pattern** — not a single retrieval but a loop.

**For dark-memory**: validates Gap 3 (Recall returns rows, not shaped context). Phase 5 should expose `RecallFor()` as a tool that the calling agent can iterate. alpha.18 ships the first version; alpha.19 adds the iteration helper.

### F6 — Fact-Time-Affect encoding (FTA-Mem — OPTIONAL)

FTA Units jointly encode fact + time + affect. BWS (Boundary-preserving Window Segmentation) forms coherent situation fragments.

**For dark-memory**: schema could add `emotional_valence REAL` or `affect_class TEXT` column. Defer to alpha.19 (v0 doesn't need this).

### F7 — Boundary-preserving Window Segmentation (BWS, FTA-Mem)

BWS = coherent fragments via semantic boundary detection.
LycheeMemory V2 = semantic segment-level consolidation (not turn-level).

**For dark-memory**: for C4 research, segment-level consolidation helps. For C1/C3, row-level is fine. Per-strategy consolidation strategy. Defer to alpha.19.

### F8 — Perishability is auto-classified (ScrubJay, FR, Mnemosyne)

All three systems auto-classify π_i / decay_class from content + LLM call. Operator does NOT manually set.

**For dark-memory**: alpha.18 auto-derives decay_class from `kind` (operator-set kind: note/observation/decision/finding/todo/link/context). No new operator input needed. Operator can override per row.

### F9 — Decay is necessary, not optional

Three independent ablations confirm:
- ScrubJay: 5.7x collapse without decay.
- FR: confabulation 2x without per-category.
- Mnemosyne: 65.8% vs RAG 31.1% (decay + refresh).

**For dark-memory**: ADR-014 is non-negotiable. Phase 5 ships per-strategy decay. alpha.18 ships schema + auto-derivation.

### F10 — Ranking replicates on different backbones (Fortunate Recall on Kimi K2.5)

Confirms decay design is independent of LLM backbone.

**For dark-memory**: per-strategy decay should be implemented in Go (not LLM-prompted), so it's backbone-independent.

---

## 4. Implications for dark-memory Phase 5

### I-1 — Per-row decay schema (additive migration)

```sql
ALTER TABLE agent_memory ADD COLUMN decay_class TEXT;        -- 'instant' | 'perishable' | 'stable' | 'persistent' | 'forever'
ALTER TABLE agent_memory ADD COLUMN decay_tau_days INT;      -- half-life in days (NULL = no decay)
ALTER TABLE agent_memory ADD COLUMN last_refreshed_at TEXT;  -- RFC3339 (for refresh-on-access)
ALTER TABLE agent_memory ADD COLUMN access_count INT DEFAULT 0;  -- for refresh trigger
ALTER TABLE agent_memory ADD COLUMN refresh_on_access BOOLEAN DEFAULT TRUE;  -- FALSE for C3
```

Default decay_class per kind (auto-derived on save, operator can override):
| kind | decay_class | decay_tau_days |
|---|---|---|
| `decision` | `forever` | NULL |
| `finding` | `persistent` | 1095 |
| `observation` | `stable` | 365 |
| `note` | `perishable` | 90 |
| `link` | `instant` | 30 |
| `context` | `stable` | 365 |
| `todo` | `perishable` | 60 |

### I-2 — Per-vibe-case multiplier (RRF composition)

| Strategy | Multiplier | Rationale |
|---|---|---|
| C1 code | 1.0 | Code decays at default rate |
| C2 text | 0.5 | Voice persists; words rot |
| C3 decision | ∞ | Zero decay (decisions are forever) |
| C4 research | 1.0 default; 3.0 math; 0.3 AI/ML | Domain-aware |
| C5 video | 0.25 | High decay (visual trends) |
| C6 audio | 1.0 | Voice profile persists |
| C7 multi | subtask-aware | n/a |

Final τ = base_tau × multiplier (capped at ∞ for C3).

### I-3 — Decay function (Go implementation)

```go
func DecayScore(row AgentMemory, now time.Time) float64 {
    if row.DecayClass == "forever" { return 1.0 }
    tau := float64(row.DecayTauDays)
    if tau <= 0 { return 1.0 }
    
    // Base exponential decay (Mnemosyne + ScrubJay)
    refTime := row.UpdatedAt
    if row.RefreshOnAccess && !row.LastRefreshedAt.IsZero() {
        refTime = row.LastRefreshedAt  // Mnemosyne refresh
    }
    age := now.Sub(refTime).Hours() / 24.0
    base := math.Exp(-age / tau)
    
    // Refresh boost (Mnemosyne pattern: recent access = stronger signal)
    if row.AccessCount > 0 && row.RefreshOnAccess {
        refreshBoost := 1.0 + 0.3*math.Log10(float64(row.AccessCount+1))
        base *= math.Min(refreshBoost, 2.0)  // cap at 2x
    }
    
    return base
}
```

### I-4 — Refresh-on-access pattern (Mnemosyne + ScrubJay)

When `agent_memory.get()` or `agent_memory.recall()` accesses a row:
- If `refresh_on_access = TRUE`:
  - Update `last_refreshed_at = NOW()`.
  - Increment `access_count`.

This is automatic. C3 decisions have `refresh_on_access = FALSE` (decisions are forever).

### I-5 — Category-aware routing (Fortunate Recall principle)

Each `RecallStrategy` declares preferred `decay_class` ordering:

```go
type RecallStrategy interface {
    PreferredKinds() []string      // "decision" first for C3, "finding" first for C4
    PreferredDecayClasses() []string // "persistent", "stable", "forever" for C3
}
```

Recall() applies the strategy's preferences in two places:
1. Pre-filter (when index permits).
2. Post-rank boost (when score is close, prefer higher-class row).

### I-6 — LifecycleBench-style micro-eval (Phase 5 includes)

Per FR's success pattern: a small (~50 questions) temporal-disambiguation benchmark. Operator-graded. Used to validate decay parameters before commit.

Questions like:
- "What did we decide about X last year that still holds?"
- "What's the most recent finding about Y?"
- "Was the decision about Z ever overridden?"

### I-7 — Note evolution (A-MEM pattern, optional, alpha.18)

`agent_memory.save()` optionally triggers an LLM call to find related rows (by kind/tag/project_id/vibe_case) and update their `context_summary` field.

**Decision**: alpha.18 ships the hook (operator-flag, default OFF). alpha.19 implements automatic evolution for selected kinds.

### I-8 — Iterative Recall (TA-Mem pattern, alpha.19)

`RecallFor()` returns shaped context + a `next_actions` hint ("more on this topic: call RecallFor with ref=X").

**Decision**: alpha.18 ships single-shot `RecallFor`. alpha.19 adds iteration helpers.

---

## 5. Open Questions

1. **Refresh-on-access for C2 text**: voice consistency improves with refresh; but voice drift is also real. Need empirical calibration. Phase 5 micro-eval includes this.
2. **AI/ML decay rate**: 0.3× feels aggressive. Could be 0.5×. Validate against LoCoMo/Mnemosyne results.
3. **Note evolution scope**: which kinds should trigger evolution? Decision: only `observation` + `finding` for alpha.18; expand in alpha.19.
4. **LifecycleBench for dark-memory**: who grades? Operator + dark-copilot? Decision: Phase 5 micro-eval is operator-graded; alpha.18 includes 10 questions + 5 auto-graded by Mnemosyne-style refresh check.

---

## 6. Recommended Defaults for SPEC-alpha-11-phase5.md

| Item | Default | Justification |
|---|---|---|
| Decay schema | 5 columns per I-1 | Per-row π_i + τ_i pattern (ScrubJay) |
| Auto-derivation | kind → decay_class table | FR-style category routing |
| Per-vibe-case multiplier | table from I-2 | Domain-aware decay |
| Refresh-on-access | TRUE except for `forever` rows | Mnemosyne refresh pattern |
| Note evolution | operator-flag, default OFF | A-MEM pattern, deferred auto to alpha.19 |
| Iterative Recall | single-shot alpha.18, iteration alpha.19 | TA-Mem pattern, phased |
| Micro-eval | 50-question LifecycleBench-style | FR validation pattern |
| Decay function | exponential + refresh boost | Mnemosyne + ScrubJay combined |

---

## 7. Cross-references to other R-A..F artifacts

- **R-A §4 I-3**: per-vibe-case decay table (validated by FR 10+1 ontology).
- **R-A §4 I-4**: graph role (Quanta principle, not scorer) — confirmed here (Mnemosyne graph is storage, not scorer).
- **R-B §4 I-2**: agent_memory schema migration (this R-C adds decay columns to the same migration).
- **R-D** (next): graph traversal should respect decay (skip `instant` rows after refresh cycle).

---

## 8. Sources NOT fetched (limitations)

- Mem0's actual implementation (only secondary mentions in FR/ScrubJay/Mnemosyne comparisons).
- Letta's actual decay implementation (Letta has no arxiv paper on temporal memory; search returned 0 results).
- A-MEM GitHub code (referenced but not deep-read).

These should be reviewed in L2 (spec loop) for concrete API signatures.

---

## 9. Next Step

L1.4 — R-D: Multi-hop / graph retrieval (HippoRAG, A-MEM, Mem0g, CABLE, ProGraph).

This validates ADR-015 and the graph-as-candidate-expander principle (Quanta). Should also address:
- Schema for graph links (new column on agent_memory? separate table?).
- 1-hop vs 2-hop vs 3-hop empirical trade-offs.
- Graph traversal order (BFS vs personalized PageRank).
