---
name: dark-memory
description: |
  Use for governance, memory, drift detection, and audit trail via
  dark-memory-mcp. Covers 52 tools (49 canonical + 3 red-team extras) across
  11 namespaces: session lifecycle, agent_memory CRUD+search, vibe-flow
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
target_version: 2.11.0-alpha
---

# dark-memory-mcp — Harness Skill

> **Targets dark-memory-mcp v2.11.0-alpha** — 49 canonical + 3 red-team tools, schema v25.
>
> **Self-update**: call `dark_memory_health_ping`. If `server.version > target_version`,
> the server was upgraded. Re-ingest via `dark_memory_agent_bootstrap(surface='system_prompt')`
> and check the latest GitHub release for an updated copy of this skill. If
> `server.version < target_version`, your binary is stale — rebuild from the
> repo tag that matches this skill.

---

## TL;DR — 7 rules that prevent 90% of friction

| # | DO | DON'T |
|---|---|---|
| 1 | **`session_start` before any write tool** | Call write tools without a session (`ErrFrameStaleTooFar`) |
| 2 | **`health_ping` first** — cheap, no side-effects | Assume the MCP is healthy after a restart |
| 3 | **`agent_memory_recall` or `research_recall` first**, then `webfetch` last | Jump to web search for facts you already stored |
| 4 | **`agent_memory_save` for persistent knowledge** (survives sessions). **`vibe_publish` for work-in-progress** (spec + judge + drift gate) | Mix them up: specs don't survive, memories don't get judged |
| 5 | **`consensus(n=3)` on high-stakes claims**, `judge` for routine | Ship debatable claims without a second opinion |
| 6 | **On `drift_detected`: fix and re-publish. On `needs_human`: STOP** | Ignore drift verdicts — they block the vibe loop |
| 7 | **`session_close(reason=clean)` at the end** | Leave sessions dangling (sweeper closes them, but loses context) |

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
| Delegate work to a sub-agent | `agent_memory_delegate` → inject `delegation_context` into sub-agent prompt |
| Compose a sub-agent system prompt | `mindset_apply(vibe_case, task_description)` |
| Diagnose "the MCP seems broken" | `health_ping` → `error_summary` → `error_list` |
| Check drift status of a published artifact | `pipeline_status(artifact_id=N)` |
| Accept/reject a drift report | `resolve_drift(drift_id=N, decision=accept|reject)` |
| See what changed recently | `writes(session_id=...)` or `recall(scope=session)` |
| Check schema version / run migrations | `admin_schema_status` → `admin_migrate` |
| Create a new tenant/project | `project_create(project_id="...", display_name="...")` |

---

## 1. Tool taxonomy — 52 tools grouped by purpose

### 1.1 Session lifecycle (4 tools)

| Tool | Purpose |
|---|---|
| `session_start(operator, project_id)` | **ALWAYS first.** Returns `session_id` + `context_recap` (pinned memories + open todos). Pass `cold_start=true` to skip recap. |
| `session_resume(session_id)` | Re-attach to an existing session after restart/crash. |
| `session_status(session_id)` | Check liveness: `status`, `closing_soon`, `seconds_until_close`. |
| `session_close(session_id)` | End cleanly. Default `close_reason=clean`. |

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
| `vibe_spec(vibe_case, tasks, session_id?)` | Create a governed spec. Tasks are validated (unique ids, no cycles). Auto-creates `kind=todo` rows per task. |
| `vibe_publish(spec, artifact, session_id?, auto_drift_check?)` | Publish artifact under spec → runs drift_judge automatically. On `aligned`, auto-saves a `kind=decision` row. |
| `pipeline_status(artifact_id)` | Get latest drift report for an artifact. Returns nil if none. |
| `resolve_drift(drift_id, decision, operator_id)` | Operator gate: `accept` (artifact correct) or `reject` (artifact wrong). |
| `vlp_handle_event(session_id, event, verdict?)` | Drive VLP state machine: `session_start → vibe_publish → drift_log → complete`. |

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

### 1.7 Delegation + mindset (4 tools)

| Tool | Purpose |
|---|---|
| `mindset_apply(vibe_case, task_description, model_floor?)` | Procedurally compose + LLM-validate a sub-agent system prompt. Cached 1h. Gated by `DARK_MEMORY_V280=1`. |
| `delegate_intent(vibe_case, task_description, operator?)` | Wave 5C: DECIDE → PLAN → MIND → CURATE pipeline. Returns ready-to-spawn subtask graph. Gated by `DARK_MEMORY_V280=1`. |
| `agent_memory_delegate(operator, subagent_id, task_description)` | Sub-agent context handoff (see §1.2). |
| `subagent_register(operator, subagent_id, ttl_seconds?)` / `subagent_unregister` | C2 memory isolation. Sub-agent writes carry `subagent_id`, never leak into principal's ContextRecap. |

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

9. **Forgetting C2 isolation on sub-agent spawn.** Without `agent_memory_delegate` + `subagent_register`, the sub-agent's writes carry the principal's `agent_id` and pollute ContextRecap. Always register before spawn.

10. **Skipping `health_ping` on startup.** The MCP might be a stale binary, the DB might be locked, or the schema might need migration. `health_ping` catches all three in 50ms.

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

### 3.3 Delegate to a sub-agent (v2.9.3+)

```
1. subagent_id = generate_uuid()
2. agent_memory_delegate(operator="dark-agent", subagent_id=subagent_id, task_description="review this code for SQL injection")
                                       → delegation_context markdown
3. Spawn sub-agent with delegation_context injected into its task prompt
4. [sub-agent works, its writes carry subagent_id, not your agent_id]
5. agent_memory_list(scope=agent, agent_id=subagent_id)
                                       → review sub-agent's output
6. subagent_unregister(operator="dark-agent", subagent_id=subagent_id)
```

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
```

---

## 4. Companion ecosystem

| Companion | When to install |
|---|---|
| **dark-research-mcp** | You need web/academic/CVE/DNS/cert/IP/threat/geo/news OSINT and your harness lacks `WebFetch`. Install: `npm install -g @opitacode/dark-research-mcp`. |
| **[FUTURE-MCP-N]-mcp** | You need a real Chromium browser for JS-gated pages, CAPTCHAs, or interactive flows. `webfetch` alone can't handle these. |

Call `dark_memory_agent_recommend_companions()` for a structured recommendation based on your harness.

---

## 5. Version awareness — don't let this skill go stale

This skill targets **dark-memory-mcp v2.11.0-alpha**. dark-memory releases frequently (v2.8.0 → v2.11.0 in ~4 days). To stay current:

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
| dark-memory-mcp repo | `C:\Users\Nico\Documents\dark-memory-mcp` (working branch `v2.8.0-alpha-dev`) |
| MCP binary (active) | `C:\Users\Nico\Documents\dark-memory-mcp\bin\dark-mem-mcp-v2.11.0-alpha.exe` |
| opencode.jsonc entry | `C:\Users\Nico\.config\opencode\opencode.jsonc` → `mcp.dark-memory` |
| DB (SQLite) | `C:\Users\Nico\AppData\Local\dark-agents\dark.db` |
| This skill | `C:\Users\Nico\.config\opencode\skills\dark-memory\SKILL.md` |
| SYSTEM_PROMPT.md (embedded) | Read via `dark_memory_agent_bootstrap(surface='system_prompt')` |
| GitHub releases | https://github.com/Opita-Code/dark-memory-mcp/releases |
| npm package | `@opitacode/dark-memory-mcp` |
