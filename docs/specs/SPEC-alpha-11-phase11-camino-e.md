# SPEC-alpha-11-phase11-camino-e — Phase 11 (Camino E: polish + spec)

**Status (2026-10-03)**: spec approved via vibe_loop. Drift verdict pending re-publish.

**Branch**: `feat/v4-redesign` (local-only, no remote push).

**Cross-version lockstep hash pin** unchanged:
`4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb`.

## §0 TL;DR

Phase 11 = **Camino E** of the 5 caminos proposed 2026-10-03.
Closes operational debt + pre-specs Phase 12. 4 tasks, 1 dependency
chain (T-404 → T-403). ~350 LoC code + ~400 LoC spec docs + 1
pinned agent_memory row. ~1.5 weeks focused. Risk LOW. North star
**maintained** (~85% to v4.0.0-GA), **NOT advanced** (no new §8
primitive shipped).

## §1 Why Phase 11 exists

After alpha.21 ship (Phase 10 deploy infra + OD7 e2e gate), the next
work could be one of 4 caminos:
- **A** Polish operativo (2-3 wk, LOW risk, no GA Δ)
- **B** Modification events (4-6 wk, MED-HIGH risk, +15% GA Δ)
- **C** Streamable HTTP (2-3 wk, MED risk, +5% GA Δ)
- **D** OAuth 2.1 (4-8 wk, HIGH risk, +15% GA Δ)

The operator chose **Camino E = A + a complete spec of B**:
execute A's polish chunks + write B's full spec (no code yet),
so Phase 12 has an approved contract to execute against.

## §2 Non-goals (scope boundary)

Explicitly OUT of Phase 11:
- Real modification events code (Phase 12)
- The 11 BUG-10 10b-e MCP tools beyond T-402's 7
- Streamable HTTP transport (Camino C, deferred)
- OAuth 2.1 + capability token (Camino D, deferred)
- ADR-016 transparency log (Rekor-style, deferred to GA)
- ADR-012 RLJF (active learning, deferred to v4.1.0+)

## §3 Data model

**No schema changes.** All 4 tasks are additive + env-gated +
docs/atomic-mirror only.

- T-401: env-only (`DARK_AUDIT_HMAC_KEY`). No new column.
- T-402: tools registry entry. No schema.
- T-403: docs/ subdirectory. No schema.
- T-404: 1 new agent_memory row. Schema v31 unchanged.

## §4 Implementation chunks

### T-401 Enable audit HMAC chain in production (~50 LoC + docs)

- Set `DARK_AUDIT_HMAC_KEY` env (multi-key rotation per
  `KeyringFromEnv` design).
- Run `dark_memory_audit_export` + `dark_memory_audit_verify`;
  expect `Status=OK` on all rows.
- Document rotation policy in `docs/audit-hmac-rotation.md`.
- Update `docs/deploy-mcp-binary.md` with the new env.
- **Closes OD7 test (c)** from `N/A bypassed` → `PASS`.

### T-402 Expose judge_util_* MCP tools in v3 mode (~150 LoC + tests)

- The 7 `judge_util_*` tools live in v4alpha MCP server
  (only available with `DARK_MEM_BRIDGE=v4alpha`).
- v3 default mode (`DARK_MEM_BRIDGE=0`) does NOT expose them.
- Add the 7 tools (normalize, validate_overrides,
  pattern_descriptions, verify, verify_hash, trace,
  validate_trace) to the v3 tools registry when
  `DARK_JUDGE_BRIDGE_MODE=v3util`.
- Wire shape: pull from `internal/v4alpha/judge/judge_util.go`.
- **Closes OD7 tests (d) + (i)** from `partial` → `PASS`.

### T-403 Write complete spec for Phase 12 modification events (~400 LoC)

- This task is the Phase 12 contract.
- Sections required (per alpha.11 spec tradition):
  - §0 TL;DR + north star position
  - §1 problem statement (why §8 primitiva #4 matters)
  - §2 non-goals (scope boundary for Phase 12)
  - §3 data model (modification_events schema + indexes +
    project_id + relationships to write_audit +
    decision_transitions + entity graph)
  - §4 writer (modification_writer.go analog of audit.Writer)
  - §5 LLM-as-judge for modifications (eval_type=modification_audit
    + new persona judge-modifications + G-Eval rubric)
  - §6 replay tool (modification_replay reconstructs history
    from event log + rationale-first INV-20)
  - §7 OD7 e2e gate for Phase 12 (8+ tests)
  - §8 implementation chunks (T-101..T-106 with LoC + tests)
  - §9 cross-refs
  - §10 LUCIDEZ honest disclosure
- Reference `SPEC-alpha-11-phase5.md` + `SPEC-alpha-11-phase7.md`
  for format conventions.

### T-404 Save pinned decision row (depends_on T-403)

- `agent_memory_save(kind=decision, memory_type=episodic, pinned=true)`
- Title: "Phase 12 = Camino B (modification events) per
  SPEC-alpha-11-phase12-modification-events.md"
- Content captures: operator decision, link to T-403 spec doc,
  OD7 e2e gate reminder.
- Row + spec doc together form the operational contract for
  Phase 12.

## §5 OD7 e2e gate (Phase 11 SHIP requirement)

Before tagging `v4.0.0-alpha.22`:
- (a) session lifecycle chain (`start` + `heartbeat` + `status`)
- (b) vibe_publish + drift_judge round-trip
- (c) audit chain cross-process monotonicity with HMAC enabled
- (d) override pattern sweep via v3 judge_util_* tools
- (e) persona registry count = 14 (8 v2 + 6 v4alpha)
- (f) concurrent write stress ≥10 sequential
- (g) cross-session atomic mirror survival across supervisor
      respawn
- (h) needs_human surface when drift_judge insufficient
- (i) override pattern sweep (re-run with HMAC on)

**0 critical findings required.**

## §6 Cross-refs

- `docs/v4-status.md` §1.1-1.8 (Phase 1-9 alpha.13-20.1 history)
- `docs/v4-alpha-11-plan.md` §7-8 (Phase 7 + Phase 9 changelogs)
- `docs/sota-critique.md` §5.2.5 (override patterns §8.9 close)
- `docs/deploy-mcp-binary.md` (deploy procedure for the new env)
- `internal/audit/hmac.go` (KeyringFromEnv design, T-401 target)
- `internal/v4alpha/judge/judge_util.go` (T-402 wire shape source)
- `internal/v4alpha/transport/mcp/delegation.go`
  (`DelegateIntentInput/Output` types — SPEC §3 reference)
- atomic mirror row 2394 (Phase 10 Chunk 10.2 SUMMARY pinned)

## §7 LUCIDEZ honest disclosure

- **SPEC-vs-reality**: Phase 11 estimates above are by-spec;
  actual LoC drift per chunk is the documented dark-memory
  norm (~+165% in Phase 9 alpha.20). Operator-approved per the
  alpha.20 disclosure table.
- **T-403 scope risk**: spec is 400 LoC; the Phase 12 code work
  it specifies is 5x that. The spec cost is justified by the
  Phase 9 lesson (Phase 8 e2e found 2 critical wiring gaps
  invisible to unit tests because there was no spec to test
  against).
- **Camino E vs B alone**: chose E (lower risk) over B alone
  because Phase 9 alpha.20 had to spend 3 of 9 chunks on
  close-out bugs. Closing operational debt first reduces Phase
  12's "Phase 9 repeat" risk.
- **No D motion**: OAuth 2.1 was deferred not because it's
  unimportant but because it requires a threat model doc
  before code (per the alpha.17 BUG-10 10b 4-doc plan
  precedent). Threat model is Phase 12+ territory.

## §8 Atomic mirror

After T-404 SHIP:
- 1 SUMMARY pinned (`kind=decision`, `agent_id=alpha-11-phase11`)
- 4 SECTION pinned=false (T-401..T-404)
- Cross-session survival tested via supervisor respawn
  (Phase 10 deploy infrastructure).