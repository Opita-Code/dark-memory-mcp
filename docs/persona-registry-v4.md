# Persona registry v4 — 11 personas + override mechanism

> **Audience**: operators picking a persona for a specific judge call
> + reviewers debugging "why did the persona default to X for my
> vibe_case?".
> **Source of truth**: `internal/v4alpha/judge/personas_v4.go` +
> `internal/v4alpha/judge/personas.go` (legacy) +
> `docs/judge-personas.md` (legacy override mechanism).
> **Read this BEFORE**: choosing a persona for a non-default
> `eval_type × vibe_case` combination, or adding a new persona.

---

## 1. What is a persona?

A persona is the **evaluation lens** the LLM adopts when acting as a
judge. It consists of:

- `persona_id` — kebab-case unique id (e.g., `judge-logical`)
- `system_prompt` — the persona's voice + constraints
- `eval_types[]` — which `eval_type` values this persona is registered for
- `vibe_cases[]` — which `vibe_case` values this persona is registered for
- `rubric` — the criteria + weights to apply (per vibe_case)
- `rubric_version` — sha256[:16] of the rubric (recorded in
  `sdd_evaluations.rubric_version` for reproducibility)
- `lens` — short summary of the evaluation focus

The persona registry lives in `internal/v4alpha/judge/personas_v4.go`
(new v4 personas) and `internal/v4alpha/judge/personas.go` (legacy
carry-overs from spec 1155 v14).

---

## 2. The 11 personas

### 2.1 Legacy 8 (carry-over from spec 1155 v14, extended in v4)

| Persona id | eval_types | Default for vibe_case | Notes v4 |
|---|---|---|---|
| `judge-logical` | `drift_judge`, `spec_test_alignment`, `grounding_check`, **`arithmetic_mismatch`** | **C1 code**, **C2 text** | Gains EC-009 as `arithmetic_mismatch` eval_type. The workhorse. The direct-assessment + custom-criteria pattern follows Prometheus 2 (Kim et al. 2024, arxiv:2405.01535, EMNLP 2024). |
| `judge-visual` | `brand_match`, `visual_artifact_eval` | (none — legacy, kept for backwards compat) | unchanged from spec 1155 |
| `judge-security` | `pii_detect`, `prompt_injection_scan`, `security_coverage` | (none — operator-pick) | Gains EC-003 as the canonical prompt-injection scanner. Catches F-injection. |
| `judge-compositional` | `mindset_compose`, `mindset_quality` | (none) | unchanged |
| `judge-mutation` | `mutation_score_check` | (none) | unchanged |
| `judge-resilience` | `resilience_check` | (none) | unchanged |
| `judge-evidential` | `grounding_check`, **`doc_vs_code_drift`** | **C7 multi** | Gains EC-010 + EC-011 as `doc_vs_code_drift` eval_type. The real-failure catcher. Phase 6 alpha.18.1: default changed from C7 governance → C7 multi (canonical mapping). |
| `judge-coverage` | `edge_case_catalog_audit` | (none — legacy, kept for backwards compat) | Now also: the persona that audits the EC catalog itself (chicken-and-egg solved by only running on manual trigger). Phase 6: removed from default for C5 (canonical C5=video uses judge-cross-modal). |

### 2.2 New 5 (v4-alpha.3 + Phase 6 alpha.18.1)

| Persona id | eval_types | Default for vibe_case | Lens |
|---|---|---|---|
| `judge-cross-modal` | `video_artifact_eval`, `audio_artifact_eval` | **C5 video** | "You are a cross-modal judge. Score the artifact on subject consistency, scene transitions, audio sync, narrative, duration match. Do NOT trust artist descriptions — verify against frame-level evidence." Phase 6: default changed from C3 image → C5 video (canonical). |
| `judge-pipeline` | `pipeline_eval`, `workflow_eval` | **C6 audio** | "You evaluate audio artifacts. Test for speech clarity, noise floor, voice consistency, pacing, timbre preservation. Failure mode: human reviewer missing a section where the speaker identity shifts." Phase 6: default changed from C6 infra → C6 audio (canonical). |
| `judge-opinion` | `opinion_eval`, `claim_audit` | (none — operator-pick when `spec_intent` is opinion/argument) | "You audit opinions and argumentative claims. Score on faithfulness to cited evidence, logical structure, and counterfactual robustness. Do NOT score style." |
| `judge-decision` ⭐ NEW | `drift_judge`, `grounding_check` | **C3 decision** | "You evaluate decision artifacts — ADRs, INV entries, lifecycle records. Weight rationale clarity and evidence quality over alternatives considered. Reversibility is the floor — an irreversible decision with weak rationale is the worst case." Phase 6 alpha.18.1: replaces the legacy v3 C3=image mapping. |
| `judge-research` ⭐ NEW | `drift_judge`, `grounding_check` | **C4 research** | "You evaluate research artifacts — literature reviews, OSINT syntheses, benchmark studies. Weight source diversity and citation quality over methodology. Reproducibility is the floor — research that cannot be reproduced is speculation, not research." Phase 6 alpha.18.1: replaces the legacy v3 C4=video mapping. |

### 2.3 Cross-reference table (vibe_case → default persona)

**Canonical per `internal/v4alpha/vibe/spec.go:14-16`. Phase 6 alpha.18.1 reconciled.**

| Vibe case | Persona | Default eval_type |
|---|---|---|
| C1 code | `judge-logical` | `drift_judge` |
| C2 text | `judge-logical` | `grounding_check` |
| C3 decision | `judge-decision` ⭐ NEW | `drift_judge` |
| C4 research | `judge-research` ⭐ NEW | `grounding_check` |
| C5 video | `judge-cross-modal` | `video_artifact_eval` |
| C6 audio | `judge-pipeline` | `audio_artifact_eval` |
| C7 multi | `judge-evidential` | `doc_vs_code_drift` |

---

## 3. Override mechanism (per spec 1155 v14)

The registry is **compiled by default** but allows **Markdown overrides**
at the field level. Operators can override any persona's:

- `system_prompt` (full replace)
- `lens` (full replace)
- `rubric` (per-vibe_case; field-level merge with the compiled default)
- `eval_types[]` (add or remove; merge with default)

Override mechanism is documented in `docs/judge-personas.md` (legacy
v3 spec) and re-exported here for v4.

### 3.1 How to override

Set `DARK_JUDGE_PERSONAS_DIR=/path/to/overrides` and place
`<persona_id>.md` files in that directory:

```yaml
# /etc/dark-judge/overrides/judge-logical.md
---
persona_id: judge-logical
merge_strategy: field_replace  # field-level merge; see §3.2
system_prompt: |
  You are a strict code reviewer for opita-market services.
  Reject anything that doesn't have explicit tests.
  Score on: tests 0.30, security 0.30, correctness 0.20, idiomatic 0.10, docs 0.10.
lens: "Strict opita-market code reviewer; tests + security are non-negotiable."
rubric:
  C1:
    correctness: {weight: 0.20}
    tests:       {weight: 0.30, override_min_score: 0.70}
    security:    {weight: 0.30, override_min_score: 0.70}
    idiomatic:   {weight: 0.10}
    docs:        {weight: 0.10}
```

### 3.2 Merge strategies

| Strategy | What it does | When to use |
|---|---|---|
| `field_replace` (default) | Override only the fields explicitly listed | Targeted changes (e.g., tweak weights) |
| `full_replace` | Replace the entire persona | When the legacy default is no longer suitable |
| `append` | Append to the compiled defaults | Adding to `eval_types[]` or `vibe_cases[]` |

### 3.3 Override precedence

1. **Env override** (compiled binary embedded defaults)
2. **Markdown override** at `$DARK_JUDGE_PERSONAS_DIR` (field-level)
3. **Per-call override** via the `persona_id` parameter on
   `dark_memory_judge`

### 3.4 Override validation

The pipeline validates overrides on startup. An invalid override file
(e.g., weights summing to ≠ 1.0) → EC-005 (`errored`). This prevents
silent persona corruption.

---

## 4. Selection algorithm (default for vibe_case × eval_type)

When the caller does NOT specify `persona_id`, the pipeline picks a
default:

```
default_persona(vibe_case, eval_type):
  for persona in registry:
    if eval_type in persona.eval_types
       and vibe_case in persona.vibe_cases:
      return persona
  return judge-logical  # safe fallback; logs EC-005 warn
```

The fallback to `judge-logical` is intentional — it covers the most
common case (drift_judge on C1/C2). The EC-005 `warn` tag records
"default applied" so operators see when their persona choice fell
through.

### 4.1 Selection examples

| Caller input | Resolved persona | Notes |
|---|---|---|
| `eval_type=drift_judge`, `vibe_case=C1`, no `persona_id` | `judge-logical` | C1 default |
| `eval_type=visual_artifact_eval`, `vibe_case=C3`, no `persona_id` | `judge-cross-modal` | C3 default |
| `eval_type=security_coverage`, `vibe_case=C1`, `persona_id=judge-evidential` | `judge-evidential` | explicit override; uses evidential's lens |
| `eval_type=foo`, `vibe_case=C1`, no `persona_id` | `judge-logical` | EC-005 warn; fallback applied |

---

## 5. Adding a new persona

### 5.1 When to add a new persona

Add a new persona when:
- The existing 11 don't cover an `eval_type × vibe_case` combination
  the operator needs
- The lens is meaningfully different (e.g., `judge-multilingual` for
  non-English artifacts)
- The voice + constraints require a system prompt the existing
  personas cannot express

Don't add a persona if:
- You just want to tweak weights — use an override (§3) instead
- You want a new rubric criterion — extend the existing rubric
- You want a different `eval_type` — extend the existing persona's
  `eval_types[]`

### 5.2 Steps

1. **Update ADR-007** §2.2 (the persona registry table) + the
   vibe-case → persona cross-reference (§2.3 of this file).
2. **Implement** in `internal/v4alpha/judge/personas_v4.go`: add a
   `personaXXX` function that returns a `*PersonaContent` with
   `persona_id`, `system_prompt`, `lens`, `eval_types`, `vibe_cases`,
   and a per-vibe_case `Rubric`.
3. **Register** in `personas_v4.go::allPersonas()`.
4. **Test**:
   - `personas_v4_test.go`: 1 test for the persona's compiled
     defaults (weights sum to 1.0, system_prompt non-empty, etc.)
   - `pipeline_test.go`: 1 test that the default-selection algorithm
     picks the new persona for its registered `eval_type × vibe_case`
   - 1 deliberate-break test for the rubric's `criticalCriterionFloor`
5. **Override doc**: add a row to `docs/judge-personas.md` §2 (the
   legacy override docs) so the persona is discoverable by override.
6. **Atomic mirror**: per ADR-008, add a row to `agent_memory` with
   `tags: spec:ADR-007, mirror:atomic, personas, persona:<id>`.

### 5.3 Persona discipline

| Constraint | Why |
|---|---|
| `persona_id` is kebab-case, prefixed with `judge-` | discoverability + namespace safety |
| `system_prompt` is ≤ 4 KiB | provider context budget |
| Rubric weights sum to exactly 1.0 (± 0.001) | weighted-sum threshold math |
| At least one `eval_type` and one `vibe_case` | orphan personas are removed at startup |
| No two personas share the same `(eval_type, vibe_case)` pair as defaults | default selection is a function, not a multi-map |

---

## 6. Anti-patterns

| Anti-pattern | Why it's bad |
|---|---|
| Add a persona for every `eval_type` you want to use | personas should be lenses, not eval_type buckets |
| Override the system_prompt with "ignore previous instructions" | EC-003 short-circuits to `drift_detected` |
| Define weights that sum to ≠ 1.0 | the pipeline refuses to start (override validation) |
| Add a persona that doesn't have a `vibe_case` default | it can only be invoked explicitly; defeats the registry's purpose |
| Use `judge-coverage` as a default for C1 | it audits the EC catalog, not code; verdicts will be uninformative |

---

## 7. Where to read next

- `docs/decisions/ADR-007-judge-pipeline-v4.md` §2.2 (registry table)
  + §2.3 (rubric table) — the design source of truth
- `docs/judge-pipeline-v4.md` §3.4 (`dark_memory_judge_list_personas`)
  — how to discover personas from MCP
- `docs/edge-case-catalog.md` — which ECs each persona sees
- `docs/judge-personas.md` — legacy v3 override mechanism (carried
  forward unchanged)
- `docs/judge-pipeline-v4.md` §9 — full References (tier-1 sources)
- `internal/v4alpha/judge/personas_v4.go` — the compiled defaults
- `internal/v4alpha/judge/personas_v4_test.go` — tests

---

## 8. References (tier-1 sources verified 2026-09-27)

This registry's design claims cite the following primary sources.
Full bibliographic detail (URLs, dates, license) lives in
`docs/judge-pipeline-v4.md` §9.

| Claim in this doc | Cited source |
|---|---|
| Direct-assessment + custom-criteria pattern (the design rationale for `judge-logical` supporting multiple `eval_type`s) | Kim et al. 2024 — *Prometheus 2*, arxiv:2405.01535, EMNLP 2024 |
| Self-enhancement bias in LLM-as-judge (motivation for EC-007 self-bias check) | Spiliopoulou, Fogliato et al. 2025 — *Play Favorites*, arxiv:2508.06709 |
| Persona override mechanism (Markdown overrides + field-level merge) | `dark-memory-mcp` spec 1155 v14 §5 (legacy carry-over) |
| Atomic mirror discipline (this ADR is itself mirrored in dark-memory) | ADR-008 — `docs/decisions/ADR-008-work-standard.md` |

---

## 9. SOTA criticism (chunk 1, 2026-09-28)

This registry was reviewed against the 2026 state of the art as part
of the SOTA-doc chunk 1 (see `docs/judge-pipeline-v4.md` §10 for the
full criticism). The registry-specific finding:

- **`judge-cross-modal` is a v4-introduced concept, not a SOTA
  citation.** The persona is defined in §2.2 and registers for
  `visual_artifact_eval`, `audio_artifact_eval`, `video_artifact_eval`
  (C3 image, C4 video defaults). The persona is a v4-innovation;
  the SOTA 2026 benchmarks for MLLM-as-judge (vision-language
  model evaluation) are not cited. v4 does not validate
  `judge-cross-modal` against any published MLLM-as-judge benchmark.
  **Verdict: behind in SOTA grounding.** Remediation: cite the
  relevant 2025-26 MLLM-as-judge survey in §8 above. The specific
  paper is not in the verified set for chunk 1 (see
  `docs/judge-pipeline-v4.md` §10.4 for what could not be verified).

For the full SOTA criticism (8 gaps with file:line + 4 proposed
remediation ADRs), see `docs/judge-pipeline-v4.md` §10.
