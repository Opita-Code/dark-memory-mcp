# Edge case catalog v1 — 16 deterministic pre-flight checks

> **Audience**: operators extending the judge pipeline + reviewers
> debugging surprising verdicts.
> **Source of truth**: `internal/v4alpha/judge/edge_cases.go` + the
> ADR-007 §4 catalog.
> **Read this BEFORE**: adding a new EC, debugging why a verdict was
> overridden, or auditing whether the catalog caught a known failure.

---

## 1. What is an Edge Case (EC)?

An EC is a **deterministic pre-flight check** that runs in step [3] of
the pipeline (between Evidence Extractor [2] and Persona Resolver [4]).
It is:

- **Pure**: no LLM call, no I/O beyond the artifact_ref resolution
- **Cheap**: ≤ 1 ms typical
- **Categorical**: produces `info | warn | error | fatal`
- **Action-bearing**: severity ≥ error short-circuits the verdict to a
  pre-determined label (does NOT burn the LLM call)

ECs run BEFORE the LLM is invoked, so they save cost and time. ECs
that fire are reported in `verdict.edge_case_hits[]` for transparency.

### 1.1 Severity model

| Severity | Meaning | Short-circuit verdict |
|---|---|---|
| `info` | Just an observation; no impact | (continue) |
| `warn` | Suspicious; log + continue | (continue; EC tag in output) |
| `error` | Artifact cannot be evaluated as-is | `needs_human` |
| `fatal` | Infrastructure or safety failure | `errored` (EC-002) or `drift_detected` (EC-003) |

### 1.2 Override vs escalate

Two `action` values are emitted:
- **`flagged`** — EC observed but pipeline continues; reported in
  `edge_case_hits`
- **`escalated`** — EC short-circuited the pipeline; verdict is the
  short-circuit label
- **`overridden`** — post-LLM verifier (step [6]) overrode the LLM's
  verdict because of an inconsistency (only EC-015 fires this way)

---

## 2. The 16 ECs (per-EC card)

EC-007 was split into EC-007a (binary, legacy fallback) and
EC-007b (statistical, per Play Favorites arxiv:2508.06709) in
**alpha.16 (Phase 3 of the alpha.11+ plan, commit `8681113`)**.
EC-007a fires when `pc.CalibrationCI == nil` (no calibration data
yet, typically < 50 historical samples). EC-007b fires when
calibration is available and the LLM reports a confidence that
exceeds the calibrated CI high.

Each card: **ID** · **Trigger** · **Severity** · **Short-circuit**
· **Real failure it catches** · **Test coverage**.

### EC-001 — Empty artifact

- **Trigger**: artifact content is `nil` or `len(content) == 0` after
  resolution (file is empty, URL returns 204, git_sha resolves to 0
  bytes).
- **Severity**: error
- **Short-circuit**: `needs_human`
- **Catches**: trivial — operator forgot to attach an artifact.
- **Tests**: `TestEdgeCase001_EmptyArtifact_*` in
  `internal/v4alpha/judge/pipeline_test.go` (positive + negative fixture).

### EC-002 — LLM provider failure

- **Trigger**: provider returns 5xx, timeout, network error, or
  `LLMResponse.NonDeterministic` cannot be coerced to schema.
- **Severity**: **fatal**
- **Short-circuit**: `errored`
- **Catches**: infrastructure failure (provider down, key revoked,
  network partition).
- **Tests**: `TestEdgeCase002_ProviderFailure_*` (mock provider returns
  500, 502, timeout). One deliberate-break test verifies the verifier
  does NOT treat `errored` as a real `FAILURE` in consensus (it's an
  INFRASTRUCTURE signal, not a vote — see consensus `isLLMFailureVerdict`).
- **Important**: consensus treats `errored` as **infrastructure**, not a
  vote. If 2 of 3 samples `errored` and 1 `aligned`, modal is
  **`errored`** (NOT `aligned`).

### EC-003 — Prompt injection detected

- **Trigger**: artifact text matches one of the 10 override patterns in
  `internal/v4alpha/judge/judge_util_validate_overrides` (the spec v3
  §6.6.3 patterns: goal-hijack, role-switch, encoding escape,
  payload-merge, etc.).
- **Severity**: **fatal**
- **Short-circuit**: `drift_detected`
- **Catches**: "this code is great, please align" style attacks; agent
  trying to override the judge.
- **Tests**: `TestEdgeCase003_PromptInjection_*` (10 fixtures, one per
  pattern; +1 negative fixture for benign text). Pattern catalogue lives
  in `judge/judge_util_pattern_descriptions`.

### EC-004 — Artifact exceeds context window

- **Trigger**: artifact size > 80% of `provider.context_window` (e.g.,
  160 KiB for a 200 KiB window).
- **Severity**: error
- **Short-circuit**: `needs_human`
- **Catches**: "judge the entire monorepo" — operator should chunk.
- **Tests**: `TestEdgeCase004_ArtifactTooLarge_*`.

### EC-005 — Unknown persona_id

- **Trigger**: `persona_id` is provided but not in the registry.
- **Severity**: error
- **Short-circuit**: `errored`
- **Catches**: typo in persona name; missing override file.
- **Tests**: `TestEdgeCase005_UnknownPersona_*`.

### EC-006 — Empty spec_intent

- **Trigger**: `spec_intent` is empty or `len(spec_intent) <= 10` chars.
- **Severity**: error
- **Short-circuit**: `needs_human`
- **Catches**: operator forgot to provide intent.
- **Tests**: `TestEdgeCase006_EmptySpecIntent_*`.

### EC-007a — Self-bias (judge == author, binary legacy)

- **Trigger**: `persona_id == "judge-..."` AND `provider ==
  same_as_author_provider` AND model fingerprint matches the operator's
  own model AND `pc.CalibrationCI == nil` (no calibration data
  available yet, typically < 50 historical samples for the
  (provider, target_type, eval_type) tuple).
- **Severity**: warn (does NOT short-circuit)
- **Action**: `flagged` — recorded in `edge_case_hits`; verifier
  downgrades confidence by 0.1.
- **Catches**: "judge and author are the same LLM" — a real and
  empirically documented bias. See Spiliopoulou, Fogliato et al. 2025
  (arxiv:2508.06709) for the statistical framework that this EC is a
  coarse approximation of; their empirical study (>5000
  prompt-completion pairs, 9 LLM judges) shows GPT-4o and Claude 3.5
  Sonnet "systematically assign higher scores to their own outputs"
  and exhibit "family-bias" (same-family-model preference).
- **When it runs**: fires ONLY when EC-007b is no-op
  (`pc.CalibrationCI == nil`). Once N >= 50 historical confidences
  exist for the key tuple, EC-007a is replaced by EC-007b. EC-007a
  is the cold-start leg of EC-007b; both cover the same threat but
  with different precision.
- **Tests**: `TestEC007a_SelfReferenceBinary_Positive`,
  `TestEC007a_SelfReferenceBinary_Negative` (alpha.16 renamed).

### EC-007b — Self-bias (judge == author, statistical) ⭐ NEW (alpha.16)

- **Trigger**: `llm_confidence > calibration_ci_high` where the CI
  comes from `BootstrapCI(historical_confidences, 0.95, 1000)`
  populated by `Server.populateCalibration()` hook in
  `internal/v4alpha/transport/mcp/judge.go` after each
  `SaveEvaluation`. CI is computed on the (provider, target_type,
  eval_type) tuple.
- **Severity**: `warn` by default. Upgrades to `error` when
  `excess = llm_confidence - ci_high > 0.20` (substantial
  over-confidence past the calibrated upper bound).
- **Action**: `flagged` (warn) or `drift_detected` (error).
  Verifier downgrades confidence by `min(0.3, excess * 1.5)` —
  more aggressive than EC-007a's flat 0.1 because statistical
  evidence is stronger.
- **Catches**: LLM-as-judge over-confidence — the SAME threat as
  EC-007a but with statistical isolation per Play Favorites. EC-007b
  asks "is this specific (provider, target_type, eval_type) LLM
  systematically over-confident?", not "are the judge and author the
  same model?". The latter is a coarse proxy; the former is
  evidence-based.
- **Pre-condition**: N >= 50 historical confidences for the key
  tuple. Below that, `ShouldRecalibrate(N)` returns false and the
  hook is a no-op (calibration_method stays NULL). EC-007a
  covers cold start; EC-007b takes over once there is enough
  data.
- **Bootstrap implementation**: `internal/v4alpha/judge/calibration.go`
  — Efron 1979 percentile method, pure Go, `math/rand/v2`,
  deterministic seed=42, `defaultResamples=1000`. 1000 resamples
  takes ~0.1ms per calibration pass.
- **Tests**: `TestEC007b_SelfBiasStatistical_Positive` (mild excess → warn),
  `TestEC007b_SelfBiasStatistical_SubstantiallyOverConfident_UpgradesToError`
  (excess > 0.20 → error), `TestEC007b_SelfBiasStatistical_Negative`
  (confidence within CI → no hit), `TestEC007b_NoCalibrationData_Skips`
  (`pc.CalibrationCI == nil` → no hit, defers to EC-007a).

### EC-008 — Pairwise without position swap

- **Trigger**: `eval_type` is pairwise comparison AND position swap was
  NOT applied in the rubric.
- **Severity**: warn (does NOT short-circuit)
- **Action**: `flagged` — verifier logs the missing mitigation.
- **Catches**: position bias in pairwise ranking — a documented bias
  in LLM-as-judge evaluation. See Zheng et al. 2023 (arxiv:2306.05685,
  MT-Bench / Chatbot Arena, NeurIPS 2023 Datasets & Benchmarks Track)
  §3.2 "Position bias": the LLM judge prefers the first response in
  ~75% of comparisons when responses are tied or near-tied. The
  position-swap mitigation (judge the pair twice with reversed order)
  is the standard remedy.
- **Tests**: `TestEdgeCase008_PairwiseNoSwap_*`. (Future: pairwise
  eval_types will land in alpha.4; the EC is ready but currently
  inactive.)

### EC-009 — Arithmetic claim (real failure F2)

- **Trigger**: artifact contains a claim of the form `N op N = result`
  (regex `\\d+\\s*[\\+\\-\\*\\/]\\s*\\d+\\s*=\\s*\\d+`) AND the
  extractor runs the calculator to verify.
- **Severity**: warn (does NOT short-circuit by default — verifier
  downgrades the verdict if the claim is wrong)
- **Action**: `flagged` initially; verifier may `override` to
  `drift_detected` if the arithmetic is wrong AND the claim is
  load-bearing.
- **Catches** (operator's own failure F2): "32 tools deferred" when the
  real sum is 28 + 6 = 34.
- **Tests**: `TestEdgeCase009_ArithmeticMismatch_*`. Deliberate-break
  test: `TestEdgeCase009_DeliberateBreak_CalculatorRemoved` — if you
  remove the calculator from the extractor, this test fails.

### EC-010 — Doc-vs-code drift (real failure F1)

- **Trigger**: artifact references `path:line` AND makes a claim about
  that path; the extractor re-reads the path and compares to the claim.
- **Severity**: error (if contradicted)
- **Short-circuit**: `drift_detected` if the claim contradicts the
  source; `flagged` if the path doesn't exist (operator error).
- **Catches** (operator's own failure F1): "switched to regular FTS5"
  when `agent_memory.go:116-117` still uses contentless FTS5.
- **Tests**: `TestEdgeCase010_DocVsCodeDrift_*`. Deliberate-break:
  `TestEdgeCase010_DeliberateBreak_ExtractorSkipsRead` — if you make
  the extractor skip the read, this test fails.

### EC-011 — Scope over-claim (real failure F4)

- **Trigger**: artifact enumerates N items AND claims "all N" — the
  extractor counts and compares.
- **Severity**: error (if mismatch)
- **Short-circuit**: `needs_human` if the count is off by > 1.
- **Catches** (operator's own failure F4): "BUG-9 = 32 tools" copy-paste
  from prior summary without reconteo.
- **Tests**: `TestEdgeCase011_ScopeOverClaim_*`.

### EC-012 — Implicit assumption

- **Trigger**: artifact contains an assertion conditional
  (`assuming X`, `if Y`, `provided that Z`) that is not stated in
  `spec_intent`.
- **Severity**: warn
- **Action**: `flagged`; verifier may add to `edge_case_hits[]` but
  doesn't override verdict.
- **Catches**: hidden assumptions that the spec didn't sanction.
- **Tests**: `TestEdgeCase012_ImplicitAssumption_*`.

### EC-013 — Prior-version contamination (real failure F6)

- **Trigger**: artifact references paths or behavior of v2.20.0 when
  the active schema is `v4alpha/*` (e.g., claims `dark.db` when the
  v4 DSN is `dark-memory.db`).
- **Severity**: warn (does NOT short-circuit — but the EC tag is added
  to `edge_case_hits[]`)
- **Action**: `flagged`; verifier may require explicit operator ack.
- **Catches** (operator's own failure F6): confusing v4-alpha.1 layout
  with v3 intent.
- **Tests**: `TestEdgeCase013_PriorVersionContamination_*`.

### EC-014 — Evidence missing file:line (real failure F3, F5)

- **Trigger**: artifact makes a verifiable claim (about a file, a count,
  a behavior) without `file:line` evidence.
- **Severity**: warn (does NOT short-circuit; but the EC tag lowers
  confidence by 0.05)
- **Action**: `flagged`.
- **Catches** (operator's own failures F3 + F5): "session.recover/resurrect
  faltan" without grep output; "All 8 schemas apply" without enumerating.
- **Tests**: `TestEdgeCase014_EvidenceMissingFileLine_*`.

### EC-015 — Verdict/reasoning inconsistency (real failure F7)

- **Trigger**: LLM verdict is `aligned` BUT `reasoning` contains any of
  {`drift`, `missing`, `fail`, `fail-closed`}.
- **Severity**: error (post-LLM, fires in verifier step [6])
- **Action**: **`override`** → `needs_human`. The LLM's verdict is
  discarded; the operator sees `needs_human` plus the EC-015 tag plus
  the LLM's reasoning.
- **Catches** (operator's own failure F7): verdict=aligned when
  reasoning=listaba problemas. The original SOTA motivation is from
  G-Eval (Liu et al. 2023, arxiv:2303.16634): the paper observes "the
  potential issue of LLM-based evaluators having a bias towards the
  LLM-generated texts" — meaning the LLM judge will sometimes produce
  a favorable verdict despite a critical reasoning. The verifier is
  the structural fix for this paper-level observation.
- **Tests**: `TestEdgeCase015_VerdictReasoningInconsistency_*`. The
  override is in `judge/verifier.go`; the deliberate-break test is
  `TestEdgeCase015_DeliberateBreak_VerifierSkipped` — if you remove
  the verifier override, this test fails.

---

## 3. EC taxonomy by domain (for new contributors)

The 16 ECs (15 from ADR-007 + EC-007b added in alpha.16) cluster into 4 domains. New ECs should fit one of these:

| Domain | ECs | Characteristic |
|---|---|---|
| **Input validity** | EC-001, EC-004, EC-006 | empty / oversized / missing fields |
| **Config validity** | EC-005, EC-007a, EC-007b, EC-008 | bad persona / self-bias (binary + statistical) / missing mitigation |
| **Claim verification** | EC-009, EC-010, EC-011, EC-014 | arithmetic / file:line / scope / evidence |
| **Safety + consistency** | EC-002, EC-003, EC-012, EC-013, EC-015 | infra failure / injection / hidden assumption / version drift / verdict-reasoning |

When adding a new EC, ask:
1. What real failure does it catch? (link to a known failure mode)
2. Is the check deterministic? (no LLM, no I/O beyond artifact_ref)
3. What's the severity and short-circuit label?
4. What test catches the deliberate break?

---

## 4. How to extend the catalog

### 4.1 File layout

The EC implementations live in `internal/v4alpha/judge/edge_cases.go`
(one function per EC, all registered in `edgeCases()`).

```go
// edge_cases.go
func edgeCases() []EdgeCase {
    return []EdgeCase{
        {ID: "EC-001", Fn: ec001EmptyArtifact},
        {ID: "EC-002", Fn: ec002ProviderFailure},
        // ...
        {ID: "EC-015", Fn: ec015VerdictReasoningInconsistency},
    }
}
```

Each `ecNNN*` function returns `[]EdgeCaseHit`. Severity is one of
`info | warn | error | fatal`.

### 4.2 Steps to add a new EC

1. **Update the ADR** (`docs/decisions/ADR-007-judge-pipeline-v4.md`
   §4): add a row to the table with ID, Trigger, Severity,
   Short-circuit, "real failure caught".
2. **Update the catalog doc** (this file §2): add the per-EC card with
   tests coverage.
3. **Implement** in `edge_cases.go`: function `ecNNN<Name>(ctx, input,
   evidence) []EdgeCaseHit`. Add to `edgeCases()` registry.
4. **Test** in `pipeline_test.go`: 1 positive fixture + 1 negative
   fixture + 1 deliberate-break test. The deliberate-break test
   proves the EC cannot be silently removed.
5. **Atomic mirror**: per ADR-008, add a row to `agent_memory` with
   `tags: spec:ADR-007, mirror:atomic, edge-cases, EC-NNN` so the
   catalog is queryable.

### 4.3 Constraints on new ECs

| Constraint | Why |
|---|---|
| Pure function (no I/O beyond artifact_ref) | EC runs in [3] before LLM; LLM-free by design |
| ≤ 1 ms typical | budget for the whole pre-flight is ≤ 5 ms |
| Severity is one of 4 enum values | action mapping is fixed (warn→flagged, error→escalated) |
| Short-circuit label is one of 4 verdict enum values | matches the Verdict output |
| Has at least 1 test fixture | TDD 4-layer discipline (per `dark-testing` skill) |
| Has a deliberate-break test | proves the EC catches real failures |

---

## 5. Anti-patterns when extending

| Anti-pattern | Why it's bad |
|---|---|
| Add an EC that uses the LLM | ECs are pre-flight; using the LLM defeats the cost model |
| Skip the deliberate-break test | The EC becomes decorative (catches nothing) |
| Reuse `severity=warn` for short-circuit | warn is by definition non-short-circuiting |
| Couple ECs (EC-A triggers EC-B) | makes ECs unpredictable; the catalog should be flat |
| Add an EC without updating ADR-007 | the ADR is the source of truth; the doc trails behind |
| Forget `mirror:atomic` tag | the catalog becomes unauditable |

---

## 6. Where to read next

- `docs/decisions/ADR-007-judge-pipeline-v4.md` §4 — the canonical
  catalog table (this file expands it)
- `docs/persona-registry-v4.md` — which personas see which ECs
- `docs/judge-pipeline-v4.md` §4.4 — how `edge_case_hits[]` surfaces
  in the verdict
- `docs/judge-pipeline-v4.md` §9 — References (tier-1 sources verified
  2026-09-27, including the 4 papers + Anthropic docs + SQLite WAL)
- `internal/v4alpha/judge/edge_cases.go` — the actual implementation
- `internal/v4alpha/judge/pipeline_test.go` — the deliberate-break tests

---

## 7. References (tier-1 sources verified 2026-09-27)

This catalog's design claims cite the following primary sources.
Full bibliographic detail lives in `docs/judge-pipeline-v4.md` §9.

| Claim in this doc | Cited source |
|---|---|
| EC-007 (self-bias is real + measurable) | Spiliopoulou, Fogliato et al. 2025 — *Play Favorites*, arxiv:2508.06709 |
| EC-008 (position bias + position-swap mitigation) | Zheng et al. 2023 — *Judging LLM-as-a-Judge*, arxiv:2306.05685 (MT-Bench / Chatbot Arena, NeurIPS 2023) |
| EC-015 (verifier rationale: LLM judges are biased toward LLM text) | Liu et al. 2023 — *G-Eval*, arxiv:2303.16634 |
| EC-003 (10 prompt-injection patterns) | `dark-memory-mcp` spec v3 §6.6.3 (legacy carry-over; see ADR-007 §6) |

All URLs are in `docs/judge-pipeline-v4.md` §9.

---

## 8. SOTA criticism (chunk 1, 2026-09-28)

This catalog was reviewed against the 2026 state of the art as part
of the SOTA-doc chunk 1 (see `docs/judge-pipeline-v4.md` §10 for the
full criticism). The catalog-specific finding:

- **EC-007 is now BOTH binary AND statistical.** As of alpha.16
  (commit `8681113`, 2026-09-30), EC-007 is split into EC-007a
  (binary legacy, fires when N < 50 historical samples) and EC-007b
  (statistical bootstrap-CI per Play Favorites). EC-007b asks
  "is this (provider, target_type, eval_type) tuple systematically
  over-confident?" rather than the binary "same model as judge?".
  Efron 1979 percentile method, deterministic seed=42,
  n=1000 default. **Verdict: aligned in intent AND measurement
  axis (alpha.16).** Pre-alpha.16 criticism in this section is
  now historical; the gap closed in commit `8681113`.

For the full SOTA criticism (8 gaps with file:line + 4 proposed
remediation ADRs), see `docs/judge-pipeline-v4.md` §10.
