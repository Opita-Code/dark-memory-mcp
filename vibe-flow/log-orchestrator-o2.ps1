# vibe-flow/log-orchestrator-o2.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-o2"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\orchestration\session_close.go",
    "tests\orchestration\orchestrator_test.go"
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
        spec_id       = 173
        session_id    = "dark-memory-mcp-wave3-orchestrators"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

$argsFile3 = Join-Path $tmpDir "drift-173-o2.json"
$payload3 = @{
    artifact_id     = 173
    spec_id         = 173
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"O2","name":"SessionClose","status":"resolved","impl":["internal/orchestration/session_close.go: SessionClose orchestrator","validates session_id","calls CloseSession","summarises via ListWrites + ListRuns + ListItems","audit row emitted via Store.CloseSession"],"tests":["TestSessionClose_HappyPath PASS","TestSessionClose_MissingSessionID PASS","TestSessionClose_NotFound PASS","TestSessionClose_DoubleClose PASS","TestSessionClose_CrossProject PASS","TestSessionClose_SummaryCounts PASS"],"note":"RunsTotal/ItemsTotal are project-scoped not session-scoped (Store lacks session-scoped runs/items query; documented in spec 173 O2)"}'
    judge_reasoning = "O2 (SessionClose) closed: closes the session, returns summary of writes+runs+items. Closes-via-store.CloseSession which already requires project filter and audit. Double-close and cross-project both surface ErrNotFound as expected. 60/60 green suite."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload3 | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host "=== O2 (SessionClose) closed ==="