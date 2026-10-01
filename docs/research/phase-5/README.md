# Phase 5 Research Index

**Loop**: L1 (research, recursive, 6 mini-loops)
**Started**: 2026-09-30
**Status**: 1/6 complete (R-A)

---

## Artifacts

| ID | Title | Status | Output | Key findings |
|---|---|---|---|---|
| **R-A** | RRF + hybrid retrieval fusion | ✅ Complete | `R-A-rrf-hybrid-retrieval.md` | 15 SOTA 2026 papers reviewed. Weighted RRF is canonical. Per-strategy weights, per-strategy decay, graph as expander. |
| R-B | Cross-modal embeddings (CLIP, ImageBind) | ✅ Complete | `R-B-cross-modal-embeddings.md` | ImageBind + Netflix MediaFM + MM-VeriRec validated. Cross-modal adapter pattern. Privacy considerations. |
| R-C | Temporal decay in memory systems | ✅ Complete | `R-C-temporal-decay.md` | Fortunate Recall 10+1 ontology DIRECTLY validates per-vibe-case decay. 6.4-15.9 pp gap over Mem0/A-MEM. Per-row π_i + τ_i schema (ScrubJay). |
| R-D | Multi-hop / graph retrieval | ✅ Complete | `R-D-multi-hop-graph.md` | HippoRAG canonical, ProGraph 2-layer outperforms by 8pp, CABLE complementary links, **ReFind counter-evidence** (NO graph beats HippoRAG 2 by 5pp). Calibration real. Cost real. |
| R-E | Code-specific retrieval | ✅ Complete | `R-E-code-retrieval.md` | **FTS5 + graph > any code embedder** (CodeCompass: 99.4% vs 78.2%). No GraphCodeBERT for alpha.18. ADR/INV refs are the differentiator. |
| R-F | Decision-context retrieval patterns | ✅ Complete | `R-F-decision-context.md` | **MOOSEDev: KG 0.98-1.00 vs vector 6-27% on supersession**. MemLACE supersession+contradiction. TokenMizer bitemporal + transition records. Engram 83.6% vs 73.2%. FARMA forgery threat. Why-arcs. |

---

## ✅ L1 (RESEARCH) COMPLETE — 6/6

All 6 research artifacts produced and saved. Total ~1,759 LoC of synthesis across ~120 SOTA 2026 papers reviewed.

## ✅ L2 (SPEC) COMPLETE

`docs/specs/SPEC-alpha-11-phase5.md` — 434 LoC, 15 sections, vibe-case-aware memory subsystem.

## ✅ L3 (DRIFT) — MANUAL JUDGE: **ALIGNED**

Drift check performed (manual; dark-memory MCP unavailable in this session):
- Per-vibe-case weights (§3) match R-A I-1 verbatim. ✓
- Schema columns (§4.1) match R-B I-2 + R-C I-1 + R-E I-3 + R-D I-2 + R-F I-2 verbatim. ✓
- New tables (§4.2) match R-D I-2/I-3 + R-F I-2 verbatim. ✓
- RecallFor dispatch (§5) implements R-A I-7 polymorphic pattern. ✓
- Per-strategy implementations (§6.1-6.7) cite each respective research artifact. ✓
- Embedder adapter (§7) implements R-B I-9 interface. ✓
- Graph subsystem (§8) implements R-D I-1 (HippoRAG PPR + ProGraph + CABLE + CatRAG + PhaseGraph). ✓
- Decay function (§9) implements R-C I-3 formula. ✓
- Decision subsystem (§10) implements R-F I-4/I-5/I-6/I-8. ✓
- Micro-eval (§11) implements R-C I-6 + R-D I-6. ✓
- Counter-evidence captured (§13.1). ✓

**Verdict**: ALIGNED. Spec is grounded in research; no detected drift.

---

## 🛑 Operator decision: L3 → L4

**Phase 5 vibe-loop status**: L1 ✅ → L2 ✅ → L3 ✅ ALIGNED.

**Next**: L4 — 5 implementation chunks, ~1,500 LoC, gated on operator authorization.

Chunks preview:
1. Schema migration (~250 LoC): 13 columns + 3 tables.
2. RecallFor dispatch + C3DecisionRecall (~400 LoC).
3. C1Code + C2Text + C4Research (~400 LoC).
4. C5Video + C6Audio + C7Multi (~300 LoC).
5. Decay/refresh/rationale/supersession/micro-eval (~150 LoC).

**Three options**:

**A. Procede con L4** — open session, implement Chunk 1, commit per ADR-008 (1 SUMMARY + N SECTION atomic mirror), continue through Chunk 5.

**B. Adjust chunk order** — e.g., ship Chunk 2 (RecallFor + C3) before Chunk 1 (schema) since C3 is the highest-impact strategy. Or merge chunks for fewer commits.

**C. Stop at spec** — Phase 5 spec is the deliverable for alpha.17 docs; implementation deferred to alpha.18 workstream.

**¿Cuál?**

---

## Loop State

- **L1 (research)**: 1/6 complete (16.7%)
- **L2 (spec)**: NOT STARTED (gated on L1)
- **L3 (drift)**: NOT STARTED (gated on L2)
- **L4 (chunks)**: NOT STARTED (gated on L3 ALIGNED)

---

## Cross-references

- Operator's prior gap analysis: gaps 1-9 (see session log).
- sota-critique.md §8.3 (7 behind-SOTA gaps with file:line grounding).
- v4-alpha-11-plan.md §5 (Phase 5 plan items 11-13).
- AGENT_MEMORY_SCHEMA.md §8.3 (deficits vs SOTA 2026).
