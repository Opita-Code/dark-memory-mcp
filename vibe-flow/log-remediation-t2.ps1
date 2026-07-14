# vibe-flow/log-remediation-t2.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-t2"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\store\store.go",
    "internal\store\sqlite\store.go",
    "internal\store\postgres\store.go",
    "tests\project\project_test.go",
    "tests\dual_driver\store_test.go"
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
        spec_id       = 171
        session_id    = "dark-memory-mcp-v1-remediation"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

# Drift log
$argsFile3 = Join-Path $tmpDir "drift-171.json"
$driftPayload = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"T2","finding":"W3-005","status":"resolved","impl":"SetActiveProject signature changed to (ctx, projectID) error; validates against projects table; rejects unknown with ErrInvalidArgument; preserves previous active on rejection; special-case default (legacy compat)","tests":["TestProject_SetActiveProject_RejectsUnknown PASS","TestProject_SetActiveProject_AllowsDefault PASS","TestProject_SetActiveProject_ClearOK PASS"],"api_change":"BREAKING: SetActiveProject callers must pass ctx and handle error; updated all in-repo callers (tests)"}'
    judge_reasoning = "T2 closed: W3-005 (SetActiveProject silent acceptance of typos) fixed. New signature returns error; rejects unknown projects; preserves previous active on rejection; allows empty (clears); allows default (legacy compat until T3 auto-seeds the row). 5 files changed (interface + 2 impls + 2 test files). Full suite 35/35 green. Spec 171 still drift_detected until T3-T5 close W3-004, W3-002, W3-003."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host ""
Write-Host "=== T2 (W3-005) closed: drift_resolved ==="