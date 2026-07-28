# Mindsets — procedural subagent system prompts

> v2.7.0-alpha. Phase 1 of dark-memory's delegation primitive. The
> principal delegates work to a subagent. dark-memory provides the
> mindset (the subagent's system prompt). The harness spawns.

---

## What this is

`dark_memory_mindset_apply` is the single new tool added in v2.7.0-alpha.
It procedurally composes a subagent **system prompt** given a
`(vibe_case, task_description)` pair, validates the prompt against
5 pass criteria via LLM-as-judge, and returns the result ready for the
harness to inject into its subagent spawn tool.

```
1. dark_memory_mindset_apply(vibe_case="C1", task_description="review auth code for SQL injection")
   ↓
   ┌──────────────────────────────────────────────────────────┐
   │ cache check: sha256(vibe_case + task + model_floor)       │
   │   hit  → return cached system_prompt (<50ms, 0 LLM calls) │
   │   miss → composition loop (max 3 iterations)              │
   └──────────────────────────────────────────────────────────┘
   ↓
2. Returns: { system_prompt, tools_recommended, model_recommended,
             judge_verdict, cache_hit, category_used, ... }
   ↓
3. Harness: Task(subagent_type="general-purpose",
                  prompt=<system_prompt>,
                  model=<model_recommended>,
                  tools=<tools_recommended>)
```

The harness (Claude Code, opencode, Cursor, etc.) does the spawn.
dark-memory provides the mindset — the role + goal + backstory +
constraints that shape the subagent's behavior.

---

## Why procedural, not pre-baked

Humans can't pre-bake every specialization. The LLM can synthesize
"senior security researcher focused on Rust async runtime vulnerabilities"
on the fly in 2 seconds; pre-baking every specialization is impossible.

The 5 meta-categories (`c1/security-review`, `c1/refactor`, `c2/docs-explain`,
`c2/marketing-copy`, `c3+generalist`) are **starting frames**, not pre-baked
content. The composition LLM uses the matching category's hint as
inspiration for the right kind of over-qualification, then writes the
actual prompt based on the task description.

---

## The 5 pass criteria (judge validation)

The validator LLM evaluates the proposed prompt against ALL of these:

| # | Criterion | Pass means | Fail example |
|---|---|---|---|
| 1 | **OVER-QUALIFIED** | Backstory names SPECIFIC expertise + track record | "experienced engineer", "you are helpful" |
| 2 | **TASK-APPROPRIATE** | Expertise matches (vibe_case, task) | C2 marketing task with security mindset |
| 3 | **CONSTRAINT-PRIMED** | ≥3 explicit "do not" statements covering drift risks | ["don't be bad"], fewer than 3 |
| 4 | **MINIMAL-TOOLS** | 3-6 tools, all relevant | 8+ tools including irrelevant ones |
| 5 | **NO-LEAKAGE** | No references to being procedurally generated | mentions "meta-prompt", "judge", "iteration" |

If the prompt fails any criterion, the validator returns
`{verdict: "drift_detected", criteria_failed: [...], reasoning: "..."}`.
The composition loop then retries with the feedback embedded in the
next iteration's prompt, up to `DARK_MINDSET_MAX_ITERATIONS` times.

---

## The cache

Cache lives in `agent_memory` rows:

```sql
SELECT * FROM agent_memory
WHERE kind = 'context'
  AND ',' || tags || ',' LIKE '%,mindset-cache,%'
  AND (expires_at IS NULL OR expires_at > now)
```

Cache key: `sha256(vibe_case || 0x00 || task_description || 0x00 || model_floor)[:32]`
(32 hex chars = 128 bits, collision-resistant).

Cache hit returns `<50ms` with 0 LLM calls.
Cache miss triggers the composition loop; the result is cached for
`DARK_MINDSET_CACHE_TTL` (default 3600s = 1h).

**Known gap** (audit-confirmed, deferred): there is no agent-memory
expiry sweeper yet. The orchestrator filters expired rows in Go on
every cache lookup, but expired rows physically persist until
out-of-band cleanup. Operators who want aggressive cleanup can run
`dark_memory_admin_vacuum` periodically.

---

## Env vars (all optional, all have sane defaults)

| Var | Default | Range | What |
|---|---|---|---|
| `DARK_MINDSET_MAX_ITERATIONS` | 3 | [1, 10] | Composition loop limit. 3 is enough for ~95% of tasks; raise to 5 for adversarial edge cases. |
| `DARK_MINDSET_TIMEOUT_MS` | 15000 | [100, 120000] | Total wall-clock for one `mindset_apply` call. Includes all LLM round-trips. |
| `DARK_MINDSET_CACHE_TTL` | 3600 | [60, 86400] | Cache row lifetime in seconds. Set higher if you trust your workload stability; lower if you want frequent re-composition. |

---

## What the harness sees

The harness's tool call looks like:

```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "method": "tools/call",
  "params": {
    "name": "dark_memory_mindset_apply",
    "arguments": {
      "vibe_case": "C1",
      "task_description": "review this authentication code for SQL injection and XSS",
      "model_floor": "sonnet"
    }
  }
}
```

The response:

```json
{
  "system_prompt": "You are a senior application security researcher with 12 years in OWASP Top 10 and CVE triage.\n\nFind security flaws in the supplied authentication code with adversarial depth.\n\nYou have 12 years experience in penetration testing for production web applications at scale (Shopify, Stripe, Cloudflare scale). You never invent CVE IDs; you only flag patterns that map to existing CVEs or known vulnerability classes. You think about the attacker's perspective: what would an adversary with 30 days and $5k actually do?\n\nConstraints:\n- do not invent CVE IDs\n- do not propose code changes outside the file under review\n- do not assume any framework's defenses without checking the actual code\n- do not recommend dependencies without checking the project's existing stack\n\nRecommended tools: Read, Grep, Glob, Bash",
  "source_mindset_id": 42,
  "match_score": 0,
  "tools_recommended": ["Read", "Grep", "Glob", "Bash"],
  "model_recommended": "sonnet",
  "composition_iterations": 1,
  "judge_verdict": {
    "verdict": "aligned",
    "confidence": 0.91,
    "reasoning": "All 5 criteria pass; over-qualification specific to OWASP/CVE domain; 4 constraints cover the right drift risks for code review."
  },
  "cache_hit": false,
  "category_used": "c1/security-review"
}
```

Then the harness spawns:

```
Task(
  subagent_type = "general-purpose",
  prompt = <system_prompt>,
  model = "sonnet",
  tools = ["Read", "Grep", "Glob", "Bash"]
)
```

---

## Failure modes (handled explicitly)

| Failure | Behavior |
|---|---|
| Composition LLM returns invalid JSON | Best-effort extract via `extractFirstJSONObject`; if extraction fails, return error |
| Judge LLM errors (network, rate limit, etc.) | Return best attempt with `judge_verdict={verdict: "errored", reasoning: <err msg>}` |
| `DARK_MINDSET_MAX_ITERATIONS` exhausted | Return best attempt with `verdict="needs_human"` + the last drift_detected reasoning |
| `DARK_MINDSET_TIMEOUT_MS` exceeded | Return best attempt with `verdict="needs_human"` + timeout flag |
| Cache write failure | Non-fatal: return the result anyway, log the cache failure |

All failure paths still return a `MindsetApplyOutput` shape (never
a hard error), so the harness's delegation loop never breaks.

---

## What this is NOT

- **Not a subagent registry.** Phase 2 (deferred until 3+ production
  subagents registered). This is just the mindset composition layer.
- **Not a handoff protocol.** Phase 3 (deferred 6+ months). No
  cross-MCP composition, no A2A compatibility, no webhook notifications.
- **Not a state machine.** The parent agent's vibe-loop runs unchanged;
  `mindset_apply` is a single tool call, not a state transition.
- **Not an enforcement layer.** `tools_recommended` is a hint string
  for the harness; dark-memory does not (yet) constrain the subagent's
  actual tool list. Phase 2 (deferred).

---

## Audit trail

Every `mindset_apply` call persists:
- 1+ `sdd_evaluations` rows (one per LLM call: composition + validation)
- 1 `agent_memory` row (the cache entry, with `write_path="MindsetApplyCache"` in `write_audit`)
- 1 `write_audit` row per cache write (INV-1 atomic with the data write)

Operators can trace any mindset back through:
- `judgment_history(eval_type="mindset_quality")` → see all validation verdicts
- `judgment_history(eval_type="mindset_compose")` → see all composition attempts
- `agent_memory_recall(query="mindset-cache")` → see all cached prompts
- `writes(session_id=...)` → audit log of every cache write

---

## See also

- `docs/agent-bootstrap.md` — how the harness discovers dark-memory's tools
- `docs/INVARIANTS.md` — INV-1 (write audit), INV-7 (project scope), INV-10 (memory persistence)
- `vibe-flow/main/DMAP_V1_1.md` — Layer 3 Delegation (planned but not shipped)
- `vibe-flow/main/ACTIVE_MEMORY_RFC.md` §M7 — original delegation spec