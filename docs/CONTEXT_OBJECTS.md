# Context Objects — Shape and Intent

> **Audience**: anyone writing a tool handler, an orchestrator, or
> extending the MCP surface. Context objects are the **LLM-facing
> projection** of dark-memory-mcp state — not row dumps.

## Why context objects?

Per RFC D-5: every retrieval returns a **composed context object**, not
a row dump. The LLM's question is rarely "give me row 47"; it's
"give me what I need to know about this artifact". A row dump
requires 4-5 tool calls and in-context joining. A Context object is one
tool call returning one coherent view.

## The 25 MCP tools → 8 Context projections

The MCP surface (`dark_memory_*`) maps onto **8 context projections**.
Some projections are returned by a single tool, others compose
multiple stores.

| Context | Returned by | Stores it composes |
|---|---|---|
| **ArtifactContext** | `dark_memory_artifact_context` | `vibe_artifacts` row + recent drift + brand + jurisdiction |
| **SpecContext** | `dark_memory_spec_context` | `vibe_specs` row + task count + intent preview |
| **SessionContext** | `dark_memory_session_context` | `sessions` row + constitution + mods + recent writes |
| **MemoryState** | `dark_memory_memory_state` | counts across all tables + driver + schema |
| **ActivePolicy** | `dark_memory_active_policy` | active constitution + mods + canary + INV-4 drift |
| **LoadConstitution** | `dark_memory_load_constitution` | full constitution row |
| **MemoryState (counts)** | `dark_memory_memory_state` | per-table counts via `Store.Stats` |
| **PipelineStatus** | `dark_memory_pipeline_status` | latest drift for an artifact_id |

---

## Shape: ArtifactContext

The most-composed context object. Returned by
`dark_memory_artifact_context`.

```typescript
{
  artifact_id:        number,
  artifact_url:       string,           // canonical location of the artifact
  artifact_type:      string,           // code | text | image | video | audio | multi
  spec_id:            number,
  brand_id?:          string,
  jurisdiction?:      string,
  has_disclosure:     boolean,          // EU AI Act flag
  validation_status:  string,           // passed | failed | pending
  session_id?:        string,
  created_at:         string            // RFC3339
}
```

**Intent**: enough for the LLM to decide whether to load the artifact
body, run drift_judge, or move on.

---

## Shape: SpecContext

Returned by `dark_memory_spec_context`.

```typescript
{
  spec_id:          number,
  vibe_case:        string,             // C1..C7
  constitution?:    string,             // JSON blob
  intent_preview?:  string,             // first 500 chars of spec.Spec
  task_count:       number,
  created_at:       string
}
```

**Intent**: the LLM can see "what was this spec about?" without
loading the full spec body. Full body is available via `Store.GetSpec`
if needed.

---

## Shape: SessionContext

Returned by `dark_memory_session_context`. Same shape as
`SessionStatusResult` (a thin projection of `session.Session`).

```typescript
{
  session_id:        string,            // sess-XXXXXXXX
  operator:          string,
  status:            string,            // active | closed | abandoned
  constitution_id?:  string,
  constitution_ver?: string,
  active_mods?:      string,            // JSON array of mod_id
  started_at?:       string,
  closed_at?:        string,
  notes?:            string,
  parent_session_id?: string
}
```

**Intent**: enough to decide whether to continue / close / branch.

---

## Shape: MemoryState

Returned by `dark_memory_memory_state`. **Global** (not
project-scoped — that's intentional per spec 171 T4g).

```typescript
{
  driver:           string,             // sqlite | postgres
  schema_version:   number,
  tables:           string[],
  counts: {
    specs:             number,
    brand_guides:      number,
    compliance_rules:  number,
    artifacts:         number,
    drift_reports:     number,
    sdd_evaluations:   number,
    sessions_active:   number,
    sessions_total:    number,
    runs_total:        number,
    items_total:       number,
    links_total:       number,
    write_audit_total: number,
    mods_total:        number,
    constitutions_total: number,
    projects_total:    number
  },
  active_project?:  string,
  canary_present:    boolean,
  constitution_id?:  string,
  constitution_ver?: string,
  constitution_drift: boolean,          // INV-4
  snapshot_version:  string              // "1.0.0"
}
```

**Intent**: operator's view of "what's in dark.db right now". Same
data is also exposed via `dark-mem-inspect` (CLI).

---

## Shape: ActivePolicy

Returned by `dark_memory_active_policy`. **Global** (constitutions +
mods are system-wide per spec 171 T4g).

```typescript
{
  constitution_id?:      string,
  constitution_version?: string,
  constitution_sha256?:  string,
  constitution_label?:   string,
  constitution_source?:  string,
  constitution_drift:    boolean,        // INV-4: SHA mismatch
  drift_reason?:         string,
  mods:                  ActiveModRef[],
  jurisdiction?:         string,
  canary_present:        boolean,
  policy_version:        string            // "1.0.0"
}
```

**Intent**: "what constitution am I running under, and is its hash
intact?" — required for INV-4 verification on every publish.

---

## Shape: PipelineStatus

Returned by `dark_memory_pipeline_status`.

```typescript
{
  artifact_id:    number,
  has_drift:      boolean,
  drift_id?:      number,
  verdict?:       string,               // aligned | drift_detected | needs_human
  spec_diff?:     string,
  reconciled_at?: string,
  created_at?:    string
}
```

**Intent**: the LLM can ask "what's the latest verdict on artifact
N?" in one call, then decide whether to call `resolve_drift`.

---

## Design rules for new contexts

When adding a new context object:

1. **Single tool call** — don't require the LLM to chain tools to
   assemble the context. Compose in the orchestrator.
2. **Friendly field names** — use `validation_status` not `v_st`,
   `created_at` not `ts`. The LLM reads these directly.
3. **Cap large fields** — text previews ≤ 500 chars; lists ≤ 50 items
   unless the tool is explicitly about listing. The Atlan economy
   pipeline is your friend.
4. **Stable shape** — fields can be added but never removed or
   renamed (would break every harness that read this context).
5. **No secrets** — the canary token is NEVER in the context. Audit
   metadata may surface `canary_present` (boolean), never the token.
6. **Failure modes** — every field is optional in the JSON schema
   (use `,omitempty`). Errors are surfaced via `ToolError` envelope,
   not empty fields.

---

*See also: [INVARIANTS.md](./INVARIANTS.md) · [RUNBOOK.md](./RUNBOOK.md)*