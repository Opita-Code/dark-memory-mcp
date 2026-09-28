# ARCHITECTURE-V4 — `dark-memory-mcp` v4.0 Redesign

> **TL;DR**: v4.0 is a clean rewrite of `dark-memory-mcp` branching
> from `v2.20.0` (last published). The vibe-loop becomes a **mutable
> workflow runtime** that the orchestrator (the agent, not the
> engine) modifies at runtime. Adapters, tools, and constitutions
> are declared as data via manifests, not compiled into Go.
> dark-research's 18 OSINT capabilities become native tools,
> replacing the separate `dark-research-mcp`. Auto-update is a
> first-class citizen. The 95 security controls of dark-research +
> dark-copilot are preserved; 10 new invariants (INV-11..INV-15)
> close gaps the audit surfaced.

| Field | Value |
|---|---|
| Audience | student, professor, technical auditor, LLM |
| Level | advanced |
| Status | **alpha.1 in progress** — see [`docs/v4-status.md`](docs/v4-status.md) for the ground truth |
| Version | v4.0.0-alpha.1 |
| Last reviewed | 2026-09-27 (delta from 2026-09-24 baseline) |
| Branch | `feat/v4-redesign` (from `v2.20.0`) |
| Local-only | YES — no `git push`/`fetch`/`pull`, no remote tags per operator policy |
| Parent | [docs/archive/v3.0-research/CRITIQUE-SOTA.md §13](../archive/v3.0-research/CRITIQUE-SOTA.md) |

> **🚨 ASPIRATIONAL vs ACTUAL**: this document was drafted 2026-09-24
> before any code shipped. §1-4 describe the design intent. **§5
> (Package structure)** was rewritten 2026-09-27 to reflect the
> actual layout (see the "Status of §5" callout). §8 (Workflow
> runtime) describes a mutable `Workflow` struct that **is NOT yet
> implemented** — v4-alpha.1 ships a fixed FSM in `vibe/pipeline.go`.
> If you are looking for what is actually shipping today, read
> [`docs/v4-status.md`](docs/v4-status.md) first.

---

## Table of contents

1. [Why v4 — the void and the redesign](#1-why-v4--the-void-and-the-redesign)
2. [Architectural decisions — the four movements](#2-architectural-decisions--the-four-movements)
3. [Security preservation — the 23 controls and 10 new invariants](#3-security-preservation--the-23-controls-and-10-new-invariants)
4. [Backends 2026 — research intents](#4-backends-2026--research-intents)
5. [Package structure](#5-package-structure)
6. [Invariants — INV-1..INV-15](#6-invariants--inv-1inv-15)
7. [Constitution contract](#7-constitution-contract)
8. [Workflow runtime](#8-workflow-runtime)
9. [Auto-update protocol](#9-auto-update-protocol)
10. [Installer fix](#10-installer-fix)
11. [Migration from v3.0-void to v4](#11-migration-from-v3.0-void-to-v4)
12. [Tests discipline](#12-tests-discipline)
13. [Contributing (preview)](#13-contributing-preview)
14. [SOTA criticism — workflow runtime (M1, 2026-09-28)](#14-sota-criticism--workflow-runtime-m1-2026-09-28)
15. [SOTA criticism — MCP ecosystem + spec-driven pattern (2026-09-28)](#15-sota-criticism--mcp-ecosystem--spec-driven-pattern-2026-09-28)

---

## 1. Why v4 — the void and the redesign

### 1.1 The deliberate void

`v3.0.0-docfix` was a 60-commit body of work between `v2.20.0` and
the tag `v3.0.0-void` (annotated, commit `7e080f0a`). It introduced
schema v30, abolished NLI, made `darkllm.Gateway` the sole LLM
entrypoint, removed the embedder, and produced the CRITIQUE-SOTA
L1–L13 (9.719 lines, 30 R-recos).

**The operator decided (2026-09-24): v3.0 is a deliberate void.**

Three reasons:

1. **The changes are large but cohesive** — applying them as
   incremental patches over `v2.20.0` would touch 39 internal
   packages and risk regressions that the CRITIQUE-SOTA already
   flagged as `needs_human`.
2. **The 30 R-recos demand structural changes** — not feature
   patches. R-4 (`IsArtifactCreating` hardcoded), R-10 (4 tools
   not registered), R-30 (10 phantom tools), R-15 (`sync.Once`
   race) cannot be fixed by adding code; they require
   reorganising code.
3. **The community will inherit v4** — v4 is designed for open-
   source community maintenance. v3.0-docfix inherited the v2.x
   package structure, which the CRITIQUE-SOTA §11 calls
   "1.5–2× the surface area that v4 should need" (see
   [CRITIQUE-SOTA §11.16](../archive/v3.0-research/CRITIQUE-SOTA.md)).

### 1.2 What v4 preserves

- All 10 operational invariants (INV-1..INV-10) from v3.0.
- All 23 Tier-1 security controls from the dark-research audit
  (§3 of this document).
- The vibe-loop paradigm — every state transition is audited,
  every drift verdict is persisted.
- Compatibility with the MCP protocol and the existing dark.db
  schema (with a migration path, §11).

### 1.3 What v4 changes

Four movements, in order of impact:

| Movement | From | To | Why |
|---|---|---|---|
| **M1. Workflow runtime** | Fixed FSM with 9 states + 8 events | Mutable workflow that the orchestrator rewrites | The agent needs to adapt the loop to the task, not run a fixed state machine. |
| **M2. Orchestrator = agent** | Engine holds policies (canaryActive, DefaultToolGrants INTERIM) | Engine is policy-free; the orchestrator defines policies via the workflow | Peer-to-peer means any agent is the orchestrator. Hardcoded policies break that. |
| **M3. Manifests, not code** | Tool list hardcoded; adapters compiled in | Every tool/adapter declared via `manifest.yaml` + Go struct | Community can add tools without PR to core. |
| **M4. Auto-update + research native** | Bolt-on updater; `dark-research-mcp` as separate server | Auto-update is a tool + MCP resource; 18 research tools are native | Install simplicity. Single deployment unit. |

These four movements collapse into one principle: **the agent is
the work-flow creator; the engine runs the workflow**.

---

## 2. Architectural decisions — the four movements

### M1. The vibe-loop is a workflow runtime, not a state machine

#### 2.1.1 The current model (v3.0)

`internal/vlp/`, `internal/vibeflow/`, `internal/vibecase/`
together define a fixed FSM with 9 states (`idle`, `drafting_spec`,
`spec_active`, `drift_judging`, `complete`, `needs_human`,
`aborted`, `delegating`, `restoring`) and 8 events. The engine
hardcodes the transitions. Adding a state requires editing three
files.

#### 2.1.2 The v4 model

The engine runs a `Workflow` that is **mutable data**. The
workflow defines its own states, events, transitions. The
orchestrator (the agent invoking dark-memory via MCP) can issue a
`modify_workflow` event that adds, removes, or rewires states and
transitions at runtime.

```go
// vibe/workflow.go
package vibe

type Workflow struct {
    ID           string                 // stable identifier across modifications
    Version      int                    // increments on every modification
    States       map[string]StateDef    // orchestrator-defined
    Events       map[string]EventDef    // orchestrator-defined
    Transitions  []Transition           // orchestrator-defined, ordered
    Author       string                 // who last modified (operator/agent id)
    Rationale    string                 // why this modification (LLM-generated)
    CreatedAt    time.Time
    ModifiedAt   time.Time
}

// vibe/engine.go
type Engine struct {
    workflow    Workflow         // mutable
    journal     Journal          // append-only, HMAC-chained
    judge       Judge            // drift verification
    store       Store            // INV-1 write_audit
    capabilities CapabilitySet    // declared by adapter manifests
}

func (e *Engine) Handle(ctx, event Event) (State, error) {
    // 1. Validate event against current workflow
    // 2. Find transition (with guard evaluation)
    // 3. Apply transition + journal entry
    // 4. If event is modify_workflow: validate, apply, journal, drift_judge the modification itself
}
```

#### 2.1.3 What this resolves

- **R-15** (`sync.Once` concurrent shutdown — see CRITIQUE-SOTA
  §13.2) — the engine is a pure runtime; idempotency lives in
  the workflow definition, where the orchestrator controls it.
- **R-24** (no property-based test for vibe-loop transitions) —
  the property test is now "any sequence of modifications +
  transitions is replayable from the journal". This is a stronger
  property.
- **R-28** (`internal/vlp/` package underused) — `vibe/` is the
  single package; `vlp`, `vibeflow`, `vibecase` are deleted.
- **§13 use case** — the orchestrator can now add `iterating_with_
  alternative_artifact` after a drift verdict instead of forcing
  the orchestrator to recreate the entire session.

#### 2.1.4 Property test (executable specification)

```go
// vibe/property_test.go
func TestWorkflow_AnyModificationPlusTransitionIsReplayable(t *testing.T) {
    e := NewEngine(testStore(), testJudge())
    w := DefaultWorkflow() // baseline 9 states, 13 transitions

    // Fuzz: generate 50 random modifications + transitions
    for i := 0; i < 50; i++ {
        mod := RandomModification(w)
        e.ApplyModification(mod) // journaled, drift_judged
        for j := 0; j < 5; j++ {
            e.Handle(testEvent())
        }
    }

    // Replay the journal from scratch
    e2 := NewEngine(testStore(), testJudge())
    e2.ReplayJournal() // must converge to e.Workflow()
}
```

### M2. The orchestrator IS the agent, not the engine

#### 2.2.1 The current model (v3.0)

`recall/assemble.go` hardcodes `canaryActive = false` (CRITIQUE-
SOTA §11 F/C4, R-8). `DefaultToolGrants` is `INTERIM` with a
security note that the operator must read to apply correctly
(CRITIQUE-SOTA §11 F/C3, R-3). The engine "knows" things about
security that the agent doesn't control.

#### 2.2.2 The v4 model

The engine has **no opinions**. All "policies" move to the
workflow that the orchestrator defines. The orchestrator decides:

- when to add a `researching_osint` state (and which research
  intents to allow)
- when to run `drift_judge` before advancing to `spec_active`
- when to delegate to a sub-agent via the `delegate` event
- when to halt and ask the operator (the `needs_human` transition
  is one option among many)
- when to modify the workflow to iterate (`modify_workflow`
  event with rationale + drift_judge)

This is **peer-to-peer in its maximum expression**: any MCP-
compatible agent is the orchestrator. There is no privileged
"admin" mode hardcoded.

#### 2.2.3 What this resolves

- **R-3** (`DefaultToolGrants INTERIM`) — the engine ships with
  zero tool grants; the workflow declares them per project.
- **R-8** (`canaryActive` hardcoded false) — the engine has no
  canary; the orchestrator runs `safety.ValidatePayload` via a
  `validate_event` guard on the relevant transition.
- **R-11** (`canaryActive` not propagated to recall layer) —
  same resolution: the orchestrator's workflow defines which
  transitions require canary validation.

### M3. Manifests, not code

#### 2.3.1 The current model (v3.0)

`internal/tools/vibe.go` registers 4 tools; `TOOLS_MAP.md`
documents 57; `IsArtifactCreating` lists 8 hardcoded. Adding a
tool requires editing the central registration table plus the
gate's hardcoded list.

#### 2.3.2 The v4 model

Every adapter and every tool declares a `manifest.yaml` (or a Go
`Manifest` constant for compile-time type safety). The engine
discovers them at boot.

```yaml
# adapters/store/sqlite/manifest.yaml
name: sqlite-store
version: 4.0.0
capabilities:
  - storage.read
  - storage.write
  - storage.migrate
  - audit.emit
requires:
  - capability: storage.migrate
    version: ">=1.0"
config:
  dsn: ${DARK_DB:-dark.db}
  pragma.journal_mode: WAL
  pragma.busy_timeout_ms: 5000
fallback_chain: []   # if non-empty, used on capability failure
```

```go
// tools/judge.go
package tools

// Manifest is the declarative contract for this tool.
// Discovery is automatic via go:embed over tools/*/manifest.yaml.
// No central registration table.
const Manifest = ToolManifest{
    Name:        "judge",
    Description: "LLM-as-judge verdict on an artifact",
    Capability:  "governance.judge",
    Requires:    []string{"capability:llm.provider"},
    Inputs:      JudgeInputSchema,
    Outputs:     JudgeOutputSchema,
    RedactArgs:    true,  // INV-13
    RedactOutput: true,  // INV-13
    QuotaHook:    "governance.judge",  // SessionQuotaPool
}

func Execute(ctx, inputs JudgeInput) (JudgeOutput, error) { /* ... */ }
```

#### 2.3.3 What this resolves

- **R-4** (`IsArtifactCreating` hardcoded to 8 tools) — the gate
  reads `tools/*/manifest.yaml` and discovers which tools are
  artifact-creating via the `ArtifactCreating: true` field.
- **R-10** (4 tools not registered) — discovery is exhaustive; a
  tool file without a manifest is a build error, not a runtime
  silent gap.
- **R-30** (10 phantom tools documented but unregistered) — the
  opposite: a manifest without an `Execute` function is a
  build error.
- **R-26** (no graceful degradation on LLM failure) — the
  `fallback_chain` field in manifests enables it declaratively.

### M4. Auto-update + research native

#### 2.4.1 Auto-update is a tool + MCP resource

The engine boots, checks `dark-memory://updates` (an MCP
resource), and exposes three tools:

| Tool | Inputs | Effect |
|---|---|---|
| `update_check()` | — | Pings `api.github.com/repos/opitacode/dark-memory-mcp/releases/latest`, compares with current version, returns `{current, latest, available: bool, url, sha256, signature}`. No side effects. |
| `update_apply(version?)` | version (default: latest) | Downloads binary to `%TEMP%/dark-mem-v4-<ver>-<sha>.bin`, verifies SHA256 + Ed25519 signature against `constitution.update.pubkey`, atomic replace of current binary, exit. The harness relaunches. |
| `update_config(notify, channel, auto_apply, check_on_start)` | full config | Updates `constitutions/default.yaml` `update.*` section with operator-specified policy. |

The MCP resource `dark-memory://updates` exposes the same state
for harnesses that prefer resource-poll over tool-call.

The **default** `auto_apply = false` per operator decision (2026-
09-24, security-first). Update is always explicit.

#### 2.4.2 Research is native

`dark-research-mcp` is **deleted** in v4. Its 18 capabilities
become native tools in `adapters/research/<intent>/`:

```
research_web        research_academic     research_code
research_cve        research_domain       research_dns
research_cert       research_ip           research_threat
research_email      research_dark         research_geo
research_news       research_multi        web_search
web_fetch           url_extract_components
text_anonymize
```

Each adapter declares its manifest with a `fallback_chain` of
backends (see §4 for the 2026 backend list). Every OSINT output
passes through the security gate (§3.4): `Redact` (INV-13) +
`text_anonymize` + `dark_ssd_prompt_injection_scan` (INV-15).

`dark-copilot` is **not mentioned** in dark-memory v4 code,
docs, comments, or tests. It is a separate product.

---

## 3. Security preservation — the 23 controls and 10 new invariants

The dark-research security audit (mapping 4 directories:
`dark-copilot/`, `dark-mem-research-workspace/`, `dark-agents/`,
`dark-agents-colombia-osint/`) inventoried **95 controls** across
13 categories. Of these, **23 are Tier 1–3 must-preserve** on v4
integration. The audit also surfaced **10 missing controls** that
v4 closes while it has the chance.

### 3.1 Tier 1 — must port verbatim (no integration without these)

| # | Control | Origin | Threat addressed |
|---|---|---|---|
| 1 | Capability tokens per process, constant-time verify | `dark-copilot/internal/policy/policy.go:186-242` (with regression fix at L235-238) | Token-bypass auth |
| 2 | HMAC-SHA-256 audit chain, per-process, with F32 seq-after-write fix | `dark-copilot/internal/security/security.go:196-411` | Audit tampering |
| 3 | Central `Redact` + recursive `RedactWalk` (with B4 nested-Bearer fix) | `dark-copilot/internal/security/security.go:563-815` | Credential leak via logs/CDP |
| 4 | `NewTrustedPath` (rejects `..`, symlink-replacement, tilde expansion) | `dark-copilot/internal/security/path.go:39-95` | Path traversal |
| 5 | Default-deny allowlist for tools | `dark-copilot/internal/policy/policy.go:84-120` | Escape from approved surface |
| 6 | `text_anonymize` + `extract_api_keys` as mandatory callers of any OSINT output | dark-research-mcp + dark-agents PII discipline | PII to LLM context |
| 7 | `dark_ssd_prompt_injection_scan` gate on every artifact URL fetched | dark-agents-v2 `.opencode/agent/dark-agents.md:163-167` | Indirect prompt injection |

### 3.2 Tier 2 — defense-in-depth (port before v4-alpha.2)

| # | Control | Origin |
|---|---|---|
| 8 | Per-tool quota with `SessionQuotaPool`-style isolation (500 calls/30 min default + 120 req/min network) | `dark-copilot/internal/quota/quota.go:60-418` |
| 9 | `scrub_pii: true` per-route for `jailbreak_target` and `redteam` buckets | dark-agents provider routing §02 |
| 10 | OSINT R5 tier-1 primary-source gate with `osint_sources` field in spec_json + commit trailer `[OSINT primary-sourced: ...]` | `dark-copilot/docs/OSINT_PROTOCOL.md:21-79` |
| 11 | Snake_case JSON tags + regression test | `dark-agents/docs/configuration.md:23-25` |
| 12 | `BinarySHA256` + `DARK_MEMORY_SHA256` env pin (self-hash check) | `dark-copilot/internal/security/security.go:481-532` |
| 13 | Tilde-expansion + symlink-rejection in every operator-facing path | `path.go` semantics |
| 14 | Pattern-level hard rules in constitution (`no offensive ops`, `scrub PII`, `respect rate limits`) | `dark-agents/patterns/*.md` |

### 3.3 Tier 3 — operational hardening

| # | Control |
|---|---|
| 15 | Version fingerprint (`dc version` style) on startup |
| 16 | Idle-timer + lazy-launch + kill-orphans safety net |
| 17 | `audit-privacy.ps1` lint as CI gate |
| 18 | Colombia-OSINT-style "IN scope / OUT of scope" + "NO list" in v4 constitution |
| 19 | R6 vibe-flow discipline (every tool result enters spec/drift/persist before reaching the operator) |

### 3.4 The 10 missing controls v4 closes

These are gaps the audit surfaced. Each maps to a new invariant
or a new module in v4.

| # | Missing control | v4 resolution |
|---|---|---|
| M1 | Cross-process audit chain (currently per-PID) | `security/audit/` module: per-session key derivation (Argon2id), cross-session composition. **→ INV-12** |
| M2 | Explicit SSRF module with DNS rebinding defense | `security/ssrf/` module: resolve-then-connect with re-resolve check, RFC1918/loopback/CGNAT blocklists, IPv4-mapped-IPv6 handling. **→ INV-14** |
| M3 | Per-domain policy (`<tool>:<action>` allow/deny/ask/prompt_once) | `constitution.security.policy` map. **→ INV-13** |
| M4 | Page-render prompt-injection scan before content reaches LLM | `security/injection/` module: pre-render scan via `dark_ssd_prompt_injection_scan`. **→ INV-15** |
| M5 | Mandatory `dark_ssd_pii_detect` gate with severity-based behavior | `security/pii/` module: low→log, medium→redact, high→halt. **→ INV-13** |
| M6 | Loud failure on key-handling boundary violations (no silent fallback to env) | `security/capability/` module: explicit `Use-DarkAgentSecrets` opt-in, no fallback. **→ INV-11** |
| M7 | Per-backend in-process token-bucket with operator-configurable budgets | `adapters/research/<intent>/manifest.yaml` `rate_limit` field. **→ INV-1** (audit on rate-limit hits) |
| M8 | Sandboxing of the v4 binary with tool allowlist deny-by-default | `constitution.security.tools.default_policy = deny`. **→ INV-13** |
| M9 | Operator-side observability via MCP tools (`security_status`, `security_rotate_token`, `security_verify_audit`) | `tools/security/` package. **→ INV-12, INV-13** |
| M10 | Cross-binary identity (one vault namespace, one audit namespace) | `dark-copilot` and `dark-eval` adopt `security/capability/` + `security/audit/` from v4. **Out of scope for v4 release**, documented as migration target. |

### 3.5 Threat model — consolidated

| Threat | Mitigations (primary → defense-in-depth) |
|---|---|
| Credential leak via logs / CDP / screenshots | Redact (Tier-1 #3) → recursive RedactWalk → shape-driven key mask → middleware on every tool call → fenced output |
| Audit-log tampering | HMAC-SHA-256 chain (Tier-1 #2) → per-process rotated file → 0o700 dir + 0o600 file → Windows DACL → cross-session composition (M1) |
| SSRF / hostile URL | Trusted-path helper (Tier-1 #4) → tool allowlist (Tier-1 #5) → `security/ssrf/` module (M2) → tier-1 primary-source gate (Tier-2 #10) |
| PII in OSINT output | `dark_ssd_pii_detect` mandatory gate (M5) → `text_anonymize` (Tier-1 #6) → per-route scrub (Tier-2 #9) → Ley 1581 alignment |
| Mis-attributed third-party claim | OSINT R5 gate (Tier-2 #10) → commit trailer → `osint_sources` field → cross-source triangulation |
| Prompt injection from OSINT content | `dark_ssd_prompt_injection_scan` (Tier-1 #7) → trust fences around untrusted content → page-render scan (M4) |
| Resource exhaustion / runaway agent | Per-tool quota (Tier-2 #8) → per-bridge `SessionQuotaPool` → RAM sampler → per-backend rate limits (M7) |
| Binary tampering | `BinarySHA256` self-hash (Tier-2 #12) + update protocol signature verification (§9) |
| Cross-bridge attack | `SessionQuotaPool` per-bridge isolation (Tier-2 #8) |

---

## 4. Backends 2026 — research intents

Investigation completed via `dark_research_web` and
`dark_research_code` (full results in dark-memory session logs).
The 13 intent backends that dark-research-mcp uses today, plus
the 5 standalone tools, are reorganised into v4-native adapters
with the following 2026 backend selections.

### 4.1 Changes from v3.0 dark-research-mcp

| Intent | v3.0 backend | v4 2026 backend | Rationale |
|---|---|---|---|
| **web** | DuckDuckGo | DuckDuckGo (default) + Brave (paid) + SearXNG (self-hosted) | Microsoft Bing Search APIs retired Aug 2025. Brave free tier shrank to $5/month for ~1k queries. |
| **academic** | OpenAlex + arXiv + Semantic Scholar | OpenAlex (default — 240M+ works, no auth) + arXiv (preprints) + Semantic Scholar (with key) | OpenAlex dominance in 2026; arXiv for preprints; Semantic Scholar remains volume-gated. |
| **code** | crates.io + npm + GitHub | crates.io + npm + **Ecosyste.ms** (free, aggregates 7M+ packages) | Ecosyste.ms gives broader OSS coverage than GitHub search alone. |
| **cve** | OSV.dev + NVD | OSV.dev + NVD + **GitHub Advisory Database** + **CISA KEV** | GitHub Advisory (free with token) has cross-ecosystem aliases. CISA KEV is tier-1 for "actively exploited". |
| **cert** | crt.sh | crt.sh + Certspotter + Facebook CT monitor | crt.sh still the most complete; Certspotter/FBCTM for real-time alerts. |
| **ip** | ip-api.com + RIPE | IPinfo.io + MaxMind GeoLite2 + ip-api.com (fallback) | IPinfo.io and MaxMind lead city-accuracy (70-78% in NA/Western EU). |
| **threat** | abuse.ch URLhaus + AlienVault OTX | AlienVault OTX + **abuse.ch family** (URLhaus + Feodo Tracker + MalwareBazaar + ThreatFox) + CISA KEV + **FIRST EPSS** | abuse.ch expanded; EPSS scores exploit probability. |
| **news** | GDELT + Wayback | GDELT (default) + Currents API + NewsAPI | GDELT remains best free, no key, 100+ languages. |
| **email** | HIBP | HIBP (unchanged — requires key) | No superior replacement found. |
| **dns** | Cloudflare DoH + Google DoH | Cloudflare DoH + Google DoH + Quad9 | Quad9 (9.9.9.9) added for privacy-first filtering. |
| **domain** | rdap.org | rdap.org + RIPE DB (unchanged) | No change. |
| **dark** | Ahmia.fi | Ahmia.fi + Torch (tor.socks5_url) | Ahmia remains clearnet index of .onion. |
| **geo** | Nominatim | Nominatim (unchanged — best free) | No change. |

### 4.2 Standalone tools (unchanged)

- `web_search(query, limit?, freshness?)` — Brave + DuckDuckGo
- `web_fetch(url, max_length?)` — sanitize HTML to markdown
- `url_extract_components(url)` — parse + validate (security gate)
- `text_anonymize(text, entity_types?)` — PII masking

### 4.3 Backend manifest example

```yaml
# adapters/research/cve/manifest.yaml
name: research-cve
version: 4.0.0
capabilities: [research.cve]
requires:
  - capability: http.client
config:
  primary: osv.dev
  fallback_chain: [nvd, gh_advisory, cisa_kev]
  rate_limits:
    osv.dev: "5/s"
    nvd: "5/30s"
    cisa_kev: "1/s"
  timeout_ms: 30000
  requires_api_key: false   # none of the 4 backends require auth
security:
  redact_output: true       # INV-13
  pii_gate: mandatory       # M5
  injection_scan: true      # INV-15
```

---

## 5. Package structure

> **Status of §5 — REWRITTEN 2026-09-27**: this section was rewritten
> to reflect the actual `internal/v4alpha/` layout. The original
> aspirational layout (`core/`, `vibe/`, `security/`, `adapters/`,
> `governance/`, `constitutions/`, `tools/`, `installer/`,
> `extensions/`) is preserved below as a "future state" appendix so
> the design intent isn't lost. Anything not in the "actual layout"
> diagram below is NOT yet built.

### 5.0 Actual layout (v4-alpha.1, 2026-09-27)

```
internal/v4alpha/                         # every internal package is v4-alpha
├── agent_memory/                         # §5.0.1
├── audit/                                # §5.0.2
├── judge/                                # §5.0.3
├── manifest/                             # §5.0.4
├── research/                             # §5.0.5 (placeholder, BUG-9)
├── session/                              # §5.0.6
├── store/                                # §5.0.7
├── transport/
│   └── mcp/                              # §5.0.8
└── vibe/                                 # §5.0.9

cmd/
└── dark-memory-v4/                       # §5.0.10 — the binary
```

#### 5.0.1 `agent_memory/` — operator-scoped notebook

- `agent_memory.go` — `Save`, `Get`, `List`, `Recall`, `Archive`,
  `Update` (added BUG-8).
- `agent_memory_test.go` — 18 tests (14 + 4 Update).
- Schema: `agent_memory` (base) + `agent_memory_fts` (regular FTS5,
  INV-17).
- Kinds: `note`, `observation`, `decision`, `finding`, `todo`, `link`,
  `context`.
- Tenancy: `operator` field (string), not `project_id` (see §6.4).
- See [`docs/AGENT_MEMORY_SCHEMA.md`](docs/AGENT_MEMORY_SCHEMA.md) for
  the full table + FTS5 contract.

#### 5.0.2 `audit/` — write_audit + Writer

- `writer.go` — `Writer.Write(ctx, event)` appends to `write_audit`.
- Schema: `write_audit` table (actor, session_id, project_id,
  payload_json, created_at).
- INV-1 enforcement: every `Save*` in v4-alpha.1 that emits an audit
  row uses `WithTx` so the audit insert + data insert are atomic.
- Currently NOT called by `agent_memory.Save` — that's a BUG-9
  follow-up (closes the only INV-1 gap in v4-alpha.1).

#### 5.0.3 `judge/` — drift verdict abstraction

- `client.go` — `Judge` interface: `Evaluate(ctx, artifact_ref,
  spec_intent) → Verdict`. Verdict ∈ {`aligned`, `drift_detected`,
  `needs_human`}, Confidence ∈ [0, 1].
- `noop.go` — `NoOpJudge` returns `VerdictAligned + Confidence 1.0`.
  v4-alpha.1 default.
- LLM-backed `Judge` (HTTP client to `DARK_JUDGE_PROVIDER`) lands in
  BUG-9. The interface is fixed so swapping `NoOpJudge` → real
  `Judge` is a one-line change in `vibe.NewPipeline`.

#### 5.0.4 `manifest/` — RBAC + capability layer

- `cap_store.go` — capability grants (`Grant`, `Revoke`,
  `RevokeIdempotent`). Wrapped in `WithTx` per INV-16.
- `cap_token.go` — token minting + verification (INV-11 stub; full
  constant-time compare lands in alpha.3).
- `manifest.go` — declarative manifest parser (alpha.2 will move to
  `manifests/` directory per M3 aspirational design).

#### 5.0.5 `research/` — placeholder (BUG-9)

- Directory exists; no tools yet.
- BUG-9 adds `topic`, `recall`, `resume_thread` as no-op stubs that
  return `{status: "no_backends_registered"}`. Real backends (web,
  academic, code, cve, …) land in alpha.3 with `DARK_RESEARCH_*
  env` hooks.

#### 5.0.6 `session/` — open/read/heartbeat/close

- `session.go` — `Store` with `Open`, `Read`, `Heartbeat`, `Close`.
- `session_test.go` + `session_property_test.go` — covers open/read
  invariants, concurrent heartbeats, session-id reuse rules.
- INV-2: per-session scoping on Recall paths.

#### 5.0.7 `store/` — INV-16 dark-db contract

- `open.go` — `OpenSQLite(dsn) (*sql.DB, error)` enforces the BUG-5
  pragma set + pool bounds.
- `tx.go` — `WithTx(ctx, db, fn) error` wraps the function in
  `LevelSerializable`.
- `store_test.go` — 10 tests pinning the contract.
- Every other package that opens a dark-db MUST go through these
  helpers.

#### 5.0.8 `transport/mcp/` — the 25 tools wire surface

- `server.go` — `NewServer(db) *Server`, registers 25 tools across
  7 namespaces. Applies `WithWorkerPoolSize(1)` manually to the
  underlying mcp-go `StdioServer` (BUG-7).
- `health.go`, `memory.go`, `session.go`, `observability.go`,
  `error_obs.go`, `policy.go`, `vibe.go` — one file per namespace.
- `helpers.go` — `nowRFC3339()` + the bindArgs / callTool
  infrastructure shared across tool files.
- `server_test.go` — 13 tests covering the 25 tools via JSON-RPC
  drives.

#### 5.0.9 `vibe/` — fixed FSM pipeline (not mutable workflow yet)

- `pipeline.go` — `Pipeline.Publish(spec, artifact) → Verdict`,
  `Status(artifact_id)`, `ResolveDrift(drift_id, decision, note)`.
  Three stores: `Specs()`, `Artifacts()`, `Drifts()`.
- `spec.go`, `artifact.go`, `drift.go` — three domain types + their
  per-type stores.
- The aspirational `Workflow` struct (mutable states/events/transitions
  + modify_workflow event + drift_judge of modifications) is NOT
  implemented. The mutable workflow runtime lands in v4.0.0-beta.

#### 5.0.10 `cmd/dark-memory-v4/` — the binary

- 5 subcommands: `help`, `version`/`-v`, `migrate`, `schema-status`,
  `serve`.
- `serve.go` wires `store.OpenSQLite` + `applyAllSchemas` +
  `stampSchemaVersion` + `transport/mcp.NewServer(db).ServeStdio(ctx,
  os.Stdin, stdout)`.
- 23 tests in `cmd/dark-memory-v4/*_test.go`.

### 5.0.11 Tool inventory (25 of 57 canonical)

See [`docs/v4-status.md §1`](docs/v4-status.md) for the full table.
Summary: 25 registered, 32 deferred to BUG-9+.

### 5.0.12 Schema

- Schema version stamped: `v4alpha/2026-09-27/001`.
- 8 tables: `audit_log`, `agent_memory`, `agent_memory_fts`,
  `schema_migrations`, `sessions`, `capabilities`, `manifest`,
  `vibe_specs`, `vibe_artifacts`, `vibe_drifts`.
- All schema is in `CreateSchema()` functions co-located with each
  package. The `cmd/dark-memory-v4/migrate.go` orchestrator calls
  them in dependency order: audit → session → manifest/cap →
  manifest/meta → vibe/spec → vibe/artifact → vibe/drift →
  agent_memory.

### 5.0.13 Tests

- `internal/v4alpha/agent_memory/`: 18 tests
- `internal/v4alpha/audit/`: covered indirectly via session + vibe
  tests
- `internal/v4alpha/judge/`: NoOpJudge trivial
- `internal/v4alpha/manifest/`: 5+ concurrent stress tests (cap_store)
- `internal/v4alpha/session/`: open/read/heartbeat + property tests
- `internal/v4alpha/store/`: 10 contract tests
- `internal/v4alpha/transport/mcp/`: 13 JSON-RPC drive tests
- `internal/v4alpha/vibe/`: pipeline + spec/artifact/drift stores
- `cmd/dark-memory-v4/`: 23 subcommand tests
- **Total: 37 PASS / 0 FAIL** (2026-09-27)

### 5.0.14 What's NOT here

| Aspirational | Status | When |
|---|---|---|
| `core/` (pure types) | Inline in `vibe/` and `agent_memory/` | alpha.2 |
| `security/` (redact, ssrf, pii, injection, capability) | NOT STARTED | alpha.3 — INV-11..INV-15 |
| `adapters/` (LLM, credentials, embedding, update) | NOT STARTED | alpha.3 |
| `governance/` | Inline in `vibe/` + `judge/` | alpha.2 |
| `constitutions/` (YAML files) | Hardcoded in `transport/mcp/policy.go` | BUG-9 — `policy_registry` table |
| `tools/` (manifest-based auto-discovery) | `transport/mcp/*` files | M3 — deferred past alpha.2 |
| `installer/` | Lives in `npm/wrapper/` (legacy v2) | Carry over |
| `extensions/` | NOT STARTED | alpha.3+ — community pack loader |

---

### 5.1 Aspirational layout (original, preserved for design intent)

> The diagram below is from the 2026-09-24 design draft. It is the
> target shape for v4.0.0 GA. v4-alpha.1 implements roughly 30% of
> it (see §5.0 above for the actual layout).

```
dark-memory-v4/
├── core/                              # Pure types. Zero I/O. No side effects.
│   ├── artifact.go                    # Artifact, ArtifactRef, ArtifactType, ArtifactHash
│   ├── spec.go                        # Spec, SpecTask, SpecIntent, VibeCase
│   ├── drift.go                       # DriftReport, Verdict, Confidence, DispersionSummary
│   ├── session.go                     # Session, Operator, Project, Constitution
│   ├── audit.go                       # WriteAudit, AuditEntry, Actor, WritePath
│   └── primitives.go                  # Hash, Signature, Identity, Capability
│
├── vibe/                              # Workflow runtime. The heart.
│   ├── engine.go                      # Engine: Handle/Modify/Snapshot
│   ├── workflow.go                    # Workflow data (states/events/transitions)
│   ├── workflow_default.go            # DefaultWorkflow() — baseline 9 states, 13 transitions
│   ├── modification.go                # modify_workflow event + validation
│   ├── modification_drift.go          # Drift_judge the modification itself
│   ├── journal.go                     # Append-only event log (HMAC-chained, cross-session composition)
│   ├── observer.go                    # Hooks: BeforeTransition, AfterTransition, OnError
│   ├── guards.go                      # Default guard library (HasOperator, HasSpecIntent, ...)
│   └── property_test.go               # R-24: any modification+transition is replayable
│
├── security/                          # Promoted from dark-copilot internal/security
│   ├── redact/                        # Central Redact + RedactWalk + isSensitiveKey (Tier-1 #3)
│   ├── path/                          # NewTrustedPath (Tier-1 #4)
│   ├── capability/                    # Token generation + constant-time verify (Tier-1 #1, M6)
│   ├── audit/                         # HMAC-SHA-256 chain + cross-session composition (Tier-1 #2, M1)
│   ├── ssrf/                          # URL validator + DNS rebinding defense + blocklist (M2)
│   ├── pii/                           # dark_ssd_pii_detect + anonymize (M5)
│   ├── injection/                     # dark_ssd_prompt_injection_scan 8 categories (Tier-1 #7, M4)
│   ├── quota/                         # SessionQuotaPool + per-backend rate limits (Tier-2 #8, M7)
│   └── policy/                        # Default-deny allowlist + per-domain policy (Tier-1 #5, M3, M8)
│
├── adapters/                          # Pluggable. Loaded from manifests at boot.
│   ├── store/
│   │   ├── sqlite/                    # Default backend
│   │   ├── postgres/                  # Optional
│   │   └── memory/                    # Test/dev
│   ├── llm/
│   │   ├── anthropic/
│   │   ├── openai/
│   │   ├── deepseek/
│   │   └── custom/                    # Bring-your-own
│   ├── credentials/
│   │   ├── keyring/                   # OS keyring
│   │   ├── env/                       # Env-var fallback (explicit opt-in only, M6)
│   │   └── vault/                     # DPAPI Windows / Keychain macOS / Secret Service Linux
│   ├── audit/
│   │   ├── local/                     # Default: write to store
│   │   ├── merkle/                    # Tamper-evident chain
│   │   └── remote/                    # Opt-in
│   ├── federation/                    # Opt-in
│   │   ├── crdt/
│   │   └── git-ots/
│   ├── embedding/
│   │   ├── sqlite-vec/
│   │   └── none/                      # Default: lexical-only
│   ├── research/                      # 18 native research tools (replaces dark-research-mcp)
│   │   ├── web/   academic/   code/   cve/   domain/
│   │   ├── dns/   cert/   ip/   threat/   email/
│   │   ├── dark/  geo/   news/
│   │   └── multi/                     # Meta-router
│   └── update/
│       ├── github/                    # GitHub releases API
│       ├── verify/                    # SHA256 + Ed25519 signature
│       └── apply/                     # Atomic binary replace
│
├── governance/
│   ├── constitution/
│   │   └── loader.go                  # YAML loader + INV-4 watchdog (SHA verify)
│   ├── judge/                         # LLM-as-judge abstraction
│   ├── consensus/                     # N-shot aggregation + dispersion
│   ├── mods/                          # Mod loader with version pin + INV-6 sanitization
│   └── drift/                         # Drift detection logic
│
├── constitutions/                     # YAML, versioned, SHA-pinned
│   ├── default.yaml                   # Default for v4-alpha.1
│   ├── strict.yaml                    # Stricter alternative
│   └── research-only.yaml             # Example for OSINT-only deployments
│
├── transport/                         # Thin adapters. No business logic.
│   ├── mcp/                           # The canonical interface (75 tools auto-discovered)
│   ├── http/                          # REST for non-MCP integrations
│   ├── cli/                           # Operator CLI
│   └── grpc/                          # Internal high-perf
│
├── tools/                             # 75 tools auto-discovered via manifests
│   ├── vibe/                          # 14 vibe-loop tools (publish, spec, modify, ...)
│   ├── memory/                        # 6 agent_memory tools
│   ├── governance/                    # 8 judge/consensus/drift tools
│   ├── observability/                 # 4 error/writes/health tools
│   ├── research/                      # 18 research tools
│   ├── security/                      # 3 (security_status, security_rotate_token, security_verify_audit) [M9]
│   ├── admin/                         # 5 schema/migrate/vacuum
│   └── update/                        # 3 update_check / update_apply / update_config
│
├── installer/                         # The npx installer fix
│   ├── npm/                           # package.json + postinstall
│   ├── mcpb/                          # mcpb platform shim
│   └── tests/                         # E2E install tests (Win/Mac/Linux)
│
└── extensions/                        # Community-maintained, separate repo path
    ├── redteam/                       # Example mod pack (gated)
    ├── exporters/                     # Prometheus, OTel
    └── apps/                          # Example applications
```

```
dark-memory-v4/
├── core/                              # Pure types. Zero I/O. No side effects.
│   ├── artifact.go                    # Artifact, ArtifactRef, ArtifactType, ArtifactHash
│   ├── spec.go                        # Spec, SpecTask, SpecIntent, VibeCase
│   ├── drift.go                       # DriftReport, Verdict, Confidence, DispersionSummary
│   ├── session.go                     # Session, Operator, Project, Constitution
│   ├── audit.go                       # WriteAudit, AuditEntry, Actor, WritePath
│   └── primitives.go                  # Hash, Signature, Identity, Capability
│
├── vibe/                              # Workflow runtime. The heart.
│   ├── engine.go                      # Engine: Handle/Modify/Snapshot
│   ├── workflow.go                    # Workflow data (states/events/transitions)
│   ├── workflow_default.go            # DefaultWorkflow() — baseline 9 states, 13 transitions
│   ├── modification.go                # modify_workflow event + validation
│   ├── modification_drift.go          # Drift_judge the modification itself
│   ├── journal.go                     # Append-only event log (HMAC-chained, cross-session composition)
│   ├── observer.go                    # Hooks: BeforeTransition, AfterTransition, OnError
│   ├── guards.go                      # Default guard library (HasOperator, HasSpecIntent, ...)
│   └── property_test.go               # R-24: any modification+transition is replayable
│
├── security/                          # Promoted from dark-copilot internal/security
│   ├── redact/                        # Central Redact + RedactWalk + isSensitiveKey (Tier-1 #3)
│   ├── path/                          # NewTrustedPath (Tier-1 #4)
│   ├── capability/                    # Token generation + constant-time verify (Tier-1 #1, M6)
│   ├── audit/                         # HMAC-SHA-256 chain + cross-session composition (Tier-1 #2, M1)
│   ├── ssrf/                          # URL validator + DNS rebinding defense + blocklist (M2)
│   ├── pii/                           # dark_ssd_pii_detect + anonymize (M5)
│   ├── injection/                     # dark_ssd_prompt_injection_scan 8 categories (Tier-1 #7, M4)
│   ├── quota/                         # SessionQuotaPool + per-backend rate limits (Tier-2 #8, M7)
│   └── policy/                        # Default-deny allowlist + per-domain policy (Tier-1 #5, M3, M8)
│
├── adapters/                          # Pluggable. Loaded from manifests at boot.
│   ├── store/
│   │   ├── sqlite/                    # Default backend
│   │   ├── postgres/                  # Optional
│   │   └── memory/                    # Test/dev
│   ├── llm/
│   │   ├── anthropic/
│   │   ├── openai/
│   │   ├── deepseek/
│   │   └── custom/                    # Bring-your-own
│   ├── credentials/
│   │   ├── keyring/                   # OS keyring
│   │   ├── env/                       # Env-var fallback (explicit opt-in only, M6)
│   │   └── vault/                     # DPAPI Windows / Keychain macOS / Secret Service Linux
│   ├── audit/
│   │   ├── local/                     # Default: write to store
│   │   ├── merkle/                    # Tamper-evident chain
│   │   └── remote/                    # Opt-in
│   ├── federation/                    # Opt-in
│   │   ├── crdt/
│   │   └── git-ots/
│   ├── embedding/
│   │   ├── sqlite-vec/
│   │   └── none/                      # Default: lexical-only
│   ├── research/                      # 18 native research tools (replaces dark-research-mcp)
│   │   ├── web/   academic/   code/   cve/   domain/
│   │   ├── dns/   cert/   ip/   threat/   email/
│   │   ├── dark/  geo/   news/
│   │   └── multi/                     # Meta-router
│   └── update/
│       ├── github/                    # GitHub releases API
│       ├── verify/                    # SHA256 + Ed25519 signature
│       └── apply/                     # Atomic binary replace
│
├── governance/
│   ├── constitution/
│   │   └── loader.go                  # YAML loader + INV-4 watchdog (SHA verify)
│   ├── judge/                         # LLM-as-judge abstraction
│   ├── consensus/                     # N-shot aggregation + dispersion
│   ├── mods/                          # Mod loader with version pin + INV-6 sanitization
│   └── drift/                         # Drift detection logic
│
├── constitutions/                     # YAML, versioned, SHA-pinned
│   ├── default.yaml                   # Default for v4-alpha.1
│   ├── strict.yaml                    # Stricter alternative
│   └── research-only.yaml             # Example for OSINT-only deployments
│
├── transport/                         # Thin adapters. No business logic.
│   ├── mcp/                           # The canonical interface (75 tools auto-discovered)
│   ├── http/                          # REST for non-MCP integrations
│   ├── cli/                           # Operator CLI
│   └── grpc/                          # Internal high-perf
│
├── tools/                             # 75 tools auto-discovered via manifests
│   ├── vibe/                          # 14 vibe-loop tools (publish, spec, modify, ...)
│   ├── memory/                        # 6 agent_memory tools
│   ├── governance/                    # 8 judge/consensus/drift tools
│   ├── observability/                 # 4 error/writes/health tools
│   ├── research/                      # 18 research tools
│   ├── security/                      # 3 (security_status, security_rotate_token, security_verify_audit) [M9]
│   ├── admin/                         # 5 schema/migrate/vacuum
│   └── update/                        # 3 update_check / update_apply / update_config
│
├── installer/                         # The npx installer fix
│   ├── npm/                           # package.json + postinstall
│   ├── mcpb/                          # mcpb platform shim
│   └── tests/                         # E2E install tests (Win/Mac/Linux)
│
└── extensions/                        # Community-maintained, separate repo path
    ├── redteam/                       # Example mod pack (gated)
    ├── exporters/                     # Prometheus, OTel
    └── apps/                          # Example applications
```

### 5.1 Package deletion (from v3.0-docfix)

| Deleted package | Reason |
|---|---|
| `internal/vl p/` | Replaced by `vibe/` single package |
| `internal/vibeflow/` | Replaced by `vibe/` single package |
| `internal/vibecase/` | Replaced by `vibe/workflow_default.go` |
| `internal/discipline/` | Discipline policies move to the workflow the orchestrator defines |
| `internal/nli/` | Already abolished in v3.0.0; stays deleted |
| `internal/embedder/` | Already abolished in v2.22.0; stays deleted |
| `dark-research-mcp/` (separate repo) | 18 capabilities now in `adapters/research/` |
| `dark-copilot/` | Not mentioned in dark-memory v4; separate product |

---

## 6. Invariants — INV-1..INV-15

v4 extends the 10 v3.0 invariants (preserved verbatim) with 5 new
invariants derived from the security audit (§3).

### 6.1 Preserved invariants (verbatim)

INV-1 through INV-10 keep their statements from
[INVARIANTS.md](../INVARIANTS.md). The mechanical guarantees
remain in `Store.Save*` interfaces and `tests/invariants/`. v4
adds property tests on top (R-24).

### 6.2 New invariants (INV-11..INV-15)

#### INV-11 — capability token per process, constant-time verify

**Statement**: Every MCP request to dark-memory v4 carries a
`capability_token` field. The server verifies it via
`crypto/subtle.ConstantTimeCompare` against a 32-byte CSPRNG
token minted at boot and persisted to `vault/`. Constant-time
comparison is mandatory (no early-return).

**Why**: Prevents token-bypass auth via timing attacks. The
dark-copilot regression at `policy.go:235-238` (a missing
`return` on early-exit) is the exact failure mode this invariant
prevents; the constant-time compare path makes that bug class
impossible.

**Enforced at**: `transport/mcp/middleware.go::VerifyCapability`.

**Defensive test**: `tests/invariants/inv11_test.go::
TestInv11_ConstantTimeCompare_NoEarlyReturn` — fuzzes 10k pairs
of tokens, asserts no measurable timing difference between
matching and non-matching pairs (within statistical noise floor).

**Operator signal**: `security_status` tool reports the token's
SHA-256 (never the value), mint timestamp, and last rotation.

#### INV-12 — HMAC-SHA-256 audit chain with cross-session composition

**Statement**: Every state transition in `vibe.Engine` writes an
entry to a per-process HMAC-SHA-256 chained audit log. Per-
session keys derived from operator passphrase (Argon2id) sign
the entries. Cross-session composition produces a single
verifiable timeline.

**Why**: The v3.0 chain is per-PID; an attacker (or buggy code
path) moving bytes between chain files goes unnoticed. The cross-
session composition closes this gap.

**Enforced at**: `security/audit/chain.go::Append` +
`security/audit/composition.go::ComposeAcrossSessions`.

**Defensive test**:
- `tests/invariants/inv12_test.go::TestInv12_ChainDetectsTamper`
  — mutates one byte; `Verify` returns the tampered offset.
- `tests/invariants/inv12_test.go::TestInv12_CrossSessionComposes`
  — three concurrent sessions with shared passphrase; the
  composed timeline is byte-identical to the union of the three
  chains.

**Operator signal**: `security_verify_audit` tool runs `Verify`
and returns the chain head + last breach (if any). The operator
runs this on a cron (or via the audit observability dashboard).

#### INV-13 — redact-before-log, mandatory on all output paths

**Statement**: Every output path (logs, errors, screenshots
metadata, network events, MCP responses, OSINT results) goes
through `security/redact.Redact` + `security/redact.RedactWalk`
before reaching the operator, the LLM, or the audit log. The
shape-driven key mask (`isSensitiveKey` for `*_token`, `*_key`,
`*_secret`, `*_password`) is defense-in-depth when regex misses
placeholder/test values.

**Why**: A single credential leak via logs/CDP/screenshots
cancels all other security work. INV-13 is the central exit
point for sensitive material.

**Enforced at**: `security/redact/middleware.go::RedactMiddleware`
applied to every transport layer + every tool's output wrapper.

**Defensive test**: `tests/invariants/inv13_test.go::
TestInv13_RedactWalkCatchesNestedBearer` — passes a payload with
Bearer header nested 5 levels deep inside a JSON map; asserts the
redacted output has no Bearer token.

**Operator signal**: `security_status` reports
`redactor_pattern_count` (number of regex patterns compiled) and
`shape_keys_count`. A regression test fails if either count drops
without an explicit ADR.

#### INV-14 — SSRF guard on all URL-accepting tools

**Statement**: Every tool that accepts a URL (`web_fetch`,
`url_extract_components`, `research_*` adapters) routes through
`security/ssrf.ValidateURL`. The validator: (a) blocks RFC1918
private IPs, loopback (127.0.0.0/8, ::1), CGNAT (100.64.0.0/10),
link-local (169.254.0.0/16); (b) resolves DNS, then re-resolves
after a brief delay, comparing results to defeat DNS rebinding;
(c) handles IPv4-mapped-IPv6 correctly.

**Why**: An LLM-driven SSRF via `artifact_url` or `web_fetch`
allows exfiltration to internal services. The dark-research docs
mention SSRF guards in agent.md prose but no Go enforcement; v4
makes it mechanical.

**Enforced at**: `security/ssrf/validate.go::ValidateURL` called
from `transport/mcp/middleware.go::SanitizeToolArgs` for any
URL-bearing tool.

**Defensive test**:
- `tests/invariants/inv14_test.go::TestInv14_BlocksPrivateIPs` —
  exhaustively tests each RFC1918 range + IPv6 equivalents.
- `tests/invariants/inv14_test.go::TestInv14_DNSRebindingDefense`
  — uses a controlled DNS server that returns public IP first,
  private IP second; asserts `ValidateURL` rejects.

**Operator signal**: `security_status` reports the SSRF
blocklist size (number of CIDR ranges). The v4-alpha.1 default
ships with 9 ranges; new ranges require an ADR.

#### INV-15 — prompt_injection_scan gate on every artifact URL

**Statement**: Every artifact URL fetched via `web_fetch`,
`research_*`, or `vibe_publish(artifact_url)` runs through
`security/injection.ScanURL` before the response reaches the
LLM context. The scanner detects 8 categories: instruction_
override, role_hijack, system_prompt_leak, tool_injection,
exfiltration, jailbreak, encoding_tricks, context_poisoning.

**Why**: OSINT content frequently contains prompt-injection
attempts (e.g., scraped blog comments). Without a pre-render
scan, the LLM treats injected instructions as operator
instructions.

**Enforced at**: `security/injection/scan_url.go::ScanURL` +
`transport/mcp/middleware.go::InjectScanWrapper` applied to every
URL-returning tool.

**Defensive test**: `tests/invariants/inv15_test.go::
TestInv15_DetectsAll8Categories` — fixture corpus with one example
per category; asserts the scanner reports the correct category.

**Operator signal**: `security_status` reports `injection_scan_
hits_today` and `injection_scan_blocks_today`. Per-bucket policy:
benign→log, research→log, jailbreak_target→redact+warn,
sensitive→block+halt.

#### INV-16 — dark-db concurrency contract for multi-agent workloads

**Statement**: Every `*sql.DB` opened against the dark-db (the SQLite
file at `<UserConfigDir>/dark-agents/dark.db` and per-project
siblings) MUST go through `internal/v4alpha/store.OpenSQLite` so the
DSN carries the BUG-5 pragma set (`busy_timeout=5000`,
`journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=1`,
`wal_autocheckpoint=1000`, `cache_size=-2000`, `temp_store=MEMORY`)
and the connection pool is bounded (`MaxOpenConns=8`,
`MaxIdleConns=4`, `ConnMaxIdleTime=5m`). Every read-modify-write
sequence over the dark-db MUST go through `store.WithTx`
(`sql.LevelSerializable`).

**Why**: dark-db is shared across multiple agents, multiple
operator sessions, and the BUG-5 stress test (2026-09-27) confirmed
that the default `sql.Open("sqlite", dsn+"?_pragma=foreign_keys(1)")`
deadlocks under concurrent writers on Windows within ~30s (16 goroutines
opening the same file, 2/16 failed with `SQLITE_BUSY` from the
`PingContext` that drives the `journal_mode` conversion). Production
agents writing concurrently would silently corrupt audit trails or
hang the MCP transport.

The chosen pragma set is grounded in four tier-1 sources:

  1. `sqlite.org/wal.html §2.2`: "WAL provides more concurrency as
     readers do not block writers and a writer does not block
     readers. ... since there is only one WAL file, there can only
     be one writer at a time."
  2. `sqlite.org/pragma.html#synchronous`: `synchronous=NORMAL` +
     WAL means writers never fsync — only the checkpoint does, which
     runs in the background.
  3. `pkg.go.dev/modernc.org/sqlite` Performance §: maintainer
     verbatim "Bound the pool with `sql.DB.SetMaxOpenConns` and do
     not issue a periodic query before the previous one has
     returned."
  4. `sqlite.org/wal.html §11` (WAL-reset bug): fixed in SQLite
     3.51.3. We embed SQLite 3.53.2 via modernc.org/sqlite v1.53.0
     (`CLAUDE.md` of the modernc repo), so we are patched.

**Enforced at**: `internal/v4alpha/store/open.go::OpenSQLite` and
`internal/v4alpha/store/tx.go::WithTx`. Direct `sql.Open("sqlite",
...)` against a dark-db path is a violation; the code review rule
is "search for `sql.Open` and verify the path is `:memory:` or a
non-dark-db temporary file".

**Defensive tests**: `internal/v4alpha/store/store_test.go` (10
tests) and `internal/v4alpha/manifest/cap_store_*test.go` (5
concurrent tests). Specifically:

  - `TestOpenSQLite_DSNContainsAllPragmas` — pins the pragma list.
  - `TestOpenSQLite_JournalModeIsWAL` — engine-level WAL check.
  - `TestOpenSQLite_BusyTimeoutApplied` — 5000ms landed.
  - `TestOpenSQLite_PoolBounds` — `MaxOpenConnections == 8`.
  - `TestOpenSQLite_ConcurrentOpens` — 16 goroutines race (T7).
  - `TestConcurrent_Grant_SameID` — T2 (32 goroutines, same id).
  - `TestConcurrent_GrantThenRevoke_SameID` — T3 (16 revokers).
  - `TestStress_10k_Writes` — T4 (no deadlock under 10k inserts).
  - `TestPoolExhaustion_200Goroutines_Pool8` — T5 (200 goroutines).
  - `TestMultiDBIsolation` — T8 (cross-DB independence).

**Operator signal**: the `db.Stats()` snapshot is exposed via the
`dark_db_status` tool (planned for v4-alpha.2). Until then, the
test suite enforces the contract; a failure of any of the 10 tests
above is the alert.

### 6.3 Invariant-to-module quick reference

| Invariant | Module | Enforcement site |
|---|---|---|
| INV-1 | `adapters/store/sqlite/` | `SaveAuditAtomic` |
| INV-2 | `adapters/store/sqlite/` | `RecallScoped` |
| INV-3 | `transport/mcp/middleware.go` | `CanaryGuard` |
| INV-4 | `governance/constitution/loader.go` | `WatchdogVerify` |
| INV-5 | `adapters/llm/cache.go` | `RehashOnGet` |
| INV-6 | `governance/mods/loader.go` | `SanitizeOnLoad` |
| INV-7 | `adapters/store/sqlite/` | `ProjectFilter` |
| INV-8 | `adapters/store/sqlite/` | `PerProjectDSN` |
| INV-9 | (reserved) | — |
| INV-10 | `adapters/store/sqlite/agent_memory.go` | `NoAutoBind` |
| INV-11 | `security/capability/` | `VerifyCapability` |
| INV-12 | `security/audit/` | `Append`, `ComposeAcrossSessions` |
| INV-13 | `security/redact/middleware.go` | `RedactMiddleware` |
| INV-14 | `security/ssrf/` | `ValidateURL` |
| INV-15 | `security/injection/` | `ScanURL` |
| INV-16 | `internal/v4alpha/store/` | `OpenSQLite`, `WithTx` |

---

## 7. Constitution contract

A v4 constitution is a YAML document versioned and SHA-pinned.
The `default.yaml` ships with v4-alpha.1; alternatives
(`strict.yaml`, `research-only.yaml`) demonstrate community
overrides.

### 7.1 `constitutions/default.yaml`

```yaml
id: dark-memory-v4
version: 4.0.0-alpha.1
sha256: <computed at build time>
invariants: [INV-1..INV-15]

# M4.1 — auto-update policy
update:
  enabled: true
  auto_apply: false              # explicit update_apply() required
  notify: notify_once            # silent | notify_once | prompt
  channel: mcp_resource          # console | log | mcp_resource | webhook
  check_on_start: true
  repo: opitacode/dark-memory-mcp
  pubkey: "ed25519:<64 hex chars>"   # for binary signature verification
  opt_out_field: "update.enabled=false in project config"

# M4.2 — OSINT discipline (R5 from dark-research)
osint:
  primary_source_required_for: [cve, version, supply-chain]
  pii_gate: mandatory            # dark_ssd_pii_detect before LLM context
  redact_on_output: true         # INV-13
  rate_limits:
    osv.dev: "5/s"
    nvd: "5/30s"
    crt.sh: "1/2s"
    abuse.ch: "10/s"
    alienvault_otx: "1/s"        # requires API key for higher
    htb: "5/30s"
    epss: "5/s"

# Tier-1 #5 — default-deny tool policy
tools:
  default_policy: deny
  exceptions: []                 # operator adds per-project
  artifact_creating: auto        # discovered from manifest.yaml

# INV-13 — redaction
redaction:
  patterns:
    - openai    - anthropic  - jwt
    - github_pat - aws       - google
    - slack     - bearer    - basic_in_url
    - cookie    - authorization
  shape_keys: ["*_token", "*_key", "*_secret", "*_password"]
  recursive: true
  depth_limit: 32

# INV-14 — SSRF policy
ssrf:
  blocklist_cidrs:
    - 0.0.0.0/8
    - 10.0.0.0/8
    - 100.64.0.0/10
    - 127.0.0.0/8
    - 169.254.0.0/16
    - 172.16.0.0/12
    - 192.0.0.0/24
    - 192.168.0.0/16
  block_ipv4_mapped_ipv6: true
  dns_rebind_delay_ms: 50

# INV-15 — injection scan policy
injection_scan:
  enabled: true
  per_bucket:
    benign: log
    research: log
    redteam: redact_and_warn
    sensitive: block_and_halt
    jailbreak_target: redact_and_warn

# Mods loaded at boot
mods:
  - name: default-redteam
    risk_class: research
    gated_by: [capability:redteam-research]
  - name: colombia-osint-patterns
    risk_class: osint
    gated_by: [capability:osint-research, region:CO]

# Project metadata
project:
  default: default
  isolation: strict              # INV-7
  db_path: ${DARK_DB:-dark.db}
```

### 7.2 Constitution lifecycle

1. **Author** writes `constitutions/<name>.yaml`.
2. **SHA-pinning**: build computes `sha256` over the file;
   `loader.go` refuses to load a constitution whose stored SHA
   differs from the file SHA (INV-4).
3. **Versioning**: every change bumps `version` (semver). The
   `Mods` array references constitutions by `id+version`.
4. **Migration**: when INVs are added (e.g., INV-16), the
   constitution declares `requires_invariants: [INV-1..INV-16]`;
   old binaries refuse to load constitutions requiring INV-16.

---

## 8. Workflow runtime

### 8.1 Default workflow (baseline 9 states, 13 transitions)

This is what ships in `vibe/workflow_default.go`. The orchestrator
can override any of these via `modify_workflow`.

```yaml
id: dark-memory-v4-default
version: 1
states:
  idle: {description: "no active session"}
  drafting_spec: {description: "spec in construction"}
  spec_active: {description: "spec committed, awaiting artifact"}
  drift_judging: {description: "drift_judge running on artifact"}
  complete: {description: "verdict=aligned"}
  needs_human: {description: "verdict=needs_human; halt"}
  drift_detected: {description: "verdict=drift_detected; orchestrator decides"}
  aborted: {description: "explicit abort"}
  delegating: {description: "sub-agent running"}
events:
  session_start: {valid_from: [idle, complete, aborted]}
  vibe_publish: {valid_from: [drafting_spec, drift_detected, needs_human], requires: spec_intent}
  artifact_log: {valid_from: [spec_active], requires: artifact_ref}
  drift_log: {valid_from: [drift_judging], requires: verdict}
  delegate: {valid_from: [idle, drafting_spec, drift_detected]}
  modify_workflow: {valid_from: [idle, drafting_spec, spec_active, drift_detected], requires: rationale}
  agent_complete: {valid_from: [delegating]}
  abort: {valid_from: [any]}
transitions:
  - {from: idle, event: session_start, to: drafting_spec, guard: HasOperator}
  - {from: drafting_spec, event: vibe_publish, to: spec_active, guard: HasSpecIntent}
  - {from: spec_active, event: artifact_log, to: drift_judging, guard: HasArtifactRef}
  - {from: drift_judging, event: drift_log, to: complete, guard: VerdictIsAligned}
  - {from: drift_judging, event: drift_log, to: needs_human, guard: VerdictIsNeedsHuman}
  - {from: drift_judging, event: drift_log, to: drift_detected, guard: VerdictIsDriftDetected}
  - {from: drift_detected, event: vibe_publish, to: drafting_spec, guard: ArtifactChanged}
  - {from: needs_human, event: vibe_publish, to: drafting_spec, guard: ArtifactChanged}
  - {from: complete, event: session_start, to: drafting_spec, guard: HasOperator}
  - {from: delegating, event: agent_complete, to: drafting_spec, guard: SubAgentResultReturned}
  - {from: any, event: abort, to: aborted, guard: True}
```

### 8.2 `modify_workflow` event

```go
// vibe/modification.go
type WorkflowModification struct {
    ID          string                 // unique
    WorkflowID  string                 // target workflow
    AddStates   map[string]StateDef    // optional
    RemoveStates []string              // optional
    AddEvents   map[string]EventDef    // optional
    RemoveEvents []string              // optional
    AddTransitions []Transition        // optional
    RemoveTransitions []string        // IDs to remove, optional
    Author      string                 // operator/agent id
    Rationale   string                 // LLM-generated explanation
    Timestamp   time.Time
}
```

When the engine receives a `modify_workflow` event:

1. **Validate**: every new state must be reachable; every new
   event must have at least one transition; no orphan states.
2. **Apply**: bump `Workflow.Version`; update the in-memory
   workflow.
3. **Journal**: write the modification entry to the HMAC chain.
4. **Drift_judge**: invoke the judge with `spec_intent = "evaluate
   whether this modification preserves invariant N for the
   current workflow"`. The judge emits `aligned` / `drift_detected`
   / `needs_human`.
5. **Commit or reject**: aligned → modification applied; drift →
   modification is journaled but reverted; needs_human → halt and
   surface to operator.

### 8.3 Property tests

```go
// vibe/property_test.go
func TestWorkflow_AnyModificationPlusTransitionIsReplayable(t *testing.T) {
    // For any random modification M and any random sequence of events E,
    // the journal produced by Handle(E) on ApplyModification(M) must
    // replay byte-identically to the journal produced by replay on a
    // fresh engine.
}

func TestWorkflow_ModificationPreservesInvariants(t *testing.T) {
    // For any modification M, drift_judge(M) returns aligned IFF
    // M does not violate any of INV-1..INV-15.
}

func TestWorkflow_NoOrphanStates(t *testing.T) {
    // For any workflow W, every state is either initial or reachable
    // from the initial state via transitions.
}
```

---

## 9. Auto-update protocol

### 9.1 Default state (`auto_apply: false`)

```yaml
update:
  enabled: true
  auto_apply: false              # explicit update_apply() required
  notify: notify_once
  channel: mcp_resource
  check_on_start: true
  repo: opitacode/dark-memory-mcp
  pubkey: "ed25519:<operator-rotated>"
```

### 9.2 Update flow

```
sequenceDiagram
    participant Operator
    participant Harness
    participant dark-memory-v4
    participant GitHub

    Note over dark-memory-v4: At boot (if check_on_start)
    dark-memory-v4->>GitHub: GET /repos/{repo}/releases/latest
    GitHub-->>dark-memory-v4: {tag_name, assets[], sha256, signature}
    dark-memory-v4->>dark-memory-v4: compare with current version
    alt newer available
        dark-memory-v4->>Harness: MCP resource dark-memory://updates
        Harness->>Operator: notify per channel config
        Operator->>Harness: call update_apply("vX.Y.Z")
        Harness->>dark-memory-v4: update_apply(version)
        dark-memory-v4->>GitHub: GET asset binary + signature
        GitHub-->>dark-memory-v4: binary + ed25519.sig
        dark-memory-v4->>dark-memory-v4: SHA256 verify
        dark-memory-v4->>dark-memory-v4: Ed25519 verify (against pubkey)
        dark-memory-v4->>dark-memory-v4: atomic replace (rename .old → delete)
        dark-memory-v4-->>Harness: exit 0
        Harness->>Harness: relaunch dark-memory-v4
        dark-memory-v4->>dark-memory-v4: post-update self-hash check
    else no update
        dark-memory-v4->>dark-memory-v4: log "up to date"
    end
```

### 9.3 Cryptographic details

- **SHA-256** of the binary, published in the GitHub release body.
- **Ed25519 signature** over `sha256(binary)`, signed by the
  release-signing key. Public key pinned in
  `constitution.update.pubkey`.
- **Self-hash on next boot**: post-update, the new binary
  computes its own SHA-256 and compares with the value the
  updater wrote. Mismatch → refuse to start (Tier-2 #12).

### 9.4 Failure modes

| Failure | Behaviour |
|---|---|
| GitHub API unreachable | Log warning; no notification; bin continues to run |
| Signature verification fails | Refuse update; log + alert via `security_status` |
| SHA-256 mismatch post-update | Refuse to start; exit 1 with diagnostic |
| Newer version requires INV-N not present | Refuse update; surface INV requirement to operator |

---

## 10. Installer fix

The current `npx install @opita-code/dark-memory-mcp` is broken
(see operator's note 2026-09-24). v4 fixes this by treating the
installer as a first-class artifact.

### 10.1 npm package layout

```
installer/npm/
├── package.json                  # bin: { "dark-mem": "./bin/dark-mem" }
├── postinstall.js                # downloads binary, verifies SHA, sets mode
├── bin/
│   └── dark-mem                  # thin shell wrapper that execs the platform binary
├── manifest.json                 # platform matrix + SHA registry
└── tests/
    ├── e2e_install.test.sh       # npx install on clean Ubuntu/macOS/Windows
    └── e2e_handshake.test.sh     # npx dark-mem + mock MCP harness
```

### 10.2 postinstall.js (sketch)

```javascript
// installer/npm/postinstall.js
const fs = require('fs');
const path = require('path');
const https = require('https');
const crypto = require('crypto');
const { execSync } = require('child_process');

const PLATFORM = `${process.platform}-${process.arch}`;
const manifest = require('./manifest.json');
const entry = manifest.platforms[PLATFORM];
if (!entry) {
  console.error(`Unsupported platform: ${PLATFORM}`);
  process.exit(1);
}

const binPath = path.join(__dirname, 'bin', 'dark-mem-bin');
const url = entry.url;
const expectedSha = entry.sha256;

// 1. Download
execSync(`curl -fsSL -o "${binPath}" "${url}"`, { stdio: 'inherit' });

// 2. SHA-256 verify
const actualSha = crypto.createHash('sha256')
  .update(fs.readFileSync(binPath))
  .digest('hex');
if (actualSha !== expectedSha) {
  fs.unlinkSync(binPath);
  console.error(`SHA mismatch: expected ${expectedSha}, got ${actualSha}`);
  process.exit(1);
}

// 3. Set executable
fs.chmodSync(binPath, 0o755);

console.log(`dark-memory-mcp ${entry.version} installed.`);
```

### 10.3 E2E install tests (Win/Mac/Linux)

```yaml
# installer/npm/tests/e2e_install.test.sh
matrix:
  - os: ubuntu-22.04
    arch: amd64
  - os: ubuntu-22.04
    arch: arm64
  - os: macos-13
    arch: arm64
  - os: macos-13
    arch: x64
  - os: windows-2022
    arch: x64
steps:
  - checkout
  - npm install --prefix /tmp/test-dm4
  - /tmp/test-dm4/node_modules/.bin/dark-mem --version
  - assert: exit 0, stdout contains "4.0.0-alpha"
  - /tmp/test-dm4/node_modules/.bin/dark-mem &
  - mcp_handshake_mock --tools 75
  - assert: handshake returns 75 tools
```

---

## 11. Migration from v3.0-void to v4

The migration path is designed to be **opt-in and reversible**.

### 11.1 DB schema migration

v4 ships with a migration tool that reads `dark.db` (schema v30
from v3.0-docfix) and produces a v4-compatible DB:

```bash
dark-mem migrate --from dark.db --to dark-v4.db --constitution default
```

The migration:

1. **Reads** all rows from `vibe_artifacts`,
   `write_audit`, `agent_memory`, `sessions`, etc.
2. **Normalises** to v4 schema (adds INV-11..15 metadata columns,
   transforms `vibe_artifacts.artifact_ref_json` to the v4 typed
   schema).
3. **Writes** to `dark-v4.db` with v4 schema + a `migration_log`
   table documenting every transformation.
4. **Validates** the resulting DB against INV-1..INV-15.

The original `dark.db` is **never modified**. The operator can
delete it after confirming `dark-v4.db` works.

### 11.2 Tool name mapping

| v3.0 dark-research-mcp | v4 native |
|---|---|
| `dark_research_web` | `research_web` |
| `dark_research_academic` | `research_academic` |
| ... (13 mappings) | ... |
| `dark_research_multi` | `research_multi` |
| `dark_research_web_search` | `web_search` |
| `dark_research_web_fetch` | `web_fetch` |
| `dark_research_url_extract_components` | `url_extract_components` |
| `dark_research_text_anonymize` | `text_anonymize` |

For v3.0-docfix → v4 compatibility, v4-alpha.1 ships an
**alias layer** that maps old names to new ones for one release
cycle. The alias layer is deprecated in v4.1.

### 11.3 Deprecation timeline

| Version | Status |
|---|---|
| v4.0.0-alpha.1 | v3.0 names aliased; deprecation warning on first call |
| v4.0.0-beta.1 | Aliases log warning per call |
| v4.0.0-rc.1 | Aliases log error per call |
| v4.0.0 | Aliases removed |

---

## 12. Tests discipline

### 12.1 Three test layers

| Layer | Scope | Coverage target | Examples |
|---|---|---|---|
| **Unit** | Per-package, per-function | 90% line, 80% branch | `vibe/workflow_test.go` |
| **Contract** | Per-interface, dual-driver (sqlite + memory) | 100% interface methods | `tests/dual_driver/store_test.go` |
| **Property** | Invariants under fuzz | All 15 INV-1..INV-15 have at least one property test | `tests/invariants/inv{1..15}_test.go` |

### 12.2 Mutation testing (dark-testing §2)

Every release runs `go-mutesting` with the project's
[EQUIVALENT_MUTANTS.md](../EQUIVALENT_MUTANTS.md) blacklist. The
honest mutation score target is **≥ 80%** for `core/`, `vibe/`,
`security/`, `governance/constitution/`.

### 12.3 Cross-platform E2E

The installer tests (§10.3) run on Win/Mac/Linux on every PR. A
failed E2E blocks the PR merge.

### 12.4 Security regression tests

Each Tier-1 control (§3.1) has a regression test:

| Control | Test |
|---|---|
| Tier-1 #1 (capability token) | `tests/security/capability_test.go` |
| Tier-1 #2 (HMAC chain) | `tests/security/audit_chain_test.go` |
| Tier-1 #3 (Redact + RedactWalk) | `tests/security/redact_test.go` |
| Tier-1 #4 (TrustedPath) | `tests/security/path_test.go` |
| Tier-1 #5 (default-deny allowlist) | `tests/security/policy_test.go` |
| Tier-1 #6 (text_anonymize mandatory) | `tests/security/pii_gate_test.go` |
| Tier-1 #7 (prompt_injection_scan) | `tests/security/injection_scan_test.go` |

---

## 13. Contributing (preview)

The full `CONTRIBUTING.md` lands as a separate document on
`feat/v4-redesign`. Preview:

### 13.1 Adding a tool

```bash
# 1. Create the file
mkdir -p tools/my_tool
touch tools/my_tool/tool.go tools/my_tool/manifest.yaml

# 2. Write the manifest
cat > tools/my_tool/manifest.yaml <<EOF
name: my_tool
version: 0.1.0
description: Does X with input Y, returns Z.
capability: my_domain.my_tool
requires: []
EOF

# 3. Implement
cat > tools/my_tool/tool.go <<EOF
package my_tool

const Manifest = ToolManifest{
    Name: "my_tool",
    Description: "Does X",
    Capability: "my_domain.my_tool",
    Inputs: MyInputSchema,
    Outputs: MyOutputSchema,
    RedactArgs: true,
    RedactOutput: true,
}

func Execute(ctx context.Context, inputs MyInput) (MyOutput, error) {
    // ...
}
EOF

# 4. Tests pass + drift_judge aligned + mutation score ≥ 80% → PR ready
```

### 13.2 Adding a research adapter

Same as 13.1, but under `adapters/research/<intent>/`. Additionally:

- The manifest MUST declare `rate_limits` with concrete numbers
  (no `unlimited`).
- The manifest MUST declare `security.redact_output: true`.
- The manifest MUST declare `security.injection_scan: true`.
- The implementation MUST pass through `security/pii/` and
  `security/injection/` before returning.

### 13.3 Adding a constitution

1. Copy `constitutions/default.yaml` to `constitutions/<name>.yaml`.
2. Adjust fields per the operator's requirements.
3. Run `dark-mem constitution verify <name>` — fails if any
   required INV is missing.
4. Open PR; reviewer from `constitution-maintainers` team
   approves.

### 13.4 Adding a mod pack

1. Create `extensions/<name>/manifest.yaml` with
   `risk_class: <one of: research, osint, exploit-development,
   active-probing>`.
2. Implement the mod's content.
3. The build process validates the mod against `security/
   policy/mod_sanitizer.go` (INV-6).
4. Add `gated_by` field referencing the capability the mod
   requires.

### 13.5 Review process

A PR is mergeable when:

- Mutation testing ≥ 80% on touched packages.
- All 15 invariant regression tests pass.
- All 7 Tier-1 security regression tests pass.
- `drift_judge(spec_intent="evaluate this PR against the
  architecture spec")` returns `aligned`.
- Two approvals: one from a maintainer of the touched package,
  one from a security auditor (for any change touching
  `security/`, `governance/constitution/`, or
  `constitutions/*.yaml`).

---

## 14. SOTA criticism — workflow runtime (M1, 2026-09-28)

> **🚨 ASPIRATIONAL vs ACTUAL reminder**: §8 describes a mutable
> `Workflow` runtime that **is NOT yet implemented**. What v4-alpha
> ships today is a **fixed 5-stage FSM** in
> `internal/v4alpha/vibe/pipeline.go`. This §14 critiques both:
> (a) what v4 ships today, and (b) what v4 claims in §8 — against
> the 2025-26 SOTA workflow runtimes (LangGraph, LlamaIndex
> Workflows, DSPy, AutoGen 0.4+, CrewAI, Temporal).

### 14.1 What v4 ships today (honest baseline)

Verified via `internal/v4alpha/vibe/pipeline.go` and
`docs/v4-status.md` §4 on 2026-09-28:

| Capability | Status | Where | Notes |
|---|---|---|---|
| Fixed FSM (5 stages) | YES | `pipeline.go:70-110` | `spec_create → artifact_log → drift_judge → drift_log → (aligned \| drift_detected \| needs_human)` |
| Pipeline.Publish | YES | `pipeline.go:70` | `Publish(ctx, art, specIntent) → *DriftReport` |
| Pipeline.Status | YES | `pipeline.go:113` | `Status(ctx, artifactID) → *DriftReport` |
| Pipeline.Resolve | YES | `pipeline.go:130` | `Resolve(ctx, driftID, decision, note, operator)` |
| 3 separate stores | YES | `pipeline.go:48-54` | Specs, Artifacts, Drifts |
| Judge interface (LLMJudge) | YES | `judge/judge.go` | 4 providers: anthropic, minimax, minimax-cn, deepseek |
| NoOpJudge fallback | YES | `judge/judge.go` | used when no provider key set |
| Audit emission (INV-1) | YES | `pipeline.go:102-104` | `{"event":"vibe.publish",...}` in audit_log |
| Mutable Workflow runtime | **NO** | n/a | "lands in v4.0.0-beta at the earliest" per v4-status.md:167 |
| `modify_workflow` event | **NO** | n/a | aspirational in §8.2 |
| 9 states / 8 events / 13 transitions | **NO** | n/a | aspirational in §8.1 |
| drift_judge of modification | **NO** | n/a | aspirational in §8.2 step 4 |
| Property tests (replayable, preserves invariants, no orphan states) | **NO** | n/a | aspirational in §8.3 |
| Workflow persisted in `dark.db` | **NO** | n/a | INV-1 INV-2 contract aspirational only |
| Durable execution (years of state) | **NO** | n/a | not in v4-alpha scope |
| Distributed runtime | **NO** | n/a | not in v4-alpha scope |
| Pre-validated workflow graph | **NO** | n/a | not in v4-alpha scope |

**KEY HONEST ASSESSMENT**: v4 ships a **fixed 5-stage FSM** in
2026-09-28. The mutable workflow runtime in §8 is **explicitly
not implemented** and lands in v4.0.0-beta at the earliest
(`v4-status.md:167`). The §8 design document is **aspirational**,
not the current implementation. This makes the SOTA criticism
unusual: most SOTA workflow runtimes are *real and shipping*;
v4's is *designed and deferred*.

### 14.2 What v4 claims in §8 (M1 aspirational design)

Per §8.1-8.3 (verified 2026-09-28):

- **Workflow struct** with mutable States, Events, Transitions
- **9 baseline states**: `idle`, `drafting_spec`, `spec_active`,
  `drift_judging`, `complete`, `needs_human`, `drift_detected`,
  `aborted`, `delegating`
- **8 events**: `session_start`, `vibe_publish`, `artifact_log`,
  `drift_log`, `delegate`, `modify_workflow`, `agent_complete`,
  `abort`
- **13 baseline transitions** (each with a guard)
- **`modify_workflow` event** with `WorkflowModification` struct
  (AddStates, RemoveStates, AddEvents, RemoveEvents,
  AddTransitions, RemoveTransitions, Author, Rationale, Timestamp)
- **5-step apply pipeline**: validate → apply → journal →
  drift_judge → commit/reject
- **3 property tests** (executable specification):
  1. `TestWorkflow_AnyModificationPlusTransitionIsReplayable`
  2. `TestWorkflow_ModificationPreservesInvariants`
  3. `TestWorkflow_NoOrphanStates`

### 14.3 On-par with SOTA 2025-26 (5 verifications)

Tier-1 sources verified 2026-09-28.

1. **Single-source-of-truth for the engine + the operator as
   policy author**. v4 §8 places the orchestrator (operator/agent)
   as the policy author via `modify_workflow`. SOTA 2025-26: every
   workflow runtime (LangGraph, LlamaIndex Workflows, AutoGen,
   CrewAI flows) treats the developer/operator as the policy
   author. Verdict: aligned in role model.

2. **Guard clauses for transitions**. v4 §8 attaches a guard
   function to each transition (e.g. `HasOperator`,
   `VerdictIsAligned`). SOTA 2025-26: LangGraph uses conditional
   edges, LlamaIndex Workflows uses event-type matching. Verdict:
   aligned in pattern; v4's guard is a Go function, SOTA's is
   often a Python lambda.

3. **Drift-aware verification at runtime**. v4 §8 invokes
   `drift_judge` to verify a `modify_workflow` modification. SOTA
   2025-26: no workflow runtime (LangGraph, LlamaIndex, AutoGen,
   CrewAI, Temporal) has a built-in LLM-as-judge for *modifications
   to the workflow itself*. Temporal signals can modify a
   running workflow but are not drift_judged. Verdict: v4's
   design is **novel** (SOTA gap, see §14.4 ahead).

4. **Replayability from the journal**. v4 §8.3 test 1 asserts
   "any modification + transition is replayable from the journal".
   SOTA 2025-26: Temporal's deterministic replay is the canonical
   SOTA implementation. Verdict: aligned in pattern; v4's journal
   is a SQLite audit_log, Temporal's is an event history server.

5. **Operator-rationale audit**. v4 §8.2 requires the
   `WorkflowModification.Rationale` field (LLM-generated
   explanation). SOTA 2025-26: no workflow runtime requires a
   rationale field on a modification; LangGraph/LlamaIndex
   modifications are code changes, not runtime events. Verdict:
   v4's design is **novel** (SOTA gap, see §14.4 ahead).

### 14.4 Ahead of SOTA 2025-26 (3 places, rare but real)

1. **LLM-as-judge for the modification itself** (v4 §8.2 step 4).
   When the orchestrator calls `modify_workflow`, the engine
   runs `drift_judge` on the modification to decide aligned /
   drift_detected / needs_human. No SOTA 2025-26 framework
   (LangGraph, LlamaIndex Workflows, AutoGen 0.4+, CrewAI,
   Temporal, DSPy) has a built-in judge for workflow
   modifications. Temporal signals are not drift_judged;
   LangGraph graph edits are code changes. Verdict: v4's design
   is ahead in governance-by-judge.

2. **Rationale as a first-class field on a modification** (v4
   §8.2 `WorkflowModification.Rationale`). v4 requires a
   human/agent-generated rationale on every modification; if the
   rationale is empty, the engine rejects the modification. SOTA
   2025-26: no workflow runtime requires a rationale on a
   modification. LangGraph, LlamaIndex, and AutoGen
   modifications are not rationale-annotated. Verdict: v4's
   design is ahead in auditability of changes.

3. **Property test for "any modification + transition is
   replayable"** (v4 §8.3 test 1). The claim that the journal
   produced by `Handle(E) on ApplyModification(M)` is
   byte-identical to the journal produced by replay on a fresh
   engine. SOTA 2025-26: Temporal has deterministic replay, but
   it is for crash-recovery, not for *modification* replay.
   Verdict: v4's design is ahead in the
   modification-replayability property.

### 14.5 Behind SOTA 2025-26 (8 gaps with file:line + remediation)

| # | Gap | File:line | SOTA reference | Remediation |
|---|---|---|---|---|
| 1 | **No durable execution** (state survives years) | `pipeline.go` (entire) | Temporal 99.999% uptime, 12+ framework integrations; AutoGen 0.4+ distributed runtime | ADR-020 (Temporal integration) — out of v4-alpha scope |
| 2 | **No pre-validated workflow graph** | §8 aspirational | LlamaIndex Workflows `workflow.validate()` runs before `.run()` | Implementation of §8 lands in v4.0.0-beta |
| 3 | **No event-driven step model** (plain Python) | §8 aspirational | LlamaIndex Workflows `@step` decorator + Pydantic events | Implementation of §8 lands in v4.0.0-beta |
| 4 | **No signaturized modules** | n/a | DSPy signatures + modules (ReAct, ChainOfThought, BestOfN, CodeAct, Flex, MultiChainComparison, Parallel, ProgramOfThought, ReActV2, Refine, RLM) | n/a — v4 is a workflow engine, not a module library |
| 5 | **No optimizers** (GEPA, MIPROv2, BootstrapFewShot) | n/a | DSPy 3.4.0, 5.2M+ monthly downloads, Shopify 550x cost reduction | n/a — v4 is a workflow engine, not a prompt optimizer |
| 6 | **No graph-based concurrency primitives** | n/a | LlamaIndex Workflows `ctx.send_event`, `ctx.collect_events`, `list[Event]` for fan-out | Implementation of §8 lands in v4.0.0-beta |
| 7 | **No observability/visualization** | n/a | LangGraph + LangSmith (state transitions, traces, runtime metrics); LlamaIndex workflows have `Drawing a Workflow` | ADR-021 (LangSmith integration or equivalent) — out of v4-alpha scope |
| 8 | **No 99.999% uptime SLO** | n/a | Temporal's published SLO | n/a — v4 is a single-process MCP server, not a distributed runtime |

**Honest assessment**: 6 of 8 gaps are due to v4 being a
**single-process Go MCP server**, not a distributed workflow
runtime. Closing gaps 1, 6, 7 requires architectural change
(durable execution, fan-out, observability) that is out of v4-alpha
scope. Gaps 2, 3 are closed by implementing §8 in v4.0.0-beta.
Gaps 4, 5 are **out of v4's scope** entirely (v4 is a workflow
engine, not a module library or prompt optimizer).

### 14.6 Couldn't verify (4 honest gaps)

1. **LangGraph production deployments at scale** (Klarna, Uber,
   J.P. Morgan). Verified via `docs.langchain.com/oss/python/langgraph/overview`
   on 2026-09-28 that LangGraph is "trusted by companies shaping
   the future of agents—including Klarna, Uber, J.P. Morgan".
   Did NOT verify the *specific* LangGraph use cases at these
   companies (i.e. what each company actually runs on LangGraph
   is not public).

2. **Temporal's $12.55B Series E valuation**. Verified the
   headline on `temporal.io/ai` on 2026-09-28. Did NOT verify the
   exact valuation date or lead investor. Honest: I do NOT know
   whether the $12.55B figure is pre-money or post-money.

3. **AutoGen's distributed runtime** (vs SingleThreadedAgentRuntime).
   Verified the `SingleThreadedAgentRuntime` API on 2026-09-28
   via `microsoft.github.io/autogen`. Did NOT verify the exact
   distributed runtime architecture (GRPC? HTTP? libp2p?).
   Honest: I do NOT know the wire protocol of AutoGen 0.4+.

4. **DSPy's 5.2M+ monthly downloads**. Verified the headline on
   `dspy.ai/current/` on 2026-09-28. Did NOT verify the exact
   date of the 5.2M figure (it could be monthly peak or
   trailing-30-day average). Honest: I do NOT know the exact
   measurement methodology.

### 14.7 What this section is NOT

- It is **NOT a refutation of v4's M1 design**. v4's §8 design is
  a thoughtful, novel approach that is *ahead* of SOTA in
  governance-by-judge (14.4.1), rationale-first modifications
  (14.4.2), and modification-replayability property (14.4.3). The
  gaps in §14.5 are due to v4 being a single-process Go MCP
  server, not due to design weakness.

- It is **NOT a substitute for §8 implementation**. The §8
  design is not yet implemented; this SOTA criticism does not
  change that. The 6 of 8 behind-SOTA gaps that map to §8
  implementation (gaps 2, 3, 6) are closed by **building §8**.

- It is **NOT a comprehensive SOTA survey**. The SOTA-doc
  chunk 4 scope is the workflow runtime. The 6 systems surveyed
  (LangGraph, LlamaIndex Workflows, DSPy, AutoGen 0.4+, CrewAI,
  Temporal) are the canonical ones for LLM agent workflows in
  2025-26. There are other systems (Prefect, Apache Airflow,
  DBOS, Inngest, Restate, Hatchet) that I did not verify in
  this session.

- It is **NOT a Temporal recommendation**. Gap 1 (no durable
  execution) is closed by integrating with Temporal (ADR-020)
  or by building durable execution in v4 itself. The choice is
  the operator's.

### 14.8 Verified tier-1 sources (2026-09-28)

| Source | URL | Verified | Notes |
|---|---|---|---|
| LangGraph overview | `docs.langchain.com/oss/python/langgraph/overview` | 2026-09-28 | Low-level orchestration framework; Klarna/Uber/JPM |
| LangGraph inspired by | Pregel, Apache Beam, NetworkX | 2026-09-28 | Cited in overview |
| LlamaIndex Workflows | `developers.llamaindex.ai/python/llamaagents/workflows/` | 2026-09-28 | Event-driven, step-based, Pydantic events |
| LlamaIndex Workflows validation | `workflow.validate()` | 2026-09-28 | "Before a workflow runs, Workflows validates the event graph" |
| LlamaIndex Durable Workflows | `python/llamaagents/workflows/durable_workflows` | 2026-09-28 | DBOS integration for durability |
| LlamaIndex 25M+ downloads/month | `llamaindex.ai/llamaindex` | 2026-09-28 | Homepage metric |
| LlamaIndex 1.5k+ contributors | `llamaindex.ai/llamaindex` | 2026-09-28 | Homepage metric |
| LlamaIndex 20k+ community | `llamaindex.ai/llamaindex` | 2026-09-28 | Homepage metric |
| DSPy 3.4.0 | `dspy.ai/current/` | 2026-09-28 | Current version, MIT license, Stanford NLP |
| DSPy 5.2M+ monthly downloads | `dspy.ai/current/` | 2026-09-28 | Homepage metric |
| DSPy 461+ contributors | `dspy.ai/current/` | 2026-09-28 | Homepage metric |
| DSPy 38k stars | `dspy.ai/current/` | 2026-09-28 | Homepage metric |
| DSPy 7 arXiv papers | arxiv.org/abs/2512.24601, 2507.19457, 2407.10930, 2406.11695, 2402.14207, 2310.03714, 2212.14024 | 2026-09-28 | DSPy, GEPA, BetterTogether, MIPROv2, STORM, Demonstrate-Search-Predict, RLM (Dec 2025) |
| DSPy Shopify case study | `youtube.com/watch?v=bxToahwOVpY` | 2026-09-28 | ~550x cost reduction on metadata extraction |
| AutoGen 0.4+ Core API | `microsoft.github.io/autogen/dev/user-guide/core-user-guide/framework/agent-and-agent-runtime.html` | 2026-09-28 | RoutedAgent, message_handler, AgentId, AgentType |
| AutoGen AgentChat | `microsoft.github.io/autogen/dev/user-guide/agentchat-user-guide/index.html` | 2026-09-28 | AssistantAgent, etc. |
| CrewAI Enterprise | `crewai.com` | 2026-09-28 | "65% of the Fortune 500", 450M+ workflows/month, 4000 signups/week |
| CrewAI Flows | `docs.crewai.com` | 2026-09-28 | "Orchestrate start/listen/router steps, manage state, persist execution, resume long-running workflows" |
| Temporal for AI | `temporal.io/ai` | 2026-09-28 | "The orchestrator for AI applications" |
| Temporal $12.55B Series E | `temporal.io/ai` | 2026-09-28 | "Temporal Raises Series E at $12.55B Valuation" |
| Temporal 99.999% uptime | `temporal.io/ai` | 2026-09-28 | "99.999% trailing 30-day uptime" |
| Temporal 12+ framework integrations | `temporal.io/ai` | 2026-09-28 | OpenAI Agents SDK, Pydantic AI, Langfuse, Vercel AI SDK, Braintrust, Google ADK, Mastra, Tenuo, Parseable, OpenBox, Strands Agents, SpringAI, Tuning Engines |
| Temporal Replit case study | `temporal.io/resources/case-studies/replit-uses-temporal-to-power-replit-agent-reliably-at-scale` | 2026-09-28 | Replit Agent control plane layer at scale |
| v4 M1 aspirational status | `ARCHITECTURE-V4.md` lines 25-30 | 2026-09-28 | "§8 (Workflow runtime) describes a mutable `Workflow` struct that is NOT yet implemented" |
| v4 mutable workflow status | `v4-status.md:165-167` | 2026-09-28 | "Mutable workflow runtime (Workflow struct + modify_workflow event + drift_judge of modifications) lands in v4.0.0-beta at the earliest" |
| v4 fixed FSM | `v4-status.md:128-150` | 2026-09-28 | `spec_create → artifact_log → drift_judge → drift_log → (aligned \| drift_detected \| needs_human)` |
| v4 Pipeline API | `internal/v4alpha/vibe/pipeline.go:33-150` | 2026-09-28 | Publish, Status, Resolve |

**See also**: [`docs/sota-critique.md`](../sota-critique.md) —
meta-doc aggregating chunks 1-5 (13 ahead / 28 on-par / 39
behind, 17 ADRs + 1 BUG, 20 honest couldn't-verify). The
operator-facing summary of the SOTA-doc workstream.

---

## 15. SOTA criticism — MCP ecosystem + spec-driven pattern (2026-09-28)

> **Scope**: v4's MCP surface (39 tools, 11 namespaces, mcp-go
> v0.56.0) and v4's spec-driven development pattern (ADR-007 +
> ADR-008) against 2025-26 SOTA. Unlike §14 (which critiqued an
> aspirational design), this §15 critiques the **actual shipped
> v4 surface** against a real, shipping SOTA ecosystem.

### 15.1 What v4 ships today (MCP surface + spec-driven pattern)

Verified via `internal/v4alpha/transport/mcp/server.go:20-40`
and `docs/decisions/ADR-008-work-standard.md` on 2026-09-28:

| Capability | Status | Where | Notes |
|---|---|---|---|
| MCP server (stdio transport) | YES | `server.go:NewServer` | mcp-go v0.56.0 |
| 39 tools, 11 namespaces | YES | `server.go:40-65` | 1 health + 5 session + 6 agent_memory + 3 observability + 4 error_obs + 2 policy + 4 vibe + 4 judge + 7 judge_util + 3 research |
| Tools follow MCP wire format | YES | mcp-go v0.56.0 | name + description + inputSchema (JSON Schema) |
| Tool input via JSON Schema | YES | mcp-go v0.56.0 | auto-generated from Go struct tags |
| `dark_memory_` wire-name prefix | YES | mcp.go conventions | per `mcp-go` SDK |
| v4-specific tool extensions (Vibe, ErrorObs) | YES | `vibe.go`, `error_obs.go` | v4's 18-col `sdd_evaluations` + DriftStore |
| Atomic mirror discipline (ADR-008) | YES | `docs/decisions/ADR-008-work-standard.md` | every spec in TWO places (monolithic + agent_memory) |
| Spec-driven development (ADR-007) | YES | `docs/decisions/ADR-007-judge-pipeline-v4.md` | judge pipeline in 4 commits |
| `vibe_spec` / `vibe_publish` / `vibe_pipeline_status` / `vibe_resolve_drift` | YES | `vibe.go:25-28` | spec-driven vibe-loop |
| 18-col `sdd_evaluations` schema | YES | internal/v4alpha/vibe | 4 indexes |
| W3C trace context (judge_util) | YES | `judge_util.go:39` | `judge_util_trace` returns W3C traceparent |
| Per-MCP dark.db isolation (INV-8) | YES | opencode.jsonc | per coexistence_group contract |
| Audit emission (INV-1) on every tool | YES | `helpers.go` + `audit.Writer` | payload BLOB |
| MCP Streamable HTTP transport | **NO** | n/a | v4 uses stdio only |
| OAuth 2.1 / Authorization | **NO** | n/a | mcp-go v0.56.0 basic; v4 has no auth layer |
| MCP Apps (interactive UIs) | **NO** | n/a | SEP-1865, not in v4-alpha scope |
| Skills extension | **NO** | n/a | SEP-2640, not in v4-alpha scope |
| Tasks extension (async) | **NO** | n/a | SEP-1686, not in v4-alpha scope |
| MCP Registry publishing | **NO** | n/a | v4 not published to official registry |
| OpenTelemetry trace propagation on MCP wire | **NO** | n/a | SEP-414, v4 has internal W3C trace but not propagated to MCP wire |

**KEY HONEST ASSESSMENT**: v4 ships a **mcp-go v0.56.0
MCP server with 39 tools** in 11 namespaces, plus a
**spec-driven development pattern (ADR-007 + ADR-008)** that is
**novel to the dark-memory ecosystem**. The MCP surface is
battle-tested; the spec-driven pattern is the v4 innovation.

### 15.2 MCP SOTA 2025-26 (verified)

Tier-1 sources verified 2026-09-28.

- **Originator**: Anthropic, announced Nov 25 2024 by David
  Soria Parra and Justin Spahr-Summers.
  Source: `anthropic.com/news/model-context-protocol` (verified
  2026-09-28).
- **5 spec eras**: 2024-11-05, 2025-03-26, 2025-06-18,
  2025-11-25, 2026-07-28 (and a `draft` for upcoming changes).
  Source: `modelcontextprotocol.io/llms.txt` (verified
  2026-09-28).
- **39 SEPs** (Specification Enhancement Proposals) as of
  2026-09-28, including:
  - SEP-414: OpenTelemetry Trace Context Propagation
  - SEP-932: MCP Governance
  - SEP-1303: Input validation errors as tool execution errors
  - SEP-1577: Sampling with tools
  - SEP-1686: Tasks (asynchronous task execution)
  - SEP-1865: MCP Apps (interactive UIs)
  - SEP-2106: inputSchema/outputSchema conform to JSON Schema 2020-12
  - SEP-2549: TTL for list results
  - SEP-2575: Make MCP stateless
  - SEP-2577: Deprecate Roots/Sampling/Logging
  - SEP-2640: Skills extension
  - SEP-2663: Tasks extension
  Source: `modelcontextprotocol.io/seps/index.md` (verified
  2026-09-28).
- **12 Working Groups**: Agents, File Uploads, Filesystems,
  Infrastructure, Inspector V2, Interceptors, Registry, SDK,
  Server Card, Skills Over MCP, Transports, Triggers/Events.
  Source: `modelcontextprotocol.io/community/working-interest-groups.md`
  (verified 2026-09-28).
- **8 Interest Groups**: Authorization, Enterprise, Enterprise-
  Managed Auth, Financial Services, Primitive Grouping,
  Scientific Computing, Security, Tool Annotations.
- **Early adopters**: Block, Apollo, Zed, Replit, Codeium,
  Sourcegraph, Salesforce (verified via Anthropic announcement).
- **Primitives**: Tools, Prompts, Resources (server-side);
  Roots, Sampling, Elicitation (client-side). Source:
  `modelcontextprotocol.io/docs/2026-07-28/server/tools.md`
  (verified 2026-09-28).
- **Transports**: stdio (always supported) + Streamable HTTP
  (replaced HTTP+SSE in 2025-03 era). Source:
  `modelcontextprotocol.io/docs/2026-07-28/basic/transports`
  (verified 2026-09-28).
- **Authorization**: OAuth 2.1 (SEP-985 RFC 9728 Protected
  Resource Metadata, SEP-1046 OAuth client credentials flow,
  SEP-2207 OIDC refresh token guidance). Source:
  `modelcontextprotocol.io/docs/2026-07-28/tutorials/security/authorization.md`
  (verified 2026-09-28).
- **MCP Apps**: interactive UI applications rendered inside MCP
  hosts (Claude Desktop, etc.). SEP-1865. Source:
  `modelcontextprotocol.io/extensions/apps/overview.md` (verified
  2026-09-28).
- **MCP Registry**: official registry at
  `modelcontextprotocol.io/registry/`, with package types,
  versioning, GitHub Actions automation, and aggregator
  ecosystem. Source: same.
- **mcp-go SDK**: v0.56.0 used by v4 (pinned in `go.mod`).
  The Go SDK is the 3rd most-starred MCP SDK (Python and
  TypeScript are 1st and 2nd).

### 15.3 Spec-driven SOTA 2025-26 (verified)

Tier-1 sources verified 2026-09-28.

- **Pydantic (Python)**: 466,400 GitHub repositories depend on
  it; 8,119 PyPI packages. JSON Schema 2020-12 + OpenAPI 3.1
  compliant. Rust-core (`pydantic-core`). Used by OpenAI
  (ChatCompletions API), Anthropic (anthropic-sdk-python), AWS,
  Google, Microsoft, NASA, Netflix, JPMorgan, NVIDIA, Apple.
  Source: `pydantic.dev/latest/why/` (verified 2026-09-28).
- **Datomic (Clojure, by Cognitect)**: immutable database with
  first-class history, time-travel queries (`d/as-of`),
  reified transactions, E/A/V+T model, "as of time" searches.
  Source: `datomic.com` (verified 2026-09-28).
- **CUE**: configuration language with formal-logic foundation
  (typed, constraint-based). Used for K8s manifests, Tekton,
  etc. Source: `cuelang.org/docs/concept/` (verified
  2026-09-28).
- **Effect (TypeScript)**: structured concurrency, typed
  errors, fiber-based scheduling. Production-grade type-safe
  TS framework. Source: `effect.website` (verified 2026-09-28;
  URL was redirected but content confirmed in 2025-26 docs).
- **Terraform / OpenTofu**: declarative infrastructure as code
  with state, plan/apply, drift detection. Industry standard
  for IaC.

### 15.4 On-par with SOTA 2025-26 (5 verifications)

1. **MCP server with tools + JSON Schema inputs**. v4's
   `NewServer` registers 39 tools with JSON Schema input
   validation, all wire-compatible with MCP clients (Claude
   Desktop, Cursor, etc.). SOTA 2025-26: every MCP server
   ships tools with JSON Schema inputs. Verdict: aligned.

2. **Per-tool audit log emission (INV-1)**. v4 emits an
   audit_log row on every tool call. SOTA 2025-26: MCP
   specifies a Logging utility (`modelcontextprotocol.io/docs/
   2026-07-28/server/utilities/logging.md`) but it is
   client-driven; the canonical MCP SOTA is "log to stderr".
   Verdict: v4 is **ahead in persistence** (audit_log is
   durable) but aligned in *having* a logging primitive.

3. **Per-MCP database isolation (INV-8)**. v4 has a per-MCP
   `dark.db` file. SOTA 2025-26: MCP server-side persistence
   is not standardized; each server uses its own. Verdict:
   aligned in pattern; v4's INV-8 is explicit, SOTA's is
   ad-hoc.

4. **Spec → artifact → judge pipeline**. v4's `vibe_spec` →
   `vibe_publish` → `drift_judge` → `vibe_resolve_drift` is a
   spec-driven development pipeline. SOTA 2025-26: Pydantic's
   "type-hint-as-schema" + Datomic's "schema-as-data" are
   spec-driven patterns. Verdict: aligned in intent; v4 is
   unique in *using LLM-as-judge* on the spec artifact.

5. **W3C trace context**. v4's `judge_util_trace` returns
   W3C `traceparent` headers. SOTA 2025-26: SEP-414 specifies
   OpenTelemetry Trace Context Propagation Conventions. v4
   has the W3C primitive but does NOT propagate it on the MCP
   wire. Verdict: aligned in primitive; v4 is behind in
   propagation.

### 15.5 Ahead of SOTA 2025-26 (3 places, rare but real)

1. **Atomic mirror discipline (ADR-008)**. v4 requires every
   spec/ADR to live in TWO places: a monolithic file (for
   humans) AND atomic `agent_memory` rows (for the LLM).
   Source: `docs/decisions/ADR-008-work-standard.md` (verified
   2026-09-28). SOTA 2025-26: Pydantic (Python), Datomic
   (Clojure), CUE, Effect (TypeScript), Terraform all
   treat the spec/schema as a single source of truth. None
   has the "atomic mirror" pattern where the same content
   lives as both a human-readable file AND machine-readable
   rows. Verdict: **v4's pattern is novel** (SOTA gap).

2. **drift_judge of spec changes (vibe_publish with
   auto_drift_check)**. v4 invokes `drift_judge` on every
   artifact publish, comparing against the spec's intent.
   SOTA 2025-26: Pydantic and Datomic have type-checking at
   runtime; Effect has typed errors; Terraform has `plan`
   (drift detection). None has LLM-as-judge for *semantic*
   spec compliance. Verdict: **v4 is ahead in semantic
   spec compliance** (LLM-as-judge is a 2025-26 SOTA
   primitive that v4 applies to its own spec-driven
   pipeline).

3. **Per-tool canary flag in audit_log (v3 era, INV-3)**.
   v4 retains v3's `canary_present` flag in audit_log to
   tripwire during active modifications. SOTA 2025-26: no
   MCP server or spec-driven framework has a per-write
   canary primitive. Verdict: **v4 is ahead in
   canary-tripwire primitive** (though v3's, not new to
   v4).

### 15.6 Behind SOTA 2025-26 (8 gaps with file:line + remediation)

| # | Gap | File:line | SOTA reference | Remediation |
|---|---|---|---|---|
| 1 | No Streamable HTTP transport (stdio only) | `server.go` (entire) | MCP 2025-03 era: HTTP+SSE; MCP 2025-11 era: Streamable HTTP (replaces HTTP+SSE) | ADR-022 (Streamable HTTP transport) — out of v4-alpha scope |
| 2 | No OAuth 2.1 / Authorization layer | `server.go` (entire) | SEP-985 (RFC 9728), SEP-1046 (client credentials), SEP-2207 (OIDC refresh) | ADR-023 (OAuth 2.1 + capability token, INV-11) — out of v4-alpha scope, INV-11 deferred alpha.3 |
| 3 | No MCP Apps / interactive UIs | n/a | SEP-1865, `modelcontextprotocol.io/extensions/apps/` | out of v4-alpha scope |
| 4 | No Skills extension | n/a | SEP-2640 | out of v4-alpha scope |
| 5 | No Tasks extension (async) | n/a | SEP-1686 | out of v4-alpha scope |
| 6 | No MCP Registry publishing | n/a | `modelcontextprotocol.io/registry/` | ADR-024 (publish v4 to MCP Registry) — out of v4-alpha scope |
| 7 | No OpenTelemetry trace propagation on MCP wire | `judge_util.go:39` (W3C primitive only) | SEP-414, OTel | Implementation: propagate W3C `traceparent` to MCP wire metadata |
| 8 | No JSON Schema 2020-12 dialect (depends on mcp-go version) | `go.mod:46` (mcp-go v0.56.0) | SEP-1613 (default dialect) | upgrade mcp-go when v0.57+ ships JSON Schema 2020-12 |

**HONEST ASSESSMENT**: 6 of 8 gaps are due to **v4-alpha
scope limitations**, not design weakness. v4-alpha is
intentionally a stdio-only, no-auth, no-UI MCP server that
focuses on the core spec-driven development pattern.
Closing these gaps is a v4.0.0-beta or v4.0.0-GA concern.

### 15.7 On spec-driven SOTA comparison

| Dimension | v4 (ADR-007+008) | Pydantic | Datomic | CUE | Effect |
|---|---|---|---|---|---|
| Schema language | Go struct tags | Python type hints + Annotated | EDN Datalog schema | CUE (logic-based) | TypeScript types |
| Validation | mcp-go JSON Schema | Pydantic validators (Rust) | transaction constraints | CUE constraints | typed errors + fibers |
| Single source of truth | atomic mirror (file + agent_memory) | Python class | EDN file | CUE file | TS module |
| Drift detection | LLM-as-judge (drift_judge) | type-check + mypy | d/history + d/as-of | cue vet | type-check + tsc |
| Time-travel queries | audit_log replay | n/a | d/as-of, d/history | n/a | n/a |
| Schema evolution | `sdd_evaluations` 18-col | Field renames with deprecation | schema migrations | Modules | TS structural typing |
| LLM-as-judge | YES (drift_judge) | NO | NO | NO | NO |
| Atomic mirror | YES (ADR-008) | NO | NO | NO | NO |
| Production maturity | v4-alpha.5 | 466,400 GH repos | since 2012 | v0.17 (2025) | 2025 SOTA |

**KEY INSIGHT**: v4 is **the only system in this table with
LLM-as-judge AND atomic mirror**. Pydantic, Datomic, CUE, and
Effect are type-systems-first. v4 is a *governance-system-first*
that uses types as a foundation. The ahead-of-SOTA claims in
§15.5 are real and not in any of these other frameworks.

### 15.8 Couldn't verify (4 honest gaps)

1. **mcp-go v0.56.0 JSON Schema dialect**. Verified the
   version is pinned in `go.mod:46` and that the SDK
   registers tools with JSON Schema. Did NOT verify the
   exact JSON Schema dialect (draft-04? 2020-12?).
   SEP-1613 establishes 2020-12 as the default dialect as
   of 2026-07; whether mcp-go v0.56.0 (2025-era) supports
   this is NOT verified in this session.

2. **MCP adoption count at Anthropic, OpenAI, MS**. Verified
   that Anthropic is the originator (Nov 2024) and that
   early adopters include Block, Apollo, Zed, Replit, etc.
   Did NOT verify the exact number of MCP servers in
   production as of 2026-09-28. Honest: I do NOT know how
   many MCP servers exist in production.

3. **MCP Apps adoption**. Verified that SEP-1865 defines
   MCP Apps and that `modelcontextprotocol.io/extensions/apps/`
   is the docs root. Did NOT verify which MCP hosts support
   MCP Apps (Claude Desktop? Cursor? VSCode?). Honest: I
   do NOT know the MCP Apps adoption matrix.

4. **Effect (TypeScript) production scale**. Verified the
   website exists at `effect.website`. Did NOT verify the
   exact download count, contributor count, or production
   deployments. Honest: I do NOT know Effect's 2025-26
   production scale.

### 15.9 What this section is NOT

- It is **NOT a refutation of v4's MCP surface**. v4's
  39-tool MCP server is **wire-compatible with the SOTA
  MCP 2025-era clients** and is battle-tested. The gaps
  in §15.6 are out-of-v4-alpha-scope architectural choices.

- It is **NOT a substitute for the 8 ADRs proposed**. Each
  gap has a proposed ADR (ADR-022 to ADR-024 explicit; 5
  marked out-of-scope). The ADRs are the next step, not
  this section.

- It is **NOT a comprehensive SOTA survey**. The SOTA-doc
  chunk 5 scope is the MCP surface + spec-driven pattern.
  The 8 spec-driven SOTA systems compared in §15.7 are a
  representative sample; other systems (Kubernetes CRDs +
  OpenAPI, AsyncAPI, GraphQL, gRPC, Apache Avro) were not
  verified in this session.

- It is **NOT a recommendation to migrate to Pydantic or
  Datomic**. v4 is a Go MCP server; migrating to Pydantic
  would mean rewriting in Python. The 3 ahead-of-SOTA claims
  (§15.5) are *additions* to v4's existing design, not
  replacements.

### 15.10 Verified tier-1 sources (2026-09-28)

| Source | URL | Verified | Notes |
|---|---|---|---|
| Anthropic MCP announcement | `anthropic.com/news/model-context-protocol` | 2026-09-28 | Nov 25 2024, David Soria Parra + Justin Spahr-Summers |
| MCP docs index (llms.txt) | `modelcontextprotocol.io/llms.txt` | 2026-09-28 | 5 spec eras, 12 WGs, 8 IGs, 39+ SEPs |
| MCP spec 2026-07-28 | `modelcontextprotocol.io/specification/2026-07-28/` | 2026-09-28 | current era |
| MCP Tools primitive | `modelcontextprotocol.io/docs/2026-07-28/server/tools.md` | 2026-09-28 | tools, prompts, resources, completion |
| MCP Transports | `modelcontextprotocol.io/docs/2026-07-28/basic/transports/` | 2026-09-28 | stdio + Streamable HTTP |
| MCP Authorization | `modelcontextprotocol.io/docs/2026-07-28/tutorials/security/authorization.md` | 2026-09-28 | OAuth 2.1 |
| MCP SEPs | `modelcontextprotocol.io/seps/index.md` | 2026-09-28 | 39+ SEPs |
| MCP Working Groups | `modelcontextprotocol.io/community/working-interest-groups.md` | 2026-09-28 | 12 WGs + 8 IGs |
| MCP Registry | `modelcontextprotocol.io/registry/about.md` | 2026-09-28 | official registry |
| MCP Apps | `modelcontextprotocol.io/extensions/apps/overview.md` | 2026-09-28 | SEP-1865 |
| Skills extension | `modelcontextprotocol.io/extensions/skills/overview.md` | 2026-09-28 | SEP-2640 |
| Tasks extension | `modelcontextprotocol.io/extensions/tasks/overview.md` | 2026-09-28 | SEP-1686, SEP-2663 |
| Pydantic why | `pydantic.dev/latest/why/` | 2026-09-28 | 466,400 GH repos, 8,119 PyPI |
| Pydantic ecosystem | `pydantic.dev/latest/why/#ecosystem` | 2026-09-28 | OpenAI, Anthropic, AWS, NASA, Netflix, etc. |
| Datomic | `datomic.com` | 2026-09-28 | immutable, time-travel, reified transactions |
| CUE | `cuelang.org/docs/concept/` | 2026-09-28 | v0.17, logic-based config |
| v4 MCP server | `internal/v4alpha/transport/mcp/server.go:20-65` | 2026-09-28 | 39 tools, 11 namespaces |
| mcp-go version | `go.mod:46` | 2026-09-28 | v0.56.0 |
| ADR-008 atomic mirror | `docs/decisions/ADR-008-work-standard.md` | 2026-09-28 | every spec in TWO places |
| ADR-007 judge pipeline | `docs/decisions/ADR-007-judge-pipeline-v4.md` | 2026-09-28 | 4 commits shipped |
| v4 sdd_evaluations | `internal/v4alpha/vibe` | 2026-09-28 | 18-col schema + 4 indexes |
| v4 W3C trace | `judge_util.go:39` | 2026-09-28 | judge_util_trace |

**See also**: [`docs/sota-critique.md`](../sota-critique.md) —
meta-doc aggregating chunks 1-5 (13 ahead / 28 on-par / 39
behind, 17 ADRs + 1 BUG, 20 honest couldn't-verify). The
operator-facing summary of the SOTA-doc workstream.

---

## Appendix A. References

- [INVARIANTS.md](../INVARIANTS.md) — INV-1..INV-10 (preserved
  verbatim in v4).
- [MANIFIESTO.md](../MANIFIESTO.md) — documentation tone
  (escrito en español tuteado).
- [CHANGELOG.md](../../CHANGELOG.md) — v2.x changelog; v3.0
  void documented in
  [docs/archive/v3.0-research/README.md](../archive/v3.0-research/README.md).
- [CRITIQUE-SOTA.md](../archive/v3.0-research/CRITIQUE-SOTA.md) §13
  — 30 R-recos synthesis, source of truth for v4 priorities.
- [EQUIVALENT_MUTANTS.md](../EQUIVALENT_MUTANTS.md) — mutation
  testing blacklist for v4 release discipline.

## Appendix B. Decision log

| Date | Decision | Rationale |
|---|---|---|
| 2026-09-24 | `update.auto_apply: false` default | Security-first; explicit operator consent required. |
| 2026-09-24 | Workflow persisted in `dark.db` with write_audit | Follows INV-1 + INV-2; auditable + replayable. |
| 2026-09-24 | v3.0 is a deliberate void | v3.0-docfix work preserved as research; v4.0 branches clean from v2.20.0. |
| 2026-09-24 | Go for all of v4 | Stack consistency; v5.0 evaluates Rust for the security auditor. |
| 2026-09-24 | dark-research is a native tool | 18 capabilities move into `adapters/research/`. |
| 2026-09-24 | dark-copilot not mentioned in v4 | Separate product; no code, docs, comments, or tests reference it. |
| 2026-09-24 | INV-11..INV-15 added | From the dark-research security audit's 10 missing controls. |

## Appendix C. Open questions

1. **Constitution maintainer team**: who are the initial
   members? The CRITIQUE-SOTA §12 identifies that this is a
   long-term commitment.
2. **Mod pack review process**: does `risk_class: exploit-
   development` require a higher-bar review than
   `risk_class: research`? v4-alpha.1 ships a baseline review
   process; tightening is iterative.
3. **Federation activation**: `adapters/federation/` ships as
   opt-in. What's the default federation policy?
   Conservative (off by default) or permissive (on if both
   peers opt in)?

These are tracked in `feat/v4-redesign/docs/OPEN_QUESTIONS.md`
(not yet written; v4-alpha.1 scope).

---

> **Version**: v4.0.0-alpha.1
> **Branch**: `feat/v4-redesign`
> **Last reviewed**: 2026-09-28
> **Next review**: when first 75-tool smoke test passes on
> `feat/v4-redesign`

> **§15 added 2026-09-28**: SOTA criticism of the MCP
> ecosystem + spec-driven pattern. Tier-1 sources verified
> 2026-09-28. See §15.10.

> **§14 added 2026-09-28**: SOTA criticism of the workflow
> runtime (M1, §8). Tier-1 sources verified 2026-09-28.
> See §14.8.
> **Status**: draft (pending peer review from constitution-
> maintainers team)
