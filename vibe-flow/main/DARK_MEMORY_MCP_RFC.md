# Dark Memory MCP — Main Spec (RFC)

**Version**: 1.0.0
**Status**: Draft → Replan pending
**Author**: dark-agents architect
**Date**: 2026-07-14
**Decision record**: supersedes fragmented specs `spec_id 141–145` persisted earlier in dark.db

---

## 0. Why this document exists

Earlier in this session five specs were persisted to `dark.db` (IDs 141–145) describing a flat CRUD-by-table tool surface for "Dark Memory MCP". That design treats the agent's persistent memory as a database of rows — the LLM is asked to compose five-to-ten tool calls in sequence, each round-tripping a flat row, to produce one artifact. The friction this creates was the actual user complaint:

> "guardar toda la spec para persistir está bien, pero es pésimo para recuperar contexto"
> "debe haber armonía y 0 fricción, el MCP debe construirse según la intención de USO"

This RFC replaces those five specs with one opinionated, decision-justified design. Sub-specs will cascade from this RFC; they do not stand alone. Cancels and supersedes `spec_id 141–145` for all architectural decisions (the persisted rows are kept as historical record; the running plan references this RFC only).

## 1. Problem statement

A multi-agent system — dark-research-mcp's OSINT router, sub-agents, vibe-flow pipelines, red-team runs — needs **persistent, intent-driven, context-aware memory**. Concretely:

- **Cross-session recall**: an LLM is asked about a CVE; yesterday's research on that CVE must be available — but not as a raw row dump, as **composed context the LLM can act on directly**.
- **Vibe-flow execution**: the user says "publish a brand-compliant artifact for EU"; the LLM should be able to do this in 1–2 tool calls, not 7.
- **Operational scale**: SQLite's single-writer mutex is the ceiling under concurrent sub-agents. Postgres with connection pooling removes it.
- **Audit + defense**: every write to persistent memory is a potential prompt-injection vector (auto-recall in `dark-recall` v2 prefill). The architecture must enforce write audit, canary-on-writes, and constitution integrity — not as documentation, as mechanical invariants.
- **Coexistence**: dark-research-mcp is a sibling OSINT tool with its own `dark_mem_*` namespace. The new unit is **sibling**, not replacement.

## 2. Design principles (the five commitments)

**P1 — Intent before CRUD.** Every tool exposed to the MCP surface answers a user-or-LLM **intent** ("publish", "research", "judge"), not a database operation (INSERT, SELECT). When the intent is "publish a brand-compliant artifact for EU", one tool call orchestrates: spec resolution → brand match → compliance check → drift judge → validation. The LLM is not the orchestrator.

**P2 — Context object, not row dump.** Every retrieval returns a **composed context object** (`ArtifactContext`, `SessionContext`, `PolicyContext`). The LLM receives a single coherent view; it does not have to call four tools and join the results in its head.

**P3 — Sequence-aware response.** Every orchestrator's response includes a `next` field — a suggestion for the next tool the LLM might call (or `null` if done). Sequence-awareness is in the protocol, not in the LLM's memory.

**P4 — Defense as contract.** The six invariants from the constitution (write-path audit, per-session scoping, canary on writes, constitution audit, cache integrity, mod content sanitization) are enforced **at the Store interface boundary**. They are not policies; they are type-system-level guarantees — `Save*` returns an error if invariant violated.

**P5 — Economy by default.** Every retrieval passes through the Atlan token-economy pipeline (dedup → filter_confidence 0.5 → truncate 500 → compress → cap 10) before reaching the LLM. This is not a tool; it is the only retrieval path the architecture exposes. The LLM never has to post-process a 50-item list.

## 3. Architectural decisions and their rationale

### D-1: Standalone Go module (sibling of dark-research-mcp)

**Decision**: `github.com/dark-agents/dark-memory-mcp` is its own Go module with its own `go.mod`. Three binaries: `dark-memory-mcp` (MCP server), `dark-memory-cli` (admin), `dark-memory-inspect` (read-only diagnostic).

**Why not extract this from dark-research-mcp's `internal/mem/`?** That path couples memory to the OSINT router and creates an import cycle. dark-research-mcp owns OSINT (research, web, academic, code); Dark Memory MCP owns memory. Different lifecycles, different upgrade cadence, different audiences.

**Coexistence contract**: dark-research-mcp's `dark_mem_*` namespace is marked DEPRECATED with a shim that emits `{deprecated: true, successor: 'dark-memory-mcp'}`. The dark-recall plugin v2.3 prefers `dark_memory_*` when both servers are present, falls back to `dark_mem_*` with a one-time warning toast otherwise. No forced migration.

**Sources consulted**: paper `2310.08560` (MemGPT, Packer et al., Berkeley) argues for "tiered memory" analogous to OS virtual memory. We adopt the principle (a memory unit, separate from inference) but resist the implementation detail of "main memory = LLM context window"; in our model persistent memory is a richer state than the LLM window. Paper `2504.01963` (Aratchige et al., multi-agent survey) is consistent with our split: each agent has its own memory + coordination surface. Anthropic's prompt caching GA (Dec 2024) justifies splitting memory into a unit the LLM can reference via cacheable static prefix — exactly the use case here.

### D-2: Dual-driver storage with PostgreSQL as default, SQLite as fallback

**Decision**: `Store` interface in `internal/store/`. Two impls: `internal/store/sqlite` (modernc.org/sqlite, pure Go) and `internal/store/postgres` (jackc/pgx/v5/pgxpool). Driver selected by `DARK_DB_DRIVER` env var.

**Why both?** The architectural ceiling is concurrency. SQLite is single-writer; with `WAL + busy_timeout(5000)` it handles modest concurrent reads. Postgres with `pgxpool` handles concurrent writers natively via MVCC + row-level locks, which is what multi-agent systems need. The downside of Postgres alone is operational overhead — for a solo developer with no DBA, SQLite as fallback is the right default. The `Store` interface makes the choice cheap.

**Why `modernc.org/sqlite` instead of `mattn/go-sqlite3`?** No CGO. Cross-compiles, no toolchain surprises. Performance difference is negligible for our access patterns (10k–100k rows).

**Why `jackc/pgx/v5` instead of `lib/pq`?** The `pgxpool` connection pool is faster than database/sql for Postgres-native workloads. `pgx` understands Postgres types natively; `lib/pq` requires workarounds.

**Operational note (decided, not deferred)**: SQLite is the default for `dark-memory-cli` and for small deployments. Postgres is opt-in via `DARK_DB_DRIVER=postgres`. The dual-driver test suite runs both; Postgres tests skip (with explicit message) when `DARK_TEST_POSTGRES_DSN` is unset.

### D-3: Schema migrations, six versions, versioned per driver

**Decision**: `internal/migrate/` with two driver packages (`sqlite/`, `postgres/`). Each defines a `[]Migration` slice — same `Version` + `Name`, different `Up` SQL (driver dialects). The `Store.Migrate()` dispatcher applies every pending migration in its own transaction.

**Why versioned?** Schema drift between dark.db versions (some at v1, some at v6) is the dominant operational risk. Versioned migrations are idempotent (every statement uses `IF NOT EXISTS` / `IF EXISTS`), reversible per-file, and the bookkeeping table (`schema_migrations`) records what has run.

**Migration history** (six steps, in order):
1. v1: initial schema (research_runs/items/links, vibe_*, sdd_evaluations)
2. v2: constitution system (constitutions, mods, mod_loads)
3. v3: sdd_evaluations extended with constitution audit columns
4. v4: write_audit (the INV-1 enforcement surface)
5. v5: sessions table (operational session as first-class row)
6. v6: constitutions watchdog columns (last_verified_at, last_verified_sha256)

**Why these six, in this order?** v1 is the historical baseline (the rows we already have in dark.db). v2 was added when constitutions became loadable. v3 closes the loop on the audit trail. v4 is where INV-1 enters — but that's also where the architecture pivots from "store data" to "audit every write". v5 introduces the session as a first-class row so workflow tools can scope reads (INV-2). v6 lets the watchdog verify on every Open without a separate scan.

**Implementation rule**: any new migration adds a `Migration{Version, Name, Up, Down}` to BOTH driver packages. The `Up` SQL is idempotent. The `Down` SQL is best-effort (Postgres/SQLite dialect differences mean rollback is approximate).

### D-4: Six operational invariants are type-system guarantees

**Decision**: every `Save*` method in `Store` interface takes a `WriteContext{actor, session_id, write_path, constitution_id, constitution_ver}`. The implementation:

1. **INV-1**: inserts a `write_audit` row in the SAME transaction as the data write. If audit insert fails, data insert rolls back. No silent writes, ever.
2. **INV-2**: `Recall(query, opts.RecallOptions)` filters by `SessionScope` — defaults to cross-session (the existing behavior), but workflow tools always pass `SessionScope=self + session_id`.
3. **INV-3**: payload-carrying Saves run `safety.ValidatePayload` against the active canary. Canary hit returns `ErrCanaryInPayload`; transaction rolls back. The hash (not the canary presence) is the derived invariant.
4. **INV-4**: `Store.Open` reads the active constitution's SHA256 from the DB, hashes the file at the configured `file_path`, compares. Mismatch raises `ErrConstitutionDrift`. Migrations refused under drift.
5. **INV-5**: `llm/cache.go Cache.Get` re-hashes stored text with SHA-256, compares to `entry.SHA256`. Mismatch → treat as miss + emit anomaly event. Migrations refuse unknown cache states.
6. **INV-6**: mod loader runs directive/knowledge bodies through `injectionMarkers` regex set. Refused unless `risk_class ∈ {exploit-development, active-probing}` AND user-file is whitelisted via `DARK_MOD_WHITELIST`.

**Why in the type system?** Constitution-text-only invariants drift under pressure. Type-system-enforced invariants are exercised by `go test`. A future contributor adding a new `Save*` cannot accidentally bypass INV-1 — the interface signature requires `WriteContext` and the test suite fails if the audit insert is missing.

**Why these six and not others?** Per the problem statement, these six are the engineering mitigations for the dark-recall-mediated prompt-injection class. Other invariants (e.g., "LLM never outputs the canary") are enforced inside the LLM layer, not here. The split is intentional: Dark Memory MCP owns store invariants; the LLM owns output invariants.

### D-5: Context objects, not row dumps

**Decision**: `internal/context/` defines composed view types returned by retrieval tools:

```go
type ArtifactContext struct {
    Artifact         *vibeflow.Artifact
    SpecMarkdown     string                    // rendered once, kept stable
    SpecTasks        []TaskView
    Brand            *vibeflow.BrandGuide       // resolved from brand_id
    Compliance       *vibeflow.ComplianceRule   // resolved from jurisdiction
    LastDrift        *vibeflow.DriftReport
    VerdictChain     []SddVerdictView          // brand + compliance + drift ordered
    WriteAuditTail   []audit.WriteEvent        // last 10 writes for this artifact
    RelatedLinks     []research.Link
}

type SessionContext struct {
    Session            *session.Session
    ActiveConstitution *constitution.Constitution
    ActiveMods         []mods.Mod
    Counts             SessionCounts
    RecentWrites       []audit.WriteEvent
    PendingDrifts      []DriftTask
    ActiveSpec         *vibeflow.Spec
}
```

**Why views?** The LLM's question is rarely "give me row 47"; it's "give me what I need to know about this artifact". A row dump requires the LLM to make 4–5 calls and join in-context. A Context object is one tool call returning one coherent thing. The cost: a bit more logic on the retrieval path; the benefit: zero context-joining friction for the LLM.

**Why a separate `internal/context/` package?** Keeps view types out of the storage layer (`internal/store/`). The store returns rows; the context layer composes them. Composition lives close to the LLM-facing surface, not the database.

### D-6: Orchestrators are functions, not tools

**Decision**: `internal/orchestration/` defines high-level workflow functions that take a Store + an LLM client + a safety holder and return composed results:

```go
func PublishVibe(ctx, deps Deps, input PublishInput) (*PublishResult, error) {
    // 1. resolve spec (existing or new)
    // 2. resolve brand + jurisdiction from spec
    // 3. save spec + tasks if new
    // 4. save artifact
    // 5. runJudge(brand_match) → persist sdd_evaluation
    // 6. runJudge(compliance_check) if jurisdiction
    // 7. runJudge(drift_judge) → persist
    // 8. saveDriftReport
    // 9. setArtifactValidation based on worst verdict
    // 10. return PublishResult{Passed, Chain, Audit, Next}
}
```

The MCP server exposes `PublishVibe` as a single tool. The LLM calls one tool; the orchestrator does the rest.

**Why orchestrators instead of LLM-chained tool calls?** Because tool-chaining has a hidden cost: each tool call requires the LLM to formulate args, parse the response, decide the next call, formulate the next args. For a 5-step workflow that's 5 generations. With an orchestrator it's 1 generation. The LLM is freed to think about the content, not the plumbing.

**Why functions, not goroutines?** Orchestrators run sequentially within a single tool call. There is no concurrency in publish_vibe because the order of writes matters (spec → artifact → verdict → drift). Sequential is the correct model.

### D-7: Sequence-aware responses — every response has a `next`

**Decision**: every orchestrator's response includes:

```go
type OrchestratorResponse struct {
    Result any          // typed result (e.g. *PublishResult)
    Next   *NextAction  // pointer to next tool to call, or nil
}

type NextAction struct {
    Tool  string                 // MCP tool name
    Args  map[string]any         // pre-filled args
    Why  string                  // "to see updated counts"
    When string                  // "always" | "on_failure" | "on_drift"
}
```

**Why?** Sequence-awareness moves the "what tool do I call next?" decision from the LLM's working memory into the response. The LLM still decides whether to act on the hint. But it doesn't have to derive the hint from first principles. Lower cognitive load → fewer wrong calls → fewer wasted rounds.

**Concrete shape**: `dark_memory_vibe_publish(spec_id=17)` returns:
```json
{
  "passed": true,
  "verdict_chain": [{"kind": "brand_match", "verdict": 0.92}, ...],
  ...
  "next": null   // done
}
```

or, on drift:
```json
{
  "passed": false,
  "drift": {"items": ["title changed", "missing EU disclosure"]},
  ...
  "next": {"tool": "dark_memory_vibe_resolve", "args": {"artifact_id": 42}, "when": "always"}
}
```

### D-8: Economy pipeline runs by default on every retrieval

**Decision**: `internal/economy/` defines:

```go
func Compress(items []Item, opts Options) []Item {
    // 1. dedup by URL (or by content hash)
    // 2. filter_confidence (drop items < 0.5)
    // 3. truncate content per item to 500 chars
    // 4. compress: prefer storing {title, one-sentence-summary, url} over full content
    // 5. cap total to 10 items
}
```

The economy pipeline is invoked inside every retrieval orchestrator before returning. The orchestrator never returns raw 50-item lists; it returns the 5–10 most relevant items in 1500 chars.

**Why economy by default?** A multi-agent system that retrieves 50 items per recall wastes tokens on dupes, low-signal items, and verbose content. The Atlan 5-bucket framework (referenced in dark-agents tooling) is the implementation pattern: dedup → filter → truncate → compress → cap. Doing this server-side means the LLM never has to deal with the bulk.

**Why not client-side?** The LLM doesn't have the budget to do this reliably. It's a small task for a tool, a hard task for a 200K-token model asked to do something else.

### D-9: MCP surface is ~25 intent-driven tools, not 52 CRUD operations

**Decision**: the tool surface (the public MCP contract):

```
SESSION (4)        dark_memory_session_start | _resume | _status | _close
RESEARCH (3)       dark_memory_research_topic | _recall | _resume_thread
VIBE (4)           dark_memory_vibe_publish | _spec | _pipeline_status | _resolve_drift
CONTEXT (3)        dark_memory_artifact_context | _spec_context | _session_context
JUDGE (3)          dark_memory_judge | _consensus | _judgment_history
POLICY (2)         dark_memory_active_policy | _load_constitution
OBSERVABILITY (3)  dark_memory_memory_state | _writes | _anomalies
ADMIN (3)          dark_memory_admin_migrate | _schema_status | _vacuum
```

Total: **25 tools**. Each maps to one intent. Each returns a Context object or an action result. Each response includes `next` when applicable.

**Why 25?** Three constraints:
- Each tool = one user/LLM intent, fully resolved in one call
- The total is small enough that the LLM can read the whole surface into context
- It covers every primary use case without overlap

A 52-tool surface requires the LLM to disambiguate between near-duplicates. A 25-tool surface has clear boundaries. The principle: tools are nouns and verbs the LLM can think about, not a vocabulary it must memorize.

### D-10: dark-recall plugin v2.3 prefers `dark_memory_*` when both servers present

**Decision**: the dark-recall plugin (in `~/.opencode/plugins/dark-recall.ts`) detects whether the dark-memory-mcp MCP server is registered in `opencode.jsonc`. If yes, it calls `dark_memory_*` tools. If no, it falls back to `dark_mem_*` with a one-time toast warning.

**Why?** Coexistence demands that users opt in to dark-memory-mcp without breaking their existing dark-research-mcp workflows. The plugin layer is the right place to make the transition invisible to the LLM.

**What changes from v2.1?** Layer 2 (Passive Prefill) now calls `dark_memory_research_topic(query)` instead of `dark_mem_recall_research(query)` — one orchestrated call instead of a flat recall. Layer 1 (System Reminder) reads from `dark_memory_active_policy()` to inject the active constitution/mods context as part of the persisted state. Layer 3 (Auto-Intercept) uses the new prefill canonicalization: inject content between `[INSTRUCTIONS]` and `[DATA]` boundary markers, scan against the canary before injection (INV-3 reinforcement on the prefill path).

## 4. Schema (canonical — supersedes anything in dark-research-mcp's `internal/mem/`)

```
research_runs(id, session_id, query, intent, backend_used, backends_tried, took_ms, confidence_avg, items_count, errors, created_at)
research_items(id, run_id→runs, title, url, snippet, source, confidence, freshness_at, lang, raw, actor, write_path, content_sha256, created_at)
research_links(id, item_id→items, target_type, target_id, note, source, confidence, created_at)

vibe_specs(id, vibe_case, session_id, constitution_json, spec_json, tasks_json, created_at, updated_at)
vibe_brands(brand_id, voice_json, visual_json, narrative_json, compliance_json, created_at, updated_at)
vibe_compliance(jurisdiction, rules_json, effective_at, source_url, created_at)
vibe_artifacts(id, session_id, vibe_case, spec_id, artifact_url, artifact_type, brand_id, jurisdiction, has_disclosure, validation_status, created_at)
vibe_drift_reports(id, artifact_id, spec_id, verdict, spec_diff_json, judge_reasoning, reconciled_at, created_at)

sdd_evaluations(id, eval_type, target_type, target_id, verdict_json, confidence, prompt_version, model, created_at,
                constitution_id, constitution_version, active_mods_json, refused_attempts, refusal_pattern)

constitutions(id, constitution_id, version, label, source, file_path, parsed_json, sha256, enabled, created_at, activated_at,
              last_verified_at, last_verified_sha256)
mods(id, mod_id, name, version, source, manifest_json, sha256, risk_class, target_scope, requires_tor, created_at, updated_at)
mod_loads(id, mod_id, session_id, loaded_at, duration_ms, capabilities_count, error, constitution_id)

sessions(id, session_id, status, constitution_id, constitution_ver, active_mods, started_at, closed_at, notes,
          parent_session_id, operator)

write_audit(id, table_name, row_id, actor, session_id, write_path, content_sha256, canary_present,
            constitution_id, constitution_ver, notes, created_at)

schema_migrations(version, applied_at)
```

The dark-research-mcp companion schema (findings, attacks, responses, profiles, models, techniques, papers, sessions-overlap, audit) lives in the same dark.db file but is **owned by dark-research-mcp**, not by Dark Memory MCP. Dark Memory MCP never writes to those tables and reads them only via raw SQL when an orchestrator needs context about a red-team attack (cross-link).

## 5. Tool surface — the full contract

See §D-9 for the count and shape. Detailed tool args/returns live in `internal/orchestration/` package documentation and are persisted as sub-specs in dark.db.

### Constraint: every tool response shape

```go
type ToolResponse struct {
    Data    any         // the result (Context object or action result)
    Audit   *AuditRef   // write_audit row id(s) produced by this call (optional)
    Next    *NextAction // sequence-aware next-step suggestion
    Error   *ToolError  // if non-nil, Data is best-effort
}

type ToolError struct {
    Code    string  // "ErrCanaryInPayload", "ErrConstitutionDrift", ...
    Message string  // 1-sentence fact + 1-sentence implication
    Hint    string  // optional: what to do next
}
```

### Naming convention

All public MCP tools use prefix `dark_memory_*`. No exceptions. This is the enforced namespace owned by this module. Any tool that mutates schema lives under `dark_memory_admin_*`. Any tool in `dark_mem_*` (legacy) is dark-research-mcp's and is being deprecated.

## 6. Operational model

### Process lifecycle

```
boot:
  1. load config from env (DARK_DB_DRIVER, DARK_DB_DSN, DARK_CACHE_DIR, DARK_MOD_WHITELIST)
  2. open Store via factory.Open(ctx, cfg)
  3. Store.Open runs migrations + constitution watchdog (INV-4)
  4. safety.NewCanary() → install on Store (INV-3)
  5. mcp.NewServer → register all 25 tools
  6. stdio transport

shutdown:
  1. close all active sessions (write status=closed)
  2. flush write_audit
  3. close Store
  4. mcp server stop
```

### Failure modes

| Mode | Behavior |
|---|---|
| Store unavailable at boot | panic with explicit message; MCP server refuses to register tools |
| Migrations pending and watchdog fails | hard stop; refuse to start until constitution SHA matches DB |
| Active session not found on `dark_memory_session_close` | return `ErrNotFound`; do not create a new row |
| Recall query contains canary | refuse with `ErrCanaryInPayload`; provide no data |
| Drift unresolved on publish | return `passed=false` with `next` pointing to `_vibe_resolve` |

### Performance targets

| Metric | Target |
|---|---|
| `SaveRun` P50 latency, SQLite, 10k rows | < 5ms |
| `SaveRun` P50 latency, Postgres, 100k rows | < 10ms |
| `Recall` P50 latency, SQLite, 10k rows | < 10ms |
| `Recall` P50 latency, Postgres, 100k rows | < 30ms |
| `PublishVibe` end-to-end (1 spec + 2 judges + drift) | < 1500ms LLM-bounded |
| 1000-call e2e stress test on dual drivers | no deadlock, no panic, audit rows match writes |

### Operational tests (in CI)

- `tests/dual_driver/store_test.go`: contract test on both drivers (always runs SQLite; Postgres if `DARK_TEST_POSTGRES_DSN` set)
- `tests/orchestrator/`: each orchestrator has unit tests covering happy path + 1–2 failure modes
- `tests/invariants/inv{1..6}_*.go`: each invariant has a test that asserts it
- `tests/stress/1000_calls.go`: server up, fire 1000 mixed calls, verify invariants

## 7. Security model

The six invariants (§D-4) plus the watchdog (§D-2). Defense is mechanical; defense text in the constitution file is commentary, not enforcement.

### Specifically: the canary

`internal/safety/canary.go` mints a per-process 128-bit canary at boot. The canary is embedded into the system prompt by external callers (e.g., a system prompt composer) when they want protected-channel behavior. Storage never embeds the canary (would defeat its purpose). Every payload-carrying `Save*`:

1. computes `sha256(payload)` → stored in `write_audit`
2. checks `payload` against the active canary → if present, rolls back the transaction and returns `ErrCanaryInPayload`

This is structurally equivalent to the existing `dark-research-mcp/safety/defense.go InputValidator`, just applied at the persistence boundary instead of the tool-arg boundary. The dark-recall plugin v2.3 will additionally scan prefill content against the canary before injection (fixing the bypass where prefill bypassed tool-arg validation).

## 8. Coexistence with dark-research-mcp

dark-research-mcp is a sibling:
- Owns OSINT acquisition (web, academic, code, cve, ip, dns, threat, ...)
- Persists research runs + items via its OWN storage layer (currently `internal/mem/`, soon to be `internal/dark/mem/` from crush fork)
- Exposes `dark_research_*` and the deprecated `dark_mem_*` namespace

Dark Memory MCP:
- Owns persistent memory as a unit
- Persists via its own Store (this module)
- Exposes `dark_memory_*`

The two share `dark.db` (different tables) and the dark-recall plugin ties them together. dark-research-mcp can depend on the Dark Memory MCP library to write to `sessions`, `write_audit`, etc., eliminating duplication. Or — and this is the current decision — they run independently and share `dark.db`. dark-research-mcp's existing `internal/mem/` keeps the legacy `dark_mem_*` namespace working but is frozen.

**Adoption path for dark-research-mcp users**: zero-cost. They keep using `dark_mem_*` and get a deprecation toast in responses. They install `dark-memory-mcp` separately. They update the dark-recall plugin to v2.3. The LLM migrates over time as the user updates the prompts.

**The canonical coexistence contract lives in `vibe-flow/main/BRIDGE_AND_COEXISTENCE.md`** (sub-spec 164). That document is the **normative** version of this RFC §8. Key declarations: MCP itself is the bridge between harnesses and MCPs; dark-recall plugin is optional opencode-specific UX glue; coexistence is declared via `serverInfo.coexistence_group`; versioned as `cx.v1 / cx.v2 / cx.v3`. This RFC §8 stays as the product summary; BRIDGE_AND_COEXISTENCE.md is the systems architecture.

## 9. Migration / deprecation

| User state | Behavior |
|---|---|
| No dark-memory-mcp installed | dark-research-mcp's `dark_mem_*` works as today; dark-recall v2.1 stays |
| dark-memory-mcp installed | dark-recall v2.3 detects; prefers `dark_memory_*`; falls back to `dark_mem_*` if dark-memory-mcp server unreachable |
| User updating prompts to `dark_memory_*` | full new surface; old `dark_mem_*` still works |
| dark-research-mcp bug in deprecated code | not fixed by us; user told to migrate to dark-memory-mcp |

The deprecation shim is documented in dark-research-mcp's `internal/tools/dark_mem.go`. Each handler returns the same shape plus `{deprecated: true, successor: 'dark-memory-mcp'}`. No forced upgrade.

## 10. Open questions and decisions deferred to v1.1

- **Vector recall via pgvector**: deferred. We rely on substring + BM25 LIKE for v1.0. Vector search is a clear win but adds a Postgres-extension dependency that's harder to install; defer to v1.1.
- **Modulo write audit partitioning for high-volume**: deferred. The current schema partitions writes by `created_at`; if we exceed 100k writes/month we should consider time-based partitioning.
- **Multi-tenancy beyond session_id**: deferred. The `session_scope=self` filter is the only isolation primitive. Real multi-tenant isolation (cross-user privacy) is a v2 problem.
- **Armed-mode red-team tools**: deferred. The `dark_memory_redteam_*` namespace is not in v1.0. When introduced, it requires `DARK_REDTEAM=armed` env var and is logged at the audit boundary.

## 11. Sub-specs cascading from this RFC

This RFC is the source of truth. The following sub-specs will be created in dark.db (each is a vibe-flow spec with its own tasks), and each drives a wave of generation:

- **sub-spec 1 (storage foundation)** — already partially built; refine + persist final state. Tasks: go.mod, types/*, Store interface, sqlite impl, postgres impl, migrations v1-v6, dual-driver contract test.
- **sub-spec 2 (safety + audit + sessions)** — implementations of the six invariants at the boundary. Tasks: canary, markers, safety holder; write_audit infrastructure; session table CRUD; watchdog; tests/invariants/inv{1..6}_*.go.
- **sub-spec 3 (context + economy)** — composed views + Atlan pipeline. Tasks: internal/context/ types; economy/compress.go; orchestrator hooks.
- **sub-spec 4 (orchestrators)** — high-level workflows. Tasks: PublishVibe, ResearchTopic, RecallContext, ActivePolicy, MemoryState, ResolveDrift. Tests for each.
- **sub-spec 5 (MCP server)** — `cmd/dark-memory-mcp` + `internal/server` + tool registry. Wires orchestrators to MCP tools. 25 tools final.
- **sub-spec 6 (CLI)** — `cmd/dark-memory-cli` + `cmd/dark-memory-inspect`. Subcommands: migrate, vacuum, schema-status, set-driver, inspect.
- **sub-spec 7 (dark-recall plugin v2.3)** — update `~/.opencode/plugins/dark-recall.ts` to prefer dark-memory-mcp, scan prefill for canary, use `dark_memory_research_topic` instead of `_recall_research`.
- **sub-spec 8 (dark-research-mcp deprecation shim)** — emit `{deprecated: true, successor: 'dark-memory-mcp'}` from `internal/tools/dark_mem.go`. README coexistence section.
- **sub-spec 9 (runbooks + ops)** — `docs/RUNBOOK.md` covering Postgres install, driver switch, vacuum policy, retention, monitoring.
- **sub-spec 10 (drift verdict + human gate)** — for each sub-spec 1–9, run `dark_ssd_drift_judge` against the generated artifacts vs the spec. Compile a final report.

Each sub-spec starts with `dark_research_spec_create` (got a session_id in `dark-memory-mcp-v1`). Each task is logged as an artifact (`dark_research_artifact_log` with `spec_id` of its parent sub-spec, `artifact_type='code'`, `artifact_url` pointing to the Go file). Each sub-spec closes with `dark_ssd_drift_judge` + `dark_research_drift_log`. The plan is durable in dark.db and recoverable across sessions.

## 12. Acceptance criteria

The product is **shippable** when, in any order:

1. `go test ./tests/dual_driver/...` passes (SQLite always; Postgres if `DARK_TEST_POSTGRES_DSN`).
2. `go test ./tests/invariants/...` passes — each of the six invariants has at least one test.
3. `go test ./tests/orchestrator/...` passes — each of the 25 orchestrators has unit tests.
4. `dark-memory-mcp` binary registers 25 tools, survives 1000 mixed tool calls without deadlock.
5. The dark-recall v2.3 plugin in `~/.opencode/plugins/dark-recall.ts` compiles and detects dark-memory-mcp presence.
6. dark-research-mcp's `dark_mem_recall_research` returns `{deprecated: true, successor: 'dark-memory-mcp', successor_tool: 'dark_memory_recall'}` in its response.
7. `dark_ssd_drift_judge` on the generated artifacts vs sub-spec 1 returns `aligned` for the dual-driver contract; subsequent sub-specs may return `drift_detected` which we resolve with patches before proceeding.
8. README + RUNBOOK + COEXISTENCE.md exist and are accurate.

## 13. Decision record

This RFC replaces `spec_id 141–145` (the earlier fragmented plan). Status changes will be reflected in dark.db via `dark_research_spec_update` and an explanatory note in `dark_research_drift_log`. Future sub-specs reference this RFC explicitly in their `constitution` field, not the older 141–145 IDs.

End of RFC.
