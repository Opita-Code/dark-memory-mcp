# vibe-flow/log-artifacts.spec141.ps1
# Logs the foundation library artifacts (spec 141) via dark_research_artifact_log.
# spec_id 141 = dark-memory-mcp-library-v1

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-artifact-args"
if (-not (Test-Path $tmpDir)) { New-Item -ItemType Directory -Path $tmpDir | Out-Null }

$root = "C:\Users\Nico\Documents\dark-memory-mcp"

# All files in this list are part of spec 141 (library + dual-driver Store)
$artifacts = @(
    "go.mod",
    "vibe-flow/constitution/dark-memory-mcp.constitution.toml",
    "vibe-flow/specs/dark-memory-mcp-v1.spec.json",
    "vibe-flow/brands/dark-memory-mcp.brand.json",
    "vibe-flow/dark-memory-mcp.tasks.ps1",
    "internal/research/types.go",
    "internal/vibeflow/types.go",
    "internal/ssd/types.go",
    "internal/constitution/types.go",
    "internal/mods/types.go",
    "internal/session/types.go",
    "internal/audit/types.go",
    "internal/safety/safety.go",
    "internal/store/store.go",
    "internal/store/factory.go",
    "internal/store/runtime/runtime.go",
    "internal/store/sqlite/export.go",
    "internal/store/sqlite/store.go",
    "internal/store/postgres/export.go",
    "internal/store/postgres/store.go",
    "internal/migrate/migrate.go",
    "internal/migrate/sqlite/ddl.go",
    "internal/migrate/postgres/ddl.go",
    "tests/dual_driver/store_test.go"
)

$ok = 0; $fail = 0
foreach ($rel in $artifacts) {
    $path = Join-Path $root $rel
    if (-not (Test-Path $path)) {
        Write-Host "MISSING: $rel" -ForegroundColor Yellow
        $fail++
        continue
    }
    $argsFile = Join-Path $tmpDir ("art-" + ($rel -replace '[/\\]', '_') + ".json")
    $url = "file:///" + ($path -replace '\\', '/')
    $payload = @{
        case_kind     = "C1"
        artifact_type = "code"
        artifact_url  = $url
        spec_id       = 141
        session_id    = "dark-memory-mcp-v1"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 20000 2>&1
    if ($out -match '"artifact_id"') {
        Write-Host "[ok]   $rel"
        $ok++
    } else {
        Write-Host "[FAIL] $rel" -ForegroundColor Red
        Write-Host $out
        $fail++
    }
}

Write-Host ""
Write-Host "Total: $ok ok, $fail fail (out of $($artifacts.Count))" -ForegroundColor $(if ($fail -eq 0) { "Green" } else { "Red" })
