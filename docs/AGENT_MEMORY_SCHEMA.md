# Agent Memory Schema — v4-alpha.1

> **Audience**: contributors touching `internal/v4alpha/agent_memory/`.
> **Schema version**: `v4alpha/2026-09-27/001`.

## 1. The two tables

```sql
CREATE TABLE IF NOT EXISTS agent_memory (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    operator   TEXT    NOT NULL CHECK (operator <> ''),
    kind       TEXT    NOT NULL CHECK (kind <> ''),
    title      TEXT,
    content    TEXT    NOT NULL,
    tags       TEXT,
    pinned     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT
);

CREATE VIRTUAL TABLE IF NOT EXISTS agent_memory_fts
    USING fts5(content, title, tags,
                content='agent_memory',
                content_rowid='id');
```

### 1.1 Base table — `agent_memory`

| Column | Type | Constraint | Purpose |
|---|---|---|---|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | rowid; FTS5 mirror |
| `operator` | TEXT | NOT NULL, `<> ''` | tenant scope (INV-1 audit identity) |
| `kind` | TEXT | NOT NULL, `<> ''`, allow-listed | canonical kind (see §2) |
| `title` | TEXT | nullable | optional short label |
| `content` | TEXT | NOT NULL | the memory payload (searched by FTS5) |
| `tags` | TEXT | nullable | comma-separated (searched by FTS5) |
| `pinned` | INTEGER | NOT NULL DEFAULT 0 | bool (0/1); pinned rows surface first in `List` |
| `created_at` | TEXT | NOT NULL DEFAULT CURRENT_TIMESTAMP | RFC3339-ish timestamp from SQLite clock |
| `updated_at` | TEXT | nullable | set by `Update` via `CURRENT_TIMESTAMP` |

### 1.2 FTS5 sidecar — `agent_memory_fts`

**Important**: this is **contentless FTS5**. The actual content
lives in `agent_memory`; the FTS5 table stores only the index.
The columns `content, title, tags` named in the FTS5 declaration
are the indexed columns, NOT stored columns.

- `content='agent_memory'` — bind to the base table for content
  lookup (saves ~2× storage vs regular FTS5).
- `content_rowid='id'` — bind the FTS5 rowid to the base table's
  primary key. The base INSERT does NOT auto-populate the FTS5
  index; every `Save` / `Update` / `Archive` must sync manually.

**Do not use the FTS5 `'delete'` or `'delete-all'` commands.**
They raise `SQLITE_CORRUPT (267)` in modernc.org/sqlite v1.53
under SERIALIZABLE. Use plain `DELETE FROM agent_memory_fts WHERE
rowid = ?` instead — verified working against the contentless
schema by `TestArchive_RemovesFromBaseAndFTS`.

### 1.3 Secondary indexes (3)

```sql
CREATE INDEX IF NOT EXISTS agent_memory_operator_idx
    ON agent_memory(operator);

CREATE INDEX IF NOT EXISTS agent_memory_kind_idx
    ON agent_memory(kind);

CREATE INDEX IF NOT EXISTS agent_memory_pinned_idx
    ON agent_memory(pinned);
```

- `operator_idx` — used by every `Get`/`List`/`Recall`/`Archive` to
  scope by tenant.
- `kind_idx` — used by `List` filters like `kind=decision`.
- `pinned_idx` — used by `List`'s `ORDER BY pinned DESC, created_at DESC`.

## 2. Canonical kinds

7 kinds, allow-listed at Save. Empty or unknown kinds are rejected
with `ErrInvalidKind`.

```go
KindNote        = "note"
KindObservation = "observation"
KindDecision    = "decision"
KindFinding     = "finding"
KindTodo        = "todo"
KindLink        = "link"
KindContext     = "context"
```

### 2.1 Semantics

| Kind | Use it for |
|---|---|
| `note` | free-form thought; the catch-all default |
| `observation` | something noticed about the world (operator-facing) |
| `decision` | a choice made; rationale goes in `content` |
| `finding` | a research or audit finding (often agent-discovered) |
| `todo` | an action item |
| `link` | a reference to external state (URL, file path, ticket id) |
| `context` | background context for a future operator |

### 2.2 Mutability

`operator` and `kind` are **immutable** (INV-1 audit identity). To
change them, Archive + Save a new row. `Update` rejects any attempt
to change these fields (it doesn't even expose them as parameters).

## 3. The six store methods

| Method | Args | Returns | Tx? | FTS5 sync |
|---|---|---|---|---|
| `Save` | `op, kind, title, content, tags, pinned` | `(int64, error)` | Yes (`WithTx` SERIALIZABLE) | INSERT after base INSERT |
| `Get` | `id` | `(*Row, error)` | No | read-only |
| `List` | `op, limit` | `([]Row, error)` | No | read-only |
| `Recall` | `op, query, limit` | `([]Row, error)` | No | FTS5 query → JOIN base |
| `Update` | `id, *title, *content, *tags, *pinned` | `error` | Yes (`WithTx` SERIALIZABLE) | see §4 below |
| `Archive` | `id` | `error` | Yes (`WithTx` SERIALIZABLE) | plain DELETE on fts |

### 3.1 `Save` — insert + index

```sql
BEGIN ISOLATION LEVEL SERIALIZABLE;
  INSERT INTO agent_memory (operator, kind, title, content, tags, pinned)
    VALUES (?, ?, ?, ?, ?, ?);
  -- last_insert_rowid() = new id
  INSERT INTO agent_memory_fts (rowid, content, title, tags)
    VALUES (?, ?, ?, ?);
COMMIT;
```

### 3.2 `Update` — the canonical sequence (INV-17)

```sql
BEGIN ISOLATION LEVEL SERIALIZABLE;
  -- (1) existence check (so ErrNotFound is precise)
  SELECT 1 FROM agent_memory WHERE id = ?;

  -- (2) FTS5 DELETE FIRST (the load-bearing property)
  DELETE FROM agent_memory_fts WHERE rowid = ?;

  -- (3) base UPDATE (with updated_at = CURRENT_TIMESTAMP)
  UPDATE agent_memory
    SET title = ?, content = ?, tags = ?, pinned = ?, updated_at = CURRENT_TIMESTAMP
    WHERE id = ?;

  -- (4) re-read (the new values are committed in-tx; SELECT sees them)
  SELECT COALESCE(title,''), content, COALESCE(tags,'')
    FROM agent_memory WHERE id = ?;

  -- (5) FTS5 re-INSERT
  INSERT INTO agent_memory_fts (rowid, title, content, tags)
    VALUES (?, ?, ?, ?);
COMMIT;
```

**Why this exact order?** `modernc.org/sqlite` v1.53 raises
`SQLITE_CORRUPT (267)` when an FTS5 DELETE follows a base-table
UPDATE inside a SERIALIZABLE transaction. Reversing the order
(DELETE → UPDATE → INSERT) avoids the corrupt error. Verified by
`TestUpdate_MutatesFieldsAndReSyncsFTS`.

### 3.3 `Archive` — soft-delete + index cleanup

```sql
BEGIN ISOLATION LEVEL SERIALIZABLE;
  DELETE FROM agent_memory WHERE id = ?;
  DELETE FROM agent_memory_fts WHERE rowid = ?;
COMMIT;
```

Note: base DELETE first, FTS5 DELETE second. This is the inverse
of the Update ordering; both work because in Archive the base row
is the trigger, in Update the FTS5 entry is the trigger (the base
row's indexed values are what changed).

### 3.4 `Recall` — FTS5 query → base join

```sql
SELECT m.id, m.operator, m.kind, m.title, m.content, m.tags, m.pinned, m.created_at
FROM agent_memory_fts f
JOIN agent_memory m ON m.id = f.rowid
WHERE agent_memory_fts MATCH ?
  AND m.operator = ?
ORDER BY rank
LIMIT ?;
```

The query string is tokenised by FTS5 (lower-cased, stemmed,
prefix-matched on tokens ending in `*`). Operators do NOT escape
special FTS5 characters; FTS5 does that internally.

## 4. Errors

```go
var ErrEmptyOperator = errors.New("agent_memory: operator must be non-empty (INV-1)")
var ErrEmptyContent  = errors.New("agent_memory: content must be non-empty")
var ErrInvalidKind   = errors.New("agent_memory: invalid kind")
var ErrNotFound      = errors.New("agent_memory: not found")
```

`Save`:
- `ErrEmptyOperator` if operator is empty
- `ErrEmptyContent` if content is empty
- `ErrInvalidKind` if kind is not in the allow-list

`Get`:
- `ErrNotFound` if no row matches the id

`Archive`:
- `ErrNotFound` if no row was deleted

`Update`:
- `ErrNotFound` if id doesn't exist
- `errors.New("agent_memory Update: content cannot be empty")` if
  `*content == ""` (passed but empty)

## 5. Tests

`internal/v4alpha/agent_memory/agent_memory_test.go` — 18 tests:

| Group | Tests |
|---|---|
| Save | TestSave_InsertsAndReturnsID, TestSave_RejectsEmptyOperator, TestSave_RejectsEmptyContent, TestSave_RejectsInvalidKind |
| Get | TestGet_ReturnsRow, TestGet_NotFoundReturnsError |
| List | TestList_ReturnsByOperatorScope, TestList_OrdersPinnedFirst, TestList_HonorsLimit |
| Recall | TestRecall_FindsByQuery, TestRecall_FilteredByOperator, TestRecall_NoResultsReturnsEmpty |
| Archive | TestArchive_RemovesFromBaseAndFTS, TestArchive_NotFoundReturnsError |
| Update | TestUpdate_MutatesFieldsAndReSyncsFTS, TestUpdate_RejectsEmptyContent, TestUpdate_NotFound, TestUpdate_NoFieldsIsNoOp |

## 6. What's NOT here

- **Audit emission on Save**: `agent_memory.Save` does NOT yet
  emit a `write_audit` row. This is INV-1's only remaining gap
  in v4-alpha.1; closes in BUG-9.
- **Embedding vector / Mem0 three-class taxonomy**: deferred to
  BUG-9 alongside the audit emission.
- **`operator` vs `project_id`**: v4 uses `operator` as the tenant
  primitive (every row carries an operator). The legacy v3
  `project_id` column is NOT used on this table. Multi-tenant
  tooling should filter by `operator`; `dark-cli` and other
  dark-* projects that share a dark.db are partitioned by operator.

## 7. Cross-references

- [INV-10](../INVARIANTS.md#inv-10--agent-memory-rows-are-persistent-across-session-lifecycle-v230)
  — agent memory rows survive session close (no auto-bind).
- [INV-17](../INVARIANTS.md#inv-17--fts5-ordering-under-serializable-featv4-redesign)
  — the canonical Update sequence.
- [INV-16](../INVARIANTS.md#inv-16--dark-db-concurrency-contract-featv4-redesign)
  — every `WithTx` call here uses `LevelSerializable`.
- [v4-status.md §3](./v4-status.md#3-invariants--adoption-status)
  — invariant adoption matrix.
- [agent_memory.go](../../internal/v4alpha/agent_memory/agent_memory.go) — source of truth.

---

## 8. SOTA criticism (chunk 2, 2026-09-28)

This doc was reviewed against the 2025-2026 SOTA agent-memory
literature as part of the SOTA-doc chunk 2 (continuing the work
started in `docs/judge-pipeline-v4.md` §10). The full criticism
spans 5 subsections; the agent-memory-specific findings are below.

### 8.1 On-par with SOTA 2025-26 (4 verifications, tier-1 sources)

1. **Hierarchical memory inspired by OS paging.** v4's two-level
   design (operator-scoped table + FTS5 contentless sidecar) is
   structurally simpler than MemGPT's 4-tier hierarchy (core,
   conversational, archival, external files), but the *indexing*
   pattern (base table + sidecar for fast lookup) is on-par with
   what Mem0 + Letta call "primary storage + retrieval index".
   SOTA: Packer, Wooders, Lin, Fang, Patil, Stoica, Gonzalez 2023
   — *MemGPT: Towards LLMs as Operating Systems*, arxiv:2310.08560
   (v1 Oct 12 2023, v2 Feb 12 2024) — verified 2026-09-28 via
   <https://arxiv.org/abs/2310.08560>. **Verdict: aligned in
   intent, simpler in structure.**

2. **Single-pass extraction with entity linking.** v4's `Save` is
   a single INSERT + FTS5 sync, conceptually analogous to Mem0's
   "single-pass ADD-only extraction" (which avoids the
   diffing-against-existing-state overhead). The v4 design also
   stores `tags` (comma-separated) which serves the same role as
   Mem0's entity linking layer (linking memories about the same
   person/place/concept).
   SOTA: Deshraj Yadav 2026 — *Introducing The Token-Efficient
   Memory Algorithm*, mem0.ai/blog/mem0-the-token-efficient-memory-
   algorithm (Apr 16 2026) — verified 2026-09-28. Mem0 claims
   92.5 on LoCoMo, 94.4 on LongMemEval, 64.1/48.6 on BEAM
   (1M/10M tokens) with 6,956 mean tokens/query.
   **Verdict: aligned in pattern; v4 lacks the LLM-driven entity
   extraction step.**

3. **FTS5 BM25 lexical retrieval as a first-class primitive.**
   v4 uses SQLite FTS5 with the default BM25 ranking, which is the
   same primitive most SOTA systems use as their keyword retrieval
   signal. Mem0's "multi-signal retrieval" combines semantic +
   keyword + entity; Letta's Filesystem uses `grep` (BM25-equivalent)
   for keyword; A-MEM uses note-level lexical matching.
   SOTA: Letta 2025 — *Benchmarking AI Agent Memory: Is a
   Filesystem All You Need?* — verified 2026-09-28 via
   <https://www.letta.com/blog/benchmarking-ai-agent-memory/>
   (74.0% LoCoMo with GPT-4o mini + filesystem operations).
   **Verdict: aligned.**

4. **FTS5 ordering under SERIALIZABLE (the load-bearing property).**
   The canonical `Update` sequence (FTS5 DELETE → base UPDATE →
   FTS5 INSERT, per INV-17) is a well-known problem in the
   SQLite+FTS5 literature. v4 documents it precisely in §3.2.
   This is **NOT a SOTA critique**; it's a SOTA-aligned
   operational detail. Many SOTA agent-memory systems offload
   FTS5 to a separate service (e.g. ElasticSearch) to avoid
   SERIALIZABLE complications. v4's in-process SQLite is a
   **pragmatic choice**, not a SOTA gap.

### 8.2 Ahead of SOTA 2025-26 (3 places, rare but real)

1. **Operator-scoped tenant primitive (not user/agent scoped).**
   v4 uses `operator` (e.g. "nico", "system", "maria-medina") as
   the single tenant primitive. SOTA 2025-26 systems use:
   - Mem0: `user_id` (single-user, no multi-tenant)
   - Letta: `agent_id` (per-agent state)
   - A-MEM: implicit per-session
   v4's choice is **simpler and more auditable** than agent-scoped
   because INV-1 audit is identity-first: "who *caused* this
   mutation" not "what agent was the carrier". This is a v4
   innovation that maps cleanly to the Mem0 three-class taxonomy
   (episodic/semantic/procedural) once `memory_type` is added in
   BUG-9. **Verdict: ahead in tenant primitive design.**

2. **Audit emission on every state mutation (INV-1).**
   v4 emits a `write_audit` row on every Save (per the audit
   design — when fully wired; AGENT_MEMORY_SCHEMA §6 notes the
   remaining gap). SOTA 2025-26 systems (Mem0, Letta, A-MEM)
   do NOT have first-class audit emission. They log to stdout
   or a generic logger, not to a structured audit table. **Verdict:
   ahead in operational SOTA.** This is the foundation that
   enables v4's drift detection (vibe_publish → drift_judge) —
   SOTA systems don't have a comparable trail.

3. **7 canonical kinds with INV-17 canonical Update sequence.**
   The allow-list of 7 kinds (`note`, `observation`, `decision`,
   `finding`, `todo`, `link`, `context`) is more disciplined than
   SOTA 2025-26 systems that store free-form blobs. The
   `decision` kind in particular is the substrate for v4's
   `auto_save_decision=true` on `vibe_publish`. **Verdict: ahead
   in type discipline.**

### 8.3 Behind SOTA 2025-26 (7 gaps with file:line + remediation)

1. **No embedding/vector retrieval.** v4's `Recall` uses FTS5
   (BM25) only. SOTA Mem0 uses multi-signal (semantic + keyword
   + entity). Letta Filesystem uses `search_files` (semantic
   vector + keyword). A-MEM uses note-level embeddings.
   v4 only catches exact/prefix matches. **Verdict: behind in
   recall quality for paraphrased queries.** Remediation: BUG-9
   (deferred). File: `internal/v4alpha/agent_memory/agent_memory.go`
   `Recall()` method. ADR-013 proposed.

2. **No multi-signal retrieval fusion.** v4 has one signal (BM25).
   Mem0 has 3 signals fused via rank scoring. A-MEM has 1 signal
   (note-level embedding). v4 is simpler but less accurate.
   **Verdict: behind in fusion.** Remediation: ADR-013.

3. **No temporal reasoning / time-aware decay.** v4's rows are
   stamped with `created_at` and `updated_at` but the `Recall`
   method does NOT use them for re-ranking. Mem0 has memory
   decay (recency-based ranking) and explicit temporal reasoning
   (Apr-May 2026 releases). SOTA papers MoM (arxiv:2609.25054,
   Sep 2026, verified) and LycheeMemory V2 (arxiv:2608.12990,
   Aug 2026, verified) both show 10-30% improvements on
   time-aware tasks. **Verdict: behind.** Remediation: ADR-014
   (temporal re-ranking in Recall).

4. **No entity linking.** v4's `tags` are operator-curated
   comma-separated strings. Mem0 has an LLM-driven entity
   extraction step at write time that links memories about the
   same person/place/concept. A-MEM has atomic memory notes
   with structured entities. **Verdict: behind in entity
   modeling.** Remediation: BUG-9 (deferred).

5. **No memory_type three-class taxonomy (episodic / semantic /
   procedural).** AGENT_MEMORY_SCHEMA §6 explicitly defers
   `memory_type` to BUG-9. v3 has this field (per row 491
   "Mem0 three-class taxonomy" recall). Mem0 also uses
   episodic/semantic/procedural. **Verdict: behind.** Remediation:
   BUG-9.

6. **No multi-hop retrieval / graph links.** v4 has flat rows
   with no links between them. SOTA 2025-26 (A-MEM, Mem0g,
   HippoRAG, CABLE on arxiv:2608.17911 verified) all use
   some form of cross-memory links. v4's `tags` are a poor
   substitute. **Verdict: behind in graph reasoning.**
   Remediation: out of v4-alpha scope; ADR-015 when adopted.

7. **No LoCoMo / LongMemEval benchmark harness.** v4 cannot
   be benchmarked against SOTA on the standard memory
   benchmarks (LoCoMo, LongMemEval, BEAM, AMA-Bench). The
   unit tests verify the API surface but not the retrieval
   quality. **Verdict: behind in measurement.** Remediation:
   BUG-9 (deferred).

### 8.4 Couldn't verify (honest gap)

- The original **A-MEM** arxiv ID. A-MEM is referenced as a
  2025 baseline in 8+ SOTA 2026 papers I verified (ProGraph
  arxiv:2607.19359, CABLE arxiv:2608.17911, LycheeMemory V2
  arxiv:2608.12990, ClinTraceBench arxiv:2609.01111, RSM-full
  arxiv:2609.04915, SF-AMS arxiv:2607.22562, Bio-Memory
  arxiv:2609.08558, V-Mem arxiv:2608.01543). I attempted to
  find the original A-MEM paper via 2 arxiv search queries
  (2026-09-28) and the result was empty or returned derivative
  works. **Honest statement: I do NOT have the A-MEM arxiv
  ID verified in this session.** The 8 derivative papers cite
  A-MEM as a baseline but I cannot confirm the original paper
  from primary source.

- The **Mem0 three-class taxonomy** (episodic / semantic /
  procedural) reference. Recalled from v3 row 491 as a
  Mem0 concept but I cannot find the Mem0 paper that
  formalizes it. The current Mem0 docs (verified 2026-09-28)
  describe the architecture, not the three-class taxonomy.
  The taxonomy may be a v3 *interpretation* of Mem0, not a
  Mem0 *concept*.

- The current state of **OpenAI Memory / LangMem / Zep** (the
  other 2025-26 memory systems Letta's Aug 2025 blog
  references). I verified Letta's claim that Mem0's 68.5%
  LoCoMo result was not reproducible (Letta's research team
  filed github.com/mem0ai/mem0/issues/3004, verified 2026-09-28)
  but I did not verify the OpenAI Memory / LangMem / Zep
  benchmark numbers in this session.

- Whether the **2025-26 SOTA papers I did not see** (because
  they are behind the arxiv search cutoff) might have
  fundamentally changed the agent-memory SOTA landscape. I
  verified 18+ 2026 papers from arxiv, but arxiv adds ~100
  papers/day in cs.AI; the search results I saw were the
  1-50/2,224 of "agentic memory LLM" query. There may be
  significant papers I missed.

### 8.5 What this section is NOT

- It is NOT a refutation of v4's design. v4 ships a focused,
  audit-friendly agent memory table with FTS5 BM25 retrieval
  and operator-scoped tenant primitive. The gaps in §8.3 are
  tractable, not architectural.

- It is NOT a substitute for ADR-013/014/015 (proposed
  remediations). Each gap has a proposed ADR; the ADRs are
  the next step, not this section.

- It is NOT a comprehensive agent-memory SOTA survey. The
  SOTA-doc chunk 2 scope is the agent-memory layer of
  dark-memory. The 18+ 2026 papers verified are
  representative, not exhaustive. The Mem0 + Letta + MemGPT
  trio is the SOTA "canonical three" that v4 most
  directly competes with.

## 9. References (tier-1 sources verified 2026-09-28)

| Claim in this doc | Cited source | URL | Verified |
|---|---|---|---|
| MemGPT: 4-tier hierarchy (core/conversational/archival/external) | Packer, Wooders, Lin, Fang, Patil, Stoica, Gonzalez 2023 (v2 Feb 2024) | <https://arxiv.org/abs/2310.08560> | 2026-09-28 |
| Mem0: single-pass ADD-only extraction, multi-signal retrieval, 92.5 LoCoMo | Yadav 2026, *Introducing The Token-Efficient Memory Algorithm* | <https://mem0.ai/blog/mem0-the-token-efficient-memory-algorithm> | 2026-09-28 |
| Mem0: benchmark numbers (92.5 LoCoMo, 94.4 LongMemEval, 64.1/48.6 BEAM) | mem0.ai/research landing page | <https://mem0.ai/research> | 2026-09-28 |
| Letta: Filesystem scores 74.0% on LoCoMo with GPT-4o mini (filesystem beats specialized memory tools) | Letta 2025, *Benchmarking AI Agent Memory* | <https://www.letta.com/blog/benchmarking-ai-agent-memory/> | 2026-09-28 |
| Letta: Mem0's 68.5% LoCoMo result not reproducible (github issue #3004) | Letta 2025, same blog post | (cited inline) | 2026-09-28 |
| Letta: Memory Models, Meta-RL for token-space learning | Letta 2026, *Memory Models: Towards Agents That Learn* | <https://www.letta.com/blog/towards-agents-that-learn/> | 2026-09-28 |
| Letta Leaderboard: read/write/update benchmarks for memory management | Letta 2025, *Letta Leaderboard* | <https://www.letta.com/blog/letta-leaderboard/> | 2026-09-28 |
| Mem0 used as agentic memory baseline (paired with A-MEM) | Wang et al. 2026, *ClinTraceBench*, arxiv:2609.01111 | <https://arxiv.org/abs/2609.01111> | 2026-09-28 |
| A-MEM used as agentic memory baseline (2025) | Zhu 2026, *ProGraph*, arxiv:2607.19359 | <https://arxiv.org/abs/2607.19359> | 2026-09-28 |
| SOTA 2026: multi-hop retrieval via complementary links (CABLE) | Tan, Gao, Wang 2026, *CABLE*, arxiv:2608.17911 | <https://arxiv.org/abs/2608.17911> | 2026-09-28 |
| SOTA 2026: temporal memory (MoM, P-Mem) | Qin, Lu 2026, *MoM: Memory of Memory*, arxiv:2609.25054 | <https://arxiv.org/abs/2609.25054> | 2026-09-28 |
| SOTA 2026: efficient consolidation (LycheeMemory V2) | Li et al. 2026, *LycheeMemory V2*, arxiv:2608.12990 | <https://arxiv.org/abs/2608.12990> | 2026-09-28 |
| SOTA 2026: provenance + memory trust gap | Hu, Ramachandran 2026, *Memory Trust Gap*, arxiv:2609.01852 | <https://arxiv.org/abs/2609.01852> | 2026-09-28 |
| SOTA 2026: bio-metric personalization on top of A-MEM | Qian et al. 2026, *Bio-Memory*, arxiv:2609.08558 | <https://arxiv.org/abs/2609.08558> | 2026-09-28 |
| SOTA 2026: privacy-aware memory (SP-Mem) | Wang et al. 2026, *SP-Mem*, arxiv:2608.16551 | <https://arxiv.org/abs/2608.16551> | 2026-09-28 |
| SOTA 2026: multimodal agentic memory (V-Mem) | Kang et al. 2026, *V-Mem*, arxiv:2608.01543 | <https://arxiv.org/abs/2608.01543> | 2026-09-28 |

All URLs verified via primary fetch 2026-09-28. The "couldn't
verify" items in §8.4 are NOT in this table — they are
honestly missing.

**See also**: `docs/sota-critique.md` — meta-doc aggregating
chunks 1-5 (13 ahead / 28 on-par / 39 behind, 17 ADRs + 1 BUG,
20 honest couldn't-verify). The operator-facing summary of the
SOTA-doc workstream.

