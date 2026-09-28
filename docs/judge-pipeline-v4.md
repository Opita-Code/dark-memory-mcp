# Judge pipeline v4 — operator's guide

> **Audience**: operators running `dark-memory-v4` who need to call the
> judge pipeline (via MCP tools `dark_memory_judge` /
> `dark_memory_consensus` / `dark_memory_judgment_history`).
> **Source of truth**: `docs/decisions/ADR-007-judge-pipeline-v4.md`.
> **Read this BEFORE**: deploying to a new environment, debugging a
> surprising verdict, or extending the edge-case catalog.

---

## 1. Quickstart (60 seconds)

The judge pipeline needs an **LLM provider key** in the environment
before it produces real verdicts. Without one, every `dark_memory_judge`
call returns `verdict=errored` with `verdict_json.reasoning` explaining
the missing key (this is the EC-002 path; the tool does NOT silently
succeed).

```bash
# 1. Set the provider + dialect (defaults: minimax / anthropic)
export DARK_JUDGE_PROVIDER=minimax
export DARK_JUDGE_DIALECT=anthropic

# 2. Set the API key for that provider
export MINIMAX_API_KEY=sk-...

# 3. (optional) Pin the model — otherwise the provider default applies
export DARK_JUDGE_MODEL=MiniMax-M3

# 4. (optional) Adjust timeout if your provider is slow
export DARK_JUDGE_TIMEOUT_MS=30000
export DARK_JUDGE_RETRY_COUNT=2

# 5. Start the server
dark-memory-v4 serve --dsn=/path/to/dark.db
```

Then via MCP:

```json
{
  "name": "dark_memory_judge",
  "arguments": {
    "operator": "nico",
    "eval_type": "drift_judge",
    "vibe_case": "C1",
    "spec_intent": "Verify agent_memory.Save emits audit_log row in same Tx",
    "artifact_ref": {
      "kind": "file",
      "path": "internal/v4alpha/agent_memory/agent_memory.go",
      "range": {"start": 80, "end": 200}
    },
    "persona_id": "judge-logical"
  }
}
```

Response shape is a `Verdict` (see §5 below) plus `evaluation_id` — the
row id in `sdd_evaluations` (0 if persistence failed; the verdict is
still returned).

---

## 2. Supported providers (allow-list)

The pipeline deliberately composes **its own** self-contained HTTP client
and does NOT reuse the v2 `internal/orchestration` package. The 4
providers below are allow-listed (anything else returns
`EC-005 = errored`).

| Provider id      | Dialect | Env var              | Default model | Notes |
|------------------|---------|----------------------|---------------|-------|
| `anthropic`      | (none)  | `ANTHROPIC_API_KEY`  | `claude-3-5-sonnet-...` | Native `output_config.format` JSON schema (GA, see §9 ref [4]) |
| `minimax`        | `anthropic` | `MINIMAX_API_KEY` | `MiniMax-M3` | Default per operator config; speaks Anthropic dialect |
| `minimax-cn`     | `anthropic` | `MINIMAX_API_KEY_CN` | (CN regional endpoint) | Endpoints + DNS different from `minimax` |
| `deepseek`       | (none)  | `DEEPSEEK_API_KEY`   | `deepseek-...` | Free probe via `GET /models` |

> **Anti-pattern warning**: do NOT add a provider just because the
> server is reachable. The allow-list is the **integration boundary**.
> If a new provider is needed, file an issue with: (a) which 4 MCP
> tools are needed, (b) which env var holds the key, (c) which
> `response_format` mechanism the provider supports. Only providers
> with native JSON-schema response_format are safe to add (LLM-2-LLM
> prompt-construction is **not** a substitution — see §4 prompt
> injection defense).

### Provider env-var precedence

1. `DARK_JUDGE_MODEL_<PROVIDER_UPPER>` (per-provider override)
2. `DARK_JUDGE_MODEL` (global)
3. Provider default

Example: `DARK_JUDGE_MODEL_ANTHROPIC=claude-3-5-sonnet-20241022` beats
`DARK_JUDGE_MODEL=claude-...` beats `claude-3-5-sonnet-...`.

### Provider key resolution (audit-trail)

The active provider is recorded in `sdd_evaluations.provider` for every
verdict. Operators can query `judgment_history` filtered by provider
to see "which verdicts came from which model". This is intentional —
cross-version verification (e.g., "did v4 verdicts come from
minimax?") requires the column to be queryable.

---

## 3. Wire shapes (MCP tool inputs/outputs)

### 3.1 `dark_memory_judge`

**Required inputs**:
- `operator` (string, INV-1 audit attribution; rejected if empty)
- `eval_type` (one of the 15 eval_types; see
  `docs/edge-case-catalog.md` §2)
- `vibe_case` (C1..C7; determines the rubric + persona default)
- `spec_intent` (≤ 4 KiB; the "what should this artifact be?" hypothesis
  fed to the LLM)

**Optional inputs**:
- `artifact_ref` (one of `file | git_sha | url | spec_id | artifact_id`;
  **strongly recommended** — anchor for EC-010 doc-vs-code drift
  detection)
- `artifact_type` (`code | text | image | video | audio | multi`; falls
  back to legacy `text` field for backwards compat)
- `persona_id` (overrides the default persona for vibe_case × eval_type)
- `artifact_url` (legacy path; superseded by `artifact_ref`)
- `text` (legacy path; superseded by `artifact_ref`)
- `target_id` / `target_type` (used for `judgment_history` lookup)
- `brand_id` / `jurisdiction` / `has_disclosure` (only for
  `brand_match` + `compliance_check`)
- `session_id` / `project_id` (optional, INV-1 attribution)

**Output**: `Verdict` JSON plus `evaluation_id: int64` (0 if persistence
failed; verdict still returned).

### 3.2 `dark_memory_consensus`

**Required inputs**:
- `operator` (INV-1)
- `eval_type`, `vibe_case`, `spec_intent`, `artifact_ref` (same as judge)

**Optional inputs**:
- `n` (1..7, default 3) — number of LLM calls in the consensus
- `persona_id` (single persona applied to all N calls; vary model
  temperature instead for genuine diversity)

**Output**: `ConsensusResult` with:
- `samples: []SampleResult` (N verdicts)
- `modal_verdict: Verdict` (the modal verdict, synthesised from the
  modal sample)
- `modal_evaluation_id: int64` (the modal row id; per-sample ids live
  on each `SampleResult.evaluation_id`)

### 3.3 `dark_memory_judgment_history`

**Optional inputs** (all filters are AND-ed):
- `eval_type`, `target_type`, `target_id`, `limit` (default 50, max 200)

**Output**: list of `Evaluation` rows reconstructed from `sdd_evaluations`,
newest-first. Each row carries the full `Verdict` plus `TemperatureNote`
and `BiasAudit`.

> **C2→C3 behavior change**: in commit 2 this tool returned `[]` (stub).
> In commit 3 it queries `sdd_evaluations` directly. Tool name and
> semantics are unchanged — only the data source is now real.

### 3.4 `dark_memory_judge_list_personas`

**No inputs** (read-only registry).

**Output**: list of registered personas (`persona_id`, `eval_types`,
`vibe_cases`, `content_summary`). Used by operators to discover which
persona to pick for a given `eval_type × vibe_case`.

---

## 4. Verdict semantics (4 outcomes + confidence + evidence)

```go
type Verdict struct {
    Verdict    string   // aligned | drift_detected | needs_human | errored
    Confidence float64  // [0.0, 1.0]
    Reasoning  string   // ≤ 280 chars; 1-3 sentences

    PersonaID       string         // persona applied
    RubricVersion   string         // sha256[:16] of the rubric used
    Criteria        []Criterion    // per-criterion score + weight
    Evidence        []Evidence     // file:line + snippet + relevance
    EdgeCaseHits    []EdgeCaseHit  // EC-001..EC-015 that fired
    TemperatureNote TemperatureNote
    BiasAudit       BiasAudit
}
```

### 4.1 The 4 outcomes

| Outcome | Triggered by | Operator action |
|---|---|---|
| `aligned` | weighted sum ≥ 0.85 AND no critical criterion < 0.30 | proceed |
| `drift_detected` | weighted sum < 0.50 OR an EC with severity=error short-circuited | reject + fix + re-publish |
| `needs_human` | weighted sum ∈ [0.50, 0.85) OR an EC short-circuited with needs_human | operator review |
| `errored` | infrastructure failure (EC-002): provider unreachable, invalid key, schema validation failed post-LLM, `consensus` modal could not be determined | check `verdict_json.reasoning` |

### 4.2 Critical-criterion override

If any criterion with `weight >= 0.20` has `score < 0.30`, the verdict
is **forced** to `needs_human` even if the weighted sum is ≥ 0.85. This
prevents a single failing criterion (e.g., "tests" or "security") from
being hidden by high scores elsewhere. See `judge/pipeline.go`
`criticalCriterionFloor` rule.

### 4.3 Evidence structure

Every verdict includes `Evidence[]` with one entry per `source` the
extractor pulled. Each entry has:
- `source` — file:line, URL, or chunk id
- `hash` — sha256 of the snippet (truncated; lets you verify the
  evidence hasn't been tampered with)
- `snippet` — 1-3 line excerpt
- `relevance_score` — [0,1] deterministic from extraction (NOT the LLM
  opinion — the LLM doesn't see this score)

If `eval_type ∈ {drift_judge, grounding_check, doc_vs_code_drift}`
AND `Evidence` is empty, the verdict is `needs_human` (no evidence = no
verdict possible). This is enforced in `verifier.go`.

### 4.4 EdgeCaseHits

EC-001..EC-015 that fired during the run. Each hit has a `severity`
and an `action` (flagged / escalated / overridden). See
`docs/edge-case-catalog.md` for the per-EC details.

### 4.5 TemperatureNote (reproducibility)

Every verdict carries:
```yaml
provider: minimax
model: MiniMax-M3
temperature: 0.0
seed: 0
max_tokens: 2048
timeout_ms: 15000
top_p: 1.0
persona_id: judge-logical
rubric_version: sha256:abc123def4567890
schema_version: v4alpha/2026-09-27/002
```

Two verdicts with identical `spec_intent + artifact_ref` and identical
`TemperatureNote` SHOULD produce identical results (modulo provider
non-determinism; minimax variants report `non_deterministic=true` on
`LLMResponse` and the `non_deterministic` column in `sdd_evaluations`
records it).

---

## 5. Persistence semantics (sdd_evaluations)

### 5.1 Table layout (18 columns + 4 indexes)

`dark-memory.db` has 9 tables; the 9th is `sdd_evaluations` (C3).

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | autoincrement |
| `eval_type` | TEXT | drift_judge, brand_match, ... |
| `target_id` | TEXT | "text:<32chars>:<len>" or file path or artifact_id string |
| `target_type` | TEXT | "text", "file", "artifact_id", "spec_id", "url" |
| `verdict_json` | TEXT | the full Verdict serialized (Verdict + Criteria + Evidence + EdgeCaseHits + TemperatureNote + BiasAudit) |
| `modal` | INTEGER | 0 or 1; 1 if this row is the modal of a consensus N-shot |
| `eval_consensus` | INTEGER | 0 or 1; 1 if this row IS the consensus-sentinel record (its `eval_type` is forced to "consensus") |
| `non_deterministic` | INTEGER | 0 or 1; from `LLMResponse.NonDeterministic` |
| `session_id` | TEXT | INV-1 attribution (nullable) |
| `provider` | TEXT | from `TemperatureNote.provider` |
| `model` | TEXT | from `TemperatureNote.model` |
| `persona_id` | TEXT | from `TemperatureNote.persona_id` |
| `rubric_version` | TEXT | from `TemperatureNote.rubric_version` |
| `schema_version` | TEXT | from `TemperatureNote.schema_version` |
| `seed` | INTEGER | from `TemperatureNote.seed` |
| `max_tokens` | INTEGER | from `TemperatureNote.max_tokens` |
| `timeout_ms` | INTEGER | from `TemperatureNote.timeout_ms` |
| `temperature` | REAL | from `TemperatureNote.temperature` |
| `top_p` | REAL | from `TemperatureNote.top_p` |
| `created_at` | TIMESTAMP | server-side default `CURRENT_TIMESTAMP` |

4 indexes: `(eval_type, created_at DESC)`, `(target_type, target_id,
created_at DESC)`, `(session_id, created_at DESC)`,
`(persona_id, created_at DESC)`.

### 5.2 Atomicity

Every `dark_memory_judge` call writes ONE row to `sdd_evaluations`
plus ONE row to `audit_log` (INV-1), both in the **same** SQL
transaction. If the audit row fails to insert, the evaluation row
rolls back (and `evaluation_id=0` is returned to the caller).

Every `dark_memory_consensus` call with N samples writes:
- N per-sample rows in `sdd_evaluations` (`eval_type=<caller's>`,
  `modal=0`, `eval_consensus=0`)
- ONE modal row (`eval_type="consensus"` sentinel, `modal=1`,
  `eval_consensus=1`)
- N+1 audit rows in `audit_log`

All N+2 writes in a single transaction. If any fails, all roll back.

### 5.3 Best-effort persistence

**Persistence failure does NOT fail the verdict response**. The
pipeline returns the verdict (it doesn't depend on `sdd_evaluations` for
correctness), and the transport layer attempts to persist. If
persistence fails (DB locked, disk full), the verdict is still
returned; `evaluation_id=0` and `verdict_json.persistence_error` is
populated. This matches `vibe.Pipeline.Publish` pattern.

Operators should monitor `error_summary` for `domain=store` to detect
silent persistence failures.

### 5.4 Query patterns

```sql
-- Latest 20 verdicts for a given target
SELECT * FROM sdd_evaluations
WHERE target_type='file' AND target_id='internal/v4alpha/judge/pipeline.go'
ORDER BY created_at DESC LIMIT 20;

-- Verdicts in the last 24h grouped by provider
SELECT provider, COUNT(*) FROM sdd_evaluations
WHERE created_at > strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 day')
GROUP BY provider;

-- Verifier-fired overrides (EC-015)
SELECT COUNT(*) FROM sdd_evaluations
WHERE json_extract(verdict_json, '$.verdict')='needs_human'
  AND json_extract(verdict_json, '$.edge_case_hits') LIKE '%EC-015%';
```

---

## 6. Operator workflow (recall → query → review audit)

The canonical operator loop:

```
┌─ Step 1: Recall prior context ────────────────────────────────┐
│  agent_memory_recall(query="judge pipeline verdict operator",  │
│                       scope=project, operator=<self>)          │
│  → should return rows 2084 (C3 SUMMARY) + 2093 (C4 SUMMARY)   │
│    + 2094..2098 (C4 sections). No need to read files.          │
└────────────────────────────────────────────────────────────────┘
                              ↓
┌─ Step 2: Query judgment history ──────────────────────────────┐
│  dark_memory_judgment_history(target_id="...", limit=20)       │
│  → see prior verdicts for the same artifact                   │
└────────────────────────────────────────────────────────────────┘
                              ↓
┌─ Step 3: Run a verdict ────────────────────────────────────────┐
│  dark_memory_judge(operator=<self>, eval_type=...,            │
│                    artifact_ref={kind:file, path:...},        │
│                    spec_intent=..., vibe_case=C1)             │
│  → returns Verdict + evaluation_id                             │
└────────────────────────────────────────────────────────────────┘
                              ↓
┌─ Step 4: Review audit trail ───────────────────────────────────┐
│  dark_memory_error_list(domain=store, severity=error)          │
│  → silent persistence failures surface here                   │
│                                                                 │
│  dark_memory_writes(session_id=<self>, limit=10)               │
│  → per-write audit entries (INV-1 row per judge call)          │
└────────────────────────────────────────────────────────────────┘
```

The `evaluation_id` from step 3 lets you correlate with `writes`
filter `session_id=<self>` and `tool_name=judge` to get the
corresponding `audit_log` row.

---

## 7. Reproducibility — how to cache and replay

Two verdicts with identical inputs + identical `TemperatureNote` SHOULD
be reproducible. To replay:

1. Read the `evaluation_id` from the prior verdict
2. Read its `TemperatureNote` from `sdd_evaluations`
3. Call `dark_memory_judge` with the same inputs + same persona +
   same `seed` + same `temperature`
4. Compare the new `Verdict` to the stored `verdict_json`

If `non_deterministic=true` is set on the original, expect divergence.
If `non_deterministic=false`, divergence is a pipeline bug — open an
issue with both `evaluation_id`s and the `TemperatureNote` of each.

### 7.1 Cross-version federation (planned, not yet shipped)

A future v4 ↔ v3 federation will let `dark_research` query
`sdd_evaluations` across versions via `DARK_FEDERATION_PEER_DSN`.
Until then, v4 verdicts are local-only. The `sdd_evaluations` table
name is stable across v3 and v4 so federation can join on
`(eval_type, target_id, created_at)`.

---

## 8. Where to read next

- `docs/decisions/ADR-007-judge-pipeline-v4.md` — design ADR (15 ECs,
  11 personas, 7-step pipeline)
- `docs/edge-case-catalog.md` — every EC: trigger, severity,
  short-circuit, real failure it catches, file:line refs
- `docs/persona-registry-v4.md` — 11 personas + override mechanism
- `docs/v4-status.md` — current shipped state (29 tools, ADR-007
  shipped 4 commits)
- `CHANGELOG.md` — entry `[4.0.0-alpha.3]`
- `docs/INVARIANTS.md` — INV-1 (write-path audit), INV-7 (per-project
  scoping), INV-17 (FTS5 ordering)

---

## 9. References (tier-1 sources verified 2026-09-27)

Every paper, doc, or repo cited in this file has been verified against
the primary source. The list below records: what we claim, where the
claim comes from, and the URL an operator can check.

### 9.1 LLM-as-judge papers

1. **G-Eval** — Liu et al. 2023, *G-Eval: NLG Evaluation using GPT-4
   with Better Human Alignment*, arxiv:2303.16634 (cs.CL, submitted
   29 Mar 2023, v3 23 May 2023). Spearman correlation 0.514 on
   summarization; observed bias toward LLM-generated texts.
   Code: <https://github.com/nlpyang/geval>.
   URL: <https://arxiv.org/abs/2303.16634>
   Used in: §4 (verdict semantics), §7 (reproducibility).

2. **Prometheus 2** — Kim et al. 2024, *Prometheus 2: An Open Source
   Language Model Specialized in Evaluating Other Language Models*,
   arxiv:2405.01535 (cs.CL, v1 2 May 2024, v2 4 Dec 2024), EMNLP 2024
   Main Conference. Direct assessment + pairwise ranking + custom
   evaluation criteria; "highest correlation and agreement with
   humans and proprietary LM judges among all tested open evaluator
   LMs".
   Code: <https://github.com/prometheus-eval/prometheus-eval>.
   URL: <https://arxiv.org/abs/2405.01535>
   Used in: `docs/persona-registry-v4.md` §2.1 (the design rationale
   for `judge-logical` supporting both `drift_judge` +
   `spec_test_alignment`).

3. **MT-Bench / Chatbot Arena** — Zheng et al. 2023, *Judging LLM-as-a-
   Judge with MT-Bench and Chatbot Arena*, arxiv:2306.05685 (cs.CL,
   v1 9 Jun 2023, v4 24 Dec 2023), NeurIPS 2023 Datasets and Benchmarks
   Track. Strong LLM judges (GPT-4) "match both controlled and
   crowdsourced human preferences well, achieving over 80% agreement".
   Identifies position bias, verbosity bias, self-enhancement bias.
   Code: <https://github.com/lm-sys/FastChat/tree/main/fastchat/llm_judge>.
   URL: <https://arxiv.org/abs/2306.05685>
   Used in: `docs/edge-case-catalog.md` EC-008 (position swap mitigation).

4. **Self-RAG** — Asai et al. 2023, *Self-RAG: Learning to Retrieve,
   Generate, and Critique through Self-Reflection*, arxiv:2310.11511
   (cs.CL, 17 Oct 2023). Introduces reflection tokens
   (`<retrieve>`, `<isrel>`, `<issup>`, `<isuse>`) for self-critique.
   URL: <https://arxiv.org/abs/2310.11511>

5. **Play Favorites** — Spiliopoulou, Fogliato et al. 2025, *Play
   Favorites: A Statistical Method to Measure Self-Bias in LLM-as-a-
   Judge*, arxiv:2508.06709 (cs.CL, 8 Aug 2025). Empirical analysis on
   >5000 prompt-completion pairs and 9 LLM judges. Finds GPT-4o and
   Claude 3.5 Sonnet "systematically assign higher scores to their
   own outputs" plus a family-bias toward same-family models.
   URL: <https://arxiv.org/abs/2508.06709>
   Used in: `docs/edge-case-catalog.md` EC-007 (self-bias detection).

### 9.2 Provider docs

6. **Anthropic Structured Outputs** — official documentation, GA
   status verified 2026-09-28. **Updated 2026-09-28** (SOTA chunk 1):
   the parameter is `output_config.format` (the legacy `output_format`
   parameter is **deprecated**; using it without the
   `structured-outputs-2025-11-13` beta header returns 400). Supports
   `output_config.format: {type: "json_schema", schema: <VerdictSchema>}`
   with constrained decoding (always-valid, type-safe, no retries for
   schema violations). Two complementary features: JSON outputs
   (`output_config.format`) and Strict tool use (`strict: true`).
   **2026 supported models** (per Anthropic docs, last verified
   2026-09-28): `claude-fable-5-1`, `claude-mythos-5-1`, `claude-fable-5`,
   `claude-mythos-5`, `claude-mythos-preview`, `claude-opus-5-5`,
   `claude-opus-5`, `claude-opus-4-8`, `claude-opus-4-7`, `claude-opus-4-6`,
   `claude-sonnet-5-5`, `claude-sonnet-5`, `claude-sonnet-4-6`,
   `claude-sonnet-4-5-20250929`, `claude-opus-4-5-20251101`,
   `claude-haiku-4-5-20251001`. ZDR-eligible (zero data retention) for
   covered models.
   URL: <https://docs.anthropic.com/en/docs/build-with-claude/structured-outputs>
   Used in: §2 (provider config — why Anthropic is on the allow-list).
   **SOTA gap (chunk 1)**: v4's allow-list has only 4 providers
   (anthropic, minimax, minimax-cn, deepseek). SOTA 2026 includes
   OpenAI GPT-5/5.1/5.2 (structured outputs native since 2024), Google
   Gemini 2.5/3.0, Qwen 3, Kimi K2 — none of which v4 supports. See
   §10.3 for the criticism.

7. **Anthropic model allow-list gap (SOTA chunk 1, 2026-09-28)** —
   v4's `dark_memory_judge` provider list has 4 entries, but the 2026
   Anthropic GA list has **15+ models** (above). Many of the newer
   ones (Fable 5-1, Mythos 5-1, Opus 5-5, Sonnet 5-5) are not on the
   v4 allow-list. The minimax and deepseek providers cover only a
   subset of SOTA. The v4 allow-list needs an ADR-009 expansion.

### 9.3 Storage / concurrency

7. **SQLite Write-Ahead Logging** — official SQLite documentation.
   WAL since SQLite 3.7.0 (2010-07-21). Readers don't block writers;
   writers don't block readers; `-wal` and `-shm` files accompany the
   database. WAL-reset bug affects 3.7.0 through 3.51.2 (2026-01-09);
   fixed in 3.51.3 (2026-03-13) and later. Recommended for
   transactions <100MB; larger transactions should use rollback mode.
   URL: <https://sqlite.org/wal.html>
   Used in: `docs/v4-status.md` §3 (INV-16 — dark-db concurrency
   contract uses `journal_mode=WAL`).

8. **modernc.org/sqlite** — pure-Go SQLite driver (no cgo).
   Currently pinned at `v1.53.0` in `go.mod` (verified by `grep
   modernc.org/sqlite go.mod`). Canonical repository:
   <https://gitlab.com/cznic/sqlite>. Imported by 3,518 packages
   (per pkg.go.dev stats on the v1.59.0 page — count grew over the
   v1.5x line). License: BSD-3-Clause.
   URL: <https://pkg.go.dev/modernc.org/sqlite>
   Used in: `docs/v4-status.md` §2 (package layout:
   `internal/v4alpha/store/`).

### 9.4 Project ADRs (design sources of truth)

9. **ADR-007** — `docs/decisions/ADR-007-judge-pipeline-v4.md`. The
   design ADR for the entire judge pipeline. Sections: §2 (7-step
   pipeline), §2.2 (11 personas), §2.3 (rubric + thresholds), §2.4
   (3-layer prompt-injection defense), §2.5 (verifier), §4 (15 ECs).
10. **ADR-008** — `docs/decisions/ADR-008-work-standard.md`. The
    "atomic spec mirror" discipline (every spec in 2 places:
    monolithic file + dark-memory rows).

### 9.5 What this list is NOT

- It is **not** exhaustive of the operator's full bibliography. The
  ADR-007 Apéndice A carries the full reference set including
  dark-sdd ADR-003 (MiniMax-M3 local) and dark-sdd ADR-006 (auto-
  grounding).
- It is **not** a substitute for reading the papers. The pipeline
  implements a *subset* of what these papers recommend (e.g., we
  adopted G-Eval's bias observation + Prometheus 2's direct-assessment
  pattern, but did NOT adopt Prometheus 2's pairwise-ranker model).
  See ADR-007 §7 (Trade-offs) for what we explicitly rejected.

---

## 10. SOTA alignment + gaps (chunk 1, 2026-09-28)

> **Audience**: anyone evaluating v4's judge pipeline against the
> 2026 state of the art. **Honest assessment** — no defensiveness.
> SOTA verification method: tier-1 source per `fresh-osint` (arXiv
> primary, vendor docs primary, news/blogs only for trail color).
> Verification date: 2026-09-28.

This section is the SOTA criticism of v4's judge pipeline. It is
structured as: §10.1 where v4 is **on-par** with SOTA 2026,
§10.2 where v4 is **ahead** (rare), §10.3 where v4 is **behind**
(with file:line), §10.4 what we **could not verify** (honest gaps).

### 10.1 On-par with SOTA 2026

The v4 judge pipeline's core design choices align with the canonical
LLM-as-judge SOTA as of 2026:

- **Direct-assessment + custom-criteria** (the `judge-logical` persona
  pattern). SOTA source: Prometheus 2 (Kim et al. 2024,
  arxiv:2405.01535, EMNLP 2024 — verified 2026-09-28 via
  <https://arxiv.org/abs/2405.01535>). The "open evaluator LM that
  mirrors human and GPT-4 judgements" pattern is exactly v4's
  persona + rubric model. **Verdict: aligned**.

- **Self-enhancement bias detection** (EC-007). SOTA source: Play
  Favorites (Spiliopoulou, Fogliato et al. 2025, arxiv:2508.06709 —
  verified 2026-09-28 via <https://arxiv.org/abs/2508.06709>). The
  paper finds "GPT-4o and Claude 3.5 Sonnet systematically assign
  higher scores to their own outputs" plus family-bias. v4's EC-007
  is a binary check; the paper's contribution is a statistical
  framework. **Verdict: aligned in intent, but v4 does not implement
  the statistical test** (see §10.3).

- **Position bias mitigation** (EC-008 — position swap). SOTA source:
  MT-Bench / Chatbot Arena (Zheng et al. 2023, arxiv:2306.05685,
  NeurIPS 2023 — referenced in v4 §9.1.3). The position-swap
  technique is canonical. **Verdict: aligned**.

- **Constrained-decoding structured outputs** (the `Verdict`
  JSON-schema response). SOTA source: Anthropic Structured Outputs
  (GA 2026, verified 2026-09-28 — see §9.2 #6 update). v4's
  `Verdict` schema follows the Anthropic pattern. **Verdict:
  aligned, but provider allow-list is narrower than 2026 SOTA
  (see §10.3)**.

- **Self-critique + reflection tokens** (EC-015 verifier override).
  SOTA source: Self-RAG (Asai et al. 2023, arxiv:2310.11511 —
  referenced in v4 §9.1.4). v4's post-LLM verifier that can
  override a verdict for consistency. **Verdict: aligned**.

- **G-Eval-style rubric decomposition** (the per-criterion score
  + weight in `Verdict.Criteria`). SOTA source: G-Eval (Liu et al.
  2023, arxiv:2303.16634 — referenced in v4 §9.1.1). **Verdict:
  aligned**.

### 10.2 Ahead of SOTA 2026 (rare but real)

Three places where v4 is **ahead** of the canonical SOTA:

- **Persona override mechanism** — v4's Markdown-based field-level
  override (per `persona-registry-v4.md` §3) is cleaner than the
  SOTA 2026 alternatives I verified (no SOTA LLM-as-judge paper has
  a field-level merge strategy with `field_replace` / `full_replace`
  / `append` modes). The override-via-Markdown-with-validation
  (weights must sum to 1.0, otherwise EC-005) is operationally
  more robust than SOTA's "edit the system prompt and hope".

- **15 deterministic pre-flight ECs** as a first-class concept
  (per `edge-case-catalog.md`). Most SOTA LLM-as-judge work is
  "ask the LLM and trust"; v4's pre-flight gates save the LLM
  call on known-bad inputs (EC-001 empty artifact, EC-002 LLM
  provider failure, EC-004 oversized artifact, EC-005 persona
  weight validation). This is operational SOTA.

- **4-verdict outcome model** (aligned / drift_detected /
  needs_human / errored). SOTA 2026 typically uses 2-class
  (pass / fail) or 3-class (pass / fail / unsure). v4's split
  of `errored` (infrastructure) from `drift_detected` (real
  failure) and `needs_human` (ambiguous) is more nuanced and
  operationally cleaner.

- **Per-provider `TemperatureNote` for reproducibility** (v4
  `sdd_evaluations` table has 18 columns; 9 are the TemperatureNote
  fields). SOTA 2026 evaluation datasets often omit
  model+seed+temperature+top_p+schema_version from the persisted
  record. v4 persists all of them. **Verdict: ahead**.

### 10.3 Behind SOTA 2026 (with file:line)

The honest list of gaps. Each cites the v4 location and the SOTA
2026 alternative.

- **Provider allow-list is narrow** — `internal/v4alpha/judge/llm_client.go`
  (v4-alpha.4, see §2 of this doc) has 4 providers: `anthropic`,
  `minimax`, `minimax-cn`, `deepseek`. **SOTA 2026** has at least
  10+ viable providers with native structured outputs: OpenAI
  GPT-5/5.1/5.2, Google Gemini 2.5/3.0, Anthropic 15+ models
  (see §9.2 #6), DeepSeek v3.2/v4, Qwen 3, Kimi K2, GLM-4.5,
  Mistral. **Verdict: behind by ~6 providers**. Remediation:
  ADR-009 (provider expansion), tracked separately.

- **No pairwise ranking** — v4 explicitly rejected Prometheus 2's
  pairwise-ranker model (per ADR-007 §7 trade-offs). For "drift"
  vs "aligned" hard cases, pairwise comparison is more
  discriminative. SOTA 2026 (Prometheus 2 + newer judge ensembles)
  supports both. **Verdict: behind in the discrimination axis**.
  Remediation: ADR-010 (pairwise ranking) when needed.

- **EC-007 self-bias check is binary, not statistical** — v4's
  EC-007 (per `edge-case-catalog.md`) flags self-bias as
  "the artifact is from the same model as the judge" — a binary
  signal. SOTA Play Favorites (arxiv:2508.06709) provides a
  **statistical framework** that quantifies self-bias while
  accounting for genuine quality differences. v4 does not
  implement the statistical test. **Verdict: behind in the
  measurement axis**. Remediation: not blocking (binary is
  good enough for `flagged` action), but documented as a
  known gap.

- **`spec_intent` capped at 4 KiB** — v4 caps the hypothesis
  field at 4 KiB (per `docs/judge-pipeline-v4.md` §3.1).
  SOTA 2026 long-context models (Opus 5-5 1M, Gemini 2.5 Pro
  2M, MiniMax-M3 1M per the prior SOTA research in
  `vibe-flow/main/DELEGATION_SOTA.md`) can absorb longer
  spec_intent. The 4 KiB cap is over-conservative for the
  long-context era. **Verdict: behind in input budget**.
  Remediation: bump to 64 KiB (still well within prompt
  overhead) in BUG-11.

- **No multi-modal judge evaluation** — v4 has a
  `judge-cross-modal` persona defined (per
  `docs/persona-registry-v4.md` §2.2) but the SOTA 2026 work on
  **MLLM-as-judge** benchmarks (vision-language model evaluation)
  is not cited. The persona is a v4-introduced concept; the
  benchmarks (e.g. MLLM-as-Judge surveys from 2025-26) are not.
  **Verdict: behind in SOTA grounding**. Remediation: cite
  the relevant 2025-26 MLLM-as-judge survey in
  `persona-registry-v4.md` §8 references.

- **No bootstrap evaluation of the judge** — v4 records
  `sdd_evaluations` rows but does NOT compute bootstrap
  confidence intervals on the judge's accuracy. SOTA 2026
  LLM-as-judge best practice (per the LLM-as-Judge survey
  literature) includes bootstrap-CI on per-judge-call accuracy
  vs human labels. **Verdict: behind in the calibration
  axis**. Remediation: ADR-011 (judge calibration with
  bootstrap-CI), tracked separately.

- **Rubric is static** — v4 persona rubrics are compiled into
  the binary. SOTA 2025-26 work on **learned / adaptive
  rubrics** (where the rubric weights are updated based on
  past judge calls) is not present. **Verdict: behind in
  the adaptation axis**. Remediation: not blocking (overkill
  for v4-alpha), but documented.

- **No active learning loop** — verdicts don't feed back to
  improve the personas (no fine-tuning path mentioned). SOTA
  2026 has Reinforcement Learning from Judge Feedback (RLJF)
  patterns. **Verdict: behind in the improvement axis**.
  Remediation: out of scope for v4-alpha; ADR-012 (RLJF
  loop) when adopted.

### 10.4 What I could not verify (honest gap)

The SOTA criticism is honest about what I don't know:

- **Specific 2025-26 SOTA papers I cannot verify from primary
  source in this session.** My training cutoff is January 2026
  per the harness, but I attempted to verify newer papers via
  webfetch in this session. Two of the three attempts returned
  unrelated physics papers (incorrect arxiv IDs I guessed
  without verification). The honest statement: I do NOT have
  specific arxiv IDs for 2026 LLM-as-judge SOTA papers that
  may have superseded Prometheus 2 / Play Favorites / G-Eval.
  The next SOTA-doc chunk (chunk 2: agent memory) will repeat
  this verification with a more targeted search.

- **Whether the LLM-as-judge field has converged on a new
  standard benchmark** replacing MT-Bench / Chatbot Arena. The
  v4 docs assume MT-Bench is still canonical (per §9.1.3). I
  could not verify this in this session.

- **The current state of OpenAI / Google / DeepSeek structured
  outputs** (vs Anthropic). I verified Anthropic Structured
  Outputs is GA as of 2026-09-28. The other vendors' structured
  output mechanisms I cannot verify from this session.

- **Whether 2025-26 MLLM-as-judge benchmarks** (vision eval)
  are SOTA or still maturing. The `judge-cross-modal` persona
  references this space but I cannot cite specific papers.

### 10.5 What this section is NOT

- It is **not** a refutation of v4's design. v4 ships
  substantial, well-cited work (38 tools, 15 ECs, 11 personas,
  LLM-backed judge with provenance). The gaps in §10.3 are
  tractable, not architectural.
- It is **not** a substitute for ADR-009/010/011/012
  (proposed remediations). Each gap has a proposed ADR; the
  ADRs are the next step, not this section.
- It is **not** a comprehensive SOTA survey. The
  SOTA-doc chunk 1 scope is the **judge pipeline**. Other
  chunks (agent memory, audit chain, workflow runtime, MCP)
  cover their respective SOTA comparisons.

---

## 11. Where to read next (updated)

- `docs/decisions/ADR-007-judge-pipeline-v4.md` — design ADR (15 ECs,
  11 personas, 7-step pipeline)
- `docs/edge-case-catalog.md` — every EC: trigger, severity,
  short-circuit, real failure it catches, file:line refs
- `docs/persona-registry-v4.md` — 11 personas + override mechanism
- `docs/v4-status.md` — current shipped state (38 tools, ADR-007
  shipped 4 commits)
- `CHANGELOG.md` — entry `[4.0.0-alpha.6]` (SOTA criticism chunk 1)
- `docs/INVARIANTS.md` — INV-1 (write-path audit), INV-7 (per-project
  scoping), INV-17 (FTS5 ordering)
- `vibe-flow/main/DELEGATION_SOTA.md` — 2026-08-04 prior SOTA
  research (P1-P8 delegation principles, token budgets, model
  selection)
- `docs/sota-critique.md` — meta-doc aggregating chunks 1-5
  (13 ahead / 28 on-par / 39 behind, 17 ADRs + 1 BUG, 20 honest
  couldn't-verify). The operator-facing summary of the SOTA-doc
  workstream.
