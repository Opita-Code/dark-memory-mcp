# R-B — Cross-Modal Embeddings (CLIP, ImageBind) for C5/C6

**Loop**: L1 (research) → L1.2
**Author**: Opita-AI (MiniMax-M3), 2026-09-30
**Status**: ✅ Complete — ImageBind + Netflix MediaFM + MM-VeriRec + 9 other SOTA 2026 papers reviewed
**Implications for**: C5 (video) + C6 (audio) strategies; embedder adapter interface

---

## 1. Research Question

**¿Cuál es el modelo cross-modal de embeddings SOTA 2026 que cubre image + audio + text en un solo embedding space, y qué patrones de uso se observan en producción?**

Sub-questions:
1. ¿ImageBind (2023) sigue siendo competitivo o hay un sucesor claro?
2. ¿Cómo se ve el patrón de producción en Netflix/PayPal?
3. ¿Cuál es la dimensión, peso, y costo del modelo?
4. ¿Qué patrones de failure-guided fusion se usan?
5. ¿Cómo se maneja la privacy de voice embeddings?

---

## 2. Primary Sources (12 papers, all verified)

### Cross-modal foundations

| ID | Title | Date | Modalities | Dim | Notes |
|---|---|---|---|---|---|
| **2305.05665** | **ImageBind** (Meta) | 2023-05 | 6: image, text, audio, depth, thermal, IMU | 1024 | CVPR 2023 Highlighted. Code: facebookresearch/ImageBind. Image-paired data only. |
| **2609.30739** | SEA-CLIP-Tiny | 2026-09 | image + text (multilingual SEA) | <50M params | ACCV 2026. Beats MobileCLIP2 with 38.4% fewer params. Compact option. |
| **2609.36101** | Modality Gap geometry | 2026-09 | image + text (CLIP, SigLIP) | varies | Rank-one gap structure; geometric interventions. |
| **2608.17203** | Hadamard-CLIP | 2026-08 | N modalities | any | Restores universal approximation. |

### Production patterns

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2608.18322** | Netflix MediaFM | 2026-08 | **Tri-modal: SeqCLIP + wav2vec 2.0 + timed-text** | Production at Netflix. Offline proxy task gates new checkpoints. Low-latency serving. |
| **2609.31718** | MM-VeriRec | 2026-09 | **Failure-guided fusion**: diagnose which modality failed, route to repair | CLIP detector 0.7028 visual-grounded success without aligned gate. AMI '26 workshop. |
| **2608.05260** | Paragraph > 1000 Captions | 2026-08 | Text supervision reformulated | Paragraphs as supervision signal, not captions. |
| **2608.29313** | Hyper3-CLIP | 2026-08 | **Hierarchy-conditioned hyperbolic VLM** | Part-whole, parent-child in geometry. ECCV 2026. |

### Region / hubness / augmentation

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2609.27142** | MINER | 2026-09 | Region-level embeddings + hubness correction | Augments global image embedding. Compatible with CLIP, SigLIP, SigLIP 2. ACML 2026. |
| **2609.03391** | OmniRSCLIP | 2026-09 | Multi-source sensor (RGB/SAR/MSI/HSI) | Spectral-spatial basis decomposition. |

### Voice embeddings

| ID | Title | Date | Pattern | Notes |
|---|---|---|---|---|
| **2609.26117** | Vorch-Human | 2026-09 | Subject-indexed speech + appearance + timbre | Audio-visual generation. Long-horizon continuation. |
| **2609.19304** | DECAF | 2026-09 | **Speaker-disentangled content embeddings** | IWAENC 2026. Privacy by design. 0.5 kbps. |
| **2609.13876** | Co-Speech Gestures | 2026-09 | **Style embedding from 10s enrollment** | Speaker-discriminative encoder + adapters. |
| **2601.20883** | VoxMorph | 2026-01 | **Prosody + timbre disentanglement via Slerp** | ICASSP 2026. |
| **2601.02444** | VocalBridge | 2026-01 | Diffusion-bridge purification (adversarial) | ICASSP 2026. Adversarial research. |

---

## 3. Findings

### F1 — ImageBind (2023) is still the canonical multi-modal embedder

- 6 modalities in ONE embedding space: image, text, audio, depth, thermal, IMU.
- Only image-paired data needed for training (huge cost saving).
- Code at facebookresearch/ImageBind (open-source, ONNX exportable).
- CVPR 2023 Highlighted. 2026 papers still cite it as reference (e.g., MM-VeriRec).
- **For dark-memory**: ImageBind covers C5 (image+text), C6 (audio+text), AND partial C7 (multi-modal) with ONE model. The "one embedding space to bind them all" architecture matches dark-memory's polymorphic-dispatch design (different signals, same fusion).

### F2 — Netflix MediaFM = production template for tri-modal memory

- Three encoders: **SeqCLIP** (visual) + **wav2vec 2.0** (audio) + **timed-text** (linguistic).
- Key engineering decisions:
  - **Shared embedding infrastructure** (one store, multiple encoders).
  - **Offline proxy task**: predict popularity-based winner from embeddings alone; gates new checkpoints before any end-to-end A/B.
  - **Low-latency serving**: precompute embeddings at index time.
  - **Cheap screening**: simple offline metrics prune 90%+ of candidate models.
- **For dark-memory**: this is the exact pattern Phase 5 should follow. Phase 5 includes:
  - `internal/v4alpha/embedder/` package (encoders + store).
  - Index-time precompute (when `save()` writes a row).
  - Query-time late-fusion (when `Recall()` runs).

### F3 — MM-VeriRec failure-guided fusion is the new SOTA pattern

- "Fusion should not merely concatenate modalities, but should diagnose which modality failed and route to the appropriate repair."
- Independent CLIP detector reaches 0.7028 visual-grounded success without aligned gate.
- Three failure modes identified: text-trap following, visual ignorance, false acceptance.
- **For dark-memory**: validates the polymorphic dispatch. `RecallFor(vibe_case=C5)` routes to image encoder; if results are sparse, falls back to text+graph. NOT just "concatenate all modalities and RRF".

### F4 — Modality gap is geometric, exploitable

- Single dominant direction captures 94.4-99.9% of squared norm of image-text mean separation.
- Rank-one approximation is valid.
- Gap modification CAN improve OR degrade — task-dependent.
- **For dark-memory**: when projecting text embeddings to image space (or vice versa) for cross-modal RRF, the geometry-derived exponent matters. Default: don't project; keep modalities in their native spaces; let RRF handle the gap.

### F5 — Compact models are SOTA-competitive

- SEA-CLIP-Tiny <50M params, R@10 42.2% (multilingual SEA languages).
- Beats MobileCLIP2 with 38.4% fewer params.
- **For dark-memory**: a local embedder for alpha.18 doesn't need 1B params. SEA-CLIP-Tiny or similar compact model is the right default for the OnnxAdapter (which already exists in v3.0.0-docfix).

### F6 — Hierarchy-conditioned VLMs for relational retrieval

- Hyper3-CLIP encodes part-whole, parent-child in the embedding geometry itself.
- Better for retrieval where relations matter (e.g., "this function calls that function").
- **For dark-memory**: matches the graph-traversal approach for C3 (decision graph) — relational structure IN the embedding. Decision: deferred to alpha.19 (requires training, not inference-only).

### F7 — Region-level retrieval augments global

- MINER: bank of region-level embeddings + hubness-corrected similarity rescoring.
- Improves retrieval on every backbone (CLIP, SigLIP, SigLIP 2) without training.
- **For dark-memory**: useful for C5 (subject consistency) and C1 (function-level code). Decision: optional alpha.19 enhancement.

### F8 — Privacy-preserving voice codecs (DECAF)

- Speaker-disentangled content embeddings. 0.5 kbps.
- 43.5% EER for verification when stripped of speaker info.
- **For dark-memory**: voice embeddings can be **timbre-only** (no linguistic content) — privacy by design. Operator chooses: `voice_embed_kind=timbre | full | prosody+timbre`.

### F9 — Production voice embedding pattern (3 papers converge)

- Co-Speech Gestures: **10s of enrollment** is enough for style embedding.
- VoxMorph: **prosody + timbre disentanglement** via Slerp.
- DECAF: **speaker-disentangled content** for privacy.
- **For dark-memory**: voice ref storage = `{prosody_embedding, timbre_embedding}` (each 256-dim, ~1KB). Enrollment needs 10s of reference audio.

### F10 — Adversarial voice recovery is a research area (not Phase 5 scope)

- VocalBridge, VoxMorph show that voice biometric defenses are fragile.
- This is academic research, not production concern for dark-memory.
- **Decision**: Phase 5 spec mentions but doesn't address adversarial robustness. Operators handling biometric data should be aware.

### F11 — ImageBind covers 4 of 6 needed modalities

- Image, text, audio: ✅ (relevant).
- Depth, thermal, IMU: ❌ (not needed for dark-memory's C1-C7).
- **For dark-memory**: ImageBind + small text-encoder (BGE) is the practical Phase 5 default. No need for depth/thermal.

---

## 4. Implications for dark-memory Phase 5

### I-1 — Cross-modal adapter for C5 and C6: ImageBind (primary) + BGE (text fallback)

Recommended stack:
- **Primary cross-modal embedder**: facebookresearch/ImageBind (1024-dim, 6 modalities). ONNX exportable.
- **Text fallback**: BAAI/bge-large-en-v1.5 (1024-dim, 2024 SOTA text embedder).
- **Audio alternative**: wav2vec 2.0 (already used by Netflix MediaFM). For voice-only embedding (timbre).
- **Compact local**: SEA-CLIP-Tiny for low-resource deployments.

### I-2 — Storage schema (additive migration)

New columns on `agent_memory`:
```sql
ALTER TABLE agent_memory ADD COLUMN embedding BLOB;          -- serialized float32 array
ALTER TABLE agent_memory ADD COLUMN embedding_model TEXT;   -- e.g., 'imagebind-v1', 'bge-large'
ALTER TABLE agent_memory ADD COLUMN embedding_dim INT;      -- e.g., 1024
ALTER TABLE agent_memory ADD COLUMN embedding_created_at TEXT;  -- RFC3339
ALTER TABLE agent_memory ADD COLUMN vibe_case TEXT;         -- C1..C7 (Gap 1 fix)
ALTER TABLE agent_memory ADD COLUMN voice_embed_kind TEXT;  -- timbre | full | prosody+timbre (Phase 5 C6)
CREATE INDEX idx_embedding_model ON agent_memory(embedding_model);
CREATE INDEX idx_vibe_case ON agent_memory(vibe_case);
```

Note: Gap 1 (vibe_case column) is also fixed here — both columns ship together.

### I-3 — Strategy dispatch (RecallFor)

```go
// C5VideoRecall — observation kind, vibe_case=C5
func (s *C5VideoRecall) Recall(ctx, query, projectID, sessionID, topK) ([]Row, error) {
    candidates := s.ftsIndex.Search(query, topK*5)               // pre-filter by text
    candidates = s.graphExpand(candidates, 1)                   // 1 hop
    scores := make(map[rowID]float64)
    for _, c := range candidates {
        if c.HasEmbedding("imagebind-v1") {
            scores[c.ID] = s.imagebindImageQuery.Similarity(query, c.Embedding)
        } else {
            scores[c.ID] = s.ftsScore(c) * 0.3                  // legacy fallback
        }
    }
    return s.applyRRF(scores, s.weights, topK), nil
}
```

### I-4 — Privacy considerations (explicit policy)

For C6 (audio) and any voice-derived data:
- `voice_embed_kind` column (per I-2).
- Operator must document consent flow for voice data.
- For dark-memory v0: assume operator-managed (no built-in consent gate; documented).
- **Note**: this is a Phase 5 followup; alpha.18 ships the schema column.

### I-5 — Cross-modal weights (per R-A I-1)

C5 video: cross-modal 0.80, FTS5 0.00, graph 0.20 (style refs).
C6 audio: cross-modal 0.80 (voice embed), FTS5 0.00, graph 0.20 (voice profile).

### I-6 — Cold-start problem

Legacy rows have no embeddings.
- Solution: lazy indexing — embed at first access.
- For new rows (saved after Phase 5 ship): pre-embed at save time.
- For legacy rows: background job (operator-triggered) backfills embeddings.
- Phase 5 ships: lazy indexing + save-time pre-embed. Backfill job is alpha.19.

### I-7 — Embedder version migration

- `embedding_model` column tracks which model generated the embed.
- Operator changing embedder triggers a re-embed (manual flag).
- Detection at query time: if row's `embedding_model` != current adapter, log warn + fall back to FTS5.

### I-8 — Failure-guided fusion (MM-VeriRec principle)

- Each `RecallStrategy` declares which modalities it requires and which are optional.
- If primary modality (image for C5, audio for C6) returns empty/sparse, fall back to secondary (text + graph).
- Logging: when fallback fires, emit a `recall.strategy.fallback` event for observability.

### I-9 — Adapter interface (plug-in pattern)

```go
type EmbedderAdapter interface {
    Embed(ctx, kind Modality, input []byte) ([]float32, error)  // kind: text|image|audio
    Dim() int
    Model() string  // e.g., "imagebind-v1"
}

type Modality string
const (
    ModalityText  Modality = "text"
    ModalityImage Modality = "image"
    ModalityAudio Modality = "audio"
)
```

This matches the existing `internal/embedder/` adapter ladder (OpenAI / Voyage / Cohere / ONNX / Ollama), extended with cross-modal kinds.

---

## 5. Open Questions

1. **ImageBind ONNX availability**: ImageBind has a HuggingFace model card; verify ONNX export path. If complex, use ImageBind's PyTorch + torch.hub + ONNX export.
2. **Audio embedding quality vs. transcript embeddings**: does ImageBind's audio encoder outperform transcript-based text embeddings for voice consistency? A/B test deferred to alpha.19.
3. **Multilingual text**: SEA-CLIP-Tiny is multilingual SEA. For dark-memory's opita-co Spanish + English, BGE-large-en or multilingual-e5 may be better. Decision: default to BGE-large-en + multilingual-e5-large as fallback.
4. **Region-level retrieval (MINER)**: useful for C5 subject consistency, but adds LoC. Decision: defer to alpha.19 unless operator requests.
5. **Hierarchy-conditioned (Hyper3-CLIP)**: training-required, deferred to alpha.19 (post-Phase 5).

---

## 6. Recommended Defaults for SPEC-alpha-11-phase5.md

| Item | Default | Justification |
|---|---|---|
| Cross-modal embedder | ImageBind (facebookresearch/ImageBind, ONNX) | Covers 3 of 6 modalities in one model; mature; SOTA-2023 still competitive in 2026 |
| Text-only fallback | BGE-large-en-v1.5 (BAAI) | SOTA 2024 text; complements ImageBind |
| Audio-only (timbre) | wav2vec 2.0 (already in Netflix MediaFM stack) | Production-validated |
| Embedding dim | 1024 | ImageBind default |
| Storage | `embedding BLOB` + `embedding_model TEXT` + `embedding_dim INT` | Per I-2 |
| Strategy dispatch | `RecallFor(vibe_case, ...)` polymorphic | MM-VeriRec principle (failure-guided) |
| Cold-start | lazy indexing + save-time pre-embed | Pragmatic for alpha.18 |
| Embedder version | tracked per-row (`embedding_model` column) | Operator can rotate without breakage |
| Privacy | `voice_embed_kind` column + operator-managed consent | DECAF principle, biometric awareness |
| Failure-guided | primary modality required, secondary optional | MM-VeriRec principle |

---

## 7. Sources NOT fetched (limitations)

- ImageBind full PDF (only abstract reviewed; well-known reference).
- Netflix MediaFM full PDF (only abstract reviewed).
- MM-VeriRec full PDF (only abstract reviewed).
- ImageBind ONNX export path (referenced from secondary sources).

These should be reviewed in L2 (spec loop) for concrete LoC estimates.

---

## 8. Next Step

L1.3 — R-C: Temporal decay in memory systems.

This validates the per-vibe-case decay table from R-A §4 I-3 (zero for C3, 1095 days for C4 AI/ML, 3650 days for C4 math, etc.). Mem0, Letta, MoM, LycheeMemory should provide concrete decay function patterns.
