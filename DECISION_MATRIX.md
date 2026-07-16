# Dark Memory MCP — Merge Decision Matrix

**Session**: dark-memory-mcp-merge-decision-2026-07-15
**Audience**: operator
**Branch**: `review/w3p1` (15 commits ahead of `main`)
**Gate verdict**: **GO** (per HUMAN_GATE_REPORT.md §1)

---

## How to read this

The agent finished its review. The gate says GO. **5 decisions** still need your eyes before the merge happens. Each section is:

- **What** — the decision in one sentence
- **Why now** — what depends on it
- **Options** — 2 or 3 choices
- **Recommendation** — single opinionated pick, with reasoning
- **Reversibility** — can you undo it

Mark each with ✓ or ✗. Once all 5 are decided, the merge command is in §6.

---

## Decision 1 — Merge review/w3p1 to main, or hold?

**What**: Should `review/w3p1` (15 commits, the full Wave 4 work) be merged to `main`?

**Why now**: This is THE decision. Everything else is conditional.

**Evidence** (from HUMAN_GATE_REPORT.md):
- 9/9 test suites green with `-count=1` cold rebuild
- 8/8 RFC §12 acceptance criteria have evidence (6 PASS, 2 DEFERRED with rationale)
- 5/5 BRIDGE contracts verified
- 7 INV-* invariants enforced and tested
- 1 HIGH gate finding found + fixed during review (bridge.7 cold-cache flake)
- 4 review-w4 ship-now fixes committed

**Options**:
| Option | Pros | Cons |
|---|---|---|
| (a) Merge now | Closes Wave 4 in this session. Evidence is fresh. | No README.md at root (gate.3 PARTIAL) |
| (b) Hold for README | Converts PARTIAL → PASS. 5-min effort. | Splits the merge across two commits |
| (c) Hold for backlog | — | Backlog is by definition non-blocking; this is a trap |

**Recommendation**: **(a) Merge now**, but ALSO ship the README in the same merge (Decision 2 = before, 5 min). The PARTIAL is cosmetic (no README at root) and would still be PARTIAL even if you held the merge — the README doesn't unblock anything. Don't trap yourself in a "perfect is the enemy of shipped" loop.

**Reversibility**: Easy — `git revert -m 1 <merge-commit>` if anything breaks post-merge.

---

## Decision 2 — Write README.md before merge, or as v1.0.1 patch?

**What**: gate.3 says no `README.md` at repo root (RFC §12 #8 partial). Should we write it now (before merge) or file as a follow-up?

**Why now**: Affects the shape of the merge commit and the operator experience for anyone arriving at the repo cold.

**Options**:
| Option | Effort | Outcome |
|---|---|---|
| (a) Before merge (now) | 5 min | RFC §12 #8 = PASS; one clean merge commit with README included |
| (b) After merge (v1.0.1 patch) | 5 min later | RFC §12 #8 = PARTIAL until patch ships; two commits |

**Recommendation**: **(a) Before merge.** The README is 5 minutes (project one-liner + install per RUNBOOK §1 + 30-second tour of the 3 binaries + links to RFC + BRIDGE + constitution). Including it in the merge commit:
- Makes `git clone + cd + open README.md` the canonical onboarding
- Removes the PARTIAL from the gate report
- Cost: zero (we're already in the merge flow)

**Reversibility**: Trivial — README is just a markdown file.

If you pick (a), say so and I'll write it before the merge command in §6.

---

## Decision 3 — Sub-spec 180 (dark-scrapper daemon on :8901): start now, defer, or ignore?

**What**: The `dark_ssd_drift_judge` MCP tool currently returns HTTP 401 because the minimax stub API key is revoked. The dark-scrapper daemon (TypeScript+Bun, listens on :8901) is the resolved-correct-path per spec 180. Without it, **drift judgments for dark-research-mcp artifacts fall back to R9 self-judge**.

**Why now**: This blocks the dark-research-mcp drift_judge pipeline, NOT the dark-memory-mcp merge. The two are sibling repos.

**Critical clarification**: This decision does NOT block the dark-memory-mcp merge. The merge of `review/w3p1` to `main` is independent. Decision 3 is about whether to ALSO start the daemon in this session.

**Options**:
| Option | Effort | Outcome |
|---|---|---|
| (a) Start daemon now (run `bun --watch 'run src/cli.ts daemon'` from `C:\Users\Nico\dark-scrapper`) | 10-15 min | dark_ssd_drift_judge works end-to-end with real LLM verdicts; future drift logs use real judge instead of R9 |
| (b) Defer to next session | 0 min | Continue using R9; spec 180 stays deferred per master plan |
| (c) Ignore | 0 min | Drift_judge always falls back to R9; future specs must accept R9 verdicts |

**Recommendation**: **(b) Defer to next session.** Reasoning:
1. dark-memory-mcp merge doesn't depend on it
2. Spec 180 has 10 sub-tasks (T1..T10 per spec 180 detail); starting the daemon is just T3; the rest (opencode.jsonc patch T6, end-to-end judge T7, etc.) deserves its own session
3. dark-memory-mcp is the current focus; mixing the two dilutes the audit trail

If you want to do (a), tell me now and I'll run the sub-tasks. Otherwise spec 180 stays in the master plan as a deferred item.

**Reversibility**: N/A — daemon is process-level, not git.

---

## Decision 4 — Backlog handling: file as issues, inline, or drop?

**What**: 7 backlog items exist:
- `gate.2` LOW: `go.mod` line 61 doc drift
- `gate.3` MEDIUM: no README.md (= Decision 2 if you pick (a))
- `review-w4-b01` TOOLING: TDM-GCC install for `go test -race`
- `review-w4-b03` ARCH: migration to `modelcontextprotocol/go-sdk` v1.6.1 (official SDK)
- `sub-spec 160` DEFERRED: dark-recall v2.3 plugin (sibling repo)
- `sub-spec 161` DEFERRED: dark-research-mcp deprecation shim (sibling repo)
- `sub-spec 180` DEFERRED: dark-scrapper daemon (= Decision 3 if you pick (b)/(c))

**Why now**: Affects how the merge commit message is shaped and what shows up in the post-merge state.

**Options**:
| Option | Pros | Cons |
|---|---|---|
| (a) File as GitHub issues / dark.db backlog, ship clean merge | Clean state; no merge commit bloat | Requires gh auth or dark.db persistence |
| (b) Inline low-effort fixes in the merge commit (gate.2 + gate.3) | Less follow-up work | Bigger merge commit; harder to revert specific items |
| (c) Drop, hope someone files later | 0 effort | Knowledge evaporates; gate.2 LOW stays a doc bug forever |

**Recommendation**: **(a) + (b) hybrid**:
- **(a)** for the deferred / sibling-repo items (sub-specs 160/161/180) and the architectural one (review-w4-b03): file in dark.db spec 184 tasks or as GitHub issues — keep the merge clean
- **(b)** for gate.2 + gate.3: include in the merge commit. gate.2 is a 1-line comment fix; gate.3 is the README (Decision 2)

Net: merge commit includes 3 fixes (canary_present already shipped in b9225eb, panic recovery already shipped, mcp-go upgrade already shipped, + gate.2 comment + gate.3 README if you pick (a) for Decision 2 + the cold-cache flake fix already shipped in d5eb093).

**Reversibility**: Git issues / dark.db tasks are append-only; safe to file.

---

## Decision 5 — Version tag + remote push?

**What**: After merge, do we (a) tag v1.0.0 and push to remote? Both are separate operator choices.

**Why now**: Versioning is a release statement. Pushing to remote requires gh auth which is currently broken (per master plan task `plan.spec_176`: "No git remote configured; gh auth token in keyring invalid").

**Options**:
| Option | Pros | Cons |
|---|---|---|
| (a) Tag v1.0.0, push to remote | Standard release; discoverable; shippable | Requires gh auth refresh; may fail |
| (b) Tag v1.0.0, NO push (local-only) | Clean release without auth dependency | Tag only exists locally; team can't pull |
| (c) Tag v0.9.0-rc1, no push | Signals "not quite v1" | Understates the work; confuses users |
| (d) No tag, no push | Minimum commitment | No release anchor; harder to refer back |

**Recommendation**: **(b) Tag v1.0.0 local-only, NO push for now.** Reasoning:
1. The work IS feature-complete per RFC + constitution + BRIDGE — v1.0.0 is the honest version
2. RFC root, constitution, BRIDGE_AND_COEXISTENCE all declare v1.0.0; tagging lower would be inconsistent
3. Push is orthogonal: when gh auth is refreshed (separate task), `git push origin main --tags` is one command
4. v1.0.0 is a release marker, not a deployment marker; the deployment story is `opencode.jsonc` which is operator-side

**Reversibility**: `git tag -d v1.0.0` deletes a local tag. Push failure is fine — merge is independent.

---

## Decision summary

| # | Decision | Recommendation | Time |
|---|---|---|---|
| 1 | Merge review/w3p1 → main | **(a) Merge now** | 30s |
| 2 | Write README before merge | **(a) Before, 5 min** | 5 min |
| 3 | Sub-spec 180 daemon | **(b) Defer** | 0 |
| 4 | Backlog handling | **(a)+(b) hybrid** — dark.db for deferred, inline gate.2/3 | 6 min |
| 5 | Tag v1.0.0 + push | **(b) Tag local-only, no push** | 1s |

**Total time to fully resolved merge**: ~12 minutes.

If you accept all 5 recommendations, the execution sequence is:
1. Write `README.md` (5 min)
2. Fix `go.mod` line 61 comment (30s)
3. File dark.db tasks for deferred sub-specs 160/161/180 (1 min)
4. `git checkout main && git merge --no-ff review/w3p1` (30s)
5. `git tag -a v1.0.0 -m "..."` (1s)
6. Done.

Total: ~7 minutes of work + merge.

---

## What's NOT a decision (already decided by evidence)

These look like decisions but the evidence has already settled them:

- **Is the code correct?** — Yes. 9/9 suites green.
- **Is the architecture right?** — Yes (per master plan spec 178, RFC + BRIDGE + constitution).
- **Should we redo the schema migrations?** — No (they're at v7 with dual-driver contract test passing).
- **Should we wait for Postgres to be installed locally?** — No (CI covers it; local dev box uses sqlite).
- **Should we fix ALL the backlog in this session?** — No (backlog = non-blocking by definition).

---

## Operator input

Reply with one of:

- **"OK all 5"** — execute the recommended path (write README + fix comment + merge + tag)
- **"Hold"** — pause the merge, more work needed
- **"Override D2"** + alternate pick — keep my D1/D3/D4/D5 recommendations, change D2 only
- **Custom** — describe your variations per decision

I'm waiting. The merge command is on standby.