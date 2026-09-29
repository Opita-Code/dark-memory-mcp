# SPEC-alpha-11-pre1c4: summarize_session + skill-loaded tracking

> **Estado**: Accepted (operator mandate 2026-09-28 "Phase 1C")
> **Fecha**: 2026-09-28
> **Autor**: Opita-AI (operator=nico, project_id=default)
> **Spec id**: SPEC-alpha-11-pre1c4
> **Mirror**: 1 SUMMARY + 5 SECTION (per ADR-008)
> **Vibe-loop**: alpha-11-phase-1c (Phase 1 of alpha.11+ plan)

## 1. Contexto

PRE-1 is the preflight foundation chunk that reduces
operational friction for BUG-10 10b-10e and the rest of
v4-alpha. C1 (RecallFiltered/ListFiltered) and C2
(docs_index) shipped 2026-09-28. C3 (Loadout for
session_start) is the next planned item but NOT
prerequisite for C4.

**C4 (summarize_session + skill-loaded tracking)** ships
now because:

1. The "skill-loaded tracking convention" was implicit in
   PRE-1 C2's lessons but never shipped (per row 2120 L2).
   C4 makes it explicit.
2. `dark_memory_summarize_session(session_id)` produces a
   handoff doc — useful for cross-session continuity when
   the harness restarts or the operator hands off to a
   new session.
3. C4 has zero dependency on C3's Loadout (per the
   corrected dependency analysis below).
4. C4 is small enough (M, ~250 LoC, LOW risk) to ship
   alongside the next Phase 1 item.

**Dependency analysis correction**: PRE-1 C4 does NOT
strictly depend on PRE-1 C3. C3 enriches session_start's
output; C4 enriches session_close's output (and a
post-hoc summarize of any session). Both can ship in any
order. The plan's "C4 depends on C3" was an overcautious
framing.

## 2. Decisión

Ship two MCP tools:

1. **`dark_memory_summarize_session(session_id)`** —
   returns a markdown handoff doc with: session metadata,
   pinned rows, open todos, recent writes (audit_log),
   skills loaded (skill_loaded rows), drift reports,
   judge verdicts.

2. **`dark_memory_skill_loaded(session_id, skill_name,
   version?, source?)`** — records a skill load event in
   agent_memory with `kind=observation, tags=skill_loaded:v1,
   skill:<name>`. The convention is the L2 lesson from
   PRE-1 C2 made explicit.

## 3. Tareas (vibe-loop tasks)

| ID | Task | Status | Notes |
|---|---|---|---|
| T1 | Write this spec | done | this file |
| T2 | Implement `internal/v4alpha/session/summarize.go` | pending | ~120 LoC |
| T3 | Implement `internal/v4alpha/session/summarize_test.go` | pending | ~150 LoC, 3 tests |
| T4 | Implement `internal/v4alpha/transport/mcp/summarize.go` | pending | ~80 LoC (both tools) |
| T5 | Wire into `server.go` (tool count 39 → 41) | pending | 2 new MCP tools |
| T6 | Update `CHANGELOG.md` ([4.0.0-alpha.12]) | pending | doc-only release |
| T7 | Update `docs/v4-status.md` (§1 tool count 38 → 41) | pending | doc-only release |
| T8 | Commit + atomic mirror | pending | per ADR-008 |
| T9 | drift_judge | pending | post-commit |

## 4. Verdict shape (per ADR-007)

```json
{
  "verdict": "aligned | drift_detected | needs_human",
  "confidence": 0.0-1.0,
  "reasoning": "...",
  "evidence": {
    "spec_intent": "summarize_session returns handoff doc + skill-loaded tracking convention",
    "files_changed": ["internal/v4alpha/session/summarize.go", "summarize_test.go", "transport/mcp/summarize.go", "server.go", "v4-status.md", "CHANGELOG.md"],
    "tools_added": ["dark_memory_summarize_session", "dark_memory_skill_loaded"],
    "tool_count": "39 → 41",
    "tests_passed": 3,
    "checks": {
      "summarize_returns_markdown": true,
      "skill_loaded_persists": true,
      "summarize_finds_skill_loaded": true,
      "build_clean": true,
      "atomic_mirror": true
    }
  }
}
```

## 5. Edge cases

- **EC-001 (empty artifact)**: N/A — code release.
- **EC-002 (LLM provider failure)**: N/A — no LLM call.
- **EC-005 (unknown persona_id)**: N/A — no personas used.
- **EC-006 (empty spec_intent)**: mitigated — this spec has
  clear intent ("summarize_session returns handoff doc +
  skill-loaded tracking convention").
- **EC-009 (arithmetic claim)**: covered by tests that
  count writes/runs/items.
- **EC-010 (doc-vs-code drift)**: covered by drift_judge
  post-commit.
- **NEW: EC-summary-001** — session_id not found in
  sessions table → return ErrNotFound (not a code path
  bug, just an honest 404).
- **NEW: EC-summary-002** — skill_name empty string →
  return ErrEmptySkill (defensive guard).
- **NEW: EC-summary-003** — agent_memory ListFiltered
  returns 0 skill_loaded rows for the session → still
  return a valid summary with skills_loaded=[]. Honest
  empty list, not an error.

## 6. Trade-offs (per ADR-007 §7)

- ✅ Additive API (new MCP tools, no breaking changes to
  existing surface).
- ✅ Single commit (vs 2 separate commits for summarize +
  skill_loaded).
- ✅ Markdown-only output for v1 (JSON is a future
  enhancement; markdown is human-readable + LLM-readable).
- ✅ Skills stored in agent_memory (no new table; follows
  PRE-1 C2's "system operator" pattern via tag prefix).
- ❌ NOT including the include filter (v1 returns
  everything; future versions can scope).
- ❌ NOT including session-level stat aggregation
  (writes/runs/items are per-session via Close()'s
  Summary, not recomputed by summarize_session).
- ❌ NOT changing the audit_log schema (queries are
  read-only).
- ❌ NOT implementing JSON output format (markdown v1;
  json v2).

## 7. Acceptance criteria

- [ ] `dark_memory_summarize_session(session_id)` returns
      a markdown string with 6 sections: Metadata, Pinned
      Rows, Open Todos, Recent Writes, Skills Loaded,
      Drift/Verdicts.
- [ ] `dark_memory_skill_loaded(session_id, skill_name,
      version, source)` writes an agent_memory row with
      kind=observation, tags=skill_loaded:v1+skill:<name>.
- [ ] `summarize_session` finds the skill_loaded rows via
      `ListFiltered(operator, ListFilter{TagPrefix:
      "skill_loaded:"})`.
- [ ] 3 new tests pass: empty session, populated session,
      skill_loaded round-trip.
- [ ] All existing v4alpha tests still pass (no
      regressions).
- [ ] `go build ./...` clean.
- [ ] Tool count goes from 39 → 41 (additive).
- [ ] Atomic mirror: 1 SUMMARY pinned + 5 SECTION pinned,
      agent_id=alpha-11-pre1c4.

## 8. Plan doc reference

- `docs/v4-alpha-11-plan.md` Phase 1 (Close + cheap wins):
  this spec is item 3 of 4 (chunk 7, PRE-1 C3, PRE-1 C4,
  BUG-12). Chunk 7 shipped in commit 829b6d1. This spec
  ships C4.
- `docs/sota-critique.md` §7.6.6 PRE-1 C3 + C4 sizing: M
  each, ~200 / ~180 LoC, LOW risk. C4 ships first because
  no prerequisite.

## 9. Next vibe-loop

After this spec ships:
- Phase 1B: PRE-1 C3 (Loadout for session_start) — M,
  ~200 LoC, ~2-3 days.
- Phase 1D: BUG-12 (cross-process monotonicity) — XS,
  ~40 LoC, ~1 day.