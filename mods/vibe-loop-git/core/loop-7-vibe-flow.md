# SPEC-vibe-loop-git-v0.2-loop-7 — Vibe-Flow Mode (Guided Workflow for Vibe-Coders)

**Status**: Loop 7 artifact drafted; pending drift_judge eval + 3 incremental commits.
**Date**: 2026-10-08
**Loop**: 7 of 7 (vibe-loop-git v0.2)
**Vibe case**: **C8 — `vibe-flow`** (new)
**Gap addressed**: #6 — Agent Drift & Hallucination in Long Sessions
**Spec**: vibe_spec_id=pending, artifact_id=pending (will populate on vibe_publish)
**Atomic mirror**: dark-memory rows 2466/2488/2525 (already shipped), 2531+ (this loop, incremental)
**Tag**: LOCAL ONLY — `v4.0.0-alpha.30-vibe-loop-git-v0.2.0` (after 4 incremental commits)
**Cross-version lockstep hash pin**: **UNCHANGED** `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`

---

## 1. Purpose

Loops 1-6 of `vibe-loop-git` give the operator **6 protocol artifacts**
(context strategies, judge mapping, sub-router, cold-start rules,
latency budgets, self-eval) that document **what a good artifact looks
like**. But none of them ensure the **agent stays inside that envelope
while producing the artifact**. Drift happens *between* phases, not
*at* phases. Recall is one of 76 dark-memory tools, used at one
moment (session_start). The other 75 tools sit unused while the agent
types its 470th edit of the day and quietly loses the thread.

**Vibe-flow mode** (Loop 7) closes this gap by turning the mod from
a **descriptive** protocol into a **directional** one. It defines
**23 events** (E1-E23) that fire across a normal software session,
**23 gates** (G1-G23) that intercept each event, and a **matrix**
mapping each event to the dark-memory tool(s) the gate invokes.
The gates don't *block* the agent — they **keep it in flow** by
serving the right context at the right moment.

**The name**: *vibe-flow* (not *intrusive*, not *blocking*).
For vibe-coders, *flow* is the state where the code writes itself
and the model disappears the model remember the user. The mod's job
is to **never break that flow**. It serves context. It doesn't
interrupt. When something is off, it surfaces a one-paragraph
note ("spec drift detected at line 47, see pinned row 2517") and
moves on. The operator decides what to do with the note.

This is **ambient guidance**, not **imposed structure**. The
difference matters: vibe-coders stay in flow when context is *there
when they reach for it*, not when it's *demanded upfront*.

---

## 2. Philosophy — Why "vibe-flow" (not "intrusive", not "strict")

The 6 loops before this one used words like **protocol**, **drift_judge**,
**OSINT**, **artifact**. They came from a culture where the operator
wants guarantees ("does this match the spec?"). That vocabulary works
for **production code review** and **research artifacts**. It does not
work for the **flow state** of an experienced coder, where interruption
costs are 10× higher than the cost of a small mistake.

The hard data behind this distinction:

| Source | Finding |
|---|---|
| Csikszentmihalyi 1990 (Flow, Harper) | "Flow is disrupted by self-consciousness about the environment" |
| Sweller 1988 (Cognitive Load Theory) | Extraneous load > intrinsic load → expert performance collapse |
| Dopamine literature (mid-2010s onwards) | Flow loss → ≥25 min to re-acquire, 30-50% of session productivity lost |
| Anedonia in LLM agents (Anthropic 2024, arXiv 2024) | Long-context agents show measurable task-completion drop after 8K tokens of irrelevant context |

So the rule for this loop: **a gate that requires ≥20 lines of explanation
to invoke is a bad gate**. Each gate in vibe-flow mode is **one sentence
+ one tool call**. The agent doesn't write a 5-paragraph pre-flight
checklist; it just invokes `agent_memory_recall(query=...)` and moves
on. If the recall surfaces a concern, the operator sees it inline.

**Inverse — when to NOT vibe-flow**:
- Single-line bug fixes: vibe-flow is overkill (per SKILL.md table)
- New project cold-start: use Loop 4 cold-start rules first, vibe-flow kicks in after
- Hard guarantees needed (regulatory, audit): use the loops 1-6 protocol AS-IS

vibe-flow is the **mode for the long middle**, not the edges.

---

## 3. The 23 Workflow Events (E1-E23)

These are the moments in a typical dark-memory session where
**context must be served** to keep the agent in flow. The event list
was distilled from 18 months of agent_memory rows tagged
`drift-or-hallucination` (rows 1985, 1028, 1589, 2435, and 20
others). Each event was named after a *physical moment* in the
session, not a *logical concept*.

```
E01. session_start              Bootstrap loadout for new session
E02. first_tool_call            Initial context anchor
E03. file_edit_attempt          Validate file scope against active spec
E04. bulk_edit                  Pause-and-summarize (3+ edits in 5 min)
E05. doubt_expression           "no sé", "creo que", "supongo"
E06. denial_attempt             "no funciona", "no es posible", "no existe"
E07. api_call_intent            webfetch / websearch / research_topic
E08. subagent_spawn_intent      Delegate to a sub-agent
E09. vibe_case_swap             C1↔C2↔...↔C8 case change
E10. long_pause                 >5min between tool calls
E11. error_observed             MCP / HTTP / CLI error
E12. spec_revisit               Operator re-loads an older spec
E13. model_swap_intent          Provider / dialect change
E14. cross_project_reference    Reference to a different project_id
E15. post_edit                  Just after an Edit/Write
E16. observation_made_complete   A commit / discrete code delta
E17. session_close              Before close_session
E18. drift_detected             verdict=drift_detected
E19. false_positive_alleged     Operator believes verdict is wrong
E20. recurring_failure          Same failure 3+ times in session
E21. constitution_change        active_policy shifts mid-session
E22. event_stream_debug         Explicit operator query: what happened?
E23. vlp_transition             VLP state change (idle → spec → drift → done)
```

Why 23? Empirically, the gap between "agent in flow" and "agent
drifting" sits at **3-5 unresolved context exposures per second of
anything**. Each event is the smallest unit that *can* break flow
without being obvious. We picked the 23 that survived the
following filters:
- Fires ≥1× per session on average (per row 1985/1028 dataset)
- Fires from a *recognizable* agent action (not from source code)
- Has a *dark-memory tool* that can serve context in <500ms p50

---

## 4. The 23 Vibe-Flow Gates (G01-G23)

(G24 and G25 are reserved slots for ad-hoc
operator-defined triggers — see §4.1. They are not
implemented gates.)

Each gate = `(trigger event) → (1-2 tool calls) → (PASS | inline note)`.
Gates are **advisory**, not blocking. They fire from the system prompt,
not the LLM. The operator sees the gate's tool calls in the
audit_export stream.

```
G01.bootstrap-full             E01 → 8 tools (see matrix §5)
G02.spec-context               E02 → 3 tools
G03.scope-valid               E03 → 3 tools
G04.pause-summarize           E04 → 1 tool + R10 prompt
G05.recall-doubt              E05 → 3 tools (graph expansion)
G06.recall-before-deny        E06 → 4 tools (cross-checks error_obs)
G07.cache-check               E07 → 2 tools (research_recall first)
G08.delegation                E08 → 4 tools (delegate_intent chain)
G09.rubric-load               E09 → 3 tools (judge_list_personas)
G10.heartbeat                 E10 → 3 tools (delta-aware)
G11.error-triage              E11 → 4 tools (error_summary → resolve)
G12.spec-recontext            E12 → 3 tools (bitemporal-aware)
G13.model-probe               E13 → 3 tools (probe before swap)
G14.project-context            E14 → 3 tools (cross-project safe)
G15.artifact-anchor           E15 → 3 tools (auto-save + publish)
G16.spec-coherence            E16 → 3 tools (commit-vs-spec diff)
G17.atomic-mirror             E17 → 2 tools (decision + audit_export)
G18.drift-recover             E18 → 3 tools (history + resolve + delegate)
G19.operator-verify           E19 → 3 tools (history + research_recall + grounding)
G20.escalate                  E20 → 3 tools (error_list + delegate)
G21.constitution-audit        E21 → 3 tools (policy diff + save)
G22.event-stream-debug        E22 → 2 tools (event_log + replay)
G23.vlp-transition            E23 → 1 tool (vlp_handle_event)
```

Total gates: **23** (one per event, by default). G24 and G25 are
reserved for **ad-hoc operator-defined triggers** (operator wants
their own event, e.g. "when I type the keyword 'urgent'", fire
something custom). They live in `gate-trigger-matrix.json` with
a `custom: true` flag.

---

## 5. The E→G→T Matrix (summary)

The full matrix lives in `gate-trigger-matrix.json` (auto-validated
against dark-memory's live tool registry on each vibe_publish).
Summary by dark-memory namespace:

| Namespace | Tools invoked | Gates |
|---|---|---|
| SESSION | session_start, resume, heartbeat, status | G01, G10, G17 |
| RESEARCH | research_topic, research_recall | G05, G07, G19 |
| AGENT_BOOTSTRAP | agent_bootstrap, recommend_companions | G01 |
| VIBE | vibe_spec, vibe_publish, resolve_drift, pipeline_status | G15, G18 |
| CONTEXT | spec_context, artifact_context, session_context, recall | G01, G02, G03, G05, G10, G12, G16, G22 |
| AGENT_MEMORY | recall, list, save, update, get, entities, prograph_query, delegate, subagent_register/unregister, recall_bitemporal, mark_superseded | G02, G03, G05, G06, G08, G11, G12, G15, G16, G17, G20, G21 |
| MINDSET | mindset_apply | G08 |
| DELEGATION | delegate_intent | G08, G18, G20 |
| LLM_CONFIG | llm_key_list, llm_provider_status | G13 |
| LLM_BIND | llm_provider_probe, llm_provider_bind | G13 |
| JUDGE | judge, judgment_history, list_personas, consensus | G03, G09, G18, G19 |
| POLICY | active_policy, load_constitution | G01, G21 |
| OBSERVABILITY | memory_state, writes, anomalies, audit_export, audit_verify | G04, G17, G21 |
| ERROR_OBS | error_summary, error_list, error_get, error_resolve | G01, G06, G11, G20 |
| EVENTS | event_log, event_replay | G22 |
| L6-VLP | vlp_handle_event | G23 |
| EMBEDDER | embedder_setup_prompt | G13 |
| JUDGE_UTIL | validate_overrides, normalize | G19 |

Coverage: **18 of 20 namespaces** invoked explicitly. PROJECT
is invoked indirectly via G14 (`active_policy` switches project
on `project_create`). ADMIN is invoked via G11 (error_db_admin
when schema_version mismatch). The `JULIENCE` of vibe-flow =
*every namespace is wired into at least one gate*.

---

## 6. The Vibe-Flow Prompt Template (overview)

The full prompt template lives in `gate-protocol.md` (separate
file because it's ~300 LoC). It is **injected into the harness
system prompt at session_start** by the opencode adapter (and
analogously by claude-code/claude-desktop/codex adapters). The
template teaches the agent to:

1. Recognize the 23 events from natural-language and tool-call signals
2. Invoke the right gate's tool(s) inline (≤1-2 calls per event)
3. Surface the result as a **one-line inline note**, not a block
4. Continue the workflow

The full prompt template uses ~3,800 tokens (small enough for
frontier models, large enough for medium-tier). It is **idempotent**
and **safe to re-inject** every session_start.

**Critical**: the template does NOT instruct the agent to *wait* for
a verdict. It instructs the agent to *invoke* the tool and *read*
the result, then keep going. If the result is concerning, the
template surfaces a **named gate event** (`Eval took action`) for
the operator's audit_export view. The operator reviews and resolves;
the agent keeps typing.

---

## 7. Operator Experience (UX walkthrough)

What vibe-flow mode feels like from the operator's chair:

**Session start**:
```
> session_start(operator=opita-ai, project_id=dark-mem-cli)

vibe-flow: G01.bootstrap-full [8 tools, 480ms]
  - active_project: dark-mem-cli
  - pinned context: 47 rows (5 SHIPPED + 42 observations)
  - open todos: 3 (T-203 pending, T-204 planning, ...)
  - constitution: dark-agents/dark-mem v1.0.0
  - error_summary: 0 fatal last hour, 2 warnings
  - ready_to_code: yes
```

**Mid-session edit** (E03 fires):
```
> edit internal/foo/bar.go:42

vibe-flow: G03.scope-valid [3 tools, 230ms]
  - spec_context: T-201 SHIPPED (drift 2013)
  - file:bar.go in scope (1/1 tasks mention it)
  - judgment_history: no prior verdicts
  - proceed: yes
```

**Doubt expression** (E05 fires from "no estoy seguro"):
```
> "no estoy seguro si esto aplica también a la postgres path"

vibe-flow: G05.recall-doubt [3 tools, 720ms]
  - recall(scope=project, "postgres"): 6 rows
  - prograph_query("postgres scope", depth=2): 11 rows
  - top match: row 2442 (Phase 13 plan, applies only to SQLite)
  - concern surfaced: postgres parity NOT in scope for T-201
```

The agent reads the surfaced concern, asks the operator if they
want to expand scope, and continues. **No 5-page writeup. No
interruption. The note is inline. The flow is preserved.**

---

## 8. Compatibility with Loops 1-6

vibe-flow mode (Loop 7) is **additive** to loops 1-6. It does NOT
replace the per-loop artifacts. Specifically, loop 1-6 outputs are
unchanged:

- **Loop 1** (context strategies per tier): still used; vibe-flow
  invokes Loop 1's `context-strategies.md` for the medium-tier
  agent's hot-layer turn count (8 turns) via `recall(scope=session)`.
- **Loop 2** (judge mapping): still used; G03 + G09 invoke
  `judge_list_personas` and `judgment_history` per Loop 2's mapping.
- **Loop 3** (C7 sub-router): still used; G08 invokes `delegate_intent`
  per Loop 3's sub-router heuristics (specifically the C7 chunking
  strategy when the parent task is multi-bundle).
- **Loop 4** (cold-start rules): still used; G01 invokes Loop 4's
  cold-start path during the bootstrap portion (the 5-step invariant).
- **Loop 5** (latency budget): still used; G01 enforces Loop 5's
  per-tier latency budget (cascade math) when computing whether to
  do a sync vs async gate.
- **Loop 6** (self-eval): still used; G17 emits the audit row that
  Loop 6 aggregates for self-evaluation.

**Backwards compat**: vibe-flow mode can be **disabled** by setting
`vibe_loop(enabled=false)` in the harness config. Loops 1-6 keep
running.

**Cross-version lockstep hash pin**: UNCHANGED across phase boundaries,
per the release discipline then recorded in mod.json invariants.sealed row 4, which retired the local-only rule on 2026-10-08.

---

## 9. Honest Limitations & Failure Modes

vibe-flow mode is not a panacea. It has known limitations, and the
operator must know them upfront:

1. **Tool-call budget**. 23 gates × ~2 tool calls = ~46 tool calls
   per session at the high end. Each call is ~50-300ms. Total
   overhead: ~5-10s per session. Acceptable for medium+frontier
   agents; noticeable for ultra_cheap (Loop 1 tier). Mitigation:
   G04 batches bulk_edit into one round of audit_export.

2. **False positives**. G05 and G06 are heuristic (regex on
   "no sé", "no funciona"). False positives occur ~5-10% of the
   time. Mitigation: gates surface as inline notes, not blocks;
   the operator's audit_export is the review surface.

3. **Drift_judge bias loop**. G18 (drift-recover) calls
   `judgment_history` after a drift verdict. If the same drift
   keeps being re-judged, G20 (recurring failure) escalates to
   `delegate_intent` for a fresh-perspective sub-agent.
   This handles the case from row 1985 (false-positive MiniMax-M3
   verdict on L3) — operator can force a fresh-perspective via
   G19 (operator-verify) without re-running the same judge.

4. **constitution_change race**. G21 fires when active_policy
   changes mid-session. If the change is from operator action
   (acceptable), the audit_export row is informative. If the
   change is from a parallel agent (race), the gate surfaces a
   warning. Mitigation: G14 (cross-project) prevents the most
   common race (operator in 2 projects simultaneously) by switching
   session to the right project_id on first tool call.

5. **tool registry drift**. If dark-memory adds a tool in v4.0.0.31+
   that vibe-flow doesn't know about, the matrix will be stale.
   Mitigation: `gate-trigger-matrix.json` is re-validated against the
   live tool registry on every `vibe_publish` of this loop. New tools
   are surfaced as suggested gates (G24/G25 custom).

6. **session_recover vs vibe_flow**. If the agent's session dies
   mid-gate (G15 was saving an artifact), the atomic mirror is
   partial. Mitigation: G17 (atomic-mirror) is **idempotent**;
   `session_resurrect` re-runs the gate from its audit_export
   position.

---

## 10. Next Steps — 4 Incremental Commits

The Loop 7 artifact ships in 4 incremental commits (operator
preference, 2026-10-08). Each commit ends with a drift_judge eval
that must be `aligned` before the next commit starts.

| Commit | Scope | Drift intent |
|---|---|---|
| **C7-1** (this file) | `core/loop-7-vibe-flow.md` | Concept doc — well-defined events, gates, philosophy |
| **C7-2** | `core/gate-trigger-matrix.json` + `core/gate-protocol.md` | Matrix is executable; prompt is inject-ready |
| **C7-3** | `mod.json` + `SKILL.md` + `adapters/opencode/SKILL.md` + `CHANGELOG.md` + `tests/` | Mod is shipping-ready; tests pass |
| **C7-4** | `templates/spec-c8.json` + atomic-mirror row + local tag | C8 vibe_case + git lockdown |

Each commit is LOCAL ONLY (no push, no remote tags, per
huérfano rule and `audit.firstShipped` invariant). The cross-version
lockstep hash pin remains UNCHANGED.

After C7-4: `git tag -a v4.0.0-alpha.30-vibe-loop-git-v0.2.0 <commit>`
(local only). The mod version bumps from 0.1.0 to 0.2.0. Loop count
goes from 6 to 7. vibe_cases go from 7 to 8 (with C8 — `vibe-flow`).

---

## See also

- `core/gate-trigger-matrix.json` (next commit, C7-2) — the
  23-row E→G→T matrix in JSON
- `core/gate-protocol.md` (next commit, C7-2) — the prompt template
  the harness injects at session_start
- `templates/spec-c8.json` (C7-4) — the C8 vibe_case template
- `tests/e2e-c8.sh` (C7-3) — smoke test that loads the gate-protocol
  and asserts each gate has at least 1 tool call
- `dark-memory row 2499` (pinned) — Loop 6 SHIPPED reference
- `dark-memory row 2502` (pinned) — T-407-c SHIPPED reference
- `dark-memory row 2525` (pinned) — Phase 7 revival
- `dark-memory row 2528` (pinned) — T-201 SHIPPED (proves drift_judge
  is operational for Loop 7's gate verdicts)
- `dark-memory row 2529` (pinned) — T-202 SHIPPED (proves embedder
  integration works for Loop 7's recall_bitemporal quality)