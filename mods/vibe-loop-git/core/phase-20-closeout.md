# Phase 20 Closeout — the honesty work

**Status**: CLOSED 2026-10-08 (operator green light)
**Loops shipped**: 8, 9, 10, 11, 12, 13 — six loops, nine artifacts
**Operator trigger**: row 2534 — *"He probado herramientas experimentales parecidas al vibe-flow, y para tareas de menos de 5 minutos el harness se vuelve pesado y tedioso."*
**Cross-version lockstep hash pin**: UNCHANGED

---

## 1. Read this first: Phase 20 is a complete LAYER, not complete WIRING

**Zero production callers.** Verified 2026-10-08 by grepping every Go
file outside the package for Phase 20 symbols:

```
grep -rE "vibeflow\.(Classify|ComputeDepth|SelectModelTier|
  ComputeDriftPolicy|ComputeEconomyDelta|BuildEconomyReport|
  BuildOperatorStatus|Arbitrate|ComputeConvergence|...)" \
  --include=*.go . | grep -v "^./internal/vibeflow/"
  -> (empty)
```

`internal/vibeflow` has ten real importers — `internal/context`,
`internal/orchestration/publish_vibe.go`, `internal/store/sqlite`, and
others — but every one of them uses only the pre-existing types
(`Artifact`, `Spec`, `DriftReport`, `BrandGuide`, `ComplianceRule`,
`ArtifactListFilters`, `SpecListFilters`, `ArtifactUpdate`). Not one
calls a single Phase 20 primitive.

And the canonical binary is older than the work:

```
bin/dark-mem-mcp.exe        Oct  7 23:10
internal/vibeflow/handoff.go Oct  8 10:56
```

So: nine primitives, 351 tests, 22 drift_judges — and **nothing in the
running MCP invokes them**. No `session_start` classifies task length. No
`vibe_publish` picks a model tier. No `delegate_intent` passes a graded
handoff. Phase 20 is a library with a safety record, not a shipped
feature.

This is stated here because "Phase 20 complete" is exactly the kind of
phrase that gets read six months later as "the optimization is live".
It is not.

---

## 2. What actually shipped

| Loop | Primitive(s) | The rule it enforces |
|---|---|---|
| 8 | `Classify`, `ComputeDepth`, `Context`, `Mode` | Tasks under 5 min should not pay harness overhead (Horvitz 1990 anytime algorithms) |
| 9 | `SelectModelTier`, `ComputeDriftPolicy`, `CostSummary` | Pick the cheapest tier that fits; never skip scrutiny silently; never report placeholder prices as fact |
| 10 | `ComputeEconomyDelta`, `BuildEconomyReport` | Measure real savings, with unclamped percentages so overspend stays visible |
| 11 | `EvidenceGrade`, `Claim`, `ComputeVerdict` | Every number carries its evidence grade; confidence propagates DOWNWARD only |
| 12 | `ClassifyIndependence`, `Arbitrate` | A verdict is only disputed by INDEPENDENT evidence; correlated re-judging is idempotent |
| 13 | `Origin`, `HandoffSpec`, `ComputeConvergence` | An epistemic rule that does not survive the delegation boundary was only ever true for one agent |

The through-line is a single idea applied six times: **a system that
reports on itself must be able to say how much its own report is worth.**

Three mechanical rules do most of the work, and each is pinned by test
rather than by convention:

- **L11.1** — the verdict's grade is the weakest of its inputs. Zero
  recorded calls yields `insufficient-data`, never "helping". A system
  that has never run cannot claim it saved anything.
- **L12.1** — re-running the same judge is a null experiment. Three
  rungs: `none` always abstains, `correlated` may uphold with the grade
  held exactly flat, `independent` is the only rung that may overturn.
- **L13.1** — headcount is not evidence. Convergence grade is a function
  of diversity, never of how many delegates agreed. Five sub-agents on
  one model are one observation sampled five times.

---

## 3. Measured results

**Economy** (`BuildEconomyReport`, 10-persona mix):

| Metric | Result |
|---|---|
| Avg token saving | 45.4% |
| Avg USD saving | 60.1% |
| Total tokens saved | 20,884 |
| Total USD saved | $0.024485 |
| Total latency saved | 7,070 ms |

These are **modeled** projections against a documented naive baseline
(DepthStandard + Balanced + Single), and the readout labels them
`[modeled]` because that is what they are. They are not observations of
a running system, for the reason in §1.

**Audit record**:

| Metric | Value |
|---|---|
| Tests in `internal/vibeflow` | 351/351 PASS |
| `go vet` | clean |
| `gofmt` (Phase 20 files) | clean |
| Race detector | clean |
| drift_judges run | 22 |
| ALIGNED | 19 |
| needs_human | 1 |
| First-try ALIGNED | 17 |
| Required a retry | 2 |
| **Consecutive ALIGNED streak at close** | **6** (2025→2030) |

The 2 retries and 1 needs_human are not noise to be explained away. They
are the MiniMax-M3 false-positive pattern (rows 482, 1985, 2013, 2016,
2017) — `nli=neutral` and `nli=contradiction` verdicts presented as
fact, twice overruled by the operator (evals 2016, 2017, row 2533).

**Loop 12 exists because of that specific failure**, and it is the only
Phase 20 loop that could not have been written without it.

---

## 4. Defects found while building — the part worth keeping

Ten defects across six loops. Every one was found by reading rendered
output or by a test asserting a semantic property, not by coverage.

**The two that mattered most:**

1. **L11.1 self-contradicting report.** The modeled-claim caveat was
   hardcoded to "no calls recorded in this session", so a session with
   one recorded call printed
   `spend so far: $0.000197 over 1 call(s) [measured]` directly above
   `caveat: ... no calls recorded in this session`. Every structural
   test was green. Found by rendering the output and reading it.

2. **L13.1 inverted ladder.** Adding the `Origin` axis initially left
   L12.1's "same provider AND same model → `None`" check running first,
   so a *delegated* artifact came out strictly LESS independent than a
   self-judged one — the exact inverse of the loop's purpose. Caught by
   a test asserting that a sub-agent artifact is strictly more
   independent than a self-produced one.

Also: **L13.1's laundering bug** (`ComputeConvergence` treated distinct
finding IDs as agreement and *promoted* a panel whose members had
reported opposite things), **L12.1's struct-equality bug** (adding
`Origin` semantics while `ClassifyIndependence` compared whole structs
meant the headline rule never fired), and `Finding.ID` carrying two
incompatible meanings at once.

The recurring lesson, recorded because it generalizes: **for
human-audit-facing artifacts, render and read the output.** Tests that
assert only on structure do not detect semantic contradictions, and both
of the worst defects above were invisible to the suite that shipped them.

---

## 5. Honest limitations

1. **Not wired.** See §1. This is the load-bearing limitation.
2. **Zero operators have felt any of this.** No latency saving, no token
   saving, no cheaper model tier. The numbers in §3 are projections
   about a system that does not yet call these functions.
3. **The economy baseline is a model**, chosen as the naive path
   (DepthStandard + Balanced + Single). It is defensible and
   documented, but a baseline chosen to be beaten is not a neutral
   benchmark. If Phase 21 exists, the first honest move is to compare
   against a *real* pre-Phase-20 run, not against the model.
4. **`StateDelegating` does not exist** in `internal/vlp/`. The
   delegation router in `internal/delegation/router.go` is not
   integrated into the VLP state machine, so a delegation has no
   lifecycle state. Verified absent by grep on 2026-10-08.
5. **Postgres subagent methods return `notImpl`** (4 methods, per row
   199). C2 delegation works on SQLite only.
6. **Pricing for Strong/MiniMax is a placeholder** (`IsEstimate=true`).
   `GradeCost` correctly drops those claims to `estimated`, so the
   system does not overclaim — but the placeholder still has to be
   replaced before any dollar figure for those vendors means anything.
7. **Judge provider is pinned to one model.** Evals 2030 and 2029 were
   issued by `chat-minimax-cn/MiniMax-M3` — the very authority whose
   self-preference bias Loop 12 documents. Unavoidable given the pin,
   and recorded rather than glossed over. L12.1 exists partly because
   this deployment cannot verify itself out of the box.

## 6. Debt deliberately left unpaid

**gofmt**: 14 pre-existing files in `internal/vibeflow` (694 diff lines,
all Go 1.19 comment reformatting originating in Loops 8-10). Not
included in any loop commit — a 694-line cosmetic reformat would bury the
real change. Every file Phase 20 created or modified is gofmt-clean.
**This is now the oldest open item in the package.**

**SKILL.md and CHANGELOG.md drift**: through all six loops they still
described a 7-loop protocol with `loops-shipped: 7`, and CHANGELOG's
newest entry was 0.2.0. That violates the operator's hard rule on
synchronizing documentation (row 1330). Fixed in the closeout commit
that ships this file; it should not have survived six loops.

---

## 7. What Phase 21 should start with

Not a new loop. **Wiring, and then measurement.**

1. Pick the smallest real surface — most likely `vibe_publish`'s path —
   and make one Phase 20 primitive load-bearing there. A single wired
   primitive beats nine unwired ones.
2. Measure against a real pre-Phase-20 run, so §3 stops being a model
   and becomes an observation.
3. Replace the placeholder pricing.
4. Pay the gofmt debt.

Until step 1 is done, the correct description of this work is: *the
safety machinery exists and is verified; the optimization is not yet
switched on.*