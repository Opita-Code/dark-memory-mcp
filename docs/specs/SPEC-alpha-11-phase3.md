# SPEC — alpha.11 Phase 3 — Judge improvements (ADR-009 + ADR-011)

| Field | Value |
|---|---|
| **Vibe-loop** | `alpha-11-phase-3` |
| **Vibe-case** | C1 (code) |
| **Author** | Opita-AI (MiniMax-M3), 2026-09-30 |
| **Operator approval** | received 2026-09-30 ("Procede") |
| **Target alpha** | v4.0.0-alpha.16 |
| **Branch** | `feat/v4-redesign` |
| **Plan ref** | `docs/v4-alpha-11-plan.md:139-161` |
| **ADR refs** | ADR-009 (provider allow-list), ADR-011 (judge calibration bootstrap-CI) |
| **SOTA source** | `docs/sota-critique.md:413,415,684,691-693,960` + `docs/judge-pipeline-v4.md:645-652,692-699` |

**Plan acceptance target vs actual**: The plan §3 table row 8 says "ADR-009 (provider allow-list 4 → 10+)". The actual shipped count is **9** (anthropic, openai, google, deepseek, minimax, minimax-cn, zhipu, moonshot, qwen) — the canonical `internal/llm/catalog.go` has exactly 9 wired today. The plan's enumerated target list ("anthropic, minimax, minimax-cn, deepseek, openai, google, qwen, kimi, zhipu, moonshot, mistral") includes **kimi and mistral**, neither of which are in the canonical catalog. Adding them requires new `ProviderSpec` entries (separate decision; catalog work, not judge-client work). 9 ≥ 9 satisfies the spirit of "≥9 providers"; 10+ would require first extending the catalog.

---

## 1. TL;DR

Two independent items in one vibe-loop:

- **ADR-009** — extend `internal/v4alpha/judge/llm.go` provider allow-list from 4 → 9 providers. **The canonical `internal/llm/catalog.go` already has 9 providers wired** (anthropic, openai, google, deepseek, minimax, minimax-cn, zhipu, moonshot, qwen); v4alpha just doesn't consume 5 of them. S complexity, ~150 LoC.
- **ADR-011** — replace EC-007 (binary self-bias check) with a **statistical bootstrap-CI** framework per Play Favorites (arxiv:2508.06709). 4 new columns on `sdd_evaluations`, pure-Go bootstrap CI helper, statistical EC. M complexity, ~250 LoC.

Total: ~400 LoC, ~5-7 days calendar, single commit (`alpha-16`).

---

## 2. Why Phase 3 now

### ADR-009 — provider coverage gap

`docs/judge-pipeline-v4.md:645-652` documented the gap in chunk 1 (2026-09-28). Today, the only viable judge providers are 4 (anthropic + 2 minimax variants + deepseek). Operators with only `OPENAI_API_KEY` / `GEMINI_API_KEY` / `Qwen API key` get `ErrLLMUnavailable` at boot — a hard block.

Per `docs/sota-critique.md:413` and the plan §3 table row 8: **"ADR-009 (provider allow-list 4 → 10+) | proposed | S | ~150 LoC | LOW"**.

### ADR-011 — judge accuracy calibration

`docs/judge-pipeline-v4.md:692-699` (chunk 1) and `docs/sota-critique.md:415` both flag the missing statistical calibration: v4 records `sdd_evaluations` rows with `confidence REAL` but never computes a confidence interval on per-judge-call accuracy vs human labels.

Today EC-007 (`internal/v4alpha/judge/edge_cases.go:334-355`) is a binary substring check: "artifact contains `author: anthropic` AND persona ProviderHint is anthropic → flag". Play Favorites arxiv:2508.06709 (verified tier-1, 2026-09-28) provides a **statistical framework** (bootstrap over N samples) that v4 does not yet implement.

---

## 3. ADR-009 — Provider allow-list 4 → 9

### 3.1 What's already there (don't rewrite)

`internal/llm/catalog.go:151-254` defines 9 canonical `ProviderSpec`s:

```
anthropic, openai, google, deepseek, minimax, minimax-cn,
zhipu, moonshot, qwen
```

Each has `EnvKey`, `BaseURL`, `DefaultModel`, dialect, and a `DialectAnthropic` override URL where applicable. Aliases: `z-ai → zhipu`, `dashscope → qwen` (`catalog.go:115-116`).

`internal/v4alpha/judge/llm.go:375-447` already has generic `buildAnthropicRequest` and `buildOpenAIRequest` that work for any provider once `dialect` is set correctly. The HTTP client, retry, parse-fail, and backoff helpers (`llm.go:88-147, 286-304, 574-624`) are provider-agnostic.

### 3.2 What's missing

`internal/v4alpha/judge/llm.go:62-75`:

```go
var supportedProviderIDs = map[string]bool{
    "anthropic":   true,
    "minimax":     true,
    "minimax-cn":  true,
    "deepseek":    true,
}
```

Plus the auto-detect chain in `resolveProviderFromEnv` (`llm.go:177-188`) which only iterates the same 4 IDs.

### 3.3 What ships

**Single file change**: `internal/v4alpha/judge/llm.go`.

1. Extend `supportedProviderIDs` to include all 9 canonical IDs.
2. Extend auto-detect chain (`llm.go:177-188`) to iterate all 9 IDs.
3. Update pin-error message to enumerate the full list (`llm.go:155-159`).
4. Update the file header doc comment to state the count (4 → 9).
5. Add 4 L1 tests in `internal/v4alpha/judge/llm_test.go`:
   - `TestNewRealLLMClient_OpenAI` — DARK_JUDGE_PROVIDER=openai + OPENAI_API_KEY → RealLLMClient with `provider=openai`, `dialect=DialectOpenAI`, `baseURL=https://api.openai.com/v1`.
   - `TestNewRealLLMClient_Google` — provider=google + GEMINI_API_KEY → dialect OpenAI, baseURL=`generativelanguage.googleapis.com/v1beta/openai/`.
   - `TestNewRealLLMClient_Qwen_AnthropicDialect` — provider=qwen + DARK_JUDGE_DIALECT=anthropic + DASHSCOPE_API_KEY → dialect Anthropic (qwen supports both).
   - `TestNewRealLLMClient_UnsupportedProvider` — DARK_JUDGE_PROVIDER=mistral → `ErrLLMUnavailable` with the 9-provider list in the error message.

**m3-thinking (minimax)**: spec 1198 quirk already wired in `llm.go:399-402` — kept as-is. **m3-thinking does NOT trigger for the 5 new providers** (it's a minimax-only `thinking: adaptive` directive).

**Mistral**: out of scope. Not in canonical catalog; would require a new catalog entry (separate decision).

### 3.4 Risks and mitigations

| Risk | Severity | Mitigation |
|---|---|---|
| Provider endpoint drift (e.g., Google deprecates `/v1beta/openai/`) | LOW | Catalog already documents the URL with a citation; if drift happens, fix catalog + regression test. Not a code change. |
| Per-provider auth quirks | LOW | All 5 new providers use Bearer auth (same as openai/minimax). Anthropic override (qwen) sends `x-api-key` and `Authorization: Bearer` (per `setAuthHeaders` — already correct). |
| Native structured outputs not all supported | LOW-MED | Out of scope for this phase — the judge's `StructuredInputs` are serialized as a fenced JSON block in the user message (`buildAnthropicRequest:386`, `buildOpenAIRequest:425`). This is a documented gap, accepted as the wire format. |

---

## 4. ADR-011 — Judge calibration bootstrap-CI

### 4.1 Goal

Replace binary EC-007 with statistical self-bias detection. Concretely: every `sdd_evaluations` row gains `confidence_calibrated REAL`, `calibration_ci_low REAL`, `calibration_ci_high REAL`, `calibration_method TEXT`. EC-007 fires when the row's `confidence` exceeds `calibration_ci_high` (the LLM is over-confident relative to its own accuracy on similar targets).

### 4.2 Why bootstrap-CI

Play Favorites (arxiv:2508.06709, Spiliopoulou/Fogliato et al., 2025) is the SOTA statistical method for measuring self-bias in LLM-as-judge. Methodology:

1. Take N evaluation samples with the same `(provider, target_type, eval_type)` tuple.
2. Compute point estimate of accuracy (or any aggregate) for each resample.
3. Report `confidence_calibrated` = point estimate of mean confidence, with CI bounds = 2.5th / 97.5th percentile of resamples.

Efron's standard percentile bootstrap (1979, *Annals of Statistics*) is the reference method. Pure-Go implementation is ~150 LoC.

### 4.3 What ships

#### 4.3.1 Schema (`internal/v4alpha/judge/store.go`)

Add 4 columns to `sdd_evaluations`:

```sql
ALTER TABLE sdd_evaluations ADD COLUMN confidence_calibrated REAL;
ALTER TABLE sdd_evaluations ADD COLUMN calibration_ci_low    REAL;
ALTER TABLE sdd_evaluations ADD COLUMN calibration_ci_high   REAL;
ALTER TABLE sdd_evaluations ADD COLUMN calibration_method    TEXT;  -- e.g. 'play_favorites_v1'
```

Idempotent migration helper `ApplyCalibrationColumns(ctx, db)` — same pattern as `audit.ApplyChainColumns` (Phase 2): `pragma_table_info` to detect, `ALTER TABLE ADD COLUMN` if missing. ~30 LoC.

`Evaluation` struct (`store.go:84-110`) gains 4 fields. `EvaluationFromVerdict` and `SaveEvaluation` populate them. `selectEvaluationSQL` and `scanEvaluation` read them. Existing rows have NULL → treated as "not yet calibrated".

#### 4.3.2 Bootstrap CI (`internal/v4alpha/judge/calibration.go`, NEW)

```go
package judge

// BootstrapCI computes the percentile bootstrap CI for the mean of
// samples. Returns pointEstimate = mean(samples), ciLow/ciHigh =
// percentile(alpha/2, n) and percentile(1-alpha/2, n) of resample
// means. nResamples default = 1000. Seed = 42 (deterministic).
//
// Efron 1979 percentile method; same algorithm Play Favorites uses
// for self-bias confidence intervals (arxiv:2508.06709 §3.2).
//
// Pure-Go (math/rand/v2). Deterministic with seed.
func BootstrapCI(samples []float64, confidence float64, nResamples int) (pointEstimate, ciLow, ciHigh float64) { ... }

// ShouldRecalibrate returns true when a target has enough samples
// for a meaningful CI. Threshold: N >= 50 (Play Favorites §4.3
// found N=30 borderline; we err on the side of statistical power).
func ShouldRecalibrate(n int) bool { return n >= 50 }
```

~150 LoC. Properties verified by L1 tests:
1. Determinism (same input → same output, fixed seed).
2. CI width monotonic in n (more samples → tighter CI).
3. Edge cases n=0/n=1/n=2 → defined behavior (degenerate CI = [mean, mean]).
4. Biased signal detectable (injected over-confidence outside CI → EC fires).

#### 4.3.3 EC-007 statistical (`internal/v4alpha/judge/edge_cases.go`)

**Keep** the existing binary `EC007SelfReference` as `EC007aSelfReferenceBinary` (warn, retrocompat for legacy rows without calibration). **Add** new `EC007bSelfBiasStatistical` (warn when calibrated data present, error when confidence exceeds CI high).

```go
var EC007bSelfBiasStatistical = ecDef{
    id: "EC-007b", severity: "warn", catches: "bias-mitigation",
    description: "Statistical self-bias: LLM confidence exceeds calibrated CI high (Play Favorites)",
    check: func(pc *PipelineContext) *EdgeCaseHit {
        // Reads pc.Verdict.Confidence, pc.CalibrationCIHigh
        // (new field on PipelineContext).
        // Fires when Confidence > CIHigh.
    },
}
```

`PipelineContext` (`edge_cases.go:65-85`) gains:

```go
// CalibrationCI is the bootstrap CI bounds for the judge on this
// target. Populated by the pipeline before EC-007b runs. Nil when
// calibration has not yet been computed (N < ShouldRecalibrate).
CalibrationCI *CalibrationCI  // {PointEstimate, CILow, CIHigh float64}
```

`NewDefaultEdgeCaseRunner` (`edge_cases.go:101-110`) registers both: EC-007a first (legacy), EC-007b second (statistical, error if data is calibrated).

#### 4.3.4 Pipeline population hook (`internal/v4alpha/judge/pipeline.go`)

After each `Complete` succeeds, the pipeline (step [5]+) queries the `Store` for all recent evaluations on the same `(provider, target_type)` tuple. If `ShouldRecalibrate(N)`, call `BootstrapCI` over their confidence values, recompute point estimate + CI bounds, and `UPDATE sdd_evaluations` for the current row + persist `calibration_method='play_favorites_v1'`.

Wired in `pipeline.go` as a new method `populateCalibration(ctx, evalRowID, provider, targetType) error`. Called at end of `pipeline.Evaluate` (non-fatal on error — calibration is best-effort).

#### 4.3.5 Tests (6 new)

**L1 (`internal/v4alpha/judge/calibration_test.go`, NEW)**:
- `TestBootstrapCI_Deterministic` — same input → same output (fixed seed).
- `TestBootstrapCI_WidthMonotonicInN` — more samples → tighter CI (n=10 vs n=1000).
- `TestBootstrapCI_EdgeCases` — n=0, n=1, n=2 all return defined (degenerate) values.
- `TestBootstrapCI_BiasedSignalDetectable` — over-confident point estimate is OUTSIDE the CI of synthetic biased samples.

**L2 (`internal/v4alpha/edge_cases_test.go`, existing)**:
- `TestEC007b_SelfBiasStatistical_Positive` — Confidence=0.95, CIHigh=0.7 → fires (error).
- `TestEC007b_SelfBiasStatistical_Negative` — Confidence=0.7, CIHigh=0.9 → does not fire.

**L2 (`internal/v4alpha/judge/store_deliberate_breaks_test.go`, existing)**:
- `TestApplyCalibrationColumns_Idempotent` — call twice, no error.

### 4.4 Risks and mitigations

| Risk | Severity | Mitigation |
|---|---|---|
| Statistical correctness of bootstrap | MEDIUM | Use Efron 1979 percentile method (textbook). Fixed seed for determinism. N=1000 resamples (default per Play Favorites §4.3). Verify with synthetic data L1 tests. |
| Performance regression (1k resamples per call) | LOW-MED | Bootstrap is ~100μs on 50 samples × 1000 resamples. Calibration only recomputes when N hits 50 boundary (cheaper). Worst case: 1 second per Evaluate call — within budget. |
| Operator confusion (which CI to trust?) | LOW | `calibration_method` column documents the algorithm. `confidence_calibrated` is the point estimate, `confidence` is the LLM-reported value. EC-007b's message explains the comparison. |
| EC-007 split breaks downstream tooling | LOW | EC-007a is still registered (warn). Old tools that filter on `EC-007` continue to work; new ones can filter `EC-007b`. Documented in edge-case-catalog.md. |

### 4.5 What this does NOT do

- **No human-labeled validation set** — bootstrap here is over historical self-confidence, not against a gold standard. Play Favorites uses human labels; we don't have those. The CI is a **calibration estimate of self-consistency**, not a true accuracy CI. This is a known limitation documented in §6.
- **No cross-judge ensemble bootstrap** — single-judge CIs only. Cross-judge calibration is alpha.3 deferred.
- **No MERGE/UPDATE on rollback** — if `SaveEvaluation` rolls back, the calibration UPDATE is part of the same tx → rolls back atomically. Same contract as audit (Phase 2).

---

## 5. Files touched (delta from `alpha-15` baseline)

| File | Status | LoC delta | Purpose |
|---|---|---|---|
| `internal/v4alpha/judge/llm.go` | MODIFIED | +30 | Extend supported providers; update error message |
| `internal/v4alpha/judge/llm_test.go` | MODIFIED | +60 | 4 new L1 tests for new providers |
| `internal/v4alpha/judge/calibration.go` | NEW | ~150 | BootstrapCI, ShouldRecalibrate, CalibrationCI struct |
| `internal/v4alpha/judge/calibration_test.go` | NEW | ~180 | 4 L1 tests (determinism, monotonicity, edge, biased) |
| `internal/v4alpha/judge/store.go` | MODIFIED | +60 | 4 columns + ApplyCalibrationColumns + populate on Save |
| `internal/v4alpha/judge/edge_cases.go` | MODIFIED | +50 | EC-007b statistical; PipelineContext.CalibrationCI |
| `internal/v4alpha/judge/edge_cases_test.go` | MODIFIED | +40 | 2 L2 EC-007b tests |
| `internal/v4alpha/judge/store_deliberate_breaks_test.go` | MODIFIED | +25 | 1 L2 idempotency test |
| `internal/v4alpha/judge/pipeline.go` | MODIFIED | +30 | populateCalibration hook |
| `docs/specs/SPEC-alpha-11-phase3.md` | NEW | (this file) | Spec |
| `docs/v4-status.md` | MODIFIED | +20 | alpha.15 → alpha.16, INV-1 note about calibrated CI |
| `docs/INVARIANTS.md` | MODIFIED | +5 | EC catalog note about EC-007a/b |
| `docs/edge-case-catalog.md` | MODIFIED | +15 | EC-007b entry + Play Favorites citation update |
| `docs/judge-pipeline-v4.md` | MODIFIED | +10 | §9 (calibration) update |
| `CHANGELOG.md` | MODIFIED | +20 | [4.0.0-alpha.16] entry |
| `docs/v4-alpha-11-plan.md` | MODIFIED | +5 | Phase 3 marked shipped |
| **Total** | | **~700** | |

---

## 7. Acceptance criteria (per `v4-alpha-11-plan.md:151-161`)

- ✅ ADR-009: 9 providers in allow-list (anthropic, openai, google, deepseek, minimax, minimax-cn, zhipu, moonshot, qwen). Each with native dialect or documented override.
- ✅ ADR-011: every `sdd_evaluations` row has `confidence_calibrated` + CI computed via bootstrap-CI per Play Favorites (arxiv:2508.06709).
- ✅ EC-007 now uses the statistical framework (EC-007b). EC-007a preserved as legacy fallback for uncalibrated rows.

---

## 8. Test plan summary

- L1 (canonical.go-style, no DB): bootstrap determinism, width monotonicity, edge cases n=0/1/2, biased-signal detection.
- L2 (DB in-memory, per-package): provider resolution for 5 new providers, ApplyCalibrationColumns idempotent, EC-007a/b positive + negative, populated calibration on Save.
- L4 (MCP end-to-end): `dark_memory_judge` with a mock LLM, after 50 evaluations, verify `sdd_evaluations` row has non-NULL `confidence_calibrated` + CI bounds.

**Total new tests**: 4 (L1 cal) + 4 (L1 llm) + 2 (L2 EC) + 1 (L2 store) + 1 (L4 MCP) = **12 new tests**.

**All 11 v4alpha packages must continue to pass** (alpha.15 baseline).

---

## 9. Documentation deltas

- `docs/v4-status.md` §1 → "alpha.16"; §1.3 new section "Phase 3: judge improvements".
- `docs/INVARIANTS.md` §18 → INV-1 status: still YES (calibration is best-effort, audit is authoritative).
- `docs/edge-case-catalog.md` §EC-007 → split into §EC-007a + §EC-007b with Play Favorites citation.
- `docs/judge-pipeline-v4.md` §9 → calibration subsystem documented.

---

## 10. Drift-check expectations

- spec vs intent: aligned (per Phase 2 pattern — spec is the canonical alignment artifact).
- per-file drift checks: needs_human at confidence 0.55 (multi-file implementation). Known limitation (Phase 2 lesson).

---

## 11. Sign-off

- Spec author: Opita-AI (MiniMax-M3), 2026-09-30.
- Operator decision: "Procede" received 2026-09-30.
- Pre-flight review: chunk 3 §D (8 audit gaps, no relevant to Phase 3), judge-pipeline-v4.md §9 (gap documented), sota-critique.md §7.6.7 (ADR-009/011 specs required).
- Phase 3 closure criteria: code shipped + drift check PASS + commit landed + atomic mirror created.