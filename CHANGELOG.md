# Changelog

All notable changes to dark-memory-mcp are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
