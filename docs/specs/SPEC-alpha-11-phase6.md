# Phase 6 — alpha.18 close-out (alpha.18.1)

> **Audience**: anyone picking up the alpha.18.1 work.
> **TL;DR**: 6 items, ~1,520 LoC, closes all known gaps before alpha.19.
> **Source**: meta-summary row 2257 + sota-critique.md §5.2 + agent_memory row 482.
> **Vibe-loop**: `alpha-11-phase-6`.

## 0. Why this phase?

Phase 5 (alpha.18) shipped the vibe-case-aware memory subsystem, but
several pre-existing gaps remain live:

1. **Vibe-case mapping inconsistency (LIVE BUG)**: `internal/v4alpha/judge/rubric.go:395-457`
   encodes v3-legacy mappings (C3=image, C4=video, C5=bundle, C6=infra, C7=governance)
   while the canonical mapping in `internal/v4alpha/vibe/spec.go:14-16` is
   (C3=decision, C4=research, C5=video, C6=audio, C7=multi). 4 docs
   (`persona-registry-v4.md`, `GLOSARIO.md:766-769`, `ADR-007:435-436`,
   `research-backends.md:16-17`) are also inconsistent.

2. **Two STUBS from Phase 4**: `mindset_apply` and `delegate_intent`
   were declared STUB in alpha.17 Chunk 4.2 with "full impl alpha.18"
   promise — Phase 5 took priority. Need to ship the real impl.

3. **Audit gaps (ADR-017 + ADR-019)**: deferred from Phase 2 Option B.
   Ed25519 payload signature + structured payload columns.

4. **Mutation coverage** (agent_memory row 482): tools 19.4%, recall
   20%, agentbootstrap 42.1% mcover. NOT-COVERED mutants (no bugs
   known), but blocks gremlins ≥0.80 score.

## 1. Items

| # | Item | Complexity | LoC | Risk | Status |
|---|---|---|---|---|---|
| 6.1 | Vibe-case mapping reconciliation (rubric.go + 4 docs) | S-M | ~200 | MEDIUM (live bug) | PLANNED |
| 6.2 | Full impl `mindset_apply` (procedural composition + cache + judge validation) | M | ~250 | LOW | PLANNED |
| 6.3 | Full impl `delegate_intent` (DECIDE→PLAN→MIND→CURATE) | M | ~300 | LOW-MED | PLANNED |
| 6.4 | Mutation coverage close (3 packages, Store-mock tests) | M | ~400 | LOW | PLANNED |
| 6.5 | ADR-017 Ed25519 payload signature | S | ~120 | LOW | PLANNED |
| 6.6 | ADR-019 payload BLOB split into structured columns | M | ~250 | MEDIUM (schema) | PLANNED |

## 2. Dependency graph

```
6.1 (rubric mapping)
  ↓
6.2 (mindset) → 6.3 (delegate) → 6.5 (Ed25519) → 6.6 (payload split)
                                                       ↓
                                                    6.4 (mutation, parallel)
```

6.4 is independent and can run parallel to anything else.

## 3. Acceptance criteria per item

### 6.1 Vibe-case mapping reconciliation

**Code changes** (`internal/v4alpha/judge/rubric.go`):

| VibeCase | Current criteria | New criteria (canonical) |
|---|---|---|
| C3 (decision) | composition/color_palette/subject_match/accessibility/no_artifact | rationale_clarity/evidence_quality/alternatives_considered/reversibility/stakeholder_impact |
| C4 (research) | subject_consistency/scene_transitions/audio_sync/narrative/duration_match | source_diversity/citation_quality/methodology/novelty/reproducibility |
| C5 (video) | provenance/lockfile_parity/transitive_safety/manifests_consistent/audit_chain | subject_consistency/scene_transitions/audio_sync/narrative/duration_match |
| C6 (audio) | idempotence/observability/rollback/blast_radius/security | speech_clarity/noise_floor/voice_consistency/pacing/timbre_preservation |
| C7 (multi) | invariant_compliance/audit_chain/rbac/provenance/documentation | unchanged |

Weights still sum to 1.0 per rubric. Each rubric gets a new PersonaID:
- C3 → `judge-decision` (NEW)
- C4 → `judge-research` (NEW)
- C5 → `judge-cross-modal` (changed from `judge-coverage`)
- C6 → `judge-pipeline` (changed from `judge-pipeline` — keep, but criteria swapped)
- C7 → `judge-evidential` (changed from `judge-evidential` — keep)

**Code changes** (`internal/v4alpha/judge/personas_v4.go`):

Add `judge-decision` and `judge-research` to `defaultPersonaContents`. Each
gets PromptTemplate, EvaluationLens, BiasControls, RequiredEvidence.

**Doc changes**:

- `docs/persona-registry-v4.md` — rebuild §2.1, §2.2, §2.3 cross-reference
  table to canonical mapping.
- `docs/GLOSARIO.md:766-769` — annotate as "deprecated v3 mapping" pointing
  to canonical source.
- `docs/decisions/ADR-007-judge-pipeline-v4.md:435-436` — update C1-C7
  description to canonical.
- `docs/research-backends.md:16-17` — update backend-by-vibe_case table
  to canonical mapping.

**Tests**:

- New: `TestRubric_AllCanonicalC3C7PersonaIDs` — verify each C3-C7 rubric
  points at the correct persona per canonical mapping.
- New: `TestPersonaRegistry_DefaultsContainDecisionAndResearch` — verify
  judge-decision + judge-research are registered.
- New: `TestPersonaContent_DefaultsValidateForDecisionAndResearch` —
  verify the new persona contents pass Validate().
- Update: any existing test asserting `judge-cross-modal` for C3 or
  `judge-coverage` for C5 → update to new mapping.

### 6.2 Full impl mindset_apply

**Spec**: `docs/specs/SPEC-alpha-11-phase4.md:218` defines the contract:
"Procedurally compose a subagent system_prompt for the given
(vibe_case, task_description) and validate it via LLM-as-judge before
returning. Cached in agent_memory (TTL via DARK_MINDSET_CACHE_TTL,
default 1h). Composition iterates up to DARK_MINDSET_MAX_ITERATIONS
(default 3) if the judge rejects the first attempt."

**Code changes** (`internal/v4alpha/transport/mcp/mindset.go` — replace stub):

- Compose: use PersonaContent for the (vibe_case, persona_id) tuple as
  the prompt template; prepend operator context (task_description +
  vibe_case); append validation rubric.
- Cache: sha256(vibe_case || task_description || model_floor) → stored
  in agent_memory with `kind=context, tags=skill_loaded:v1+persona:v1+<persona_id>`.
  TTL via DARK_MINDSET_CACHE_TTL (default 3600s).
- Judge validation: invoke `dark_memory_judge` with
  `eval_type=mindset_compose`, `vibe_case` from input, `persona_id`,
  `spec_intent="mindset_apply validation"`, artifact=system_prompt.
  If verdict != aligned → retry up to DARK_MINDSET_MAX_ITERATIONS (3)
  with LLM-driven refinement of the prompt.
- If all 3 iterations rejected → needs_human response.

### 6.3 Full impl delegate_intent

**Spec**: `docs/specs/SPEC-alpha-11-phase4.md:241` defines the contract:
"Wave 5C: Decide whether an intent is handled inline, delegated to
sub-agents, or refused (A1: Memory decides). Runs the DelegationRouter
pipeline: DECIDE (deterministic rules per vibe_case + bounded LLM
choice) → PLAN (subtask graph with dependency batches) → MIND
(mindset_apply per subtask) → CURATE (agent_memory_delegate per
subtask: curated agent_memory context + C2 subagent binding)."

**Code changes** (`internal/v4alpha/transport/mcp/delegation.go` — replace stub):

- DECIDE: based on `vibe_case` + task complexity signals (e.g.,
  single-domain = inline; multi-domain = delegate; impossible/scope=
  outside = refuse).
- PLAN: split task_description into subtasks via bounded LLM call;
  each subtask gets its own vibe_case + depends_on graph.
- MIND: invoke `mindset_apply` (now full impl from 6.2) per subtask.
- CURATE: invoke `agent_memory_delegate` per subtask to bind subagent_id.

### 6.4 Mutation coverage close

**Spec**: `agent_memory row 482` defines the 3 gaps:
1. `internal/tools` 19.4% mcover (271 NOT COVERED) — needs Store mock
2. `internal/recall` 20% mcover (72 NOT COVERED) — needs Store mock
3. `internal/agentbootstrap` 42.1% mcover (22 NOT COVERED) — deeper error paths

**Code changes**:

- `internal/tools/mock_store_test.go` (NEW): mock `store.Store`
  interface for handler tests.
- `internal/recall/mock_store_test.go` (NEW): same pattern.
- `internal/agentbootstrap/clientinfo_test.go` (NEW): exercise clientInfo
  deeper error paths.
- Use `gremlins v0.6.0` from clean worktree with `.gremlins.yaml`
  (timeout-coefficient 15, strip embedder blobs).
- DO NOT re-run `go-mutesting` (retired, results invalid).

### 6.5 ADR-017 Ed25519 payload signature

**Spec**: alpha.15 deferred to alpha.18.1 per Phase 2 Option B.

**Code changes** (`internal/v4alpha/audit/signature.go` — NEW):

- `Sign(rowHash []byte, privateKey ed25519.PrivateKey) []byte` — Ed25519
  signature over the row_hash.
- `Verify(rowHash, signature, publicKey) bool` — public verification.
- Add `signature BLOB` column to `audit_log` (nullable for legacy rows).
- `audit.Writer.Write` + `WriteExec` sign the row_hash before UPDATE.
- Add `judge_util_verify(content_b64, signature_b64, pubkey_hex)` — already
  exists per `dark-memory://docs/system-prompt.md` (7 utilities). Verify it
  actually verifies signatures (not just hashes).

**Tests**: chain integrity test (sign → modify row → verify catches
forgery). 5 tests.

### 6.6 ADR-019 payload BLOB split

**Spec**: alpha.15 deferred per Phase 2 Option B.

**Code changes** (`internal/v4alpha/audit/payload_split.go` — NEW):

- Add structured columns to `audit_log`:
  - `payload_actor TEXT` (extracted from payload BLOB)
  - `payload_target_type TEXT`
  - `payload_eval_type TEXT`
  - `payload_vibe_case TEXT`
  - `payload_session_kind TEXT`
- `audit.ApplyPayloadColumns(ctx, db)` — idempotent migration.
- `audit.Writer` extracts structured fields from payload on Write (parses
  the BLOB JSON; falls back to NULL on parse error).
- `dark_memory_audit_query` (NEW tool) — SQL-based query over the
  structured columns without BLOB parsing.

**Tests**: 8 tests covering extract + query + migration idempotency.

## 4. Operator decisions

| # | Decision | Default |
|---|---|---|
| OD7 | Chunk 6.1 ship ordering (rubric mapping FIRST or after 6.5/6.6) | FIRST (live bug) |
| OD8 | Use `gremlins` vs hire mutation coverage tool | gremlins v0.6.0 |
| OD9 | 6.5/6.6 ship as one Chunk or split | one Chunk |
| OD10 | alpha.18.1 tag local | YES |

## 5. Drift_judge contract

Per chunk:
1. vibe_publish with `artifact_ref: git_sha + path` + `spec_intent`
2. drift_judge returns aligned/drift_detected/needs_human
3. aligned → proceed; drift_detected → fix + re-publish; needs_human → STOP

## 6. Atomic mirror per ADR-008

Per chunk: 1 SUMMARY pinned + N SECTION pinned=false.

## 7. Cross-references

- `docs/v4-alpha-11-plan.md` §5 — Phase 5 SHIPPED (alpha.18).
- `docs/sota-critique.md` §5.2 — 3 tractable agent-memory gaps NOW ENFORCED.
- `agent_memory row 2257` — Phase 5 meta SUMMARY.
- `agent_memory row 482` — mutation coverage gap todo.
- `agent_memory row 106` — v2.8.0 edge case catalog (C1 inheritance attacks).
