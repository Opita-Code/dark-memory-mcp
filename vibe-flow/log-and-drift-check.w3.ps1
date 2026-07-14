# vibe-flow/log-and-drift-check.w3.ps1
# Wave 3 close: project namespace (INV-7) + migration v7 + tests.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-w3"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal/migrate/sqlite/ddl.go",                  # 157.add:v7 migration sqlite
    "internal/migrate/postgres/ddl.go",                # 157.add:v7 migration postgres (with RLS)
    "internal/project/types.go",                       # 157.add:Project + Membership + ProjectFilter
    "internal/store/store.go",                         # 157.add:project methods + WriteContext.ProjectID
    "internal/store/sqlite/store.go",                  # 157.add:project methods + project_id filter
    "internal/store/postgres/store.go",                # 157.add:project methods + RLS context setter
    "tests/dual_driver/store_test.go",                 # 157.add:tests use SetActiveProject("default")
    "tests/project/project_test.go"                    # 157.add:INV-7 isolation tests
)

$ok = 0; $fail = 0
foreach ($rel in $artifacts) {
    $full = Join-Path $root $rel
    if (-not (Test-Path $full)) { Write-Host "[MISSING] $rel" -ForegroundColor Yellow; $fail++; continue }
    $argsFile = Join-Path $tmpDir ("art-" + ($rel -replace '[/\\]', '_') + ".json")
    $url = "file:///" + ($full -replace '\\', '/')
    $payload = @{
        case_kind     = "C1"
        artifact_type = "code"
        artifact_url  = $url
        spec_id       = 157
        session_id    = "dark-memory-mcp-v1"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

# Drift log: Wave 3 part 1 (project namespace) shipped.
$argsFile = Join-Path $tmpDir "drift-157.json"
$payload = @{
    artifact_id     = 157
    spec_id         = 157
    verdict         = "drift_resolved"
    spec_diff       = '{"wave":"3a","status":"shipped","scope":"project namespace (INV-7)","files":["internal/migrate/sqlite/ddl.go (+v7)","internal/migrate/postgres/ddl.go (+v7 RLS)","internal/project/types.go (new)","internal/store/store.go (WriteContext.ProjectID)","internal/store/sqlite/store.go (project CRUD + project_id filter)","internal/store/postgres/store.go (project CRUD + RLS context)","tests/dual_driver/store_test.go (SetActiveProject default)","tests/project/project_test.go (5 tests: isolation, no-active error, migration backward-compat, CRUD, write tagging)"]}'
    judge_reasoning = "Wave 3 part 1 ships project namespace (INV-7). Migration v7 adds projects table + project_id column on every tenant-scoped table with default 'default' (preserves the 164 existing specs). SQLite impl uses application-layer project_id filter on every read. Postgres impl uses RLS via dark_mem.project_id session GUC + FORCE ROW LEVEL SECURITY. Five isolation tests pass: cross-project query returns empty, no-active-project reads refused, backward-compat migration, project CRUD round-trip, write tagging. Wave 3 part 2 (orchestrators) deferred to next iteration per senior architect pacing."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1 | Select-Object -First 2

Write-Host ""
Write-Host "=== Wave 3 closed (project namespace part) ==="