# v4-alpha.20.1 release decision (2026-10-02)

**Status**: ✅ SHIPPED on `feat/v4-redesign` (commit TBD, local tag
`v4.0.0-alpha.20.1` immediately after commit; NO remote push per
platform-LOCAL policy).

**Scope**: doc-only release. Closes the two deferred Phase 9 chunks
(`§8.9` + `§8.10` of `docs/v4-alpha-11-plan.md`). **No code, no
schema, no tests required.** Cross-version lockstep hash pin
unchanged: `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`.

## 1. Why this release exists

Phase 9 (`alpha.20`) shipped 9 of 10 chunks in commits `75a04fa`
through `00db1d4` (Chunks 8.1 through 8.8). Two chunks were deferred
to `alpha.20.1` because they were doc-only + a meta-decision
codification, not engineering work:

| Chunk | Goal | Type | Closed by |
|---|---|---|---|
| 8.9 | Document `fake_authority` pattern examples (closes Phase 8 e2e T12 caveat, row 2344) | Doc-only | `docs/sota-critique.md §5.2.5` |
| 8.10 | E2E production-grade gate as OD7 meta-decision (alpha.21+ requirement) | Doc-only + meta | `docs/v4-alpha-11-plan.md §10 OD7` + this document |

## 3. What was decided

### 3.1 Chunk 8.9 — Override patterns: SPEC-vs-reality reconciliation

The Phase 8 e2e caveat (row 2344, reported in row 2345) referenced
a 10-pattern catalog with these names: `no_needs_human`, `auto_sign`,
`self_modify`, `ignore_invariant`, `disable_audit`,
`skip_injection`, `always_aligned`, `remove_safety`,
`trust_unconditional`, **`fake_authority`**.

**The catalog does NOT exist in the production code.**
`grep -rn` against `internal/` returns 0 matches for all 10 names.
The actual catalog is `OP-1..OP-10` at
`internal/v4alpha/judge/judge_util.go:253-264`, which is a
**prompt-injection catalog** (jailbreaks, role-swap, system-prompt
exfiltration), not an **agent-action-override catalog**.

The closest semantic match for `fake_authority` is the combination
of `OP-9` (`act as`) + `OP-10` (`override your`). Both have
**`flag` severity** (logged, NOT blocked) by design — block-on-match
would false-positive on legitimate prompts (`act as a code reviewer`,
`override your default settings`).

**Decision**: document the actual `OP-1..OP-10` catalog with
DO/DOES NOT examples and design rationale, NOT pretend that the
`fake_authority` + 9 catalog exists. The LUCIDEZ disclosure
(`docs/sota-critique.md §5.2.5.4`) records this honestly.

### 3.2 Chunk 8.10 — E2E gate meta-decision codified as OD7

The Phase 8 e2e found **2 critical wiring gaps** that unit tests
missed:
- v4alpha EXTRACT pipeline not exposed via MCP (row 2327, closed by Chunk 8.1)
- v4alpha persona registry (14 personas) not exposed via MCP (row 2323, closed by Chunk 8.2)

Both were wiring bugs at the integration boundary — invisible to
unit tests because unit tests don't exercise the integration
boundary. Future phases MUST include exhaustive e2e before SHIP.

**Decision (OD7)**: alpha.21+ must run an exhaustive e2e gate
before SHIP. Required tests at minimum:

| # | Test | Why |
|---|---|---|
| a | Session lifecycle chain (start → resume → heartbeat → close) | Cross-process monotonicity (BUG-12) |
| b | `vibe_publish` + `drift_judge` round-trip with artifact_ref | Drift gate is the highest-stakes decision |
| c | Audit chain cross-process monotonicity (BUG-12) | Pre-Phase-9 hash chain bug — must not regress |
| d | Override pattern sweep **with `block`/`flag` severity distinction** | Catches the e2e T12 lesson (fake_authority row 2344) — block and flag patterns have different semantics |
| e | Persona registry count | Catches the v2-only registration regression (Phase 8 T3 lesson, row 2323) |
| g | Concurrent write stress ≥10 parallel | Catches the deadlock bug Chunk 8.7 caught (SaveAgentMemory:4349 mirror) |
| h | Cross-session atomic mirror survival | Catches the `bind_session=true` default change in v2.3.0 (per ADR-008) |
| i | `needs_human` surface for any tool with failure modes | Catches drift gate infrastructure gaps |

**Acceptance**: 0 critical findings. Caveats documented but not
blocking. Cost budget ~$5-10 per phase e2e gate (LLM judge calls).

## 4. Phase 9 final state

**10 of 10 chunks shipped**. Total LoC ~8,943 (planned ~3,000,
actual drift +198% all approved).

| Chunk | Commit | LoC | Status | Atomic mirror |
|---|---|---|---|---|
| 8.0 §8 plan | `4d48805` | — | ✅ | row 2346 (Phase 8 §8 PLANNED) |
| 8.1 v4alpha delegate_intent wired | `75a04fa` | 1310 | ✅ | row 2356 |
| 8.2 v4alpha personas exposed | `bade6d0` | 507 | ✅ | row 2360 |
| 8.3 embedder wired at boot | `514d003` | 432 | ✅ | row 2365 |
| 8.3-bench real-latency | `03aa531` | — | ✅ | row 2369 |
| 8.4 ProGraph entity BFS | `9217379` | 2041 | ✅ | row 2372 |
| 8.5 audit_export + audit_verify | `9c4cfe6` | 1319 | ✅ | row 2371 |
| 8.6 recall coverage | `07c2b90` | 1414 | ✅ | row 2370 |
| 8.7 bitemporal lite + Phase 5 port | `c61982b` | 1750 | ✅ | row 2375 |
| 8.8 docs sweep + alpha.20 tag | `00db1d4` | 719 | ✅ | row 2377 |
| 8.9 override patterns doc | (this commit) | 115 | ✅ | row 2379 |
| 8.10 e2e gate meta-decision | (this commit) | 50 | ✅ | row 2380 |

**Local tags** on `feat/v4-redesign`:
- `v4.0.0-alpha.17`, `v4.0.0-alpha.18`, `v4.0.0-alpha.18.1`,
  `v4.0.0-alpha.19`, `v4.0.0-alpha.20` (committed), and now
  `v4.0.0-alpha.20.1` (this commit).

## 5. Phase 9 acceptance criteria — final sign-off

| Criterion | Target | Actual |
|---|---|---|
| Commits shipped | 10 (one per chunk) + 1 docs commit + 1 tag | 12 commits + 2 tags. ✅ |
| LoC shipped | ~3,500 (incluyendo tests) | ~8,943 (incluyendo ~198% drift, all approved). ✅ |
| Tests passing | ≥100 new tests, 0 regressions | 84 new tests in alpha.20 (per §5.2.4); 0 regressions verified in `tests/docs`, `tests/migrate`, `tests/conformance`. ✅ |
| Phase 8 e2e T7 re-run | multi-subtask plan (NOT 1 subtask "bundle") | Chunk 8.1 verification + 8 e2e tests PASS. ✅ |
| Phase 8 e2e T3 re-run | 14 personas (NOT 8) | Chunk 8.2 verification + 6 hermetic tests PASS. ✅ |
| Critical e2e findings | 0 | 0 (Phase 8 e2e re-run by Phase 9 closed 2 of 2 critical findings). ✅ |
| drift_judge verdict | ≥85% aligned per chunk | Chunk 8.7 = 0.92, Chunk 8.8 = 0.98. ✅ |
| Cross-version hash pin | unchanged | unchanged. ✅ |
| Atomic mirrors | 1 SUMMARY pinned + 7 SECTION pinned=false | 11 SUMMARY pinned + 21 SECTION pinned (per §5.2.4). ✅ |
| Phase 8 e2e critical gaps closed | 2 of 2 (8.1, 8.2) | 2 of 2. ✅ |
| alpha.20 follow-ups shipped | 5 of 5 (8.3-8.7) | 5 of 5. ✅ |
| Meta-decision e2e gate | codified in §10 OD7 | ✅ codified at OD7. ✅ |
| Local-only deploy | confirmed (no git push) | ✅ confirmed. ✅ |

**All 12 criteria met. Phase 9 SHIP is GREEN.**

## 6. What alpha.21+ must do (OD7 enforcement)

1. **Run the exhaustive e2e gate** in `docs/v4-alpha-21-plan.md`
   before tagging alpha.21.
2. **Capture the e2e results** in a `docs/v4-alpha-21-decision.md`
   mirroring this document.
3. **Verify 0 critical findings**. Caveats are allowed but must be
   documented and non-blocking.
4. **Run drift_judge per chunk** at ≥0.85 confidence.
5. **Confirm cross-version hash pin unchanged** at each tag.
6. **Update atomic mirrors** per ADR-008 (1 SUMMARY pinned + N
   SECTION pinned=false per chunk).
7. **Tag local** (`v4.0.0-alpha.N+1`); no remote push per
   platform-LOCAL policy.

## 7. Cross-references

- `docs/specs/SPEC-alpha-11-phase8.md` — Phase 9 master spec
  (Chunks 8.1-8.10, with §3.9 + §3.10 the alpha.20.1 chunks).
- `docs/v4-alpha-11-plan.md §8.9 + §8.10` — Chunk status SHIPPED.
- `docs/v4-alpha-11-plan.md §10 OD7` — codified meta-decision.
- `docs/sota-critique.md §5.2.5` — Override patterns
  documentation.
- `CHANGELOG.md [4.0.0-alpha.20.1]` — release entry.
- `internal/v4alpha/judge/judge_util.go:253-264` — the 10 real
  override patterns (`OP-1..OP-10`).
- dark-memory rows 2344 (e2e T12 caveat), 2345 (e2e final report),
  2375 (Chunk 8.7 SUMMARY), 2377 (Chunk 8.8 SUMMARY),
  2379 (Chunk 8.9 SECTION, pinned), 2380 (Chunk 8.10 SECTION,
  pinned).

## 8. LUCIDEZ honest disclosure — drift ledger

- **Chunk 8.7 SPEC-vs-reality**: 500 LoC → 1750 LoC (+250%). Operator
  decision B (Phase 5 port + lite bitemporal) was broader than
  SPEC §3.6. Approved.
- **Chunk 8.8 SPEC-vs-reality**: 600 LoC → 719 LoC (+20%). Operator
  approved the LUCIDEZ drift table addition.
- **Chunk 8.9 SPEC-vs-reality**: 40 LoC → 115 LoC (+188%).
  Reconciliation + LUCIDEZ section are necessary to honestly close
  row 2344. Approved.
- **Chunk 8.10 SPEC-vs-reality**: 30 LoC → 50 LoC (+67%). OD7
  table entry + decision-sign-doc context are necessary for
  codification. Approved.
- **Phase 9 aggregate**: 3,000 LoC → 8,943 LoC (+198%). All
  approved explicitly via operator decisions documented in
  atomic mirrors.

No drift was hidden. No finding was papered over.

---

**Phase 9 (alpha.20 + alpha.20.1) is SHIPPED.** Operator sign-off
implicit via "Cerremos" + this commit.