# SPEC-alpha-11-chunk7: SOTA-doc workstream close + alpha.11 plan announcement

> **Estado**: Accepted (operator mandate 2026-09-28 "arranca")
> **Fecha**: 2026-09-28
> **Autor**: Opita-AI (operator=nico, project_id=default)
> **Spec id**: SPEC-alpha-11-chunk7
> **Mirror**: 1 SUMMARY + 6 SECTION (per ADR-008)
> **Vibe-loop**: alpha.11-chunk7 (closes SOTA-doc workstream)

## 1. Contexto

The SOTA-doc workstream shipped 6 of 7 chunks (2026-09-28,
+2,380/-7 lines, 12 file operations). The 7th chunk closes
the workstream by updating the operator-facing docs
(`v4-status.md` + `CHANGELOG.md`), introduces the alpha.11
plan as a 5-vibe-loop structure, and applies the namespace
reframe (per the operator's 2026-09-28 question on
multi-tenant for a tool-of-tools MCP).

The SOTA-doc workstream's mandate was "retomar el trabajo
de v4, respecto a la documentación y crítica SOTA". The
criticism axis is now closed. The implementation axis
(alpha.11+) is the next concrete work.

The namespace reframe (per row 2173, decision 2026-09-28)
clarifies that `project_id` is a soft workstream namespace,
not a SaaS multi-tenant primitive. The hard isolation
primitive in v4 is `coexistence_group` (per-MCP `dark.db`),
not `project_id`. BUG-10 10b's sizing drops accordingly
(L, ~500 LoC, MEDIUM risk) — see sota-critique.md §7.6.9.

## 2. Decisión

Ship chunk 7 in 4 deliverables:

1. `docs/v4-status.md` — update header + §1 tool count +
   §7 SOTA-doc section + status flip to alpha.10.
2. `CHANGELOG.md` — add `[4.0.0-alpha.11]` entry at the
   top of the v4 section.
3. `docs/v4-alpha-11-plan.md` (NEW) — 5 vibe-loop plans
   (Phase 1-5) for the alpha.11+ work, with specs, sizing,
   dependencies, and the namespace primitive threat model.
4. `docs/sota-critique.md` §7.6 — apply namespace reframe
   (already shipped in commit 6b4daae + this commit's
   follow-up edits).

## 3. Tareas (vibe-loop tasks)

| ID | Task | Status | Notes |
|---|---|---|---|
| T1 | Apply namespace reframe to sota-critique.md §7.6 | done | commit 6b4daae + follow-up edits in this chunk |
| T2 | Add sota-critique.md §7.6.9 threat model | done | follow-up edits in this chunk |
| T3 | Update docs/v4-status.md (SOTA-doc summary + alpha.10) | pending | this commit |
| T4 | Add [4.0.0-alpha.11] to CHANGELOG.md | pending | this commit |
| T5 | Create docs/v4-alpha-11-plan.md (5 vibe-loops) | pending | this commit |
| T6 | Commit + atomic mirror (1 SUMMARY + 6 SECTION) | pending | this commit |
| T7 | drift_judge (LLM-as-judge of the spec vs artifact) | pending | post-commit |

## 4. Verdict shape (per ADR-007)

```json
{
  "verdict": "aligned | drift_detected | needs_human",
  "confidence": 0.0-1.0,
  "reasoning": "...",
  "evidence": {
    "spec_intent": "close SOTA-doc + announce alpha.11 plan + namespace reframe",
    "artifact_paths": ["docs/v4-status.md", "CHANGELOG.md", "docs/v4-alpha-11-plan.md", "docs/sota-critique.md"],
    "checks": {
      "v4-status.md_updated": true,
      "changelog_entry_added": true,
      "plan_doc_created": true,
      "namespace_reframe_applied": true,
      "build_clean": true
    }
  }
}
```

## 5. Edge cases

- EC-001: empty artifact (N/A — this is a doc release, not
  an artifact publish).
- EC-002: LLM provider failure (N/A — chunk 7 is doc-only,
  no LLM call).
- EC-003: prompt injection (N/A — no LLM input from
  operator).
- EC-005: unknown persona (N/A — chunk 7 doesn't add
  personas).
- EC-006: empty spec_intent (mitigated — this spec has
  intent "close SOTA-doc + announce alpha.11 plan +
  namespace reframe").

## 6. Trade-offs (per ADR-007 §7)

- ✅ Doc-only release (no schema bump, no test changes).
- ✅ Single commit (vs 4 separate commits) for atomic
  workstream close.
- ✅ Plan doc as a new file (vs embedded in sota-critique.md)
  for separation of concerns.
- ❌ NOT proposing any new ADRs (chunk 7 is the close, not
  new design).
- ❌ NOT touching the v4 binary (alpha.11+ is the next
  binary release).

## 7. Acceptance criteria

- [ ] v4-status.md reflects SOTA-doc workstream completion
      (6 of 7 chunks shipped, +2,380 lines, 17 ADRs + 1 BUG)
- [ ] CHANGELOG.md has [4.0.0-alpha.11] entry at top
- [ ] docs/v4-alpha-11-plan.md exists with 5 vibe-loop plans
- [ ] sota-critique.md §7.6 has namespace reframe (BUG-10
      10b L, ~500 LoC, MEDIUM risk)
- [ ] sota-critique.md §7.6.9 has the threat model statement
- [ ] go build ./... returns clean
- [ ] Atomic mirror: 1 SUMMARY pinned=true + 6 SECTION
      pinned=false, agent_id=alpha-11-chunk7

## 8. Next vibe-loop

After chunk 7 closes, the next vibe-loop is **Phase 1B:
PRE-1 C3 (Loadout for session_start)**. The spec lives in
`docs/specs/SPEC-alpha-11-pre1c3.md` (to be created when
PRE-1 C3 starts). The vibe-loop is the same shape: spec →
artifact → drift_judge → verdict → resolve.
