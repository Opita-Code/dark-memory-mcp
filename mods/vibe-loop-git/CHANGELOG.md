# Changelog

All notable changes to **vibe-loop-git** are documented in this file.
Format: [version] — date — summary. Local tags only (no push).

## [0.2.0] — 2026-10-08 — Loop 7 vibe-flow SHIPPED

### Added
- **Loop 7 (vibe-flow mode)**: turns the mod from descriptive (6 loops shipped) to ambient-directive (Loop 7 = C8 vibe_case). 23 workflow events E1-E23 fire across a normal session; 25 named gates G1-G25 intercept each event with 1-4 dark-memory tool calls; agent stays in flow via inline notes (NOT intrusive blocking).
- **`mods/vibe-loop-git/core/loop-7-vibe-flow.md`** (NEW, ~493 LoC): concept doc with philosophy, events, gates, UX walkthrough, compatibility, honest failure modes.
- **`mods/vibe-loop-git/core/gate-trigger-matrix.json`** (NEW, ~300 LoC): 23-row E→G→T matrix with every tool reference verified against the live dark-memory-mcp v4.0.0-alpha.30 registry.
- **`mods/vibe-loop-git/core/gate-protocol.md`** (NEW, ~260 LoC): the system-prompt block (~3,800 tokens) injected by the harness at session_start. Compatible with opencode / claude-code / claude-desktop / codex adapters.
- **`mods/vibe-loop-git/templates/spec-c8.json`** (NEW): the C8 vibe_case template (mirrors spec-c1..spec-c7 structure).
- **`mods/vibe-loop-git/tests/e2e-c8.sh`** (NEW): e2e smoke for the 23-event matrix + spec-c8 template.
- **`mods/vibe-loop-git/tests/gate-coverage-check.sh`** (NEW): verifies every tool name in the matrix actually exists in the dark-memory registry (catches stale tool references).
- **`vibe_case C8 — `vibe-flow`** added to `internal/vibecase/taxonomy.go` (canonical SoT). The package's `validVibeCases` map and JSON enums are derived from `vibecase.All()` at init — no more hardcoded lists in 4-5 places that fall out of sync.

### Changed
- **`mod.json`**: version 0.1.0 → 0.2.0, `targetDarkMemoryVersion`: `>=4.0.0-alpha.28` → `>=4.0.0-alpha.30`, `audit.loopsShipped`: 6 → 7, `audit.driftJudgesRun`: 7 → 9, `protocol.loops`: 6 → 7, `vibeCases.C8` added.
- **`internal/vibecase/taxonomy.go`**: `CaseVibeFlow Case = "C8"` added to constants, `all` slice, `descriptions` map.
- **`internal/v4alpha/vibe/spec.go`**: removed `CaseC1..CaseC8` local duplicates (was breaking the "single source of truth" rule per `taxonomy.go:32-36`). Now delegates to `vibecase.IsValid()`.
- **`internal/v4alpha/transport/mcp/mindset.go`**: `validVibeCases` map now derived from `vibecase.All()` at init. Error message includes live allow-list.
- **`internal/v4alpha/transport/mcp/vibe.go`**: jsonschema description updated, error message uses live `vibecase.JSONSchemaEnum()`.
- **`internal/v4alpha/transport/mcp/judge.go`**: 2 jsonschema enums (judge + consensus) updated to include C8.
- **`internal/v4alpha/transport/mcp/wire.go`**: `errInvalidVibeCase` uses live `vibecase.JSONSchemaEnum()`.
- **`internal/v4alpha/delegation/extract.go`**: `validVibeCaseForExtract` delegates to `vibecase.IsValid()`; error message uses live allow-list.
- **Tests**: `TestParse_RejectsUnknown`, `TestIsValid`, `TestCardinality` (vibecase) + `TestProperty_Spec_UnknownVibeCaseAlwaysRejected` (v4alpha/vibe) updated to reflect new contract (C8 valid, C9+ rejected).
- **README.md + SKILL.md**: Loop 7 section to be added in Commit 3.

### Drift_judge results
| Artifact | Eval ID | Verdict | Notes |
|---|---|---|---|
| `core/loop-7-vibe-flow.md` | 2015 | **ALIGNED** conf=1.0 | MiniMax-M3 chat-minimax-cn (post-T-201) |
| `core/gate-trigger-matrix.json` | 2016 | operator-override (nli=contradiction) | MiniMax-M3 false-positive pattern (rows 482/1985/2013); all tool references manually verified |
| `core/gate-protocol.md` | (audit via 2016) | operator-override | Same pattern as matrix |
| `templates/spec-c8.json` | pending | C8 template validates | manual check: matches spec-c1 schema family |

### Cross-version lockstep hash pin
UNCHANGED: `4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb` (per Phase 13 plan + mod invariants.sealed row 4).

### Local-only discipline
NO push, NO remote tags (per huérfano rule + mod invariants.sealed row 3). All commits local on branch `feat/v4-redesign`.

---

## [0.1.0] — 2026-10-06 — initial packaging

(prior content for retro doc)