# Changelog

All notable changes to dark-memory-mcp are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.2.1] — 2026-07-16

### Fixed
- **F36 — `vibe_spec` rejects payloads from MCP harnesses that stringify arrays.** The gemela tool `dark_research_spec_create` (separate server, same `vibe_specs` table) declares `tasks` as `type: "string"` and persists the value as opaque text. `dark_memory_vibe_spec` declared `tasks` as `type: "array"` and required `Tasks []VibeSpecTask`. Some MCP harnesses serialise array arguments as JSON-encoded strings under either schema; in that case `BindOrchestrator`'s `json.Unmarshal` fails with `*json.UnmarshalTypeError: cannot unmarshal string into Go struct field VibeSpecInput.tasks of type []orchestration.VibeSpecTask`, and the operator-visible error surfaced as a generic `ErrInvalidArgument` (without a precise field hint) — F35's structured-field reporting kicked in only on successful unmarshal-then-orchestrator failure paths, not on raw unmarshal failures. Symptom: every `dark_memory_vibe_spec` call from certain harnesses returned `{"code":"ErrInvalidArgument","message":"One or more arguments failed validation..."}` regardless of payload validity.
  - `internal/orchestration/vibe_spec.go` — `Tasks` is now `json.RawMessage`; new helper `parseTasksField` accepts both forms (leading-byte dispatch on `[` vs `"`) and returns a typed `[]VibeSpecTask`. The validation graph (unique ids, non-empty description, depends_on consistency, cycle detection) is unchanged.
  - `internal/tools/vibe.go` — schema for `tasks` widened from `type: "array"` to `anyOf: [{...array, items: vibeSpecTaskSchema}, {type: "string"}]`. Both forms now advertise at the wire layer so harnesses can pick whichever shape they prefer.
  - `tests/orchestration/orchestrator_test.go` — added `mustMarshalTasks` helper bridging the old typed-slice test bodies; added 2 new tests: `TestVibeSpec_AcceptsStringifiedTasks` (round-trip: raw string in, parsed array in storage) and `TestVibeSpec_StringifiedTasks_MalformedRejected` (precise error mentions "stringified" plus `ErrInvalidArgument`). The 8 pre-existing VibeSpec tests updated from `Tasks: []orchestration.VibeSpecTask{...}` to `Tasks: mustMarshalTasks(t, []orchestration.VibeSpecTask{...})`.

### Operator notes
- v1.2.1 is a **drop-in replacement** for v1.2.0. No migrations required. The 27-tool canonical surface is unchanged (no new tools, no deprecations). No DB schema change.
- Restart the running `dark-mem-mcp.exe` (PIDs currently running the pre-v1.2.1 binary are tagged in the process list) to pick up the new code. Until restart, `dark_memory_vibe_spec` calls that pass `tasks` as a raw array will continue to fail — pass them as a JSON-encoded string in the meantime.

---

## [1.2.0] — 2026-07-16

### Added
- **`dark_memory_project_create`** (F33 / Bug C) — new PROJECT namespace tool (1 tool) that closes the bootstrap loop for INV-7 multi-tenancy. Prior to v1.2.0, the only way to provision a non-`default` project was to insert into the `projects` table out of band; now operators can create tenants from inside the MCP surface, then immediately call `dark_memory_session_start` with the new `project_id`. Idempotent on `project_id` — re-creating an existing project returns the existing row with `idempotent_replay: true` and the original `created_at`.
  - `internal/tools/project.go` — new file (RegisterProject + ProjectCreateInput/Result + validation)
  - Kebab-case pattern enforced: `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`
  - Placed at canonical index 0 (before `session_start`) so tools/list discovery order matches the natural bootstrap flow
- **F35 structured error reporting** — `ToolError` extended with `Field`, `ExpectedType`, `ActualType`, and `SchemaHintURL`. `BindOrchestrator` now promotes `*json.UnmarshalTypeError` paths into discrete fields instead of hiding them in `Message`. Callers (LLM-driven or operator-driven) can render targeted fix-up hints without parsing free-form strings. All new fields are `omitempty` so the legacy shape is preserved for non-type-mismatch errors.
- **`vibeSpecTaskSchema`** (F33 / Bug B) — extracted shared strict schema for `vibe_spec` / `vibe_publish` task items. `additionalProperties: false` + explicit property list (`id`, `description`, `depends_on`, `owner`). Stops the silent-drop / type-coerce behavior that made calls fail with `cannot unmarshal string into ... depends_on of type []string` when callers passed `title`/`status`/`priority`.
- **`tests/tools/project_tool_test.go`** — 7 sub-tests covering happy path, idempotent replay, schema rejection (uppercase project_id, empty display_name, missing fields, unknown field) and the BindStore error envelope shape.

### Fixed
- **F33 / Bug A — `vibe_publish` JSON Schema is wrong.** Schema declared `spec`, `constitution`, `tasks`, `artifact_url`, `artifact_type`, `text` as flat top-level strings, but the Go struct `PublishVibeInput` (internal/orchestration/publish_vibe.go:42-72) nests them under `Spec PublishSpecInput` and `Artifact PublishArtifactInput`. Result: every harness call failed with `cannot unmarshal string into Go struct field PublishVibeInput.spec of type orchestration.PublishSpecInput`. Schema is now nested-correct with `additionalProperties: false` on both sub-objects.
- **F33 / Bug C — `dark_memory_project_create` was documented but not implemented.** `internal/project/types.go:9` advertised the tool, but no `tools/project.go` existed. Closed by adding the tool in this release.

### Changed
- **Canonical tool surface: 26 → 27** (F33). New PROJECT namespace (1 tool) inserted at index 0. `NewRegistry`, `CanonicalOrder`, and the boot-time sanity check in `RegisterAll` updated to expect 27.
- **Tool surface layout**:
  - `PROJECT (1) → create`
  - `SESSION (4) → start, resume, status, close`
  - `RESEARCH (3) → topic, recall, resume_thread`
  - `VIBE (4) → publish, spec, pipeline_status, resolve_drift`
  - `CONTEXT (3) → artifact_context, spec_context, session_context`
  - `JUDGE (3) → judge, consensus, judgment_history`
  - `POLICY (2) → active_policy, load_constitution`
  - `OBSERVABILITY (3) → memory_state, writes, anomalies`
  - `ADMIN (3) → admin_migrate, admin_schema_status, admin_vacuum`
  - `L6-VLP (1) → vlp_handle_event` (DMAP v1.1 spec 193)
  - Total: 1+4+3+4+3+3+2+3+3+1 = 27.
- Schema strictness: `vibe_publish`, `vibe_spec`, `project_create` now use `additionalProperties: false` on their nested objects so the harness rejects unknown fields at parse time instead of silently dropping or coercing them.

### Migration notes
- **No DB migration.** `dark_memory_project_create` writes to the existing `projects` table (migrations/v7) — no schema change. Existing operators running v1.1.x keep their data; the new tool just provides an in-band path to provision what previously required `INSERT INTO projects (...)`.
- **Backwards compatibility for `vibe_publish` callers.** The schema fix is breaking for callers that built payloads against the old (broken) flat-string shape — those payloads were never valid against the Go struct and would have failed unmarshal at runtime. New payloads use the nested shape. See `docs/PR-v1.2.0.md` (added in this release) for a before/after payload diff.
- **Backwards compatibility for `ToolError` consumers.** The four new fields (`Field`, `ExpectedType`, `ActualType`, `SchemaHintURL`) are `omitempty`, so existing JSON consumers that ignore unknown fields keep working. Consumers that strictly validate the response shape should add the new fields to their allow-list.

### Tests
- 7 new sub-tests in `tests/tools/project_tool_test.go` (success, idempotent replay, schema rejection, error envelope).
- All existing v1.1.0 tests still pass against the updated `RegisterAll` (27-tool surface); existing test fixtures that asserted on the 26-tool count have been updated.

[1.2.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.1.0...v1.2.0

---

## [1.1.0] — 2026-07-16

### Added
- **DMAP v1.1 (Dark Memory Agent Protocol)** — 6-layer architecture, 26 atomic specs
  - Layer 2 (loop coordinator) closed with 5 atomic specs:
    - 2.1 SessionState — pure state-machine logic
    - 2.2 VLPPackage — 4 typed primitives (Brief/Propose/Record/Complete)
    - 2.3 VLPPersistence — Store-backed state with audit
    - 2.4 VLPAuditor — transition-level audit
    - 2.5 VLPLoopUseCase — end-to-end loop driver
- `Store.SaveVLPStateWithTransition` — atomic combo: UPSERT + row-level audit + transition-level audit in one DB transaction
- `audit.WriteEvent.ProjectID` field — INV-7 multi-tenancy at the audit layer
- `audit.ListFilters.ProjectID` — read-side tenant filtering
- 2 new dual-driver sub-tests: `write_audit_project_isolation` (F33), `vlp_state_roundtrip` enhancements (F33 cross-project)

### Changed
- **INV-1 hardening (F32)**: 21 SQLite Save*/Update*/Delete*/Close*/Link* methods now wrapped in `BeginTx` + `Commit` + `defer Rollback`
  - New helpers: `runInTx`, `recordWriteLockedTx` (SQLite); `runInTx`, `recordWriteTx` (Postgres)
  - Data row + audit row now atomic; partial failure rolls back both
  - **Critical**: helpers read `s.activeProject` without re-locking (deadlock avoidance — caller already holds `s.mu`)
- `UseCase.HandleEvent` (spec 2.5) refactored to use `Store.SaveVLPStateWithTransition` instead of two separate calls
- Default version bumped from `0.1.0-dev` to `1.1.0-dev` in `cmd/dark-mem-cli` + `cmd/dark-mem-inspect`

### Database
- **Migration v9** (`vlp_state_table`) — vlp_state per-session state row
  - `UNIQUE INDEX (project_id, session_id)` — multi-tenancy at vlp layer (INV-7)
- **Migration v10** (`audit_project_index`) — composite index on `write_audit(project_id, session_id)` for ListWrites filtering efficiency
  - **No column changes** — `write_audit.project_id` was already added in v7 (`project_namespace`)
  - **Idempotent** — `CREATE INDEX IF NOT EXISTS`
  - **Backwards compatible**

### Tests
- `internal/vlp` — 12 tests including new `TestVLP_E2E_AtomicSaveEmitsTwoAuditRows`
- `tests/dual_driver` — 11 sub-tests including F33 isolation
- 10 packages, all PASS (374s full suite)

### Known v2 follow-ups (not blocking)
- Postgres `notImpl` stubs need same F32 wrapping when real impls land (~30 methods)
- No meta-test verifying "every Save* rolls back its audit row on data-write failure" — only VLP has this
- `usecaseTransitionNotes` and `auditor.marshalTransitionNotes` produce byte-identical JSON but are duplicated; trivial refactor when v2 reorganizes vlp package

---

## [1.0.0] — 2026-07-12

### Added
- **Initial release**: 25 MCP tools, dual-driver SQLite + Postgres, 7 operational invariants
- 8 trades: SESSION (4), RESEARCH (3), VIBE (4), CONTEXT (3), JUDGE (3), POLICY (2), OBSERVABILITY (3), ADMIN (3)
- Migrations v1-v8 establishing core schema (sessions, research, vibe_specs, vibe_artifacts, vibe_brands, vibe_compliance, vibe_drift_reports, sdd_evaluations, write_audit, constitutions, mods, projects, mod_loads)
- CLI tools: `dark-mem-mcp` (MCP server), `dark-mem-cli` (admin), `dark-mem-inspect` (read-only observability)
- 9 test suites: cli, conformance, context, dual_driver, e2e, economy, invariants, orchestration, project
- Constitution watchdog (INV-4) — `constitutions` table + `Store.VerifyConstitutionHash`
- Canary protection (INV-3) — `SafetyHolder` rejects payloads containing canary
- Mod sanitization (INV-6) — content loader refuses unsafe content
- Multi-tenancy foundation (INV-7) — projects table + project_id column on every tenant-scoped table
- Bridge documentation: 5/7 bridges complete (bridge.3 + bridge.5 deferred per spec 164)
- MCP Inspector conformance test (`tests/conformance/`)

### License
- MIT — see [LICENSE](LICENSE)

[1.1.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/Opita-Code/dark-memory-mcp/releases/tag/v1.0.0
