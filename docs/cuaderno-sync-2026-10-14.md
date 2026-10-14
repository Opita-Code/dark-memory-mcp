# Cuaderno re-sync — 2026-10-14

This document is the on-disk mirror of the cuaderno (project_id=`dark-memory`) re-sync
performed on 2026-10-14 by operator `nico`. It exists because the cuaderno had drifted
10 days behind the repo: the last pinned decision was 2026-10-04 (Phase 12 WIP), but
the repo `feat/v4-redesign` carried 92 commits between `v4.0.0-alpha.22` and HEAD
`3c02b9c` that the cuaderno had not captured.

The re-sync closes that gap with **10 new agent_memory rows** (4 SUMMARY pinned +
6 SECTION pinned=false) and one orphan commit (this file plus `internal/redact/redact.go`).

## What shipped between 2026-10-04 and 2026-10-13

### Phase 12 closure (10 commits, all 2026-10-04)

Local tag `v4.0.0-alpha.23` on commit `1f03bef` (HEAD of v4.0.0-alpha.23).
The cuaderno already had T-101 (id 2416) and T-102 (id 2417). This re-sync adds:

| Chunk | Commit | Local tag | Cuaderno id | Pinned |
|---|---|---|---|---|
| T-103a | `3245e1a` | `v4.0.0-alpha.23-pre-3` | **2688** | false |
| T-103b | `e52c2ce` | `v4.0.0-alpha.23-pre-4` | **2691** | false |
| T-103c | `f0f23d9` | `v4.0.0-alpha.23-pre-5` | **2695** | false |
| T-104  | `fc7a45f` | `v4.0.0-alpha.23-pre-6` | **2692** | false |
| T-103a-extension   | `1d925ce` | `v4.0.0-alpha.23-pre-7` | (folded into T-103a id 2688) | false |
| T-105  | `f655350` | `v4.0.0-alpha.23-pre-8` | **2693** | false |
| T-103a-extension-2 | `d5bfaea` | `v4.0.0-alpha.23-pre-9` | (folded into T-103a id 2688) | false |
| T-106  | `1f03bef` | HEAD of alpha.23 | **2694** | false |

Phase 12 SUMMARY: **pinned=true**, auto-created via `vibe_publish` artifact 1653 with
drift verdict `aligned@0.95 confidence (model_used=MiniMax-M3)`. The drift judge verified
all 10 SHAs, all 10 tags, all aggregate metrics (71 canonical tools, 19 namespaces,
schema v32, ~3.5k LoC, 42 tests), and the LUCIDEZ honest disclosure for the T-103 split.

### Phase 13 + hygiene (commits 2026-10-05..2026-10-09)

SUMMARY id **2686** (pinned=true). Key fixes:

- **G4 gate fix** (`b94b5a3`): 4 canonical tools were registered but never granted
  (LLM_BIND + EVENTS namespaces missing from `internal/recall.DefaultToolGrants`).
  Silent failure mode. Caught by the 2026-10-08 release audit when `llm_provider_probe`
  returned `ErrCapabilityNotGranted`.
- **NLI store redaction** (`2a2c36d`, "option C"): `Store.GetProject` redacts on
  generic read paths; `GetProjectRaw` is the sealed accessor for the 4 call sites
  that must actually authenticate (orchestration.EnsureNLIRouter, ensureLLMJudge,
  tools.llm_provider_bind partial merge, tools.llm_provider_probe sends bearer).
- **C8 vibecase** (`fbb6f48`): 4 duplicated C1..C7 allow-lists deleted; C8 in the
  canonical taxonomy. Closes the row that rejected every Phase 20/21 `vibe_publish`
  shipping `vibe_case "C8"` (23 drift judges producing evals 2011-2032).
- **NLI T-201 think-only** (`a5db4ec`): strip the `<think>...</think>` block first;
  if anything visible remains, parse that as the label. Resolves Phase 20 documented
  false positives (rows 482/1985/2013/2016/2017) that operators overrode twice.
- **L8.1a delegation ADVISORY** (`3da7c08` + `efa6e59`): instrument as ADVISORY,
  measure, do not flip.
- **Docs reconcile** (`4d0162f`): measured reality + test guard.

### Phase 20 vibe-loop-git CLOSED + Loop 12/13

Phase 20 SUMMARY id **2689** (pinned=true). Loop 12/13 SUMMARY id **2690** (pinned=true).

HEADLINE: **Phase 20 has ZERO production callers.** Verified 2026-10-08 by grepping
every Go file outside `internal/vibeflow` for the 14 Phase 20 symbols (Classify,
ComputeDepth, SelectModelTier, ComputeDriftPolicy, ComputeEconomyDelta,
BuildEconomyReport, BuildOperatorStatus, GradeCost, GradeEconomy, ComputeVerdict,
Arbitrate, ComputeConvergence, ArbitrateByOperator, ClassifyIndependence): zero hits.

Phase 20 is a complete, tested, drift-judged LIBRARY and an optimization that is
NOT SWITCHED ON. `internal/vibeflow` has ten real importers (internal/context,
internal/orchestration/publish_vibe.go, internal/store/sqlite, internal/store/postgres)
but every one uses only pre-existing types.

Loop 13 L13.1 (commit `5031d6e`, alpha.30-vibe-loop-git-v0.3.0-pre11): the contract
for the 4 epistemic failures at the delegation boundary:
(A) calibration does not transfer (GradeUnverified must not become GradeMeasured);
(B) self-preference propagates (per arXiv:2404.13076);
(C) finding provenance;
(D) memory isolation. PRESENCE IN MEMORY IS NOT EVIDENCE.

Mod registry: L13.1 registered (commit `9b25c63`, driftJudgesRun=22, 21 artifacts,
loopsShipped=13, 6th consecutive ALIGNED).

Phase 21 L11.2 (commit `cac9ecd`) wired the first Phase 20 primitive into production.

### Release reconciliation

SUMMARY id **2687** (pinned=true). Decisions per operator 2026-10-08:
license MIT (was "proprietary"); LOCAL ONLY retired (was factually false — repo
was already public); docs updated to current reality.

Key commits:

- `6b7a180` retire LOCAL ONLY, license MIT, fix G4
- `16215c7` reconcile identity to alpha.31 + ES/EN audience section (**the commit
  the live binario is based on**, per `health_ping`: commit=16215c7, tag=4.0.0-alpha.31)
- `a5d3d7c` name the released tag so the status guard stops failing
- `e9590f7` close published-claims gap, document the boundary rule
- `20ac823` move the reason to care above the fold
- `3c02b9c` (HEAD) **the LUCIDEZ honest disclosure definitivo**:
  - Neither `v4alpha/audit.CreateSchema` nor any v4alpha writer is called by the
    shipped boot path. Its table `audit_log` does not exist in any database on
    this machine. Since `CreateSchema` issues `CREATE TABLE IF NOT EXISTS`, the
    absence of the table is proof the writer never ran.
  - **4,199 LoC of tested SHA-256 row chaining, Ed25519 signatures (ADR-017)
    and payload columnarisation (ADR-019) protect nothing here** — while §1.2 of
    the status document reported them as delivered features.
  - Reachability generalises: a package can be compiled, tested and documented
    as shipped while never being called. **ARGUMENT FOR MEASURING THE ARTIFACT
    RATHER THAN THE SOURCE** when the question is "what does this actually do".

## Adopted orphan: `internal/redact/redact.go`

`internal/redact/redact.go` (7827 bytes, written 2026-10-09) is an orphan module
that implements INV-13 redact-before-log. It existed in the working tree but had
never been committed in any branch. The package's own header documents the gap:

> WHY THIS EXISTS. INV-13 (redact-before-log) is recorded in docs/v4-status.md §3
> as NOT STARTED, and measurement confirmed it: as of 2026-10-09 the repository
> contained no secret detector at all, only six unrelated ad-hoc redactors ...
> Each knows about one type. None is a policy.
>
> The motivation is concrete rather than theoretical. During the work that
> produced this package, an npm access token was pasted into an operator
> conversation. The discipline that says "rotate it and never store it"
> existed in a skill file, which is advice, not a control.

Adopting it preserves an audit trail (we found it during cuaderno-sync) and
prevents the working tree from being silently mutated by a subsequent `go fmt`.

## NLI wiring fix (session-bound)

During this session, `dark_memory_project_create` was called against the existing
`dark-memory` project with a full `nli_config` (UPSERT, returned
`idempotent_replay: true`). The legacy Content path of `vibe_publish` now returns
`confidence_grade=measured, model_used=MiniMax-M3`. The artifact-anchored path
still reports "no project NLI override registered" until a true project-level
NLI binding is recorded by a future write that is not idempotent-replayed — known
limitation, tracked in cuaderno id 2686 (Phase 13 + hygiene SUMMARY).

## Cross-version hash pin

`4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb` — unchanged.
None of the 92 commits between `v4.0.0-alpha.22` and HEAD `3c02b9c` included
cross-version comparison, so the pin stays.

## North star position

alpha.22 = ~85% (maintained). alpha.23 = ~88-94% per Phase 12 predictions
(events subsystem shipped, tools +19, personas +2). North star does not jump
until primitives are switched on (Phase 21 L11.2 onward). Commit 3c02b9c's
audit_verify disclosure is a *negative* movement on the % axis: 4,199 LoC of
tested audit code was reported as delivered and is not. The honest % is below
the previously reported number.

## Files in this commit

| File | Action | Why |
|---|---|---|
| `docs/cuaderno-sync-2026-10-14.md` | NEW | This document. On-disk mirror of the cuaderno re-sync so future git archaeology can recover the mapping. |
| `internal/redact/redact.go` | adopt (untracked → tracked) | Orphan module for INV-13 redact-before-log. Written 2026-10-09, never committed in any branch. |
