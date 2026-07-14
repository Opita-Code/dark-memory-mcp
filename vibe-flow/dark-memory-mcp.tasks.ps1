# vibe-flow/dark-memory-mcp.tasks.ps1
# Plan creation script — generates the 5 coordinated specs via dark-research tools.
# Each spec is a work-package. Tasks are embedded in the spec.tasks field.

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-vibe"
if (-not (Test-Path $tmpDir)) { New-Item -ItemType Directory -Path $tmpDir | Out-Null }

# Shared constitution (same for all specs)
$constitutionRef = "vibe-flow/constitution/dark-memory-mcp.constitution.toml"
$constitutionJSON = @"
{
  "ref": "$constitutionRef",
  "summary": "Dark Memory MCP v1.0.0 — sibling of dark-research-mcp. Coexistence model. 6 operational invariants: write-path audit, per-session scoping, canary in writes, constitution audit, cache integrity, mod content sanitization."
}
"@

# Helper
function Create-Spec {
    param(
        [string]$Id,
        [string]$Case,
        [string]$SessionId,
        [string]$Constitution,
        [string]$SpecJSON,
        [string]$TasksJSON
    )
    $argsFile = Join-Path $tmpDir "spec-$Id.json"
    $payload = @{
        case_kind    = $Case
        session_id   = $SessionId
        constitution = $Constitution
        spec         = $SpecJSON
        tasks        = $TasksJSON
    } | ConvertTo-Json -Depth 16 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    Write-Host "=== Creating spec $Id ===" -ForegroundColor Cyan
    & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_spec_create" -ArgsFile $argsFile -TimeoutMs 20000
    Write-Host ""
}

# ----------------------------------------------------------------------------
# SPEC 1 — Dark Memory MCP library + dual-driver Store (foundation)
# ----------------------------------------------------------------------------
$spec1 = @"
{
  "spec_id": "dark-memory-mcp-library-v1",
  "title": "Dark Memory MCP — library + dual-driver Store foundation",
  "what": "Module github.com/dark-agents/dark-memory-mcp. Library root has zero MCP dependency. internal/store.Store interface is the abstraction. Both internal/store/sqlite and internal/store/postgres implement it. internal/migrate runs schema v1–v6 per-driver. internal/safety provides canary + injectionMarkers. internal/session provides lifecycle. internal/audit provides write_audit. internal/observability surfaces state.",
  "why": "The Store interface is the seam that lets us swap SQLite and Postgres without rewriting any tool handler. Both drivers must pass the SAME test suite in tests/dual_driver/ — that is the contract.",
  "scope_in": ["go.mod (module + deps)", "internal/store/Store interface + factory", "internal/store/sqlite.Open + 30+ methods", "internal/store/postgres.Open + 30+ methods", "internal/migrate per-driver SQL v1–v6", "internal/session (lifecycle)", "internal/audit (write_audit + RecordWrite)", "internal/safety (canary, markers, hash)", "tests/dual_driver/ (interface contract test)"],
  "scope_out": ["MCP server binary", "CLI admin binary", "Tool handlers (spec 2)", "opencode.jsonc config"]
}
"@

$tasks1 = @"
[
  {"id": "1.1", "description": "go.mod: module github.com/dark-agents/dark-memory-mcp, deps jackc/pgx/v5, pelletier/go-toml/v2, modernc.org/sqlite. NO mcp-go.", "depends_on": []},
  {"id": "1.2", "description": "internal/store/store.go: Store interface with Open/Close/Ping/DriverName/Migrate/SchemaVersion + 30+ Save*/Get*/List*/Update*/Delete* methods for all layers (research, vibeflow, ssd, constitution, mods, audit, session).", "depends_on": ["1.1"]},
  {"id": "1.3", "description": "internal/store/factory.go: mem.Open(ctx, cfg) selects driver based on cfg.Driver (sqlite|postgres) and cfg.DSN.", "depends_on": ["1.2"]},
  {"id": "1.4", "description": "internal/store/sqlite/store.go: SQLite impl using modernc.org/sqlite. WAL mode + busy_timeout + foreign_keys pragma. All methods map to prepared statements.", "depends_on": ["1.2"]},
  {"id": "1.5", "description": "internal/store/postgres/store.go: Postgres impl using jackc/pgx/v5/pgxpool. Connection pool configurable. All methods map to parameterized queries.", "depends_on": ["1.2"]},
  {"id": "1.6", "description": "internal/migrate/sqlite/v1.go to v6.go: per-driver SQL. v1=initial, v2=constitutions+mods, v3=sdd_evaluations audit cols, v4=write_audit table + research_items columns, v5=sessions table, v6=constitutions watchdog columns.", "depends_on": ["1.4"]},
  {"id": "1.7", "description": "internal/migrate/postgres/v1.go to v6.go: Postgres-flavored DDL using SERIAL/BIGSERIAL instead of INTEGER PRIMARY KEY AUTOINCREMENT.", "depends_on": ["1.5"]},
  {"id": "1.8", "description": "internal/safety/canary.go: NewCanary() + SetCanary() + ValidatePayload(payload) returns canary_present bool. Hash payload with SHA-256 for INV-1.", "depends_on": ["1.1"]},
  {"id": "1.9", "description": "internal/safety/markers.go: injectionMarkers regex set + CheckPayload(payload) → list of marker hits. Used by INV-6.", "depends_on": ["1.1"]},
  {"id": "1.10", "description": "internal/session/session.go: Session primitive with session_id, constitution_id@version, active_mods, started_at, closed_at, audit_run_id.", "depends_on": ["1.2"]},
  {"id": "1.11", "description": "internal/audit/write_audit.go: WriteEvent struct + Store.RecordWrite(ctx, ev). Called from every Save* in the impl.", "depends_on": ["1.4", "1.5"]},
  {"id": "1.12", "description": "tests/dual_driver/store_test.go: TestStoreContract runs identical assertions against BOTH drivers (sqlite.Open + postgres.Open). At least 20 test cases covering every Save*/Get*/List* in the interface.", "depends_on": ["1.4", "1.5"]}
]
"@

Create-Spec -Id "library-v1" -Case "C1" -SessionId "dark-memory-mcp-v1" -Constitution $constitutionJSON -SpecJSON $spec1 -TasksJSON $tasks1

# ----------------------------------------------------------------------------
# SPEC 2 — Dark Memory MCP server binary + 52 tools in dark_memory_* namespace
# ----------------------------------------------------------------------------
$spec2 = @"
{
  "spec_id": "dark-memory-mcp-server-v1",
  "title": "Dark Memory MCP — MCP server binary + 52 tools in dark_memory_* namespace",
  "what": "cmd/dark-memory-mcp/main.go is the MCP server. It imports the library, instantiates the Store, registers 52 tools organized in 15 namespaces. Tools are split: standard tools (open to any MCP client), admin tools (driver-locked), redteam tools (gated by DARK_REDTEAM=armed — not in v1).",
  "why": "The MCP server is the user-facing surface of Dark Memory MCP. Tool naming convention dark_memory_* keeps it collision-free with dark-research-mcp's dark_research_*/dark_mem_*. The deprecation shim (spec 4) handles the legacy tools.",
  "scope_in": ["cmd/dark-memory-mcp/main.go (MCP server boot)", "internal/server/server.go (server setup + tool registry)", "internal/tools/session.go (3 tools)", "internal/tools/research.go (6 tools)", "internal/tools/vibeflow.go (4 workflow + 21 primitives)", "internal/tools/ssd.go (9 judges)", "internal/tools/constitution.go (4 tools)", "internal/tools/mods.go (5 tools)", "internal/tools/economy.go (3 tools)", "internal/tools/observability.go (4 tools)", "internal/tools/admin.go (8 tools)", "tests/e2e/ (server up + 1000 calls)", "tests/stress/ (concurrency test)"],
  "scope_out": ["library code (spec 1)", "CLI admin (spec 3)", "deprecation shim (spec 4)", "threat model patches (spec 5)"]
}
"@

$tasks2 = @"
[
  {"id": "2.1", "description": "cmd/dark-memory-mcp/main.go: parse flags (--driver, --dsn, --constitution, --armed). Construct Store via factory. Load constitution. Initialize safety (canary). Wire shared state into server.", "depends_on": ["1.12"]},
  {"id": "2.2", "description": "internal/server/server.go: register MCP server, attach tools from internal/tools.All(cfg), expose stdio transport.", "depends_on": ["2.1"]},
  {"id": "2.3", "description": "internal/tools/session.go: dark_memory_session_start, _close, _status. session_start returns session_id; persists Session row; sets process-local active session.", "depends_on": ["2.2"]},
  {"id": "2.4", "description": "internal/tools/research.go: dark_memory_research_topic (orchestrator), dark_memory_research_list, _get, _delete, _update, _export.", "depends_on": ["2.2"]},
  {"id": "2.5", "description": "internal/tools/recall.go: dark_memory_recall_research (substring + session_scope), _list_runs, _list_items, _status, _schema_status, _link (idempotent, source-labelled).", "depends_on": ["2.2"]},
  {"id": "2.6", "description": "internal/tools/vibeflow.go: dark_memory_vibe_spec (orchestrator: spec_create + tasks), _vibe_artifact (orchestrator: artifact_log + brand_match + compliance_check + drift_judge + drift_log), _vibe_pipeline_status (per-spec pipeline state), _vibe_drift_resolve (human gate).", "depends_on": ["2.2"]},
  {"id": "2.7", "description": "internal/tools/spec.go: dark_memory_spec_create, _get, _update, _delete, _list, _render (markdown).", "depends_on": ["2.2"]},
  {"id": "2.8", "description": "internal/tools/brand.go, compliance.go, artifact.go, drift.go: low-level CRUD wrapped as dark_memory_* tools (mirror existing dark_research_* primitives).", "depends_on": ["2.2"]},
  {"id": "2.9", "description": "internal/tools/ssd.go: dark_memory_ssd_brand_match, _compliance_check, _drift_judge, _grounding_check, _pii_detect, _prompt_injection_scan, _consensus, _list_evaluations, _list_refusal_patterns.", "depends_on": ["2.2"]},
  {"id": "2.10", "description": "internal/tools/constitution.go: dark_memory_constitution_register, _get, _list, _verify_hash (returns ConstitutionDriftError if SHA mismatches).", "depends_on": ["2.2"]},
  {"id": "2.11", "description": "internal/tools/mods.go: dark_memory_mod_register, _get, _list, _load, _unload. Each load runs content sanitization (INV-6).", "depends_on": ["2.2"]},
  {"id": "2.12", "description": "internal/tools/economy.go: dark_memory_economize (Atlan 5-bucket pipeline), _estimate_buckets, _cache_key.", "depends_on": ["2.2"]},
  {"id": "2.13", "description": "internal/tools/observability.go: dark_memory_memory_state, _memory_audit, _anomaly_events, _canary_status.", "depends_on": ["2.2"]},
  {"id": "2.14", "description": "internal/tools/admin.go: dark_memory_admin_open, _close, _migrate, _schema_status, _vacuum, _set_driver, _set_session_scope, _inspect (read-only debug).", "depends_on": ["2.2"]},
  {"id": "2.15", "description": "tests/e2e/server_test.go: start server, register all 52 tools, fire 1000 mixed calls, assert no deadlock, no panic, no row inconsistency.", "depends_on": ["2.14"]}
]
"@

Create-Spec -Id "server-v1" -Case "C1" -SessionId "dark-memory-mcp-v1" -Constitution $constitutionJSON -SpecJSON $spec2 -TasksJSON $tasks2

# ----------------------------------------------------------------------------
# SPEC 3 — dark-memory-cli (admin CLI)
# ----------------------------------------------------------------------------
$spec3 = @"
{
  "spec_id": "dark-memory-cli-v1",
  "title": "dark-memory-cli — admin CLI for migrations, vacuum, schema-status, set-driver",
  "what": "cmd/dark-memory-cli/main.go is a CLI binary. Subcommands: migrate (apply pending schema), vacuum (GC + retention), schema-status (version + applied migrations), set-driver (write env file). Each subcommand is a thin wrapper around the library.",
  "why": "Operators need a non-MCP way to manage the store. The CLI uses the same library the server uses, so behavior is identical. No duplicated logic.",
  "scope_in": ["cmd/dark-memory-cli/main.go (CLI dispatch)", "cmd/dark-memory-cli/migrate.go", "cmd/dark-memory-cli/vacuum.go", "cmd/dark-memory-cli/schema-status.go", "cmd/dark-memory-cli/set-driver.go", "tests/cli/"],
  "scope_out": ["library (spec 1)", "MCP server (spec 2)", "interactive UI"]
}
"@

$tasks3 = @"
[
  {"id": "3.1", "description": "cmd/dark-memory-cli/main.go: cobra-style or stdlib flag parsing. Dispatch to subcommand.", "depends_on": ["1.12"]},
  {"id": "3.2", "description": "migrate subcommand: read DARK_DB_DRIVER + DARK_DB_DSN, Open, call Store.Migrate, print summary.", "depends_on": ["3.1"]},
  {"id": "3.3", "description": "vacuum subcommand: read retention policy from flag/env, run Store.Vacuum, print deleted row counts.", "depends_on": ["3.1"]},
  {"id": "3.4", "description": "schema-status subcommand: call Store.SchemaVersion + Store.MigrationStatus, print pretty table.", "depends_on": ["3.1"]},
  {"id": "3.5", "description": "set-driver subcommand: write DARK_DB_DRIVER and DARK_DB_DSN to a .env file. Refuses if env file is read-only or if existing DARK_DB_DRIVER is sqlite AND user did not pass --force.", "depends_on": ["3.1"]}
]
"@

Create-Spec -Id "cli-v1" -Case "C1" -SessionId "dark-memory-mcp-v1" -Constitution $constitutionJSON -SpecJSON $spec3 -TasksJSON $tasks3

# ----------------------------------------------------------------------------
# SPEC 4 — dark-research-mcp deprecation shim
# ----------------------------------------------------------------------------
$spec4 = @"
{
  "spec_id": "dark-research-mcp-deprecation-v1",
  "title": "dark-research-mcp — deprecation shim for legacy dark_mem_* namespace",
  "what": "Modify dark-research-mcp/internal/tools/dark_mem.go to emit a deprecation warning on every response. Update dark-recall v2 plugin to prefer dark_memory_* when both servers are present. Document coexistence in dark-research-mcp README.",
  "why": "Users of dark-research-mcp's internal dark_mem_* tools should be informed (not forced) to migrate. The deprecation shim runs indefinitely without maintenance commitment — bugs there are not fixed in this work. Users who want fixes move to Dark Memory MCP.",
  "scope_in": ["dark-research-mcp/internal/tools/dark_mem.go (deprecation response field)", "dark-research-mcp/internal/tools/dark_research.go (dark_research_spec_create etc. — also deprecate? no, only dark_mem_*)", "dark-recall plugin v2.2: prefer dark_memory_* when available", "dark-research-mcp/README.md coexistence section"],
  "scope_out": ["removing legacy tools (no — they stay)", "porting dark_mem_* logic to dark_memory_* (no — reimplemented from spec 1+2)", "fixing bugs in legacy tools (no — out of scope by definition)"]
}
"@

$tasks4 = @"
[
  {"id": "4.1", "description": "dark-research-mcp/internal/tools/dark_mem.go: add deprecationResponse helper. Every existing handler returns the same shape PLUS {deprecated: true, successor: 'dark-memory-mcp', successor_tool: 'dark_memory_<X>', sunset: 'no-maintenance-mode'}.", "depends_on": []},
  {"id": "4.2", "description": "dark-research-mcp/internal/tools/dark_research.go: ONLY the spec/brand/compliance/artifact/drift tools that overlap with Dark Memory MCP get the deprecation shim. The router tools (dark_research, dark_research_cve, etc.) stay untouched — they own OSINT, not memory.", "depends_on": ["4.1"]},
  {"id": "4.3", "description": "dark-recall plugin v2.2: when MCP server 'dark-memory' is detected in opencode.jsonc, prefer dark_memory_* calls (status, recall_research, link_research). Falls back to dark_mem_* otherwise with a one-time toast.", "depends_on": ["2.15"]},
  {"id": "4.4", "description": "dark-research-mcp README: add 'Coexistence with Dark Memory MCP' section explaining the deprecation, the namespace partition, and the migration path (opt-in, no pressure).", "depends_on": ["4.1"]}
]
"@

Create-Spec -Id "deprecation-v1" -Case "C1" -SessionId "dark-memory-mcp-v1" -Constitution $constitutionJSON -SpecJSON $spec4 -TasksJSON $tasks4

# ----------------------------------------------------------------------------
# SPEC 5 — Threat model + 6 invariants as engineering code + dark-recall v2.2
# ----------------------------------------------------------------------------
$spec5 = @"
{
  "spec_id": "dark-memory-mcp-threat-model-v1",
  "title": "Engineering threat model — 6 invariants + dark-recall plugin v2.2 + cache integrity + mod sanitization",
  "what": "Codify the 6 operational invariants from the constitution as testable code: INV-1 write-path audit, INV-2 per-session scoping, INV-3 canary in writes, INV-4 constitution audit, INV-5 cache integrity, INV-6 mod content sanitization. Update dark-recall plugin v2.2 to scan prefill content for the canary before injection. Update internal/llm/cache.go to re-hash on Get. Update internal/mods/loader.go to sanitize content.",
  "why": "These invariants are engineering requirements, not bug reports. They were derived from analysis of how dark-recall v2 auto-prefills recalled research into every LLM turn, and how the existing schema lacks per-session distinction. Codifying them as tests makes the architecture self-enforcing.",
  "scope_in": ["internal/audit/write_audit.go (INV-1 enforcement)", "internal/store/recall.go with session_scope parameter (INV-2)", "internal/safety/canary.go with write-time check (INV-3)", "internal/constitution/watchdog.go SHA256 verify (INV-4)", "internal/llm/cache.go re-hash on Get (INV-5)", "internal/mods/loader.go content sanitize (INV-6)", "dark-recall plugin v2.2 canary scan prefill", "tests/invariants/ — one test file per invariant"],
  "scope_out": ["offensive primitives themselves (we do not generate exploit payloads)", "third-party targets (we attack only our own setup)", "CVE-class advisory publication (this is engineering, not disclosure)"]
}
"@

$tasks5 = @"
[
  {"id": "5.1", "description": "internal/audit/write_audit.go: RecordWrite is called from every Store.Save* method. Writes row to write_audit table. Verifies SHA-256 of payload before insert.", "depends_on": ["1.11"]},
  {"id": "5.2", "description": "internal/store/recall.go: Recall signature extended with session_id optional. Default cross-session. session_scope='self' filters by storeSessionID. session_scope='all' explicit cross-session.", "depends_on": ["1.4", "1.5"]},
  {"id": "5.3", "description": "internal/safety/canary.go: ValidateWritePayload(payload) runs before every Save. If payload contains active canary, return ErrCanaryInPayload. The hash goes into write_audit.", "depends_on": ["1.8"]},
  {"id": "5.4", "description": "internal/constitution/watchdog.go: On Store.Open, hash the constitution file at the active path. Compare to stored value in constitutions table. On mismatch, return ConstitutionDriftError.", "depends_on": ["1.4", "1.5"]},
  {"id": "5.5", "description": "internal/llm/cache.go: Cache.Get re-hashes stored text with SHA-256, compares to entry.SHA256. On mismatch, treat as miss + emit anomaly event.", "depends_on": ["1.1"]},
  {"id": "5.6", "description": "internal/mods/loader.go: ValidateModBody(body) runs the content through injectionMarkers regex set. Refuse load unless risk_class in {exploit-development, active-probing} AND user file is in DARK_MOD_WHITELIST.", "depends_on": ["1.9"]},
  {"id": "5.7", "description": "dark-recall plugin v2.2: in Layer 2 (Passive Prefill), before injecting the recall block, scan the content against safety.Canary(). If canary present, abort the prefill + reportHookFailure to telemetry.", "depends_on": ["5.3"]},
  {"id": "5.8", "description": "tests/invariants/inv1_write_audit_test.go: assert every Save produces a write_audit row.", "depends_on": ["5.1"]},
  {"id": "5.9", "description": "tests/invariants/inv2_session_scope_test.go: assert cross-session reads include all sessions; session_scope=self returns only own session.", "depends_on": ["5.2"]},
  {"id": "5.10", "description": "tests/invariants/inv3_canary_test.go: assert Save with canary in payload returns ErrCanaryInPayload.", "depends_on": ["5.3"]},
  {"id": "5.11", "description": "tests/invariants/inv4_watchdog_test.go: assert Store.Open with mismatched constitution SHA returns ConstitutionDriftError.", "depends_on": ["5.4"]},
  {"id": "5.12", "description": "tests/invariants/inv5_cache_test.go: assert Cache.Get with corrupted stored text emits anomaly + returns miss.", "depends_on": ["5.5"]},
  {"id": "5.13", "description": "tests/invariants/inv6_mod_sanitize_test.go: assert mod load with injection markers in body returns ErrModContentSanitized.", "depends_on": ["5.6"]}
]
"@

Create-Spec -Id "threat-model-v1" -Case "C1" -SessionId "dark-memory-mcp-v1" -Constitution $constitutionJSON -SpecJSON $spec5 -TasksJSON $tasks5

Write-Host ""
Write-Host "=== Vibe-flow planning complete ===" -ForegroundColor Green
Write-Host "5 specs created in dark.db under session_id='dark-memory-mcp-v1'."
Write-Host "Next step: query dark_research_spec_list to confirm spec_ids."
