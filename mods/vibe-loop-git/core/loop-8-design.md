# Loop 8 Design — <5min Mitigation

**Status**: DRAFT (2026-10-08, opita-ai, session=sess-abf1c398ce86a16b)
**Phase**: 20 (vibe-flow polish)
**Operator-validated trigger**: row 2534 (operator confirmed vibe-flow is heavy on <5min tasks)
**SOTA foundations**: row 2535 (Horvitz 1990 anytime algorithms)
**Cost-economy constraint**: row 2538 (operator validated: dev cost is fine, user cost must be minimal)
**Persona set**: 10 (5 technical, 5 cross-cutting — see §Validation summary)
**Commits planned**: 6 (Loop 8 core: 3 + Loop 9 cost economy: 3)
**Cross-version lockstep hash pin UNCHANGED**

---

## Validation summary (operator-approved 2026-10-08)

The 10-persona theorizing + cost-lens synthesis established:

### 10 personas (5 technical + 5 cross-cutting)

| # | Persona | Vibe_case | Cost sensitivity |
|---|---|---|---|
| 1 | Táctico (junior, mode-fix) | C1 | Latency |
| 2 | Arquitecto (senior, design) | C1/C3/C7 | Tokens |
| 3 | Investigador | C4 | Tokens |
| 4 | Escritor | C2 | Tokens |
| 5 | Debugger en pánico | C1 | Latency (urgent) |
| 6 | Novato absoluto | any | Everything |
| 7 | Operador no-técnico | C2/C3 | Tokens |
| 8 | Multi-idioma (opita focus) | C2/C1 | Translation |
| 9 | Móvil-first | C2/C1 | Data + latency |
| 10 | Rush cognitivo | any | Latency |

5 of 10 personas are **cost-minimal** (1, 5, 6, 9, 10). 5 of 10 can absorb cost. The default must be the minimal tier, not the average.

### Per-vibe_case thresholds (L8.1a)

| Vibe_case | short_score threshold | long_score threshold | Justification |
|---|---|---|---|
| C1 code | **2.0** | 3.5 | Many short fixes; "refactor" can be 1 fn |
| C2 text | 3.0 | 3.0 | 50/50 |
| C3 decision | **4.5** | 2.5 | Decisions are serious work |
| C4 research | **5.0** | 2.0 | Research is deep by definition |
| C5 video | 3.0 | 3.0 | Similar to text |
| C6 audio | 3.0 | 3.0 | Similar to text |
| C7 multi | 4.0 | 2.5 | Multi = complex |
| C8 vibe-flow | 3.0 | 3.0 | Meta-case |

Defaults can be overridden per spec (deferred to L8.1b).

### Cost economy (Loop 9 — separate loop)

The cost-lens synthesis identified 4 additional decisions that warrant their own loop:

- **L9.1** — Model selection per persona (Haiku for cost-sensitive, Sonnet for expert)
- **L9.2** — Skip policy: short + novice = skip drift_judge, consensus, expensive recalls
- **L9.3** — Cost transparency: `dark_memory_session_cost(session_id)` tool

**Result**: Loop 8 stays focused on the heuristic core (3 commits). Loop 9 handles the cost economy (3 commits). Total 6 commits for Phase 20.

### Updated commits plan

- **L8.1a** (this commit) — Heuristic base: 5 features, 8 vibe_case thresholds, Go classifier package
- **L8.1b** (deferred) — Persona-tier detection: 4 new features (tier, domain, lang, device, rush)
- **L8.2** (deferred) — Lazy activation state machine (11 signals + `cost_budget_remaining`)
- **L8.3** (deferred) — Adaptive depth (sizing multiplicativo)
- **L9.1** (Loop 9) — Model selection per persona
- **L9.2** (Loop 9) — Skip policy
- **L9.3** (Loop 9) — Cost transparency

---

## Problem statement

Vibe-flow currently fires G1-G25 on every gate event regardless of task length. On tasks <5min, the cumulative injection overhead (block size, tool calls, system-prompt growth) exceeds the benefit (context the LLM didn't actually need because the task was simple). Operator-validated: row 2534.

**Goal**: make vibe-flow aware of task length and adapt the injection profile to it. Three layers, in increasing cost:

```
[Layer 1: heuristic upfront]    ->  0 LLM calls, <1ms, classify on first message
[Layer 2: lazy activation]      ->  0 LLM calls, state machine tracks session
[Layer 3: adaptive depth]       ->  same cost as current, but block size scales
```

## Layer 1 — Heuristic upfront (L8.1)

**When**: Immediately on `E1: session_start` (or on the first operator message after session start).

**What**: A cheap, deterministic, "fast and frugal" classifier (Gigerenzer 2000) on the operator's first message.

**Cost**: 0 LLM calls. ~1ms regex + scoring.

**Output**: `task_length_class ∈ {short, medium, long}` + `confidence ∈ [0, 1]`.

### Features (5)

| Feature | Weight | Type | "short" signal | "long" signal |
|---|---|---|---|---|
| `token_count` | high | int | <20 | >50 |
| `short_keywords` | medium | regex | "just", "quickly", "typo", "small", "single", "minor", "1 line" | — |
| `long_keywords` | medium | regex | — | "refactor", "migrate", "design", "implement", "investigate", "build", "create", "research" |
| `multi_step` | low | regex | — | "and then", "after that", "first... then", numbered list |
| `imperative` | low | pos | question form | imperative form |

### Threshold

```
short_score = (token_count < 20) * 2 + len(short_keywords) * 1.5
long_score  = (token_count > 50) * 2 + len(long_keywords) * 1.5 + multi_step * 1

if short_score >= 3 and short_score > long_score:
    return "short", min(short_score / 5, 1.0)
elif long_score >= 3 and long_score > short_score:
    return "long", min(long_score / 5, 1.0)
else:
    return "medium", 0.5
```

### Design choice: no LLM call here

A naive implementation would call the LLM: "classify this as short/medium/long". But that's 1 expensive call on every session start, even for trivial tasks. The deterministic heuristic is ~80% as accurate at 0% of the cost. SOTA precedent: Gigerenzer's "fast and frugal" trees (Gigerenzer, Todd, & ABC Research Group 1999, "Simple Heuristics That Make Us Smart").

### Edge cases (documented, not handled)

- Sarcastic "just refactor the entire auth system" → misclassified as short. **Operator's responsibility**: this is a heuristic, not an oracle.
- Non-English first messages → short_keywords regex won't match. **Operator's responsibility**: extend regex or use LLM fallback.
- Operator provides extensive context before asking → high token count misclassifies. **Operator's responsibility**: heuristic uses token count, not all-up context size.

## Layer 2 — Lazy activation (L8.2)

**When**: At session start (initial state = DEGRADED). Re-evaluated on every event.

**What**: State machine that escalates from DEGRADED to FULL on signal. Horvitz 1990 anytime algorithm pattern: "make a decision when you have enough info, not before".

**Cost**: 0 LLM calls for state tracking. 1-4 tool calls on escalation (same as current full mode).

**Output**: `vibe_mode ∈ {off, degraded, full}` per session, persisted across events.

### State machine

```
                operator override (vibe_loop{enabled: false})
                OR task_class == "short" AND elapsed < 5min
   ┌──────────────────────────────────────────────────────────┐
   ↓                                                          │
[off] ─────────(session_start)─────────> [degraded]          │
   ↑                                          │               │
   │                                          │ (any signal) │
   │                                          ↓               │
   └─────────────(operator override)────── [full] ────────────┘
```

### Escalation signals (any of)

- `elapsed > 5min` since session start
- `llm_tool_calls >= 3` in current session
- `operator_followup_count >= 1` (operator sent a message after the LLM already responded)
- `llm_emitted_doubt` (E5 fired at least once)
- `session_tokens > 1500`

### De-escalation signals (any of)

- `operator_override` (spec says `mode: "off"` or operator changes via tool)
- `task_class == "short"` AND `elapsed < 5min` AND no escalation signals

### Why this works

The intuition: in the first 5 minutes, the LLM is likely working on a small task. If the task turns out to be larger (escalation signal), we wake up the full vibe-flow. If the task is genuinely small, we never fire the expensive gates. This is exactly Horvitz's "expected value of additional computation" formula:

```
EV(more context) = P(task is long | current signals) × benefit(long context) - cost(more tokens)
```

If `EV > 0`, escalate. If `EV <= 0`, stay degraded.

## Layer 3 — Adaptive depth (L8.3)

**When**: On every gate fire, the block size is computed from the current `task_length_class`.

**What**: The gate-protocol.md block size scales with predicted task length. 3 sizes:

| Task class | Block size (tokens) | Content |
|---|---|---|
| `short` | ~500 | spec digest + 1 pinned memory |
| `medium` | ~2,000 | spec + 3 pinned + key memories + constitution summary |
| `long` | ~3,800 | full block (current behavior) |

**Cost**: Same as current (1-4 tool calls), but the *content* is smaller for short tasks.

**Citation**: Anthropic Sept 2024 "Contextual Retrieval" (row 2536). Key insight: 50-100 tokens of *high-signal* context reduces retrieval failure 49%. Smaller + sharper beats larger + noisier. Loop 9 will do the same on the per-chunk level (delta-based), Loop 8.3 does it on the per-block level.

### Mapping in gate-protocol.md

The current gate-protocol.md template has 7 sections (per loop-7 design). For `short` tasks, only section 1 (spec digest) and section 5 (1 pinned memory) are emitted. For `medium`, sections 1-4 + 6. For `long`, all 7.

## State persistence

The `vibe_mode` and `task_length_class` are stored in the session context (not agent_memory). They survive across events but not across sessions. This is intentional: each new session starts fresh with the heuristic on the first message.

## Override surface

- **Spec-level**: `vibe_loop{enabled: true|false, mode: "off|degraded|full", predicted_class: "auto|short|medium|long"}`
- **Operator-level**: `dark_memory_vibe_set_mode(mode="off|degraded|full")` (new tool, not in this loop)
- **Default**: `enabled: true, mode: "degraded"`, escalating to `full` on signal.

## Subtasks

| ID | Subtask | LoC est. | Commit |
|---|---|---|---|
| L8.1 | Heuristic upfront classifier (5 features, 1 threshold) | ~150 | commit 1 |
| L8.2 | Lazy activation state machine (3 modes, 5 escalation signals) | ~200 | commit 2 |
| L8.3 | Adaptive depth in gate-protocol.md template (3 block sizes) | ~100 | commit 3 |

3 commits total. Drift-judge between each. Local-only.

## Test plan

- Unit tests for the classifier (5 features, threshold edge cases)
- Integration test: simulate a 2-minute session, verify DEGRADED throughout
- Integration test: simulate a 10-minute session, verify escalation at 5min
- Integration test: simulate `short` task, verify block size ~500 tokens
- Integration test: simulate `long` task, verify block size ~3,800 tokens

## Failure modes (honest)

1. **Heuristic misclassifies** — task is actually long but classified short. Mitigation: escalation signals catch this within 5min.
2. **Escalation signal is too sensitive** — short task with one doubt signal escalates. Mitigation: 5min elapsed is the *primary* signal, others are secondary.
3. **Adaptive depth causes inconsistency** — LLM sees different block size on each event. Mitigation: the *content* is consistent, only the *size* varies.
4. **State machine races** — operator override and event fire at same time. Mitigation: operator override always wins (last-writer).

## Cross-references

- Row 2534: operator-validated <5min problem
- Row 2535: Horvitz 1990 SOTA foundation
- Row 2536: Anthropic 2024 contextual retrieval (Loop 9 dependency)
- Row 2537: Kadavath 2022 (Loop 11 dependency)
- Row 1985, 2013, 2016, 2017: MiniMax-M3 nli=contradiction/neutral false-positive pattern (Loop 11 motivation)
- `core/gate-protocol.md`: the template to be modified by L8.3
- `core/gate-trigger-matrix.json`: the matrix to be referenced by L8.2

## Operator decision points

1. **Threshold values** (3.0 short, 3.0 long): are these the right cutoffs? Or should I tune them with operator's prior session corpus?
2. **Escalation signals list** (5 signals): are there signals I'm missing? E.g., `multi_artifact` (LLM started working on multiple specs)?
3. **Block size mapping** (500/2000/3800): are these the right sizes? Or should `short` be even smaller (200 tokens)?
4. **Persistence** (session only, not cross-session): should `vibe_mode` carry over to next session if it's the same project?
