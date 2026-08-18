---
name: dark-memory
description: |
  Use for governance, memory, drift detection, and audit trail via
  dark-memory-mcp. Covers 52 canonical + 3 red-team tools across
  16 namespaces: session lifecycle, agent_memory CRUD+search, vibe-flow
  spec/artifact publish + drift, LLM-as-judge, delegation+mindset, research,
  observability, error observatory, governance, admin, and self-bootstrap.
  Use ONLY when the work touches dark-memory, dark-memory-mcp, agent_memory,
  vibe-flow, drift, constitution, governed, auditable, session, recall,
  research_recall, agent_memory_recall, or subagent delegation. Triggers on
  keywords: dark-memory, dark_memory_*, session_start, session_resume,
  session_close, vibe_publish, vibe_spec, vlp_handle_event, agent_memory_save,
  agent_memory_list, agent_memory_get, agent_memory_recall, agent_memory_delegate,
  drift_judge, drift_detected, resolve_drift, pipeline_status, judge, consensus,
  judgment_history, research_topic, research_recall, health_ping, memory_state,
  writes, anomalies, error_list, error_summary, error_resolve, mindset_apply,
  delegate_intent, subagent_register, project_create, active_policy,
  load_constitution, admin_migrate, admin_schema_status, admin_vacuum,
  agent_bootstrap, agent_recommend_companions, agent_detect_environment,
  embedder_setup_prompt, redteam_list_mods, "guardar en memoria", "qué sabemos",
  "crear spec", "publicar artifact", "delegar a subagente", "sesión dark-memory",
  "audit trail", "governance", "drift check", "Mem0", "FTS5".
target_version: 2.13.0
---

# dark-memory-mcp — Harness Skill

> **Targets dark-memory-mcp v2.13.0** — 52 canonical + 3 red-team tools, schema v25.
>
> **Self-update**: call `dark_memory_health_ping`. If `server.version > target_version`,
> the server was upgraded. Re-ingest via `dark_memory_agent_bootstrap(surface='system_prompt')`
> and check the latest GitHub release for an updated copy of this skill. If
> `server.version < target_version`, your binary is stale — rebuild from the
> repo tag that matches this skill.

---

## TL;DR — 8 rules that prevent 90% of friction

| # | DO | DON'T |
|---|---|---|
| 1 | **`session_start` before any write tool** | Call write tools without a session (`ErrFrameStaleTooFar`) |
| 2 | **`health_ping` first** — cheap, no side-effects | Assume the MCP is healthy after a restart |
| 3 | **`agent_memory_recall` or `research_recall` first**, then `webfetch` last | Jump to web search for facts you already stored |
| 4 | **`agent_memory_save` for persistent knowledge** (survives sessions). **`vibe_publish` for work-in-progress** (spec + judge + drift gate) | Mix them up: specs don't survive, memories don't get judged |
| 5 | **`consensus(n=3)` on high-stakes claims**, `judge` for routine | Ship debatable claims without a second opinion |
| 6 | **On `drift_detected`: fix and re-publish. On `needs_human`: STOP** | Ignore drift verdicts — they block the vibe loop |
| 7 | **`session_close(reason=clean)` at the end** | Leave sessions dangling (sweeper closes them, but loses context) |
| 8 | **Sub-agent spawn: `delegate_intent` for routing decisions, `mindset_apply` for prompt composition, `agent_memory_delegate` for context handoff** | Spawn sub-agents blind — they inherit no dark-memory context and their writes pollute your ContextRecap |

---

## 0. Decision tree — which tool when

| Need | Tool |
|---|---|
| Start tracking work | `session_start` → get `session_id` back |
| Store a fact, decision, finding, or todo for later | `agent_memory_save(kind=...)` |
| "What do we know about X?" | `agent_memory_recall(query="X")` (FTS5-BM25 ranked) |
| Browse all memories by type | `agent_memory_list(scope=project, kind=...)` |
| Research something new (CVE, paper, domain, IP) | `research_topic(query=...)`, then `research_recall` to retrieve |
| Create a governed spec with tasks | `vibe_spec(vibe_case=C1..C7, tasks=[...])` |
| Publish an artifact under a spec + get drift verdict | `vibe_publish(artifact={...}, spec={vibe_case:...})` |
| Self-check a claim | `judge(eval_type=drift_judge|compliance_check|...)` |
| N-shot verification | `consensus(eval_type=..., n=3)` |
| "Should I delegate this work to a sub-agent?" — decide + plan + prompt | `delegate_intent(vibe_case=C7, task_description="...")` |
| "Just give me a sub-agent prompt for X" — compose only | `mindset_apply(vibe_case=C1..C7, task_description="...")` |
| Hand off dark-memory context to a sub-agent | `agent_memory_delegate` → inject `delegation_context` into sub-agent prompt |
| Isolate sub-agent writes (C2) | `subagent_register` before spawn, `subagent_unregister` after |
| Diagnose "the MCP seems broken" | `health_ping` → `error_summary` → `error_list` |
| Check drift status of a published artifact | `pipeline_status(artifact_id=N)` |
| Accept/reject a drift report | `resolve_drift(drift_id=N, decision=accept|reject)` |
| See what changed recently | `writes(session_id=...)` or `recall(scope=session)` |
| Check schema version / run migrations | `admin_schema_status` → `admin_migrate` |
| Create a new tenant/project | `project_create(project_id="...", display_name="...")` |

---

## 1. Tool taxonomy — 57 canonical tools grouped by purpose

> **Frozen 2026-08-18 (spec 1270, dark-mem v2.15.2).** Any addition,
> removal, or rename of a canonical tool requires an ADR + minor version
> bump. Extras (env-gated, e.g. red-team) are NOT frozen here. See
> `internal/tools/registry.go` for the freeze marker.

### 1.1 Session lifecycle (7 tools)

| Tool | Purpose |
|---|---|
| `session_start(operator, project_id)` | **ALWAYS first.** Returns `session_id` + `context_recap` (pinned memories + open todos). Pass `cold_start=true` to skip recap. Auto-emits `EventSessionStart` into the VLP (v2.13.0). |
| `session_resume(session_id)` | Re-attach to an existing session after restart/crash. |
| `session_heartbeat(session_id)` | **New in v2.13.0.** Refresh `last_heartbeat_at` so the sweeper doesn't demote an active session during long reasoning pauses. Call before stretches > 15m. |
| `session_status(session_id)` | Check liveness: `status`, `closing_soon`, `seconds_until_close`. |
| `session_close(session_id)` | End cleanly. Default `close_reason=clean`. |
| `session_recover(operator, lookback?)` | **New in v2.13.0.** Find the most-recent `closed_aborted` session (crashed harness, INV-8). |
| `session_resurrect(original_session_id?, ...)` | **New in v2.13.0.** Create a new session inheriting the aborted one's constitution + mods. |

### 1.2 Agent memory — persistent cross-session knowledge (8 tools)

| Tool | Cost | When |
|---|---|---|
| **`agent_memory_recall(query, kind?, memory_type?`** | O(log n) FTS5 | **PRIMARY discovery.** "What do we know about X?" Ranked BM25. Max 50 results. |
| `agent_memory_save(kind, content, title?, tags?, pinned?, bind_session?)` | 1 INSERT | Store a fact, decision, finding, todo, note, link, or context. `bind_session=false` (default) = survives session close. |
| `agent_memory_list(scope, kind?, tag?, pinned_only?, limit?)` | O(n) scan | Browse ALL rows with filters. Default `scope=current`, `limit=50`. |
| `agent_memory_get(id)` | O(1) PK | Fetch one row by id. Cross-project → `ErrCrossProjectAccess`. |
| `agent_memory_update(id, content?, title?, tags?, pinned?)` | 1 UPDATE | Edit mutable fields. Operator + project are immutable. |
| `agent_memory_archive(id)` | soft-delete | Recoverable with `list(include_archived=true)`. |
| `agent_memory_delegate(operator, subagent_id, task_description)` | Register + build context | **Sub-agent handoff.** Returns `delegation_context` markdown to inject into sub-agent task prompt. Gated by `DARK_MEMORY_V280=1`. |
| `agent_memory_entities(id)` | read-only | Extracted entities for one row. Returns `{entities: [...]}` or nil. |

**Kind taxonomy**: `note`, `observation`, `decision`, `finding`, `todo`, `link`, `context`.
**Memory type** (Mem0): `episodic`, `semantic`, `procedural`.
**Recall vs List**: recall for search, list for browsing. NEVER use `list(limit=200)` and filter manually.

### 1.3 Vibe-flow — specs, artifacts, drift (5 tools)

| Tool | Purpose |
|---|---|
| `vibe_spec(vibe_case, tasks, session_id?)` | Create a governed spec. Tasks are validated (unique ids, no cycles). Auto-creates `kind=todo` rows per task. Auto-emits `EventVibePublish` into the VLP (v2.13.0). |
| `vibe_publish(spec, artifact, session_id?, auto_drift_check?)` | Publish artifact under spec → runs drift_judge automatically. On `aligned`, auto-saves a `kind=decision` row. Auto-emits `EventVibePublish → EventArtifactLog → EventDriftLog` (v2.13.0). |
| `pipeline_status(artifact_id)` | Get latest drift report for an artifact. Returns nil if none. |
| `resolve_drift(drift_id, decision, operator_id)` | Operator gate: `accept` (artifact correct) or `reject` (artifact wrong). |
| `vlp_handle_event(session_id, event, verdict?)` | Drive VLP state machine: `session_start → vibe_publish → artifact_log → delegate → drift_log → complete`. **v2.13.0**: `event="delegate"` is valid now; orchestrators auto-drive the rest — call this tool only for delegation transitions or manual overrides. |

**Drift verdicts**: `aligned` (ship), `drift_detected` (fix + re-publish), `needs_human` (STOP, surface).

### 1.4 LLM-as-judge (3 tools)

| Tool | Purpose |
|---|---|
| `judge(eval_type, content, target_id?, model?)` | Single-shot verdict. Eval types: `drift_judge`, `brand_match`, `compliance_check`, `pii_detect`, `prompt_injection_scan`, `grounding_check`, `mindset_compose`, `mindset_quality`. |
| `consensus(eval_type, content, n?, model?)` | N-shot (default 3, max 7). Returns modal verdict + confidence interval. |
| `judgment_history(eval_type?, target_id?, limit?)` | List past verdicts. Read-only. |

### 1.5 Context projection (4 tools)

| Tool | Purpose |
|---|---|
| `artifact_context(artifact_id)` | Projection of an artifact: id, type, spec, brand, jurisdiction, disclosure, validation status. |
| `spec_context(spec_id)` | Projection of a spec: id, case, constitution, intent (truncated), task count. |
| `session_context(session_id)` | Projection of a session: operator, project, status, timestamps. |
| `recall(scope, project_id?, session_id?, since_token?)` | Scoped context replay: returns atomic frames + delta write_audit rows since `since_token`. |

### 1.6 Research (3 tools)

| Tool | Purpose |
|---|---|
| `research_topic(query, intent?, max_items?, thread_id?)` | Fresh OSINT research. Intent: `web|academic|code|cve|domain|dns|cert|ip|threat|email|dark|geo|news`. |
| `research_recall(query, max_tokens?, min_confidence?)` | Retrieve previously stored research items. 5-bucket economy pipeline. |
| `research_resume_thread(thread_id, query, max_items?)` | Continue a multi-turn research thread. |

**Rule**: `research_recall` before `research_topic`. `research_topic` before `webfetch`.

### 1.7 Delegation + mindset (4 tools) — v2.13.0 guidance

**v2.13.0: LLM wiring = provider catalog.** The harness (opencode / Claude Desktop) provides the LLM to dark-memory via `WithLLMSelector` at boot time — the PRIMARY mechanism, uses the same cloud model as your agent. If the harness doesn't inject, dark-memory auto-detects from the FIRST catalog `*_API_KEY` env var in order: `DARK_JUDGE_PROVIDER` override → ANTHROPIC → OPENAI → GEMINI → DEEPSEEK → MINIMAX → MOONSHOT → ZAI → DASHSCOPE → daemon → legacy. **You do NOT need to set API keys** — the harness already has them. The `judge`, `consensus`, `mindset_apply`, and `delegate_intent` tools all use the harness-injected LLM automatically.

**When to use each delegation tool:**

| Scenario | Tool |
|---|---|
| "Should I delegate this task, and if so how?" — routing decision + plan | `delegate_intent(vibe_case=C1..C7, task_description=...)` |
| "Compose a sub-agent system prompt for this task" — prompt generation | `mindset_apply(vibe_case, task_description)` |
| "Give a sub-agent access to my dark-memory context" — curated context handoff | `agent_memory_delegate(operator, subagent_id, task_description)` |
| "Spawn a sub-agent that should work in isolation" | Chain all three: `delegate_intent` → `mindset_apply` → `agent_memory_delegate` → `subagent_register` → spawn with `delegation_context` |

| Tool | Purpose |
|---|---|
| `delegate_intent(vibe_case, task_description, operator?)` | Wave 5C: DECIDE → PLAN → MIND → CURATE pipeline. Returns ready-to-spawn subtask graph with `system_prompt`, `tools_recommended`, `model_recommended`, and `delegation_context` per subtask. Gated by `DARK_MEMORY_V280=1`. |
| `mindset_apply(vibe_case, task_description, model_floor?)` | Procedurally compose + LLM-validate a sub-agent system prompt. Cached 1h. Returns `system_prompt` + `tools_recommended` + `model_recommended`. Gated by `DARK_MEMORY_V280=1`. |
| `agent_memory_delegate(operator, subagent_id, task_description)` | Sub-agent context handoff. Returns `delegation_context` markdown (curated pinned memories + open todos) for injection into sub-agent task prompt. See §1.2. |
| `subagent_register(operator, subagent_id, ttl_seconds?)` / `subagent_unregister` | C2 memory isolation. Sub-agent writes carry `subagent_id`, never leak into principal's ContextRecap. |

**Full delegation flow** (v2.13.0 pattern — VLP delegate event wired):
```
1. delegate_intent(vibe_case="C7", task_description="review SQL injection in auth.go")
   → decide DELEGATE → plan 1 subtask "bundle"
   → mindset_apply composes system_prompt via harness LLM
   → agent_memory_delegate builds curated context → C2 binding registered
   → returns per-subtask: {id, system_prompt, delegation_context, subagent_id, tools_recommended}

2. Fire vlp_handle_event(event="delegate") — moves the VLP loop
   spec_active → delegating (v2.13.0 wire).

3. Pull the returned fields into your sub-agent spawn:
   - system_prompt → the sub-agent's role + goal + backstory
   - delegation_context → inject AFTER your task description so the sub-agent sees dark-memory context
   - subagent_id → pass to subagent_register (already done by delegate_intent) and track for cleanup

4. After sub-agent finishes:
   - agent_memory_list(scope=agent, agent_id=subagent_id) → review findings
   - subagent_unregister(operator="dark-agent", subagent_id=subagent_id)

5. vibe_publish the sub-agent's output as an artifact under a spec —
   the orchestrator auto-emits EventArtifactLog + EventDriftLog.
```

### 1.8 Observability + Error Observatory (8 tools)

| Tool | Purpose |
|---|---|
| `health_ping` | **FIRST call.** Returns server version, schema, uptime, tool count, error summary. No audit side-effects. |
| `memory_state` | Runtime snapshot: driver, schema, per-table counts, active project. |
| `writes(actor?, session_id?, limit?)` | Recent write_audit rows. INV-1 audit trail. |
| `anomalies(kind?, limit?)` | Fatal clusters + gate refusals. Kind: `fatal|gate`. |
| `error_list(domain?, severity?, session_id?, tool_name?, since?, limit?)` | Durable error clusters. Domain: `store|llm|gate|validation|network|sweep|unknown`. |
| `error_get(id)` | One error cluster by id. |
| `error_summary(hours?)` | Aggregate: total, unresolved, by domain+severity, top-5 recurring. Cross-project. |
| `error_resolve(id, note?)` | Mark error cluster as resolved (backlog triage). |

### 1.9 Governance + admin (5 tools)

| Tool | Purpose |
|---|---|
| `active_policy` | Active policy snapshot: constitution id+ver+sha, drift status, active mods, canary. |
| `load_constitution(constitution_id, version?)` | Full constitution row (label, source, parsed JSON). |
| `admin_schema_status` | Current schema version, applied migrations, table list. |
| `admin_migrate` | Run pending schema migrations. |
| `admin_vacuum(days_old?, dry_run?, tables?)` | GC old rows. SQLite: reclaims space. Postgres: no-op. |

### 1.10 Bootstrap (4 tools)

| Tool | Purpose |
|---|---|
| `agent_bootstrap(surface, target?, force_embedded?)` | Returns markdown content of any resource: `system_prompt|compatibility_matrix|install_guide|companion|all`. |
| `agent_recommend_companions` | Structured recommendations for missing companion MCPs (dark-research, [FUTURE-MCP-N]). |
| `agent_detect_environment` | Runtime inference: client name/version, transport, negotiated capabilities. |
| `embedder_setup_prompt` | Embedder consent prompt. Single call per project lifecycle. |

### 1.11 Red-team — defensive security research (3 tools, env-gated: `DARK_REDTEAM=armed`)

> **Scope**: defensive-only. Dark-web threat intelligence, CVE validation against your own
> infrastructure, and adversarial prompt testing of your own models. Never for attacking
> third parties. All mods are `risk_class=research-only`, `target_scope=public_internet`.
> Every attempt is audited with `constitution_id=redteam-research`.

| Tool | Purpose |
|---|---|
| `redteam_list_mods` | List installed research mods (risk_class, target_scope, capability_count). |
| `redteam_get_prompts(mod_id, family?, severity_min?)` | Read payloads from a mod's knowledge files for study. |
| `redteam_log_attempt(mod_id, prompt_id, target_model, observed_label)` | Log a defensive experiment. Audited. Labels: `REFUSAL|PARTIAL_COMPLIANCE|FULL_COMPLIANCE`. |

### 1.12 Project (1 tool)

| Tool | Purpose |
|---|---|
| `project_create(project_id, display_name, constitution_id?, default_agent_id?)` | Create a tenant. Idempotent. `default` project is seeded on open. |

---

## 2. Anti-patterns — 10 mistakes that waste sessions

1. **Writing without a session.** `session_start` before any write tool. The first write after a fresh boot fails with `ErrFrameStaleTooFar` because the resolver cache is stale — this is expected; `session_start` flushes it (v2.1.3 fix).

2. **Calling `session_start` every turn.** One session per unit of work. Use `session_resume` to re-attach after a restart or long pause.

3. **Using `agent_memory_list(limit=200)` for discovery.** `list` is O(n) table scan. `agent_memory_recall(query=...)` is O(log n) FTS5-BM25. Use recall first, list only for "show me all todos."

4. **Mixing `agent_memory_save` and `vibe_publish` for the same content.** Memories are persistent (cross-session). Specs are transient (done when `resolve_drift(accept)` lands). Never use `vibe_publish` for notes you need next week.

5. **Ignoring drift verdicts.** `drift_detected` means the judge found semantic misalignment. Fix the artifact text and re-publish. `needs_human` means the judge can't decide — surface to operator, don't guess.

6. **Trusting `webfetch` over local recall.** `research_recall` + `agent_memory_recall` are cheaper, faster, and already vetted. Only fall through to web when recall returns empty or low-confidence.

7. **Not binding `session_id` to writes.** Every `vibe_spec`, `vibe_publish`, and `agent_memory_save` (with `bind_session=true`) should carry the active `session_id`. Without it, audit trail is fragmented.

8. **Closing sessions with `close_reason=aborted` habitually.** The default is `clean`. Use `aborted` only when the session actually crashed. The sweeper marks crashed sessions differently for resurrection.

9. **Forgetting C2 isolation on sub-agent spawn.** Without `agent_memory_delegate` + `subagent_register`, the sub-agent's writes carry the principal's `agent_id` and pollute ContextRecap. Always register before spawn. Use `delegate_intent` — it chains all three steps (MIND → CURATE with C2 binding) automatically.

10. **Skipping `health_ping` on startup.** The MCP might be a stale binary, the DB might be locked, or the schema might need migration. `health_ping` catches all three in 50ms.

11. **Worrying about LLM API keys.** The harness (opencode) injects its LLM into dark-memory at boot time. You do NOT need to set `ANTHROPIC_API_KEY` or similar env vars — judge, consensus, mindset_apply, and delegate_intent all use the harness-provided cloud LLM automatically. If a LLM-using tool returns `ErrNoLLMAvailable`, the harness's injection is not configured — surface to operator, don't try to set keys yourself.

---

## 3. Common workflows

### 3.1 Start a governed unit of work

```
1. health_ping                         → confirm server alive, version matches
2. session_start(operator="...", project_id="default")
                                       → get session_id + context_recap
3. agent_memory_recall(query="<topic>") → what do we already know?
4. [work: research, code, write, judge]
5. agent_memory_list(scope=project, kind=todo)
                                       → surface unfinished todos
6. session_close(session_id)           → close cleanly
```

### 3.2 Publish a governed spec with drift check

```
1. vibe_spec(vibe_case="C1", tasks=[...], session_id=...)
                                       → spec_id returned
2. [do the work the spec describes]
3. vibe_publish(spec={vibe_case:"C1"}, artifact={text:"<work output>", artifact_url:"...", artifact_type:"text"}, session_id=...)
                                       → returns verdict (aligned|drift_detected|needs_human) + drift_id
4. IF drift_detected: fix artifact text → re-publish
5. IF needs_human: STOP, tell operator
6. IF aligned: resolve_drift(drift_id, "accept") [optional — aligned artifacts auto-resolve]
```

### 3.3 Delegate to a sub-agent (v2.13.0 — delegate_intent chains all steps)

```
1. delegate_intent(vibe_case="C7", task_description="review auth.go for SQL injection")
   → DECIDE: DELEGATE (1 bundle subtask)
   → PLAN: 1 subtask "bundle" with no dependencies
   → MIND: mindset_apply composes system_prompt via harness LLM
   → CURATE: agent_memory_delegate builds curated context + C2 binding
   → returns per-subtask: {id, system_prompt, delegation_context, subagent_id, tools_recommended, model_recommended}

2. Fire vlp_handle_event(event="delegate") — moves spec_active → delegating (v2.13.0).

3. Spawn sub-agent with:
   - system_prompt  → role + goal + backstory (from step 1)
   - delegation_context → inject AFTER your task description (curated memories + todos)
   - subagent_id → already registered via C2; use for tracking and cleanup

4. [sub-agent works, its writes carry subagent_id, not your agent_id]

5. agent_memory_list(scope=agent, agent_id=subagent_id)
   → review sub-agent's output

6. subagent_unregister(operator="dark-agent", subagent_id=subagent_id)
```

**When to use `delegate_intent` (all-in-one) vs individual tools:**

- **`delegate_intent`**: "should I delegate this, and if so, give me everything I need to spawn." Best for one-shot delegation decisions where the routing (DECIDE) + plan shape (PLAN) + prompt composition (MIND) + context curation (CURATE) are needed together. Typical use: "review this code", "research this topic", "fix this bug".

- **`mindset_apply` alone**: "just give me a system prompt for this task." Use when you already know you're spawning, already have context, and just need the prompt. Cached 1h for repeated calls with the same parameters.

- **`agent_memory_delegate` alone**: "give a sub-agent access to my dark-memory context without composing a new prompt." Use when the sub-agent already has a prompt but needs curated dark-memory context injected.

### 3.4 Research something new

```
1. research_recall(query="CVE-2024-1234") → any cached results?
2. IF empty: research_topic(query="CVE-2024-1234", intent="cve")
3. IF cross-reference needed: webfetch(url="https://nvd.nist.gov/vuln/detail/CVE-2024-1234")
4. agent_memory_save(kind="finding", content="<synthesized result>", tags="cve,...")
```

### 3.5 Diagnose errors

```
1. health_ping → error_summary section — any red flags?
2. error_summary(hours=24) → aggregate view
3. error_list(domain="gate", severity="fatal", limit=20) → drill down
4. error_get(id=N) → full cluster details
5. error_resolve(id=N, note="root cause: ...") → triage
6. If a judge/LLM call failed with 401: check ANTHROPIC_API_KEY +
   SDD_LLM_BASE_URL in opencode.jsonc. A 401 almost always means the
   key/endpoint mismatch (e.g. api.minimaxi.com vs api.minimax.io),
   not a code bug. Verify with curl before touching the Go code.
```

### 3.6 Judge delegation (spec 874 — use when content is large)

**When to delegate** (count-tokens.py decides, but the shortcut):
- Content ≤ ~8K tokens → single-shot `judge` is faster AND more accurate.
- Content > ~8K tokens OR > 50% of model context → delegate.

**The pipeline** lives at `~/.config/dark-agent/judge-delegation/`:
```
count-tokens.py → chunk-content.py → delegate-judge.sh (parallel LLM) → aggregate-verdicts.py
```

**Usage from dark-agent**:
```bash
# Decide first
python ~/.config/dark-agent/judge-delegation/count-tokens.py --file artifact.md
# exit 0 → single-shot judge (dark_memory_judge)
# exit 2 → delegated
ANTHROPIC_API_KEY=... bash ~/.config/dark-agent/judge-delegation/delegate-judge.sh \
  --file artifact.md --eval-type drift_judge --max-chunk-tokens 4000 --parallel 4 --json
```

**Known working config (2026-08-08)**:
- `SDD_LLM_BASE_URL=https://api.minimax.io/anthropic` (NOT `.com` — that 401s)
- Model: MiniMax-M3
- The delegate-judge verdict schema matches dark-memory's `parseDriftVerdict`
  (aligned | drift_detected | needs_human) so results are interchangeable.

### 3.7 Judge consistency + vibe-case rubrics (spec 878 — use for technical/code artifacts)

**Two defects fixed (2026-08-08):**
1. **Contradiction verdict↔reasoning**: the LLM could say `drift_detected` while
   its reasoning said "no drift". Fix (G-Eval): force the judge to evaluate EACH
   rubric criterion with quoted evidence, then verdict = LOGICAL CONCLUSION of
   the checklist. Post-check (`detect_contradiction`) overrides to `needs_human`.
2. **No technical evaluation for code**: the judge never knew vibe_case.
   Fix: per-(vibe_case, eval_type) rubrics. C1(code) gets CORRECTNESS, SECURITY,
   MAINTAINABILITY, SPEC_CONFORMANCE — objective checks, not vibes.

**Tool to use** — `vibe-judge.py` (works today, no restart):
```bash
# C1 code artifact — technical rubric
ANTHROPIC_API_KEY=... python ~/.config/dark-agent/judge-delegation/vibe-judge.py \
  --file auth.go --vibe-case C1 --eval-type drift_judge

# C2 text — communication rubric + self-consistency (3 samples)
ANTHROPIC_API_KEY=... python ~/.config/dark-agent/judge-delegation/vibe-judge.py \
  --file copy.md --vibe-case C2 --eval-type brand_match --consensus 3
```

**MCP-native (v2.13.0, provider = harness LLM):**
```json
dark_memory_judge(eval_type="drift_judge", content="...", vibe_case="C1")
dark_memory_consensus(eval_type="drift_judge", content="...", vibe_case="C1", n=3)
```
`vibe_case` enum: C1=code, C2=text, C3=image, C4=video, C5=audio, C6=multimodal, C7=mixed.
Empty vibe_case = legacy generic prompt (retrocompat).

**Decision rule**: if the artifact is code or technical (C1), or you suspect the
LLM might contradict itself (large content, borderline verdict), use
`vibe-judge.py` (or `vibe_case` on the native tool). It returns
`criteria[]` + `criteria_failed[]` + `consistency` — the checklist grounds the verdict.
Full design: `dark-memory-mcp/vibe-flow/main/JUDGE_RUBRICS.md`.

**Windows gotchas** (encoded in the scripts):
- Never pass large content via `--text` (32K argv limit) — use `--file`/stdin.
- MSYS paths need `cygpath -w` before native Python touches them.
- Full design: `dark-memory-mcp/vibe-flow/main/JUDGE_DELEGATION.md`

---

## 4. Companion ecosystem

| Companion | When to install |
|---|---|
| **dark-research-mcp** | You need web/academic/CVE/DNS/cert/IP/threat/geo/news OSINT and your harness lacks `WebFetch`. Install: `npm install -g @opitacode/dark-research-mcp`. |
| **[FUTURE-MCP-N]-mcp** | You need a real Chromium browser for JS-gated pages, CAPTCHAs, or interactive flows. `webfetch` alone can't handle these. |

Call `dark_memory_agent_recommend_companions()` for a structured recommendation based on your harness.

---

## 5. Version awareness — don't let this skill go stale

This skill targets **dark-memory-mcp v2.13.0**. dark-memory releases frequently (v2.8.0 → v2.13.0 in ~2 weeks). To stay current:

1. **Every session start**: call `dark_memory_health_ping`. Check `server.version`.
2. **If `server.version > target_version`**: the server was upgraded since this skill was written.
   - Call `dark_memory_agent_bootstrap(surface='system_prompt')` to ingest the latest operating manual.
   - Check https://github.com/Opita-Code/dark-memory-mcp/releases for an updated `SKILL.md` in the release assets.
   - New tools may have been added; old tools may have changed behavior.
3. **If `server.version < target_version`**: your binary is older than this skill. Some tools/behaviors described here may not exist yet. Rebuild from the repo tag.
4. **If versions match**: this skill is current. Proceed.

---

## 6. Where things live (for debugging dark-memory itself)

| What | Where |
|---|---|
| dark-memory-mcp repo | `C:\Users\Nico\Documents\dark-memory-mcp` (main branch, tag `v2.13.0`) |
| MCP binary (active) | `C:\Users\Nico\Documents\dark-memory-mcp\bin\dark-mem-mcp.exe` (30M, buildVersion=2.13.0) |
| opencode.jsonc entry | `C:\Users\Nico\.config\opencode\opencode.jsonc` → `mcp.dark-memory` |
| DB (SQLite) | `C:\Users\Nico\AppData\Local\dark-agents\dark.db` |
| This skill | `C:\Users\Nico\.config\opencode\skills\dark-memory\SKILL.md` |
| SYSTEM_PROMPT.md (embedded) | Read via `dark_memory_agent_bootstrap(surface='system_prompt')` |
| GitHub releases | https://github.com/Opita-Code/dark-memory-mcp/releases |
| npm package | `@opitacode/dark-memory-mcp` |
