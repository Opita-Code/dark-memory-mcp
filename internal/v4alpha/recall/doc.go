// Package recall (v4alpha) — Phase 5 vibe-case-aware memory subsystem.
//
// This package is the read-side complement to the agent_memory writer
// (internal/v4alpha/agent_memory). It owns:
//
//   - Schema extensions to agent_memory: 20 additive columns for
//     embeddings, decay, code refs, graph residuals, and decision
//     subsystems (Phase 5 Chunk 1, per SPEC-alpha-11-phase5.md §4.1).
//   - Three new tables: agent_memory_entities (ProGraph 2-layer),
//     agent_memory_links (CABLE sparse directed), decision_transitions
//     (TokenMizer-style).
//   - The RecallFor() polymorphic dispatch and 7 RecallStrategy
//     implementations (C1..C7), one per vibe_case.
//   - The EmbedderAdapter interface (ImageBind, BGE-large, etc.).
//   - The decay function (per-kind decay_class, per-vibe-case
//     multipliers, refresh-on-access).
//   - The decision subsystem (supersession, rationale, transitions).
//
// Phase 5 closes the v4 memory subsystem gaps surfaced in sota-critique.md
// §7.6.7 and §8.3. Each design decision is grounded in the 6 research
// artifacts under docs/research/phase-5/ (~120 SOTA 2026 papers).
//
// Backwards compatibility:
//   - The agent_memory.Row struct is unchanged; the new columns are
//     surfaced via AnnotatedRow (this package).
//   - All migrations are idempotent (pragma_table_info check + ALTER).
//   - Existing rows get NULL for new columns; decay_class auto-derived
//     from kind on read.
package recall
