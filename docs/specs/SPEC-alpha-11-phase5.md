# SPEC-alpha-11-phase5 — Memory Subsystem (Vibe-Case-Aware)

**Version**: v4alpha.18-dev (target)
**Date**: 2026-09-30
**Author**: Opita-AI (MiniMax-M3), per operator authorization
**Vibe-loop**: `alpha-11-phase-5`
**Status**: Draft, pending drift_judge verdict (L3)
**Supersedes**: v4-alpha-11-plan.md §5 (Phase 5 plan items 11-13)

---

## 1. TL;DR

Phase 5 ships a **vibe-case-aware memory subsystem** for `dark-memory`. The unit of work is the `RecallStrategy`, one per vibe_case (C1..C7), each composing signals (FTS5, vector embeddings, graph traversal, cross-modal embeddings) with per-strategy weights, decay functions, and graph scopes.

This supersedes the capability-driven Phase 5 plan (ADR-013/014/015 as monolithic capabilities). The 6 research artifacts (R-A..R-F, in `docs/research/phase-5/`) ground every design decision in primary-source SOTA 2026 evidence (~120 papers reviewed).

**Total scope**: ~1,500 LoC across 5 implementation chunks. Target: v4.0.0-alpha.18. Gated on research findings (no OD2 dependency for alpha.18; full embedder library + cross-modal for alpha.19).

**Operator decisions baked in** (per `v4-alpha-11-plan.md` §7):
- OD1: chunk-7 first (closed).
- OD2: ADR-013 vector retrieval — included via C2/C4 strategies (NOT universal).
- OD3: BUG-10 10b namespace primitive — closed (alpha.17, INV-19).
- OD4: v2.9.x embedder ABANDONED (per row 1578).
- OD5: ADR-013 strategy = FRESH (per D1 in sota-critique.md §7.6.5).

---

## 2. Scope & non-goals

### 2.1 In scope (alpha.18)

- **Schema migrations**: agent_memory gets 13 new columns + 3 new tables (see §4).
- **RecallFor() polymorphic dispatch** (per R-A, R-D).
- **7 RecallStrategy implementations**: C1Code, C2Text, C3Decision, C4Research, C5Video, C6Audio, C7Multi (per R-A..R-F).
- **Embedder adapter interface**: ImageBind (cross-modal), BGE-large (text), OpenAI (HTTP fallback). NO embedder for C1.
- **Graph subsystem**: HippoRAG-style PPR over entity KG + ProGraph 2-layer schema + CABLE complementary links.
- **Decay + refresh-on-access**: per-kind decay_class, per-vibe-case multipliers (per R-C).
- **Decision subsystem**: rationale + supersedes_id + decision_state + decision_transitions table (per R-F).
- **Micro-eval**: 50-question LifecycleBench-style + 100-question MemHop subset.
- **Cost model**: explicit alpha.18 vs alpha.19 cost comparison (per R-D F10).

### 2.2 Non-goals (alpha.18 — deferred to alpha.19 or beta)

- Cross-encoder reranking (alpha.19; too expensive for v0).
- Surrogate cascade (ASCR pattern, alpha.19).
- Bitemporal full model (alpha.18: simple valid_from/to; alpha.19: transaction_time + valid_time per Engram).
- Note evolution (A-MEM pattern, alpha.19; alpha.18 ships hook only).
- Forgery detection (FARMA/SENTINEL, alpha.19).
- Region-level retrieval (MINER, alpha.19).
- Hierarchy-conditioned VLMs (Hyper3-CLIP, alpha.19).
- GraphCodeBERT code embedder (XSearch pattern, alpha.19).
- ReFind-style agent-controlled search (alpha.19 alternative path per R-D F7).
- DPM enterprise mode (stateless decision memory, alpha.19+).
- Auto-supersede detection (alpha.19; alpha.18 manual only).

---

## 3. Vibe-case strategy matrix (per R-A §4 I-1, R-B §4, R-C §4 I-2, R-D §4 I-5, R-E §6, R-F §4)

| Vibe | Description | Embedder | FTS5 | Vector | Graph | Cross-modal | Decay | Hop depth |
|---|---|---|---|---|---|---|---|---|
| **C1 code** | review/implement code | NONE | 0.55 | 0.00 | 0.45 (ADR/INV refs) | — | medium (90d snippet, ∞ ADR) | 1 |
| **C2 text** | write/edit text | BGE-large | 0.40 | 0.50 | 0.10 (same project) | — | medium (180d) | 1 |
| **C3 decision** | make/recall decisions | NONE | 0.30 | 0.00 | 0.70 (decision graph) | — | **∞ zero** (forever) | 2 |
| **C4 research** | find/analyze research | BGE-large | 0.25 | 0.45 | 0.30 (citation) | — | domain-aware (1095d AI/ML, 3650d math) | 2 |
| **C5 video** | generate/review video | ImageBind | 0.00 | 0.00 | 0.20 (style ref) | 0.80 | high (90d) | 1 |
| **C6 audio** | generate/review audio | ImageBind | 0.00 | 0.00 | 0.20 (voice profile) | 0.80 | medium (365d) | 1 |
| **C7 multi** | composite multi-modal | ensemble | n/a | n/a | n/a | n/a | subtask-aware | n/a |

Each strategy declares its weights as `Weights map[string]float64`. Core does not bake defaults.

---

## 4. Schema migrations (per R-B §4 I-2, R-C §4 I-1, R-D §4 I-2/I-3, R-F §4 I-2)

### 4.1 `agent_memory` new columns (idempotent migration)

```sql
-- Per R-B §4 I-2:
ALTER TABLE agent_memory ADD COLUMN embedding BLOB;
ALTER TABLE agent_memory ADD COLUMN embedding_model TEXT;
ALTER TABLE agent_memory ADD COLUMN embedding_dim INT;
ALTER TABLE agent_memory ADD COLUMN embedding_created_at TEXT;
ALTER TABLE agent_memory ADD COLUMN vibe_case TEXT;
ALTER TABLE agent_memory ADD COLUMN voice_embed_kind TEXT;
CREATE INDEX IF NOT EXISTS idx_embedding_model ON agent_memory(embedding_model);
CREATE INDEX IF NOT EXISTS idx_vibe_case ON agent_memory(vibe_case);

-- Per R-C §4 I-1:
ALTER TABLE agent_memory ADD COLUMN decay_class TEXT;
ALTER TABLE agent_memory ADD COLUMN decay_tau_days INT;
ALTER TABLE agent_memory ADD COLUMN last_refreshed_at TEXT;
ALTER TABLE agent_memory ADD COLUMN access_count INT DEFAULT 0;
ALTER TABLE agent_memory ADD COLUMN refresh_on_access BOOLEAN DEFAULT TRUE;

-- Per R-E §4 I-3:
ALTER TABLE agent_memory ADD COLUMN adr_refs TEXT;
ALTER TABLE agent_memory ADD COLUMN inv_refs TEXT;
ALTER TABLE agent_memory ADD COLUMN commit_hashes TEXT;
CREATE INDEX IF NOT EXISTS idx_adr_refs ON agent_memory(adr_refs);
CREATE INDEX IF NOT EXISTS idx_inv_refs ON agent_memory(inv_refs);

-- Per R-D §4 I-2:
ALTER TABLE agent_memory ADD COLUMN extracted_residuals TEXT;

-- Per R-F §4 I-2:
ALTER TABLE agent_memory ADD COLUMN rationale TEXT;
ALTER TABLE agent_memory ADD COLUMN supersedes_id INTEGER;
ALTER TABLE agent_memory ADD COLUMN decision_state TEXT DEFAULT 'active';
ALTER TABLE agent_memory ADD COLUMN valid_from TEXT;
ALTER TABLE agent_memory ADD COLUMN valid_to TEXT;
```

### 4.2 New tables

```sql
-- Per R-D §4 I-2 (ProGraph 2-layer):
CREATE TABLE agent_memory_entities (
    entity_id INTEGER PRIMARY KEY AUTOINCREMENT,
    row_id INTEGER NOT NULL,
    entity TEXT NOT NULL,
    entity_kind TEXT NOT NULL,
    FOREIGN KEY (row_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_entity ON agent_memory_entities(entity);

-- Per R-D §4 I-3 (CABLE sparse directed):
CREATE TABLE agent_memory_links (
    source_id INTEGER NOT NULL,
    target_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    weight REAL DEFAULT 1.0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source_id, target_id, kind),
    FOREIGN KEY (source_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE,
    FOREIGN KEY (target_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_link_source ON agent_memory_links(source_id);
CREATE INDEX IF NOT EXISTS idx_link_target ON agent_memory_links(target_id);

-- Per R-F §4 I-2 (TokenMizer-style decision transitions):
CREATE TABLE decision_transitions (
    transition_id INTEGER PRIMARY KEY AUTOINCREMENT,
    decision_id INTEGER NOT NULL,
    superseded_id INTEGER,
    trigger TEXT NOT NULL,
    reason TEXT NOT NULL,
    evidence TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    project_id TEXT NOT NULL DEFAULT 'default',
    FOREIGN KEY (decision_id) REFERENCES agent_memory(row_id) ON DELETE CASCADE,
    FOREIGN KEY (superseded_id) REFERENCES agent_memory(row_id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_dt_decision ON decision_transitions(decision_id);
CREATE INDEX IF NOT EXISTS idx_dt_superseded ON decision_transitions(superseded_id);
```

### 4.3 Backwards compatibility

All migrations idempotent (use `ALTER TABLE ... ADD COLUMN` with `IF NOT EXISTS` check via `pragma_table_info`). Pre-Phase-5 rows get NULL for new columns. `decay_class` auto-derived from `kind` on read (per R-C I-8).

---

## 5. RecallFor dispatch (per R-A §4 I-7, R-D §4 I-1)

```go
// Per R-A I-7 (polymorphic dispatch):
type RecallStrategy interface {
    Name() string
    Weights() map[string]float64  // signal name → weight
    PreferredKinds() []string
    PreferredDecayClasses() []string
    HopDepth() int
    Recall(ctx, query, projectID, sessionID, topK) ([]Row, error)
}

func RecallFor(vibeCase string, query string, projectID, sessionID string, topK int) ([]Row, error) {
    strategy := strategyRegistry[vibeCase]
    if strategy == nil { return nil, ErrUnknownVibeCase }
    candidates := preFilter(query, projectID, vibeCase)
    candidates = strategy.GraphExpand(candidates, strategy.HopDepth())
    scored := strategy.ApplyWeights(candidates)
    scored = strategy.ApplyDecay(scored)
    scored = strategy.FilterActive(scored)  // excludes superseded decisions for C3
    return topK(scored, topK), nil
}
```

The dispatch is gated by (per R-A I-7, R-C I-5):
- `project_id` (INV-19, alpha.17).
- `vibe_case` (per §4.1 schema).
- `session_id` (proposed, alpha.18 wires it).
- Operator's `RecallFor(vibe_case=C3)` is explicit (per R-E F6: 58% non-adoption without prompting).

---

## 6. Per-strategy implementations

### 6.1 C1CodeRecall (~80 LoC, per R-E)

- FTS5 with custom code-aware tokenizer (CamelCase, snake_case, kebab-case, dot-split).
- Graph: 1-hop over ADR/INV refs + call graph (when available).
- No embedder.
- Weights: 0.55 FTS5, 0.45 graph.

### 6.2 C2TextRecall (~80 LoC, per R-A + R-B)

- FTS5 + BGE-large text vector + 1-hop same-project graph.
- Weights: 0.40 FTS5, 0.50 vector, 0.10 graph.
- Decay: medium (180d).

### 6.3 C3DecisionRecall (~120 LoC, per R-F) — most critical

- FTS5 with decision-aware query expansion (rationale + transition triggers).
- Graph: 2-hop decision-kind graph (ADR/INV chains).
- Decision-transition filter: returns `decision_state='active'` only by default.
- Weights: 0.30 FTS5, 0.70 graph.
- Decay: zero (forever).
- Rationale surfaced in response.

### 6.4 C4ResearchRecall (~100 LoC, per R-A + R-B + R-D)

- FTS5 + BGE-large + 2-hop citation graph.
- Weights: 0.25 FTS5, 0.45 vector, 0.30 graph.
- Decay: domain-aware (1095d AI/ML, 3650d math).
- Per-domain decay configurable.

### 6.5 C5VideoRecall (~80 LoC, per R-B)

- ImageBind image encoder (cross-modal 0.80).
- FTS5 + style ref graph.
- Weights: 0.00 FTS5, 0.00 vector, 0.20 graph, 0.80 cross-modal.
- Decay: high (90d).
- Lazy indexing for legacy rows (alpha.18); save-time pre-embed for new rows.

### 6.6 C6AudioRecall (~80 LoC, per R-B + R-D F9)

- ImageBind audio encoder (cross-modal 0.80).
- FTS5 + voice profile graph (timbre embedding).
- `voice_embed_kind` column (timbre | full | prosody+timbre).
- Weights: 0.00 FTS5, 0.00 vector, 0.20 graph, 0.80 cross-modal.
- Decay: medium (365d).

### 6.7 C7MultiRecall (~80 LoC, per R-B)

- Ensemble dispatcher: detects sub-tasks, delegates to C1-C6 strategies, merges via RRF.
- Per-subtask weight tuning (operator-configurable).
- Decay: subtask-aware (max of subtask decays).

---

## 7. Embedder adapter interface (per R-B §4 I-9)

```go
// Per R-B §4 I-9:
type EmbedderAdapter interface {
    Embed(ctx, kind Modality, input []byte) ([]float32, error)
    Dim() int
    Model() string
}

type Modality string
const (
    ModalityText  Modality = "text"
    ModalityImage Modality = "image"
    ModalityAudio Modality = "audio"
)

type AdapterRegistry struct {
    adapters map[string]EmbedderAdapter  // model name → adapter
}
```

Default adapters (per R-B §6):
- `imagebind-v1` (1024-dim, ONNX): cross-modal.
- `bge-large-en-v1.5` (1024-dim): text fallback.
- `wav2vec-2` (256-dim, voice_only): audio timbre.
- `openai-text-embedding-3-small` (1536-dim): HTTP fallback (alpha.19).

Cold-start: lazy indexing for legacy rows; save-time pre-embed for new rows (per R-B I-6).

---

## 8. Graph subsystem (per R-D §4 I-1)

- Algorithm: HippoRAG-style PPR over entity KG (per R-D F1).
- Schema: ProGraph 2-layer (entities + residuals) + CABLE sparse links (per R-D I-2/I-3).
- Role: candidate expander, NOT scorer (Quanta principle, per R-A I-4 + R-D I-1).
- Hop depth: 1 default, 2 for C3/C4, 3+ deferred to alpha.19.
- Query-aware edge weighting (CatRAG principle, per R-D F5).
- Score calibration: percentile-rank before fusion (PhaseGraph, per R-D I-4).
- CABLE link construction: triggered by `agent_memory.save()` (operator-flag, default OFF).

---

## 9. Decay + refresh-on-access (per R-C §4 I-3)

```go
// Per R-C I-3:
func DecayScore(row AgentMemory, now time.Time) float64 {
    if row.DecayClass == "forever" { return 1.0 }
    tau := float64(row.DecayTauDays)
    if tau <= 0 { return 1.0 }
    refTime := row.UpdatedAt
    if row.RefreshOnAccess && !row.LastRefreshedAt.IsZero() {
        refTime = row.LastRefreshedAt
    }
    age := now.Sub(refTime).Hours() / 24.0
    base := math.Exp(-age / tau)
    if row.AccessCount > 0 && row.RefreshOnAccess {
        boost := 1.0 + 0.3*math.Log10(float64(row.AccessCount+1))
        base *= math.Min(boost, 2.0)
    }
    return base
}
```

Auto-derivation per kind (per R-C I-1):
- `decision` → `forever`, τ = NULL.
- `finding` → `persistent`, τ = 1095.
- `observation` → `stable`, τ = 365.
- `note` → `perishable`, τ = 90.
- `link` → `instant`, τ = 30.
- `context` → `stable`, τ = 365.
- `todo` → `perishable`, τ = 60.

Per-vibe-case multiplier (per R-C I-2):
- C1: × 1.0; C2: × 0.5; C3: × ∞; C4: × 1.0 (× 3.0 math, × 0.3 AI/ML); C5: × 0.25; C6: × 1.0; C7: subtask.

Refresh-on-access: TRUE except `forever` rows (per R-C I-4).

---

## 10. Decision subsystem (per R-F §4)

C3 strategy (§6.3) + new table (`decision_transitions`, §4.2) + schema columns (`rationale`, `supersedes_id`, `decision_state`, `valid_from/to`, §4.1).

Pipeline (per R-F I-5, MemClaw warning): write-first, async contradiction detection. Don't sync-gate contradictions.

Bitemporal (per R-F I-6): alpha.18 ships simple `valid_from` + `valid_to`. Alpha.19 adds full bitemporal (transaction_time + valid_time).

Decision-transition filter (per R-F I-4): `RecallFor(C3)` returns `decision_state='active'` only. Operator can set `include_superseded=true` for audit views.

Rationale preservation (per R-F I-8): `rationale` field required for `kind='decision'` rows after operator migration.

---

## 11. Micro-eval (per R-C §4 I-6, R-D §4 I-6)

- **LifecycleBench-style**: 50 temporal-disambiguation questions (per R-C I-6).
  - "What did we decide about X last year that still holds?"
  - "What's the most recent finding about Y?"
  - "Was the decision about Z ever overridden?"
- **MemHop subset**: 100 multi-hop questions (per R-D I-6).
  - Compare Phase 5 graph retrieval to ProGraph's 80.1% baseline.
- **C1 code subset**: 20 code queries (per R-E Open Q1).
- **C3 decision subset**: 20 decision queries (per F2 validation).

Operator-graded; results documented in Phase 5 release notes.

---

## 12. Acceptance criteria

| Chunk | Criterion | Evidence |
|---|---|---|
| 1 — Schema | All 13 columns + 3 tables added; migrations idempotent | `git diff` shows DDL; migration tests pass |
| 1 — Schema | All 12 v4alpha packages pass; `go vet` clean | `go test ./...` output |
| 2 — Dispatch | `RecallFor(vibe_case=X)` returns strategy-specific results | 7 unit tests |
| 2 — Dispatch | C3DecisionRecall hits ≥80% on MemHop-style questions | Micro-eval results |
| 3 — Code/Text/Research | C1 hits ≥99% on CodeCompass-style hidden-dependency tasks | Micro-eval results |
| 3 — Code/Text/Research | C2/C4 use BGE-large; FTS5 + vector + graph per strategy | Unit tests |
| 4 — Video/Audio/Multi | ImageBind adapter works for cross-modal queries | Unit tests with mock |
| 4 — Video/Audio/Multi | C5/C6 default to cross-modal 0.80 weight | Unit tests |
| 5 — Decay/Refresh/Rationale | DecayScore matches Fortunate Recall 10+1 ontology | Unit tests per kind |
| 5 — Decay/Refresh/Rationale | Refresh-on-access updates last_refreshed_at | Unit tests |
| 5 — Micro-eval | 50-question LifecycleBench-style answered | Operator-graded |
| 5 — Micro-eval | 100-question MemHop subset answered | Auto + operator graded |

---

## 13. Risks & open questions

### 13.1 Counter-evidence (per R-D F7)

- **ReFind**: agent-controlled search beats HippoRAG 2 by 5pp on 2,800 questions. Phase 5 alpha.19 should ship agent-controlled search as alternative.
- **FARMA**: 100% ASR on reasoning history forgery. SENTINEL reduces to 0% with no false positives. Phase 5 alpha.19 integrates.

### 13.2 Pipeline ordering (per R-F F7)

- MemClaw: sync gate can reject contradictory writes before async detector. Phase 5 design: write-first, async contradiction. Documented in §10.

### 13.3 Cost (per R-D F10)

- API embedders: $0.02/1M tokens. Re-index: $0.10-1.00 per 100 rows.
- Default alpha.18: local embedder (BGE/ONNX/ImageBind). $0/1k queries.
- Document cost in operator-facing guide.

### 13.4 Cold-start (per R-B I-6)

- Legacy rows have no embeddings. Phase 5 ships lazy indexing + save-time pre-embed.
- Backfill job is alpha.19.

### 13.5 Open questions

- (R-A F2) k=60 in RRF: validate empirically per strategy.
- (R-B F1) ImageBind ONNX export path.
- (R-B F9) Audio embedding quality vs transcript embeddings.
- (R-C F8) AI/ML decay rate (0.3× vs 0.5×).
- (R-D F3) CABLE link verification (LLM call vs local model).
- (R-E F5) Code-aware tokenizer quality on dark-memory's corpus.
- (R-F F1) Auto-detect supersession (alpha.19).

---

## 14. Cross-references

- **Research artifacts**: `docs/research/phase-5/{R-A,R-B,R-C,R-D,R-E,R-F}-*.md` (~2,400 LoC).
- **Prior spec**: `docs/specs/SPEC-alpha-11-phase4.md` (alpha.17, INV-19 namespace primitive).
- **Plan**: `docs/v4-alpha-11-plan.md` §5 (Phase 5 plan, items 11-13).
- **SOTA critique**: `docs/sota-critique.md` §8.3 (7 behind-SOTA gaps), §7.6.5 (decisions), §7.6.7 (memory subsystem).
- **Embedder docs**: `docs/embedders/{install,voyage,ollama,onnx}.md`.
- **ADR-008**: `docs/decisions/ADR-008-work-standard.md` (atomic mirror pattern).
- **Persona registry**: `docs/persona-registry-v4.md` (C3 mapping inconsistency to address).

---

## 15. Sign-off

- **Spec author**: Opita-AI (MiniMax-M3), 2026-09-30.
- **Operator authorization**: pending.
- **Drift check**: pending L3 (drift_judge vs 6 research artifacts).
- **Implementation chunks**: gated on L3 ALIGNED verdict.
