# vibe-flow/log-review-findings.w3p1.ps1
# Fase 6: persist review findings table + drift_log decision.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-review-w3p1"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

# 8 files re-logged under spec_id 170 ("w3p1 review findings artifact batch")
$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal/migrate/sqlite/ddl.go",
    "internal/migrate/postgres/ddl.go",
    "internal/project/types.go",
    "internal/store/store.go",
    "internal/store/sqlite/store.go",
    "internal/store/postgres/store.go",
    "tests/dual_driver/store_test.go",
    "tests/project/project_test.go"
)

$ok = 0; $fail = 0
foreach ($rel in $artifacts) {
    $full = Join-Path $root $rel
    $argsFile = Join-Path $tmpDir ("art-" + ($rel -replace '[/\\]', '_') + ".json")
    $url = "file:///" + ($full -replace '\\', '/')
    $payload = @{
        case_kind     = "C1"
        artifact_type = "code"
        artifact_url  = $url
        spec_id       = 170
        session_id    = "dark-memory-mcp-v1-review"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

# Findings spec — register the review findings before drift
$findingsPayload = @{
    spec_id  = 170
    findings = @(
        # HIGH
        @{ id="W3-001"; severity="HIGH"; component="sqlite"; invariant="INV-7"; file="internal/store/sqlite/store.go"; lines="546,703"; issue="GetRun and ListItems do not filter by project_id. Cross-project read leak. ListRuns (sister method L575) DOES filter — internal inconsistency."; action="Add project_id WHERE clause + requireProject guard to GetRun and ListItems." },
        @{ id="W3-002"; severity="HIGH"; component="sqlite"; invariant="INV-7"; file="internal/store/sqlite/store.go"; lines="379,427,843,929,1014,1072,1145,1169,1239,1311,1472,1479,1593,1605,1748,1757,1878,1886,1983,2033"; issue="12+ read methods (GetSession, ListSessions, GetSpec, ListSpecs, GetBrandGuide, ListBrandGuides, GetComplianceRule, ListComplianceRules, GetArtifact, ListArtifacts, LatestDriftForArtifact, ListDriftReports, LatestSDDEvaluation, ListSDDEvaluations, GetConstitution, ListConstitutions, GetMod, ListMods, ListModLoads, Stats, ActiveConstitution) have NO project filter."; action="Apply requireProject + WHERE project_id=? at each read site. Or, document these as 'global' reads by-design and add a separate `IsGlobal` flag to WriteContext." },
        @{ id="W3-003"; severity="HIGH"; component="postgres"; invariant="INV-7"; file="internal/store/postgres/store.go"; lines="139 (def) — never called"; issue="withProjectTx defined but never called. RLS policies reference dark_mem.project_id GUC, but no method sets it. Every read on Postgres returns 0 rows (RLS evaluates NULL = NULL FALSE). Dual-driver 'feature parity' claim is FALSE for Postgres."; action="Either (a) wire withProjectTx around every read and write, OR (b) drop RLS and add explicit WHERE project_id = $1 like SQLite. Recommend (b) for v1.0; defer (a) to v2 with proper RLS testing harness." },
        @{ id="W3-004"; severity="HIGH"; component="sqlite"; invariant="backward-compat"; file="internal/store/sqlite/store.go:openSQLite (L59-95)"; issue="openSQLite does NOT auto-seed 'default' project row. Migration v7 ALTER TABLE on 11 columns with DEFAULT 'default' populates existing rows but the projects table itself is empty after v7. ListProjects() returns [] on first Open of a fresh v7-migrated DB; GetProject('default') returns nil. Reads via project_id string still work (data is queryable) but metadata is missing."; action="After migrate v7, run: INSERT OR IGNORE INTO projects (project_id, display_name, created_at) VALUES ('default', 'Default Project', ?) OR move this logic into the migration v7 UP script itself, running inside the migration transaction. Test must cover this case." },
        @{ id="W3-005"; severity="HIGH"; component="sqlite"; invariant="INV-7 strictness"; file="internal/store/sqlite/store.go:SetActiveProject (L115-118)"; issue="SetActiveProject validates nothing. Any string accepted. Subsequent writes silently go to a phantom project_id. Reads via string filter still work, but ListProjects() returns the phantom's absence — confusing UX and obscures typos."; action="On SetActiveProject(id), call GetProject(id) and return ErrInvalidArgument if not found. Or, document explicitly that SetActiveProject is a trust-based setter and add a separate ValidateProject(id) helper." },

        # MEDIUM
        @{ id="W3-006"; severity="MEDIUM"; component="sqlite"; invariant="INV-1"; file="internal/store/sqlite/store.go:CreateProject, ArchiveProject (L2060, L2172)"; issue="Project mutations (Create, Archive) do NOT emit write_audit. Per INV-1 spirit, mutations of the projects table should be audited. Currently the contract is 'every Save* method with WriteContext emits audit' — CreateProject/ArchiveProject don't take WriteContext, so technically exempt, but practically these are mutations of who-can-Read-and-Write to a tenant."; action="Add a WriteContext argument to CreateProject and ArchiveProject, and emit RecordWrite. Or, document explicitly in the interface that project management is meta-out-of-band and audited at a higher level (the orchestrator). The latter is acceptable if orchestrators log 'project_created' events elsewhere." },
        @{ id="W3-007"; severity="MEDIUM"; component="postgres"; invariant="dual-driver-parity"; file="internal/store/postgres/store.go:L851-958"; issue="~25 methods (SaveSpec, GetSpec, ListSpecs, SaveBrandGuide, GetBrandGuide, ListBrandGuides, SaveComplianceRule, GetComplianceRule, ListComplianceRules, SaveArtifact, GetArtifact, UpdateArtifact, DeleteArtifact, ListArtifacts, SetArtifactValidation, SaveDriftReport, LatestDriftForArtifact, ListDriftReports, SaveSDDEvaluation, LatestSDDEvaluation, ListSDDEvaluations, SaveConstitution, GetConstitution, ListConstitutions, SaveMod, GetMod, ListMods, RecordModLoad, ListModLoads) return notImpl(). The contract test skips them. Postgres is half-implemented; cannot be used in production for vibe-flow features."; action="Mark Postgres impl as 'research-only' until these are implemented. Or implement them with the same pattern as the implemented methods. Update dark-memory-mcp README to clarify which Store methods work on Postgres." },

        # LOW
        @{ id="W3-008"; severity="LOW"; component="project"; invariant="API-coherence"; file="internal/project/types.go:ProjectFilter (L47-65)"; issue="ProjectFilter type is defined but never used. The Store interface does NOT take it as a parameter; methods rely on Store.ActiveProject() at call time. ProjectFilter.DefaultFilter() and ProjectFilter.IsValid() are dead code today."; action="Either (a) remove ProjectFilter and use Store.ActiveProject() — the current approach; (b) refactor Store methods to take ProjectFilter as a parameter, more explicit but breaks signature. Decision: keep current design, remove ProjectFilter type and helpers in v2 cleanup." },
        @{ id="W3-009"; severity="LOW"; component="sqlite"; invariant="test-coverage"; file="tests/project/"; issue="No test exercises Postgres path (DARK_TEST_POSTGRES_DSN not set). Postgres is feature-flagged off in this environment, so defects in RLS, withProjectTx usage, etc. WILL NOT be caught by CI."; action="Add CI step that starts a Postgres container and exports DARK_TEST_POSTGRES_DSN. Without live Postgres coverage, the dual-driver claim is theoretical." },

        # Concerns on production dark.db live deployment
        @{ id="W3-010"; severity="MEDIUM"; component="deployment"; invariant="production-deploy"; file="production dark.db: C:\Users\Nico\AppData\Local\dark-agents\dark.db"; issue="production dark.db at v6 (dark-research-mcp), NOT v7. dark.db has 169 vibe_specs, 961 research_runs, 7399 research_items — NONE have project_id column yet. First Open of dark-memory-mcp on production will run v1-v7 migrations (since MAX(version)=3 from dark-research-mcp's history, dark-memory-mcp sees MAX=3 and applies v4,v5,v6,v7). v7 ALTER TABLE 11 tables adds project_id with DEFAULT 'default' — existing rows backfill to 'default'. dark-research-mcp keeps running unaware. Future writes from dark-research-mcp.exe will go to project_id='default' (via DEFAULT). NO backfill needed for legacy data, but no isolation either."; action="Run dark-memory-mcp.exe once against production to apply v7. Add a smoke test verifying migration v7 succeeds idempotently. Document for the operator." },
        @{ id="W3-011"; severity="LOW"; component="schemas"; invariant="schema-collision"; file="both servers' migrate.go"; issue="dark-research-mcp has its own migration versions 1-3 in `schema_migrations`. dark-memory-mcp has versions 1-7 in the same table. When both write to the same dark.db, MAX(version) is the only collision guard. Since dark-research-mcp's v3 stops at 3 and dark-memory-mcp adds v4+ on the same sequence, there's NO version collision today, but adding a dark-research-mcp v4 later would collide. Future risk."; action="Decide: keep both servers independent on shared DB (operational risk), OR shard via table prefix (e.g. research_runs -> dr_research_runs), OR dark-memory-mcp becomes the authoritative store and dark-research-mcp is deprecated (alignment with BRIDGE_AND_COEXISTENCE.md)." }
    )
} | ConvertTo-Json -Depth 8 -Compress
$argsFile = Join-Path $tmpDir "findings.json"
$findingsPayload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
# Note: spec_create would normally be called, but spec_id 170 is the same artifact_log target.
# We log the findings via dark_research_artifact_log with the JSON findings as a synthetic doc.
$findingsDoc = @{
    case_kind     = "C2"
    artifact_type = "review-findings"
    artifact_url  = "file:///C:\Users\Nico\Documents\dark-memory-mcp\vibe-flow\review-w3p1-findings.json"
    spec_id       = 170
    session_id    = "dark-memory-mcp-v1-review"
    has_disclosure = $false
} | ConvertTo-Json -Depth 4 -Compress
$argsFile2 = Join-Path $tmpDir "findings-doc.json"
$findingsDoc | Out-File -FilePath $argsFile2 -Encoding utf8 -NoNewline
# Write the actual findings json file so the URL points to a real file
$findingsJson = @{
    review_id    = "w3p1"
    reviewed_at  = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
    reviewer     = "dark-agents-v2 reviewer"
    spec_under_review = 157
    summary      = "Wave 3 part 1 review found 11 issues: 5 HIGH, 4 MEDIUM, 2 LOW. spec 157 closed too early; re-opening as drift_detected."
    findings      = @(
        "W3-001 HIGH INV-7 GetRun/ListItems cross-project leak",
        "W3-002 HIGH INV-7 12+ read methods without project filter",
        "W3-003 HIGH INV-7 Postgres withProjectTx never called (RLS broken)",
        "W3-004 HIGH backward-compat default project not auto-seeded",
        "W3-005 HIGH SetActiveProject no validation",
        "W3-006 MEDIUM INV-1 project mutations not audited",
        "W3-007 MEDIUM dual-driver parity: 25 Postgres stubs",
        "W3-008 LOW ProjectFilter type dead code",
        "W3-009 LOW no live Postgres test",
        "W3-010 MEDIUM production dark.db at v6, never had v7",
        "W3-011 LOW future schema_migrations collision risk"
    )
} | ConvertTo-Json -Depth 4
$findingsJsonPath = "C:\Users\Nico\Documents\dark-memory-mcp\vibe-flow\review-w3p1-findings.json"
$findingsJson | Out-File -FilePath $findingsJsonPath -Encoding utf8 -NoNewline
$out2 = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile2 -TimeoutMs 15000 2>&1
Write-Host "Findings doc logged"

# Drift log: spec 157 re-opened (drift_detected) and spec 170 (review findings) resolved.
$driftPayload = @{
    artifact_id     = 170
    spec_id         = 157
    verdict         = "drift_detected"
    spec_diff       = '{"review":"w3p1","findings":11,"high":5,"medium":4,"low":2,"verdict":"W3-001..W3-005 are HIGH severity and require remediation before Wave 3 part 2 (orchestrators) can proceed. Wave 3 part 1 was premature-closed."}'
    judge_reasoning = "Wave 3 part 1 shipped project namespace (INV-7) on top of migration v7, but the static review (28 tests green, but reads not filtered, Postgres RLS inactive, default project not seeded, SetActiveProject unvalidated) uncovered 5 HIGH-severity gaps that mean INV-7 is only partially enforced. The Wave should be re-opened as drift_detected; spec 157 mark needs a remediation-required sub-spec (spec 171) before part 2 (orchestrators) starts. Drift closes once W3-001..W3-005 are fixed and a follow-up review confirms."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$argsFile3 = Join-Path $tmpDir "drift-157.json"
$driftPayload | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2

Write-Host ""
Write-Host "=== Wave 3 part 1 review: drift_detected ==="