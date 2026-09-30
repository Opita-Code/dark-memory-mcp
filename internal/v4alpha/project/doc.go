// Package project implements the BUG-10 10b "Namespace Primitive"
// (alpha.11 Phase 4 of the alpha.11+ plan). The package owns the
// `projects` table (the namespace registry, NOT a multi-tenant
// registry per docs/sota-critique.md §7.6.9 threat model) and the
// `ApplyProjectIDColumns` migration that adds the `project_id`
// column to the 5 v4 core tables.
//
// # Threat model (canonical)
//
// v4 assumes the harness session is the only concurrent consumer.
// Project IDs scope workstreams within one operator. For HARD
// isolation between concurrent users, use separate
// `coexistence_group`s or separate MCP instances. The `project_id`
// column is a soft namespace, not a security boundary.
//
// HARD isolation = `coexistence_group` (per-MCP `dark.db`).
// SOFT separation = `project_id` column (this package).
//
// # What this package does
//
//   - Defines the `Project` type (kebab-case id, display name,
//     optional description, optional default_agent_id).
//   - Provides `CreateSchema` for the `projects` table + seeds the
//     `'default'` project on first boot.
//   - Provides `Store.Create`/`List`/`Lookup`/`Archive` operations
//     for project lifecycle management.
//   - Provides `ApplyProjectIDColumns` for the schema migration
//     that adds `project_id` to 5 existing tables (agent_memory,
//     audit_log, sdd_evaluations, vibe_specs, vibe_artifacts).
//   - Rejects reserved ids (`default`, `dark`) in `Create`.
//   - Validates kebab-case format (lowercase alnum + hyphen, 3-64
//     chars) in `Create`.
//
// # What this package does NOT do
//
//   - It does NOT isolate data physically (all rows share one
//     `dark.db`).
//   - It does NOT enforce quotas per project.
//   - It does NOT apply RBAC (the operator can read any project's
//     rows by omitting the filter).
//   - It does NOT bill per project (self-hosted single-operator).
//
// # Companion packages
//
//   - `internal/v4alpha/audit` — INV-1 actor + INV-12 hash chain.
//     `Store.Create` emits one audit row per project create.
//   - `internal/v4alpha/session` — sessions already have
//     `project_id` (Phase 1 PRE-1 C3). This package provides the
//     existence check used by Phase 4 Chunk 4.3.
//   - `internal/v4alpha/vibe` — `vibe_specs` and `vibe_artifacts`
//     gain a `project_id` column via `ApplyProjectIDColumns`.
package project