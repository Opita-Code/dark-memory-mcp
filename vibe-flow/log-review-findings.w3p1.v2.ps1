# vibe-flow/log-review-findings.w3p1.v2.ps1
# Loads JSON files instead of inline strings; PowerShell-friendly.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-review-w3p1"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\migrate\sqlite\ddl.go",
    "internal\migrate\postgres\ddl.go",
    "internal\project\types.go",
    "internal\store\store.go",
    "internal\store\sqlite\store.go",
    "internal\store\postgres\store.go",
    "tests\dual_driver\store_test.go",
    "tests\project\project_test.go"
)

$ok = 0; $fail = 0
foreach ($rel in $artifacts) {
    $full = Join-Path $root $rel
    $argsFile = Join-Path $tmpDir ("art-" + ($rel -replace '[/\\]', '_') + ".json")
    $url = "file:///" + (($full -replace '\\', '/').TrimStart('/'))
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

# Findings doc: log it as an artifact pointing to the JSON file
$findingsJson = Join-Path $root "vibe-flow\review-w3p1-findings.json"
$findingsUrl  = "file:///" + (($findingsJson -replace '\\', '/').TrimStart('/'))
$argsFile2 = Join-Path $tmpDir "findings-doc.json"
$payload2 = @{
    case_kind      = "C2"
    artifact_type  = "review-findings"
    artifact_url   = $findingsUrl
    spec_id        = 170
    session_id     = "dark-memory-mcp-v1-review"
    has_disclosure = $false
} | ConvertTo-Json -Depth 4 -Compress
$payload2 | Out-File -FilePath $argsFile2 -Encoding utf8 -NoNewline
$out2 = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile2 -TimeoutMs 15000 2>&1
if ($out2 -match '"artifact_id"') { Write-Host "[ok]   findings doc logged" } else { Write-Host "[FAIL] findings doc"; Write-Host $out2 }

# Drift log: spec 157 re-opened as drift_detected (Wave 3 part 1 was premature-closed)
$argsFile3 = Join-Path $tmpDir "drift-157.json"
$driftPayload = @{
    artifact_id     = 170
    spec_id         = 157
    verdict         = "drift_detected"
    spec_diff       = '{"review":"w3p1","findings":11,"high":5,"medium":4,"low":2,"verdict":"W3-001..W3-005 HIGH severity: invite to remediate before Wave 3 part 2 (orchestrators)."}'
    judge_reasoning = "Wave 3 part 1 shipped project namespace (INV-7) on top of migration v7, but the static review (28 tests green, but reads not filtered, Postgres RLS inactive, default project not seeded, SetActiveProject unvalidated) uncovered 5 HIGH-severity gaps that mean INV-7 is only partially enforced. Wave should be re-opened as drift_detected; spec 157 needs a remediation-required sub-spec (spec 171) before part 2 (orchestrators) starts. Drift closes once W3-001..W3-005 are fixed and a follow-up review confirms."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2

Write-Host ""
Write-Host "=== Wave 3 part 1 review complete: drift_detected ==="
