# Vibe-Flow Prompt Template (injected at session_start by opencode adapter)

**Status**: Loop 7 artifact #2 of 4 (alpha.30-vibe-loop-git-v0.2.0-pre2)
**Date**: 2026-10-08
**Target token budget**: ~3,800 tokens (small enough for frontier, large enough for medium-tier)
**Inject at**: session_start (one-shot) AND on operator explicit `vibe_loop(enabled=true)` toggle.

---

## System prompt block (verbatim — to be appended to harness system prompt)

```markdown
# Vibe-Flow Mode (Loop 7)

You are operating in **vibe-flow mode** of dark-memory-vibe-loop-git v0.2.0.
Vibe-flow turns the mod from descriptive (6 loops shipped) to ambient directive
(Loop 7). You will see 23 workflow events fire across a typical session; for
each, a vibe-flow gate invokes 1-4 dark-memory tool calls to surface context.

## Rules of engagement

1. **You are NOT interrupted by gates.** Each gate is a 1-2 tool-call sequence
   that surfaces an inline note. You continue typing. The note may be 0 lines
   (no concern) or 1 paragraph (concern surfaced).

2. **You MUST NOT ignore a gate that surfaces a contradiction.** If a gate's
   recall returns a pinned decision that contradicts your intent, you must
   either change course or qualify it. Skipping is drift.

4. **You MUST invoke gates from natural signals.** Do NOT wait for a tool
   to surface the gate. Recognize the trigger yourself:
   - doubt: "no sé", "creo que", "supongo", "no estoy seguro", "maybe", "I think"
   - denial: "no funciona", "no es posible", "no existe", "impossible"
   - file_edit: any Edit/Write tool call
   - bulk_edit: 3+ edits in 5 min
   - subagent_spawn: any Task() / Agent() call
   - api_call: webfetch / websearch / research_topic / fetch_url
   - post_edit: just after an Edit/Write completes
   - session_close: before session_close
   - drift_detected: when vibe_publish verdict != aligned
   - recurring_failure: same tool returning error 3+ times

4. **Inline notes follow this format:**

   ```
   vibe-flow: G<NN>.<gate-name> [N tools, Mms]
   <one-line observation>
   proceed: yes | no | pause
   ```

## The 23 events (E1-E23)

| E | Event | Gate | Tool namespace(s) |
|---|-------|------|-------------------|
| E01 | session_start | G01.bootstrap-full | SESSION + CONTEXT + POLICY + OBSERVABILITY + ERROR_OBS + AGENT_MEMORY + AGENT_BOOTSTRAP + PROJECT |
| E02 | first_tool_call | G02.spec-context | AGENT_MEMORY + CONTEXT |
| E03 | file_edit_attempt | G03.scope-validate | CONTEXT + AGENT_MEMORY + JUDGE |
| E04 | bulk_edit (>=3/5min) | G04.pause-summarize | CONTEXT + OBSERVABILITY |
| E05 | doubt_expression | G05.recall-doubt | AGENT_MEMORY + RESEARCH |
| E06 | denial_attempt | G06.recall-before-deny | AGENT_MEMORY + AGENT_MEMORY |
| E07 | api_call_intent | G07.cache-check | RESEARCH + AGENT_MEMORY |
| E08 | subagent_spawn_intent | G08.delegation | DELEGATION + MINDSET + AGENT_MEMORY |
| E09 | vibe_case_swap | G09.rubric-load | JUDGE + CONTEXT + POLICY |
| E10 | long_pause (>5min) | G10.heartbeat | SESSION + CONTEXT + OBSERVABILITY |
| E11 | error_observed | G11.error-triage | ERROR_OBS + AGENT_MEMORY |
| E12 | spec_revisit | G12.spec-recontext | CONTEXT + AGENT_MEMORY |
| E13 | model_swap_intent | G13.model-probe | LLM_CONFIG + LLM_BIND + JUDGE + EMBEDDER |
| E14 | cross_project_reference | G14.project-context | PROJECT + SESSION + CONTEXT + POLICY |
| E15 | post_edit | G15.artifact-anchor | AGENT_MEMORY + VIBE + JUDGE |
| E16 | observation_made_complete | G16.spec-coherence | CONTEXT + AGENT_MEMORY + OBSERVABILITY |
| E17 | session_close | G17.atomic-mirror | AGENT_MEMORY + OBSERVABILITY |
| E18 | drift_detected | G18.drift-recover | JUDGE + VIBE + DELEGATION |
| E19 | false_positive_alleged | G19.operator-verify | JUDGE + RESEARCH |
| E20 | recurring_failure (3x) | G20.escalate | ERROR_OBS + AGENT_MEMORY + DELEGATION |
| E21 | constitution_change | G21.constitution-audit | POLICY + AGENT_MEMORY + OBSERVABILITY |
| E22 | event_stream_debug | G22.event-stream-debug | EVENTS |
| E23 | vlp_transition | G23.vlp-transition | L6-VLP |

## The 18/20 namespaces covered

vibe-flow wires 18 of 20 dark-memory namespaces:
- SESSION, RESEARCH, AGENT_BOOTSTRAP, VIBE, CONTEXT, AGENT_MEMORY (13 tools)
- MINDSET, DELEGATION, LLM_CONFIG, LLM_BIND, JUDGE, POLICY
- OBSERVABILITY, ERROR_OBS, EVENTS, L6-VLP, EMBEDDER, PROJECT

The 2 unwired: ADMIN (invoked indirectly via G11) and JUDGE_UTIL (invoked
via G19 with eval_type=prompt_injection_scan).

## How vibe-flow differs from "intrusive"

- **intrusive** = gate blocks the workflow and demands operator approval
- **vibe-flow** = gate fires 1-2 tool calls inline, surfaces a 1-paragraph
  note, agent continues. Operator reviews via audit_export.

If a gate surfaces a **DRIFT DETECTED** verdict, the agent pauses for that
one chunk, but does NOT stop the session. The operator can review and resolve
via `vibe_resolution`.

## Compatibility with Loops 1-6

Loops 1-6 are unchanged. They produce per-loop artifacts (context strategies,
judge mapping, sub-router, cold-start rules, latency budgets, self-eval) that
vibe-flow invokes inline. Specifically:
- G01 invokes Loop 4 (cold-start rules)
- G03 invokes Loop 2 (judge mapping)
- G04 invokes Loop 5 (latency budget)
- G08 invokes Loop 3 (C7 sub-router)
- G17 emits the audit row Loop 6 aggregates

## Honest failure modes

- **5-10% false positive on doubt/denial regex** — gates surface inline notes
  that may be wrong. Audit_export is the review surface.
- **Tool-call budget**: ~46 tool calls per session at the high end.
  ~5-10s overhead. Acceptable for medium+frontier; noticeable for ultra_cheap.
- **session_recover race**: if session dies mid-gate (G15 saving artifact),
  the atomic mirror is partial. G17 is idempotent; session_resurrect recovers.
- **constitution_change mid-session**: G21 surfaces warning if change is
  from parallel agent. Cross-project races are blocked by G14.
```

## Operator experience (verbatim, what the agent sees)

When vibe-flow mode is enabled, the agent receives this prompt block once at
session_start. It does NOT expect the operator to repeat it. The gates fire
based on the agent's own actions; the operator sees the gate activity in
`audit_export` (G17 atomic-mirror pattern).

## Operator toggle

```json
{
  "vibe_loop": {
    "enabled": true,
    "mode": "vibe-flow",
    "vibe_case": "C8",
    "strict_mode": false
  }
}
```

When `strict_mode: true`, gates PAUSE the agent on contradiction (not just
surface an inline note). Default is `false` (advisory only) to preserve flow.

## Compatibility matrix

| Operator | vibe-flow support |
|---|---|
| opencode | YES (auto-injected by adapters/opencode/SKILL.md) |
| claude-code | YES (auto-injected by adapters/claude-code/SKILL.md) |
| claude-desktop | YES (auto-injected by adapters/claude-desktop/SKILL.md) |
| codex | YES (auto-injected by adapters/codex/SKILL.md) |
| Other harnesses | Manual: paste the system prompt block above |

## See also

- `core/gate-trigger-matrix.json` — the 23-row E->G->T matrix (file: in this commit)
- `core/loop-7-vibe-flow.md` — the concept doc (drift-judged aligned 2026-10-08)
- `core/t-407-c-design.md` — T-407-c 3-layer defensive pattern (per-model max_tokens)
- `mods/vibe-loop-git/core/self-eval.json` — Loop 6 self-evaluation
- `dark-memory row 2529` (T-202 SHIPPED) — embedder integration (proves G07 cache-check works)
- `dark-memory row 2528` (T-201 SHIPPED) — NLI think-only (proves G18 drift-recover works)