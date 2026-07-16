# Coexistence — dark-research-mcp + dark-memory-mcp

> **Audience**: anyone running both `dark-research-mcp` and
> `dark-memory-mcp` in the same harness. Pairs with
> `BRIDGE_AND_COEXISTENCE.md` (the systems architecture) and
> [RUNBOOK.md](./RUNBOOK.md).
>
> **TL;DR**: install both servers, the harness picks the right one
> per call. `dark_mem_*` is deprecated; `dark_memory_*` is the new
> canonical namespace. Both servers share `coexistence_group` so
> harnesses know they belong to the same memory group.

---

## What runs where

```
┌──────────────────────────────────────────────────────────────────┐
│ harness (opencode, claude, MCPJam, custom)                        │
│                                                                  │
│   reads:                                                         │
│     dark-research-mcp serverInfo                                 │
│       → coexistence_group = "dark-agents/memory"                │
│                                                                  │
│   detects both servers → routes by namespace:                    │
│     dark_mem_*         →  dark-research-mcp (DEPRECATED shim)    │
│     dark_memory_*      →  dark-memory-mcp     (PREFERRED)         │
└──────────────────────────────────────────────────────────────────┘
        │                                  │
        ▼                                  ▼
┌─────────────────────┐        ┌─────────────────────────┐
│ dark-research-mcp   │        │ dark-memory-mcp          │
│                     │        │                         │
│ owns: OSINT         │        │ owns: persistent memory │
│   research_runs     │        │   sessions               │
│   research_items    │        │   write_audit            │
│   (legacy)          │        │   vibe_specs             │
│                     │        │   vibe_artifacts         │
│ exposes:            │        │   vibe_drift_reports     │
│   dark_mem_*        │        │   sdd_evaluations        │
│   (deprecated)      │        │   constitutions + mods   │
│   dark_research_*   │        │                         │
│                     │        │ exposes:                 │
│                     │        │   dark_memory_*          │
└─────────────────────┘        └─────────────────────────┘
        │                                  │
        └────────────┬─────────────────────┘
                     ▼
            shared dark.db (different tables)
```

The two servers own **different tables** in the same dark.db file
(SQLite) or the same database (Postgres). They don't write each
other's tables except via explicit cross-link (see below).

---

## Why coexistence, not replacement?

The two servers have different upgrade cadences and audiences:

- **dark-research-mcp**: OSINT acquisition. Changes when the OSINT
  landscape changes (new sources, new research methods). Owned by the
  research team.
- **dark-memory-mcp**: persistent memory as a unit. Changes when
  the memory model evolves (new context types, new orchestrators).
  Owned by the platform team.

Coexistence lets each team ship independently without forcing
lock-step upgrades on operators.

---

## `coexistence_group` — the conformance field

Every MCP server in the `dark-agents/*` group declares the same
`coexistence_group` string in its initialize response. The harness
inspects this string and knows which servers belong together.

For `dark-memory-mcp` (this repo), the wire declaration is via the
MCP `instructions` field (mcp-go v0.40.0 doesn't yet expose a
custom `coexistence_group` serverInfo field — see bridge.2 in
[BRIDGE_AND_COEXISTENCE.md](../../vibe-flow/main/BRIDGE_AND_COEXISTENCE.md)):

```
coexistence_group=dark-agents/memory (spec 164 bridge.2). 
Canonical 25-tool order preserved per spec 164 bridge.4. 
This server is part of the dark-agents/memory coexistence group; 
harnesses detecting another dark-agents/* server should prefer 
the local dark_memory_* tools over dark_mem_*.
```

`dark-research-mcp` carries the same string in its own initialize
response (see spec 164 bridge.3 — operator-side update in the
sibling repo).

---

## The dark-recall plugin (opencode-specific UX)

If the operator has the `~/.opencode/plugins/dark-recall.ts` plugin
installed (v2.3+), it does the routing automatically:

1. **Detection**: reads each server's `coexistence_group`.
2. **Routing**: prefers `dark_memory_*` over `dark_mem_*` when both
   are present.
3. **Fallback**: if `dark-memory-mcp` is unreachable, falls back to
   `dark_mem_*` with a one-time warning toast.
4. **Prefill scanning**: scans prefill content against the canary
   before injection (INV-3 reinforcement on the prefill path).

The dark-recall plugin lives in the **opencode-plugin** repo (a
sibling to dark-research-mcp and dark-memory-mcp). Spec 160 is the
canonical description.

---

## Migration path: `dark_mem_*` → `dark_memory_*`

If you have an existing dark-research-mcp setup:

| Step | Action | Owner |
|---|---|---|
| 1 | Install `dark-memory-mcp` (separate binary, separate DARK_DB) | Operator |
| 2 | Verify the harness sees both servers in `tools/list` | Operator |
| 3 | Update the `dark-recall` plugin to v2.3 (auto-routes to `dark_memory_*`) | Operator / plugin maintainer |
| 4 | Update your LLM prompts to prefer `dark_memory_*` tool names | Operator / user |
| 5 | (optional) Read `dark_mem_*` results + rewrite as `dark_memory_*` calls in the harness | Harness |

There is **no forced upgrade**. `dark_mem_*` keeps working as long as
`dark-research-mcp` is running. The deprecation is a soft signal, not
a hard cut.

---

## Cross-server workflows

Some workflows span both servers. Examples:

1. **OSINT → Memory**: dark-research-mcp returns research results;
   dark-memory-mcp's `dark_memory_research_topic` wraps the same call
   inside a session_id + project_id context, persists the run +
   items, and returns a context-shaped projection.
2. **Memory → OSINT**: dark-memory-mcp's `dark_memory_recall` returns
   prior research items; the LLM uses them to ground a new
   dark-research-mcp call (avoiding redundant OSINT).

The two servers do NOT call each other directly. Cross-server flows
go through the LLM.

---

## Schema ownership

| Table | Owner | Other server can read? |
|---|---|---|
| `research_runs`, `research_items`, `research_links` | dark-research-mcp | yes (raw SQL only) |
| `vibe_specs`, `vibe_artifacts`, `vibe_drift_reports`, `vibe_brands`, `vibe_compliance` | dark-memory-mcp | no |
| `sdd_evaluations` | dark-memory-mcp | yes (auditing other servers' calls is allowed) |
| `write_audit` | dark-memory-mcp | no |
| `constitutions`, `mods`, `mod_loads` | dark-memory-mcp | no |
| `sessions` | dark-memory-mcp | no |
| `schema_migrations` | both | yes (shared migration tracking) |

---

## See also

- `BRIDGE_AND_COEXISTENCE.md` — the systems architecture (cx.v1 contract).
- [RUNBOOK.md](./RUNBOOK.md) — operator how-to.
- spec 164 (dark.db) — bridge conformance + coexistence state.