# vibe-loop-git v0.2 — Cold-Start Bootstrap Rules

**Version**: 0.2.0  
**Loop**: 4  
**Status**: draft (pending drift_judge aligned)  
**Audience**: operator / harness implementer

---

## 1. Overview

`vibe-loop-git v0.2` is a mod that ships as a directory of JSON/markdown files at `mods/vibe-loop-git/core/`. Unlike server-side providers, the mod has **no runtime startup cost** — it is read at `vibe_publish` time, not at harness boot.

**Cold-start** for this mod means: the operator drops the mod directory into a fresh harness that has:

- no `agent_memory` rows
- no per-project config (`projects.config.json`)
- no operator history
- no NLI bindings beyond what dark-memory auto-mirrors

This document codifies how the mod works in that state. The short answer: **the mod files ARE the defaults**. Cold-start = use them verbatim.

The principles below follow Letta's hierarchical configuration model (global → project → runtime) and Mem0's managed-memory pattern (graceful degradation when state is empty), and align with dark-memory's own `agentbootstrap` + VLP invariants.

---

## 2. Bootstrap Invariants (Inv-1..Inv-5)

These invariants MUST hold for the mod to be considered operational.

### Inv-1: VLIP `session_start` is mandatory

The very first `vibe_publish` call requires a valid `session_id`. The mod itself does not enforce this — `vibe_publish` does, via the VLP state machine (`internal/vlp/usecase.go:20, 133`):

> "The first event for a session_id must be EventSessionStart; otherwise vlp returns store.ErrInvalidArgument"

Cold-start implication — the operator MUST begin every session with:

```bash
dark_memory_session_start(operator=<handle>, project_id=<namespace>)
```

### Inv-2: No required project config

The mod works with **zero** `projects.config.json` entries. Every JSON file in `mods/vibe-loop-git/core/` ships with conservative defaults:

| File | Conservative default |
|---|---|
| `judge-mapping.json` | `tier_default = cheap` |
| `c7-subrouter.json` | `max_subtasks = 8`, `orchestrator_tier = medium` |
| `context-strategies.md` | per-tier strategy already baked in |
| `coldstart-rules.md` | this file |
| `latency-budget.json` | per-vibe_case budgets |
| `self-eval.md` | audit schema already defined |

If the operator never writes config, the mod still works.

### Inv-3: Graceful LLM absence

If no LLM is bound (no API key, no harness provider), `vibe_publish` + `drift_judge` MUST return `needs_human` with explicit reason. **No crash.** This is per row 2486 DESIGN_PIVOT (NLI = LLMJudge auto-mirror).

Wire-level log entry emitted at save time:

```
drift_judge: no LLM provider bound; returning needs_human
  recommended_action: dark_memory_llm_key_add(provider="anthropic", key="<your-key>")
```

### Inv-4: Graceful embedder absence

If no embedder is bound, recall falls back to **FTS5-only** (per T-202 backward compat: `Embedder=nil/KindNone → ftsScore fallback`). ProGraph BFS still works (it is FTS5-driven).

Wire-level behavior — recall returns results with `score_breakdown` showing:

```json
{
  "fts_score": 0.85,
  "graph_score": 0.42,
  "vector_score": null,
  "vector_disabled_reason": "no embedder bound"
}
```

### Inv-5: First-write wins precedence

As soon as the operator writes `projects.config.json` or passes `vibe_publish.spec.constitution.*` overrides, those take precedence over mod defaults. Mod defaults become the fallback rung.

This is the Letta pattern (global → project → runtime). The mod is the global layer.

---

## 3. Five-Step First-Use Path

The operator's first interaction with the mod. Each step is a single tool call. Steps are ordered — later steps depend on earlier.

### Step 1: `dark_memory_session_start`

Required VLP bootstrap. Always.

```bash
dark_memory_session_start({
  "operator": "nico",
  "project_id": "<your-project>"
})
```

Returns: `session_id` (e.g., `sess-fb4070d7b2e6ea03`).

### Step 2: `dark_memory_agent_recommend_companions`

Discovers what companion MCPs the harness should install. Zero-config companion install. May or may not have companion MCPs available — both states are graceful.

```bash
dark_memory_agent_recommend_companions()
```

Returns: `{ harness, recommended: [...], already_installed: [...] }`.

### Step 3: `dark_memory_agent_detect_environment`

Confirms what the harness can see (operator, project, transport, capabilities). Useful for debugging.

```bash
dark_memory_agent_detect_environment()
```

Returns: `{ client_info, capabilities, transport, negotiated }`.

### Step 4: `dark_memory_health_ping`

Confirms dark-memory is reachable + which schema version. Cheap. Idempotent. Safe to call in any state.

```bash
dark_memory_health_ping()
```

Returns: `{ server_identity, db_connectivity, schema_version, uptime, latency_ms }`.

### Step 5: `dark_memory_llm_key_add`

Binds the harness's primary LLM. **This is the moment vibe-loop-git becomes operational.** Per Loop 2 + row 2486, drift_judge auto-mirrors from this binding.

```bash
dark_memory_llm_key_add({
  "provider": "<anthropic|openai|deepseek|minimax|google>",
  "key": "<your-api-key>"
})
```

After step 5, `vibe_publish` + `drift_judge` work end-to-end.

---

## 4. Fallback Semantics

When a component is unavailable, the mod MUST degrade gracefully. This table is the single source of truth for fallback behavior.

| Component | Cold-start behavior | Audit row emitted |
|---|---|---|
| **LLM (drift_judge)** | `drift_judge` returns `needs_human("reason: no LLM provider bound")` | `kind=observation, tag=vibe-loop-git,cold-start,reason=no-llm` |
| **Embedder (recall)** | recall uses FTS5 + ProGraph only; `vector_score=null` | `kind=observation, tag=vibe-loop-git,cold-start,reason=no-embedder` |
| **agent_memory rows** | Recall returns empty array; vibe_publish artifact rejected with `reason: cold-start; no agent_memory yet` | `kind=observation, tag=vibe-loop-git,cold-start,reason=no-history` |
| **project_config** | Mod defaults applied (this file + judge-mapping + c7-subrouter) | none — silent fallback |
| **operator_override** | Mod defaults applied | none — silent fallback |
| **NLI binding** | Auto-mirrored from LLMJudge (per row 2486); if no LLM, drift_judge returns needs_human | none — derived |

The principle (per `summarize.go:79` test "no pinned rows" + `loadout_test.go:102` "empty operator"): **empty state returns explicit messages, never errors**.

---

## 5. Override Precedence Chain

The mod files are rung 3 of a 4-rung chain. Top wins. Cold-start = rung 3 (mod defaults), since no operator or project config exists yet.

| Rung | Source | When it takes precedence |
|---|---|---|
| 1 (top) | Operator explicit override (`vibe_publish.spec.constitution.*`) | When operator passes override keys in spec |
| 2 | Project-level config (`projects.config.json`) | When operator writes per-project config |
| 3 | Mod file defaults (`mods/vibe-loop-git/core/*`) | Always — fallback when rung 1+2 silent |
| 4 (bottom) | Harness default (opencode/mcp) | When mod + project + operator all silent |

**Cold-start = chain collapses to rung 3.**

Example — operator forces `force_tier = "medium"` for a single vibe_publish:

```json
{
  "vibe_publish": {
    "spec": {
      "constitution": {
        "force_tier": "medium"
      }
    }
  }
}
```

→ rung 1 wins → tier = medium (overrides the mod default `cheap`).

---

## 6. Mod Defaults Reference

The mod files are the single source of truth for defaults. Do NOT duplicate them elsewhere.

| File | Controls | Default value |
|---|---|---|
| `judge-mapping.json` | Which LLM judge per (provider, vibe_case, artifact_size) tier | `tier_default = cheap`; 5 providers × 4 tiers |
| `c7-subrouter.json` | C7 multi-artifact decomposition | `decomposition_strategy = orchestrator_workers_dynamic`, `max_subtasks = 8` |
| `context-strategies.md` | Per-tier context engineering pattern | per-tier token targets + retrieval hops |
| `latency-budget.json` | Per-vibe_case latency budgets | per-vibe_case p50/p95 targets |
| `self-eval.md` | Self-evaluation meta-loop | audit schema + scoring function |
| `coldstart-rules.md` | This file | the cold-start path |

When the operator upgrades the mod (e.g., from v0.2.0 to v0.3.0), the files are read fresh. The override chain re-evaluates. If operator wrote `projects.config.json` against v0.2.0 keys, those stay. New keys are added as defaults at rung 3.

---

## 7. Audit Row Schema (`cold_start_completed`)

At the operator's first interaction with vibe-loop-git (after step 5 in §3, or earlier if drift_judge runs), an observation row is emitted:

```json
{
  "kind": "observation",
  "operator": "<handle>",
  "tags": "vibe-loop-git,first_use,cold_start_completed",
  "title": "vibe-loop-git v0.2 cold-start completed",
  "content": {
    "event": "cold_start_completed",
    "mod_version": "v0.2.0",
    "files_loaded": [
      "judge-mapping.json",
      "c7-subrouter.json",
      "context-strategies.md",
      "coldstart-rules.md",
      "latency-budget.json",
      "self-eval.md"
    ],
    "defaults_applied": {
      "tier_default": "cheap",
      "decomposition_strategy": "orchestrator_workers_dynamic",
      "max_subtasks": 8,
      "orchestrator_tier": "medium",
      "voting_n": 3,
      "min_floor": "cheap"
    },
    "llm_bound": true,
    "embedder_bound": false,
    "session_id": "sess-..."
  }
}
```

This row is consumable by Loop 6 (self-evaluation). The row is emitted **once per session** to avoid spamming the audit trail.

---

## 8. Evidence

| ID | URL | Claim | Tier |
|---|---|---|---|
| Letta-Configuration | https://docs.letta.com/reference/settings | "Hierarchical configuration system with global and project-level settings." Pattern: global → project → runtime. Missing config = defaults. | 1 |
| Mem0-Guide-2026 | https://baeseokjae.github.io/posts/mem0-agent-memory-guide-2026 | "AI agents without persistent memory lose 80% of context between interactions — every session starts cold." Mitigations: managed memory layer + vector + graph + KV. | 1 |
| Anthropic-Building-Effective-Agents | https://www.anthropic.com/research/building-effective-agents | "Pre-flight checks, error handling, fallback patterns." | 1 |
| internal-agentbootstrap | `internal/agentbootstrap/` (fs.go, resources.go, render.go, clientinfo.go) | dark-memory's canonical self-bootstrap mechanism. 3 tools + 10 resources. | internal |
| internal-vlp-usecase | `internal/vlp/usecase.go:20, 133` | "First event for a session_id must be EventSessionStart" — bootstrap invariant. | internal |
| internal-loadout-test | `internal/v4alpha/transport/mcp/loadout_test.go:102` | "TestBuildLoadout_EmptyOperator — fresh operator, no rows" → loadout succeeds with empty tools. | internal |
| internal-summarize-test | `internal/v4alpha/session/summarize_test.go:79` | "writes, no pinned rows, no todos, no skills" → explicit "no pinned rows" message. Empty state = explicit, not silent. | internal |
| internal-embedder-setup-prompt | `dark_memory_embedder_setup_prompt` tool | Consent-gated single call per project lifecycle. Per-tenant embedder persistence. | internal |
| row-2486-DESIGN-PIVOT | internal://agent_memory/2486 | "NLI = LLMJudge auto-mirror. NO new API keys, NO new endpoints." | Inv-1 |

---

## 9. Operator Quick-Reference Card

```
Cold-start in 5 tool calls:

1. dark_memory_session_start(operator, project_id)
2. dark_memory_agent_recommend_companions()
3. dark_memory_agent_detect_environment()
4. dark_memory_health_ping
5. dark_memory_llm_key_add(provider, key)

After step 5 → vibe-loop-git is operational.
```

```
Fallback cheat sheet:

  no LLM         → drift_judge = needs_human
  no embedder    → recall = FTS5 + ProGraph only
  no memory      → recall = empty array
  no config      → mod defaults
  no override    → mod defaults
```

```
Override precedence (top wins):

  1. operator explicit (vibe_publish.spec.constitution.*)
  2. project config (projects.config.json)
  3. mod defaults (this file + judge-mapping + c7-subrouter)
  4. harness default
```

---

## 10. Related Files

- `mods/vibe-loop-git/core/judge-mapping.json` (Loop 2 SHIPPED, drift 1521 aligned)
- `mods/vibe-loop-git/core/c7-subrouter.json` (Loop 3 SHIPPED, drift 1524 aligned)
- `docs/specs/SPEC-vibe-loop-git-v0.2-loop-1.md` (Loop 1 SHIPPED, drift 1520 aligned)
- `mods/vibe-loop-git/core/context-strategies.md` (Loop 1 artifact provisional — pending Loop 6 close)
- `mods/vibe-loop-git/core/latency-budget.json` (Loop 5 PENDING)
- `mods/vibe-loop-git/core/self-eval.md` (Loop 6 PENDING)

---

**END OF FILE**