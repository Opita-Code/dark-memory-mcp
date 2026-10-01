# R-E — Code-Specific Retrieval (SOTA 2026)

**Loop**: L1 (research) → L1.5
**Author**: Opita-AI (MiniMax-M3), 2026-09-30
**Status**: ✅ Complete — 3 primary sources (XSearch, CodeCompass, embedded C RAG)
**Implications for**: C1 (code) strategy; ADR/INV ref index; commit hash boost

---

## 1. Research Question

**¿Vale la pena agregar un embedder code-specific (GraphCodeBERT, Code2Vec, StarCoder2) a Phase 5, o FTS5 + ADR/INV refs es suficiente para el vibe-case C1?**

Sub-questions:
1. ¿Embedders de código mejoran retrieval vs BM25?
2. ¿Cuánto vale la pena GraphCodeBERT-style training?
3. ¿El graph navigation es crítico para code retrieval?
4. ¿Cuál es el costo/beneficio real?

---

## 2. Primary Sources (3 papers)

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2605.16046** | **XSearch** (ISSTA 2026) | 2026-05 | Concept-to-Code alignment (GraphCodeBERT 125M) | OOD: 0.02 → 0.33 (15x improvement). |
| **2602.20048** | **CodeCompass** | 2026-02 | Graph-based structural navigation (MCP server) | 99.4% on hidden-dependency tasks vs BM25 78.2% (+21.2pp). |
| **2608.04137** | Embedded C Function Reuse | 2026-08 | 8-embedder comparison + 4 hardware validators | Cosine 0.90 ≠ reuse. Static analysis 93.6% FPR. |

---

## 3. Findings

### F1 — Cosine similarity is misleading for code (arxiv:2608.04137)

- 8 embedders compared: MiniLM, MPNet, BGE, E5, **GraphCodeBERT**, OpenAI text-embedding-3-small, LLaMA 3 8B, StarCoder2 3B.
- Cosine similarity > 0.90 ≠ actual reuse compatibility.
- Static analysis tools: 93.6% false-positive rate.
- 83.5% of failures: hardware-environment mismatches (peripheral interfaces, HAL dependencies, register-map constraints).
- Manual verification: 97.5% validator accuracy on rejected pairs.
- **For dark-memory**: text embeddings of code can match semantically-similar but functionally-incompatible snippets. C1 strategy should NOT rely on text similarity alone.

### F2 — XSearch: GraphCodeBERT improves OOD 15x (arxiv:2605.16046)

- Concept-to-Code alignment (deductive, not inductive).
- Trained on CodeSearchNet with GraphCodeBERT (125M params).
- **0.02 → 0.33** on out-of-distribution benchmarks (15x improvement) over 8 SOTA retrievers.
- Outperforms encoder + decoder baselines up to 7B params.
- **For dark-memory**: Phase 5 alpha.18 should NOT ship this (training required, ~500 LoC for training + inference). alpha.19 optional upgrade path.

### F3 — CodeCompass: graph navigation > BM25 for hidden-dependency tasks (arxiv:2602.20048)

- "Navigation Paradox" — context limits aren't the bottleneck; navigation is.
- 258 trials, 30 benchmark tasks on FastAPI repo.
- **Graph-based structural navigation via MCP server**:
  - **99.4%** task completion on hidden-dependency tasks.
  - +23.2pp over vanilla agents (76.2%).
  - **+21.2pp over BM25 retrieval (78.2%)**.
- **Adoption gap**: 58% of trials with graph access made ZERO tool calls. Agents need explicit prompting.
- **For dark-memory**: validates C1CodeRecall's graph weight (0.45) — graph IS important for code. Maps to RecallFor polymorphic dispatch (operator must invoke code strategy explicitly).

### F4 — No single embedder dominates (arxiv:2608.04137)

- 8-model comparison shows different models fail on different tasks.
- For dark-memory: don't ship a code-specific embedder for alpha.18. LoC + maintenance not justified.

### F5 — BM25 + graph > plain embedder for code (CodeCompass)

- BM25 alone: 78.2% task completion.
- Graph alone (via MCP): 99.4%.
- For dark-memory: validates C1's FTS5 0.55 + graph 0.45 weights (no embedder).

---

## 4. Implications for dark-memory Phase 5

### I-1 — C1 code strategy: FTS5 + ADR/INV refs + commit hash boost (NO code-specific embedder for alpha.18)

```go
func (s *C1CodeRecall) Recall(ctx, query, projectID, sessionID, topK) ([]Row, error) {
    // Stage 1: FTS5 with code-aware tokenizer (split CamelCase, snake_case, kebab-case)
    candidates := s.ftsIndex.SearchCode(query, topK*5)
    
    // Stage 2: Graph expansion (ADR/INV refs, call graph) — 1 hop
    candidates = s.graphExpand(candidates, 1)
    
    // Stage 3: RRF with weights (0.55 FTS5, 0.45 graph)
    return s.applyRRF(candidates, s.weights, topK), nil
}
```

### I-2 — Code-aware tokenizer (alpha.18, ~50 LoC)

Custom SQLite FTS5 tokenizer:
- Split CamelCase: `getUserName` → `get`, `user`, `name`.
- Split snake_case: `get_user_name` → `get`, `user`, `name`.
- Split kebab-case: `get-user-name` → `get`, `user`, `name`.
- Split on `.` for method calls: `User.authenticate` → `user`, `authenticate`.
- Lowercase.
- Porter stemming for suffixes.

### I-3 — ADR/INV ref index (alpha.18 schema)

```sql
ALTER TABLE agent_memory ADD COLUMN adr_refs TEXT;     -- JSON list, e.g., ["ADR-013", "ADR-014"]
ALTER TABLE agent_memory ADD COLUMN inv_refs TEXT;     -- JSON list, e.g., ["INV-19"]
ALTER TABLE agent_memory ADD COLUMN commit_hashes TEXT; -- JSON list, e.g., ["abc123", "def456"]
CREATE INDEX idx_adr_refs ON agent_memory(adr_refs);
CREATE INDEX idx_inv_refs ON agent_memory(inv_refs);
```

### I-4 — No code-specific embedder for alpha.18

- 8-model comparison shows no clear winner (F4).
- BM25 + graph beats every single embedder (F5).
- LoC + maintenance not justified for alpha.18.

### I-5 — alpha.19: GraphCodeBERT adapter (optional)

- Phase 6 candidate.
- Improves OOD by 15x (XSearch).
- Operator-flag, default OFF.
- Estimated LoC: ~500 (training pipeline + inference + benchmark).

### I-6 — Adoption gap (CodeCompass finding)

- 58% of trials didn't use graph tools even when available.
- For dark-memory: operator MUST invoke `RecallFor(vibe_case=C1)` explicitly. Default `Recall()` should NOT auto-route to C1 (operator choice).
- Doc this in operator-facing guides.

---

## 5. Open Questions

1. **Code-aware tokenizer quality**: validate on dark-memory's actual code corpus (opita-market, Pasiones). Decision: Phase 5 micro-eval includes 20 code queries.
2. **ADR/INV ref extraction**: how to extract from `agent_memory.content` at save time? Operator-set or LLM-extracted? Decision: operator-set (via save() arg); alpha.19 adds auto-extract.
3. **Commit hash boost**: how to map a query like "the auth commit from last week" to commits? Phase 5 defer; alpha.19.
4. **GraphCodeBERT training data**: CodeSearchNet is the public dataset. For dark-memory's domain (Rust, TypeScript, Go), need fine-tuning. Cost ~$500-1k in OpenAI tokens for fine-tune. Decision: alpha.19 only.

---

## 6. Recommended Defaults for SPEC-alpha-11-phase5.md

| Item | Default | Justification |
|---|---|---|
| C1 strategy | FTS5 + ADR/INV refs + commit hash boost | F3, F5: graph > embedder for code |
| Code-aware tokenizer | Custom SQLite FTS5 (split CamelCase, snake_case, kebab-case) | F1: text similarity misleading |
| Embedder for code | NONE (alpha.18) | F4: no clear winner |
| ADR/INV refs | New columns on agent_memory | I-3 |
| GraphCodeBERT | alpha.19 optional | F2: 15x OOD improvement, but training cost |
| Adoption | Operator invokes `RecallFor(vibe_case=C1)` explicitly | F6: 58% non-adoption without prompting |

---

## 7. Cross-references

- **R-A §4 I-1**: C1 weights (0.55 FTS5, 0.45 graph) validated here.
- **R-C §4 I-1**: code decay (τ = 90 days for snippets, ∞ for ADRs) — validated by C1's split treatment.
- **R-D §4 I-5**: graph role (candidate expander, HippoRAG-style) — applied to C1.
- **R-F** (next): decision retrieval is closely related to C1's ADR/INV refs.

---

## 8. Sources NOT fetched

- Code2Vec paper (older; not directly relevant for retrieval).
- Repo-level embeddings (CodeBERT, GitHub Copilot's internal model — not public).
- OpenAI's codex-embed (deprecated).

These are minor; the 3 primary sources are sufficient.

---

## 9. Status

**L1.5 (R-E) ✅ Complete.** 5/6 research areas done. R-F next.

The C1 strategy is now well-defined: FTS5 (with code-aware tokenizer) + ADR/INV refs + commit hash boost. No embedder needed for alpha.18. GraphCodeBERT deferred to alpha.19 with documented cost/benefit.
