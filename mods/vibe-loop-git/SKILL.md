---
name: vibe-loop-git
description: |
  The vibe-loop protocol packaged as a dark-memory companion mod. Encodes
  a 13-loop OSINT-first workflow that turns any artifact (code, text,
  decision, research, video, audio, multi-bundle) into a drift-judged,
  atomic-mirrored, version-controlled work product. Loops 1-6 follow
  OSINT → spec → artifact → drift_judge → resolve_drift → atomic_mirror.
  Loop 7 (vibe-flow mode, vibe_case C8) adds ambient workflow gating:
  23 events E1-E23 fire across a normal session; 23 named gates G01-G23
  intercept each event with 1-4 dark-memory tool calls; the agent stays
  in flow via inline notes (NOT intrusive blocking). G24 and G25 are
  RESERVED slots for ad-hoc operator-defined triggers — not implemented
  gates, and they have no matrix entry. Loops 8-13 (Phase 20,
  SHIPPED 2026-10-08) add the honesty layer: <5min mitigation, model-tier
  + cost transparency, a measured token economy, operator-visible
  epistemic labeling, G19 drift arbitration, and the sub-agent handoff
  contract. Built on the drift_judge pipeline (Phase 16 SHIPPED in
  alpha.27) plus the T-407-c 3-layer defensive pattern (Phase 17 SHIPPED
  in alpha.28). LOCAL-ONLY — never git push.

  IMPORTANT: Loops 8-13 are a verified library with NO production
  callers yet, except Loop 11.2 (Phase 21) which now labels the
  confidence that vibe_publish actually emits. See
  core/phase-20-closeout.md §1 before assuming any runtime effect.

  Triggers on keywords: vibe-loop, vibe loop, vibe-loop-git, vibe loop
  protocol, drift-judge loop, atomic-mirror loop, OSINT-first workflow,
  13-loop vibe, vibe-flow mode, vibe_case C1 C2 C3 C4 C5 C6 C7 C8,
  drift_judge pipeline, resolve_drift, vibe_spec, vibe_publish, gate
  trigger matrix, ambient workflow gating, evidence grade, epistemic
  labeling, arbitration, sub-agent handoff.
license: proprietary
metadata:
  author: dark-agent
  operator: nico
  version: 0.4.3
  dark-memory-mcp-version: ">=4.0.0-alpha.30"
  loops-shipped: 13
  phase-20: CLOSED 2026-10-08 (6 loops, 9 artifacts, 360 tests, 23 drift judges, 8 consecutive ALIGNED). Library layer — 7 of 9 primitives still unwired. Phase 21 wired L11.2 (labels its own confidence) and instrumented L8.1a (advisory, non-load-bearing). See core/phase-20-closeout.md.
  drift-verdicts: 20-aligned-1-needs_human (2 operator-overrides for MiniMax-M3 false-positive pattern)
  loop-7-vibe-case: C8
---

# vibe-loop-git — 13-Loop Vibe Protocol for dark-memory

## What this mod is

A **reusable protocol** for turning raw work (a code change, a research
question, an architecture decision, a video script) into a
**drift-judged, atomic-mirrored, version-controlled** work product. It
encodes the lessons from closing 4 phase deferrals in dark-memory
(Phase 14 → 17) into 6 nested loops that the operator can invoke
on any artifact.

## What this mod is NOT

- **Not a CLI** — it's a protocol manifest + templates + tests
- **Not auto-executing** — every loop requires operator approval
- **Not pushing to remotes** — LOCAL ONLY (no `git push`, no remote tags)
- **Not a replacement for vibe-flow** — it sits ON TOP of vibe-flow

## When to load this skill

| If you want to... | Load this skill |
|---|---|
| Build a new artifact with drift-judge baked in | YES |
| Audit a past drift-judge verdict | YES |
| Decide between 3+ options for a non-trivial change | YES |
| Write a 1-line bug fix | NO (overkill — use direct commit) |
| Run a Phase-style SHIP on dark-memory itself | YES (this is what produced v0.2) |

## The 13-loop protocol (in dependency order)

```
Loop 1: Context strategies per tier  (C2 — text)
  ↓ defines how sub-agents should structure their context
Loop 2: Judge model auto-mirror      (C3 — decision)
  ↓ defines which judge model per vibe_case
Loop 3: C7 multi-agent subrouter     (C7 — multi)
  ↓ depends on Loop 1's context strategies
Loop 4: Cold-start bootstrap rules   (C2 — text)
  ↓ depends on Loop 3's sub-router
Loop 5: Latency axis + cascade math  (C3 — decision)
  ⊥ parallel to Loop 4
Loop 6: Self-evaluation meta-loop    (C3 — decision)
  ↓ needs audit rows from Loops 1-5
Loop 7: vibe-flow mode               (C8 — ambient workflow gating)
  ↓ needs Loops 1-6 wired; adds 23 ambient gates (E1-E23) that fire inline

── Phase 20: the honesty layer (SHIPPED 2026-10-08) ──

Loop 8: <5min mitigation            (C8 — Horvitz anytime algorithms)
  ↓ classifier + context + mode + adaptive depth
Loop 9: model tier + cost           (C8 — cheapest tier that fits)
  ↓ cost transparency; placeholder pricing never reported as fact
Loop 10: token economy              (C8 — measured savings vs baseline)
  ↓ unclamped percentages so overspend stays visible
Loop 11: operator visibility        (C8 — Kadavath calibration)
  ↓ every number carries an EvidenceGrade; confidence goes DOWN only
Loop 12: G19 epistemic arbitration  (C8 — Panickssery 2024)
  ↓ only INDEPENDENT evidence may dispute a verdict
Loop 13: sub-agent handoff          (C8 — delegation contract)
  ↓ headcount is not evidence; only unresolved disputes travel
```

**Phase 20 is a verified library. Eight of its nine primitives still have
zero production callers.** The primitives exist, are tested (360/360) and
drift-judged (23 evals), but nothing in the running MCP invokes them yet,
and `bin/dark-mem-mcp.exe` predates all of it. Read
`core/phase-20-closeout.md` §1 before assuming a runtime effect.

**Phase 21 wires the first one (L11.2):** `vibe_publish` now returns
`confidence_grade` and `confidence_caveat` alongside `confidence`. A
`skipped` or `pending` verdict grades `unverified` — because a 0 there
means no LLM ran, not a measured zero.

**Per-loop phases (6)**:

1. **OSINT** — parallel `web_search` + `academic_search` + `web_fetch`
   against tier-1 sources (vendor blogs > NVD > arXiv > news)
2. **SPEC** — `vibe_spec` with structured tasks
3. **ARTIFACT** — concrete file/decision produced
4. **DRIFT_JUDGE** — `vibe_publish` (artifact_ref=file) emits drift verdict
5. **RESOLVE_DRIFT** — accept (aligned) or revise (drift_detected) or stop (needs_human)
6. **ATOMIC_MIRROR** — `dark_memory_agent_memory_save` row for cross-session recall

**Loop 7 adds a 7th phase that operates orthogonally**:

7. **GATE_PROMPTS** — `vibe_flow` mode injects the gate-protocol.md system-prompt
   block at session_start. The agent recognizes 23 events from natural signals
   and invokes the right gate's tool calls inline. Each gate fires 1-4 dark-memory
   tool calls (e.g. agent_memory_recall, prograph_query, research_recall) and
   surfaces a 1-line inline observation. The operator reviews via audit_export.

## The 7 vibe_cases

Each loop declares its vibe_case upfront so the drift_judge knows what
canonical artifact to expect and what to look for:

| Case | Label | Canonical artifact | Drift intent |
|---|---|---|---|
| C1 | code | `internal/**/*.{go,ts,tsx,js,jsx}` | Does the diff match the spec's intent? |
| C2 | text | `**/*.md` | Does the prose faithfully cite tier-1 sources? |
| C3 | decision | `**/DECISIONS.md` or atomic-mirror row | 3 options documented? Bias declared? |
| C4 | research | `**/*-osint*.md` or atomic-mirror row | All claims tier-1 sourced? |
| C5 | video | `video-output/*.mp4` + S2V-01 ref | Subject persists? No AI-slop signals? |
| C6 | audio | `audio-output/*.wav` + speaker embedding | Voice persists? Noise floor OK? |
| C7 | multi | `core/c7-subrouter.json` + N bundled artifacts | Internally consistent? Bundle coherent? |

## The T-407-c 3-layer defensive pattern (built-in)

For C1 / C2 / C5 / C6 / C7 (any loop that calls a chat-completion API),
this mod assumes dark-memory is on alpha.28+ which provides:

1. **Per-model defaults table** (`resolveMaxTokens`) — right budget per
   model family (Anthropic Claude, DeepSeek, OpenAI o-series, MiniMax)
2. **Operator override** (`nli_config_json.primary.max_tokens_override`)
   — escape hatch for models not in the table
3. **`finish_reason="length"` detection + retry-on-length** — first-class
   handling of reasoning-budget exhaustion (1 retry at 4×, capped 8192)

**Observed variance** (alpha.27): 2/7 = 28%
**Expected variance** (alpha.28+ with T-407-c): <1% (TokenMix Q1 2026
wallet-log evidence: 40% scenario at max_tokens=200 dropped to <1% at 1500+)

## How to invoke (the minimal happy path)

```bash
# 1. Start a session
dark_memory_session_start(operator="nico", project_id="<your-project>")

# 2. Plan a loop
vibe_spec(
  vibe_case="C2",  # or whichever
  tasks=[
    "OSINT: 3 tier-1 sources on <topic>",
    "SPEC: write 1-page spec",
    "ARTIFACT: produce <file>",
    "DRIFT_JUDGE: vibe_publish artifact_ref=<file>"
  ]
)

# 3. Execute each phase (skip the OSINT if already researched)
# 4. After drift verdict, save the result
dark_memory_agent_memory_save(
  kind="decision",  # or finding / observation / todo / link / context
  title="<loop N> SHIPPED — <artifact>",
  content="<summary with file:line refs and drift verdict>",
  tags="vibe-loop-git,loop-N,osint-first,drift-judge",
  pinned=true
)
```

## Required operator action items (BLOCKING for production use)

From `core/self-eval.json` honest_failure analysis (Loop 6):

| ID | Action | Blocking? | Time |
|---|---|---|---|
| OI-1 | Cold-start validation from fresh dark-memory project | YES | 15min |
| OI-2 | Review honest_failures severity ratings | YES | 20min |
| OI-3 | Decide A1 methodology replacement | YES | 5min |
| OI-4 | Cross-model blind eval with non-MiniMax-M3 NLI | no | 30min |
| OI-5 | Brier score on drift_judge verdicts | no | 45min |
| OI-6 | Empirical MiniMax-M3 thinking-block distribution | no | 60min |
| OI-7 | Cost analysis: variance × retry per 100 calls/day | no | 15min |

## Cross-references (this mod's evidence trail)

- `docs/specs/SPEC-vibe-loop-git-v0.2-loop-1.md` — Loop 1 spec (drift 1520)
- `mods/vibe-loop-git/core/judge-mapping.json` — Loop 2 (drift 1521)
- `mods/vibe-loop-git/core/c7-subrouter.json` — Loop 3 (drift 1524)
- `mods/vibe-loop-git/core/coldstart-rules.md` — Loop 4 (drift 1525)
- `mods/vibe-loop-git/core/latency-budget.json` — Loop 5 (drift 1526)
- `mods/vibe-loop-git/core/self-eval.json` — Loop 6 (drift 1527)
- `mods/vibe-loop-git/core/t-407-c-design.md` — T-407-c design
- dark-memory row 2499 (Loop 6 SHIPPED, pinned)
- dark-memory row 2501 (T-407-c OSINT, 5 tier-1 sources, pinned)
- dark-memory row 2502 (T-407-c SHIPPED, pinned)

## Local-only discipline

This mod NEVER pushes. All work is in `feat/v4-redesign` branch,
LOCAL ONLY. Cross-version lockstep hash pin remains UNCHANGED across
all phase boundaries. Tags are local (`v4.0.0-alpha.28` etc.).

## Status

- v0.1.0 packaging (this commit) — 6 loops shipped, 7 drift verdicts aligned
- Next: v0.2 — per-adapter smoke tests + per-template drift examples
- v0.3 (deferred): OI-4/5/6/7 + T-407-c-b/c/d

## See also

- `adapters/opencode/SKILL.md` — harness-specific invocation
- `adapters/claude-code/SKILL.md` — Claude Code equivalent
- `adapters/claude-desktop/SKILL.md` — Claude Desktop equivalent
- `adapters/codex/SKILL.md` — OpenAI Codex equivalent
- `templates/spec-c{1..8}.json` — per-vibe_case spec templates
- `tests/` — bootstrap, e2e-c*, audit-trail-verify
- `core/self-eval.json` — meta-loop honesty (what this protocol can and
  cannot measure)
