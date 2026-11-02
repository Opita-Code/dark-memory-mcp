# Integrating dark-memory into a development flow

> **TL;DR**: dark-memory gives a development flow three things a chat
> session does not have — a promise written before the work, a judge that
> reads the delivered bytes, and a durable trail that survives the
> session. This document describes how those three fit together, what
> each layer guarantees, and the gaps a caller must know about.
>
> Everything here was measured against `dark-memory-mcp v4.0.0-alpha.33`
> (commit `ba1c29e`). Numbers are observations, not recollections.

The policy that governs the flow is the workspace constitution
(`workspace-policy.constitution.json`, kept in the project workspace and
tracked in this repo as
[`workspace-policy.constitution.example.json`](./workspace-policy.constitution.example.json)).

---

## 1. The flow

```
  ┌─ SESSION (one unit of work) ───────────────────────────────────────┐
  │                                                                     │
  │  1. session_start ──► binds project + constitution_id@version        │
  │  2. recall ────────► what we already know (memory before search)    │
  │  3. vibe_spec ─────► the PROMISE: intent + tasks + constitution      │
  │  4. [work] ────────► the agent writes the artifact                  │
  │  5. vibe_publish ─► DELIVERY to the judge (artifact_ref required)   │
  │  6. verdict ───────► aligned | drift_detected | needs_human         │
  │  7. memory_save ──► the durable decision (kind=decision, pinned)    │
  │  8. session_close ─► the trail is left behind                       │
  │                                                                     │
  └─────────────────────────────────────────────────────────────────────┘
              │                                    │
              │ constitution (SHA-pinned)         │ persona frame
              │                                   │ (injected into
              ▼                                   ▼ every recall)
        integrity + provenance          the rule reaches the agent
```

Each layer does a different job and **none substitutes for another**.

---

## 2. What each layer guarantees (measured, not assumed)

| Layer | Mechanism | What it guarantees | How it was verified |
|---|---|---|---|
| **Policy integrity** | Watchdog SHA (`internal/store/sqlite/store.go`, `runWatchdog`) | Editing the constitution without bumping the version ⇒ the store **does not open**. Fail closed. | `active_policy` reports `constitution_drift: false`; the file's SHA-256 matches the pinned `bb781d71…`. |
| **The rule reaches the agent** | `PersonaFrame` in `dark_memory_recall` (`internal/recall/assemble.go`, `parsePersonaFromConstitution`) | The constitution's `voice` and `claims_policy` are injected into **every** recall. | The recall returned the WS-1…WS-6 text in `frames.persona`. |
| **The promise precedes the work** | `vibe_spec` | An intent exists **before** the artifact does. | Spec 20 and 25 carried intent + tasks + constitution. |
| **The judge reads the real bytes** | `artifact_ref` + `drift_judge` | The judge scores the **file's bytes**, not the agent's description of them. | `aligned` conf 1.00 for a conforming artifact; `drift_detected` conf 1.0 for a text that pointed at `/tmp/build` — the judge quoted the path. |
| **Provenance** | `write_audit` + `constitution_id@version` | Every write is attributed to the policy in force. | `session_close` reports a `writes_total` for the session. |
| **Continuity** | `agent_memory` + `ContextRecap` | The next session starts knowing what was decided. | `session_start` returned a recap containing the open todo. |
| **Nothing fails silently** | Error Observatory (41 `RecordError` call sites in `internal/orchestration`) | Every error is recorded and queryable. | `error_summary` showed the cluster counts by domain and severity. |

---

## 3. Where the budget is

The judge is the expensive part: one LLM call per publication. Everything
else is a local SQLite read. Practical rules:

- **Publish evidence, not the binary.** The judge reads bytes; a diff or a
  verification report costs fewer tokens than the whole artifact.
- **One spec per deliverable.** Reusing an old intent against a new
  artifact produces spurious `drift_detected`.
- **Do not retry `needs_human`.** It means the judge could not decide; the
  operator decides. Retrying spends tokens to get the same no.
- **The verdict comes from one judge.** The response says so itself
  (`confidence_caveat: "single-judge measurement … correlated, not
  independent review"`). For high-stakes calls use `consensus` with n≤7
  rather than relying on luck.

---

## 4. Where the flow lives

| Layer | Where | Why |
|---|---|---|
| Rules | `governance/workspace-policy.constitution.json` (operator workspace; tracked here as the `.example.json`) | Per-project operator policy: SHA-pinned, living in the project workspace (WS-4). Not a product default — see `constitutions/` in [ARCHITECTURE-V4.md](../ARCHITECTURE-V4.md) for what the product ships. |
| Orchestration | `governance/dm-flow.sh` | Validates the rules before any token is spent, then emits the call plan. |
| Decisions | the dark-memory SQLite database | Crosses sessions and projects. |
| Trail | `write_audit` | One write, one row. |

The script **does not talk to MCP by itself**. The first prototype started
its own MCP server and spoke to it over FIFOs: it hung whenever a reply
did not arrive, and a hung script takes the harness session down with it. A
development gate cannot be the component that hangs the flow.

So `dm-flow.sh` does two things and only two:

1. **Pre-flight** (cheap, before the expensive work): is the constitution in
   the workspace? Is the artifact inside the workspace? Does it exist?
2. **Plan**: the exact MCP calls, with the arguments already resolved, for
   the agent to run with its own tools — where the server-side gate
   validates them anyway.

```bash
# does this respect the rules? (exit 0 = yes, 65/66 = no, with the reason)
governance/dm-flow.sh check  dark-memory-mcp 25 --artifact "$PATH"

# the call plan, arguments resolved
governance/dm-flow.sh plan   dark-memory-mcp 25 --artifact "$PATH"
```

---

## 5. Gaps the integration must know about

Verified on this machine, not inferred.

### 5.1 `vibe_spec` and `vibe_publish` do not share a `spec_id`

`vibe_publish` **creates its own row** in `vibe_specs`
(`internal/orchestration/publish_vibe.go:265` → `SaveSpec`). Publishing an
artifact with a forwarded `spec.spec` produces a **new** spec (observed:
the open spec 20 produced specs 21, 22 and 23).

Consequence: the `todos` that `vibe_spec` creates are **never** archived by
the publish, because the publish does not point at the original spec.

**Mitigation applied**: `dm-flow.sh` does not depend on the auto-close.
Step 7 of the plan is an explicit `agent_memory_save`. The auto-close does
exist (`auto_save_decision: true` works: it created a decision row with
`prov=judge-minimax`), but it **does not** close the open spec.

### 5.2 `auto_save_todos` is not exposed through MCP

The field exists in the orchestrator (`vibe_spec.go:80`) but **not** in the
schema of the `dark_memory_vibe_spec` tool. Confirmed by searching the
namespace for the parameter, and by a one-task spec that created no todo.
Auto-archiving todos is unreachable from MCP.

### 5.3 Mods are not loaded at boot

`mods_total: 0`, an empty `mod_loads`, and `SaveMod` is never called during
startup. The repo's `lucidez-operator-risk` mod is **not** loaded. A mod
written today is decorative: the two live enforcement vectors are the
watchdog SHA and the persona frame.

### 5.4 A TOML constitution never reaches the agent

The watchdog stores the file's raw bytes in `constitutions.parsed_json` and
the injector parses them with `json.Unmarshal`. The repo's constitutions
are TOML: they store fine, but their `[persona]` section is **never
injected**. That is why the workspace policy is JSON.

### 5.5 The gate protects memory, not disk

`ErrFrameStaleTooFar` means "no active session". The gate does **not**
validate where files were written. What protects the disk is WS-2/WS-3,
which is why `dm-flow.sh`'s pre-flight validates them explicitly: it is
the layer the gate does not cover.

---

## 6. What NOT to automate

- **`needs_human`.** The judge does not know; the operator decides.
- **Changes of purpose.** If the artifact changed *what* it does and not
  *how* it does it, write a new spec. Forcing the artifact against an old
  intent produces a correct drift verdict and a waste of time.
- **The session close.** It is one line of code and it is what makes the
  next session start knowing. Without it the work is lost — which is
  exactly the problem dark-memory exists to solve.
