# Spec 193: Dark Memory Agent Protocol (DMAP) v1.1

> **Status:** Atomic spec 2.1 (SessionState) IMPLEMENTED + tests green. Other 25 atomic specs pending.
> **Vibe case:** C5 (strategic planning / architecture)
> **Constitution ref:** dark-agents/dark-memory-mcp@1.0.0
> **Author:** Opita Code + dark-research-mcp build agent
> **Date:** 2026-07-16

## §0 Metadata

| Field | Value |
|---|---|
| spec_id (dark-mem) | 193 |
| parent specs | 188 (delegation), 189 (persona+minset), 190 (context handoff), 191 (integration), 192 (unified arch v1) |
| supersedes | spec 192's "17-bundled-spec plan" (which violated atomicity) |
| derived atomic specs | 26 (see §8) |
| main package | `internal/vlp/` |
| first atomic spec implemented | **2.1 SessionState** |

## §1 Problem statement

Today, `dark-memory-mcp` exposes 25 tools via MCP and trusts the LLM to call them correctly per turn. This is brittle (research-backed):

- **Anthropic Context Engineering (Sep 2025):** LLM "context rot" — as the prompt grows, recall degrades. Best practice is "just-in-time retrieval" with minimal context per task.
- **Anthropic Multi-Agent Research (Jun 2025):** production multi-agent systems use 15× more tokens than chat, but outperform single-agent by 90.2% on breadth-first queries. The key is server-driven loop control, not LLM-driven tool selection.
- **MCP 2026-07-28 RC (locks May 21, ships July 28, 2026):** introduces Tasks extension, MRTR (Multi Round-Trip Requests), Resources, Prompts, Extensions framework — but **none of these are adopted yet** in dark-memory-mcp.
- **OWASP MCP Top 10 — MCP10:2025 Context Injection:** context shared across sessions/tasks without scoping leads to leaks. Mitigation = atomic isolation per task.
- **A2A Protocol v1.0.0 (Google, complementary to MCP):** introduces explicit Task lifecycle, handoff vs delegation semantics, Agent Card discovery.

The current architecture has no state machine per session, no per-task context isolation, no mod system for context injection/validation, no subagent delegation, and no persona/minset distinction.

## §2 Scope

### 2.1 In scope (v1.1)

- Per-session state machine (drafting → active → judging → complete | needs_human)
- Atomic context injection (3-tier memory + Contextual Retrieval + per-task shapes)
- Mod system (Go plugins + 3 first-party mods: cve-context, threat-intel, compliance)
- Subagent delegation (orchestrator-worker + TaskHandle + handoff contracts + blackboard)
- Persona (stored in constitution) + Minset (computed per-turn)
- Harness adapters for opencode (Go), Claude Code (TS), Cursor (TS)
- Conformance test suite per adapter
- Reference implementation: CVE investigation workflow

### 2.2 Out of scope (v1.1)

- Async streaming responses (deferred to v1.2)
- Multi-language harness adapters beyond opencode/Claude Code/Cursor (community-driven after v1.1)
- Vector store beyond sqlite FTS5 / pgvector (deferred to v1.2)
- Server-side LLM-as-judge (`dark_ssd_drift_judge` remains external; integration in v1.2)
- MCP 2026-07-28-specific features (Tasks extension, MRTR) — wait for mcp-go Tier 1 SDK support

### 2.3 Backward compatibility

The 25 existing tools (8 namespaces) remain unchanged. All v1.1 capabilities are **opt-in** via new packages (`internal/vlp/`, `internal/mods/`, etc.) and new tools layered on top.

## §3 Architecture overview

DMAP v1.1 has 6 layers. Each layer has **one** responsibility:

```
┌──────────────────────────────────────────────────────────────────────────┐
│  Layer 6: Harness Adapter                                                │
│  Bridge harness lifecycle events ↔ MCP server calls                      │
│  (opencode, Claude Code, Cursor)                                         │
├──────────────────────────────────────────────────────────────────────────┤
│  Layer 5: Mod System                                                     │
│  Pluggable context injectors + validators                                │
│  (Mod{OnBrief, OnPropose, OnRecord, OnComplete})                         │
├──────────────────────────────────────────────────────────────────────────┤
│  Layer 4: Persona + Minset                                               │
│  Configure WHO (persona, stored) and HOW (minset, computed)              │
├──────────────────────────────────────────────────────────────────────────┤
│  Layer 3: Delegation                                                     │
│  Spawn subagents and orchestrate handoff                                 │
│  (TaskHandle + HandoffContract + Blackboard + RoleRegistry)              │
├──────────────────────────────────────────────────────────────────────────┤
│  Layer 2: Loop Coordinator (VLP)                                         │
│  Drive per-session state machine (server-driven loop)                    │
│  (Brief / Propose / Record / Complete primitives)                        │
├──────────────────────────────────────────────────────────────────────────┤
│  Layer 1: Atomic Context                                                 │
│  Select + inject minimal context per task (server-driven)                │
│  (ContextShape + Contextualizer + Retriever + TierCoordinator)           │
├──────────────────────────────────────────────────────────────────────────┤
│  Layer 0: Foundation (existing, shipped)                                 │
│  25 tools (8 namespaces) + 7 INV-* + Store + Audit + Constitution         │
└──────────────────────────────────────────────────────────────────────────┘
```

## §4 Layer decomposition

### Layer 0 — Foundation (existing, no changes)
- **Responsibility:** persist + audit + enforce invariants
- **Components:** 25 tools, 7 INV-*, Store (sqlite + postgres), write_audit table, Constitution registry
- **Atomic specs:** none — already shipped

### Layer 1 — Atomic Context
- **Responsibility:** select + inject minimal context per task (server-driven)
- **Interface:** `ContextSelector.Select(taskCtx) → ContextPackage`
- **Research basis:** MemGPT 3-tier memory + Anthropic Contextual Retrieval (49-67% reduction in failed retrievals) + OWASP MCP10 mitigation
- **Atomic specs:** 1.1, 1.2, 1.3, 1.4, 1.5 (5 specs, ~1350 LoC total)

### Layer 2 — Loop Coordinator (VLP)
- **Responsibility:** drive per-session state machine (server-driven loop)
- **Interface:** `Coordinator.HandleEvent(sessionID, event, payload) → StateDelta` (spec 2.5)
- **Research basis:** Anthropic multi-agent research + Claude Code subagents + LangGraph supervisor + A2A delegation semantics
- **Atomic specs:** 2.1, 2.2, 2.3, 2.4, 2.5 (5 specs, ~1050 LoC total)
- **FIRST IMPLEMENTED:** 2.1 SessionState (this iteration — see §4.2.1)

#### §4.2.1 Normative: atomic spec 2.1 (SessionState)

**Source of truth**: `internal/vlp/state.go` + `internal/vlp/state_test.go`. This subsection is a normative summary; the table in `state.go` is authoritative.

| Property | Value |
|---|---|
| Package | `internal/vlp` |
| Acceptance test | `TestTransition_AllValidPaths` |
| Transition cardinality | `expectedTransitionCount = 10` (enforced by `TestTransition_TableCardinality`) |
| Function contract | `Transition(from State, event Event, verdict Verdict) → (State, error)` |
| Trust boundary | Caller-supplied `from` is trusted; spec 2.3 (VLPPersistence) MUST do atomic CAS |

**States (8, including zero-value sentinel)**:
`StateUnknown` (zero), `StateIdle`, `StateDraftingSpec`, `StateSpecActive`, `StateDriftJudging`, `StateComplete` (terminal), `StateNeedsHuman` (terminal), `StateAborted` (terminal).

APPEND-ONLY: new states must be appended at end of const block.

**Events (5)**:
`EventUnknown` (zero), `EventSessionStart`, `EventVibePublish`, `EventArtifactLog`, `EventDriftLog` (requires verdict payload), `EventAbort`. APPEND-ONLY.

**Verdicts (3)**:
`VerdictUnknown` (zero, "no payload"), `VerdictAligned`, `VerdictDriftDetected`, `VerdictNeedsHuman`. APPEND-ONLY.

**Transition table (10 rows, authoritative in `state.go`)**:

| From | Event | Verdict | To | Notes |
|---|---|---|---|---|
| idle | session_start | — | drafting_spec | session_start opens loop |
| idle | abort | — | aborted | operator can cancel before starting |
| drafting_spec | vibe_publish | — | spec_active | spec published |
| drafting_spec | abort | — | aborted | operator aborted before spec |
| spec_active | artifact_log | — | drift_judging | artifact logged, awaiting drift verdict |
| spec_active | abort | — | aborted | operator abort |
| drift_judging | drift_log | aligned | complete | drift_judge: aligned → complete |
| drift_judging | drift_log | drift_detected | spec_active | loop back to regenerate |
| drift_judging | drift_log | needs_human | needs_human | escalate |
| drift_judging | abort | — | aborted | operator abort during judging |

**Out of scope for spec 2.1**: persistence (spec 2.3), audit (spec 2.4), loop driver (spec 2.5). Spec 2.1 is pure in-memory.

### Layer 3 — Delegation
- **Responsibility:** spawn subagents and orchestrate handoff
- **Interface:** `Delegator.Spawn(parent, role, task) → TaskHandle`
- **Research basis:** Anthropic orchestrator-worker + A2A TaskHandle + HumanLayer scratchpad + 4 canonical handoff patterns (push/pull/blackboard/streaming)
- **Atomic specs:** 3.1, 3.2, 3.3, 3.4 (4 specs, ~800 LoC total)

### Layer 4 — Persona + Minset
- **Responsibility:** configure WHO (persona, stored) and HOW (minset, computed)
- **Interface:** `PersonaStore.Get(projectID) → Persona` + `Minset.Compute(state, spec, mods) → Minset`
- **Research basis:** Anthropic Persona Vectors (arXiv 2507.21509) — directions in activation space controlling character traits
- **Atomic specs:** 4.1, 4.2, 4.3 (3 specs, ~650 LoC total)

### Layer 5 — Mod System
- **Responsibility:** pluggable context injectors + validators
- **Interface:** `Mod{OnBrief, OnPropose, OnRecord, OnComplete}(ctx, payload) → Result`
- **Research basis:** Go plugin model + first-party CVE/threat/compliance use cases
- **Atomic specs:** 5.1, 5.2, 5.3, 5.4, 5.5, 5.6 (6 specs, ~1600 LoC total)

### Layer 6 — Harness Adapter
- **Responsibility:** bridge harness lifecycle events ↔ MCP server calls
- **Interface:** per-harness (opencode hooks, Claude Code hooks, Cursor hooks)
- **Research basis:** Claude Code subagent patterns + opencode agent events + MCP client SDKs
- **Atomic specs:** 6.1, 6.2, 6.3 (3 specs, ~1200 LoC total)

## §5 Cross-layer contracts

| From → To | Contract | Semantics |
|---|---|---|
| L1 → L2 | `ContextPackage` passed in `Brief()` contains only T1+T2 references, never raw payloads | <4000 tokens default, hard cap, budget violation = warn + drop lowest-priority chunks |
| L2 → L3 | `VLPPackage.Record()` emits `TaskHandle` on every subagent spawn | `task_id` UUID v7, state machine synced with parent session state |
| L3 → L4 | `RoleRegistry` computes persona + minset for spawned `TaskHandle` | persona inherited from parent; minset can diverge with explicit declaration |
| L4 → L5 | `PersonaDriftDetector` reads `write_audit` + mod behavior | deviation score > 0.7 → trigger `audit_alert` + state transition to `needs_human` |
| L5 → L6 | Adapters consume only `VLPPackage` + `PersonaStore` + `Minset` | never directly query Store; all access goes through layered interfaces |

## §6 Atomic spec dependency DAG

```
Layer 0 (Foundation) ──────────────────────────────────────────────────┐
   │                                                                   │
   ├─► L1.1 (Shape) ──────────┐                                       │
   ├─► L1.2 (Contextualizer) ─┤                                       │
   ├─► L1.3 (Retriever) ◄─────┤                                       │
   │                          ▼                                       │
   │                       L1.4 (TierCoord)                            │
   │                          │                                       │
   │                          ▼                                       │
   │                       L1.5 (AtomicCtx UseCase) ◄── Layer 1 done  │
   │                                                                   │
   ├─► L2.1 (SessionState) ───┐  ◄── FIRST IMPLEMENTED                │
   ├─► L2.2 (VLPPackage) ◄────┤                                       │
   ├─► L2.3 (VLPPersistence) ◄┤                                       │
   └─► L2.4 (VLPAuditor) ◄────┘                                       │
                              │                                       │
                              ▼                                       │
                       L2.5 (VLP LoopUseCase) ◄── Layer 2 done         │
                                                                      │
       L2.5 ───┬──► L3.1 (TaskHandle)                                  │
               ├──► L3.2 (HandoffContract)                             │
               ├──► L3.3 (Blackboard)                                  │
               └──► L3.4 (RoleRegistry) ◄── 4.1, 4.2                  │
                                                                      │
       L2.5 ───┬──► L4.1 (PersonaSchema)                               │
               ├──► L4.2 (MinsetModes)                                 │
               └──► L4.3 (DriftDetector) ◄── 2.4, 4.1                 │
                                                                      │
       L2.5 ───┬──► L5.1 (ModRuntime)                                  │
               └──► L5.2 (ModHookContract) ◄── 2.2                    │
                                                                      │
       5.1 + 5.2 ─► L5.3 (ModRegistry)                                 │
                                                                      │
       L5.3 ───┬──► L5.4 (ModCVE)                                      │
               ├──► L5.5 (ModThreat)                                   │
               └──► L5.6 (ModCompliance)                               │
                                                                      │
       L2.5 ───┬──► L6.1 (OpenCodeAdapter)                             │
               ├──► L6.2 (ClaudeCodeAdapter)                           │
               └──► L6.3 (CursorAdapter)                               │
```

## §7 Acceptance criteria for the protocol

| Criterion | Target |
|---|---|
| 26 atomic specs implemented + acceptance tests passing | 100% |
| opencode adapter demonstrably runs end-to-end | 1 demo (CVE workflow) |
| 3 first-party mods demonstrably inject context | 3 demos |
| Drift judgment pass rate for "aligned" | > 80% on integration corpus |
| Token budget per turn (CVE workflow) | < 5000 (down from ~12K baseline) |
| Persona drift detection rate | > 95% on synthetic mod-behavior tests |
| Cross-session info leaks | 0 (per OWASP MCP10) |
| Backward compat: existing 25 tools untouched | 100% |

## §8 Atomic spec registry (full list)

See [§4 Layer decomposition](#4-layer-decomposition) above. Total: **26 atomic specs** across Layers 1-6, plus this main spec = 27 specs total.

| ID | Name | LoC | Deps | Status |
|---|---|---|---|---|
| 1.1 | ContextShape | 150 | L0 | pending |
| 1.2 | Contextualizer | 300 | L0 | pending |
| 1.3 | Retriever | 400 | 1.2 | pending |
| 1.4 | TierCoordinator | 300 | 1.1, 1.3 | pending |
| 1.5 | AtomicContextUseCase | 200 | 1.1-1.4 | pending |
| **2.1** | **SessionState** | **~250** | **L0** | **✅ IMPLEMENTED (this iteration)** |
| 2.2 | VLPPackage | 250 | 2.1 | pending |
| 2.3 | VLPPersistence | 200 | 2.1 | pending |
| 2.4 | VLPAuditor | 150 | 2.3 | pending |
| 2.5 | VLPLoopUseCase | 250 | 2.1-2.4 | pending |
| 3.1 | TaskHandle | 250 | 2.5 | pending |
| 3.2 | HandoffContract | 200 | L0 | pending |
| 3.3 | Blackboard | 200 | L0 | pending |
| 3.4 | RoleRegistry | 150 | 4.1, 4.2 | pending |
| 4.1 | PersonaSchema | 200 | L0 | pending |
| 4.2 | MinsetModes | 250 | L0 | pending |
| 4.3 | PersonaDriftDetector | 200 | 2.4, 4.1 | pending |
| 5.1 | ModRuntime | 300 | L0 | pending |
| 5.2 | ModHookContract | 200 | 2.2 | pending |
| 5.3 | ModRegistry | 200 | 5.1, 5.2 | pending |
| 5.4 | ModCVEContext | 300 | 5.3 | pending |
| 5.5 | ModThreatIntel | 300 | 5.3 | pending |
| 5.6 | ModCompliance | 300 | 5.3 | pending |
| 6.1 | OpenCodeAdapter | 400 | 2.5 | pending |
| 6.2 | ClaudeCodeAdapter | 400 | 2.5 | pending |
| 6.3 | CursorAdapter | 400 | 2.5 | pending |

## §9 Out of scope (non-goals)

- **No LLM-side logic.** All loop control is server-side. LLM is content generator only.
- **No auto-generation of specs.** Vibe_publish requires a complete spec from the caller.
- **No streaming responses in v1.1.** SSE/streaming deferred to v1.2 (when MCP Tasks extension stabilizes).
- **No vector store in v1.1.** Uses sqlite FTS5 + pgvector only. Dedicated vector DB deferred.
- **No server-side LLM judge.** `dark_ssd_drift_judge` remains external.
- **No hard persona steering via activation vectors.** Persona is enforced at the system level (constitution + mods), not at the inference activation level (out of scope for an MCP server).

## §10 Open decisions (linked to atomic decision specs)

| ID | Decision | Default | Spec link |
|---|---|---|---|
| D15 | Minset: stored or computed? | Computed (logged in drift_report) | 4.2 |
| D16 | Persona scope: org, project, or both? | Both (org default + project override) | 4.1 |
| D17 | Subagent implementation: real subprocess or "soft" via state machine? | Soft first, real subprocess when MCP 2026-07-28+ | 3.1 |
| D18 | Handoff medium: filesystem, MCP Resources, or both? | Both (filesystem for big outputs, Resources for state) | 3.2, 3.3 |
| D19 | How many minset modes? | 5 (Recon, Exploit, Adjudicate, Synthesize, Hunt) | 4.2 |
| D20 | CVE workflow: demo or release? | Both (demo in spec 202, release as v1.1) | (meta) |

---

**This spec is the source of truth for DMAP v1.1. All 26 atomic specs derive from it. Any change to a layer's responsibility requires updating this spec first.**