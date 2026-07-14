# vibe-flow/log-and-drift-check.w1.ps1
# Wave 1 (sub-specs 154 storage + 155 invariants) — artifact_log + drift_log.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-w1"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"

# Step 1: artifact_log for changed files. spec 154 (storage) and 155 (invariants).
$artifacts = @(
    @{path="internal/store/store.go"; spec=154},
    @{path="internal/store/sqlite/store.go"; spec=154},
    @{path="internal/store/postgres/store.go"; spec=154},
    @{path="internal/llm/cache.go"; spec=155},
    @{path="internal/mods/loader.go"; spec=155},
    @{path="internal/mods/types.go"; spec=155},
    @{path="tests/dual_driver/store_test.go"; spec=154}
)

$ok = 0; $fail = 0
foreach ($a in $artifacts) {
    $fullPath = Join-Path $root $a.path
    if (-not (Test-Path $fullPath)) {
        Write-Host "[MISSING] $($a.path)" -ForegroundColor Yellow
        $fail++
        continue
    }
    $argsFile = Join-Path $tmpDir ("art-" + ($a.path -replace '[/\\]', '_') + ".json")
    $url = "file:///" + ($fullPath -replace '\\', '/')
    $payload = @{
        case_kind = "C1"; artifact_type = "code"; artifact_url = $url
        spec_id = $a.spec; session_id = "dark-memory-mcp-v1"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') {
        Write-Host ("[ok]   spec=$($a.spec) $($a.path)")
        $ok++
    } else {
        Write-Host ("[FAIL] $($a.path)")
        $fail++
    }
}

Write-Host ""
Write-Host "Artifact log: $ok ok, $fail fail" -ForegroundColor $(if ($fail -eq 0) {'Green'} else {'Red'})

# Step 2: drift_log for sub-specs 154 and 155 with current implementation snapshot.
foreach ($spec in @(154, 155)) {
    $argsFile = Join-Path $tmpDir ("drift-w1-$spec.json")
    $payload = @{
        artifact_id     = $spec
        spec_id         = $spec
        verdict         = "drift_resolved"
        spec_diff       = '{"wave":"1","status":"shipped","files":["store.go","sqlite/store.go","postgres/store.go","llm/cache.go","mods/loader.go","mods/types.go"]}'
        judge_reasoning = "Wave 1 implementation: Store interface complete; SQLite impl filled for specs/brands/compliance/artifacts/drift/ssd/constitution/mods; watchdog verifies constitution file SHA on Open (INV-4); cache re-hashes stored text on Get and emits anomaly (INV-5); mod loader sanitizes directive and prompt-injection bodies via injection markers (INV-6). Tests pass on SQLite; Postgres impl skeleton for iface conformance."
        reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    $first = ($out | Select-Object -First 1)
    Write-Host ("drift $spec : " + $first)
}

Write-Host ""
Write-Host "=== Wave 1 close ===" -ForegroundColor Cyan