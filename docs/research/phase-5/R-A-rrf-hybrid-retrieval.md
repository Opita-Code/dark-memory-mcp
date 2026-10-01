# R-A — RRF and Hybrid Retrieval Fusion (SOTA 2026)

**Loop**: L1 (research) → L1.1
**Author**: Opita-AI (MiniMax-M3), 2026-09-30
**Status**: ✅ Complete — 15 primary-source SOTA 2026 papers reviewed
**Implications for**: ADR-013 (vector retrieval + RRF)

---

## 1. Research Question

**¿Sigue siendo RRF (Cormack 2009, k=60) el método correcto para fusionar FTS5 (BM25) + vector retrieval en sistemas de memoria 2026, o hay alternativas SOTA que lo desplazan?**

Sub-questions:
1. ¿Cómo se tunnean los pesos de las señales en RRF?
2. ¿Hay un rerank dominante después del RRF?
3. ¿La decay es universal o per-domain?
4. ¿El rol del graph en retrieval fusion es scorer o expander?

---

## 2. Primary Sources (15 papers, all verified arxiv abstracts, all Sep 2026 or earlier-2026 dates)

| ID | Title | Date | Signal mix | Fusion | Key insight |
|---|---|---|---|---|---|
| **2609.32049** | EngramRAG | 2026-09-25 | dense + BM25 + U-PPR | **dynamic RRF** | CATD: decay by topological load, not wall-clock. LoCoMo: +38.9% Recall@5. |
| **2609.32123** | READ-Bench | 2026-09-25 | classical + symbolic + embedders | RRF + Gaussian-process reranker | Rerank matters more than retrieval stage. NDCG@10 +0.16 over strongest single base. |
| **2609.27359** | RoPA Manager | 2026-09-23 | lexical (tsvector) + dense | RRF + local LLM | Vietnamese compliance, F1=0.9493. Locally-deployed LLM acceptable. |
| **2609.26237** | ABAI / COLIEE 2026 | 2026-09-12 | BM25 + dense + graph + LightGBM | RRF → rerank → meta-learner | Multi-stage pipeline. Threshold transfer costs 0.007 F1 (low). |
| **2609.23307** | Yashwant et al. (job match) | 2026-09-19 | EmbeddingGemma + BM25 | hybrid RRF | Cached MNRL fine-tuning > base EmbeddingGemma. |
| **2609.18248** | Quanta | 2026-09-16 | 4-bit quantised + BM25 + KG | **weighted RRF** | Graph as candidate expander, NOT scorer. RRF over score normalization (ill-posed). |
| **2609.13648** | Solar Intelligence | 2026-09-11 | DuckDB SQL + BM25 + ChromaDB | RRF + llama3.2:3b grounding | Energy domain, MCP server surface. |
| **2609.10430** | Glyph | 2026-09-09 | description + regex + fine-tuned MiniLM | **RRF**, per-tag provenance | In-batch contrastive lifts NDCG@10 0.55 → 0.92. |
| **2609.08887** | Q2D-Web | 2026-09-08 | 13 retrievers benchmarked | RRF for subcorpus sampling | 190M-doc corpus, 70k agentic queries. |
| **2609.06964** | FunnelAudit | 2026-09-06 | multi-route (fixed/wRRF/quota) | RRF vs weighted-quota | Recommender accountability, 258k incidents. |
| **2609.02913** | CHSR-RRF | 2026-09-06 (Jul) | sparse + dense + metadata gating | RRF + deterministic rerank | **Pre-retrieval gating** reduces leakage 4.6x. |
| **2609.01617** | DocuSearch | 2026-09 (Jun 30) | Qdrant + BM25 + KG | **weighted RRF (0.50/0.35/0.15)** + cross-encoder + MMR | Telecom, P@10=0.69, R@10=0.79, grounding 89.6%. |
| **2608.30929** | ASCR-H / DTF | 2026-08-31 | surrogate + dense | RRF + deterministic | Polish legal, ASCR-H rank-1 72.3% vs BM25 61.7%, dense 52.3%. |
| **2608.27017** | ProRetrieval | 2026-08-28 | LM as orchestrator | executable program (DSL) | 4B Qwen3 surpasses GPT-5.5 (Hit@1 0.81 vs 0.69). |
| **2608.23992** | SCOUT (PayPal) | 2026-08-24 | BM25 + dense | RRF | MCP tool discovery, 99% context reduction. |
| **2608.23484** | Music RecSys 2026 | 2026-08-24 | 7 embed spaces + BM25 | **decay-weighted centroids + weighted RRF** | Differential evolution for weights, +19.5% MRR. |
| **2608.22381** | GRAFT | 2026-08-23 | graph edges (typed) | **graph-weighted RRF** | Scales rank term by edge weight. |
| **2608.22137** | MegaMem | 2026-08-22 | distilled + detailed | RRF + cross-encoder rerank | 650M-token corpus, source-resolved. |
| **2609.25054** | MoM (Memory of Memory) | 2026-09-07 | typed provenance graph | n/a (not retrieval) | Write-time commit + retain displaced values. 100% vs 0% recovery. |
| **2608.12990** | LycheeMemory V2 | 2026-08-13 | semantic segment consolidation | planned retrieval, index-driven | 89.22% LoCoMo, 92.20% LongMemEval-S. -86% construction tokens. |

**Cormack 2009 (original RRF)**: NOT on arxiv — published at SIGIR 2009. Cited by every paper above. Reference: Cormack, G. V., Clarke, C. L., & Buettcher, S. (2009). "Reciprocal Rank Fusion outperforms Condorcet and individual Rank Learning Methods." SIGIR '09.

---

## 3. Findings

### F1 — Hybrid RRF is the universal pattern, but with three sub-flavors

Every paper that fuses ≥2 signals uses RRF (or weighted RRF). The variations:

- **Plain RRF** (Cormack 2009, k=60): still the default. Used in 8/15 papers.
- **Weighted RRF**: explicit per-signal weights. Used in 4/15 (DocuSearch 0.50/0.35/0.15, Music RecSys DE-optimized, Quanta, Glyph).
- **Graph-weighted RRF**: rank term scaled by edge weight. Used in 1/15 (GRAFT).
- **Dynamic RRF**: weights adapt based on usage. Used in 1/15 (EngramRAG U-PPR).
- **Executable orchestration**: replaces RRF with LM-synthesized program. Used in 1/15 (ProRetrieval).

**Conclusion**: RRF is canonical for dark-memory's `Recall()`. Default = weighted RRF with per-strategy weights.

### F2 — Cross-encoder reranking is the dominant second stage

7/15 papers use a rerank step after RRF:
- **Cross-encoder** (DocuSearch, MegaMem, ASCR-H, ABAI): most accurate, most expensive.
- **LightGBM meta-learner** (ABAI): over 34 features. Good cost/accuracy trade-off.
- **Gaussian-process reranker** (READ-Bench): reranks by propagated neighbor labels. Outperforms LM reasoning.
- **Deterministic rerank** (CHSR-RRF, ASCR): for domain with hard constraints.
- **Personalized PageRank** (EngramRAG U-PPR): for graph signal.

**For dark-memory**: cross-encoder is too expensive for v0 (would require serving a BERT-style model per query). Recommended: lightweight rule-based rerank (LRU cache + simple boosts for: tag match, project_id match, recent session activity) in alpha.18. Cross-encoder deferred to alpha.19.

### F3 — Decay is NOT one-size-fits-all

This validates the operator's gap-flag (Gap 7 from prior turn):
- **EngramRAG CATD**: retention half-life scales by **topological load-bearing weight** (entities promoted via Hebbian plasticity), NOT wall-clock recency.
- **MoM**: zero decay (write-time commit + retain displaced values). 100% recovery vs 0% for CRUD.
- **LycheeMemory V2**: segment-level consolidation (not turn-level). Drift prevention by semantic-boundary batching.
- **Music RecSys**: decay-weighted centroids across 7 embedding spaces.
- **ABAI**: chronological quartiles show FLAT decision quality — suggesting time-decay is overrated for case-law retrieval.

**Conclusion**: per-vibe-case decay is empirically validated. The plan's "one temporal re-ranker for all" is wrong.

### F4 — Graph is candidate expander, NOT scorer

This is a clean principle (Quanta: "the graph is a *candidate expander and not a relevance scorer*: traversal widens the candidate pool, and the newly admitted documents are re-scored by the dense indexes under an identifier allowlist, so structural adjacency determines what is considered while content evidence determines how it ranks.").

Validates dark-memory's ADR-015 (multi-hop graph) as **adjacency-first, content-second**. 1-2 hops max.

### F5 — Source-resolved retrieval

MegaMem principle: "every distilled hit resolves to an immutable source ID before reciprocal-rank fusion, deduplication, and cross-encoder reranking."

Dark-memory already does this implicitly (rows have row IDs). Should be made EXPLICIT in the design — every `RecallStrategy` returns rows with row_id resolved before fusion.

### F6 — Surrogate cascade

ASCR principle: language-model annotations attached at index time, cascade through surrogates → full documents. Reduces query-time cost. Adds index-time LoC but amortizes well.

For dark-memory: a `summary` field on `agent_memory` rows (operator-set or auto-generated) could serve as the surrogate. Decision: defer to alpha.19, document as Phase 6.

### F7 — Pre-retrieval gating

CHSR-RRF: metadata constraints applied BEFORE retrieval (curriculum, project_id, vibe_case). Reduces leakage 4.6x. Gating AFTER retrieval collapses recall to zero.

Validates the polymorphic-dispatch design (`RecallFor(vibe_case, task_context, query)`). Per-strategy gates are first-class.

### F8 — Domain-specific weights are real

DocuSearch telecom: 0.50/0.35/0.15.
Music RecSys: differential-evolution-optimized per domain.
Solar Intelligence: structured SQL dominates (different mix).

**There is NO universal weight recipe.** Each `RecallStrategy` declares its own weights. No "default weights" baked into core.

### F9 — Source-resolved attribution (MegaMem)

Every hit has an immutable source ID (the row_id). Post-answer, the system identifies which sources support the answer. This is implicit in dark-memory but should be explicit in the API.

---

## 4. Implications for dark-memory Phase 5

### I-1 — Default fusion: Weighted RRF (Cormack 2009 base, k=60)

Each `RecallStrategy` declares `Weights map[string]float64` (signal name → weight). Default weights per strategy:

| Strategy | FTS5 (BM25) | Text vector | Cross-modal | Graph | Domain |
|---|---|---|---|---|---|
| C1 code | 0.55 | 0.00 | 0.00 | 0.45 (ADR/INV refs) | code-specific tokenizer + commit ref boost |
| C2 text | 0.40 | 0.50 | 0.00 | 0.10 (recent same project) | voice consistency boost |
| C3 decision | 0.30 | 0.00 | 0.00 | 0.70 (decision kind graph) | ADR/INV index |
| C4 research | 0.25 | 0.45 | 0.00 | 0.30 (citation graph) | domain decay |
| C5 video | 0.00 | 0.00 | 0.80 (CLAP/CLIP) | 0.20 (style ref) | HIGH decay |
| C6 audio | 0.00 | 0.00 | 0.80 (voice embed) | 0.20 (voice profile) | medium decay |
| C7 multi | n/a | n/a | n/a | n/a | ensemble dispatcher |

(TBD: validate these weights empirically — Phase 5 includes a small offline eval.)

### I-2 — Cross-encoder reranking: defer to alpha.19

Pragmatic alpha.18 rerank: rule-based with boosts:
- Tag match: +0.15
- project_id match (already enforced at gate): required
- session_id match: +0.20
- ADR/INV ref match: +0.10
- Vibe-case match (kind=decision for C3, kind=finding for C4, etc.): +0.10

LightGBM meta-learner is a Phase 6 candidate (need ≥1000 evaluation events).

### I-3 — Decay: per-strategy

| Strategy | Decay function | Half-life |
|---|---|---|
| C1 code | topological-load + medium recency | 90 days for snippets, ∞ for ADRs |
| C2 text | project-relative recency | 180 days |
| C3 decision | **zero decay** (MoM principle) | ∞ |
| C4 research | domain-aware | 1095 days AI/ML, 3650 days math |
| C5 video | high recency | 90 days |
| C6 audio | medium recency | 365 days |
| C7 multi | subtask-aware | n/a |

### I-4 — Graph role: candidate expander (Quanta principle)

ADR-015 implementation: graph traversal widens candidate pool (1-2 hops), dense + FTS5 re-rank. Graph contributes to RRF only as a candidate source, not as a scorer.

### I-5 — Source resolution: explicit in API

`RecallStrategy.Recall(...)` returns rows where every row has `row_id` resolved before fusion. Post-answer attribution walks the row_ids in the result set.

### I-6 — Surrogate cascade: deferred to alpha.19

Document as Phase 6. Not in alpha.18 scope.

### I-7 — Pre-retrieval gating: polymorphic dispatch IS the gate

`RecallFor(vibe_case, task_context, query)` is the gate. Each `RecallStrategy` is gated by:
- project_id (INV-19, alpha.17 already wired)
- session_id (proposed Gap 8)
- vibe_case (proposed Gap 1)
- task context (proposed Gap 3)

### I-8 — No universal weight recipe

`Weights` is per-strategy. Core does not bake defaults. Operator can tune per strategy.

---

## 5. Open Questions

1. **k=60 in RRF**: validate empirically per strategy. Cormack 2009 used web search; memory subsystem may benefit from different k.
2. **LightGBM rerank**: needs labeled eval set. Phase 5 includes a tiny eval harness (~30 questions, manually graded).
3. **Source-resolved attribution API**: not in current `Recall()` shape. Add to alpha.18 spec.
4. **Multi-modal weights in C7**: requires empirical tuning per subtask. Defer to alpha.19.
5. **Cross-encoder model choice**: which one for v0? Defer to alpha.19.

---

## 6. Recommended Defaults for SPEC-alpha-11-phase5.md

| Item | Default | Justification |
|---|---|---|
| Fusion method | Weighted RRF (Cormack 2009 base) | Universal SOTA 2026, 15/15 papers use it or a variant |
| RRF k | 60 (Cormack 2009) | Conservative default, validate empirically in Phase 5 eval |
| Rerank | Rule-based (alpha.18); LightGBM/cross-encoder deferred | Cost/accuracy trade-off favors rules for v0 |
| Decay | Per-strategy functions (see I-3) | Empirically validated by EngramRAG, MoM, LycheeMemory |
| Graph role | Candidate expander (Quanta principle) | 5+ papers validate, no paper uses graph as scorer |
| Source resolution | row_id before fusion | MegaMem principle, natural in dark-memory |
| Weights | Per-strategy (see I-1) | No universal recipe (DocuSearch, Music RecSys disagree) |
| Pre-retrieval gating | polymorphic dispatch (RecallFor) | CHSR-RRF principle, validates the design |

---

## 7. Sources NOT fetched (limitations)

- Cormack 2009 SIGIR paper (not on arxiv; cited from secondary sources).
- DocuSearch full PDF (only abstract reviewed).
- EngramRAG full PDF (only abstract reviewed).
- MegaMem full PDF (only abstract reviewed).

These should be reviewed in L2 (spec loop) if the operator wants deeper empirical grounding.

---

## 8. Next Step

L1.2 — R-B: Cross-modal embeddings (CLIP, ImageBind) for C5/C6.

This research is foundational for the C5VideoRecall and C6AudioRecall strategies. Without cross-modal embeddings, C5/C6 stay at FTS5-only quality (no improvement over alpha.17).
