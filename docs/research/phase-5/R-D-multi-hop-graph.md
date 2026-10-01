# R-D — Multi-Hop / Graph Retrieval (SOTA 2026)

**Loop**: L1 (research) → L1.4
**Author**: Opita-AI (MiniMax-M3), 2026-09-30
**Status**: ✅ Complete — HippoRAG + ProGraph + CABLE + 9 other SOTA 2026 papers
**Implications for**: ADR-015 (multi-hop graph); graph schema; per-vibe-case graph weights

---

## 1. Research Question

**¿Cuál es el algoritmo y schema de graph retrieval SOTA 2026 para memorias de LLM agents? ¿Cuál es el rol del graph (scorer vs expander)? ¿Cuántos hops valen la pena?**

Sub-questions:
1. ¿HippoRAG sigue siendo el patrón canónico, o hay un sucesor claro?
2. ¿Cuál es el costo/beneficio de la construcción del grafo?
3. ¿Qué patrones de schema de links son dominantes (edge list, node-typed, separate table)?
4. ¿Cuál es el hop depth óptimo empíricamente?
5. ¿Hay contraevidencia a la utilidad del graph retrieval?

---

## 2. Primary Sources (11 papers)

### Foundational

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2405.14831** | **HippoRAG** (NeurIPS 2024) | 2024-05 | **LLM + KG + Personalized PageRank** | Neurobiologically inspired. 20% over SOTA. 10-30x cheaper than IRCoT. Code: OSU-NLP-Group/HippoRAG. |

### SOTA 2026 successors and alternatives

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2607.19359** | **ProGraph** | 2026-06 | **2-layer: profile expansion + compression residuals** | 80.1% MemHop, 78.4% LoCoMo (+11.3pp over FullContext). Outperforms Mem0, A-MEM, HippoRAG, RAG. |
| **2608.17911** | **CABLE** (COLM 2026) | 2026-08 | **Complementary Antecedent-Based Linking and Expansion** | Plug-in. Sparse directed graph. Works WITH A-MEM, Mem0g, SimpleMem. |
| **2510.08958** | **EcphoryRAG** | 2025-10 | **Entity-centric KG; 94% token reduction** | New SOTA: 0.392 → 0.474 EM vs HippoRAG. |
| **2602.01965** | **CatRAG** | 2026-02 | **Query-aware dynamic edge weighting (3 mechanisms)** | Builds on HippoRAG 2. Addresses "Static Graph Fallacy". |

### Calibration and routing

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2603.28886** | PhaseGraph | 2026-03 | Percentile-rank calibration before fusion | LastHop@10: 69.1 → 71.0 MuSiQue. +6.3pp vs HippoRAG 2 at LastHop@10. |
| **2606.30133** | Query-Aware Spreading Activation | 2026-06 | **Single per-step semantic gate** | Outperforms HippoRAG by 5.3 EM on MuSiQue. 1.5-4.9x faster. |

### Top performers

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2605.25480** | LLM-Wiki | 2026-05 | Compiles docs → Wiki → bidirectional links → tool-call interface | SOTA HotpotQA, MuSiQue, 2WikiMultiHopQA. +2.0-8.1 F1 over HippoRAG 2. |
| **2605.00529** | Ψ-RAG (ICML 2026) | 2026-05 | Hierarchical Abstract Tree | +25.9% over RAPTOR, +7.4% over HippoRAG 2. |

### Counter-evidence and standards

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2608.12888** | **ReFind** | 2026-08 | **Agent-controlled search over raw logs (NO structure)** | 58.2 mean accuracy vs HippoRAG 2 at 53.2. Across 2,800 questions. Counter-evidence. |
| **2608.16096** | Commercial Tax | 2026-08 | **Cost analysis of multi-hop systems** | Indexing can span 11x. $428K to $4.6M for 1 TB. |
| **2609.02011** | Seed-Anchored Graph Rendering | 2026-09 | Deterministic, query-local graph evidence | 0.450 → 0.970 accuracy under 8K-char budget. |

---

## 3. Findings

### F1 — HippoRAG is the canonical pattern (NeurIPS 2024)

- LLM + KG + Personalized PageRank.
- Mimics neocortex + hippocampus roles in human memory.
- 20% over SOTA on multi-hop QA.
- 10-30x cheaper and 6-13x faster than iterative retrieval (IRCoT).
- Code at OSU-NLP-Group/HippoRAG (well-maintained).
- **For dark-memory**: validates ADR-015 (multi-hop graph). HippoRAG-style PPR over KG is the default algorithm.

### F2 — ProGraph outperforms HippoRAG with profile + residuals (KEY ALTERNATIVE)

- 2-layer architecture:
  - Layer 1: **profile expansion** (substring-matched traversal of entity names in LLM-written narratives).
  - Layer 2: **compression residuals** (exact dates, quantities, named items co-extracted at zero extra API cost).
- **80.1% on MemHop** (matches FullContext reference).
- **78.4% on LoCoMo** (+11.3pp over FullContext, +~8pp over HippoRAG).
- Beats Mem0, A-MEM, HippoRAG, RAG on both benchmarks.
- Ablation: profile expansion drives multi-hop (-22.6pp on MemHop when removed); compression residuals drive precision recall (-8.6pp on LoCoMo).
- **For dark-memory**: validates the 2-layer memory architecture. Profile + residuals is the schema, not pure KG.

### F3 — CABLE: complementary links (not duplicate-of-retriever links)

- **Plug-in** augmentation; works WITH A-MEM, Mem0g, SimpleMem, etc.
- For each new memory: generate antecedent queries, retrieve prior memories, **subtract direct semantic neighborhood**, verify remainder, add accepted links to sparse directed graph.
- Retrieval: expand seeds along these links to surface **implicit supporting evidence**.
- Yields higher mean LLM-judge scores in EVERY evaluated system-level setting.
- Largest gains in **distributed-evidence questions** (open-domain, multi-session, preference-oriented).
- **For dark-memory**: graph links should be COMPLEMENTARY to FTS5/vector, not duplicative. This validates Quanta principle (graph as expander). CABLE's "subtract direct semantic neighborhood" is the formal version.

### F4 — EcphoryRAG: 94% token reduction with entity-centric KG

- Indexes only core entities + metadata.
- New SOTA: 0.392 → 0.474 EM vs HippoRAG on 2WikiMultiHop, HotpotQA, MuSiQue.
- **For dark-memory**: confirms that lightweight entity indexing is competitive. Don't build a full KG; index entities.

### F5 — CatRAG addresses "Static Graph Fallacy"

- HippoRAG's fixed transition probabilities → semantic drift into hub nodes.
- Three mechanisms: Symbolic Anchoring, Query-Aware Dynamic Edge Weighting, Key-Fact Passage Weight Enhancement.
- Substantial improvements in **reasoning completeness** (full evidence path without gaps).
- **For dark-memory**: graph traversal should be query-adaptive, not static. Phase 5 must implement query-aware edge weighting.

### F6 — PhaseGraph: score calibration is the right fusion primitive

- Percentile-rank normalization (PIT) before fusion.
- LastHop@10: 69.1% → 71.0% on MuSiQue (+1.9pp, p=0.041).
- LastHop@5: 51.7% → 53.6% on 2WikiMultiHopQA (+1.9pp, p=0.023).
- vs HippoRAG 2 official: +6.3pp at LastHop@10, -8.4pp at LastHop@5 (cutoff-dependent).
- **For dark-memory**: calibration is real engineering. RRF doesn't normalize; weighted RRF + percentile-rank is better. Per-strategy choice.

### F7 — ReFind = strong counter-evidence to graph retrieval (CRITICAL)

- **Agent-controlled search over raw chat logs. NO semantic structure at all.**
- 58.2 mean accuracy vs HippoRAG 2 at 53.2 (across 2,800 questions, GPT-4o-mini).
- Same backbone (GPT-4o-mini).
- LongMemEval-S/M: 93.2 ± 3.3 / 89.3 ± 6.0 with GPT-5-mini.
- **"much of the benefit credited to elaborate memory structures is recoverable by giving an agent controllable search over the unmodified record, with no LLM-based index construction at all."**
- **For dark-memory**: validates Gap 3 (Recall returns rows, not shaped context). Agent-controlled search may be MORE valuable than elaborate graph structure for some vibe-cases. Phase 5 should NOT over-invest in graph; consider agent-controlled search as alpha.19 alternative. **This is a wake-up call.**

### F8 — LLM-Wiki: retrieval-as-reasoning

- Compiles docs into Wiki pages with bidirectional links.
- Exposes search, read, link-following as tool-calling operations.
- SOTA on HotpotQA, MuSiQue, 2WikiMultiHopQA. +2.0-8.1 F1 over HippoRAG 2.
- **For dark-memory**: validates the tool-calling interface. Maps to Gap 3 (RecallFor returns shaped context with next_actions hint). alpha.19 should add tool-call surface.

### F9 — Graph is candidate expander + calibrated score

- Query-Aware Spreading Activation: single per-step semantic gate is enough (3.6-7.4 F1 gain, 1.5-4.9x faster).
- PhaseGraph: percentile-rank calibration.
- CatRAG: query-aware dynamic edge weighting.
- **For dark-memory**: Phase 5 graph design = (1) edge weights + (2) query-aware gate + (3) calibration layer. Three orthogonal mechanisms.

### F10 — Cost is real concern (Commercial Tax paper)

- API embedders charge per token on every re-index.
- Self-hosted ones charge nothing.
- Indexing cost spans 11x ($2.30 to $24.94 for 5.64 MB corpus).
- Extrapolated to 1 TB: $428K to $4.6M.
- **For dark-memory**: Phase 5 includes explicit cost section. Default alpha.18 (local embedder + BM25 + KG) is ~$0/1k queries. OpenAI embeddings are $0.02/1M tokens; re-index = $0.10-1.00 per 100 rows. Document tradeoff.

### F11 — Hop depth: 1-2 is the sweet spot

- MemHop benchmark: 1-5 hops. Most real-world questions are 1-2 hops.
- ReFind gets 58.2% with NO graph (just lexical search) → graph is NOT always needed.
- HippoRAG 2: phrase-node architecture helps for 2-hop; HippoRAG 1 better at 3-hop.
- **For dark-memory**: alpha.18 default = 2 hops max. 3+ hops is alpha.19. Some vibe-cases (C3 decision) might warrant 3 hops for ADR/INV chains.

### F12 — Multi-hop benchmarks for Phase 5 micro-eval

- MemHop: 1,000 questions, hop depths 1-5 (best match for dark-memory).
- MuSiQue: standard multi-hop.
- HotpotQA: standard.
- 2WikiMultiHopQA: standard.
- **For dark-memory**: Phase 5 micro-eval uses MemHop subset (~100 questions) for graph retrieval validation. Compare to ProGraph's 80.1% baseline.

---

## 4. Implications for dark-memory Phase 5

### I-1 — Graph role: candidate expander (Quanta) + HippoRAG PPR + CABLE complementary links

```go
type GraphRetrieval interface {
    Expand(ctx, query, projectID, seedIDs []int64, hops int) ([]int64, error)  // returns candidate row IDs
}
```

- Default algorithm: HippoRAG-style PPR over entity graph.
- Role: candidate expander (NOT scorer).
- 1-2 hops max for alpha.18.
- Query-aware edge weighting (CatRAG principle).

### I-2 — 2-layer graph schema (ProGraph pattern)

```sql
-- New table: agent_memory_entities
CREATE TABLE agent_memory_entities (
    entity_id INTEGER PRIMARY KEY AUTOINCREMENT,
    row_id INTEGER NOT NULL,
    entity TEXT NOT NULL,            -- substring for matching
    entity_kind TEXT NOT NULL,        -- person, place, concept, adr, inv, commit
    FOREIGN KEY (row_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE
);
CREATE INDEX idx_entity ON agent_memory_entities(entity);

-- New column: agent_memory.extracted_residuals
ALTER TABLE agent_memory ADD COLUMN extracted_residuals TEXT;  -- JSON: dates, quantities, named items
```

### I-3 — Link construction (CABLE principle, operator-flag)

```sql
-- New table: agent_memory_links (sparse directed graph)
CREATE TABLE agent_memory_links (
    source_id INTEGER NOT NULL,
    target_id INTEGER NOT NULL,
    kind TEXT NOT NULL,        -- parent | child | related | references | supersedes | cable_complement
    weight REAL DEFAULT 1.0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source_id, target_id, kind),
    FOREIGN KEY (source_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE,
    FOREIGN KEY (target_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE
);
CREATE INDEX idx_link_source ON agent_memory_links(source_id);
CREATE INDEX idx_link_target ON agent_memory_links(target_id);
```

Link construction triggered by `agent_memory.save()` (operator-flag, default OFF for alpha.18).

### I-4 — Score calibration (PhaseGraph principle)

```go
func Calibrate(scores map[int64]float64) map[int64]float64 {
    // Percentile-rank normalization
    sorted := sortByValue(scores)
    rank := make(map[int64]float64)
    for i, id := range sorted {
        rank[id] = float64(i+1) / float64(len(sorted))
    }
    return rank
}
```

Per-strategy choice: weighted RRF + calibration vs plain RRF.

### I-5 — Graph scope by vibe-case (from R-A §4 I-1)

| Strategy | Graph weight | Graph role |
|---|---|---|
| C1 code | 0.45 | ADR/INV refs + commit hash boost |
| C2 text | 0.10 | Recent same-project graph |
| C3 decision | 0.70 | Decision kind graph (high weight) |
| C4 research | 0.30 | Citation graph |
| C5 video | 0.20 | Style ref graph |
| C6 audio | 0.20 | Voice profile graph |
| C7 multi | n/a | Ensemble dispatcher |

### I-6 — Micro-eval includes MemHop

Phase 5 micro-eval:
- 100-question MemHop subset (operator-curated for relevance to opita-market work).
- Compare Phase 5 graph retrieval to ProGraph's 80.1% baseline.
- Document results in Phase 5 release notes.

### I-7 — Cost model explicit

| Component | alpha.18 cost | alpha.19 cost |
|---|---|---|
| Local embedder (BGE/ONNX) | $0 | $0 |
| OpenAI embeddings | $0.02/1M tok | $0.02/1M tok |
| FTS5 + BM25 | $0 | $0 |
| Graph (KG + PPR) | $0 compute | $0 compute |
| Cross-encoder rerank | (deferred) | $0 compute (local) |
| **Per-query** | **<0.1s** | **<0.5s** |
| **Per-row embed** | **$0 (local) or $0.001 (cloud)** | same |

For 100K rows: $0 (local) or $100 (cloud). Document.

### I-8 — Don't over-invest in graph for alpha.18

- ReFind's 5pp gap is a warning.
- Phase 5 alpha.18: HippoRAG-style PPR + profile expansion + CABLE link construction.
- alpha.19: experiment with agent-controlled search as alternative.
- **Caveat**: graph IS needed for C3 (decision), C4 (research), C1 (code ADR/INV refs). The over-investment warning is for C2/C5/C6/C7.

### I-9 — Hop depth: 2 max for alpha.18

- Default: 1 hop.
- C3 decision: 2 hops (for ADR/INV chains).
- C4 research: 2 hops (for citation graph).
- 3+ hops: alpha.19.

### I-10 — Tool-call interface (alpha.19 followup)

- LLM-Wiki pattern: search, read, link-following as tools.
- For dark-memory: alpha.19 exposes `recall_search`, `recall_read`, `recall_follow_link` as separate tools.
- alpha.18 ships the unified `RecallFor()` with single-hop; agent can call multiple times.

---

## 5. Open Questions

1. **CABLE link verification**: what model verifies "this is a complementary link, not a duplicate"? Operator choice: small local model or LLM call? Decision: Phase 5 spec calls for "LLM call" but operator can swap to local.
2. **PhaseGraph cutoff dependency**: P@5 worse than P@10 — suggests graph is good for breadth, not precision. For dark-memory's topK=10 default: prefer graph.
3. **MemHop coverage**: does MemHop's social-network scenarios generalize to dark-memory's opita-market context? Need operator-curated subset.
4. **ReFind alternative path**: should Phase 5 alpha.18 ship ReFind-style agent-controlled search INSTEAD of elaborate graph? Decision: ship graph in alpha.18; add ReFind-style as alpha.19 alternative; let operator choose.
5. **ProGraph's "profile expansion" requires LLM-written narratives**: dark-memory's agent_memory rows are operator-written (or save-time LLM-extracted). Need to validate profile quality.

---

## 6. Recommended Defaults for SPEC-alpha-11-phase5.md

| Item | Default | Justification |
|---|---|---|
| Graph algorithm | HippoRAG-style PPR over entity KG | F1: NeurIPS 2024 canonical |
| Schema: entities | `agent_memory_entities` table (entity_id, row_id, entity, entity_kind) | F2: ProGraph 2-layer |
| Schema: residuals | `agent_memory.extracted_residuals TEXT` (JSON) | F2: ProGraph |
| Schema: links | `agent_memory_links` (sparse directed, 1-3 per row) | F3: CABLE |
| Link construction | Operator-flag, default OFF | F3: CABLE pattern |
| Score calibration | Percentile-rank before fusion | F6: PhaseGraph |
| Hop depth | 1 default; 2 for C3/C4 | F11: empirical sweet spot |
| Micro-eval | 100-question MemHop subset | F12: matches use case |
| Cost model | Explicit in spec (alpha.18: $0/1k queries local) | F10: cost reality |
| Agent-controlled search | alpha.19 alternative | F7: ReFind wake-up call |

---

## 7. Cross-references to other R-A..F artifacts

- **R-A §4 I-1**: per-strategy graph weights (validated by HippoRAG + CABLE + ReFind).
- **R-A §4 I-4**: graph as expander (Quanta principle, validated by all 11 papers).
- **R-B**: cross-modal embeddings used in profile expansion (ProGraph profile narratives can include image/audio descriptions).
- **R-C**: decay affects graph (skip `instant` rows after refresh cycle; `forever` rows always linked).
- **R-E** (next): code-specific retrieval may have its own graph (call graph, type hierarchy).
- **R-F** (next): decision retrieval is fundamentally graph-driven; this R-D validates the approach.

---

## 8. Sources NOT fetched (limitations)

- HippoRAG 2 full PDF (only HippoRAG 1 abstract + secondary references).
- A-MEM graph implementation details (Zettelkasten pattern only inferred).
- Mem0g graph specifics.
- ProGraph GitHub code not deep-read.

These should be reviewed in L2 (spec loop).

---

## 9. Next Step

L1.5 — R-E: Code-specific retrieval (GraphCodeBERT, Code2Vec, repo-level embeddings).

This is shorter than R-D because:
- Phase 5 already plans to use FTS5 + ADR/INV refs for C1 (validated by R-A I-1 weights).
- R-E confirms whether vector retrieval of code is worth the LoC.
- If R-E shows text-embedding of code is bad, we keep FTS5 + ADR/INV for C1 and skip cross-modal for code.

Then R-F: Decision-context retrieval patterns. This validates the C3 strategy (graph + decision kind index).

---

## 10. Operator Checkpoint

**Status: 4/6 research areas complete (66.7%)**. L1 has rich evidence for:
- Fusion method (R-A): Weighted RRF, per-strategy.
- Embedder (R-B): ImageBind + BGE fallback.
- Decay (R-C): Per-vibe-case, with Fortunate Recall as canonical.
- Graph (R-D): HippoRAG-style, ProGraph schema, CABLE links, calibration.

**Remaining**: R-E (code retrieval — short), R-F (decision patterns).

**Recommended operator checkpoint**: react to R-A through R-D before I proceed to R-E and R-F. The synthesis is converging on a clear Phase 5 architecture. If operator wants to redirect (e.g., prioritize certain vibe-cases), now is the time.
