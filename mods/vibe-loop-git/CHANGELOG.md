# Changelog

All notable changes to **vibe-loop-git** are documented in this file.
Format: [version] — date — summary.

## [0.4.5] — 2026-10-09 — public MIT release, LOCAL ONLY retired, G4 closed

The mod manifest declared 0.4.5 while this file stopped at 0.4.4. This
entry reconciles them, and records the three decisions that produced
the first public release.

### Changed

- **License MIT** (`mods/vibe-loop-git/mod.json`): the mod is now
  distributable. It carried no license field, which meant it was
  not legally reusable despite being public.
- **LOCAL ONLY retired.** The no-push policy is gone from
  `CONSTITUTION-V4.md` and `.github/workflows/ci.yml`. Commits are
  local only no longer; releases are tagged and published.
- **G4 closed** (`b94b5a3`): `scripts/inject-version.sh` parses
  dotted pre-release versions (`alpha.31`) and mod-tag suffixes
  instead of falling through to `dev/unknown`. Guarded by
  `internal/tools/g4_version_regex_test.go`, calibrated by
  deliberate break. The root cause analysis is retained in
  `docs/v4-status.md` §1.16.

### Fixed

- **`DefaultToolGrants` omitted `LLM_BIND` and `EVENTS`**
  (`internal/recall/assemble.go`). The capability gate silently
  refused four canonical tools in live use:
  `llm_provider_bind`, `llm_provider_probe`, `event_log`,
  `event_replay`. Surfaced as `ErrCapabilityNotGranted` on
  `llm_provider_probe`, which reads like operator configuration
  rather than a code defect. Guarded by
  `tests/wire/tool_gate_contract_test.go`.

## [0.4.4] — 2026-10-08 — two fixes the 0.4.3 entry did not contain, plus documentation truth

0.4.3 recorded L8.1a as instrumented and live. It was not live. This
entry records the two commits that shipped after it.

### Fixed

- **`efa6e59` — `fix(delegation)`: L8.1a advisory dropped at the
  v4alpha→v3 converter.** `RunDelegateIntentCore` computed `TaskClass`
  and attached it to the v4alpha output; `convertV4AlphaToV3Output`
  then dropped it. Every existing L8.1a test called the inner function
  directly and never crossed the converter, the binary contained the
  `task_class` string, and the deploy note claimed "live in the
  process". Only the end-to-end `delegate_intent` call exposed it.
  Fixed: `TaskClassAdvisory` is defined in the stable layer
  (`internal/orchestration`) so the v3 struct can hold it without an
  import cycle, and `convertV4AlphaTaskClass` copies field-by-field
  following the existing `convertV4AlphaAlternatives` convention.
  Added the boundary test that should have existed: build a v4alpha
  output with `TaskClass`, run the converter, assert the v3 output
  carries it. Calibrated by deliberate break — deleting the single
  converter line kills 2 of 3 assertions with messages that name the
  regression.
- **`2a2c36d` — `fix(store)`: option C, redact NLI tokens on read paths
  with a sealed raw accessor.** Resolves a contract that had been
  inverted once and left three of its own tests failing. T-405 found
  that store-level redaction broke `drift_judge` (empty bearer → 401 →
  `needs_human`), inverted the store to return raw, and never updated
  the three `internal/tools` tests that asserted the original contract.
  This resolves it without picking a side: the store **redacts** on
  `GetProject`/`ListProjects`, and `GetProjectRaw` is a **sealed**
  accessor for the only four call sites that must authenticate —
  `EnsureNLIRouter`, `ensureLLMJudge`, `llm_provider_bind`,
  `llm_provider_probe`. The fourth is the dangerous one: `provider_bind`
  merges partially against the config it loads, so a redacted read
  would **silently wipe the stored token**. Both halves of the contract
  are guarded, and the raw half is calibrated by deliberate break —
  making `GetProjectRaw` redact fails only
  `TestGetProjectRaw_RetainsAuthToken` with "drift_judge would 401",
  while both redaction guards keep passing.
  Two false claims in comments were corrected rather than left next to
  the new code (a `project_list` tool that never existed; a "Store no
  longer calls Redacted()" note that had become false).

### Documentation

- **`docs/v4-status.md` reconciled with measured reality** (spec 1895,
  vibe-loop `alpha-21-docs-truth`). The status table said `alpha.17`
  while the file's own banner said `alpha.30`; it named a binary
  (`dark-memory-v4`, 19.66 MB) that does not exist on disk and a schema
  version string (`v4alpha/2026-09-30/004`) that the live database
  stores as the integer `32`; §1 claimed "57 tools" one line after its
  own header said 73. Measured: **73 canonical tools, 20 namespaces**
  (verified against `tools.NamespaceGroups()`), schema `32`.
  Phase 20 and Phase 21 were absent from the document entirely; both
  are now recorded, including the load-bearing fact that Phase 20
  closed with **zero production callers**.
- **`mod.json`**: `driftJudgesRun` 23 → 24, `needs_human` 1 → 2 (the
  post-restart verification produced drift 1534); L8.1a re-described
  as *wired as advisory* now that `efa6e59` carries it across the
  boundary; the Postgres debt corrected from "4 methods" to the
  **measured 58** — all in `internal/store/postgres`, none in sqlite,
  therefore dormant while production runs SQLite.

### Added

- **`internal/tools/v4_status_consistency_test.go`** — the anti-drift
  mechanism. It measures the live registry and schema version and
  asserts `docs/v4-status.md` reports the same values, failing with the
  field, the documented value and the measured value. Added because a
  cleanup without a mechanism decays within two phases, as this one
  just demonstrably did: nothing compared a doc claim to the real
  system, so the contradiction survived.

### Known, not fixed here

- **G4 (release identity) remains open.** `scripts/inject-version.sh:65`
  cannot parse this project's tag format — verified: both
  `4.0.0-alpha.30-vibe-loop-git-v0.4.1-4-g2a2c36d-dirty` and the
  simplified `4.0.0-alpha30-4-g2a2c36d-dirty` return NO MATCH. Every
  release stamp falls through to dev/unknown, so `IsDev` is always
  true and drift warnings fire on every deploy.
- **`TestRecallAtTime_OrderingNewestFirst` is flaky** — failed 1 of 3
  full-suite runs under parallel load while passing in isolation, in
  code untouched by either commit.

## [0.4.3] — 2026-10-08 — L8.1a instrumented as ADVISORY, and why it is not wired into DECIDE

Second Phase 21 primitive. This one is deliberately **not**
load-bearing, and the reason matters more than the feature.

### Added

`delegate_intent` now returns `task_class`: what the canonical
`vibeflow.Classify` sees in the task, published next to the router's own
decision. Class, confidence, grade, token count, multi-step and code
signals, plus the router's own `router_threshold` so the two opinions
are comparable in one place.

### Why advisory and not wired

DECIDE already routes on `len(task) > 200`
(`internal/v4alpha/delegation/router.go:73`). `Classify` is richer —
keywords, language detection, code signals, multi-artifact. Adopting it
as the router would change **who gets delegated**, and therefore change
spend.

That is a design decision with a cost attached, and Phase 20's own
closeout says these optimizations were never switched on *because
nobody measured them against a real baseline*. Flipping one now would
repeat the exact error the phase was criticized for, on the phase's own
code.

So it runs and reports, and changes nothing. After a measurement window
the operator compares `router_threshold` against `task_class.class` on
real traffic and decides with data instead of by argument.

### Three things this deliberately does not do

1. **It is not a second opinion competing with the router.** The router
   remains the single decision-maker. Adding a competing opinion would
   be the failure mode this whole phase documents.
2. **`agrees_with_router` is `null`, not `false`, when DECIDE was not
   length-driven.** If the router delegated on a coordination marker or
   on `vibe_case=C7`, a length-only router would have decided
   differently — but reporting `false` would present a comparison the
   router never performed as if it were a disagreement. `null` says
   "not comparable for this call", which is the truthful answer.
3. **The grade is `modeled`, never `measured`.** A deterministic
   heuristic over task text is a projection, not an observation. Same
   downward-only rule L11.2 applies to the drift judge's confidence.

### A bug the wiring test caught

`agrees_with_router` initially computed `len(task) > threshold` — that
is "would it delegate", not "do they agree". For the short-task case
that inverted the meaning and reported disagreement where there was
agreement.

`TestL8_1a_AgreesIsComputedWhenLengthDriven` caught it. The correction
compares the router's hypothetical decision against the actual one.

### The load-bearing test

`TestL8_1a_DecisionIsUnchanged` asserts that for five inputs — short,
long, coordination marker, C7, C8 — the instrumented pipeline returns
exactly what `delegation.DecideDelegation` returns.

An instrumentation commit that accidentally moved a routing decision
would be worse than no instrumentation: it would silently change who
pays for what. That test is the guarantee it did not.

### Duplication, bounded and marked

`hasCoordinationMarker` is a third copy of the router's keyword list
(the router has two internally). Importing the router's unexported
matcher would couple instrumentation to DECIDE's internals; exporting it
is a larger refactor than an instrumentation commit should carry. Marked
in-code as temporary, to be deleted when the measurement window closes.

### Rejected, with reasons

- **`ComputeVerdict`** — needs a `CostSummary`, and `CostSummary` has
  zero producers outside `internal/vibeflow`. Wiring it means first
  inventing a cost subsystem. Inventing the input to justify the output
  is the failure this phase exists to prevent.
- **`ComputeConvergence` at `delegate_intent`** — needs a set of
  findings from multiple delegates. `delegate_intent` spawns one
  subagent; no collector exists. Same objection.

### Audit

`go vet` clean, `gofmt` clean on all 3 touched files, 6/6 new tests pass,
and the pre-existing `v4alpha/delegation` + `v4alpha/vibe` suites pass
unchanged — which is the evidence that no behavior moved.

## [0.4.2] — 2026-10-08 — correct four false claims in the mod's own manifest

Documentation-only. No Go changed. The point of this entry is that the
drift finding is now the *fourth* time this pattern has appeared in two
phases, so it is recorded as a pattern and not as a one-off.

### Fixed

1. **`summary.gates: 25` → `23`, plus `gatesReserved: ["G24","G25"]`.**
   23 concrete gates exist (G01-G23, one per event E01-E23). G24/G25 are
   *reserved slots* for ad-hoc operator-defined triggers — `loop-7-vibe-flow.md`
   §4.1 says so and has said so; the heading above it, `SKILL.md`, and the
   manifest summary all said "25 gates". A single integer that conflates
   implemented gates with reserved slots is a claim that cannot be checked
   against the artifact, which is the whole problem with it.
2. **`entryPoints.readme: "README.md"` — removed.** No such file exists.
   Removed rather than created: `SKILL.md` is the mod's declared primary
   entrypoint, and adding a human-facing README would create a second copy
   of the same facts — which is precisely the mechanism that produced
   items 1, 3 and 4 below.
3. **`entryPoints.templates`: `spec-c{1..7}` → `spec-c{1..8}`.**
   Eight template files exist; `spec-c8.json` shipped with the C8
   taxonomy work and the manifest was never told.
4. **`entryPoints.tests`**: added `e2e-c8.sh` and `gate-coverage-check.sh`.
   Both exist; neither was listed, so the manifest understated its own
   test surface.

### Not changed, deliberately

The `[0.2.0]` Loop 7 entry still says "25 named gates G1-G25". It was
written before this correction and it describes what was believed at the
time. A CHANGELOG is an append-only record; rewriting a shipped entry to
match today's understanding falsifies it. The `[0.3.0]` entry already set
the precedent of recording a documentation gap as its own entry rather
than quietly editing history.

### The pattern

Three of the four are the same defect: a fact stated in more than one
place, edited in one and not the other. Commit `fbb6f48` deleted four
duplicated `C1..C7` allow-lists from the Go side for exactly this reason.

**A claim that lives in two places will eventually be true in one of
them.** The only durable fix is to delete the duplicate, not to keep
re-syncing both.

### Audit

`tests/gate-coverage-check.sh` passes: 39 tool references resolve against
the live registry. Both JSON files re-validated, LF-only, 0 CR bytes.

## [0.4.1] — 2026-10-08 — Phase 21 OPENS: the first Phase 20 primitive actually wired

Phase 20's closeout finding was that all nine primitives had **zero
production callers**. This release wires exactly ONE of them — and
rejects the originally proposed one.

### Added
- **L11.2 — the production path now labels its own confidence.** `PublishResult` gains `confidence_grade` (`unverified | estimated | modeled | measured`) and `confidence_caveat`. Every `vibe_publish` response used to ship `{"verdict":"needs_human","confidence":0.9}` as a bare number; that is the precise failure Loop 11 L11.1 was written to fix, sitting in the one output the operator reads on every publish.
- **`internal/vibeflow/judgegrade.go`** — `GradeJudgeConfidence`, `JudgeConfidenceCaveat`, `JudgeConfidenceClaim`. Reuses the L11.1 `Claim`/`EvidenceGrade` types; adds no new epistemic vocabulary, no new grade, no new struct family.
- **`internal/orchestration/publish_vibe_l11_2_wiring_test.go`** — wiring tests. These exist because Phase 20's lesson is that a green library suite proves the rules are correct and says nothing about whether the production path calls them. Every test drives the real `PublishVibe` entry point and asserts on what the harness actually receives.
- **`internal/vibeflow/judgegrade_test.go`** — 9 ladder tests.

### Changed
- `internal/orchestration/publish_vibe.go`: 3 graded construction paths + 1 re-grade after the async `pending` flip. All grading funnels through a single method `(*PublishResult).applyConfidenceGrade()` so there is exactly one derivation that could disagree with itself.

### The rules, as shipped
| verdict | grade | why |
|---|---|---|
| `skipped` | `unverified` | confidence 0 means **no LLM ran** — not a measured zero |
| `pending` | `unverified` | check still running; nothing observed |
| `needs_human` (infra failure, conf 0) | `unverified` | the judge never ran |
| any real judge output | `measured` | it is a real recorded observation |
| *all cases* | — | measured is still qualified: one authority judging is **correlated, not independent review** (L12.1) |

Never `estimated` or `modeled`: this number is either an observation or absent. Downward-only — once unverified, a larger confidence never strengthens it.

### Rejected: ComputeVerdict
It was the proposed wiring and it was the wrong one. `ComputeVerdict` needs a `CostSummary`, and `CostSummary` has **zero producers** outside `internal/vibeflow/` (verified by grep). Wiring it would have required inventing a cost-recording subsystem first. Inventing the input to justify shipping the output is the exact failure this phase exists to prevent. The confidence was better: a real observation that only needed a label.

### Two bugs the wiring tests caught
1. **Async re-grade.** The constructor grades the pessimistic `needs_human` default, then the async branch flips the verdict to `pending`. Without a re-grade, `verdict=pending` shipped carrying the caveat "no judge provider recorded" — a caveat about the wrong state. Structurally valid, semantically false: the L11.1 failure class. Caught by reading the rendered output, not by structural assertions.
2. **"unknown" read as "no provider".** The authority caveat rendered "single-judge measurement from unknown", which contradicts "a judge ran" in the same sentence. Now it says the call path does not return the provider id — honest instead of ambiguous.

### Known limitations — still true
- **`runJudgePipeline` does not plumb `provider_id`/`model_rev`**, so the authority caveat cannot name the judge. `applyConfidenceGrade` documents this: naming a provider we did not observe would be a guess.
- **8 of the 9 Phase 20 primitives remain unwired.** This release is the first, not the fix.
- gofmt debt on 14 pre-existing `internal/vibeflow` files (694 lines) unchanged; the 4 files touched here are gofmt-clean.
- `bin/dark-mem-mcp.exe` is still stale — **this wiring is not in the running binary until it is rebuilt.**

### Audit
`internal/vibeflow`: 360/360 PASS (was 351). `internal/orchestration`: PASS. `go vet` clean on both, race detector clean on both, gofmt clean on all 4 touched files.
drift_judge: eval 2032 → ALIGNED. 23 run, 20 ALIGNED, 18 first-try, 8 consecutive ALIGNED.

## [0.4.0] — 2026-10-08 — Phase 20 CLOSED (Loops 8-13, the honesty layer)

Six loops shipped in one phase, unified by a single idea: *a system that
reports on itself must be able to say how much its own report is worth.*
Full balance and honest limitations in `core/phase-20-closeout.md`.

### Added
- **Loop 8 — <5min mitigation** (operator-validated, row 2534): `Classify` (deterministic task-length predictor, zero LLM calls, per-vibe_case calibration), `Context`, `Mode`, `ComputeDepth`. Horvitz 1990 anytime algorithms.
- **Loop 9 — model tier + cost**: `SelectModelTier`, `ComputeDriftPolicy`, `CostSummary`. Placeholder pricing (`IsEstimate=true`) is never reported as fact.
- **Loop 10 — token economy**: `ComputeEconomyDelta`, `BuildEconomyReport`. Modeled 45.4% avg token saving / 60.1% avg USD saving across the 10-persona mix; unclamped percentages so overspend stays visible.
- **Loop 11 — operator visibility**: `EvidenceGrade`, `Claim`, `ComputeVerdict`. Kadavath et al. 2022 (arXiv:2207.05221) calibration; the verdict's grade is the weakest of its inputs; confidence propagates DOWNWARD only; zero recorded calls yields `insufficient-data`, never "helping".
- **Loop 12 — G19 epistemic arbitration**: `ClassifyIndependence`, `Arbitrate`, `ArbitrateByOperator`. Panickssery/Bowman/Feng 2024 (arXiv:2404.13076). Fixes the real G19 defect: it re-ran `judge(grounding_check)` on the SAME provider/model and presented repetition as verification. Rule: a verdict is only disputed by INDEPENDENT evidence; correlated re-judging is idempotent.
- **Loop 13 — sub-agent handoff contract**: `Origin`, `HandoffSpec`, `ComputeConvergence`. Row 251's 5-stage delegation thesis (CURATE/MIND/ISOLATE/RECOVER/SYNTHESIZE). Headcount is not evidence — convergence grade derives from diversity, never from how many delegates agreed.
- **`core/phase-20-closeout.md`** (NEW): the phase balance, including the load-bearing limitation that Phase 20 has **zero production callers** and the canonical binary predates the work.

### Changed
- **`mod.json`**: version 0.3.0 → 0.4.0, `protocol.loops` 8 → 13, description rewritten, `audit.phase20` closeout record added.
- **`SKILL.md`**: 7-loop → 13-loop protocol, `loops-shipped` 7 → 13, metadata version 0.2.0 → 0.4.0, Phase 20 loops added to the dependency graph. **This file had not been updated for six shipped loops — a violation of the operator's hard rule on synchronizing documentation (row 1330), now corrected.**
- **`internal/vibeflow/arbitration.go`**: `JudgeSource` gained the `Origin` third axis (operator decision: extend rather than run a parallel notion of independence). L12.1's two-axis behavior is preserved and pinned by `TestL13_BackCompat`.
- **`mod.json` `audit`**: `driftJudgesRun` 21 → 22, `driftVerdicts.aligned` 18 → 19, `firstTryAligned` 16 → 17, artifacts 20 → 21.

### Known limitations at closeout
- **NOT WIRED** — zero production callers for any Loops 8-13 symbol; `bin/dark-mem-mcp.exe` (Oct 7) predates all Phase 20 code. The safety machinery is verified; the optimization is not switched on.
- **No operator has felt any of this.** The §Loop-10 figures are modeled projections, not observations of a running system.
- **gofmt debt**: 14 pre-existing `internal/vibeflow` files (694 diff lines, Go 1.19 comment reformatting from Loops 8-10). Deliberately excluded from every loop commit. Now the oldest open item in the package.
- `StateDelegating` absent from `internal/vlp/`; 4 Postgres subagent methods return `notImpl`; Strong/MiniMax pricing is still a placeholder.

### Audit
351/351 tests PASS, `go vet` clean, Phase 20 files gofmt-clean, race detector clean.
drift_judges: 22 run, 19 ALIGNED, 1 needs_human, 17 first-try, 6 consecutive ALIGNED (evals 2025→2030).
Phase 20 closeout verdict: **eval 2031**.

## [0.3.0] — 2026-10-08 — Loops 8-12 code (CHANGELOG entry written retroactively)

Version 0.3.0 was bumped in `mod.json` at the start of Phase 20 but its
CHANGELOG entry was never written, leaving this file's newest entry at
0.2.0 for six shipped loops. Recorded here for honesty rather than
silently folded into 0.4.0.

### Added
- Nine `internal/vibeflow` artifacts across Loops 8-12 (classifier, context, mode, depth, model tier, drift policy, cost transparency, token economy, operator visibility, epistemic arbitration). Each shipped as its own drift-judged commit; see `mod.json` `core.artifacts` for the full list with verdicts.

### Changed
- `mod.json`: `audit.loopsShipped` 7 → 12, `audit.driftJudgesRun` 9 → 21, artifacts 7 → 20.

### Known limitations
- The version 0.2.0 → 0.3.0 documentation gap described above.

## [0.2.0] — 2026-10-08 — Loop 7 vibe-flow SHIPPED

### Added
- **Loop 7 (vibe-flow mode)**: turns the mod from descriptive (6 loops shipped) to ambient-directive (Loop 7 = C8 vibe_case). 23 workflow events E1-E23 fire across a normal session; 25 named gates G1-G25 intercept each event with 1-4 dark-memory tool calls; agent stays in flow via inline notes (NOT intrusive blocking).
- **`mods/vibe-loop-git/core/loop-7-vibe-flow.md`** (NEW, ~493 LoC): concept doc with philosophy, events, gates, UX walkthrough, compatibility, honest failure modes.
- **`mods/vibe-loop-git/core/gate-trigger-matrix.json`** (NEW, ~300 LoC): 23-row E→G→T matrix with every tool reference verified against the live dark-memory-mcp v4.0.0-alpha.30 registry.
- **`mods/vibe-loop-git/core/gate-protocol.md`** (NEW, ~260 LoC): the system-prompt block (~3,800 tokens) injected by the harness at session_start. Compatible with opencode / claude-code / claude-desktop / codex adapters.
- **`mods/vibe-loop-git/templates/spec-c8.json`** (NEW): the C8 vibe_case template (mirrors spec-c1..spec-c7 structure).
- **`mods/vibe-loop-git/tests/e2e-c8.sh`** (NEW): e2e smoke for the 23-event matrix + spec-c8 template.
- **`mods/vibe-loop-git/tests/gate-coverage-check.sh`** (NEW): verifies every tool name in the matrix actually exists in the dark-memory registry (catches stale tool references).
- **`vibe_case C8 — `vibe-flow`** added to `internal/vibecase/taxonomy.go` (canonical SoT). The package's `validVibeCases` map and JSON enums are derived from `vibecase.All()` at init — no more hardcoded lists in 4-5 places that fall out of sync.

### Changed
- **`mod.json`**: version 0.1.0 → 0.2.0, `targetDarkMemoryVersion`: `>=4.0.0-alpha.28` → `>=4.0.0-alpha.30`, `audit.loopsShipped`: 6 → 7, `audit.driftJudgesRun`: 7 → 9, `protocol.loops`: 6 → 7, `vibeCases.C8` added.
- **`internal/vibecase/taxonomy.go`**: `CaseVibeFlow Case = "C8"` added to constants, `all` slice, `descriptions` map.
- **`internal/v4alpha/vibe/spec.go`**: removed `CaseC1..CaseC8` local duplicates (was breaking the "single source of truth" rule per `taxonomy.go:32-36`). Now delegates to `vibecase.IsValid()`.
- **`internal/v4alpha/transport/mcp/mindset.go`**: `validVibeCases` map now derived from `vibecase.All()` at init. Error message includes live allow-list.
- **`internal/v4alpha/transport/mcp/vibe.go`**: jsonschema description updated, error message uses live `vibecase.JSONSchemaEnum()`.
- **`internal/v4alpha/transport/mcp/judge.go`**: 2 jsonschema enums (judge + consensus) updated to include C8.
- **`internal/v4alpha/transport/mcp/wire.go`**: `errInvalidVibeCase` uses live `vibecase.JSONSchemaEnum()`.
- **`internal/v4alpha/delegation/extract.go`**: `validVibeCaseForExtract` delegates to `vibecase.IsValid()`; error message uses live allow-list.
- **Tests**: `TestParse_RejectsUnknown`, `TestIsValid`, `TestCardinality` (vibecase) + `TestProperty_Spec_UnknownVibeCaseAlwaysRejected` (v4alpha/vibe) updated to reflect new contract (C8 valid, C9+ rejected).
- **README.md + SKILL.md**: Loop 7 section to be added in Commit 3.

### Drift_judge results
| Artifact | Eval ID | Verdict | Notes |
|---|---|---|---|
| `core/loop-7-vibe-flow.md` | 2015 | **ALIGNED** conf=1.0 | MiniMax-M3 chat-minimax-cn (post-T-201) |
| `core/gate-trigger-matrix.json` | 2016 | operator-override (nli=contradiction) | MiniMax-M3 false-positive pattern (rows 482/1985/2013); all tool references manually verified |
| `core/gate-protocol.md` | (audit via 2016) | operator-override | Same pattern as matrix |
| `templates/spec-c8.json` | pending | C8 template validates | manual check: matches spec-c1 schema family |

### Cross-version lockstep hash pin
UNCHANGED: `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb` (per Phase 13 plan + mod invariants.sealed row 4).

### Local-only discipline
NO push, NO remote tags (per huérfano rule + mod invariants.sealed row 3). All commits local on branch `feat/v4-redesign`.

### Git tag
`v4.0.0-alpha.30-vibe-loop-git-v0.2.0` created locally on 2026-10-08 (no push).

### Atomic mirror
Dark-memory row 2533 (pinned, kind=decision, memory_type=semantic) — "Loop 7 SHIPPED — vibe-loop-git v0.2.0 (vibe-flow mode, C8) — 7 loops, 9 drift verdicts, 4 incremental commits".

### Drift-judge eval IDs
- 2015: core/loop-7-vibe-flow.md → ALIGNED conf=1.0 (provider=chat-minimax-cn, model=MiniMax-M3, latency 7,160ms)
- 2016: core/gate-trigger-matrix.json → operator override (MiniMax-M3 nli=contradiction false-positive pattern)
- 2017: templates/spec-c8.json → operator override (MiniMax-M3 nli=neutral false-positive pattern)

---

## [0.1.0] — 2026-10-06 — initial packaging

(prior content for retro doc)