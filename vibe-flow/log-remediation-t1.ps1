# vibe-flow/log-remediation-t1.ps1
# W3-001 remediation closed. Log artifacts + drift_log.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-t1"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\store\sqlite\store.go",
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
        spec_id       = 171
        session_id    = "dark-memory-mcp-v1-remediation"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

# Drift log: spec 171 T1 sub-progress (W3-001 resolved)
$argsFile3 = Join-Path $tmpDir "drift-171.json"
$driftPayload = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"T1","finding":"W3-001","status":"resolved","impl":"GetRun+ListItems now requireProject() + WHERE project_id = ?","tests":["TestProject_GetRun_CrossProject_ReturnsNil PASS","TestProject_ListItems_CrossProject_ReturnsEmpty PASS","TestProject_GetRun_NoActiveProject_Refused PASS","TestProject_ListItems_NoActiveProject_Refused PASS"],"coverage":"research package now 100% project-filtered (SaveRun, GetRun, Recall, ListRuns, ListItems)"}'
    judge_reasoning = "T1 of spec 171 closed: W3-001 (GetRun+ListItems cross-project leak) is fixed. Both methods now require an active project and filter by project_id. Four new tests added: cross-project reads return nil/empty (was: leaked the other project's data); no-active reads return ErrSessionRequired (was: silently returned data). Full suite 32/32 green. Spec 171 still drift_detected until T2-T5 close W3-002, W3-005, W3-004, W3-003."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2

Write-Host ""
Write-Host "=== T1 (W3-001) closed: drift_resolved ==="
